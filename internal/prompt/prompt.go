// Package prompt composes the initial assignment sent to a launched agent.
//
// The guiding rule: tell the agent where it is and what to go and read. Never
// concatenate the repository into the prompt — the agent has a whole terminal
// and can inspect the project far better than dispatch can summarise it.
package prompt

import (
	"fmt"
	"strings"

	"github.com/matthewalunni/dispatch/internal/project"
	"github.com/matthewalunni/dispatch/internal/roles"
)

// Request is everything the builder needs.
type Request struct {
	Role    roles.Role
	Project project.Project

	Title       string
	Description string

	Isolation roles.IsolationMode
	Worktree  string
	Branch    string
	BaseRef   string

	// ProjectInstructions come from an optional .dispatch/config.yaml.
	ProjectInstructions string

	// ParentTaskID is set for a dispatch created underneath another task.
	ParentTaskID string
}

// Build renders the initial prompt as markdown.
func Build(req Request) string {
	var b strings.Builder

	b.WriteString(opening(req))
	b.WriteString("\n\n")

	section(&b, "TASK", taskBody(req))

	if env := environment(req); env != "" {
		section(&b, "ENVIRONMENT", env)
	}

	if discovery := discovery(req); discovery != "" {
		section(&b, "BEFORE YOU BEGIN", discovery)
	}

	section(&b, "HOW YOU WORK", strings.TrimSpace(req.Role.Instructions))

	if instructions := strings.TrimSpace(req.ProjectInstructions); instructions != "" {
		section(&b, "PROJECT INSTRUCTIONS", instructions)
	}

	section(&b, "WHEN YOU ARE UNSURE", strings.TrimSpace(`
If product intent or project conventions remain ambiguous after you have
looked, say so explicitly and ask. Do not invent a rule and proceed as though
the project had stated it.`))

	return strings.TrimRight(b.String(), "\n") + "\n"
}

func opening(req Request) string {
	desc := strings.TrimSpace(req.Role.Description)
	if desc == "" {
		desc = req.Role.Name
	}
	return fmt.Sprintf("You have been dispatched as the **%s** for this task (%s).", req.Role.Name, strings.ToLower(desc))
}

func taskBody(req Request) string {
	body := strings.TrimSpace(req.Title)
	if detail := strings.TrimSpace(req.Description); detail != "" {
		body += "\n\n" + detail
	}
	return body
}

func environment(req Request) string {
	var lines []string

	switch req.Isolation {
	case roles.IsolationWorktree:
		lines = append(lines, "You are operating inside an isolated git worktree created for this task.")
		if req.Worktree != "" {
			lines = append(lines, fmt.Sprintf("- worktree: `%s`", req.Worktree))
		}
		if req.Branch != "" {
			lines = append(lines, fmt.Sprintf("- branch: `%s`", req.Branch))
		}
		if req.BaseRef != "" {
			lines = append(lines, fmt.Sprintf("- branched from: `%s`", req.BaseRef))
		}
		lines = append(lines, "",
			"Your work is isolated: committing here affects only this branch. Nobody",
			"else is working in this checkout, so you do not need to coordinate edits.")
	default:
		if req.Project.IsGit {
			lines = append(lines, fmt.Sprintf("You are operating in the shared checkout of `%s`.", req.Project.Name))
			if req.Project.Branch != "" {
				lines = append(lines, fmt.Sprintf("- branch: `%s`", req.Project.Branch))
			}
			lines = append(lines, "",
				"This checkout is not isolated. Treat it as someone's live working",
				"copy: do not switch branches, stash, reset, or commit unless the task",
				"explicitly asks you to.")
		} else {
			lines = append(lines, fmt.Sprintf("You are operating in `%s`, which is not a git repository.", req.Project.Root))
		}
	}

	if req.ParentTaskID != "" {
		lines = append(lines, "", fmt.Sprintf("This task was dispatched as part of a larger piece of work (parent task `%s`).", req.ParentTaskID))
	}

	return strings.Join(lines, "\n")
}

func discovery(req Request) string {
	ctx := req.Role.Context
	if !ctx.Repository && !ctx.DiscoverProjectDocs && len(ctx.ExtraDiscovery) == 0 && !ctx.GitHistory {
		return ""
	}

	var b strings.Builder
	if ctx.Repository {
		b.WriteString("You are operating inside an existing project, not a greenfield one.\n" +
			"Treat the patterns already in this repository as evidence of how work is\n" +
			"expected to be done here, and load only the context relevant to your\n" +
			"assignment.\n\n")
	}

	var steps []string
	if ctx.DiscoverProjectDocs {
		steps = append(steps,
			"Discover repository-specific agent instructions and read them.",
			"Inspect the documentation that is relevant to this task.")
	}
	steps = append(steps, "Understand the existing implementation before changing it.")
	steps = append(steps, "Locate the architectural and design conventions already in use.")
	if ctx.GitHistory {
		steps = append(steps, "Use git history to understand why the current design is the way it is.")
	}
	for _, extra := range ctx.ExtraDiscovery {
		steps = append(steps, "Inspect "+strings.TrimSpace(extra)+".")
	}

	for i, step := range steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, step)
	}

	if sources := presentSourceLabels(req.Project); len(sources) > 0 {
		fmt.Fprintf(&b, "\nContext sources dispatch noticed in this project (there may be more —\n"+
			"this is a starting point, not an inventory): %s.\n", strings.Join(sources, ", "))
	}

	return strings.TrimRight(b.String(), "\n")
}

func presentSourceLabels(p project.Project) []string {
	var out []string
	for _, s := range p.PresentSources() {
		out = append(out, "`"+s.Label+"`")
	}
	return out
}

func section(b *strings.Builder, title, body string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}
	fmt.Fprintf(b, "## %s\n\n%s\n\n", title, body)
}
