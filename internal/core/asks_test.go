package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/matthewalunni/dispatch/internal/herdrx"
	"github.com/matthewalunni/dispatch/internal/store"
)

func decisionAsk() store.Ask {
	return store.Ask{
		Kind: store.AskDecision, Title: "Which cache?",
		Options:     []store.AskOption{{Label: "Redis"}, {Label: "In-memory"}},
		Recommended: 1,
	}
}

func TestFileAskValidates(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	task := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"}).Task

	one := decisionAsk()
	one.Options = one.Options[:1]
	badRec := decisionAsk()
	badRec.Recommended = 2
	negRec := decisionAsk()
	negRec.Recommended = -1
	noTitle := store.Ask{Kind: store.AskChore, Items: []string{"rotate key"}}
	for name, ask := range map[string]store.Ask{"one option": one, "rec high": badRec, "rec negative": negRec, "chore no title": noTitle} {
		if _, err := h.FileAsk(ctx, task.ID, ask); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}

	chore := store.Ask{Kind: store.AskChore, Title: "Rotate key", Items: []string{"a"}, Recommended: 5}
	got, err := h.FileAsk(ctx, task.ID, chore)
	if err != nil {
		t.Fatal(err)
	}
	if got.Recommended != -1 || got.State != store.AskOpen || got.TaskID != task.ID || got.ID == "" || got.CreatedAt.IsZero() {
		t.Errorf("chore = %+v, want normalised open ask", got)
	}

	if err := h.Store().SetStatus(ctx, task.ID, store.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	_, err = h.FileAsk(ctx, task.ID, decisionAsk())
	if err == nil || !strings.Contains(err.Error(), "completed") {
		t.Errorf("err = %v, want mention of completed status", err)
	}
}

func TestAnswerDeliversAndMarksDelivered(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	task := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"}).Task
	ask, err := h.FileAsk(ctx, task.ID, decisionAsk())
	if err != nil {
		t.Fatal(err)
	}
	ans := Answer{Option: 0}
	res, err := h.AnswerAsk(ctx, ask.ID, ans)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Delivered || res.Ask.State != store.AskDelivered {
		t.Fatalf("result = %+v, want delivered", res)
	}
	last := h.Herdr.Prompts[len(h.Herdr.Prompts)-1]
	if last.Target != task.SessionRef() || last.Text != AnswerPrompt(ask, ans) {
		t.Errorf("prompt = %+v", last)
	}
	got, _ := h.Store().GetAsk(ctx, ask.ID)
	if got.State != store.AskDelivered || got.DeliveredAt == nil || got.AnsweredAt == nil {
		t.Errorf("stored ask = %+v", got)
	}
	prompts := len(h.Herdr.Prompts)
	if _, err := h.AnswerAsk(ctx, ask.ID, Answer{Option: 1}); err == nil {
		t.Error("answering twice should fail")
	}
	if len(h.Herdr.Prompts) != prompts {
		t.Error("a second answer must not prompt the agent again")
	}
}

func TestAnswerToABlockedAgentIsHeldAndRedelivered(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	task := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"}).Task
	ask, _ := h.FileAsk(ctx, task.ID, decisionAsk())

	h.Herdr.PromptErrFor = map[string]error{task.SessionRef(): &herdrx.Error{Code: "agent_blocked", Message: "blocked"}}
	before := len(h.Herdr.Prompts)
	res, err := h.AnswerAsk(ctx, ask.ID, Answer{Option: 1, Note: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Delivered || res.Reason == "" || res.Ask.State != store.AskAnswered {
		t.Fatalf("result = %+v, want held", res)
	}
	if len(h.Herdr.Prompts) != before {
		t.Error("no prompt should be recorded")
	}

	h.Herdr.PromptErrFor = nil
	h.Herdr.SetStatus(task.HerdrAgent, herdrx.StatusIdle)
	if _, err := h.ActiveViews(ctx); err != nil {
		t.Fatal(err)
	}
	if len(h.Herdr.Prompts) != before+1 {
		t.Fatalf("prompts = %d, want redelivery", len(h.Herdr.Prompts))
	}
	got, _ := h.Store().GetAsk(ctx, ask.ID)
	if got.State != store.AskDelivered {
		t.Errorf("state = %q, want delivered", got.State)
	}
}

func TestPendingAnswerSkipsABlockedAgent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	task := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"}).Task
	ask, _ := h.FileAsk(ctx, task.ID, decisionAsk())
	h.Herdr.PromptErrFor = map[string]error{task.SessionRef(): errors.New("nope")}
	res, _ := h.AnswerAsk(ctx, ask.ID, Answer{Option: 0})
	if res.Delivered || !strings.Contains(res.Reason, "nope") {
		t.Fatalf("result = %+v, want error text as reason", res)
	}
	h.Herdr.PromptErrFor = nil
	h.Herdr.SetStatus(task.HerdrAgent, herdrx.StatusBlocked)
	before := len(h.Herdr.Prompts)
	if _, err := h.ActiveViews(ctx); err != nil {
		t.Fatal(err)
	}
	if len(h.Herdr.Prompts) != before {
		t.Error("must not prompt a blocked agent")
	}
}

func TestPendingAnswerWaitsWhileHerdrIsUnreachable(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	task := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"}).Task
	ask, _ := h.FileAsk(ctx, task.ID, decisionAsk())
	h.Herdr.PromptErrFor = map[string]error{task.SessionRef(): &herdrx.Error{Code: "agent_blocked"}}
	if _, err := h.AnswerAsk(ctx, ask.ID, Answer{Option: 0}); err != nil {
		t.Fatal(err)
	}
	h.Herdr.PromptErrFor = nil
	h.Herdr.ListErr = errors.New("herdr is down")
	before := len(h.Herdr.Prompts)
	if _, err := h.ActiveViews(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := h.Store().GetAsk(ctx, ask.ID)
	if got.State != store.AskAnswered || len(h.Herdr.Prompts) != before {
		t.Errorf("state = %q prompts = %d, want untouched", got.State, len(h.Herdr.Prompts))
	}
}

func TestAnswerPromptWording(t *testing.T) {
	dec := decisionAsk()
	chore := store.Ask{Kind: store.AskChore, Title: "Rotate key"}
	tests := []struct {
		name string
		ask  store.Ask
		ans  Answer
		want string
	}{
		{"option", dec, Answer{Option: 0}, `[dispatch] Your question "Which cache?" was answered: Redis. Carry on.`},
		{"option and note", dec, Answer{Option: 1, Note: "keep it small"}, `[dispatch] Your question "Which cache?" was answered: In-memory. Note: keep it small Carry on.`},
		{"note only", dec, Answer{Option: -1, Note: "use sqlite"}, `[dispatch] Reply to "Which cache?": use sqlite Carry on.`},
		{"chore", chore, Answer{Option: -1, ChoreDone: true}, `[dispatch] "Rotate key" is done. Carry on.`},
		{"chore note", chore, Answer{Option: -1, ChoreDone: true, Note: "key is in vault."}, `[dispatch] "Rotate key" is done. key is in vault. Carry on.`},
	}
	for _, tt := range tests {
		if got := AnswerPrompt(tt.ask, tt.ans); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestHydrateCarriesReportAndOpenAsks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	task := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"}).Task
	if err := h.Report(ctx, task.ID, "wiring routes", 2, 5); err != nil {
		t.Fatal(err)
	}
	keep, _ := h.FileAsk(ctx, task.ID, decisionAsk())
	gone, _ := h.FileAsk(ctx, task.ID, decisionAsk())
	if err := h.WithdrawAsk(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	views, err := h.ActiveViews(ctx)
	if err != nil || len(views) != 1 {
		t.Fatalf("views = %v err = %v", views, err)
	}
	v := views[0]
	if v.Report == nil || v.Report.Line != "wiring routes" || v.Report.Done != 2 || v.Report.Total != 5 {
		t.Errorf("report = %+v", v.Report)
	}
	if len(v.OpenAsks) != 1 || v.OpenAsks[0].ID != keep.ID {
		t.Errorf("open asks = %+v", v.OpenAsks)
	}
}

func TestWithdrawnAskCannotBeAnswered(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	task := h.dispatch(t, DispatchRequest{Role: "engineer", Title: "Add the endpoint"}).Task
	ask, _ := h.FileAsk(ctx, task.ID, decisionAsk())
	if err := h.WithdrawAsk(ctx, ask.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.AnswerAsk(ctx, ask.ID, Answer{Option: 0}); err == nil {
		t.Error("want an error answering a withdrawn ask")
	}
	if err := h.WithdrawAsk(ctx, ask.ID); err == nil {
		t.Error("want an error withdrawing twice")
	}
}
