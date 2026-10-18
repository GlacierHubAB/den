# denconformance

A conformance suite for servers of the Den API: the local JSON-lines socket
(`den-api.sock`) a desktop shell such as Drey uses to show Den's fleet,
inbox and chat. It needs only the standard library and a dialer, so a
server's own repository can carry a copy as a test-only package and run it.

Package SHA-256 `6c2244979c6a6403d41d31af449829d08b360b79f0702b456b501a36576415b6`. Den's copy is pinned to the dev commit this package lands as: the commit that adds these files cannot name itself, so the line states the hash only, which is what proves the content.

The package SHA-256 is computed over the Go files in name order, in the
package's directory:

```
sha256sum $(ls *.go | sort) | sha256sum
```

## Copying it

1. Copy the `.go` files and this README into a test-only package of your
   repository, unchanged (the package name stays `denconformance`).
2. Check the SHA-256 above with the command above.
3. Run it from a `_test.go` file against the socket of an isolated server:

```go
func TestDreyInterface(t *testing.T) {
	denconformance.Run(t, denconformance.Options{
		Dial: func(ctx context.Context) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
		Skip: denconformance.D1Skip, // mission.start, mission.drop, view.data
	})
}
```

Options: `Dial` (required), `Console` (below), `DialAsOtherUser` (a connection the server
must refuse; the peer-credential subtest is skipped without it),
`DialAsOtherExe` (a connection from another program of the same user,
whose `ask.cancel` of this program's turn must be `not_found`; that check
is skipped without it),
`Timeout` (per subtest, default 5 s), `Focusable` (an agent or item id
focus must accept; found from the fleet otherwise) and `Skip` (subtest
names; `Subtests` lists them all). Each subtest is named after the verb
it checks or the property it checks (`hello first`, `unknown verb`,
`oversized line`, `second client`, `peer credentials`).

### The console

`Console` (a `*ConsoleOptions`) turns on the console subtests,
`ConsoleSubtests`: the headless console's pairing and verbs. The suite
plays the dock. It makes an Ed25519 key pair, claims a pairing code with
the code proof, checks the server's bind, proves itself with signatures
over the pinned string, subscribes to `console.events`, fetches and
answers a card, and fills the connection cap. It ends with
`console.unpair {"all":true}`. Left nil, every console subtest is skipped.

`console orch.say` speaks to the orchestrator from the proved console: it
wants `forbidden` before any proof, a `{turn}` and that turn's `orch`
events after `console.auth`, `unavailable` for a second turn while one
runs, and a new turn possible soon after the asking connection closes. It
starts three or four real turns, so run it against a test orchestrator; it
is skipped when hello's `control` is not "on". A Den from before console
turns (v0.5.2) refuses the proved console with `forbidden`: list
`console orch.say` in `Skip` for it.

What only the server's own test can do is passed in:
- `Offer` starts a console pairing and returns its eight words. Required.
- `Confirm` stands in for the human's ^Y on the console card. Left nil,
  the subtests that need a confirmed console are skipped.
- `Decline` stands in for ^N. Left nil, `console cooldown` is skipped.
- `Card` raises a pending card the dock may answer, with "deny" among its
  verbs. Left nil, `card.get` and `card.answer` are skipped.
- `LongCard` raises one like it with more than ten body lines. Left nil,
  `card read timer` is skipped.
- `View` says the server switches views and can identify its own window:
  `view.hide keeps a waiting card` raises a card with `Card`, hides, and
  wants Den's own window still counted in `shown`, with a `reason`. It
  really hides the server's windows (and shows them again after), so set
  it only for a test server on a test desktop. The reply does not name the
  window kept; the server's own test pins that it is its own. Left false,
  it is skipped.
