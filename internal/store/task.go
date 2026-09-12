package store

import (
	"strings"
	"time"

	"github.com/matthewalunni/dispatch/internal/roles"
)

// Status is dispatch's own lifecycle state for a task. It is deliberately
// separate from herdr's live agent status: dispatch owns what a task *is*,
// herdr owns whether its process is currently alive or waiting.
type Status string

const (
	// StatusPending means the task row exists but the agent has not started.
	StatusPending Status = "pending"
	// StatusRunning means dispatch launched an agent and has not stopped it.
	StatusRunning Status = "running"
	// StatusStopped means the agent was stopped through dispatch.
	StatusStopped Status = "stopped"
	// StatusCompleted means a human marked the task done.
	StatusCompleted Status = "completed"
	// StatusFailed means dispatch could not bring the task up.
	StatusFailed Status = "failed"
)

// Terminal reports whether a status is final.
func (s Status) Terminal() bool {
	return s == StatusStopped || s == StatusCompleted || s == StatusFailed
}

func (s Status) String() string { return string(s) }

// Task is dispatch's canonical record of a dispatched piece of work.
//
// Runtime-specific handles live in the Herdr* fields and nowhere else, so the
// rest of the model stays independent of which multiplexer is in use.
type Task struct {
	ID          string              `json:"id"`
	Title       string              `json:"title"`
	Description string              `json:"description,omitempty"`
	Slug        string              `json:"slug"`
	ProjectRoot string              `json:"project_root"`
	ProjectName string              `json:"project_name"`
	Role        string              `json:"role"`
	Runtime     string              `json:"runtime"`
	Isolation   roles.IsolationMode `json:"isolation"`
	Status      Status              `json:"status"`

	Worktree string `json:"worktree,omitempty"`
	Branch   string `json:"branch,omitempty"`
	BaseRef  string `json:"base_ref,omitempty"`

	// Herdr handles, captured from herdr responses rather than predicted.
	HerdrAgent       string `json:"herdr_agent,omitempty"`
	HerdrWorkspaceID string `json:"herdr_workspace_id,omitempty"`
	HerdrTabID       string `json:"herdr_tab_id,omitempty"`
	HerdrPaneID      string `json:"herdr_pane_id,omitempty"`
	HerdrSession     string `json:"herdr_session,omitempty"`
	// HerdrLayout records whether dispatch created a whole workspace for this
	// task or only a tab, which decides what stopping it should tear down.
	HerdrLayout string `json:"herdr_layout,omitempty"`

	ParentTaskID string `json:"parent_task_id,omitempty"`

	// Error records why a dispatch failed, when it did.
	Error string `json:"error,omitempty"`

	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// SessionRef is the durable pointer back into a persistent conversation.
func (t Task) SessionRef() string {
	if t.HerdrAgent != "" {
		return t.HerdrAgent
	}
	return t.HerdrPaneID
}

// ShortID is the prefix users can type instead of the full id.
func (t Task) ShortID() string {
	id := strings.TrimPrefix(t.ID, "task_")
	if len(id) > 12 {
		return id[len(id)-8:]
	}
	return id
}

// Age renders how long ago the task was created.
func (t Task) Age(now time.Time) string { return HumanDuration(now.Sub(t.CreatedAt)) }

// HumanDuration renders a compact age like "3m" or "2d".
func HumanDuration(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return itoa(int(d.Hours())) + "h"
	default:
		return itoa(int(d.Hours()/24)) + "d"
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
