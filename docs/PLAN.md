# Den: scoped plan

Den is a small Go + bubbletea TUI that shows every coding agent running in
your terminals, puts everything that needs your answer in one inbox, and lets
you talk to one agent with an `@mention` or to an orchestrator that relays to
all of them. Claude Code is the only first-class harness in v1.

Mockup: `docs/mockup/den-mockup.html` (also published as a claude.ai artifact).
"Den" is a working title.

## Goals

1. See at a glance who is running, what each agent is doing, and who needs you.
2. Answer permission prompts, questions and plan reviews from one place, or
   jump to the agent's real terminal with one key.
3. Message one agent (`@Mossy Lemur …`) or the orchestrator (plain text).
4. Stay small: one binary, zero config to start, Omarchy theme colours.

## Non-goals (v1)

- No embedded terminals, panes, tabs or workspaces. Enter focuses the real
  Ghostty window through Hyprland. herdr already owns that problem.
- No plugin system, no themes beyond Omarchy `colors.toml`, no sounds.
- No multi-machine, SSH or session persistence across restarts.
- No auto-approval. Den never answers a permission prompt on its own.
- Other harnesses (pi, Codex) appear as dim read-only rows if detected at all.

## What Claude Code already gives us

Verified against Claude Code 2.1.289 and its docs on 2026-10-07/08.

| Need | Source | Notes |
| --- | --- | --- |
| Discover sessions | `~/.claude/sessions/<pid>.json` | sessionId, cwd, `status` busy/idle, name, `messagingSocketPath`. File semantics are not documented; verify pid is alive. |
| Identity | agent-badge plugin store `~/.claude/plugins/store/agent-badge_*.json` | `badge:<sessionId>` → role, task, history. Critter name = same generator (`internal/badge`). |
| Needs-you signals | Hooks: `Notification` (`permission_prompt`, `idle_prompt`, `agent_needs_input`, `elicitation_*`), `PermissionRequest` (may return allow/deny), `Stop` (`last_assistant_message`), `SessionStart`/`SessionEnd`, `UserPromptSubmit` | Hooks receive `session_id`, `cwd`, `transcript_path`, `tool_name`, `tool_input`. Default timeout 600 s. All matching hooks run in parallel. |
| Send a message to an agent | Per-session inbox socket, documented for scripts: one JSON line `{"type":"user","message":{"role":"user","content":"…"}}`; optional first line `{"type":"auth","token":"…"}` | Arrives as "message from another session": cannot approve anything, commands are plain text. Delivered between tool calls or starts a turn when idle. Verified 2026-10-08: the socket sends no acknowledgement, and extra fields such as `from`/`name` are ignored, so Den names the real author in the text (`peer.Header`). |
| Idle notice | `SendMessage` `notify_when_idle` (peer feature `notify_idle`) | Only from a Claude session, which the orchestrator is. |
| Orchestrator | A Claude Code session Den spawns with `claude -p --input-format stream-json --output-format stream-json`, allowed tools `ListAgents`, `SendMessage` | It already knows how to list and message peers. Den only renders its stream. |
| Transcript tail | `~/.claude/projects/<slug>/<sessionId>.jsonl` | Internal format, changes between releases. Best effort only, behind an interface. |
| Terminal window | `{"type":"ai-title","aiTitle":…}` records in the transcript | Claude Code sets the terminal title to this text behind a status glyph (`✳`, `◑`). Ghostty is one process for all windows, so pid ancestry cannot tell windows apart; Den matches `hyprctl clients -j` on the stripped title, then on the shell's cwd title. Sessions whose ancestry includes `herdr` are focused through `herdr agent focus` plus the herdr client window. |

Hard parts, to prototype first:

- **AskUserQuestion has no remote answer path.** Hooks can see it
  (`PreToolUse`, `Notification agent_needs_input`) but not answer it. v1 shows
  the question in the inbox and Enter jumps to the terminal. Later options: a
  channel with permission relay, or a mod using `ui.*` events.
- **PermissionRequest hook vs. the terminal dialog.** Den assumes the dialog
  waits for the hook. So Den holds a request only while you are *not* at that
  terminal: if the agent's window is Hyprland's active window when the
  request arrives, or becomes active while it is held, Den releases it with no
  decision and the normal dialog appears. Otherwise it is held for up to
  580 s (`DEN_PERMISSION_WAIT`), and `y`/`n`/`a` in Den answer it. Still to
  confirm by hand: that an interactive session shows nothing while held and
  shows its dialog promptly on release.
- **Typing guard.** Never post into a session whose terminal window is
  focused and whose prompt has text. Shipped as: while the agent's window is
  Hyprland's active window, the message waits in the inbox as "waiting: you
  are at that terminal" and is delivered within a second of you leaving, or
  after 10 minutes regardless.

## Architecture

```
┌──────────────────────── den (one Go process) ────────────────────────┐
│ discover   badge   theme   hooks-bridge   peer   orchestrator   ui   │
│   │          │       │          │           │         │          │   │
│ sessions/  store   colors   $XDG_RUNTIME_  cc-socks  claude -p   bubbletea
│ *.json    *.json   .toml    DIR/den.sock   /<pid>    stream-json         │
└──────────────────────────────────────────────────────────────────────┘
                 ▲ JSON lines from `den hook <event>` installed in ~/.claude/settings.json
```

- **Event bus.** A tagged union (`Type` discriminator) of events:
  `session.seen`, `session.gone`, `status.changed`, `attention.opened`,
  `attention.resolved`, `turn.done`, `message.sent`, `message.failed`.
  The UI is a pure function of the resulting state.
