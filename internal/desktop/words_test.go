package desktop

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSensitive(t *testing.T) {
	hits := []string{
		"Send", "SEND", "Send now", "send-later", "Submit order", "Delete", "Pay $20", "Sign in", "Sign  In",
		"Log in", "Skicka", "Ta bort", "Logga in", "Löschen", "Envoyer", "Iniciar sesión", "Invia", "Verzenden",
		"Отправить", "送信", "立即发送", "ログインする",
		"S\u200bend",               // a zero-width space inside
		"\u202eSend",               // a bidi override in front
		"\uff33\uff45\uff4e\uff44", // fullwidth letters
	}
	for _, name := range hits {
		if _, ok := Sensitive(name); !ok {
			t.Errorf("%q should ask", name)
		}
	}
	misses := []string{"", "Sender name", "Bold", "Insert table", "Payload", "Postpone", "Sendmail settings", "Ordering", "Font size"}
	for _, name := range misses {
		if w, ok := Sensitive(name); ok {
			t.Errorf("%q matched %q", name, w)
		}
	}
}

func TestChunks(t *testing.T) {
	text := strings.Repeat("a", 40) + "\r\nsecond\nå" + strings.Repeat("ö", 33)
	got := Chunks(text)
	var b strings.Builder
	for _, p := range got {
		if p.Enter {
			b.WriteString("\n")
			continue
		}
		if n := len([]rune(p.Text)); n == 0 || n > MaxChunk {
			t.Fatalf("chunk of %d runes", n)
		}
		if strings.ContainsAny(p.Text, "\r\n") {
			t.Fatal("a line break inside a chunk")
		}
		b.WriteString(p.Text)
	}
	if want := strings.ReplaceAll(text, "\r\n", "\n"); b.String() != want {
		t.Fatalf("chunks rejoin to %q", b.String())
	}
	if n := len(Chunks("\n\n")); n != 2 {
		t.Fatalf("two line breaks gave %d pieces", n)
	}
}

func TestDescribe(t *testing.T) {
	if got := Describe("a\nb", 10); got != "a⏎b" {
		t.Fatalf("%q", got)
	}
	if got := Describe(strings.Repeat("x", 20), 5); got != "xxxx…" {
		t.Fatalf("%q", got)
	}
}

func TestFilterDesktop(t *testing.T) {
	m, s := fixture()
	raw := `{"monitors":[{"name":"DP-1","geometry":[0,0,1920,1080]},{"name":"DP-2","geometry":[1920,0,1920,1080]}],
	"workspaces":[{"id":1,"name":"1"},{"id":9,"name":"9"}],
	"windows":[{"address":"0xb","title":"Inbox"},{"address":"0xe","title":"note"},{"address":"0xs","title":"stranger"},{"address":"0xh","title":"~/secret"}],
	"active_window":"0xe","cursor":[100,100],"layers":[{"namespace":"notifications"}]}`
	out, err := FilterDesktop([]byte(raw), ScopeOf(m, s))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, gone := range []string{"DP-2", "secret", "stranger", "notifications", `"id":1`} {
		if strings.Contains(got, gone) {
			t.Errorf("%q leaked: %s", gone, got)
		}
	}
	for _, kept := range []string{"DP-1", "Inbox", "note", `"active_window":"0xe"`, `"cursor":[100,100]`} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q missing: %s", kept, got)
		}
	}
	// The focused window is the human's: not shown.
	s.Active = "0xh"
	out, _ = FilterDesktop([]byte(strings.Replace(raw, `"active_window":"0xe"`, `"active_window":"0xh"`, 1)), ScopeOf(m, s))
	var back map[string]any
	json.Unmarshal(out, &back)
	if back["active_window"] != nil {
		t.Errorf("active window %v shown", back["active_window"])
	}
	if _, err := FilterDesktop([]byte(`{"windows":"x"}`), ScopeOf(m, s)); err == nil {
		t.Error("a snapshot of another shape must fail")
	}
}

// A task sees the whole desktop but Den's own windows: every monitor,
// workspace and other window, the focused window and the cursor. Layers
// stay dropped.
func TestFilterDesktopTask(t *testing.T) {
	m := Mission{Task: true, Protected: map[string]bool{"0xd": true, "0xgone": true}}
	raw := `{"monitors":[{"name":"DP-1","geometry":[0,0,1920,1080]},{"name":"DP-2","geometry":[1920,0,1920,1080]}],
	"workspaces":[{"id":1,"name":"1"},{"id":4,"name":"4"}],
	"windows":[{"address":"0xd","title":"Den"},{"address":"0xf","title":"x.com - Firefox"},{"address":"0xt","title":"notes"}],
	"active_window":"0xf","cursor":[2500,300],"layers":[{"namespace":"drey-dock"}]}`
	sc := ScopeOf(m, Snapshot{})
	out, err := FilterDesktop([]byte(raw), sc)
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Monitors   []map[string]any `json:"monitors"`
		Workspaces []map[string]any `json:"workspaces"`
		Windows    []map[string]any `json:"windows"`
		Active     any              `json:"active_window"`
		Cursor     []float64        `json:"cursor"`
		Layers     any              `json:"layers"`
		Note       string           `json:"note"`
	}
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Monitors) != 2 || len(back.Workspaces) != 2 {
		t.Errorf("monitors %v, workspaces %v", back.Monitors, back.Workspaces)
	}
	if len(back.Windows) != 2 || back.Windows[0]["address"] != "0xf" || back.Windows[1]["address"] != "0xt" {
		t.Errorf("windows %v", back.Windows)
	}
	if strings.Contains(string(out), `"Den"`) || strings.Contains(string(out), "0xd") {
		t.Errorf("Den's window leaked: %s", out)
	}
	if back.Active != "0xf" || len(back.Cursor) != 2 || back.Cursor[0] != 2500 {
		t.Errorf("active %v, cursor %v", back.Active, back.Cursor)
	}
	if back.Layers != nil || strings.Contains(string(out), "drey-dock") {
		t.Errorf("layers shown: %s", out)
	}
	if back.Note != "Den hides only its own windows." {
		t.Errorf("note %q", back.Note)
	}

	// Den's window focused: not named as the focused window.
	out, _ = FilterDesktop([]byte(strings.Replace(raw, `"active_window":"0xf"`, `"active_window":"0xd"`, 1)), sc)
	back.Active = nil
	json.Unmarshal(out, &back)
	if back.Active != nil {
		t.Errorf("Den's focused window shown as %v", back.Active)
	}
	// No cursor or focus at all: null, not an error.
	out, err = FilterDesktop([]byte(`{"monitors":[],"workspaces":[],"windows":[],"active_window":null,"cursor":null}`), sc)
	if err != nil || !strings.Contains(string(out), `"cursor":null`) {
		t.Errorf("empty desktop: %s, %v", out, err)
	}
}

// ScopeOf for a task names every protected address, sorted, and no
// mission workspace or windows; a mission's Scope has no Hidden.
func TestScopeOfTask(t *testing.T) {
	sc := ScopeOf(Mission{Task: true, Protected: map[string]bool{"0xb": true, "0xa": true, "": true}}, Snapshot{})
	if !sc.Task || len(sc.Windows) != 0 || sc.Workspace != 0 || strings.Join(sc.Hidden, ",") != "0xa,0xb" {
		t.Errorf("task scope %+v", sc)
	}
	m, s := fixture()
	if mc := ScopeOf(m, s); mc.Task || mc.Hidden != nil {
		t.Errorf("mission scope %+v", mc)
	}
}
