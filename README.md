# Den

One screen for every coding agent on this machine.

Den is a small TUI that finds the Claude Code sessions already running in your
terminals, shows who they are (the agent-badge critter, role and task), streams
everything that needs your answer into one inbox, and lets you talk to one
agent with an `@mention` or to an orchestrator that speaks to all of them.

Status: milestones 0 to 5 shipped; Den is a daily driver. The Fleet view is live: agents grouped by
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
make install          # builds ~/.local/bin/den with the version from git
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
den config            # the config file path and the settings in effect
den config init       # write a documented ~/.config/den/config.toml
den version
# Every hook run appends a line to ~/.local/state/den/hooks.log (event,
# session, delivered or not). DEN_HOOK_LOG=off silences it. Finished turns
# are appended to ~/.local/state/den/history.jsonl and come back on restart.
```

Hooks apply to Claude Code sessions started after installing. Den holds a
permission request only while you are not at that terminal; if the agent's
window is focused, or you switch to it, the normal dialog appears instead.

The command line at the bottom is always open: type to ask the orchestrator,
`@Name …` to message one agent (Tab completes the name), `/help` for commands.
Keys: `Tab` switches between Fleet, Inbox, Orchestrator and Review · `← →` move
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
first plain-text message. It has four tools: ListAgents and SendMessage,
plus two Den gives it through its own MCP server (`den mcp`), spawn_agent
and open_terminal. It gets a fresh roster of the fleet with every message,
so it can answer "who is blocked on what?" or relay "tell Mossy to rebase
first" without hunting around. It cannot approve anything. Its turns are
billed like any other session; it uses `sonnet` unless the config file or
`DEN_ORCHESTRATOR_MODEL` says otherwise.

The Orchestrator view has no tab: `Ctrl+O` opens it and brings you back,
and sending plain text opens it too. Its state sits beside the keycap on the
tab row.

Ask it for a new agent ("start an agent in ~/code/site that fixes the login
redirect", "spawn someone on the release notes") and it opens your default
terminal on the workspace you are looking at, in that directory, with
`claude --name <peer>` running and the brief it wrote as the first prompt.
The brief arrives introduced as coming from the orchestrator on your behalf,
and asks the agent to report back to it when done or blocked. The new agent
shows up in the Fleet and the roster like any other, and the orchestrator
tells you its critter name. "Open a terminal in ~/code/site" gives you a
plain shell the same way. The terminal is launched as Omarchy's own keybinding
does it (`uwsm-app -- xdg-terminal-exec --dir=…`), so it is detached from Den;
Den records your active workspace first and moves the window there if it
landed anywhere else. Set `spawn = false` under `[orchestrator]`, or
`DEN_ORCHESTRATOR_SPAWN=off`, to take the two tools away again. Only Den's
MCP server is loaded into the orchestrator (`--strict-mcp-config`); your own
MCP servers never are.

## Dispatch cards

When the orchestrator hands a brief to an agent, that agent's turn shows up
in the chat as a card the agent owns: its avatar and colour, the role chip,
the brief, and a state chip through queued, working, needs you, done or
failed. While it works the card shows the latest tool call from the hook
stream; done, it collapses to the agent's last message in one sentence and
the call and file counts. With two or more cards live, an in-flight strip at
the top of the pane keeps them in view. Everything on a card comes from the
agent's own hooks and transcript, never from the orchestrator's retelling.

| Key | On a focused card |
| --- | --- |
| `↑ ↓` | move between cards; past the last one the chat follows its tail again |
| `Enter` (empty line) | expand to the agent's full turn, with diffs and output, and its last message verbatim |
| `→` | open the agent in the Fleet view; `←` comes back to the card |
| `^Y ^N` | answer the card's permission, exactly as in the inbox |
| `/open` | focus the agent's terminal |
| `PgUp PgDn` | scroll the chat |

The chat persists: every entry and card state is appended to
`~/.local/state/den/orchestrator.jsonl` and restored on launch (newest 500).
Cards that were live when Den quit are settled from the agent's transcript or
marked gone. The orchestrator itself resumes its previous session, so it
remembers the conversation; if that fails, Den starts a fresh one and replays
the last exchanges. `/orch new` starts a fresh session on purpose and marks
the chat; the history stays.

## Overview and missions

The Overview tab, first in the row, shows one project at a time. Projects
are the repositories agents work in, found through the files their
transcripts say they edited plus session folders that are checkouts; the
strip under the tabs lists them with a count of agents on each. `← →` or
`/project <name>` switch, the pick is remembered, and the Review tab follows
it: its work list is that checkout's. Left, the project's agents in their
tree; right, its missions.

A mission is a standing goal, in Aimless's terms: a purpose in one sentence,
constraints that ride on every dispatch, and what done looks like, or
"standing" for one that never ends. Type the goal on the Overview command
line (or `/mission <goal>`) and the orchestrator answers with a proposal:
the mission rewritten, done-when, constraints it inferred, the agents it
means to use, and the shape it picked with one line of reasoning. `^Y`
starts it, `^N` drops it, typing refines it, `Esc` keeps it for later.
Nothing runs before `^Y`.

| Shape | Runs as |
| --- | --- |
| `▶ once` | one dispatch to one agent; done when it reports back |
| `↻ loop` | one agent iterates; after each pass Den asks the orchestrator whether the goal is met, and it re-dispatches until it is |
| `⇶ fan-out` | a lead with children in parallel, using the hierarchy, repeated until the lead says done |
| `⏲ schedule` | Den fires a run on the cadence (`every 30m`, `daily 08:30`, `weekly mon 09:00`, or a five-field cron line); each run is a once or a fan-out |

Den drives the runs: it asks the orchestrator to dispatch, tags the cards
that come out of that turn with the mission, notices when they settle, and
asks again when the shape calls for another pass. `Enter` on a mission opens
it: the fields, then its runs with the same dispatch cards the orchestrator
chat shows; `^Y ^N` answer a card's permission from there. `/pause` holds
the next run, `/resume` lets it go, `/stop` ends the mission and keeps its
history. Missions persist in `~/.local/state/den/missions.jsonl` and the
project pick in `overview.json` beside it.

## Who directs whom

Agents hand work to each other over Claude Code's session messaging, so the
fleet is a tree, not a list. Den learns the edges from the traffic it already
sees: a dispatch card is an edge from the orchestrator; a peer message
arriving at an agent (the UserPromptSubmit hook carries the sender) puts the
receiver under the sender, unless the receiver sits above the sender, in
which case it is a report back; an agent the orchestrator spawned is its
child from the start. Edges are appended to `~/.local/state/den/edges.jsonl`
and come back on restart; a child keeps its newest parent and drops out of
the tree when its session is gone.

In the Fleet view every tree sits in its own section, the orchestrator's
first, with guides between parent and child, `directs n` on a parent, and
what the branch needs rolled up (`1 needs you`). Agents nobody directs keep
their status sections. `←` folds the selected branch and `→` unfolds it;
`↑ ↓` skip folded rows. The details pane shows the chain of command (`you ›
orchestrator › Mossy Lemur`) and a `directs` list with each child's brief and
state. `^T` or `/map` swaps the pane for a map: one column per level from
you to the leaves, `↑ ↓` walk it, `←` goes to the parent and `→` to the first
child, and the map scrolls to keep the selection in view.

In the orchestrator chat a card whose agent handed work on grows a line per
child with its brief and state, and the card's own state says `needs you ·
via <child>` when something deeper down is waiting on you; `^Y ^N` on that
card answer the child's request when the card's own agent has none. Inbox items from
an agent inside a tree carry a `via orchestrator › …` line so a permission
from a sub-agent is not a stranger asking.

## The command line

The line at the bottom is always open. It grows with what you type, one row
for a short message and up to eight as text wraps or you add line breaks,
then scrolls; the panes above shrink to make room. `Enter` sends,
`Shift+Enter`, `Alt+Enter` or `Ctrl+J` insert a newline, `Esc` clears.

Images travel with a message. Paste or drag an image file (a path, a
`file://` URL or several lines of them) and it becomes a chip, `[Image #1]`,
in the text; `Ctrl+V` takes an image straight from the clipboard (saved
under `~/.local/state/den/attachments/`). When the message goes out the
chip reads `[Image #1: /full/path.png]`, so the receiving session can open
the file. Den asks the terminal whether it speaks the kitty graphics
protocol; if it answers, a small thumbnail is drawn next to each chip, and
otherwise the chip and file name stand alone. Delete a chip from the text
and its image is left out.

## Configuration

Everything has a default, so the file is optional. `den config init` writes
this starting point to `~/.config/den/config.toml` (`DEN_CONFIG` overrides
the path):

```toml
[orchestrator]
model = "sonnet"            # any claude --model value, or "off" for Claude Code's default
name = "den-orchestrator"   # how the orchestrator session names itself to peers
spawn = true                # may open terminals and start agents on the workspace you are on

[inbox]
history = 100               # finished turns kept as history

[detail]
recent = 20                 # activity entries shown in the details pane

[hooks]
permission_wait = 580       # seconds Den may hold a permission before the terminal dialog takes over
log = true                  # append every hook run to ~/.local/state/den/hooks.log
```

`DEN_ORCHESTRATOR_MODEL`, `DEN_ORCHESTRATOR_SPAWN=off`, `DEN_PERMISSION_WAIT`
and `DEN_HOOK_LOG=off` still win over the file, so scripts keep working.

## Releases

`make release` builds static `linux/amd64` and `linux/arm64` binaries into
`dist/` with a checksum file, stamped with `git describe`. Pushing a `v*` tag
runs the same build on GitHub Actions and attaches the binaries to a release;
`make test` runs on every push.

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
