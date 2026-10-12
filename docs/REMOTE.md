# Remote agents

Den can show the Claude Code agents running on other machines you reach over
ssh, in the same Fleet, Inbox and cost totals as the agents on this machine.
Nothing new runs permanently anywhere: Den starts one `ssh` session per host
while it runs, and a small helper on the host lives exactly as long as that
session.

Use it when you keep agents on a dev server, a GPU box or a build machine and
want to see from one screen who is working, who is waiting on you, and what the
work costs. Remote hosts are Linux machines that you can already `ssh` to with
a key, like this machine.

What you can do with a host:

- see its agents in the Fleet, each with an `@devbox` chip, role, task, branch
  and context use.
- get a desktop notification when one waits on you.
- see what they cost, per machine and in the total.
- keep working when a host drops: it shows as offline, with its last known
  agents dimmed.
- on a host you set to `approve`, answer its waiting permissions, plans and
  questions in the inbox, as you do for local agents.
- on an `approve` host, message its agents and close them.
- on a host you set to `full`, also read their recent activity and review what
  they changed.

## Requirements

- **This machine:** Den as for any use, and the system `ssh` and `scp`. Den
  uses your `~/.ssh/config`, agent, keys and `known_hosts`; it stores no
  password or key of its own.
- **Each host:** Linux, Claude Code 2.1.289 or newer, and `den` installed with
  `den remote serve` in it. Den can copy itself there for you (below). Den's
  hooks must be installed on the host (`den install-hooks`, run there once) for
  it to report events and requests; without them you see its agents but no
  events.
- **Key-based ssh that works without a prompt.** Den never answers an ssh
  prompt, so a passphrase-protected key needs to be in your ssh-agent, and a
  host needs its key accepted already.
- macOS and Windows hosts, and agents other than Claude Code (Codex, for one),
  are not supported.

## Quick start

1. **Add the host.** Use an `~/.ssh/config` alias or `user@host`:

   ```
   den remote add devbox
   ```

   Den runs read-only checks over ssh and prints what it found: the system,
   whether `den` and `claude` are there, and whether Den's hooks are installed.
   `--label gpu` names it differently, `--den /opt/den/bin/den` says where `den`
   is, and `--skip-checks` adds the host without connecting. The host goes into
   `~/.config/den/remotes.toml`.

2. **Trust the host key once, by hand.** Den's connections never prompt, and
   they refuse an unknown host key. In a terminal, run:

   ```
   ssh devbox
   ```

   Answer the prompt, so ssh records the key, then log out.