- `Request` says the server pairs a console by `console.request` for this
  test process (its unit check stood in for, as Den's harness does):
  `console.request` sends a fresh key and a pages hash. A server that pairs
  its console's service by itself replies at once with no card, and
  `Confirm` is not pressed; if no reply comes within a second, a card is
  taken to wait and `Confirm` answers it (and the reply must then carry
  powers). Either way the reply `{pairing, den_public_key, console:
  true, powers}` must name hello's `console_public_key`; the pairing then
  proves. `console.reconnect` rejoins that pairing with the same pages
  hash (a server records the unit only for a request). Left false,
  `console.request` is skipped and `console.reconnect` uses the console
  paired by code.
- `RequestRefusals` (with `Request`) says the server refuses
  `console.request` as Den's own pairing of its console's service does:
  `console.request after an unpairing` wants `forbidden` "unpaired in Den:
  …" after the console unpaired itself, and a pairing again once `Offer`
  starts one in the server; `unpaired cause intruders` then wants
  `unavailable` from a request (for ten minutes after an unpairing for
  other programs' tries).
- `PowersLocked` says the server holds back powers from a console that
  pairs or rejoins on its own, as Den does while `/api lock` or God mode
  holds them: a `console.reconnect`, and a `console.request` answered at
  once, must then say `powers: false`. Left false, those may say either.
- `console.events snapshot` (needs `Card`): on subscribe the server sends
  the current value of each stateful kind before any change. With a card
  waiting, the subtest wants, in order, a `console` event `connected` or
  `restored`, a `card` event `new` for each card `console.events` listed,
  a `console` event of kind `powers` with `on` (equal to `console.auth`'s
  `powers` on that connection, when it sent one), a `god` event with `on`,
  then `mode` and `view`, each optional and in that order; a new card
  raised after must come after them. The snapshot then ends with a
  `desktop` event (`state` idle, asking or active, with `mission` for
  active) and a `mirror` event (`state` off, asking or on, with `since` in
  RFC3339 UTC for on only), each once and in that order. One-shot console
  kinds (`paired`, `reconnected`) pass. `fleet.events` must start with the
  whole fleet. A kind the suite does not know may join the snapshot later;
  it is let pass.
- The conversation, mirror and doing shapes are checked strictly: a field
  the document does not list fails (`mirror`, an entry and its `console`,
  `doing`); origin and speaker must be a pair the document allows (`den`
  speaks only for origin report, mission or desktop, `you` only for origin
  you or console, `orchestrator` for any); `agent` is required for report
  and desktop and for no other origin, one plain line of at most 64 runes,
  never Den, Drey, You or Orchestrator in any case; `turn` only with origin
  console, as t- and 32 hex digits; text and `doing.text` hold none of
  these characters: a control (a newline only in an entry's text), DEL, a C1
  control, U+2028 and U+2029, U+061C, the zero-width and bidi characters
  (U+200B to U+200F, U+202A to U+202E, U+2060 to U+2069, U+FEFF), U+FFF9 to
  U+FFFB, and the tag characters U+E0000 to U+E007F; a text is the whole
  message: at most 65536 bytes of UTF-8 before Den's cut mark and no limit on
  lines, and a cut text ends with the mark (the same words mid-text are the text's own); `doing.text` is
  one line of at most 80 runes. `conversation.history` with a limit of 201
  is refused with `bad_request` or answers at most 200 entries.
- `console mirror`: `conversation.history` is `forbidden` before any
  proof; on a proved console it is `forbidden` while the snapshot's mirror
  state is not on, and `{entries}`, newest first (ids falling), while it
  is on, each entry `{id, at, turn?, origin, speaker, agent?, agent_id?,
  console?, text}` with origin and speaker from their closed sets, `console` only
  with origin console, `agent` only with report or desktop, `agent_id`
  (the agent's session id, `^[A-Za-z0-9_.:-]{1,64}$`) only in the line that has
  `agent`, so only with report or desktop, and text of at
  most 65536 bytes (any number of lines) with no control, bidi or zero-width
  character. A `conversation` event line is at most 409600 bytes and a
  `conversation.history` reply line at most 819200 (encoded, with the
  newline); live entry ids strictly increase on a connection, and a gap is
  legal (Den may drop a conversation entry and then sends `conversation_gap
  {from}`, the id of the first one it did not send, nothing else in it). An
  `orch` or `chat` text chunk is at most 64 KiB. With `Mirror` set, the subtest switches the mirror on and
  off, wants a `mirror` event for each change, and checks the history
  after each.
- `Mirror` switches the server's mirror (state on, asking or off) as the
  human's answer in the server does. Left nil, `console mirror` only reads
  the snapshot's state. `Conversation` adds one entry of the human's
  conversation while the mirror is on; with both set, `conversation entry`
  wants a live `conversation` event whose entry is also in the history
  with the same id and text. Left nil, it is skipped.
- `fleet doing`: an app connection's fleet carries every agent's `doing`
  empty; the proved console's own connection carries it, `text` empty or
  at most 80 runes and a lead phrase of Den's closed set, case-sensitive
  (Waiting for you; Stuck, needs a look; Idle; Navigating files and folders;
  Reading; Editing; Writing; Searching the code; Running the tests; Checking
  the code; Building the project; Installing packages; Looking over the
  changes; Committing changes; Pushing changes; Working with git; Working on
  GitHub; Fetching a web page; Searching the web; Handing work to a helper;
  Working with containers; Managing services; Running a command; Working
  on; Working), alone or followed by " " and a target, ":" and a reason, or
  "…" (only the lead is checked), `at` in RFC3339, and at least
  one agent non-empty. Skipped when the server has no agent.
- `chat.history` holds only Den's two roles, user and orchestrator.
- Console events of a kind a subtest is not waiting for (Den's `paired`,
  `reconnected`, `powers`) are skipped, as a client ignores kinds it does
  not know.
- `Reconnect` says the server answers `console.reconnect` at once (a
  silent reconnect) for this test process. `console.reconnect` then sends
  it before `hello` from a fresh key: a missing or malformed `nonce` must
  be `bad_request`, and `den_sig` must verify under hello's
  `console_public_key` (the key pinned at pairing) and fail for another
  nonce; the new pairing must then prove with `console.auth`. Den checks
  the dock's systemd unit first, which a test process is not: a refusal
  `not drey-host.service` or `pair again` skips the rest. Left false, only
  the subtest's known answer for its own verify (DREY-API.md's) runs.
- The `unpaired cause …` subtests pair a fresh console each and watch it
  on one proved connection subscribed to `console.events`: the `unpaired`
  event must carry exactly the cause, with a `reason` of at most 40
  characters. `self` (`console.unpair` from the console's own connection),
  `replaced` (a second console paired), `proofs` (three wrong proofs),
  `intruders` (`console thief`'s process run three times; needs `ThiefTest`)
  and `other` (`console.unpair {all: true}` from a connection that is not
  the console's, after `console.unpair all`'s 10 s, which also checks
  `other`) run over the wire. `guard` and `record` come from inside the
  server and are not in the suite.
- `Unpair` has the server unpair its own console (Den's `/api unpair`, or
  `den unpair` in a terminal): `unpaired cause den` wants cause `den`.
  Left nil, it is skipped.
- `Burst` (a server's test build or test hooks only, never a switch in a
  running server: it turns a safety guard's pacing off) turns the
  server's pacing of card answers off for
  `unpaired cause burst` (five answers from `Card` less than a second
  apart, cause `burst`) and returns the function that turns it back on.
  Left nil, it is skipped.
- `Disconnect` ends every connection as `/api disconnect` does:
  `unpaired: not at end of file` wants no `unpaired` event and the pairing
  to prove again. Left nil, it is skipped.
- `ThiefTest` and `SocketPath` turn on `console thief`. The suite runs the
  test binary again as `-test.run=^<ThiefTest>$`, and that copy proves with
  the console's own key from another process. The server must refuse it
  and send a `refused` console event naming its pid. Add a test of that
  name to your package whose body is `denconformance.ConsoleThiefHelper(t)`.
  Left empty, `console thief` is skipped.
- `Identity` and `DenIdentity` override how the suite names this test
  process (default `exe:<hex of /proc/self/exe>:<boot_id>:<pid>:<start>`)
  and the server (default `den:…` of the connection's SO_PEERCRED peer).

The subtests check hello's `features`, `console.challenge`, refusals
before a proof, and wrong claim proofs spending the code (a proof made for
another key, and one with the wrong code). They check `console.claim`
with both bind MACs, and `console.auth` and its refusals:
- a replay of the last proof on the same connection;
- the other direction's label;
- another connection;
- a short nonce;
- the right string signed by another key.

Each refused connection must stay unproved, or be closed by the server.
Because three refused proofs in ten minutes unpair a console, the
signature refusals and the thief run late, each group on a fresh pairing
with at most two failures. The suite also paces `console.challenge` at one a
second per connection. The subtests then check the
console event about this console, a thief in another process refused, `card.get`'s hash over kind, agent, tool, title, note and body,
`card.answer` with a changed hash, another connection's nonce, a spent
nonce and the settled event (result `denied`, by `console`, no
`text_sha256`; result and by from their closed sets), the read timer on a long card, the replay of waiting cards after
`console.events` (its reply's `cards` lists the pending set, and exactly
those come as `new` after `connected` and before any live event), the cap of six with `console_in_use`,
`console.unpair {}` refused without a proof, `console.unpair {"all":true}`
without a proof and its 10 s limit, and the 30 s cooldown after a decline.

Changes are made upstream and the pin above bumped; a copy is replaced,
never edited.

## Licence

MIT License

Copyright (c) 2026 Martin Granström

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## Limits of this suite

If the console link drops while an agent drives the desktop, the glow and Stop are gone until it rejoins, and input continues (Den's input gate is a tracked follow-up).
