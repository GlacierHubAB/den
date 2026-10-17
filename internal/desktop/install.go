package desktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Lock is the hash-locked requirements file for hypruse's dependencies.
//
//go:embed hypruse.lock
var Lock string

// Paths is where Den keeps its hypruse: the venv under Den's data dir and
// the manifest of what was installed under its state dir, apart from the
// venv so that editing the venv alone cannot also rewrite the record.
type Paths struct {
	Root     string // $XDG_DATA_HOME/den/hypruse
	Venv     string // Root/venv
	Manifest string // $XDG_STATE_HOME/den/desktop/venv-manifest.json
}

// DefaultPaths are the Paths under the XDG base directories.
func DefaultPaths() Paths {
	home, _ := os.UserHomeDir()
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		state = filepath.Join(home, ".local", "state")
	}
	root := filepath.Join(data, "den", "hypruse")
	return Paths{Root: root, Venv: filepath.Join(root, "venv"), Manifest: filepath.Join(state, "den", "desktop", "venv-manifest.json")}
}

// Python is the venv's interpreter.
func (p Paths) Python() string { return filepath.Join(p.Venv, "bin", "python") }

// ServerArgv is how the hypruse MCP server is started: isolated mode, so
// neither the working directory nor the user's site-packages nor PYTHON*
// variables reach it; -B, so it never writes bytecode into the venv (no
// .pyc is ever legitimate there, and Verify counts any as added); and no
// args, which is hypruse's stdio server. Its environment carries
// PYTHONDONTWRITEBYTECODE=1 as well (ServerEnv).
func (p Paths) ServerArgv() []string { return []string{p.Python(), "-I", "-B", "-m", "hypruse"} }

// ServerEnv is what the server's environment must carry besides hypruse's
// own settings.
func ServerEnv() []string { return []string{"PYTHONDONTWRITEBYTECODE=1"} }

// Plan is an install the human is asked to approve.
type Plan struct {
	Paths
	Python    string // the system interpreter the venv is made from, absolute and resolved
	Clone     string // the hypruse clone the package files are copied from
	Wheels    string // a folder of wheels for an offline install, "" for PyPI
	Packages  int    // how many packages pip installs on this platform
	Reinstall bool   // a venv is there already and is replaced
	Pin       Pin    // what is installed: Pinned, except in tests
	// PythonNote says why the interpreter is not the system's own
	// /usr/bin/python3, "" when it is.
	PythonNote string
}

// MakePlan checks what an install needs before anything is asked: a
// python3 on PATH, a clone whose package files are exactly the pinned ones,
// and a wheels folder when one is named.
func MakePlan(paths Paths, clone, wheels string) (Plan, error) {
	return makePlan(paths, clone, wheels, Pinned)
}

func makePlan(paths Paths, clone, wheels string, pin Pin) (Plan, error) {
	p := Plan{Paths: paths, Packages: LockPackages(Lock), Pin: pin}
	var err error
	if p.Python, p.PythonNote, err = FindPython(); err != nil {
		return p, err
	}
	if clone == "" {
		return p, errors.New("name the hypruse clone with --from (a clone of " + Pinned.URL + " at " + short(Pinned.Commit) + ")")
	}
	if p.Clone, err = filepath.Abs(clone); err != nil {
		return p, err
	}
	if err := VerifyClone(p.Pin, p.Clone); err != nil {
		return p, err
	}
	if wheels != "" {
		if p.Wheels, err = filepath.Abs(wheels); err != nil {
			return p, err
		}
		if st, err := os.Stat(p.Wheels); err != nil || !st.IsDir() {
			return p, fmt.Errorf("no wheels folder at %s", p.Wheels)
		}
	}
	if _, err := os.Lstat(paths.Venv); err == nil {
		p.Reinstall = true
	}
	return p, nil
}

// FindPython is the interpreter a venv is made from, absolute and
// resolved: the system's own first, since one earlier on PATH (a
// ~/.local/bin, a pyenv shim) is the human's to vouch for, and note says
// so when it is not under /usr.
func FindPython() (python, note string, err error) {
	py := systemPython
	if _, err := os.Stat(py); err != nil {
		if py, err = exec.LookPath("python3"); err != nil {
			return "", "", errors.New("no python3 found; hypruse needs Python 3.11 or later")
		}
	}
	if py, err = filepath.EvalSymlinks(py); err != nil {
		return "", "", fmt.Errorf("python3: %v", err)
	}
	if python, err = filepath.Abs(py); err != nil {
		return "", "", err
	}
	if !strings.HasPrefix(python, "/usr/") {
		note = "it is not under /usr: it comes from your PATH, not the system"
	}
	return python, note, nil
}