3. **Install den on the host, if it needs it.** If the checks found no `den`
   or an older one, `den remote add` (or `den remote install devbox` later)
   asks the running Den, which puts one question in the inbox with the whole
   plan. Nothing is copied until you press `Ctrl+Y` there. See
   [Installing den on a host](#installing-den-on-a-host). Then run
   `den install-hooks` on the host, once.

4. **Start Den** (or restart it: it reads `remotes.toml` when it starts). The
   host's agents appear in the Fleet with an `@devbox` chip after their name.
   `/remote` lists the hosts and their state.

If a host is unreachable, Den shows it as one row with the reason, keeps its
last known agents dimmed, and counts none of them as working or needing you.

## The commands

```
den remote add <target> [--label L] [--policy P] [--den PATH] [--skip-checks]
den remote list [-json]       label, target, policy, state, agents, last seen
den remote status <label>     the checks again, plus a read-only handshake with the host
den remote install <label>    put den on the host: asks in Den's inbox
den remote remove <label>     also disable, enable
den remote serve --proto 1    run by Den on the host over ssh; not for humans
```

`--policy` sets the host's policy for the new host (see [Policies](#policies));
without it, `default_policy` in `remotes.toml` applies.

In the TUI, `/remote` lists the hosts with state, policy, agent count, cost,
the last message, versions and the clock; `/remote connect <label>` retries a
host now. `disable`, `enable` and `remove` take effect when Den restarts.

Addressing a remote agent: `@Name@devbox text` messages it, `/close Name@devbox`
closes it, and from a script `den send name@devbox "text"`. A bare `@Name` or
`den send name` means an agent on this machine first.

## remotes.toml

`~/.config/den/remotes.toml` sits beside `config.toml`, so `DEN_CONFIG` moves
it. It holds no secrets and you can edit it by hand; `den remote` rewrites it
and keeps keys it does not know, but not comments.

```toml
default_policy = "observe"   # for den remote add without --policy; absent means observe

[hosts.devbox]
target = "devbox"            # an ~/.ssh/config alias, or user@host
policy = "observe"           # what the host may send, see Policies
labels = true                # false withholds the badge role and task
den_path = "/opt/den/bin/den" # optional; else found through a login shell and ~/.local/bin
enabled = true
```

A label is lower-case letters, digits and `-`, and `local` is taken. A target
that starts with `-` is refused. Den reads the file when it starts. A file that
does not parse connects no host, and `/remote` says why. There is no command to
change a host's policy: edit `policy` and restart Den.
`policy` and `default_policy` take `observe` or `approve`.
They also take `full`.

## Policies

Each host has a policy, which is how much it may send you. The default is the
strictest. The host's own `den remote serve` filters what it sends, and Den
checks it again on arrival and drops, and flags the host for, anything its
policy does not allow.

| Policy | What crosses the wire |
| --- | --- |
| `observe` (default) | Metadata only. Per agent: status, critter name, session id, peer name, badge role and task (at most 80 characters, withheld with `labels = false`), model, Claude Code version, repository name and branch, context and usage figures, tokens by model, and how long ago things happened. Per event: kind, session, agent name, time, request kind, tool name, error type. |
| `approve` | Everything under `observe`, plus, for a waiting permission, plan or question only, what Den shows to decide it: the command, the edit or file, the plan, the question and its options. Your answers travel the other way. |
| `full` | Everything under `approve`, plus each agent's title, its folder on the host and its recent activity (prompts, replies, tool calls and their output, each capped), and, when you open Review, the diff of the session's own work in its checkout, sanitised and capped. |

Under `observe` a host never sends prompts, replies, transcripts, commands,
tool input, file paths, working directories, window titles or recent activity.
Under `approve` that stays true except for the body of a waiting request.
Text you send to an agent and a request to close one also travel to the host.
Only `full` sends content, so set it for hosts whose work you are willing to
read on this machine. Content from a `full` host is held in memory while Den
runs and never written down on this machine: not to `remote/<label>.json`,
`reviews.json`, `den list -json` or the orchestrator's roster.

Badge role and task are short summaries Claude Code's own model writes about
the work, so they say a little about it. Set `labels = false` on a sensitive
host to keep them back.

## What it looks like

- **Fleet.** A remote agent has an `@devbox` chip after its name and the same
  role, task, branch and context figures as a local one. Its critter name is
  computed here from its session id; a host cannot choose it. The details pane
  adds the host, the link, the policy, the host's Claude Code version, and the
  clock offset when it is over 2 seconds (ages are always the host's own,
  never compared with this machine's clock). `Enter` and `/open` cannot raise
  a window on another machine: they say `ssh -t <target>`.
- **Inbox.** A remote permission, plan or question is an item with the host
  chip. On an `observe` host it says "answer on devbox": Den never held the
  request, so `Ctrl+Y` and `Ctrl+N` explain where to answer, and the terminal on
  the host shows Claude Code's own dialog as if Den were not running. Finished
  turns, failed and stalled show as for local agents.
- **Notifications** read "Mossy Lemur on devbox needs your permission".
- **Cost.** A host sends tokens by model, never dollars. Den prices them with
  its own table, and the Overview strip splits the total by machine
  (`here $1.20 · devbox $0.40`); `/remote` shows each host's total.
- **Overview.** A project is a repository on one host: the same repository on
  devbox is its own project, `den @devbox`.
- **The orchestrator** lists remote agents in its roster as host-tagged lines
  that say `SendMessage` cannot reach them. It does not act on remote agents.
- **Details pane, `full` hosts.** It also shows the agent's folder on the host,
  its title and its recent activity.
- **Inbox, `approve` hosts.** The item shows the host chip, "on devbox" in its
  title and the whole body Den was sent. `Ctrl+Y`, `Ctrl+N` and `/always`
  answer it exactly as for a local agent; `/always` allows that tool again for
  that agent on that host only, and is kept on this machine. There is no extra
  confirmation: the host chip, "on devbox" and the full body are the guard. On
  an `observe` host, `Ctrl+Y` says how to change that.

### Hosts that are not live

A host that is connecting, offline, needs you or has a version mismatch is one
row in the Fleet: "devbox · offline since 14:02 · 3 agents (last known)", with
the reason and what to do under it. Its agents stay in the list, dimmed, as
last known. They are never counted as working or needing you.

| State | Means | Den |
| --- | --- | --- |
| offline | ssh ended or failed | retries after 5 s, doubling up to 2 minutes |
| needs you | unknown or changed host key, or ssh cannot log in without a prompt | stops; run `ssh <target>`, then `/remote connect devbox` |
| version mismatch | `den` is missing on the host, or too old for `remote serve` | stops; `den remote install devbox` |
| live, controlled elsewhere | another Den holds the host's requests | shows the host's agents; answers and acts on nothing there; asks again every minute |
| disabled | the host flooded Den with events (see Security model) | stops until `/remote connect devbox`, or until Den restarts |

A link that goes silent is noticed in 45 to 60 seconds; a killed ssh at once.
Events that happened while the link was down arrive once it is back, once each.
Items waiting from the host read "released to the terminal on devbox (link
lost)", and the terminal's own dialog shows: a request is never left looking
answerable.

## Installing den on a host

`den remote install devbox` (and `den remote add`, when it finds no `den` or an
older one on the host) asks the running Den, which puts one question in the
inbox with the whole plan:

1. copy this binary to `~/.local/bin` on the host with `scp`, under a
   temporary name,
2. check its sha256 there, deleting it and stopping on a mismatch,
3. move it to `~/.local/bin/den`.

Nothing happens before you press `Ctrl+Y`. There is no flag to skip the
question, Den refuses a request that did not come from this machine, and a
background connection never installs or updates anything. With no Den running,
the command prints the plan and changes nothing. If the host's architecture
differs from this machine's, Den cannot copy; it prints the release URL and
the `sha256sum` check to run by hand. A host whose `den` is not known to be
older is left alone. Den never installs hooks on a host: run
`den install-hooks` there yourself.

## Remote approvals

On an `approve` host, `den remote serve` listens on a Unix socket on the host,
`den-remote.sock`, only while a Den is attached. On the host, `den hook` asks a
Den running there first, as always, and only if none is, that socket. So a
human at the server who runs Den there keeps control of that machine's
requests.

A waiting permission, plan or question travels to your inbox with a random id.
You answer; the answer goes back down the same ssh session; the hook on the
host returns it to Claude Code. The terminal there stays usable the whole time,
and whichever answer comes first counts. If it was answered or dismissed on the
host, the item reads "no longer waiting on devbox: answered or dismissed there".

**It fails open.** If the link drops, Den is not running, or anything is
doubtful, the hook on the host returns no decision and Claude Code asks in its
own terminal, as if Den did not exist. A request is sent to the terminal rather
than to you when it cannot be shown whole: a command or path over 16 KB, or one
that changes when sanitised, an input with a key Den does not know, a request
over 1 MB, or more than 32 held at once on one host.

A question (`AskUserQuestion`) is relayed only if the host's own config has
`hooks.answer_questions` on, because the hook on the host applies its own
config.

**One Den per host.** Only one Den controls a host's requests. A second Den
attached to the same host sees "another Den controls devbox, this one only
observes it".

The socket is in `$XDG_RUNTIME_DIR`, else in `den-<uid>/` under `$TMPDIR` (a
directory only that user may use, checked); `DEN_REMOTE_SOCKET` moves it. It is
mode 0600 and `serve` refuses a connection from another user. `serve` keeps a
metadata-only log of what it did with requests, never a command or text, in
`remote-serve.log` in the state directory on the host.

## Sending and closing

On an `approve` host:

- **Messaging.** `@Name@devbox text` messages an agent on a host (`Tab`
  completes to "Name @devbox"). `@Name@local` means the one on this machine
  when both have that name, and a bare `@Name` prefers agents on this machine.
  From a script, `den send name@devbox "text"` goes through the running Den; it
  exits 1 when Den is not attached, and `--wait` is not supported for remote
  agents. `den send name@host` needs an exact name, a peer name or a unique
  prefix on that host, and lists the candidates otherwise. The text goes down
  the ssh session to the agent's inbox on the host, never in an argument list.
  Sending needs no confirmation, as for local agents. A message too large once
  encoded says so, and the host refuses a messaging socket that is a symlink
  or not the agent's own. A host that is offline gets nothing: "devbox is
  offline; nothing was sent".
- **Closing.** `Ctrl+X`, `/close`, `/close Name@devbox` and `/close idle`
  include remote agents. The question names the host: "Close Mossy Lemur on
  devbox?", "on devbox · SIGTERM there, no window", and "! uncommitted changes
  in its checkout on devbox". Every close needs `Ctrl+Y`. `/protect` and
  `/flag` apply to remote agents too: `/protect Name@devbox` keeps that
  host's agent out of every close, by name or not, and a bare
  `/protect <Name>` covers every agent of that name, here and on every host.
  A close of a protected agent is refused with the `/unprotect` that allows
  it. A protected folder in `protected.json` only protects agents on this
  machine.

  On the host, `serve` closes only a live Claude Code session, refuses a
  working agent without `--force`, sends SIGTERM and only with `--force`
  SIGKILL. Before it signals, it checks that the pid is still that session's
  Claude Code (its name or executable, and a start time that fits) and signals
  through a pidfd. If it cannot be sure (no pidfd, an unknown start time) it
  refuses, and a forced close never escalates to SIGKILL after the link
  dropped: the result is "not known".

The orchestrator cannot message or close remote agents.
A `full` host does all of this too: it is `approve` plus content.

## Review on a `full` host

Remote agents on a `full` host are listed in Review with their `@host` chip.
Den asks the host for each diff, at most 2 at a time per host, every agent in
turn; an agent whose diff fails is asked again after a wait that grows from
15 s to 5 minutes, so it never holds up the others. The host reads
it in the agent's checkout, with the session's own work marked, as Review does
locally. While it waits, the row says "reading its diff on devbox…", and a
failure shows its reason. If none of the changes in the checkout are the
agent's, Review says so and shows nothing. A diff over 200 KB, 300 files or
2000 lines in a file is cut and says what was left out. A file whose name has
control characters is listed by its cleaned name, without its content.

When the host goes offline its diffs disappear ("devbox is offline"); they are
read again when it is back. Below `full` the agent is greyed with "diff on
devbox, policy observe; to review it here, set policy = "full" for it in
remotes.toml".

Review comments `/send` through the host like `@` messages. `/merge` and
`/cleanup` refuse remote work. Comments on remote work last until Den exits and
are not saved.

## For scripts

`den list` and `den list -json` include remote agents as the running Den last
saw them, with a `Host` field, and `Stale` and `HostState` when the link is
not live now (`last known:` in front of the status in the table). The JSON
carries the same metadata as the Fleet. `den events -json` carries a `host`
field on remote events while Den runs, and `[on]` commands for remote events
get `DEN_HOST` set to the host's label (see [EVENTS.md](EVENTS.md)); they run
on this machine.

## Limits

- Linux hosts only; agents are Claude Code only.
- Remote agents cannot be spawned or restored from Den, missions cannot use
  them, and the orchestrator cannot message or close them.
- `Enter` and `/open` cannot raise a terminal on another machine.
- Den does not keep remote agents alive: if the link drops, they carry on on
  the host, and Den shows them as last known until it is back.
- A silent link is noticed in 45 to 60 seconds.

## Troubleshooting

`den remote status devbox` repeats the checks and says what to fix.

- **"needs you": host key unknown or changed.** Den refuses it and does not
  retry. If the host is new, run `ssh devbox` once in a terminal and accept
  the key. If it changed unexpectedly, find out why before you accept it. Then
  `/remote connect devbox`.
- **"needs you": ssh cannot log in.** Den never answers a prompt. Make the
  login work without one: load the key into your ssh-agent, or fix the
  account, then `/remote connect devbox`.
- **Offline.** Den retries, with a wait that doubles from 5 seconds up to 2
  minutes. `/remote connect devbox` retries now; `/remote` shows the reason.
- **"den is not installed on the host", or `den` not found.** A non-login ssh
  command has a short `PATH` that may lack `~/.local/bin`. Den looks through a
  login shell and `~/.local/bin`; if `den` lives elsewhere, set `den_path` for
  the host in `remotes.toml` (an absolute path, or one starting with `~/`), or
  run `den remote install devbox`.
- **Version mismatch.** `den` on the host is missing or too old for
  `remote serve`: `den remote install devbox`, or install a matching release
  there by hand. Den connects again by itself once an install approved in its
  inbox succeeds; after an install by hand, `/remote connect devbox`.
- **Agents appear but no events or requests.** Den's hooks are not installed on
  the host: run `den install-hooks` there. Only sessions started afterwards
  report.
- **"another Den controls devbox, this one only observes it".** Another Den
  (on another machine, or the same one in another session) holds the host's
  requests. Quit that one, or use it; this one asks again every minute.
- **A request went to the terminal on the host.** It could not be shown whole
  (see Remote approvals), or the link was down. The terminal's own dialog
  works as usual.
- **No `XDG_RUNTIME_DIR` on the host.** The socket then lives in
  `den-<uid>/` under `$TMPDIR`, which must be your own directory with mode
  0700. If it is not, `serve` applies `observe` for the host, and `/remote` shows
  "the host applies observe".
  `DEN_REMOTE_SOCKET` names another path. The same applies on this machine to
  `den.sock`: without `XDG_RUNTIME_DIR` it is in `den-<uid>/` under `$TMPDIR`,
  so reinstall `den` and run `den install-hooks` again so the hooks and Den
  agree.
- **"devbox is offline; nothing was sent".** The host is not live. Nothing
  queues: send again when it is back.
- **"disabled", "flooding".** The host sent far more events than its agents
  could, or events for sessions it does not list: a broken or compromised
  `den` there. Find out why on the host, then `/remote connect devbox`.
  `/remote` shows how many events and inbox items Den dropped.

## Security model

- **Your ssh, nothing else.** Den uses the system `ssh` with your config and
  keys, and stores no password, token or key. Every connection runs with
  `BatchMode=yes`, `StrictHostKeyChecking=yes` and `ConnectTimeout=10`, and Den
  never passes `accept-new`, `StrictHostKeyChecking=no` or a disabled
  `known_hosts`. An unknown or changed host key stops the host.
- **Nothing forwarded.** `ForwardAgent=no`, `ForwardX11=no`,
  `ClearAllForwardings=yes`, `PermitLocalCommand=no`. The target is checked
  (one starting with `-` is refused) and goes to `ssh` after `--`, and the
  command on the host is a fixed string. ControlMaster sockets live in
  `$XDG_RUNTIME_DIR/den/ssh` (mode 0700); a master ends with its last session.
- **No daemon, no network listener.** `den remote serve` is a child of the ssh
  session Den started and exits when it ends, or if its parent is gone.
  Under `observe` it opens no socket; under `approve` it listens only on a
  Unix socket on the host, mode 0600, while a Den is attached.
- **A host cannot reach this Den.** What a host sends never touches Den's own
  socket, so it cannot ask this Den to close, spawn or install anything. A host
  speaks a closed set of messages: an unknown type is dropped and the host
  flagged, and a line over 256 KB or more than 2000 messages a second drops it
  to offline as flooding. Den never runs anything a host sends.
- **A host cannot flood you.** Den takes an event only for an agent the host
  lists, or one whose start it just reported; events for any other session are
  dropped. It takes 1000 events from a host at once, then 20 a second, and
  drops the rest. A host that makes Den drop 2000 events within a minute is
  disabled until you run `/remote connect devbox` (or restart Den). A host's
  events open at most 32 inbox items (128 for all hosts together), and at most
  60 new ones a minute. Its desktop notifications are one at once, then at
  most one every 30 seconds ("devbox: 5 agents need you"), and no more than 6 a
  minute for all hosts; its `[on]` commands run at most 30 times a minute.
  `/remote` shows what was dropped.
- **Metadata by default, checked twice.** The host filters before sending; Den
  filters again on arrival.
- **Everything from a host is sanitised.** Escape sequences, control
  characters and bidirectional overrides are removed from every string a host
  sends, before it is shown, put in a notification or title, or logged.
- **A host cannot answer for you.** Decisions only go from Den to the host
  and are never read from it. Each is bound to a random request id and to a
  hash of the body you were shown, is valid once, and only while the request
  is open and on the connection that offered it. A late, replayed or
  mismatched decision is dropped. A compromised host can lie about what its
  own agent will run, which it controls anyway; it cannot approve anything for
  another host or for this machine.
- **What you approve is what you saw.** The host cuts the body and Den checks
  it again; a command, path or URL that would not show whole sends the request
  to the terminal instead.
- **No generic command.** Messages and closes go through a closed pair of
  commands with results. There is no shell on the host.
- **What Den keeps.** On this machine: `remotes.toml`, and per host the last
  metadata it received and the position in its event stream
  (`remote/<label>.json` in the state directory, mode 0600).
