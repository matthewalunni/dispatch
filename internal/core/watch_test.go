package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/matthewalunni/dispatch/internal/herdrx"
)

func TestWatchResyncsOnConnect(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	changes := make(chan Change, 8)
	go func() { _ = h.Watch(ctx, WatchOptions{}, changes) }()

	select {
	case change := <-changes:
		// A caller cannot know what it missed while disconnected, so the
		// first thing a live stream must say is "re-read everything".
		if change.Reason != ReasonResync {
			t.Errorf("first change = %q, want resync", change.Reason)
		}
		if !change.Live {
			t.Error("a connected stream should report Live")
		}
	case <-ctx.Done():
		t.Fatal("no resync arrived")
	}
}

func TestWatchForwardsAgentStatusChanges(t *testing.T) {
	h := newHarness(t)
	result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	changes := make(chan Change, 8)
	go func() { _ = h.Watch(ctx, WatchOptions{}, changes) }()

	waitForReason(t, ctx, changes, ReasonResync)

	h.Herdr.Emit(herdrx.Event{
		Kind:   herdrx.EventAgentStatusChanged,
		PaneID: result.Task.HerdrPaneID,
		Status: herdrx.StatusBlocked,
	})

	change := waitForReason(t, ctx, changes, ReasonAgentStatus)
	if change.PaneID != result.Task.HerdrPaneID {
		t.Errorf("PaneID = %q, want %q", change.PaneID, result.Task.HerdrPaneID)
	}
	if change.Status != herdrx.StatusBlocked {
		t.Errorf("Status = %q", change.Status)
	}
}

func TestWatchReportsSessionEnd(t *testing.T) {
	h := newHarness(t)
	result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	changes := make(chan Change, 8)
	go func() { _ = h.Watch(ctx, WatchOptions{}, changes) }()
	waitForReason(t, ctx, changes, ReasonResync)

	h.Herdr.Emit(herdrx.Event{Kind: herdrx.EventPaneClosed, PaneID: result.Task.HerdrPaneID})
	change := waitForReason(t, ctx, changes, ReasonSessionEnded)
	if change.PaneID != result.Task.HerdrPaneID {
		t.Errorf("PaneID = %q", change.PaneID)
	}
}

func TestWatchSubscribesToActiveTaskPanes(t *testing.T) {
	h := newHarness(t)
	first := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "First task"})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	changes := make(chan Change, 16)
	go func() { _ = h.Watch(ctx, WatchOptions{PollInterval: 150 * time.Millisecond}, changes) }()
	waitForReason(t, ctx, changes, ReasonResync)

	// A task dispatched from anywhere else must get picked up: the watcher
	// rederives its pane set rather than staying subscribed to a stale one.
	second := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Second task"})

	deadline := time.After(3 * time.Second)
	for {
		if h.Herdr.SubscribedTo(first.Task.HerdrPaneID) && h.Herdr.SubscribedTo(second.Task.HerdrPaneID) {
			return
		}
		select {
		case <-changes:
		case <-deadline:
			t.Fatalf("watcher never subscribed to both panes: %v", h.Herdr.SubscribedPanes())
		}
	}
}

func TestWatchFallsBackToPollingWhenEventsAreUnsupported(t *testing.T) {
	h := newHarness(t)
	h.Herdr.EventsUnsupported = true

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	changes := make(chan Change, 8)
	go func() { _ = h.Watch(ctx, WatchOptions{PollInterval: 100 * time.Millisecond}, changes) }()

	change := waitForReason(t, ctx, changes, ReasonPoll)
	if change.Live {
		// Never claim to be live when running on the fallback.
		t.Error("Live should be false on the polling fallback")
	}
}

func TestWatchStopsWithItsContext(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	changes := make(chan Change, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = h.Watch(ctx, WatchOptions{PollInterval: time.Second}, changes)
	}()

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer waitCancel()
	waitForReason(t, waitCtx, changes, ReasonResync)
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Watch did not return after cancellation")
	}
	// The subscription must be torn down with it, not leaked.
	deadline := time.After(2 * time.Second)
	for h.Herdr.WatchCount() > 0 {
		select {
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatal("event subscription leaked after the watcher stopped")
		}
	}
}

