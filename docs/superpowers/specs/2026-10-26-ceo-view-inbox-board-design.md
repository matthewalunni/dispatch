# CEO view: Inbox, Board, and proactive agents

## Intent

The user runs many dispatched agents at once, across several apps, and thinks
of themselves as a manager or CEO. They want two things:

1. **To see the state of things at a glance**: who is working on what, how far
   along they are, and above all what is waiting on them.
2. **Agents that are more proactive**: they push work as far as they can on
   their own, and when they genuinely need the human they hand over a short,
   structured ask instead of a wall of text.

The trigger was a message like this one, from an agent that knew more about
App Store ads than the user did:

> Choose how the ads get them … Custom Product Page (recommended, faster) …
> Next version: send them with 1.0.2 … Set up the Search Ads account and
> billing. That's still yours to do. Once the custom page is approved, I can
> walk you through the campaigns …

That one message carries a **decision** (two options, one recommended), a
**chore** only the human can do (account + billing), and **what the agent will
do next** once unblocked. Today it sits in one herdr pane among many, and
dispatch only knows the agent is "waiting". The user does not want to be handed
walls of text.

## Goals

- An **Inbox** of asks across all agents: each ask is a decision (options, one
  recommended) or a chore (checklist, what it blocks), with agent, project and age.
- A **Board** of the team: columns *Needs you / Working / Done today*, each card
  showing role, project, a one-line "doing now", and progress when known.
- Answering from the Inbox with one key, after which the agent carries on.
  Dropping into the conversation must remain one key away; the user said they
  "sometimes" want each.
- Agents report status and asks to dispatch themselves, and every role is
  told to work proactively and to stop only for what only the human can do.
- Fix stale tasks: tasks whose herdr agent is gone stop showing as active.

## Non-goals

- A browser UI. This lives in the terminal, inside the existing `dispatch`
  control centre. A `dispatch web` view could reuse the same data later; it is
  not part of this work.
- Scraping agent prose to infer decisions or progress. Structure comes from the
  agent reporting it.
- Classifying or routing work automatically (unchanged stance from the README).
- A timeline view (direction C in brainstorming; not chosen).

## Anti-goals (would make this a failure even if it works)

- The Inbox fills with ghosts (dead sessions) or with noise the agent could
  have resolved itself.
- An ask the agent files is lost or silently ignored: an agent that only
  reports via herdr "waiting" must still appear.
- Answering from the Inbox leaves the agent not knowing it was answered.
- Existing keys and flows (`n`, `o`, `x`, `dispatch ls`, `--json`) break.

## Current state (relevant facts)

- `internal/tui`: Bubble Tea app with a home menu (`New task / Active / Needs
  you / Recent / Roles`) and per-screen lists; header shows project name and a
  live/polling indicator.
- `internal/core/view.go`: `TaskView` joins a task with herdr's live agent list;
  `resolveStatus` produces `EffectiveStatus`. A `running` task whose herdr
  agent is gone resolves to `detached`, but its stored status never changes.
- `internal/store`: SQLite, ordered idempotent migrations appended to a slice.
- `internal/herdrx`: CLI wrapper; `PromptAgent(target, text)` already exists
  (backs onto `herdr agent prompt`). herdr also offers `agent read` for recent
  pane output.
- Agents already receive `DISPATCH_TASK_ID` / `DISPATCH_TASK_REF` in their pane.
- `internal/prompt.Build` renders every agent's initial prompt with fixed
  sections (`TASK`, `HOW YOU WORK`, `WHEN YOU ARE UNSURE`, …).
- The user has many custom roles in `~/.config/dispatch/roles/` (debugger,
  video-editor, content-strategist, …). Seeding never overwrites user files.

## Design

### 1. Stale task fix (ship first, separately)

