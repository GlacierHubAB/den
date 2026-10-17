package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/mission"
	denpeer "github.com/GlacierHubAB/den/internal/peer"
	"github.com/GlacierHubAB/den/internal/theme"
	"github.com/GlacierHubAB/den/internal/usage"
)

// usageModel is a fleet of sample agents whose statuslines report percent
// of the weekly limit, read age ago; percent < 0 reports none. Nothing is
// written to disk and no session is ever sent anything: the deliveries the
// stop makes are returned as commands the tests do not run.
func usageModel(t *testing.T, percent float64, age time.Duration, stop int) Model {
	t.Helper()
	m := Model{st: newStyles(theme.Deepharness), base: sample(), store: inbox.New(), width: 120, height: 40, orch: orchState{cardCur: -1}}
	m.input = newInput(m.st)
	m.missions = mission.New("")
	m.cfg.Usage.StopPercent = stop
	for i := range m.base {
		m.base[i].Session.SessionID = strings.ToLower(strings.ReplaceAll(m.base[i].Name, " ", "-"))
		m.base[i].Session.Socket = "/nonexistent/" + m.base[i].Session.SessionID + ".sock"
		if percent >= 0 && i == 0 {
			p := percent
			m.base[i].HasLimit = true
			m.base[i].Limits.SevenDay = &p
			m.base[i].Limits.SevenDayResets = time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339)
			m.base[i].Limits.Now = time.Now().Add(-age).UnixMilli()
		}
	}
	m.refresh()
	return m
}

func items(m Model, k inbox.Kind) []*inbox.Item {
	var out []*inbox.Item
	for _, it := range m.store.Items() {
		if it.Kind == k {
			out = append(out, it)
		}
	}
	return out
}

// anyHas reports whether an item's title or body holds text.
func anyHas(its []*inbox.Item, text string) bool {
	for _, it := range its {
		if strings.Contains(it.Title+"\n"+strings.Join(it.Body, "\n"), text) {
			return true
		}
	}
	return false
}

func TestUsageInTheHeader(t *testing.T) {
	m := usageModel(t, 63, 2*time.Minute, 0)
	m.checkUsage(time.Now())
	if out := ansi.Strip(m.View().Content); !strings.Contains(out, "usage 63% · 2m ago") {
		t.Errorf("header:\n%s", out)
	}
	// The reading goes stale: unknown, once Den has had one.
	p := 63.0
	m.base[0].Limits.SevenDay, m.base[0].Limits.Now = &p, time.Now().Add(-20*time.Minute).UnixMilli()
	m.checkUsage(time.Now())
	if out := ansi.Strip(m.View().Content); !strings.Contains(out, "usage unknown") {
		t.Errorf("a stale reading:\n%s", out)
	}
	// Never a reading: the header says nothing about usage.
	m = usageModel(t, 63, 20*time.Minute, 0)
	m.checkUsage(time.Now())
	if out := ansi.Strip(m.View().Content); strings.Contains(out, "usage") {
		t.Errorf("no reading ever:\n%s", out)
	}
	m = usageModel(t, -1, 0, 0)
	m.cfg.Usage.WeeklyUSD = 100
	m.checkUsage(time.Now())
	if !m.use.ok || !m.use.reading.Estimate() || !strings.Contains(usage.Label(m.use.reading, m.use.ok, m.use.st, time.Now()), "estimate") {
		t.Errorf("the budget estimate: %+v", m.use.reading)
	}
}

func TestUsageWarnsOncePerThreshold(t *testing.T) {
	m := usageModel(t, 85, time.Minute, 0)
	m.checkUsage(time.Now())
	m.checkUsage(time.Now())
	if got := items(m, inbox.Usage); len(got) != 1 || !strings.Contains(got[0].Title, "past 80%") {
		t.Fatalf("at 85%%: %d cards", len(got))
	}
	p := 91.0
	m.base[0].Limits.SevenDay = &p
	m.checkUsage(time.Now())
	if got := items(m, inbox.Usage); len(got) != 2 || !anyHas(got, "past 90%") {
		t.Errorf("at 91%%: %d cards", len(got))
	}
}

