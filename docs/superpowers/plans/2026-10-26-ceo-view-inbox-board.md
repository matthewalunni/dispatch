# CEO view: Inbox, Board and proactive agents — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the `dispatch` control centre an Inbox of structured asks and a Board of the team, fed by agents that report status and file asks themselves, and stop dead sessions showing as active.

**Architecture:** A reconcile pass inside `core.App.Hydrate` ends tasks whose herdr agent is gone (and revives them if it returns) and redelivers pending answers. Asks and status lines persist in SQLite (`asks`, `task_reports` tables). Pure functions in `internal/core/overview.go` group hydrated views and asks into Inbox items and Board columns; the TUI renders them as tabs. Agents talk to dispatch through two new commands, `dispatch status` and `dispatch ask`, and every prompt carries a proactive-working section.

**Tech Stack:** Go, cobra, SQLite (existing store), Bubble Tea + lipgloss (existing TUI), herdr CLI via `internal/herdrx`.

**Spec:** `docs/superpowers/specs/2026-10-26-ceo-view-inbox-board-design.md`

## Global Constraints

- Ship the stale-task fix (Task 1) first, as its own commit, before anything else.
- Never write to herdr-owned state or to the user's config/role files; seeding never overwrites.
- `--json` output: fields may be added, existing names and meanings must not change (`internal/clix/output.go` contract).
- An unreachable herdr must degrade, never fail a read command, and never end a task.
- Existing keys keep working on every tab: `n`, `o`, `x`, `r` (refresh), `?`, `q`/`esc`. Reply is `m`.
- An ask is never shown as resolved until its answer was delivered to the agent.
- Run `make check` (vet + test) green at the end of every task.

## Review Focus

1. **herdr unreachable mid-session** — reconcile must neither end tasks nor drop pending answers; they wait. (Task 1, Task 3 tests.)
2. **Answering a `blocked` agent** — `PromptAgent` fails with `agent_blocked`; answer stays pending and is delivered on a later hydrate once the agent is not blocked. (Task 3 test.)
3. **Ask filed for a task that has already ended / from outside dispatch** (no `DISPATCH_TASK_ID`, no `--task`) — clear error, non-zero exit, no row written. (Task 4 test.)
4. **Narrow terminal (< 100 cols) and long titles** — Board stacks columns; titles truncate, never wrap the layout. (Task 8 test.)
5. **A task with several open asks, and a working agent with an open ask** — each ask is its own Inbox item; the task appears in Needs you on the Board, not Working. (Task 6 test.)

---

### Task 1: End tasks whose herdr session is gone

**Files:**
- Modify: `internal/store/task.go` (status constants, `Terminal`)
- Modify: `internal/store/store.go` (`SetStatus` completed_at, `Count`)
- Modify: `internal/fakes/fakes.go` (`ListErr error`, `Revive(agent herdrx.Agent)`)
- Modify: `internal/core/view.go` (`EffectiveEnded`, `resolveStatus`, reconcile in `Hydrate`)
- Modify: `internal/core/watch.go:272` (`DefaultWaitStates`)
- Modify: `internal/clix/tasks.go` (`statusLabel`, `ls` long help)
- Modify: `README.md` (status list)
- Test: `internal/core/core_test.go`, `internal/store/store_test.go`

**Interfaces:**
- Produces: `store.StatusEnded Status = "ended"` (terminal); `core.EffectiveEnded EffectiveStatus = "ended"`; `core.EndGrace = 60 * time.Second`; `func (a *App) reconcile(ctx context.Context, live map[string]herdrx.Agent) map[string]store.Status` called from `Hydrate` only when herdr is reachable. Later tasks add delivery retry into `reconcile`.

