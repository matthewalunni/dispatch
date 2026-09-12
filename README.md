# dispatch

A local-first control plane for launching and managing specialized AI agents.

The primitive dispatch exists to make excellent is one step:

```
task + role
     ↓
the right environment          (git worktree, or the project in place)
     ↓
a herdr session                (persistent, interactive, survives your terminal)
     ↓
the right specialized agent    (engineer, designer, reviewer, anything you define)
     ↓
repository-aware instructions  (discover the project, don't assume greenfield)
     ↓
a conversation you can come back to
```

It works from any repository or directory. The launched agent infers what it
needs from the project it is standing in — dispatch tells it where to look
rather than pasting the repository into a prompt.

```sh
cd ~/src/some-project
dispatch                                   # the control centre

dispatch run --role engineer  --task "Implement exercise substitution"
dispatch run --role designer  --task "Prototype alternatives to onboarding"
dispatch run --role reviewer  --task "Review the current branch against main"
dispatch ls
dispatch open workout-editor               # back into that exact conversation
```

## Install

dispatch needs [herdr](https://herdr.dev) (it owns the terminal sessions) and at
least one agent CLI (Claude by default).

```sh
# herdr, if you do not have it
curl -fsSL https://herdr.dev/install.sh | sh

# dispatch
go install github.com/matthewalunni/dispatch/cmd/dispatch@latest

# or from a clone
git clone https://github.com/matthewalunni/dispatch && cd dispatch
make install                # builds and installs to $(go env GOPATH)/bin
```

Then start a herdr session (dispatch launches agents into it) and check the
environment:

```sh
herdr &          # or just run `herdr` in another terminal
dispatch doctor
```

The first run creates everything dispatch needs and never overwrites anything
you have edited, so it is always safe to run again.

## What goes where

Nothing is written into your source repositories or your dotfiles.

```
~/.config/dispatch/
├── config.yaml            # defaults, runtimes, herdr settings
├── roles/                 # one YAML file per agent role
│   ├── README.md          # the role file format, in full
│   ├── engineer.yaml
│   ├── designer.yaml
│   ├── reviewer.yaml
│   └── general.yaml
└── workflows/             # reserved

~/.local/share/dispatch/
├── dispatch.db            # the canonical task registry (SQLite)
├── sessions/<task-id>/
│   ├── metadata.json      # portable per-task metadata
│   ├── initial-prompt.md  # exactly what the agent was sent
│   └── result.json        # how the task ended
├── worktrees/<project>/   # isolated checkouts, outside your repo
└── logs/
```

Both locations follow XDG conventions (`XDG_CONFIG_HOME`, `XDG_DATA_HOME`), and
`DISPATCH_CONFIG_DIR` / `DISPATCH_DATA_DIR` override them outright.

## Roles

A role says which agent to launch, what environment it needs, and how it should
behave. Roles are declarative — adding one never means touching Go.

```sh
dispatch roles                       # what is available here
dispatch roles show engineer         # resolved, including project overrides
dispatch roles schema                # the file format
dispatch role init security-reviewer # a starter file to edit
```

```yaml
name: engineer
description: Implementation specialist
runtime: claude
isolation: worktree           # none | worktree
context:
  repository: true            # you are inside an existing project
  discover_project_docs: true # go and find CLAUDE.md / AGENTS.md / docs
  git_history: true
  extra_discovery:
    - tests that already cover nearby behaviour
instructions: |
  Own the assigned implementation.
  Understand the existing architecture before modifying it.
  Keep changes focused and reviewable.
  Test your work before declaring completion.
```

The four roles shipped by default:

| role       | runtime | isolation  | what it is for                                   |
|------------|---------|------------|--------------------------------------------------|
| `engineer` | claude  | `worktree` | owns an implementation, on its own branch        |
| `designer` | claude  | `none`     | inspects the product, proposes concrete designs  |
| `reviewer` | claude  | `none`     | reviews code without changing it                 |
| `general`  | claude  | `none`     | research, planning, consultation, specialists    |

Roles compose. `extends` inherits another role and states only what differs:

```yaml
name: security-reviewer
description: Security-focused reviewer
extends: reviewer
context:
  extra_discovery:
    - anywhere untrusted input crosses a trust boundary
instructions_append: |
  Rank findings by exploitability, not by how interesting they are.
```

The child wins field by field, `instructions_append` adds to what it inherited,
and `extra_discovery` merges. Chains work; cycles are rejected with the chain
named. Composition runs after project overrides, so a role can extend one the
repository amended.

Nothing assumes an agent is a software engineer. `ux-researcher`,
`behavioral-science-consultant`, `growth-strategist` and `accessibility-reviewer`
are all just YAML files. See `~/.config/dispatch/roles/README.md` for the full
format.

## What a task looks like in herdr

Each dispatched agent gets **its own herdr workspace**, not a pane in a shared
one. For a worktree-isolated task the workspace *is* the worktree, so herdr
shows the repository and branch beside the agent:

```
w9   orchestrator: ship-the-onboarding-revamp     —
wA   designer: design-the-new-first-run-screen    —
wB   engineer: implement-the-new-first-run-screen worktree of repvault
wC   reviewer: review-the-first-run-changes       —
```

dispatch still owns the worktree: git creates it, and herdr is only asked to
open it. Stopping a task closes the workspace dispatch made and leaves the
worktree on disk unless you pass `--remove-worktree`.

Two things worth knowing:

- The first worktree dispatch opens in a repository makes herdr create a
  **primary workspace** for that repository's main checkout — herdr groups
  worktree workspaces under one. It is one per repository, not per task, and
  dispatch never closes it, since it may be yours.
- If a repository's ownership makes git refuse (`dubious ownership`), herdr
  needs explicit trust. dispatch will not grant that for you; verify the
  repository, then set `herdr.trust_repository: true`.

Prefer everything packed into one workspace? Set `herdr.layout: tab` and tasks
become tabs of the focused workspace, as before.

## Per-repository configuration (optional)

A repository can commit a `.dispatch/` directory. Every project works without
one.

```
.dispatch/
├── config.yaml     # augments the global config, field by field
├── roles/          # adds roles, or amends global ones
└── workflows/
```

```yaml
# .dispatch/config.yaml
branch_prefix: agent/
project_instructions: |
  This repository ships a design system in packages/ui.
  Prefer its primitives over new CSS.
```

```yaml
# .dispatch/roles/engineer.yaml — amend, don't restate
name: engineer
instructions_append: |
  Run `npm test -- --watch=false` before you claim the work is done.
context:
  extra_discovery:
    - the packages/ui design system primitives
```

Resolution runs global defaults → global config → project overrides → task flags.

## Commands

| command | what it does |
|---|---|
| `dispatch` | the Bubble Tea control centre |
| `dispatch run` | dispatch a task non-interactively |
| `dispatch ls` (`list`) | tasks, with live agent state from herdr |
| `dispatch open <task>` | back into that task's conversation |
| `dispatch wait <task>` | block until an agent needs you or finishes |
| `dispatch show <task>` | everything about a task (`--prompt` for what was sent) |
| `dispatch stop <task>` | end the agent session |
| `dispatch done <task>` | mark complete, leaving the session running |
| `dispatch roles` / `roles show` / `roles schema` | role discovery |
| `dispatch role init <name>` | generate a starter role file |
| `dispatch doctor` | diagnose the environment |

Tasks are addressed by their readable reference, a unique prefix of it, the
herdr agent name, or the task id — never a UUID you have to memorise. An
ambiguous reference is reported, never guessed at.

### Machine-facing use

Every command that produces a result takes `--json`, and JSON mode keeps stdout
clean: warnings and diagnostics go to stderr, exit codes are meaningful. Another
agent can treat dispatch as an execution primitive.

```sh
dispatch run --role engineer --task "Implement feature X" --json
```

```json
{
  "id": "task_20260912T101500_abcd1234",
  "title": "Implement feature X",
  "role": "engineer",
  "status": "working",
  "project_root": "/path/to/project",
  "branch": "dispatch/implement-feature-x",
  "worktree": "/path/to/worktree",
  "herdr_agent": "implement-feature-x"
}
```

`status` is the resolved state a human would read; `dispatch_status` and
`agent_status` expose dispatch's and herdr's views separately when you need them.

### Parent and child tasks

A task can record what it was dispatched underneath, so a planning agent can
fan out work and keep the lineage:

```sh
parent=$(dispatch run --role designer --task "Plan the onboarding flow" --json | jq -r .id)
dispatch run --parent "$parent" --role engineer --task "Build the onboarding screens"
dispatch ls --parent "$parent"
```

## Orchestration

dispatch is an execution primitive, so an agent can drive it the same way you
do. Three things make that work:

**Every agent knows its own task.** dispatch exports the task's identity into
the agent's pane, so a delegating agent can dispatch child work that keeps its
lineage:

| variable | |
|---|---|
| `DISPATCH_TASK_ID` | the task's id |
| `DISPATCH_TASK_REF` | the readable reference |
| `DISPATCH_PROJECT_ROOT` | the project it was dispatched from |
| `DISPATCH_PARENT_TASK_ID` | set when the task has a parent |

**Waiting is event-driven, not a poll loop.** `dispatch wait` blocks on herdr's
event stream and returns the moment an agent needs a human or finishes. Exit
status is 0 on a match and 2 on timeout, so a script never has to parse output
to tell them apart.

```sh
dispatch wait implement-feature-x --until waiting --timeout 20m --json
```

**An `orchestrator` role ships by default.** It coordinates rather than
implementing, and delegates with its own task as the parent:

```sh
dispatch run --role orchestrator --task "Ship the onboarding revamp"
```

```sh
# what that agent then runs, from inside its own session
dispatch roles                                            # what specialists exist here
dispatch run --parent "$DISPATCH_TASK_ID" --role designer --task "..." --json
dispatch run --parent "$DISPATCH_TASK_ID" --role engineer --task "..." --json
dispatch wait <child> --until waiting,done --timeout 20m
dispatch ls --parent "$DISPATCH_TASK_ID"
```

If you want a bare `dispatch run --task "..."` (no `--role`) to go to the
orchestrator and be broken down, set it as your default:

```yaml
# ~/.config/dispatch/config.yaml
default_role: orchestrator
```

That is off by default deliberately: delegation spends real tokens and spawns
real sessions, so dispatch does not classify work for you unless you ask it to.
Which role fits a task stays a decision an agent makes explicitly, in its own
instructions, rather than something dispatch infers.

## The control centre

```
dispatch
```

```
  Dispatch  repvault

  › New task
    Active      4
    Needs you   2
    Recent     11
    Roles       6
```

| key | |
|---|---|
| `n` | new task |
| `enter` | select / open details |
| `o` | open the agent's session in this terminal |
| `x` | stop the selected task |
| `tab` | cycle Active / Needs you / Recent |
| `j` `k` `↑` `↓` | move |
| `r` | refresh |
| `q` `esc` | back, or quit from home |
| `?` | help |

Creating a task is: launch dispatch, press `n`, type the task, pick a role,
press enter.

The TUI does not reproduce the terminal — conversations live in herdr. Opening
a task hands your terminal to that agent and returns you here when you detach.

## Status

Dispatch stores what a task *is*. Herdr knows whether its process is alive and
what it is doing. `dispatch ls` shows the combination:

| status | meaning |
|---|---|
| `working` | the agent is producing work |
| `waiting` | herdr detected an approval or question — it needs you |
| `idle` | ready for input |
| `done` | finished a turn you have not looked at |
| `detached` | dispatch has the task, but herdr no longer has its session |
| `stopped` / `completed` / `failed` | closed out |

`waiting` comes from herdr's own agent-state detection, not from scraping
terminal output. When herdr is unreachable, dispatch degrades to its own view
rather than claiming sessions have died.

Status arrives live. dispatch subscribes to herdr's event stream over its
socket API, so the control centre reflects a state change as it happens rather
than on a timer; the header says `● live` or `○ polling` so a quiet screen is
never ambiguous. A slow poll runs underneath as a safety net, and dispatch
falls back to it entirely where live events are unavailable.

## Architecture

```
cmd/dispatch          the binary
internal/core         the application layer — Dispatch(), Open(), Stop(), Watch(), Doctor()
internal/clix         the CLI (cobra)
internal/tui          the control centre (Bubble Tea)
internal/herdrx       the herdr adapter — CLI calls, plus the event subscriber
internal/gitx         the git adapter — every git call lives here
internal/runtime      agent runtimes, resolved from config
internal/roles        role definitions, merging and seeding
internal/project      project detection and context discovery
internal/prompt       initial prompt composition
internal/store        SQLite task registry
internal/config       layered configuration
internal/paths        XDG locations
internal/naming       slugs, branch names, agent names, task ids
internal/fakes        in-memory git and herdr, for tests
```

The boundaries are deliberate:

- **dispatch** owns tasks, roles, workspace preparation, prompt construction and
  orchestration metadata.
- **herdr** owns workspaces, terminal sessions, panes and the live agent
  processes.
- **git** owns branches, worktrees and repository state.
- **the repository** owns product knowledge, conventions and its own agent
  instructions.

The CLI and TUI both call `internal/core` and nothing else, so external
orchestrators are never coupled to Bubble Tea. Dispatch never predicts a herdr
identifier: every pane, tab and workspace id it stores came back from herdr.

## Development

```sh
make build        # ./bin/dispatch
make test         # the full suite
make check        # vet + test
make install      # into $(go env GOPATH)/bin
```

Tests cover config layering, role resolution, composition and project
overrides, project detection, slug and branch and agent naming, database
persistence and task lookup, prompt construction, worktree path selection, the
JSON contract, the event subscriber and watcher, and the herdr and git
adapters. The concurrent paths are covered under `go test -race`. Herdr and git are interfaces with in-memory fakes, so
the whole application layer runs without a terminal, a repository or a server.

## Requirements

- Go 1.24+ (to build; the binary is self-contained and needs no cgo)
- [herdr](https://herdr.dev) 0.9+
- git
- an agent CLI — `claude` by default

## License

MIT
