package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/matthewalunni/dispatch/internal/core"
)

func (m *model) View() string {
	if m.quitting {
		return ""
	}

	header, banner, footer := m.viewHeader(), m.viewBanner(), m.viewFooter()
	sections := func(body string) string {
		parts := []string{header, body}
		if banner != "" {
			parts = append(parts, banner)
		}
		parts = append(parts, footer)
		return strings.Join(parts, "\n") + "\n"
	}

	// Bubble Tea keeps only the last height lines of a view that is too tall,
	// which would cut the tab bar off, so the body gets what the header,
	// banner and footer leave over.
	maxBody := 0
	if m.height > 0 {
		maxBody = maxInt(1, m.height-lineCount(sections(""))+1)
	}

	var body string
	switch m.screen {
	case screenMain:
		body = m.viewTab(maxBody)
	case screenNew:
		body = m.viewNew()
	case screenDetail:
		body = m.viewDetail()
	case screenHelp:
		body = m.viewHelp()
	}
	return sections(body)
}

// viewTab renders the active tab; maxBody, when positive, is how many lines
// the Inbox and Board may take.
func (m *model) viewTab(maxBody int) string {
	switch m.tab {
	case tabBoard:
		return m.viewBoard(maxBody)
	case tabRecent:
		return m.viewTasks()
	case tabRoles:
		return m.viewRoles()
	default:
		return m.viewInbox(maxBody)
	}
}

// lineCount is how many terminal lines s draws.
func lineCount(s string) int { return strings.Count(s, "\n") + 1 }

// fitLines cuts s to at most max lines, keeping lines focusStart..focusEnd in
// view. A non-positive max means no limit.
func fitLines(s string, focusStart, focusEnd, max int) string {
	lines := strings.Split(s, "\n")
	if max <= 0 || len(lines) <= max {
		return s
	}
	return strings.Join(scrollWindow(lines, focusStart, focusEnd, max), "\n")
}

// scrollWindow returns height lines around the focus, the top of the focus
// winning when it is taller than the window. A line at each end says when
// there is more beyond it.
func scrollWindow(lines []string, focusStart, focusEnd, height int) []string {
	if len(lines) <= height {
		return lines
	}
	if height < 3 {
		start := maxInt(0, minInt(focusStart, len(lines)-height))
		return lines[start : start+height]
	}
	inner := height - 2
	start := 0
	if focusEnd >= inner {
		start = minInt(focusStart, focusEnd+1-inner)
	}
	start = maxInt(0, minInt(start, len(lines)-inner))
	above, below := "", ""
	if start > 0 {
		above = "  " + faintStyle.Render("↑ more")
	}
	if start+inner < len(lines) {
		below = "  " + faintStyle.Render("↓ more")
	}
	out := make([]string, 0, height)
	out = append(out, above)
	out = append(out, lines[start:start+inner]...)
	return append(out, below)
}

func (m *model) viewHeader() string {
	needsYou, working := 0, 0
	if m.overview != nil {
		needsYou, working = m.overview.NeedsYou, m.overview.WorkingCount
	}
	// Say plainly whether status is arriving live from herdr or on the slower
	// polling fallback, so a stale-looking screen is never a mystery.
	live := faintStyle.Render("○ polling")
	if m.live {
		live = okStyle.Render("● live")
	}
	return fmt.Sprintf("\n  %s  %s\n", renderTabBar(m.tab, needsYou, working, m.scopeLabel()), live)
}

// renderTabBar draws the header line: where you are, the tabs with the active
// one bracketed, and the two counts that matter at a glance.
func renderTabBar(active tab, needsYou, working int, scope string) string {
	parts := []string{titleStyle.Render("Dispatch"), dimStyle.Render(scope)}
	for t := tabInbox; t < tabCount; t++ {
		name := fmt.Sprintf("%d %s", int(t)+1, t.title())
		if t == active {
			parts = append(parts, selectedStyle.Render("["+name+"]"))
		} else {
			parts = append(parts, dimStyle.Render(name))
		}
	}
	needStyle := dimStyle
	if needsYou > 0 {
		needStyle = warnStyle
	}
	parts = append(parts,
		needStyle.Render(fmt.Sprintf("● %d need you", needsYou)),
		okStyle.Render(fmt.Sprintf("● %d working", working)))
	return strings.Join(parts, "  ")
}

// scopeLabel names what the tabs are showing: every project, or this one.
func (m *model) scopeLabel() string {
	if m.allProjects {
		return "all projects"
	}
	if m.ctx == nil {
		return "this project"
	}
	label := m.ctx.Project.Name
	if !m.ctx.Project.IsGit {
		label += " (not a repo)"
	}
	return label
}

func (m *model) viewBanner() string {
	if m.confirm != nil {
		return "\n  " + warnStyle.Render(m.confirm.prompt)
	}
	if m.err != nil {
		lines := strings.Split(strings.TrimRight(m.err.Error(), "\n"), "\n")
		width := maxInt(40, minInt(m.width-6, 90))
		return "\n" + indent(errorBoxStyle.Width(width).Render(strings.Join(lines, "\n")), 2)
	}
	if m.status != "" {
		return "\n  " + dimStyle.Render(m.status)
	}
	return ""
}

