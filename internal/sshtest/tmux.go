package sshtest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Tmux is a private tmux server (its own socket under the scratch dir) running
// one command, normally the den TUI, for end-to-end tests. It is started with
// the local isolated environment passed through `new-session -e`, because a
// tmux server's global environment belongs to whoever started it.
type Tmux struct {
	lab     *Lab
	socket  string
	env     []string
	PanePID int
}

// StartTUI starts argv (den, by default) in a cols x rows window of a private
// tmux server and proves, from /proc/<pid>/environ, that the process runs in
// the isolated environment before anything is typed. The server is killed at
// cleanup. extra is more KEY=VALUE environment for the process.
func (lo *Local) StartTUI(cols, rows int, extra []string, argv ...string) *Tmux {
	t := lo.lab.t
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("sshtest: tmux not found")
	}
	if len(argv) == 0 {
		argv = []string{lo.lab.Den}
	}
	tm := &Tmux{lab: lo.lab, socket: lo.TmuxSocket}
	// The tmux client and server themselves get the scratch environment too,
	// with the socket directory inside the lab.
	tm.env = append(lo.Env(), "TMUX_TMPDIR="+filepath.Join(lo.lab.Dir, "tm"), "TMUX=")
	must(t, os.MkdirAll(filepath.Join(lo.lab.Dir, "tm"), 0o700))
	t.Cleanup(func() {
		tm.run("kill-server")
		os.RemoveAll(filepath.Join(lo.lab.Dir, "tm"))
	})

	args := []string{"-L", tm.socket, "-f", "/dev/null", "new-session", "-d", "-s", "v", "-x", strconv.Itoa(cols), "-y", strconv.Itoa(rows)}
	penv := append(lo.Env(), extra...)
	for _, kv := range penv {
		args = append(args, "-e", kv)
	}
	args = append(args, argv...)
	if out, err := tm.cmd(args...).CombinedOutput(); err != nil {
		t.Fatalf("sshtest: tmux new-session: %v: %s", err, out)
	}
	out, err := tm.cmd("-L", tm.socket, "list-panes", "-t", "v", "-F", "#{pane_pid}").Output()
	if err != nil {
		t.Fatalf("sshtest: tmux list-panes: %v", err)
	}
	tm.PanePID, _ = strconv.Atoi(strings.TrimSpace(string(out)))
	tm.proveIsolation(lo, penv)
	return tm
}

func (tm *Tmux) cmd(args ...string) *exec.Cmd {
	c := exec.Command("tmux", args...)
	c.Env = tm.env
	c.Dir = tm.lab.Local.Home
	return c
}

func (tm *Tmux) run(args ...string) ([]byte, error) {
	return tm.cmd(append([]string{"-L", tm.socket}, args...)...).CombinedOutput()
}

// proveIsolation reads the pane process's own environment and fails the test
// unless every isolated variable has its scratch value and nothing mentions
// the real home. The pane may be the den process or its shell parent: it
// follows the pid until the environment has DEN_SOCKET.
func (tm *Tmux) proveIsolation(lo *Local, want []string) {
	t := tm.lab.t
	t.Helper()
	var env map[string]string
	ok := WaitFor(5*time.Second, func() bool {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", tm.PanePID))
		if err != nil {
			return false
		}
		env = map[string]string{}
		for _, kv := range strings.Split(string(b), "\x00") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				env[k] = v
			}
		}
		return env["DEN_SOCKET"] != ""
	})
	if !ok {
		t.Fatalf("sshtest: could not read the TUI's environment (pid %d)", tm.PanePID)
	}
	real := realHome
	for _, k := range []string{"HOME", "CLAUDE_CONFIG_DIR", "XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "DEN_SOCKET", "DEN_CONFIG", "DEN_SSH", "DEN_SCP"} {
		var w string
		for _, kv := range want {
			if v, ok := strings.CutPrefix(kv, k+"="); ok {
				w = v
			}
		}
		if env[k] != w || w == "" {
			t.Fatalf("sshtest: isolation: the TUI's %s = %q, want %q", k, env[k], w)
		}
		if !within(env[k], lo.lab.Dir) {
			t.Fatalf("sshtest: isolation: the TUI's %s = %q is outside the scratch dir", k, env[k])
		}
		t.Logf("sshtest: TUI pid %d environ %s=%s", tm.PanePID, k, env[k])
	}
	for k, v := range env {
		if real != "" && strings.Contains(v, real) {
			t.Fatalf("sshtest: isolation: the TUI's %s mentions the real home: %q", k, v)
		}
	}
}

// Capture returns the visible pane as text.
func (tm *Tmux) Capture() string {
	out, _ := tm.run("capture-pane", "-p", "-t", "v")
	return string(out)
}

// Keys sends keys as tmux names them (C-y, Enter, Escape) or literal text
// with Type. Escape must be sent alone: followed quickly by text it arrives as
// Alt+key.
func (tm *Tmux) Keys(keys ...string) {
	tm.lab.t.Helper()
	if out, err := tm.run(append([]string{"send-keys", "-t", "v"}, keys...)...); err != nil {
		tm.lab.t.Fatalf("sshtest: send-keys: %v: %s", err, out)
	}
}

// Type sends text literally.
func (tm *Tmux) Type(text string) {
	tm.lab.t.Helper()
	if out, err := tm.run("send-keys", "-t", "v", "-l", text); err != nil {
		tm.lab.t.Fatalf("sshtest: send-keys: %v: %s", err, out)
	}
}

// WaitFor polls the pane until every substring is on screen, and returns that
// frame. On timeout it fails the test with the last frame.
func (tm *Tmux) WaitFor(timeout time.Duration, subs ...string) string {
	tm.lab.t.Helper()
	var frame string
	ok := WaitFor(timeout, func() bool {
		frame = tm.Capture()
		for _, s := range subs {
			if !strings.Contains(frame, s) {
				return false
			}
		}
		return true
	})
	if !ok {
		tm.lab.t.Fatalf("sshtest: timed out after %v waiting for %q on screen; last frame:\n%s", timeout, subs, Squeeze(frame))
	}
	return frame
}

// Alive reports whether the pane's process still runs.
func (tm *Tmux) Alive() bool {
	out, err := tm.run("list-panes", "-t", "v", "-F", "#{pane_dead}")
	return err == nil && strings.TrimSpace(string(out)) == "0"
}

// Squeeze drops trailing blanks and runs of empty or box-only lines, so a
// logged frame is the part that says something.
func Squeeze(f string) string {
	var out []string
	for _, l := range strings.Split(f, "\n") {
		l = strings.TrimRight(l, " ")
		if strings.Trim(l, " │┌┐└┘─") == "" && len(out) > 0 && strings.Trim(out[len(out)-1], " │┌┐└┘─") == "" {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}
