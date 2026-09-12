package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestXDGOverridesAreHonoured(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg/config")
	t.Setenv("XDG_DATA_HOME", "/xdg/data")
	t.Setenv(EnvConfigDir, "")
	t.Setenv(EnvDataDir, "")

	if got := ConfigDir(); got != filepath.Join("/xdg/config", "dispatch") {
		t.Errorf("ConfigDir = %q", got)
	}
	if got := DataDir(); got != filepath.Join("/xdg/data", "dispatch") {
		t.Errorf("DataDir = %q", got)
	}
}

func TestExplicitOverridesWinOverXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg/config")
	t.Setenv(EnvConfigDir, "/explicit/config")
	t.Setenv(EnvDataDir, "/explicit/data")

	if got := ConfigDir(); got != "/explicit/config" {
		t.Errorf("ConfigDir = %q", got)
	}
	if got := DataDir(); got != "/explicit/data" {
		t.Errorf("DataDir = %q", got)
	}
}

func TestDefaultsFollowXDGConvention(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv(EnvConfigDir, "")
	t.Setenv(EnvDataDir, "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if got := ConfigDir(); got != filepath.Join(home, ".config", "dispatch") {
		t.Errorf("ConfigDir = %q", got)
	}
	if got := DataDir(); got != filepath.Join(home, ".local", "share", "dispatch") {
		t.Errorf("DataDir = %q", got)
	}
}

func TestWorktreesLiveUnderTheDataDirectory(t *testing.T) {
	t.Setenv(EnvDataDir, "/explicit/data")
	// Runtime state must never land inside a source repo or a dotfiles clone.
	if got := DefaultWorktreesDir(); got != filepath.Join("/explicit/data", "worktrees") {
		t.Errorf("DefaultWorktreesDir = %q", got)
	}
}

func TestExpand(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if got := Expand("~/x/y"); got != filepath.Join(home, "x", "y") {
		t.Errorf("Expand(~/x/y) = %q", got)
	}
	if got := Expand("~"); got != home {
		t.Errorf("Expand(~) = %q", got)
	}
	if got := Expand("/absolute/path"); got != "/absolute/path" {
		t.Errorf("Expand kept a path it should not touch: %q", got)
	}
	if got := Expand(""); got != "" {
		t.Errorf("Expand(empty) = %q", got)
	}
	// A bare ~ inside a path is a literal, not a home reference.
	if got := Expand("/a/~/b"); got != "/a/~/b" {
		t.Errorf("Expand(/a/~/b) = %q", got)
	}
}

func TestEnsureBaseDirsIsIdempotent(t *testing.T) {
	root := t.TempDir()
	t.Setenv(EnvConfigDir, filepath.Join(root, "config"))
	t.Setenv(EnvDataDir, filepath.Join(root, "data"))

	for i := 0; i < 2; i++ {
		if err := EnsureBaseDirs(); err != nil {
			t.Fatalf("EnsureBaseDirs (run %d): %v", i+1, err)
		}
	}
	for _, dir := range []string{ConfigDir(), RolesDir(), WorkflowsDir(), DataDir(), SessionsDir(), LogsDir()} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Errorf("%s not created: %v", dir, err)
		}
	}
}

func TestSessionDirIsPerTask(t *testing.T) {
	t.Setenv(EnvDataDir, "/data")
	got := SessionDir("task_123")
	if !strings.HasSuffix(got, filepath.Join("sessions", "task_123")) {
		t.Errorf("SessionDir = %q", got)
	}
}
