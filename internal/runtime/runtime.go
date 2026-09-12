// Package runtime keeps agent-binary specifics out of the rest of dispatch.
//
// v0.1 launches Claude, but nothing outside this package knows that: a runtime
// is resolved from configuration, so adding Codex or another terminal agent is
// a config entry, and adding one that needs bespoke behaviour is one small
// implementation of Runtime.
package runtime

import (
	"fmt"
	"os/exec"

	"github.com/matthewalunni/dispatch/internal/config"
)

// LaunchSpec is everything a runtime needs to decide how to start.
type LaunchSpec struct {
	// WorkingDir is where the agent process should run.
	WorkingDir string
	// RoleArgs are extra arguments contributed by the role definition.
	RoleArgs []string
}

// Runtime describes how to start one kind of terminal agent under herdr.
type Runtime interface {
	// Name is the dispatch-facing runtime name (e.g. "claude").
	Name() string
	// AgentKind is the herdr `--kind` label used to launch and detect it.
	AgentKind() string
	// Binary is the executable dispatch checks for in `dispatch doctor`.
	Binary() string
	// Args returns the arguments passed through to the agent binary.
	Args(spec LaunchSpec) []string
	// PromptOnLaunch reports whether the assignment should be delivered as an
	// interactive prompt after the agent becomes ready.
	PromptOnLaunch() bool
}

// configured is the generic, config-driven runtime. It covers every terminal
// agent herdr can launch and detect, which in practice is all of them.
type configured struct {
	name string
	cfg  config.RuntimeConfig
}

func (r configured) Name() string      { return r.name }
func (r configured) AgentKind() string { return r.cfg.Kind }
func (r configured) Binary() string    { return r.cfg.Binary }

func (r configured) Args(spec LaunchSpec) []string {
	args := make([]string, 0, len(r.cfg.Args)+len(spec.RoleArgs))
	args = append(args, r.cfg.Args...)
	args = append(args, spec.RoleArgs...)
	return args
}

func (r configured) PromptOnLaunch() bool {
	if r.cfg.PromptOnLaunch == nil {
		return true
	}
	return *r.cfg.PromptOnLaunch
}

// Resolve builds the Runtime for a configured runtime name.
func Resolve(cfg config.Config, name string) (Runtime, error) {
	rc, err := cfg.Runtime(name)
	if err != nil {
		return nil, err
	}
	return configured{name: name, cfg: rc}, nil
}

// Installed reports whether a runtime's binary is on PATH.
func Installed(rt Runtime) (string, error) {
	path, err := exec.LookPath(rt.Binary())
	if err != nil {
		return "", fmt.Errorf("%s is not installed or not on PATH", rt.Binary())
	}
	return path, nil
}
