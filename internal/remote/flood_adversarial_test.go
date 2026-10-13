package remote_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/sshtest"
)

// A hostile host that floods Den. The host is a stub den that is this test
// binary again: its stub script execs the binary with DEN_FLOOD set to a
// plan, and TestMain runs the plan instead of the tests. Nothing here
// listens on a port or leaves the lab.

// advPlan is what the hostile host does after its hello.
type advPlan struct {
	Policy   string   `json:"policy"`    // what hello says it applies; default observe
	Snapshot int      `json:"snapshot"`  // agents in a first snapshot (the "known" sessions)
	SnapEach int      `json:"snap_each"` // resend the snapshot every this many messages (0: once)
	Types    []string `json:"types"`     // message types, cycled: snapshot, event, held, gone, result, error, junk
	Kind     string   `json:"kind"`      // event kind; default needs_you
	Known    bool     `json:"known"`     // events name sessions of the snapshot
	Spread   int      `json:"spread"`    // distinct session ids the messages are spread over; default 1000000
	Rate     int      `json:"rate"`      // messages a second; 0 is as fast as the pipe takes them
	Total    int      `json:"total"`     // messages in all; 0 until Den hangs up
	Seconds  float64  `json:"seconds"`   // stop after this long; 0 no limit
	Mark     string   `json:"mark"`      // a file that gets one line for each connection
	Linger   bool     `json:"linger"`    // after the plan, stay connected, silent, until Den hangs up
}

func advID(i int) string { return fmt.Sprintf("%08x-2222-4333-8444-%012x", i, i) }

// advRunFlooder plays the plan on stdout. It returns when it is done and, with
// Linger, when stdin closes.
func advRunFlooder(p advPlan) {
	in := bufio.NewReader(os.Stdin)
	in.ReadString('\n') // the request
	if p.Mark != "" {
		if f, err := os.OpenFile(p.Mark, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintln(f, time.Now().UnixMilli())
			f.Close()
		}
	}
	out := bufio.NewWriterSize(os.Stdout, 1<<16)
	pol := p.Policy
	if pol == "" {
		pol = "observe"
	}
	now := func() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }
	fmt.Fprintf(out, `{"type":"hello","proto":1,"den":"v0.2.0","hostname":"flood","remote_now":%q,"policy":%q,"labels":true}`+"\n", now(), pol)
	out.Flush()
	hangup := make(chan struct{})
	go func() { io.Copy(io.Discard, in); close(hangup) }()

	if p.Spread == 0 {
		p.Spread = 1000000
	}
	if p.Kind == "" {
		p.Kind = events.NeedsYou
	}
	if len(p.Types) == 0 {
		p.Types = []string{"event"}
	}
	snapshot := func() {
		var agents []string
		for i := 0; i < p.Snapshot; i++ {
			agents = append(agents, fmt.Sprintf(`{"index":%d,"session":%q,"status":"working","name":"k%d","status_age_ms":0,"started_age_ms":0,"last_active_ms":-1}`, i, advID(i), i))
		}
		fmt.Fprintf(out, `{"type":"snapshot","remote_now":%q,"agents":[%s]}`+"\n", now(), strings.Join(agents, ","))
	}
	if p.Snapshot > 0 {
		snapshot()
		out.Flush()
	}
	start := time.Now()
	sent := 0
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		batch := 1 << 30
		if p.Rate > 0 {
			select {
			case <-tick.C:
			case <-hangup:
				return
			}
			// Paced against the clock, so 19 a second is 19 and not 100.
			batch = int(float64(p.Rate)*time.Since(start).Seconds()) - sent
			if batch <= 0 {
				continue
			}
		}
		for k := 0; k < batch; k++ {
			if (p.Total > 0 && sent >= p.Total) || (p.Seconds > 0 && time.Since(start).Seconds() >= p.Seconds) {
				out.Flush()
				if p.Linger {
					<-hangup
				}
				return
			}
			select {
			case <-hangup:
				return
			default:
			}
			var n int
			if p.Known && p.Snapshot > 0 {
				n = sent % p.Snapshot
			} else {
				n = p.Snapshot + sent%p.Spread
			}
			id := advID(n)
			typ := p.Types[sent%len(p.Types)]
			switch typ {
			case "snapshot":
				snapshot()
			case "event":
				fmt.Fprintf(out, `{"type":"event","cursor":{"ino":1,"off":%d},"event":{"v":1,"time":%q,"kind":%q,"session":%q,"agent":"x","request":"permission","tool":"Bash"}}`+"\n", sent*100, now(), p.Kind, id)
			case "held":
				fmt.Fprintf(out, `{"type":"held","id":"%032x","session":%q,"tool":"Bash","body":{"command":"echo %d"}}`+"\n", sent, id, sent)
			case "gone":
				fmt.Fprintf(out, `{"type":"gone","id":"%032x"}`+"\n", sent)
			case "result":
				fmt.Fprintf(out, `{"type":"result","id":"%032x","ok":true}`+"\n", sent)
			case "error":
				fmt.Fprintf(out, `{"type":"error","code":"internal","message":"flood %d"}`+"\n", sent)
			case "junk":
				fmt.Fprintf(out, "not json %d\n", sent)
			}
			sent++
			if p.SnapEach > 0 && p.Snapshot > 0 && sent%p.SnapEach == 0 {
				snapshot()
			}
		}
		out.Flush()
	}
}

