package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultRole != "general" || cfg.DefaultRuntime != "claude" {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if cfg.BranchPrefix != "dispatch/" {
		t.Errorf("BranchPrefix = %q", cfg.BranchPrefix)
	}
	if len(cfg.Sources) != 0 {
		t.Errorf("Sources = %v, want empty for a missing file", cfg.Sources)
	}
}

func TestLoadOverridesOnlyStatedFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, "default_role: engineer\nbranch_prefix: wip/\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultRole != "engineer" {
		t.Errorf("DefaultRole = %q", cfg.DefaultRole)
	}
	if cfg.BranchPrefix != "wip/" {
		t.Errorf("BranchPrefix = %q", cfg.BranchPrefix)
	}
	// Untouched fields keep their defaults rather than going zero.
	if cfg.DefaultRuntime != "claude" {
		t.Errorf("DefaultRuntime = %q, want the default to survive", cfg.DefaultRuntime)
	}
	if cfg.Herdr.StartTimeoutMS != 60000 {
		t.Errorf("StartTimeoutMS = %d", cfg.Herdr.StartTimeoutMS)
	}
}

func TestApplyProjectLayersOverGlobal(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.yaml")
	writeFile(t, global, "default_role: engineer\nbranch_prefix: wip/\nherdr:\n  session: work\n")

	projectDir := t.TempDir()
	project := filepath.Join(projectDir, ".dispatch", "config.yaml")
	writeFile(t, project, "default_role: reviewer\nproject_instructions: |\n  Use the design system.\n")

	cfg, err := Load(global)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := cfg.ApplyProject(project)
	if err != nil {
		t.Fatal(err)
	}

	if merged.DefaultRole != "reviewer" {
		t.Errorf("project should win: DefaultRole = %q", merged.DefaultRole)
	}
	if merged.BranchPrefix != "wip/" {
		t.Errorf("global should survive: BranchPrefix = %q", merged.BranchPrefix)
	}
	if merged.Herdr.Session != "work" {
		t.Errorf("global herdr settings should survive: %q", merged.Herdr.Session)
	}
	if merged.ProjectInstructions == "" {
		t.Error("ProjectInstructions not loaded from the project layer")
	}
	if len(merged.Sources) != 2 {
		t.Errorf("Sources = %v, want both layers", merged.Sources)
	}
	// The global config must not be mutated by layering a project over it.
	if cfg.DefaultRole != "engineer" {
		t.Errorf("ApplyProject mutated the receiver: %q", cfg.DefaultRole)
	}
}

func TestApplyProjectMissingFileIsNotAnError(t *testing.T) {
	cfg := Defaults()
	merged, err := cfg.ApplyProject(filepath.Join(t.TempDir(), ".dispatch", "config.yaml"))
	if err != nil {
		t.Fatalf("ApplyProject: %v", err)
	}
	if merged.DefaultRole != cfg.DefaultRole {
		t.Error("a missing project config changed the configuration")
	}
}

func TestProjectRuntimesAugmentRatherThanReplace(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "config.yaml")
	writeFile(t, project, "runtimes:\n  aider:\n    kind: opencode\n    binary: aider\n")

	cfg, err := Defaults().ApplyProject(project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Runtime("claude"); err != nil {
		t.Errorf("global runtime lost after project override: %v", err)
	}
	rt, err := cfg.Runtime("aider")
	if err != nil {
		t.Fatalf("project runtime missing: %v", err)
	}
	if rt.Kind != "opencode" || rt.Binary != "aider" {
		t.Errorf("runtime = %+v", rt)
	}
}

func TestRuntimeDefaultsKindAndBinaryToItsName(t *testing.T) {
	cfg := Defaults()
	cfg.Runtimes["custom"] = RuntimeConfig{}
	rt, err := cfg.Runtime("custom")
	if err != nil {
		t.Fatal(err)
	}
	if rt.Kind != "custom" || rt.Binary != "custom" {
		t.Errorf("runtime = %+v, want kind and binary to default to the name", rt)
	}
}

