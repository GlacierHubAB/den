package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
)

// Den's console key is made once, 0600 and 32 bytes, and read back the
// same; a key anyone else could read, a link, or one of the wrong size is
// refused, never trusted.
func TestConsoleKeyFile(t *testing.T) {
	state := t.TempDir()
	k1, err := loadConsoleKey(state)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, consoleKeyFile)
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 || fi.Size() != 32 {
		t.Errorf("key file %v, %d bytes", fi.Mode(), fi.Size())
	}
	k2, err := loadConsoleKey(state)
	if err != nil || !k1.Equal(k2) {
		t.Errorf("not read back the same: %v", err)
	}
	os.Chmod(path, 0o644)
	if _, err := loadConsoleKey(state); err == nil {
		t.Error("a key others can read was used")
	}
	os.Chmod(path, 0o600)
	os.WriteFile(path, []byte("short"), 0o600)
	if _, err := loadConsoleKey(state); err == nil {
		t.Error("a key of the wrong size was used")
	}
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "real"), make([]byte, 32), 0o600)
	os.Symlink(filepath.Join(other, "real"), filepath.Join(other, consoleKeyFile))
	if _, err := loadConsoleKey(other); err == nil {
		t.Error("a link was followed")
	}
}

// consoleModel is a model with den-api.sock on.
func consoleModel(t *testing.T) Model {
	t.Helper()
	apiEnv(t)
	m := testModel(140, 40)
	apiOn(t, &m)
	return m
}

// /api console shows a code in the header; a claim is a card that names
// the program and what it replaces, and the human's ^Y settles it.
func TestConsoleCodeAndCard(t *testing.T) {
	m := consoleModel(t)
	mm, _ := m.apiCommand([]string{"console"})
	m = mm.(Model)
	if !strings.Contains(m.consoleCodeLine(), "Console code (") || len(m.con.code) != 8 {
		t.Fatalf("code line %q", m.consoleCodeLine())
	}
	ask := &api.ConsoleAsk{ID: "c-1", Exe: "/usr/bin/app-host", PID: 4242, Started: time.Date(2026, 10, 15, 13, 5, 7, 0, time.Local), Parent: "systemd", Cmdline: "app-host --dock",
		Replaces: &api.Console{ID: "c-0", Exe: "/usr/bin/app-host", PID: 4100}}
	m.applyConsoleAct(api.Act{Console: ask})
	var card *inbox.Item
	for _, it := range m.store.Items() {
		if it.Kind == inbox.Console {
			card = it
		}
	}
	if card == nil {
		t.Fatal("no console card")
	}
	body := strings.Join(card.Body, "\n")
	for _, want := range []string{"app-host (pid 4242, started 13:05:07)", "program: /usr/bin/app-host", "it can be spoofed", "replaces your current console (app-host pid 4100)"} {
		if !strings.Contains(card.Title+"\n"+body, want) {
			t.Errorf("card lacks %q:\n%s\n%s", want, card.Title, body)
		}
	}
	if !card.DenAsks() {
		t.Error("the console card is not Den's own question")
	}
	mm, _ = m.decideConsole(card, "always")
	m = mm.(Model)
	if card.State == inbox.Resolved {
		t.Error("/always answered a console card")
	}
	mm, _ = m.decideConsole(card, "allow")
	m = mm.(Model)
	if card.State != inbox.Resolved {
		t.Error("^Y did not settle the card")
	}
	// A note for another program's try.
	m.applyConsoleAct(api.Act{ConsoleIntruder: &api.ConsoleIntruder{Exe: "/tmp/x", PID: 9}})
	found := false
	for _, it := range m.store.Items() {
		found = found || (it.Kind == inbox.ConsoleNote && strings.Contains(strings.Join(it.Body, "\n"), "x (pid 9) tried"))
	}
	if !found {
		t.Error("no note for another program's try")
	}
}

// den pair is refused for a caller under Claude Code or an agent, and
// otherwise shows a console code; den unpair all ends every pairing.
func TestDenPairAndUnpair(t *testing.T) {
	m := consoleModel(t)
	old := pairCaller
	t.Cleanup(func() { pairCaller = old })
	pairCaller = func(Model, int) string { return "it runs under Claude Code" }
	r := bridge.TestRequest(bridge.Envelope{Event: bridge.ConsolePairEvent})
	m.handleConsolePair(r)
	if d, _ := r.Decided(); d.Behavior != "deny" || !strings.Contains(d.Message, "under Claude Code") || m.con.code != nil {
		t.Errorf("refused caller: %+v, code %v", d, m.con.code)
	}
	pairCaller = func(Model, int) string { return "" }
	r = bridge.TestRequest(bridge.Envelope{Event: bridge.ConsolePairEvent})
	m.handleConsolePair(r)
	if d, _ := r.Decided(); d.Behavior != "allow" || m.consoleCodeLine() == "" {
		t.Errorf("den pair: %+v", d)
	}
	payload, _ := json.Marshal(map[string]bool{"all": true})
	r = bridge.TestRequest(bridge.Envelope{Event: bridge.ConsoleUnpairEvent, Payload: payload})
	m.handleConsoleUnpair(r)
	if d, _ := r.Decided(); d.Behavior != "allow" || !strings.HasPrefix(d.Message, "Unpaired") {
		t.Errorf("den unpair all: %+v", d)
	}
}

// The real check: this test's own process may run under anything, but a
// pid of 0 is never let through, and Den's own agents' sessions refuse.
func TestPairCallerRefusal(t *testing.T) {
	m := testModel(120, 40)
	if m.pairCallerRefusal(0) == "" {
		t.Error("pid 0 let through")
	}
}