// advFloodMain runs the flood plan from DEN_FLOOD, if there is one. TestMain
// calls it first: it reports whether this process was the hostile host.
func advFloodMain() bool {
	s := os.Getenv("DEN_ADV_FLOOD")
	if s == "" {
		return false
	}
	var p advPlan
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		fmt.Fprintln(os.Stderr, "bad DEN_FLOOD:", err)
		os.Exit(2)
	}
	advRunFlooder(p)
	return true
}

// advFloodRig is a rig whose host b is the hostile flooder, configured with the
// policy the plan says. The plan is what runs on every connection.
func advFloodRig(t *testing.T, p advPlan) *rig {
	t.Helper()
	l := sshtest.New(t, "b")
	r := l.Remote("b")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pb, _ := json.Marshal(p)
	stub := filepath.Join(r.Home, "stubden")
	script := "#!/bin/sh\nexec env DEN_ADV_FLOOD='" + string(pb) + "' '" + self + "'\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	pol := p.Policy
	if pol == "" {
		pol = "observe"
	}
	l.Local.Apply()
	l.Local.WriteRemotes(fmt.Sprintf("[hosts.b]\ntarget = \"b\"\npolicy = %q\nden_path = %q\n", pol, stub))
	return &rig{t: t, l: l, r: r}
}

// cpu returns this process's user+system CPU time.
func cpu() time.Duration {
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// advProcCPU returns the CPU time of a process by pid, from /proc.
func advProcCPU(pid int) time.Duration {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	s := string(b)
	f := strings.Fields(s[strings.LastIndexByte(s, ')')+2:])
	var u, k int64
	fmt.Sscan(f[11], &u)
	fmt.Sscan(f[12], &k)
	return time.Duration(u+k) * 10 * time.Millisecond // 100 ticks a second
}

// dirBytes is the size of all regular files under dir.
func dirBytes(dir string) int64 {
	var n int64
	filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			n += fi.Size()
		}
		return nil
	})
	return n
}

// Bounds the tests hold the Manager to. They are the caps the flood fix
// promises, with room for what is already in flight.
const (
	floodEventBurst = remote.EventBurst
	floodEventRate  = remote.EventRate
)

// forwarded returns what the Manager handed on, by kind.
func (g *rig) forwarded() (ev, held, fleet int) {
	for _, u := range g.arrived() {
		switch u.Kind {
		case remote.EventArrived:
			ev++
		case remote.HeldArrived:
			held++
		case remote.FleetChanged:
			fleet++
		}
	}
	return
}

// idleCPU gives Den up to 30 s to work off what is still queued (a slow
// or raced build reads a backlog for a while), then returns the share of a
// core the process used over d. A Den that never settles returns the last
// share it measured.
func idleCPU(d time.Duration) float64 {
	window := func(w time.Duration) float64 {
		before := cpu()
		time.Sleep(w)
		return float64(cpu()-before) / float64(w)
	}
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end); {
		if window(time.Second) < 0.1 {
			break
		}
	}
	return window(d)
}