- [ ] **Step 1: Write failing tests** in `internal/core/core_test.go`:
  - `TestHydrateEndsATaskWhoseSessionIsGone`: dispatch, `h.Herdr.Kill(agent)`, rewrite the task's `CreatedAt` to `now-2m` via `Store().Update`, `Hydrate` an `ActiveViews` call → returned list excludes it; `Store().Get` shows `Status == store.StatusEnded` and `CompletedAt != nil`; `HydrateOne` gives `EffectiveEnded`.
  - `TestHydrateLeavesAFreshTaskAlone`: same but `CreatedAt` = now → still `running`, `EffectiveDetached` (existing `TestHydrateReportsADetachedSession` must keep passing unchanged).
  - `TestHydrateDoesNotEndTasksWhenHerdrIsUnreachable`: fake `ListAgents` errors (add `ListErr error` field to `fakes.Herdr`) → status stays `running`, effective `unknown`.
  - `TestHydrateRevivesAnEndedTaskWhoseAgentReturns`: end it as above, then re-add the agent to the fake (add `fakes.Herdr.Revive(agent herdrx.Agent)`), call `ActiveViews` → task back to `running`, `CompletedAt` cleared, listed.
  - `TestWaitTreatsEndedLikeDetached`: `DefaultWaitStates()` contains `EffectiveEnded`.
  - `internal/store/store_test.go` `TestEndedIsTerminalAndCounted`: `StatusEnded.Terminal()` true; `Count` puts it in a new `Counts.Ended` field; `ActiveFilter()` excludes it.

- [ ] **Step 2: Run** `go test ./internal/core/ ./internal/store/` — expect compile failures / FAIL on the new tests.

- [ ] **Step 3: Implement.**
  - `StatusEnded` with doc comment "the agent's session ended outside dispatch (closed in herdr, or the agent exited)". `Terminal()` includes it. `SetStatus` sets `completed_at` for `StatusEnded` too; when setting `StatusRunning`, set `completed_at = NULL`.
  - `Counts.Ended int \`json:"ended"\``.
  - In `Hydrate`, after building `live`, when `reachable`: call `a.reconcile(ctx, live)`, which returns `map[taskID]store.Status` of tasks it changed; apply those to the in-memory tasks before `resolveStatus`, so the same call reports `EffectiveEnded` / revived status. `ActiveViews` (and the TUI/`ls` active paths) then drop views whose `Task.Status.Terminal()`.
  - `reconcile`: load `store.List(Filter{Statuses: {StatusRunning, StatusEnded}})`; for each running task not in `live` with `a.now().Sub(CreatedAt) > EndGrace` → `SetStatus(ended)` and `WriteResult(a.dataDir, task, "session closed outside dispatch")`; for each ended task found in `live` → `SetStatus(running)`. Errors are logged-and-skipped (never fail Hydrate).
  - `resolveStatus`: `StatusEnded` → `EffectiveEnded` (before the alive checks).
  - `DefaultWaitStates` appends `EffectiveEnded`; `statusLabel` renders ended dim; `ls` help gains `ended      the session closed outside dispatch`.
  - README "Commands"/status text: one line for `ended`.

- [ ] **Step 4: Run** `make check` — all PASS.

- [ ] **Step 5: Commit** `git commit -m "End tasks whose herdr session is gone"`.

---

### Task 2: Store asks and status reports

**Files:**
- Create: `internal/store/ask.go`
- Modify: `internal/store/store.go` (migration `0003_asks_and_reports`)
- Test: `internal/store/ask_test.go`

**Interfaces:**
- Produces:
  ```go
  type AskKind string   // AskDecision = "decision", AskChore = "chore"
  type AskState string  // AskOpen = "open", AskAnswered = "answered" (pending delivery), AskDelivered = "delivered", AskWithdrawn = "withdrawn"
  type AskOption struct{ Label, Detail string } // json "label","detail"
  type Ask struct {
      ID, TaskID string; Kind AskKind; Title, Body string
      Options []AskOption; Recommended int // 0-based index, -1 for chores
      Items []string; Blocks string
      State AskState; Answer string
      CreatedAt time.Time; AnsweredAt, DeliveredAt *time.Time
  }
  type Report struct{ TaskID, Line string; Done, Total int; UpdatedAt time.Time }

  func (s *Store) CreateAsk(ctx, a *Ask) error
  func (s *Store) GetAsk(ctx, id string) (Ask, error)            // ErrNotFound
  func (s *Store) ListAsks(ctx, f AskFilter) ([]Ask, error)       // newest first
  func (s *Store) SetAskState(ctx, id string, state AskState, answer string) error // stamps answered_at / delivered_at
  func (s *Store) SetReport(ctx, r Report) error                  // upsert by task_id
  func (s *Store) Reports(ctx, taskIDs []string) (map[string]Report, error)
  type AskFilter struct{ TaskIDs []string; States []AskState }
  ```
