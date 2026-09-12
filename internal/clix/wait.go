package clix

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/matthewalunni/dispatch/internal/core"
	"github.com/matthewalunni/dispatch/internal/paths"
)

func newWaitCommand() *cobra.Command {
	var (
		until   []string
		timeout time.Duration
		asJSON  bool
	)

	cmd := &cobra.Command{
		Use:   "wait <task>",
		Short: "Block until a task's agent reaches a given state",
		Long: `Wait for a dispatched agent to stop producing work.

This is the counterpart to ` + "`dispatch run`" + ` for anything driving dispatch:
hand out work, then block on it instead of polling ` + "`dispatch ls`" + ` in a loop.
It is event-driven where herdr supports it, with a slow poll as a safety net.

By default it returns when the agent is waiting on a human, idle, done, or its
session has gone away. Exit status is 0 when a state matched and 2 on timeout,
so a script can tell the two apart without parsing anything.`,
		Example: `  dispatch wait implement-feature-x
  dispatch wait implement-feature-x --until waiting --timeout 10m
  dispatch wait implement-feature-x --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			states, err := parseStates(until)
			if err != nil {
				return err
			}

			app, err := openApp()
			if err != nil {
				return err
			}
			defer app.Close()

			result, err := app.WaitFor(cmd.Context(), args[0], core.WaitOptions{
				Until:   states,
				Timeout: timeout,
			})
			if err != nil {
				return err
			}

			if asJSON {
				if err := emitJSON(map[string]any{
					"task":      toJSON(result.Task, paths.DataDir()),
					"matched":   result.Matched,
					"timed_out": result.TimedOut,
					"live":      result.Live,
				}); err != nil {
					return err
				}
			} else if result.TimedOut {
				fmt.Printf("%s %s is %s after %s\n",
					styleWarn("timed out"), result.Task.Slug, result.Task.Effective, timeout)
			} else {
				fmt.Printf("%s %s is %s\n",
					styleAccent(string(result.Task.Effective)), result.Task.Slug, statusLabel(result.Task.Effective))
			}

			if result.TimedOut {
				return errTimedOut
			}
			return nil
		},
	}

	cmd.Flags().StringSliceVar(&until, "until", nil,
		"states to wait for (default: waiting, idle, done, detached)")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "give up after this long (e.g. 10m); 0 waits forever")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable output")
	return cmd
}

// waitableStates are the effective statuses `--until` accepts.
var waitableStates = []core.EffectiveStatus{
	core.EffectiveWorking,
	core.EffectiveWaiting,
	core.EffectiveIdle,
	core.EffectiveDone,
	core.EffectiveDetached,
	core.EffectiveStopped,
	core.EffectiveCompleted,
	core.EffectiveFailed,
}

func parseStates(values []string) ([]core.EffectiveStatus, error) {
	var out []core.EffectiveStatus
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		var matched bool
		for _, state := range waitableStates {
			if string(state) == value {
				out = append(out, state)
				matched = true
				break
			}
		}
		if !matched {
			names := make([]string, 0, len(waitableStates))
			for _, state := range waitableStates {
				names = append(names, string(state))
			}
			return nil, fmt.Errorf("unknown state %q (valid: %s)", value, strings.Join(names, ", "))
		}
	}
	return out, nil
}
