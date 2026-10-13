package hypr

import (
	"strings"
	"testing"
)

func TestStripStatusGlyph(t *testing.T) {
	for in, want := range map[string]string{
		"✳ Install herdr":      "Install herdr",
		"◑ TUI agent platform": "TUI agent platform",
		"~/projects/x":         "~/projects/x",
		"  ✳  Fix login  ":     "Fix login",
		"devbox: Main Session": "devbox: Main Session",
	} {
		if got := StripStatusGlyph(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestFindPrefersHerdrThenTitleThenCwd(t *testing.T) {
	cs := []Client{
		{Address: "a", Class: "com.mitchellh.ghostty", Title: "✳ Fix login", FocusHist: 3},
		{Address: "b", Class: "com.mitchellh.ghostty", Title: "~/code/den", FocusHist: 1},
		{Address: "c", Class: "com.mitchellh.ghostty", Title: "devbox: Main Session", FocusHist: 2},
		{Address: "d", Class: "zen", Title: "Fix login", FocusHist: 0},
	}
	if m, ok := Find(cs, "Fix login", "~/code/den", ""); !ok || m.Client.Address != "a" || m.How != "title" {
		t.Errorf("title match = %+v %v", m, ok)
	}
	if m, ok := Find(cs, "", "~/code/den", ""); !ok || m.Client.Address != "b" {
		t.Errorf("cwd match = %+v %v", m, ok)
	}
	if m, ok := Find(cs, "Fix login", "", "devbox: "); !ok || m.Client.Address != "c" || m.How != "herdr" {
		t.Errorf("herdr match = %+v %v", m, ok)
	}
	if _, ok := Find(cs, "Nope", "", ""); ok {
		t.Error("unexpected match")
	}
}

func TestMarker(t *testing.T) {
	for in, want := range map[string]string{
		"helper":                           "io.glacierhub.den.helper",
		"Quiet Panda":                      "io.glacierhub.den.quiet-panda",
		"drey/../x;rm -rf":                 "io.glacierhub.den.drey-x-rm-rf",
		"--":                               "",
		strings.Repeat("ab", 30):           "io.glacierhub.den." + strings.Repeat("ab", 20),
		"a" + strings.Repeat("-", 3) + "b": "io.glacierhub.den.a-b",
	} {
		if got := Marker(in); got != want {
			t.Errorf("Marker(%q) = %q, want %q", in, got, want)
		}
	}
}
