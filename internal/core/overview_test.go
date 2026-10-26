package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matthewalunni/dispatch/internal/herdrx"
	"github.com/matthewalunni/dispatch/internal/store"
)

func viewOf(id string, status herdrx.Status, asks ...store.Ask) TaskView {
	return TaskView{
		Task:        store.Task{ID: id, Status: store.StatusRunning},
		AgentStatus: status,
		AgentAlive:  true,
		OpenAsks:    asks,
	}
}

func TestGroupPutsAsksInTheInbox(t *testing.T) {
	now := time.Now()
	v := viewOf("t1", herdrx.StatusWorking, store.Ask{ID: "a1", CreatedAt: now.Add(-time.Minute)}, store.Ask{ID: "a2", CreatedAt: now})
	o := GroupOverview([]TaskView{v}, nil, now)

	if len(o.Inbox) != 2 {
		t.Fatalf("inbox = %d items, want 2", len(o.Inbox))
	}
	if o.Inbox[0].Ask.ID != "a2" || o.Inbox[1].Ask.ID != "a1" {
		t.Errorf("asks not newest first: %s, %s", o.Inbox[0].Ask.ID, o.Inbox[1].Ask.ID)
	}
	if n := len(o.Board[ColumnNeedsYou]); n != 1 {
		t.Errorf("needs you = %d, want the task once", n)
	}
	if len(o.Board[ColumnWorking]) != 0 || len(o.Working) != 0 {
		t.Errorf("a task needing the human must not also be working")
	}
	if o.NeedsYou != 1 || o.WorkingCount != 0 {
		t.Errorf("counts = %d/%d", o.NeedsYou, o.WorkingCount)
	}
}

func TestGroupFallbackCardsForBlockedAndDone(t *testing.T) {
	o := GroupOverview([]TaskView{
		viewOf("blocked", herdrx.StatusBlocked),
		viewOf("done", herdrx.StatusDone),
		viewOf("idle", herdrx.StatusIdle),
		viewOf("busy", herdrx.StatusWorking),
	}, nil, time.Now())

	if len(o.Inbox) != 2 {
		t.Fatalf("inbox = %d, want 2 fallback cards", len(o.Inbox))
	}
	for _, item := range o.Inbox {
		if item.Ask != nil {
			t.Errorf("%s: fallback card has an ask", item.Task.ID)
		}
	}
	if len(o.Board[ColumnIdle]) != 1 || o.Board[ColumnIdle][0].ID != "idle" {
		t.Errorf("idle column = %+v", o.Board[ColumnIdle])
	}
	if len(o.Board[ColumnWorking]) != 1 || o.WorkingCount != 1 || o.NeedsYou != 2 {
		t.Errorf("working = %d count = %d needs = %d", len(o.Board[ColumnWorking]), o.WorkingCount, o.NeedsYou)
	}
	if len(o.Working) != 2 {
		t.Errorf("quiet list = %d, want working + idle", len(o.Working))
	}
}

func TestGroupDoneTodayUsesLocalMidnight(t *testing.T) {
	now := time.Date(2026, 10, 26, 15, 0, 0, 0, time.Local)
	recent := func(id string, at time.Time, status store.Status) TaskView {
		return TaskView{Task: store.Task{ID: id, Status: status, CompletedAt: &at}}
	}
	o := GroupOverview(nil, []TaskView{
		recent("in", now.Add(-time.Hour), store.StatusCompleted),
		recent("out", now.Add(-24*time.Hour), store.StatusCompleted),
		{Task: store.Task{ID: "live", Status: store.StatusRunning}},
	}, now)

	got := o.Board[ColumnDoneToday]
	if len(got) != 1 || got[0].ID != "in" {
		t.Errorf("done today = %+v, want only the task finished an hour ago", got)
	}
}

func TestOverviewFillsScreensForFallbackCards(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	blocked := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Blocked one"}).Task
	asked := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Asked one"}).Task
	h.Herdr.SetStatus(blocked.HerdrAgent, herdrx.StatusBlocked)
	h.Herdr.SetStatus(asked.HerdrAgent, herdrx.StatusBlocked)
	if _, err := h.FileAsk(ctx, asked.ID, decisionAsk()); err != nil {
		t.Fatal(err)
	}
	h.Herdr.Screens = map[string]string{blocked.HerdrAgent: "Proceed? (y/n)", asked.HerdrAgent: "ignored"}

	o, err := h.Overview(ctx, OverviewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Inbox) != 2 {
		t.Fatalf("inbox = %d, want an ask and a fallback", len(o.Inbox))
	}
	for _, item := range o.Inbox {
		switch {
		case item.Ask != nil && item.Screen != "":
			t.Errorf("ask item has a screen: %q", item.Screen)
		case item.Ask == nil && item.Screen != "Proceed? (y/n)":
			t.Errorf("fallback screen = %q", item.Screen)
		}
	}

	h.Herdr.ReadErr = errors.New("boom")
	o, err = h.Overview(ctx, OverviewOptions{})
	if err != nil {
		t.Fatalf("a failed screen read must not fail the overview: %v", err)
	}
	for _, item := range o.Inbox {
		if item.Screen != "" {
			t.Errorf("screen = %q after a read error", item.Screen)
		}
	}
}

func TestOverviewFiltersByProject(t *testing.T) {
	h := newHarness(t)
	other := t.TempDir()
	h.Git.Repos[other] = other
	h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Here"})
	h.dispatch(t, DispatchRequest{Role: "engineer", Title: "There", Dir: other})

	all, err := h.Overview(context.Background(), OverviewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Working)+len(all.Board[ColumnIdle]) < 2 {
		t.Fatalf("unfiltered overview lost tasks: %+v", all)
	}
	scoped, err := h.Overview(context.Background(), OverviewOptions{ProjectRoot: other})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, col := range scoped.Board {
		for _, v := range col {
			n++
			if v.ProjectRoot != other {
				t.Errorf("task from %q leaked into the %q view", v.ProjectRoot, other)
			}
		}
	}
	if n != 1 {
		t.Errorf("scoped tasks = %d, want 1", n)
	}
}
