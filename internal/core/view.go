package core

import (
	"context"
	"time"

	"github.com/matthewalunni/dispatch/internal/herdrx"
	"github.com/matthewalunni/dispatch/internal/store"
)

// EffectiveStatus is what a human or an orchestrator should read as "what is
// this task doing right now".
//
// Dispatch's own status says what a task *is*; herdr says whether its process
// is alive, working or waiting for a human. The effective status is the
// combination, resolved in one place so the CLI, the JSON output and the TUI
// can never disagree.
type EffectiveStatus string

const (
	EffectiveWorking   EffectiveStatus = "working"
	EffectiveWaiting   EffectiveStatus = "waiting"
	EffectiveIdle      EffectiveStatus = "idle"
	EffectiveDone      EffectiveStatus = "done"
	EffectiveDetached  EffectiveStatus = "detached"
	EffectiveStopped   EffectiveStatus = "stopped"
	EffectiveCompleted EffectiveStatus = "completed"
	EffectiveFailed    EffectiveStatus = "failed"
	EffectivePending   EffectiveStatus = "pending"
	EffectiveUnknown   EffectiveStatus = "unknown"
)

// NeedsAttention reports statuses a human should look at.
func (s EffectiveStatus) NeedsAttention() bool { return s == EffectiveWaiting }

// Active reports statuses that represent a live session.
func (s EffectiveStatus) Active() bool {
	switch s {
	case EffectiveWorking, EffectiveWaiting, EffectiveIdle, EffectiveDone, EffectiveUnknown:
		return true
	}
	return false
}

// TaskView is a task joined with herdr's live runtime state.
type TaskView struct {
	store.Task
	// AgentStatus is herdr's live state, empty when herdr has no such agent.
	AgentStatus herdrx.Status `json:"agent_status,omitempty"`
	// AgentAlive reports whether herdr still knows about the agent.
	AgentAlive bool `json:"agent_alive"`
	// Effective is the resolved status a human should read.
	Effective EffectiveStatus `json:"effective_status"`
	// HerdrReachable reports whether live state could be read at all.
	HerdrReachable bool `json:"herdr_reachable"`
}

// Age renders the task's age relative to now.
func (v TaskView) Age(now time.Time) string { return store.HumanDuration(now.Sub(v.CreatedAt)) }

// Hydrate joins tasks with herdr's live agent list.
//
// One herdr call serves any number of tasks, and an unreachable herdr degrades
// to dispatch's own view rather than failing the command.
func (a *App) Hydrate(ctx context.Context, tasks []store.Task) []TaskView {
	live := map[string]herdrx.Agent{}
	reachable := false
	if agents, err := a.herdr.ListAgents(ctx); err == nil {
		reachable = true
		for _, agent := range agents {
			if agent.Name != "" {
				live[agent.Name] = agent
			}
			if agent.PaneID != "" {
				live[agent.PaneID] = agent
			}
		}
	}

	out := make([]TaskView, 0, len(tasks))
	for _, task := range tasks {
		view := TaskView{Task: task, HerdrReachable: reachable}
		if agent, ok := lookupAgent(live, task); ok {
			view.AgentAlive = true
			view.AgentStatus = agent.Status
		}
		view.Effective = resolveStatus(task, view, reachable)
		out = append(out, view)
	}
	return out
}

// HydrateOne is Hydrate for a single task.
func (a *App) HydrateOne(ctx context.Context, task store.Task) TaskView {
	views := a.Hydrate(ctx, []store.Task{task})
	if len(views) == 0 {
		return TaskView{Task: task}
	}
	return views[0]
}

func lookupAgent(live map[string]herdrx.Agent, task store.Task) (herdrx.Agent, bool) {
	if task.HerdrAgent != "" {
		if agent, ok := live[task.HerdrAgent]; ok {
			return agent, true
		}
	}
	if task.HerdrPaneID != "" {
		if agent, ok := live[task.HerdrPaneID]; ok {
			return agent, true
		}
	}
	return herdrx.Agent{}, false
}

func resolveStatus(task store.Task, view TaskView, herdrReachable bool) EffectiveStatus {
	switch task.Status {
	case store.StatusStopped:
		return EffectiveStopped
	case store.StatusCompleted:
		return EffectiveCompleted
	case store.StatusFailed:
		return EffectiveFailed
	case store.StatusPending:
		if !view.AgentAlive {
			return EffectivePending
		}
	}

	if !view.AgentAlive {
		if !herdrReachable {
			// Do not claim a session died just because herdr is unreachable.
			return EffectiveUnknown
		}
		return EffectiveDetached
	}

	switch view.AgentStatus {
	case herdrx.StatusWorking:
		return EffectiveWorking
	case herdrx.StatusBlocked:
		return EffectiveWaiting
	case herdrx.StatusIdle:
		return EffectiveIdle
	case herdrx.StatusDone:
		return EffectiveDone
	default:
		return EffectiveUnknown
	}
}

// ActiveViews returns live tasks joined with herdr state, newest first.
func (a *App) ActiveViews(ctx context.Context) ([]TaskView, error) {
	tasks, err := a.store.List(ctx, store.ActiveFilter())
	if err != nil {
		return nil, err
	}
	return a.Hydrate(ctx, tasks), nil
}

// NeedsAttention returns the live tasks whose agents are waiting on a human.
func (a *App) NeedsAttention(ctx context.Context) ([]TaskView, error) {
	views, err := a.ActiveViews(ctx)
	if err != nil {
		return nil, err
	}
	var out []TaskView
	for _, v := range views {
		if v.Effective.NeedsAttention() {
			out = append(out, v)
		}
	}
	return out, nil
}
