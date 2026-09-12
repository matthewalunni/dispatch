package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/matthewalunni/dispatch/internal/roles"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedTask(t *testing.T, s *Store, id, slug string, opts ...func(*Task)) Task {
	t.Helper()
	task := Task{
		ID:          id,
		Title:       slug,
		Slug:        slug,
		ProjectRoot: "/src/proj",
		ProjectName: "proj",
		Role:        "engineer",
		Runtime:     "claude",
		Isolation:   roles.IsolationWorktree,
		Status:      StatusRunning,
		HerdrAgent:  slug,
		CreatedAt:   time.Now().UTC(),
	}
	for _, opt := range opts {
		opt(&task)
	}
	if err := s.Create(context.Background(), &task); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return task
}

func TestCreateAndGetRoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	completed := time.Now().UTC().Truncate(time.Second)

	original := seedTask(t, s, "task_1", "fix-login", func(task *Task) {
		task.Description = "the login form rejects valid emails"
		task.Branch = "dispatch/fix-login"
		task.Worktree = "/wt/fix-login"
		task.BaseRef = "main"
		task.HerdrPaneID = "w1:p2"
		task.HerdrTabID = "w1:t2"
		task.HerdrWorkspaceID = "w1"
		task.ParentTaskID = "task_parent"
		task.CompletedAt = &completed
	})

	got, err := s.Get(ctx, original.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != original.Title || got.Description != original.Description {
		t.Errorf("title/description not round-tripped: %+v", got)
	}
	if got.Branch != "dispatch/fix-login" || got.Worktree != "/wt/fix-login" || got.BaseRef != "main" {
		t.Errorf("git fields not round-tripped: %+v", got)
	}
	if got.HerdrPaneID != "w1:p2" || got.HerdrTabID != "w1:t2" || got.HerdrWorkspaceID != "w1" {
		t.Errorf("herdr handles not round-tripped: %+v", got)
	}
	if got.ParentTaskID != "task_parent" {
		t.Errorf("ParentTaskID = %q", got.ParentTaskID)
	}
	if got.Isolation != roles.IsolationWorktree {
		t.Errorf("Isolation = %q", got.Isolation)
	}
	if got.CompletedAt == nil || !got.CompletedAt.Equal(completed) {
		t.Errorf("CompletedAt = %v, want %v", got.CompletedAt, completed)
	}
}

