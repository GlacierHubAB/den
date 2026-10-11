package closer

import (
	"context"
	"errors"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func cands() []Target {
	return []Target{
		{SessionID: "a", Name: "Mossy Lemur", Peer: "mossy", PID: 11, Status: "idle", Window: "0xa", Dir: "/r/a"},
		{SessionID: "b", Name: "Snug Capybara", Peer: "snug", PID: 12, Status: "working", Window: "0xb", Children: []string{"Fuzzy Sprite"}},
		{SessionID: "orc", Name: "orchestrator", Peer: "den-orchestrator", PID: 13, Status: "idle"},
		{SessionID: "me", Name: "Den", Peer: "den", PID: 99, Status: "idle"},
		{SessionID: "up", Name: "Shell", Peer: "sh", PID: 7, Status: "idle"},
		{SessionID: "g", Name: "Comet Newt", Peer: "comet", PID: 14, Status: "gone"},
		{SessionID: "n", Name: "Plucky", Peer: "plucky", PID: 15, Status: "needs you", Window: "0xd"},
	}
}

func self() Self {
	return Self{PID: 99, Orchestrator: "orc", Window: "0xd", IsAncestor: func(pid int) bool { return pid == 7 }}
}

func TestBuildAppliesTheSafetyRules(t *testing.T) {
	p := Build(cands(), false, self())
	if len(p.Targets) != 1 || p.Targets[0].Name != "Mossy Lemur" {
		t.Fatalf("targets = %+v", p.Targets)
	}
	why := map[string]string{}
	for _, r := range p.Refused {
		why[r.Target.Name] = r.Why
	}
	for name, want := range map[string]string{
		"Snug Capybara": "working · add --force",
		"orchestrator":  "it is the orchestrator",
		"Den":           "it is Den itself",
		"Shell":         "Den runs inside it",
		"Comet Newt":    "already gone",
		"Plucky":        "needs you · add --force",
	} {
		if !strings.HasPrefix(why[name], want) {
			t.Errorf("%s refused with %q, want %q", name, why[name], want)
		}
	}
	// Force lets the working and waiting ones through, never the protected ones.
	f := Build(cands(), true, self())
	var names []string
	for _, x := range f.Targets {
		names = append(names, x.Name)
	}
	if strings.Join(names, ",") != "Mossy Lemur,Snug Capybara,Plucky" {
		t.Errorf("forced targets = %v", names)
	}
}

func TestLinesSayExactlyWhatCloses(t *testing.T) {
	p := Build(cands(), true, self())
	p.From, p.Reason = "orchestrator", "they are idle"
	p.Targets[0].Dirty = []string{"a.go", "b.go", "c.go", "d.go", "e.go"}
	text := strings.Join(p.Lines(), "\n")
	for _, want := range []string{
		"The orchestrator asks to close these agents. Its reason: they are idle",
		"Mossy Lemur (mossy) · idle · pid 11 · window 0xa closes",
		"! 5 uncommitted change(s) in /r/a: a.go, b.go, c.go, d.go, … 1 more",
		"Snug Capybara (snug) · working · pid 12 · window 0xb closes",
		"! it directs Fuzzy Sprite; they stay open unless also selected",
		"Forced: a process that does not stop",
		"Not orchestrator: it is the orchestrator",
		"Not Comet Newt: already gone",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("lines lack %q:\n%s", want, text)
		}
	}
	if p.Title() != "Close 3 agents?" || (Plan{}).Title() != "Nothing to close" {
		t.Errorf("title = %q", p.Title())
	}
	one := Build(cands()[:1], false, self())
	if one.Title() != "Close Mossy Lemur?" || !strings.Contains(strings.Join(one.Lines(), "\n"), "graceful stop first") {
		t.Errorf("single: %q %v", one.Title(), one.Lines())
	}
}

// fakeOps is a process table: pids die on SIGTERM unless stubborn, on
// SIGKILL always; windows record what was asked.
type fakeOps struct {
	mu       sync.Mutex
	alive    map[int]bool
	stubborn map[int]bool
	signals  []string
	windows  []string
	winErr   error
}

func (f *fakeOps) Signal(pid int, sig syscall.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.alive[pid] {
		return syscall.ESRCH
	}
	f.signals = append(f.signals, sig.String()+":"+itoa(pid))
	if sig == syscall.SIGKILL || (sig == syscall.SIGTERM && !f.stubborn[pid]) {
		f.alive[pid] = false
	}
	return nil
}

func (f *fakeOps) Alive(pid int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive[pid]
}

func (f *fakeOps) CloseWindow(a string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.windows = append(f.windows, a)
	return f.winErr
}

func itoa(i int) string { return string(rune('0' + i%10)) }

