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

// screen is what fills the body: the tabbed main view, or a screen laid over it.
type screen int

const (
	screenMain screen = iota
	screenNew
	screenDetail
	screenHelp
)

// tab is one of the main view's tabs, switched with 1–4 or tab.
type tab int

const (
	tabInbox tab = iota
	tabBoard
	tabRecent
	tabRoles
	tabCount
)

func (t tab) title() string {
	switch t {
	case tabBoard:
		return "Board"
	case tabRecent:
		return "Recent"
	case tabRoles:
		return "Roles"
	default:
		return "Inbox"
	}
}

type model struct {
	app     *core.App
	version string

	screen   screen
	prev     screen
	tab      tab
	width    int
	height   int
	quitting bool

	// allProjects shows every project's tasks; off narrows to the current one.
	allProjects bool

	// Inbox and board data.
	overview    *core.Overview
	inboxCursor int
	// expanded shows the selected item's full body or screen.
	expanded bool
	replying bool
	reply    textinput.Model
	// replyAskID is the ask the open reply answers, fixed when it was opened
	// so a reload that moves the cursor cannot redirect it.
	replyAskID string
	// replyOption is the option sent with the reply, -1 for none; tab cycles it.
	replyOption int
	// inFlight holds asks whose answer is being sent, so a second key press
	// does not send it twice.
	inFlight map[string]bool
	// pending remembers why an answer could not be delivered, by ask id, for
	// as long as the ask stays answered.
	pending map[string]string

	// Board cursor: the column and the card within it.
	boardCol core.Column
	boardRow int

	// Recent list
	views      []core.TaskView
	taskCursor int
	loading    bool
	// loadInFlight keeps one load running at a time, so an older load can
	// never land over a newer one; reloadAfter asks for one more once it does.
	loadInFlight bool
	reloadAfter  bool

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

	reply := textinput.New()
	reply.Placeholder = "reply to the agent"
	reply.CharLimit = 1000
	reply.Width = 60
	reply.Prompt = "reply › "

	return &model{
		app:         app,
		version:     version,
		screen:      screenMain,
		tab:         tabInbox,
		boardCol:    core.ColumnNeedsYou,
		allProjects: true,
		form:        form{input: input},
		reply:       reply,
		replyOption: -1,
		inFlight:    map[string]bool{},
		pending:     map[string]string{},
		loading:     true,
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.loadCmd(), m.startWatch())
}

// ---------------------------------------------------------------- messages

type loadedMsg struct {
	ctx      *core.Context
	overview *core.Overview
	// views is the Recent list, loaded only when recent is set.
	views  []core.TaskView
	recent bool
	err    error
}

type answeredMsg struct {
	askID  string
	result *core.AnswerResult
	err    error
}

type dispatchedMsg struct {
	result *core.DispatchResult
	err    error
}

