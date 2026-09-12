package roles

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSeedWritesDefaultsAndDoesNotClobber(t *testing.T) {
	dir := t.TempDir()

	written, err := Seed(dir)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if len(written) == 0 {
		t.Fatal("Seed wrote nothing")
	}

	// A user edit must survive a second run.
	custom := "name: engineer\ndescription: mine\nruntime: claude\nisolation: none\ninstructions: |\n  Mine.\n"
	write(t, dir, "engineer.yaml", custom)

	if _, err := Seed(dir); err != nil {
		t.Fatalf("second Seed: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "engineer.yaml"))
	if string(data) != custom {
		t.Error("Seed overwrote a user-modified role")
	}
}

func TestSeededDefaultRolesAreValid(t *testing.T) {
	dir := t.TempDir()
	if _, err := Seed(dir); err != nil {
		t.Fatal(err)
	}
	set, err := Resolve(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"engineer", "designer", "reviewer", "general"} {
		role, err := set.Get(want)
		if err != nil {
			t.Errorf("default role %q: %v", want, err)
			continue
		}
		if err := role.Validate(); err != nil {
			t.Errorf("default role %q is invalid: %v", want, err)
		}
	}
}

func TestDefaultRoleIsolationMatchesTheSpecifiedBehaviour(t *testing.T) {
	dir := t.TempDir()
	if _, err := Seed(dir); err != nil {
		t.Fatal(err)
	}
	set, _ := Resolve(dir, "")

	// Only the engineer needs a mutable, isolated checkout; roles that
	// inspect or advise must not create worktrees.
	want := map[string]IsolationMode{
		"engineer": IsolationWorktree,
		"designer": IsolationNone,
		"reviewer": IsolationNone,
		"general":  IsolationNone,
	}
	for name, mode := range want {
		role, err := set.Get(name)
		if err != nil {
			t.Fatal(err)
		}
		if role.Isolation != mode {
			t.Errorf("%s isolation = %q, want %q", name, role.Isolation, mode)
		}
		if !role.Context.Repository {
			t.Errorf("%s should carry repository context", name)
		}
	}
}

func TestProjectRoleOverridesGlobalFieldByField(t *testing.T) {
	globalDir := t.TempDir()
	write(t, globalDir, "engineer.yaml", `name: engineer
description: Implementation specialist
runtime: claude
isolation: worktree
context:
  repository: true
  discover_project_docs: true
  extra_discovery:
    - existing tests
instructions: |
  Global instructions.
`)

	projectDir := t.TempDir()
	write(t, projectDir, "engineer.yaml", `name: engineer
runtime: codex
context:
  extra_discovery:
    - the packages/ui design system
instructions_append: |
  Run npm test before finishing.
`)

	set, err := Resolve(globalDir, projectDir)
	if err != nil {
		t.Fatal(err)
	}
	role, err := set.Get("engineer")
	if err != nil {
		t.Fatal(err)
	}

	if role.Runtime != "codex" {
		t.Errorf("project override lost: runtime = %q", role.Runtime)
	}
	if role.Isolation != IsolationWorktree {
		t.Errorf("global value should survive: isolation = %q", role.Isolation)
	}
	if role.Description != "Implementation specialist" {
		t.Errorf("global description should survive: %q", role.Description)
	}
	if !strings.Contains(role.Instructions, "Global instructions.") {
		t.Error("instructions_append dropped the global instructions")
	}
	if !strings.Contains(role.Instructions, "Run npm test before finishing.") {
		t.Error("instructions_append did not append")
	}
	if len(role.Context.ExtraDiscovery) != 2 {
		t.Errorf("extra_discovery should merge, got %v", role.Context.ExtraDiscovery)
	}
	if role.Origin != "merged" {
		t.Errorf("Origin = %q, want merged", role.Origin)
	}
}

func TestProjectOnlyRoleNeedsNoGlobalCounterpart(t *testing.T) {
	globalDir := t.TempDir()
	if _, err := Seed(globalDir); err != nil {
		t.Fatal(err)
	}
	projectDir := t.TempDir()
	write(t, projectDir, "ios-engineer.yaml", `name: ios-engineer
description: iOS specialist
runtime: claude
isolation: worktree
instructions: |
  Match existing SwiftUI conventions.
`)

	set, err := Resolve(globalDir, projectDir)
	if err != nil {
		t.Fatal(err)
	}
	role, err := set.Get("ios-engineer")
	if err != nil {
		t.Fatalf("project-only role not resolved: %v", err)
	}
	if role.Origin != "project" {
		t.Errorf("Origin = %q", role.Origin)
	}
	// The global roles must still be there alongside it.
	if _, err := set.Get("engineer"); err != nil {
		t.Errorf("global roles lost: %v", err)
	}
}

func TestUnknownRoleListsAvailable(t *testing.T) {
	dir := t.TempDir()
	if _, err := Seed(dir); err != nil {
		t.Fatal(err)
	}
	set, _ := Resolve(dir, "")

	_, err := set.Get("wizard")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error should unwrap to ErrNotFound: %v", err)
	}
	var unknown *UnknownRoleError
	if !errors.As(err, &unknown) {
		t.Fatalf("error should be an UnknownRoleError: %v", err)
	}
	if len(unknown.Available) < 4 {
		t.Errorf("Available = %v, want the installed roles", unknown.Available)
	}
}

func TestResolveIgnoresNonYAMLFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "README.md", "# not a role")
	write(t, dir, "engineer.yaml", "name: engineer\nruntime: claude\nisolation: none\ninstructions: go\n")

	set, err := Resolve(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if set.Len() != 1 {
		t.Errorf("Len = %d, want only the YAML role", set.Len())
	}
}

