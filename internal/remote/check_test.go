package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/cost"
	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/transcript"
)

// hostileRun runs a Manager against a host under approve that says hello,
// sends msgs, then an event for sidA as a marker, and stays connected. It
// returns once the marker is in the local events log, so every message
// before it was handled, with the updates seen so far.
func hostileRun(t *testing.T, msgs ...any) (*Manager, []Update) {
	t.Helper()
	s := newScratch(t)
	marker := Event{Type: TypeEvent, Cursor: events.Cursor{Ino: 1, Off: 999}, Event: events.New(events.Done, sidA)}
	d := &pipeDialer{serve: func(ctx context.Context, in io.Reader, out io.Writer) error {
		newLineReader(in, MaxLine).next() // Den's request
		writeMsg(out, Hello{Type: TypeHello, Proto: 1, Policy: Approve, RemoteNow: time.Now()})
		for _, v := range msgs {
			writeMsg(out, v)
		}
		writeMsg(out, marker)
		<-ctx.Done()
		return nil
	}}
	h := hostB()
	h.Policy = Approve
	m := s.manager(d, h)
	var mu sync.Mutex
	var ups []Update
	done := make(chan struct{})
	go func() {
		defer close(done)
		for u := range m.Updates() {
			mu.Lock()
			ups = append(ups, u)
			mu.Unlock()
		}
	}()
	m.Start()
	t.Cleanup(func() { m.Close(); <-done })
	deadline := time.Now().Add(5 * time.Second)
	// Wait for the marker itself: a message before it may be an event of
	// the same session, logged before the messages after it are read.
	for !slices.ContainsFunc(localLines(t, s.localEvts), func(ev events.Event) bool { return ev.Session == sidA && ev.Kind == events.Done }) {
		if time.Now().After(deadline) {
			t.Fatal("the marker event never arrived")
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	return m, slices.Clone(ups)
}

func hasFlag(m *Manager, sub string) bool {
	return slices.ContainsFunc(state(m).Flags, func(f string) bool { return strings.Contains(f, sub) })
}

// Every session id a host sends must be a UUID: an agent, an event or a
// held request with anything else is dropped and the host flagged.
func TestHostSessionIDsMustBeUUIDs(t *testing.T) {
	const bad = "../../etc/passwd"
	m, ups := hostileRun(t,
		Snapshot{Type: TypeSnapshot, RemoteNow: time.Now(), Agents: []fleet.Meta{
			{Session: sidA, Status: "idle"},
			{Session: bad, Status: "idle"},
			{Session: "", Status: "idle"},
			{Session: strings.ToUpper(sidA[:35]) + "g", Status: "idle"},
		}},
		Event{Type: TypeEvent, Cursor: events.Cursor{Ino: 1, Off: 10}, Event: events.New(events.NeedsYou, bad)},
		Held{Type: TypeHeld, ID: strings.Repeat("a", 32), Session: bad, Tool: "Bash", Body: json.RawMessage(`{"command":"ls"}`)},
	)
	agents := m.Agents()
	if len(agents) != 1 || agents[0].Session.SessionID != sidA {
		t.Errorf("agents kept: %+v", agents)
	}
	for _, f := range []string{"an agent whose session id", "an event whose session id", "a held request whose session id"} {
		if !hasFlag(m, f) {
			t.Errorf("not flagged: %q; flags %q", f, state(m).Flags)
		}
	}
	for _, u := range ups {
		if u.Kind == HeldArrived || (u.Kind == EventArrived && u.Event.Session != sidA) {
			t.Errorf("an update for a bad session id: %+v", u)
		}
	}
}

func TestIsSessionID(t *testing.T) {
	for s, want := range map[string]bool{
		sidA:                                   true,
		strings.ToUpper(sidA):                  true,
		"":                                     false,
		sidA + "0":                             false,
		strings.ReplaceAll(sidA, "-", "0"):     false,
		"11111111-2222-4333-8444-55555555555g": false,
		"1111111-22222-4333-8444-555555555555": false,
		"11111111-2222-4333-8444-5555555555é":  false,
	} {
		if got := isSessionID(s); got != want {
			t.Errorf("isSessionID(%q) = %v, want %v", s, got, want)
		}
	}
}

// What an older Den saved is checked again when it is loaded.
func TestLoadKnownChecksAgain(t *testing.T) {
	dir := t.TempDir()
	saveKnown(dir, Known{Label: "b", Agents: []fleet.Meta{{Session: sidA}, {Session: "s1"}}})
	k, ok := LoadKnown(dir, "b")
	if !ok || len(k.Agents) != 1 || k.Agents[0].Session != sidA {
		t.Errorf("loaded %v %+v", ok, k.Agents)
	}
}

// A host's token counts are bounded: out of bounds (below zero, or more in
// one session than maxSessionTokens), they are cleared and the host
// flagged, so they are never shown or summed. A reported cost likewise.
func TestImplausibleTokensAreNeverPriced(t *testing.T) {
	const sidB, sidC = "11111111-2222-4333-8444-555555555556", "11111111-2222-4333-8444-555555555557"
	model := "claude-opus-4-5-20251101"
	huge, reported := 4_000_000_000_000.0, 4_000_000.0
	m, _ := hostileRun(t, Snapshot{Type: TypeSnapshot, RemoteNow: time.Now(), Agents: []fleet.Meta{
		{Session: sidA, Status: "idle", Tokens: map[string]transcript.Tokens{model: {Input: 1000, Output: 500}}},
		{Session: sidB, Status: "idle", Tokens: map[string]transcript.Tokens{model: {Output: int64(huge)}}, Reported: &reported},
		{Session: sidC, Status: "idle", Tokens: map[string]transcript.Tokens{model: {Input: 10}},
			Sub: map[string]transcript.Tokens{model: {CacheRead: -5}}},
	}})
	got := map[string]cost.Summary{}
	for _, a := range m.Agents() {
		got[a.Session.SessionID] = a.Cost
	}
	if c := got[sidA]; c.Total() != 1500 || c.USD <= 0 {
		t.Errorf("a plausible session lost its cost: %+v", c)
	}
	for _, sid := range []string{sidB, sidC} {
		if c := got[sid]; c.Total() != 0 || c.USD != 0 || c.ReportedUSD != nil {
			t.Errorf("%s: implausible figures were kept: %+v", sid[len(sid)-1:], c)
		}
	}
	if !hasFlag(m, "implausible token counts") || !hasFlag(m, "implausible reported cost") {
		t.Errorf("flags %q", state(m).Flags)
	}
}

func TestPlausible(t *testing.T) {
	tok := func(ts ...transcript.Tokens) map[string]transcript.Tokens {
		m := map[string]transcript.Tokens{}
		for i, t := range ts {
			m[string(rune('a'+i))] = t
		}
		return m
	}
	half := int64(maxSessionTokens / 2)
	for _, c := range []struct {
		maps []map[string]transcript.Tokens
		want bool
	}{
		{nil, true},
		{[]map[string]transcript.Tokens{tok(transcript.Tokens{CacheRead: maxSessionTokens})}, true},
		{[]map[string]transcript.Tokens{tok(transcript.Tokens{CacheRead: maxSessionTokens + 1})}, false},
		{[]map[string]transcript.Tokens{tok(transcript.Tokens{Input: half}), tok(transcript.Tokens{Output: half + 1})}, false},
		{[]map[string]transcript.Tokens{tok(transcript.Tokens{Input: math.MaxInt64}, transcript.Tokens{Input: math.MaxInt64})}, false},
		{[]map[string]transcript.Tokens{tok(transcript.Tokens{Input: -1})}, false},
	} {
		if got := plausible(c.maps...); got != c.want {
			t.Errorf("plausible(%v) = %v", c.maps, got)
		}
	}
	for _, r := range []float64{math.NaN(), math.Inf(1), -1, maxReportedUSD + 1} {
		a, why, ok := checkMeta(fleet.Meta{Session: sidA, Reported: &r})
		if !ok || a.Reported != nil || len(why) != 1 {
			t.Errorf("reported %v: kept %v, why %q", r, a.Reported, why)
		}
	}
}

// Model ids are keys in the file Den keeps of a host: only letters, digits
// and . _ : - up to maxModelKey are kept, and no more than maxModels.
func TestModelKeysAreRestricted(t *testing.T) {
	good := "us.anthropic.claude-opus-4-5-20251101-v1:0"
	m, _ := hostileRun(t, Snapshot{Type: TypeSnapshot, RemoteNow: time.Now(), Agents: []fleet.Meta{{
		Session: sidA, Status: "idle",
		Tokens: map[string]transcript.Tokens{
			good:                      {Input: 10},
			"../../x":                 {Input: 20},
			"a\u001b]52;c;eA==\u0007": {Input: 30},
			"claude opus":             {Input: 40},
			strings.Repeat("a", 129):  {Input: 50},
			"":                        {Input: 60},
		},
	}}})
	a := m.Agents()[0]
	if a.Cost.Tokens.Input != 10 {
		t.Errorf("tokens kept = %d, want only the good model's 10", a.Cost.Tokens.Input)
	}
	if !hasFlag(m, "model name") {
		t.Errorf("flags %q", state(m).Flags)
	}
	k, _ := LoadKnown(m.o.StateDir, "b")
	if len(k.Agents) != 1 || len(k.Agents[0].Tokens) != 1 || k.Agents[0].Tokens[good] != (transcript.Tokens{Input: 10}) {
		t.Errorf("saved tokens = %+v", k.Agents)
	}

	many := map[string]transcript.Tokens{}
	for i := range maxModels + 1 {
		many[fmt.Sprintf("m%d", i)] = transcript.Tokens{Input: 1}
	}
	if a, why, _ := checkMeta(fleet.Meta{Session: sidA, Sub: many}); a.Sub != nil || len(why) != 1 {
		t.Errorf("%d models kept %d, why %q", len(many), len(a.Sub), why)
	}
	for s, want := range map[string]bool{good: true, "claude-sonnet-4-5": true, "x/y": false, "a b": false, "é": false} {
		if modelKey(s) != want {
			t.Errorf("modelKey(%q) = %v", s, !want)
		}
	}
}

func TestManagerFlag(t *testing.T) {
	s := newScratch(t)
	m := s.manager(&pipeDialer{}, hostB())
	defer m.Close()
	m.Flag("b", "an agent with the same session id as Mossy Lemur here")
	m.Flag("nope", "x")
	if !hasFlag(m, "same session id") || len(state(m).Flags) != 1 {
		t.Errorf("flags %q", state(m).Flags)
	}
}

// Events whose session id is not a UUID are dropped like events for an
// unlisted session, and count towards flooding: a host that keeps sending
// them is disabled, and none reach the events log.
func TestNonUUIDEventsCountAsFlooding(t *testing.T) {
	s := newScratch(t)
	d := &pipeDialer{serve: func(ctx context.Context, in io.Reader, out io.Writer) error {
		newLineReader(in, MaxLine).next() // Den's request
		writeMsg(out, Hello{Type: TypeHello, Proto: 1, Policy: Observe, RemoteNow: time.Now()})
		writeMsg(out, Snapshot{Type: TypeSnapshot, RemoteNow: time.Now(), Agents: []fleet.Meta{{Session: sidA, Status: "busy", Name: "a"}}})
		for i := range 20 {
			ev := events.New(events.NeedsYou, "not-a-uuid")
			ev.Time = time.Now().Add(time.Minute) // live, not backfill
			writeMsg(out, Event{Type: TypeEvent, Cursor: events.Cursor{Ino: 1, Off: int64(i + 1)}, Event: ev})
		}
		<-ctx.Done()
		return nil
	}}
	m := NewManager(Config{Hosts: []Host{hostB()}}, Options{
		Version: "v0.2.0", StateDir: s.stateDir, EventsPath: s.localEvts, Dial: d.dial,
		Backoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond, Healthy: time.Second,
		FloodDrops: 10, FloodWindow: time.Minute,
	})
	go func() {
		for range m.Updates() {
		}
	}()
	m.Start()
	t.Cleanup(m.Close)
	waitFor(t, "the host disabled", func() bool { return state(m).State == Disabled })
	if st := state(m); !slices.Contains(st.Flags, "an event whose session id is not a UUID") || st.Dropped < 10 {
		t.Errorf("flags %q dropped %d", st.Flags, st.Dropped)
	}
	if n := len(localLines(t, s.localEvts)); n != 0 {
		t.Errorf("%d events logged", n)
	}
}

// Context and usage figures out of bounds are cleared before they are
// shown or written to the host's file, and the host is flagged; a file
// written before is checked again when it is loaded.
func TestImplausibleContextAndUsageAreDropped(t *testing.T) {
	const sidB = "11111111-2222-4333-8444-555555555556"
	pct, huge, neg := 42.0, 1e15, -5.0
	bad := fleet.Meta{Session: sidB, Status: "idle", Context: 1_000_000_000_000_000, Window: 200_000, HasUsage: true,
		Limits: &fleet.MetaLimits{Context: &huge, FiveHour: &huge, SevenDay: &neg, Filled: &pct}}
	m, _ := hostileRun(t, Snapshot{Type: TypeSnapshot, RemoteNow: time.Now(), Agents: []fleet.Meta{
		{Session: sidA, Status: "idle", Context: 50_000, Window: 200_000, HasUsage: true},
		bad,
	}})
	for _, a := range m.Agents() {
		switch a.Session.SessionID {
		case sidA:
			if a.Usage.Tokens != 50_000 || a.Usage.Window != 200_000 {
				t.Errorf("a plausible usage was lost: %+v", a.Usage)
			}
		case sidB:
			if a.Usage.Tokens != 0 || a.Usage.Window != 0 || a.HasUsage {
				t.Errorf("implausible context kept: %+v", a.Usage)
			}
			if l := a.Limits; l.Context != nil || l.FiveHour != nil || l.SevenDay != nil || l.Filled == nil || *l.Filled != pct {
				t.Errorf("limits %+v", l)
			}
		}
	}
	if !hasFlag(m, "implausible context or usage figures") {
		t.Errorf("flags %q", state(m).Flags)
	}
	// What is kept, and what an older file held, is checked on load.
	dir := t.TempDir()
	saveKnown(dir, Known{Label: "b", Agents: []fleet.Meta{bad}})
	raw, _ := os.ReadFile(filepath.Join(dir, "b.json"))
	if !strings.Contains(string(raw), "1000000000000000") {
		t.Fatalf("the test file does not hold the raw figure:\n%s", raw)
	}
	k, _ := LoadKnown(dir, "b")
	if len(k.Agents) != 1 || k.Agents[0].Context != 0 || k.Agents[0].Limits.FiveHour != nil {
		t.Errorf("loaded %+v", k.Agents)
	}
	if *bad.Limits.FiveHour != huge {
		t.Error("the check changed the host's copy")
	}
}