func TestWaitForReturnsImmediatelyWhenAlreadyMatched(t *testing.T) {
	h := newHarness(t)
	result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"})
	h.Herdr.SetStatus(result.Task.HerdrAgent, herdrx.StatusBlocked)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	waited, err := h.WaitFor(ctx, "add-the-endpoint", WaitOptions{Until: []EffectiveStatus{EffectiveWaiting}})
	if err != nil {
		t.Fatalf("WaitFor: %v", err)
	}
	if !waited.Matched || waited.TimedOut {
		t.Errorf("result = %+v, want an immediate match", waited)
	}
	if waited.Task.Effective != EffectiveWaiting {
		t.Errorf("Effective = %q", waited.Task.Effective)
	}
}

func TestWaitForBlocksUntilTheStateArrives(t *testing.T) {
	h := newHarness(t)
	result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"})
	h.Herdr.SetStatus(result.Task.HerdrAgent, herdrx.StatusWorking)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan *WaitResult, 1)
	go func() {
		waited, err := h.WaitFor(ctx, result.Task.ID, WaitOptions{
			Until:        []EffectiveStatus{EffectiveWaiting},
			PollInterval: 100 * time.Millisecond,
		})
		if err != nil {
			t.Errorf("WaitFor: %v", err)
		}
		done <- waited
	}()

	// Let the wait establish itself, then move the agent.
	time.Sleep(200 * time.Millisecond)
	h.Herdr.SetStatus(result.Task.HerdrAgent, herdrx.StatusBlocked)
	h.Herdr.Emit(herdrx.Event{
		Kind:   herdrx.EventAgentStatusChanged,
		PaneID: result.Task.HerdrPaneID,
		Status: herdrx.StatusBlocked,
	})

	select {
	case waited := <-done:
		if !waited.Matched {
			t.Errorf("result = %+v, want a match", waited)
		}
		if waited.Task.Effective != EffectiveWaiting {
			t.Errorf("Effective = %q", waited.Task.Effective)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitFor did not return")
	}
}

func TestWaitForNoticesDispatchSideChanges(t *testing.T) {
	h := newHarness(t)
	result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"})
	h.Herdr.SetStatus(result.Task.HerdrAgent, herdrx.StatusWorking)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan *WaitResult, 1)
	go func() {
		waited, _ := h.WaitFor(ctx, result.Task.ID, WaitOptions{
			Until:        []EffectiveStatus{EffectiveStopped},
			PollInterval: 100 * time.Millisecond,
		})
		done <- waited
	}()

	time.Sleep(200 * time.Millisecond)
	// Stopping moves dispatch's own status with no herdr event at all; the
	// wait has to re-read rather than trust the event stream.
	if _, err := h.Stop(context.Background(), result.Task.ID, StopOptions{}); err != nil {
		t.Fatal(err)
	}

	select {
	case waited := <-done:
		if !waited.Matched || waited.Task.Effective != EffectiveStopped {
			t.Errorf("result = %+v, want a stopped match", waited)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitFor did not notice a dispatch-side status change")
	}
}

func TestWaitForTimesOutWithoutError(t *testing.T) {
	h := newHarness(t)
	result := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"})
	h.Herdr.SetStatus(result.Task.HerdrAgent, herdrx.StatusWorking)

	waited, err := h.WaitFor(context.Background(), result.Task.ID, WaitOptions{
		Until:        []EffectiveStatus{EffectiveWaiting},
		Timeout:      300 * time.Millisecond,
		PollInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("a timeout is an outcome, not an error: %v", err)
	}
	if !waited.TimedOut || waited.Matched {
		t.Errorf("result = %+v, want a timeout", waited)
	}
	// The caller still needs to know where the task got to.
	if waited.Task.Effective != EffectiveWorking {
		t.Errorf("Effective = %q, want the current state reported", waited.Task.Effective)
	}
}

func TestWaitForUnknownTask(t *testing.T) {
	h := newHarness(t)
	_, err := h.WaitFor(context.Background(), "nothing-like-this", WaitOptions{Timeout: time.Second})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "dispatch ls") {
		t.Errorf("error should suggest how to find tasks: %v", err)
	}
}

