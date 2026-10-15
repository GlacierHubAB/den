package api

import (
	"strings"
	"testing"
	"time"
)

// focus resolves only Den's agent ids and names, never focuses an inbox
// item or a remote agent, hands nothing an app wrote to the focuser, and
// is rate-limited.
func TestFocus(t *testing.T) {
	h := newHarness(t, 2)
	h.s.Publish(Project(snapshot()))
	c := h.dial()
	c.hello()
	for target, want := range map[string]string{
		"i-1":                     CodeUnavailable, // an inbox item
		"Remote Ray":              CodeUnavailable,
		"nobody":                  CodeNotFound,
		`address:0x1"); os.exit(`: CodeNotFound,
		"mossy lemur":             CodeNotFound, // exact names only
	} {
		if l := c.call("focus", map[string]string{"target": target}); code(l) != want {
			t.Errorf("%q: %+v", target, l)
		}
	}
	select {
	case a := <-h.s.Acts():
		t.Fatalf("a refused focus reached the UI: %+v", a)
	default:
	}
	id := c.send("focus", map[string]string{"target": "Mossy Lemur"})
	a := nextAct(t, h.s)
	if a.Focus == nil || a.Focus.AgentID != "s-1" {
		t.Fatalf("act %+v", a)
	}
	a.Focus.Reply <- ""
	for {
		l := c.next()
		if l.ID != nil && *l.ID == id {
			if !l.OK || !strings.Contains(string(l.Result), "Mossy Lemur") {
				t.Errorf("focus: %+v", l)
			}
			break
		}
	}
	if l := c.call("focus", map[string]string{"target": "s-2"}); code(l) != CodeUnavailable {
		t.Errorf("a second focus within 3 s: %+v", l)
	}
}

func TestFocusLimit(t *testing.T) {
	var f focusLimit
	now := time.Now()
	if !f.allow("a", now) || f.allow("a", now.Add(time.Second)) || !f.allow("a", now.Add(4*time.Second)) {
		t.Error("per app")
	}
	for i := 0; i < 20; i++ {
		f.allow(string(rune('b'+i)), now.Add(5*time.Second))
	}
	if f.allow("z", now.Add(6*time.Second)) {
		t.Error("over ten a minute")
	}
}
