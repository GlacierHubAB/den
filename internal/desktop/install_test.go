package desktop

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fakeClone writes a clone holding two package files and a LICENSE, and
// the pin that matches it.
func fakeClone(t *testing.T) (string, Pin) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"LICENSE":                 "MIT License\n",
		"src/hypruse/__init__.py": "__version__ = \"0\"\n",
		"src/hypruse/server.py":   "def main(): pass\n",
	}
	pin := Pin{URL: "https://example.invalid/hypruse", Commit: "0123456789abcdef", Version: "0", Files: map[string]string{}}
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		pin.Files[rel] = sum([]byte(body))
	}
	return dir, pin
}

func TestVerifyClone(t *testing.T) {
	clone, pin := fakeClone(t)
	if err := VerifyClone(pin, clone); err != nil {
		t.Fatalf("a matching clone was refused: %v", err)
	}
	os.WriteFile(filepath.Join(clone, "src/hypruse/server.py"), []byte("import os\n"), 0o644)
	if err := VerifyClone(pin, clone); err == nil || !strings.Contains(err.Error(), "server.py (differs)") {
		t.Fatalf("a changed file was not named: %v", err)
	}
	clone, pin = fakeClone(t)
	os.WriteFile(filepath.Join(clone, "src/hypruse/extra.py"), []byte("x = 1\n"), 0o644)
	if err := VerifyClone(pin, clone); err == nil || !strings.Contains(err.Error(), "extra.py") {
		t.Fatalf("an extra module was not refused: %v", err)
	}
	clone, pin = fakeClone(t)
	os.Remove(filepath.Join(clone, "LICENSE"))
	if err := VerifyClone(pin, clone); err == nil || !strings.Contains(err.Error(), "LICENSE (missing)") {
		t.Fatalf("a missing LICENSE was not named: %v", err)
	}
}

// The real pin names every package file and the licence, by full commit.
func TestPinnedShape(t *testing.T) {
	if len(Pinned.Commit) != 40 || !strings.HasPrefix(Pinned.Commit, "0035d86") {
		t.Fatalf("pinned commit %q", Pinned.Commit)
	}
	if _, ok := Pinned.Files["LICENSE"]; !ok {
		t.Fatal("the pin must carry hypruse's LICENSE")
	}
	hex := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for rel, h := range Pinned.Files {
		if !hex.MatchString(h) {
			t.Errorf("%s: %q is not a SHA-256", rel, h)
		}
		if rel != "LICENSE" && filepath.Dir(rel) != PackageDir {
			t.Errorf("%s is outside the package", rel)
		}
	}
}

// Every requirement is pinned to one version with at least one hash, and
// the lock says where it came from.
func TestLockIsHashPinned(t *testing.T) {
	if !strings.Contains(Lock, Pinned.Commit) || !strings.Contains(Lock, Pinned.URL) {
		t.Fatal("the lock must record its provenance (URL and commit)")
	}
	req := regexp.MustCompile(`^[a-z0-9-]+==[0-9][0-9a-z.]*( ; .+)? \\$`)
	hash := regexp.MustCompile(`^    --hash=sha256:[0-9a-f]{64}( \\)?$`)
	var name string
	hashes := map[string]int{}
	for _, line := range strings.Split(Lock, "\n") {
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, " "):
			if !hash.MatchString(line) {
				t.Fatalf("not a hash line: %q", line)
			}
			hashes[name]++
		default:
			if !req.MatchString(line) {
				t.Fatalf("not a pinned requirement: %q", line)
			}
			name = line[:strings.Index(line, "==")]
			hashes[name] = 0
		}
	}
	for n, c := range hashes {
		if c == 0 {
			t.Errorf("%s has no hash", n)
		}
	}
	if _, ok := hashes["mcp"]; !ok {
		t.Fatal("mcp is missing from the lock")
	}
	if got := LockPackages(Lock); got != len(hashes)-2 {
		t.Fatalf("LockPackages = %d, want %d (all but the two Windows-only ones)", got, len(hashes)-2)
	}
}

