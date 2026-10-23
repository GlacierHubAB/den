package ui

import (
	"os"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/inbox"
)

// powersModel is a model whose API runs, started as Den starts with the
// config file holding console_powers = file ("" for no such line).
func powersModel(t *testing.T, file string) Model {
	t.Helper()
	cfgPath, _ := apiEnv(t)
	if file != "" {
		os.WriteFile(cfgPath, []byte("[api]\nconsole_powers = "+file+"\n"), 0o644)
	}
	m := testModel(140, 40)
	m.startAPI()
	if m.api.srv == nil {
		t.Fatalf("the API did not start: %s", m.api.err)
	}
	t.Cleanup(m.closeAPI)
	m.startPowers("written to config.toml while Den was stopped")
	return m
}

func powersCards(m Model) []*inbox.Item {
	var out []*inbox.Item
	for _, it := range m.store.Items() {
		if it.Kind == inbox.PowersSet && it.State == inbox.Waiting {
			out = append(out, it)
		}
	}
	return out
}

// setPowersFile makes the next poll read value.
func setPowersFile(m *Model, value, refused string) {
	m.powers.load = func() powersReadMsg { return powersReadMsg{ok: true, value: value, refused: refused} }
}

// poll runs one tick's read of the setting.
func poll(m *Model) {
	if cmd := m.pollPowers(); cmd != nil {
		m.applyPowersRead(cmd().(powersReadMsg))
	}
}

// The lead's must-have: Den starts with "always" already in the file, and
// it does not hold: the server has it off, and a card asks, naming where
// it came from. Only the ^Y turns it on. (A console pairing by itself with
// it off gets no powers: api's TestAlwaysOffAtStart.)
func TestPowersAlwaysAtStartWaitsOnACard(t *testing.T) {
	m := powersModel(t, `"always"`)
	if m.api.srv.ConsolePowersAlways() {
		t.Fatal("always holds at start without a ^Y")
	}
	cards := powersCards(m)
	if len(cards) != 1 {
		t.Fatalf("%d cards", len(cards))
	}
	body := strings.Join(cards[0].Body, "\n")
	if !strings.Contains(body, "written to config.toml while Den was stopped") || strings.Contains(body, "you asked") {
		t.Errorf("card body: %s", body)
	}
	mm, _ := m.decidePowersSet(cards[0], "allow")
	m = mm.(Model)
	if !m.api.srv.ConsolePowersAlways() || !m.powers.on || len(powersCards(m)) != 0 {
		t.Errorf("after ^Y: always %v, on %v, %d cards", m.api.srv.ConsolePowersAlways(), m.powers.on, len(powersCards(m)))
	}
}

func TestPowersDeclined(t *testing.T) {
	m := powersModel(t, `"always"`)
	mm, _ := m.decidePowersSet(powersCards(m)[0], "deny")
	m = mm.(Model)
	if m.api.srv.ConsolePowersAlways() || len(powersCards(m)) != 0 {
		t.Errorf("after ^N: always %v, %d cards", m.api.srv.ConsolePowersAlways(), len(powersCards(m)))
	}
	// The same file on the next tick asks nothing more: Den acts on changes.
	setPowersFile(&m, "always", "")
	poll(&m)
	if len(powersCards(m)) != 0 {
		t.Error("asked again for an unchanged file")
	}
}

// "unchanged", no line, a refused value: no card, "always" off; a refused
// value is a red note.
func TestPowersAtStartWithoutAlways(t *testing.T) {
	for file, red := range map[string]bool{"": false, `"unchanged"`: false, `"sometimes"`: true} {
		m := powersModel(t, file)
		if m.api.srv.ConsolePowersAlways() || len(powersCards(m)) != 0 || m.flashErr != red {
			t.Errorf("file %q: always %v, %d cards, flash %q", file, m.api.srv.ConsolePowersAlways(), len(powersCards(m)), m.flash)
		}
	}
}

// While Den runs: a change to "always" asks, naming it a change; back to
// "unchanged" applies at once; a read Den refuses changes nothing (C6).
func TestPowersConfigWatch(t *testing.T) {
	m := powersModel(t, "")
	setPowersFile(&m, "always", "")
	poll(&m)
	cards := powersCards(m)
	if len(cards) != 1 || !strings.Contains(strings.Join(cards[0].Body, "\n"), "changed in config.toml while Den runs") {
		t.Fatalf("cards %+v", cards)
	}
	if m.api.srv.ConsolePowersAlways() {
		t.Fatal("always before the ^Y")
	}
	mm, _ := m.decidePowersSet(cards[0], "allow")
	m = mm.(Model)
	setPowersFile(&m, "", "sometimes")
	poll(&m)
	if !m.api.srv.ConsolePowersAlways() || !m.flashErr {
		t.Errorf("a refused read: always %v, flash %q", m.api.srv.ConsolePowersAlways(), m.flash)
	}
	setPowersFile(&m, "unchanged", "")
	poll(&m)
	if m.api.srv.ConsolePowersAlways() || m.powers.on {
		t.Error("a lowering did not apply at once")
	}
}

// C3: every change is a new card; a ^Y on the one before finds it gone.
func TestPowersCardReplacedAndStale(t *testing.T) {
	m := powersModel(t, `"always"`)
	first := powersCards(m)[0]
	setPowersFile(&m, "unchanged", "")
	poll(&m)
	setPowersFile(&m, "always", "")
	poll(&m)
	cards := powersCards(m)
	if len(cards) != 1 || cards[0].ID == first.ID {
		t.Fatalf("cards %+v", cards)
	}
	mm, _ := m.decidePowersSet(first, "allow")
	m = mm.(Model)
	if m.api.srv.ConsolePowersAlways() {
		t.Error("a ^Y on a replaced card turned always on")
	}
	// The file changed after the card: its ^Y changes nothing.
	m.powers.file = "unchanged"
	mm, _ = m.decidePowersSet(powersCards(m)[0], "allow")
	if mm.(Model).api.srv.ConsolePowersAlways() {
		t.Error("a ^Y after the file changed turned always on")
	}
}

// C1, as Den's window shows it: a lock of the powers ends "always" for the
// run, and the next tick says so.
func TestPowersLockEndsAlways(t *testing.T) {
	m := powersModel(t, `"always"`)
	mm, _ := m.decidePowersSet(powersCards(m)[0], "allow")
	m = mm.(Model)
	m.api.srv.LockConsolePowers("another program tried your console")
	setPowersFile(&m, "always", "")
	poll(&m)
	if m.api.srv.ConsolePowersAlways() || m.powers.on || len(powersCards(m)) != 0 {
		t.Errorf("after a lock: always %v, on %v, %d cards", m.api.srv.ConsolePowersAlways(), m.powers.on, len(powersCards(m)))
	}
	found := false
	for _, e := range m.orch.chat {
		if strings.Contains(e.text, "is off for the rest of this run") {
			found = true
		}
	}
	if !found {
		t.Error("no note that the lock ended always")
	}
}

// The card is answered in Den's own window only, God mode or not.
func TestPowersSetNeverFromTheDock(t *testing.T) {
	m := powersModel(t, `"always"`)
	if dockMayAnswer(powersCards(m)[0]) || !godTTYKinds[inbox.PowersSet] {
		t.Error("PowersSet is answerable from the dock")
	}
}
