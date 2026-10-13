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
	if err := Bind("helper", "s1"); err != nil {
		t.Fatal(err)
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
	Bind("helper", "s3")
	if For("s1") != "io.glacierhub.den.helper" || For("s3") != "io.glacierhub.den.helper" {
		t.Error("the second bind took the first entry")
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
