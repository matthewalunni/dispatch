package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/matthewalunni/dispatch/internal/core"
	"github.com/matthewalunni/dispatch/internal/herdrx"
	"github.com/matthewalunni/dispatch/internal/store"
)

// The Inbox: everything waiting on the human, each answerable with one key,
// above a quiet list of what is still working.

const (
	// kindWidth is the column the item kind ("◆ DECISION") takes, so titles
	// line up whatever the kind.
	kindWidth = 10
	// tailLines is how much of an agent's screen a collapsed card shows.
	tailLines = 3
)

// setOverview swaps in fresh data, keeping the cursor on the same item when it
// is still there so a live update never moves the selection underfoot.
func (m *model) setOverview(o *core.Overview) {
	before, had := m.selectedItem()
	m.overview = o
	items := m.inboxItems()
	// An answer that has since been delivered leaves the inbox; forget why it
	// was waiting.
	for id := range m.pending {
		if !hasAsk(items, id) {
			delete(m.pending, id)
		}
	}
	if m.replying && !hasOpenAsk(items, m.replyAskID) {
		m.cancelReply()
		m.status = "reply cancelled: that ask is no longer open"
	}
	if had {
		for i, item := range items {
			if itemKey(item) == itemKey(before) {
				if i != m.inboxCursor {
					m.expanded = false
				}
				m.inboxCursor = i
				return
			}
		}
		m.expanded = false
	}
	if m.inboxCursor >= len(items) {
		m.inboxCursor = maxInt(0, len(items)-1)
	}
}

func (m *model) inboxItems() []core.InboxItem {
	if m.overview == nil {
		return nil
	}
	return m.overview.Inbox
}

func (m *model) selectedItem() (core.InboxItem, bool) {
	items := m.inboxItems()
	if m.inboxCursor < 0 || m.inboxCursor >= len(items) {
		return core.InboxItem{}, false
	}
	return items[m.inboxCursor], true
}

func hasOpenAsk(items []core.InboxItem, askID string) bool {
	for _, item := range items {
		if item.Ask != nil && item.Ask.ID == askID {
			return item.Ask.State == store.AskOpen
		}
	}
	return false
}

func hasAsk(items []core.InboxItem, askID string) bool {
	for _, item := range items {
		if item.Ask != nil && item.Ask.ID == askID {
			return true
		}
	}
	return false
}

func itemKey(item core.InboxItem) string {
	if item.Ask != nil {
		return "ask:" + item.Ask.ID
	}
	return "task:" + item.Task.ID
}

func (m *model) handleInboxKey(key string) (tea.Model, tea.Cmd) {
	items := m.inboxItems()
	item, ok := m.selectedItem()
	switch key {
	case "up", "k":
		m.inboxCursor = wrap(m.inboxCursor-1, len(items))
		m.expanded = false
		return m, nil
	case "down", "j":
		m.inboxCursor = wrap(m.inboxCursor+1, len(items))
		m.expanded = false
		return m, nil
	}
	if !ok {
		return m, nil
	}

	switch key {
	case "enter":
		m.expanded = !m.expanded
		return m, nil
	case "o":
		return m, m.openCmd(item.Task)
	case "x":
		m.confirmStop(item.Task)
		return m, nil
	case "m":
		if item.Ask == nil || item.Ask.Kind != store.AskDecision || item.Ask.State != store.AskOpen {
			m.status = "only an open decision takes a reply — o opens the conversation"
			return m, nil
		}
		m.replying = true
		m.replyAskID = item.Ask.ID
		m.reply.SetValue("")
		m.reply.Width = maxInt(30, m.contentWidth()-16)
		m.reply.Focus()
		return m, textinput.Blink
	}

	if ans, ok := inboxAnswer(item, key); ok {
		if m.inFlight[item.Ask.ID] {
			return m, nil
		}
		m.status = "sending answer…"
		return m, m.answerCmd(item.Ask.ID, ans)
	}
	if item.Ask != nil && item.Ask.State == store.AskAnswered && (key == "d" || optionIndex(key, len(item.Ask.Options)) >= 0) {
		m.status = "already answered · waiting to deliver"
	}
	return m, nil
}

// handleReplyKey drives the one-line reply input.
func (m *model) handleReplyKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.cancelReply()
		return m, nil
	case "enter":
		note := strings.TrimSpace(m.reply.Value())
		askID := m.replyAskID
		m.cancelReply()
		if note == "" || askID == "" || !hasOpenAsk(m.inboxItems(), askID) || m.inFlight[askID] {
			return m, nil
		}
		m.status = "sending reply…"
		return m, m.answerCmd(askID, core.Answer{Option: -1, Note: note})
	}
	var cmd tea.Cmd
	m.reply, cmd = m.reply.Update(msg)
	return m, cmd
}

