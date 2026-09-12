package tui

import "github.com/charmbracelet/lipgloss"

var (
	colAccent = lipgloss.Color("212")
	colDim    = lipgloss.Color("244")
	colFaint  = lipgloss.Color("240")
	colOK     = lipgloss.Color("42")
	colWarn   = lipgloss.Color("214")
	colFail   = lipgloss.Color("203")
	colText   = lipgloss.Color("252")
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	dimStyle   = lipgloss.NewStyle().Foreground(colDim)
	faintStyle = lipgloss.NewStyle().Foreground(colFaint)
	textStyle  = lipgloss.NewStyle().Foreground(colText)

	okStyle   = lipgloss.NewStyle().Foreground(colOK)
	warnStyle = lipgloss.NewStyle().Foreground(colWarn)
	failStyle = lipgloss.NewStyle().Foreground(colFail)

	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	headerStyle   = lipgloss.NewStyle().Foreground(colFaint)

	errorBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colFail).
			Foreground(colText).
			Padding(0, 1)

	fieldLabelStyle  = lipgloss.NewStyle().Foreground(colFaint).Width(12)
	activeFieldStyle = lipgloss.NewStyle().Foreground(colAccent).Width(12).Bold(true)

	footerStyle = lipgloss.NewStyle().Foreground(colFaint)
)
