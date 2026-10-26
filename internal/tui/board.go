package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/matthewalunni/dispatch/internal/core"
	"github.com/matthewalunni/dispatch/internal/store"
)

// The Board: the whole team at a glance, one column per state, one card per
// agent.

const (
	// boardSideBySide is the narrowest terminal that fits four columns.
	boardSideBySide = 100
	boardGap        = 2
	// stackedCardWidth caps a card on a narrow terminal, where the columns are
	// stacked and a card would otherwise stretch across the whole screen.
	stackedCardWidth = 60
)

var boardColumns = []core.Column{
	core.ColumnNeedsYou, core.ColumnWorking, core.ColumnIdle, core.ColumnDoneToday,
}

func columnTitle(c core.Column) string {
	switch c {
	case core.ColumnNeedsYou:
		return "NEEDS YOU"
	case core.ColumnWorking:
		return "WORKING"
	case core.ColumnIdle:
		return "IDLE"
	default:
		return "DONE TODAY"
	}
}

var (
	cardStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colFaint).
			Padding(0, 1)
	cardSelectedStyle = cardStyle.BorderForeground(colAccent)
)

func (m *model) boardCards(c core.Column) []core.TaskView {
	if m.overview == nil {
		return nil
	}
	return m.overview.Board[c]
}

// boardSelected is the card under the board cursor.
func (m *model) boardSelected() (core.TaskView, bool) {
	cards := m.boardCards(m.boardCol)
	if m.boardRow < 0 || m.boardRow >= len(cards) {
		return core.TaskView{}, false
	}
	return cards[m.boardRow], true
}

// clampBoard keeps the cursor on a real card: a column that emptied under it
// hands it to the nearest column that still has cards.
func (m *model) clampBoard() {
	if len(m.boardCards(m.boardCol)) == 0 {
		for _, c := range boardColumns {
			if len(m.boardCards(c)) > 0 {
				m.boardCol, m.boardRow = c, 0
				return
			}
		}
		m.boardRow = 0
		return
	}
	m.boardRow = maxInt(0, minInt(m.boardRow, len(m.boardCards(m.boardCol))-1))
}

// moveColumn steps to the next column with cards in that direction, or stays.
func (m *model) moveColumn(delta int) {
	at := 0
	for i, c := range boardColumns {
		if c == m.boardCol {
			at = i
		}
	}
	for i := at + delta; i >= 0 && i < len(boardColumns); i += delta {
		if n := len(m.boardCards(boardColumns[i])); n > 0 {
			m.boardCol = boardColumns[i]
			m.boardRow = minInt(m.boardRow, n-1)
			return
		}
	}
}

func (m *model) handleBoardKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "left", "h":
		m.moveColumn(-1)
		return m, nil
	case "right", "l":
		m.moveColumn(1)
		return m, nil
	case "up", "k":
		m.boardRow = maxInt(0, m.boardRow-1)
		return m, nil
	case "down", "j":
		m.boardRow++
		m.clampBoard()
		return m, nil
	}
	card, ok := m.boardSelected()
	if !ok {
		return m, nil
	}
	switch key {
	case "enter":
		m.screen = screenDetail
	case "o":
		return m, m.openCmd(card)
	case "x":
		m.confirmStop(card)
	}
	return m, nil
}

