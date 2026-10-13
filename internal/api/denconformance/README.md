# denconformance

A conformance suite for servers of the Den API: the local JSON-lines socket
(`den-api.sock`) a desktop shell such as Drey uses to show Den's fleet,
inbox and chat. It needs only the standard library and a dialer, so a
server's own repository can carry a copy as a test-only package and run it.

Source: github.com/GlacierHubAB/drey, `pkg/denconformance`.
Exported at Drey commit `613e65b4cf33bfcfd38456c8f7a6d6ee88a55cc0`, package SHA-256 `d251218a9b2f46804bbca41e47439d2e783b990a5711aa979b1f5e9bb40b075f`.

The package SHA-256 is computed over the Go files in name order:

```
cd pkg/denconformance && sha256sum $(ls *.go | sort) | sha256sum
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

Options: `Dial` (required), `DialAsOtherUser` (a connection the server
must refuse; the peer-credential subtest is skipped without it),
`DialAsOtherExe` (a connection from another program of the same user,
whose `ask.cancel` of this program's turn must be `not_found`; that check
is skipped without it),
`Timeout` (per subtest, default 5 s), `Focusable` (an agent or item id
focus must accept; found from the fleet otherwise) and `Skip` (subtest
names; `Subtests` lists them all). Each subtest is named after the verb
it checks or the property it checks (`hello first`, `unknown verb`,
`oversized line`, `second client`, `peer credentials`).

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
