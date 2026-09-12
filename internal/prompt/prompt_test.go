package prompt

import (
	"strings"
	"testing"

	"github.com/matthewalunni/dispatch/internal/project"
	"github.com/matthewalunni/dispatch/internal/roles"
)

func engineerRole() roles.Role {
	return roles.Role{
		Name:        "engineer",
		Description: "Implementation specialist",
		Runtime:     "claude",
		Isolation:   roles.IsolationWorktree,
		Context: roles.Context{
			Repository:          true,
			DiscoverProjectDocs: true,
			GitHistory:          true,
			ExtraDiscovery:      []string{"tests that already cover nearby behaviour"},
		},
		Instructions: "Own the assigned implementation.\nKeep changes focused.",
	}
}

func sampleProject() project.Project {
	return project.Project{
		Root: "/src/repvault", Name: "repvault", IsGit: true, Branch: "main",
		Sources: []project.Source{
			{Label: "CLAUDE.md", Present: true, Kind: "instructions"},
			{Label: "docs/", Present: true, Kind: "docs"},
			{Label: "AGENTS.md", Present: false, Kind: "instructions"},
		},
	}
}

func TestBuildIncludesEverySection(t *testing.T) {
	out := Build(Request{
		Role:      engineerRole(),
		Project:   sampleProject(),
		Title:     "Implement exercise substitution",
		Isolation: roles.IsolationWorktree,
		Worktree:  "/wt/implement-exercise-substitution",
		Branch:    "dispatch/implement-exercise-substitution",
		BaseRef:   "main",
	})

	for _, want := range []string{
		"engineer",
		"## TASK",
		"Implement exercise substitution",
		"## ENVIRONMENT",
		"isolated git worktree",
		"/wt/implement-exercise-substitution",
		"dispatch/implement-exercise-substitution",
		"## BEFORE YOU BEGIN",
		"## HOW YOU WORK",
		"Own the assigned implementation.",
		"## WHEN YOU ARE UNSURE",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt is missing %q\n---\n%s", want, out)
		}
	}
}

func TestBuildNamesDiscoveredSourcesWithoutInliningThem(t *testing.T) {
	out := Build(Request{
		Role: engineerRole(), Project: sampleProject(),
		Title: "Do the thing", Isolation: roles.IsolationWorktree,
	})

	if !strings.Contains(out, "`CLAUDE.md`") || !strings.Contains(out, "`docs/`") {
		t.Errorf("present sources should be named:\n%s", out)
	}
	if strings.Contains(out, "AGENTS.md") {
		t.Error("absent sources must not be named as available context")
	}
	// The whole point is to tell the agent to go and look, not to paste.
	if !strings.Contains(out, "Discover repository-specific agent instructions") {
		t.Error("prompt should instruct discovery")
	}
	if !strings.Contains(out, "starting point, not an inventory") {
		t.Error("prompt should not present the source list as exhaustive")
	}
}

func TestBuildIsolationNoneWarnsAboutTheSharedCheckout(t *testing.T) {
	role := engineerRole()
	role.Isolation = roles.IsolationNone

	out := Build(Request{
		Role: role, Project: sampleProject(),
		Title: "Review the branch", Isolation: roles.IsolationNone,
	})

	if strings.Contains(out, "isolated git worktree") {
		t.Error("a non-isolated task must not claim to be in a worktree")
	}
	if !strings.Contains(out, "not isolated") {
		t.Errorf("prompt should warn that the checkout is shared:\n%s", out)
	}
	if !strings.Contains(out, "do not switch branches") {
		t.Error("prompt should tell the agent not to disturb the shared checkout")
	}
}

func TestBuildNonGitDirectory(t *testing.T) {
	proj := project.Project{Root: "/tmp/scratch", Name: "scratch", IsGit: false}
	out := Build(Request{
		Role: engineerRole(), Project: proj,
		Title: "Research options", Isolation: roles.IsolationNone,
	})
	if !strings.Contains(out, "not a git repository") {
		t.Errorf("prompt should say the directory is not a repository:\n%s", out)
	}
}