// advFloodRun starts the Manager on the plan, lets it run for d, and checks
// what must hold whatever the host sent: Den answers while flooded, closes
// promptly, leaves little behind on disk and goes back to idle.
func advFloodRun(t *testing.T, p advPlan, d time.Duration, opts func(*remote.Options)) *rig {
	t.Helper()
	g := advFloodRig(t, p)
	g.opts = opts
	base := dirBytes(g.l.Local.Home)
	g.start()
	worst := time.Duration(0)
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		t0 := time.Now()
		g.m.Hosts()
		g.m.Agents()
		worst = max(worst, time.Since(t0))
		time.Sleep(50 * time.Millisecond)
	}
	if worst > time.Second {
		t.Errorf("Den took %v to answer Hosts/Agents while flooded", worst)
	}
	return func() *rig {
		t.Logf("grew %d bytes on disk; status %+v", dirBytes(g.l.Local.Home)-base, g.statusQuiet())
		if grew := dirBytes(g.l.Local.Home) - base; grew > 4<<20 {
			t.Errorf("the flood grew Den's files by %d bytes", grew)
		}
		return g
	}()
}

// every message type, at a rate that stays under the line rate cap.
func TestAdvFloodEveryTypeAtRate(t *testing.T) {
	cases := []struct {
		name string
		plan advPlan
		slow bool // only with DEN_SLOW_TESTS=1
	}{
		{"event, unknown sessions", advPlan{Types: []string{"event"}}, false},
		{"event, known sessions", advPlan{Types: []string{"event"}, Snapshot: 50, Known: true}, true},
		{"held", advPlan{Types: []string{"held"}, Policy: "approve"}, false},
		{"snapshot", advPlan{Types: []string{"snapshot"}, Snapshot: 40}, true},
		{"gone", advPlan{Types: []string{"gone"}}, true},
		{"result", advPlan{Types: []string{"result"}}, true},
		{"junk", advPlan{Types: []string{"junk"}}, true},
		{"all of them", advPlan{Types: []string{"event", "held", "gone", "result", "snapshot", "junk"}, Policy: "approve", Snapshot: 10}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.slow {
				slowOnly(t)
			}
			p := c.plan
			p.Rate, p.Seconds, p.Linger = 1500, 2, true
			g := advFloodRun(t, p, 2*time.Second, nil)
			ev, held, fleet := g.forwarded()
			t.Logf("forwarded: %d events, %d held, %d fleet updates; flags %v", ev, held, fleet, g.statusQuiet().Flags)
			if want := floodEventBurst + 6*floodEventRate; ev > want {
				t.Errorf("%d events reached the UI from a flood (cap about %d)", ev, want)
			}
			if held > 2*remote.MaxHeld {
				t.Errorf("%d held requests reached the inbox (cap %d)", held, remote.MaxHeld)
			}
			if n := len(g.localEvents()); n > floodEventBurst+6*floodEventRate {
				t.Errorf("%d events written to the local events.jsonl", n)
			}
			if got := len(g.statusQuiet().Flags); got > 20 {
				t.Errorf("%d flags: the list must stay short", got)
			}
			if c := idleCPU(time.Second); c > 0.25 {
				t.Errorf("Den is not idle after the flood: %.0f%% of a core", c*100)
			}
			done := make(chan struct{})
			go func() { g.stop(); close(done) }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("Close did not return")
			}
		})
	}
}

// slowOnly skips a test unless DEN_SLOW_TESTS=1.
func slowOnly(t *testing.T) {
	t.Helper()
	if testing.Short() || os.Getenv("DEN_SLOW_TESTS") != "1" {
		t.Skip("slow: set DEN_SLOW_TESTS=1")
	}
}

// 100 000 events as fast as the pipe takes them, and a snapshot too big for
// a line.
func TestAdvFloodHugeCounts(t *testing.T) {
	slowOnly(t)
	t.Run("100000 events at once", func(t *testing.T) {
		g := advFloodRun(t, advPlan{Total: 100000, Linger: true}, 8*time.Second, nil)
		ev, _, _ := g.forwarded()
		t.Logf("forwarded %d events; local events.jsonl %d; status %+v", ev, len(g.localEvents()), g.statusQuiet())
		if ev > floodEventBurst+10*floodEventRate {
			t.Errorf("%d of 100000 events reached the UI", ev)
		}
		if n := len(g.localEvents()); n > floodEventBurst+10*floodEventRate {
			t.Errorf("%d of 100000 events were written to events.jsonl", n)
		}
		if c := idleCPU(3 * time.Second); c > 0.25 {
			t.Errorf("not idle after: %.0f%% of a core", c*100)
		}
	})
	t.Run("10000 sessions in a snapshot", func(t *testing.T) {
		g := advFloodRun(t, advPlan{Snapshot: 10000, Total: 1, Linger: true}, 4*time.Second, nil)
		st := g.statusQuiet()
		if n := len(g.m.Agents()); n > 2000 {
			t.Errorf("%d agents kept from a snapshot that cannot fit a line", n)
		}
		t.Logf("status %+v", st)
		if st.State != remote.Offline && st.State != remote.Live && string(st.State) != "disabled" && st.State != remote.Connecting {
			t.Errorf("state %s", st.State)
		}
		if c := idleCPU(3 * time.Second); c > 0.25 {
			t.Errorf("not idle after: %.0f%% of a core", c*100)
		}
	})
	t.Run("the biggest snapshot that fits, over and over", func(t *testing.T) {
		g := advFloodRun(t, advPlan{Snapshot: 1300, Types: []string{"snapshot"}, Rate: 200, Seconds: 3, Linger: true}, 5*time.Second, nil)
		if n := len(g.m.Agents()); n > 2000 {
			t.Errorf("%d agents", n)
		}
		if c := idleCPU(3 * time.Second); c > 0.25 {
			t.Errorf("not idle after: %.0f%% of a core", c*100)
		}
	})
}

