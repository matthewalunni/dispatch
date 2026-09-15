package core

import (
	"fmt"
	"strings"

	"github.com/matthewalunni/dispatch/internal/config"
	"github.com/matthewalunni/dispatch/internal/project"
	"github.com/matthewalunni/dispatch/internal/roles"
)

// IsolationSource records who decided a task's isolation mode.
//
// It matters for one reason: a worktree a blanket default asked for can be
// given up when there is no repository to branch from, while a worktree a
// role or an explicit flag asked for is a requirement, and failing is the
// honest answer.
type IsolationSource int

const (
	// IsolationFromRole means the role's own `isolation:` decided.
	IsolationFromRole IsolationSource = iota
	// IsolationFromConfig means config's `default_isolation` decided.
	IsolationFromConfig
	// IsolationFromRequest means --isolation (or the TUI form) decided.
	IsolationFromRequest
)

func (s IsolationSource) String() string {
	switch s {
	case IsolationFromConfig:
		return "default"
	case IsolationFromRequest:
		return "override"
	default:
		return "role"
	}
}

// ResolveIsolation applies dispatch's isolation precedence: an explicit
// per-task mode beats the configured default, which beats the role's own.
//
// The role is last because every role must declare a mode, so a default that
// only filled gaps would never be consulted. Clearing default_isolation hands
// the decision back to the roles.
func ResolveIsolation(cfg config.Config, role roles.Role, override string) (roles.IsolationMode, IsolationSource, error) {
	mode, source := role.Isolation, IsolationFromRole

	if configured := strings.TrimSpace(cfg.DefaultIsolation); configured != "" {
		parsed, err := roles.ParseIsolation(configured)
		if err != nil {
			return "", source, &UserError{
				Summary: fmt.Sprintf("Invalid default_isolation %q in configuration.", configured),
				Reason:  err.Error(),
				Hints:   []string{"dispatch doctor", `set default_isolation: worktree, none, or "" to follow each role`},
				Err:     err,
			}
		}
		mode, source = parsed, IsolationFromConfig
	}

	if override = strings.TrimSpace(override); override != "" {
		parsed, err := roles.ParseIsolation(override)
		if err != nil {
			return "", source, &UserError{Summary: "Invalid isolation mode.", Reason: err.Error(), Err: err}
		}
		mode, source = parsed, IsolationFromRequest
	}

	return mode, source, nil
}

// degradeIsolation gives up a worktree that nobody specifically asked for.
//
// dispatch is meant to work from any directory, so a blanket
// `default_isolation: worktree` must not turn "run an agent in this folder"
// into an error the moment the folder is not a repository. Only the blanket
// default is given up: a mode that came from a flag, or one the role itself
// declares, is a requirement, and prepareWorkspace refuses it with an
// explanation — which is what was asked for.
func degradeIsolation(mode roles.IsolationMode, source IsolationSource, role roles.Role, proj project.Project) (roles.IsolationMode, string) {
	if source != IsolationFromConfig || !mode.NeedsRepository() || proj.IsGit {
		return mode, ""
	}
	if role.Isolation.NeedsRepository() {
		return mode, ""
	}
	return roles.IsolationNone, fmt.Sprintf(
		"%s is not a git repository, so this task runs in place instead of in a worktree", proj.Root)
}
