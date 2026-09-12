package clix

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/matthewalunni/dispatch/internal/core"
	"github.com/matthewalunni/dispatch/internal/paths"
	"github.com/matthewalunni/dispatch/internal/store"
)

func newListCommand() *cobra.Command {
	var (
		all       bool
		allProj   bool
		attention bool
		parent    string
		limit     int
		asJSON    bool
		dir       string
	)

	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List dispatched tasks",
		Long: `List tasks dispatch knows about, joined with herdr's live agent state.

By default this shows active tasks across every project. Status comes from
herdr where a session is still alive:

  working    the agent is producing work
  waiting    herdr detected an approval or question; the agent needs you
  idle       the agent is ready for input
  done       the agent finished a turn and has not been looked at
  detached   dispatch has a task but herdr no longer has its session`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := openApp()
			if err != nil {
				return err
			}
			defer app.Close()

			filter := store.Filter{Limit: limit, IncludeTerminal: all}
			if !allProj {
				rc, err := app.Resolve(dir)
				if err == nil && rc.Project.Root != "" {
					filter.ProjectRoot = rc.Project.Root
				}
			}
			if parent != "" {
				task, err := app.ResolveTask(cmd.Context(), parent)
				if err != nil {
					return err
				}
				filter.ParentTaskID = task.ID
				filter.IncludeTerminal = true
				filter.ProjectRoot = ""
			}

			tasks, err := app.Tasks(cmd.Context(), filter)
			if err != nil {
				return err
			}
			views := app.Hydrate(cmd.Context(), tasks)

			if attention {
				var filtered []core.TaskView
				for _, v := range views {
					if v.Effective.NeedsAttention() {
						filtered = append(filtered, v)
					}
				}
				views = filtered
			}

			if asJSON {
				return emitJSON(map[string]any{"tasks": toJSONList(views, paths.DataDir())})
			}
			printTaskTable(views, filter.ProjectRoot == "")
			return nil
		},
	}

	cmd.Flags().BoolVarP(&all, "all", "a", false, "include stopped, completed and failed tasks")
	cmd.Flags().BoolVar(&allProj, "all-projects", true, "list tasks from every project (use --all-projects=false for this project only)")
	cmd.Flags().BoolVar(&attention, "needs-attention", false, "only tasks whose agent is waiting on a human")
	cmd.Flags().StringVar(&parent, "parent", "", "only the children of this task")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "maximum tasks to list")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable output")
	cmd.Flags().StringVarP(&dir, "dir", "C", "", "treat this directory as the current project")
	return cmd
}

func printTaskTable(views []core.TaskView, showProject bool) {
	if len(views) == 0 {
		fmt.Println(styleDim("no tasks. dispatch run --role engineer --task \"...\" to create one"))
		return
	}
	now := time.Now().UTC()
	tw := newTabWriter(os.Stdout)
	if showProject {
		fmt.Fprintln(tw, styleDim("PROJECT\tTASK\tROLE\tSTATUS\tAGE\tREF"))
	} else {
		fmt.Fprintln(tw, styleDim("TASK\tROLE\tSTATUS\tAGE\tREF"))
	}
	for _, v := range views {
		status := statusLabel(v.Effective)
		if showProject {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
				truncate(v.ProjectName, 18), truncate(v.Title, 44), v.Role, status, v.Age(now), v.Slug)
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
				truncate(v.Title, 52), v.Role, status, v.Age(now), v.Slug)
		}
	}
	tw.Flush()
}

func statusLabel(s core.EffectiveStatus) string {
	switch s {
	case core.EffectiveWorking:
		return styleOK(string(s))
	case core.EffectiveWaiting:
		return styleWarn(string(s))
	case core.EffectiveFailed, core.EffectiveDetached:
		return styleFail(string(s))
	case core.EffectiveStopped, core.EffectiveCompleted:
		return styleDim(string(s))
	default:
		return string(s)
	}
}

