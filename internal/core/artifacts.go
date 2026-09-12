package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/matthewalunni/dispatch/internal/store"
)

// SessionDir is where a task's portable artifacts live.
func SessionDir(dataDir, taskID string) string {
	return filepath.Join(dataDir, "sessions", taskID)
}

// PromptPath is the saved initial prompt for a task.
func PromptPath(dataDir, taskID string) string {
	return filepath.Join(SessionDir(dataDir, taskID), "initial-prompt.md")
}

// MetadataPath is the portable per-task metadata file.
func MetadataPath(dataDir, taskID string) string {
	return filepath.Join(SessionDir(dataDir, taskID), "metadata.json")
}

// ResultPath is where a task's outcome is recorded when it finishes.
func ResultPath(dataDir, taskID string) string {
	return filepath.Join(SessionDir(dataDir, taskID), "result.json")
}

// WriteArtifacts persists the human- and machine-readable record of a task.
//
// SQLite stays the canonical registry; these files exist so a task is portable
// and inspectable without the database (and so the initial prompt can be
// re-read or re-pasted).
func WriteArtifacts(dataDir string, task store.Task, initialPrompt string) error {
	dir := SessionDir(dataDir, task.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}
	if initialPrompt != "" {
		if err := os.WriteFile(PromptPath(dataDir, task.ID), []byte(initialPrompt), 0o644); err != nil {
			return err
		}
	}
	return WriteMetadata(dataDir, task)
}

// WriteMetadata refreshes metadata.json for a task.
func WriteMetadata(dataDir string, task store.Task) error {
	dir := SessionDir(dataDir, task.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(MetadataPath(dataDir, task.ID), append(data, '\n'), 0o644)
}

// Result records how a task ended.
type Result struct {
	TaskID     string    `json:"task_id"`
	Status     string    `json:"status"`
	Detail     string    `json:"detail,omitempty"`
	Branch     string    `json:"branch,omitempty"`
	Worktree   string    `json:"worktree,omitempty"`
	RecordedAt time.Time `json:"recorded_at"`
}

// WriteResult records a task's outcome alongside its other artifacts.
func WriteResult(dataDir string, task store.Task, detail string) error {
	dir := SessionDir(dataDir, task.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	result := Result{
		TaskID:     task.ID,
		Status:     string(task.Status),
		Detail:     detail,
		Branch:     task.Branch,
		Worktree:   task.Worktree,
		RecordedAt: time.Now().UTC(),
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ResultPath(dataDir, task.ID), append(data, '\n'), 0o644)
}

// ReadPrompt loads a saved initial prompt.
func ReadPrompt(dataDir, taskID string) (string, error) {
	data, err := os.ReadFile(PromptPath(dataDir, taskID))
	if err != nil {
		return "", err
	}
	return string(data), nil
}
