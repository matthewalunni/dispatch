// Command dispatch launches and manages specialized AI agents in herdr.
package main

import (
	"os"

	"github.com/matthewalunni/dispatch/internal/clix"
	"github.com/matthewalunni/dispatch/internal/core"
	"github.com/matthewalunni/dispatch/internal/tui"
)

// version is overridable at build time:
//
//	go build -ldflags "-X main.version=$(git describe --tags)" ./cmd/dispatch
var version = "0.1.0"

func main() {
	os.Exit(clix.Execute(version, func(app *core.App) error {
		return tui.Run(app, version)
	}))
}
