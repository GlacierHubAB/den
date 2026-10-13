package ui

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/inbox"
)

// apiEnv gives a test its own runtime folder, state folder and config.
func apiEnv(t *testing.T) (cfgPath, run string) {
	t.Helper()
	run = filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(run, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", run)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	cfgPath = filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("DEN_CONFIG", cfgPath)
	return cfgPath, run
}

func apiItem(m *Model) *inbox.Item {
	for _, it := range m.store.Items() {
		if it.Kind == inbox.APIOn && it.State == inbox.Waiting {
			return it
		}
	}
	return nil
}

// /api on asks first; only the human's ^Y turns it on and writes the
// config; /api off drops apps at once and keeps the path Den's.
func TestAPIOnNeedsTheHumansYes(t *testing.T) {
	_, run := apiEnv(t)
	m := testModel(140, 50)
	if m.apiChip() != "" {
		t.Error("a chip while the API is off")
	}
	mm, _ := m.apiCommand([]string{"on"})
	m = mm.(Model)
	it := apiItem(&m)
	if it == nil || m.api.srv != nil || config.Load().API.Enabled {
		t.Fatal("/api on must only ask")
	}
	if !strings.Contains(strings.Join(it.Body, " "), "costs money") {
		t.Errorf("the question does not say what it enables: %q", it.Body)
	}
	// The request path agents' hooks use cannot answer it.
	if m.store.Answer(it, "allow") {
		t.Fatal("the request path answered /api on")
	}
	readToEnd(t, &m, it)
	answerGuidance(&m, "allow")
	if m.api.srv == nil || !config.Load().API.Enabled {
		t.Fatalf("^Y did not turn it on: %q", m.flash)
	}
	defer m.api.srv.Close()
	sock := filepath.Join(run, "den-api.sock")
	if fi, err := os.Lstat(sock); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket: %v %v", fi, err)
	}
	if m.apiChip() == "" {
		t.Error("no chip with the API on")
	}
	mm, _ = m.apiCommand([]string{"off"})
	m = mm.(Model)
	if config.Load().API.Enabled || !m.api.srv.Paused() || m.apiChip() != "" {
		t.Error("/api off")
	}
	if _, err := os.Lstat(sock); err != nil {
		t.Error("/api off gave up the path")
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	n, _ := c.Read(buf)
	c.Close()
	if !strings.Contains(string(buf[:n]), `"unavailable"`) {
		t.Errorf("a client after /api off got %q", buf[:n])
	}
}

// ^N leaves the API off.
func TestAPIOnDeclined(t *testing.T) {
	apiEnv(t)
	m := testModel(140, 50)
	mm, _ := m.apiCommand([]string{"on"})
	m = mm.(Model)
	readToEnd(t, &m, apiItem(&m))
	answerGuidance(&m, "deny")
	if m.api.srv != nil || config.Load().API.Enabled {
		t.Error("^N turned the API on")
	}
}