func TestGetMissingReturnsErrNotFound(t *testing.T) {
	s := newStore(t)
	if _, err := s.Get(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestListFiltersByStatusAndProject(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	seedTask(t, s, "task_1", "live-one")
	seedTask(t, s, "task_2", "done-one", func(task *Task) { task.Status = StatusCompleted })
	seedTask(t, s, "task_3", "other-project", func(task *Task) { task.ProjectRoot = "/src/other" })

	active, err := s.List(ctx, ActiveFilter())
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 {
		t.Errorf("active = %d, want 2 (completed excluded)", len(active))
	}

	all, err := s.List(ctx, Filter{IncludeTerminal: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("all = %d, want 3", len(all))
	}

	scoped, err := s.List(ctx, Filter{ProjectRoot: "/src/proj", IncludeTerminal: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 2 {
		t.Errorf("project-scoped = %d, want 2", len(scoped))
	}
}

func TestListIsNewestFirst(t *testing.T) {
	s := newStore(t)
	base := time.Now().UTC()
	seedTask(t, s, "task_old", "old", func(task *Task) { task.CreatedAt = base.Add(-time.Hour) })
	seedTask(t, s, "task_new", "new", func(task *Task) { task.CreatedAt = base })

	tasks, err := s.List(context.Background(), ActiveFilter())
	if err != nil {
		t.Fatal(err)
	}
	if tasks[0].ID != "task_new" {
		t.Errorf("first = %q, want the newest task", tasks[0].ID)
	}
}

func TestChildrenPreserveLineage(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	parent := seedTask(t, s, "task_parent", "plan-onboarding", func(task *Task) { task.Role = "designer" })
	seedTask(t, s, "task_child_a", "build-screens", func(task *Task) { task.ParentTaskID = parent.ID })
	seedTask(t, s, "task_child_b", "write-copy", func(task *Task) { task.ParentTaskID = parent.ID })
	seedTask(t, s, "task_unrelated", "unrelated")

	children, err := s.Children(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 2 {
		t.Fatalf("children = %d, want 2", len(children))
	}
	for _, child := range children {
		if child.ParentTaskID != parent.ID {
			t.Errorf("child %q has parent %q", child.ID, child.ParentTaskID)
		}
	}
}

func TestResolveByIdSlugAgentAndPrefix(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	task := seedTask(t, s, "task_20260912T101500_abcd1234", "fix-login", func(t2 *Task) {
		t2.HerdrAgent = "fix-login-agent"
	})

	for _, ref := range []string{
		task.ID,                        // full id
		"fix-login",                    // exact slug
		"fix-login-agent",              // herdr agent name
		"fix-lo",                       // slug prefix
		"abcd1234",                     // id suffix
		"task_20260912T101500_abcd123", // id prefix
	} {
		got, err := s.Resolve(ctx, ref)
		if err != nil {
			t.Errorf("Resolve(%q): %v", ref, err)
			continue
		}
		if got.ID != task.ID {
			t.Errorf("Resolve(%q) = %q, want %q", ref, got.ID, task.ID)
		}
	}
}

func TestResolveReportsAmbiguity(t *testing.T) {
	s := newStore(t)
	seedTask(t, s, "task_1", "implement-login")
	seedTask(t, s, "task_2", "implement-logout")

	_, err := s.Resolve(context.Background(), "implement-log")
	if err == nil {
		t.Fatal("expected an ambiguity error")
	}
	var ambiguous *AmbiguousError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("err = %v, want AmbiguousError", err)
	}
	if len(ambiguous.Matches) != 2 {
		t.Errorf("Matches = %d, want 2", len(ambiguous.Matches))
	}
}

func TestResolvePrefersTheOnlyLiveMatch(t *testing.T) {
	s := newStore(t)
	// A finished task must not make its name ambiguous forever.
	seedTask(t, s, "task_old", "fix-login", func(task *Task) { task.Status = StatusStopped })
	seedTask(t, s, "task_live", "fix-login")

	got, err := s.Resolve(context.Background(), "fix-login")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ID != "task_live" {
		t.Errorf("Resolve = %q, want the live task", got.ID)
	}
}

func TestResolveUnknownReturnsErrNotFound(t *testing.T) {
	s := newStore(t)
	if _, err := s.Resolve(context.Background(), "nothing-like-this"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if _, err := s.Resolve(context.Background(), ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty ref err = %v, want ErrNotFound", err)
	}
}

func TestUpdateAndSetStatus(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	task := seedTask(t, s, "task_1", "fix-login")

	task.Title = "Fix login properly"
	task.Status = StatusStopped
	if err := s.Update(ctx, &task); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, task.ID)
	if got.Title != "Fix login properly" || got.Status != StatusStopped {
		t.Errorf("update not persisted: %+v", got)
	}

	if err := s.SetStatus(ctx, task.ID, StatusCompleted); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, task.ID)
	if got.Status != StatusCompleted {
		t.Errorf("Status = %q", got.Status)
	}
	if got.CompletedAt == nil {
		t.Error("SetStatus should stamp completed_at for a terminal status")
	}
}

func TestUpdateMissingTaskReturnsErrNotFound(t *testing.T) {
	s := newStore(t)
	task := Task{ID: "ghost", CreatedAt: time.Now()}
	if err := s.Update(context.Background(), &task); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestCount(t *testing.T) {
	s := newStore(t)
	seedTask(t, s, "task_1", "a")
	seedTask(t, s, "task_2", "b", func(task *Task) { task.Status = StatusCompleted })
	seedTask(t, s, "task_3", "c", func(task *Task) { task.Status = StatusStopped })
	seedTask(t, s, "task_4", "d", func(task *Task) { task.Status = StatusFailed })

	counts, err := s.Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts.Total != 4 || counts.Active != 1 || counts.Done != 1 || counts.Stopped != 1 || counts.Failed != 1 {
		t.Errorf("counts = %+v", counts)
	}
}

func TestMigrationsAreIdempotentAcrossOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dispatch.db")

	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seedTask(t, first, "task_1", "fix-login")
	first.Close()

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	if _, err := second.Get(context.Background(), "task_1"); err != nil {
		t.Errorf("data lost across reopen: %v", err)
	}
}

func TestHumanDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{3 * time.Hour, "3h"},
		{50 * time.Hour, "2d"},
		{-time.Second, "0s"},
	}
	for _, tc := range cases {
		if got := HumanDuration(tc.d); got != tc.want {
			t.Errorf("HumanDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestStatusTerminal(t *testing.T) {
	for _, status := range []Status{StatusStopped, StatusCompleted, StatusFailed} {
		if !status.Terminal() {
			t.Errorf("%q should be terminal", status)
		}
	}
	for _, status := range []Status{StatusPending, StatusRunning} {
		if status.Terminal() {
			t.Errorf("%q should not be terminal", status)
		}
	}
}