func newOpenCommand() *cobra.Command {
	var (
		asJSON   bool
		noAttach bool
	)
	cmd := &cobra.Command{
		Use:   "open <task>",
		Short: "Return to a task's persistent agent conversation",
		Long: `Bring a task's herdr session back to you.

A task can be named by its slug, a unique prefix of it, its herdr agent name,
or its task id. dispatch focuses the session for any attached herdr client and,
when this terminal can take it over, attaches to the conversation directly.`,
		Example: `  dispatch open workout-editor
  dispatch open workout --no-attach`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := openApp()
			if err != nil {
				return err
			}
			result, err := app.Open(cmd.Context(), args[0])
			if err != nil {
				app.Close()
				return err
			}

			if asJSON {
				defer app.Close()
				return emitJSON(map[string]any{
					"task":           toJSON(result.Task, paths.DataDir()),
					"focused":        result.Focused,
					"attach_command": result.AttachCommand,
				})
			}

			if noAttach || len(result.AttachCommand) == 0 {
				defer app.Close()
				fmt.Printf("%s %s (%s)\n", styleAccent("focused"), result.Task.Title, result.Task.HerdrAgent)
				fmt.Printf("attach with:\n  %s\n", shellJoin(result.AttachCommand))
				return nil
			}

			// Attaching replaces this process, so release the database first.
			app.Close()
			return attach(result.AttachCommand)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable output instead of attaching")
	cmd.Flags().BoolVar(&noAttach, "no-attach", false, "focus the session but do not take over this terminal")
	return cmd
}

func shellJoin(argv []string) string {
	out := ""
	for i, a := range argv {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}

func newStopCommand() *cobra.Command {
	var (
		removeWorktree bool
		force          bool
		asJSON         bool
	)
	cmd := &cobra.Command{
		Use:   "stop <task>",
		Short: "Stop a task's agent session",
		Long: `Stop the agent running for a task and close the task out.

The task's git worktree is left in place by default so any work in it survives;
pass --remove-worktree to delete it too.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := openApp()
			if err != nil {
				return err
			}
			defer app.Close()

			result, err := app.Stop(cmd.Context(), args[0], core.StopOptions{
				RemoveWorktree: removeWorktree,
				Force:          force,
			})
			if err != nil {
				return err
			}
			if asJSON {
				return emitJSON(map[string]any{
					"task":             toJSON(result.Task, paths.DataDir()),
					"agent_stopped":    result.AgentStopped,
					"worktree_removed": result.WorktreeRemoved,
					"warnings":         result.Warnings,
				})
			}
			for _, w := range result.Warnings {
				warn("%s", w)
			}
			fmt.Printf("%s %s\n", styleAccent("stopped"), result.Task.Title)
			if result.Task.Worktree != "" {
				fmt.Printf("  worktree kept at %s (branch %s)\n", result.Task.Worktree, result.Task.Branch)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&removeWorktree, "remove-worktree", false, "also delete the task's git worktree")
	cmd.Flags().BoolVar(&force, "force", false, "remove the worktree even if it has modifications")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable output")
	return cmd
}

func newDoneCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "done <task>",
		Short: "Mark a task complete without stopping its session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := openApp()
			if err != nil {
				return err
			}
			defer app.Close()
			task, err := app.Complete(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			view := app.HydrateOne(cmd.Context(), task)
			if asJSON {
				return emitJSON(toJSON(view, paths.DataDir()))
			}
			fmt.Printf("%s %s\n", styleAccent("completed"), task.Title)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable output")
	return cmd
}

func newShowCommand() *cobra.Command {
	var (
		asJSON     bool
		showPrompt bool
	)
	cmd := &cobra.Command{
		Use:   "show <task>",
		Short: "Show everything dispatch knows about a task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := openApp()
			if err != nil {
				return err
			}
			defer app.Close()

			task, err := app.ResolveTask(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			view := app.HydrateOne(cmd.Context(), task)
			children, _ := app.Store().Children(cmd.Context(), task.ID)

			if showPrompt {
				text, err := core.ReadPrompt(paths.DataDir(), task.ID)
				if err != nil {
					return fmt.Errorf("no saved prompt for %s: %w", task.Slug, err)
				}
				fmt.Print(text)
				return nil
			}

			if asJSON {
				return emitJSON(map[string]any{
					"task":     toJSON(view, paths.DataDir()),
					"children": toJSONList(app.Hydrate(cmd.Context(), children), paths.DataDir()),
				})
			}

			tw := newTabWriter(os.Stdout)
			fmt.Fprintf(tw, "%s\t%s\n", styleDim("title"), view.Title)
			if view.Description != "" {
				fmt.Fprintf(tw, "%s\t%s\n", styleDim("description"), view.Description)
			}
			fmt.Fprintf(tw, "%s\t%s\n", styleDim("id"), view.ID)
			fmt.Fprintf(tw, "%s\t%s\n", styleDim("slug"), view.Slug)
			fmt.Fprintf(tw, "%s\t%s\n", styleDim("role"), view.Role)
			fmt.Fprintf(tw, "%s\t%s\n", styleDim("runtime"), view.Runtime)
			fmt.Fprintf(tw, "%s\t%s\n", styleDim("status"), statusLabel(view.Effective))
			fmt.Fprintf(tw, "%s\t%s\n", styleDim("dispatch status"), view.Task.Status)
			fmt.Fprintf(tw, "%s\t%s\n", styleDim("project"), view.ProjectRoot)
			fmt.Fprintf(tw, "%s\t%s\n", styleDim("isolation"), view.Isolation)
			if view.Branch != "" {
				fmt.Fprintf(tw, "%s\t%s (from %s)\n", styleDim("branch"), view.Branch, view.BaseRef)
			}
			if view.Worktree != "" {
				fmt.Fprintf(tw, "%s\t%s\n", styleDim("worktree"), view.Worktree)
			}
			fmt.Fprintf(tw, "%s\t%s\n", styleDim("herdr agent"), view.HerdrAgent)
			if view.HerdrLayout == "workspace" {
				fmt.Fprintf(tw, "%s\t%s %s\n", styleDim("herdr workspace"), view.HerdrWorkspaceID, styleDim("(this task's own)"))
			} else {
				fmt.Fprintf(tw, "%s\t%s %s\n", styleDim("herdr workspace"), view.HerdrWorkspaceID, styleDim("(shared)"))
			}
			fmt.Fprintf(tw, "%s\t%s / %s\n", styleDim("herdr pane"), view.HerdrPaneID, view.HerdrTabID)
			if view.ParentTaskID != "" {
				fmt.Fprintf(tw, "%s\t%s\n", styleDim("parent"), view.ParentTaskID)
			}
			fmt.Fprintf(tw, "%s\t%s\n", styleDim("created"), view.CreatedAt.Format(time.RFC3339))
			fmt.Fprintf(tw, "%s\t%s\n", styleDim("prompt"), core.PromptPath(paths.DataDir(), view.ID))
			if view.Task.Error != "" {
				fmt.Fprintf(tw, "%s\t%s\n", styleDim("error"), view.Task.Error)
			}
			tw.Flush()

			if len(children) > 0 {
				fmt.Printf("\n%s\n", styleDim("children"))
				printTaskTable(app.Hydrate(cmd.Context(), children), false)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable output")
	cmd.Flags().BoolVar(&showPrompt, "prompt", false, "print the initial prompt that was sent")
	return cmd
}
