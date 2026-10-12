package remote

import (
	"path/filepath"
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
	m.reload("")
	if h, s := got(); h.Policy != Approve || h.Labels || s.Policy != Approve || s.Pending != "remotes.toml asks for full and turns labels on; /remote connect b to apply" {
		t.Errorf("raise applied by a reload: %+v %q", h, s.Pending)
	}
	set(Host{Target: "b", Policy: Approve, Labels: true})
	m.reload("")
	if h, s := got(); h.Labels || s.Pending != "remotes.toml turns labels on; /remote connect b to apply" {
		t.Errorf("labels on applied by a reload: %+v %q", h, s.Pending)
	}
	// Another target or den_path is a raise too: held, the name cleaned.
	set(Host{Target: "user@other\x1b[2J", Policy: Approve})
	m.reload("")
	if h, s := got(); h.Target != "b" || !m.isHeld("b") || s.Target != "b" || s.Pending != "remotes.toml points b at user@other; /remote connect b to apply" {
		t.Errorf("a new target: %+v held %v %q", h, m.isHeld("b"), s.Pending)
	}
	set(Host{Target: "b", DenPath: "/tmp/den", Policy: Approve})
	m.reload("")
	if h, s := got(); h.DenPath != "" || !m.isHeld("b") || s.Pending != "remotes.toml sets b's den_path to /tmp/den; /remote connect b to apply" {
		t.Errorf("a new den_path: %+v %q", h, s.Pending)
	}
	m.reload("other") // Connect for another host
	if m.isHeld("b") != true || m.hostNow("b").DenPath != "" {
		t.Error("released by another host's connect")
	}
	set(Host{Target: "b", Policy: Approve})
	m.reload("")
	if m.isHeld("b") || m.Hosts()[0].Pending != "" {
		t.Errorf("held after the file named the old target again: %q", m.Hosts()[0].Pending)
	}
	set(Host{Target: "user@other", Policy: Observe})
	m.reload("")
	if h, s := got(); h.Policy != Observe || s.Policy != Observe || !m.isHeld("b") {
		t.Errorf("a lower policy with a new target: %+v %q", h, s.Pending)
	}
	set(Host{Target: "user@other", Policy: Full, Labels: true})
	m.reload("other") // Connect for another host
	if h, _ := got(); h.Policy != Observe {
		t.Errorf("raised by another host's connect: %+v", h)
	}
	m.reload("b")
	if h, s := got(); h.Policy != Full || !h.Labels || h.Target != "user@other" || m.isHeld("b") || s.Policy != Full || s.Pending != "" {
		t.Errorf("Connect did not apply the raise: %+v %q", h, s.Pending)
	}
}
