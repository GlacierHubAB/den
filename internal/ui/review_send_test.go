package ui

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/peer"
	"github.com/GlacierHubAB/den/internal/review"
	"github.com/GlacierHubAB/den/internal/theme"
)

// fakePeer replaces the socket delivery for a test and records what was
// sent where.
type fakePeer struct {
	mu   sync.Mutex
	got  map[string][]string // socket → texts
	fail map[string]error
	// framed is each text as the agent sees it, with its header: the
	// human's (peer.Header) or Den's own (peer.DenHeader).
	framed map[string][]string
}

func useFakePeer(t *testing.T) *fakePeer {
	f := &fakePeer{got: map[string][]string{}, fail: map[string]error{}, framed: map[string][]string{}}
	record := func(compose func(string) string) func(socket, text string) error {
		return func(socket, text string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if err := f.fail[socket]; err != nil {
				return err
			}
			f.got[socket] = append(f.got[socket], text)
			f.framed[socket] = append(f.framed[socket], compose(text))
			return nil
		}
	}
	oldPeer, oldDen, oldUsage := sendPeer, sendDen, sendUsage
	sendPeer, sendDen, sendUsage = record(peer.Compose), record(peer.ComposeDen), record(peer.ComposeUsage)
	t.Cleanup(func() { sendPeer, sendDen, sendUsage = oldPeer, oldDen, oldUsage })
	return f
}