// A snapshot that lists thousands of sessions makes every event for them
// "known": the budget still holds.
func TestAdvFloodKnownSessionsBySnapshot(t *testing.T) {
	slowOnly(t)
	g := advFloodRun(t, advPlan{Snapshot: 1200, Known: true, Rate: 1500, Seconds: 4, Linger: true}, 6*time.Second, nil)
	ev, _, _ := g.forwarded()
	t.Logf("forwarded %d events from known sessions; agents %d", ev, len(g.m.Agents()))
	if want := floodEventBurst + 8*floodEventRate; ev > want {
		t.Errorf("%d events from known sessions reached the UI (cap about %d)", ev, want)
	}
	if c := idleCPU(3 * time.Second); c > 0.25 {
		t.Errorf("not idle after: %.0f%% of a core", c*100)
	}
}

// 50 connections in a row, each with a burst: reconnecting must not refill
// the budget. A storm that stays inside the budget is left alone; one that
// goes over it gets the host disabled, and Den stops dialing.
func TestAdvFloodReconnects(t *testing.T) {
	quick := func(o *remote.Options) {
		o.Backoff, o.MaxBackoff, o.Healthy = 20*time.Millisecond, 50*time.Millisecond, time.Hour
	}
	conns := func(mark string) int {
		b, _ := os.ReadFile(mark)
		return strings.Count(string(b), "\n")
	}
	want := 20 // 50 with DEN_SLOW_TESTS=1
	if os.Getenv("DEN_SLOW_TESTS") == "1" {
		want = 50
	}
	t.Run("connections inside the budget", func(t *testing.T) {
		mark := t.TempDir() + "/conn"
		start := time.Now()
		g := advFloodRun(t, advPlan{Snapshot: 20, Known: true, Total: 35, Mark: mark}, time.Second, quick)
		g.wait("the connections", 90*time.Second, func() bool { return conns(mark) >= want })
		took := time.Since(start)
		ev, _, _ := g.forwarded()
		t.Logf("%d connections in %v forwarded %d events; status %v", want, took.Round(time.Millisecond), ev, g.statusQuiet().State)
		if limit := floodEventBurst + int(took.Seconds()+1)*floodEventRate; ev > limit {
			t.Errorf("%d events over %d connections (cap about %d)", ev, want, limit)
		}
		if st := g.statusQuiet(); st.State == "disabled" {
			t.Errorf("a host inside the budget was disabled: %s", st.Reason)
		}
		g.stop()
		if c := idleCPU(time.Second); c > 0.25 {
			t.Errorf("not idle after: %.0f%% of a core", c*100)
		}
	})
	t.Run("a storm over the budget is disabled, not redialed", func(t *testing.T) {
		slowOnly(t)
		mark := t.TempDir() + "/conn"
		start := time.Now()
		g := advFloodRun(t, advPlan{Snapshot: 20, Known: true, Total: 1500, Mark: mark}, time.Second, quick)
		g.wait("disabled", 60*time.Second, func() bool { return string(g.statusQuiet().State) == "disabled" })
		took := time.Since(start)
		n := conns(mark)
		time.Sleep(3 * time.Second)
		if after := conns(mark); after != n {
			t.Errorf("Den dialed %d more times after disabling the host", after-n)
		}
		ev, _, _ := g.forwarded()
		t.Logf("disabled after %d connections in %v; %d events forwarded; %s", n, took.Round(time.Millisecond), ev, g.statusQuiet().Reason)
		if want := floodEventBurst + int(took.Seconds()+1)*floodEventRate; ev > want {
			t.Errorf("%d events forwarded before the host was disabled (cap about %d)", ev, want)
		}
		if g.statusQuiet().Hint == "" {
			t.Errorf("a disabled host says nothing about what to do")
		}
		g.stop()
		if c := idleCPU(2 * time.Second); c > 0.25 {
			t.Errorf("not idle after: %.0f%% of a core", c*100)
		}
	})
}

