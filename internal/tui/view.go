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

	var body string
	switch m.screen {
	case screenHome:
		body = m.viewHome()
	case screenTasks:
		body = m.viewTasks()
	case screenNew:
		body = m.viewNew()
	case screenRoles:
		body = m.viewRoles()
	case screenDetail:
		body = m.viewDetail()
	case screenHelp:
		body = m.viewHelp()
	}

	sections := []string{m.viewHeader(), body}
	if banner := m.viewBanner(); banner != "" {
		sections = append(sections, banner)
	}
	sections = append(sections, m.viewFooter())
	return strings.Join(sections, "\n") + "\n"
}

func (m *model) viewHeader() string {
	title := titleStyle.Render("Dispatch")
	right := ""
	if m.ctx != nil {
		right = dimStyle.Render(m.ctx.Project.Name)
		if !m.ctx.Project.IsGit {
			right += faintStyle.Render(" (not a repo)")
		}
	}
	return fmt.Sprintf("\n  %s  %s\n", title, right)
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
	case screenHome:
		keys = "↑/↓ move · enter select · n new task · r refresh · ? help · q quit"
	case screenTasks:
		keys = "↑/↓ move · enter details · o open session · x stop · tab switch list · n new · r refresh · q back"
	case screenDetail:
		keys = "enter open session · x stop · q back"
	case screenNew:
		keys = "tab next field · ←/→ change · enter dispatch · esc cancel"
	case screenRoles:
		keys = "↑/↓ move · enter dispatch with this role · q back"
	case screenHelp:
		keys = "? or q to close"
	}
	return "\n" + footerStyle.Render("  "+keys)
}

func (m *model) viewHome() string {
	var b strings.Builder
	counts := map[string]string{
		"Active":    fmt.Sprintf("%d", m.counts.Active),
		"Needs you": fmt.Sprintf("%d", m.attention),
		"Recent":    fmt.Sprintf("%d", m.counts.Total),
		"Roles":     fmt.Sprintf("%d", len(m.allRoles())),
	}

	b.WriteString("\n")
	for i, item := range homeItems {
		cursor := "  "
		label := textStyle.Render(item.label)
		if i == m.homeCursor {
			cursor = selectedStyle.Render("› ")
			label = selectedStyle.Render(item.label)
		}
		count := ""
		if value, ok := counts[item.label]; ok {
			style := dimStyle
			if item.label == "Needs you" && m.attention > 0 {
				style = warnStyle
			}
			count = "  " + style.Render(value)
		}
		fmt.Fprintf(&b, "  %s%s%s\n", cursor, label, count)
	}

	if m.ctx != nil {
		b.WriteString("\n")
		fmt.Fprintf(&b, "  %s\n", faintStyle.Render(m.ctx.Project.Root))
		if m.ctx.Project.HasDispatchDir {
			fmt.Fprintf(&b, "  %s\n", faintStyle.Render(".dispatch/ overrides active"))
		}
	}
	return b.String()
}

func (m *model) viewTasks() string {
	tasks := m.visibleTasks()
	var b strings.Builder
	fmt.Fprintf(&b, "\n  %s %s\n\n", titleStyle.Render(m.scope.title()), dimStyle.Render(fmt.Sprintf("(%d)", len(tasks))))

	if m.loading && len(tasks) == 0 {
		b.WriteString("  " + dimStyle.Render("loading…") + "\n")
		return b.String()
	}
	if len(tasks) == 0 {
		b.WriteString("  " + dimStyle.Render(emptyMessage(m.scope)) + "\n")
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

func emptyMessage(scope taskScope) string {
	switch scope {
	case scopeAttention:
		return "nothing is waiting on you"
	case scopeRecent:
		return "no tasks yet — press n to dispatch one"
	default:
		return "no active tasks — press n to dispatch one"
	}
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
		"  n          new task",
		"  enter      select / open details",
		"  o          open the agent's herdr session in this terminal",
		"  x          stop the selected task's agent",
		"  tab        switch between Active / Needs you / Recent",
		"  j k ↑ ↓    move",
		"  r          refresh",
		"  q esc      back, or quit from the home screen",
		"  ctrl+c     quit",
		"  ?          this help",
		"",
		"Statuses come from herdr:",
		"  working    the agent is producing work",
		"  waiting    it hit an approval or question and needs you",
		"  idle       ready for input",
		"  done       finished a turn you have not looked at",
		"  detached   dispatch has the task but herdr lost the session",
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
	case core.EffectiveStopped, core.EffectiveCompleted:
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
