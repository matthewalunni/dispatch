//go:build windows

package clix

import (
	"errors"
	"os"
	"os/exec"
)

// attach runs herdr as a child process with this terminal's stdio. Windows has
// no exec-replace, so dispatch stays alive as a thin parent and exits with the
// same status herdr did.
func attach(argv []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		os.Exit(exitErr.ExitCode())
	}
	return err
}