func (m *model) cancelReply() {
	m.replying = false
	m.replyAskID = ""
	m.reply.Blur()
}

// inboxAnswer is the answer a key gives an inbox item: a letter picks a
// decision's option, d marks a chore done. Anything not open is not answered.
func inboxAnswer(item core.InboxItem, key string) (core.Answer, bool) {
	ask := item.Ask
	if ask == nil || ask.State != store.AskOpen {
		return core.Answer{}, false
	}
	switch ask.Kind {
	case store.AskChore:
		if key == "d" {
			return core.Answer{Option: -1, ChoreDone: true}, true
		}
	case store.AskDecision:
		if i := optionIndex(key, len(ask.Options)); i >= 0 {
			return core.Answer{Option: i}, true
		}
	}
	return core.Answer{}, false
}

// optionIndex maps an option key (a, b, c…) to its 0-based index, or -1 when
// the key names no option among count.
func optionIndex(key string, count int) int {
	if len(key) != 1 || key[0] < 'a' || key[0] > 'z' {
		return -1
	}
	i := int(key[0] - 'a')
	if i >= count {
		return -1
	}
	return i
}

func optionKey(i int) string { return string(rune('a' + i)) }

func pendingLabel(reason string) string {
	if reason == "" {
		return "waiting to deliver"
	}
	return "waiting to deliver (" + reason + ")"
}

// ------------------------------------------------------------------ view

func (m *model) viewInbox() string {
	var b strings.Builder
	if m.overview == nil {
		b.WriteString("\n  " + dimStyle.Render("loading…") + "\n")
		return b.String()
	}
	width := m.contentWidth()
	now := time.Now().UTC()

	b.WriteString("\n  " + headerStyle.Render("NEEDS YOU") + "\n")
	if len(m.overview.Inbox) == 0 {
		b.WriteString("  " + dimStyle.Render("nothing needs you") + "\n")
	}
	for i, item := range m.overview.Inbox {
		b.WriteString(m.renderInboxItem(item, i == m.inboxCursor, width, now))
	}

	if len(m.overview.Working) > 0 {
		b.WriteString("\n  " + headerStyle.Render("WORKING") + "\n")
		for _, v := range m.overview.Working {
			b.WriteString(renderWorking(v, width, now))
		}
	}
	return b.String()
}

func (m *model) renderInboxItem(item core.InboxItem, selected bool, width int, now time.Time) string {
	var b strings.Builder
	ask := item.Ask
	expanded := selected && m.expanded

	kind, title, age := fallbackKind(item.Task), item.Task.Title, item.Task.Age(now)
	if ask != nil {
		kind, title, age = "◆ DECISION", ask.Title, store.HumanDuration(now.Sub(ask.CreatedAt))
		if ask.Kind == store.AskChore {
			kind = "○ CHORE"
		}
	}
	meta := strings.Join([]string{item.Task.Role, item.Task.ProjectName, age}, " · ")
	titleWidth := maxInt(16, width-kindWidth-len([]rune(meta))-8)

	cursor, style := "  ", textStyle
	if selected {
		cursor, style = selectedStyle.Render("› "), selectedStyle
	}
	fmt.Fprintf(&b, "%s%s  %s  %s\n", cursor,
		warnStyle.Render(cell(kind, kindWidth)),
		style.Render(cell(title, titleWidth)),
		dimStyle.Render(meta))

	sub := strings.Repeat(" ", 2+2+kindWidth+2)
	switch {
	case ask == nil:
		lines := screenLines(item.Screen)
		if !expanded && len(lines) > tailLines {
			lines = lines[len(lines)-tailLines:]
		}
		for _, line := range lines {
			b.WriteString(sub + faintStyle.Render(truncate(line, maxInt(20, width-len(sub)))) + "\n")
		}
	case ask.Kind == store.AskChore:
		b.WriteString(renderChecklist(ask.Items, sub, width))
		if ask.Blocks != "" {
			b.WriteString(sub + dimStyle.Render("blocks: "+ask.Blocks) + "\n")
		}
	default:
		b.WriteString(renderOptions(ask, width))
	}

	if expanded && ask != nil && ask.Body != "" {
		b.WriteString("\n")
		for _, line := range strings.Split(strings.TrimRight(ask.Body, "\n"), "\n") {
			b.WriteString("      " + textStyle.Render(line) + "\n")
		}
		b.WriteString("\n")
	}

	switch {
	case ask != nil && ask.State == store.AskAnswered:
		b.WriteString("      " + warnStyle.Render("answered · "+pendingLabel(m.pending[ask.ID])) + "\n")
	case selected && m.replying:
		b.WriteString("      " + m.reply.View() + "\n")
	case selected:
		b.WriteString("      " + faintStyle.Render(itemHint(item)) + "\n")
	}
	return b.String()
}