- **State vocabulary.** `working · needs you · idle · gone · unknown`.
  `needs you` only ever comes from an explicit hook signal; nothing is guessed.
  `unknown` is shown as `?`, never as idle.
- **Inbox item.** `{id, sessionId, kind, title, body, options, requestedAt,
  expiresAt, resolve}` with kinds `permission · question · plan · done · idle`.
  Only "waiting on the human" items are inbox items; agent-to-agent waits are
  not.
- **Hook bridge.** `den hook` is the single hook command, installed once by
  `den install-hooks` (adds entries to `~/.claude/settings.json`, backs the
  file up, removable with `den uninstall-hooks`). It reads hook JSON on stdin,
  drops subagent events (`agent_id` present), posts one JSON line with a
  monotonic `seq` to `$XDG_RUNTIME_DIR/den.sock`, and fails open with a 0.5 s
  timeout when Den is not running. `PermissionRequest` is the only blocking
  hook: the connection stays open until Den replies `{"behavior":"allow|deny"}`
  or `{}` for "show the dialog". `a` ("always") is Den-side and in-memory: the
  same tool, and for Bash the same first two words, is allowed again for that
  session while Den runs. Claude Code's own permission rules are never edited.
  `AskUserQuestion` is shown from `PreToolUse` and resolved by `PostToolUse`;
  only the terminal can answer it. `ExitPlanMode` arrives as a
  `PermissionRequest`, so plans are approved with `y` like any permission.
- **Focus terminal.** Match the window by title (see the table above), then
  dispatch `hl.dsp.focus({ window = "address:<addr>" })`; Hyprland 0.56+
  takes dispatchers as Lua, and Den falls back to the classic `focuswindow`
  form on older releases.
- **Theme.** Read `~/.local/state/omarchy/current/theme/colors.toml` at start
  and on change; fall back to Deepharness. Hue only for status.

## Patterns borrowed (ideas only, no code)

From herdr: the `working / blocked / done / idle / unknown` vocabulary with the
rule "only call it blocked when we recognise the prompt"; `done` meaning
"finished while you weren't looking", cleared on view; a blocked-only filter
with Enter-to-jump; `prompt --wait` that returns on a settled state; `agent
explain` to show why a state was assigned.

From OpenRig: an honest `unknown`; hooks as the primary signal and the screen
only as fallback; a dedicated attention list separate from the activity feed;
the typing guard; discover and adopt existing sessions instead of requiring
spawn-through-us; refuse to auto-answer prompts.

From agent-deck: the glyph set `● ◐ ○ ✕`.

From Aimless (proprietary, ideas only): one registry drives mentions, colours
and capabilities; specialists ask, the hub routes, with identity in an envelope
the model cannot set; `waiting_dependency` kept apart from `waiting_user` so
only the latter reaches the inbox; inbox items carry kind, priority and expiry
with badge counts; a tagged-union event stream; moderator turn-taking that
pauses for the user; fail-open hooks with `seq` ordering and subagent
filtering; relay loop guards (depth cap plus visited set).

## Milestones

| | Deliverable | Done when |
| --- | --- | --- |
| M0 | Scaffold: discovery, badge identity, theme, live list | `go run ./cmd/den` lists the sessions on this machine with names and status. **Shipped in this commit.** |
| M1 | Fleet pane for real: grouping by state, `?` unknown, repo · branch, model, Enter focuses the Hyprland window (herdr panes too), fsnotify refresh, `den list` and `den focus` | Matches screen 1's left pane. **Shipped.** Context % moved to M5: the badge store does not record it yet. |
| M2 | Hook bridge + inbox: `den install-hooks`, socket server, `Notification`/`Stop`/`SessionStart`/`SessionEnd`/`UserPromptSubmit`/`PostToolUse` → inbox and status; `PermissionRequest` round trip with `y`/`a`/`n`; `den events` | Permission prompts from any terminal can be answered in Den; the terminal fallback works. **Shipped.** Verified headless: a `claude -p` session's `PermissionRequest` reached Den, was allowed there, the tool ran and `PostToolUse`/`Stop` followed. Interactive dialog behaviour while the hook is held still needs a human check. |
| M3 | Messaging: `@` completion, post to the inbox socket, delivery receipts, typing guard, `m` on an agent, `den send` | Screen 1's command line. **Shipped.** Verified: `den send` delivered into this very session and into a herdr-hosted session; the message arrived framed as a peer message with Den's header. |
| M4 | Orchestrator tab: spawn `claude -p` with stream-json, render its text and fan-out, `notify_when_idle` reports | Screen 2. |
| M5 | Polish: detail pane with gauges and transcript tail (best effort), `den explain <agent>`, config file (`~/.config/den/config.toml`, keys only), README, release build | Daily-driver quality. |

## Decisions

- Language and UI: Go 1.26, `charm.land/bubbletea/v2`, `charm.land/lipgloss/v2`, `charm.land/bubbles/v2`.
- Module: `github.com/GlacierHubAB/den`. Private repository to start.
- Single binary with subcommands: `den` (TUI), `den hook <event>`, `den install-hooks`, `den explain`.
- No daemon. Den's socket lives only while the TUI runs; hooks fail open.
- Keys follow the mockup: `j k` move, `Enter` focus, `y n a` answer the top inbox item, `1-3` tabs, `Tab` next pane, `@` mention, `m` message, `?` help.

## Open questions

1. Name. "Den" is the placeholder.
2. Should the orchestrator be allowed to answer a permission on your explicit instruction ("allow the PR once it rebases"), or only relay text? Default: relay only; Den itself answers permissions.
3. Context % and usage limits: read from the agent-badge store (needs a small addition to the mod) or from the transcript? Prefer the store.
