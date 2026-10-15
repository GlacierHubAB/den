# Den's local API (den-api.sock)

Den can serve a small local API so that apps on your machine can show your
fleet and ask about it. Drey is the first such app. The API is **off** until
you turn it on, and it lets no app approve, answer, start, close or message
anything, except the one console you pair yourself (see Console).

## Turning it on and off

- `/api on` in Den shows what the API lets apps do and asks you to confirm
  with `Ctrl+Y` once you have read it. Den then writes `api.enabled = true` to
  its config. An app never writes Den's config; Drey shows "Turn on the Den
  API: type /api on in Den" when the API is off.
- `/api off` drops every app at once, refuses new ones and writes
  `api.enabled = false`.
- `/api disconnect` drops every app until `/api resume` (or a restart).
- `/api` lists the connected apps: process id, the executable Den read from
  `/proc`, what they subscribe to, and whether one is asking.
- While the API is on, Den's header shows `API on`, or `⇄ <app> connected`
  (the executable's name as Den read it, never the name an app gives itself),
  or, for an app whose executable Den cannot read, `⇄ 1 app connected ·
  unidentified`. The full description is in `/api` and on its cards.
  The first connection of each app also leaves a note in the orchestrator
  chat.

```toml
[api]
enabled = false      # turned on by /api on
ask_budget_usd = 2   # what apps' questions may spend a day
```

## Transport

- A Unix socket at `$XDG_RUNTIME_DIR/den-api.sock`, mode `0600`, served by the
  running Den. Without `XDG_RUNTIME_DIR`, or when that folder is not private
  to you, there is no API. Den binds the socket at a temporary name, sets its
  mode and renames it into place; it never touches a path that holds anything
  but a dead socket of your own, and leaves the path bound while it runs, even
  after `/api off`, so no other program can take it.
- **Same user only.** Before reading a byte, Den reads the peer's credentials
  from the kernel (`SO_PEERCRED`) and refuses another user with one line,
  `{"id":0,"ok":false,"error":{"code":"forbidden"}}`, and a close.
- **Not agents.** A client whose process runs under one of Den's agents, its
  orchestrator or its read-only lane gets `forbidden` for every verb after
  `hello`. Den walks the client's parents and matches each against the live
  Claude sessions on this machine by pid and start time, so a pid an ended
  or remote session once had never counts. Den's chat notes the first
  refusal of each program, naming the agent it runs under, then at most one
  count a minute; `/api` lists them all. (Any program running as you can still connect;
  every verb is built to be safe for that.)
- JSON lines, UTF-8, one object per line. A line over 1 MiB gets
  `{"id":0,"ok":false,"error":{"code":"too_large"}}` and a close.
- `hello` first: any other op before it fails with `hello_first`.
- At most 16 connections, 6 per process. `hello` must come within 5 seconds.

## Lines

```json
{"v":1,"id":7,"op":"inbox.body","params":{"id":"i-1"}}
{"id":7,"ok":true,"result":{"id":"i-1","kind":"permission","text":"…","hash":"sha256:…"}}
{"id":7,"ok":false,"error":{"code":"not_found","message":"no item i-1"}}
{"event":"fleet","data":{"taken":"…","agents":[…],"windows":[…],"missions":[…],"cost":{…}}}
```

A subscribing op is answered first; then Den pushes `event` lines on that
connection until either side closes it.

Error codes: `unsupported` (an unknown op or version: hide the feature),
`bad_request`, `not_found`, `too_large`, `hello_first`, `forbidden`,
`unavailable` (Den has the verb but cannot serve it now), `internal`.

## Versioning

`v` is 1. `hello {v, client, version, group?}` returns `{version,
versions:[1], host, control?, features?}`; `features` lists `"console"`
when Den serves a console (see Console); a `v` Den does not serve gets
`unsupported`. `control` is `"on"` when an app may pair for control (see
Control), `"off"` when you turned control off in Den (`/api control off`:
`/api pair` shows no new code, a pairing made then starts off, and
`orch.say` is refused, until `/api control on`); a Den without
control sends none. A Den with no den-api.sock at all (v0.4.2 and older)
is told by the missing socket, never by this field. `group`
is optional: 32 lowercase hex digits (128 random bits) the app draws once
per process and sends on each of its connections, so that its asks on all
of them count as one app's (see Asking); any other shape gets
`bad_request`. Without it, each connection is on its own. A group is not a
secret like an ask id, but whoever holds it can replace the group's
questions: keep it in the process that drew it, and never pass it to a
child process or another program. Added fields never change
`v`; a removed or changed verb does.

## Verbs

| Op | Params | Result |
|---|---|---|
| `hello` | `{v, client, version, group?}` | `{version, versions, host, control?, features?}` |
| `fleet.snapshot` | | `Fleet` |
| `fleet.events` | | `{subscribed:true}`, then a `fleet` event at once and on every change |
| `inbox.list` | | `[Item]`, each with a digest of its body, never the body |
| `inbox.events` | | `{subscribed:true}`, then `inbox` events `{change: added\|answered, item}` |
| `inbox.body` | `{id}` | `{id, kind, text, hash}` for a request on this machine; `unavailable` for one from another machine |
| `mission.list` | | `[Mission]` |
| `focus` | `{target}` an agent's id or the name Den gave it | `{focused}`: that agent's terminal comes to the front. An inbox item answers `unavailable` (open the Inbox in Den), as do an agent on another machine and any focus within 1.5 s of your last key in Den. One per app every 3 s, ten a minute in all. Den's own view, selection and command line never change. |
| `ask` | `{text, session?}` | `{session, ask}`, then `chat` events until a `done` or `error` chunk. `ask` is 128 random bits, told only to the connection that asked: it is what cancels the ask, so keep it to yourself |
| `ask.cancel` | `{ask}` | `{cancelled:true}`; the ask's stream ends with an `error` chunk `"cancelled"`. It takes the ask's id, on any connection of the program that asked; any other id is `not_found` |
| `chat.history` | `{session}` | `[Message]` of an ask session |
| `chat.sessions` | | `[{id, title, last, current}]`, newest first, exactly one current |
| `grant.request` | `{manifest}` | `{id}`; the answer comes on `grant.events`, never here |
| `grant.events` | | `{subscribed:true}`, then `grant` events `{id, answer: granted\|denied\|expired, version, hash}` |

Every other op answers `unsupported`, including every verb that would approve,
allow, deny, answer, start, close or message: there are none but the
console's. Control's verbs (`pair.claim`, `pair.challenge`, `pair.auth`,
`orch.say`) are under Control below, the console's (`console.*`, `card.*`,
`cmd.*`) under Console. This release
also answers `unsupported` for `theme.get`, `mission.start`,
`mission.drop` and `view.data`.

