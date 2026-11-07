package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pairedEvents is a paired console with a proved connection subscribed to
// its events, the first (connected) already read.
func pairedEvents(t *testing.T) (*harness, *dock, consolePaired, *client) {
	t.Helper()
	h, _ := consoleHarness(t)
	d := newDock(t, h)
	p := d.pair(h.s.StartConsoleCode())
	c := h.dial()
	if l := c.call("hello", map[string]any{"v": 1, "client": "test", "version": "0", "card_hash": 2}); !l.OK {
		t.Fatalf("hello: %+v", l.Error)
	}
	if l, _, _, _ := d.auth(c, p); !l.OK {
		t.Fatalf("auth: %+v", l.Error)
	}
	c.call("console.events", nil)
	skipSnapshot(t, h.s, c)
	return h, d, p, c
}

// unpairedEvent reads c until the console's unpaired event.
func unpairedEvent(t *testing.T, c *client) ConsoleEvent {
	t.Helper()
	for {
		l := c.next()
		if l.Event != EventConsole {
			continue
		}
		var ev ConsoleEvent
		if err := json.Unmarshal(l.Data, &ev); err != nil {
			t.Fatalf("console event %s: %v", l.Data, err)
		}
		if ev.Kind == "unpaired" {
			return ev
		}
	}
}

// Every way the console stops being paired says why, in the event's cause,
// whatever its display text.
func TestUnpairedEventNamesItsCause(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause string
		run   func(t *testing.T) ConsoleEvent
	}{
		{"the console unpairs itself", CauseSelf, func(t *testing.T) ConsoleEvent {
			_, _, _, c := pairedEvents(t)
			c.send("console.unpair", map[string]any{})
			return unpairedEvent(t, c)
		}},
		{"unpair all from the console's own connection", CauseSelf, func(t *testing.T) ConsoleEvent {
			_, _, _, c := pairedEvents(t)
			c.send("console.unpair", map[string]bool{"all": true})
			return unpairedEvent(t, c)
		}},
		{"unpair all from another program", CauseOther, func(t *testing.T) ConsoleEvent {
			h, _, _, c := pairedEvents(t)
			other := h.dial()
			other.hello()
			if l := other.call("console.unpair", map[string]bool{"all": true}); !l.OK {
				t.Fatalf("unpair all: %+v", l.Error)
			}
			return unpairedEvent(t, c)
		}},
		{"unpaired in Den", CauseDen, func(t *testing.T) ConsoleEvent {
			h, _, _, c := pairedEvents(t)
			h.s.UnpairConsole(CauseDen, "")
			return unpairedEvent(t, c)
		}},
		{"Den's guard", CauseGuard, func(t *testing.T) ConsoleEvent {
			h, _, _, c := pairedEvents(t)
			h.s.UnpairConsole(CauseGuard, "Den's guard stopped the orchestrator: the console is unpaired; pair again with den pair")
			ev := unpairedEvent(t, c)
			if ev.Reason != "unpaired in Den" {
				t.Errorf("a long reason is shortened for display: %q", ev.Reason)
			}
			return ev
		}},
		{"the record changed outside Den", CauseRecord, func(t *testing.T) ConsoleEvent {
			h, _, _, c := pairedEvents(t)
			h.s.recordTampered()
			return unpairedEvent(t, c)
		}},
		{"another console is paired", CauseReplaced, func(t *testing.T) ConsoleEvent {
			h, d, p, c := pairedEvents(t)
			_ = p
			cl := h.dial()
			cl.hello()
			ch := d.challenge(cl)
			cl.send("console.claim", map[string]string{"public_key": d.pub, "proof": proof(h.s.StartConsoleCode(), ch.ClaimSalt, d.pub)})
			ask := ctlAct(t, h.s, func(a Act) bool { return a.Console != nil }).Console
			h.s.ConsoleAnswered(ask.ID, true)
			return unpairedEvent(t, c)
		}},
		{"three other programs", CauseIntruders, func(t *testing.T) ConsoleEvent {
			h, d, p, c := pairedEvents(t)
			h.s.setIdentify(func(p Peer) Peer { p.Exe = "/usr/bin/other"; return p })
			for i := 0; i < consoleStrikesMax; i++ {
				other := h.dial()
				other.hello()
				if l, _, _, _ := d.auth(other, p); code(l) != CodeForbidden {
					t.Fatalf("another program %d: %+v", i, l)
				}
			}
			return unpairedEvent(t, c)
		}},
		{"three wrong proofs", CauseProofs, func(t *testing.T) ConsoleEvent {
			h, d, p, c := pairedEvents(t)
			for i := 0; i < consoleStrikesMax; i++ {
				wrong := h.dial()
				wrong.hello()
				d.challenge(wrong)
				sig := make([]byte, 64)
				rand.Read(sig)
				l := wrong.call("console.auth", map[string]string{"pairing": p.ID, "nonce": randHex64(), "sig": hex.EncodeToString(sig)})
				if code(l) != CodeForbidden {
					t.Fatalf("wrong proof %d: %+v", i, l)
				}
			}
			return unpairedEvent(t, c)
		}},
		{"answers faster than anyone could read", CauseBurst, func(t *testing.T) ConsoleEvent {
			h, _, _, c := pairedEvents(t)
			var cards []Card
			for i := 0; i < cardBurst; i++ {
				cards = append(cards, card("u-"+strconv.Itoa(i), 1, true))
			}
			h.s.Publish(Project(Snapshot{Taken: time.Now(), Cards: cards}))
			for range cards {
				c.next() // new
			}
			for i := 0; i < cardBurst; i++ {
				id := answerCard(c, getCard(t, c, "u-"+strconv.Itoa(i)), "allow")
				if i == cardBurst-1 {
					break // refused, and the event follows
				}
				ctlAct(t, h.s, func(a Act) bool { return a.CardAnswer != nil }).CardAnswer.Reply <- CardResult{Result: "allowed"}
				c.replyTo(id)
			}
			return unpairedEvent(t, c)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := tc.run(t)
			if ev.Cause != tc.cause {
				t.Errorf("cause %q, want %q (event %+v)", ev.Cause, tc.cause, ev)
			}
			if !slices.Contains(UnpairCauses, ev.Cause) {
				t.Errorf("cause %q is not one of UnpairCauses %v", ev.Cause, UnpairCauses)
			}
		})
	}
}