// runQuick runs a command and every command batched in it, keeping the
// messages that arrive at once; ticks (the flash timers) are left behind.
func runQuick(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(200 * time.Millisecond):
		return nil
	}
	if b, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range b {
			out = append(out, runQuick(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

func sendModel(t *testing.T) Model {
	mk := func(id, name string, st fleet.Status, socket string) fleet.Agent {
		return fleet.Agent{Session: discover.Session{SessionID: id, Socket: socket, Alive: st != fleet.Gone}, Name: name, Status: st}
	}
	m := Model{st: newStyles(theme.Deepharness), store: inbox.New(), tab: tabReview, width: 160, height: 45}
	m.agents = []fleet.Agent{
		mk("s-mossy", "Mossy Lemur", fleet.Idle, "/tmp/mossy.sock"),
		mk("s-maple", "Maple Owl", fleet.Working, "/tmp/maple.sock"),
		mk("s-comet", "Comet Newt", fleet.Gone, "/tmp/comet.sock"),
	}
	m.base = m.agents // refresh rebuilds agents from base
	m.input = newInput(m.st)
	m.rev.commentsPath = filepath.Join(t.TempDir(), "reviews.json")
	at := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	m.rev.comments = []review.Comment{
		{ID: "m1", Root: "/r/demo-app", Path: "main.go", Side: "new", Start: 4, End: 4, Quote: []string{"+\treturn a - b"}, Body: "Should add.", SessionID: "s-mossy", Created: at},
		{ID: "m2", Root: "/r/demo-app", Path: "util.go", Side: "new", Start: 9, End: 10, Quote: []string{"+x := 1", "+y := 2"}, Body: "Name these.", SessionID: "s-mossy", Created: at},
		{ID: "p1", Root: "/r/demo-app", Path: "api.go", Side: "new", Start: 2, End: 2, Quote: []string{"+// TODO"}, Body: "Do it now.", SessionID: "s-maple", Created: at},
		{ID: "c1", Root: "/r/demo-app", Path: "old.go", Side: "new", Start: 1, End: 1, Quote: []string{"+package old"}, Body: "Delete this file.", SessionID: "s-comet", Created: at},
		{ID: "n1", Root: "/r/demo-app", Path: "README.md", Side: "new", Start: 1, End: 1, Quote: []string{"+hand edit"}, Body: "Who wrote this?", Created: at},
		{ID: "old", Root: "/r/demo-app", Path: "main.go", Side: "new", Start: 1, End: 1, Quote: []string{"+package main"}, Body: "Already sent.", SessionID: "s-mossy", Created: at, Sent: at.Add(time.Minute)},
	}
	return m
}

func byID(m Model, id string) review.Comment {
	for _, c := range m.rev.comments {
		if c.ID == id {
			return c
		}
	}
	return review.Comment{}
}

func TestSendReviewOneMessagePerAgent(t *testing.T) {
	f := useFakePeer(t)
	m := sendModel(t)
	mm, cmd := m.command("/send")
	m = mm.(Model)
	if !strings.Contains(m.flash, "Sending review to Maple Owl, Mossy Lemur.") || !strings.Contains(m.flash, "Comet Newt (1), no agent (1)") || !strings.Contains(m.flash, "/send orchestrator") {
		t.Errorf("flash = %q", m.flash)
	}
	// While the messages are on their way the drafts are not sent twice.
	if c := byID(m, "m1"); !c.Draft() || !m.rev.sending["m1"] {
		t.Fatalf("m1 before delivery = %+v sending=%v", c, m.rev.sending)
	}
	if _, again := m.command("/send"); len(runQuick(again)) != 0 {
		t.Error("a second /send delivered the same drafts again")
	}
	for _, msg := range runQuick(cmd) {
		if s, ok := msg.(sentMsg); ok {
			mm, _ := m.Update(s)
			m = mm.(Model)
		}
	}
	if len(f.got["/tmp/mossy.sock"]) != 1 || len(f.got["/tmp/maple.sock"]) != 1 || len(f.got["/tmp/comet.sock"]) != 0 {
		t.Fatalf("deliveries = %v", f.got)
	}
	want := review.Message([]review.Comment{byID(m, "m1"), byID(m, "m2")})
	if got := f.got["/tmp/mossy.sock"][0]; got != want || !strings.Contains(got, "main.go:4\n  > \treturn a - b\n  Should add.") || strings.Contains(got, "Already sent") {
		t.Errorf("mossy got:\n%s", got)
	}
	for _, id := range []string{"m1", "m2", "p1"} {
		if c := byID(m, id); c.Draft() || !c.Open() || m.rev.sending[id] {
			t.Errorf("%s after delivery = %+v", id, c)
		}
	}
	for _, id := range []string{"c1", "n1"} {
		if !byID(m, id).Draft() {
			t.Errorf("%s left draft status", id)
		}
	}
	// Saved: a restart sees them sent.
	saved := review.LoadComments(m.rev.commentsPath)
	if len(saved) != 6 || saved[0].Draft() {
		t.Errorf("saved = %+v", saved)
	}
	// The inbox shows the message like any message to an agent.
	found := false
	for _, it := range m.store.Items() {
		if it.Kind == inbox.Sent && it.SessionID == "s-mossy" && strings.HasPrefix(it.Title, "→ Review of your changes: 2 comments.") && it.Resolved == "delivered" {
			found = true
		}
	}
	if !found {
		t.Error("no delivered notice in the inbox")
	}
}

// The bytes on the wire: Den's peer header, then the review message.
func TestSendReviewWireFormat(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "den-rv")
	if err != nil {
		t.Skip(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 1<<16)
		n, _ := c.Read(buf)
		got <- string(buf[:n])
	}()
	m := sendModel(t)
	m.agents[0].Session.Socket = sock
	mm, cmd := m.command("/send mossy")
	m = mm.(Model)
	runQuick(cmd)
	text := review.Message([]review.Comment{byID(m, "m1"), byID(m, "m2")})
	select {
	case line := <-got:
		var wire struct {
			Type    string `json:"type"`
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		}
		if !strings.HasSuffix(line, "}\n") || strings.Count(line, "\n") != 1 {
			t.Errorf("not one JSON line: %q", line)
		}
		if err := json.Unmarshal([]byte(line), &wire); err != nil || wire.Type != "user" || wire.Message.Role != "user" || wire.Message.Content != peer.Header+"\n\n"+text {
			t.Errorf("wire = %s (%v)", line, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing arrived on the socket")
	}
}

func TestSendReviewFailureKeepsDrafts(t *testing.T) {
	f := useFakePeer(t)
	f.fail["/tmp/mossy.sock"] = errors.New("connect: refused")
	m := sendModel(t)
	mm, cmd := m.command("/send Mossy")
	m = mm.(Model)
	for _, msg := range runQuick(cmd) {
		if s, ok := msg.(sentMsg); ok {
			mm, _ := m.Update(s)
			m = mm.(Model)
		}
	}
	if !byID(m, "m1").Draft() || m.rev.sending["m1"] || !strings.Contains(m.flash, "stay drafts") {
		t.Errorf("after failure: %+v flash %q", byID(m, "m1"), m.flash)
	}
	if !byID(m, "p1").Draft() || len(f.got) != 0 {
		t.Errorf("/send Mossy touched another agent: %v", f.got)
	}
}

func TestSendReviewToOrchestrator(t *testing.T) {
	useFakePeer(t)
	m := sendModel(t)
	m.chatLog = filepath.Join(t.TempDir(), "orchestrator.jsonl")
	mm, _ := m.command("/send orchestrator")
	m = mm.(Model)
	if len(m.orch.pending) != 1 {
		t.Fatalf("orchestrator prompts = %v", m.orch.pending)
	}
	p := m.orch.pending[0]
	if !strings.Contains(p, "for Comet Newt, whose session has ended") || !strings.Contains(p, "on lines no agent wrote") || strings.Contains(p, "Should add.") {
		t.Errorf("prompt:\n%s", p)
	}
	for _, id := range []string{"c1", "n1"} {
		if c := byID(m, id); c.Draft() || c.Via != "orchestrator" {
			t.Errorf("%s = %+v", id, c)
		}
	}
	if !byID(m, "m1").Draft() {
		t.Error("a live agent's draft went to the orchestrator")
	}
	if m.tab != tabReview {
		t.Error("handing comments on left the Review view")
	}
}

// Sent comments turn addressed when the quoted lines change on disk, and
// stay open while they do not.
func TestReviewMarksAddressed(t *testing.T) {
	root := t.TempDir()
	// "return a - b" is there twice: in add, which m1 is about, and in sub.
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc add(a, b int) int {\n\treturn a - b\n}\n\nfunc sub(a, b int) int {\n\treturn a - b\n}\n"), 0o644)
	useFakePeer(t)
	m := sendModel(t)
	for i := range m.rev.comments {
		m.rev.comments[i].Root = root
	}
	mm, cmd := m.command("/send mossy")
	m = mm.(Model)
	for _, msg := range runQuick(cmd) {
		if s, ok := msg.(sentMsg); ok {
			mm, _ := m.Update(s)
			m = mm.(Model)
		}
	}
	if c := byID(m, "m1"); c.Copies != 2 || !c.Open() {
		t.Fatalf("m1 sent = %+v", c)
	}
	open := func() []review.Comment {
		var out []review.Comment
		for _, c := range m.rev.comments {
			if c.Open() {
				out = append(out, c)
			}
		}
		return out
	}
	// "old" quotes "package main", which is still there; m1's line too.
	m.applyReview(reviewLoadedMsg{addressed: addressedComments(open())})
	if !byID(m, "m1").Open() || !byID(m, "old").Open() {
		t.Fatalf("addressed too early: %+v", m.rev.comments)
	}
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\n// add adds.\nfunc add(a, b int) int {\n\treturn a + b\n}\n\nfunc sub(a, b int) int {\n\treturn a - b\n}\n"), 0o644)
	m.applyReview(reviewLoadedMsg{addressed: addressedComments(open())})
	if c := byID(m, "m1"); c.Open() || c.Addressed.IsZero() {
		t.Errorf("m1 not addressed: %+v", c)
	}
	// m2 (util.go) was sent too, and its file is gone: addressed as well.
	if !byID(m, "old").Open() || !byID(m, "p1").Draft() || byID(m, "m2").Open() {
		t.Error("a comment has the wrong state")
	}
	if c := review.LoadComments(m.rev.commentsPath); len(c) == 0 || c[0].Addressed.IsZero() {
		t.Error("addressed not saved")
	}
	// Drawn as such in the drafts column and under the line.
	m.agents[0].Status = fleet.Idle
	out := ansi.Strip(m.notesPane(50, 40))
	if !strings.Contains(out, "✓ main.go:4") || !strings.Contains(out, "→ main.go:1") || !strings.Contains(out, "✓ util.go:9") {
		t.Errorf("notes pane:\n%s", out)
	}
	if !strings.Contains(out, "session ended · /send orchestrator hands these on") {
		t.Errorf("gone recipient not explained:\n%s", out)
	}
	if got := ansi.Strip(strings.Join(m.commentRows(byID(m, "m1"), 80), "\n")); !strings.Contains(got, "sent · addressed") {
		t.Errorf("comment rows = %q", got)
	}
	if got := m.notesTitle(); got != "Review · 3 drafts · 1 open" {
		t.Errorf("title = %q", got)
	}
}

// A review held by the typing guard goes out, ids and all, once you leave
// that terminal.
func TestHeldReviewKeepsItsComments(t *testing.T) {
	f := useFakePeer(t)
	m := sendModel(t)
	text := review.Message([]review.Comment{byID(m, "p1")})
	it := m.store.Sent("s-maple", text, "waiting: you are at that terminal")
	m.rev.sending = map[string]bool{"p1": true}
	m.queue = []outgoing{{agent: m.agents[1], text: text, item: it, since: time.Now(), review: []string{"p1"}}}
	cmds, queued := m.flushQueue()
	if queued || len(cmds) != 1 {
		t.Fatalf("queued=%v cmds=%d", queued, len(cmds))
	}
	s, ok := cmds[0]().(sentMsg)
	if !ok || len(s.review) != 1 || s.review[0] != "p1" {
		t.Fatalf("msg = %+v", s)
	}
	m.applySent(s)
	if byID(m, "p1").Draft() || len(f.got["/tmp/maple.sock"]) != 1 {
		t.Errorf("p1 = %+v, got %v", byID(m, "p1"), f.got)
	}
}
