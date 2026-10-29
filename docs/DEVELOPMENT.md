# Developing Den

A short guide for people changing the code. For the user-facing description
see the [README](../README.md); for how to submit changes see
[CONTRIBUTING.md](../CONTRIBUTING.md).

## Setup

```
git clone --recurse-submodules https://github.com/GlacierHubAB/den
cd den
make build && make test
```

Go 1.26+. The root package (`den.go`) embeds `mods/agent-badge` (a git
submodule) with `go:embed`, so the submodule must be checked out to build.

## Package map

`cmd/den` is the CLI: `main.go` dispatches the subcommands, `hook.go` is what
Claude Code's hooks run, `mcp.go` serves the orchestrator's tools, `ask.go`,
`send.go` and `wait.go` are the scripting commands, `mods.go` installs the badge.

| Package | Role |
| --- | --- |
| `internal/discover` | finds live sessions from `~/.claude/sessions/<pid>.json` and checks the pid is alive |
| `internal/fleet` | joins a session with its badge labels, git checkout, transcript facts and terminal window into an `Agent`; `Focus`, `Explain` |
| `internal/badge` | agent-badge identity: the name generator (ported from the mod) and the roles/tasks the mod saved |
| `internal/critter` | the pixel-art creature, drawn with half-block and octant characters |
| `internal/colors` | one colour per live session, recorded in `colors.json` for the mod to read |
| `internal/theme` | colours from the current Omarchy theme, with a built-in fallback |
| `internal/gitinfo` | repo and branch for a directory without shelling out |
| `internal/procs` | process tree from `/proc` |
| `internal/hypr` | `hyprctl` wrapper: windows, focus, workspaces |
| `internal/transcript` | the few facts Den reads from a session's JSONL transcript, the file changes Review needs, and the incremental token ledger (`ledger.go`) |
| `internal/hooks` | installs and removes Den's entries in Claude Code's `settings.json` |
| `internal/bridge` | Unix-socket protocol between `den hook` and the running TUI; carries permission decisions back |
| `internal/inbox` | turns hook events into inbox items (permission, plan, question, done, stalled, failed) and keeps history |
| `internal/events` | the versioned event stream (`events.jsonl`) for `den events -json`, `den wait` and `[on]` commands |
| `internal/stats` | `den stats`: reads the state files, bounded and read-only, and counts what they prove about a window |
| `internal/notify` | desktop notifications through `notify-send`, and whether one was clicked |
| `internal/peer` | delivers a message into a session through its inbox socket |
| `internal/orch` | runs the orchestrator, a headless `claude -p` stream-json session, and its roster |
| `internal/mcp` | minimal stdio MCP server |
| `internal/spawn` | opens a terminal and starts an agent on a brief; the work behind `spawn_agent` and `open_terminal` |
| `internal/tree` | who directs whom, persisted as `edges.jsonl` |
| `internal/mission` | missions, their shapes, cadences, run log and budgets |
| `internal/cost` | the price table, estimates, tree roll-ups and budget parsing |
| `internal/templates` | mission templates: the built-ins (`builtin.json`, embedded) and the user's `templates.json` |
| `internal/roster` | the snapshot of live agents behind the restore question and `/reopen`, persisted as `roster.json` |
| `internal/worktree` | git worktrees: the main repository of a worktree, and the planned, re-checked merge and removal behind Review's `/merge` and `/cleanup` |
| `internal/review` | diff against a base, per-line attribution to sessions, review comments |
| `internal/mods` | list, install and uninstall the embedded mods |
| `internal/config` | `config.toml` and the environment overrides |
| `internal/ui` | the bubbletea application: model and keys (`app.go`), rendering (`view.go`), and one file or group per view (Fleet and tree, `orchestrator.go`, `dispatch.go`, `overview.go`, `missions.go`, `review*.go`) |

## How Den finds sessions

