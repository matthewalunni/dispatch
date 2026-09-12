package core

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/matthewalunni/dispatch/internal/herdrx"
	"github.com/matthewalunni/dispatch/internal/store"
)

// ChangeReason says why dispatch thinks live state moved.
type ChangeReason string

const (
	// ReasonAgentStatus is herdr reporting a lifecycle transition.
	ReasonAgentStatus ChangeReason = "agent_status"
	// ReasonSessionEnded is a pane closing or its process exiting.
	ReasonSessionEnded ChangeReason = "session_ended"
	// ReasonResync is a (re)connected stream: anything may have changed while
	// dispatch was not listening, so the caller must re-read.
	ReasonResync ChangeReason = "resync"
	// ReasonPoll is the periodic safety net.
	ReasonPoll ChangeReason = "poll"
)

// Change is a signal that a caller should re-read task state.
//
// It deliberately carries no task payload: herdr is the source of truth for
// runtime state and dispatch's database for everything else, so the caller
// re-reads both rather than trusting a diff assembled here.
type Change struct {
	Reason ChangeReason
	// PaneID is set when one specific pane moved.
	PaneID string
	// Status is herdr's new status, when the event carried one.
	Status herdrx.Status
	// Live reports whether the event stream is currently connected. When
	// false the caller is running on the polling fallback.
	Live bool
}

// WatchOptions tune the watcher.
type WatchOptions struct {
	// PollInterval is the safety net that runs regardless of the event
	// stream. Zero uses a sensible default.
	PollInterval time.Duration
	// Panes restricts agent-status subscriptions. Empty means "whatever the
	// active tasks are using", refreshed as tasks come and go.
	Panes []string
}

// DefaultPollInterval is the fallback cadence. It is deliberately slow: it
// exists to catch what the event stream missed, not to drive the UI.
const DefaultPollInterval = 30 * time.Second

// Watch emits a Change whenever live task state may have moved.
//
// It prefers herdr's event stream and falls back to polling when live events
// are unavailable, so callers get one behaviour that degrades rather than two
// code paths. It returns when ctx is cancelled.
func (a *App) Watch(ctx context.Context, opts WatchOptions, out chan<- Change) error {
	interval := opts.PollInterval
	if interval <= 0 {
		interval = DefaultPollInterval
	}

	emit := func(change Change) {
		select {
		case out <- change:
		case <-ctx.Done():
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// The polling safety net always runs. With live events it fires rarely
	// enough to be free; without them it is the whole mechanism.
	watcher, canWatch := a.herdr.(herdrx.Watcher)
	if !canWatch {
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				emit(Change{Reason: ReasonPoll})
			}
		}
	}

	stream := newStreamSupervisor(ctx, a, watcher, opts.Panes)
	defer stream.stop()
	stream.restart()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case change := <-stream.events:
			change.Live = stream.connected()
			emit(change)

		case <-stream.unsupported:
			// Live events can never work here; stop trying and let the poll
			// carry the load for the rest of this watch.
			stream.gaveUp = true
			stream.stop()

		case <-ticker.C:
			// A dispatch from another terminal adds a pane this stream is not
			// subscribed to, so the safety net is also where we notice the
			// set has changed and resubscribe.
			stream.restart()
			emit(Change{Reason: ReasonPoll, Live: stream.connected()})
		}
	}
}

// streamSupervisor owns the herdr event subscription and keeps it pointed at
// the current set of task panes.
type streamSupervisor struct {
	ctx     context.Context
	app     *App
	watcher herdrx.Watcher
	// explicit pins the subscription to a fixed pane set; empty means the
	// set is derived from the active tasks and refreshed as they change.
	explicit []string

	events      chan Change
	unsupported chan struct{}

	live atomic.Bool

	cancel context.CancelFunc
	done   chan struct{}
	sig    string
	gaveUp bool
}

func newStreamSupervisor(ctx context.Context, app *App, watcher herdrx.Watcher, explicit []string) *streamSupervisor {
	return &streamSupervisor{
		ctx:         ctx,
		app:         app,
		watcher:     watcher,
		explicit:    explicit,
		events:      make(chan Change, 64),
		unsupported: make(chan struct{}, 1),
	}
}

func (s *streamSupervisor) connected() bool { return s.live.Load() }

// restart (re)subscribes when the pane set has changed, and is a no-op when it
// has not, so calling it on every tick is cheap.
func (s *streamSupervisor) restart() {
	if s.gaveUp || s.ctx.Err() != nil {
		return
	}
	panes := s.explicit
	if len(panes) == 0 {
		panes = s.app.watchPanes(s.ctx, nil)
	}
	sig := strings.Join(panes, "\x00")
	if s.cancel != nil && sig == s.sig {
		return
	}
	s.stop()
	s.sig = sig

	streamCtx, cancel := context.WithCancel(s.ctx)
	done := make(chan struct{})
	s.cancel, s.done = cancel, done

	go func() {
		defer close(done)
		err := herdrx.WatchWithRetry(streamCtx, s.watcher, panes,
			func(event herdrx.Event) {
				change, ok := toChange(event)
				if !ok {
					return
				}
				select {
				case s.events <- change:
				case <-streamCtx.Done():
				}
			},
			func(connected bool, _ error) { s.live.Store(connected) },
		)
		s.live.Store(false)
		if errors.Is(err, herdrx.ErrEventsUnsupported) || errors.Is(err, herdrx.ErrNotInstalled) {
			select {
			case s.unsupported <- struct{}{}:
			default:
			}
		}
	}()
}

