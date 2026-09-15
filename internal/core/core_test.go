package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matthewalunni/dispatch/internal/fakes"
	"github.com/matthewalunni/dispatch/internal/herdrx"
	"github.com/matthewalunni/dispatch/internal/roles"
	"github.com/matthewalunni/dispatch/internal/store"
)

// harness wires an App onto temp directories and in-memory git and herdr.
type harness struct {
	*App
	Git        *fakes.Git
	Herdr      *fakes.Herdr
	Repo       string
	DataDir    string
	ConfigPath string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	configDir := t.TempDir()
	dataDir := t.TempDir()
	repo := t.TempDir()

	// A runtime binary dispatch can find, so launches are not refused for a
	// reason unrelated to what each test is about.
	binDir := t.TempDir()
	stub := filepath.Join(binDir, "claude")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	git := fakes.NewGit(repo)
	herdr := fakes.NewHerdr()

	configFile := filepath.Join(configDir, "config.yaml")
	rolesDir := filepath.Join(configDir, "roles")
	if err := os.MkdirAll(rolesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := roles.Seed(rolesDir); err != nil {
		t.Fatal(err)
	}
	// default_isolation is deliberately cleared: most of these tests are
	// about what a *role* asks for, and the shipped default (worktree) would
	// otherwise answer for every one of them. The tests that are about the
	// default set it themselves.
	if err := os.WriteFile(configFile, []byte(
		"default_role: general\ndefault_isolation: \"\"\nworktrees_dir: "+
			filepath.Join(dataDir, "worktrees")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	app, err := New(Options{
		ConfigFile: configFile,
		RolesDir:   rolesDir,
		DBPath:     filepath.Join(dataDir, "dispatch.db"),
		DataDir:    dataDir,
		Git:        git,
		Herdr:      herdr,
		SkipSeed:   true,
		Now:        func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { app.Close() })

	return &harness{App: app, Git: git, Herdr: herdr, Repo: repo, DataDir: dataDir, ConfigPath: configFile}
}

// reopen rebuilds the App so an edited config file takes effect, keeping the
// same fakes and directories.
func reopen(t *testing.T, h *harness) *harness {
	t.Helper()
	if err := h.App.Close(); err != nil {
		t.Fatal(err)
	}
	app, err := New(Options{
		ConfigFile: h.ConfigPath,
		RolesDir:   h.RolesDir(),
		DBPath:     filepath.Join(h.DataDir, "dispatch.db"),
		DataDir:    h.DataDir,
		Git:        h.Git,
		Herdr:      h.Herdr,
		SkipSeed:   true,
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { app.Close() })
	return &harness{App: app, Git: h.Git, Herdr: h.Herdr, Repo: h.Repo, DataDir: h.DataDir, ConfigPath: h.ConfigPath}
}

func (h *harness) dispatch(t *testing.T, req DispatchRequest) *DispatchResult {
	t.Helper()
	if req.Dir == "" {
		req.Dir = h.Repo
	}
	result, err := h.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatalf("Dispatch(%q): %v", req.Title, err)
	}
	return result
}

func TestDispatchEngineerCreatesWorktreeAndLaunchesAgent(t *testing.T) {
	h := newHarness(t)

	result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Implement exercise substitution"})
	task := result.Task

	if task.Isolation != roles.IsolationWorktree {
		t.Errorf("Isolation = %q", task.Isolation)
	}
	if task.Branch != "dispatch/implement-exercise-substitution" {
		t.Errorf("Branch = %q", task.Branch)
	}
	if task.Worktree == "" {
		t.Fatal("no worktree recorded")
	}
	if strings.HasPrefix(task.Worktree, h.Repo) {
		t.Errorf("worktree %q was created inside the source repository", task.Worktree)
	}
	if len(h.Git.Added) != 1 {
		t.Fatalf("git worktree calls = %d, want 1", len(h.Git.Added))
	}
	added := h.Git.Added[0]
	if added.Repo != h.Repo || added.Branch != task.Branch || added.Base != "main" {
		t.Errorf("AddWorktree = %+v", added)
	}

	// The agent must be launched in the worktree, not the source checkout.
	if len(h.Herdr.Prompts) != 1 {
		t.Fatalf("prompts = %d, want 1", len(h.Herdr.Prompts))
	}
	if h.Herdr.Prompts[0].Target != task.HerdrAgent {
		t.Errorf("prompt target = %q, want the agent name", h.Herdr.Prompts[0].Target)
	}
	if !strings.Contains(h.Herdr.Prompts[0].Text, "Implement exercise substitution") {
		t.Error("the assignment was not in the submitted prompt")
	}

	// Herdr's identifiers must be captured, never predicted.
	if task.HerdrPaneID == "" || task.HerdrTabID == "" || task.HerdrWorkspaceID == "" {
		t.Errorf("herdr handles not captured: %+v", task)
	}
	if task.Status != store.StatusRunning {
		t.Errorf("Status = %q", task.Status)
	}
}

func TestDispatchNoIsolationDoesNotCreateAWorktree(t *testing.T) {
	h := newHarness(t)

	result := h.dispatch(t, DispatchRequest{Role: "reviewer", Title: "Review the current branch against main"})

	if result.Task.Worktree != "" || result.Task.Branch != "" {
		t.Errorf("a reviewer must not get a worktree: %+v", result.Task)
	}
	if len(h.Git.Added) != 0 {
		t.Errorf("git worktree created for a task that does not need one: %v", h.Git.Added)
	}
}

func TestDispatchWritesSessionArtifacts(t *testing.T) {
	h := newHarness(t)
	result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"})

	promptPath := PromptPath(h.DataDir, result.Task.ID)
	data, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("initial-prompt.md not written: %v", err)
	}
	if string(data) != result.Prompt {
		t.Error("saved prompt differs from the one that was sent")
	}
	if _, err := os.Stat(MetadataPath(h.DataDir, result.Task.ID)); err != nil {
		t.Errorf("metadata.json not written: %v", err)
	}
}

func TestDispatchPersistsAndIsRetrievable(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"})

	got, err := h.Store().Get(ctx, result.Task.ID)
	if err != nil {
		t.Fatalf("task not persisted: %v", err)
	}
	if got.Title != "Add the endpoint" {
		t.Errorf("Title = %q", got.Title)
	}
	// And it must be findable the way a human would name it.
	resolved, err := h.ResolveTask(ctx, "add-the-endpoint")
	if err != nil {
		t.Fatalf("Resolve by slug: %v", err)
	}
	if resolved.ID != result.Task.ID {
		t.Errorf("resolved %q, want %q", resolved.ID, result.Task.ID)
	}
}

func TestDispatchDuplicateTitlesStayIndividuallyAddressable(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	var ids []string
	slugs := map[string]bool{}
	branches := map[string]bool{}
	agents := map[string]bool{}

	for i := 0; i < 3; i++ {
		result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Implement exercise substitution"})
		ids = append(ids, result.Task.ID)
		slugs[result.Task.Slug] = true
		branches[result.Task.Branch] = true
		agents[result.Task.HerdrAgent] = true
	}

	if len(slugs) != 3 {
		t.Errorf("slugs = %v, want three distinct references", slugs)
	}
	if len(branches) != 3 {
		t.Errorf("branches = %v, want three distinct branches", branches)
	}
	if len(agents) != 3 {
		t.Errorf("agent names = %v, want three distinct agents", agents)
	}

	// Each slug must resolve back to exactly its own task.
	for i, id := range ids {
		task, err := h.Store().Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := h.ResolveTask(ctx, task.Slug)
		if err != nil {
			t.Fatalf("task %d slug %q does not resolve: %v", i, task.Slug, err)
		}
		if resolved.ID != id {
			t.Errorf("slug %q resolved to %q, want %q", task.Slug, resolved.ID, id)
		}
	}
}

func TestDispatchWorktreePathsNeverCollide(t *testing.T) {
	h := newHarness(t)
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Same title every time"})
		if seen[result.Task.Worktree] {
			t.Fatalf("two tasks share worktree %q", result.Task.Worktree)
		}
		seen[result.Task.Worktree] = true
	}
}

func TestDispatchReusesASlugOnceTheTaskIsClosed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	first := h.dispatch(t, DispatchRequest{Role: "reviewer", Title: "Review the diff"})
	if _, err := h.Stop(ctx, first.Task.ID, StopOptions{}); err != nil {
		t.Fatal(err)
	}

	second := h.dispatch(t, DispatchRequest{Role: "reviewer", Title: "Review the diff"})
	if second.Task.Slug != first.Task.Slug {
		t.Errorf("a finished task should release its name: got %q, want %q", second.Task.Slug, first.Task.Slug)
	}
	// And the live one wins when both exist.
	resolved, err := h.ResolveTask(ctx, "review-the-diff")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ID != second.Task.ID {
		t.Errorf("resolved the stopped task; want the live one")
	}
}

func TestDispatchExplicitBranchThatExistsIsRefusedHelpfully(t *testing.T) {
	h := newHarness(t)
	h.Git.Branches[h.Repo]["dispatch/taken"] = true

	_, err := h.Dispatch(context.Background(), DispatchRequest{
		Dir: h.Repo, Role: "engineer", Title: "Do the thing", Branch: "dispatch/taken",
	})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	var userErr *UserError
	if !errors.As(err, &userErr) {
		t.Fatalf("err = %v, want a UserError with a suggestion", err)
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should say the branch exists: %v", err)
	}
	if len(userErr.Hints) == 0 {
		t.Error("a refusal should suggest what to do instead")
	}
}

func TestDispatchWorktreeOutsideARepositoryIsRefused(t *testing.T) {
	h := newHarness(t)
	outside := t.TempDir()

	_, err := h.Dispatch(context.Background(), DispatchRequest{
		Dir: outside, Role: "engineer", Title: "Do the thing",
	})
	if err == nil {
		t.Fatal("expected a refusal outside a repository")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "--isolation none") {
		t.Errorf("error should suggest the way forward: %v", err)
	}
}

func TestDispatchNonIsolatedRoleWorksOutsideARepository(t *testing.T) {
	h := newHarness(t)
	outside := t.TempDir()

	result, err := h.Dispatch(context.Background(), DispatchRequest{
		Dir: outside, Role: "general", Title: "Research the options",
	})
	if err != nil {
		t.Fatalf("Dispatch outside a repo: %v", err)
	}
	if result.Task.ProjectRoot != outside {
		t.Errorf("ProjectRoot = %q, want %q", result.Task.ProjectRoot, outside)
	}
}

func TestDispatchRollsBackTheWorktreeWhenTheAgentFailsToStart(t *testing.T) {
	h := newHarness(t)
	h.Herdr.StartErr = &herdrx.Error{Code: herdrx.CodeAgentNotReady, Message: "agent did not become ready"}

	_, err := h.Dispatch(context.Background(), DispatchRequest{
		Dir: h.Repo, Role: "engineer", Title: "Doomed task",
	})
	if err == nil {
		t.Fatal("expected a failure")
	}

	// Nothing may be left behind: no worktree, no tab, no task row.
	if len(h.Git.Removed) != 1 {
		t.Errorf("worktree not cleaned up: removed = %v", h.Git.Removed)
	}
	if len(h.Herdr.ClosedSessions) != 1 {
		t.Errorf("herdr session not cleaned up: closed = %v", h.Herdr.ClosedSessions)
	}
	if h.Herdr.OpenWorkspaceCount() != 0 {
		t.Errorf("a failed dispatch left %d workspaces open", h.Herdr.OpenWorkspaceCount())
	}
	tasks, err := h.Store().All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Errorf("a failed dispatch left %d task rows behind", len(tasks))
	}
}