// Text is the plan as the install card shows it.
func (p Plan) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Make a Python venv for hypruse at %s, from %s.\n", p.Venv, p.Python)
	if p.PythonNote != "" {
		fmt.Fprintf(&b, "Note on that interpreter: %s.\n", p.PythonNote)
	}
	if p.Reinstall {
		b.WriteString("The venv there now is removed first.\n")
	}
	fmt.Fprintf(&b, "Copy hypruse %s (%s, commit %s, MIT) from %s: %d files, each checked against the SHA-256 Den was built with. Nothing of it runs now.\n",
		p.Pin.Version, p.Pin.URL, short(p.Pin.Commit), p.Clone, len(p.Pin.Files))
	if p.Wheels != "" {
		fmt.Fprintf(&b, "Install %d packages it needs (mcp and its dependencies) from %s, offline, each pinned by hash; wheels only, no build code runs.\n", p.Packages, p.Wheels)
	} else {
		fmt.Fprintf(&b, "Download %d packages from PyPI (mcp and its dependencies), pinned by hash; wheels only, no build code runs.\n", p.Packages)
	}
	b.WriteString("Compile the Python files to bytecode now, so nothing is written into the venv later.\n")
	fmt.Fprintf(&b, "Record a hash of every installed file in %s; Den checks them before every desktop agent starts.", p.Manifest)
	return b.String()
}

// LockPackages counts the requirements in a lock that apply on Linux:
// those without a marker, or whose marker does not ask for Windows.
func LockPackages(lock string) int {
	n := 0
	for _, line := range strings.Split(lock, "\n") {
		if line == "" || line[0] == ' ' || line[0] == '#' {
			continue
		}
		if strings.Contains(line, "sys_platform == 'win32'") {
			continue
		}
		n++
	}
	return n
}

