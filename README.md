# Den

One screen for every coding agent on this machine.

Den is a small TUI that finds the Claude Code sessions already running in your
terminals, shows who they are (the agent-badge critter, role and task), streams
everything that needs your answer into one inbox, and lets you talk to one
agent with an `@mention` or to an orchestrator that speaks to all of them.

Status: milestone 3. The Fleet view is live: agents grouped by state with
their critter, role, task, repo and branch, a detail pane, and Enter brings the
agent's terminal to the front through Hyprland (herdr panes included). With the
hooks installed, every permission prompt, plan review, question and finished
turn from every session lands in the inbox, and permissions can be answered
with one key. `@Name` on the command line sends a message straight into that
agent's session. See
[docs/PLAN.md](docs/PLAN.md) for scope and milestones and
[docs/mockup/den-mockup.html](docs/mockup/den-mockup.html) for the target UI.

```
go build -o ~/.local/bin/den ./cmd/den
den install-hooks     # once; wires den into ~/.claude/settings.json (backed up)
den                   # the TUI
den list [-json]      # what Den sees, for scripts and debugging
den focus mossy       # focus an agent's terminal by name, session id or pid
den send mossy "rebase on main first"   # message an agent from a script
den events            # headless: print hook events as they arrive
den uninstall-hooks   # undo
```

Hooks apply to Claude Code sessions started after installing. Den holds a
permission request only while you are not at that terminal; if the agent's
window is focused, or you switch to it, the normal dialog appears instead.

Keys: `j k` move · `Enter` focus terminal · `y n a` allow, deny, allow again
this session · `x` dismiss · `@` or `m` message the agent (Tab completes the
name, Enter sends, Esc cancels) · `1 2` Fleet and Inbox tabs · `Tab` next
pane · `r` rescan · `?` help · `q` quit.

Messages arrive in the agent's session the way messages from other Claude
Code sessions do, with a first line saying they come from you via Den. While
you are at that agent's terminal, Den holds the message and delivers it when
you leave, so it never lands under something you are typing.

Requirements: Claude Code 2.1.289+, Hyprland (for focusing), and optionally
the [agent-badge](https://github.com/GlacierHubAB/agent-badge) mod for names,
roles and tasks. Colours follow the current Omarchy theme.