func TestDispatchSurvivesAFailedPromptDelivery(t *testing.T) {
	h := newHarness(t)
	h.Herdr.PromptErr = &herdrx.Error{Code: herdrx.CodeAgentBlocked, Message: "agent is blocked"}

	result, err := h.Dispatch(context.Background(), DispatchRequest{
		Dir: h.Repo, Role: "engineer", Title: "Add the endpoint",
	})
	if err != nil {
		t.Fatalf("a live session with an undelivered prompt is not a failed dispatch: %v", err)
	}
	if len(result.Warnings) == 0 {
		t.Error("the caller should be warned that the prompt was not delivered")
	}
	if !strings.Contains(strings.Join(result.Warnings, " "), "initial-prompt.md") {
		t.Errorf("the warning should point at the saved prompt: %v", result.Warnings)
	}
	// The task must still exist so the human can open it and recover.
	if _, err := h.Store().Get(context.Background(), result.Task.ID); err != nil {
		t.Errorf("task not persisted: %v", err)
	}
}

func TestDispatchRefusesWhenHerdrIsNotRunning(t *testing.T) {
	h := newHarness(t)
	h.Herdr.HealthState = herdrx.Health{Installed: true, ServerRunning: false, ClientVersion: "0.9.0"}

	_, err := h.Dispatch(context.Background(), DispatchRequest{
		Dir: h.Repo, Role: "engineer", Title: "Add the endpoint",
	})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "No herdr server is running") {
		t.Errorf("err = %v", err)
	}
	// And it must refuse before creating anything.
	if len(h.Git.Added) != 0 {
		t.Errorf("a worktree was created despite herdr being down: %v", h.Git.Added)
	}
}

