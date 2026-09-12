package clix

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/matthewalunni/dispatch/internal/core"
)

func newDoctorCommand() *cobra.Command {
	var (
		asJSON bool
		dir    string
	)
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose the dispatch environment",
		Long: `Check everything dispatch needs: its own configuration and state, the herdr
runtime, the agent binaries, git, and what the current project offers.

Exits non-zero when something is actually broken, so it is usable in scripts.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := openApp()
			if err != nil {
				return err
			}
			defer app.Close()

			diag, err := app.Doctor(cmd.Context(), dir, Version)
			if err != nil {
				return err
			}
			if asJSON {
				if err := emitJSON(diag); err != nil {
					return err
				}
			} else {
				printDiagnosis(diag)
			}
			if !diag.Healthy {
				return errUnhealthy
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable output")
	cmd.Flags().StringVarP(&dir, "dir", "C", "", "diagnose as if invoked from this directory")
	return cmd
}

// errUnhealthy makes `dispatch doctor` exit non-zero without printing a second
// error line on top of the report the user just read.
var errUnhealthy = &silentError{}

type silentError struct{}

func (e *silentError) Error() string { return "environment has problems (see above)" }

func printDiagnosis(d *core.Diagnosis) {
	out := os.Stdout
	fmt.Fprintf(out, "%s %s\n", styleAccent("Dispatch"), styleDim(d.Version))
	for _, group := range d.Groups {
		fmt.Fprintf(out, "\n%s\n", group.Name)
		tw := newTabWriter(out)
		for _, check := range group.Checks {
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", truncate(check.Label, 52), mark(check.State), styleDim(check.Detail))
		}
		tw.Flush()
		for _, check := range group.Checks {
			if check.Fix != "" {
				fmt.Fprintf(out, "    %s %s\n", styleDim("try:"), check.Fix)
			}
		}
	}
	fmt.Fprintln(out)
	if d.Healthy {
		fmt.Fprintln(out, styleOK("dispatch is ready"))
	} else {
		fmt.Fprintln(out, styleFail("dispatch cannot run tasks until the failures above are fixed"))
	}
}

func mark(state core.CheckState) string {
	switch state {
	case core.CheckOK:
		return styleOK("✓")
	case core.CheckWarn:
		return styleWarn("!")
	case core.CheckFail:
		return styleFail("✗")
	case core.CheckAbsent:
		return styleDim("-")
	default:
		return " "
	}
}
