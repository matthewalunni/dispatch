package tui

import "github.com/matthewalunni/dispatch/internal/core"

// Run opens the dispatch control centre.
func Run(app *core.App, version string) error { return run(app, version) }
