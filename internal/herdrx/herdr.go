// Package herdrx encapsulates every interaction with the herdr runtime.
//
// Herdr owns terminal sessions, panes, tabs and the live agent processes.
// Dispatch never predicts an identifier: every id it stores comes back from a
// herdr response. Written against herdr 0.9.0 (API protocol 22).
package herdrx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// Status is one of herdr's agent lifecycle states.
type Status string

const (
	StatusIdle    Status = "idle"
	StatusWorking Status = "working"
	StatusBlocked Status = "blocked"
	StatusDone    Status = "done"
	StatusUnknown Status = "unknown"
)

// NeedsAttention reports whether herdr thinks a human should look at this agent.
// "blocked" is herdr recognising an approval or question UI — it is a reliable
// signal, so dispatch does not scrape terminal output for one.
func (s Status) NeedsAttention() bool { return s == StatusBlocked }

// Live reports whether the agent is actively producing work.
func (s Status) Live() bool { return s == StatusWorking }

// Agent is herdr's view of an agent occupying a pane.
type Agent struct {
	Name             string `json:"name"`
	Kind             string `json:"agent"`
	Status           Status `json:"agent_status"`
	PaneID           string `json:"pane_id"`
	TabID            string `json:"tab_id"`
	WorkspaceID      string `json:"workspace_id"`
	TerminalID       string `json:"terminal_id"`
	CWD              string `json:"cwd"`
	Focused          bool   `json:"focused"`
	InteractiveReady bool   `json:"interactive_ready"`
}

// Tab is a created tab plus its root pane.
type Tab struct {
	TabID       string
	WorkspaceID string
	PaneID      string
	Label       string
}

// Workspace is a herdr workspace.
type Workspace struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	Focused     bool   `json:"focused"`
	ActiveTabID string `json:"active_tab_id"`
}

// Health summarises whether herdr is usable right now.
type Health struct {
	Installed     bool   `json:"installed"`
	BinaryPath    string `json:"binary_path,omitempty"`
	ClientVersion string `json:"client_version,omitempty"`
	ServerRunning bool   `json:"server_running"`
	ServerVersion string `json:"server_version,omitempty"`
	Socket        string `json:"socket,omitempty"`
	Compatible    bool   `json:"compatible"`
	Detail        string `json:"detail,omitempty"`
}

// CreateTabRequest asks herdr for a new tab rooted at a working directory.
type CreateTabRequest struct {
	WorkspaceID string
	CWD         string
	Label       string
	Focus       bool
	// Env is set on the launched process, as KEY=VALUE pairs.
	Env []string
}

// StartAgentRequest launches an agent runtime inside an existing pane.
type StartAgentRequest struct {
	Name      string
	Kind      string
	PaneID    string
	TimeoutMS int
	Args      []string
}

// Client is the herdr surface dispatch depends on. Kept deliberately narrow so
// it is cheap to fake in tests.
type Client interface {
	Health(ctx context.Context) Health
	Workspaces(ctx context.Context) ([]Workspace, error)
	CreateTab(ctx context.Context, req CreateTabRequest) (Tab, error)
	StartAgent(ctx context.Context, req StartAgentRequest) (Agent, error)
	PromptAgent(ctx context.Context, target, text string) error
	ListAgents(ctx context.Context) ([]Agent, error)
	GetAgent(ctx context.Context, target string) (Agent, error)
	FocusAgent(ctx context.Context, target string) error
	StopAgent(ctx context.Context, target string) error
	CloseTab(ctx context.Context, tabID string) error
	// AttachCommand returns the argv that hands the terminal to a live agent.
	// Attaching replaces dispatch's own stdio, so the caller runs it.
	AttachCommand(target string) []string
}

// Error is a structured herdr API error.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Sentinel codes dispatch reacts to specifically.
const (
	CodeServerNotRunning = "server_not_running"
	CodeAgentNotFound    = "agent_not_found"
	CodeAgentNotReady    = "agent_not_ready"
	CodeAgentBlocked     = "agent_blocked"
	CodePaneNotFound     = "pane_not_found"
)

// IsCode reports whether err is a herdr error with the given code.
func IsCode(err error, code string) bool {
	var herdrErr *Error
	return errors.As(err, &herdrErr) && herdrErr.Code == code
}

