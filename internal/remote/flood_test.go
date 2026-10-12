package remote_test

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/sshtest"
)

// A hostile host floods Den with events under the link's message cap:
// needs_you for a new session each time, or for sessions it does list,
// fast. The host runs this test binary as its `den` (floodServe), through
// the private sshd.

// floodEnv makes the test binary act as a flooding serve (see TestMain).
const floodEnv = "DEN_FLOOD_SERVE"

// floodSpec is what floodServe does: rate events a second for secs, for
// new sessions each time (unknown) or for the sessions listed in its
// snapshot (listed of them), and where it records Den's request.
type floodSpec struct {
	Rate     int    `json:"rate"`
	Secs     int    `json:"secs"`
	Unknown  bool   `json:"unknown"`
	Listed   int    `json:"listed"`
	Requests string `json:"requests"`
}

// floodServe speaks just enough of the protocol: hello, one snapshot, then
// events in batches every 10 ms, each with a cursor one past the last, from
// the cursor Den asked for. It stays until stdin closes or a write fails.
func floodServe(raw string) int {
	var sp floodSpec
	if json.Unmarshal([]byte(raw), &sp) != nil {
		return 2
	}
	in := bufio.NewReader(os.Stdin)
	line, err := in.ReadBytes('\n')
	if err != nil {
		return 1
	}
	if f, err := os.OpenFile(sp.Requests, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		f.Write(line)
		f.Close()
	}
	var req remote.Request
	json.Unmarshal(line, &req)
	cur := req.Cursor
	cur.Ino = 7
	out := bufio.NewWriter(os.Stdout)
	put := func(v any) bool {
		b, _ := json.Marshal(v)
		out.Write(append(b, '\n'))
		return true
	}
	now := time.Now().UTC()
	put(map[string]any{"type": "hello", "proto": remote.Proto, "den": "v0.2.0", "hostname": "flood", "remote_now": now, "policy": "observe", "labels": true, "cursor": cur})
	sessions := make([]string, sp.Listed)
	agents := make([]fleet.Meta, sp.Listed)
	for i := range sessions {
		sessions[i] = fmt.Sprintf("11111111-2222-4333-8444-%012d", i)
		agents[i] = fleet.Meta{Index: i, Session: sessions[i], Status: "busy", Name: "a"}
	}
	put(map[string]any{"type": "snapshot", "remote_now": now, "agents": agents})
	if out.Flush() != nil {
		return 1
	}
	end := time.Now().Add(time.Duration(sp.Secs) * time.Second)
	sent := 0
	start := time.Now()
	for time.Now().Before(end) {
		due := int(time.Since(start).Seconds() * float64(sp.Rate))
		for ; sent < due; sent++ {
			sid := ""
			if sp.Unknown || len(sessions) == 0 {
				var b [16]byte
				rand.Read(b[:])
				sid = fmt.Sprintf("%x-%x-4%x-8%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])[:36]
			} else {
				sid = sessions[sent%len(sessions)]
			}
			cur.Off++
			ev := events.Event{V: events.Version, Kind: events.NeedsYou, Session: sid, Request: "permission", Tool: "Bash", Time: time.Now().UTC()}
			put(map[string]any{"type": "event", "cursor": cur, "event": ev})
		}
		if out.Flush() != nil {
			return 0 // Den hung up
		}
		time.Sleep(10 * time.Millisecond)
	}
	in.ReadBytes('\n') // until Den closes stdin
	return 0
}

// floodRig runs the Manager against floodServe on host b.
func floodRig(t *testing.T, sp floodSpec) *rig {
	t.Helper()
	l := sshtest.New(t, "b")
	r := l.Remote("b")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sp.Requests = filepath.Join(l.Dir, "requests")
	spec, _ := json.Marshal(sp)
	stub := filepath.Join(r.Home, "flooden")
	script := fmt.Sprintf("#!/bin/sh\n%s=%s exec %s -test.run='^$'\n", floodEnv, shellQuote(string(spec)), shellQuote(self))
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	l.Local.Apply()
	l.Local.WriteRemotes(fmt.Sprintf("[hosts.b]\ntarget = \"b\"\npolicy = \"observe\"\nden_path = %q\n", stub))
	return &rig{t: t, l: l, r: r}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (g *rig) requests() []remote.Request {
	b, _ := os.ReadFile(filepath.Join(g.l.Dir, "requests"))
	var out []remote.Request
	for l := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		var r remote.Request
		if json.Unmarshal([]byte(l), &r) == nil {
			out = append(out, r)
		}
	}
	return out
}

func (g *rig) eventUpdates() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, u := range g.ups {
		if u.Kind == remote.EventArrived {
			n++
		}
	}
	return n
}