// TestUsageNeverStopsByDefault: the human turned the automatic stop off.
func TestUsageNeverStopsByDefault(t *testing.T) {
	m := usageModel(t, 99, time.Minute, 0)
	m.checkUsage(time.Now())
	if m.use.st.Stopped || len(items(m, inbox.Sent)) != 0 {
		t.Errorf("stopped %v, %d messages", m.use.st.Stopped, len(items(m, inbox.Sent)))
	}
}

// The stop reaches each local agent framed as Den enforcing the human's
// own rule, with the share the human set: binding, but never claiming the
// human typed it. A stop the human ordered says so.
func TestUsageStopIsFramedAsDensEnforcement(t *testing.T) {
	peer := useFakePeer(t)
	m := usageModel(t, 96, time.Minute, 95)
	runCmds(m.checkUsage(time.Now()))
	var framed []string
	for _, f := range peer.framed {
		framed = append(framed, f...)
	}
	if len(framed) == 0 {
		t.Fatal("nothing sent")
	}
	for _, f := range framed {
		if !strings.HasPrefix(f, denpeer.UsageHeader+"\n\nUSAGE STOP: your human set a usage stop at 95% of the weekly limit, and it is reached.") || strings.Contains(f, "typed by your human") {
			t.Errorf("framed %q", f)
		}
	}
	if got := m.usageStopText("stopped by you"); !strings.HasPrefix(got, "USAGE STOP: your human stopped the fleet in Den.") {
		t.Errorf("a stop the human ordered: %q", got)
	}
}

// The human's frame (peer.Header, "typed by your human") is only for text
// the human typed: m.sender() is called only inside the functions that send
// it (submit, flushQueue for the messages it held, sendReview) and
// usageSender's branch for agents on a host, named here one by one, so no
// automatic message can borrow it, in any file.
func TestOnlyTypedTextUsesTheHumansFrame(t *testing.T) {
	names, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	allowed := map[string]bool{"compose.go/submit": true, "compose.go/flushQueue": true, "review_send.go/sendReview": true, "compose.go/usageSender": true}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "sender" && !allowed[name+"/"+fn.Name.Name] {
					t.Errorf("%s (%s) calls sender(): Den's own messages use denSender or usageSender", fset.Position(call.Pos()), fn.Name.Name)
				}
				return true
			})
		}
	}
}

func TestUsageStopAtTheSetShare(t *testing.T) {
	m := usageModel(t, 96, time.Minute, 95)
	// A remote agent whose host Den cannot act on.
	far := m.base[1]
	far.Host, far.Name, far.Session.SessionID = "build1", "Far Fox", "far-fox@build1"
	m.remote.agents = []fleet.Agent{far}
	m.refresh()
	if cmd := m.checkUsage(time.Now()); cmd == nil {
		t.Fatal("no deliveries")
	}
	if !m.use.st.Stopped {
		t.Fatal("not stopped")
	}
	sent := items(m, inbox.Sent)
	live := 0
	for _, a := range m.agents {
		if a.Host == "" && a.Session.Alive && a.Status != fleet.Gone {
			live++
		}
	}
	if len(sent) != live || live == 0 {
		t.Errorf("sent %d messages for %d live local agents", len(sent), live)
	}
	for _, it := range sent {
		if !strings.Contains(strings.Join(it.Body, " ")+it.Title, usageText) {
			t.Errorf("message %+v", it)
		}
	}
	if !anyHas(items(m, inbox.Usage), "not reachable for stop: Far Fox@build1") {
		t.Error("the stop card should name the unreachable agent")
	}
	if out := ansi.Strip(m.View().Content); !strings.Contains(out, "usage stop") {
		t.Errorf("header:\n%s", out)
	}
	// Stopped, it stops once: the next tick sends nothing more.
	m.checkUsage(time.Now())
	if len(items(m, inbox.Sent)) != len(sent) {
		t.Error("sent again")
	}
}