// fakeRun stands in for python -m venv and pip: it lays out a venv whose
// interpreter links to py, and pip "installs" one module.
func fakeRun(t *testing.T, py string, calls *[][]string) Runner {
	return func(_ context.Context, env []string, argv ...string) ([]byte, error) {
		*calls = append(*calls, argv)
		for _, kv := range env {
			if strings.HasPrefix(kv, "PIP_INDEX_URL=") || strings.HasPrefix(kv, "PYTHONPATH=") {
				t.Errorf("install environment carries %s", kv)
			}
		}
		switch {
		case len(argv) > 3 && argv[3] == "venv":
			venv := argv[4]
			os.MkdirAll(filepath.Join(venv, "bin"), 0o755)
			os.MkdirAll(filepath.Join(venv, "lib", "python3.14", "site-packages"), 0o755)
			os.Symlink(py, filepath.Join(venv, "bin", "python"))
			os.WriteFile(filepath.Join(venv, "pyvenv.cfg"), []byte("home = /usr/bin\n"), 0o644)
		case len(argv) > 3 && argv[3] == "pip":
			venv := filepath.Dir(filepath.Dir(argv[0]))
			mod := filepath.Join(venv, "lib", "python3.14", "site-packages", "mcp")
			os.MkdirAll(mod, 0o755)
			os.WriteFile(filepath.Join(mod, "__init__.py"), []byte("# mcp\n"), 0o644)
		case len(argv) > 3 && argv[3] == "compileall":
			cache := filepath.Join(argv[len(argv)-1], "hypruse", "__pycache__")
			os.MkdirAll(cache, 0o755)
			os.WriteFile(filepath.Join(cache, "__init__.cpython-314.pyc"), []byte("compiled"), 0o644)
		}
		return nil, nil
	}
}

func installed(t *testing.T) (Plan, []([]string)) {
	t.Helper()
	clone, pin := fakeClone(t)
	base := t.TempDir()
	py := filepath.Join(base, "python3.14")
	os.WriteFile(py, []byte("#!fake\n"), 0o755)
	paths := Paths{Root: filepath.Join(base, "data", "hypruse"), Manifest: filepath.Join(base, "state", "desktop", "venv-manifest.json")}
	paths.Venv = filepath.Join(paths.Root, "venv")
	p := Plan{Paths: paths, Python: py, Clone: clone, Pin: pin}
	var calls [][]string
	if _, err := Install(context.Background(), p, fakeRun(t, py, &calls)); err != nil {
		t.Fatalf("install: %v", err)
	}
	return p, calls
}

func TestInstallThenVerify(t *testing.T) {
	p, calls := installed(t)
	if len(calls) != 3 || calls[2][3] != "compileall" {
		t.Fatalf("want venv, pip, then compileall, got %q", calls)
	}
	// Bytecode is compiled once, at install, by source hash, and is in the
	// record like every other file.
	compile := strings.Join(calls[2], " ")
	if !strings.Contains(compile, "-I -m compileall -q --invalidation-mode checked-hash ") {
		t.Errorf("compileall: %s", compile)
	}
	if m, _ := LoadManifest(p.Manifest); m.Files == nil || m.Files[filepath.Join(strings.TrimPrefix(calls[2][len(calls[2])-1], p.Venv+"/"), "hypruse", "__pycache__", "__init__.cpython-314.pyc")] == "" {
		t.Errorf("the compiled bytecode is not in the manifest: %v", m.Files)
	}
	pip := strings.Join(calls[1], " ")
	for _, flag := range []string{"-I -m pip install", "--require-hashes", "--only-binary :all:", "--no-deps", "--no-compile", "-r " + filepath.Join(p.Root, "requirements.lock")} {
		if !strings.Contains(pip, flag) {
			t.Errorf("pip lacks %q: %s", flag, pip)
		}
	}
	if strings.Contains(pip, "--no-index") {
		t.Error("an online install must not pass --no-index")
	}
	if st, err := os.Stat(p.Root); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("root mode: %v %v", st.Mode(), err)
	}
	if st, err := os.Stat(p.Manifest); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("manifest mode: %v %v", st.Mode(), err)
	}
	site, _ := SitePackages(p.Venv)
	if _, err := os.Stat(filepath.Join(site, "hypruse", "LICENSE")); err != nil {
		t.Fatal("hypruse's LICENSE must be installed beside it")
	}
	if err := verify(p.Paths, p.Pin); err != nil {
		t.Fatalf("a fresh install fails its check: %v", err)
	}
}

