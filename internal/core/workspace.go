package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/matthewalunni/dispatch/internal/gitx"
	"github.com/matthewalunni/dispatch/internal/naming"
	"github.com/matthewalunni/dispatch/internal/roles"
)

// Workspace is the prepared place an agent will run.
type Workspace struct {
	// Dir is the agent's working directory.
	Dir string
	// Slug is the task slug actually used. Preparing a workspace can shift it
	// (a branch, directory or earlier task may already hold the obvious one),
	// and the branch, worktree, agent name and `dispatch open` reference all
	// have to agree on the result.
	Slug string
	// Worktree is set when dispatch created a git worktree.
	Worktree string
	// Branch is the branch checked out in that worktree.
	Branch string
	// BaseRef is what the branch was created from.
	BaseRef string
	// Created records whether dispatch created something it may need to undo.
	Created bool
}

// UserError is an error with an actionable suggestion attached. Dispatch is a
// developer tool: "failed" is never an acceptable message.
type UserError struct {
	Summary string
	Reason  string
	Hints   []string
	Err     error
}

func (e *UserError) Error() string {
	var b strings.Builder
	b.WriteString(e.Summary)
	if e.Reason != "" {
		b.WriteString("\n")
		b.WriteString(e.Reason)
	}
	if len(e.Hints) > 0 {
		b.WriteString("\nTry:")
		for _, hint := range e.Hints {
			b.WriteString("\n  ")
			b.WriteString(hint)
		}
	}
	return b.String()
}

func (e *UserError) Unwrap() error { return e.Err }

// slugTaken reports whether a task slug is already spoken for by a live task.
type slugTaken func(string) bool

// prepareWorkspace builds the environment a task's isolation mode requires.
func (a *App) prepareWorkspace(ctx context.Context, rc *Context, isolation roles.IsolationMode, slug, branchOverride, baseOverride string, taken slugTaken) (Workspace, error) {
	switch isolation {
	case roles.IsolationNone:
		return Workspace{Dir: rc.Project.Root, Slug: nextFreeSlug(slug, taken)}, nil
	case roles.IsolationWorktree:
		return a.prepareWorktree(ctx, rc, slug, branchOverride, baseOverride, taken)
	default:
		return Workspace{}, &UserError{
			Summary: fmt.Sprintf("Unsupported isolation mode %q.", isolation),
			Reason:  "This build of dispatch understands: " + roles.JoinIsolationModes() + ".",
		}
	}
}

