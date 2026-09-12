package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/matthewalunni/dispatch/internal/config"
)

func TestResolveBuiltInRuntime(t *testing.T) {
	rt, err := Resolve(config.Defaults(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if rt.Name() != "claude" || rt.AgentKind() != "claude" || rt.Binary() != "claude" {
		t.Errorf("runtime = %s/%s/%s", rt.Name(), rt.AgentKind(), rt.Binary())
	}
	if !rt.PromptOnLaunch() {
		t.Error("claude should receive its assignment as an interactive prompt")
	}
}

// Adding a terminal agent must be a configuration change, not a code change.
func TestResolveConfiguredRuntimeNeedsNoCode(t *testing.T) {
	cfg := config.Defaults()
	cfg.Runtimes["my-agent"] = config.RuntimeConfig{
		Kind: "opencode", Binary: "my-agent-bin", Args: []string{"--fast"},
	}

	rt, err := Resolve(cfg, "my-agent")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if rt.AgentKind() != "opencode" {
		t.Errorf("AgentKind = %q, want the herdr kind from config", rt.AgentKind())
	}
	if rt.Binary() != "my-agent-bin" {
		t.Errorf("Binary = %q", rt.Binary())
	}
	args := rt.Args(LaunchSpec{RoleArgs: []string{"--model", "opus"}})
	want := []string{"--fast", "--model", "opus"}
	if len(args) != len(want) {
		t.Fatalf("Args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("Args = %v, want %v (runtime args first, then the role's)", args, want)
		}
	}
}

func TestPromptOnLaunchCanBeDisabled(t *testing.T) {
	off := false
	cfg := config.Defaults()
	cfg.Runtimes["batch"] = config.RuntimeConfig{Kind: "codex", PromptOnLaunch: &off}

	rt, err := Resolve(cfg, "batch")
	if err != nil {
		t.Fatal(err)
	}
	if rt.PromptOnLaunch() {
		t.Error("prompt_on_launch: false was ignored")
	}
}

func TestResolveUnknownRuntime(t *testing.T) {
	if _, err := Resolve(config.Defaults(), "nope"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestInstalledFindsAndReportsBinaries(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "my-agent-bin")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	cfg := config.Defaults()
	cfg.Runtimes["mine"] = config.RuntimeConfig{Kind: "opencode", Binary: "my-agent-bin"}
	rt, _ := Resolve(cfg, "mine")

	path, err := Installed(rt)
	if err != nil {
		t.Fatalf("Installed: %v", err)
	}
	if path != stub {
		t.Errorf("path = %q, want %q", path, stub)
	}

	cfg.Runtimes["missing"] = config.RuntimeConfig{Kind: "codex", Binary: "definitely-not-installed"}
	rtMissing, _ := Resolve(cfg, "missing")
	if _, err := Installed(rtMissing); err == nil {
		t.Error("expected an error for a missing binary")
	}
}
