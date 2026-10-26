package tui

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/matthewalunni/dispatch/internal/core"
	"github.com/matthewalunni/dispatch/internal/herdrx"
	"github.com/matthewalunni/dispatch/internal/store"
)

func TestMain(m *testing.M) {
	// Plain text, so assertions match what a reader sees.
	lipgloss.SetColorProfile(termenv.Ascii)
	os.Exit(m.Run())
}

func task(id, role, project, title string) core.TaskView {
	return core.TaskView{Task: store.Task{
		ID: id, Role: role, ProjectName: project, Title: title,
		CreatedAt: time.Now().Add(-time.Hour),
	}}
}

func decisionAsk() *store.Ask {
	return &store.Ask{
		ID: "ask-1", TaskID: "t1", Kind: store.AskDecision,
		Title: "How should Search Ads get the new screenshots?",
		Body:  "The full reasoning lives here.",
		Options: []store.AskOption{
			{Label: "Custom Product Page", Detail: "faster · short review"},
			{Label: "Ship with 1.0.2", Detail: "changes the main listing"},
		},
		Recommended: 0,
		State:       store.AskOpen,
		CreatedAt:   time.Now().Add(-12 * time.Minute),
	}
}

func choreAsk() *store.Ask {
	return &store.Ask{
		ID: "ask-2", TaskID: "t1", Kind: store.AskChore,
		Title:       "Set up Search Ads account + billing",
		Items:       []string{"Create account", "Add a card"},
		Blocks:      "campaign setup",
		Recommended: -1,
		State:       store.AskOpen,
		CreatedAt:   time.Now().Add(-12 * time.Minute),
	}
}

func testModel(o *core.Overview) *model {
	m := newModel(nil, "test")
	m.width, m.height = 110, 40
	m.overview = o
	m.loading = false
	return m
}