// VerifyClone checks that the clone holds the pinned files, byte for byte,
// and that hypruse's package folder holds nothing else (an extra module
// would be imported like the rest). It reads files only: no git command
// runs in the clone, since a repository's config can run programs.
func VerifyClone(pin Pin, clone string) error {
	var bad []string
	for rel, want := range pin.Files {
		got, err := fileSHA256(filepath.Join(clone, rel))
		if err != nil {
			bad = append(bad, rel+" (missing)")
		} else if got != want {
			bad = append(bad, rel+" (differs)")
		}
	}
	entries, err := os.ReadDir(filepath.Join(clone, PackageDir))
	if err != nil {
		return fmt.Errorf("%s is not a hypruse clone: %v", clone, err)
	}
	for _, e := range entries {
		rel := PackageDir + "/" + e.Name()
		if _, ok := pin.Files[rel]; !ok && e.Name() != "__pycache__" {
			bad = append(bad, rel+" (not in hypruse "+pin.Version+")")
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("the clone at %s is not hypruse %s at %s, unmodified: %s", clone, pin.Version, short(pin.Commit), strings.Join(bad, ", "))
	}
	return nil
}

// systemPython is the interpreter MakePlan prefers; tests replace it.
var systemPython = "/usr/bin/python3"

// Runner runs a command for the install; tests replace it.
type Runner func(ctx context.Context, env []string, argv ...string) ([]byte, error)

// Run is the real Runner.
func Run(ctx context.Context, env []string, argv ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Dir = "/"
	return cmd.CombinedOutput()
}

// installEnv is the environment pip and venv run in: only what they need,
// so no PIP_* variable, pip.conf or user site changes what is installed.
func installEnv() []string {
	env := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + os.Getenv("HOME"),
		"LANG=C.UTF-8",
		"PIP_CONFIG_FILE=" + os.DevNull,
		"PIP_DISABLE_PIP_VERSION_CHECK=1",
		"PIP_NO_INPUT=1",
		"PYTHONNOUSERSITE=1",
		"PYTHONDONTWRITEBYTECODE=1",
	}
	// A proxy the human set up is how pip reaches PyPI from here.
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// Install carries out an approved plan. It checks the clone again (it may
// have changed since the card was shown), makes the venv, installs the
// locked dependencies, copies hypruse's files and records the manifest.
// A failure part way leaves no manifest, so no spawn will use the venv.
func Install(ctx context.Context, p Plan, run Runner) (string, error) {
	if err := VerifyClone(p.Pin, p.Clone); err != nil {
		return "", err
	}
	_ = os.Remove(p.Manifest)
	// A link at either path could aim the chmod or the removal elsewhere.
	for _, d := range []string{p.Root, p.Venv} {
		if st, err := os.Lstat(d); err == nil && st.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%s is a symbolic link; Den installs only into a real folder", d)
		}
	}
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(p.Root, 0o700); err != nil {
		return "", err
	}
	if _, err := os.Lstat(p.Venv); err == nil {
		if err := os.RemoveAll(p.Venv); err != nil {
			return "", fmt.Errorf("remove the old venv: %v", err)
		}
	}
	env := installEnv()
	if out, err := run(ctx, env, p.Python, "-I", "-m", "venv", p.Venv); err != nil {
		return "", fmt.Errorf("python -m venv: %v: %s", err, lastLine(out))
	}
	lock := filepath.Join(p.Root, "requirements.lock")
	if err := os.WriteFile(lock, []byte(Lock), 0o600); err != nil {
		return "", err
	}
	argv := []string{p.Paths.Python(), "-I", "-m", "pip", "install", "--isolated", "--require-hashes", "--only-binary", ":all:", "--no-deps", "--no-compile", "-r", lock}
	if p.Wheels != "" {
		argv = append(argv, "--no-index", "--find-links", p.Wheels)
	}
	if out, err := run(ctx, env, argv...); err != nil {
		return "", fmt.Errorf("pip install: %v: %s", err, lastLine(out))
	}
	site, err := SitePackages(p.Venv)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(site, "hypruse")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", err
	}
	for rel, want := range p.Pin.Files {
		name := filepath.Base(rel)
		if rel == "LICENSE" {
			name = "LICENSE"
		} else if filepath.Dir(rel) != PackageDir {
			continue
		}
		if err := copyChecked(filepath.Join(p.Clone, rel), filepath.Join(dst, name), want); err != nil {
			return "", err
		}
	}
	// Bytecode now, recorded in the manifest with everything else: the
	// server runs with -B and never writes any, so a .pyc that appears
	// later is not Den's (it is code, and Verify counts it as added).
	// checked-hash: a .pyc is used only while its source's hash matches.
	if out, err := run(ctx, env, p.Paths.Python(), "-I", "-m", "compileall", "-q", "--invalidation-mode", "checked-hash", site); err != nil {
		return "", fmt.Errorf("compileall: %v: %s", err, lastLine(out))
	}
	m, err := Scan(p.Venv)
	if err != nil {
		return "", err
	}
	m.Interpreter = p.Python
	if err := m.Save(p.Manifest); err != nil {
		return "", err
	}
	return fmt.Sprintf("hypruse %s in %s (%d files recorded)", p.Pin.Version, p.Venv, len(m.Files)), nil
}

// SitePackages is the venv's one site-packages folder.
func SitePackages(venv string) (string, error) {
	found, _ := filepath.Glob(filepath.Join(venv, "lib", "python3*", "site-packages"))
	if len(found) != 1 {
		return "", fmt.Errorf("expected one site-packages in %s, found %d", venv, len(found))
	}
	return found[0], nil
}

// copyChecked copies src to dst only when src has the wanted hash, and
// hashes what it wrote, so a file swapped mid-copy is caught.
func copyChecked(src, dst, want string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if sum(data) != want {
		return fmt.Errorf("%s changed since it was checked; nothing was copied from it", src)
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if got, err := fileSHA256(tmp); err != nil || got != want {
		os.Remove(tmp)
		return fmt.Errorf("writing %s did not give the pinned bytes", dst)
	}
	return os.Rename(tmp, dst)
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func lastLine(out []byte) string {
	out = bytes.TrimSpace(out)
	if i := bytes.LastIndexByte(out, '\n'); i >= 0 {
		out = out[i+1:]
	}
	if len(out) > 300 {
		out = out[:300]
	}
	return string(out)
}

// short is a commit's first seven characters.
func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