// Every cause has a test above, and no test names a cause the contract
// does not list.
func TestEveryCauseIsListedOnce(t *testing.T) {
	want := []string{CauseBurst, CauseProofs, CauseIntruders, CauseSelf, CauseDen, CauseOther, CauseReplaced, CauseRecord, CauseGuard}
	if len(UnpairCauses) != len(want) {
		t.Fatalf("UnpairCauses %v, want %v", UnpairCauses, want)
	}
	seen := map[string]bool{}
	for _, c := range UnpairCauses {
		if seen[c] || !slices.Contains(want, c) || c == "" || c != strings.ToLower(c) {
			t.Errorf("cause %q listed twice, unknown or not lower case", c)
		}
		seen[c] = true
	}
}

// No path unpairs the console without naming a cause: every call of
// UnpairConsole passes one of the Cause constants first, and every
// "unpaired" ConsoleEvent built by hand carries a Cause. Found by reading
// the source, so a call added later is caught too.
func TestEveryUnpairPathNamesACause(t *testing.T) {
	names := map[string]bool{"CauseBurst": true, "CauseProofs": true, "CauseIntruders": true, "CauseSelf": true, "CauseDen": true, "CauseOther": true, "CauseReplaced": true, "CauseRecord": true, "CauseGuard": true}
	known := func(e ast.Expr) bool {
		switch x := e.(type) {
		case *ast.Ident:
			return names[x.Name]
		case *ast.SelectorExpr:
			return names[x.Sel.Name]
		}
		return false
	}
	var calls, events int
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".claude", "mods":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				sel, ok := x.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "UnpairConsole" {
					return true
				}
				calls++
				if len(x.Args) != 2 || !known(x.Args[0]) {
					t.Errorf("%s: UnpairConsole is called without one of the Cause constants first", path)
				}
			case *ast.CompositeLit:
				if id, ok := x.Type.(*ast.Ident); !ok || id.Name != "ConsoleEvent" {
					return true
				}
				var unpaired, cause bool
				for _, el := range x.Elts {
					kv, ok := el.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, _ := kv.Key.(*ast.Ident)
					if key == nil {
						continue
					}
					if lit, ok := kv.Value.(*ast.BasicLit); ok && key.Name == "Kind" && lit.Value == `"unpaired"` {
						unpaired = true
					}
					if key.Name == "Cause" {
						cause = true
						// Either a Cause constant, or the cause UnpairConsole
						// was given, which the call sites above check.
						if id, ok := kv.Value.(*ast.Ident); !ok || id.Name != "cause" {
							if !known(kv.Value) {
								t.Errorf("%s: an unpaired event's Cause is not a Cause constant", path)
							}
						}
					}
				}
				if unpaired {
					events++
					if !cause {
						t.Errorf("%s: an unpaired ConsoleEvent is built without a Cause", path)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The paths there are today: the calls (burst, record, intruders twice,
	// proofs, self, unpair all twice, den twice, guard) and the three events
	// built by hand (UnpairConsole's, a replaced pairing's, and a silent
	// reconnect's).
	if calls < 11 || events < 3 {
		t.Errorf("found %d UnpairConsole calls and %d unpaired events: has the source moved?", calls, events)
	}
}