// ErrNotInstalled is returned when the herdr binary cannot be found.
var ErrNotInstalled = errors.New("herdr is not installed or not on PATH")

// CLI talks to herdr through its command line interface, which speaks JSON
// over the local socket API.
type CLI struct {
	Binary  string
	Session string

	// socketOnce guards the cached event-socket path.
	socketOnce   sync.Mutex
	cachedSocket string
}

// NewCLI builds a herdr adapter. An empty binary defaults to "herdr".
func NewCLI(binary, session string) *CLI {
	if binary == "" {
		binary = "herdr"
	}
	return &CLI{Binary: binary, Session: session}
}

func (c *CLI) argv(args ...string) []string {
	out := make([]string, 0, len(args)+2)
	if c.Session != "" {
		out = append(out, "--session", c.Session)
	}
	return append(out, args...)
}

// AttachCommand returns the argv for an interactive attach.
func (c *CLI) AttachCommand(target string) []string {
	return append([]string{c.Binary}, c.argv("agent", "attach", target)...)
}

// envelope is herdr's success response shape.
type envelope struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
}

// exec runs a herdr command and returns its decoded `result` object.
func (c *CLI) exec(ctx context.Context, args ...string) (json.RawMessage, error) {
	cmd := exec.CommandContext(ctx, c.Binary, c.argv(args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	if isMissingBinary(runErr) {
		return nil, ErrNotInstalled
	}

	// herdr reports API errors as JSON on stderr with exit status 1.
	if errText := strings.TrimSpace(stderr.String()); errText != "" {
		var env envelope
		if err := json.Unmarshal([]byte(errText), &env); err == nil && env.Error != nil {
			return nil, env.Error
		}
		if runErr != nil {
			return nil, fmt.Errorf("herdr %s: %s", strings.Join(args, " "), firstLine(errText))
		}
	}
	if runErr != nil {
		return nil, fmt.Errorf("herdr %s: %w", strings.Join(args, " "), runErr)
	}

	out := bytes.TrimSpace(stdout.Bytes())
	if len(out) == 0 {
		return nil, nil
	}
	var env envelope
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("herdr %s: unexpected output: %s", strings.Join(args, " "), firstLine(string(out)))
	}
	if env.Error != nil {
		return nil, env.Error
	}
	return env.Result, nil
}

// isMissingBinary reports an unrunnable herdr binary. A name resolved through
// PATH fails as *exec.Error; an absolute path configured in config.yaml fails
// as a plain not-exist error, and both should produce the same advice.
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

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Health probes the binary and the running server without mutating anything.
func (c *CLI) Health(ctx context.Context) Health {
	var h Health
	path, err := exec.LookPath(c.Binary)
	if err != nil {
		h.Detail = fmt.Sprintf("%s not found on PATH", c.Binary)
		return h
	}
	h.Installed = true
	h.BinaryPath = path

	cmd := exec.CommandContext(ctx, c.Binary, c.argv("status", "--json")...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		h.Detail = strings.TrimSpace(stderr.String())
		if h.Detail == "" {
			h.Detail = err.Error()
		}
		return h
	}

	var status struct {
		Client struct {
			Version string `json:"version"`
		} `json:"client"`
		Server struct {
			Running    bool   `json:"running"`
			Version    string `json:"version"`
			Socket     string `json:"socket"`
			Compatible *bool  `json:"compatible"`
		} `json:"server"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		h.Detail = "could not parse `herdr status --json` output"
		return h
	}
	h.ClientVersion = status.Client.Version
	h.ServerRunning = status.Server.Running
	h.ServerVersion = status.Server.Version
	h.Socket = status.Server.Socket
	h.Compatible = status.Server.Compatible == nil || *status.Server.Compatible
	if !h.ServerRunning {
		h.Detail = "no herdr server is running; start one with `herdr`"
	} else if !h.Compatible {
		h.Detail = "herdr client and server protocol versions differ; restart the server"
	}
	return h
}

func (c *CLI) Workspaces(ctx context.Context) ([]Workspace, error) {
	raw, err := c.exec(ctx, "workspace", "list")
	if err != nil {
		return nil, err
	}
	var result struct {
		Workspaces []Workspace `json:"workspaces"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("parse workspace list: %w", err)
	}
	return result.Workspaces, nil
}

func (c *CLI) CreateTab(ctx context.Context, req CreateTabRequest) (Tab, error) {
	args := []string{"tab", "create"}
	if req.WorkspaceID != "" {
		args = append(args, "--workspace", req.WorkspaceID)
	}
	if req.CWD != "" {
		args = append(args, "--cwd", req.CWD)
	}
	if req.Label != "" {
		args = append(args, "--label", req.Label)
	}
	for _, entry := range req.Env {
		args = append(args, "--env", entry)
	}
	if req.Focus {
		args = append(args, "--focus")
	} else {
		args = append(args, "--no-focus")
	}

	raw, err := c.exec(ctx, args...)
	if err != nil {
		return Tab{}, err
	}
	var result struct {
		Tab struct {
			TabID       string `json:"tab_id"`
			WorkspaceID string `json:"workspace_id"`
			Label       string `json:"label"`
		} `json:"tab"`
		RootPane struct {
			PaneID string `json:"pane_id"`
		} `json:"root_pane"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return Tab{}, fmt.Errorf("parse tab create: %w", err)
	}
	if result.RootPane.PaneID == "" {
		return Tab{}, errors.New("herdr created a tab but did not return a root pane id")
	}
	return Tab{
		TabID:       result.Tab.TabID,
		WorkspaceID: result.Tab.WorkspaceID,
		PaneID:      result.RootPane.PaneID,
		Label:       result.Tab.Label,
	}, nil
}

func (c *CLI) StartAgent(ctx context.Context, req StartAgentRequest) (Agent, error) {
	args := []string{"agent", "start", req.Name, "--kind", req.Kind, "--pane", req.PaneID}
	if req.TimeoutMS > 0 {
		args = append(args, "--timeout", strconv.Itoa(req.TimeoutMS))
	}
	if len(req.Args) > 0 {
		args = append(args, "--")
		args = append(args, req.Args...)
	}
	raw, err := c.exec(ctx, args...)
	if err != nil {
		return Agent{}, err
	}
	var result struct {
		Agent Agent `json:"agent"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return Agent{}, fmt.Errorf("parse agent start: %w", err)
	}
	return result.Agent, nil
}

// PromptAgent submits the assignment. Deliberately without --wait: dispatch
// hands work off and leaves the conversation running for the human, and a
// blocking wait would also fail for agents that never leave the idle state.
func (c *CLI) PromptAgent(ctx context.Context, target, text string) error {
	_, err := c.exec(ctx, "agent", "prompt", target, text)
	return err
}

func (c *CLI) ListAgents(ctx context.Context) ([]Agent, error) {
	raw, err := c.exec(ctx, "agent", "list")
	if err != nil {
		return nil, err
	}
	var result struct {
		Agents []Agent `json:"agents"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("parse agent list: %w", err)
	}
	return result.Agents, nil
}

func (c *CLI) GetAgent(ctx context.Context, target string) (Agent, error) {
	raw, err := c.exec(ctx, "agent", "get", target)
	if err != nil {
		return Agent{}, err
	}
	var result struct {
		Agent Agent `json:"agent"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return Agent{}, fmt.Errorf("parse agent get: %w", err)
	}
	return result.Agent, nil
}

func (c *CLI) FocusAgent(ctx context.Context, target string) error {
	_, err := c.exec(ctx, "agent", "focus", target)
	return err
}

// StopAgent ends the agent by closing the pane that hosts it. herdr 0.9.0 has
// no `agent stop`, and the pane is the thing dispatch created, so closing it
// is the operation that actually reverses the dispatch.
func (c *CLI) StopAgent(ctx context.Context, target string) error {
	agent, err := c.GetAgent(ctx, target)
	if err != nil {
		return err
	}
	_, err = c.exec(ctx, "pane", "close", agent.PaneID)
	return err
}

func (c *CLI) CloseTab(ctx context.Context, tabID string) error {
	_, err := c.exec(ctx, "tab", "close", tabID)
	return err
}

// InsideHerdr reports whether this process is itself running in a herdr pane.
func InsideHerdr() bool { return os.Getenv("HERDR_ENV") == "1" }

var _ Client = (*CLI)(nil)
