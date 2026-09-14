package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/matthewalunni/dispatch/internal/core"
	"github.com/matthewalunni/dispatch/internal/roles"
	"github.com/matthewalunni/dispatch/internal/store"
)

type screen int

const (
	screenHome screen = iota
	screenTasks
	screenNew
	screenRoles
	screenDetail
	screenHelp
)

// taskScope selects which tasks the task screen is showing.
type taskScope int

const (
	scopeActive taskScope = iota
	scopeAttention
	scopeRecent
)

func (s taskScope) title() string {
	switch s {
	case scopeAttention:
		return "Needs you"
	case scopeRecent:
		return "Recent"
	default:
		return "Active"
	}
}

type model struct {
	app     *core.App
	version string

	screen   screen
	prev     screen
	width    int
	height   int
	quitting bool

	// Home
	homeCursor int

	// Task list
	scope      taskScope
	views      []core.TaskView
	taskCursor int
	loading    bool

	// Counts for the home screen
	counts    store.Counts
	attention int

	// Resolved project context, refreshed on load.
	ctx *core.Context

	// New task form
	form form

	// Roles screen
	roleCursor int

	// Live runtime state from herdr's event stream.
	changes     chan core.Change
	cancelWatch context.CancelFunc
	live        bool

	// Transient UI state
	err     error
	status  string
	confirm *confirmation
}

// confirmation is a yes/no gate in front of a destructive action.
type confirmation struct {
	prompt string
	action func(m *model) tea.Cmd
}

type form struct {
	input     textinput.Model
	roleNames []string
	roleIdx   int
	isolIdx   int
	// isolOverride is false while the role's own isolation mode is in use.
	isolOverride bool
	field        int
	submitting   bool
}

const (
	fieldTask = iota
	fieldRole
	fieldIsolation
	fieldCount
)

func run(app *core.App, version string) error {
	m := newModel(app, version)
	program := tea.NewProgram(m, tea.WithAltScreen())
	_, err := program.Run()
	return err
}

func newModel(app *core.App, version string) *model {
	input := textinput.New()
	input.Placeholder = "What should the agent do?"
	input.CharLimit = 500
	input.Width = 60
	input.Prompt = "› "

	return &model{
		app:     app,
		version: version,
		screen:  screenHome,
		form:    form{input: input},
		loading: true,
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.loadCmd(), m.startWatch())
}

// ---------------------------------------------------------------- messages

type loadedMsg struct {
	ctx       *core.Context
	views     []core.TaskView
	counts    store.Counts
	attention int
	err       error
}

type dispatchedMsg struct {
	result *core.DispatchResult
	err    error
}

type stoppedMsg struct {
	title string
	err   error
}

type completedMsg struct {
	title string
	err   error
}

type attachedMsg struct{ err error }

// changeMsg is one signal from herdr that live state moved. The TUI does not
// trust its payload: it re-reads, because dispatch's own status can move
// without any herdr event.
type changeMsg core.Change

// watchEndedMsg means the watcher stopped; the TUI keeps working on whatever
// the last load showed rather than freezing.
type watchEndedMsg struct{}

// waitForChange blocks one Bubble Tea command on the next runtime change.
func waitForChange(ch chan core.Change) tea.Cmd {
	return func() tea.Msg {
		change, ok := <-ch
		if !ok {
			return watchEndedMsg{}
		}
		return changeMsg(change)
	}
}