// A host that says error at once is retried with backoff, not in a loop.
func TestAdvFloodErrorRetriesArePaced(t *testing.T) {
	slowOnly(t)
	mark := t.TempDir() + "/conn"
	g := advFloodRun(t, advPlan{Types: []string{"error"}, Total: 1000, Mark: mark}, 6*time.Second, func(o *remote.Options) { o.Backoff, o.MaxBackoff = 500*time.Millisecond, 2*time.Second })
	b, _ := os.ReadFile(mark)
	n := strings.Count(string(b), "\n")
	t.Logf("%d connections in 6 s; status %+v", n, g.statusQuiet())
	if n > 12 {
		t.Errorf("%d connections in 6 seconds: retries are not paced", n)
	}
}

// notifySendsAlive counts notify-send processes of this lab still running.
func notifySendsAlive(lab string) int {
	n := 0
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err == nil && strings.Contains(string(b), lab) && strings.Contains(string(b), "notify-send") {
			n++
		}
	}
	return n
}

var inboxCount = regexp.MustCompile(`inbox (\d+) waiting`)

// advTUIFlood runs the real TUI against the flood plan. After d it returns what
// the human has by then: the inbox count on the header, the notify-send
// calls, and whether the TUI is idle again.
type advTUIResult struct {
	g      *rig
	tm     *sshtest.Tmux
	inbox  int
	notes  []string
	idle   float64
	growth int64
	frame  string
}

func advTUIFlood(t *testing.T, p advPlan, d time.Duration) advTUIResult {
	t.Helper()
	g := advFloodRig(t, p)
	l := g.l
	l.Local.WriteConfig("[orchestrator]\nspawn = false\n")
	base := dirBytes(l.Local.Home)
	tm := l.Local.StartTUI(160, 50, []string{"DEN_ORCHESTRATOR_SPAWN=off", "DEN_RESTORE=off"})
	time.Sleep(d)
	if !tm.Alive() {
		t.Fatal("the TUI died")
	}
	res := advTUIResult{g: g, tm: tm}
	// Quiet for a while, then measure what the TUI does with no input.
	time.Sleep(3 * time.Second)
	c0, t0 := advProcCPU(tm.PanePID), time.Now()
	time.Sleep(5 * time.Second)
	res.idle = float64(advProcCPU(tm.PanePID)-c0) / float64(time.Since(t0))
	res.frame = sshtest.Squeeze(tm.Capture())
	if m := inboxCount.FindStringSubmatch(res.frame); m != nil {
		res.inbox, _ = strconv.Atoi(m[1])
	} else if !strings.Contains(res.frame, "orchestrator off") {
		t.Fatalf("no header in the TUI:\n%s", res.frame)
	}
	res.notes = l.Local.Notifications()
	res.growth = dirBytes(l.Local.Home) - base
	t.Logf("inbox %d, notifications %d, TUI idle %.1f%% of a core, state grew %d bytes", res.inbox, len(res.notes), res.idle*100, res.growth)
	if n := len(res.notes); n > 0 {
		t.Logf("first notification: %q; last: %q", res.notes[0], res.notes[n-1])
	}
	if n := notifySendsAlive(l.Dir); n > 0 {
		t.Errorf("%d notify-send processes still running", n)
	}
	if res.idle > 0.15 {
		t.Errorf("the TUI is not idle after the flood: %.0f%% of a core", res.idle*100)
	}
	if res.growth > 4<<20 {
		t.Errorf("the flood grew Den's files by %d bytes", res.growth)
	}
	return res
}

