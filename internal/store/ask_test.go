package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestAskRoundTrips(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	in := Ask{
		ID: "ask_1", TaskID: "task_1", Kind: AskDecision, Title: "Pick a db", Body: "Which one?",
		Options:     []AskOption{{Label: "sqlite", Detail: "simple"}, {Label: "postgres", Detail: "heavy"}},
		Recommended: 0, Blocks: "migration",
		State: AskOpen, CreatedAt: time.Date(2026, 9, 12, 10, 15, 0, 123, time.UTC),
	}
	if err := s.CreateAsk(ctx, &in); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAsk(ctx, "ask_1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, in)
	}
	if _, err := s.GetAsk(ctx, "nope"); err != ErrNotFound {
		t.Fatalf("GetAsk missing = %v, want ErrNotFound", err)
	}
}

func TestChoreAskRoundTrips(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	in := Ask{ID: "ask_c", TaskID: "task_1", Kind: AskChore, Title: "Do it", Items: []string{"a", "b"},
		Recommended: -1, State: AskOpen, CreatedAt: time.Date(2026, 9, 12, 10, 15, 0, 0, time.UTC)}
	if err := s.CreateAsk(ctx, &in); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAsk(ctx, "ask_c")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, in)
	}
}

func TestListAsksFiltersByStateAndTask(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	for i, a := range []Ask{
		{ID: "ask_1", TaskID: "t1", State: AskOpen},
		{ID: "ask_2", TaskID: "t2", State: AskOpen},
		{ID: "ask_3", TaskID: "t2", State: AskOpen},
	} {
		a.Kind, a.Title, a.Recommended = AskDecision, "x", 0
		a.CreatedAt = base.Add(time.Duration(i) * time.Minute)
		if err := s.CreateAsk(ctx, &a); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetAskState(ctx, "ask_3", AskWithdrawn, ""); err != nil {
		t.Fatal(err)
	}
	open, err := s.ListAsks(ctx, AskFilter{States: []AskState{AskOpen}})
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 2 || open[0].ID != "ask_2" || open[1].ID != "ask_1" {
		t.Fatalf("open asks = %+v, want ask_2 then ask_1", open)
	}
	narrow, err := s.ListAsks(ctx, AskFilter{TaskIDs: []string{"t2"}, States: []AskState{AskOpen}})
	if err != nil {
		t.Fatal(err)
	}
	if len(narrow) != 1 || narrow[0].ID != "ask_2" {
		t.Fatalf("t2 open asks = %+v, want ask_2", narrow)
	}
	all, err := s.ListAsks(ctx, AskFilter{})
	if err != nil || len(all) != 3 {
		t.Fatalf("all asks = %d, %v; want 3", len(all), err)
	}
}

func TestSetAskStateStampsTimes(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a := Ask{ID: "ask_1", TaskID: "t1", Kind: AskDecision, Title: "x", State: AskOpen}
	if err := s.CreateAsk(ctx, &a); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAskState(ctx, "ask_1", AskAnswered, "sqlite"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetAsk(ctx, "ask_1")
	if got.State != AskAnswered || got.Answer != "sqlite" || got.AnsweredAt == nil || got.DeliveredAt != nil {
		t.Fatalf("after answer: %+v", got)
	}
	if err := s.SetAskState(ctx, "ask_1", AskDelivered, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAsk(ctx, "ask_1")
	if got.State != AskDelivered || got.Answer != "sqlite" || got.AnsweredAt == nil || got.DeliveredAt == nil {
		t.Fatalf("after delivery: %+v", got)
	}
	if err := s.SetAskState(ctx, "missing", AskWithdrawn, ""); err != ErrNotFound {
		t.Fatalf("SetAskState missing = %v, want ErrNotFound", err)
	}
}

func TestSetReportUpserts(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.SetReport(ctx, Report{TaskID: "t1", Line: "first", Done: 1, Total: 5}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetReport(ctx, Report{TaskID: "t1", Line: "second", Done: 3, Total: 5}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Reports(ctx, []string{"t1", "t2"})
	if err != nil {
		t.Fatal(err)
	}
	r, ok := got["t1"]
	if len(got) != 1 || !ok || r.Line != "second" || r.Done != 3 || r.Total != 5 || r.UpdatedAt.IsZero() {
		t.Fatalf("reports = %+v", got)
	}
	if empty, err := s.Reports(ctx, nil); err != nil || len(empty) != 0 {
		t.Fatalf("Reports(nil) = %v, %v", empty, err)
	}
}

func TestMigrationIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dispatch.db")
	for i := 0; i < 2; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("open #%d: %v", i+1, err)
		}
		s.Close()
	}
}

func TestTransitionAskIsConditional(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	in := Ask{ID: "ask_1", TaskID: "task_1", Kind: AskDecision, Title: "t", State: AskOpen,
		CreatedAt: time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)}
	if err := s.CreateAsk(ctx, &in); err != nil {
		t.Fatal(err)
	}
	if err := s.TransitionAsk(ctx, "ask_1", AskOpen, AskAnswered, "first"); err != nil {
		t.Fatal(err)
	}
	if err := s.TransitionAsk(ctx, "ask_1", AskOpen, AskAnswered, "second"); !errors.Is(err, ErrAskState) {
		t.Fatalf("second transition = %v, want ErrAskState", err)
	}
	got, _ := s.GetAsk(ctx, "ask_1")
	if got.Answer != "first" || got.AnsweredAt == nil {
		t.Errorf("ask = %+v, want first answer preserved", got)
	}
	if err := s.TransitionAsk(ctx, "ask_1", AskAnswered, AskDelivered, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAsk(ctx, "ask_1")
	if got.State != AskDelivered || got.DeliveredAt == nil || got.Answer != "first" {
		t.Errorf("ask = %+v, want delivered with answer kept", got)
	}
	if err := s.TransitionAsk(ctx, "missing", AskOpen, AskAnswered, "x"); err != ErrNotFound {
		t.Fatalf("missing = %v, want ErrNotFound", err)
	}
}
