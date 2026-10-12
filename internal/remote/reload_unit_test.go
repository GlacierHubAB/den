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
	if h, s := got(); h.Policy != Approve || h.Labels || s.Policy != Approve || s.Pending != "remotes.toml asks for full and labels on; /remote connect b to apply" {
		t.Errorf("raise applied by a reload: %+v %q", h, s.Pending)
	}
	set(Host{Target: "b", Policy: Approve, Labels: true})
	m.reload("")
	if h, s := got(); h.Labels || s.Pending != "remotes.toml turns labels on; /remote connect b to apply" {
		t.Errorf("labels on applied by a reload: %+v %q", h, s.Pending)
	}
	set(Host{Target: "user@other", Policy: Full})
	m.reload("")
	if h, s := got(); h.Target != "user@other" || h.Policy != Approve || s.Target != "user@other" {
		t.Errorf("a new target: %+v", h)
	}
	set(Host{Target: "user@other", Policy: Observe})
	m.reload("")
	if h, s := got(); h.Policy != Observe || s.Policy != Observe || s.Pending != "" {
		t.Errorf("a lower policy: %+v %q", h, s.Pending)
	}
	set(Host{Target: "user@other", Policy: Full, Labels: true})
	m.reload("other") // Connect for another host
	if h, _ := got(); h.Policy != Observe {
		t.Errorf("raised by another host's connect: %+v", h)
	}
	m.reload("b")
	if h, s := got(); h.Policy != Full || !h.Labels || s.Policy != Full || s.Pending != "" {
		t.Errorf("Connect did not apply the raise: %+v %q", h, s.Pending)
	}
}
