package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/matthewalunni/dispatch/internal/core"
	"github.com/matthewalunni/dispatch/internal/store"
)

func boardOverview() *core.Overview {
	need := task("n1", "growth", "RepClock", "Launch ads")
	work := task("w1", "engineer", "ReadFlow", "Offline sync")
	work.Report = &store.Report{Line: "adding offline sync tests", Done: 2, Total: 3}
	orch := task("o1", "orchestrator", "RepClock", "Launch ads test")
	kid1 := task("k1", "growth", "RepClock", "Copy")
	kid1.ParentTaskID = "o1"
	kid2 := task("k2", "designer", "RepClock", "Art")
	kid2.ParentTaskID = "o1"
	done := task("d1", "reviewer", "ReadFlow", "approved PR #14")
	return &core.Overview{Board: map[core.Column][]core.TaskView{
		core.ColumnNeedsYou:  {need},
		core.ColumnWorking:   {work, orch, kid1, kid2},
		core.ColumnDoneToday: {done},
	}}
}

func TestProgressBar(t *testing.T) {
	got := progressBar(2, 3, 9)
	if got != strings.Repeat("█", 6)+strings.Repeat("░", 3) {
		t.Errorf("progressBar(2,3,9) = %q", got)
	}
	if progressBar(0, 0, 9) != "" {
		t.Errorf("no total should give no bar")
	}
}

func TestCardTruncatesLongTitles(t *testing.T) {
	v := task("t1", "engineer", "A-very-long-project-name", strings.Repeat("long title ", 20))
	v.Report = &store.Report{Line: strings.Repeat("doing a lot ", 20), Done: 1, Total: 4}
	for _, w := range []int{24, 32, 40} {
		out := renderCard(v, nil, w, time.Now())
		for _, line := range strings.Split(out, "\n") {
			if lipgloss.Width(line) > w {
				t.Errorf("width %d: line %q is %d wide", w, line, lipgloss.Width(line))
			}
		}
	}
}

func TestOrchestratorCardListsHelpers(t *testing.T) {
	orch := task("o1", "orchestrator", "RepClock", "Launch ads test")
	kids := []core.TaskView{task("k1", "growth", "RepClock", "a"), task("k2", "designer", "RepClock", "b")}
	assertContains(t, renderCard(orch, kids, 40, time.Now()), "└ 2 helpers: growth, designer")
}

func TestBoardStacksOnNarrowTerminals(t *testing.T) {
	m := testModel(boardOverview())
	m.tab = tabBoard

	m.width = 120
	lines := nonBlank(m.viewBoard())
	assertContains(t, lines[0], "NEEDS YOU · 1", "WORKING · 4", "IDLE · 0", "DONE TODAY · 1")

	m.width = 80
	out := m.viewBoard()
	prev := -1
	for _, h := range []string{"NEEDS YOU · 1", "WORKING · 4", "IDLE · 0", "DONE TODAY · 1"} {
		i := strings.Index(out, h)
		if i <= prev {
			t.Fatalf("header %q missing or out of order:\n%s", h, out)
		}
		prev = i
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, h) && strings.Count(line, " · ") > 1 {
				t.Errorf("headers share a line at width 80: %q", line)
			}
		}
	}
	assertContains(t, out, "└ 2 helpers: growth, designer", "2 of 3")
}

func nonBlank(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func TestBoardNavigationClampsToNonEmptyColumns(t *testing.T) {
	m := testModel(boardOverview())
	m.tab = tabBoard
	m.handleKey(key("l")) // working
	if m.boardCol != core.ColumnWorking {
		t.Fatalf("l: col = %v", m.boardCol)
	}
	m.handleKey(key("l")) // skips empty idle
	if m.boardCol != core.ColumnDoneToday {
		t.Fatalf("l over empty: col = %v", m.boardCol)
	}
	m.handleKey(key("l")) // stays
	if m.boardCol != core.ColumnDoneToday {
		t.Fatalf("l past end: col = %v", m.boardCol)
	}
	m.handleKey(key("h"))
	m.handleKey(key("h"))
	m.handleKey(key("h"))
	if m.boardCol != core.ColumnNeedsYou {
		t.Fatalf("h past start: col = %v", m.boardCol)
	}
	m.handleKey(key("l"))
	m.handleKey(key("j"))
	m.handleKey(key("j"))
	m.handleKey(key("j"))
	m.handleKey(key("j"))
	if m.boardRow != 3 {
		t.Errorf("j clamps at last card: row = %d", m.boardRow)
	}
	m.handleKey(key("k"))
	if m.boardRow != 2 {
		t.Errorf("k: row = %d", m.boardRow)
	}
}

func TestBoardKeysActOnTheSelectedCard(t *testing.T) {
	m := testModel(boardOverview())
	m.tab = tabBoard
	m.handleKey(key("l"))
	m.handleKey(key("j")) // the orchestrator
	m.handleKey(key("x"))
	if m.confirm == nil || !strings.Contains(m.confirm.prompt, "Launch ads test") {
		t.Fatalf("x should confirm stopping the selected card, got %+v", m.confirm)
	}
	m.handleKey(key("n")) // swallowed by the confirmation
	m.confirm = nil

	m.handleKey(key("enter"))
	if m.screen != screenDetail {
		t.Fatalf("enter should open details, screen = %v", m.screen)
	}
	assertContains(t, m.viewDetail(), "Launch ads test")
	m.handleKey(key("esc"))
	if m.screen != screenMain {
		t.Fatalf("esc should return to the board")
	}

	for k, want := range map[string]tab{"1": tabInbox, "3": tabRecent} {
		m.tab = tabBoard
		m.handleKey(key(k))
		if m.tab != want {
			t.Errorf("%s should switch tabs", k)
		}
	}
}

func TestBoardSelectionFollowsTheCardAcrossReloads(t *testing.T) {
	m := testModel(boardOverview())
	m.tab = tabBoard
	m.handleKey(key("l"))
	m.handleKey(key("j"))
	o := boardOverview()
	o.Board[core.ColumnNeedsYou] = nil
	m.setOverview(o)
	if v, _ := m.selected(); v.ID != "o1" {
		t.Errorf("selection moved to %q", v.ID)
	}
}