## Grants

`grant.request {manifest: {id, name, version, capabilities, provenance?,
body}}` is the one way an app asks you for something. Den puts a card in its
inbox: a title and lines of its own (which app asked, by the executable Den
read, and the capabilities), then `body` exactly as the app sent it, between
rules Den draws. You answer with `Ctrl+Y` (granted) or `Ctrl+N` (denied), and
`Ctrl+Y` counts only once the whole card has been on screen; there is no
"always". The answer goes to `grant.events` connections of the same
executable that asked, never to another app; an app that is away gets it
when it subscribes again. Its `hash` is `"sha256:"` + the hex SHA-256 of the
body Den showed, so an app records a grant only when that hash is the one it
computed.

Den refuses, with `bad_request`, a body that is empty, over 16 KiB or 200
lines, not valid UTF-8, holds a tab, a control, bidi or invisible character,
or draws rules like Den's own frame: it shows a body exactly or not at all.
An app may have three requests waiting, and Den takes twenty per run; a body
you denied is not asked again until Den restarts. A request expires when its
app has had no connection for 30 seconds, on `/api disconnect`, and when Den
exits.

## Types

Metadata only: names, states, ids, times, counts and digests. No prompt text,
no transcript, and no request body outside `inbox.body`.

- **Fleet** `{taken, agents, windows, missions, cost, asks?}`. An agent is
  `{id, name, role?, state, waiting?, worktree?, model?, since, window?, host?,
  remote?, costUsd, turns, lastSeen, color?}`; `state` is `idle`, `working`, `waiting`
  or `stalled`, and `waiting` names what waits: `permission`, `question` or
  `plan`. Agents on other machines carry `remote: true` and their host's
  label; what their hosts report is passed on as labels, never as Den's own
  words. Windows of agents on other machines are not listed. `color` is the
  colour Den draws an agent of this machine in, its critter and an `@Name`
  in Den's own text, as `#rrggbb`; agents on other machines have none.
  Den colours a name only where it is Den's word that the agent is in the
  fleet: never inside an agent's or a host's own text, and never a name two
  local agents share.