func key(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func assertContains(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
}

func TestInboxRendersADecisionWithItsRecommendation(t *testing.T) {
	m := testModel(&core.Overview{
		Inbox:    []core.InboxItem{{Task: task("t1", "growth", "RepClock", "Launch ads"), Ask: decisionAsk()}},
		NeedsYou: 1,
	})
	out := m.viewInbox()
	assertContains(t, out,
		"How should Search Ads get the new screenshots?",
		"a  ★ Custom Product Page",
		"b  Ship with 1.0.2",
		"growth", "RepClock",
		"faster · short review",
	)
	if strings.Contains(out, "The full reasoning lives here.") {
		t.Errorf("body should stay hidden until enter:\n%s", out)
	}

	m.handleKey(key("enter"))
	assertContains(t, m.viewInbox(), "The full reasoning lives here.")
}

func TestInboxRendersAChoreChecklist(t *testing.T) {
	m := testModel(&core.Overview{
		Inbox: []core.InboxItem{{Task: task("t1", "growth", "RepClock", "Launch ads"), Ask: choreAsk()}},
	})
	assertContains(t, m.viewInbox(), "CHORE", "Set up Search Ads account + billing",
		"☐ Create account", "☐ Add a card", "blocks: campaign setup")
}

func TestInboxFallbackCardShowsScreenTail(t *testing.T) {
	v := task("t2", "engineer", "ReadFlow", "Offline sync")
	v.AgentStatus = herdrx.StatusBlocked
	m := testModel(&core.Overview{
		Inbox: []core.InboxItem{{Task: v, Screen: "line one\nline two\nline three\nDo you want to proceed? (y/n)\n"}},
	})
	out := m.viewInbox()
	assertContains(t, out, "BLOCKED", "Offline sync", "engineer", "ReadFlow", "Do you want to proceed? (y/n)")
	if strings.Contains(out, "line one") {
		t.Errorf("collapsed card should show only the tail:\n%s", out)
	}
	m.handleKey(key("enter"))
	assertContains(t, m.viewInbox(), "line one")
}

func TestInboxShowsTheQuietWorkingList(t *testing.T) {
	busy := task("t3", "engineer", "ReadFlow", "Offline sync")
	busy.AgentStatus = herdrx.StatusWorking
	busy.Report = &store.Report{Line: "adding offline sync tests", Done: 2, Total: 3}
	quiet := task("t4", "designer", "RepClock", "Onboarding variants")
	quiet.AgentStatus = herdrx.StatusIdle
	m := testModel(&core.Overview{Working: []core.TaskView{busy, quiet}, WorkingCount: 1})
	assertContains(t, m.viewInbox(), "nothing needs you", "WORKING",
		"adding offline sync tests · 2 of 3", "Onboarding variants")
}

func TestOptionKeyMapsToIndex(t *testing.T) {
	cases := []struct {
		key   string
		count int
		want  int
	}{
		{"a", 2, 0},
		{"b", 2, 1},
		{"c", 2, -1},
		{"z", 2, -1},
		{"B", 2, -1},
		{"ab", 2, -1},
	}
	for _, tc := range cases {
		if got := optionIndex(tc.key, tc.count); got != tc.want {
			t.Errorf("optionIndex(%q, %d) = %d, want %d", tc.key, tc.count, got, tc.want)
		}
	}
}

func TestInboxKeysBuildAnswers(t *testing.T) {
	decision := core.InboxItem{Ask: decisionAsk()}
	if ans, ok := inboxAnswer(decision, "b"); !ok || ans.Option != 1 || ans.ChoreDone {
		t.Errorf("b on a decision = %+v, %v", ans, ok)
	}
	if _, ok := inboxAnswer(decision, "d"); ok {
		t.Error("d is not an option of a two-option decision")
	}
	chore := core.InboxItem{Ask: choreAsk()}
	if ans, ok := inboxAnswer(chore, "d"); !ok || !ans.ChoreDone || ans.Option != -1 {
		t.Errorf("d on a chore = %+v, %v", ans, ok)
	}
	if _, ok := inboxAnswer(chore, "a"); ok {
		t.Error("a chore has no options")
	}
	if _, ok := inboxAnswer(core.InboxItem{}, "a"); ok {
		t.Error("a fallback card has nothing to answer")
	}
	answered := decisionAsk()
	answered.State = store.AskAnswered
	if _, ok := inboxAnswer(core.InboxItem{Ask: answered}, "a"); ok {
		t.Error("an answered ask cannot be answered again")
	}
}

func TestTabKeysSwitchTabs(t *testing.T) {
	m := testModel(&core.Overview{})
	if m.tab != tabInbox {
		t.Fatalf("initial tab = %v, want Inbox", m.tab)
	}
	for k, want := range map[string]tab{"2": tabBoard, "3": tabRecent, "4": tabRoles, "1": tabInbox} {
		m.handleKey(key(k))
		if m.tab != want {
			t.Errorf("after %q tab = %v, want %v", k, m.tab, want)
		}
	}
	m.tab = tabInbox
	for _, want := range []tab{tabBoard, tabRecent, tabRoles, tabInbox} {
		m.handleKey(key("tab"))
		if m.tab != want {
			t.Errorf("tab cycled to %v, want %v", m.tab, want)
		}
	}
}

func TestTabBarMarksTheActiveTabAndCounts(t *testing.T) {
	bar := renderTabBar(tabBoard, 2, 3, "all projects")
	assertContains(t, bar, "Dispatch", "all projects", "1 Inbox", "[2 Board]", "3 Recent", "4 Roles",
		"● 2 need you", "● 3 working")
	if strings.Contains(bar, "[1 Inbox]") {
		t.Errorf("only the active tab is bracketed: %s", bar)
	}
}

func TestPendingDeliveryIsLabelled(t *testing.T) {
	ask := decisionAsk()
	ask.State = store.AskAnswered
	m := testModel(&core.Overview{
		Inbox: []core.InboxItem{{Task: task("t1", "growth", "RepClock", "Launch ads"), Ask: ask}},
	})
	assertContains(t, m.viewInbox(), "answered · waiting to deliver")
}

func TestAnswerResultBanner(t *testing.T) {
	m := testModel(&core.Overview{})
	m.Update(answeredMsg{askID: "ask-1", result: &core.AnswerResult{Delivered: true}})
	if m.status != "answered · delivered" {
		t.Errorf("status = %q", m.status)
	}
	m.Update(answeredMsg{askID: "ask-1", result: &core.AnswerResult{Reason: "agent is busy"}})
	if m.status != "answered · waiting to deliver (agent is busy)" {
		t.Errorf("status = %q", m.status)
	}

	ask := decisionAsk()
	ask.State = store.AskAnswered
	m.overview = &core.Overview{Inbox: []core.InboxItem{{Task: task("t1", "growth", "RepClock", "x"), Ask: ask}}}
	assertContains(t, m.viewInbox(), "answered · waiting to deliver (agent is busy)")

	m.Update(answeredMsg{askID: "ask-1", err: errors.New("ask ask-1 is answered and cannot be answered")})
	if m.err == nil {
		t.Error("an answer error should surface as the error banner")
	}
}

func TestReplySendsANote(t *testing.T) {
	m := testModel(&core.Overview{
		Inbox: []core.InboxItem{{Task: task("t1", "growth", "RepClock", "Launch ads"), Ask: decisionAsk()}},
	})
	m.handleKey(key("m"))
	if !m.replying {
		t.Fatal("m should open the reply input")
	}
	for _, r := range "use the page" {
		m.handleKey(key(string(r)))
	}
	if m.tab != tabInbox {
		t.Fatal("typing a reply must not switch tabs")
	}
	_, cmd := m.handleKey(key("enter"))
	if m.replying || cmd == nil {
		t.Fatalf("enter should send the reply (replying=%v, cmd=%v)", m.replying, cmd != nil)
	}
	if !m.inFlight["ask-1"] {
		t.Error("the ask should be marked in flight")
	}
}