func TestDispatchUnknownRoleListsAvailableRoles(t *testing.T) {
	h := newHarness(t)
	_, err := h.Dispatch(context.Background(), DispatchRequest{
		Dir: h.Repo, Role: "wizard", Title: "Cast a spell",
	})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"wizard", "engineer", "designer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
}

func TestDispatchEmptyTitleIsRefused(t *testing.T) {
	h := newHarness(t)
	if _, err := h.Dispatch(context.Background(), DispatchRequest{Dir: h.Repo, Title: "   "}); err == nil {
		t.Fatal("expected a refusal for an empty task")
	}
}

func TestDispatchIsolationOverrideBeatsTheRole(t *testing.T) {
	h := newHarness(t)

	// A reviewer normally gets no worktree; asking for one must work.
	result := h.dispatch(t, DispatchRequest{
		Role: "reviewer", Title: "Review in isolation", Isolation: "worktree",
	})
	if result.Task.Isolation != roles.IsolationWorktree || result.Task.Worktree == "" {
		t.Errorf("isolation override ignored: %+v", result.Task)
	}

	// And the reverse: an engineer forced to work in place.
	result = h.dispatch(t, DispatchRequest{
		Role: "engineer", Title: "Quick fix in place", Isolation: "none",
	})
	if result.Task.Isolation != roles.IsolationNone || result.Task.Worktree != "" {
		t.Errorf("isolation override ignored: %+v", result.Task)
	}
}

func TestDispatchInvalidIsolationIsRefused(t *testing.T) {
	h := newHarness(t)
	_, err := h.Dispatch(context.Background(), DispatchRequest{
		Dir: h.Repo, Role: "engineer", Title: "X", Isolation: "container",
	})
	if err == nil || !strings.Contains(err.Error(), "container") {
		t.Errorf("err = %v, want a refusal naming the bad mode", err)
	}
}

