// Package fakes provides in-memory stand-ins for the two external systems
// dispatch depends on, so the application layer can be tested without a real
// git repository or a running herdr server.
//
// They are deliberately small: enough to observe what dispatch asked for and
// to simulate the failures that matter, and no more.
package fakes

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/matthewalunni/dispatch/internal/gitx"
	"github.com/matthewalunni/dispatch/internal/herdrx"
)

// Git is an in-memory gitx.Git.
type Git struct {
	mu sync.Mutex

	// Repos maps a directory to the repository root containing it. A
	// directory with no entry is treated as not being in a repository.
	Repos map[string]string
	// Branches records existing local branches per repository root.
	Branches map[string]map[string]bool
	// Worktrees records worktrees per repository root.
	Worktrees map[string][]gitx.Worktree
	// Branch is the checked-out branch reported for every repository.
	Branch string
	// Dirty is what IsDirty reports.
	Dirty bool
	// Version is what Available reports.
	Version string

	// AddWorktreeErr, when set, fails every AddWorktree call.
	AddWorktreeErr error
	// RemoveWorktreeErr, when set, fails every RemoveWorktree call.
	RemoveWorktreeErr error

	// Added records every worktree dispatch asked git to create.
	Added []AddedWorktree
	// Removed records every worktree dispatch asked git to remove.
	Removed []string
}

// AddedWorktree captures one AddWorktree call.
type AddedWorktree struct {
	Repo   string
	Path   string
	Branch string
	Base   string
}

// NewGit returns a fake git where repoRoot is a repository containing itself.
func NewGit(repoRoot string) *Git {
	return &Git{
		Repos:     map[string]string{repoRoot: repoRoot},
		Branches:  map[string]map[string]bool{repoRoot: {"main": true}},
		Worktrees: map[string][]gitx.Worktree{},
		Branch:    "main",
		Version:   "2.43.0",
	}
}

func (g *Git) Available() (string, error) { return g.Version, nil }

func (g *Git) RepoRoot(dir string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if root, ok := g.Repos[dir]; ok {
		return root, nil
	}
	// Walk upwards, mirroring how git discovers a working tree.
	for current := dir; ; {
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		if root, ok := g.Repos[parent]; ok {
			return root, nil
		}
		current = parent
	}
	return "", gitx.ErrNotARepository
}

func (g *Git) CurrentBranch(string) (string, error) { return g.Branch, nil }
func (g *Git) IsDirty(string) (bool, error)         { return g.Dirty, nil }

func (g *Git) HasBranch(repo, branch string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.Branches[repo][branch], nil
}

func (g *Git) DefaultBase(repo string) (string, error) {
	if g.Branch == "" {
		return "HEAD", nil
	}
	return g.Branch, nil
}

func (g *Git) ListWorktrees(repo string) ([]gitx.Worktree, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.Worktrees[repo], nil
}