When herdr is **reachable** and a `running` task's agent is not in herdr's
agent list, dispatch records the task as ended: a new terminal store status
(e.g. `ended`, "session closed outside dispatch") with `completed_at` set. It
then appears under Recent, not Active. When herdr is unreachable, nothing
changes (current behaviour: `unknown`). This should happen wherever views are
hydrated, so `dispatch ls`, the TUI and `wait` all agree.

Today the user has 22 "active" tasks, all `detached`. After the fix, the first
run cleans them up.

Guards (my calls):
- Only end a task whose launch is more than a short grace period old (e.g. 60s),
  so a freshly launched agent not yet listed is not ended.
- `ended` is terminal for `ActiveFilter`, but not irreversible: if an `ended`
  task's agent reappears in herdr's list (session restored), it returns to
  `running`.
- `dispatch wait` treats `ended` exactly as it treats `detached` today, so
  orchestrators waiting on children keep the same behaviour.

### 2. Agent-facing commands

Run by agents from inside their session; the task is taken from
`DISPATCH_TASK_ID` (or an explicit `--task`). Both take `--json`.

- `dispatch status "<doing now>" [--progress 2/3]`: sets the task's current
  one-liner and optional progress. Latest wins.
- `dispatch ask` files an ask on the task. It comes in two kinds:
  - **decision**: a question, two or more options each with a short label and
    one-line consequence, exactly one marked recommended, and an optional
    longer body (the full reasoning the human can expand).
  - **chore**: a title, checklist items, an optional "blocks: …" line, and
    optional body.
  The command returns an ask id. A task can have several open asks.

Asks persist in the store (new table via a migration) with: id, task id, kind,
question/title, options or checklist (JSON), recommended option, body, state
(`open`/`answered`/`withdrawn`), answer, created/answered times. The current
status line and progress live on the task (or a small companion table).

An agent can withdraw an ask it no longer needs (e.g. `dispatch ask --withdraw <id>`)
so the Inbox does not show stale asks.

### 3. Answer delivery

Answering in the TUI marks the ask answered and sends the agent a short prompt
through `herdr agent prompt`, e.g.:

> [dispatch] Your decision "How should Search Ads get the new screenshots?" was answered: **Custom Product Page**. Carry on.

- decision: `a`/`b`/… picks an option.
- `m` (message): a typed free-text reply (sent alongside, or instead of, an option).
- chore: `d` marks it done ("Search Ads account + billing is done. Carry on.").
- `o`: open the conversation (existing attach behaviour), usable before or after answering.

`herdr agent prompt` accepts input while the agent is working (it is queued
by the agent CLI) and rejects it only when the agent is `blocked`
(`agent_blocked`). So (my call):
- Answers go through an `answered → delivered` step. Delivery is attempted
  immediately; if rejected as blocked, or herdr is unreachable, the answer
  stays `answered` (pending delivery) and is retried whenever the TUI sees the
  agent leave `blocked`, and on any later `dispatch` invocation that hydrates
  views. No daemon.
- The Inbox shows pending-delivery answers as "answered · waiting to deliver".
- An ask is never shown as resolved to the human until delivery succeeded.

### 4. Proactive instructions (all roles)

Add a fixed section to `prompt.Build`, so it applies to every role including
the user's custom ones without editing their YAML. In substance:

- Push the work as far as you can on your own. Make the calls you are
  qualified to make; state them rather than asking.
- Stop only for what only the human can do or decide (accounts, billing,
  credentials, product direction, irreversible or outward-facing actions).
- When you stop, file it with `dispatch ask` (decision with a recommendation,
  or chore with a checklist and what it blocks) and keep the chat message to a
  line or two. Continue any work the ask does not block.
- Keep `dispatch status` current at meaningful milestones.

This must coexist with the existing `WHEN YOU ARE UNSURE` section (ambiguity
of intent still means asking), now pointed at `dispatch ask`.

### 4b. Asks from children of an orchestrator

