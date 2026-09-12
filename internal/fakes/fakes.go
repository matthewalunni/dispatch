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
	"strings"
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

	agents           map[string]herdrx.Agent
	tabCounter       int
	workspaceCounter int
	openWorkspaces   map[string]bool
	workspaces       []herdrx.Workspace

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
	// TabEnv records the environment dispatch set on each created session.
	TabEnv [][]string
	// Sessions records every session dispatch asked herdr to build.
	Sessions []herdrx.CreateSessionRequest
	// ClosedSessions records every session dispatch tore down.
	ClosedSessions []herdrx.Session
	// ClosedWorkspaces records workspaces closed, so a test can tell a
	// workspace teardown from a tab teardown.
	ClosedWorkspaces []string
	// WatchedPanes records the pane set each Watch call subscribed to.
	WatchedPanes [][]string
	// EventsUnsupported makes Watch refuse, exercising the polling fallback.
	EventsUnsupported bool

	listeners []chan herdrx.Event
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
		agents:         map[string]herdrx.Agent{},
		openWorkspaces: map[string]bool{},
		workspaces:     []herdrx.Workspace{{WorkspaceID: "w1", Label: "default", Focused: true}},
		StartedArgs:    map[string][]string{},
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
	h.TabEnv = append(h.TabEnv, append([]string(nil), req.Env...))
	return herdrx.Tab{
		TabID:       fmt.Sprintf("w1:t%d", h.tabCounter),
		WorkspaceID: "w1",
		PaneID:      fmt.Sprintf("w1:p%d", h.tabCounter),
		Label:       req.Label,
	}, nil
}

// CreateSession mirrors the real adapter: a workspace per task, or a tab in a
// shared one.
func (h *Herdr) CreateSession(ctx context.Context, req herdrx.CreateSessionRequest) (herdrx.Session, error) {
	if h.CreateTabErr != nil {
		return herdrx.Session{}, h.CreateTabErr
	}
	h.mu.Lock()
	h.Sessions = append(h.Sessions, req)
	h.mu.Unlock()

	if req.Layout == herdrx.LayoutTab {
		tab, err := h.CreateTab(ctx, herdrx.CreateTabRequest{
			WorkspaceID: req.WorkspaceID, CWD: req.CWD, Label: req.Label,
			Focus: req.Focus, Env: req.Env,
		})
		if err != nil {
			return herdrx.Session{}, err
		}
		return herdrx.Session{
			WorkspaceID: tab.WorkspaceID, TabID: tab.TabID, PaneID: tab.PaneID,
			Label: tab.Label, Layout: herdrx.LayoutTab,
		}, nil
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.workspaceCounter++
	h.TabEnv = append(h.TabEnv, append([]string(nil), req.Env...))
	workspace := fmt.Sprintf("w%d", h.workspaceCounter+1)
	session := herdrx.Session{
		WorkspaceID: workspace,
		TabID:       fmt.Sprintf("%s:t1", workspace),
		PaneID:      fmt.Sprintf("%s:p1", workspace),
		Label:       req.Label,
		Layout:      herdrx.LayoutWorkspace,
	}
	if req.Worktree {
		session.WorktreePath = req.CWD
	}
	h.openWorkspaces[workspace] = true
	return session, nil
}

// CloseSession removes the workspace dispatch created, or just its tab.
func (h *Herdr) CloseSession(_ context.Context, session herdrx.Session) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ClosedSessions = append(h.ClosedSessions, session)
	if session.OwnsWorkspace() {
		h.ClosedWorkspaces = append(h.ClosedWorkspaces, session.WorkspaceID)
		delete(h.openWorkspaces, session.WorkspaceID)
	} else if session.TabID != "" {
		h.ClosedTabs = append(h.ClosedTabs, session.TabID)
	}
	// The agent in that container goes with it.
	for name, agent := range h.agents {
		if agent.PaneID == session.PaneID {
			delete(h.agents, name)
			h.StoppedAgents = append(h.StoppedAgents, name)
		}
	}
	return nil
}

// OpenWorkspaceCount reports workspaces the fake still has open.
func (h *Herdr) OpenWorkspaceCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.openWorkspaces)
}

func (h *Herdr) StartAgent(_ context.Context, req herdrx.StartAgentRequest) (herdrx.Agent, error) {
	if h.StartErr != nil {
		return herdrx.Agent{}, h.StartErr
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	workspace, _, _ := strings.Cut(req.PaneID, ":")
	agent := herdrx.Agent{
		Name: req.Name, Kind: req.Kind, PaneID: req.PaneID,
		TabID: workspace + ":t1", WorkspaceID: workspace, TerminalID: "term_" + req.Name,
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

// Watch implements herdrx.Watcher. Events are whatever a test pushes through
// Emit, so the application layer's event handling can be exercised without a
// socket.
func (h *Herdr) Watch(ctx context.Context, panes []string, handle func(herdrx.Event)) error {
	if h.EventsUnsupported {
		return herdrx.ErrEventsUnsupported
	}
	h.mu.Lock()
	h.WatchedPanes = append(h.WatchedPanes, append([]string(nil), panes...))
	ch := make(chan herdrx.Event, 32)
	h.listeners = append(h.listeners, ch)
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		for i, listener := range h.listeners {
			if listener == ch {
				h.listeners = append(h.listeners[:i], h.listeners[i+1:]...)
				break
			}
		}
		h.mu.Unlock()
	}()

	handle(herdrx.Event{Kind: herdrx.EventStreamReady})
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-ch:
			handle(event)
		}
	}
}

// Emit pushes an event to every active Watch call.
func (h *Herdr) Emit(event herdrx.Event) {
	h.mu.Lock()
	listeners := append([]chan herdrx.Event(nil), h.listeners...)
	h.mu.Unlock()
	for _, listener := range listeners {
		select {
		case listener <- event:
		default:
		}
	}
}

// SubscribedTo reports whether any Watch call has asked for a pane's
// agent-status transitions.
func (h *Herdr) SubscribedTo(pane string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, set := range h.WatchedPanes {
		for _, candidate := range set {
			if candidate == pane {
				return true
			}
		}
	}
	return false
}

// SubscribedPanes returns a copy of every pane set Watch has been called with.
func (h *Herdr) SubscribedPanes() [][]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([][]string, 0, len(h.WatchedPanes))
	for _, set := range h.WatchedPanes {
		out = append(out, append([]string(nil), set...))
	}
	return out
}

// WatchCount reports how many Watch calls are currently subscribed.
func (h *Herdr) WatchCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.listeners)
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

var (
	_ herdrx.Client  = (*Herdr)(nil)
	_ herdrx.Watcher = (*Herdr)(nil)
)