func TestDispatchRecordsParentLineage(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	parent := h.dispatch(t, DispatchRequest{Role: "designer", Title: "Plan the onboarding flow"})
	child := h.dispatch(t, DispatchRequest{
		Role: "engineer", Title: "Build the onboarding screens", ParentTaskID: parent.Task.ID,
	})

	if child.Task.ParentTaskID != parent.Task.ID {
		t.Errorf("ParentTaskID = %q, want %q", child.Task.ParentTaskID, parent.Task.ID)
	}
	children, err := h.Store().Children(ctx, parent.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 || children[0].ID != child.Task.ID {
		t.Errorf("children = %+v", children)
	}
	// A parent may also be named the way a human would.
	grandchild := h.dispatch(t, DispatchRequest{
		Role: "reviewer", Title: "Review the screens", ParentTaskID: "plan-the-onboarding-flow",
	})
	if grandchild.Task.ParentTaskID != parent.Task.ID {
		t.Errorf("a parent reference should resolve like any other task reference")
	}
}

func TestDispatchUnknownParentIsRefused(t *testing.T) {
	h := newHarness(t)
	_, err := h.Dispatch(context.Background(), DispatchRequest{
		Dir: h.Repo, Role: "engineer", Title: "X", ParentTaskID: "no-such-task",
	})
	if err == nil || !strings.Contains(err.Error(), "parent") {
		t.Errorf("err = %v, want a refusal naming the unknown parent", err)
	}
}

func TestDispatchProjectOverridesApply(t *testing.T) {
	h := newHarness(t)

	dispatchDir := filepath.Join(h.Repo, ".dispatch")
	if err := os.MkdirAll(filepath.Join(dispatchDir, "roles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dispatchDir, "config.yaml"), []byte(
		"branch_prefix: agent/\nproject_instructions: |\n  Prefer the design system primitives.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dispatchDir, "roles", "ios-engineer.yaml"), []byte(
		"name: ios-engineer\ndescription: iOS specialist\nruntime: claude\nisolation: worktree\ninstructions: |\n  Match existing SwiftUI conventions.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := h.dispatch(t, DispatchRequest{Role: "ios-engineer", Title: "Add the workout editor"})

	if result.Task.Role != "ios-engineer" {
		t.Errorf("Role = %q, want the project-local role", result.Task.Role)
	}
	if !strings.HasPrefix(result.Task.Branch, "agent/") {
		t.Errorf("Branch = %q, want the project branch prefix", result.Task.Branch)
	}
	if !strings.Contains(result.Prompt, "Match existing SwiftUI conventions.") {
		t.Error("project role instructions missing from the prompt")
	}
	if !strings.Contains(result.Prompt, "Prefer the design system primitives.") {
		t.Error("project instructions missing from the prompt")
	}
}

func TestDispatchPassesRoleRuntimeArgsThrough(t *testing.T) {
	h := newHarness(t)
	rolePath := filepath.Join(h.RolesDir(), "picky.yaml")
	if err := os.WriteFile(rolePath, []byte(
		"name: picky\ndescription: Picky\nruntime: claude\nisolation: none\nruntime_args: [\"--model\", \"opus\"]\ninstructions: |\n  Be picky.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := h.dispatch(t, DispatchRequest{Role: "picky", Title: "Be careful"})
	args := h.Herdr.StartedArgs[result.Task.HerdrAgent]
	if len(args) != 2 || args[0] != "--model" || args[1] != "opus" {
		t.Errorf("runtime args = %v, want the role's arguments passed through", args)
	}
}

func TestOpenFocusesTheLiveSession(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"})

	opened, err := h.Open(ctx, "add-the-endpoint")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !opened.Focused {
		t.Error("Open did not focus the session")
	}
	if len(h.Herdr.Focused) != 1 || h.Herdr.Focused[0] != result.Task.HerdrAgent {
		t.Errorf("focused = %v, want the task's agent", h.Herdr.Focused)
	}
	if len(opened.AttachCommand) == 0 {
		t.Error("Open should hand back a way to attach")
	}
}

func TestOpenExplainsALostSession(t *testing.T) {
	h := newHarness(t)
	result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"})
	h.Herdr.Kill(result.Task.HerdrAgent) // the user closed the pane in herdr

	_, err := h.Open(context.Background(), "add-the-endpoint")
	if err == nil {
		t.Fatal("expected an explanation")
	}
	if !strings.Contains(err.Error(), "no longer has a live herdr session") {
		t.Errorf("err = %v", err)
	}
}

func TestOpenUnknownTaskSuggestsListing(t *testing.T) {
	h := newHarness(t)
	_, err := h.Open(context.Background(), "nothing-like-this")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "dispatch ls") {
		t.Errorf("error should suggest how to find tasks: %v", err)
	}
}

func TestStopEndsTheAgentAndKeepsTheWorktree(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"})

	stopped, err := h.Stop(ctx, "add-the-endpoint", StopOptions{})
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !stopped.AgentStopped {
		t.Error("agent not stopped")
	}
	if stopped.WorktreeRemoved {
		t.Error("the worktree must be kept unless removal was asked for")
	}
	if len(h.Git.Removed) != 0 {
		t.Errorf("worktree removed without being asked: %v", h.Git.Removed)
	}

	task, err := h.Store().Get(ctx, result.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != store.StatusStopped {
		t.Errorf("Status = %q", task.Status)
	}
	if task.CompletedAt == nil {
		t.Error("CompletedAt not stamped")
	}
	if _, err := os.Stat(ResultPath(h.DataDir, task.ID)); err != nil {
		t.Errorf("result.json not written: %v", err)
	}
}

func TestStopCanRemoveTheWorktree(t *testing.T) {
	h := newHarness(t)
	result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"})

	stopped, err := h.Stop(context.Background(), result.Task.ID, StopOptions{RemoveWorktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !stopped.WorktreeRemoved || len(h.Git.Removed) != 1 {
		t.Errorf("worktree not removed: %+v %v", stopped, h.Git.Removed)
	}
}

func TestStopReportsAWorktreeItCouldNotRemove(t *testing.T) {
	h := newHarness(t)
	result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"})
	h.Git.RemoveWorktreeErr = errors.New("contains modified or untracked files")

	stopped, err := h.Stop(context.Background(), result.Task.ID, StopOptions{RemoveWorktree: true})
	if err != nil {
		t.Fatalf("a failed worktree removal should not fail the stop: %v", err)
	}
	if stopped.WorktreeRemoved {
		t.Error("WorktreeRemoved should be false")
	}
	if len(stopped.Warnings) == 0 || !strings.Contains(strings.Join(stopped.Warnings, " "), "--force") {
		t.Errorf("warnings should explain the recovery: %v", stopped.Warnings)
	}
	// The task still closes out; the leftover worktree is reported, not hidden.
	task, _ := h.Store().Get(context.Background(), result.Task.ID)
	if task.Status != store.StatusStopped {
		t.Errorf("Status = %q", task.Status)
	}
}

func TestStopIsIdempotentAfterTheSessionIsGone(t *testing.T) {
	h := newHarness(t)
	result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"})
	h.Herdr.Kill(result.Task.HerdrAgent)

	stopped, err := h.Stop(context.Background(), result.Task.ID, StopOptions{})
	if err != nil {
		t.Fatalf("Stop on a dead session: %v", err)
	}
	if stopped.AgentStopped {
		t.Error("AgentStopped should be false when there was nothing to stop")
	}
}

func TestHydrateResolvesLiveStatus(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"})

	cases := []struct {
		agent herdrx.Status
		want  EffectiveStatus
	}{
		{herdrx.StatusWorking, EffectiveWorking},
		{herdrx.StatusBlocked, EffectiveWaiting},
		{herdrx.StatusIdle, EffectiveIdle},
		{herdrx.StatusDone, EffectiveDone},
		{herdrx.StatusUnknown, EffectiveUnknown},
	}
	for _, tc := range cases {
		h.Herdr.SetStatus(result.Task.HerdrAgent, tc.agent)
		task, _ := h.Store().Get(ctx, result.Task.ID)
		view := h.HydrateOne(ctx, task)
		if view.Effective != tc.want {
			t.Errorf("herdr %q -> %q, want %q", tc.agent, view.Effective, tc.want)
		}
		if !view.AgentAlive {
			t.Errorf("AgentAlive should be true for %q", tc.agent)
		}
	}
}

func TestHydrateReportsADetachedSession(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"})
	h.Herdr.Kill(result.Task.HerdrAgent)

	task, _ := h.Store().Get(ctx, result.Task.ID)
	view := h.HydrateOne(ctx, task)
	if view.Effective != EffectiveDetached {
		t.Errorf("Effective = %q, want detached", view.Effective)
	}
	if view.AgentAlive {
		t.Error("AgentAlive should be false")
	}
}

func TestHydrateKeepsDispatchStatusForFinishedTasks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"})
	if _, err := h.Stop(ctx, result.Task.ID, StopOptions{}); err != nil {
		t.Fatal(err)
	}

	task, _ := h.Store().Get(ctx, result.Task.ID)
	view := h.HydrateOne(ctx, task)
	if view.Effective != EffectiveStopped {
		t.Errorf("Effective = %q, want stopped", view.Effective)
	}
}

func TestNeedsAttentionSurfacesBlockedAgents(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	busy := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Busy task"})
	blocked := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Blocked task"})
	h.Herdr.SetStatus(busy.Task.HerdrAgent, herdrx.StatusWorking)
	h.Herdr.SetStatus(blocked.Task.HerdrAgent, herdrx.StatusBlocked)

	waiting, err := h.NeedsAttention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(waiting) != 1 || waiting[0].ID != blocked.Task.ID {
		t.Errorf("NeedsAttention = %+v, want only the blocked task", waiting)
	}
}

func TestCompleteMarksDoneWithoutTouchingTheSession(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"})

	task, err := h.Complete(ctx, result.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != store.StatusCompleted {
		t.Errorf("Status = %q", task.Status)
	}
	if h.Herdr.AgentCount() != 1 {
		t.Error("Complete must not stop the agent")
	}
}

func TestResolveLayersProjectConfigOverGlobal(t *testing.T) {
	h := newHarness(t)
	dispatchDir := filepath.Join(h.Repo, ".dispatch")
	if err := os.MkdirAll(dispatchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dispatchDir, "config.yaml"), []byte("default_role: reviewer\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rc, err := h.Resolve(h.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if rc.Config.DefaultRole != "reviewer" {
		t.Errorf("DefaultRole = %q, want the project override", rc.Config.DefaultRole)
	}
	if !rc.Project.HasDispatchDir {
		t.Error("HasDispatchDir not detected")
	}

	// The app's own global config must be untouched for the next project.
	if h.App.Config().DefaultRole != "general" {
		t.Errorf("global config mutated: %q", h.App.Config().DefaultRole)
	}
}

func TestDoctorReportsAMissingHerdrServer(t *testing.T) {
	h := newHarness(t)
	h.Herdr.HealthState = herdrx.Health{Installed: true, ServerRunning: false, Detail: "no server"}

	diag, err := h.Doctor(context.Background(), h.Repo, "test")
	if err != nil {
		t.Fatal(err)
	}
	if diag.Healthy {
		t.Error("doctor should not report a healthy environment without a herdr server")
	}

	var found bool
	for _, group := range diag.Groups {
		if group.Name != "Herdr" {
			continue
		}
		for _, check := range group.Checks {
			if check.Label == "server reachable" {
				found = true
				if check.State != CheckFail {
					t.Errorf("server reachable state = %q", check.State)
				}
				if check.Fix == "" {
					t.Error("a failing check should carry an actionable fix")
				}
			}
		}
	}
	if !found {
		t.Error("doctor did not report on herdr reachability")
	}
}

func TestDoctorIsHealthyInAGoodEnvironment(t *testing.T) {
	h := newHarness(t)
	diag, err := h.Doctor(context.Background(), h.Repo, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !diag.Healthy {
		for _, group := range diag.Groups {
			for _, check := range group.Checks {
				if check.State == CheckFail {
					t.Errorf("unexpected failure: %s / %s: %s", group.Name, check.Label, check.Detail)
				}
			}
		}
	}
}

func TestSeedCreatesConfigAndRolesOnFirstRun(t *testing.T) {
	configDir := t.TempDir()
	dataDir := t.TempDir()

	app, err := New(Options{
		ConfigFile: filepath.Join(configDir, "config.yaml"),
		RolesDir:   filepath.Join(configDir, "roles"),
		DBPath:     filepath.Join(dataDir, "dispatch.db"),
		DataDir:    dataDir,
		Git:        fakes.NewGit(t.TempDir()),
		Herdr:      fakes.NewHerdr(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer app.Close()

	if _, err := os.Stat(filepath.Join(configDir, "config.yaml")); err != nil {
		t.Errorf("config.yaml not seeded: %v", err)
	}
	for _, name := range []string{"engineer.yaml", "designer.yaml", "reviewer.yaml", "general.yaml", "README.md"} {
		if _, err := os.Stat(filepath.Join(configDir, "roles", name)); err != nil {
			t.Errorf("%s not seeded: %v", name, err)
		}
	}
}

func TestDispatchGivesEachTaskItsOwnWorkspace(t *testing.T) {
	h := newHarness(t)

	first := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "First task"})
	second := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Second task"})

	if first.Task.HerdrWorkspaceID == second.Task.HerdrWorkspaceID {
		t.Errorf("both tasks landed in workspace %q; each should get its own", first.Task.HerdrWorkspaceID)
	}
	for _, task := range []store.Task{first.Task, second.Task} {
		if task.HerdrLayout != string(herdrx.LayoutWorkspace) {
			t.Errorf("%s layout = %q, want workspace", task.Slug, task.HerdrLayout)
		}
		if task.HerdrWorkspaceID == "" {
			t.Errorf("%s has no workspace id", task.Slug)
		}
	}
}

func TestWorktreeTaskOpensItsWorktreeAsTheWorkspace(t *testing.T) {
	h := newHarness(t)
	result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"})

	if len(h.Herdr.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(h.Herdr.Sessions))
	}
	session := h.Herdr.Sessions[0]

	// The workspace must BE the worktree: rooted there, and flagged so herdr
	// shows the repository and branch beside the agent.
	if !session.Worktree {
		t.Error("session was not marked as a worktree workspace")
	}
	if session.CWD != result.Task.Worktree {
		t.Errorf("session CWD = %q, want the worktree %q", session.CWD, result.Task.Worktree)
	}
	if session.RepoRoot != result.Task.ProjectRoot {
		t.Errorf("session RepoRoot = %q, want %q", session.RepoRoot, result.Task.ProjectRoot)
	}
	if session.Layout != herdrx.LayoutWorkspace {
		t.Errorf("layout = %q", session.Layout)
	}
	// The label should say what the workspace is, at a glance in the sidebar.
	if !strings.Contains(session.Label, "engineer") || !strings.Contains(session.Label, result.Task.Slug) {
		t.Errorf("label = %q, want the role and task", session.Label)
	}
}

func TestNonIsolatedTaskGetsAPlainWorkspace(t *testing.T) {
	h := newHarness(t)
	h.dispatch(t, DispatchRequest{Role: "reviewer", Title: "Review the diff"})

	session := h.Herdr.Sessions[0]
	if session.Worktree {
		t.Error("a task with no worktree must not ask herdr to open one")
	}
	if session.Layout != herdrx.LayoutWorkspace {
		t.Errorf("layout = %q, want its own workspace anyway", session.Layout)
	}
	if session.CWD != h.Repo {
		t.Errorf("CWD = %q, want the project root", session.CWD)
	}
}

func TestTabLayoutIsStillAvailable(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(h.ConfigPath, []byte(
		"default_role: general\nworktrees_dir: "+filepath.Join(h.DataDir, "worktrees")+
			"\nherdr:\n  layout: tab\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Reopen so the new config is loaded.
	h2 := reopen(t, h)

	result := h2.dispatch(t, DispatchRequest{Role: "engineer", Title: "Packed into a tab"})
	if result.Task.HerdrLayout != string(herdrx.LayoutTab) {
		t.Errorf("layout = %q, want tab", result.Task.HerdrLayout)
	}
	if h2.Herdr.Sessions[0].Layout != herdrx.LayoutTab {
		t.Errorf("session layout = %q", h2.Herdr.Sessions[0].Layout)
	}
	// Tab layout shares an existing workspace rather than making one.
	if h2.Herdr.Sessions[0].WorkspaceID != "w1" {
		t.Errorf("WorkspaceID = %q, want the focused workspace", h2.Herdr.Sessions[0].WorkspaceID)
	}
}

func TestStopClosesTheWorkspaceItCreated(t *testing.T) {
	h := newHarness(t)
	result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"})
	if h.Herdr.OpenWorkspaceCount() != 1 {
		t.Fatalf("open workspaces = %d, want 1", h.Herdr.OpenWorkspaceCount())
	}

	if _, err := h.Stop(context.Background(), result.Task.ID, StopOptions{}); err != nil {
		t.Fatal(err)
	}

	// An empty workspace left in the sidebar is exactly the clutter that
	// giving each task its own workspace is supposed to avoid.
	if h.Herdr.OpenWorkspaceCount() != 0 {
		t.Errorf("stopping left %d workspaces open", h.Herdr.OpenWorkspaceCount())
	}
	if len(h.Herdr.ClosedWorkspaces) != 1 {
		t.Errorf("closed workspaces = %v, want the task's own", h.Herdr.ClosedWorkspaces)
	}
}

func TestStopOfATabTaskClosesOnlyItsTab(t *testing.T) {
	h := newHarness(t)
	// A task recorded before workspace layout existed, or dispatched under
	// layout: tab, lives in a workspace dispatch does not own. Closing that
	// workspace would take the user's other work with it.
	result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"})
	task, err := h.Store().Get(context.Background(), result.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	task.HerdrLayout = string(herdrx.LayoutTab)
	if err := h.Store().Update(context.Background(), &task); err != nil {
		t.Fatal(err)
	}

	if _, err := h.Stop(context.Background(), task.ID, StopOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(h.Herdr.ClosedWorkspaces) != 0 {
		t.Errorf("a tab task must not close its workspace: %v", h.Herdr.ClosedWorkspaces)
	}
	if len(h.Herdr.ClosedTabs) != 1 {
		t.Errorf("closed tabs = %v, want the task's tab", h.Herdr.ClosedTabs)
	}
}

func TestLegacyTasksWithNoLayoutAreTreatedAsTabs(t *testing.T) {
	h := newHarness(t)
	result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"})
	task, _ := h.Store().Get(context.Background(), result.Task.ID)
	task.HerdrLayout = "" // a row written before the column existed
	if err := h.Store().Update(context.Background(), &task); err != nil {
		t.Fatal(err)
	}

	if _, err := h.Stop(context.Background(), task.ID, StopOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(h.Herdr.ClosedWorkspaces) != 0 {
		t.Errorf("a task with unknown layout must not close a workspace: %v", h.Herdr.ClosedWorkspaces)
	}
}

func TestOrchestratorChildrenEachGetTheirOwnWorkspace(t *testing.T) {
	h := newHarness(t)
	boss := h.dispatch(t, DispatchRequest{Role: "orchestrator", Title: "Ship the revamp"})

	workspaces := map[string]bool{boss.Task.HerdrWorkspaceID: true}
	for _, spec := range []struct{ role, title string }{
		{"designer", "Design the screen"},
		{"engineer", "Implement the screen"},
		{"reviewer", "Review the changes"},
	} {
		child := h.dispatch(t, DispatchRequest{
			Role: spec.role, Title: spec.title, ParentTaskID: boss.Task.ID,
		})
		if workspaces[child.Task.HerdrWorkspaceID] {
			t.Errorf("%s reused workspace %q", spec.role, child.Task.HerdrWorkspaceID)
		}
		workspaces[child.Task.HerdrWorkspaceID] = true
	}
	if len(workspaces) != 4 {
		t.Errorf("workspaces = %d, want one per agent", len(workspaces))
	}
	if h.Herdr.OpenWorkspaceCount() != 4 {
		t.Errorf("open workspaces = %d, want one per agent", h.Herdr.OpenWorkspaceCount())
	}
}

// withDefaultIsolation rewrites the harness config with a given
// default_isolation and reopens the App so it takes effect.
func withDefaultIsolation(t *testing.T, h *harness, mode string) *harness {
	t.Helper()
	if err := os.WriteFile(h.ConfigPath, []byte(
		"default_role: general\ndefault_isolation: \""+mode+"\"\nworktrees_dir: "+
			filepath.Join(h.DataDir, "worktrees")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return reopen(t, h)
}

func TestTheConfiguredDefaultIsolationOutranksTheRole(t *testing.T) {
	h := withDefaultIsolation(t, newHarness(t), "worktree")

	// A reviewer declares `isolation: none`, but the default is a statement
	// about how this user wants to work, so it wins.
	result := h.dispatch(t, DispatchRequest{Role: "reviewer", Title: "Review the diff"})

	if result.Task.Isolation != roles.IsolationWorktree {
		t.Errorf("Isolation = %q, want worktree from the configured default", result.Task.Isolation)
	}
	if result.Task.Worktree == "" || result.Task.Branch == "" {
		t.Errorf("no worktree was prepared: %+v", result.Task)
	}
}

func TestAnExplicitIsolationStillBeatsTheConfiguredDefault(t *testing.T) {
	h := withDefaultIsolation(t, newHarness(t), "worktree")

	result := h.dispatch(t, DispatchRequest{
		Role: "engineer", Title: "Quick fix in place", Isolation: "none",
	})

	if result.Task.Isolation != roles.IsolationNone || result.Task.Worktree != "" {
		t.Errorf("the per-task flag lost to the default: %+v", result.Task)
	}
}

func TestClearingDefaultIsolationHandsTheChoiceBackToRoles(t *testing.T) {
	h := withDefaultIsolation(t, newHarness(t), "")

	reviewer := h.dispatch(t, DispatchRequest{Role: "reviewer", Title: "Read the diff"})
	if reviewer.Task.Isolation != roles.IsolationNone {
		t.Errorf("reviewer Isolation = %q, want the role's own mode", reviewer.Task.Isolation)
	}
	engineer := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"})
	if engineer.Task.Isolation != roles.IsolationWorktree {
		t.Errorf("engineer Isolation = %q, want the role's own mode", engineer.Task.Isolation)
	}
}

func TestAnInvalidDefaultIsolationIsRefusedWithASuggestion(t *testing.T) {
	h := withDefaultIsolation(t, newHarness(t), "container")

	_, err := h.Dispatch(context.Background(), DispatchRequest{
		Dir: h.Repo, Role: "engineer", Title: "Do the thing",
	})
	if err == nil {
		t.Fatal("expected a refusal for an unknown default_isolation")
	}
	var userErr *UserError
	if !errors.As(err, &userErr) {
		t.Fatalf("err = %v, want a UserError", err)
	}
	if !strings.Contains(err.Error(), "default_isolation") {
		t.Errorf("the error should name the setting at fault: %v", err)
	}
	if len(userErr.Hints) == 0 {
		t.Error("a refusal should suggest what to do instead")
	}
}

func TestTheDefaultWorktreeGivesWayOutsideARepository(t *testing.T) {
	h := withDefaultIsolation(t, newHarness(t), "worktree")
	outside := t.TempDir()

	// dispatch is meant to work from any directory, so a blanket default must
	// not turn "run an agent here" into a refusal where there is no repo.
	result, err := h.Dispatch(context.Background(), DispatchRequest{
		Dir: outside, Role: "general", Title: "Research the options",
	})
	if err != nil {
		t.Fatalf("Dispatch outside a repo: %v", err)
	}
	if result.Task.Isolation != roles.IsolationNone || result.Task.Worktree != "" {
		t.Errorf("the default worktree was not given up: %+v", result.Task)
	}
	if len(result.Warnings) == 0 || !strings.Contains(result.Warnings[0], "not a git repository") {
		t.Errorf("the user was not told why: %v", result.Warnings)
	}
	if len(h.Git.Added) != 0 {
		t.Errorf("git worktree created outside a repository: %v", h.Git.Added)
	}
}

func TestAnExplicitWorktreeIsStillRefusedOutsideARepository(t *testing.T) {
	h := withDefaultIsolation(t, newHarness(t), "worktree")
	outside := t.TempDir()

	// Asking for a worktree by name is a requirement, not a preference: the
	// honest answer is a refusal, not a quiet downgrade.
	_, err := h.Dispatch(context.Background(), DispatchRequest{
		Dir: outside, Role: "general", Title: "Do the thing", Isolation: "worktree",
	})
	if err == nil {
		t.Fatal("expected a refusal for an explicit worktree outside a repository")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("err = %v", err)
	}
}

func TestARoleThatNeedsAWorktreeIsStillRefusedOutsideARepository(t *testing.T) {
	h := withDefaultIsolation(t, newHarness(t), "worktree")
	outside := t.TempDir()

	// The engineer role declares `isolation: worktree` itself, so the blanket
	// default is not the only thing asking for one — there is nothing here to
	// quietly give up.
	_, err := h.Dispatch(context.Background(), DispatchRequest{
		Dir: outside, Role: "engineer", Title: "Do the thing",
	})
	if err == nil {
		t.Fatal("expected a refusal outside a repository")
	}
	if !strings.Contains(err.Error(), "--isolation none") {
		t.Errorf("error should suggest the way forward: %v", err)
	}
}