type stoppedMsg struct {
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

// loadCmd starts a load, or, if one is already running, queues a single
// follow-up so changes that arrive meanwhile are still picked up.
func (m *model) loadCmd() tea.Cmd {
	if m.loadInFlight {
		m.reloadAfter = true
		return nil
	}
	m.loadInFlight = true
	return m.fetchCmd()
}

func (m *model) fetchCmd() tea.Cmd {
	app := m.app
	allProjects := m.allProjects
	// The Recent list costs a second hydrate, so it loads only when shown.
	recent := m.tab == tabRecent
	return func() tea.Msg {
		ctx := context.Background()
		rc, err := app.Resolve("")
		if err != nil {
			return loadedMsg{err: err}
		}
		root := ""
		if !allProjects {
			root = rc.Project.Root
		}

		overview, err := app.Overview(ctx, core.OverviewOptions{ProjectRoot: root})
		if err != nil {
			return loadedMsg{ctx: rc, err: err}
		}
		msg := loadedMsg{ctx: rc, overview: overview}
		if !recent {
			return msg
		}
		msg.recent = true

		tasks, err := app.Tasks(ctx, store.Filter{IncludeTerminal: true, Limit: 50})
		if err != nil {
			msg.err = err
			return msg
		}
		for _, v := range app.Hydrate(ctx, tasks) {
			if root == "" || v.ProjectRoot == root {
				msg.views = append(msg.views, v)
			}
		}
		return msg
	}
}

// answerCmd records and delivers an answer; the reply arrives as answeredMsg.
func (m *model) answerCmd(askID string, ans core.Answer) tea.Cmd {
	app := m.app
	m.inFlight[askID] = true
	return func() tea.Msg {
		result, err := app.AnswerAsk(context.Background(), askID, ans)
		return answeredMsg{askID: askID, result: result, err: err}
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
		m.loadInFlight = false
		var again tea.Cmd
		if m.reloadAfter {
			m.reloadAfter = false
			again = m.loadCmd()
		}
		if msg.err != nil {
			m.err = msg.err
			return m, again
		}
		m.ctx = msg.ctx
		m.setOverview(msg.overview)
		if msg.recent {
			m.views = msg.views
		}
		m.form.syncRoles(msg.ctx)
		if m.taskCursor >= len(m.views) {
			m.taskCursor = maxInt(0, len(m.views)-1)
		}
		return m, again

	case answeredMsg:
		delete(m.inFlight, msg.askID)
		if msg.err != nil {
			m.err = msg.err
			return m, m.loadCmd()
		}
		m.err = nil
		if msg.result.Delivered {
			delete(m.pending, msg.askID)
			m.status = "answered · delivered"
		} else {
			m.pending[msg.askID] = msg.result.Reason
			m.status = "answered · " + pendingLabel(msg.result.Reason)
		}
		return m, m.loadCmd()

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
		m.screen = screenMain
		return m, m.loadCmd()

	case stoppedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.status = fmt.Sprintf("stopped %q", msg.title)
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

	// A reply being typed owns space, enter and esc, even under an error.
	if m.replying {
		return m.handleReplyKey(msg)
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
		switch {
		case m.screen == screenMain && key == "esc" && m.expanded:
			// esc folds an opened item away before it quits anything.
			m.expanded = false
		case m.screen == screenMain:
			return m, m.quit()
		case m.screen == screenHelp:
			m.screen = m.prev
		default:
			m.screen = screenMain
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
	case screenDetail:
		return m.handleDetailKey(key)
	case screenMain:
		return m.handleMainKey(key)
	}
	return m, nil
}

// handleMainKey takes the keys every tab shares, then the active tab's own.
func (m *model) handleMainKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "1", "2", "3", "4":
		return m, m.switchTab(tab(key[0] - '1'))
	case "tab":
		return m, m.switchTab(tab(wrap(int(m.tab)+1, int(tabCount))))
	case "shift+tab":
		return m, m.switchTab(tab(wrap(int(m.tab)-1, int(tabCount))))
	case "p":
		m.allProjects = !m.allProjects
		m.inboxCursor, m.taskCursor = 0, 0
		m.boardCol, m.boardRow = core.ColumnNeedsYou, 0
		return m, m.loadCmd()
	}

	switch m.tab {
	case tabInbox:
		return m.handleInboxKey(key)
	case tabBoard:
		return m.handleBoardKey(key)
	case tabRecent:
		return m.handleTasksKey(key)
	case tabRoles:
		return m.handleRolesKey(key)
	}
	return m, nil
}

func (m *model) switchTab(t tab) tea.Cmd {
	if t == m.tab {
		return nil
	}
	m.tab = t
	m.expanded = false
	if t == tabRecent {
		m.taskCursor = 0
		return m.loadCmd()
	}
	return nil
}

func (m *model) handleTasksKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		m.taskCursor = wrap(m.taskCursor-1, len(m.views))
	case "down", "j":
		m.taskCursor = wrap(m.taskCursor+1, len(m.views))
	case "enter":
		if _, ok := m.selected(); ok {
			m.screen = screenDetail
		}
	case "o":
		if selected, ok := m.selected(); ok {
			return m, m.openCmd(selected)
		}
	case "x":
		if selected, ok := m.selected(); ok {
			m.confirmStop(selected)
		}
	}
	return m, nil
}

func (m *model) handleDetailKey(key string) (tea.Model, tea.Cmd) {
	selected, ok := m.selected()
	if !ok {
		return m, nil
	}
	switch key {
	case "enter", "o":
		return m, m.openCmd(selected)
	case "x":
		m.confirmStop(selected)
	}
	return m, nil
}

func (m *model) confirmStop(view core.TaskView) {
	m.confirm = &confirmation{
		prompt: fmt.Sprintf("Stop %q? Its worktree is kept. (y/N)", truncate(view.Title, 48)),
		action: func(m *model) tea.Cmd { return m.stopCmd(view) },
	}
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
		m.screen = screenMain
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

// selected is the task the detail screen, open and stop act on: the board's
// card on the Board, the Recent list's row otherwise.
func (m *model) selected() (core.TaskView, bool) {
	if m.tab == tabBoard {
		return m.boardSelected()
	}
	if m.taskCursor < 0 || m.taskCursor >= len(m.views) {
		return core.TaskView{}, false
	}
	return m.views[m.taskCursor], true
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
