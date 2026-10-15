package desktop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The server runs with -B and PYTHONDONTWRITEBYTECODE, so a start leaves
// the venv as it was, and any .pyc there (planted, or written by a run
// without -B) counts as added.
func TestServerWritesNoBytecode(t *testing.T) {
	argv := (Paths{Venv: "/v"}).ServerArgv()
	if strings.Join(argv, " ") != "/v/bin/python -I -B -m hypruse" {
		t.Fatalf("server argv %q", argv)
	}
	if strings.Join(ServerEnv(), " ") != "PYTHONDONTWRITEBYTECODE=1" {
		t.Fatalf("server env %q", ServerEnv())
	}
	p, _ := installed(t)
	if err := verify(p.Paths, p.Pin); err != nil {
		t.Fatalf("after a start that writes nothing: %v", err)
	}
	site, _ := SitePackages(p.Venv)
	os.MkdirAll(filepath.Join(site, "hypruse", "__pycache__"), 0o755)
	os.WriteFile(filepath.Join(site, "hypruse", "__pycache__", "server.cpython-314.pyc"), []byte("planted"), 0o644)
	if err := verify(p.Paths, p.Pin); err == nil || !strings.Contains(err.Error(), "server.cpython-314.pyc was added") {
		t.Fatalf("a planted .pyc was not caught: %v", err)
	}
}

func TestInstallRefusesLinkedFolders(t *testing.T) {
	clone, pin := fakeClone(t)
	base := t.TempDir()
	target := filepath.Join(base, "elsewhere")
	os.MkdirAll(target, 0o755)
	root := filepath.Join(base, "root")
	os.Symlink(target, root)
	p := Plan{Paths: Paths{Root: root, Venv: filepath.Join(root, "venv"), Manifest: filepath.Join(base, "m.json")}, Python: "/bin/false", Clone: clone, Pin: pin}
	var calls [][]string
	if _, err := Install(context.Background(), p, fakeRun(t, "/bin/false", &calls)); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("a linked root was installed into: %v", err)
	}
	if st, _ := os.Stat(target); st.Mode().Perm() != 0o755 {
		t.Fatal("the link's target was chmodded")
	}
}

func TestPipIsIsolated(t *testing.T) {
	_, calls := installed(t)
	if !strings.Contains(strings.Join(calls[1], " "), "pip install --isolated") {
		t.Fatalf("pip without --isolated: %q", calls[1])
	}
}

// With no /usr/bin/python3, the PATH's interpreter is used and the card
// notes it when it is not under /usr.
func TestPlanPrefersSystemPython(t *testing.T) {
	clone := os.Getenv("DEN_HYPRUSE_CLONE")
	if clone == "" {
		t.Skip("DEN_HYPRUSE_CLONE not set")
	}
	old := systemPython
	t.Cleanup(func() { systemPython = old })
	systemPython = filepath.Join(t.TempDir(), "none")
	p, err := MakePlan(Paths{Venv: "/v"}, clone, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(p.Python, "/usr/") != (p.PythonNote == "") {
		t.Fatalf("python %s with note %q", p.Python, p.PythonNote)
	}
}
