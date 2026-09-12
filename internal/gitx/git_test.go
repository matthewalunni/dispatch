package gitx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseWorktreeList(t *testing.T) {
	out := `worktree /src/repvault
HEAD a906f10bd27a2f7f0b5c9d8e
branch refs/heads/main

worktree /home/me/.local/share/dispatch/worktrees/repvault/fix-login
HEAD a906f10bd27a2f7f0b5c9d8e
branch refs/heads/dispatch/fix-login

worktree /src/bare
bare
`
	got := ParseWorktreeList(out)
	if len(got) != 3 {
		t.Fatalf("parsed %d worktrees, want 3: %+v", len(got), got)
	}
	if got[0].Path != "/src/repvault" || got[0].Branch != "main" {
		t.Errorf("main worktree = %+v", got[0])
	}
	if got[1].Branch != "dispatch/fix-login" {
		t.Errorf("branch not stripped of refs/heads/: %q", got[1].Branch)
	}
	if !got[2].Bare {
		t.Errorf("bare worktree not flagged: %+v", got[2])
	}
}

func TestParseWorktreeListEmpty(t *testing.T) {
	if got := ParseWorktreeList(""); len(got) != 0 {
		t.Errorf("got %+v, want nothing", got)
	}
}

// gitAvailable skips integration tests where git is not installed.
func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// newRepo creates a real repository with one commit.
func newRepo(t *testing.T) string {
	t.Helper()
	gitAvailable(t)
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "initial")
	return dir
}

func TestRepoRootFromSubdirectory(t *testing.T) {
	repo := newRepo(t)
	sub := filepath.Join(repo, "packages", "ui")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	root, err := New().RepoRoot(sub)
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	// macOS resolves TempDir through /private, so compare resolved paths.
	wantResolved, _ := filepath.EvalSymlinks(repo)
	gotResolved, _ := filepath.EvalSymlinks(root)
	if gotResolved != wantResolved {
		t.Errorf("RepoRoot = %q, want %q", root, repo)
	}
}

func TestRepoRootOutsideARepository(t *testing.T) {
	gitAvailable(t)
	_, err := New().RepoRoot(t.TempDir())
	if !errors.Is(err, ErrNotARepository) {
		t.Errorf("err = %v, want ErrNotARepository", err)
	}
}

func TestCurrentBranchAndDetachedHead(t *testing.T) {
	repo := newRepo(t)
	git := New()

	branch, err := git.CurrentBranch(repo)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "main" {
		t.Errorf("CurrentBranch = %q", branch)
	}

	// Detaching must report "no branch" rather than failing.
	head, err := git.run(context.Background(), repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := git.run(context.Background(), repo, "checkout", "-q", "--detach", head); err != nil {
		t.Fatal(err)
	}
	branch, err = git.CurrentBranch(repo)
	if err != nil {
		t.Fatalf("CurrentBranch on a detached HEAD should not error: %v", err)
	}
	if branch != "" {
		t.Errorf("CurrentBranch = %q, want empty on a detached HEAD", branch)
	}
}

func TestIsDirty(t *testing.T) {
	repo := newRepo(t)
	git := New()

	dirty, err := git.IsDirty(repo)
	if err != nil {
		t.Fatal(err)
	}
	if dirty {
		t.Error("a fresh checkout should be clean")
	}

	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty, err = git.IsDirty(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !dirty {
		t.Error("an untracked file should count as dirty")
	}
}

func TestHasBranch(t *testing.T) {
	repo := newRepo(t)
	git := New()

	if ok, err := git.HasBranch(repo, "main"); err != nil || !ok {
		t.Errorf("HasBranch(main) = %v, %v", ok, err)
	}
	if ok, err := git.HasBranch(repo, "no-such-branch"); err != nil || ok {
		t.Errorf("HasBranch(missing) = %v, %v", ok, err)
	}
}

func TestAddAndRemoveWorktree(t *testing.T) {
	repo := newRepo(t)
	git := New()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fix-login")

	if err := git.AddWorktree(ctx, repo, path, "dispatch/fix-login", "main"); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "README.md")); err != nil {
		t.Errorf("worktree not checked out: %v", err)
	}

	worktrees, err := git.ListWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, wt := range worktrees {
		if wt.Branch == "dispatch/fix-login" {
			found = true
		}
	}
	if !found {
		t.Errorf("new worktree not listed: %+v", worktrees)
	}

	if err := git.RemoveWorktree(ctx, repo, path, false); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("worktree directory still present after removal")
	}
}

func TestAddWorktreeOnAnExistingBranchFailsWithGitsReason(t *testing.T) {
	repo := newRepo(t)
	git := New()
	path := filepath.Join(t.TempDir(), "dup")

	err := git.AddWorktree(context.Background(), repo, path, "main", "main")
	if err == nil {
		t.Fatal("expected a failure creating a branch that already exists")
	}
	var cmdErr *CommandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("err = %v, want a CommandError carrying git's stderr", err)
	}
	if cmdErr.Stderr == "" {
		t.Error("git's own explanation was dropped")
	}
}

func TestDefaultBase(t *testing.T) {
	repo := newRepo(t)
	base, err := New().DefaultBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	if base != "main" {
		t.Errorf("DefaultBase = %q, want the checked-out branch", base)
	}
}

func TestMissingGitBinaryIsReported(t *testing.T) {
	git := &CLI{Binary: filepath.Join(t.TempDir(), "no-such-git")}
	if _, err := git.Available(); !errors.Is(err, ErrGitMissing) {
		t.Errorf("err = %v, want ErrGitMissing", err)
	}
}

func TestAvailableReportsAVersion(t *testing.T) {
	gitAvailable(t)
	version, err := New().Available()
	if err != nil {
		t.Fatal(err)
	}
	if version == "" || strings.HasPrefix(version, "git version") {
		t.Errorf("Available = %q, want a bare version number", version)
	}
}
