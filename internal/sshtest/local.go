package sshtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Local is the isolated environment of the "local" Den under test: its own
// HOME, CLAUDE_CONFIG_DIR, XDG_*, DEN_SOCKET, DEN_CONFIG (with a remotes.toml
// beside it), a private tmux socket name, and a notify-send stub that records
// its arguments.
type Local struct {
	lab  *Lab
	seed *seedDir

	Home       string
	ClaudeDir  string
	StateHome  string
	ConfigHome string
	RuntimeDir string
	Socket     string // DEN_SOCKET
	Config     string // DEN_CONFIG (config.toml); remotes.toml lives beside it
	// TmuxSocket is a unique name for `tmux -L`, if a test ever needs a server.
	TmuxSocket string
	// Stubs holds fake desktop tools first on PATH (notify-send).
	Stubs string
	// NotifyLog collects one line per notify-send call, arguments separated by ␟ (U+241F).
	NotifyLog string
}

func newLocal(l *Lab) *Local {
	t := l.t
	d := filepath.Join(l.Dir, "l")
	lo := &Local{lab: l, Home: filepath.Join(d, "home")}
	lo.ClaudeDir = filepath.Join(lo.Home, ".claude")
	lo.StateHome = filepath.Join(lo.Home, ".local", "state")
	lo.ConfigHome = filepath.Join(lo.Home, ".config")
	// Short: ssh's ControlPath (<runtime>/den/ssh/<40 chars>, plus ssh's 17
	// character temporary suffix) must stay under a Unix socket's 108 bytes.
	lo.RuntimeDir = filepath.Join(l.Dir, "lr")
	lo.Socket = filepath.Join(lo.RuntimeDir, "den.sock")
	lo.Config = filepath.Join(lo.ConfigHome, "den", "config.toml")
	lo.seed = &seedDir{lab: l, claudeDir: lo.ClaudeDir, runtimeDir: lo.RuntimeDir}
	if n := len(lo.RuntimeDir) + len("/den/ssh/") + 40 + 17; n > 104 {
		t.Fatalf("sshtest: the local ControlPath would be %d bytes, over the Unix socket limit", n)
	}
	lo.TmuxSocket = "dt" + filepath.Base(l.Dir)
	lo.Stubs = filepath.Join(d, "stubs")
	lo.NotifyLog = filepath.Join(d, "notify.log")
	for _, p := range []string{filepath.Join(lo.ClaudeDir, "sessions"), lo.StateHome, filepath.Dir(lo.Config), lo.Stubs} {
		must(t, os.MkdirAll(p, 0o755))
	}
	must(t, os.MkdirAll(lo.RuntimeDir, 0o700))
	must(t, os.Chmod(lo.RuntimeDir, 0o700))
	stub := fmt.Sprintf("#!/bin/sh\nfirst=1\nfor a in \"$@\"; do if [ $first = 1 ]; then printf '%%s' \"$a\"; first=0; else printf '\\342\\220\\237%%s' \"$a\"; fi; done >> %s\necho >> %s\n", shq(lo.NotifyLog), shq(lo.NotifyLog))
	must(t, os.WriteFile(filepath.Join(lo.Stubs, "notify-send"), []byte(stub), 0o755))
	return lo
}

func (lo *Local) paths() map[string]string {
	return map[string]string{"HOME": lo.Home, "CLAUDE_CONFIG_DIR": lo.ClaudeDir, "XDG_STATE_HOME": lo.StateHome,
		"XDG_CONFIG_HOME": lo.ConfigHome, "XDG_RUNTIME_DIR": lo.RuntimeDir, "DEN_SOCKET": lo.Socket,
		"DEN_CONFIG": lo.Config, "stubs": lo.Stubs}
}

// StateDir is where Den keeps its local state files (events, history, ...).
func (lo *Local) StateDir() string { return filepath.Join(lo.StateHome, "den") }

// RemotesPath is remotes.toml, beside DEN_CONFIG.
func (lo *Local) RemotesPath() string { return filepath.Join(filepath.Dir(lo.Config), "remotes.toml") }

// WriteConfig writes config.toml.
func (lo *Local) WriteConfig(content string) {
	must(lo.lab.t, os.WriteFile(lo.Config, []byte(content), 0o644))
}

// WriteRemotes writes remotes.toml.
func (lo *Local) WriteRemotes(content string) {
	must(lo.lab.t, os.WriteFile(lo.RemotesPath(), []byte(content), 0o644))
}

// RemotesTOML returns a remotes.toml that lists every remote of the lab by
// label with the given policy ("" leaves the key out), in the shape of design
// §4.1. Adjust it once the real schema is final.
func (l *Lab) RemotesTOML(policy string) string {
	var b strings.Builder
	for _, k := range l.order {
		fmt.Fprintf(&b, "[hosts.%s]\ntarget = %q\n", k, l.Remotes[k].Target())
		if policy != "" {
			fmt.Fprintf(&b, "policy = %q\n", policy)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// Env is the complete environment for a local den process: only isolated
// values (nothing from the test runner's environment leaks in but TERM and
// LANG). DEN_SSH and DEN_SCP point at the scratch wrappers.
func (lo *Local) Env() []string {
	return []string{
		"PATH=" + lo.Stubs + ":" + lo.lab.Bin + ":/usr/local/bin:/usr/bin:/bin",
		"HOME=" + lo.Home,
		"CLAUDE_CONFIG_DIR=" + lo.ClaudeDir,
		"XDG_STATE_HOME=" + lo.StateHome,
		"XDG_CONFIG_HOME=" + lo.ConfigHome,
		"XDG_RUNTIME_DIR=" + lo.RuntimeDir,
		"DEN_SOCKET=" + lo.Socket,
		"DEN_CONFIG=" + lo.Config,
		"DEN_SSH=" + lo.lab.SSH,
		"DEN_SCP=" + lo.lab.SCP,
		"HYPRLAND_INSTANCE_SIGNATURE=",
		"TERM=xterm-256color",
		"LANG=C.UTF-8",
		"DEN_SSHTEST=" + lo.lab.Dir,
	}
}

// Apply sets the Env in the test process itself (t.Setenv), for in-process
// code such as internal/remote's client. It also blanks TMUX.
func (lo *Local) Apply() {
	t := lo.lab.t
	t.Helper()
	for _, kv := range lo.Env() {
		k, v, _ := strings.Cut(kv, "=")
		if k == "PATH" {
			continue // keep go, git and friends reachable in-process
		}
		t.Setenv(k, v)
	}
	t.Setenv("PATH", lo.Stubs+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX", "")
}

// Notifications returns the notify-send calls recorded so far, one per
// element, arguments joined with a space.
func (lo *Local) Notifications() []string {
	b, _ := os.ReadFile(lo.NotifyLog)
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l != "" {
			out = append(out, strings.ReplaceAll(l, "␟", " "))
		}
	}
	return out
}

// Grep returns the files under the local home (all Den state, config and
// claude files) whose bytes contain needle.
func (lo *Local) Grep(needle string) []string {
	var hits []string
	filepath.Walk(lo.Home, func(p string, fi os.FileInfo, err error) error {
		if err != nil || !fi.Mode().IsRegular() {
			return nil
		}
		if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), needle) {
			hits = append(hits, p)
		}
		return nil
	})
	return hits
}
