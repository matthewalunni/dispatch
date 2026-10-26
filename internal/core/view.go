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
	EffectiveEnded     EffectiveStatus = "ended"
	EffectiveStopped   EffectiveStatus = "stopped"
	EffectiveCompleted EffectiveStatus = "completed"
	EffectiveFailed    EffectiveStatus = "failed"
	EffectivePending   EffectiveStatus = "pending"
	EffectiveUnknown   EffectiveStatus = "unknown"
)

// EndGrace is how long a running task may be missing from herdr before
// dispatch concludes its session is gone, so a task still being launched is
// not ended by a race.
const EndGrace = 60 * time.Second

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
	// Report is the agent's latest self-reported status, if it has sent one.
	Report *store.Report `json:"report,omitempty"`
	// OpenAsks are the task's asks that are open or answered but undelivered.
	OpenAsks []store.Ask `json:"open_asks,omitempty"`
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

	var changed map[string]store.Status
	if reachable {
		changed = a.reconcile(ctx, live)
		a.deliverPending(ctx, live)
	}

	ids := make([]string, len(tasks))
	for i, task := range tasks {
		ids[i] = task.ID
	}
	reports, _ := a.store.Reports(ctx, ids)
	openAsks := map[string][]store.Ask{}
	if len(ids) > 0 {
		asks, _ := a.store.ListAsks(ctx, store.AskFilter{TaskIDs: ids, States: []store.AskState{store.AskOpen, store.AskAnswered}})
		for _, ask := range asks {
			openAsks[ask.TaskID] = append(openAsks[ask.TaskID], ask)
		}
	}

	out := make([]TaskView, 0, len(tasks))
	for _, task := range tasks {
		if status, ok := changed[task.ID]; ok {
			task.Status = status
		}
		view := TaskView{Task: task, HerdrReachable: reachable, OpenAsks: openAsks[task.ID]}
		if r, ok := reports[task.ID]; ok {
			view.Report = &r
		}
		if agent, ok := lookupAgent(live, task); ok {
			view.AgentAlive = true
			view.AgentStatus = agent.Status
		}
		view.Effective = resolveStatus(task, view, reachable)
		out = append(out, view)
	}
	return out
}

// reconcile brings stored task status in line with what herdr reports, and
// returns the tasks whose status it changed. It runs only when herdr is
// reachable, so an outage never ends anything.
//
// A running task whose agent is gone past EndGrace becomes ended; an ended
// task whose own agent has returned becomes running again. Agent names and
// pane ids can be reused, so an ended task is revived only on an exact match
// and never onto an agent a live task already claims.
func (a *App) reconcile(ctx context.Context, live map[string]herdrx.Agent) map[string]store.Status {
	changed := map[string]store.Status{}
	tasks, err := a.store.List(ctx, store.ActiveOrEndedFilter())
	if err != nil {
		return changed
	}
	for i, task := range tasks {
		if task.Status != store.StatusRunning {
			continue
		}
		if _, alive := lookupAgent(live, task); alive || a.now().Sub(task.CreatedAt) <= EndGrace {
			continue
		}
		// Reconciliation is best effort: a failure here is retried on the
		// next hydrate and must never fail the caller.
		if err := a.store.SetStatus(ctx, task.ID, store.StatusEnded); err != nil {
			continue
		}
		tasks[i].Status = store.StatusEnded
		_ = WriteResult(a.dataDir, tasks[i], "session closed outside dispatch")
		changed[task.ID] = store.StatusEnded
	}

	claimed := map[string]bool{}
	claim := func(task store.Task) {
		if task.HerdrAgent != "" {
			claimed[task.HerdrAgent] = true
		}
		if task.HerdrPaneID != "" {
			claimed[task.HerdrPaneID] = true
		}
	}
	for _, task := range tasks {
		if !task.Status.Terminal() {
			claim(task)
		}
	}
	// Newest first, so if several ended tasks match one agent the most
	// recent is the one revived.
	for _, task := range tasks {
		if task.Status != store.StatusEnded || claimed[task.HerdrAgent] || claimed[task.HerdrPaneID] {
			continue
		}
		if !ownAgentAlive(live, task) {
			continue
		}
		if err := a.store.SetStatus(ctx, task.ID, store.StatusRunning); err != nil {
			continue
		}
		claim(task)
		changed[task.ID] = store.StatusRunning
	}
	return changed
}

// ownAgentAlive reports whether herdr has the task's agent back: when both a
// name and a pane id are stored, both must match the same live agent.
func ownAgentAlive(live map[string]herdrx.Agent, task store.Task) bool {
	if task.HerdrAgent != "" && task.HerdrPaneID != "" {
		agent, ok := live[task.HerdrAgent]
		return ok && agent.Name == task.HerdrAgent && agent.PaneID == task.HerdrPaneID
	}
	_, ok := lookupAgent(live, task)
	return ok
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
	case store.StatusEnded:
		return EffectiveEnded
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
	tasks, err := a.store.List(ctx, store.ActiveOrEndedFilter())
	if err != nil {
		return nil, err
	}
	return DropTerminal(a.Hydrate(ctx, tasks)), nil
}

// DropTerminal removes views of tasks that hydration found to be finished.
// Hydrate can end a task that was listed as active.
func DropTerminal(views []TaskView) []TaskView {
	out := views[:0]
	for _, v := range views {
		if !v.Task.Status.Terminal() {
			out = append(out, v)
		}
	}
	return out
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