func TestDispatchExportsTaskIdentityToTheAgent(t *testing.T) {
	h := newHarness(t)
	parent := h.dispatch(t, DispatchRequest{Role: "designer", Title: "Plan the flow"})
	child := h.dispatch(t, DispatchRequest{
		Role: "engineer", Title: "Build the screens", ParentTaskID: parent.Task.ID,
	})

	if len(h.Herdr.TabEnv) != 2 {
		t.Fatalf("tab env captured for %d tabs, want 2", len(h.Herdr.TabEnv))
	}
	env := envMap(h.Herdr.TabEnv[1])

	// An agent that cannot name its own task cannot dispatch child work that
	// keeps lineage, which is what a delegating role needs.
	if env["DISPATCH_TASK_ID"] != child.Task.ID {
		t.Errorf("DISPATCH_TASK_ID = %q, want %q", env["DISPATCH_TASK_ID"], child.Task.ID)
	}
	if env["DISPATCH_TASK_REF"] != child.Task.Slug {
		t.Errorf("DISPATCH_TASK_REF = %q, want %q", env["DISPATCH_TASK_REF"], child.Task.Slug)
	}
	if env["DISPATCH_PROJECT_ROOT"] != child.Task.ProjectRoot {
		t.Errorf("DISPATCH_PROJECT_ROOT = %q", env["DISPATCH_PROJECT_ROOT"])
	}
	if env["DISPATCH_PARENT_TASK_ID"] != parent.Task.ID {
		t.Errorf("DISPATCH_PARENT_TASK_ID = %q, want %q", env["DISPATCH_PARENT_TASK_ID"], parent.Task.ID)
	}

	// A task with no parent must not claim one.
	if _, present := envMap(h.Herdr.TabEnv[0])["DISPATCH_PARENT_TASK_ID"]; present {
		t.Error("a root task should not export a parent id")
	}
}

func TestOrchestratorCanDispatchChildren(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// The end-to-end shape an orchestrating agent uses: it is itself a task,
	// and it dispatches children that reference it by its exported id.
	boss := h.dispatch(t, DispatchRequest{Role: "orchestrator", Title: "Ship the onboarding revamp"})
	if boss.Task.Isolation != "none" {
		t.Errorf("an orchestrator should not take a worktree: %q", boss.Task.Isolation)
	}
	ownID := envMap(h.Herdr.TabEnv[0])["DISPATCH_TASK_ID"]

	for _, spec := range []struct{ role, title string }{
		{"designer", "Design the new first-run screen"},
		{"engineer", "Implement the new first-run screen"},
		{"reviewer", "Review the first-run changes"},
	} {
		h.dispatch(t, DispatchRequest{Role: spec.role, Title: spec.title, ParentTaskID: ownID})
	}

	children, err := h.Store().Children(ctx, boss.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 3 {
		t.Fatalf("children = %d, want 3", len(children))
	}
	roles := map[string]bool{}
	for _, child := range children {
		roles[child.Role] = true
		if child.ParentTaskID != boss.Task.ID {
			t.Errorf("child %q lost its lineage", child.Slug)
		}
	}
	for _, want := range []string{"designer", "engineer", "reviewer"} {
		if !roles[want] {
			t.Errorf("no %s child dispatched", want)
		}
	}
	// Only the engineer's work needed an isolated checkout.
	worktrees := 0
	for _, child := range children {
		if child.Worktree != "" {
			worktrees++
		}
	}
	if worktrees != 1 {
		t.Errorf("worktrees = %d, want only the engineer's", worktrees)
	}
}

func envMap(entries []string) map[string]string {
	out := map[string]string{}
	for _, entry := range entries {
		if key, value, ok := strings.Cut(entry, "="); ok {
			out[key] = value
		}
	}
	return out
}

func waitForReason(t *testing.T, ctx context.Context, changes <-chan Change, reason ChangeReason) Change {
	t.Helper()
	for {
		select {
		case change := <-changes:
			if change.Reason == reason {
				return change
			}
		case <-ctx.Done():
			t.Fatalf("no %q change arrived", reason)
			return Change{}
		}
	}
}
