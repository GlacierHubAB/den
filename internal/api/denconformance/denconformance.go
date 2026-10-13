// Copyright (c) 2026 Martin Granström
// SPDX-License-Identifier: MIT
// This file is part of the Den API conformance suite; the MIT licence text
// is in README.md beside it.

// Package denconformance checks a server of the Den API, the local JSON-lines
// socket a shell such as Drey uses to show Den's fleet, inbox and chat,
// against its wire contract. It has no dependencies beyond the standard
// library, so a server's own repository can carry a copy and run it:
//
//	func TestDreyInterface(t *testing.T) {
//		denconformance.Run(t, denconformance.Options{
//			Dial: func(ctx context.Context) (net.Conn, error) {
//				var d net.Dialer
//				return d.DialContext(ctx, "unix", socketPath)
//			},
//			Skip: denconformance.D1Skip,
//		})
//	}
//
// Each subtest is named after the verb it checks (fleet.snapshot,
// inbox.body, ask, …) or the property (hello first, oversized line,
// second client, peer credentials); Options.Skip names the ones the server
// does not serve yet.
package denconformance

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// Dialer opens one connection to the server under test.
type Dialer func(ctx context.Context) (net.Conn, error)

// Options configures a run.
type Options struct {
	// Dial opens a connection as the current user.
	Dial Dialer
	// DialAsOtherUser, when set, opens a connection the server must refuse
	// (a different uid). Left nil, the peer-credential subtest is skipped.
	DialAsOtherUser Dialer
	// Timeout bounds each subtest. Default 5 s.
	Timeout time.Duration
	// Focusable is an agent id or name, or an inbox item id, focus must
	// accept. Default: the first agent of fleet.snapshot, else the first
	// inbox item.
	Focusable string
	// Skip names subtests not to run (D1Skip for Den's first release).
	Skip []string
}

// Subtests lists every subtest name, in the order Run runs them.
var Subtests = []string{
	"hello", "hello first", "unknown verb", "oversized line",
	"fleet.snapshot", "inbox.list", "inbox.body", "focus",
	"ask", "chat.sessions", "chat.history",
	"mission.list", "mission.start", "mission.drop",
	"view.data", "grant.request", "grant.events", "theme.get",
	"fleet.events", "inbox.events", "second client", "peer credentials",
}