Asks always go to the human's Inbox, including asks filed by children of an
orchestrator (decided by the user). The orchestrator does not answer them.
Because a child now files an ask and keeps working, the orchestrator's
`wait --until waiting,done` may not fire, so:
- `dispatch ls` (and `--json`) shows each task's open-ask count, so
  `dispatch ls --parent "$DISPATCH_TASK_ID"` tells the orchestrator which
  children are waiting on the human.
- `orchestrator.yaml` instructions are updated: children's asks go to the
  human; do not stall on them; keep coordinating unblocked work; mention in
  your own status what is waiting on the human. The built-in file changes; a
  user's existing copy is not overwritten (seeding rule), so the README notes it.

### 5. TUI: tabs instead of the home menu

The home menu becomes a tab bar; the Inbox is the landing screen:

```
 Dispatch  all projects     [1 Inbox]  2 Board  3 Recent  4 Roles     ● 2 need you  ● 3 working
```

- **Inbox** (replaces "Needs you"): open asks at the top, newest first. The
  selected decision shows its options with the recommendation starred and keys
  `a`/`b`/…; chores show their checklist and "blocks". `enter` expands the body.
  Below the asks, one quiet line per working agent (role, project, doing-now, age).
- **Fallback cards**: an agent with no open ask that herdr reports as
  `blocked` (approval/question prompt on screen) **or** `done` (finished a turn
  the human has not looked at) appears as a plain "needs you" card with the last few lines of its
  screen (`herdr agent read`). `o` opens it.
- **Board** (replaces "Active"): four columns *Needs you / Working / Idle /
  Done today*. Needs you = open asks plus fallback-card agents; Working = herdr
  `working`; Idle = live sessions sitting `idle` (turn finished and seen);
  Done today = tasks reaching a terminal status since local midnight. Each task is a boxed card with role, project, age, doing-now, and a
  progress bar when progress is known. Orchestrator cards list their children.
  Navigation `h`/`l` between columns, `j`/`k` between cards.
- **Recent** and **Roles** unchanged.
- `n`, `o`, `x`, `r` (refresh, unchanged), `?`, `q` keep working on every tab. `1`–`4`
  switch tabs, `tab` cycles.

Mockups the user reacted to ("this looks great"):
`.superpowers/brainstorm/*/content/terminal.html`.

## Decided by the user

- Both Inbox (A) and Board (B); no timeline.
- Terminal, inside the existing control centre.
- Structured asks with a recommendation; no walls of text.
- Answering both ways: one key to answer and carry on, one key to open the conversation.
- The design above as played back ("looks good").
- Children's asks under an orchestrator go to the human, not the orchestrator.

## My calls (user can override)

- Stale-task fix ships first as its own change.
- Inbox/Board default to **all projects**; `p` toggles to the current project only.
- Proactive rules live in `prompt.Build`, not role YAML, so custom roles get them.
- Fallback cards for agents that don't use `dispatch ask`.
- Tab layout replaces the home menu rather than adding to it.
- Answer delivery reuses `herdr agent prompt`; the exact wording is the builder's.
- Board's "Done today" means tasks reaching a terminal state since local midnight.
- Agents that finished a turn you haven't looked at (herdr `done`) count as
  needing you; an Idle column holds seen-but-idle sessions.
- Answers to a `blocked` agent are held and redelivered; no daemon.
- Ghost fix: 60s grace period, `ended` revives if the agent reappears, `wait`
  treats `ended` like `detached`.
- Reply key is `m`, since `r` stays refresh.

## Left to the builder

- Exact command flag shapes for `dispatch ask` (how options and the
  recommendation are passed), with sensible `--help` and `--json`.
- Schema details and naming of the new terminal status.
- Layout behaviour on narrow terminals (Board can stack columns under ~100 cols).
- Whether `dispatch ls` shows doing-now and open-ask count (recommended).
- README updates for new commands, tabs and keys.
- Tests in the style of the existing `core_test.go` / `fakes`.
