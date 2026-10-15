package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A connection past the per-process cap, whose client has already written
// its first line, reads why it was refused rather than a reset; and once
// a connection closes there is room again.
func TestTurnedAwayReadsWhy(t *testing.T) {
	h := newHarness(t, 1)
	var held []*client
	for range maxConnsPerPID {
		held = append(held, h.dial())
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(h.s.Clients()) < maxConnsPerPID && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := len(h.s.Clients()); n != maxConnsPerPID {
		t.Fatalf("%d connections served, want %d", n, maxConnsPerPID)
	}
	for i := range 3 {
		c := h.dial()
		c.send("hello", map[string]any{"v": 1, "client": "test", "version": "0"})
		c.nc.SetReadDeadline(time.Now().Add(2 * time.Second))
		raw, err := c.r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("refused client %d: %v", i, err)
		}
		var r Reply
		if json.Unmarshal(raw, &r) != nil || r.OK || r.Error == nil || r.Error.Code != CodeUnavailable || !strings.Contains(r.Error.Message, "too many connections") {
			t.Fatalf("refused client %d read %s", i, raw)
		}
	}
	if n := len(h.s.Clients()); n != maxConnsPerPID {
		t.Errorf("a refused connection counts: %d", n)
	}
	held[0].nc.Close()
	for len(h.s.Clients()) >= maxConnsPerPID && time.Now().Before(deadline.Add(2*time.Second)) {
		time.Sleep(5 * time.Millisecond)
	}
	c := h.dial()
	c.send("hello", map[string]any{"v": 1, "client": "test", "version": "0"})
	c.nc.SetReadDeadline(time.Now().Add(2 * time.Second))
	raw, err := c.r.ReadBytes('\n')
	if err != nil || strings.Contains(string(raw), "too many connections") {
		t.Errorf("after a close: %s %v", raw, err)
	}
}