// startWatch subscribes to live runtime changes.
//
// core.Watch prefers herdr's event stream and falls back to polling on its
// own, so the TUI has one path whether or not live events are available.
func (m *model) startWatch() tea.Cmd {
	if m.cancelWatch != nil {
		m.cancelWatch()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelWatch = cancel
	ch := make(chan core.Change, 32)
	m.changes = ch

	app := m.app
	go func() {
		defer close(ch)
		_ = app.Watch(ctx, core.WatchOptions{}, ch)
	}()
	return waitForChange(ch)
}

func (m *model) loadCmd() tea.Cmd {
	app := m.app
	scope := m.scope
	return func() tea.Msg {
		ctx := context.Background()
		rc, err := app.Resolve("")
		if err != nil {
			return loadedMsg{err: err}
		}

		filter := store.Filter{}
		switch scope {
		case scopeRecent:
			filter = store.Filter{IncludeTerminal: true, Limit: 50}
		default:
			filter = store.ActiveFilter()
		}
		tasks, err := app.Tasks(ctx, filter)
		if err != nil {
			return loadedMsg{ctx: rc, err: err}
		}
		views := app.Hydrate(ctx, tasks)

		// Attention count always reflects live tasks, whatever is on screen.
		active := views
		if scope == scopeRecent {
			live, err := app.Tasks(ctx, store.ActiveFilter())
			if err == nil {
				active = app.Hydrate(ctx, live)
			}
		}
		attention := 0
		for _, v := range active {
			if v.Effective.NeedsAttention() {
				attention++
			}
		}

		counts, _ := app.Store().Count(ctx)
		return loadedMsg{ctx: rc, views: views, counts: counts, attention: attention}
	}
}

func (m *model) dispatchCmd() tea.Cmd {
	app := m.app
	title := strings.TrimSpace(m.form.input.Value())
	role := m.form.selectedRole()
	isolation := ""
	if m.form.isolOverride {
		isolation = string(roles.IsolationModes()[m.form.isolIdx])
	}
	return func() tea.Msg {
		result, err := app.Dispatch(context.Background(), core.DispatchRequest{
			Title:     title,
			Role:      role,
			Isolation: isolation,
		})
		return dispatchedMsg{result: result, err: err}
	}
}

func (m *model) stopCmd(view core.TaskView) tea.Cmd {
	app := m.app
	return func() tea.Msg {
		_, err := app.Stop(context.Background(), view.ID, core.StopOptions{})
		return stoppedMsg{title: view.Title, err: err}
	}
}

// completeCmd marks a task done in dispatch's registry and deliberately
// leaves its herdr session alone — the same thing `dispatch done` does.
func (m *model) completeCmd(view core.TaskView) tea.Cmd {
	app := m.app
	return func() tea.Msg {
		_, err := app.Complete(context.Background(), view.ID)
		return completedMsg{title: view.Title, err: err}
	}
}

// openCmd suspends the TUI and hands the terminal to the live herdr session,
// returning here when the user detaches.
func (m *model) openCmd(view core.TaskView) tea.Cmd {
	app := m.app
	return func() tea.Msg {
		result, err := app.Open(context.Background(), view.ID)
		if err != nil {
			return attachedMsg{err: err}
		}
		argv := result.AttachCommand
		if len(argv) == 0 {
			return attachedMsg{}
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		return tea.ExecProcess(cmd, func(err error) tea.Msg {
			return attachedMsg{err: err}
		})()
	}
}

// ------------------------------------------------------------------ update

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.form.input.Width = maxInt(30, msg.Width-24)
		return m, nil

	case changeMsg:
		m.live = msg.Live
		next := waitForChange(m.changes)
		if m.screen == screenNew {
			// Don't reload underneath someone who is typing; the next change
			// or the form being dismissed will pick the state back up.
			return m, next
		}
		return m, tea.Batch(m.loadCmd(), next)

	case watchEndedMsg:
		m.live = false
		return m, nil

	case loadedMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.ctx = msg.ctx
		m.views = msg.views
		m.counts = msg.counts
		m.attention = msg.attention
		m.form.syncRoles(msg.ctx)
		if m.taskCursor >= len(m.visibleTasks()) {
			m.taskCursor = maxInt(0, len(m.visibleTasks())-1)
		}
		return m, nil

	case dispatchedMsg:
		m.form.submitting = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.status = fmt.Sprintf("dispatched %q to %s", msg.result.Task.Title, msg.result.Task.Role)
		if len(msg.result.Warnings) > 0 {
			m.status += " (" + msg.result.Warnings[0] + ")"
		}
		m.form.input.SetValue("")
		m.form.field = fieldTask
		m.screen = screenTasks
		m.scope = scopeActive
		m.taskCursor = 0
		return m, m.loadCmd()

	case stoppedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.status = fmt.Sprintf("stopped %q", msg.title)
		return m, m.loadCmd()

	case completedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.status = fmt.Sprintf("completed %q", msg.title)
		return m, m.loadCmd()

	case attachedMsg:
		if msg.err != nil {
			m.err = msg.err
		}
		return m, m.loadCmd()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// A confirmation gate swallows everything until answered.
	if m.confirm != nil {
		switch key {
		case "y", "Y", "enter":
			action := m.confirm.action
			m.confirm = nil
			return m, action(m)
		default:
			m.confirm = nil
			return m, nil
		}
	}

	// ctrl+c always quits; errors never corrupt that.
	if key == "ctrl+c" {
		return m, m.quit()
	}

	// Dismiss an error banner with any key rather than trapping the user.
	if m.err != nil && (key == "esc" || key == "enter" || key == " ") {
		m.err = nil
		return m, nil
	}

	if m.screen == screenNew {
		return m.handleNewKey(msg)
	}

	switch key {
	case "?":
		if m.screen == screenHelp {
			m.screen = m.prev
		} else {
			m.prev = m.screen
			m.screen = screenHelp
		}
		return m, nil
	case "q", "esc":
		switch m.screen {
		case screenHome:
			return m, m.quit()
		case screenHelp:
			m.screen = m.prev
		default:
			m.screen = screenHome
		}
		return m, nil
	case "n":
		m.screen = screenNew
		m.form.field = fieldTask
		m.form.input.Focus()
		m.status = ""
		return m, textinput.Blink
	case "r":
		m.status = "refreshed"
		return m, m.loadCmd()
	}

	switch m.screen {
	case screenHome:
		return m.handleHomeKey(key)
	case screenTasks, screenDetail:
		return m.handleTasksKey(key)
	case screenRoles:
		return m.handleRolesKey(key)
	}
	return m, nil
}

var homeItems = []struct {
	label  string
	screen screen
	scope  taskScope
}{
	{"New task", screenNew, scopeActive},
	{"Active", screenTasks, scopeActive},
	{"Needs you", screenTasks, scopeAttention},
	{"Recent", screenTasks, scopeRecent},
	{"Roles", screenRoles, scopeActive},
}

func (m *model) handleHomeKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		m.homeCursor = wrap(m.homeCursor-1, len(homeItems))
	case "down", "j":
		m.homeCursor = wrap(m.homeCursor+1, len(homeItems))
	case "enter", "l", "right":
		item := homeItems[m.homeCursor]
		m.screen = item.screen
		m.scope = item.scope
		m.taskCursor = 0
		if item.screen == screenNew {
			m.form.field = fieldTask
			m.form.input.Focus()
			return m, textinput.Blink
		}
		return m, m.loadCmd()
	}
	return m, nil
}

