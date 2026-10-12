package sshtest

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// SessionOpts describes a fake remote Claude Code session. Zero values give a
// neutral idle session.
type SessionOpts struct {
	ID     string // default SessionA
	Name   string // peer name in the session file
	CWD    string // default /home/user/proj
	Status string // "idle" (default) or "busy"
	// Badge store entry (role and task as agent-badge saved them).
	Role, Task string
	// Transcript content. Any of these set writes a transcript.
	Prompt, Reply, Command, Title string
}

// SecretOpts returns options whose prompt, reply, command, cwd and title all
// carry SECRET-* markers, for the "nothing leaks" grep test.
func SecretOpts() SessionOpts {
	return SessionOpts{
		CWD:     "/home/user/SECRET-cwd",
		Prompt:  "SECRET-prompt please read the file",
		Reply:   "SECRET-reply here is what I found",
		Command: "cat /home/user/SECRET-command",
		Title:   "SECRET-title",
	}
}

// Session is a fake session backed by a live dummy process so that kill(pid,
// 0) says it is alive, like discover expects. The process is sleep run under
// the name "claude", and the session's startedAt is when it started, so the
// closer's check that a pid is still the session's Claude Code passes for it
// as it does for a real session.
type Session struct {
	ID   string
	PID  int
	File string // ~/.claude/sessions/<pid>.json
	h    *seedDir
	opts SessionOpts
	proc *exec.Cmd
	// started is the session's startedAt: when its process started, in ms.
	started int64
	// reaped is closed once the dummy process has exited and been waited
	// for: a zombie would still answer kill(pid, 0) as alive.
	reaped chan struct{}
}

// seedDir is a fake ~/.claude that sessions are written into: a remote's or
// the local side's.
type seedDir struct {
	lab        *Lab
	claudeDir  string
	runtimeDir string
	sessions   []*Session
}

// SeedSession starts a dummy process and writes the session file, and when
// asked a transcript and a badge store entry, into the remote's fake ~/.claude.
func (r *Remote) SeedSession(o SessionOpts) *Session {
	r.lab.t.Helper()
	return r.seed.add(o)
}

// SeedSession is Remote.SeedSession for the local side's fake ~/.claude.
func (lo *Local) SeedSession(o SessionOpts) *Session {
	lo.lab.t.Helper()
	return lo.seed.add(o)
}