- Ask IDs: `naming.AskID(now)` → `"ask_" + same timestamp+random format as TaskID` (add to `internal/naming/naming.go`, with a test beside `TaskID`'s).

- [ ] **Step 1: Write failing tests** `internal/store/ask_test.go`:
  - `TestAskRoundTrips`: create a decision with 2 options, `Recommended: 0`, body; `GetAsk` equals input (options and times included).
  - `TestListAsksFiltersByStateAndTask`: two tasks, three asks, one withdrawn → filter `States: {AskOpen}` returns 2; `TaskIDs` narrows.
  - `TestSetAskStateStampsTimes`: open→answered sets `AnsweredAt` and `Answer`; answered→delivered sets `DeliveredAt`, keeps `Answer`.
  - `TestSetReportUpserts`: two `SetReport` for one task → `Reports` returns the latest line and progress.
  - `TestMigrationIsIdempotent`: open the same db path twice.

- [ ] **Step 2: Run** `go test ./internal/store/` — FAIL.

- [ ] **Step 3: Implement** migration `0003_asks_and_reports` creating `asks(id PK, task_id, kind, title, body, options_json, recommended, items_json, blocks, state, answer, created_at, answered_at, delivered_at)` with indexes on `task_id` and `state`, and `task_reports(task_id PK, line, done, total, updated_at)`. Options/items stored as JSON text.

- [ ] **Step 4: Run** `make check` — PASS.

- [ ] **Step 5: Commit** `git commit -m "Store asks and status reports"`.

---

### Task 3: Core operations: report, file, withdraw, answer, deliver

**Files:**
- Create: `internal/core/asks.go`
- Modify: `internal/core/view.go` (`TaskView` gains report + open asks; `Hydrate` loads them; `reconcile` redelivers)
- Modify: `internal/fakes/fakes.go` (`PromptErrFor map[string]error` so one agent can reject)
- Test: `internal/core/asks_test.go`

**Interfaces:**
- Consumes: Task 2 store API; `herdrx.Client.PromptAgent`; `herdrx.IsCode(err, "agent_blocked")`.
- Produces:
  ```go
  // TaskView additions (json):
  Report   *store.Report `json:"report,omitempty"`
  OpenAsks []store.Ask   `json:"open_asks,omitempty"` // state open or answered

  func (a *App) Report(ctx, ref, line string, done, total int) error
  func (a *App) FileAsk(ctx, ref string, ask store.Ask) (store.Ask, error)   // validates, sets ID/TaskID/State/CreatedAt
  func (a *App) WithdrawAsk(ctx, askID string) error
  type Answer struct{ Option int /* -1 = none */; Note string; ChoreDone bool }
  type AnswerResult struct{ Ask store.Ask; Delivered bool; Reason string }
  func (a *App) AnswerAsk(ctx, askID string, ans Answer) (*AnswerResult, error)
  func AnswerPrompt(ask store.Ask, ans Answer) string
  func (a *App) deliverPending(ctx, live map[string]herdrx.Agent) // called from reconcile
  ```
- `AnswerPrompt` text (exact):
  - decision: `[dispatch] Your question "<title>" was answered: <label>.` + (` Note: <note>` if note) + ` Carry on.`
  - decision note only: `[dispatch] Reply to "<title>": <note> Carry on.`
  - chore: `[dispatch] "<title>" is done.` + optional note + ` Carry on.`

- [ ] **Step 1: Write failing tests** `internal/core/asks_test.go`:
  - `TestFileAskValidates`: decision with <2 options, or `Recommended` out of range → error; chore with no title → error; task in terminal status → error mentioning the status.
  - `TestAnswerDeliversAndMarksDelivered`: file a decision, `AnswerAsk(Option: 0)` → fake `Prompts` last entry targets the task's agent with `AnswerPrompt(...)`; ask state `delivered`; `Delivered == true`.
  - `TestAnswerToABlockedAgentIsHeldAndRedelivered`: fake rejects that agent with `&herdrx.Error{Code: "agent_blocked"}` → state `answered`, `Delivered == false`, `Reason` non-empty, no prompt recorded; clear the error and set agent `idle`; call `ActiveViews` → prompt recorded, state `delivered`.
  - `TestPendingAnswerWaitsWhileHerdrIsUnreachable`: answered ask, `ListErr` set → `ActiveViews` delivers nothing, state stays `answered`.
  - `TestAnswerPromptWording`: table test over the three wordings above.
  - `TestHydrateCarriesReportAndOpenAsks`: `Report(...)` + one open + one withdrawn ask → view has the report and exactly one `OpenAsks` entry.
  - `TestWithdrawnAskCannotBeAnswered`: error.

- [ ] **Step 2: Run** `go test ./internal/core/` — FAIL.

- [ ] **Step 3: Implement** in `internal/core/asks.go`. `AnswerAsk`: refuse unless state `open`; write `answered` with the answer text first, then attempt `PromptAgent(task.SessionRef(), AnswerPrompt(...))`; on success set `delivered`. `deliverPending`: for `answered` asks whose task's agent is in `live` and not `blocked`, attempt delivery once per hydrate. `Hydrate` loads reports and open/answered asks for all task IDs in two queries.

- [ ] **Step 4: Run** `make check` — PASS.

- [ ] **Step 5: Commit** `git commit -m "Let agents file asks and humans answer them"`.

---

### Task 4: `dispatch status`, `dispatch ask`, and ask info in `ls`

**Files:**
- Create: `internal/clix/asks.go`
- Modify: `internal/clix/root.go` (register commands)
- Modify: `internal/clix/output.go` (`taskJSON` adds `doing_now`, `progress`, `open_asks` count)
- Modify: `internal/clix/tasks.go` (`printTaskTable` adds an `ASKS` column when any task has one)
- Modify: `README.md` (new commands, machine-facing section)
- Test: `internal/clix/asks_test.go`, `internal/clix/output_test.go`

**Interfaces:**
- Consumes: Task 3 `App.Report`, `App.FileAsk`, `App.WithdrawAsk`.
- Produces the command shapes agents are told about in Task 5:
  ```
  dispatch status "<doing now>" [--progress 2/3] [--task <ref>] [--json]
  dispatch ask decision "<question>" --option "<label> | <detail>" --option ... --recommend <1-based n> [--body "<text>" | --body-file <path|->] [--task <ref>] [--json]
  dispatch ask chore "<title>" --item "<step>" ... [--blocks "<what it blocks>"] [--body ...] [--task <ref>] [--json]
  dispatch ask withdraw <ask-id> [--json]
  ```
  Task defaults to `$DISPATCH_TASK_ID`. `--json` emits `{"ask": {...}}` / `{"report": {...}}`. Parsing helpers: `func parseOption(s string) (store.AskOption, error)` (split on the first `" | "`; detail optional), `func parseProgress(s string) (done, total int, err error)`.

- [ ] **Step 1: Write failing tests**:
  - `TestParseOption` (`"Custom Product Page | faster, no new build"` → label/detail; `"Ship with 1.0.2"` → no detail; `""` → error).
  - `TestParseProgress` (`"2/3"` ok; `"3/2"`, `"x"`, `"0/0"` → error).
  - `TestAskWithoutATaskIsRefused`: unset `DISPATCH_TASK_ID`, no `--task` → error text names `DISPATCH_TASK_ID` and `--task`.
  - `TestTaskJSONIncludesAskCountAndReport` in `output_test.go`: view with report + 2 open asks → `doing_now`, `progress: "2/3"`, `open_asks: 2`; empty view omits all three (`omitempty`).

- [ ] **Step 2: Run** `go test ./internal/clix/` — FAIL.

- [ ] **Step 3: Implement** the commands as thin wrappers over core, following `tasks.go` style (openApp, `--json`, `warn` to stderr).

- [ ] **Step 4: Run** `make check` — PASS. Manually: `go run ./cmd/dispatch ask decision "Q" --option "A | x" --option B --recommend 1 --task <some running ref> --json` prints an ask with `recommended: 0`.

- [ ] **Step 5: Commit** `git commit -m "Add dispatch status and dispatch ask"`.

---

### Task 5: Proactive instructions in every prompt, and the orchestrator

**Files:**
- Modify: `internal/prompt/prompt.go` (`Build`)
- Modify: `internal/roles/builtin/orchestrator.yaml`
- Modify: `README.md` (Orchestration section; note that an existing `~/.config/dispatch/roles/orchestrator.yaml` is not overwritten — delete it, or re-run `dispatch role init`, to pick up the new text)
- Test: `internal/prompt/prompt_test.go`, `internal/roles/roles_test.go`

**Interfaces:**
- Consumes: the command shapes from Task 4.

- [ ] **Step 1: Write failing tests**:
  - `TestBuildIncludesWorkingProactively`: output contains a `WORKING PROACTIVELY` section, after `PROJECT INSTRUCTIONS` and before `WHEN YOU ARE UNSURE`, containing `dispatch ask decision`, `dispatch ask chore` and `dispatch status`.
  - `TestUnsureSectionPointsAtDispatchAsk`: `WHEN YOU ARE UNSURE` mentions `dispatch ask`.
  - `TestOrchestratorKnowsAsksGoToTheHuman`: builtin orchestrator instructions contain `dispatch ls --parent` and state children's asks go to the human.

- [ ] **Step 2: Run** `go test ./internal/prompt/ ./internal/roles/` — FAIL.

- [ ] **Step 3: Implement.** Section text (exact; the builder may wrap lines):

  ```
  Push the work as far as you can on your own. Make the calls you are qualified
  to make, and state them rather than asking.

  Stop only for what only the human can do or decide: accounts, billing,
  credentials, product direction, and anything irreversible or outward-facing.

  When you do need the human, file it instead of writing a long message:

    dispatch ask decision "<question>" --option "<label> | <consequence>" \
      --option "..." --recommend <n>
    dispatch ask chore "<what they must do>" --item "<step>" --blocks "<what waits on it>"

  Then say so in a line or two and carry on with any work the ask does not
  block. The answer will arrive in this conversation, starting "[dispatch]".

  Keep a one-line status current at meaningful milestones:

    dispatch status "<what you are doing now>" [--progress 2/3]
  ```

  `WHEN YOU ARE UNSURE` keeps its text and adds: `File the question with \`dispatch ask\`.`
  Orchestrator gains: children's asks go to the human's inbox, not to you; do not stall on them; `dispatch ls --parent "$DISPATCH_TASK_ID"` shows each child's open asks; keep coordinating unblocked work and mention in your own `dispatch status` what is waiting on the human.

- [ ] **Step 4: Run** `make check` — PASS.

- [ ] **Step 5: Commit** `git commit -m "Tell every agent to work proactively and file asks"`.

---

### Task 6: Overview model: Inbox items and Board columns

**Files:**
- Create: `internal/core/overview.go`
- Modify: `internal/herdrx/herdr.go` (`ReadAgent` on `Client` and `CLI`), `internal/fakes/fakes.go` (`Screens map[string]string`)
- Test: `internal/core/overview_test.go`, `internal/herdrx/herdr_test.go` (argv test in the existing style)

**Interfaces:**
- Consumes: `TaskView` with `Report`/`OpenAsks` (Task 3).
- Produces:
  ```go
  // herdrx
  ReadAgent(ctx context.Context, target string, lines int) (string, error) // herdr agent read <t> --source recent --lines <n> --format text

  // core
  type InboxItem struct {
      Task   TaskView
      Ask    *store.Ask // nil for a fallback card
      Screen string     // last lines of the agent's screen, fallback cards only
  }
  type Column string // ColumnNeedsYou, ColumnWorking, ColumnIdle, ColumnDoneToday
  type Overview struct {
      Inbox   []InboxItem          // asks newest first, then fallback cards
      Working []TaskView           // the quiet list under the Inbox: working + idle
      Board   map[Column][]TaskView
      NeedsYou, WorkingCount int    // header counts
  }
  type OverviewOptions struct{ ProjectRoot string; ScreenLines int /* default 6 */ }
  func (a *App) Overview(ctx context.Context, opts OverviewOptions) (*Overview, error)
  func GroupOverview(active, recent []TaskView, now time.Time) *Overview // pure; no Screen
  ```
- Grouping rules (from the spec):
  - Needs you: any task with an `OpenAsks` entry (one InboxItem per ask), or no open ask and `AgentStatus` `blocked` or `done` (one fallback InboxItem).
  - Working: `AgentStatus == working` and not in Needs you. Idle: alive, `idle`, not in Needs you.
  - Done today: `recent` tasks with terminal status and `CompletedAt` ≥ local midnight of `now`.
  - Orchestrator children are found by `ParentTaskID` among the active views (rendered by the TUI; no extra field needed).

- [ ] **Step 1: Write failing tests** `overview_test.go` (pure, on hand-built `TaskView`s):
  - `TestGroupPutsAsksInTheInbox`: one task with two open asks and `working` → 2 InboxItems, task in `Board[ColumnNeedsYou]` once, not in Working.
  - `TestGroupFallbackCardsForBlockedAndDone`: `blocked` and `done` with no asks → 2 fallback items (`Ask == nil`); `idle` → Idle column, no item.
  - `TestGroupDoneTodayUsesLocalMidnight`: completed 1h ago in, completed yesterday out.
  - `TestOverviewFillsScreensForFallbackCards`: via the App with fake `Screens` → `Screen` set on fallback items only; `ReadAgent` error leaves it empty and does not fail.
  - `TestOverviewFiltersByProject`.

- [ ] **Step 2: Run** `go test ./internal/core/ ./internal/herdrx/` — FAIL.

- [ ] **Step 3: Implement.** `Overview` = `ActiveViews` + recent (`Filter{IncludeTerminal: true, Limit: 50}`), optional project filter, `GroupOverview`, then `ReadAgent` for fallback items.

- [ ] **Step 4: Run** `make check` — PASS.

- [ ] **Step 5: Commit** `git commit -m "Group tasks and asks into an inbox and a board"`.

---

### Task 7: TUI tabs and the Inbox

**Files:**
- Create: `internal/tui/inbox.go` (Inbox rendering + key handling)
- Modify: `internal/tui/model.go` (tabs replace `screenHome`; load `Overview`; answer commands)
- Modify: `internal/tui/view.go` (header tab bar + counts; help text; remove `viewHome`)
- Modify: `README.md` (control centre section and key table)
- Test: `internal/tui/inbox_test.go`

**Interfaces:**
- Consumes: `App.Overview`, `App.AnswerAsk`, `AnswerResult`, existing `openCmd`/`stopCmd`/`dispatchCmd`.
- Produces: `type tab int` with `tabInbox, tabBoard, tabRecent, tabRoles`; `func (m *model) viewInbox() string`; `func renderTabBar(active tab, needsYou, working int, scope string) string`. Task 8 adds `viewBoard`.

Behaviour:
- Header: `Dispatch  <all projects|project name>  [1 Inbox]  2 Board  3 Recent  4 Roles  ● N need you  ● M working  ● live|○ polling`.
- Keys: `1`–`4` switch tab, `tab` cycles; `j`/`k` move between Inbox items; on a decision `a`,`b`,`c`… pick option 0,1,2…; `m` opens a one-line text input and sends it as `Answer.Note` (with no option); on a chore `d` sends `ChoreDone`; `enter` toggles the ask body / full screen text; `o` open; `x` stop (existing confirm); `p` toggles all-projects/current-project; `n`, `r`, `?`, `q` unchanged. Recent and Roles reuse today's list and roles screens.
- After an answer: banner `answered · delivered` or `answered · waiting to deliver (<reason>)`; item stays shown with that label until delivered.
- Recommended option shown with `★` and accent colour; options lettered `a`, `b`, …

- [ ] **Step 1: Write failing tests** `inbox_test.go` (construct `model` with a fixed `*core.Overview`, no App calls, `lipgloss` colour disabled):
  - `TestInboxRendersADecisionWithItsRecommendation`: output contains the title, `a`, `★ Custom Product Page`, `b`, `Ship with 1.0.2`, the agent role and project.
  - `TestInboxRendersAChoreChecklist`: `☐ Create account`, `blocks: campaign setup`.
  - `TestInboxFallbackCardShowsScreenTail`.
  - `TestOptionKeyMapsToIndex`: `optionIndex("b") == 1`, `optionIndex("z")` with 2 options → `-1`.
  - `TestTabKeysSwitchTabs`: `1`–`4` and `tab`.
  - `TestPendingDeliveryIsLabelled`: an `answered` ask renders `waiting to deliver`.

- [ ] **Step 2: Run** `go test ./internal/tui/` — FAIL.

- [ ] **Step 3: Implement.** Keep commands asynchronous like existing `stopCmd` (return a msg, reload overview after). Inbox is the initial tab.

- [ ] **Step 4: Run** `make check` — PASS; `make run` against a real herdr: Inbox opens, `2`/`3`/`4` switch, `n` still creates a task.

- [ ] **Step 5: Commit** `git commit -m "Open the control centre on an inbox of asks"`.

---

### Task 8: TUI Board

**Files:**
- Create: `internal/tui/board.go`
- Modify: `internal/tui/model.go` (board cursor: column + card), `internal/tui/view.go` (help)
- Modify: `README.md` (Board keys)
- Test: `internal/tui/board_test.go`

**Interfaces:**
- Consumes: `Overview.Board`, `Column` constants (Task 6), `tab` (Task 7).
- Produces: `func (m *model) viewBoard() string`; `func renderCard(v core.TaskView, children []core.TaskView, width int, now time.Time) string`; `func progressBar(done, total, width int) string`.

Behaviour: four columns `NEEDS YOU · n`, `WORKING · n`, `IDLE · n`, `DONE TODAY · n`, side by side when `m.width >= 100`, otherwise stacked vertically. Card: role (bold), `project · age`, doing-now line (or the task title when no report), progress bar + `done of total` when `Total > 0`, and for orchestrators `└ n helpers: role, role`. Titles/lines truncate to card width. `h`/`l` column, `j`/`k` card, `enter` details, `o` open, `x` stop.

- [ ] **Step 1: Write failing tests** `board_test.go`:
  - `TestProgressBar`: `progressBar(2, 3, 9)` → 6 filled + 3 empty runes; `Total == 0` → `""`.
  - `TestCardTruncatesLongTitles`: every rendered line ≤ card width.
  - `TestBoardStacksOnNarrowTerminals`: width 80 → column headers on separate lines in order; width 120 → all four headers on the first content line.
  - `TestOrchestratorCardListsHelpers`.
  - `TestBoardNavigationClampsToNonEmptyColumns`: `l` past the last column stays put; moving into an empty column skips it.

- [ ] **Step 2: Run** `go test ./internal/tui/` — FAIL.

- [ ] **Step 3: Implement** with lipgloss `JoinHorizontal`/`JoinVertical` and rounded borders, matching existing `styles.go`.

- [ ] **Step 4: Run** `make check` — PASS; `make run`: `2` shows the board, cards move between columns as agents change state (live events).

- [ ] **Step 5: Commit** `git commit -m "Add the team board"`.
