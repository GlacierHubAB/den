# Den

One screen for every coding agent on this machine.

Den is a small TUI that finds the Claude Code sessions already running in your
terminals, shows who they are (the agent-badge critter, role and task), streams
everything that needs your answer into one inbox, and lets you talk to one
agent with an `@mention` or to an orchestrator that speaks to all of them.

Status: milestone 5 in progress. The Fleet view is live: agents grouped by
state with their critter, role, task, repo, branch and context usage, a detail
pane with the agent's animated critter, context and usage gauges and recent
activity, and Enter brings the
agent's terminal to the front through Hyprland (herdr panes included). With the
hooks installed, every permission prompt, plan review, question and finished
turn from every session lands in the inbox, and permissions can be answered
with one key. `@Name` on the command line sends a message straight into that
agent's session, and plain text goes to an orchestrator that knows the whole
fleet and can relay for you. See
[docs/PLAN.md](docs/PLAN.md) for scope and milestones and
[docs/mockup/den-mockup.html](docs/mockup/den-mockup.html) for the target UI.

```
go build -o ~/.local/bin/den ./cmd/den
den install-hooks     # once; wires den into ~/.claude/settings.json (backed up)
den                   # the TUI
den list [-json]      # what Den sees, for scripts and debugging
den focus mossy       # focus an agent's terminal by name, session id or pid
den explain mossy     # how Den arrived at that agent's name, status, window…
den send mossy "rebase on main first"   # message an agent from a script
den ask "who is blocked on what?"       # one orchestrator turn from a script
den events            # headless: print hook events as they arrive
den uninstall-hooks   # undo
den install-mods      # once; puts the bundled agent-badge mod into ~/.claude/skills
den mods              # what ships in this binary and where it is loaded from
den uninstall-mods    # undo
# Every hook run appends a line to ~/.local/state/den/hooks.log (event,
# session, delivered or not). DEN_HOOK_LOG=off silences it. Finished turns
# are appended to ~/.local/state/den/history.jsonl and come back on restart.
```

Hooks apply to Claude Code sessions started after installing. Den holds a
permission request only while you are not at that terminal; if the agent's
window is focused, or you switch to it, the normal dialog appears instead.

The command line at the bottom is always open: type to ask the orchestrator,
`@Name …` to message one agent (Tab completes the name), `/help` for commands.
Keys: `Tab` switches between Fleet, Inbox and Orchestrator · `← →` move
between the panes of a view · `↑ ↓` select or scroll in the focused pane ·
`Enter` sends, or with an empty line focuses the selected agent's terminal ·
`Ctrl+Y` allow and `Ctrl+N` deny the waiting prompt (`/always` allows it again
this session) · `/open`, `/dismiss`, `/rescan`, `/quit` · `Esc` clears the
line · `Ctrl+C` quits.

Messages arrive in the agent's session the way messages from other Claude
Code sessions do, with a first line saying they come from you via Den. While
you are at that agent's terminal, Den holds the message and delivers it when
you leave, so it never lands under something you are typing.

The orchestrator (tab `3`) is a headless Claude Code session Den starts on your
first plain-text message. It has exactly two tools, ListAgents and SendMessage,
and gets a fresh roster of the fleet with every message, so it can answer
"who is blocked on what?" or relay "tell Mossy to rebase first" without
hunting around. It cannot approve anything. Its turns are billed like any
other session; it uses `sonnet` unless `DEN_ORCHESTRATOR_MODEL` says otherwise.

Requirements: Claude Code 2.1.289+ and Hyprland (for focusing). The
[agent-badge](https://github.com/GlacierHubAB/agent-badge) mod ships inside
the binary (git submodule `mods/agent-badge`, embedded at build time) and
`den install-mods` drops it into `~/.claude/skills`, which Claude Code loads
without further setup; if a development checkout is already loaded through
`CLAUDE_CODE_PLUGIN_DIRS` the installer says so and leaves it alone. The
badge supplies roles, tasks and the 5-hour and weekly gauges (1.1.0+).
Critter names and faces are generated the same way the badge does, so they
match with or without it.

Colours follow the current Omarchy theme. Only the theme's real hues are used
for agents, topped up from a built-in set of twelve soft colours to at least
ten. Den gives every live session its own colour, oldest session first, and
records the choice in `~/.local/state/den/colors.json`; the badge (1.2.0+)
reads that file so the critter in each terminal matches the one in Den.

Clone with `git clone --recurse-submodules`, or run `git submodule update
--init` afterwards, before building.
