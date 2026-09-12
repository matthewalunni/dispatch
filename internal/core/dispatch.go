package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/matthewalunni/dispatch/internal/herdrx"
	"github.com/matthewalunni/dispatch/internal/naming"
	"github.com/matthewalunni/dispatch/internal/prompt"
	"github.com/matthewalunni/dispatch/internal/roles"
	"github.com/matthewalunni/dispatch/internal/runtime"
	"github.com/matthewalunni/dispatch/internal/store"
)

// DispatchRequest is the input to the one operation that matters.
type DispatchRequest struct {
	// Dir is the directory dispatch was invoked from. Empty means cwd.
	Dir string

	Title       string
	Description string

	// Role names the specialism. Empty uses the resolved default role.
	Role string
	// Runtime overrides the role's runtime.
	Runtime string
	// Isolation overrides the role's isolation mode.
	Isolation string
	// Branch overrides the generated branch name (worktree isolation only).
	Branch string
	// BaseRef overrides what a new branch is created from.
	BaseRef string
	// ParentTaskID records lineage when a task dispatches child tasks.
	ParentTaskID string
	// AgentName overrides the generated herdr agent name.
	AgentName string
	// Focus brings the new herdr tab to the front.
	Focus bool
}

// DispatchResult is a created task plus anything non-fatal worth reporting.
type DispatchResult struct {
	Task     store.Task
	Prompt   string
	Warnings []string
}

