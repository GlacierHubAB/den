# Den

See, approve and steer all your Claude Code agents from one terminal screen.

![Den's Fleet view: seven Claude Code agents, five of them in the orchestrator's tree of who directs whom, each with its role, task, model and context use, and the selected tech lead's details, critter, usage gauges and recent tool calls on the right.](docs/images/fleet.png)

Den is a small terminal UI that finds the Claude Code sessions already running
in your terminals, shows who they are (the agent-badge critter, role and task),
streams everything that needs your answer into one inbox, and lets you talk to
one agent with an `@mention` or to an orchestrator that speaks to all of them.
It also draws who directs whom, keeps standing goals as missions, and shows what
each agent changed as a diff you can comment on and send back to the agent,
and adds up what the work cost. Over ssh it shows the agents on your other
machines in the same Fleet, inbox and cost totals, and answers their requests
from here ([Remote agents](#remote-agents), [docs/REMOTE.md](docs/REMOTE.md)).

Den is Linux software. It reads `/proc` and `~/.claude/sessions`, brings
terminals to the front through Hyprland, and starts new ones through
`xdg-terminal-exec`. See [Requirements](#requirements) for what works without
Hyprland.

## Contents

- [Install](#install) · [Requirements](#requirements) · [On Omarchy](#on-omarchy) · [Quick start](#quick-start)
- [Views](#views): Overview, Fleet, Inbox, Review, Orchestrator
- [Notifications](#notifications) · [Events for scripts](#events-for-scripts)
- [The command line](#the-command-line) · [Keys](#keys)
- [Orchestrator](#orchestrator) · [Dispatch cards](#dispatch-cards)
- [Who directs whom](#who-directs-whom) · [Spawning agents and teams](#spawning-agents-and-teams) · [Closing agents](#closing-agents) · [Bringing agents back](#bringing-agents-back) · [Desktop control](#desktop-control) · [Skills for roles](#skills-for-roles)
- [Overview, projects and missions](#overview-projects-and-missions) · [Templates](#templates)
- [Cost](#cost)
- [Review](#review) · [Worktrees in Review](#worktrees-in-review) · [The agent badge](#the-agent-badge)
- [Remote agents](#remote-agents) · [Text from outside Den](#text-from-outside-den) · [docs/REMOTE.md](docs/REMOTE.md)
- [Configuration](#configuration) · [Files Den writes](#files-den-writes)
- [Troubleshooting](#troubleshooting) · [Den, Herdr and OpenRig](#den-herdr-and-openrig) · [Contributing](#contributing) · [License](#license)

## Install

### From source

The repository uses a git submodule (`mods/agent-badge`, the agent-badge mod
that Den embeds in its binary). Clone with it, or the build has nothing to
embed:

```
git clone --recurse-submodules https://github.com/GlacierHubAB/den
cd den
# already cloned without it?  git submodule update --init
make build        # ./den, stamped with the version from git describe
make install      # builds into $PREFIX/bin/den, PREFIX defaults to ~/.local
```

`go install …/cmd/den@latest` does not work: the badge mod lives in the
submodule, which a Go module download does not include.

`make test` runs `go vet ./...` and the test suite against a scratch
`XDG_STATE_HOME`, so it never touches your own Den state.

### From a release

Tagged releases carry static Linux binaries, `den_linux_amd64` and
`den_linux_arm64`, the `THIRD_PARTY_LICENSES` notices and a `SHA256SUMS` file
covering all three. Download the binary for your machine and the checksum file
into one folder, then:

```
sha256sum -c --ignore-missing SHA256SUMS
chmod +x den_linux_amd64
mkdir -p ~/.local/bin && mv den_linux_amd64 ~/.local/bin/den
den version
```

Releases after v0.3.1 also carry a build provenance attestation, which says
the binary was built by Den's release workflow from the tagged commit:

```
gh attestation verify den_linux_amd64 --repo GlacierHubAB/den
```

`make release` builds the same files into `dist/`; pushing a `v*` tag on main
runs it on GitHub Actions (see [docs/RELEASING.md](docs/RELEASING.md)). Only
Linux binaries are built.

## Requirements

- **Linux**, amd64 or arm64. Den reads the process tree from `/proc`
  (`internal/procs`), so it does not run on macOS or Windows.
- **Claude Code 2.1.289 or newer.** That is the version that writes
  `~/.claude/sessions/<pid>.json` for every live session, which Den treats as
  the source of truth (`internal/discover`). `claude` must be on `PATH` for the
  orchestrator and for spawned agents.
- **Go 1.26** (the `go` line in `go.mod`) to build from source.
- **A terminal** that reports `Shift+Enter` (Den asks for it; `Ctrl+J` and
  `Alt+Enter` also insert a newline). Thumbnails of pasted images need the kitty graphics protocol and
  are skipped without it.
- **Hyprland** for focusing an agent's terminal (`Enter` on an empty line,
  `den focus`) and for putting spawned terminals on your workspace. Without
  `hyprctl` the Fleet, Inbox, Review, orchestrator and hooks still work; focus
  reports that no window was found. herdr panes are
  focused too when the agent runs inside one.
- **git 2.38 or newer** for the merge check in Review (`git merge-tree
  --write-tree`); the rest of Den does not need it.
- **A terminal launcher for spawning:** `uwsm-app` with `xdg-terminal-exec`,
  or `xdg-terminal-exec` alone, or `$TERMINAL`. Without one `spawn_agent` and
  `open_terminal` report an error (`internal/spawn/spawn.go`).
- **`notify-send`** (libnotify) for desktop notifications. Clicking one to
  focus the agent works on omarchy-shell (Omarchy 4), mako and swaync, and
  through a middle click on dunst. Without `notify-send` nothing is shown and
  nothing else changes.
- **`ssh` and `scp`** only for [remote hosts](#remote-agents); Den uses your
  own ssh config and keys.
- **Hooks** (`den install-hooks`) for the inbox, permission answers, history
  and the hierarchy. Without them Den still lists and messages agents.
- **The agent-badge mod** (`den install-mods`) for roles, tasks and the 5-hour
  and weekly gauges. Optional: names and critters match with or without it.

## On Omarchy

Den is built on and for [Omarchy](https://omarchy.org), and that is where every
part of it works. On Omarchy:

- **Spawned agents open the way Omarchy opens a terminal.** Den runs
  `setsid uwsm-app -- xdg-terminal-exec --dir=<dir> -- claude …`, the same
  launch as Omarchy's terminal keybinding, so the window is your default
  terminal and is detached from Den (`internal/spawn/spawn.go`).
- **They land on the workspace you are on.** Den records the active Hyprland
  workspace before it launches and moves the new window there if it opened
  elsewhere (`internal/hypr`).
- **Enter focuses the agent's window** through `hyprctl`, matching Ghostty's
  one-process-many-windows by title and directory.
- **Colours follow the current Omarchy theme**, read from
  `~/.local/state/omarchy/current/theme/colors.toml` (`internal/theme`).

Install it the usual way: download a release binary into `~/.local/bin`
(already on Omarchy's `PATH`), or build it with `make install` (Go 1.26 or
later), then run `den install-hooks` once. Den ships no keybinding of its own;
to give it a key, bind `omarchy-launch-or-focus-tui den` in your Hyprland
bindings.

Other Linux desktops work too, without the window placement (see
[Requirements](#requirements)). Den does not run on macOS or Windows: it reads
`/proc`.

## Quick start

```
den install-hooks     # once; wires Den into ~/.claude/settings.json (backed up first)
den install-mods      # once; puts the bundled agent-badge mod into ~/.claude/skills
den                   # the TUI
```

Hooks and the mod apply to Claude Code sessions started after installing them.
Existing sessions show up in the Fleet at once but only report prompts and
turns once restarted.

`den install-hooks` adds one entry per event for `SessionStart`, `SessionEnd`,
`UserPromptSubmit`, `Notification`, `Stop`, `PostToolUse`, `PreToolUse`
(matcher `AskUserQuestion`), `PermissionRequest`, `StopFailure` and
`PostToolUseFailure`, each running `den hook`. Installing twice replaces Den's
old entries, so run it again after upgrading Den; `den uninstall-hooks` removes
them. `CLAUDE_CONFIG_DIR` moves the settings file Den edits.

After updating the `den` binary, restart Den as well: the orchestrator's own
hook and tools are recognised by the binary Den started them from, so a
running orchestrator's peer reports and mission updates are refused until
Den, and with it the orchestrator, starts again on the new binary.

### Commands

```
den                     the TUI
den list [-json]        what Den sees, for scripts and debugging
den focus mossy         focus an agent's terminal by name, session id or pid
den explain mossy       how Den arrived at that agent's name, status, window…
den send mossy "rebase on main first"   message an agent from a script; --wait for its turn to end
den send mossy@devbox "…"               the same for an agent on a remote host, through the running Den, after your ^Y in its inbox (no --wait)
den wait mossy --until done             block until an agent is idle, needs you, or done
den ask "who is blocked on what?"       one orchestrator turn from a script
den events -json        follow the event stream, one JSON object per line
den events              headless: print hook events as they arrive
den install-hooks       wire Den into Claude Code's hooks
den uninstall-hooks     undo
den skills              the skills Den installed, for which project or agent, from where
den skills catalogue    the vetted skills catalogue: repo, pinned commit, license, why
den skills match "ui designer"          what the catalogue offers a role
den skills remove frontend-design ~/code/site   remove what Den installed
den remote add devbox   add a host to the Fleet over ssh (docs/REMOTE.md)
den remote list [-json] the hosts, their policy, state and agents
den remote status devbox   the read-only checks again; also: install, remove, disable, enable
den install-mods        put the bundled agent-badge mod into ~/.claude/skills
den mods                what ships in this binary and where it is loaded from
den uninstall-mods      undo
den pair                pair a console app by hand: a code in Den's header, then your ^Y on its card
den unpair [all]        end the console; all also ends every app pairing
den config              the config file path and the settings in effect
den config init         write a documented ~/.config/den/config.toml
den version
```

`den mcp` and `den hook` are run by Claude Code, not by you.

## Views

The tab row reads `Overview  Fleet  Inbox  Review`. `Tab` and `Shift+Tab` cycle
through them. The orchestrator has no tab: `Ctrl+O` opens it and brings you
back, and sending plain text opens it too. Its state sits beside the keycap on
the tab row.

- **Overview**: one project at a time, its agents on the left and its missions
  on the right. See [Overview, projects and missions](#overview-projects-and-missions).
- **Fleet**: agents grouped by state (needs you, working, idle, gone) or by the
  tree that directs them, each with critter, role, task, repo, branch and
  context usage. A details pane shows the selected agent's animated critter,
  context and usage gauges, the chain of command and recent activity. `Enter`
  on an empty line brings that agent's terminal to the front.
- **Inbox**: every permission request, plan review, question and finished turn
  from every session. See below.
- **Review**: what each agent changed, as a diff. See [Review](#review).
- **Orchestrator**: the chat with the agent that knows the fleet.

### Inbox

![Den's Inbox: a waiting permission request from a backend developer agent to run go test, with Ctrl+Y allow, /always and Ctrl+N deny, above two finished turns from a technical writer and a reviewer.](docs/images/inbox.png)

With the hooks installed, each item is a permission, a plan review, a question
or a finished turn; Den adds its own questions, to close agents, to install
skills or to reopen agents after a restart. Waiting permissions and plans can
be answered from Den: `Ctrl+Y` allows and `Ctrl+N` denies the waiting one,
`/always` allows it and the same again this session (see below), `/open` focuses the
terminal, `/dismiss` clears an item, and `/unread` keeps the selected item
counted as new until you open or dismiss it (`/unread` again marks it read).
Finished turns are appended to `~/.local/state/den/history.jsonl` (the newest
`inbox.history`, 100 by default, come back on restart). The request shows the
whole command or edit the permission is about. A request taller than the pane
scrolls inside it (`↑` `↓`, `PgUp` `PgDn`) under a marker that says how many
lines are left, and `Ctrl+Y`, `/always` or a typed answer waits until you have
scrolled to its end. In another view, `Ctrl+Y` answers only a request that
view shows whole, as the Fleet view's Details pane can; otherwise it opens the
Inbox on that request to be read there first, and `Ctrl+Y` again answers it.
`Ctrl+N` never waits.

What `/always` allows from then on, for that agent's session only (an agent
on a host is told apart by its host as well), kept in
memory until Den exits (the line that confirms it says which):

- a command: that exact command only. A command with shell operators,
  substitutions, redirections or several lines (`;`, `&`, `|`, `$(`, a
  backtick, `<`, `>`) takes no `/always`; `Ctrl+Y` allows it once.
- a file edit (`Write`, `Edit`, `MultiEdit`, `NotebookEdit`): edits by that
  tool inside the agent's working directory, never outside it, never through
  a symlink that leads out, and never of a file whose change runs code or
  raises trust: anything under `~/.ssh`, `~/.config` or `~/.local`, a
  `.claude` folder or `.git` (any repository), `.githooks`, `.gitattributes`,
  `.gitmodules`, `.mcp.json`, `.gitconfig`, `.npmrc`, `.netrc`, `.tmux.conf`,
  `.vimrc`, X session files (`.xprofile`, `.xinitrc` and the like),
  `.pam_environment` or a shell start-up file (`.bashrc`, `.profile`,
  `.zshrc` and the like). An agent working in your home directory, or above
  it, gets no edit rule at all. Such an edit, or one outside, is asked about
  as usual, and takes no `/always`. Inside the working
  directory a rule does cover files other tools run later, such as `.envrc`,
  `.vscode/`, a `Makefile` or `package.json`.
  An agent on a remote host gets no edit rule at all: its paths and
  symlinks cannot be checked from here, so each edit there is asked about.
- `WebFetch`: fetches from the same host.
- an MCP tool: that tool of that server, never the whole server.
- any other tool: every call of that tool.
- a plan or a question: nothing; each is answered on its own.

Claude Code shows its own dialog in the agent's terminal at once, beside
Den's hold, so a request can always be answered there too; whichever answers
first counts. Answered or dismissed in the terminal first, the inbox item
closes at once and takes no `Ctrl+Y` or `Ctrl+N`. Den holds a request only
while you are not at that terminal: if the agent's window is focused, or you
switch to it, Den lets go and the terminal dialog is the only one, as it is
when `hooks.permission_wait` (580 s) runs out or Den is not running.

A plan (`claude --permission-mode plan`) is approved from Den like a
permission: `Ctrl+Y` is the dialog's "Yes, manually approve edits", so the
agent leaves plan mode for the mode it had before and starts on the plan;
`Ctrl+N` sends it back to keep planning, with a note that you denied it from
Den. Claude Code ignores a plain allow for a plan, so Den's hook answers with
the plan's own call, which Claude Code 2.1.289 takes; a plan call Den cannot
answer that way is left to the terminal and says so. Plans are approved one
at a time: `/always` does not answer a plan, as the dialog has no "always"
for plans either.

#### Answering an agent's questions

Off by default. With `answer_questions = true` under `[hooks]` in the config
(restart Den after changing it), a question an agent asks with Claude Code's
`AskUserQuestion` tool waits in the inbox as well as in its terminal: the
question, its header, its numbered options with their descriptions, and
"Question 2 of 3" when it asks several. On the Inbox view, select it and type
an option's number then `Enter`; for a question that takes several answers,
type several numbers (`1 3`). Anything else you type is your own answer, as
the dialog's "Type something" row takes it; put a number meant as text in
quotes (`"3"`). `/answer <text>` does the same from any view. A call with
several questions takes them one at a time, the answers so far listed above
the next one, and the agent hears back only once all are answered. `Ctrl+Y`
does not answer a question; `Ctrl+N` leaves it to the terminal.

The terminal dialog stays usable the whole time, and whichever you answer
first counts: answered or dismissed (`Esc`) in the terminal, the inbox item
clears. Den lets go after `hooks.permission_wait` (580 s, inside the hook's
600 s timeout), when you are at that terminal, or at once when Den is not
running; the question is then the terminal's alone. Den never answers on its
own.

This relies on undocumented Claude Code behaviour (a `PermissionRequest`
hook that allows the call with the answers added to its input), checked
against Claude Code 2.1.289. A later version may ignore such an answer; the
terminal dialog then stays and works as usual. No new hook is needed: it uses
the `PermissionRequest` hook `den install-hooks` already sets up.

Two kinds of item say an agent may be stuck. Neither ever counts as "needs
you":

- **failed**: a turn ended on an API error (`StopFailure`: rate limit,
  overload, login, billing), or `stuck.tool_failures` tool calls (3) failed in
  a row (`PostToolUseFailure`; a tool you interrupt with `Esc` does not
  count). The item names the error type or the tool, never the command or its
  output. A new prompt, or a tool call that works, resolves it.
- **stalled**: an agent has been working for `stuck.stalled` minutes (20) with
  no hook event. A long build looks the same, so this is a hint to look, not a
  verdict. Any hook event from that agent resolves it.

### Notifications

Den sends a desktop notification (through `notify-send`) when a permission,
plan or question starts waiting on you, and one reminder if it is still
waiting after `notify.remind` minutes (10). A notification reads like "Mossy
Lemur needs your permission": the agent's name and the kind of request, never
the command, a path, a prompt or file text; for an agent on a remote host,
"Mossy Lemur on devbox needs your permission". Clicking it brings that agent's
terminal to the front, as `den focus` does (for a remote agent it opens Den;
there is no window here to raise). That uses the notification's
default action: omarchy-shell (Omarchy 4), mako and swaync run it on a click,
dunst on a middle click; on a server without actions the click does nothing.

There is no notification for an agent whose terminal is the focused window, or
while Den's own window is focused (when Den can tell: Ghostty runs all its
windows in one process, so there it cannot), and none for the orchestrator's
own traffic.

The rest are off by default, each with its own switch under `[notify]`:

- `done`: an agent finished. Only the top of a branch notifies, an agent
  nobody in the fleet directs, or one the orchestrator does, and only once
  nobody under it is still working. Workers finishing for a lead stay quiet.
- `failed` and `stalled`: the stuck items above.
- `without_den`: Den only notifies while it runs. With this on, `den hook`
  turns Claude Code's own permission and input notices into a desktop
  notification when no Den is listening. There is nothing to click then.

`DEN_NOTIFY=off` turns every notification off.

### Events for scripts

`den hook` appends every event to `~/.local/state/den/events.jsonl`, one JSON
object per line with a version field and metadata only (kind, session, agent
name, time; never a command, prompt or file text). While Den runs it also
appends the events of [remote hosts](#remote-agents), each with a `host` field
(the host's label; absent for this machine). `den events -json` follows
it, `den wait <agent> --until idle|needs-you|done` blocks on it, and the `[on]`
config section runs a command per event kind, for example to push to your
phone. The stream and `den wait` work without Den running; the `stalled` and
`still_waiting` events and `[on]` commands come from a running Den. The
schema, exit codes and the environment `[on]` commands get are in
[docs/EVENTS.md](docs/EVENTS.md).

Messages you send to an agent arrive in its session the way messages from other
Claude Code sessions do, with a first line saying they come from you via Den.
While you are at that agent's terminal Den holds the message and delivers it
when you leave, so it never lands under something you are typing.

## The command line

The line at the bottom is always open. Type to ask the orchestrator, `@Name …`
to message one agent (`Tab` completes the name; `@Name@devbox` for one on a remote host), `/help` for commands. It grows
with what you type, one row for a short message and more as text wraps (by
display width, so wide characters count double) or you add line breaks, up to
a third of the screen; past that it scrolls inside itself and keeps the cursor
line in view. Nothing you type or paste is cut off. The panes above shrink to
make room.

Images travel with a message. Paste or drag an image file (a path, a `file://`
URL or several lines of them) and it becomes a chip, `[Image #1]`, in the text;
`Ctrl+V` takes an image straight from the clipboard (saved under
`~/.local/state/den/attachments/`). When the message goes out the chip reads
`[Image #1: /full/path.png]`, so the receiving session can open the file. If
the terminal answers Den's kitty-graphics query, a thumbnail is drawn next to
each chip. Delete a chip from the text and its image is left out.

### Slash commands

| Command | Does |
| --- | --- |
| `/allow` `/y`, `/deny` `/n`, `/always` `/a` | answer the waiting prompt |
| `/open` `/o` `/focus` | focus the selected agent's terminal |
| `/answer <text>` | answer the agent's question waiting in Den: an option's number, several for a multi-select, or your own words (with `hooks.answer_questions`) |
| `/dismiss` `/x` | clear the selected inbox item |
| `/unread` `/u` | keep the selected inbox item counted as new until opened or dismissed; again to mark it read |
| `/copy` | copy the last command block in view (Orchestrator and Overview) |
| `/close` `/kill` | close the selected agent (also one on a remote host, `/close Name@devbox`); `/close idle` every idle one; `/close idle 1h` those idle at least that long (`30m`, `2h`, `1d`); `/close <name>, <name>`; `--force` includes working agents and kills what does not stop |
| `/reopen <name>`, `/reopen --fork <name>` | reopen an agent Den has seen in the last two weeks with `claude --resume`; `--fork` opens a copy of its conversation under a new session id |
| `/desktop`, `/desktop stop`, `/desktop keep` | how a desktop mission stands; stop it at once; keep the last ended mission's journal, screenshots and log (see [Desktop control](#desktop-control)) |
| `/protect <Name>`, `/unprotect <Name>` | keep an agent out of every close; `/protect` alone lists what is protected |
| `/flag <Name>: <note>`, `/unflag <Name>` | mark an agent as holding context: a bulk close lists it without selecting it |
| `/rescan` `/r` | refresh now (in Review, reload the diffs) |
| `/view`, `/view rescue` | say whether the windows are shown (Agent view) or hidden on Hyprland's `special:den` (User view, set by an app on den-api.sock); `rescue` brings back every window on `special:den`, whatever it is, to its workspace or the one you are on. Only you can run it, from Den's own screen |
| `/remote`, `/remote connect <label>`, `/remote retry <label>` | list the remote hosts with their state; retry one now, applying a raise in `remotes.toml` after showing it (`connect`) or not (`retry`) (see [Remote agents](#remote-agents)) |
| `/overview` `/missions`, `/fleet`, `/inbox`, `/review` | switch view |
| `/fleet project`, `/fleet flat` | group the Fleet by project, or not; remembered |
| `/map` | toggle the Fleet map |
| `/orchestrator` `/orch` | open the orchestrator; `/orch new` starts a fresh session, `/orch restart` restarts it |
| `/project <name>`, `/project add <dir>`, `/project drop` | pick, list or unlist a project |
| `/mission <goal>`, `/pause`, `/resume`, `/stop` | missions |
| `/mission move <project>` | ask to move the open or selected mission to another listed project; `Ctrl+Y` in the Inbox moves it |
| `/mode`, `/mode <mode>` | show the permission mode agents start in, and the agents running in another; set it: a mode that asks more applies at once, one that asks less waits on a card (see [Permission mode](#permission-mode)) |
| `/budget 2M`, `/budget $5`, `/budget off` | set, show (`/budget` alone) or remove the budget on the open or selected mission |
| `/template`, `/template <name> <target or goal>`, `/template save <name>` | list the mission templates, start a proposal from one, save the open mission as one |
| `/delete` `/del` | remove the review comment under the cursor |
| `/send`, `/send <Name>`, `/send orchestrator` | send review drafts to their agents, to one agent, or those no live session can take to the orchestrator |
| `/merge [<Name>]`, `/cleanup [<Name>]` | in Review, ask to merge the selected agent's worktree branch, and to remove its worktree |
| `/help` `/?`, `/quit` `/q` `/exit` | |

## Keys

| Key | Does |
| --- | --- |
| `Tab` / `Shift+Tab` | next / previous view (with `@` typed, `Tab` completes the name) |
| `Ctrl+O` | open the orchestrator, press again to go back |
| `← →` | move between panes; on Overview switch project; in Fleet fold and unfold a branch |
| `↑ ↓` | select, or scroll in the focused pane |
| `PgUp PgDn` | scroll the chat; half a screen in Review; five rows elsewhere |
| `Enter` | send; with an empty line, focus the selected agent's terminal (Overview: open the mission; chat: expand the card; Review: edit the comment under the cursor) |
| `Shift+Enter`, `Alt+Enter`, `Ctrl+J` | newline |
| `Ctrl+Y` / `Ctrl+N` | allow / deny the waiting prompt (Overview: start / drop the proposed mission) |
| `Ctrl+T` | toggle the Fleet map |
| `Ctrl+E` | on an open mission with an empty line: show or hide the full text of its actions (a click on one toggles just that one) |
| `Ctrl+X` | close the selected agent (Fleet, map, Inbox): a question in the inbox lists what would close, `Ctrl+Y` does it |
| `Ctrl+V` | paste an image from the clipboard |
| `Esc` | clear the line; closes a proposal or an open mission; drops a review edit or range |
| `Ctrl+C` | quit |

Review adds `Shift+↑ ↓` to extend a selection and mouse support: click a work,
file or line, drag or shift-click for a range, wheel to scroll.

On the Orchestrator and Overview views Den owns the mouse too, because the
terminal's own selection runs across the whole row and would take the agents
pane and the borders along with the chat. Drag inside the chat or the mission
pane to select that pane's text only; letting go copies it. Click a command
block (the lines with a bar down the left) to copy the whole block, or type
`/copy` for the last one in view. The wheel scrolls. Copies go through OSC 52
(the terminal's clipboard escape) and through `wl-copy` when it is installed.
`Esc` drops the highlight. The terminal's own selection is still there: hold
`Shift` while dragging in Ghostty. The Fleet and Inbox views never capture the
mouse.

## Orchestrator

The orchestrator is a headless Claude Code session Den starts on your first
plain-text message. It has seven tools: `ListAgents` and `SendMessage`, plus
five Den gives it through its own MCP server (`den mcp`): `spawn_agent`,
`open_terminal`, `close_agent`, `propose_skills` and `update_mission`;
`close_agent` and `propose_skills` only ever ask you (see Closing agents and
Skills for roles), and `update_mission` is its only way to move a mission on
(see missions). With `spawn = false` it keeps `update_mission` alone. It gets a fresh roster of
the fleet with every message, so it can answer "who is blocked on what?" or
relay "tell Mossy to rebase first". It cannot approve anything; you do that in
the inbox.

Its turns are billed like any other session. It uses `sonnet` unless the config
file or `DEN_ORCHESTRATOR_MODEL` says otherwise (`"off"` keeps Claude Code's
default). Only Den's MCP server is loaded into it (`--strict-mcp-config`); your
own MCP servers never are. A watchdog names a silent turn, restarts it once and
resends what it owes.

It loads none of your Claude Code settings files on purpose
(`--setting-sources ""`): your hooks, permissions, plugins and MCP servers do
not apply to it. Den copies only what it needs to reach the API from your user
settings: your `env` goes into its process environment (without `DEN_*`,
`XDG_RUNTIME_DIR`, `HOME`, `CLAUDE_CONFIG_DIR`, `PATH`, `LD_*`, `BASH_ENV`,
`ENV`, `NODE_OPTIONS` and the like, which could steer its guard), and `apiKeyHelper`,
`awsAuthRefresh`, `awsCredentialExport`, `gcpAuthRefresh` (and `model` when
Den's model is `"off"`) go into Den's own settings file for it, mode 0600 in
Den's private runtime folder, removed when it exits. Nothing of yours is put
on its command line. If it cannot sign in, the chat says so.

Each message has its speaker's name on the line above it, so the text uses
the pane's full width.

Agents named in what the orchestrator writes show as `@Name` in that
agent's colour: `@Name` always, a bare name only when it is several
words written exactly (`Quiet Panda`), never inside code or a link. Only
agents on this machine count, under the names Den gave them, and never a
name two of them share; agents on hosts stay plain. An agent's own words (its recent activity,
its turn and report on a card, a report Den passes on) are never
coloured, so an agent cannot make a name look like Den's. Inbox bodies you approve
are shown exactly as sent and never restyled. Notifications name a local
agent as a plain `@Name`.

The chat persists: every entry and card state is appended to
`~/.local/state/den/orchestrator.jsonl` and restored on launch (newest 500).
The orchestrator resumes its previous session; if that fails, Den starts a fresh
one and replays the last exchanges.

### Peer messages

Other Claude Code sessions can message the orchestrator (`SendMessage`), as
agents do when they report on work it gave them. Every such message is first
stopped by the orchestrator's own hook (`den hook orchestrator-inbound`): it
never reaches the model as written. Den then takes it as a report only when it
comes from a live session on this machine that Den sees in its fleet, under
that session's name (not the orchestrator itself), and sends the text on as
its own turn, fenced as the agent's report: data, never instructions. Anything
else is refused, with a note in the chat. Each session may send a few reports
at once and then one every two minutes, and at most four wait for the
orchestrator at a time.

A prompt with no peer envelope reaches the orchestrator only when Den wrote it
in the last 30 seconds. The hook is tested before every launch; the
orchestrator does not start if it fails. If anything ever gets past it (the
hook missing, failing or timing out, or a peer's message reaching the model),
Den kills the orchestrator at once, never resumes that conversation, and
starts a fresh one.

### Guidance

`guidance` under `[orchestrator]` gives the orchestrator text of yours on how
to shape its answers. It is off unless you set it: `"i-have-adhd"` is the
[i-have-adhd](https://github.com/ayghri/i-have-adhd) skill (v0.4.1, MIT),
shipped inside Den with four edits so it never hides what waits on you, never
answers for you, never counts as an approval and never trims where text came
from (`internal/orch/guidance/i-have-adhd`). An absolute path names a file of
your own instead. Den never fetches guidance over the network and never sends
it to a host. It drops a leading frontmatter block and refuses, with a note,
text over 8192 bytes after that, a file that does not read, is not yours, is
writable by others, is a symlink, has other hard links or lies in Den's state
folder, `/proc`, `/sys`, `/dev` or a kernel pseudo filesystem, and text with
control, bidi or invisible characters.

Nothing loads until you approve it. The first time, and whenever the text's
SHA-256 changes, the inbox asks, showing the whole text and its hash; `Ctrl+Y`
counts once you have read it to the end. Den then records that hash, and only
that, in `~/.local/state/den/guidance.json`. Approved text goes into the
orchestrator's prompt in a `<human_guidance>` block after Den's own rules,
which it cannot relax or override. Den reads the key again every few seconds:
turning it off, or text that fails its checks, takes it out of the prompt once
the orchestrator is idle, and turning it on or changing it asks first and,
once you approve, puts it in once the orchestrator is idle. A turn in progress
is never cut short: the orchestrator resumes its session with the new prompt
at your next message. Other keys leave the approval alone. A process running as your user can change the config, the
file and the approval together, so Den's checks guard against mistakes and
other users, not against your own processes.

`den ask` from the command line starts a fresh orchestrator and doesn't load
guidance.

### God mode

God mode lets the agents you start act without asking, and lets Den answer
some of its own cards. It is turned on only in Den's own window: type
`/god on`, read the warning, then type `let agents act without asking`
within a minute. Nothing else turns it on (no config key, environment
variable, command from an app or socket verb), and every Den start begins
with it off. `/god off`, `den god off` in a terminal, your console and any
unpair end it.

While it is on, Den's header says so in red, and:

- agents the orchestrator starts on this machine run in
  `bypassPermissions`: Claude Code never asks them, so Den shows no
  permission cards for them at all. Remote hosts keep their own policy;
  agents a lead starts, and agents Den reopens, start in the global mode,
  and a bypass agent cannot start agents of its own;
- Den answers, yes, its own close, cleanup, restore, mission-update and
  mission-move cards that came after God mode went on, with a line in its
  chat for each;
- pairings, app grants, turning the API on, installs, guidance, skills,
  budgets and usage, messages to hosts, an app's turn, merges and your
  console stay yours, and only Den's own window answers them, as it alone
  gives an "always" answer to any card. That includes each tool in a turn
  your console's chat started: from the dock alone you cannot approve it
  while God mode is on, so answer it in Den's window;
- every message to an agent that runs without asking goes through Den: a
  hook Den gives that agent lets a peer message in only if Den delivered
  it, and Den shows a red notice for any it blocked. Den tests that hook
  when the agent starts, and reads the agent's transcript for any peer
  message Den did not deliver. If the test fails, such a message shows
  up, or that agent's Den settings file or the den binary changes, Den
  marks it unguarded in red, delivers nothing more to it and offers to
  close it. An agent in `bypassPermissions` that Den did not start this
  way is listed unguarded too;
- the orchestrator runs on Den's own `send_message`, never Claude Code's
  `SendMessage`, while God mode is on and while any guarded agent still
  runs.

What stays open: an agent that runs without asking can do anything you
can, including changing Den itself. What it reads is not fenced: files,
cloned repositories (their CLAUDE.md too), web pages and MCP output, which
is how an injection most often reaches it. Any program or agent that can run
commands as you without asking (including agents in auto, plan or God
mode) can type into a God-mode agent's terminal, or write to its inbox
socket, and is not stopped. Den lowers the chance that an injection
reaches such an agent; it cannot contain one that has.

### Apps (den-api.sock)

Den can serve a local API to apps on your machine: the
fleet, inbox digests, missions, and questions to a read-only orchestrator
that has no tools and cannot act. It is off until you type `/api on` and
confirm with `Ctrl+Y`; `/api off` turns it off at once, and `/api` lists the
apps connected. Agents' own processes are refused. See
[docs/DREY-API.md](docs/DREY-API.md).

While an app is connected, Den's header says so: `⇄ app-host connected`,
named by the program Den read from the system, never by a name the app
gives itself. An app that hides which program it is (it made itself
non-dumpable) shows as `unidentified`, and its cards say "a program Den
cannot identify" with its pid and start time: check those against what the
app shows before you answer.

**Control.** `/api pair` shows a code of eight words in Den's header for two
minutes. Type it only into the app's own pairing window. Den then asks you on
a card, naming the program, its path, pid and command line; only your
`Ctrl+Y` pairs it. A paired app may send messages to the orchestrator. Den
marks them as the app's, and anything the orchestrator would do for them
waits for your `Ctrl+Y`: for an app pairing, everything that would change
your fleet waits for you in Den. `/api control off` stops it, and `/api
unpair` revokes every pairing, your console's too.

**Your console.** Drey's dock pairs as your console by itself, with no code
and no card, whenever Den and the dock both run: Den checks through your
systemd user manager that it is `drey-host.service`'s main process, and says
in its chat what it paired (the program and its SHA-256, and what changed
since last time). It pairs again by itself after either restarts. Its powers
(answering cards, running commands) come with it when the program, unit and
pins are what you last gave them to since Den started; the first pairing
after Den starts, and one after a new Drey build, wait for one `Ctrl+Y` on
Den's powers card (`api.console_powers = "always"` skips that). `den pair`
in a terminal (or `/api console` in Den) shows a console code the same way
as above, for pairing by hand. The console may:

- read the cards Den shows and answer most of them, as your `Ctrl+Y` would.
  It never answers a console or app pairing, turning the API on, guidance,
  installing on a host, a message to an agent on a host, an app's grant,
  skills, an agent's question, or anything about a remote host: those wait
  for you in Den. A long card must be read before it can be answered; more
  than 20 answers a minute are refused; five answers in a row faster than
  one a second unpair the console.
- send Den's commands, one line at a time, checked by Den against a fixed
  list. Commands that answer cards (`/allow`, `/deny`, `/always`,
  `/answer`), `/quit`, `/unprotect`, `/send`, turning the API or control on,
  pairing, `/view rescue`, mission proposals and template runs are refused.
  Commands that act on Den's own screen (`/open`, `/dismiss` and the like)
  stay in Den. `/close`, `/reopen`, `/merge`, `/cleanup`, `/mission move` and
  `/usage resume`, `set` and `clear` raise a card (typed in Den, `/usage set`
  and `clear` apply at once). The console can answer those cards too, so it
  can lower the usage figure your usage stop is measured against. Den's chat
  notes every command a console sent.
- chat with the orchestrator, as an app's messages, never as yours. While
  the console has its powers, the orchestrator lists agents and messages
  them for it without a card, as it does for your chat in Den; anything
  else it wants still waits for your `Ctrl+Y`, and with its powers locked
  every tool waits again.

The header shows your console while one is paired: `⇄ Drey connected` when
Den read it clean as drey-host.service itself, else `⇄ console connected`,
with `· ^Y for powers` while it has none; `/api` says which program and pid
it is. `den unpair` ends it from any terminal; `den unpair all`
ends it and every app pairing. A code an app sends, in a question, a
message to the orchestrator or a console command, is refused and the code
cancelled.

After Den restarts, it restores your console and says so in its chat, with
the program's pid and start time: if that is not your app, run `den unpair
all`. `/api unpair` or `den unpair` stops the dock pairing by itself until
you start a pairing in Den again (`/api console` or `den pair`); `/api lock`
takes the console's powers, for every pairing after too, until your `Ctrl+Y`
on Den's powers card.

What stays: anything running as you that can change drey-host.service's
unit or program (both in your home dir) gets the dock's reading and chat
with no question in Den, and its full powers once you press `Ctrl+Y` on the
powers card after a change; with `api.console_powers = "always"` it gets
them with no question at all. Your levers are `/api lock`, `/api unpair`, and
God mode staying off.

Any program running as you can type into Den's window, read its
screen (and a code on it), or drive the app you paired. A paired console
answers for you, within the limits above, so pair only an app you trust,
and unpair it when you are not using it. Anything that can type into the
console's chat, or drive the console's process, can message any of your
agents through the orchestrator without a card; to stop that, lock the
console's powers (`/api lock`, until your `Ctrl+Y` on Den's powers card)
or unpair it. On Hyprland, `permission` rules for
keyboard access and screen capture can keep other programs from Den's
window.

### Dispatch cards

When the orchestrator hands a brief to an agent, that agent's turn shows up in
the chat as a card the agent owns: its avatar and colour, the role chip, the
brief, and a state chip through queued, working, needs you, done or failed.
While it works the card shows the latest tool call from the hook stream; done,
it collapses to the agent's last message in one sentence with call and file
counts. With two or more cards live, a strip at the top of the pane keeps them
in view. Everything on a card comes from the agent's own hooks and transcript,
never from the orchestrator's retelling.

| Key | On a focused card |
| --- | --- |
| `↑ ↓` | move between cards; past the last one the chat follows its tail again |
| `Enter` (empty line) | expand to the agent's full turn, with diffs and output |
| `→` | open the agent in the Fleet; `←` comes back to the card |
| `Ctrl+Y` `Ctrl+N` | answer the card's permission, exactly as in the inbox |
| `/open` | focus the agent's terminal |

## Who directs whom

Agents hand work to each other over Claude Code's session messaging, so the
fleet is a tree, not a list. Den learns the edges from traffic it already sees:
a dispatch card is an edge from the orchestrator; a peer message arriving at an
agent (the `UserPromptSubmit` hook carries the sender) puts the receiver under
the sender, unless the receiver sits above the sender, in which case it is a
report back; an agent the orchestrator spawned is its child from the start.
Edges are appended to `~/.local/state/den/edges.jsonl` and come back on
restart; a child keeps its newest parent and drops out of the tree when its
session is gone.

In the Fleet every tree sits in its own section, the orchestrator's first, with
guides between parent and child, `directs n` on a parent and what the branch
needs rolled up (`1 needs you`). `←` folds the selected branch and `→` unfolds
it. The details pane shows the chain of command (`you › orchestrator › Mossy
Lemur`) and a `directs` list with each child's brief and state. `Ctrl+T` or
`/map` swaps the pane for a map: one column per level from you to the leaves;
`↑ ↓` walk it, `←` goes to the parent and `→` to the first child.

In the chat, a card whose agent handed work on grows a line per child, and its
state says `needs you · via <child>` when something deeper down is waiting on
you; `Ctrl+Y` / `Ctrl+N` on it answer the child's request. Inbox items from an
agent inside a tree carry a `via orchestrator › …` line.

## Spawning agents and teams

Ask the orchestrator for a new agent ("start an agent in ~/code/site that fixes
the login redirect") and it opens your default terminal in that directory with
`claude --name <peer>` running and its brief as the first prompt. The brief
arrives introduced as coming from the orchestrator on your behalf and asks the
agent to report back when done or blocked. The agent appears in the Fleet like
any other, and the orchestrator tells you its critter name. "Open a terminal in
~/code/site" gives you a plain shell the same way.

The terminal is launched the way Omarchy's own keybinding does it (`setsid
uwsm-app -- xdg-terminal-exec --dir=…`), so it is detached from Den. Den records
your active workspace first and moves the new window there if it landed
elsewhere. If Claude Code has never worked in the folder, it asks you to trust
it in that terminal first; nothing happens until you answer. Set `spawn =
false` under `[orchestrator]`, or `DEN_ORCHESTRATOR_SPAWN=off`, to take the two
tools away.

### Overlapping work

Once a minute Den reads, from git, the files each local agent's branch changes
against the repository's integration branch (`origin/dev`, `origin/main` or
`origin/master`, else a local `dev`, `main` or `master`): committed, staged,
unstaged and untracked. When two agents in different checkouts of one
repository change the same files, Den tells both, once, which files and with
whom, so they agree who goes first or how to split the work, and the
orchestrator, which sees the overlap in its view of the fleet, decides. Den
notes it in its chat and never stops anyone over it. An agent hears it at most
once every ten minutes. Den runs nothing the checkout's own git config names
while it reads. On the trunk (`main`, `dev`, `master`, or origin's default
branch), or once a branch's commits have landed (a squash merge, say), only
what is uncommitted counts. These messages, and the branch nudges
below, reach the agent framed as Den's own ("Automatic message from Den … your
human did not type it"), never as words you typed.

### Team size

A repository's team is small by default: 3 live agents (`max_agents` under
`[team]`, 1 to 20; the orchestrator is not counted, and every worktree of one
repository counts toward the same team). The orchestrator and its leads start
a fourth only with spawn_agent's `reason`, one line on why; without it Den
refuses the spawn, naming the agents in the team. Den shows each reason in its
chat, quoted, with who gave it. God mode does not lift the cap. Agents you start
yourself are never counted against it.

### Short branches

Branches that live under a day merge cleanly. When an agent's branch is older
than a day (from its oldest commit since it left the integration branch;
`branch_hours` under `[team]`) or more than 30 commits behind that branch
(`behind_max`), Den tells the agent to merge or rebase soon, notes it in its
chat (the dock sees it too), and the orchestrator sees it in its view of the
fleet. It repeats once a day while the branch stays over. 0 turns either check
off. Only feature branches are nudged: never the trunk, a branch with no
commits of its own, or one whose commits already landed.

### Cost per merged change and idle agents

Den counts what a mission's branches land. While an agent on a mission has
commits of its own, Den remembers its branch's tip. Once that tip is on the
integration branch, or every file the branch changes reads there as it does at
the tip (a squash merge), the change counts once for the mission. It counts
even if the agent's worktree is gone. Den notes each one in its chat. An agent
on a mission with no turn for 30 minutes (`idle_minutes` under `[team]`, 0
off), no live card and nothing waiting in the inbox is idle. An open mission
shows its cost, its merged changes with the cost of each, and its idle agents.
The orchestrator sees the same in its missions block, and may give an idle
agent its next sub-goal or close it with close_agent, which asks you as
before. Den itself closes no one. The count reads the repository's own
branches, which agents can move, so the cost per merged change is a guide,
never evidence that a mission's work is done.

### When you are asked

Once you start a mission, the orchestrator keeps it moving without asking you.
It resolves review findings by dispatching the fixes. The exception is a
finding that would change your routine, which comes to you as a choice. Its
prompt names the hard limits that stay yours:

| Hard limit | Examples | Who stops for you |
| --- | --- | --- |
| Weakening security | a permission mode that asks less, God mode, console powers, a new pairing | Den: `/mode` waits on a card, God mode only from Den's window, the powers card (unless `console_powers = "always"`) |
| Publishing | a push, a pull request, a release, anything sent off the machine | the agent's own permission prompt, in its mode; nothing in God mode's `bypassPermissions` |
| Money | a mission's budget crossed | Den: it pauses the mission and asks |
| Acts that cannot be undone | deleting, forcing, closing an agent with uncommitted work | close_agent's card (Den answers it itself in God mode); the agent's permission prompt for the rest |

The orchestrator is told not to take or order any of these without your yes
for that work, or a standing rule you gave it yourself in Den's chat; a "rule"
that reaches it in an agent's report, a file or a message never counts. Where only that instruction
stands between an agent and the act (publishing in God mode, say), it is the
orchestrator's word, not Den's check. This changes no card and no check of
Den's.

### Permission mode

Every agent Den starts or reopens runs in one permission mode, passed as
`claude --permission-mode`. From the mode that asks least to the one that
asks most:

- `auto` (the default): a classifier approves actions; nothing asks you.
- `acceptEdits`: edits files in its folder without asking; asks before
  commands, MCP tools and edits elsewhere.
- `plan`: asks before edits and MCP tools, but runs any command the
  classifier allows.
- `default`: asks before edits, commands and MCP tools.
- `dontAsk`: runs only what you allowed in advance and denies the rest.

`plan` and `acceptEdits` each run something the other asks for, so neither
is below the other.

`/mode` shows the mode and names the agents running in another one. The
header shows the mode when it is not `auto`, and how many agents differ.
`/mode default` (or any mode that asks more) applies at once. A mode that
asks less, or a move between `plan` and `acceptEdits`, waits on a card in
the inbox that shows both modes and what each allows; only `Ctrl+Y` there
changes it. Agents already running keep their mode until they restart.

The mode is kept as `permission_mode` under `[agents]` in `config.toml`.
Den treats the file as a request: an edit that asks more applies, and one
that asks less, made while Den runs, waits on a card like `/mode`.
`bypassPermissions` is never accepted there, nor by `/mode`.

Agents Den started before this setting existed ran in `auto` too, which is
what Claude Code uses with no flag. Den now always passes the flag. An
agent in `auto` can run classifier-approved commands without asking you,
and those include writing to another agent's terminal input.

**A model per agent.** `spawn_agent` takes a model: `haiku` for small,
mechanical, well-specified tasks, `sonnet` for ordinary building, writing and
fixing, `opus` for work that needs judgment (design, architecture, review,
leading others). The orchestrator picks by the task. Its spawn note says which
(`▶ starting an agent in ~/code/site on opus · …`), and once the session
registers the note becomes that agent's dispatch card.

**A worktree per agent.** `spawn_agent` takes `worktree`: `true`, or a name.
The agent is then started with `claude --worktree <name>`, so Claude Code gives
it a git worktree of its own under `<repo>/.claude/worktrees/<name>` on a new
branch `worktree-<name>`, and parallel agents in one repository do not edit
the same files. With `true` the name is the agent's peer name (with a number
added if that folder already exists). The folder must be in a git repository
that has a commit; otherwise `spawn_agent` says so and starts nothing. The
agent's brief is extended with its branch and the instruction to commit there
and leave merging to you. A lead can give each of its people a tree the same
way. By default Claude Code branches from the repository's default branch on
the remote; its `worktree.baseRef` setting changes that. To carry gitignored
files such as `.env` into every new worktree, use Claude Code's own
`.worktreeinclude` file. Den has no per-repository setup command. When a
session exits with changes or commits in its worktree, Claude Code asks whether
to keep or remove it; an untouched worktree is removed without asking, and a
crashed session's stays. Den never removes one by itself (see Review).

**Leads staff their own work.** Every agent the orchestrator starts gets Den's
MCP server too, so it has `spawn_agent` itself, pre-approved. A lead's brief
names the roles under it with their models and parts; it starts them in their
own terminals, they report to the lead, and the lead reports up when the piece
is done. Ask for "a team" and the orchestrator starts one lead on `opus` and
leaves the rest to it. Den learns each of those spawns from the lead's own hook
stream (`PostToolUse` on `spawn_agent`), so the Fleet draws the tree as it
grows, and the lead's card stays `working · waiting on 3 agents` between turns
until nobody under it is at work.

## Closing agents

`Ctrl+X` on an agent, or `/close`, asks before anything happens: a question
lands in the inbox listing exactly what would close, one line per agent
with its process id and the terminal window Den matched to it (or "no
window found, only the process ends"), and `Ctrl+Y` carries it out, `Ctrl+N`
keeps everything. `/close idle` clears every idle agent in one question;
`/close Mossy Lemur, Snug Capybara` names them. The question warns when an
agent's checkout has uncommitted changes (which files), and when an agent
directs others, who stay open unless also selected.

The rules, enforced whatever asked: an agent that is working or waiting on
you is refused unless you add `--force`; the orchestrator, Den itself and
anything Den runs inside are never closed; an agent not in the fleet is
refused by name; the window Den runs in is never closed. Agents close one at
a time, half a second apart. Each gets a graceful stop (SIGTERM) and a short
wait; only `--force` sends SIGKILL to one that does not stop, and nothing is
forced from the orchestrator's side. Only the agent's own process is
signalled, never its terminal. A terminal Den opened for the agent runs it
as its command, as a Ghostty process of its own with a window class Den
chose (`io.glacierhub.den.<peer>`, recorded in Den's state with the
agent's session), and closes with it. Den closes a window itself only when
it is still open after the agent ended, is still the one window with the
class recorded for that session, is run by the agent's terminal, and has
nothing else running in it (no shell in another tab, no second claude):
never a window of an agent Den did not spawn, never one matched by title or
folder, never the window in front or its own. The question says,
agent by agent, which window closes and how, or why Den will not close it.
Closed agents leave Den's state at once: their open inbox items are
resolved, they drop out of the tree, and the roster rescans.

`/close idle 1h` picks the agents that have been idle for at least that long.
Idle time is measured from the agent's last activity, which Den takes from
Claude Code's own session file: the moment the session last changed between
busy and idle (`statusUpdatedAt`), that is, when its last turn started or
ended; a session that has not reported a change yet counts from its start.
Den does not use the transcript's last write for this, because Claude Code
appends to the transcript when messages arrive, which is not use. An agent
whose last activity is unknown is never treated as stale. The same time is
`LastActive` in `den list -json`.

Some agents must survive a bulk close. `/protect <Name>` keeps an agent out
of every close, even by name, until `/unprotect`; the list is
`~/.local/state/den/protected.json`, which also takes `paths` (an agent whose
working directory, edited repositories or task involve one of them is
protected) and `flagged` agents. `/flag <Name>: <note>` marks an agent as
holding context you may still need: a bulk close shows it as "Holds context,
not selected" with the note, and only `/close <Name>` includes it. The
question lists what was skipped and why, so the list is always exact.

The orchestrator has the same door, close_agent: "kill the idle agents we are
not using" or "close what has not been used for an hour" (`idle_for: "1h"`)
becomes the same question in your inbox, marked as its request with its
reason, protected agents skipped and flagged ones unselected, and it only
hears what you decided. It cannot force.

## Bringing agents back

Den keeps a snapshot of every live agent in `~/.local/state/den/roster.json`:
its session id, working directory, peer name, critter name, role, model,
who directs it, its mission, whether Den started it, its private skills
folder if it has one, and when Den last saw it. The file is rewritten
atomically when the fleet changes, and otherwise at most once a minute to keep
the last-seen times current.

When Den starts and agents that were live when it last stopped are gone (after
a reboot, say), one question lands in the inbox: `Restore 3 agents?`, one line
per agent with its name, role, model and folder, who directed it, its mission,
when Den last saw it, and how many tokens of conversation it would reload with
an estimated cost at its model's cache-write rate from the price table (see
[Cost](#cost)). `Ctrl+Y` reopens them, each in a new terminal on your workspace, the
way spawned agents start: `claude --resume <id>` with the same peer name,
model and skills folder, and Den's tools again for agents Den started.
`Ctrl+N` leaves them closed, and later restarts do not ask about them again.
Nothing reopens without `Ctrl+Y`.

The question leaves out agents you closed through Den (`/close`, `Ctrl+X` or
the orchestrator's close_agent) and agents that ended on their own while Den
was running, unless they ended within two minutes of Den's own last write,
which is how a shutdown looks. Agents whose conversation is no longer on disk
are left out too.

A resumed session keeps its session id, so it takes its old place in the tree
and its mission. Den puts each reopened agent back under the agent that
directed it once both are running again. `/reopen <name>` asks the same
question for one agent, closed or ended; `/reopen --fork <name>` works on a
running agent too and opens a copy of its conversation under a new session
id, which Den records so the copy takes the original's place in the tree.

`restore.ask = false` in the config, or `DEN_RESTORE=off`, turns the restart
question off; the roster is still kept for `/reopen`.

## Desktop control

A desktop agent moves the mouse and types on your Hyprland desktop, in apps
you allowed, on a workspace Den frees for it, and every step passes Den's
gate. It is built on [hypruse](https://github.com/IlyasKhallouki/hypruse)
(MIT), pinned at commit 0035d86 and installed unmodified into Den's own
venv.

**Install.** Nothing to do by hand. When hypruse is missing, the first
desktop card says "Den installs hypruse first", and `Ctrl+Y` installs it,
then starts. Den downloads GitHub's archive of the pinned commit (HTTPS to
github.com only) and keeps only the files the pin names, each checked
against the SHA-256 Den was built with. Any mismatch stops the install. It
makes a venv from the system's `python3`, and installs the dependencies
(`mcp` and 28 more) from PyPI, each pinned by hash, wheels only. None of
hypruse runs while it installs. It compiles the bytecode once, then records a
hash of every installed file. Den checks them all before every desktop agent
starts, and runs hypruse so that it never writes bytecode. If the venv is no
longer as recorded (a file added or changed), the next card says what is wrong
and `Ctrl+Y` reinstalls it. The venv is `~/.local/share/den/hypruse/venv`;
nothing runs `uvx` later.

To install from a clone you made yourself instead, run `den install-hypruse`.
It asks on a card too. It uses `--from <folder>` (a local clone, never a URL),
or the folder it runs in, or else a folder named `hypruse` up to four levels
under your home, but only a clone whose every file matches the pin.
`--wheels <dir>` installs the dependencies from a folder, offline. When the
venv already holds the pinned hypruse, it says so and changes nothing.

**Allow apps.** A desktop agent uses only the apps you list in Den's config:

```toml
[desktop.apps.editor]
command = ["foot", "--app-id", "den-editor", "nvim"]
class = "den-editor"
outward = "no"            # "ask" (the default): every click and keystroke asks; "words": only Send-like clicks and Enter ask

[desktop.apps.browser]
command = ["zen-browser", "--new-instance"]
class = "zen"
profile = ["--profile", "{dir}"]   # a profile of its own, in the mission's folder
if_running = "refuse"              # or "warn": joining an instance already running uses its sign-ins
```

**Start a mission.** The orchestrator (or a lead) calls `desktop_mission`
with a plan, app names and the steps only you do ("sign in to Gmail"). A card
shows the plan, each app, and what the agent will and will not have. On
`Ctrl+Y` Den picks an empty workspace (9 down to 5), opens the apps there
itself, and starts the agent. The agent has only the desktop tools: no
shell, files, web, messages, app launching, keybinds or clipboard. It runs
with no window of its own, so nothing opens in front of the work: what it
says and does shows in Den's chat, and Drey's overlay shows
it at work on the screen. To watch it in a terminal of its own instead, set
`agent_view = true` in the `[desktop]` section; `Ctrl+C` there stops it.

**A task: the whole desktop.** The orchestrator can instead call
`desktop_mission` with one task and no apps. The card reads "Control your
mouse and keyboard to: …" with the whole task (a task too long to show whole
is refused, and so is one holding hidden characters). On `Ctrl+Y` the agent
may use your whole desktop for that task: the pointer and keyboard in any
window, your Hyprland binds, and launching apps, with no app config. No step
asks you except the agent's own `request_approval` and `your_turn`.

Read this before you say yes: **while a task runs, the agent can do anything
you can at the keyboard.** That includes running commands as you in a
terminal, typing into other agents' terminals (their permission prompts
too), the clipboard, everything on the screen, and every app you are signed
in to. Den refuses only a short fixed list: anything on Den's own window,
the agent's terminal when you turned it on, or a paired console's dock
(Drey's); exiting or reloading Hyprland, or running `hyprctl` or
`den` through a bind or a launch; closing or killing Den's window; binds
that send keys to another window by name; and `ctrl+alt+Delete`,
`Backspace` and `F1`–`F12`. Your levers are these:

- **The panic stop.** `/desktop stop` or `den desktop stop` (bind it to a
  key, below). The agent also stops when Den quits, however Den ends: it is
  Den's own child, signalled when Den goes (with `agent_view`, its terminal
  watches Den), and `den desktop stop` still works from Den's records.
- **The per-task limit.** Den stops the task after `task_minutes` of wall
  time or `task_actions` desktop calls, whichever comes first. Den counts
  them itself, and only your config sets them:

  ```toml
  [desktop]
  task_minutes = 20   # the default
  task_actions = 300  # the default; every forwarded call counts, each typed piece too
  ```

- **One task per card.** A task never widens. Another task is another
  card, answered only in Den's own window.

Den finds its own window by briefly setting its terminal title to a one-off
token, so it starts only when it can see that title. Run Den in its own
terminal window, with its tab in front, not inside tmux or screen; otherwise
the start is refused and says so. In a window with several tabs, the whole
window is off limits to the agent, other tabs too. `/desktop` shows how many
actions and minutes a running task has left. One gap: if the dock's command
bar takes the keyboard (through one of your binds, say), Den cannot see it,
and the agent's typing may reach the bar; clicking into it is refused.

**While it runs.** Den reads each call before hypruse gets it. Reading its
own windows, and clicking and typing in them, goes ahead, unless a
notification or other overlay covers the point, or the keys are one of your
Hyprland shortcuts (both are refused); anything outside the mission is
refused; in an app marked outward, every click and keystroke
asks you on a card, as do Send-like controls anywhere and the agent's own
`request_approval`. A card shows the reason, the window, the input and a
screenshot of the window, and is answered only in Den's own window, once.
For a step only you do, the agent calls `your_turn`: every desktop tool is
paused until you say it is done (`Ctrl+Y`) or end the mission (`Ctrl+N`). The
header shows `DESKTOP · on` (or `acting`) while a mission runs.

**Stop it.** `/desktop stop` in Den, `/desktop stop` from the dock, or
`den desktop stop` from anywhere: it stops hypruse, the agent and its server
at once and closes the windows Den opened. It works with Den's window hidden,
and with Den not running (from Den's records; the windows then stay). A
Hyprland bind makes it one key; Den never edits your config, so add it
yourself, for example in Lua:

```lua
hl.bind("SUPER + SHIFT + Escape", hl.dsp.exec_cmd("den desktop stop"))
```

**Files.** Each mission has a private folder in
`~/.local/state/den/desktop/<mission>/`: its settings, hypruse's journal
(typed text as length and digest only), screenshot crops for cards, and a log.
The journal, crops and log are deleted ten minutes after the mission ends;
`/desktop keep` keeps them.

**Limits.** Read these before you let it near anything that matters.

- Den's proxy, the agent's only route to hypruse, is the gate: it forwards
  nothing Den did not allow. The agent's hook is a second layer only (a hook
  that fails lets a tool run).
- In an app not marked outward, a click by coordinates is not asked about;
  only Send-like names and the outward rules ask.
- hypruse focuses the window and then types, about 50 ms apart, with no check
  in between.
- A zoom can show a notification drawn over the window.
- The apps you allowed can take the keyboard for a moment when they open;
  Den gives the focus back to your window. (The agent's own terminal opens
  without taking it.)
- Strict mode (refusing when you move the mouse) has not been tried against a
  real takeover; treat stopping as the way to take control back.
- A desktop agent is never restored or reopened by Den. If you resume one by
  hand with `claude --resume` outside Den, it gets an ordinary agent's tools
  on a conversation that read your screen, and Den cannot stop that.
- Everything runs as your user: Den detects changes to its venv and the
  agent's settings, but anything running as you could change them.
- The agent never types credentials: signing in is your turn. Text on the
  screen (a web page, an email) can still try to steer it, which is why
  outward steps ask you.

## Skills for roles

Den ships a small, vetted catalogue of Claude Code skills (SKILL.md folders)
and offers the ones that fit an agent's role. Nothing is installed until you
press `Ctrl+Y` on a question in the inbox that lists each skill with its
repository, pinned commit, license and why it fits; `Ctrl+N` declines.

The catalogue is `internal/skills/catalogue.json`, built into the binary. Every
entry names a GitHub repository, a full commit SHA (never a branch), an
allowed license, its role tags, a one-line reason, what was checked before it
was admitted, and every file with its SHA-256. `den skills catalogue` prints it.
It holds seven skills from five repositories:

| Skill | Roles | Repository @ commit | License |
| --- | --- | --- | --- |
| frontend-design | frontend, design | anthropics/skills @ dbd4588f9e10 | Apache-2.0 |
| test-driven-development | testing, coding | obra/superpowers @ bb92a7774141 | MIT |
| verification-before-completion | coding | obra/superpowers @ bb92a7774141 | MIT |
| diagnosing-bugs | coding | mattpocock/skills @ 49dd158d1076 | MIT |
| vulnerability-triage-brocards | security | trailofbits/skills @ 442fc9d6c89b | CC-BY-SA-4.0 |
| property-based-testing | testing | trailofbits/skills @ 442fc9d6c89b | CC-BY-SA-4.0 |
| ponytail | coding | DietrichGebert/ponytail @ 2961b52fc1fe | MIT |

The writing, devops and research tags have no skills yet. Roles are free text
("coding agent", "ui designer", "sysadmin"), so Den matches them word by word
to the tags coding, frontend, design, writing, testing, security, devops and
research, and offers at most four skills; a role with no known word gets
nothing. `den skills match "<role>"` shows what a role would be offered. A
request that names skills (`skills` on `spawn_agent` or `propose_skills`) can
also have a catalogue skill past those four, if its tags fit the role.

**Two ways in.** `spawn_agent` takes a `role`: Den offers the matching skills
for that agent only and waits for your answer before starting it. On `Ctrl+Y`
they go into a folder of Den's own, `~/.local/state/den/skills/agents/<peer>-<time>/.claude/skills`,
and the agent is started with `claude --add-dir` on it, so no other session
sees them; on `Ctrl+N` the agent starts without them. `propose_skills` offers
a role's skills to a project (an agent already running, or a folder you
name): they go into `<project>/.claude/skills`, where every Claude Code
session in that project finds them. A session that started before that folder
existed needs `/reload-skills`. The orchestrator offers skills when it starts
an agent with a role, or when you ask; a lead can pass a role to the agents it
starts.

**What Den checks at install.** It looks the skills up again in its own
catalogue (a request can only name skills whose tags fit the role), downloads each file
from `raw.githubusercontent.com/<repo>/<commit>/…`, and refuses the skill if
any file's size or SHA-256 differs from the catalogue, if any markdown file's
frontmatter sets `hooks` or `allowed-tools`, if it runs a shell command when
it loads (``!`cmd` `` or a ` ```! ` block), or if SKILL.md names a different
skill. It never replaces a folder that is already there, writes no file
executable, and installs nothing into `~/.claude/skills`. Skills whose folder
carries no license get their repository's license file copied in beside them.
Den runs nothing a skill ships.

**Provenance and removal.** Every install is recorded in
`~/.local/state/den/skills.json`: skill, repository, commit, license, scope,
project or agent, path, date, who approved (your user, in Den) and who asked.
`den skills` lists them. `den skills remove <skill> [<project | peer | path>]`
deletes the files Den installed, keeping any file changed since, removes the
skill's folder once it is empty, and drops the record.

## Overview, projects and missions

The Overview tab, first in the row, shows one project at a time. Each agent is
listed under one project: the project of the live mission it serves (Den put it
on the mission, or it works for an agent Den did), the most recently started
one if several; else the project of the mission Den last put it on; else the
repository its session folder is in. An agent whose folder is in no
repository, with no mission, is in no project. (Earlier versions also
listed an agent on every repository it had edited files in lately; now the
files it edits elsewhere do not list it there.) The strip under the tabs lists
the projects with a count of agents on each. `← →` or `/project <name>` switch,
the pick is remembered, and the Review tab follows it. A folder nobody works in
yet is listed with `/project add <dir>` (its repository root when it is inside
one) and stays listed, empty, until `/project drop`.

A mission is a standing goal: a purpose in one sentence, constraints that ride
on every dispatch, and what done looks like, or "standing" for one that never
ends. Type the goal on the Overview command line (or `/mission <goal>`) and the
orchestrator answers with a proposal: the mission rewritten, done-when,
constraints it inferred, the agents it means to reuse, the team it means to
start (roles as a tree, each with the model it picked), and the shape it chose
with one line of reasoning. `Ctrl+Y` starts it, `Ctrl+N` drops it, typing
refines it, `Esc` keeps it for later. Nothing runs before `Ctrl+Y`. Den adds
one criterion of its own to every mission that is not a schedule: "Shown
working on the human's real setup: what was run, and what the human saw".
Neither the orchestrator nor a template can leave it out. When the
run starts, the orchestrator spawns only the roles that report to it; each lead
starts its own people.

| Shape | Runs as |
| --- | --- |
| `▶ once` | one dispatch to one agent; done when it reports back |
| `↻ loop` | one agent iterates; after each pass Den asks the orchestrator whether the goal is met, and it re-dispatches until it is |
| `⇶ fan-out` | a lead with children in parallel, repeated until the lead says done |
| `⏲ schedule` | Den fires a run on the cadence (`every 30m`, `daily 08:30`, `weekly mon 09:00`, or a five-field cron line); each run is a once or a fan-out |

Den drives the runs: it asks the orchestrator to dispatch, tags the resulting
cards with the mission, notices when they settle, and asks again when the shape
calls for another pass. The orchestrator says where a mission stands only
through its `update_mission` tool, never in its text: Den applies `continue`
and `waiting` at once when you asked it about the mission, and puts `done`,
`failed`, and any update from a turn Den or an agent's report started, to you
as an inbox question (`Ctrl+Y` applies it, `Ctrl+N` leaves the mission as it
is). `Enter` on a mission opens it: the fields, a line per
run with its progress or summary, then the mission's conversation, where the
agents the orchestrator dispatched work inline as dispatch cards, in the order
they were sent, just as in the Orchestrator chat. A dispatch made while you
talk about a finished mission shows there too. `/pause` holds the next run,
`/resume` lets it go, `/stop` ends the mission and keeps its history.

A mission belongs to the project it was made in, and only you move it:
`/mission move <project>` (a project Den lists, picked or added) asks in the
Inbox, showing both project names, and `Ctrl+Y` moves it. Nothing the
orchestrator or an agent says, and no `den` command, changes a mission's
project. Den records which agents it put on each mission (a dispatch to the
agent, or a spawn for the mission) and until when; that record, not what the
agents say, decides where they are listed. `/fleet project` groups the Fleet
by project in the same way, each lead with its people, and `/fleet flat` goes
back. Missions persist in `~/.local/state/den/missions.jsonl`; the project
pick, pinned projects and the Fleet grouping in `overview.json` beside it.

### Templates

A template is a mission's shape and team saved under a name: the purpose with a
blank (`{target}`) for what it is about, done-when, constraints, the shape, and
the roles as a tree, each with its model. Four ship in the binary:

| Template | Shape | Team |
| --- | --- | --- |
| `code-review` | fan-out | a review lead on opus, a code reviewer and a security auditor on sonnet; read-only |
| `bugfix` | loop | a coding agent on sonnet, a code reviewer on opus |
| `research` | fan-out | a research lead on opus, a web researcher and a codebase researcher on sonnet |
| `docs-pass` | once | a technical writer on sonnet, an editor on haiku |

`/template` lists them. `/template code-review ~/code/demo-app` puts the
template's proposal on the Overview at once and asks the orchestrator to adapt it
to the target; it may change the team or the wording, and says what it changed.
A folder as the first word of the target picks that project. It is the usual
proposal card: `Ctrl+Y` starts it, `Ctrl+N` drops it, typing refines it, and
nothing runs before `Ctrl+Y`. The roles keep their words ("security auditor",
"technical writer"), so the vetted skills that fit a role are offered when the
agent is started, for you to approve, as for any other agent.

`/template save <name>` keeps the shape and team of the pending proposal or of
the open or selected mission (a name is lower-case letters, digits and
hyphens; a built-in's name is refused). Yours live in
`~/.config/den/templates.json` (beside `config.toml`, so `DEN_CONFIG` moves it):
a JSON list with the fields above, `name`, `about` and `mission` as the
minimum, which you can edit by hand. An entry with the name of a built-in
replaces it; an entry that is not valid is skipped; a file that does not parse
is reported on the list and never overwritten. Templates are data, with no
conditions or scripts. The orchestrator is told the template names with every
message, so "start a code review of ~/code/demo-app" can point you to one.

## Cost

Den adds up the tokens in each session's transcript, exactly: every assistant
message counts once (Claude Code writes a message once per content block, so
the repeats are skipped), cache reads and writes are kept apart, and a session's
subagents, which Claude Code keeps in files beside the session's own, are shown
as a separate figure. A transcript is read once and then only what was appended.
The format is Claude Code's own and may change; what does not parse is skipped.

Tokens come first. Dollars are an estimate: the same tokens at API list prices
from a small built-in table taken 2026-09-25, written `≈ $1.23`, with a `+` when
a model has no price and its tokens are left out. On a subscription nothing is
billed per token, so read it as what the work would cost at list price. When a
transcript carries Claude Code's own cost record, the details pane shows it as
`reported`, beside the estimate and never added to it. Prices can be overridden
by model-id prefix in `config.toml`, in dollars per million tokens (input,
output, cache write, cache read; the last two are optional):

```toml
[prices]
claude-opus-5-5 = "4, 20, 5, 0.2"
```

The Fleet details pane shows an agent's tokens, subagents, estimate and, for an
agent that directs others, the figures for its whole team. The Overview strip
shows the fleet total: every session Den sees, plus what the orchestrator
reports for itself. With remote hosts the strip adds a split by machine
(`here $1.20 · devbox $0.40`) and `/remote` shows each host's own total. A
remote host sends its tokens by model, never dollars; the estimate uses this
machine's price table. An open mission shows what it used, and each run its own:
the tokens of the agents its dispatches went to, and of everyone they direct,
from the first dispatch until the run ended. A run's figure is kept in
`missions.jsonl` when it ends. `den list -json` gains a `Cost` object on every
agent (`tokens`, `subagent_tokens`, `estimated_usd`, `partial`,
`claude_code_reported_usd`) and a `TeamCost` for agents that direct others.

A mission can have a budget, off unless set: `/budget 2M` (tokens) or
`/budget $5` (estimated dollars) on the open or selected mission, counting all
its runs. When the mission crosses it Den pauses it, as `/pause` does, and asks
once in the inbox: `Ctrl+Y` keeps going past the budget, `Ctrl+N` stops the
mission. Den never stops agents itself; a pause only holds the next run.
Setting a new budget while the question is open replaces it.

## Review

The Review tab shows what each agent changed, as git sees it on disk, with
every changed line coloured by the session that wrote it. It follows the
project picked on Overview.

- **Which files.** From each agent's transcript (its Edit, Write and Bash file
  changes), not from the folder it started in, so an agent started in `~` that
  edits files in several repositories is covered.
- **Which diff.** The working tree against a base: committed, staged and
  unstaged changes, and untracked files that are not ignored. On a feature
  branch the base is where it left the default branch (origin's HEAD, else
  `main` or `master`); on the default branch itself it is the last commit
  before the session started, so work committed straight to main still shows.
  Submodules are not looked into. Untracked files are read without following
  symlinks, at most 1000 of them and 16 MB in all; the rest are listed as
  binary. A file shows under an agent only if it owns a line in it (next item),
  so files the agent never touched stay out of its Review. A file whose name
  has control characters is listed by its cleaned name, which may not be its
  real one, and its content is not shown, here or in the agent's Tail.
- **Per agent, per line.** Each agent gets the files in which it owns at least
  one changed line, with its own added and removed counts. A line belongs to
  the newest agent edit that added (or removed) exactly that text. Lines no
  edit explains stay grey. Two agents in one file are told apart line by line.
- **Syntax highlighting.** Code in the diff is coloured with
  [chroma](https://github.com/alecthomas/chroma), the lexer picked from the
  file name. Each hunk is lexed as the new side and the old side so block
  comments and strings that span lines colour correctly. Changed lines sit on a
  faint green or red tint. Files chroma does not know, and diffs over 256 KB,
  are drawn plain.
- **Comments.** Select a line (`Shift+↑ ↓` or the mouse for a range), type, and
  `Enter` saves a draft comment addressed to the agent that wrote most of those
  lines. `Enter` on an empty line edits the comment under the cursor and
  `/delete` removes it. Comments are kept in `~/.local/state/den/reviews.json`
  (readable only by you) and survive restarts.
- **Sending.** `/send` delivers each agent's drafts as one message into its
  own session, the way `@Name` messages go, held while you are at that
  terminal. Each comment cites its file and lines and quotes them, so the
  agent finds the place even after the lines have moved. `/send <Name>` sends
  one agent's drafts. There is no confirmation step: the message is plain
  text and cannot approve anything. Drafts for an agent whose session has
  ended, and for lines no agent wrote, stay drafts and the right column says
  so; `/send orchestrator` hands them to the orchestrator to pass on. If
  delivery fails the comments stay drafts.
- **Addressed.** A sent comment stays under its line, marked "sent · open",
  until the lines it quotes change on disk. When it is sent Den counts how
  often those lines appear in the file, and whenever the diffs reload it
  counts again: fewer means the agent changed them, and the comment reads
  "sent · addressed". Edits that only move the lines do not count. A comment
  on removed lines counts as addressed once those lines are back.
- **Refresh.** The diffs reload when you enter the tab if they are older than
  2 s, every 15 s while it is open, and on `/rescan`.

Left column: **Recent work**, one entry per agent, and **Files** for the
selected work. Centre: the diff. On wide terminals a right column lists the
comments by recipient, with whether that agent is live: drafts first, then
open (→) and addressed (✓). Narrower than 72 columns the diff is shown alone.

### Worktrees in Review

An agent that works in a git worktree (one Den started with `worktree`, one
you started with `claude --worktree`, or one you made with `git worktree add`)
is shown under the repository the worktree belongs to, not as a project of its
own, and its row names its branch and how many commits it is ahead of the main
checkout's branch. Its diff is still read inside the worktree, on its branch,
from the commit Claude Code made the worktree from when the session recorded
it (else the usual base). Review offers two things for it, next to `/send`
(which sends your comments back to the agent):

- **`/merge`** (or `/merge <Name>`) puts one question in the inbox and moves you
  to it: the branch, the branch it goes into, the commits, the files with their
  line counts, and whether it fast-forwards or needs a merge commit. Nothing is
  merged by asking. Den checks first, with `git merge-tree`, which changes
  nothing: the merge goes into the branch checked out in the repository's main
  checkout, and only if that checkout is on a branch (not a detached HEAD), has
  no staged, unstaged or untracked changes (Claude Code's own
  `.claude/worktrees/` folders do not count) and no merge, rebase or
  cherry-pick in progress, and the two branches combine without conflicts.
  Otherwise it says exactly why and asks nothing; a conflict is listed in the
  question and `Ctrl+Y` refuses it. `Ctrl+Y` checks again (same branch, same
  commits on both sides, still clean) and then merges, as a fast-forward or with
  a merge commit as the question said. If git fails anyway the merge is aborted,
  so the checkout is never left half-merged. If either branch moved since the
  question, it is dropped and `/merge` asks again. `Ctrl+N` drops it. Uncommitted
  changes in the worktree are not carried by a merge, and the question says so.
  The repository's hooks do not run during the merge, and a merge commit Den
  makes is not signed, even if you sign commits by default. If the branches need a
  merge commit and the repository's git config names its own merge driver, Den
  does not run it and asks you to merge yourself.
- **`/cleanup`**: removing the worktree is a second, separate question, asked
  right after a successful merge and any time by `/cleanup`. It is refused while
  an agent is still running in the worktree, and while the worktree has any
  uncommitted or untracked file: Den never removes work that is not committed,
  and never forces a removal. If git still holds the lock Claude Code took, Den
  releases it only when the process named in the lock has ended. `Ctrl+Y` removes
  the worktree and, only if its branch is merged into the main checkout's branch,
  deletes the branch; an unmerged branch stays with its commits. Git deletes the
  worktree's ignored files with it (a `.env`, build output), so the question
  lists them, and asks again if new ones appear before `Ctrl+Y`.

## The agent badge

[agent-badge](https://github.com/GlacierHubAB/agent-badge) is a Claude Code
mod, MIT licensed, that pins a band above each session's prompt with the
agent's name, critter, role, current and earlier tasks, and gauges for context,
the 5-hour and weekly limits. It is a git submodule at `mods/agent-badge` and is
embedded in the Den binary; `den install-mods` copies it to `~/.claude/skills`,
which Claude Code loads without further setup. If a development checkout is
already loaded through `CLAUDE_CODE_PLUGIN_DIRS` the installer says so and
leaves it alone (`--force` installs anyway). Claude Code 2.1.289+ is needed.
Role and task come from a small Haiku call through your own Claude Code session,
billed like any other usage.

The critter name and face are seeded from the session id, so a resumed session
keeps them. Den computes the same names and sprites itself (a port of the
badge's generator in `internal/badge`), so they match with or without the badge;
the badge adds roles, tasks and the gauges (1.1.0+).

Colours follow the current Omarchy theme. Only the theme's real hues are used
for agents, topped up from a built-in set of twelve soft colours to at least ten.
Den gives every live session its own colour, oldest session first, and records
the choice in `~/.local/state/den/colors.json`; the badge (1.2.0+) reads that
file so the critter in each terminal matches the one in Den. If something else
replaces or removes that file, Den writes its record back within seconds. The
same colour marks the agent in the Fleet, inbox, dispatch cards, the map and
Review.

## Remote agents

Den can show the agents on other machines you reach over ssh in the same Fleet,
Inbox and cost totals as the local ones. The full guide is
[docs/REMOTE.md](docs/REMOTE.md); this is the summary.

```
den remote add devbox      # an ~/.ssh/config alias, or user@host
ssh devbox                 # once, by hand, to record the host key
den remote install devbox  # if the host has no den: asks in Den's inbox, ^Y copies it
```

Then start Den: the host's agents carry an `@devbox` chip, and `/remote` lists
the hosts. Each host has a **policy** in `~/.config/den/remotes.toml`
(`default_policy` sets it for new hosts):

| Policy | What the host sends, and what you can do |
| --- | --- |
| `observe` (default) | Metadata only: status, names, role and task (not with `labels = false`), repository name and branch, context, tokens by model. You see the agents, get notified, and see the cost. |
| `approve` | Plus the body of a waiting permission, plan or question. You answer it in the inbox with `Ctrl+Y`, `Ctrl+N` and `/always`; you can also message the host's agents (`@Name@devbox text`, `den send name@devbox …`) and close them (`/close Name@devbox`, `Ctrl+X`). |
| `full` | Plus each agent's title, folder and recent activity, and its diff in Review, read on the host. Held in memory, never written to disk. |

What runs where: Den starts one system `ssh` per host while it runs, and on the
host that session runs `den remote serve --proto 1`, which exits when the
session does. There is no daemon and no network listener; under `approve` and
`full` it also listens on a Unix socket on the host, only while a Den is
attached. Den uses your `~/.ssh/config` and keys, stores no secret, runs every
connection with `BatchMode=yes` and `StrictHostKeyChecking=yes`, forwards
nothing, and never answers an ssh prompt, so you run `ssh <target>` once by
hand. Den never installs itself on a host without your `Ctrl+Y` on the plan in
its inbox, and never installs hooks there: run `den install-hooks` on the
host. A permission it relays fails open: if the link drops or anything is
doubtful, the terminal on the host asks, as if Den were not running.

An unreachable host is one row, "devbox · offline since 14:02 · 3 agents (last
known)", its agents dimmed and never counted as working or needing you. A
silent link is noticed in 45 to 60 seconds. `den list -json` carries a `Host`
field (and `Stale` when the link is down), `den events -json` a `host` field,
and `[on]` commands get `DEN_HOST`.

Limits today: Linux hosts and Claude Code agents only (no macOS, no Codex);
Den cannot spawn agents on a host, missions cannot use remote agents, and the
orchestrator lists them but cannot message or close them; `Enter` and `/open`
cannot raise a terminal on another machine, they print `ssh -t <target>`;
a lowered policy in `remotes.toml` applies within seconds, and a raised one or a
new target on `/remote connect`; disabling or removing a host ends its link at
once, and adding one is picked up when Den restarts. Every string a host
sends is sanitised before Den shows it (see
[Text from outside Den](#text-from-outside-den)).

## Text from outside Den

Agents, transcripts, hook payloads, git and remote hosts give Den text it does
not control. Printed raw, a command's output or an agent's name could carry
terminal escape sequences that set your clipboard (OSC 52), clear the screen,
retitle the window, plant a link or reorder what you read with bidirectional
overrides. Den removes from all such text, local and remote alike, before it is
shown, put in a notification or window title, logged or printed by a command:
escape sequences (CSI, OSC, DCS, APC and the like), other control characters
except newline and tab, bidirectional overrides and invalid UTF-8. A carriage
return becomes a newline. Ordinary Unicode, such as CJK, emoji and accents,
stays. Notification bodies also escape markup, so a name cannot become a link
or an image.

The change you will see: colour in a command's output (a test run, `git
diff`'s own colouring in a Bash result) is no longer drawn in Den. The text is
there, without its colours; Den's own colours are not affected.

### Other safety changes, for local agents too

- **The inbox lists flags that change a call.** A Bash call with
  `dangerouslyDisableSandbox`, an Edit with `replace_all`, a MultiEdit's edits
  and the like are shown on the request, not hidden behind the command.
- **Den's socket.** With `XDG_RUNTIME_DIR` unset, `den.sock` is now
  `den-<uid>/den.sock` under `$TMPDIR`, a directory only you may use, checked at
  each use; it used to be `/tmp/den-<uid>.sock`. Every connection to it, in
  either direction, checks that the peer is the same user, and sends one line
  of at most 1 MB; at most 256 requests wait for an answer at once. A request
  past either limit gets no decision from Den, and Claude Code's own dialog
  answers it. (`den hook` drops the tool input and output from an event Den
  only follows when it would not fit.) On a machine without
  `XDG_RUNTIME_DIR`, reinstall `den` and run `den install-hooks` again so the
  hooks and Den agree on the path.
- **A hook that crashes fails open.** `den hook` exits 0 with no decision, so
  the request stays with Claude Code's own dialog, which is on screen from
  the start, instead of exiting 2, which Claude Code reads as a denial.
- **Git reads of an agent's checkout run nothing the repository names.** Review,
  the uncommitted-changes check and worktree reads turn off fsmonitor, filter
  drivers, merge drivers, external diff, textconv and lazy fetch, never look
  into submodules (whose own config could name a filter), and do not inherit
  git's own environment variables, so a repository's config cannot make Den
  run a program. Merging and removing a worktree run none of the repository's
  hooks or merge drivers either; filter drivers still run there, as a checkout
  needs them (Git LFS is one).

## Usage

The header shows how much of the account's weekly limit is used, as Claude
Code reports it to the agent-badge statusline in your sessions on this
machine: the newest reading and how old it is (`usage 63% · 2m ago`). Once
it has had one, no reading in the last 15 minutes shows as `usage unknown`;
before Den ever had a figure, a warning or a stop, the header says nothing
about usage. Two stand-ins, always
labelled estimates: a figure you set (`/usage set 87`, good until the next
weekly reset; `/usage clear` drops it), or, with `weekly_usd` set under
`[usage]`, Den's own cost estimate for the week against that budget.

At 80% and again at 90% Den puts a card in the inbox and sends a desktop
notification, once each per weekly reset. Nothing stops on its own unless you
set `stop_percent` under `[usage]` (off by default). Then, at that share,
or at once with `/usage stop`, Den sends `USAGE STOP` to every agent on this
machine, and to agents on hosts whose policy lets Den act on them (the others
are listed as not reachable for stop). On this machine it arrives framed as
Den enforcing your rule: an automatic message from Den, binding because the
stop is yours, naming the share you set, and never claiming you typed it. On
a host it still arrives as that host's Den delivers any message. The header turns red (`usage stop`), and no agent is started (by the
orchestrator or by a lead) and no mission run goes until you let the fleet go
on: `/usage resume` puts the question in the inbox, and only your `Ctrl+Y`
there answers it. Den's cost estimate never triggers a stop; Claude Code's
figure or yours does.

## Configuration

Everything has a default, so the file is optional. `den config init` writes
this starting point to `~/.config/den/config.toml` (`$XDG_CONFIG_HOME/den/` when
set; `DEN_CONFIG` overrides the path):

```toml
[orchestrator]
model = "sonnet"            # any claude --model value, or "off" for Claude Code's default
name = "den-orchestrator"   # how the orchestrator session names itself to peers
spawn = true                # may open terminals and start agents on the workspace you are on
guidance = ""               # off; "i-have-adhd" or /abs/path/to/your.md: text that shapes its answers, at most
                            # 8192 bytes after frontmatter, loaded only once you approve its SHA-256 in the inbox

[agents]
permission_mode = "auto"    # the mode agents Den starts run in: auto, acceptEdits, plan, default or dontAsk;
                            # /mode in Den changes it, and a change to a mode that asks less waits for your ^Y

[inbox]
history = 100               # finished turns kept as history

[detail]
recent = 20                 # activity entries shown in the details pane

[hooks]
permission_wait = 580       # seconds Den may hold a permission; the terminal dialog then answers alone
log = true                  # append every hook run to ~/.local/state/den/hooks.log
answer_questions = false    # answer an agent's questions (AskUserQuestion) from the inbox, see Inbox

[restore]
ask = true                  # after a restart, ask once whether to reopen the agents that were live

[notify]
needs_you = true            # desktop notification when a permission, plan or question waits on you
remind = 10                 # minutes before one more for something still waiting; 0 never
done = false                # when the top agent of a branch finishes; its workers stay quiet
failed = false              # when a turn ends on an API error or tool calls keep failing
stalled = false             # when an agent has gone quiet while working
without_den = false         # let den hook notify needs-you itself while Den is not running

[stuck]
stalled = 20                # minutes working with no hook event before the inbox says stalled; 0 off
tool_failures = 3           # tool calls failing in a row before the inbox says so; 0 off

[events]
log = true                  # den hook appends metadata-only events to ~/.local/state/den/events.jsonl

[team]
max_agents = 3              # live agents per repository before spawn_agent needs a stated reason (the orchestrator not counted)
branch_hours = 24           # nudge an agent whose branch is older than this; 0 off
behind_max = 30             # nudge an agent whose branch is this many commits behind dev, main or master; 0 off
idle_minutes = 30           # an agent on a mission with no turn for this long and nothing open is idle to the orchestrator; 0 off

[api]
enabled = false             # den-api.sock for apps on this machine; turn it on with /api on in Den
ask_budget_usd = 2          # what apps' questions to the read-only orchestrator may spend a day
console_powers = "unchanged" # the dock pairs by itself; "unchanged": with its powers only as Den last gave them in this run, "always": whatever changed

# A shell command per event kind, run by Den with the event's metadata as
# DEN_* variables and as JSON on stdin. Nothing runs unless you add one.
# Kinds: session_start session_end turn_start done needs_you idle
# turn_failed tool_failed stalled still_waiting. See docs/EVENTS.md.
[on]
# needs_you = "ntfy publish my-den-topic \"$DEN_AGENT needs you\""

# [prices]                  # override the cost estimate's price table, see Cost
# claude-opus-5-5 = "4, 20, 5, 0.2"
```

Mission templates are in `templates.json` beside it (see Templates), and the
hosts of [Remote agents](#remote-agents) in `remotes.toml` (`default_policy`,
and per host `target`, `policy`, `labels`, `den_path`, `enabled`).

`DEN_ORCHESTRATOR_MODEL`, `DEN_ORCHESTRATOR_SPAWN=off`, `DEN_PERMISSION_WAIT`,
`DEN_HOOK_LOG=off`, `DEN_RESTORE=off` and `DEN_NOTIFY=off` win over the file.
`DEN_SOCKET` moves the socket the hooks talk to (default
`$XDG_RUNTIME_DIR/den.sock`; without `XDG_RUNTIME_DIR`, `den.sock` in
`$TMPDIR/den-<uid>/`, a directory Den makes with mode 0700 and refuses to use
if it is a symlink, another user's, or open to others). Den and the hooks only
talk to processes of the same user on that socket.

## Files Den writes

State lives under `$XDG_STATE_HOME/den/` (`~/.local/state/den/` by default):

| File | Holds |
| --- | --- |
| `hooks.log` | one line per hook run: event, session, delivered or not; failed `[on]` commands |
| `events.jsonl` | the event stream for scripts, metadata only ([docs/EVENTS.md](docs/EVENTS.md)); moves to `events.jsonl.1` past 4 MB |
| `history.jsonl` | finished turns |
| `orchestrator.jsonl` | the orchestrator chat and card states |
| `edges.jsonl` | who directs whom |
| `missions.jsonl` | missions, their runs, each run's tokens and estimate, and budgets |
| `roster.json` | the agents Den has seen live in the last two weeks, to reopen them |
| `protected.json` | agents a close leaves alone, folders that protect their agents, and flagged agents with their notes |
| `skills.json` | every skill Den installed: where from, where to, when, who approved |
| `skills/agents/` | skills installed for one agent only |
| `overview.json` | the picked and pinned projects, and whether the Fleet is grouped by project |
| `reviews.json` | Review comments |
| `guidance.json` | the SHA-256 of the orchestrator guidance you approved, nothing else |
| `api-chat.jsonl` | apps' questions to the read-only orchestrator and its replies, by ask session (den-api.sock) |
| `api-lane/` | the read-only orchestrator's own empty working folder |
| `api-spend.json` | what apps' questions spent today, against `api.ask_budget_usd` |
| `colors.json` | the colour of each session |
| `remote/<label>.json` | per remote host: its state, the agents it last reported (metadata only) and where its event stream was read up to; readable only by you |
| `remote/controller` | a random id that tells a host which Den is attached (an identity marker, not a secret) |
| `attachments/` | images pasted from the clipboard |

Templates you save go to `~/.config/den/templates.json`, and the remote hosts
you add to `~/.config/den/remotes.toml`, both beside `config.toml`.
Den also edits `~/.claude/settings.json` (hooks, with a backup beside it) and
`~/.claude/skills` (the badge mod), writes `<project>/.claude/skills` only
for skills you approved for that project, and listens on a Unix socket in
`$XDG_RUNTIME_DIR`. It reads `~/.claude/sessions/` and Claude Code's transcripts
under `~/.claude/projects/`; it does not write to those two.

## Troubleshooting

- **No agents listed.** Den needs Claude Code 2.1.289+; older versions do not
  write `~/.claude/sessions`. Press `/rescan`, or run `den list` to see what
  Den sees and `den explain <name>` for how it arrived at a name or status.
- **Nothing reaches the inbox.** Hooks apply to sessions started after
  `den install-hooks`. Check `~/.local/state/den/hooks.log`; a line ending in
  `no den listening` means the hook ran but Den was not running.
- **Enter does not focus a terminal.** Focus needs Hyprland (`hyprctl`) and
  finds the window by title or working directory; `den explain <name>` says how
  it matched, or why not.
- **Spawn fails.** Install `xdg-terminal-exec` or set `$TERMINAL`; `claude`
  must be on `PATH`.
- **A remote host shows "needs you" or "version mismatch".** See the table in
  [Remote agents](#remote-agents); `den remote status <label>` repeats the checks
  and says what to fix.
- **Orchestrator costs.** It is a real Claude Code session; lower the cost with
  `model = "haiku"` or turn spawning off.

## Den, Herdr and OpenRig

[Herdr](https://github.com/herdrdev/herdr) and
[OpenRig](https://github.com/mvschwarz/openrig) are open-source tools for running
many coding agents too. They solve different parts of the problem, and Den can
sit next to Herdr rather than replace it.

| | Den | Herdr | OpenRig |
| --- | --- | --- | --- |
| What it is | A dashboard and control room for the Claude Code sessions already running on your machine | A terminal multiplexer for agents: it owns their terminals in a background server | A daemon that runs agent teams defined in YAML, on tmux |
| Agents | Claude Code | Claude Code, Codex, Cursor, OpenCode and others | Claude Code, Codex and Pi |
| Where agents run | In your own terminal windows; Den reads `~/.claude/sessions` | In Herdr's panes; detach and reattach, also over SSH | In tmux sessions OpenRig starts, or Herdr when installed |
| Platforms | Linux (Omarchy/Hyprland for window placement) | Linux, macOS, Windows | macOS, Linux, WSL2 |
| License | MIT | Apache-2.0 | Apache-2.0 |

What Den does that is its own:

- **One approval inbox.** Permission requests, plan reviews, questions and
  finished turns from every session land in one list; `^Y` / `^N` answer a
  waiting permission or plan without switching windows, and with
  `hooks.answer_questions` on, an agent's question is answered there too.
- **An orchestrator you talk to,** a Claude Code session that can message,
  spawn and staff agents for you. (OpenRig also lets you talk to a lead agent.)
- **Missions:** standing goals run once, in a loop, fanned out or on a schedule.
- **Who directs whom,** drawn as a tree from the messages agents send each other.
- **Review:** every agent's changes as a highlighted diff, each line in the
  colour of the agent that wrote it, with comments you send back to the agent
  and see marked addressed once the lines change.
- **Cost:** tokens rolled up per team and per mission run, an estimate in
  dollars at list price, and an optional budget on a mission. (OpenRig also
  counts tokens per seat.)
- **Attention:** a desktop notification when something waits on you, with a
  click that focuses the agent's terminal, silence for workers that finish for
  a lead, and inbox items for failed and stalled agents. (Herdr also sends OS
  notifications.)
- **Events for scripts:** `den wait`, `den send --wait`, a versioned
  `den events -json` and `[on]` commands per event kind, with metadata only.
  (Herdr has a socket API and `agent prompt --wait`.)
- **A worktree per agent:** `spawn_agent` can start an agent in its own git
  worktree, and Review asks before it merges the branch and, separately,
  before it removes the worktree. (Herdr also makes worktrees first-class.)
- **Bringing agents back:** after a reboot or a closed Den, one inbox question
  reopens the agents that were live with `claude --resume`, and puts them back
  in their tree and mission; `/reopen <name>` does it for one agent. (Herdr
  and OpenRig also restore agents after a restart.)
- **Mission templates:** four built in (code review, bugfix, research, docs
  pass), and your own saved from a mission; the orchestrator proposes and you
  confirm. (OpenRig also ships built-in teams and shareable bundles.)
- **The agent badge:** a name, critter, role and task for each session.
- **One Fleet across your machines,** over plain ssh with no daemon and no
  listener: agents on other hosts, their requests and their cost beside the
  local ones, metadata only by default; per host you can let Den answer their
  permissions, message and close them, and review their work. (Herdr and
  agent-deck also connect machines.)

Where the others go further: Herdr keeps agents running when you close the
client or lose SSH. Den restores conversations, the tree and missions after a
restart with one question, but it does not keep processes alive. Herdr also
works across several machines with full control of the panes there, where Den
shows, answers, messages, closes and reviews remote agents but never types into
their terminals; it supports many agent CLIs and runs on
macOS and Windows. OpenRig gives agent teams persistent roles,
addresses and a work queue, and runs Claude Code and Codex side by side.

Den works with Herdr: it notices a Claude Code session running inside a Herdr
pane and, on focus, asks Herdr to bring that pane forward
(`internal/fleet/fleet.go`). Herdr and OpenRig are their authors' projects;
the descriptions above come from their READMEs as of October 2026.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) and [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md).

## License

MIT, see [LICENSE](LICENSE). The bundled agent-badge mod
(`mods/agent-badge`) is MIT licensed under its own `LICENSE`, and Den's name
generator in `internal/badge` is ported from it.

[THIRD_PARTY_LICENSES](THIRD_PARTY_LICENSES) holds the license of every Go
module built into the binary, and says where the catalogue's skills come from
and under which license: Den embeds only its own catalogue and downloads a
skill, at its pinned commit, when you approve it. Releases ship the file next
to the binaries. `go run ./internal/tools/thirdparty` regenerates its Go
module section, and a test fails when that section no longer matches the
build.