func (a *App) prepareWorktree(ctx context.Context, rc *Context, slug, branchOverride, baseOverride string, taken slugTaken) (Workspace, error) {
	proj := rc.Project

	if !proj.IsGit {
		return Workspace{}, &UserError{
			Summary: "Cannot create a worktree: this directory is not a git repository.",
			Reason:  fmt.Sprintf("%s has no git working tree.", proj.Root),
			Hints: []string{
				"git init                        # make it a repository",
				"dispatch run ... --isolation none   # run the agent here instead",
			},
		}
	}

	base := strings.TrimSpace(baseOverride)
	if base == "" {
		resolved, err := a.git.DefaultBase(proj.Root)
		if err != nil {
			return Workspace{}, &UserError{
				Summary: "Cannot determine a base revision for the new branch.",
				Reason:  err.Error(),
				Hints: []string{
					"git commit --allow-empty -m 'initial'   # a repository needs at least one commit",
					"dispatch run ... --base <ref>",
				},
				Err: err,
			}
		}
		base = resolved
		if proj.DetachedHEAD {
			// A detached HEAD is fine as a base, but the user should know
			// which commit their agent is branching from.
			base = shortRef(resolved)
		}
	}

	branch := strings.TrimSpace(branchOverride)
	explicitBranch := branch != ""
	root := filepath.Join(rc.Config.WorktreesDir, sanitizeDirName(proj.Name))
	path := filepath.Join(root, naming.TruncateSlug(slug, 60))

	// Two dispatch tasks must never share a mutable worktree, so a taken path,
	// branch or slug means we pick a new set rather than reusing any of them.
	if !explicitBranch {
		var err error
		branch, path, slug, err = a.freeWorktreeSlot(proj.Root, rc.Config.BranchPrefix, slug, root, taken)
		if err != nil {
			return Workspace{}, err
		}
	} else {
		slug = nextFreeSlug(slug, taken)
		path = filepath.Join(root, naming.TruncateSlug(slug, 60))
		exists, err := a.git.HasBranch(proj.Root, branch)
		if err != nil {
			return Workspace{}, err
		}
		if exists {
			return Workspace{}, &UserError{
				Summary: "Unable to create worktree.",
				Reason:  fmt.Sprintf("Branch `%s` already exists.", branch),
				Hints: []string{
					"dispatch run ... --branch <different-name>",
					fmt.Sprintf("git branch -d %s   # if that branch is finished with", branch),
				},
			}
		}
		if _, err := os.Stat(path); err == nil {
			path = path + "-" + naming.Slug(branch)
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Workspace{}, fmt.Errorf("create worktree parent directory: %w", err)
	}

	if err := a.git.AddWorktree(ctx, proj.Root, path, branch, base); err != nil {
		return Workspace{}, &UserError{
			Summary: "Unable to create worktree.",
			Reason:  gitReason(err),
			Hints: []string{
				fmt.Sprintf("git worktree list                  # in %s", proj.Root),
				"dispatch run ... --branch <different-name>",
				"dispatch run ... --isolation none",
			},
			Err: err,
		}
	}

	return Workspace{
		Dir:      path,
		Slug:     slug,
		Worktree: path,
		Branch:   branch,
		BaseRef:  base,
		Created:  true,
	}, nil
}

// freeWorktreeSlot finds a slug whose branch, directory and task reference are
// all unused, so every handle for the task agrees.
func (a *App) freeWorktreeSlot(repoRoot, prefix, slug, worktreeRoot string, taken slugTaken) (branch string, path string, chosen string, err error) {
	occupied := map[string]bool{}
	if list, err := a.git.ListWorktrees(repoRoot); err == nil {
		for _, wt := range list {
			occupied[filepath.Clean(wt.Path)] = true
		}
	}

	for attempt := 0; attempt < 100; attempt++ {
		candidateSlug := slugAttempt(slug, attempt)
		branch = naming.BranchName(prefix, candidateSlug)
		path = filepath.Join(worktreeRoot, candidateSlug)

		branchTaken, err := a.git.HasBranch(repoRoot, branch)
		if err != nil {
			return "", "", "", err
		}
		_, statErr := os.Stat(path)
		pathTaken := statErr == nil || occupied[filepath.Clean(path)]

		if !branchTaken && !pathTaken && !slugIsTaken(candidateSlug, taken) {
			return branch, path, candidateSlug, nil
		}
	}
	return "", "", "", &UserError{
		Summary: "Unable to pick a free worktree location.",
		Reason:  fmt.Sprintf("Every candidate branch and directory derived from %q is already in use.", slug),
		Hints: []string{
			"dispatch run ... --branch <explicit-name>",
			"git worktree prune",
		},
	}
}

// cleanupWorkspace reverses a prepared workspace after a failed dispatch.
func (a *App) cleanupWorkspace(ctx context.Context, repoRoot string, ws Workspace) error {
	if !ws.Created || ws.Worktree == "" {
		return nil
	}
	return a.git.RemoveWorktree(ctx, repoRoot, ws.Worktree, true)
}

func gitReason(err error) string {
	var cmdErr *gitx.CommandError
	if ok := asCommandError(err, &cmdErr); ok {
		return cmdErr.Stderr
	}
	return err.Error()
}

func asCommandError(err error, target **gitx.CommandError) bool {
	if err == nil {
		return false
	}
	if ce, ok := err.(*gitx.CommandError); ok {
		*target = ce
		return true
	}
	return false
}

// nextFreeSlug returns the first slug variant no live task is using.
func nextFreeSlug(slug string, taken slugTaken) string {
	for attempt := 0; attempt < 1000; attempt++ {
		candidate := slugAttempt(slug, attempt)
		if !slugIsTaken(candidate, taken) {
			return candidate
		}
	}
	return slug
}

// slugAttempt renders the nth candidate for a slug: the slug itself, then
// -2, -3 and so on, always staying inside the length budget.
func slugAttempt(slug string, attempt int) string {
	if attempt == 0 {
		return naming.TruncateSlug(slug, 60)
	}
	suffix := fmt.Sprintf("-%d", attempt+1)
	return naming.TruncateSlug(slug, 60-len(suffix)) + suffix
}

func slugIsTaken(slug string, taken slugTaken) bool {
	return taken != nil && taken(slug)
}

func sanitizeDirName(name string) string {
	slug := naming.Slug(name)
	if slug == "" {
		return "project"
	}
	return slug
}

func shortRef(ref string) string {
	if len(ref) > 12 && !strings.ContainsAny(ref, "/-") {
		return ref[:12]
	}
	return ref
}
