package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/events"
)

// scratch gives the test its own sessions directory and event stream, and
// writes a session file for this test process, which is alive.
func scratch(t *testing.T, sid, status string) (sessionFile, stream string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	os.MkdirAll(filepath.Join(dir, "claude", "sessions"), 0o755)
	sessionFile = filepath.Join(dir, "claude", "sessions", "1.json")
	writeSession(sessionFile, sid, os.Getpid(), status)
	return sessionFile, events.Path()
}

func writeSession(path, sid string, pid int, status string) {
	raw, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": sid, "cwd": "/home/x/code/demo-app", "status": status})
	os.WriteFile(path, raw, 0o644)
}

func TestWaitIdle(t *testing.T) {
	f, stream := scratch(t, "s-idle", "busy")
	if code := waitFor("s-idle", "idle", 700*time.Millisecond, events.Open(stream)); code != exitTimeout {
		t.Fatalf("busy: code %d", code)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		writeSession(f, "s-idle", os.Getpid(), "idle")
	}()
	if code := waitFor("s-idle", "idle", 5*time.Second, events.Open(stream)); code != exitReached {
		t.Errorf("idle: code %d", code)
	}
}

func TestWaitDoneAndNeedsYouFromTheStream(t *testing.T) {
	_, stream := scratch(t, "s-work", "busy")
	events.Append(stream, events.New(events.Done, "s-work")) // before the wait: not counted
	for _, c := range []struct{ until, kind string }{{"done", events.Done}, {"needs-you", events.NeedsYou}} {
		rd := events.Open(stream)
		go func() {
			time.Sleep(300 * time.Millisecond)
			events.Append(stream, events.New(c.kind, "someone-else"))
			events.Append(stream, events.New(c.kind, "s-work"))
		}()
		if code := waitFor("s-work", c.until, 5*time.Second, rd); code != exitReached {
			t.Errorf("%s: code %d", c.until, code)
		}
	}
	rd := events.Open(stream)
	go func() {
		time.Sleep(300 * time.Millisecond)
		events.Append(stream, events.New(events.SessionEnd, "s-work"))
	}()
	if code := waitFor("s-work", "done", 5*time.Second, rd); code != exitGone {
		t.Errorf("session end: code %d", code)
	}
}

func TestWaitGoneProcess(t *testing.T) {
	f, stream := scratch(t, "s-gone", "busy")
	writeSession(f, "s-gone", 1<<22+12345, "busy") // no such pid
	if code := waitFor("s-gone", "done", 5*time.Second, events.Open(stream)); code != exitGone {
		t.Errorf("code %d", code)
	}
}
