package ui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/theme"
)

// With its real duration, a flash stays in the top bar until its time is up
// and then clears; the other tests run it with none (TestMain).
func TestFlashStaysUpForItsTime(t *testing.T) {
	old := flashFor
	flashFor = flashDefault
	t.Cleanup(func() { flashFor = old })
	st := newStyles(theme.Deepharness)
	m := Model{st: st, agents: sample(), store: inbox.New(), width: 120, height: 36, tab: tabFleet}
	m.input = newInput(st)
	mm, cmd := m.say("Delivered to Mossy Lemur", false)
	m = mm.(Model)
	if m.flash != "Delivered to Mossy Lemur" || cmd == nil {
		t.Fatalf("flash %q, cmd %v", m.flash, cmd)
	}
	start := time.Now()
	fired := make(chan tea.Msg, 1)
	go func() { fired <- cmd() }()
	select {
	case msg := <-fired:
		t.Fatalf("the flash cleared after %v, before its %v: %#v", time.Since(start), flashDefault, msg)
	case <-time.After(flashDefault / 2):
	}
	var msg tea.Msg
	select {
	case msg = <-fired:
	case <-time.After(flashDefault):
		t.Fatalf("the flash did not clear within %v", time.Since(start))
	}
	if e := time.Since(start); e < flashDefault-50*time.Millisecond {
		t.Errorf("the flash cleared after %v, before its %v", e, flashDefault)
	}
	mm, _ = m.Update(msg)
	if m = mm.(Model); m.flash != "" {
		t.Errorf("the flash is still %q after its time", m.flash)
	}
}