func TestUnknownRuntimeListsWhatExists(t *testing.T) {
	_, err := Defaults().Runtime("nope")
	if err == nil {
		t.Fatal("expected an error for an unknown runtime")
	}
	if got := err.Error(); got == "" || !contains(got, "claude") {
		t.Errorf("error should list configured runtimes, got %q", got)
	}
}

func TestSeedIsIdempotentAndNeverClobbers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	created, err := Seed(path)
	if err != nil || !created {
		t.Fatalf("Seed: created=%v err=%v", created, err)
	}
	writeFile(t, path, "default_role: mine\n")

	created, err = Seed(path)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("Seed overwrote an existing config")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "default_role: mine\n" {
		t.Errorf("Seed clobbered user edits: %q", data)
	}
}

func TestParseErrorMentionsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, path, "default_role: [unclosed\n")
	if _, err := Load(path); err == nil {
		t.Fatal("expected a parse error")
	} else if !contains(err.Error(), path) {
		t.Errorf("error should name the file: %v", err)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

func TestLayoutDefaultsToWorkspace(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// Each task getting its own herdr workspace is the default; tabs are the
	// opt-in for people who want everything packed together.
	if cfg.Herdr.Layout != "workspace" {
		t.Errorf("Layout = %q, want workspace", cfg.Herdr.Layout)
	}
	if cfg.Herdr.TrustRepository {
		t.Error("git trust must be off unless explicitly enabled")
	}
}

func TestLayoutAndTrustAreConfigurable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, "herdr:\n  layout: tab\n  trust_repository: true\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Herdr.Layout != "tab" || !cfg.Herdr.TrustRepository {
		t.Errorf("herdr config = %+v", cfg.Herdr)
	}
	// Other herdr defaults must survive setting just these.
	if cfg.Herdr.StartTimeoutMS != 60000 || cfg.Herdr.Binary != "herdr" {
		t.Errorf("herdr defaults lost: %+v", cfg.Herdr)
	}
}

func TestProjectCanChooseItsOwnLayout(t *testing.T) {
	global := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, global, "herdr:\n  layout: workspace\n  session: work\n")
	project := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, project, "herdr:\n  layout: tab\n")

	cfg, err := Load(global)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := cfg.ApplyProject(project)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Herdr.Layout != "tab" {
		t.Errorf("Layout = %q, want the project override", merged.Herdr.Layout)
	}
	if merged.Herdr.Session != "work" {
		t.Errorf("Session = %q, want the global value to survive", merged.Herdr.Session)
	}
}

func TestEmptyLayoutNormalizesToWorkspace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, path, "herdr:\n  binary: herdr\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Herdr.Layout != "workspace" {
		t.Errorf("Layout = %q", cfg.Herdr.Layout)
	}
}

func TestDefaultIsolationIsWorktree(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultIsolation != "worktree" {
		t.Errorf("DefaultIsolation = %q, want worktree out of the box", cfg.DefaultIsolation)
	}
}

func TestDefaultIsolationCanBeCleared(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	// An empty value is a decision, not an omission: it hands isolation back
	// to each role, so it must survive the merge instead of falling through
	// to the built-in default.
	writeFile(t, path, "default_isolation: \"\"\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultIsolation != "" {
		t.Errorf("DefaultIsolation = %q, want it cleared", cfg.DefaultIsolation)
	}
}

func TestProjectCanChooseItsOwnDefaultIsolation(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.yaml")
	writeFile(t, global, "default_isolation: worktree\n")
	project := filepath.Join(dir, "repo", ".dispatch", "config.yaml")
	writeFile(t, project, "default_isolation: none\n")

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg, err = cfg.ApplyProject(project)
	if err != nil {
		t.Fatalf("ApplyProject: %v", err)
	}
	if cfg.DefaultIsolation != "none" {
		t.Errorf("DefaultIsolation = %q, want the project's choice", cfg.DefaultIsolation)
	}
}

func TestSeededConfigStatesTheDefaultIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if _, err := Seed(path); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultIsolation != "worktree" {
		t.Errorf("DefaultIsolation = %q, want the seeded file to agree with the built-in default", cfg.DefaultIsolation)
	}
}
