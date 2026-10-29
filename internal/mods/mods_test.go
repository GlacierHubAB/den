package mods

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bundled(t *testing.T) Mod {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CODE_PLUGIN_DIRS", "")
	list, err := List()
	if err != nil || len(list) == 0 {
		t.Fatalf("no bundled mod: %v", err)
	}
	return list[0]
}

func TestInstallWritesTheMod(t *testing.T) {
	m := bundled(t)
	if err := Install(m); err != nil {
		t.Fatal(err)
	}
	if v := InstalledVersion(m); v != m.Version {
		t.Errorf("installed %q, bundled %q", v, m.Version)
	}
}

// D7 (den-sec-review): Install never writes through a symbolic link, the
// skills folder's or the mod's own, and leaves what it points at alone.
func TestInstallRefusesASymlink(t *testing.T) {
	for _, which := range []string{"skills", "mod"} {
		m := bundled(t)
		elsewhere := t.TempDir()
		os.WriteFile(filepath.Join(elsewhere, "keep"), []byte("x"), 0o644)
		link := filepath.Dir(m.Installs)
		if which == "mod" {
			os.MkdirAll(link, 0o755)
			link = m.Installs
		} else {
			os.MkdirAll(filepath.Dir(link), 0o755)
		}
		if err := os.Symlink(elsewhere, link); err != nil {
			t.Fatal(err)
		}
		if Symlinked(m) != link {
			t.Errorf("%s: Symlinked = %q, want %q", which, Symlinked(m), link)
		}
		err := Install(m)
		if err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Errorf("%s a symlink: Install = %v", which, err)
		}
		entries, _ := os.ReadDir(elsewhere)
		if len(entries) != 1 || entries[0].Name() != "keep" {
			t.Errorf("%s: what the link points at changed: %v", which, entries)
		}
	}
}