- **Item** `{id, kind, agent, since, title, digest, host?}`. A **digest** is
  `{firstLine, bytes, hash}`, with `hash` `"sha256:"` + the hex SHA-256 of the
  body; `inbox.body` returns the same hash. For a request from another machine
  the digest describes it in Den's words ("a permission request") and hashes
  that, never the host's text.
- **Mission** `{id, title, state, agents?, lines, done?, spendUsd?,
  budgetUsd?, proposed}`; `state` is `proposed`, `running`, `done` or
  `dropped`.
- **Chunk**, the `ask` stream: `{"type":"text","delta":"…"}`,
  `{"type":"status","seq":n,"text":"…"}`, `{"type":"done","session":"…"}`
  or `{"type":"error","message":"…"}`, each with the `ask` it belongs to. Status lines are Den's
  own fixed sentences ("Reading the fleet in Den…", "Writing a reply…"),
  never model text; `seq` rises per ask, and an empty `text` clears the line
  before `done`. The fleet's `asks` lists each running and waiting ask's
  latest status, which every app sees; its `ask` and `session` ids are
  there only for the connection that asked, or one in the same hello
  `group`.

## Asking

`ask` goes to a **read-only lane**: a second Claude Code process Den starts for
apps, apart from the orchestrator you talk to. It has no tools and no MCP
servers, loads no skills, plugins, hooks, slash commands, `CLAUDE.md` or
memory, refuses messages from other sessions, and runs in an empty folder of
its own. Den checks what the process reports at start and stops it unless it
has no tools and no MCP servers and runs a Claude Code release that
`scripts/lane-spike.sh` has passed: that script starts the lane offline with
a hostile user config and checks that it gets no tools, MCP servers, skills,
hooks or `CLAUDE.md`, and that a message from another session never reaches
it. On any other release `ask` answers with an error until the spike is run
and the release added. Whatever an app asks, the lane can only write
text: it cannot message, start, close or approve anything, and nothing it
writes changes Den's state.

The question goes to the lane fenced as an app's request, after the same
roster the orchestrator reads (without agents' recent words and tool calls,
and with what waits on you named by kind and title only). A question with a
control, bidi or invisible character, or over 16 KiB, is refused with
`bad_request`. One question runs at a time and up to four wait; a waiting
question gets a status saying how many are ahead. A connection, or the
connections that sent one hello `group`, have one question at a time: a
newer question from them replaces their earlier one, running or waiting,
whose stream ends with the error `"superseded by a newer question"`.
Another process of the same program, with no group or its own, never
replaces, cancels or sees the ids of their questions; its question waits
its turn. When the
connection that asked closes, or stops reading, its turn stops and the next
question runs. Every ask stream ends with a `done` or an `error` chunk; when
the lane cannot start (a release the spike has not passed, a failed check, the
budget, claude not starting) the error says why within seconds. A reply is
cut at 64 KiB or 3 minutes, and a lane silent for 90 seconds is stopped.
`/api` shows the lane's state (off, starting, ready, failed and why), what
runs, what waits and today's spend, which Den keeps across restarts. A lane
that fails to start waits a second before it tries again, twice as long each
time up to a minute; after three failures in five minutes asks are refused
until `/api resume`. A lane session starts afresh after 50 questions. Asks stop for the day once
`api.ask_budget_usd` is spent. The lane stops after ten minutes without
questions and resumes its session on the next one.

