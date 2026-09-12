// Package clix is dispatch's machine- and human-facing command line.
//
// Every command is a thin wrapper over internal/core. Nothing here holds
// product logic, and nothing here imports the TUI except the root command.
package clix

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/matthewalunni/dispatch/internal/core"
)

// Version is set from main at build time.
var Version = "0.1.0"

// Execute runs the dispatch CLI and returns a process exit code.
func Execute(version string, tui func(*core.App) error) int {
	Version = version
	root := newRootCommand(tui)
	root.SilenceErrors = true
	root.SilenceUsage = true

	if err := root.Execute(); err != nil {
		printError(err)
		return exitCode(err)
	}
	return 0
}

func newRootCommand(tui func(*core.App) error) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dispatch",
		Short: "Launch and manage specialized AI agents",
		Long: `dispatch is a local-first control plane for launching specialized AI agents.

It takes a task and an agent role, prepares the right environment (including an
isolated git worktree when the role needs one), launches the agent in herdr, and
keeps the resulting interactive session available to come back to.

Run with no arguments to open the control centre.`,
		Version: version(),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := openApp()
			if err != nil {
				return err
			}
			defer app.Close()
			return tui(app)
		},
	}

	cmd.AddCommand(
		newRunCommand(),
		newListCommand(),
		newOpenCommand(),
		newWaitCommand(),
		newStopCommand(),
		newDoneCommand(),
		newShowCommand(),
		newRolesCommand(),
		newRoleCommand(),
		newDoctorCommand(),
	)
	return cmd
}

func version() string { return Version }

func openApp() (*core.App, error) {
	return core.New(core.Options{})
}

// errTimedOut gives `dispatch wait` a distinct exit status so a script can
// tell "the agent reached the state" from "it did not in time".
var errTimedOut = &timeoutError{}

type timeoutError struct{}

func (e *timeoutError) Error() string { return "timed out waiting for the task" }

// exitCode maps errors onto conventional process exit codes.
func exitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case isTimeout(err):
		return 2
	default:
		return 1
	}
}

func isTimeout(err error) bool {
	_, ok := err.(*timeoutError)
	return ok
}

func printError(err error) {
	// doctor already printed a full report; a second one-line summary on top
	// of it is noise.
	if _, ok := err.(*silentError); ok {
		return
	}
	// `wait` already printed its own outcome line.
	if isTimeout(err) {
		return
	}
	fmt.Fprintln(os.Stderr, "dispatch: "+err.Error())
}
