# denconformance

A conformance suite for servers of the Den API: the local JSON-lines socket
(`den-api.sock`) a desktop shell such as Drey uses to show Den's fleet,
inbox and chat. It needs only the standard library and a dialer, so a
server's own repository can carry a copy as a test-only package and run it.

Exported at Drey commit `5b1805e89d9508e0296891cae22bca5c7b472ea7`, package SHA-256 `97c43fcbf3be867befd36777c431b957ff0ab89674c8fc82c0b3068b63bba17f`.

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