func (h *seedDir) add(o SessionOpts) *Session {
	t := h.lab.t
	t.Helper()
	if o.ID == "" {
		o.ID = SessionA
	}
	if o.CWD == "" {
		o.CWD = "/home/user/proj"
	}
	if o.Status == "" {
		o.Status = "idle"
	}
	cmd := exec.Command(h.lab.claudeStandIn(), "3600")
	cmd.Env = append(os.Environ(), "DEN_SSHTEST="+h.lab.Dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	must(t, cmd.Start())
	s := &Session{ID: o.ID, PID: cmd.Process.Pid, h: h, opts: o, proc: cmd, reaped: make(chan struct{}), started: time.Now().UnixMilli()}
	go func() { cmd.Wait(); close(s.reaped) }()
	h.sessions = append(h.sessions, s)
	s.File = filepath.Join(h.claudeDir, "sessions", fmt.Sprintf("%d.json", s.PID))
	s.writeFile(o.Status)

	if o.Prompt != "" || o.Reply != "" || o.Command != "" || o.Title != "" {
		s.writeTranscript()
	}
	if o.Role != "" || o.Task != "" {
		s.writeBadge()
	}
	return s
}

func (s *Session) writeFile(status string) {
	now := time.Now().UnixMilli()
	b, _ := json.Marshal(map[string]any{
		"pid": s.PID, "sessionId": s.ID, "cwd": s.opts.CWD, "startedAt": s.started,
		"version": "2.1.289", "kind": "interactive", "entrypoint": "cli", "name": s.opts.Name,
		"status": status, "updatedAt": now, "statusUpdatedAt": now,
		"messagingSocketPath": s.Socket(),
		"peerFeatures":        []string{},
	})
	tmp := s.File + ".tmp"
	must(s.h.lab.t, os.WriteFile(tmp, b, 0o644))
	must(s.h.lab.t, os.Rename(tmp, s.File)) // atomic, like Claude Code's writes
}

// SetStatus rewrites the session file with a new busy/idle status.
func (s *Session) SetStatus(status string) { s.writeFile(status) }

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

func (s *Session) writeTranscript() {
	o := s.opts
	dir := filepath.Join(s.h.claudeDir, "projects", nonAlnum.ReplaceAllString(o.CWD, "-"))
	must(s.h.lab.t, os.MkdirAll(dir, 0o755))
	var b strings.Builder
	line := func(v any) { j, _ := json.Marshal(v); b.Write(j); b.WriteByte('\n') }
	if o.Prompt != "" {
		line(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": o.Prompt}})
	}
	var content []any
	if o.Reply != "" {
		content = append(content, map[string]any{"type": "text", "text": o.Reply})
	}
	if o.Command != "" {
		content = append(content, map[string]any{"type": "tool_use", "id": "t1", "name": "Bash",
			"input": map[string]any{"command": o.Command, "description": "Run it"}})
	}
	if len(content) > 0 {
		line(map[string]any{"type": "assistant", "message": map[string]any{"model": "claude-sonnet-5-5", "content": content,
			"usage": map[string]any{"input_tokens": 20, "output_tokens": 30, "cache_read_input_tokens": 1000, "cache_creation_input_tokens": 0}}})
	}
	if o.Title != "" {
		line(map[string]any{"type": "ai-title", "aiTitle": o.Title, "sessionId": s.ID})
	}
	must(s.h.lab.t, os.WriteFile(filepath.Join(dir, s.ID+".jsonl"), []byte(b.String()), 0o644))
}

func (s *Session) writeBadge() {
	dir := filepath.Join(s.h.claudeDir, "plugins", "store")
	must(s.h.lab.t, os.MkdirAll(dir, 0o755))
	p := filepath.Join(dir, "agent-badge_test.json")
	store := map[string]any{}
	if b, err := os.ReadFile(p); err == nil {
		json.Unmarshal(b, &store)
	}
	store["badge:"+s.ID] = map[string]any{"role": s.opts.Role, "task": s.opts.Task, "history": []string{}}
	b, _ := json.Marshal(store)
	must(s.h.lab.t, os.WriteFile(p, b, 0o644))
}

// SetBadge rewrites the badge store entry (use it to put hostile text in a role).
func (s *Session) SetBadge(role, task string) {
	s.opts.Role, s.opts.Task = role, task
	s.writeBadge()
}

// Stop kills the dummy process and reaps it, so the session reads as dead
// (a zombie would still answer kill(pid, 0)). The session file stays, as it
// does when Claude Code is killed.
func (s *Session) Stop() {
	if s.proc == nil {
		return
	}
	syscall.Kill(-s.proc.Process.Pid, syscall.SIGKILL)
	<-s.reaped
	s.proc = nil
}

// Exited reports whether the dummy process is gone (something killed it).
func (s *Session) Exited() bool {
	select {
	case <-s.reaped:
		return true
	default:
		return false
	}
}

// Socket is the messagingSocketPath the session file names: a test that
// listens there stands in for Claude Code's inbox socket.
func (s *Session) Socket() string {
	return filepath.Join(s.h.runtimeDir, fmt.Sprintf("msg-%d.sock", s.PID))
}

// Remove stops the process and deletes the session file, a clean exit.
func (s *Session) Remove() {
	s.Stop()
	os.Remove(s.File)
}

func (h *seedDir) stop() {
	for _, s := range h.sessions {
		s.Stop()
	}
}

// claudeStandIn is sleep under the name claude (a symlink in the lab), so a
// dummy session process has the comm a native Claude Code install has.
func (l *Lab) claudeStandIn() string {
	p := filepath.Join(l.Dir, "bin", "claude")
	if _, err := os.Lstat(p); err == nil {
		return p
	}
	sleep, err := exec.LookPath("sleep")
	must(l.t, err)
	must(l.t, os.MkdirAll(filepath.Dir(p), 0o755))
	if err := os.Symlink(sleep, p); err != nil && !os.IsExist(err) {
		l.t.Fatalf("sshtest: %v", err)
	}
	return p
}