func (m *model) handleTasksKey(key string) (tea.Model, tea.Cmd) {
	tasks := m.visibleTasks()
	switch key {
	case "up", "k":
		m.taskCursor = wrap(m.taskCursor-1, len(tasks))
	case "down", "j":
		m.taskCursor = wrap(m.taskCursor+1, len(tasks))
	case "tab":
		m.scope = taskScope(wrap(int(m.scope)+1, 3))
		m.taskCursor = 0
		return m, m.loadCmd()
	case "enter":
		if selected, ok := m.selected(); ok {
			if m.screen == screenDetail {
				return m, m.openCmd(selected)
			}
			m.screen = screenDetail
		}
	case "o":
		if selected, ok := m.selected(); ok {
			return m, m.openCmd(selected)
		}
	case "d":
		if selected, ok := m.selected(); ok {
			if selected.Status.Terminal() {
				m.status = fmt.Sprintf("%q is already %s", truncate(selected.Title, 40), selected.Status)
				return m, nil
			}
			// Completing is bookkeeping, not a teardown: the session keeps
			// running, so this needs no confirmation gate the way stopping
			// does. The task leaves the Active list, which is the point.
			if m.screen == screenDetail {
				// Its row is about to disappear from underneath the cursor.
				m.screen = screenTasks
			}
			return m, m.completeCmd(selected)
		}
	case "x":
		if selected, ok := m.selected(); ok {
			m.confirm = &confirmation{
				prompt: fmt.Sprintf("Stop %q? Its worktree is kept. (y/N)", truncate(selected.Title, 48)),
				action: func(m *model) tea.Cmd { return m.stopCmd(selected) },
			}
		}
	}
	return m, nil
}

func (m *model) handleRolesKey(key string) (tea.Model, tea.Cmd) {
	all := m.allRoles()
	switch key {
	case "up", "k":
		m.roleCursor = wrap(m.roleCursor-1, len(all))
	case "down", "j":
		m.roleCursor = wrap(m.roleCursor+1, len(all))
	case "enter":
		// Start a new task pre-set to this role: the common next action.
		if len(all) > 0 {
			m.form.selectRole(all[m.roleCursor].Name)
			m.screen = screenNew
			m.form.field = fieldTask
			m.form.input.Focus()
			return m, textinput.Blink
		}
	}
	return m, nil
}