// Run executes the suite as subtests of t.
func Run(t *testing.T, o Options) {
	t.Helper()
	if o.Timeout == 0 {
		o.Timeout = 5 * time.Second
	}
	skip := map[string]bool{}
	for _, name := range o.Skip {
		skip[name] = true
	}
	known := map[string]bool{}
	for _, name := range Subtests {
		known[name] = true
	}
	for _, name := range o.Skip {
		if !known[name] {
			t.Errorf("Options.Skip names %q, which is not a subtest (Subtests lists them)", name)
		}
	}
	run := func(name string, f func(t *testing.T, ctx context.Context, cl *client)) {
		t.Run(name, func(t *testing.T) {
			if skip[name] {
				t.Skipf("%s is in Options.Skip", name)
			}
			ctx, cancel := context.WithTimeout(context.Background(), o.Timeout)
			defer cancel()
			cl := &client{dial: o.Dial}
			defer cl.close()
			f(t, ctx, cl)
		})
	}
	// State one subtest leaves for a later one.
	var (
		focusable = o.Focusable
		session   string
		cardID    string
		items     []item
		missionID string
	)

	run("hello", func(t *testing.T, ctx context.Context, cl *client) {
		c, in, err := open(ctx, o.Dial)
		if err != nil {
			t.Fatal(err)
		}
		c.close()
		if in.Version == "" || !in.supports(V) {
			t.Fatalf("hello: version %q, versions %v (want %d among them)", in.Version, in.Versions, V)
		}
	})

	run("hello first", func(t *testing.T, ctx context.Context, cl *client) {
		nc := dialRaw(t, ctx, o.Dial)
		defer func() { _ = nc.Close() }()
		rep := raw(t, nc, `{"v":1,"id":1,"op":"fleet.snapshot"}`)
		if rep.OK || rep.Error == nil || rep.Error.Code != CodeHelloFirst {
			t.Fatalf("an op before hello must fail with %s: %+v", CodeHelloFirst, rep)
		}
	})

	run("unknown verb", func(t *testing.T, ctx context.Context, cl *client) {
		nc := dialRaw(t, ctx, o.Dial)
		defer func() { _ = nc.Close() }()
		hello(t, nc)
		for i, op := range []string{"inbox.answer", "permission.allow", "agent.spawn", "chat.ask", "nope"} {
			rep := raw(t, nc, fmt.Sprintf(`{"v":1,"id":%d,"op":%q}`, i+2, op))
			if rep.OK || rep.Error == nil || rep.Error.Code != CodeUnsupported {
				t.Errorf("%s must be %s: %+v", op, CodeUnsupported, rep)
			}
		}
	})

	run("oversized line", func(t *testing.T, ctx context.Context, cl *client) {
		nc := dialRaw(t, ctx, o.Dial)
		defer func() { _ = nc.Close() }()
		hello(t, nc)
		big := `{"v":1,"id":2,"op":"focus","params":{"target":"` + strings.Repeat("x", MaxLine) + `"}}` + "\n"
		_ = nc.SetDeadline(time.Now().Add(o.Timeout))
		_, _ = nc.Write([]byte(big)) // the server may close before reading it all
		r := bufio.NewReaderSize(nc, 64*1024)
		if b, err := r.ReadBytes('\n'); err == nil {
			var l line
			if json.Unmarshal(b, &l) == nil && l.Error != nil && l.Error.Code != CodeTooLarge {
				t.Errorf("want %s, got %+v", CodeTooLarge, l.Error)
			}
		}
		if _, err := r.ReadByte(); err == nil {
			t.Error("the connection is still open after an oversized line")
		}
	})

	run("fleet.snapshot", func(t *testing.T, ctx context.Context, cl *client) {
		var fl fleet
		if err := cl.call(ctx, "fleet.snapshot", nil, &fl); err != nil {
			t.Fatal(err)
		}
		if fl.Taken.IsZero() || fl.Cost.Currency == "" {
			t.Errorf("fleet without taken or cost.currency: %+v", fl)
		}
		for _, a := range fl.Agents {
			if a.ID == "" || a.Name == "" || a.State == "" {
				t.Errorf("agent without id, name or state: %+v", a)
			}
		}
		if focusable == "" && len(fl.Agents) > 0 {
			focusable = fl.Agents[0].ID
		}
	})

	run("inbox.list", func(t *testing.T, ctx context.Context, cl *client) {
		if err := cl.call(ctx, "inbox.list", nil, &items); err != nil {
			t.Fatal(err)
		}
		for _, it := range items {
			if it.ID == "" || it.Kind == "" || it.Digest.Hash == "" || it.Digest.Bytes <= 0 {
				t.Errorf("item without id, kind or digest: %+v", it)
			}
			if strings.Contains(it.Digest.FirstLine, "\n") {
				t.Errorf("digest first line has a newline: %+v", it)
			}
			if focusable == "" {
				focusable = it.ID
			}
		}
	})

	run("inbox.body", func(t *testing.T, ctx context.Context, cl *client) {
		for _, it := range items {
			var b body
			if err := cl.call(ctx, "inbox.body", map[string]string{"id": it.ID}, &b); err != nil {
				t.Errorf("body %s: %v", it.ID, err)
				continue
			}
			if b.Hash != it.Digest.Hash || len(b.Text) != it.Digest.Bytes || HashOf(b.Text) != b.Hash {
				t.Errorf("body %s: hash %s len %d, computed %s, digest %+v", it.ID, b.Hash, len(b.Text), HashOf(b.Text), it.Digest)
			}
		}
		if err := cl.call(ctx, "inbox.body", map[string]string{"id": "no-such-item"}, nil); codeOf(err) != CodeNotFound {
			t.Errorf("an unknown item must be %s: %v", CodeNotFound, err)
		}
	})

	run("focus", func(t *testing.T, ctx context.Context, cl *client) {
		if focusable == "" {
			t.Skip("nothing to focus: no agent and no inbox item")
		}
		if err := cl.call(ctx, "focus", map[string]string{"target": focusable}, nil); err != nil {
			t.Fatal(err)
		}
		if err := cl.call(ctx, "focus", map[string]string{"target": "no-such-target"}, nil); codeOf(err) != CodeNotFound {
			t.Errorf("an unknown target must be %s: %v", CodeNotFound, err)
		}
	})

	run("ask", func(t *testing.T, ctx context.Context, cl *client) {
		chunks, err := cl.ask(ctx, "what did this week cost?")
		if err != nil {
			t.Fatal(err)
		}
		lastSeq, askID, cleared := 0, "", false
		var types []string
		for _, ch := range chunks {
			types = append(types, ch.Type)
			switch ch.Type {
			case "status":
				if ch.Ask == "" || ch.Seq <= lastSeq || (askID != "" && ch.Ask != askID) {
					t.Errorf("status chunk: ask %q seq %d after %d", ch.Ask, ch.Seq, lastSeq)
				}
				if ch.Text != StatusText(ch.Text) {
					t.Errorf("status text is not one plain line of at most %d characters: %q", StatusMax, ch.Text)
				}
				askID, lastSeq, cleared = ch.Ask, ch.Seq, ch.Text == ""
			case "card":
				if ch.Card == nil || ch.Card.ID == "" || len(ch.Card.Actions) == 0 {
					t.Errorf("card chunk without id or actions: %+v", ch)
					continue
				}
				cardID = ch.Card.ID
				for _, a := range ch.Card.Actions {
					if a.ID != "start" && a.ID != "drop" {
						t.Errorf("card action %q is not start or drop", a.ID)
					}
				}
			case "done":
				session = ch.Session
			case "error":
				t.Errorf("error chunk: %s", ch.Message)
			}
		}
		if len(types) == 0 || types[len(types)-1] != "done" || session == "" {
			t.Fatalf("the stream %v must end with done carrying the session (%q)", types, session)
		}
		if lastSeq > 0 && !cleared {
			t.Error("a status line was left standing at done; send an empty status first")
		}
	})

	run("chat.sessions", func(t *testing.T, ctx context.Context, cl *client) {
		var ss []chatSession
		if err := cl.call(ctx, "chat.sessions", nil, &ss); err != nil {
			t.Fatal(err)
		}
		current, found := 0, false
		for _, s := range ss {
			if s.ID == "" || s.Last.IsZero() {
				t.Errorf("session without id or last: %+v", s)
			}
			if s.Current {
				current++
			}
			found = found || s.ID == session
		}
		if current != 1 || (session != "" && !found) {
			t.Errorf("chat.sessions: %d current (want 1), the ask's session listed %v: %+v", current, found, ss)
		}
	})

	run("chat.history", func(t *testing.T, ctx context.Context, cl *client) {
		if session == "" {
			t.Skip("no session from ask")
		}
		var hist []message
		if err := cl.call(ctx, "chat.history", map[string]string{"session": session}, &hist); err != nil {
			t.Fatal(err)
		}
		if len(hist) < 2 || hist[0].Role != "user" {
			t.Errorf("history must hold the user's turn and the reply: %+v", hist)
		}
	})

	run("mission.list", func(t *testing.T, ctx context.Context, cl *client) {
		var ms []mission
		if err := cl.call(ctx, "mission.list", nil, &ms); err != nil {
			t.Fatal(err)
		}
		for _, m := range ms {
			if m.ID == "" || m.Title == "" || m.State == "" {
				t.Errorf("mission without id, title or state: %+v", m)
			}
		}
	})

	run("mission.start", func(t *testing.T, ctx context.Context, cl *client) {
		if cardID == "" {
			t.Skip("no card from ask")
		}
		var before, after []mission
		if err := cl.call(ctx, "mission.list", nil, &before); err != nil {
			t.Fatal(err)
		}
		if err := cl.call(ctx, "mission.start", map[string]string{"session": session, "card": cardID}, nil); err != nil {
			t.Fatal(err)
		}
		if err := cl.call(ctx, "mission.list", nil, &after); err != nil {
			t.Fatal(err)
		}
		if len(after) != len(before)+1 {
			t.Errorf("missions %d -> %d after a start", len(before), len(after))
		} else {
			missionID = after[len(after)-1].ID
		}
		if err := cl.call(ctx, "mission.start", map[string]string{"session": session, "card": "no-such-card"}, nil); codeOf(err) != CodeNotFound {
			t.Errorf("an unknown card must be %s: %v", CodeNotFound, err)
		}
	})

	run("mission.drop", func(t *testing.T, ctx context.Context, cl *client) {
		if missionID == "" {
			t.Skip("no mission from mission.start")
		}
		if err := cl.call(ctx, "mission.drop", map[string]string{"id": missionID}, nil); err != nil {
			t.Fatal(err)
		}
		if err := cl.call(ctx, "mission.drop", map[string]string{"id": "no-such-mission"}, nil); codeOf(err) != CodeNotFound {
			t.Errorf("an unknown mission must be %s: %v", CodeNotFound, err)
		}
	})

	run("view.data", func(t *testing.T, ctx context.Context, cl *client) {
		var v struct {
			View string `json:"view"`
		}
		if err := cl.call(ctx, "view.data", map[string]string{"source": "cost", "query": "this week"}, &v); err != nil {
			t.Fatal(err)
		}
		if v.View == "" {
			t.Error("a view model without a view")
		}
		if err := cl.call(ctx, "view.data", map[string]string{"source": "no-such-source"}, nil); codeOf(err) != CodeNotFound {
			t.Errorf("an unknown source must be %s: %v", CodeNotFound, err)
		}
	})

	const grantBody = "Release checklist v1 asks to read Den's missions"
	run("grant.request", func(t *testing.T, ctx context.Context, cl *client) {
		var res struct {
			ID string `json:"id"`
		}
		m := map[string]any{"id": "app-1", "name": "Release checklist", "version": 1, "capabilities": []string{"den.missions:read"}, "body": grantBody}
		if err := cl.call(ctx, "grant.request", map[string]any{"manifest": m}, &res); err != nil || res.ID == "" {
			t.Fatalf("grant.request: %q %v", res.ID, err)
		}
		var list []item
		if err := cl.call(ctx, "inbox.list", nil, &list); err != nil {
			t.Fatal(err)
		}
		for _, it := range list {
			if it.ID == res.ID && it.Kind == "grant" && it.Digest.Hash == HashOf(grantBody) {
				return
			}
		}
		t.Errorf("grant %s is not in inbox.list as a grant card with the body's hash", res.ID)
	})

	run("grant.events", func(t *testing.T, ctx context.Context, cl *client) {
		ev, err := cl.subscribe(ctx, "grant.events", nil)
		if err != nil {
			t.Fatal(err)
		}
		var res struct {
			ID string `json:"id"`
		}
		m := map[string]any{"id": "app-3", "name": "Stand-up board", "version": 1, "capabilities": []string{"den.fleet:read"}, "body": grantBody}
		if err := cl.call(ctx, "grant.request", map[string]any{"manifest": m}, &res); err != nil {
			t.Fatal(err)
		}
		for e := range ev {
			if e.Event != "grant" {
				continue
			}
			var a grantAnswer
			if json.Unmarshal(e.Data, &a) != nil || a.ID == "" || a.Answer == "" {
				t.Errorf("grant event: %s", e.Data)
				continue
			}
			if a.ID != res.ID {
				continue
			}
			if a.Hash != HashOf(grantBody) {
				t.Errorf("the grant answer's hash %q is not the hash of the body sent", a.Hash)
			}
			return
		}
		t.Error("no grant event for the request within the timeout")
	})

	run("theme.get", func(t *testing.T, ctx context.Context, cl *client) {
		var p map[string]string
		err := cl.call(ctx, "theme.get", nil, &p)
		if codeOf(err) == CodeUnsupported {
			return // optional
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(p) == 0 {
			t.Error("an empty palette")
		}
	})

	run("fleet.events", func(t *testing.T, ctx context.Context, cl *client) {
		ev, err := cl.subscribe(ctx, "fleet.events", nil)
		if err != nil {
			t.Fatal(err)
		}
		for e := range ev {
			if e.Event != "fleet" {
				continue
			}
			var f fleet
			if err := json.Unmarshal(e.Data, &f); err != nil || f.Taken.IsZero() {
				t.Errorf("the first fleet event must be a snapshot: %s", e.Data)
			}
			return
		}
		t.Error("no fleet event after subscribing (Den sends a snapshot first)")
	})

	run("inbox.events", func(t *testing.T, ctx context.Context, cl *client) {
		ev, err := cl.subscribe(ctx, "inbox.events", nil)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]any{"id": "app-2", "name": "Two", "version": 1, "capabilities": []string{"den.cost:read"}, "body": "Two v1 asks to read Den's cost"}
		if err := cl.call(ctx, "grant.request", map[string]any{"manifest": m}, nil); err != nil {
			t.Fatal(err)
		}
		for e := range ev {
			if e.Event == "inbox" {
				return
			}
		}
		t.Error("no inbox event after a grant card was added")
	})

	run("second client", func(t *testing.T, ctx context.Context, cl *client) {
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				other := &client{dial: o.Dial}
				defer other.close()
				for j := 0; j < 5; j++ {
					if err := other.call(ctx, "fleet.snapshot", nil, nil); err != nil {
						errs <- err
						return
					}
					if err := other.call(ctx, "inbox.list", nil, nil); err != nil {
						errs <- err
						return
					}
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Error(err)
		}
	})

	run("peer credentials", func(t *testing.T, ctx context.Context, cl *client) {
		if o.DialAsOtherUser == nil {
			t.Skip("no DialAsOtherUser: the same-uid check is not exercised")
		}
		nc, err := o.DialAsOtherUser(ctx)
		if err != nil {
			return // refused at connect: fine
		}
		defer func() { _ = nc.Close() }()
		_ = nc.SetDeadline(time.Now().Add(o.Timeout))
		_, _ = nc.Write([]byte(`{"v":1,"id":1,"op":"hello","params":{"v":1,"client":"x","version":"y"}}` + "\n"))
		r := bufio.NewReader(nc)
		if b, err := r.ReadBytes('\n'); err == nil {
			var l line
			if json.Unmarshal(b, &l) == nil && l.OK != nil && *l.OK {
				t.Fatal("another user's hello was accepted")
			}
		}
		if _, err := r.ReadByte(); err == nil {
			t.Error("a connection from another user stayed open")
		}
	})
}

func dialRaw(t *testing.T, ctx context.Context, dial Dialer) net.Conn {
	t.Helper()
	nc, err := dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return nc
}

func hello(t *testing.T, nc net.Conn) {
	t.Helper()
	if rep := raw(t, nc, `{"v":1,"id":1,"op":"hello","params":{"v":1,"client":"conformance","version":"test"}}`); !rep.OK {
		t.Fatal(rep.Error)
	}
}

// raw writes one line and returns the next reply, skipping events.
func raw(t *testing.T, nc net.Conn, req string) reply {
	t.Helper()
	_ = nc.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := nc.Write([]byte(req + "\n")); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(nc)
	for {
		b, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var l line
		if err := json.Unmarshal(b, &l); err != nil {
			t.Fatalf("not a JSON line %q: %v", b, err)
		}
		if l.Event == "" && l.ID != nil && l.OK != nil {
			return reply{OK: *l.OK, Error: l.Error}
		}
	}
}