Claude Code 2.1.289+ writes `~/.claude/sessions/<pid>.json` per live session
(session id, cwd, busy/idle, the session's messaging socket). `discover.Scan`
reads them all and marks each alive or gone by probing the pid.
`fleet.Snapshot` then adds the rest: the critter name from the session id, role
and task from the badge's records in Claude Code's plugin store, the git
checkout, the transcript title and model, and the Hyprland window (matched by
title or working directory, or through herdr). `CLAUDE_CONFIG_DIR` moves the
Claude Code directory for discovery, transcripts and the settings file.

`den list` prints the snapshot; `den explain <name>` prints how each field was
derived.

## Hooks and state

`den install-hooks` adds entries to `~/.claude/settings.json` (backed up first)
that run `den hook` for `SessionStart`, `SessionEnd`, `UserPromptSubmit`,
`Notification`, `Stop`, `PostToolUse`, `PreToolUse` (`AskUserQuestion`),
`PermissionRequest`, `StopFailure` and `PostToolUseFailure`
(`internal/hooks/install.go`). Claude Code 2.1.289 sends `StopFailure`'s error
type as `error` (for example `"authentication_failed"`), not the documented
`error_type`; Den reads both.

`den hook` reads the event from stdin and sends it as one JSON line to the
socket (`$DEN_SOCKET`, else `$XDG_RUNTIME_DIR/den.sock`, else `den.sock` in
the private directory `$TMPDIR/den-<uid>/`; `internal/bridge/dial.go` checks
that directory and, with `SO_PEERCRED`, that the other end of every
connection is the same user). A panic in `den hook` exits 0 with no output,
never Go's exit 2, which Claude Code would read as a denial. For
`PermissionRequest` it waits, up to `hooks.permission_wait` seconds, for the
TUI's decision and prints it for Claude Code; with no answer it prints nothing
and Claude Code shows its own dialog. Events from subagents are ignored.
`internal/ui/attention.go` decides which inbox items become desktop
notifications, and adds reminders and stalled items on the periodic tick.
`den events` listens without the TUI and prints what arrives
(`DEN_EVENTS_DECISION=allow|deny` answers permissions). Before delivering, `den
hook` also appends a metadata-only line to `events.jsonl`
(`internal/events.FromHook` picks the fields; see [EVENTS.md](EVENTS.md)).

State files are listed in the README. Every one is under
`$XDG_STATE_HOME/den/` (default `~/.local/state/den/`) and appended or written
atomically, so Den restarts cleanly.

## Running against scratch state

Give Den an empty state and config directory so your own files are untouched:

```
export XDG_STATE_HOME=$(mktemp -d)
export XDG_CONFIG_HOME=$(mktemp -d)
export DEN_SOCKET=$XDG_STATE_HOME/den.sock
go run ./cmd/den
```

A second Den started with a different `DEN_SOCKET` does not take the first
one's hook events. Hooks installed by `den install-hooks` point at the binary
that installed them and use the socket from the environment of the Claude Code
session, so for end-to-end work start the test sessions from a shell with the
same `DEN_SOCKET` exported. To keep your real `~/.claude/settings.json` out of
it, set `CLAUDE_CONFIG_DIR` to a scratch directory too; Claude Code then needs
its own login there.

`den list`, `den explain`, `den config` and `den events` are handy without the
TUI.

## Tests

```
make test                                              # vet + everything
XDG_STATE_HOME=$(mktemp -d) go test ./internal/ui     # one package
XDG_STATE_HOME=$(mktemp -d) go test ./internal/review -run TestAttributeTwoAgentsOneFile -v
```

Always set `XDG_STATE_HOME` to a scratch directory, as `make test` does. UI
tests build a `Model` directly and render it, so most behaviour is testable
without a terminal. `internal/spawn/live_test.go` is behind the `live` build tag and
opens a real terminal with a real Claude Code session; run it by hand only
(`go test -tags live -run TestLiveSpawn -v ./internal/spawn`).

## CI and releases

`.github/workflows/ci.yml` runs on pull requests into `dev` and `main`, on
pushes to them and weekly: `gofmt`, `go mod tidy -diff`, the
`THIRD_PARTY_LICENSES` check, `actionlint`, `make test TESTFLAGS=-race` on the
release Go and the newest Go (with no global git config and no module proxy,
so tests must set their own git identity and need no network; with the full
history checked out and ssh, sshd and tmux installed, for the remote
acceptance tests), `make build`,
`govulncheck` and `gitleaks`. Its last job, `ci-passed`, is the check branch
protection requires.

`make release` builds static `linux/amd64` and `linux/arm64` binaries,
`THIRD_PARTY_LICENSES` and `SHA256SUMS` into `dist/`, with the version from
`git describe --tags` unless `VERSION=` is given.
`.github/workflows/release.yml` runs when a `v*` tag is pushed: it checks the
tag is an annotated semver tag on `main`, runs the CI workflow, builds with
`make release VERSION=<tag>`, and publishes the four files with a build
provenance attestation. The branch model and the release steps are in
[RELEASING.md](RELEASING.md); the repository settings behind them in
[GITHUB-SETUP.md](GITHUB-SETUP.md). Only the maintainers tag releases.
