// Package paths resolves the XDG-style locations dispatch uses on disk.
//
// Every location can be overridden with an environment variable so that tests
// (and users with unusual setups) never have to touch the real home directory.
package paths

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	EnvConfigDir = "DISPATCH_CONFIG_DIR"
	EnvDataDir   = "DISPATCH_DATA_DIR"
)

// ConfigDir is where config.yaml, roles/ and workflows/ live.
func ConfigDir() string {
	if dir := os.Getenv(EnvConfigDir); dir != "" {
		return Expand(dir)
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(Expand(dir), "dispatch")
	}
	return filepath.Join(home(), ".config", "dispatch")
}

// DataDir is where the database, session artifacts, logs and worktrees live.
func DataDir() string {
	if dir := os.Getenv(EnvDataDir); dir != "" {
		return Expand(dir)
	}
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(Expand(dir), "dispatch")
	}
	return filepath.Join(home(), ".local", "share", "dispatch")
}

func ConfigFile() string   { return filepath.Join(ConfigDir(), "config.yaml") }
func RolesDir() string     { return filepath.Join(ConfigDir(), "roles") }
func WorkflowsDir() string { return filepath.Join(ConfigDir(), "workflows") }

func DBPath() string      { return filepath.Join(DataDir(), "dispatch.db") }
func SessionsDir() string { return filepath.Join(DataDir(), "sessions") }
func LogsDir() string     { return filepath.Join(DataDir(), "logs") }

// DefaultWorktreesDir is the fallback location for isolated worktrees. It
// deliberately lives under the data dir so nothing is ever written into the
// user's source repository or dotfiles.
func DefaultWorktreesDir() string { return filepath.Join(DataDir(), "worktrees") }

// SessionDir holds the per-task artifacts (metadata.json, initial-prompt.md...).
func SessionDir(taskID string) string { return filepath.Join(SessionsDir(), taskID) }

// ProjectConfigDirName is the optional per-repository override directory.
const ProjectConfigDirName = ".dispatch"

// EnsureBaseDirs creates the directories dispatch needs to operate. It is safe
// to call on every run.
func EnsureBaseDirs() error {
	for _, dir := range []string{ConfigDir(), RolesDir(), WorkflowsDir(), DataDir(), SessionsDir(), LogsDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// Expand resolves a leading ~ and any environment variables in a path.
func Expand(path string) string {
	if path == "" {
		return ""
	}
	path = os.ExpandEnv(path)
	if path == "~" {
		return home()
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home(), path[2:])
	}
	return path
}

func home() string {
	if dir, err := os.UserHomeDir(); err == nil && dir != "" {
		return dir
	}
	if dir := os.Getenv("HOME"); dir != "" {
		return dir
	}
	return "."
}
