package api

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// helperEnv names the socket a TestNonDumpableHelper process dials.
const helperEnv = "DEN_API_TEST_NONDUMPABLE"

// TestNonDumpableHelper is not a test of its own: TestNonDumpableIsItsOwn
// runs it as a child, which makes itself non-dumpable (as a hardened app
// does), says hello on the socket, and waits until its stdin closes.
func TestNonDumpableHelper(t *testing.T) {
	path := os.Getenv(helperEnv)
	if path == "" {
		t.Skip("run as a child only")
	}
	if _, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, 4 /* PR_SET_DUMPABLE */, 0, 0, 0, 0, 0); e != 0 {
		t.Fatal(e)
	}
	nc, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	nc.Write([]byte(`{"v":1,"id":1,"op":"hello","params":{"v":1,"client":"test","version":"0"}}` + "\n"))
	if _, err := bufio.NewReader(nc).ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("ready\n")
	io.Copy(io.Discard, os.Stdin)
}

// A process that hides its executable is known as that one process in this
// boot: never "", never the same as another such process, and named to the
// human as a program Den cannot identify.
func TestNonDumpableIsItsOwn(t *testing.T) {
	h := newHarness(t, 2)
	start := func() int {
		cmd := exec.Command(os.Args[0], "-test.run=^TestNonDumpableHelper$", "-test.count=1")
		cmd.Env = append(os.Environ(), helperEnv+"="+h.s.Path())
		in, _ := cmd.StdinPipe()
		out, _ := cmd.StdoutPipe()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { in.Close(); cmd.Wait() })
		ready := make(chan bool, 1)
		go func() {
			sc := bufio.NewScanner(out)
			for sc.Scan() {
				if sc.Text() == "ready" {
					ready <- true
				}
			}
		}()
		select {
		case <-ready:
		case <-time.After(10 * time.Second):
			t.Fatal("the helper did not connect")
		}
		return cmd.Process.Pid
	}
	pids := map[int]bool{start(): true, start(): true}
	seen := map[string]bool{}
	for _, c := range h.s.Clients() {
		if !pids[c.PID] {
			continue
		}
		want := unreadablePrefix + bootID() + ":" + strconv.Itoa(c.PID) + ":"
		if !strings.HasPrefix(c.Exe, want) || !Unidentified(c.Exe) {
			t.Errorf("pid %d: identity %q, want %s<start>", c.PID, c.Exe, want)
		}
		if c.Label != "a program Den cannot identify" {
			t.Errorf("pid %d: label %q", c.PID, c.Label)
		}
		seen[c.Exe] = true
	}
	if len(seen) != 2 {
		t.Errorf("two non-dumpable processes, identities %v", seen)
	}
}

// asUnidentified makes every new connection a different process whose
// executable Den cannot read.
func asUnidentified(s *Server) {
	var n atomic.Int32
	s.setIdentify(func(p Peer) Peer {
		p.Exe = unreadableExe(9000+int(n.Add(1)), 77)
		return p
	})
}

// Two such processes share nothing: not a grant or its answer, not an
// ask to cancel, not a pairing.
func TestUnidentifiedShareNothing(t *testing.T) {
	h := newHarness(t, 2)
	asUnidentified(h.s)
	a := h.dial()
	a.hello()
	l := a.call("grant.request", manifest("for a"))
	var r struct{ ID string }
	json.Unmarshal(l.Result, &r)
	nextAct(t, h.s)
	a.call("grant.events", nil)
	b := h.dial()
	b.hello()
	b.call("grant.events", nil)
	h.s.GrantAnswered(r.ID, "granted")
	b.nc.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if raw, err := b.r.ReadBytes('\n'); err == nil {
		t.Errorf("another unidentified process got the answer: %s", raw)
	}
	var ev GrantAnswer
	if e := a.next(); json.Unmarshal(e.Data, &ev) != nil || ev.ID != r.ID {
		t.Errorf("the asker's answer: %+v", e)
	}

	// An ask: the other cannot cancel it.
	os.WriteFile(h.lane+"/slow", nil, 0o600)
	q := a.call("ask", map[string]string{"text": "slow"})
	var ask struct{ Ask string }
	json.Unmarshal(q.Result, &ask)
	waitChunk(t, a, "text")
	if l := b.call("ask.cancel", map[string]string{"ask": ask.Ask}); code(l) != CodeNotFound {
		t.Errorf("another unidentified process cancelled the ask: %+v", l)
	}
	os.Remove(h.lane + "/slow")

	// A pairing: the other cannot prove it, even with the key.
	_, id, key := paired(t, h)
	c := h.dial()
	c.hello()
	nonce, conn := challengeOf(t, c)
	if l := c.call("pair.auth", map[string]string{"pairing": id, "mac": pairMAC(key, "app-to-den", conn, nonce)}); code(l) != CodeForbidden {
		t.Errorf("another unidentified process passed the pairing: %+v", l)
	}
}

