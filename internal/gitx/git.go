// Package gitx wraps the git CLI behind a small injectable interface.
//
// Git owns branches, worktrees and repository state; dispatch only asks it
// questions and asks it to create or remove worktrees.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Worktree is one entry from `git worktree list`.
type Worktree struct {
	Path   string
	Branch string
	Head   string
	Bare   bool
}

// Git is the repository surface dispatch depends on.
type Git interface {
	// Available reports the installed git version.
	Available() (string, error)
	// RepoRoot returns the working tree root containing dir.
	RepoRoot(dir string) (string, error)
	// CurrentBranch returns the checked-out branch, or "" when detached.
	CurrentBranch(dir string) (string, error)
	// IsDirty reports uncommitted changes (tracked or untracked).
	IsDirty(dir string) (bool, error)
	// HasBranch reports whether a local branch already exists.
	HasBranch(dir, branch string) (bool, error)
	// DefaultBase returns a sensible base ref for new branches.
	DefaultBase(dir string) (string, error)
	// ListWorktrees enumerates the repository's worktrees.
	ListWorktrees(dir string) ([]Worktree, error)
	// AddWorktree creates a worktree at path on a new branch from base.
	AddWorktree(ctx context.Context, repo, path, branch, base string) error
	// RemoveWorktree removes a worktree checkout.
	RemoveWorktree(ctx context.Context, repo, path string, force bool) error
}

// ErrNotARepository is returned when a directory is not inside a git worktree.
var ErrNotARepository = errors.New("not a git repository")

// ErrGitMissing is returned when the git binary cannot be found.
var ErrGitMissing = errors.New("git is not installed or not on PATH")

// CLI is the real implementation, shelling out to git.
type CLI struct {
	// Binary defaults to "git".
	Binary string
}

// New returns a CLI git adapter.
func New() *CLI { return &CLI{Binary: "git"} }

func (c *CLI) bin() string {
	if c.Binary == "" {
		return "git"
	}
	return c.Binary
}

func (c *CLI) run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if isMissingBinary(err) {
			return "", ErrGitMissing
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return strings.TrimSpace(stdout.String()), &CommandError{Args: args, Stderr: msg, Err: err}
	}
	return strings.TrimSpace(stdout.String()), nil
}

// isMissingBinary reports an unrunnable git binary. A name resolved through
// PATH fails as *exec.Error; an absolute path fails as a plain not-exist
// error, and both mean the same thing to the user.
func isMissingBinary(err error) bool {
	if err == nil {
		return false
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return true
	}
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrPermission)
}

// CommandError carries git's own stderr so dispatch can show a real reason.
type CommandError struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), e.Stderr)
}

func (e *CommandError) Unwrap() error { return e.Err }

func (c *CLI) Available() (string, error) {
	out, err := c.run(context.Background(), "", "--version")
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(out, "git version "), nil
}

func (c *CLI) RepoRoot(dir string) (string, error) {
	out, err := c.run(context.Background(), dir, "rev-parse", "--show-toplevel")
	if err != nil {
		if errors.Is(err, ErrGitMissing) {
			return "", err
		}
		return "", ErrNotARepository
	}
	if out == "" {
		return "", ErrNotARepository
	}
	return filepath.Clean(out), nil
}

func (c *CLI) CurrentBranch(dir string) (string, error) {
	out, err := c.run(context.Background(), dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		// A detached HEAD makes symbolic-ref exit non-zero; that is not a
		// failure, it just means there is no branch name.
		return "", nil
	}
	return out, nil
}

func (c *CLI) IsDirty(dir string) (bool, error) {
	out, err := c.run(context.Background(), dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

func (c *CLI) HasBranch(dir, branch string) (bool, error) {
	_, err := c.run(context.Background(), dir, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrGitMissing) {
		return false, err
	}
	return false, nil
}

// DefaultBase prefers the current branch, falling back to HEAD when detached.
func (c *CLI) DefaultBase(dir string) (string, error) {
	branch, err := c.CurrentBranch(dir)
	if err != nil {
		return "", err
	}
	if branch != "" {
		return branch, nil
	}
	out, err := c.run(context.Background(), dir, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", fmt.Errorf("repository has no commits yet: %w", err)
	}
	return out, nil
}

func (c *CLI) ListWorktrees(dir string) ([]Worktree, error) {
	out, err := c.run(context.Background(), dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return ParseWorktreeList(out), nil
}

// ParseWorktreeList parses `git worktree list --porcelain` output.
func ParseWorktreeList(out string) []Worktree {
	var result []Worktree
	var current Worktree
	flush := func() {
		if current.Path != "" {
			result = append(result, current)
		}
		current = Worktree{}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			flush()
			current.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "HEAD "):
			current.Head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			current.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "bare":
			current.Bare = true
		}
	}
	flush()
	return result
}

func (c *CLI) AddWorktree(ctx context.Context, repo, path, branch, base string) error {
	args := []string{"worktree", "add", "-b", branch, path}
	if base != "" {
		args = append(args, base)
	}
	_, err := c.run(ctx, repo, args...)
	return err
}

func (c *CLI) RemoveWorktree(ctx context.Context, repo, path string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	_, err := c.run(ctx, repo, args...)
	return err
}

var _ Git = (*CLI)(nil)
