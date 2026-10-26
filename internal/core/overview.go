package core

import (
	"context"
	"sort"
	"time"

	"github.com/matthewalunni/dispatch/internal/herdrx"
	"github.com/matthewalunni/dispatch/internal/store"
)

// InboxItem is one thing waiting on the human: an ask an agent filed, or a
// fallback card for an agent herdr reports as blocked or done with no ask.
type InboxItem struct {
	Task TaskView
	// Ask is nil for a fallback card.
	Ask *store.Ask
	// Screen is the last lines of the agent's screen, fallback cards only.
	Screen string
}

// Column is a board column.
type Column string

const (
	ColumnNeedsYou  Column = "needs_you"
	ColumnWorking   Column = "working"
	ColumnIdle      Column = "idle"
	ColumnDoneToday Column = "done_today"
)

// Overview is the data the CEO view renders.
type Overview struct {
	// Inbox lists asks newest first, then fallback cards.
	Inbox []InboxItem
	// Working is the quiet list under the Inbox: working plus idle tasks.
	Working []TaskView
	Board   map[Column][]TaskView
	// NeedsYou and WorkingCount are the header counts.
	NeedsYou, WorkingCount int
}

// OverviewOptions narrows and tunes an Overview.
type OverviewOptions struct {
	ProjectRoot string
	// ScreenLines is how much screen a fallback card shows; default 6.
	ScreenLines int
}

// recentLimit caps how many finished tasks the Done today column considers.
const recentLimit = 50

// Overview builds the inbox and board from live tasks and recent history.
func (a *App) Overview(ctx context.Context, opts OverviewOptions) (*Overview, error) {
	active, err := a.ActiveViews(ctx)
	if err != nil {
		return nil, err
	}
	tasks, err := a.store.List(ctx, store.Filter{IncludeTerminal: true, Limit: recentLimit})
	if err != nil {
		return nil, err
	}
	recent := a.Hydrate(ctx, tasks)

	if opts.ProjectRoot != "" {
		active = inProject(active, opts.ProjectRoot)
		recent = inProject(recent, opts.ProjectRoot)
	}

	o := GroupOverview(active, recent, a.now())

	lines := opts.ScreenLines
	if lines <= 0 {
		lines = 6
	}
	for i := range o.Inbox {
		item := &o.Inbox[i]
		if item.Ask != nil {
			continue
		}
		// A screen is decoration: a failed read leaves the card without one.
		if screen, err := a.herdr.ReadAgent(ctx, agentTarget(item.Task), lines); err == nil {
			item.Screen = screen
		}
	}
	return o, nil
}

func inProject(views []TaskView, root string) []TaskView {
	out := make([]TaskView, 0, len(views))
	for _, v := range views {
		if v.ProjectRoot == root {
			out = append(out, v)
		}
	}
	return out
}

func agentTarget(v TaskView) string {
	if v.HerdrAgent != "" {
		return v.HerdrAgent
	}
	return v.HerdrPaneID
}

// GroupOverview sorts views into the inbox and board columns. It is pure and
// leaves every Screen empty.
func GroupOverview(active, recent []TaskView, now time.Time) *Overview {
	o := &Overview{Board: map[Column][]TaskView{}}

	var asks, cards []InboxItem
	for _, v := range active {
		switch {
		case len(v.OpenAsks) > 0:
			for i := range v.OpenAsks {
				asks = append(asks, InboxItem{Task: v, Ask: &v.OpenAsks[i]})
			}
		case v.AgentStatus == herdrx.StatusBlocked || v.AgentStatus == herdrx.StatusDone:
			cards = append(cards, InboxItem{Task: v})
		default:
			switch {
			case v.AgentStatus == herdrx.StatusWorking:
				o.Board[ColumnWorking] = append(o.Board[ColumnWorking], v)
				o.Working = append(o.Working, v)
			case v.AgentAlive && v.AgentStatus == herdrx.StatusIdle:
				o.Board[ColumnIdle] = append(o.Board[ColumnIdle], v)
				o.Working = append(o.Working, v)
			}
			continue
		}
		o.Board[ColumnNeedsYou] = append(o.Board[ColumnNeedsYou], v)
	}
	sort.SliceStable(asks, func(i, j int) bool { return asks[i].Ask.CreatedAt.After(asks[j].Ask.CreatedAt) })
	o.Inbox = append(asks, cards...)

	// "Today" is the user's day: dispatch's clock runs in UTC.
	local := now.In(time.Local)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
	for _, v := range recent {
		if v.Status.Terminal() && v.CompletedAt != nil && !v.CompletedAt.Before(midnight) {
			o.Board[ColumnDoneToday] = append(o.Board[ColumnDoneToday], v)
		}
	}

	o.NeedsYou = len(o.Board[ColumnNeedsYou])
	o.WorkingCount = len(o.Board[ColumnWorking])
	return o
}