func (m *model) handleNewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.form.input.Blur()
		m.screen = screenHome
		return m, nil
	case "tab", "down":
		m.form.field = wrap(m.form.field+1, fieldCount)
		return m, m.form.focusCurrent()
	case "shift+tab", "up":
		m.form.field = wrap(m.form.field-1, fieldCount)
		return m, m.form.focusCurrent()
	case "left":
		m.form.cycle(-1)
		return m, nil
	case "right":
		m.form.cycle(1)
		return m, nil
	case "enter":
		if strings.TrimSpace(m.form.input.Value()) == "" {
			m.err = fmt.Errorf("a task needs a description")
			return m, nil
		}
		if m.form.submitting {
			return m, nil
		}
		m.form.submitting = true
		m.status = "dispatching…"
		return m, m.dispatchCmd()
	}

	if m.form.field == fieldTask {
		var cmd tea.Cmd
		m.form.input, cmd = m.form.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

// ------------------------------------------------------------------ helpers

func (m *model) visibleTasks() []core.TaskView {
	if m.scope != scopeAttention {
		return m.views
	}
	var out []core.TaskView
	for _, v := range m.views {
		if v.Effective.NeedsAttention() {
			out = append(out, v)
		}
	}
	return out
}

func (m *model) selected() (core.TaskView, bool) {
	tasks := m.visibleTasks()
	if m.taskCursor < 0 || m.taskCursor >= len(tasks) {
		return core.TaskView{}, false
	}
	return tasks[m.taskCursor], true
}

func (m *model) allRoles() []roles.Role {
	if m.ctx == nil {
		return nil
	}
	return m.ctx.Roles.All()
}

func (f *form) syncRoles(rc *core.Context) {
	if rc == nil {
		return
	}
	previous := f.selectedRole()
	f.roleNames = rc.Roles.Names()
	if previous != "" {
		f.selectRole(previous)
		return
	}
	f.selectRole(rc.Config.DefaultRole)
}

func (f *form) selectRole(name string) {
	for i, candidate := range f.roleNames {
		if candidate == name {
			f.roleIdx = i
			return
		}
	}
}

func (f *form) selectedRole() string {
	if f.roleIdx < 0 || f.roleIdx >= len(f.roleNames) {
		return ""
	}
	return f.roleNames[f.roleIdx]
}

func (f *form) cycle(delta int) {
	switch f.field {
	case fieldRole:
		if len(f.roleNames) > 0 {
			f.roleIdx = wrap(f.roleIdx+delta, len(f.roleNames))
			// Choosing a role resets isolation back to that role's default.
			f.isolOverride = false
		}
	case fieldIsolation:
		modes := roles.IsolationModes()
		f.isolIdx = wrap(f.isolIdx+delta, len(modes))
		f.isolOverride = true
	}
}

func (f *form) focusCurrent() tea.Cmd {
	if f.field == fieldTask {
		f.input.Focus()
		return textinput.Blink
	}
	f.input.Blur()
	return nil
}

// isolationLabel shows the effective isolation mode and where it came from.
func (f *form) isolationLabel(rc *core.Context) string {
	if f.isolOverride {
		return string(roles.IsolationModes()[f.isolIdx]) + dimStyle.Render("  (override)")
	}
	if rc == nil {
		return "role default"
	}
	role, err := rc.Roles.Get(f.selectedRole())
	if err != nil {
		return "role default"
	}
	return string(role.Isolation) + dimStyle.Render("  (from role)")
}

// quit tears down the watcher before leaving, so the event subscription and
// its goroutine do not outlive the UI.
func (m *model) quit() tea.Cmd {
	m.quitting = true
	if m.cancelWatch != nil {
		m.cancelWatch()
		m.cancelWatch = nil
	}
	return tea.Quit
}

func wrap(value, length int) int {
	if length <= 0 {
		return 0
	}
	value %= length
	if value < 0 {
		value += length
	}
	return value
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return string(runes[:maxInt(max, 0)])
	}
	return strings.TrimRight(string(runes[:max-1]), " ") + "…"
}
