package clix

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/matthewalunni/dispatch/internal/core"
)

// emitJSON writes a machine-readable document to stdout.
//
// JSON mode never mixes human text into stdout: warnings and diagnostics go to
// stderr so a consuming orchestrator can parse stdout unconditionally.
func emitJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func warn(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "warning: "+format+"\n", args...)
}

func newTabWriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
}

// taskJSON is the stable machine-facing shape of a task.
//
// External orchestration treats dispatch as an execution primitive, so this
// shape is part of the contract: fields may be added, but existing names and
// meanings should not change.
type taskJSON struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Slug        string `json:"slug"`
	Role        string `json:"role"`
	Runtime     string `json:"runtime"`
	Isolation   string `json:"isolation"`
	Status      string `json:"status"`

	DispatchStatus string `json:"dispatch_status"`
	AgentStatus    string `json:"agent_status,omitempty"`
	AgentAlive     bool   `json:"agent_alive"`

	ProjectRoot string `json:"project_root"`
	ProjectName string `json:"project_name"`
	Branch      string `json:"branch,omitempty"`
	Worktree    string `json:"worktree,omitempty"`
	BaseRef     string `json:"base_ref,omitempty"`

	HerdrAgent       string `json:"herdr_agent,omitempty"`
	HerdrPaneID      string `json:"herdr_pane_id,omitempty"`
	HerdrTabID       string `json:"herdr_tab_id,omitempty"`
	HerdrWorkspaceID string `json:"herdr_workspace_id,omitempty"`
	HerdrSession     string `json:"herdr_session,omitempty"`
	HerdrLayout      string `json:"herdr_layout,omitempty"`

	ParentTaskID string `json:"parent_task_id,omitempty"`
	PromptPath   string `json:"prompt_path,omitempty"`
	SessionDir   string `json:"session_dir,omitempty"`

	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	CompletedAt string `json:"completed_at,omitempty"`
	Error       string `json:"error,omitempty"`
}

func toJSON(view core.TaskView, dataDir string) taskJSON {
	out := taskJSON{
		ID:               view.ID,
		Title:            view.Title,
		Description:      view.Description,
		Slug:             view.Slug,
		Role:             view.Role,
		Runtime:          view.Runtime,
		Isolation:        string(view.Isolation),
		Status:           string(view.Effective),
		DispatchStatus:   string(view.Task.Status),
		AgentStatus:      string(view.AgentStatus),
		AgentAlive:       view.AgentAlive,
		ProjectRoot:      view.ProjectRoot,
		ProjectName:      view.ProjectName,
		Branch:           view.Branch,
		Worktree:         view.Worktree,
		BaseRef:          view.BaseRef,
		HerdrAgent:       view.HerdrAgent,
		HerdrPaneID:      view.HerdrPaneID,
		HerdrTabID:       view.HerdrTabID,
		HerdrWorkspaceID: view.HerdrWorkspaceID,
		HerdrSession:     view.HerdrSession,
		HerdrLayout:      view.HerdrLayout,
		ParentTaskID:     view.ParentTaskID,
		Error:            view.Task.Error,
		CreatedAt:        view.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:        view.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if dataDir != "" {
		out.PromptPath = core.PromptPath(dataDir, view.ID)
		out.SessionDir = core.SessionDir(dataDir, view.ID)
	}
	if view.CompletedAt != nil {
		out.CompletedAt = view.CompletedAt.Format("2006-01-02T15:04:05Z07:00")
	}
	return out
}

func toJSONList(views []core.TaskView, dataDir string) []taskJSON {
	out := make([]taskJSON, 0, len(views))
	for _, v := range views {
		out = append(out, toJSON(v, dataDir))
	}
	return out
}

// truncate shortens a string for column display.
func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return string(runes[:max])
	}
	return strings.TrimRight(string(runes[:max-1]), " ") + "…"
}
