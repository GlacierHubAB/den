package winmark

import (
	"os"
	"testing"
)

// TestRecordSurvivesARestart: what one Den process records, a fresh read
// (another process, or Den after a restart) finds; only a bound session
// has a marker, and the file is private.
func TestRecordSurvivesARestart(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := Record("helper", "io.glacierhub.den.helper"); err != nil {
		t.Fatal(err)
	}
	if For("s1") != "" {
		t.Error("an unbound spawn has no session yet")
	}
	if ok, err := Bind("helper", "s1", yes); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if got := For("s1"); got != "io.glacierhub.den.helper" {
		t.Errorf("For(s1) = %q", got)
	}
	if For("s2") != "" || For("") != "" {
		t.Error("an unrecorded session has a marker")
	}
	fi, err := os.Stat(Path())
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("record mode %v, %v", fi.Mode().Perm(), err)
	}
	alive := func(s string) bool { return s == "s1" }
	if !Held("io.glacierhub.den.helper", alive) || Held("io.glacierhub.den.helper", func(string) bool { return false }) {
		t.Error("Held should follow the session's life")
	}
	// A second spawn under the same name binds to its own session.
	Record("helper", "io.glacierhub.den.helper")
	Bind("helper", "s3", yes)
	if For("s1") != "io.glacierhub.den.helper" || For("s3") != "io.glacierhub.den.helper" {
		t.Error("the second bind took the first entry")
	}
}

func yes(string) bool { return true }

// TestBindNeedsTheMarkedWindow: a session that took the spawn's name but
// does not run in its marked window is not bound; the agent that does,
// arriving later, still is.
func TestBindNeedsTheMarkedWindow(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	Record("helper", "io.glacierhub.den.helper")
	var asked []string
	holds := func(session string) func(string) bool {
		return func(marker string) bool { asked = append(asked, marker); return session == "agent" }
	}
	if ok, _ := Bind("helper", "impostor", holds("impostor")); ok || For("impostor") != "" {
		t.Error("a session outside the marked window was bound")
	}
	if ok, _ := Bind("helper", "agent", holds("agent")); !ok || For("agent") != "io.glacierhub.den.helper" {
		t.Error("the agent in the marked window was not bound")
	}
	if len(asked) != 2 || asked[0] != "io.glacierhub.den.helper" {
		t.Errorf("holds asked about %q, want the entry's marker", asked)
	}
	if ok, _ := Bind("helper", "s", nil); ok {
		t.Error("a bind with no window check went through")
	}
}

func TestRecordIsCapped(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for range keep + 20 {
		Record("p", "m")
	}
	es, _ := load()
	if len(es) != keep {
		t.Errorf("%d entries", len(es))
	}
}