// TestUsageStopHoldsMissionsUntilResume: no runs while stopped; only the
// human's ^Y on the resume question lets the fleet go on.
func TestUsageStopHoldsMissionsUntilResume(t *testing.T) {
	m := usageModel(t, 10, time.Minute, 0)
	mm, _ := m.usageCommand([]string{"stop"})
	m = mm.(Model)
	ms := &mission.Mission{ID: "m1", Name: "x", Mission: "x", State: mission.Running, Shape: mission.Once}
	m.missions.Save(ms)
	if cmd := m.runMission(ms, "Start"); cmd != nil || len(ms.Runs) != 0 {
		t.Error("a mission run started while stopped")
	}
	mm, _ = m.usageCommand([]string{"resume"})
	m = mm.(Model)
	q := items(m, inbox.UsageResume)
	if len(q) != 1 || !m.use.st.Stopped {
		t.Fatalf("resume asks first: %d questions, stopped %v", len(q), m.use.st.Stopped)
	}
	m.tab, m.inboxSel = tabInbox, q[0].ID
	mm, _ = m.answer("allow")
	m = mm.(Model)
	if m.use.st.Stopped {
		t.Error("^Y did not let the fleet go on")
	}
	if cmd := m.runMission(ms, "Start"); cmd == nil {
		t.Error("runs go on after the resume")
	}
}

// TestUsageResumeDoesNotLoop: still past the stop share after the human
// resumes, Den does not stop and send again that week; a new stop share
// does.
func TestUsageResumeDoesNotLoop(t *testing.T) {
	m := usageModel(t, 96, time.Minute, 95)
	m.checkUsage(time.Now())
	if !m.use.st.Stopped {
		t.Fatal("not stopped")
	}
	sent := len(items(m, inbox.Sent))
	mm, _ := m.usageCommand([]string{"resume"})
	m = mm.(Model)
	q := items(m, inbox.UsageResume)
	m.tab, m.inboxSel = tabInbox, q[0].ID
	mm, _ = m.answer("allow")
	m = mm.(Model)
	for range 3 {
		m.checkUsage(time.Now())
	}
	if m.use.st.Stopped || len(items(m, inbox.Sent)) != sent {
		t.Errorf("stopped again after the resume: %v, %d messages (was %d)", m.use.st.Stopped, len(items(m, inbox.Sent)), sent)
	}
	m.cfg.Usage.StopPercent = 90 // the human changes the share
	m.checkUsage(time.Now())
	if !m.use.st.Stopped {
		t.Error("a new stop share should arm the stop again")
	}
}

func TestUsageSetByHand(t *testing.T) {
	m := usageModel(t, -1, 0, 0)
	mm, _ := m.usageCommand([]string{"set", "87"})
	m = mm.(Model)
	if !m.use.ok || m.use.reading.Source != usage.Set || m.use.reading.Percent != 87 {
		t.Errorf("reading %+v", m.use.reading)
	}
	if got := items(m, inbox.Usage); len(got) != 1 {
		t.Errorf("87%% set by hand warns at 80%%: %d", len(got))
	}
	for _, bad := range [][]string{{"set"}, {"set", "x"}, {"set", "101"}} {
		if _, _ = m.usageCommand(bad); m.flashErr == false && m.flash == "" {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestUsageStatePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	m := usageModel(t, 10, time.Minute, 0)
	m.use.path = path
	mm, _ := m.usageCommand([]string{"stop"})
	_ = mm
	if st := usage.Load(path); !st.Stopped || st.StopWhy != "stopped by you" {
		t.Errorf("saved %+v", st)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v %v", fi, err)
	}
}