func (s *streamSupervisor) stop() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	<-s.done
	s.cancel, s.done = nil, nil
	s.live.Store(false)
}

// watchPanes is the set of panes whose agent-status transitions matter.
func (a *App) watchPanes(ctx context.Context, explicit []string) []string {
	if len(explicit) > 0 {
		return explicit
	}
	tasks, err := a.store.List(ctx, store.ActiveFilter())
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var panes []string
	for _, task := range tasks {
		if task.HerdrPaneID != "" && !seen[task.HerdrPaneID] {
			seen[task.HerdrPaneID] = true
			panes = append(panes, task.HerdrPaneID)
		}
	}
	sort.Strings(panes)
	return panes
}

func toChange(event herdrx.Event) (Change, bool) {
	switch event.Kind {
	case herdrx.EventStreamReady:
		return Change{Reason: ReasonResync}, true
	case herdrx.EventAgentStatusChanged:
		return Change{Reason: ReasonAgentStatus, PaneID: event.PaneID, Status: event.Status}, true
	case herdrx.EventAgentDetected:
		return Change{Reason: ReasonAgentStatus, PaneID: event.PaneID}, true
	case herdrx.EventPaneClosed, herdrx.EventPaneExited:
		return Change{Reason: ReasonSessionEnded, PaneID: event.PaneID}, true
	default:
		return Change{}, false
	}
}

// WaitResult is the outcome of blocking on a task's state.
type WaitResult struct {
	Task     TaskView `json:"task"`
	Matched  bool     `json:"matched"`
	TimedOut bool     `json:"timed_out"`
	// Live reports whether the wait was event-driven or fell back to polling.
	Live bool `json:"live"`
}

// WaitOptions control WaitFor.
type WaitOptions struct {
	// Until is the set of effective statuses to wait for. Empty waits for any
	// status where the agent is no longer producing work.
	Until []EffectiveStatus
	// Timeout bounds the wait. Zero waits indefinitely.
	Timeout time.Duration
	// PollInterval overrides the safety-net cadence.
	PollInterval time.Duration
}

// DefaultWaitStates are the states a caller normally cares about: the agent
// has stopped producing work and either needs a human or has finished.
func DefaultWaitStates() []EffectiveStatus {
	return []EffectiveStatus{EffectiveWaiting, EffectiveIdle, EffectiveDone, EffectiveDetached}
}

// WaitFor blocks until a task reaches one of the requested states.
//
// This is the primitive an orchestrating agent needs: hand out work, then wait
// on it without polling `dispatch ls` in a loop.
func (a *App) WaitFor(ctx context.Context, ref string, opts WaitOptions) (*WaitResult, error) {
	task, err := a.store.Resolve(ctx, ref)
	if err != nil {
		return nil, taskRefError(ref, err)
	}

	until := opts.Until
	if len(until) == 0 {
		until = DefaultWaitStates()
	}
	wanted := make(map[EffectiveStatus]bool, len(until))
	for _, status := range until {
		wanted[status] = true
	}

	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	// Check before subscribing: the task may already be in a wanted state,
	// and a caller must never block on something that already happened.
	if view := a.HydrateOne(ctx, task); wanted[view.Effective] {
		return &WaitResult{Task: view, Matched: true}, nil
	}

	changes := make(chan Change, 16)
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		defer close(changes)
		_ = a.Watch(watchCtx, WatchOptions{
			Panes:        []string{task.HerdrPaneID},
			PollInterval: opts.PollInterval,
		}, changes)
	}()

	// The final read must not use ctx: when the deadline is what woke us, a
	// cancelled context would fail the very query that reports where the task
	// actually got to.
	finish := func(matched bool, live bool) *WaitResult {
		final := context.WithoutCancel(ctx)
		current, err := a.store.Get(final, task.ID)
		if err != nil {
			current = task
		}
		return &WaitResult{Task: a.HydrateOne(final, current), Matched: matched, Live: live}
	}

	// Cancelling the wait closes the change channel and fires ctx.Done at the
	// same time, so both have to produce the same verdict: whichever the
	// select happens to pick, a deadline is still a timeout.
	outcome := func(live bool) (*WaitResult, error) {
		result := finish(false, live)
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			result.TimedOut = true
			return result, nil
		case ctx.Err() != nil:
			return result, ctx.Err()
		default:
			return result, nil
		}
	}

	live := false
	for {
		select {
		case <-ctx.Done():
			return outcome(live)

		case change, ok := <-changes:
			if !ok {
				return outcome(live)
			}
			live = live || change.Live
			if ctx.Err() != nil {
				continue // let the ctx.Done() branch produce one outcome
			}
			// Re-read rather than trusting the event: dispatch's own status
			// (a stop, a completion) can move without any herdr event.
			current, err := a.store.Get(ctx, task.ID)
			if err != nil {
				if ctx.Err() != nil {
					continue
				}
				return nil, err
			}
			view := a.HydrateOne(ctx, current)
			if wanted[view.Effective] {
				return &WaitResult{Task: view, Matched: true, Live: live}, nil
			}
		}
	}
}