// fallbackKind labels a card for an agent that stopped without filing an ask.
func fallbackKind(v core.TaskView) string {
	if v.AgentStatus == herdrx.StatusDone {
		return "✓ DONE"
	}
	return "▲ BLOCKED"
}

func renderOptions(ask *store.Ask, width int) string {
	labels := make([]string, len(ask.Options))
	labelWidth := 0
	for i, opt := range ask.Options {
		labels[i] = opt.Label
		if i == ask.Recommended {
			labels[i] = "★ " + opt.Label
		}
		labelWidth = maxInt(labelWidth, len([]rune(labels[i])))
	}
	labelWidth = minInt(labelWidth, maxInt(16, width/2))

	var b strings.Builder
	for i, opt := range ask.Options {
		style := textStyle
		if i == ask.Recommended {
			style = selectedStyle
		}
		line := "      " + style.Render(optionKey(i)) + "  " + style.Render(cell(labels[i], labelWidth))
		if opt.Detail != "" {
			line += "  " + dimStyle.Render(truncate(opt.Detail, maxInt(10, width-labelWidth-12)))
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// renderChecklist puts a chore's items on one line when they fit, else one
// per line.
func renderChecklist(items []string, prefix string, width int) string {
	if len(items) == 0 {
		return ""
	}
	boxes := make([]string, len(items))
	for i, item := range items {
		boxes[i] = "☐ " + item
	}
	if line := strings.Join(boxes, "   "); len([]rune(prefix+line)) <= width {
		return prefix + textStyle.Render(line) + "\n"
	}
	var b strings.Builder
	for _, box := range boxes {
		b.WriteString(prefix + textStyle.Render(truncate(box, maxInt(20, width-len(prefix)))) + "\n")
	}
	return b.String()
}

// itemHint names the keys that act on the selected item.
func itemHint(item core.InboxItem) string {
	ask := item.Ask
	switch {
	case ask == nil:
		return "o open the conversation · enter more of the screen"
	case ask.Kind == store.AskChore:
		hint := "d mark done · o open the conversation"
		if ask.Body != "" {
			hint += " · enter read the full message"
		}
		return hint
	}
	letters := make([]string, len(ask.Options))
	for i := range ask.Options {
		letters[i] = optionKey(i)
	}
	hint := "m reply · o open the conversation"
	if len(letters) > 0 {
		hint = strings.Join(letters, "/") + " answer and the agent carries on · " + hint
	}
	if ask.Body != "" {
		hint += " · enter read the full message"
	}
	return hint
}

func renderWorking(v core.TaskView, width int, now time.Time) string {
	glyph := dimStyle.Render("○")
	if v.AgentStatus == herdrx.StatusWorking {
		glyph = okStyle.Render("●")
	}
	doing := v.Title
	if v.Report != nil && v.Report.Line != "" {
		doing = v.Report.Line
		if v.Report.Total > 0 {
			doing += fmt.Sprintf(" · %d of %d", v.Report.Done, v.Report.Total)
		}
	}
	const roleWidth, projectWidth = 12, 12
	doingWidth := maxInt(16, width-roleWidth-projectWidth-18)
	return fmt.Sprintf("    %s %s  %s  %s  %s\n", glyph,
		textStyle.Render(cell(v.Role, roleWidth)),
		dimStyle.Render(cell(v.ProjectName, projectWidth)),
		textStyle.Render(cell(doing, doingWidth)),
		dimStyle.Render(v.Age(now)))
}

// screenLines splits an agent's screen into lines, dropping trailing blanks.
func screenLines(screen string) []string {
	lines := strings.Split(screen, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t\r")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// contentWidth is the width the inbox lays out to: the terminal's, within
// reason, and a sensible default before the first resize arrives.
func (m *model) contentWidth() int {
	if m.width <= 0 {
		return 100
	}
	return maxInt(60, minInt(m.width-2, 140))
}
