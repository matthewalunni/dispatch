package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/matthewalunni/dispatch/internal/core"
	"github.com/matthewalunni/dispatch/internal/store"
)

// screenLineCount is how many terminal lines Bubble Tea draws for a view.
func screenLineCount(view string) int { return len(strings.Split(view, "\n")) }

func manyAsks(n int) *core.Overview {
	o := &core.Overview{NeedsYou: n}
	for i := 0; i < n; i++ {
		ask := decisionAsk()
		ask.ID = fmt.Sprintf("ask-%02d", i)
		ask.Title = fmt.Sprintf("Question number %02d", i)
		ask.CreatedAt = time.Now().Add(-time.Duration(i) * time.Minute)
		o.Inbox = append(o.Inbox, core.InboxItem{Task: task("t1", "growth", "RepClock", "Launch ads"), Ask: ask})
	}
	return o
}

func TestInboxScrollsToKeepTheSelectionOnScreen(t *testing.T) {
	m := testModel(manyAsks(20))
	m.height = 16
	for _, cursor := range []int{0, 9, 19} {
		m.inboxCursor = cursor
		out := m.View()
		if got := screenLineCount(out); got > m.height {
			t.Errorf("cursor %d: view is %d lines, want at most %d:\n%s", cursor, got, m.height, out)
		}
		assertContains(t, out, "[1 Inbox]", "20 need you", fmt.Sprintf("Question number %02d", cursor), "j/k move")
	}
}

func TestInboxShowsTheExpandedSelectionWhenScrolled(t *testing.T) {
	m := testModel(manyAsks(20))
	m.height = 18
	m.inboxCursor = 15
	m.expanded = true
	out := m.View()
	if got := screenLineCount(out); got > m.height {
		t.Errorf("view is %d lines, want at most %d", got, m.height)
	}
	assertContains(t, out, "[1 Inbox]", "Question number 15")
}

func manyCards(n int) *core.Overview {
	o := &core.Overview{Board: map[core.Column][]core.TaskView{}}
	for i := 0; i < n; i++ {
		v := task(fmt.Sprintf("w%02d", i), "engineer", "ReadFlow", fmt.Sprintf("Card number %02d", i))
		v.Report = &store.Report{Line: fmt.Sprintf("Card number %02d", i)}
		o.Board[core.ColumnWorking] = append(o.Board[core.ColumnWorking], v)
	}
	return o
}

func TestBoardScrollsToKeepTheSelectedCardOnScreen(t *testing.T) {
	for _, width := range []int{80, 120} {
		m := testModel(manyCards(12))
		m.tab = tabBoard
		m.width, m.height = width, 20
		m.boardCol, m.boardRow = core.ColumnWorking, 10
		out := m.View()
		if got := screenLineCount(out); got > m.height {
			t.Errorf("width %d: view is %d lines, want at most %d:\n%s", width, got, m.height, out)
		}
		assertContains(t, out, "[2 Board]", "Card number 10", "h/l column")
	}
}