// An identity Den read and one it could not never match, either way.
func TestReadableAndUnreadableNeverMatch(t *testing.T) {
	h := newHarness(t, 2)
	_, id, key := paired(t, h) // a program Den read
	asUnidentified(h.s)
	c := h.dial()
	c.hello()
	nonce, conn := challengeOf(t, c)
	if l := c.call("pair.auth", map[string]string{"pairing": id, "mac": pairMAC(key, "app-to-den", conn, nonce)}); code(l) != CodeForbidden {
		t.Errorf("known, then unreadable: %+v", l)
	}
	h2 := newHarness(t, 2)
	asUnidentified(h2.s)
	_, id2, key2 := paired(t, h2)
	h2.s.setIdentify(nil) // now a program Den reads
	d := h2.dial()
	d.hello()
	nonce, conn = challengeOf(t, d)
	if l := d.call("pair.auth", map[string]string{"pairing": id2, "mac": pairMAC(key2, "app-to-den", conn, nonce)}); code(l) != CodeForbidden {
		t.Errorf("unreadable, then known: %+v", l)
	}
}

// The human never sees "." or the identity itself.
func TestExeLabels(t *testing.T) {
	for exe, want := range map[string]string{
		"/usr/bin/app-host":             "app-host",
		"":                              "a program Den cannot identify",
		"/":                             "a program Den cannot identify",
		unreadableExe(4242, 99):         "a program Den cannot identify",
		unreadablePrefix + "x:conn:abc": "a program Den cannot identify",
	} {
		if got := ExeLabel(exe); got != want {
			t.Errorf("ExeLabel(%q) = %q, want %q", exe, got, want)
		}
		if Unidentified(exe) && (strings.Contains(ExeShown(exe), unreadablePrefix) || strings.Contains(ExeShownShort(exe), unreadablePrefix)) {
			t.Errorf("%q shown as the identity", exe)
		}
	}
	if Unidentified("/usr/bin/app-host") || !Unidentified(unreadableExe(1, 1)) {
		t.Error("Unidentified")
	}
}

// A program that restarts in a loop, each time a new identity, earns at
// most maxConnectNotes connect notes, and its refusals one entry.
func TestUnidentifiedRestartLoopIsCapped(t *testing.T) {
	h := newHarness(t, 2)
	asUnidentified(h.s)
	for range maxConnectNotes + 5 {
		c := h.dial()
		c.hello()
		c.nc.Close()
	}
	notes := 0
	for done := false; !done; {
		select {
		case n := <-h.s.Notes():
			if strings.Contains(n, "an app connected") {
				notes++
			}
		case <-time.After(300 * time.Millisecond):
			done = true
		}
	}
	if notes != maxConnectNotes {
		t.Errorf("%d connect notes, want %d", notes, maxConnectNotes)
	}

	// Refused (under an agent) from ten restarts: one entry, one note.
	h2 := newHarness(t, 2)
	h2.s.Publish(Project(Snapshot{Taken: time.Now(), AgentProcs: []AgentProc{{PID: 9100}}}))
	var n atomic.Int32
	h2.s.setIdentify(func(p Peer) Peer {
		p.PID, p.Exe = 9100, unreadableExe(9100, uint64(n.Add(1)))
		return p
	})
	for range 10 {
		c := h2.dial()
		c.hello()
		c.nc.Close()
	}
	if rs := h2.s.Refusals(); len(rs) != 1 || rs[0].Count != 10 {
		t.Errorf("refusals %+v", rs)
	}

	// Past maxRefusals programs Den read, the rest count as one entry.
	s := &Server{refusals: map[string]*refusal{}}
	for i := range maxRefusals + 10 {
		s.refuse(Peer{PID: i + 1, Exe: "/tmp/copy-" + strconv.Itoa(i)}, refusedAgent, "", time.Now())
	}
	if rs := s.Refusals(); len(rs) != maxRefusals+1 {
		t.Errorf("%d refusal entries, want %d", len(rs), maxRefusals+1)
	}
}