func TestBuildIncludesDescriptionAndProjectInstructions(t *testing.T) {
	out := Build(Request{
		Role: engineerRole(), Project: sampleProject(),
		Title:               "Add the endpoint",
		Description:         "It must accept a cursor parameter.",
		Isolation:           roles.IsolationNone,
		ProjectInstructions: "Prefer the design system primitives.",
	})
	if !strings.Contains(out, "It must accept a cursor parameter.") {
		t.Error("description not included")
	}
	if !strings.Contains(out, "## PROJECT INSTRUCTIONS") || !strings.Contains(out, "design system primitives") {
		t.Errorf("project instructions not included:\n%s", out)
	}
}

func TestBuildOmitsProjectInstructionsWhenAbsent(t *testing.T) {
	out := Build(Request{
		Role: engineerRole(), Project: sampleProject(),
		Title: "Add the endpoint", Isolation: roles.IsolationNone,
	})
	if strings.Contains(out, "## PROJECT INSTRUCTIONS") {
		t.Error("an empty project instruction block should be omitted entirely")
	}
}

func TestBuildMentionsParentLineage(t *testing.T) {
	out := Build(Request{
		Role: engineerRole(), Project: sampleProject(),
		Title: "Build the screens", Isolation: roles.IsolationNone,
		ParentTaskID: "task_parent_123",
	})
	if !strings.Contains(out, "task_parent_123") {
		t.Error("a child task's prompt should mention its parent")
	}
}

func TestDesignerAndEngineerPromptsDiffer(t *testing.T) {
	designer := roles.Role{
		Name: "designer", Description: "Design specialist", Runtime: "claude",
		Isolation: roles.IsolationNone,
		Context: roles.Context{
			Repository: true, DiscoverProjectDocs: true,
			ExtraDiscovery: []string{"design tokens and theme definitions"},
		},
		Instructions: "Inspect the existing product before proposing anything.",
	}

	engineerPrompt := Build(Request{Role: engineerRole(), Project: sampleProject(), Title: "X", Isolation: roles.IsolationWorktree})
	designerPrompt := Build(Request{Role: designer, Project: sampleProject(), Title: "X", Isolation: roles.IsolationNone})

	if engineerPrompt == designerPrompt {
		t.Fatal("roles with different responsibilities must produce different prompts")
	}
	if !strings.Contains(designerPrompt, "design tokens and theme definitions") {
		t.Error("role extra_discovery should reach the prompt")
	}
	if strings.Contains(designerPrompt, "Own the assigned implementation") {
		t.Error("designer prompt leaked engineer instructions")
	}
}

func TestBuildSkipsDiscoveryWhenRoleWantsNoContext(t *testing.T) {
	role := roles.Role{
		Name: "consultant", Description: "Consultant", Runtime: "claude",
		Isolation: roles.IsolationNone, Instructions: "Advise.",
	}
	out := Build(Request{Role: role, Project: sampleProject(), Title: "Advise on X", Isolation: roles.IsolationNone})
	if strings.Contains(out, "## BEFORE YOU BEGIN") {
		t.Errorf("a role with no context requirements should get no discovery section:\n%s", out)
	}
	if !strings.Contains(out, "Advise.") {
		t.Error("role instructions missing")
	}
}

func TestBuildEndsWithASingleNewline(t *testing.T) {
	out := Build(Request{Role: engineerRole(), Project: sampleProject(), Title: "X", Isolation: roles.IsolationNone})
	if !strings.HasSuffix(out, "\n") || strings.HasSuffix(out, "\n\n") {
		t.Error("prompt should end with exactly one trailing newline")
	}
}

func TestBuildDoesNotLeakMetadataIntoThePrompt(t *testing.T) {
	out := Build(Request{
		Role: engineerRole(), Project: sampleProject(),
		Title: "Add the endpoint", Isolation: roles.IsolationWorktree,
		Worktree: "/wt/x", Branch: "dispatch/x", BaseRef: "main",
	})
	// Dispatch's own bookkeeping has no business in an agent's instructions.
	for _, leak := range []string{"task_", "herdr", "pane", "sqlite"} {
		if strings.Contains(strings.ToLower(out), leak) {
			t.Errorf("prompt leaks orchestration metadata %q:\n%s", leak, out)
		}
	}
}