func TestVerifyCatches(t *testing.T) {
	cases := map[string]struct {
		edit func(p Plan, site string)
		want string
	}{
		"changed hypruse": {func(p Plan, site string) {
			os.WriteFile(filepath.Join(site, "hypruse", "server.py"), []byte("import os\n"), 0o644)
		}, "hypruse/server.py is not the pinned file"},
		"changed dependency": {func(p Plan, site string) {
			os.WriteFile(filepath.Join(site, "mcp", "__init__.py"), []byte("import os\n"), 0o644)
		}, "mcp/__init__.py changed"},
		"planted bytecode": {func(p Plan, site string) {
			os.WriteFile(filepath.Join(site, "hypruse", "__pycache__", "server.cpython-314.pyc"), []byte("code"), 0o644)
		}, "server.cpython-314.pyc was added"},
		"changed bytecode": {func(p Plan, site string) {
			os.WriteFile(filepath.Join(site, "hypruse", "__pycache__", "__init__.cpython-314.pyc"), []byte("other code"), 0o644)
		}, "__init__.cpython-314.pyc changed"},
		"added module": {func(p Plan, site string) {
			os.WriteFile(filepath.Join(site, "hypruse", "evil.py"), []byte("x\n"), 0o644)
		}, "evil.py was added"},
		"pth file": {func(p Plan, site string) {
			os.WriteFile(filepath.Join(site, "zz.pth"), []byte("import os\n"), 0o644)
		}, "zz.pth was added (Python runs it at start-up)"},
		"sitecustomize": {func(p Plan, site string) {
			os.WriteFile(filepath.Join(site, "sitecustomize.py"), []byte("import os\n"), 0o644)
		}, "sitecustomize.py was added (Python runs it at start-up)"},
		"usercustomize": {func(p Plan, site string) {
			os.WriteFile(filepath.Join(site, "usercustomize.py"), []byte("import os\n"), 0o644)
		}, "usercustomize.py was added"},
		"removed file": {func(p Plan, site string) {
			os.Remove(filepath.Join(site, "mcp", "__init__.py"))
		}, "mcp/__init__.py is missing"},
		"interpreter swapped": {func(p Plan, site string) {
			other := filepath.Join(filepath.Dir(p.Python), "other")
			os.WriteFile(other, nil, 0o755)
			os.Remove(p.Paths.Python())
			os.Symlink(other, p.Paths.Python())
		}, "points elsewhere"},
		"no manifest": {func(p Plan, site string) { os.Remove(p.Manifest) }, "not installed"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p, _ := installed(t)
			site, _ := SitePackages(p.Venv)
			c.edit(p, site)
			err := verify(p.Paths, p.Pin)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want %q, got %v", c.want, err)
			}
		})
	}
}

// A clone changed after the card was shown is caught at install, before
// anything is made.
func TestInstallRechecksClone(t *testing.T) {
	clone, pin := fakeClone(t)
	os.WriteFile(filepath.Join(clone, "src/hypruse/server.py"), []byte("import os\n"), 0o644)
	base := t.TempDir()
	p := Plan{Paths: Paths{Root: filepath.Join(base, "r"), Venv: filepath.Join(base, "r", "venv"), Manifest: filepath.Join(base, "m.json")}, Python: "/bin/false", Clone: clone, Pin: pin}
	var calls [][]string
	if _, err := Install(context.Background(), p, fakeRun(t, "/bin/false", &calls)); err == nil {
		t.Fatal("a changed clone was installed")
	}
	if len(calls) != 0 {
		t.Fatalf("commands ran for a refused install: %q", calls)
	}
	if _, err := os.Stat(p.Root); err == nil {
		t.Fatal("a refused install made its folder")
	}
}

func TestOfflineInstallFlags(t *testing.T) {
	clone, pin := fakeClone(t)
	base := t.TempDir()
	py := filepath.Join(base, "py")
	os.WriteFile(py, nil, 0o755)
	p := Plan{Paths: Paths{Root: filepath.Join(base, "r"), Venv: filepath.Join(base, "r", "venv"), Manifest: filepath.Join(base, "s", "m.json")}, Python: py, Clone: clone, Pin: pin, Wheels: "/w"}
	var calls [][]string
	if _, err := Install(context.Background(), p, fakeRun(t, py, &calls)); err != nil {
		t.Fatal(err)
	}
	if pip := strings.Join(calls[1], " "); !strings.Contains(pip, "--no-index --find-links /w") {
		t.Fatalf("offline pip: %s", pip)
	}
}

// With DEN_HYPRUSE_CLONE naming a clone of hypruse at the pinned commit,
// the compiled pin matches it. Off by default: CI has no clone.
func TestPinnedMatchesClone(t *testing.T) {
	clone := os.Getenv("DEN_HYPRUSE_CLONE")
	if clone == "" {
		t.Skip("DEN_HYPRUSE_CLONE not set")
	}
	if err := VerifyClone(Pinned, clone); err != nil {
		t.Fatal(err)
	}
}

func TestPlanText(t *testing.T) {
	p := Plan{Paths: Paths{Venv: "/v", Manifest: "/m"}, Python: "/usr/bin/python3.14", Clone: "/c", Packages: 29, Pin: Pinned}
	text := p.Text()
	for _, want := range []string{"Download 29 packages from PyPI", "pinned by hash", "0035d86", "MIT", "Nothing of it runs now"} {
		if !strings.Contains(text, want) {
			t.Errorf("card text lacks %q:\n%s", want, text)
		}
	}
	p.Wheels = "/w"
	if !strings.Contains(p.Text(), "from /w, offline") {
		t.Errorf("offline text: %s", p.Text())
	}
}
