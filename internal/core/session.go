package core

import (
	"context"
	"fmt"

	"github.com/matthewalunni/dispatch/internal/herdrx"
	"github.com/matthewalunni/dispatch/internal/store"
)

// OpenResult describes how to get back to a task's conversation.
type OpenResult struct {
	Task TaskView `json:"task"`
	// Focused reports that herdr brought the session to the front for any
	// attached client.
	Focused bool `json:"focused"`
	// AttachCommand is the argv that hands this terminal to the agent.
	AttachCommand []string `json:"attach_command,omitempty"`
}

// Open takes the user back to a task's persistent herdr session.
//
// Focusing is what a human wants when a herdr window is already on screen;
// attaching is what they want from a bare shell. Dispatch does the focus and
// hands back the attach command so the caller decides whether to replace its
// own terminal.
func (a *App) Open(ctx context.Context, ref string) (*OpenResult, error) {
	task, err := a.store.Resolve(ctx, ref)
	if err != nil {
		return nil, taskRefError(ref, err)
	}
	view := a.HydrateOne(ctx, task)

	if !view.AgentAlive {
		return nil, &UserError{
			Summary: fmt.Sprintf("Task %q no longer has a live herdr session.", task.Slug),
			Reason:  openDetail(view),
			Hints: []string{
				"dispatch ls --all",
				fmt.Sprintf("dispatch stop %s     # close the task out", task.Slug),
			},
		}
	}

	target := task.SessionRef()
	result := &OpenResult{Task: view, AttachCommand: a.herdr.AttachCommand(target)}
	if err := a.herdr.FocusAgent(ctx, target); err != nil {
		if herdrx.IsCode(err, herdrx.CodeAgentNotFound) {
			return nil, &UserError{
				Summary: fmt.Sprintf("herdr no longer knows about agent %q.", target),
				Hints:   []string{"dispatch ls", "herdr agent list"},
				Err:     err,
			}
		}
		return nil, herdrError("Unable to focus the herdr session.", err)
	}
	result.Focused = true
	return result, nil
}

func openDetail(view TaskView) string {
	if !view.HerdrReachable {
		return "herdr is not reachable, so dispatch cannot tell whether the session is still alive."
	}
	return fmt.Sprintf("herdr has no agent named %q; its pane was probably closed.", view.HerdrAgent)
}

// StopOptions control how far a stop goes.
type StopOptions struct {
	// RemoveWorktree also deletes the task's git worktree.
	RemoveWorktree bool
	// Force removes a worktree that still has modifications.
	Force bool
}

// StopResult reports what a stop actually did.
type StopResult struct {
	Task            TaskView `json:"task"`
	AgentStopped    bool     `json:"agent_stopped"`
	WorktreeRemoved bool     `json:"worktree_removed"`
	Warnings        []string `json:"warnings,omitempty"`
}

// Stop ends a task's agent session and records the outcome.
func (a *App) Stop(ctx context.Context, ref string, opts StopOptions) (*StopResult, error) {
	task, err := a.store.Resolve(ctx, ref)
	if err != nil {
		return nil, taskRefError(ref, err)
	}
	view := a.HydrateOne(ctx, task)
	result := &StopResult{}

	// Tear down whatever dispatch built for this task. When it made a whole
	// workspace, closing only the pane would leave an empty workspace behind
	// in the sidebar; when it made a tab in someone else's workspace, closing
	// that workspace would take their work with it.
	session := taskSession(task)
	if view.AgentAlive || session.OwnsWorkspace() {
		if err := a.herdr.CloseSession(ctx, session); err != nil {
			if !herdrx.IsCode(err, herdrx.CodeAgentNotFound) && !herdrx.IsCode(err, herdrx.CodePaneNotFound) {
				return nil, herdrError("Unable to stop the herdr session.", err)
			}
		} else {
			result.AgentStopped = view.AgentAlive
		}
	}

	if opts.RemoveWorktree && task.Worktree != "" {
		if err := a.git.RemoveWorktree(ctx, task.ProjectRoot, task.Worktree, opts.Force); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"could not remove worktree %s: %s (retry with --force, or `git worktree remove --force %s`)",
				task.Worktree, gitReason(err), task.Worktree))
		} else {
			result.WorktreeRemoved = true
			task.Worktree = ""
		}
	}

	task.Status = store.StatusStopped
	now := a.now()
	task.CompletedAt = &now
	if err := a.store.Update(ctx, &task); err != nil {
		return nil, err
	}
	if err := WriteMetadata(a.dataDir, task); err != nil {
		result.Warnings = append(result.Warnings, err.Error())
	}
	if err := WriteResult(a.dataDir, task, stopDetail(result)); err != nil {
		result.Warnings = append(result.Warnings, err.Error())
	}

	result.Task = a.HydrateOne(ctx, task)
	return result, nil
}

// taskSession reconstructs the herdr container from what dispatch stored.
func taskSession(task store.Task) herdrx.Session {
	layout := herdrx.Layout(task.HerdrLayout)
	if layout != herdrx.LayoutWorkspace {
		// Anything not explicitly a workspace — including tasks created
		// before workspace layout existed — is a tab in a shared workspace.
		layout = herdrx.LayoutTab
	}
	return herdrx.Session{
		WorkspaceID: task.HerdrWorkspaceID,
		TabID:       task.HerdrTabID,
		PaneID:      task.HerdrPaneID,
		Layout:      layout,
	}
}

func stopDetail(r *StopResult) string {
	switch {
	case r.AgentStopped && r.WorktreeRemoved:
		return "agent stopped and worktree removed"
	case r.AgentStopped:
		return "agent stopped"
	default:
		return "task closed; no live agent to stop"
	}
}

// Complete marks a task done without touching its session.
func (a *App) Complete(ctx context.Context, ref string) (store.Task, error) {
	task, err := a.store.Resolve(ctx, ref)
	if err != nil {
		return store.Task{}, taskRefError(ref, err)
	}
	task.Status = store.StatusCompleted
	now := a.now()
	task.CompletedAt = &now
	if err := a.store.Update(ctx, &task); err != nil {
		return store.Task{}, err
	}
	_ = WriteMetadata(a.dataDir, task)
	_ = WriteResult(a.dataDir, task, "marked complete")
	return task, nil
}

func taskRefError(ref string, err error) error {
	var ambiguous *store.AmbiguousError
	if asAmbiguous(err, &ambiguous) {
		return &UserError{
			Summary: fmt.Sprintf("%q is ambiguous.", ref),
			Reason:  ambiguous.Error(),
			Hints:   []string{"dispatch ls --all      # use a longer prefix or the task id"},
			Err:     err,
		}
	}
	return &UserError{
		Summary: fmt.Sprintf("No task matches %q.", ref),
		Hints:   []string{"dispatch ls", "dispatch ls --all"},
		Err:     err,
	}
}

func asAmbiguous(err error, target **store.AmbiguousError) bool {
	if ae, ok := err.(*store.AmbiguousError); ok {
		*target = ae
		return true
	}
	return false
}
