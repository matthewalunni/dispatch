package roles

import "fmt"

// IsolationMode describes the workspace dispatch prepares for a task.
//
// v0.1 implements "none" and "worktree". New modes only need an entry in
// allIsolationModes plus a preparer in the core package, so the type system
// does not have to change to grow (container, clone, sandbox, ...).
type IsolationMode string

const (
	// IsolationNone runs the agent directly in the detected project root.
	IsolationNone IsolationMode = "none"
	// IsolationWorktree gives the agent a private git worktree and branch.
	IsolationWorktree IsolationMode = "worktree"
)

var allIsolationModes = []IsolationMode{IsolationNone, IsolationWorktree}

// IsolationModes returns every mode this build understands.
func IsolationModes() []IsolationMode {
	out := make([]IsolationMode, len(allIsolationModes))
	copy(out, allIsolationModes)
	return out
}

// ParseIsolation validates a mode coming from YAML, a flag or the database.
func ParseIsolation(value string) (IsolationMode, error) {
	for _, mode := range allIsolationModes {
		if string(mode) == value {
			return mode, nil
		}
	}
	return "", fmt.Errorf("unknown isolation mode %q (supported: %s)", value, JoinIsolationModes())
}

// JoinIsolationModes renders the supported modes for help text and errors.
func JoinIsolationModes() string {
	out := ""
	for i, mode := range allIsolationModes {
		if i > 0 {
			out += ", "
		}
		out += string(mode)
	}
	return out
}

// NeedsRepository reports whether a mode can only be prepared inside a git repo.
func (m IsolationMode) NeedsRepository() bool { return m == IsolationWorktree }

func (m IsolationMode) String() string { return string(m) }