func (m *model) viewFooter() string {
	var keys string
	switch m.screen {
	case screenMain:
		keys = m.tabKeys()
	case screenDetail:
		keys = "enter open session · x stop · q back"
	case screenNew:
		keys = "tab next field · ←/→ change · enter dispatch · esc cancel"
	case screenHelp:
		keys = "? or q to close"
	}
	return "\n" + footerStyle.Render("  "+keys)
}

func (m *model) tabKeys() string {
	const shared = "1-4 tabs · p projects · n new · ? help · q quit"
	switch m.tab {
	case tabInbox:
		if m.replying {
			return "enter send · tab option · esc cancel"
		}
		return "j/k move · a/b choose · m reply · d chore done · enter more · o open · x stop · " + shared
	case tabBoard:
		return "h/l column · j/k card · enter details · o open · x stop · " + shared
	case tabRecent:
		return "↑/↓ move · enter details · o open session · x stop · " + shared
	case tabRoles:
		return "↑/↓ move · enter dispatch with this role · " + shared
	default:
		return shared
	}
}

func (m *model) viewTasks() string {
	tasks := m.views
	var b strings.Builder
	fmt.Fprintf(&b, "\n  %s %s\n\n", titleStyle.Render("Recent"), dimStyle.Render(fmt.Sprintf("(%d)", len(tasks))))

	if m.loading && len(tasks) == 0 {
		b.WriteString("  " + dimStyle.Render("loading…") + "\n")
		return b.String()
	}
	if len(tasks) == 0 {
		b.WriteString("  " + dimStyle.Render("no tasks yet — press n to dispatch one") + "\n")
		return b.String()
	}

	now := time.Now().UTC()
	const (
		projectWidth = 14
		roleWidth    = 13
		statusWidth  = 9
		ageWidth     = 4
		refWidth     = 28
	)
	// The reference column is what makes two identically titled tasks
	// distinguishable, so it earns its space whenever there is room.
	fixed := projectWidth + roleWidth + statusWidth + ageWidth + 14
	showRef := m.width >= fixed+refWidth+24
	if showRef {
		fixed += refWidth + 2
	}
	titleWidth := maxInt(16, minInt(46, m.width-fixed))

	header := fmt.Sprintf("  %s  %s  %s  %s  %s",
		cell("PROJECT", projectWidth), cell("TASK", titleWidth),
		cell("ROLE", roleWidth), cell("STATUS", statusWidth), cell("AGE", ageWidth))
	if showRef {
		header += "  " + cell("REF", refWidth)
	}
	fmt.Fprintln(&b, headerStyle.Render(header))

	for i, view := range tasks {
		cursor := "  "
		style := textStyle
		if i == m.taskCursor {
			cursor = selectedStyle.Render("› ")
			style = selectedStyle
		}
		line := fmt.Sprintf("%s%s  %s  %s  %s  %s",
			cursor,
			style.Render(cell(view.ProjectName, projectWidth)),
			style.Render(cell(view.Title, titleWidth)),
			dimStyle.Render(cell(view.Role, roleWidth)),
			statusStyle(view.Effective).Render(cell(string(view.Effective), statusWidth)),
			dimStyle.Render(cell(view.Age(now), ageWidth)))
		if showRef {
			line += "  " + dimStyle.Render(cellTail(view.Slug, refWidth))
		}
		fmt.Fprintln(&b, line)
	}
	return b.String()
}

func (m *model) viewNew() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n  %s\n\n", titleStyle.Render("New task"))

	fmt.Fprintf(&b, "  %s%s\n", label("Task", m.form.field == fieldTask), m.form.input.View())
	fmt.Fprintf(&b, "  %s%s\n", label("Role", m.form.field == fieldRole), m.roleField())
	fmt.Fprintf(&b, "  %s%s\n", label("Isolation", m.form.field == fieldIsolation), m.form.isolationLabel(m.ctx))

	b.WriteString("\n")
	if m.ctx != nil {
		fmt.Fprintf(&b, "  %s%s\n", fieldLabelStyle.Render("Project"), dimStyle.Render(m.ctx.Project.Root))
		if role, err := m.ctx.Roles.Get(m.form.selectedRole()); err == nil {
			fmt.Fprintf(&b, "  %s%s\n", fieldLabelStyle.Render("Runtime"), dimStyle.Render(role.Runtime))
			b.WriteString("\n  " + faintStyle.Render(truncate(firstLine(role.Description), maxInt(20, m.width-10))) + "\n")
		}
	}
	if m.form.submitting {
		b.WriteString("\n  " + dimStyle.Render("preparing workspace and launching agent…") + "\n")
	}
	return b.String()
}

func (m *model) roleField() string {
	name := m.form.selectedRole()
	if name == "" {
		return dimStyle.Render("no roles installed")
	}
	arrows := faintStyle.Render(" ←/→ ")
	return name + arrows + dimStyle.Render(fmt.Sprintf("(%d/%d)", m.form.roleIdx+1, len(m.form.roleNames)))
}

