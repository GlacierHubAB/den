# denconformance

A conformance suite for servers of the Den API: the local JSON-lines socket
(`den-api.sock`) a desktop shell such as Drey uses to show Den's fleet,
inbox and chat. It needs only the standard library and a dialer, so a
server's own repository can carry a copy as a test-only package and run it.

Exported at Drey commit `030b342ee2f527f95d58ab04006360020bebea35`, package SHA-256 `3fc9017eed5116b84f2a3982e1713eb75278fac6d344cf33acb1eb1298cf0c20`.

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

What only the server's own test can do is passed in:
- `Offer` starts a console pairing and returns its eight words. Required.
- `Confirm` stands in for the human's ^Y on the console card. Left nil,
  the subtests that need a confirmed console are skipped.
- `Decline` stands in for ^N. Left nil, `console cooldown` is skipped.
- `Card` raises a pending card the dock may answer, with "deny" among its
  verbs. Left nil, `card.get` and `card.answer` are skipped.
- `LongCard` raises one like it with more than ten body lines. Left nil,
  `card read timer` is skipped.
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