// viewBoard renders the board in at most maxLines lines (no limit when not
// positive), scrolled to keep the selected card on screen.
func (m *model) viewBoard(maxLines int) string {
	if m.overview == nil {
		return "\n  " + dimStyle.Render("loading…") + "\n"
	}
	m.clampBoard()
	now := time.Now().UTC()
	all := make([]core.TaskView, 0)
	for _, c := range boardColumns {
		all = append(all, m.boardCards(c)...)
	}

	total := m.contentWidth()
	sideBySide := m.width >= boardSideBySide
	cardWidth := minInt(stackedCardWidth, total)
	if sideBySide {
		cardWidth = (total - boardGap*(len(boardColumns)-1)) / len(boardColumns)
	}

	// focus is the selected card's line range within its column.
	columns := make([]string, len(boardColumns))
	focusCol, focusStart, focusEnd := -1, 0, 0
	for i, c := range boardColumns {
		cards := m.boardCards(c)
		header := headerStyle.Render(cell(fmt.Sprintf("%s · %d", columnTitle(c), len(cards)), cardWidth))
		parts := []string{header}
		height := lipgloss.Height(header)
		for row, v := range cards {
			selected := c == m.boardCol && row == m.boardRow
			card := renderCardStyled(v, childrenOf(v, all), cardWidth, now, selected)
			if selected {
				focusCol, focusStart, focusEnd = i, height, height+lipgloss.Height(card)-1
			}
			parts = append(parts, card)
			height += lipgloss.Height(card)
		}
		if len(cards) == 0 {
			parts = append(parts, "  "+faintStyle.Render("—"))
		}
		columns[i] = lipgloss.JoinVertical(lipgloss.Left, parts...)
	}

	if !sideBySide {
		// Stacked, the columns scroll as one list: find the card in it.
		start, end := 1, 1
		offset := 1 // the leading blank line
		for i, col := range columns {
			if i == focusCol {
				start, end = offset+focusStart, offset+focusEnd
			}
			offset += lipgloss.Height(col) + 1
		}
		out := "\n" + indent(lipgloss.JoinVertical(lipgloss.Left, interleave(columns)...), 2) + "\n"
		return fitLines(out, start, end, maxLines)
	}

	// Side by side, each column scrolls on its own under its header, within
	// the lines left once the blank lines above and below are taken.
	if colHeight := maxLines - 2; maxLines > 0 {
		for i, col := range columns {
			lines := strings.Split(col, "\n")
			if len(lines) <= colHeight || colHeight < 2 {
				continue
			}
			start, end := 0, 0
			if i == focusCol {
				start, end = focusStart-1, focusEnd-1
			}
			lines = append(lines[:1:1], scrollWindow(lines[1:], start, end, colHeight-1)...)
			columns[i] = strings.Join(lines, "\n")
		}
	}
	spaced := make([]string, 0, len(columns)*2)
	for i, col := range columns {
		if i > 0 {
			spaced = append(spaced, strings.Repeat(" ", boardGap))
		}
		spaced = append(spaced, col)
	}
	return "\n" + indent(lipgloss.JoinHorizontal(lipgloss.Top, spaced...), 2) + "\n"
}

// interleave puts a blank line between stacked columns.
func interleave(cols []string) []string {
	out := make([]string, 0, len(cols)*2)
	for i, c := range cols {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, c)
	}
	return out
}

// childrenOf finds an orchestrator's helpers among the board's tasks.
func childrenOf(v core.TaskView, all []core.TaskView) []core.TaskView {
	if v.Role != "orchestrator" {
		return nil
	}
	var kids []core.TaskView
	for _, k := range all {
		if k.ParentTaskID == v.ID {
			kids = append(kids, k)
		}
	}
	return kids
}

// renderCard draws one agent: who, where and how long, what it is doing, and
// how far along it is. width is the whole card, border included.
func renderCard(v core.TaskView, children []core.TaskView, width int, now time.Time) string {
	return renderCardStyled(v, children, width, now, false)
}

func renderCardStyled(v core.TaskView, children []core.TaskView, width int, now time.Time, selected bool) string {
	inner := maxInt(8, width-4)

	// The role keeps its name where it can; the project gives way first.
	age := " · " + v.Age(now)
	meta := truncate(v.ProjectName, maxInt(4, inner-len([]rune(v.Role))-len([]rune(age))-1)) + age
	roleWidth := maxInt(1, inner-len([]rune(meta))-1)
	role := truncate(v.Role, roleWidth)
	gap := maxInt(1, inner-len([]rune(role))-len([]rune(meta)))
	roleStyle := lipgloss.NewStyle().Bold(true).Foreground(colText)
	lines := []string{roleStyle.Render(role) + strings.Repeat(" ", gap) + dimStyle.Render(meta)}

	doing := v.Title
	switch {
	case len(v.OpenAsks) > 0:
		glyph := "◆ "
		if v.OpenAsks[0].Kind == store.AskChore {
			glyph = "○ "
		}
		doing = glyph + v.OpenAsks[0].Title
	case v.Report != nil && v.Report.Line != "":
		doing = v.Report.Line
	}
	lines = append(lines, textStyle.Render(truncate(doing, inner)))

	if v.Report != nil && v.Report.Total > 0 {
		label := fmt.Sprintf("%d of %d", v.Report.Done, v.Report.Total)
		barWidth := maxInt(4, inner-len([]rune(label))-2)
		lines = append(lines, okStyle.Render(progressBar(v.Report.Done, v.Report.Total, barWidth))+"  "+dimStyle.Render(label))
	}

	if len(children) > 0 {
		names := make([]string, len(children))
		for i, k := range children {
			names[i] = k.Role
		}
		noun := "helpers"
		if len(children) == 1 {
			noun = "helper"
		}
		lines = append(lines, dimStyle.Render(truncate(fmt.Sprintf("└ %d %s: %s", len(children), noun, strings.Join(names, ", ")), inner)))
	}

	style := cardStyle
	if selected {
		style = cardSelectedStyle
	}
	return style.Width(width - 2).Render(strings.Join(lines, "\n"))
}

// progressBar is done/total drawn in width cells, or nothing without a total.
func progressBar(done, total, width int) string {
	if total <= 0 || width <= 0 {
		return ""
	}
	done = maxInt(0, minInt(done, total))
	filled := done * width / total
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}