func (m *model) viewRoles() string {
	all := m.allRoles()
	var b strings.Builder
	fmt.Fprintf(&b, "\n  %s\n\n", titleStyle.Render("Roles"))
	if len(all) == 0 {
		b.WriteString("  " + dimStyle.Render("no roles installed; run `dispatch doctor`") + "\n")
		return b.String()
	}
	for i, role := range all {
		cursor := "  "
		style := textStyle
		if i == m.roleCursor {
			cursor = selectedStyle.Render("› ")
			style = selectedStyle
		}
		fmt.Fprintf(&b, "%s%s  %s  %s\n",
			cursor,
			style.Render(cell(role.Name, 16)),
			dimStyle.Render(cell(role.Runtime+"/"+string(role.Isolation), 18)),
			faintStyle.Render(truncate(role.Description, maxInt(20, m.width-42))))
	}
	if m.roleCursor < len(all) {
		role := all[m.roleCursor]
		b.WriteString("\n  " + faintStyle.Render(role.Source) + "\n")
	}
	return b.String()
}

func (m *model) viewDetail() string {
	view, ok := m.selected()
	if !ok {
		return "\n  " + dimStyle.Render("no task selected") + "\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n  %s\n\n", titleStyle.Render(truncate(view.Title, maxInt(20, m.width-6))))

	row := func(k, v string) {
		if v == "" {
			return
		}
		fmt.Fprintf(&b, "  %s%s\n", fieldLabelStyle.Render(k), textStyle.Render(v))
	}
	row("Status", string(view.Effective))
	row("Role", view.Role)
	row("Runtime", view.Runtime)
	row("Project", view.ProjectRoot)
	row("Isolation", string(view.Isolation))
	row("Branch", view.Branch)
	row("Worktree", view.Worktree)
	row("Agent", view.HerdrAgent)
	row("Workspace", view.HerdrWorkspaceID)
	row("Pane", view.HerdrPaneID)
	row("Parent", view.ParentTaskID)
	row("Age", view.Age(time.Now().UTC()))
	row("Id", view.ID)
	if view.Task.Error != "" {
		fmt.Fprintf(&b, "  %s%s\n", fieldLabelStyle.Render("Note"), warnStyle.Render(view.Task.Error))
	}
	if view.Description != "" {
		b.WriteString("\n  " + faintStyle.Render(truncate(view.Description, maxInt(20, m.width-6))) + "\n")
	}
	return b.String()
}

func (m *model) viewHelp() string {
	lines := []string{
		"dispatch is a control centre. Conversations live in herdr;",
		"this is where you start them and find your way back.",
		"",
		"  1 2 3 4    Inbox / Board / Recent / Roles (tab cycles)",
		"  a b c …    answer a decision with that option (★ is the agent's pick)",
		"  m          reply to a decision in your own words",
		"  d          mark a chore done",
		"  enter      read the full message, or open details",
		"  n          new task",
		"  o          open the agent's herdr session in this terminal",
		"  x          stop the selected task's agent",
		"  p          all projects / this project",
		"  j k ↑ ↓    move (on the Board h l ← → change column)",
		"  r          refresh",
		"  q esc      back, or quit from the tabs",
		"  ctrl+c     quit",
		"  ?          this help",
		"",
		"Statuses come from herdr:",
		"  working    the agent is producing work",
		"  waiting    it hit an approval or question and needs you",
		"  idle       ready for input",
		"  done       finished a turn you have not looked at",
		"  detached   dispatch has the task but herdr lost the session",
		"  ended      the session closed outside dispatch; it comes back if the agent does",
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n  %s\n\n", titleStyle.Render("Help"))
	for _, line := range lines {
		if line == "" {
			b.WriteString("\n")
			continue
		}
		fmt.Fprintf(&b, "  %s\n", dimStyle.Render(line))
	}
	return b.String()
}

// ------------------------------------------------------------------ helpers

func statusStyle(status core.EffectiveStatus) lipgloss.Style {
	switch status {
	case core.EffectiveWorking:
		return okStyle
	case core.EffectiveWaiting:
		return warnStyle
	case core.EffectiveDetached, core.EffectiveFailed:
		return failStyle
	case core.EffectiveStopped, core.EffectiveCompleted, core.EffectiveEnded:
		return faintStyle
	default:
		return dimStyle
	}
}

func label(text string, active bool) string {
	if active {
		return activeFieldStyle.Render(text)
	}
	return fieldLabelStyle.Render(text)
}

// cell renders a fixed-width column: truncated if too long, padded if short,
// so a long role or project name can never shunt the columns after it.
func cell(s string, width int) string {
	return pad(truncate(s, width), width)
}

// cellTail renders a fixed-width column keeping the END of the value. Task
// references are disambiguated by a trailing "-2"/"-3", so trimming the head
// is the only truncation that keeps them tellable apart.
func cellTail(s string, width int) string {
	runes := []rune(s)
	if len(runes) <= width || width <= 1 {
		return pad(s, width)
	}
	return pad("…"+string(runes[len(runes)-(width-1):]), width)
}

func pad(s string, width int) string {
	runes := []rune(s)
	if len(runes) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(runes))
}

func indent(s string, n int) string {
	prefix := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
