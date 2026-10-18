package desktop

import "testing"

// The console's glow counts only when a proved console process draws it
// under its own namespace.
func TestConsoleGlow(t *testing.T) {
	console := map[int]bool{1500: true}
	for _, c := range []struct {
		l    Layer
		want bool
	}{
		{Layer{Level: 1, Namespace: GlowNamespace, PID: 1500}, true},
		{Layer{Level: 1, Namespace: GlowNamespace, PID: 900}, false},     // another process
		{Layer{Level: 1, Namespace: PointerNamespace, PID: 1500}, false}, // the console's, but not the glow
		{Layer{Level: 1, Namespace: GlowNamespace, PID: 1}, false},       // no real process
	} {
		if got := (Snapshot{Layers: []Layer{c.l}}).ConsoleGlow(console); got != c.want {
			t.Errorf("%+v: %v, want %v", c.l, got, c.want)
		}
	}
	if (Snapshot{Layers: []Layer{{Namespace: GlowNamespace, PID: 1500}}}).ConsoleGlow(nil) {
		t.Error("a glow with no console proved")
	}
}
