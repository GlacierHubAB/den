package remote

import (
	"path/filepath"
	"strings"
	"testing"
)

// What remotes.toml changes for a host applies at once when it trusts the
// host less, and waits for Connect when it trusts it more; a new target
// goes under the lower of the two policies.
func TestReloadLowersAtOnceAndRaisesOnConnect(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Hosts: []Host{{Label: "b", Target: "b", Policy: Approve, Labels: false, Enabled: true}}}
	file := cfg
	m := NewManager(cfg, Options{StateDir: dir, EventsPath: filepath.Join(dir, "events.jsonl"), Controller: "c",
		Reload: func() (Config, error) { return file, nil }})
	defer m.Close()
	set := func(h Host) { h.Label, h.Enabled = "b", true; file = Config{Hosts: []Host{h}} }
	got := func() (Host, HostStatus) { return m.hostNow("b"), m.Hosts()[0] }

	set(Host{Target: "b", Policy: Full, Labels: true})
	m.reload("", "")
	if h, s := got(); h.Policy != Approve || h.Labels || s.Policy != Approve || s.Pending != "remotes.toml asks for full and turns labels on; /remote connect b to apply" {
		t.Errorf("raise applied by a reload: %+v %q", h, s.Pending)
	}
	set(Host{Target: "b", Policy: Approve, Labels: true})
	m.reload("", "")
	if h, s := got(); h.Labels || s.Pending != "remotes.toml turns labels on; /remote connect b to apply" {
		t.Errorf("labels on applied by a reload: %+v %q", h, s.Pending)
	}
	// Another target or den_path is a raise too: held, the name cleaned.
	set(Host{Target: "user@other\x1b[2J", Policy: Approve})
	m.reload("", "")
	if h, s := got(); h.Target != "b" || !m.isHeld("b") || s.Target != "b" || s.Pending != "remotes.toml points b at a target with characters Den cannot show, which it does not apply; edit remotes.toml" {
		t.Errorf("a new target: %+v held %v %q", h, m.isHeld("b"), s.Pending)
	}
	set(Host{Target: "b", DenPath: "/tmp/den", Policy: Approve})
	m.reload("", "")
	if h, s := got(); h.DenPath != "" || !m.isHeld("b") || s.Pending != "remotes.toml sets b's den_path to /tmp/den; /remote connect b to apply" {
		t.Errorf("a new den_path: %+v %q", h, s.Pending)
	}
	m.reload("other", "") // Connect for another host
	if m.isHeld("b") != true || m.hostNow("b").DenPath != "" {
		t.Error("released by another host's connect")
	}
	set(Host{Target: "b", Policy: Approve})
	m.reload("", "")
	if m.isHeld("b") || m.Hosts()[0].Pending != "" {
		t.Errorf("held after the file named the old target again: %q", m.Hosts()[0].Pending)
	}
	set(Host{Target: "user@other", Policy: Observe})
	m.reload("", "")
	if h, s := got(); h.Policy != Observe || s.Policy != Observe || !m.isHeld("b") {
		t.Errorf("a lower policy with a new target: %+v %q", h, s.Pending)
	}
	set(Host{Target: "user@other", Policy: Full, Labels: true})
	m.reload("other", "") // Connect for another host
	if h, _ := got(); h.Policy != Observe {
		t.Errorf("raised by another host's connect: %+v", h)
	}
	// Connect applies only the raise the human was shown: one the file
	// asks for since they looked stays pending.
	seen := m.Hosts()[0].Pending
	m.reload("b", "remotes.toml asks for full; /remote connect b to apply")
	if h, s := got(); h.Policy != Observe || h.Target != "b" || s.Pending != seen {
		t.Errorf("Connect applied a raise other than the one shown: %+v %q", h, s.Pending)
	}
	m.reload("b", "")
	if h, _ := got(); h.Policy != Observe || h.Target != "b" {
		t.Errorf("Connect with no raise shown applied one: %+v", h)
	}
	// A target that cannot be shown whole (too long, or with characters
	// sanitising would drop) never applies: two such targets could read
	// alike.
	for _, long := range []string{"user@" + strings.Repeat("a", 300), "user@oth\u200ber"} {
		set(Host{Target: long, Policy: Observe})
		m.reload("", "")
		shown := m.Hosts()[0].Pending
		if !strings.Contains(shown, "which it does not apply; edit remotes.toml") && !strings.Contains(shown, "which Den does not apply; edit remotes.toml") {
			t.Errorf("pending %q", shown)
		}
		m.reload("b", shown)
		if h, _ := got(); h.Target == long {
			t.Errorf("a target not shown whole applied: %q", shown)
		}
	}
	// Two targets alike but for their end are shown whole: the raise
	// shown for one never applies the other.
	prefix := "user@" + strings.Repeat("h", 74)
	set(Host{Target: prefix + "-one", Policy: Observe})
	m.reload("", "")
	seenOne := m.Hosts()[0].Pending
	set(Host{Target: prefix + "-two", Policy: Observe})
	m.reload("b", seenOne)
	if h, s := got(); h.Target != "b" || !strings.Contains(s.Pending, prefix+"-two;") {
		t.Errorf("a raise shown for %s applied %s: %+v %q", prefix+"-one", prefix+"-two", h, s.Pending)
	}
	set(Host{Target: "user@other", Policy: Full, Labels: true})
	m.reload("", "")
	seen = m.Hosts()[0].Pending
	m.reload("b", seen)
	if h, s := got(); h.Policy != Full || !h.Labels || h.Target != "user@other" || m.isHeld("b") || s.Policy != Full || s.Pending != "" {
		t.Errorf("Connect did not apply the raise: %+v %q", h, s.Pending)
	}
}