func (g *Git) AddWorktree(_ context.Context, repo, path, branch, base string) error {
	if g.AddWorktreeErr != nil {
		return g.AddWorktreeErr
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.Branches[repo] == nil {
		g.Branches[repo] = map[string]bool{}
	}
	g.Branches[repo][branch] = true
	g.Worktrees[repo] = append(g.Worktrees[repo], gitx.Worktree{Path: path, Branch: branch})
	g.Repos[path] = repo
	g.Added = append(g.Added, AddedWorktree{Repo: repo, Path: path, Branch: branch, Base: base})
	return nil
}

func (g *Git) RemoveWorktree(_ context.Context, repo, path string, _ bool) error {
	if g.RemoveWorktreeErr != nil {
		return g.RemoveWorktreeErr
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Removed = append(g.Removed, path)
	kept := g.Worktrees[repo][:0]
	for _, wt := range g.Worktrees[repo] {
		if wt.Path != path {
			kept = append(kept, wt)
		}
	}
	g.Worktrees[repo] = kept
	return nil
}

var _ gitx.Git = (*Git)(nil)

// Herdr is an in-memory herdrx.Client.
type Herdr struct {
	mu sync.Mutex

	// HealthState is what Health reports.
	HealthState herdrx.Health

	// StartErr, when set, fails every StartAgent call.
	StartErr error
	// PromptErr, when set, fails every PromptAgent call.
	PromptErr error
	// CreateTabErr, when set, fails every CreateTab call.
	CreateTabErr error

	agents     map[string]herdrx.Agent
	tabCounter int
	workspaces []herdrx.Workspace

	// Prompts records every assignment dispatch submitted.
	Prompts []Prompt
	// Focused records every agent dispatch asked herdr to focus.
	Focused []string
	// ClosedTabs records tabs dispatch cleaned up.
	ClosedTabs []string
	// StoppedAgents records agents dispatch stopped.
	StoppedAgents []string
	// StartedArgs records the extra argv passed to each launch.
	StartedArgs map[string][]string
}

// Prompt is one assignment submission.
type Prompt struct {
	Target string
	Text   string
}

// NewHerdr returns a healthy fake herdr with one workspace.
func NewHerdr() *Herdr {
	return &Herdr{
		HealthState: herdrx.Health{
			Installed: true, ServerRunning: true, Compatible: true,
			ClientVersion: "0.9.0", ServerVersion: "0.9.0",
		},
		agents:      map[string]herdrx.Agent{},
		workspaces:  []herdrx.Workspace{{WorkspaceID: "w1", Label: "default", Focused: true}},
		StartedArgs: map[string][]string{},
	}
}

func (h *Herdr) Health(context.Context) herdrx.Health { return h.HealthState }

func (h *Herdr) Workspaces(context.Context) ([]herdrx.Workspace, error) {
	return h.workspaces, nil
}

func (h *Herdr) CreateTab(_ context.Context, req herdrx.CreateTabRequest) (herdrx.Tab, error) {
	if h.CreateTabErr != nil {
		return herdrx.Tab{}, h.CreateTabErr
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.tabCounter++
	return herdrx.Tab{
		TabID:       fmt.Sprintf("w1:t%d", h.tabCounter),
		WorkspaceID: "w1",
		PaneID:      fmt.Sprintf("w1:p%d", h.tabCounter),
		Label:       req.Label,
	}, nil
}

func (h *Herdr) StartAgent(_ context.Context, req herdrx.StartAgentRequest) (herdrx.Agent, error) {
	if h.StartErr != nil {
		return herdrx.Agent{}, h.StartErr
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	agent := herdrx.Agent{
		Name: req.Name, Kind: req.Kind, PaneID: req.PaneID,
		TabID: "w1:t1", WorkspaceID: "w1", TerminalID: "term_" + req.Name,
		Status: herdrx.StatusIdle, InteractiveReady: true,
	}
	h.agents[req.Name] = agent
	h.StartedArgs[req.Name] = req.Args
	return agent, nil
}

func (h *Herdr) PromptAgent(_ context.Context, target, text string) error {
	if h.PromptErr != nil {
		return h.PromptErr
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Prompts = append(h.Prompts, Prompt{Target: target, Text: text})
	return nil
}

func (h *Herdr) ListAgents(context.Context) ([]herdrx.Agent, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]herdrx.Agent, 0, len(h.agents))
	for _, agent := range h.agents {
		out = append(out, agent)
	}
	return out, nil
}

func (h *Herdr) GetAgent(_ context.Context, target string) (herdrx.Agent, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if agent, ok := h.agents[target]; ok {
		return agent, nil
	}
	return herdrx.Agent{}, &herdrx.Error{Code: herdrx.CodeAgentNotFound, Message: "agent target " + target + " not found"}
}

func (h *Herdr) FocusAgent(_ context.Context, target string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.agents[target]; !ok {
		return &herdrx.Error{Code: herdrx.CodeAgentNotFound, Message: "agent target " + target + " not found"}
	}
	h.Focused = append(h.Focused, target)
	return nil
}

func (h *Herdr) StopAgent(_ context.Context, target string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.agents[target]; !ok {
		return &herdrx.Error{Code: herdrx.CodeAgentNotFound, Message: "agent target " + target + " not found"}
	}
	delete(h.agents, target)
	h.StoppedAgents = append(h.StoppedAgents, target)
	return nil
}

func (h *Herdr) CloseTab(_ context.Context, tabID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ClosedTabs = append(h.ClosedTabs, tabID)
	return nil
}

func (h *Herdr) AttachCommand(target string) []string {
	return []string{"herdr", "agent", "attach", target}
}

// SetStatus changes an agent's live state, simulating herdr detection.
func (h *Herdr) SetStatus(name string, status herdrx.Status) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if agent, ok := h.agents[name]; ok {
		agent.Status = status
		h.agents[name] = agent
	}
}

// Kill removes an agent without going through dispatch, simulating a pane the
// user closed in herdr directly.
func (h *Herdr) Kill(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.agents, name)
}

// AgentCount reports how many agents are live.
func (h *Herdr) AgentCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.agents)
}

var _ herdrx.Client = (*Herdr)(nil)
