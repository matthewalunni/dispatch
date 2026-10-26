package clix

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/matthewalunni/dispatch/internal/store"
)

// taskRef names the task a status or ask belongs to: --task, else the
// DISPATCH_TASK_ID dispatch exports into every agent session.
func taskRef(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if id := os.Getenv("DISPATCH_TASK_ID"); id != "" {
		return id, nil
	}
	return "", errors.New("no task: DISPATCH_TASK_ID is not set (run this inside a dispatched session) or pass --task <ref>")
}

// parseOption splits "label | detail" on the first " | "; the detail is optional.
func parseOption(s string) (store.AskOption, error) {
	label, detail, _ := strings.Cut(s, " | ")
	label = strings.TrimSpace(label)
	if label == "" {
		return store.AskOption{}, errors.New("option needs a label: --option \"<label> | <detail>\"")
	}
	return store.AskOption{Label: label, Detail: strings.TrimSpace(detail)}, nil
}

// parseProgress reads "done/total".
func parseProgress(s string) (done, total int, err error) {
	bad := fmt.Errorf("invalid progress %q: want done/total such as 2/3", s)
	a, b, ok := strings.Cut(s, "/")
	if !ok {
		return 0, 0, bad
	}
	done, err1 := strconv.Atoi(strings.TrimSpace(a))
	total, err2 := strconv.Atoi(strings.TrimSpace(b))
	if err1 != nil || err2 != nil || done < 0 || total <= 0 || done > total {
		return 0, 0, bad
	}
	return done, total, nil
}

func newStatusCommand() *cobra.Command {
	var (
		progress string
		task     string
		asJSON   bool
	)
	cmd := &cobra.Command{
		Use:   "status <doing now>",
		Short: "Report what this task is doing right now",
		Long: `Record a one-line status for a task, shown in the control centre and dispatch ls.

Agents run this from inside their own session; the task defaults to
$DISPATCH_TASK_ID.`,
		Example: `  dispatch status "wiring the endpoint" --progress 2/3`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := taskRef(task)
			if err != nil {
				return err
			}
			var done, total int
			if progress != "" {
				if done, total, err = parseProgress(progress); err != nil {
					return err
				}
			}
			app, err := openApp()
			if err != nil {
				return err
			}
			defer app.Close()

			if err := app.Report(cmd.Context(), ref, args[0], done, total); err != nil {
				return err
			}
			if asJSON {
				report := store.Report{Line: strings.TrimSpace(args[0]), Done: done, Total: total}
				if t, err := app.ResolveTask(cmd.Context(), ref); err == nil {
					if got, err := app.Store().Reports(cmd.Context(), []string{t.ID}); err == nil {
						if r, ok := got[t.ID]; ok {
							report = r
						}
					}
				}
				return emitJSON(map[string]any{"report": report})
			}
			fmt.Printf("%s %s\n", styleAccent("status"), strings.TrimSpace(args[0]))
			return nil
		},
	}
	cmd.Flags().StringVar(&progress, "progress", "", "progress as done/total, e.g. 2/3")
	cmd.Flags().StringVar(&task, "task", "", "task reference (default $DISPATCH_TASK_ID)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable output")
	return cmd
}

func newAskCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ask",
		Short: "File something you need from the human",
		Long: `File an ask for the human: a decision to make or a chore only they can do.

Agents run this from inside their own session; the task defaults to
$DISPATCH_TASK_ID. The human answers in the control centre and the answer is
typed back into the session.`,
	}
	cmd.AddCommand(newAskDecisionCommand(), newAskChoreCommand(), newAskWithdrawCommand())
	return cmd
}