// Dispatch is dispatch's core operation:
//
//	detect project -> load role -> resolve overrides -> determine isolation ->
//	prepare workspace -> create herdr tab -> launch runtime -> build prompt ->
//	send assignment -> persist task.
//
// It is deliberately free of any terminal-UI dependency: the CLI and the TUI
// call exactly this.
func (a *App) Dispatch(ctx context.Context, req DispatchRequest) (*DispatchResult, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return nil, &UserError{
			Summary: "A task needs a description.",
			Hints:   []string{`dispatch run --role engineer --task "Implement feature X"`},
		}
	}

	rc, err := a.Resolve(req.Dir)
	if err != nil {
		return nil, err
	}

	roleName := strings.TrimSpace(req.Role)
	if roleName == "" {
		roleName = rc.Config.DefaultRole
	}
	role, err := rc.Roles.Get(roleName)
	if err != nil {
		return nil, roleError(err, rc)
	}

	runtimeName := firstNonEmpty(req.Runtime, role.Runtime, rc.Config.DefaultRuntime)
	rt, err := runtime.Resolve(rc.Config, runtimeName)
	if err != nil {
		return nil, &UserError{
			Summary: fmt.Sprintf("Unknown runtime %q.", runtimeName),
			Reason:  fmt.Sprintf("Configured runtimes: %s.", strings.Join(rc.Config.RuntimeNames(), ", ")),
			Hints: []string{
				"dispatch doctor",
				fmt.Sprintf("add a `runtimes.%s` entry to %s", runtimeName, a.configFile),
			},
			Err: err,
		}
	}

	isolation := role.Isolation
	if req.Isolation != "" {
		parsed, err := roles.ParseIsolation(req.Isolation)
		if err != nil {
			return nil, &UserError{Summary: "Invalid isolation mode.", Reason: err.Error(), Err: err}
		}
		isolation = parsed
	}

	// Fail before creating anything if the runtime binary is missing: a stale
	// worktree left behind by a doomed launch is a worse outcome than a
	// refusal.
	if _, err := runtime.Installed(rt); err != nil {
		return nil, &UserError{
			Summary: fmt.Sprintf("Runtime %q is not available.", runtimeName),
			Reason:  err.Error(),
			Hints:   []string{"dispatch doctor"},
			Err:     err,
		}
	}
	if err := a.requireHerdr(ctx); err != nil {
		return nil, err
	}

	if req.ParentTaskID != "" {
		parent, err := a.store.Resolve(ctx, req.ParentTaskID)
		if err != nil {
			return nil, &UserError{
				Summary: fmt.Sprintf("Unknown parent task %q.", req.ParentTaskID),
				Hints:   []string{"dispatch ls --all"},
				Err:     err,
			}
		}
		req.ParentTaskID = parent.ID
	}

	now := a.now()
	taskID := naming.TaskID(now)

	// The slug is the reference a human types into `dispatch open`, so it has
	// to be unique among live tasks — and the branch, worktree and agent name
	// all have to agree with whichever variant we settle on.
	ws, err := a.prepareWorkspace(ctx, rc, isolation,
		naming.TruncateSlug(naming.Slug(title), 60), req.Branch, req.BaseRef, a.slugTakenFunc(ctx))
	if err != nil {
		return nil, err
	}
	slug := ws.Slug

	// From here on, failures must undo what they created.
	rollback := func() []string {
		var warnings []string
		if err := a.cleanupWorkspace(ctx, rc.Project.Root, ws); err != nil {
			warnings = append(warnings, fmt.Sprintf("could not remove worktree %s: %v", ws.Worktree, err))
		}
		return warnings
	}

	agentName, err := a.pickAgentName(ctx, req.AgentName, role, slug)
	if err != nil {
		rollback()
		return nil, err
	}

	workspaceID, err := a.targetWorkspace(ctx, rc)
	if err != nil {
		rollback()
		return nil, err
	}

	tab, err := a.herdr.CreateTab(ctx, herdrx.CreateTabRequest{
		WorkspaceID: workspaceID,
		CWD:         ws.Dir,
		Label:       tabLabel(role.Name, slug),
		Focus:       req.Focus || rc.Config.Herdr.FocusOnCreate,
		Env:         taskEnv(taskID, slug, rc.Project.Root, req.ParentTaskID),
	})
	if err != nil {
		rollback()
		return nil, herdrError("Unable to create a herdr tab for this task.", err)
	}

	agent, err := a.herdr.StartAgent(ctx, herdrx.StartAgentRequest{
		Name:      agentName,
		Kind:      rt.AgentKind(),
		PaneID:    tab.PaneID,
		TimeoutMS: rc.Config.Herdr.StartTimeoutMS,
		Args:      rt.Args(runtime.LaunchSpec{WorkingDir: ws.Dir, RoleArgs: role.RuntimeArgs}),
	})
	if err != nil {
		if closeErr := a.herdr.CloseTab(ctx, tab.TabID); closeErr != nil {
			_ = closeErr // best effort; the real error is the launch failure
		}
		rollback()
		return nil, startAgentError(err, rt.AgentKind(), rt.Binary())
	}

	initialPrompt := prompt.Build(prompt.Request{
		Role:                role,
		Project:             rc.Project,
		Title:               title,
		Description:         req.Description,
		Isolation:           isolation,
		Worktree:            ws.Worktree,
		Branch:              ws.Branch,
		BaseRef:             ws.BaseRef,
		ProjectInstructions: rc.Config.ProjectInstructions,
		ParentTaskID:        req.ParentTaskID,
	})

	task := store.Task{
		ID:               taskID,
		Title:            title,
		Description:      req.Description,
		Slug:             slug,
		ProjectRoot:      rc.Project.Root,
		ProjectName:      rc.Project.Name,
		Role:             role.Name,
		Runtime:          runtimeName,
		Isolation:        isolation,
		Status:           store.StatusRunning,
		Worktree:         ws.Worktree,
		Branch:           ws.Branch,
		BaseRef:          ws.BaseRef,
		HerdrAgent:       agentName,
		HerdrWorkspaceID: firstNonEmpty(agent.WorkspaceID, tab.WorkspaceID),
		HerdrTabID:       firstNonEmpty(agent.TabID, tab.TabID),
		HerdrPaneID:      firstNonEmpty(agent.PaneID, tab.PaneID),
		HerdrSession:     rc.Config.Herdr.Session,
		ParentTaskID:     req.ParentTaskID,
		CreatedAt:        now,
	}

	result := &DispatchResult{Prompt: initialPrompt}

	if err := a.store.Create(ctx, &task); err != nil {
		return nil, fmt.Errorf("persist task: %w", err)
	}

	if err := WriteArtifacts(a.dataDir, task, initialPrompt); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("could not write session artifacts: %v", err))
	}

	if rt.PromptOnLaunch() {
		if err := a.herdr.PromptAgent(ctx, agentName, initialPrompt); err != nil {
			// The session is live and the prompt is on disk, so this is a
			// warning rather than a failed dispatch: the human can recover by
			// opening the task and pasting initial-prompt.md.
			task.Error = fmt.Sprintf("initial prompt not delivered: %v", err)
			_ = a.store.Update(ctx, &task)
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"agent %s is running but the initial prompt was not delivered (%v); the prompt is saved at %s",
				agentName, err, PromptPath(a.dataDir, task.ID)))
		}
	}

	result.Task = task
	return result, nil
}

// slugTakenFunc reports which task slugs live tasks already hold. Finished
// tasks release their slug: resolution prefers the live task anyway, and a
// user should be able to reuse a name once the work is closed out.
func (a *App) slugTakenFunc(ctx context.Context) slugTaken {
	taken := map[string]bool{}
	if tasks, err := a.store.List(ctx, store.ActiveFilter()); err == nil {
		for _, t := range tasks {
			if t.Slug != "" {
				taken[t.Slug] = true
			}
		}
	}
	return func(slug string) bool { return taken[slug] }
}

// pickAgentName generates a herdr-valid, currently-unique agent name.
func (a *App) pickAgentName(ctx context.Context, override string, role roles.Role, slug string) (string, error) {
	if override != "" {
		if !naming.ValidAgentName(override) {
			return "", &UserError{
				Summary: fmt.Sprintf("Invalid agent name %q.", override),
				Reason:  "herdr agent names must match [a-z][a-z0-9_-]{0,31}.",
			}
		}
		return override, nil
	}

	live := map[string]bool{}
	if agents, err := a.herdr.ListAgents(ctx); err == nil {
		for _, agent := range agents {
			if agent.Name != "" {
				live[agent.Name] = true
			}
		}
	}
	// Also avoid names dispatch believes belong to its own running tasks, so a
	// restarted herdr server does not produce confusing duplicates.
	if tasks, err := a.store.List(ctx, store.ActiveFilter()); err == nil {
		for _, t := range tasks {
			if t.HerdrAgent != "" {
				live[t.HerdrAgent] = true
			}
		}
	}

	prefix := role.AgentPrefix
	return naming.AgentName(prefix, slug, func(name string) bool { return live[name] }), nil
}