func cpuTime() time.Duration {
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

// Events for sessions the host never listed are dropped, never logged or
// shown, and a host that keeps on is disabled until the human reconnects
// it; a reconnect starts after what was dropped.
func TestFloodUnknownSessions(t *testing.T) {
	secs := 3
	if os.Getenv("DEN_SLOW_TESTS") == "1" {
		secs = 20
	}
	g := floodRig(t, floodSpec{Rate: 1500, Secs: secs, Unknown: true, Listed: 1})
	cpu0, t0 := cpuTime(), time.Now()
	g.start()
	g.wait("disabled", time.Duration(secs+10)*time.Second, func() bool { return g.statusQuiet().State == remote.Disabled })
	t.Logf("disabled after %v", time.Since(t0).Round(time.Millisecond))
	st := g.status()
	if !strings.Contains(st.Reason, "flooding") || !strings.Contains(st.Hint, "/remote connect b") {
		t.Errorf("reason %q hint %q", st.Reason, st.Hint)
	}
	if !g.flagged("never listed") || st.Dropped < remote.FloodDrops {
		t.Errorf("flags %v dropped %d", st.Flags, st.Dropped)
	}
	if n := g.eventUpdates(); n != 0 {
		t.Errorf("%d events reached the views", n)
	}
	if n := fileSize(events.Path()); n != 0 {
		t.Errorf("events.jsonl grew to %d bytes", n)
	}
	// Disabled stays disabled: no retry.
	conns := len(g.requests())
	time.Sleep(time.Second)
	if n := len(g.requests()); n != conns || g.status().State != remote.Disabled {
		t.Errorf("a disabled host was retried (%d -> %d connections, %s)", conns, n, g.status().State)
	}
	// Den is idle again: the rest of the flood is not read.
	cpu1 := cpuTime()
	time.Sleep(2 * time.Second)
	if used := cpuTime() - cpu1; used > 400*time.Millisecond {
		t.Errorf("Den used %v of CPU in 2 s after disabling the host", used)
	}
	t.Logf("CPU for the flood: %v over %v", cpu1-cpu0, time.Since(t0).Round(time.Millisecond))

	// The human reconnects: the cursor asked for is past every dropped event.
	k, _ := remote.LoadKnown(remote.StateDir(), "b")
	if k.Cursor.Off < int64(st.Dropped) {
		t.Errorf("saved cursor %d is before the %d events dropped", k.Cursor.Off, st.Dropped)
	}
	g.m.Reconnect("b")
	g.wait("reconnect", 10*time.Second, func() bool { return len(g.requests()) > conns })
	if got := g.requests()[conns].Cursor; got.Off < k.Cursor.Off {
		t.Errorf("reconnect asked from %d, before the saved %d", got.Off, k.Cursor.Off)
	}

	// After a restart the host connects again: disabled is for one run of Den.
	g.wait("disabled again", time.Duration(secs+10)*time.Second, func() bool { return g.statusQuiet().State == remote.Disabled })
	g.stop()
	before := len(g.requests())
	g.start()
	g.wait("connect after restart", 10*time.Second, func() bool { return len(g.requests()) > before })
}

// A host that floods with events for agents it does list gets EventBurst,
// then EventRate a second; past that they are dropped, and it is disabled
// too. Below the rate nothing is dropped.
func TestFloodListedSessions(t *testing.T) {
	g := floodRig(t, floodSpec{Rate: 1500, Secs: 5, Listed: 200})
	start := time.Now()
	g.start()
	g.wait("disabled", 20*time.Second, func() bool { return g.statusQuiet().State == remote.Disabled })
	took := time.Since(start)
	n, size := g.eventUpdates(), fileSize(events.Path())
	t.Logf("%d events shown in %v, events.jsonl %d bytes, %d dropped", n, took.Round(time.Millisecond), size, g.status().Dropped)
	// The bucket refills while the flood lasts, longer on a loaded machine
	// (the link cap may drop the host and it reconnects).
	if most := remote.EventBurst + int(took.Seconds()+1)*remote.EventRate; n < remote.EventBurst || n > most {
		t.Errorf("%d events shown in %v, want the burst of %d and at most %d/s after it (%d)", n, took, remote.EventBurst, remote.EventRate, most)
	}
	if size > int64(n)*400 {
		t.Errorf("events.jsonl is %d bytes for %d events", size, n)
	}
	if !g.flagged("more events than Den takes") {
		t.Errorf("flags %v", g.status().Flags)
	}
}

func TestFloodListedUnderRate(t *testing.T) {
	g := floodRig(t, floodSpec{Rate: 15, Secs: 3, Listed: 4})
	g.opts = func(o *remote.Options) { o.EventBurst = 10 }
	g.start()
	g.wait("live", 10*time.Second, func() bool { return g.statusQuiet().State == remote.Live })
	g.wait("the events", 15*time.Second, func() bool { return g.eventUpdates() >= 40 })
	if st := g.status(); st.Dropped != 0 || st.State != remote.Live {
		t.Errorf("under the rate: %d dropped, state %s, flags %v", st.Dropped, st.State, st.Flags)
	}
}

func init() {
	if spec := os.Getenv(floodEnv); spec != "" {
		os.Exit(floodServe(spec))
	}
}

// procCPU is the CPU time a process has used, from /proc/<pid>/stat.
func procCPU(t *testing.T, pid int) time.Duration {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatalf("den is gone: %v", err)
	}
	f := strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+2:]))
	var ticks int64
	for _, s := range f[11:13] { // utime, stime
		var n int64
		fmt.Sscan(s, &n)
		ticks += n
	}
	return time.Duration(ticks) * 10 * time.Millisecond // USER_HZ is 100 on Linux
}

