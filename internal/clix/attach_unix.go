//go:build !windows

package clix

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// attach hands this terminal to herdr. exec replaces the dispatch process, so
// the user lands directly in the conversation with no wrapper in the way:
// signals, window resizes and detach all behave as if they had run herdr
// themselves.
func attach(argv []string) error {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("cannot find %s on PATH: %w", argv[0], err)
	}
	return syscall.Exec(path, argv, os.Environ())
}
