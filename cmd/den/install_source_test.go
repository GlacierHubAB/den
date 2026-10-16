package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/desktop"
)

// fakeInstall says whether hypruse is installed, and which folders are
// clones of the pin.
func fakeInstall(t *testing.T, installed bool, clones ...string) {
	t.Helper()
	oldI, oldC := hypruseInstalled, hypruseClone
	t.Cleanup(func() { hypruseInstalled, hypruseClone = oldI, oldC })
	hypruseInstalled = func() error {
		if installed {
			return nil
		}
		return errors.New("no venv")
	}
	hypruseClone = func(dir string) bool {
		for _, c := range clones {
			if dir == c {
				return true
			}
		}
		return false
	}
}

// Installed and verified already: nothing to ask, whatever was given.
func TestInstallSourceAlreadyInstalled(t *testing.T) {
	fakeInstall(t, true)
	for _, from := range []string{"", "/src/hypruse", "https://github.com/IlyasKhallouki/hypruse"} {
		clone, note, err := installSource(from, "/tmp", t.TempDir())
		if err != nil || clone != "" || note != "hypruse "+desktop.Pinned.Version+" is already installed." {
			t.Errorf("from %q: %q %q %v", from, clone, note, err)
		}
	}
}

// A URL is never joined onto the folder: it is refused with how to clone.
func TestInstallSourceRefusesAURL(t *testing.T) {
	fakeInstall(t, false)
	for _, from := range []string{"https://github.com/IlyasKhallouki/hypruse", "github.com/IlyasKhallouki/hypruse", "git@github.com:IlyasKhallouki/hypruse.git", "HTTP://example.com/x"} {
		clone, _, err := installSource(from, "/tmp", "")
		if err == nil || clone != "" || !strings.Contains(err.Error(), "--from takes a local clone, not a URL") || !strings.Contains(err.Error(), "git checkout "+desktop.Pinned.Commit) {
			t.Errorf("from %q: %q %v", from, clone, err)
		}
	}
	// A folder is made absolute and passed on (Den checks it).
	if clone, _, err := installSource("rel/hypruse", "/tmp", ""); err != nil || !filepath.IsAbs(clone) {
		t.Errorf("a folder: %q %v", clone, err)
	}
}

// With no --from: the folder it runs in, else a verified clone under home,
// else how to get one.
func TestInstallSourceFindsAClone(t *testing.T) {
	home := t.TempDir()
	deep := filepath.Join(home, "research", "os-control", "src", "hypruse")
	other := filepath.Join(home, "notes", "hypruse") // a folder of that name, not the pin
	for _, d := range []string{deep, other} {
		os.MkdirAll(d, 0o700)
	}
	fakeInstall(t, false, deep, "/work/hypruse")
	if clone, note, err := installSource("", "/work/hypruse", home); err != nil || clone != "/work/hypruse" || !strings.Contains(note, "/work/hypruse") {
		t.Fatalf("the folder it runs in: %q %q %v", clone, note, err)
	}
	if clone, note, err := installSource("", "/elsewhere", home); err != nil || clone != deep || !strings.Contains(note, deep) {
		t.Fatalf("under home: %q %q %v", clone, note, err)
	}
	fakeInstall(t, false)
	if _, _, err := installSource("", "/elsewhere", home); err == nil || !strings.Contains(err.Error(), "git clone "+desktop.Pinned.URL) {
		t.Fatalf("none: %v", err)
	}
}

// The search stays shallow and out of hidden folders, and takes only what
// passes the check.
func TestFindCloneBounds(t *testing.T) {
	home := t.TempDir()
	tooDeep := filepath.Join(home, "a", "b", "c", "d", "hypruse")
	hidden := filepath.Join(home, ".cache", "hypruse")
	vendored := filepath.Join(home, "app", "node_modules", "hypruse")
	for _, d := range []string{tooDeep, hidden, vendored} {
		os.MkdirAll(d, 0o700)
	}
	var asked []string
	ok := func(dir string) bool { asked = append(asked, dir); return true }
	if got := findClone(home, ok); got != "" {
		t.Fatalf("found %q; asked %v", got, asked)
	}
	if findClone("", ok) != "" {
		t.Fatal("searched with no home")
	}
	shallow := filepath.Join(home, "a", "b", "c", "hypruse")
	os.MkdirAll(shallow, 0o700)
	if got := findClone(home, ok); got != shallow {
		t.Fatalf("four levels down: %q", got)
	}
	if got := findClone(home, func(string) bool { return false }); got != "" {
		t.Fatalf("an unverified folder was taken: %q", got)
	}
}
