package bridge

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestSendAndAskRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "den.sock")
	srv, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	if _, err := Listen(path); err != ErrAlreadyRunning {
		t.Errorf("second listen = %v, want ErrAlreadyRunning", err)
	}

	payload, _ := json.Marshal(map[string]any{"session_id": "s1", "hook_event_name": "Stop", "last_assistant_message": "done"})
	if !Send(path, Envelope{Seq: 1, Event: "Stop", SessionID: "s1", Payload: payload}) {
		t.Fatal("send failed")
	}
	req := <-srv.Requests()
	if req.Event != "Stop" || req.Input.LastAssistant != "done" || req.WantsReply() {
		t.Errorf("got %+v", req.Envelope)
	}

	go func() {
		r := <-srv.Requests()
		if r.Input.ToolName != "Bash" || !r.WantsReply() {
			t.Errorf("permission request = %+v", r.Input)
		}
		r.Reply(Decision{Behavior: "allow"})
		r.Reply(Decision{Behavior: "deny"}) // ignored
	}()
	payload, _ = json.Marshal(map[string]any{"session_id": "s1", "hook_event_name": "PermissionRequest", "tool_name": "Bash", "tool_input": map[string]string{"command": "ls"}})
	d, ok := Ask(path, Envelope{Seq: 2, Event: "PermissionRequest", SessionID: "s1", Payload: payload}, 2*time.Second)
	if !ok || d.Behavior != "allow" {
		t.Errorf("ask = %+v %v", d, ok)
	}

	if Send(filepath.Join(t.TempDir(), "missing.sock"), Envelope{}) {
		t.Error("send to missing socket should fail open")
	}
}

func TestAnswersRoundTripAndHangUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "den.sock")
	srv, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	payload, _ := json.Marshal(map[string]any{"session_id": "s1", "hook_event_name": "PermissionRequest", "tool_name": "AskUserQuestion"})
	env := Envelope{Event: "PermissionRequest", SessionID: "s1", Payload: payload}

	go func() {
		r := <-srv.Requests()
		r.Reply(Decision{Behavior: "allow", Answers: map[string]string{"Which one?": "Warm"}})
	}()
	d, ok := Ask(path, env, 2*time.Second)
	if !ok || d.Answers["Which one?"] != "Warm" {
		t.Errorf("ask = %+v %v", d, ok)
	}

	// A hook that gives up first: Den sees the hang-up.
	done := make(chan struct{})
	go func() {
		Ask(path, env, 50*time.Millisecond)
		close(done)
	}()
	r := <-srv.Requests()
	if r.HungUp() {
		t.Error("hung up before the hook gave up")
	}
	<-done
	deadline := time.Now().Add(2 * time.Second)
	for !r.HungUp() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !r.HungUp() {
		t.Error("hang-up not noticed")
	}
	r.Reply(Decision{}) // nobody listens; must not block or panic
}

// A Den on the hook's own machine comes first: with den.sock and
// den-remote.sock both there, only den.sock is asked. With den.sock gone,
// the remote socket is asked; with neither, the hook fails open at once.
func TestAskFirstPrefersADenOnThisMachine(t *testing.T) {
	dir := shortDir(t)
	local, remote := filepath.Join(dir, "den.sock"), filepath.Join(dir, RemoteSocketName)
	answer := func(path, behavior string) (*Server, *int32) {
		srv, err := Listen(path)
		if err != nil {
			t.Fatal(err)
		}
		var n int32
		go func() {
			for r := range srv.Requests() {
				atomic.AddInt32(&n, 1)
				r.Reply(Decision{Behavior: behavior})
			}
		}()
		return srv, &n
	}
	lsrv, ln := answer(local, "deny")
	rsrv, rn := answer(remote, "allow")
	defer rsrv.Close()
	env := Envelope{Event: "PermissionRequest", SessionID: "s1", Payload: json.RawMessage(`{"hook_event_name":"PermissionRequest"}`)}
	if d, ok := AskFirst(local, remote, env, time.Second); !ok || d.Behavior != "deny" || atomic.LoadInt32(rn) != 0 {
		t.Errorf("with both: %+v %v, remote asked %d times", d, ok, atomic.LoadInt32(rn))
	}
	lsrv.Close()
	if d, ok := AskFirst(local, remote, env, time.Second); !ok || d.Behavior != "allow" || atomic.LoadInt32(ln) != 1 {
		t.Errorf("remote only: %+v %v", d, ok)
	}
	rsrv.Close()
	start := time.Now()
	if _, ok := AskFirst(local, remote, env, time.Second); ok || time.Since(start) > 50*time.Millisecond {
		t.Errorf("neither: ok %v after %v", ok, time.Since(start))
	}
}

// What trying den.sock first costs a hook on a host with no
// Den of its own. A stale den.sock file (refused) and no file at all.
func TestFailedLocalDialCost(t *testing.T) {
	dir := shortDir(t)
	missing := filepath.Join(dir, "den.sock")
	stale := filepath.Join(dir, "stale.sock")
	ln, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close() // the file stays, nobody listens
	for _, p := range []string{missing, stale} {
		const n = 2000
		start := time.Now()
		for range n {
			if c, err := net.DialTimeout("unix", p, 500*time.Millisecond); err == nil {
				c.Close()
				t.Fatal("dialled")
			}
		}
		per := time.Since(start) / n
		t.Logf("failed den.sock dial (%s): %v each, mean of %d", filepath.Base(p), per, n)
		if per > time.Millisecond {
			t.Errorf("a failed den.sock dial costs %v, over 1 ms", per)
		}
	}
}

func TestRemoteSocketPathFallbackMustBePrivate(t *testing.T) {
	t.Setenv("DEN_REMOTE_SOCKET", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	tmp := shortDir(t)
	t.Setenv("TMPDIR", tmp)
	if _, err := RemoteSocketPath(); err == nil {
		t.Error("a missing fallback dir was accepted")
	}
	os.Mkdir(FallbackDir(), 0o755)
	if _, err := RemoteSocketPath(); err == nil {
		t.Error("a fallback dir open to others was accepted")
	}
	os.Chmod(FallbackDir(), 0o700)
	if p, err := RemoteSocketPath(); err != nil || p != filepath.Join(tmp, fmt.Sprintf("den-%d", os.Getuid()), RemoteSocketName) {
		t.Errorf("private fallback: %q %v", p, err)
	}
	os.Remove(FallbackDir())
	os.Symlink(tmp, FallbackDir())
	if _, err := RemoteSocketPath(); err == nil {
		t.Error("a symlinked fallback dir was accepted")
	}
}