// What a real VM showed: a host sends needs_you for sessions it never
// listed, at 1500 a second.
func TestAdvFloodTUIUnknownNeedsYou(t *testing.T) {
	slowOnly(t)
	r := advTUIFlood(t, advPlan{Rate: 1500, Seconds: 10, Linger: true}, 14*time.Second)
	if r.inbox > 32 {
		t.Errorf("%d inbox items from sessions the host never listed (cap 32 a host)", r.inbox)
	}
	if len(r.notes) > 8 {
		t.Errorf("%d notify-send calls", len(r.notes))
	}
}

// The same flood naming sessions that a snapshot did list.
func TestAdvFloodTUIKnownNeedsYou(t *testing.T) {
	slowOnly(t)
	r := advTUIFlood(t, advPlan{Snapshot: 500, Known: true, Rate: 1500, Seconds: 10, Linger: true}, 14*time.Second)
	if r.inbox > 32 {
		t.Errorf("%d inbox items (cap 32 a host)", r.inbox)
	}
	if n := len(r.notes); n > 8 {
		t.Errorf("%d notify-send calls in a flood of a few seconds (the first at once, the rest coalesced, 6 a minute)", n)
	}
	if len(r.notes) == 0 {
		t.Errorf("a real host with agents that need you was never announced")
	}
}

// Held requests at a rate, under approve.
func TestAdvFloodTUIHeld(t *testing.T) {
	slowOnly(t)
	r := advTUIFlood(t, advPlan{Policy: "approve", Types: []string{"held"}, Snapshot: 500, Known: true, Rate: 1500, Seconds: 10, Linger: true}, 14*time.Second)
	if r.inbox > 32 {
		t.Errorf("%d inbox items from held requests (cap 32 a host)", r.inbox)
	}
	if len(r.notes) > 8 {
		t.Errorf("%d notify-send calls", len(r.notes))
	}
}

// The reconnects the default backoff allows, each with its own burst of
// needs_you for known sessions.
func TestAdvFloodTUIReconnects(t *testing.T) {
	slowOnly(t)
	mark := t.TempDir() + "/conn"
	r := advTUIFlood(t, advPlan{Snapshot: 100, Known: true, Total: 800, Mark: mark}, 60*time.Second)
	b, _ := os.ReadFile(mark)
	t.Logf("%d connections (the default backoff paces them)", strings.Count(string(b), "\n"))
	if r.inbox > 32 {
		t.Errorf("%d inbox items (cap 32 a host)", r.inbox)
	}
	if n := len(r.notes); n > 14 {
		t.Errorf("%d notify-send calls over a minute (6 a minute)", n)
	}
}

// A trickle just under the event budget for minutes: it never trips the
// flood threshold, and the human's inbox, notifications and history still
// stay within their caps.
func TestAdvFloodTUITrickleUnderTheCaps(t *testing.T) {
	slowOnly(t)
	const mins = 3
	r := advTUIFlood(t, advPlan{Snapshot: 500, Known: true, Rate: floodEventRate - 1, Seconds: mins * 60, Linger: true}, mins*time.Minute+10*time.Second)
	if r.inbox > 32 {
		t.Errorf("%d inbox items (cap 32 a host)", r.inbox)
	}
	if n := len(r.notes); n > 6*(mins+1) {
		t.Errorf("%d notify-send calls in %d minutes (6 a minute)", n, mins)
	}
	hist := 0
	if b, err := os.ReadFile(filepath.Join(r.g.l.Local.Home, ".local/state/den/history.jsonl")); err == nil {
		hist = strings.Count(string(b), "\n")
	}
	if hist > 30*(mins+1) {
		t.Errorf("%d history lines in %d minutes (30 a minute)", hist, mins)
	}
	if st := r.g.l.Local.Grep("flooding"); len(st) > 0 {
		t.Logf("files mentioning flooding: %v", st)
	}
}

// The Manager alone, for minutes: events for unknown sessions just under
// the budget never take the host down and are never forwarded.
func TestAdvFloodManagerTrickle(t *testing.T) {
	slowOnly(t)
	g := advFloodRun(t, advPlan{Rate: floodEventRate - 1, Seconds: 180, Linger: true}, 190*time.Second, nil)
	ev, _, _ := g.forwarded()
	if st := g.statusQuiet(); st.State != remote.Live {
		t.Errorf("a trickle under the budget took the host to %s (%s)", st.State, st.Reason)
	}
	if ev > floodEventBurst {
		t.Errorf("%d events for unknown sessions were forwarded", ev)
	}
	if n := len(g.localEvents()); n > floodEventBurst {
		t.Errorf("%d events for unknown sessions were written to events.jsonl", n)
	}
}