// The real den TUI against the flood from the report: 1500 events a second
// for new sessions for 20 s. The host is disabled with a reason in
// /remote, at most a notification or two goes out, nothing is logged, and
// Den is idle afterwards.
func TestFloodTUI(t *testing.T) {
	if testing.Short() || os.Getenv("DEN_SLOW_TESTS") != "1" {
		t.Skip("end-to-end flood test: set DEN_SLOW_TESTS=1")
	}
	g := floodRig(t, floodSpec{Rate: 1500, Secs: 20, Unknown: true, Listed: 1})
	l := g.l
	l.Local.WriteConfig("[orchestrator]\nspawn = false\n")
	tm := l.Local.StartTUI(200, 56, []string{"DEN_ORCHESTRATOR_SPAWN=off", "DEN_RESTORE=off"})
	start := time.Now()
	show := func(cmd string, panel ...string) string {
		tm.Keys("Escape")
		time.Sleep(300 * time.Millisecond)
		tm.Type(cmd)
		tm.Keys("Enter")
		return tm.WaitFor(30*time.Second, panel...)
	}
	f := show("/remote", "Remote hosts", "disabled", "flooding")
	t.Logf("disabled within %v:\n%s", time.Since(start).Round(time.Second), sshtest.Squeeze(f))
	if !strings.Contains(f, "/remote connect b") {
		t.Errorf("/remote does not say how to re-enable the host:\n%s", f)
	}
	time.Sleep(time.Until(start.Add(22 * time.Second))) // the flood would be over by now
	cpu0 := procCPU(t, tm.PanePID)
	time.Sleep(5 * time.Second)
	idle := procCPU(t, tm.PanePID) - cpu0
	notes := l.Local.Notifications()
	ev := fileSize(filepath.Join(l.Local.StateDir(), "events.jsonl")) + fileSize(filepath.Join(l.Local.StateDir(), "events.jsonl.1"))
	hist := fileSize(filepath.Join(l.Local.StateDir(), "history.jsonl"))
	t.Logf("after the flood: %d notifications, events.jsonl %d bytes, history.jsonl %d bytes, %v CPU in 5 s idle", len(notes), ev, hist, idle)
	if len(notes) > 2 {
		t.Errorf("%d notifications: %q", len(notes), notes)
	}
	if ev > 64<<10 || hist > 0 {
		t.Errorf("logs grew: events %d bytes, history %d bytes", ev, hist)
	}
	if idle > 500*time.Millisecond {
		t.Errorf("Den used %v of CPU in 5 s after the flood", idle)
	}
	if !tm.Alive() {
		t.Fatal("the TUI died")
	}
}
