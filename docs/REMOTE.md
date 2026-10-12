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

Answering a remote agent's requests is not part of this version: you answer in
the terminal on the host.

Messaging and closing a remote agent are not part of this version: for those
you go to the terminal on the host.

Review of a remote agent's changes is not part of this version: for that you go
to the host.

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

## Policies

Each host has a policy, which is how much it may send you. The default is the
strictest. The host's own `den remote serve` filters what it sends, and Den
checks it again on arrival and drops, and flags the host for, anything its
policy does not allow.

| Policy | What crosses the wire |
| --- | --- |
| `observe` (default) | Metadata only. Per agent: status, critter name, session id, peer name, badge role and task (at most 80 characters, withheld with `labels = false`), model, Claude Code version, repository name and branch, context and usage figures, tokens by model, and how long ago things happened. Per event: kind, session, agent name, time, request kind, tool name, error type. |

Under `observe` a host never sends prompts, replies, transcripts, commands,
tool input, file paths, working directories, window titles or recent activity.

Badge role and task are short summaries Claude Code's own model writes about
the work, so they say a little about it. Set `labels = false` on a sensitive
host to keep them back.

## What it looks like

- **Fleet.** A remote agent has an `@devbox` chip after its name and the same
  role, task, branch and context figures as a local one. The details pane
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

A link that goes silent is noticed in 45 to 60 seconds; a killed ssh at once.
Events that happened while the link was down arrive once it is back, once each.

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

## For scripts

`den list` and `den list -json` include remote agents as the running Den last
saw them, with a `Host` field, and `Stale` and `HostState` when the link is
not live now (`last known:` in front of the status in the table). The JSON
carries the same metadata as the Fleet. `den events -json` carries a `host`
field on remote events while Den runs, and `[on]` commands for remote events
get `DEN_HOST` set to the host's label (see [EVENTS.md](EVENTS.md)); they run
on this machine.

## Limits

- Den cannot answer a remote agent's requests in this version.
- Den cannot message or close a remote agent in this version.
- Den cannot review a remote agent's changes in this version.
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
  there by hand.
- **Agents appear but no events or requests.** Den's hooks are not installed on
  the host: run `den install-hooks` there. Only sessions started afterwards
  report.

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
  It opens no socket.
- **A host cannot reach this Den.** What a host sends never touches Den's own
  socket, so it cannot ask this Den to close, spawn or install anything. A host
  speaks a closed set of messages: an unknown type is dropped and the host
  flagged, and a line over 256 KB or more than 2000 messages a second drops it
  to offline as flooding. Den never runs anything a host sends.
- **Metadata by default, checked twice.** The host filters before sending; Den
  filters again on arrival.
- **Everything from a host is sanitised.** Escape sequences, control
  characters and bidirectional overrides are removed from every string a host
  sends, before it is shown, put in a notification or title, or logged.
- **What Den keeps.** On this machine: `remotes.toml`, and per host the last
  metadata it received and the position in its event stream
  (`remote/<label>.json` in the state directory, mode 0600).