// readBody returns the ask body from --body or --body-file ("-" is stdin).
func readBody(cmd *cobra.Command, body, file string) (string, error) {
	if body != "" && file != "" {
		return "", errors.New("use either --body or --body-file, not both")
	}
	switch file {
	case "":
		return body, nil
	case "-":
		data, err := io.ReadAll(cmd.InOrStdin())
		return strings.TrimSpace(string(data)), err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("read body file: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func printAsk(ask store.Ask, asJSON bool) error {
	if asJSON {
		return emitJSON(map[string]any{"ask": ask})
	}
	fmt.Printf("%s %s %s\n", styleAccent("asked"), ask.Kind, ask.Title)
	fmt.Printf("  id %s\n", ask.ID)
	return nil
}

func newAskDecisionCommand() *cobra.Command {
	var (
		options   []string
		recommend int
		body      string
		bodyFile  string
		task      string
		asJSON    bool
	)
	cmd := &cobra.Command{
		Use:   "decision <question>",
		Short: "Ask the human to pick between options",
		Example: `  dispatch ask decision "How should we ship the page?" \
    --option "Custom Product Page | faster, no new build" \
    --option "Ship with 1.0.2" --recommend 1`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := taskRef(task)
			if err != nil {
				return err
			}
			ask := store.Ask{Kind: store.AskDecision, Title: args[0]}
			for _, o := range options {
				opt, err := parseOption(o)
				if err != nil {
					return err
				}
				ask.Options = append(ask.Options, opt)
			}
			if recommend < 1 || recommend > len(ask.Options) {
				return fmt.Errorf("--recommend must be between 1 and %d (the number of options)", len(ask.Options))
			}
			ask.Recommended = recommend - 1
			if ask.Body, err = readBody(cmd, body, bodyFile); err != nil {
				return err
			}
			return fileAsk(cmd, ref, ask, asJSON)
		},
	}
	cmd.Flags().StringArrayVar(&options, "option", nil, `an option, "<label> | <detail>" (repeat; at least two)`)
	cmd.Flags().IntVar(&recommend, "recommend", 0, "which option you recommend, 1-based")
	addAskFlags(cmd, &body, &bodyFile, &task, &asJSON)
	return cmd
}

func newAskChoreCommand() *cobra.Command {
	var (
		items    []string
		blocks   string
		body     string
		bodyFile string
		task     string
		asJSON   bool
	)
	cmd := &cobra.Command{
		Use:   "chore <title>",
		Short: "Ask the human to do something only they can",
		Example: `  dispatch ask chore "Add the App Store key" \
    --item "Create the key in App Store Connect" --item "Save it to .env" \
    --blocks "TestFlight upload"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := taskRef(task)
			if err != nil {
				return err
			}
			ask := store.Ask{Kind: store.AskChore, Title: args[0], Items: items, Blocks: blocks}
			if ask.Body, err = readBody(cmd, body, bodyFile); err != nil {
				return err
			}
			return fileAsk(cmd, ref, ask, asJSON)
		},
	}
	cmd.Flags().StringArrayVar(&items, "item", nil, "a step for the human (repeatable)")
	cmd.Flags().StringVar(&blocks, "blocks", "", "what this chore is blocking")
	addAskFlags(cmd, &body, &bodyFile, &task, &asJSON)
	return cmd
}

func addAskFlags(cmd *cobra.Command, body, bodyFile, task *string, asJSON *bool) {
	cmd.Flags().StringVar(body, "body", "", "longer explanation")
	cmd.Flags().StringVar(bodyFile, "body-file", "", "read the explanation from a file, or - for stdin")
	cmd.Flags().StringVar(task, "task", "", "task reference (default $DISPATCH_TASK_ID)")
	cmd.Flags().BoolVar(asJSON, "json", false, "emit machine-readable output")
}

func fileAsk(cmd *cobra.Command, ref string, ask store.Ask, asJSON bool) error {
	app, err := openApp()
	if err != nil {
		return err
	}
	defer app.Close()
	filed, err := app.FileAsk(cmd.Context(), ref, ask)
	if err != nil {
		return err
	}
	return printAsk(filed, asJSON)
}

func newAskWithdrawCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "withdraw <ask-id>",
		Short: "Withdraw an ask that is no longer needed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := openApp()
			if err != nil {
				return err
			}
			defer app.Close()
			if err := app.WithdrawAsk(cmd.Context(), args[0]); err != nil {
				return err
			}
			if asJSON {
				return emitJSON(map[string]any{"withdrawn": args[0]})
			}
			fmt.Printf("%s %s\n", styleAccent("withdrew"), args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable output")
	return cmd
}
