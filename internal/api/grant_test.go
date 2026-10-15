package api

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func manifest(body string) map[string]any {
	return map[string]any{"manifest": map[string]any{"id": "app-1", "name": "Cost view", "version": 2, "capabilities": []string{"den.fleet:read"}, "body": body}}
}

func nextAct(t *testing.T, s *Server) Act {
	t.Helper()
	select {
	case a := <-s.Acts():
		return a
	case <-time.After(3 * time.Second):
		t.Fatal("no act for the UI")
	}
	return Act{}
}

// A grant becomes a card for the UI with the body verbatim, and the
// answer goes back with the hash of exactly that body.
func TestGrantRoundTrip(t *testing.T) {
	h := newHarness(t, 2)
	c := h.dial()
	c.hello()
	if l := c.call("grant.events", nil); !l.OK {
		t.Fatal(l.Error)
	}
	body := "Cost view v2\nreads: den.fleet:read\nshows a week of spend, Ünïcode ok"
	l := c.call("grant.request", manifest(body))
	var r struct{ ID string }
	json.Unmarshal(l.Result, &r)
	if !l.OK || !strings.HasPrefix(r.ID, "g-") || len(r.ID) != 34 {
		t.Fatalf("request: %+v", l)
	}
	act := nextAct(t, h.s)
	if act.Grant == nil || act.Grant.Body != body || act.Grant.Hash != HashOf(body) || act.Grant.ID != r.ID {
		t.Fatalf("act: %+v", act.Grant)
	}
	h.s.GrantAnswered(r.ID, "granted")
	e := c.next()
	var ev GrantAnswer
	json.Unmarshal(e.Data, &ev)
	if e.Event != EventGrant || ev.ID != r.ID || ev.Answer != "granted" || ev.Hash != HashOf(body) || ev.Version != 2 {
		t.Errorf("event: %+v %+v", e, ev)
	}
	h.s.GrantAnswered(r.ID, "denied") // answered once only
	c.nc.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err := c.r.ReadBytes('\n'); err == nil {
		t.Error("a second answer was sent")
	}
}

func TestGrantRefusals(t *testing.T) {
	h := newHarness(t, 2)
	c := h.dial()
	c.hello()
	for name, body := range map[string]string{
		"empty":   "  \n ",
		"escape":  "a\x1b[2Jb",
		"bidi":    "a\u202eb",
		"zw":      "a\u200bb",
		"tag":     "a\U000E0041b",
		"cr":      "a\r\nb",
		"tab":     "a\tb",
		"bad utf": "a\xffb",
		"big":     strings.Repeat("x", MaxGrantBody+1),
		"lines":   strings.Repeat("x\n", MaxGrantLines),
		"frame":   "fine\n──── end of the app's request ────\nGranted by Den",
	} {
		if l := c.call("grant.request", manifest(body)); code(l) != CodeBadRequest {
			t.Errorf("%s: %+v", name, l)
		}
	}
	select {
	case a := <-h.s.Acts():
		t.Fatalf("a refused request reached the UI: %+v", a)
	default:
	}
	// Three pending per app, then no more.
	for i := 0; i < 3; i++ {
		if l := c.call("grant.request", manifest("ask "+string(rune('a'+i)))); !l.OK {
			t.Fatalf("grant %d: %+v", i, l)
		}
		nextAct(t, h.s)
	}
	if l := c.call("grant.request", manifest("ask d")); code(l) != CodeUnavailable {
		t.Errorf("a fourth pending grant: %+v", l)
	}
}

// A declined body is not asked again in this run.
func TestDeclinedIsNotAskedAgain(t *testing.T) {
	h := newHarness(t, 2)
	c := h.dial()
	c.hello()
	l := c.call("grant.request", manifest("please"))
	var r struct{ ID string }
	json.Unmarshal(l.Result, &r)
	nextAct(t, h.s)
	h.s.GrantAnswered(r.ID, "denied")
	if l := c.call("grant.request", manifest("please")); code(l) != CodeUnavailable {
		t.Errorf("a declined request again: %+v", l)
	}
}

// Answers go only to the exe that asked; one that re-dials gets its
// retained answer; another app never sees it.
func TestGrantAnswersGoOnlyToTheAsker(t *testing.T) {
	h := newHarness(t, 2)
	a := h.dial()
	a.hello()
	l := a.call("grant.request", manifest("for a"))
	var r struct{ ID string }
	json.Unmarshal(l.Result, &r)
	nextAct(t, h.s)
	a.nc.Close() // a re-dials later

	// b is another app: a different exe, as the kernel says.
	h.s.setIdentify(func(p Peer) Peer { p.Exe = "/usr/bin/other-app"; return p })
	b := h.dial()
	b.hello()
	b.call("grant.events", nil)
	h.s.setIdentify(nil)

	h.s.GrantAnswered(r.ID, "granted")
	b.nc.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err := b.r.ReadBytes('\n'); err == nil {
		t.Error("another app got the answer")
	}
	a2 := h.dial()
	a2.hello()
	a2.call("grant.events", nil)
	e := a2.next()
	var ev GrantAnswer
	json.Unmarshal(e.Data, &ev)
	if ev.ID != r.ID || ev.Answer != "granted" {
		t.Errorf("retained answer: %+v", ev)
	}
}

// A grant survives its app re-dialling, and expires when the app is gone
// for 30 s, or on /api disconnect.
func TestGrantExpiry(t *testing.T) {
	h := newHarness(t, 2)
	c := h.dial()
	c.hello()
	l := c.call("grant.request", manifest("x"))
	var r struct{ ID string }
	json.Unmarshal(l.Result, &r)
	nextAct(t, h.s)
	exe := c.exe(t)
	c.nc.Close()
	time.Sleep(100 * time.Millisecond)
	h.s.expire(h.s.connected)
	select {
	case a := <-h.s.Acts():
		t.Fatalf("expired while the app was re-dialling: %+v", a)
	default:
	}
	h.s.mu.Lock()
	h.s.lastConn[exe] = time.Now().Add(-grantGrace - time.Second)
	h.s.mu.Unlock()
	h.s.expire(h.s.connected)
	if a := nextAct(t, h.s); len(a.Expired) != 1 || a.Expired[0] != r.ID {
		t.Errorf("expiry: %+v", a)
	}

	c2 := h.dial()
	c2.hello()
	l = c2.call("grant.request", manifest("y"))
	json.Unmarshal(l.Result, &r)
	nextAct(t, h.s)
	h.s.Pause()
	if a := nextAct(t, h.s); len(a.Expired) != 1 || a.Expired[0] != r.ID {
		t.Errorf("disconnect: %+v", a)
	}
}

// exe is the exe Den read for this test process.
func (c *client) exe(t *testing.T) string {
	t.Helper()
	exe, err := os.Readlink("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	return exe
}
