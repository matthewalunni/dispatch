package clix

import (
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
)

// Colour is only used when stdout is a terminal, so piped and JSON output
// stays clean.
var useColour = isatty.IsTerminal(os.Stdout.Fd())

var (
	accentStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	failStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)

func styleAccent(s string) string { return render(accentStyle, s) }
func styleDim(s string) string    { return render(dimStyle, s) }
func styleOK(s string) string     { return render(okStyle, s) }
func styleWarn(s string) string   { return render(warnStyle, s) }
func styleFail(s string) string   { return render(failStyle, s) }

func render(style lipgloss.Style, s string) string {
	if !useColour {
		return s
	}
	return style.Render(s)
}