// targetWorkspace picks the herdr workspace new tabs are created in.
func (a *App) targetWorkspace(ctx context.Context, rc *Context) (string, error) {
	if rc.Config.Herdr.Workspace != "" {
		return rc.Config.Herdr.Workspace, nil
	}
	workspaces, err := a.herdr.Workspaces(ctx)
	if err != nil {
		return "", herdrError("Unable to list herdr workspaces.", err)
	}
	for _, ws := range workspaces {
		if ws.Focused {
			return ws.WorkspaceID, nil
		}
	}
	if len(workspaces) > 0 {
		return workspaces[0].WorkspaceID, nil
	}
	// No workspaces at all: let herdr choose its own default.
	return "", nil
}

func (a *App) requireHerdr(ctx context.Context) error {
	health := a.herdr.Health(ctx)
	if !health.Installed {
		return &UserError{
			Summary: "herdr is not installed.",
			Reason:  "dispatch launches agents inside herdr panes, so it needs the herdr runtime.",
			Hints: []string{
				"curl -fsSL https://herdr.dev/install.sh | sh",
				"dispatch doctor",
			},
		}
	}
	if !health.ServerRunning {
		return &UserError{
			Summary: "No herdr server is running.",
			Reason:  fmt.Sprintf("dispatch found herdr %s but nothing is listening at %s.", health.ClientVersion, health.Socket),
			Hints: []string{
				"herdr            # start or attach the herdr session, then retry",
				"dispatch doctor",
			},
		}
	}
	if !health.Compatible {
		return &UserError{
			Summary: "The herdr client and server are running incompatible protocol versions.",
			Reason:  health.Detail,
			Hints:   []string{"herdr server stop && herdr", "dispatch doctor"},
		}
	}
	return nil
}

// taskEnv is the agent's own identity, exported into its pane.
//
// Without this an agent cannot refer to the task it *is*, which is what a
// delegating role needs in order to dispatch child work that keeps lineage:
//
//	dispatch run --parent "$DISPATCH_TASK_ID" --role engineer --task "..."
func taskEnv(taskID, slug, projectRoot, parentTaskID string) []string {
	env := []string{
		"DISPATCH_TASK_ID=" + taskID,
		"DISPATCH_TASK_REF=" + slug,
		"DISPATCH_PROJECT_ROOT=" + projectRoot,
	}
	if parentTaskID != "" {
		env = append(env, "DISPATCH_PARENT_TASK_ID="+parentTaskID)
	}
	return env
}

func tabLabel(role, slug string) string {
	label := role + ": " + slug
	if len(label) > 48 {
		label = label[:48]
	}
	return label
}

func roleError(err error, rc *Context) error {
	var unknown *roles.UnknownRoleError
	if errors.As(err, &unknown) {
		hints := []string{"dispatch roles"}
		if rc.Project.HasDispatchDir {
			hints = append(hints, "dispatch roles show <name>   # project overrides are applied")
		}
		hints = append(hints, fmt.Sprintf("dispatch role init %s", unknown.Name))
		return &UserError{
			Summary: fmt.Sprintf("Unknown role %q.", unknown.Name),
			Reason:  "Available roles: " + strings.Join(unknown.Available, ", ") + ".",
			Hints:   hints,
			Err:     err,
		}
	}
	return err
}

func herdrError(summary string, err error) error {
	if errors.Is(err, herdrx.ErrNotInstalled) {
		return &UserError{
			Summary: "herdr is not installed.",
			Hints:   []string{"curl -fsSL https://herdr.dev/install.sh | sh", "dispatch doctor"},
			Err:     err,
		}
	}
	if herdrx.IsCode(err, herdrx.CodeServerNotRunning) {
		return &UserError{
			Summary: "No herdr server is running.",
			Hints:   []string{"herdr    # start or attach the herdr session, then retry"},
			Err:     err,
		}
	}
	return &UserError{Summary: summary, Reason: err.Error(), Hints: []string{"dispatch doctor"}, Err: err}
}

func startAgentError(err error, kind, binary string) error {
	switch {
	case herdrx.IsCode(err, herdrx.CodeAgentNotReady):
		return &UserError{
			Summary: fmt.Sprintf("herdr started %s but it did not become ready for input.", kind),
			Reason:  err.Error(),
			Hints: []string{
				fmt.Sprintf("%s    # run it once by hand; it may be waiting on login or a first-run prompt", binary),
				"dispatch doctor",
			},
			Err: err,
		}
	default:
		return &UserError{
			Summary: fmt.Sprintf("Unable to launch the %s agent.", kind),
			Reason:  err.Error(),
			Hints: []string{
				fmt.Sprintf("%s --version", binary),
				"herdr agent          # check which agent kinds this herdr build supports",
			},
			Err: err,
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
