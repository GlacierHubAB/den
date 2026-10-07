# Den

One screen for every coding agent on this machine.

Den is a small TUI that finds the Claude Code sessions already running in your
terminals, shows who they are (the agent-badge critter, role and task), streams
everything that needs your answer into one inbox, and lets you talk to one
agent with an `@mention` or to an orchestrator that speaks to all of them.

Status: milestone 1. The Fleet view is live: agents grouped by state with
their critter, role, task, repo and branch, a detail pane, and Enter brings the
agent's terminal to the front through Hyprland (herdr panes included). See
[docs/PLAN.md](docs/PLAN.md) for scope and milestones and
[docs/mockup/den-mockup.html](docs/mockup/den-mockup.html) for the target UI.

```
go build -o den ./cmd/den
./den                 # the TUI
./den list [-json]    # what Den sees, for scripts and debugging
./den focus mossy     # focus an agent's terminal by name, session id or pid
```

Keys: `j k` move · `Enter` focus terminal · `Tab` next pane · `r` rescan ·
`?` help · `q` quit.

Requirements: Claude Code 2.1.289+, Hyprland (for focusing), and optionally
the [agent-badge](https://github.com/GlacierHubAB/agent-badge) mod for names,
roles and tasks. Colours follow the current Omarchy theme.
