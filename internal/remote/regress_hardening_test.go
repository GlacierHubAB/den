package remote_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/sshtest"
)

// Regression tests for what a real host showed about hostile or half-dead
// hosts. The hostile ones are stub dens that say exactly what a bad host
// would.

// Under observe too, a serve that hears nothing from Den for a minute ends:
// a half-open link does not leave it alive on the host. Slow.
func TestRegressObserveServeEndsWhenDenGoesSilent(t *testing.T) {
	if testing.Short() || os.Getenv("DEN_SLOW_TESTS") != "1" {
		t.Skip("slow (about 70 s): set DEN_SLOW_TESTS=1")
	}
	g := newRig(t)
	ms := g.manual(remote.Observe)
	start := time.Now()
	select {
	case <-ms.done:
	case <-time.After(100 * time.Second):
		t.Fatal("an observe serve never ended after Den fell silent")
	}
	took := time.Since(start)
	t.Logf("serve ended %v after Den fell silent", took.Round(time.Second))
	if took < 40*time.Second || took > 90*time.Second {
		t.Errorf("serve ended after %v, want about 60 s", took)
	}
	if n := len(g.r.ServePIDs()); n != 0 {
		t.Errorf("%d serve processes still run on the host", n)
	}
}

// stubWith runs host b as a stub den that says hello with policy, then plays script (sh).
func stubWith(t *testing.T, policy, script string) *rig {
	t.Helper()
	l := sshtest.New(t, "b")
	r := l.Remote("b")
	stub := filepath.Join(r.Home, "stubden")
	body := fmt.Sprintf(`#!/bin/sh
read -r _request
now=$(date -u +%%Y-%%m-%%dT%%H:%%M:%%S.000Z)
printf '{"type":"hello","proto":1,"den":"v0.2.0","hostname":"stub","remote_now":"%%s","policy":"%s","labels":true}\n' "$now"
%s
cat >/dev/null
`, policy, script)
	if err := os.WriteFile(stub, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	l.Local.Apply()
	l.Local.WriteRemotes(fmt.Sprintf("[hosts.b]\ntarget = \"b\"\npolicy = %q\nden_path = %q\n", policy, stub))
	return &rig{t: t, l: l, r: r}
}

// A held id is single use: once the human answered it, a host that offers the
// same id again gets no new question.
func TestRegressHeldIDIsSingleUse(t *testing.T) {
	held := `{"type":"held","id":"11111111111111111111111111111111","session":"` + idA + `","tool":"Bash","body":{"command":"echo replay-me"}}`
	g := stubWith(t, "approve", `
cat <<'EOF'
`+held+`
EOF
while IFS= read -r line; do
  case "$line" in *'"type":"decision"'*) break;; esac
done
cat <<'EOF'
`+held+`
EOF
`)
	g.start()
	r := g.held(1)
	r.Reply(bridge.Decision{Behavior: "allow"})
	time.Sleep(4 * time.Second)
	if n := g.count(remote.HeldArrived); n != 1 {
		t.Errorf("%d questions for one held id: an answered id was answerable again", n)
	}
	if !g.flagged("held") && !g.flagged("again") && !g.flagged("twice") {
		t.Logf("flags: %v (an answered id re-offered may be flagged; not required)", g.statusQuiet().Flags)
	}
}

// A session id that is not a UUID is dropped on every message type, and
// nothing derived from it is written down.
func TestRegressSessionIDsMustBeUUIDs(t *testing.T) {
	bad := `../../\u001b[31mREDLINK`
	g := stubWith(t, "approve", `
cat <<'EOF'
{"type":"snapshot","remote_now":"2026-10-12T12:00:00.000Z","agents":[{"index":0,"session":"`+bad+`","status":"idle","name":"Evil","status_age_ms":0,"started_age_ms":0,"last_active_ms":-1}]}
{"type":"event","cursor":{"ino":1,"off":10},"event":{"v":1,"time":"2026-10-12T12:00:01.000Z","kind":"needs_you","session":"`+bad+`","agent":"x","request":"permission","tool":"Bash"}}
{"type":"held","id":"22222222222222222222222222222222","session":"`+bad+`","tool":"Bash","body":{"command":"ls"}}
EOF
`)
	g.start()
	g.wait("flags", 10*time.Second, func() bool { return len(g.statusQuiet().Flags) >= 1 })
	time.Sleep(2 * time.Second)
	if n := len(g.m.Agents()); n != 0 {
		t.Errorf("%d agents kept from a host whose session id is not a UUID", n)
	}
	if n := g.count(remote.HeldArrived) + g.count(remote.EventArrived); n != 0 {
		t.Errorf("%d held requests or events passed on", n)
	}
	if hits := g.l.Local.Grep("REDLINK"); len(hits) > 0 {
		t.Errorf("the bad session id was written to local state: %v", hits)
	}
	t.Logf("flags: %v", g.statusQuiet().Flags)
}

// Absurd token counts and cost never show or add up, and model names with
// escape sequences never reach local state.
func TestRegressTokensAndModelKeysAreChecked(t *testing.T) {
	evilKey := `model\u001b[31mRED\u001b[0m\u001b]0;PWNED-TITLE\u0007\u001b]52;c;UFdORUQ=\u0007‮exe.GNP NEXT\r\nCR\u0000NUL`
	g := stubWith(t, "observe", `
cat <<'EOF'
{"type":"snapshot","remote_now":"2026-10-12T12:00:00.000Z","agents":[{"index":0,"session":"`+idA+`","status":"idle","name":"Huge","status_age_ms":0,"started_age_ms":0,"last_active_ms":-1,"context_tokens":1000000000000000,"context_window":1,"has_usage":true,"reported_usd":1e12,"tokens":{"claude-opus-5-5":{"input":7777777777777,"output":-5,"cache_creation":0,"cache_read":0},"`+evilKey+`":{"input":1,"output":1}}}]}
EOF
`)
	g.start()
	g.wait("the agent", 10*time.Second, func() bool { return len(g.m.Agents()) == 1 })
	time.Sleep(time.Second)
	a := g.m.Agents()[0]
	if n := a.Cost.Total(); n < 0 || n > 1e11 {
		t.Errorf("%d tokens kept for one session", n)
	}
	if a.Cost.USD < 0 || a.Cost.USD > 1e6 {
		t.Errorf("an estimated cost of $%v kept", a.Cost.USD)
	}
	if r := a.Cost.ReportedUSD; r != nil && (*r < 0 || *r > 1e6) {
		t.Errorf("a reported cost of $%v kept", *r)
	}
	g.wait("a flag", 5*time.Second, func() bool { return len(g.statusQuiet().Flags) > 0 })
	t.Logf("flags: %v", g.statusQuiet().Flags)
	// Whatever Den stored of this host.
	for _, bad := range []string{"PWNED", "202e", "\\u0000", "\\u001b", "7777777777777"} {
		if hits := g.l.Local.Grep(bad); len(hits) > 0 {
			t.Errorf("%q was written to local state: %v", bad, hits)
		}
	}
}

// Two hosts that send the same session id are called out, not left to look
// like one agent. Slow: real Den TUI.
func TestRegressSameSessionIDOnTwoHostsIsCalledOut(t *testing.T) {
	if testing.Short() || os.Getenv("DEN_SLOW_TESTS") != "1" {
		t.Skip("types into the TUI in a private tmux: set DEN_SLOW_TESTS=1")
	}
	l := sshtest.New(t, "b", "c")
	l.Local.Apply()
	l.Local.WriteConfig("[orchestrator]\nspawn = false\n")
	l.Local.WriteRemotes(l.RemotesTOML("observe"))
	l.Remote("b").SeedSession(sshtest.SessionOpts{ID: idA, Name: "alpha"})
	l.Remote("c").SeedSession(sshtest.SessionOpts{ID: idA, Name: "gamma"})

	tm := l.Local.StartTUI(200, 56, []string{"DEN_ORCHESTRATOR_SPAWN=off", "DEN_RESTORE=off"})
	tm.WaitFor(30*time.Second, "2 agents")
	tm.Type("/fleet")
	tm.Keys("Enter")
	f := tm.WaitFor(20*time.Second, "same session id as")
	t.Logf("--- fleet ---\n%s", sshtest.Squeeze(f))
	tm.Type("/remote")
	tm.Keys("Enter")
	f = tm.WaitFor(20*time.Second, "Remote hosts")
	time.Sleep(time.Second)
	f = sshtest.Squeeze(tm.Capture())
	t.Logf("--- remote ---\n%s", f)
	if !strings.Contains(f, "same session id") {
		t.Errorf("/remote does not flag the hosts that share a session id:\n%s", f)
	}
}

// A context size, window or usage percentage out of bounds is cleared before
// it is shown or written down, and the host is flagged; plausible figures of
// another agent on the same host are kept.
func TestRegressContextAndUsageFiguresAreBounded(t *testing.T) {
	g := stubWith(t, "observe", `
cat <<'EOF'
{"type":"snapshot","remote_now":"2026-10-12T12:00:00.000Z","agents":[{"index":0,"session":"`+idA+`","status":"idle","name":"Huge","status_age_ms":0,"started_age_ms":0,"last_active_ms":-1,"context_tokens":8888888888888,"context_window":1,"has_usage":true,"limits":{"filled":987654,"five_hour":-40,"seven_day":4321,"usd":1e12}},{"index":1,"session":"`+idB+`","status":"idle","name":"Fine","status_age_ms":0,"started_age_ms":0,"last_active_ms":-1,"context_tokens":150000,"context_window":1000000,"has_usage":true,"limits":{"filled":15,"five_hour":40,"seven_day":10,"usd":12.5}}]}
EOF
`)
	g.start()
	g.wait("both agents", 10*time.Second, func() bool { return len(g.m.Agents()) == 2 })
	g.wait("a flag", 5*time.Second, func() bool { return g.flagged("implausible context or usage") })
	time.Sleep(time.Second)
	for _, a := range g.m.Agents() {
		switch a.Session.SessionID {
		case idA:
			if a.Usage.Tokens > 10_000_000 || a.Usage.Window > 10_000_000 || a.Usage.Tokens < 0 {
				t.Errorf("the huge agent's context %d / %d kept", a.Usage.Tokens, a.Usage.Window)
			}
			if l := a.Limits; (l.Filled != nil && (*l.Filled < 0 || *l.Filled > 100)) || (l.FiveHour != nil && (*l.FiveHour < 0 || *l.FiveHour > 100)) ||
				(l.SevenDay != nil && (*l.SevenDay < 0 || *l.SevenDay > 100)) || (l.USD != nil && (*l.USD < 0 || *l.USD > 100_000)) {
				t.Errorf("out-of-bounds limits kept: %+v", l)
			}
		case idB:
			if a.Usage.Tokens != 150000 || a.Usage.Window != 1000000 {
				t.Errorf("a plausible agent's context was changed: %d / %d", a.Usage.Tokens, a.Usage.Window)
			}
			if a.Limits.USD == nil || *a.Limits.USD != 12.5 {
				t.Errorf("a plausible agent's spend was changed: %+v", a.Limits)
			}
		}
	}
	// Nothing out of bounds is written down.
	for _, bad := range []string{"8888888888888", "987654", "4321"} {
		if hits := g.l.Local.Grep(bad); len(hits) > 0 {
			t.Errorf("%q was written to local state: %v", bad, hits)
		}
	}
}