`chat.sessions` and `chat.history` serve these ask sessions only, never your
own orchestrator chat. They are kept in `~/.local/state/den/api-chat.jsonl`
(mode `0600`).

## Control

Control lets one paired app (Drey's `drey-host`) speak to the orchestrator
you talk to in Den, as you would from Den's chat. It is off until you pair
an app, and everything that would change your fleet still waits for you in
Den: the app never approves, answers or starts anything.

### Pairing

- `/api pair` in Den shows a code of eight words for two minutes, in Den's
  header only, with "Type this only into the app's own pairing window." It is
  never written to Den's chat or logs, and it leaves the screen once it is
  claimed or expires. The words come from a list of 256, so a code is 64
  random bits.
- The app sends it once: `pair.claim {code}`. Words are matched in order,
  in any case, with any separators. Three wrong codes end the attempt;
  `/api pair` starts another.
- A correct claim does not pair yet. Den puts a card in its inbox, "Pair
  <program> (pid N, started HH:MM) for dock control?", which also names the
  program's path, its command line and its parent. Only your `Ctrl+Y` on
  it, once the whole card has been shown, completes the pairing; `Ctrl+N`,
  two minutes without an answer or the app's connection closing refuse it.
  Until then `pair.claim` has no answer, for at most two minutes from the
  claim; afterwards it answers `{pairing, key, control}` (`pairing` and
  `key` 32 lowercase hex digits each, `key` 128 random bits).
- A refused claim answers `forbidden` with one of these messages, exactly:
  `no code waiting`, `wrong code`, `code spent` (this claim reused a code,
  or came within two minutes of another claim), `revoked: the code was used
  by another program too` (a waiting claim, answered at once when another
  claim revoked it), `denied in Den` (`Ctrl+N`) or `not confirmed in time`.
  `Den is busy` comes with `unavailable` when Den cannot take the card now
  (its queue to its screen is full): that claim did not count, the code is
  still live, and the same claim may be sent again.
- The code is single use. A second `pair.claim` within a claimed code's two
  minutes, or a claimed code again within ten minutes (even after another
  `/api pair`), means another program had the code too (anything that can
  read Den's screen can): Den revokes the pairing that code made, closes its
  connections and notes "two programs used the pairing code: <program>,
  pid N claimed it first, then <program>, pid M; the pairing is revoked,
  pair again", naming both, since either may be the one that should not
  have had it.
- Den and the app keep the key in memory only; when either restarts, pair
  again. A pairing is bound to the program Den read for the app (its
  executable): no other program can use it. When Den cannot read the
  executable (the app made itself non-dumpable), the pairing is bound to
  that one process instead, and the card says Den cannot check which
  program it is.
- `pair.claim`, like every verb, is refused for processes under Den's
  agents, its orchestrator or its lane; and it answers only while a code is
  live.
- If an `ask` or `orch.say` text holds the pairing code (after Unicode NFKC
  and lower case, five or more of its eight words as whole words, in any
  order, while the code is live and for ten minutes after it was claimed),
  Den refuses the text with `bad_request`, cancels the code (it is no longer
  secret), revokes the pairing a claimed code made, and notes "The pairing
  code was typed into a chat; pair again." Den never keeps the refused
  text.

### Proving the key

Each connection that wants control proves the key once, at its start; the
connection is a peer-checked unix socket that cannot be taken over
afterwards, so no later message carries a MAC.

- `pair.challenge {}` answers `{nonce, conn}`, 32 lowercase hex digits each:
  a single-use nonce for this connection, and the connection's name. One
  challenge a second per connection, else `unavailable`.
- `pair.auth {pairing, mac}`, with `mac` the hex HMAC-SHA256 under the key
  of `"pair-auth\napp-to-den\n" + conn + "\n" + nonce`, answers `{paired:
  true, control: true, mac}`, where `mac` is Den's own proof,
  `"pair-auth\nden-to-app\n" + conn + "\n" + nonce` under the same key:
  check it, so you know you reached the Den that holds the key.
- A wrong `mac` answers `forbidden` and closes the connection; three wrong
  ones in five minutes revoke the pairing, with a note in Den. Only a wrong
  `mac` counts. A pairing that is gone (revoked, refused, or Den restarted)
  answers `not_found`; one that belongs to another program `forbidden`; no
  challenge first `bad_request`.
- `orch.say` on a connection that has not passed `pair.auth` answers
  `forbidden`.

### Speaking to the orchestrator

| Op | Params | Result |
|---|---|---|
| `pair.claim` | `{code}` | `{pairing, key, control}`, once you `Ctrl+Y` in Den |
| `pair.challenge` | | `{nonce, conn}` |
| `pair.auth` | `{pairing, mac}` | `{paired, control, mac}` |
| `orch.say` | `{text}` | `{turn}`, then `orch` events until a `done` or `error` chunk |

- Text: at most 16 KiB. Den applies Unicode NFKC, breaks lines at `\n`,
  `\r\n`, `\r`, U+0085, U+2028 and U+2029, removes every C0 and C1 control
  (tab, escape and bracketed-paste markers included), refuses a bidi or
  invisible character, and refuses the whole text with `bad_request` if any
  line starts with `/`, `!` or `#` once leading white space is trimmed. `<`
  and `>` become `‹` and `›`, so the text cannot open or close a block of
  Den's. The lines are joined with `\n`.
- The orchestrator reads the text after Den's fixed preface, at most 512
  bytes, which names the program and says that every tool in the turn waits
  for you in Den; then the roster, as for your own messages.
- One app turn at a time on the whole machine (else `unavailable`), six a
  minute per pairing. `turn` is "t-" and 32 hex digits.
- Chunks, each with its `turn`: `{"type":"text","delta":"…"}`,
  `{"type":"status","seq":n,"text":"…"}` (Den's fixed sentences),
  `{"type":"needs_approval","card":"…","item":"…"}` and `{"type":"done"}` or
  `{"type":"error","message":"…"}`. `card` is the inbox card's title, one
  line of at most 200 runes, already sanitised by Den but written from what
  the orchestrator asked for: show it as text under your own fixed words
  ("Needs approval in Den"), with "Open in Den" as the only action. `item`
  is the card's id in Den's own inbox ("i" and a number), opaque to the
  app: Den's cards are not served by `inbox.list` or `inbox.body`. `focus`
  with `item:<id>` still answers `unavailable` in this release; a later one
  brings Den forward on that card, under the same guard against your own
  typing.
- Closing the connection ends the turn: Den stops it, and the stream ends
  with `error "cancelled"` if it can still be written. A turn ends with an
  error after ten minutes, or two minutes without a word from the
  orchestrator, and when you type in Den's chat while it runs: your
  message goes first.
- The orchestrator starts in control mode for the app's first turn, on a
  Claude Code release Den has checked control on (2.1.289, 2.1.296); on any
  other release `orch.say` ends with an error that says so, and Den's own
  chat works as before.

### What an app's turn may do

While a pairing lasts, Den runs the orchestrator so that every tool but
`ListAgents` asks Den first (Claude Code's manual mode, with its permission
prompts sent to Den), and Den answers from a path that never waits on its
screen:

- An app's turn lasts from the moment Den sends it until the result that
  follows Claude Code's replay of that very turn: only that result closes
  it; a refused prompt's result does not. Den sends it only when no other
  turn is in flight, and holds every other turn (yours, its own and agents'
  reports) until it ends. Anything Den cannot place counts as the app's.
- In an app's turn every tool request, Den's tools and messages to agents
  alike, becomes a card in Den's inbox, "Requested from <program>: …", with
  the request in full, for your `Ctrl+Y` (once) or `Ctrl+N`. Nothing is
  allowed by default. A request with no answer in a minute is denied.
- In your own turns Den's named tools work as before, without a card; any
  other tool is denied.
- Claude Code's own SendMessage is turned off while a pairing lasts: it
  never asks permission, so it could not be held. Den's `send_message`
  takes its place and asks like any other tool. Den delivers it the way
  SendMessage does, from the orchestrator's own address and name, to live
  sessions on this machine only, so agents see what they always saw and
  report back the same way.
- If Den's guard finds a tool that ran in an app's turn without Den
  allowing it, Den stops the orchestrator, notes why and revokes every
  pairing.
- `/api` lists each pairing (program, pid, paired since, last used, control
  on or waiting for your `Ctrl+Y`); `/api unpair` revokes every pairing at
  once and closes their connections. Den's header shows `paired: <program>
  <path>` while one lasts.

The read-only `ask` lane is a separate process: an app turn never holds an
`ask`, and an `ask` never holds an app turn.

### Limits

- Any program running as you can type into Den's terminal window as if it
  were you, or read Den's screen (and so the pairing code); pairing does
  not change that, and the `Ctrl+Y` card is there so that a code read off
  the screen cannot pair in silence. On Hyprland, its `permission` rules
  for keyboard access and for screen capture (screencopy) can keep other
  programs from both.
- A standing allowance to message an agent depends on the permission mode
  that agent reports through its own hooks. Den takes a mode only from the
  agent's own hook processes, so no other program can claim one for it,
  but the agent itself could report a mode it is not in (by running Den's
  hook from its own shell). The card shows the mode "as the session
  reports it".
- A pairing is bound to the program Den read for the app. An app run by an
  interpreter (`python3`, `node`) is that interpreter to Den, so the card
  also shows its command line: read it before you pair.
- A process can make itself non-dumpable (`PR_SET_DUMPABLE` 0), and then
  the kernel keeps its executable from Den. Any program can do this, so it
  is no evidence of which program it is. Den then knows the app as that one
  process in this boot (pid and start time): its grants, groups, pairing
  and limits are that process's alone, and a new process of the same app
  asks again. Cards say "a program Den cannot identify (pid N, started
  HH:MM:SS)" and show its command line as the process reports it, which it
  can make up; match the pid and start time with what the app shows.
- Claude Code's inbox sockets accept messages from any process of your user
  without proof of who sent them; Den's `send_message` relies on that to
  deliver as SendMessage does. It is a property of Claude Code running as
  you, not one Den adds.

## Console

A console is the one program you pair, in Den's own window, as yours:
Drey's dock. It may read Den's cards and answer the ones Den lets it, and
send Den's commands, as you would in Den. It is not Control's app pairing:
the code never crosses the socket, both sides sign every proof with
Ed25519, and the console is bound to one process. Den's `hello` lists
`"console"` in `features` when it serves one.

### Pairing a console

- `den pair` in a terminal, or `/api console` in Den, shows a code of eight
  words for two minutes, in Den's header only. `den pair` is refused under
  Claude Code, under one of Den's agents, and from `den mcp` or `den hook`.
- Type the code only into the app's own pairing window. The app uses it as
  its eight words in lower case, joined by single spaces.
- On one connection: `console.challenge {}` answers `{nonce, conn,
  claim_salt}`, each 64 lowercase hex digits but `conn` (32), at most one a
  second on each connection. Then `console.claim {public_key, proof, pages_sha256?}`:
  - `public_key` is the app's Ed25519 public key, 64 hex digits, drawn for
    this pairing and kept in memory only;
  - `proof` is the hex HMAC-SHA256, under HKDF-SHA256(code, salt =
    `claim_salt`, info = `den-console-bind-app-v1`, 32 bytes), of
    `"den-console-bind-app-v1\n" + public_key`;
  - `pages_sha256`, optional, is the hash of the app's pages, shown on the
    card.
- A code takes one claim, right or wrong. A right proof does not pair yet:
  Den puts a card in its inbox naming the program (path, pid, start time,
  parent, command line), and only your `Ctrl+Y` on it pairs. The claim then
  answers `{pairing, den_public_key, bind, console: true}`, where `bind` is
  the hex HMAC-SHA256 under HKDF-SHA256(code, salt = `pairing`, info =
  `den-console-bind-v1`) of `"den-console-bind-v1\n" + public_key + "\n" +
  den_public_key + "\n" + pairing`. Check it: it proves this Den saw the
  code, and pins Den's key.
- A refused claim answers `forbidden`: `no code waiting`, `wrong code`,
  `code spent` (the code was claimed already, right or wrong, and its two
  minutes are not over), `denied in Den`, `not confirmed in time` or `revoked: the code was used
  by another program too`. `unavailable` comes with `Den is busy` or, for
  30 seconds after a `Ctrl+N`, `wait: a pairing was just declined`.
- One console at a time: a new one replaces the old, which hears
  `unpaired`. Den keeps its side (the console's identity and public key)
  across its own clean restarts, and says so (see Events). A console
  whose process ended, a new boot, or a record Den did not write unpairs.

### Proving a connection

Each connection proves once, at its start:

- `console.challenge {}` gives `nonce` (Den's, *nd*) and `conn`.
- `console.auth {pairing, nonce, sig}`: `nonce` is the app's own 64 hex
  digits (*na*), `sig` the hex Ed25519 signature (128 digits) of these
  lines joined by `\n`: `den-console-v1`, `app-to-den`, `conn`, `pairing`,
  the app's public key, Den's public key, *nd*, *na*, and the app's
  identity.
- It answers `{paired: true, sig, god: {on: false}, view: "agent"}`, `sig` Den's signature
  of the same lines with `den-to-app` and Den's identity as the last line.
  Check it with the pinned `den_public_key`. `god` and `view` are fixed in
  this release: God mode is off and every session shows in its own window
  (`agent`); a later Den adds `user` and an event for each change.
- The identity is the process as Den reads it in this boot:
  `exe:<path as hex>:<boot_id>:<pid>:<start>`, or
  `unreadable:<boot_id>:<pid>:<start>` when the process is not dumpable;
  `boot_id` from `/proc/sys/kernel/random/boot_id`, `start` field 22 of
  `/proc/<pid>/stat`. Den's own is `den:<boot_id>:<pid>:<start>`.
- A proof from another process answers `forbidden` (`another program holds
  this console`) and tells the console's connections (a `refused` event);
  a wrong signature answers `forbidden` and closes. Three of either in ten
  minutes unpair the console. A seventh connection gets
  `console_in_use`; a pairing that is gone, `not_found`.

### Console verbs

| Op | Params | Result |
|---|---|---|
| `console.challenge` | | `{nonce, conn, claim_salt}` |
| `console.claim` | `{public_key, proof, pages_sha256?}` | `{pairing, den_public_key, bind, console}`, once you `Ctrl+Y` in Den |
| `console.auth` | `{pairing, nonce, sig}` | `{paired, sig, god, view}` |
| `console.events` | | `{ok, cards}`, `cards` the ids of every card waiting; then a `console` event, a `card` event for each of them, then `console` and `card` events as they come |
| `console.unpair` | `{}` or `{all: true}` | `{ok, unpaired}` |
| `card.get` | `{id}` | the card and a `nonce` |
| `card.answer` | `{id, text_sha256, nonce, verb}` | `{ok, result, note}` |
| `cmd.list` | | `[{name, usage, help, dock}]` |
| `cmd.complete` | `{prefix}` | the commands that start with it, at most 64 |
| `cmd.run` | `{text}` | `{ok, note}` |

All but `console.challenge`, `console.claim`, `console.auth` and
`console.unpair {all: true}` need a proved connection, else `forbidden`
(`prove first`). `console.unpair {}` ends the console from its own
connection; `{all: true}` ends every console and app pairing from any
connection, at most once every ten seconds for everyone, as `den unpair
all` does in a terminal.

### Events

- `console {kind, pid, start_ticks, started?, program?, reason?}`: `kind`
  is `connected`, `restored` (the first `console.events` after Den
  restarted with this console: check `pid` and `start_ticks` are yours),
  `refused` (another program tried this console) or `unpaired`.
- `card {id, kind, agent, change, text_sha256?, result?, by?}`: `change` is
  `new`, `changed` or `settled`. A settled card has no `text_sha256`;
  `result` is `allowed`, `denied`, `always` or `gone` (it settled without
  an answer), and `by` is `den` or `console`, absent for `gone`. `rule`
  is reserved for a later Den that answers by a rule of its own. Take any
  `by` you do not know as Den's own answer, never the console's or yours,
  and never drop the event for it.
- After a re-dial, `cards` in the `console.events` reply is the whole set
  of cards waiting: one you knew that is not in it is gone. The `new`
  events that follow are those cards.

### Cards

- `card.get {id}` answers `{id, kind, agent, tool, title, note, body,
  text_sha256, truncated, cut_at?, verbs, dock_answers, nonce}`: the card
  as Den shows it, and a nonce for one answer, on this connection, for ten
  minutes. `text_sha256` is the SHA-256 of `kind`, `agent`, `tool`,
  `title`, `note` and `body` joined by `\n`. Show all of it, `note` (a
  warning Den shows) too.
- `card.answer` takes `verb` `allow`, `deny` or `always`, one of the card's
  `verbs`, with the hash and nonce of a `card.get`. Den answers it as your
  `Ctrl+Y` would, and replies once it is settled. Refusals:
  - `not_found`: no such card, or it was answered;
  - `nonce_expired`: spent, too old, or from another connection;
  - `card_changed`: the card is not what you read: get it again;
  - `refused_from_dock`: `dock_answers` is false or the verb is not the
    card's;
  - `read_it_first`: a card of more than ten lines, answered within 300 ms
    of its first `card.get`;
  - `unavailable`: Den has the card open (it waits for you there), it was
    answered only in part (Den shows the rest), or more than 20 answers in
    a minute.
- Five answers in a row less than a second apart unpair the console.
- Never from the dock: a console or app pairing, turning the API on,
  orchestrator guidance, installing on a host, a message to an agent on a
  host, an app's grant, skills, an agent's question, and any card of or
  about a remote host.

### Commands

- `cmd.list` is every Den command with `dock`: `ok` (runs), `card` (runs,
  and asks you on a card in Den), `needs_den_screen` (it acts on Den's own
  screen) or `refused`.
- `cmd.complete {prefix}` lists the commands that start with it, refused
  ones left out, at most ten a second per connection. Use it only to fill
  the input.
- `cmd.run {text}`: one command, at most 512 bytes, 30 a minute. Den takes
  it after Unicode NFKC: no control or format characters, no line or
  paragraph separators, no `;`, `&&`, `||`, `` ` `` or `$(`, no second
  command word. It maps it onto one of its own commands, by name or alias
  in any case, and runs it as typed in Den, with a note in Den's chat
  naming the console; anything else is `refused_from_dock` with why. A
  command's own error comes back `unavailable` with Den's words.
- From the dock, `/usage set` and `/usage clear` put the figure on a card
  ("A lower figure can hold off your usage stop."), and no command proposes
  a mission or runs a template: those wait for Den.

### Console limits

- A console answers for you: a card it may answer is as answered as by
  your `Ctrl+Y`, and a console can send `/usage set` and answer that card,
  so it can lower the usage figure your usage stop is measured against.
  Pair only a program you trust with that.
- Any program running as you can read Den's screen, and so the code; the
  `Ctrl+Y` card is there so that a code read off the screen cannot pair in
  silence. Read the card's program and pid before you pair.
- Any program running as you can also write Den's console record; a
  restored console is announced in Den and to the console, so check that
  it is yours.
