package clix

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/matthewalunni/dispatch/internal/core"
	"github.com/matthewalunni/dispatch/internal/herdrx"
	"github.com/matthewalunni/dispatch/internal/roles"
	"github.com/matthewalunni/dispatch/internal/store"
)

func sampleView() core.TaskView {
	created := time.Date(2026, 9, 12, 10, 15, 0, 0, time.UTC)
	return core.TaskView{
		Task: store.Task{
			ID:               "task_20260912T101500_abcd1234",
			Title:            "Implement feature X",
			Slug:             "implement-feature-x",
			ProjectRoot:      "/path/to/project",
			ProjectName:      "project",
			Role:             "engineer",
			Runtime:          "claude",
			Isolation:        roles.IsolationWorktree,
			Status:           store.StatusRunning,
			Branch:           "dispatch/implement-feature-x",
			Worktree:         "/path/to/worktree",
			BaseRef:          "main",
			HerdrAgent:       "implement-feature-x",
			HerdrPaneID:      "w1:p3",
			HerdrTabID:       "w1:t3",
			HerdrWorkspaceID: "w1",
			CreatedAt:        created,
			UpdatedAt:        created,
		},
		AgentStatus: herdrx.StatusWorking,
		AgentAlive:  true,
		Effective:   core.EffectiveWorking,
	}
}

// The JSON shape is a contract with external orchestrators, so the field names
// an orchestrator would key on are asserted explicitly.
func TestTaskJSONContract(t *testing.T) {
	data, err := json.Marshal(toJSON(sampleView(), "/data"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	want := map[string]any{
		"id":           "task_20260912T101500_abcd1234",
		"title":        "Implement feature X",
		"role":         "engineer",
		"status":       "working",
		"project_root": "/path/to/project",
		"branch":       "dispatch/implement-feature-x",
		"worktree":     "/path/to/worktree",
		"herdr_agent":  "implement-feature-x",
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %v, want %v", key, got[key], value)
		}
	}
	if got["agent_alive"] != true {
		t.Errorf("agent_alive = %v", got["agent_alive"])
	}
	// Both views of status must be available, not just the resolved one.
	if got["dispatch_status"] != "running" || got["agent_status"] != "working" {
		t.Errorf("status fields = %v / %v", got["dispatch_status"], got["agent_status"])
	}
	if got["prompt_path"] == "" || got["session_dir"] == "" {
		t.Error("artifact paths should be addressable from JSON output")
	}
	if got["created_at"] != "2026-09-12T10:15:00Z" {
		t.Errorf("created_at = %v, want RFC 3339", got["created_at"])
	}
}

func TestTaskJSONOmitsEmptyOptionalFields(t *testing.T) {
	view := sampleView()
	view.Worktree = ""
	view.Branch = ""
	view.ParentTaskID = ""
	view.Isolation = roles.IsolationNone

	data, err := json.Marshal(toJSON(view, ""))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"worktree", "branch", "parent_task_id", "error"} {
		if _, present := got[key]; present {
			t.Errorf("%q should be omitted when empty", key)
		}
	}
	// Required fields stay present even when a task is not isolated.
	for _, key := range []string{"id", "status", "isolation", "project_root"} {
		if _, present := got[key]; !present {
			t.Errorf("%q must always be present", key)
		}
	}
}

func TestTaskJSONIncludesLineage(t *testing.T) {
	view := sampleView()
	view.ParentTaskID = "task_parent"
	data, _ := json.Marshal(toJSON(view, ""))
	var got map[string]any
	_ = json.Unmarshal(data, &got)
	if got["parent_task_id"] != "task_parent" {
		t.Errorf("parent_task_id = %v", got["parent_task_id"])
	}
}

func TestTaskJSONListRoundTrips(t *testing.T) {
	list := toJSONList([]core.TaskView{sampleView(), sampleView()}, "/data")
	data, err := json.Marshal(map[string]any{"tasks": list})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tasks) != 2 {
		t.Errorf("tasks = %d", len(got.Tasks))
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"short", 10, "short"},
		{"exactly-10", 10, "exactly-10"},
		{"much too long for this", 10, "much too…"}, // trailing space trimmed before the ellipsis
		{"日本語のテキストです", 5, "日本語の…"},
	}
	for _, tc := range cases {
		if got := truncate(tc.in, tc.max); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
		if len([]rune(truncate(tc.in, tc.max))) > tc.max {
			t.Errorf("truncate(%q, %d) exceeded the limit", tc.in, tc.max)
		}
	}
}