func TestResolveMissingDirectoriesIsNotAnError(t *testing.T) {
	set, err := Resolve(filepath.Join(t.TempDir(), "nope"), filepath.Join(t.TempDir(), "also-nope"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if set.Len() != 0 {
		t.Errorf("Len = %d", set.Len())
	}
}

func TestValidateRejectsIncompleteRoles(t *testing.T) {
	cases := map[string]Role{
		"no name":         {Runtime: "claude", Isolation: IsolationNone, Instructions: "x"},
		"no runtime":      {Name: "a", Isolation: IsolationNone, Instructions: "x"},
		"no isolation":    {Name: "a", Runtime: "claude", Instructions: "x"},
		"bad isolation":   {Name: "a", Runtime: "claude", Isolation: "container", Instructions: "x"},
		"no instructions": {Name: "a", Runtime: "claude", Isolation: IsolationNone},
		"bad name":        {Name: "Not Valid", Runtime: "claude", Isolation: IsolationNone, Instructions: "x"},
	}
	for label, role := range cases {
		if err := role.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", label)
		}
	}
}

func TestTemplateProducesAValidStartingPoint(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "security-reviewer.yaml", Template("security-reviewer"))

	set, err := Resolve(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	role, err := set.Get("security-reviewer")
	if err != nil {
		t.Fatalf("generated template does not load: %v", err)
	}
	if err := role.Validate(); err != nil {
		t.Errorf("generated template is not valid: %v", err)
	}
}

func TestParseIsolation(t *testing.T) {
	if _, err := ParseIsolation("worktree"); err != nil {
		t.Errorf("worktree should parse: %v", err)
	}
	if _, err := ParseIsolation("container"); err == nil {
		t.Error("unknown mode should fail")
	} else if !strings.Contains(err.Error(), "worktree") {
		t.Errorf("error should list supported modes: %v", err)
	}
}

func TestNeedsRepository(t *testing.T) {
	if !IsolationWorktree.NeedsRepository() {
		t.Error("worktree isolation needs a repository")
	}
	if IsolationNone.NeedsRepository() {
		t.Error("none isolation must not require a repository")
	}
}

func TestSchemaCoversEveryRequiredField(t *testing.T) {
	schema := Describe()
	required := map[string]bool{"name": false, "runtime": false, "isolation": false, "instructions": false}
	for _, field := range schema.Fields {
		if _, ok := required[field.Name]; ok {
			required[field.Name] = field.Required
		}
	}
	for name, ok := range required {
		if !ok {
			t.Errorf("schema does not mark %q required", name)
		}
	}
}
