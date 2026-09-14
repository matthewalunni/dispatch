package tui

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/matthewalunni/dispatch/internal/core"
	"github.com/matthewalunni/dispatch/internal/fakes"
	"github.com/matthewalunni/dispatch/internal/roles"
	"github.com/matthewalunni/dispatch/internal/store"
)

// tuiHarness wires a real App onto temp directories and in-memory git and
// herdr, so key handling can be exercised without a terminal or a server.
type tuiHarness struct {
	app   *core.App
	herdr *fakes.Herdr
	repo  string
}

func newTUIHarness(t *testing.T) *tuiHarness {
	t.Helper()

	configDir, dataDir, repo := t.TempDir(), t.TempDir(), t.TempDir()

	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	rolesDir := filepath.Join(configDir, "roles")
	if err := os.MkdirAll(rolesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := roles.Seed(rolesDir); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(configFile, []byte(
		"default_role: general\nworktrees_dir: "+filepath.Join(dataDir, "worktrees")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	herdr := fakes.NewHerdr()
	app, err := core.New(core.Options{
		ConfigFile: configFile,
		RolesDir:   rolesDir,
		DBPath:     filepath.Join(dataDir, "dispatch.db"),
		DataDir:    dataDir,
		Git:        fakes.NewGit(repo),
		Herdr:      herdr,
		SkipSeed:   true,
		Now:        func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	t.Cleanup(func() { app.Close() })

	return &tuiHarness{app: app, herdr: herdr, repo: repo}
}

// onTaskList returns a model sitting on the task list with one dispatched
// task under the cursor.
func (h *tuiHarness) onTaskList(t *testing.T, title string) *model {
	t.Helper()
	ctx := context.Background()
	if _, err := h.app.Dispatch(ctx, core.DispatchRequest{
		Dir: h.repo, Role: "general", Title: title,
	}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	tasks, err := h.app.Tasks(ctx, store.Filter{IncludeTerminal: true})
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(h.app, "test")
	m.screen = screenTasks
	m.views = h.app.Hydrate(ctx, tasks)
	m.loading = false
	return m
}

// press sends one key and runs whatever command it produced, returning the
// resulting message.
func press(t *testing.T, m *model, key rune) tea.Msg {
	t.Helper()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
	if cmd == nil {
		return nil
	}
	return cmd()
}

func TestPressingDoneCompletesTheSelectedTask(t *testing.T) {
	h := newTUIHarness(t)
	m := h.onTaskList(t, "Ship the onboarding revamp")
	id := m.views[0].ID

	msg, ok := press(t, m, 'd').(completedMsg)
	if !ok {
		t.Fatal("d produced no completion")
	}
	if msg.err != nil {
		t.Fatalf("complete: %v", msg.err)
	}
	m.Update(msg)

	task, err := h.app.ResolveTask(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != store.StatusCompleted {
		t.Errorf("Status = %q, want %q", task.Status, store.StatusCompleted)
	}
	// Completing is bookkeeping: the conversation must survive it.
	if h.herdr.AgentCount() != 1 {
		t.Error("marking a task done must not stop its herdr session")
	}
	if m.status == "" {
		t.Error("the completion was not reported to the user")
	}
}

func TestPressingDoneOnAFinishedTaskChangesNothing(t *testing.T) {
	h := newTUIHarness(t)
	ctx := context.Background()
	m := h.onTaskList(t, "Already closed out")
	if _, err := h.app.Stop(ctx, m.views[0].ID, core.StopOptions{}); err != nil {
		t.Fatal(err)
	}
	tasks, err := h.app.Tasks(ctx, store.Filter{IncludeTerminal: true})
	if err != nil {
		t.Fatal(err)
	}
	m.views = h.app.Hydrate(ctx, tasks)

	if msg := press(t, m, 'd'); msg != nil {
		t.Fatalf("d on a stopped task did something: %#v", msg)
	}
	task, err := h.app.ResolveTask(ctx, m.views[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != store.StatusStopped {
		t.Errorf("Status = %q, want it left at %q", task.Status, store.StatusStopped)
	}
}

func TestCompletingFromTheDetailScreenReturnsToTheList(t *testing.T) {
	h := newTUIHarness(t)
	m := h.onTaskList(t, "Prototype the first-run screen")
	m.screen = screenDetail

	if _, ok := press(t, m, 'd').(completedMsg); !ok {
		t.Fatal("d produced no completion")
	}
	if m.screen != screenTasks {
		t.Error("detail screen kept showing a task that left the list")
	}
}
