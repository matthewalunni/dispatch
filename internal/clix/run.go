package clix

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/matthewalunni/dispatch/internal/core"
	"github.com/matthewalunni/dispatch/internal/paths"
	"github.com/matthewalunni/dispatch/internal/roles"
)

func newRunCommand() *cobra.Command {
	var (
		task        string
		description string
		role        string
		runtimeName string
		isolation   string
		branch      string
		base        string
		parent      string
		agentName   string
		dir         string
		focus       bool
		asJSON      bool
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Dispatch a task to an agent without opening the TUI",
		Long: `Create a task, prepare its environment, and launch the agent in herdr.

The task text may be given with --task or as positional arguments:

  dispatch run --role engineer --task "Implement exercise substitution"
  dispatch run --role reviewer "Review the current branch against main"

--json prints a machine-readable task document on stdout and nothing else, so
another agent or orchestrator can consume the result directly.`,
		Example: `  dispatch run --role engineer --task "Implement exercise substitution"
  dispatch run --role designer --task "Prototype alternatives to onboarding"
  dispatch run --role reviewer --task "Review the current branch against main" --json
  dispatch run --parent task_20260912T101500_ab12cd34 --role engineer --task "Add the API endpoint"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if task == "" {
				task = strings.TrimSpace(strings.Join(args, " "))
			}
			app, err := openApp()
			if err != nil {
				return err
			}
			defer app.Close()

			result, err := app.Dispatch(cmd.Context(), core.DispatchRequest{
				Dir:          dir,
				Title:        task,
				Description:  description,
				Role:         role,
				Runtime:      runtimeName,
				Isolation:    isolation,
				Branch:       branch,
				BaseRef:      base,
				ParentTaskID: parent,
				AgentName:    agentName,
				Focus:        focus,
			})
			if err != nil {
				return err
			}

			view := app.HydrateOne(cmd.Context(), result.Task)
			for _, w := range result.Warnings {
				warn("%s", w)
			}

			if asJSON {
				return emitJSON(toJSON(view, paths.DataDir()))
			}
			printDispatched(view)
			return nil
		},
	}

	cmd.Flags().StringVarP(&task, "task", "t", "", "what the agent should do (required)")
	cmd.Flags().StringVar(&description, "description", "", "additional detail appended to the task")
	cmd.Flags().StringVarP(&role, "role", "r", "", "agent role (default: config default_role)")
	cmd.Flags().StringVar(&runtimeName, "runtime", "", "override the role's runtime")
	cmd.Flags().StringVar(&isolation, "isolation", "", "override the role's isolation mode ("+roles.JoinIsolationModes()+")")
	cmd.Flags().StringVarP(&branch, "branch", "b", "", "branch name for worktree isolation")
	cmd.Flags().StringVar(&base, "base", "", "base ref for a new branch (default: current branch)")
	cmd.Flags().StringVar(&parent, "parent", "", "parent task, recording lineage")
	cmd.Flags().StringVar(&agentName, "name", "", "explicit herdr agent name")
	cmd.Flags().StringVarP(&dir, "dir", "C", "", "run as if dispatch were invoked from this directory")
	cmd.Flags().BoolVar(&focus, "focus", false, "bring the new herdr tab to the front")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a machine-readable task document")
	return cmd
}

func printDispatched(view core.TaskView) {
	out := os.Stdout
	fmt.Fprintf(out, "%s  %s\n", styleAccent("dispatched"), view.Title)
	tw := newTabWriter(out)
	fmt.Fprintf(tw, "  role\t%s\n", view.Role)
	fmt.Fprintf(tw, "  runtime\t%s\n", view.Runtime)
	fmt.Fprintf(tw, "  project\t%s\n", view.ProjectRoot)
	fmt.Fprintf(tw, "  isolation\t%s\n", view.Isolation)
	if view.Branch != "" {
		fmt.Fprintf(tw, "  branch\t%s\n", view.Branch)
	}
	if view.Worktree != "" {
		fmt.Fprintf(tw, "  worktree\t%s\n", view.Worktree)
	}
	fmt.Fprintf(tw, "  agent\t%s\n", view.HerdrAgent)
	fmt.Fprintf(tw, "  status\t%s\n", view.Effective)
	fmt.Fprintf(tw, "  id\t%s\n", view.ID)
	tw.Flush()
	fmt.Fprintf(out, "\nOpen the conversation with:\n  dispatch open %s\n", view.Slug)
}
