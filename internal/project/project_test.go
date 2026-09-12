package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/matthewalunni/dispatch/internal/fakes"
)

func TestDetectGitRepository(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "packages", "ui")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	git := fakes.NewGit(root)
	git.Branch = "feature/x"
	git.Dirty = true

	proj, err := Detect(git, sub)
	if err != nil {
		t.Fatal(err)
	}
	if proj.Root != root {
		t.Errorf("Root = %q, want the repository root %q", proj.Root, root)
	}
	if proj.Name != filepath.Base(root) {
		t.Errorf("Name = %q", proj.Name)
	}
	if !proj.IsGit || proj.Branch != "feature/x" || !proj.Dirty {
		t.Errorf("git state wrong: %+v", proj)
	}
	if proj.DetachedHEAD {
		t.Error("DetachedHEAD should be false on a named branch")
	}
}

func TestDetectNonGitDirectoryStillWorks(t *testing.T) {
	dir := t.TempDir()
	git := fakes.NewGit(filepath.Join(t.TempDir(), "elsewhere"))

	proj, err := Detect(git, dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if proj.IsGit {
		t.Error("IsGit should be false outside a repository")
	}
	if proj.Root != dir {
		t.Errorf("Root = %q, want the working directory %q", proj.Root, dir)
	}
	if proj.Name == "" {
		t.Error("Name should still be populated")
	}
}

func TestDetectDetachedHead(t *testing.T) {
	root := t.TempDir()
	git := fakes.NewGit(root)
	git.Branch = ""

	proj, err := Detect(git, root)
	if err != nil {
		t.Fatal(err)
	}
	if !proj.DetachedHEAD {
		t.Error("an empty branch should be reported as a detached HEAD")
	}
}

func TestDetectContextSources(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"CLAUDE.md", "README.md", "package.json"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}

	proj, err := Detect(fakes.NewGit(root), root)
	if err != nil {
		t.Fatal(err)
	}

	present := map[string]bool{}
	for _, source := range proj.PresentSources() {
		present[source.Label] = true
	}
	for _, want := range []string{"CLAUDE.md", "README", "docs/", "package manifest"} {
		if !present[want] {
			t.Errorf("context source %q not detected", want)
		}
	}
	if present["AGENTS.md"] {
		t.Error("AGENTS.md reported present when it does not exist")
	}
	// Absent sources are still listed, so doctor can show them as missing.
	if len(proj.Sources) <= len(proj.PresentSources()) {
		t.Error("Sources should include absent candidates too")
	}
}

func TestDetectProjectOverrideDirectory(t *testing.T) {
	root := t.TempDir()
	proj, err := Detect(fakes.NewGit(root), root)
	if err != nil {
		t.Fatal(err)
	}
	if proj.HasDispatchDir {
		t.Error("HasDispatchDir should be false without .dispatch/")
	}

	if err := os.MkdirAll(filepath.Join(root, ".dispatch", "roles"), 0o755); err != nil {
		t.Fatal(err)
	}
	proj, err = Detect(fakes.NewGit(root), root)
	if err != nil {
		t.Fatal(err)
	}
	if !proj.HasDispatchDir {
		t.Error("HasDispatchDir should be true with .dispatch/")
	}
	if proj.ConfigFile() != filepath.Join(root, ".dispatch", "config.yaml") {
		t.Errorf("ConfigFile = %q", proj.ConfigFile())
	}
	if proj.RolesDir() != filepath.Join(root, ".dispatch", "roles") {
		t.Errorf("RolesDir = %q", proj.RolesDir())
	}
}

func TestGlobPatternMatchesXcodeProject(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "MyApp.xcodeproj"), 0o755); err != nil {
		t.Fatal(err)
	}
	proj, err := Detect(fakes.NewGit(root), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range proj.Sources {
		if source.Label == "package manifest" && !source.Present {
			t.Error("an .xcodeproj should count as a package manifest")
		}
	}
}