func TestRunStopsGracefullyAndKillsOnlyWhenForced(t *testing.T) {
	StopWait, KillWait, pollEvery = 50*time.Millisecond, 50*time.Millisecond, 5*time.Millisecond
	ops := &fakeOps{alive: map[int]bool{1: true, 2: true}, stubborn: map[int]bool{2: true}}
	plan := Plan{Targets: []Target{{Name: "A", PID: 1, Window: "0xa"}, {Name: "B", PID: 2, Window: "0xb"}}}
	res := Run(context.Background(), plan, Self{}, ops)
	if !res[0].Ended || res[0].Killed || !res[0].WindowClosed {
		t.Errorf("A: %+v", res[0])
	}
	if res[1].Ended || res[1].WindowClosed {
		t.Errorf("B should still run without force: %+v", res[1])
	}
	if strings.Join(ops.signals, " ") != "terminated:1 terminated:2" {
		t.Errorf("signals = %v", ops.signals)
	}
	if s := Summary(res); s != "closed A · still running: B · 1 window(s) closed" {
		t.Errorf("summary = %q", s)
	}

	ops = &fakeOps{alive: map[int]bool{2: true}, stubborn: map[int]bool{2: true}}
	plan = Plan{Force: true, Targets: []Target{{Name: "B", PID: 2, Window: "0xb"}}}
	res = Run(context.Background(), plan, Self{}, ops)
	if !res[0].Ended || !res[0].Killed || !res[0].WindowClosed {
		t.Errorf("forced B: %+v", res[0])
	}
	if strings.Join(ops.signals, " ") != "terminated:2 killed:2" {
		t.Errorf("signals = %v", ops.signals)
	}
	if s := Summary(res); s != "killed B · 1 window(s) closed" {
		t.Errorf("summary = %q", s)
	}
}

func TestRunNeverTouchesDensWindowAndIgnoresAClosedOne(t *testing.T) {
	StopWait, pollEvery = 50*time.Millisecond, 5*time.Millisecond
	ops := &fakeOps{alive: map[int]bool{1: true, 3: true}, winErr: errors.New("hyprland: window not found")}
	plan := Plan{Targets: []Target{{Name: "A", PID: 1, Window: "0xden"}, {Name: "C", PID: 3, Window: "0xc"}}}
	res := Run(context.Background(), plan, Self{Window: "0xden"}, ops)
	if len(ops.windows) != 1 || ops.windows[0] != "0xc" {
		t.Errorf("windows asked to close = %v", ops.windows)
	}
	if res[0].WindowClosed || res[1].WindowClosed || res[1].Err != nil {
		t.Errorf("results = %+v", res)
	}
	// A process that is already gone is not an error.
	ops = &fakeOps{alive: map[int]bool{}}
	res = Run(context.Background(), Plan{Targets: []Target{{Name: "Z", PID: 5}}}, Self{}, ops)
	if !res[0].Ended || res[0].Err != nil {
		t.Errorf("gone process: %+v", res[0])
	}
}

func TestGuardRulesAndIdleParsing(t *testing.T) {
	g := &Guard{Names: []string{"Pebble Capybara", "grvlle-44"}, Paths: []string{"/srv/xml-converter"}, Flagged: []Flag{{Name: "Plucky Narwhal", Note: "issue pending"}}}
	if why := g.Protected("Pebble Capybara", "x", nil, ""); why != "protected (Pebble Capybara)" {
		t.Errorf("by name: %q", why)
	}
	if why := g.Protected("Fuzzy Sprite", "grvlle-44", nil, ""); why != "protected (grvlle-44)" {
		t.Errorf("by peer: %q", why)
	}
	if why := g.Protected("A", "a", []string{"/srv/xml-converter/src"}, ""); why != "protected: works in /srv/xml-converter" {
		t.Errorf("by folder: %q", why)
	}
	if why := g.Protected("A", "a", []string{"/srv/xml-converter-old"}, ""); why != "" {
		t.Errorf("a sibling folder is not inside: %q", why)
	}
	if why := g.Protected("A", "a", nil, "fix the XML-Converter build"); why != "protected: its task mentions xml-converter" {
		t.Errorf("by task: %q", why)
	}
	if note, ok := g.Held("plucky narwhal", ""); !ok || note != "issue pending" {
		t.Errorf("held: %q %v", note, ok)
	}
	if !g.Protect("New One") || g.Protect("new one") || !g.Unprotect("NEW ONE") || g.Unprotect("New One") {
		t.Error("protect/unprotect bookkeeping")
	}
	for in, want := range map[string]time.Duration{"30m": 30 * time.Minute, "1h": time.Hour, "2h": 2 * time.Hour, "1h30m": 90 * time.Minute, "1d": 24 * time.Hour, " 45M ": 45 * time.Minute} {
		if d, err := ParseIdle(in); err != nil || d != want {
			t.Errorf("ParseIdle(%q) = %v %v", in, d, err)
		}
	}
	for _, bad := range []string{"soon", "0m", "-1h", ""} {
		if _, err := ParseIdle(bad); err == nil {
			t.Errorf("ParseIdle(%q) should fail", bad)
		}
	}
	if s := roundIdle(3*time.Hour + 5*time.Minute); s != "3h05m" {
		t.Errorf("roundIdle = %q", s)
	}
	if s := roundIdle(50 * time.Hour); s != "2d2h" {
		t.Errorf("roundIdle days = %q", s)
	}
	for d, want := range map[time.Duration]string{time.Hour: "1h", 90 * time.Minute: "1h30m", 45 * time.Minute: "45m", 48 * time.Hour: "2d"} {
		if s := ShortDuration(d); s != want {
			t.Errorf("ShortDuration(%v) = %q", d, s)
		}
	}
	path := t.TempDir() + "/p.json"
	if err := g.Save(path); err != nil {
		t.Fatal(err)
	}
	if back := LoadGuard(path); len(back.Names) != 2 || len(back.Paths) != 1 || back.Flagged[0].Note != "issue pending" {
		t.Errorf("round trip = %+v", back)
	}
}
