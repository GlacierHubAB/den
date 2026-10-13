package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/closer"
	"github.com/GlacierHubAB/den/internal/cost"
	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/fleet"
)

// Fuzz targets for what the protocol decoders take from the wire, on both
// sides. Under a plain `go test` they run their seeds only; long fuzzing is
// manual:
//
//	go test ./internal/remote -run '^$' -fuzz FuzzSnapshot -fuzztime 2m
//
// The properties, wherever they apply: nothing panics; a type or a key the
// version does not know is reported, never acted on; every string a decoder
// lets through to the model is clean (valid UTF-8, no ESC, C0, C1, bidi
// control, or line and paragraph separator); sizes stay inside the caps; and
// a valid message survives encode and decode unchanged.

// unclean says what is wrong with s for a place that shows it, "" when it
// is fine. lines allows the newline and tab a code line keeps.
func unclean(s string, lines bool) string {
	if !utf8.ValidString(s) {
		return "invalid UTF-8"
	}
	for _, r := range s {
		switch {
		case lines && (r == '\n' || r == '\t'):
		case r < 0x20 || r == 0x7f:
			return fmt.Sprintf("control character %U", r)
		case r >= 0x80 && r <= 0x9f:
			return fmt.Sprintf("C1 control %U", r)
		case r == 0x2028 || r == 0x2029:
			return fmt.Sprintf("separator %U", r)
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0x200e, r == 0x200f, r == 0x061c:
			return fmt.Sprintf("bidi control %U", r)
		}
	}
	return ""
}

// checkClean walks v and fails on any string (or map key) that is not clean.
func checkClean(t *testing.T, what string, v any, lines bool) {
	t.Helper()
	var walk func(path string, v reflect.Value)
	walk = func(path string, v reflect.Value) {
		switch v.Kind() {
		case reflect.String:
			if why := unclean(v.String(), lines); why != "" {
				t.Fatalf("%s: %s = %q: %s", what, path, v.String(), why)
			}
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				walk(path, v.Elem())
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if v.Type().Field(i).IsExported() {
					walk(path+"."+v.Type().Field(i).Name, v.Field(i))
				}
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				walk(fmt.Sprintf("%s[%d]", path, i), v.Index(i))
			}
		case reflect.Map:
			for _, k := range v.MapKeys() {
				walk(path+"{key}", k)
				walk(fmt.Sprintf("%s[%v]", path, k), v.MapIndex(k))
			}
		}
	}
	walk("", reflect.ValueOf(v))
}

// seedLines are messages as hosts and Dens send them, valid and not.
var seedLines = []string{
	`{"type":"hello","proto":1,"den":"v0.4.0","claude":"2.1.289","hostname":"devbox","remote_now":"2026-10-12T12:00:00.123Z","policy":"full","labels":true,"cursor":{"ino":12,"off":3456,"head":"ab","head_len":2}}`,
	`{"type":"hello","proto":1,"policy":"approve","labels":false,"controller":"c0ffee","attached":{"pid":4242,"since":"2026-10-12T12:00:00Z"},"note":"n"}`,
	`{"type":"snapshot","remote_now":"2026-10-12T12:00:00Z","agents":[{"index":0,"session":"11111111-2222-4333-8444-555555555555","status":"idle","name":"Brave Quokka","model":"claude-opus-4-5","repo":"r","branch":"b","context_tokens":1000,"context_window":200000,"has_usage":true,"tokens":{"claude-opus-4-5":{"input":1,"output":2,"cache_creation":3,"cache_read":4}}}]}`,
	`{"type":"snapshot","remote_now":"2026-10-12T12:00:00Z","agents":[{"session":"11111111-2222-4333-8444-555555555555","name":"\u001b[2Jx\u202e","tokens":{"bad key\n":{"input":-1}},"reported_usd":1e300,"full":{"title":"\u001b]52;c;eA==\u0007","folder":"/x\ny","recent":["a\u009bb"]}}]}`,
	`{"type":"event","cursor":{"ino":1,"off":2,"head":"h","head_len":1},"event":{"v":1,"time":"2026-10-12T12:00:00Z","kind":"needs_you","session":"11111111-2222-4333-8444-555555555555","agent":"A","request":"permission","tool":"Bash"}}`,
	`{"type":"event","event":{"v":1,"kind":"turn_start","session":"x\u001b[0m","host":"third"}}`,
	`{"type":"held","id":"0123456789abcdef0123456789abcdef","session":"11111111-2222-4333-8444-555555555555","tool":"Bash","body":{"command":"go test ./...","description":"d"}}`,
	`{"type":"held","id":"0123456789abcdef0123456789abcdef","session":"11111111-2222-4333-8444-555555555555","tool":"AskUserQuestion","body":{"questions":[{"question":"Q?","header":"H","options":[{"label":"A","description":"a"},{"label":"B","description":"b"}],"multiSelect":false}]}}`,
	`{"type":"held","id":"x","session":"s","tool":"Bash\n","body":[]}`,
	`{"type":"gone","id":"0123456789abcdef0123456789abcdef"}`,
	`{"type":"result","id":"i","ok":true,"killed":false,"error":"e\u001b[31m","diff":{"root":"/r","repo":"r","branch":"b","files":[{"path":"a.txt","created":true,"hunks":[{"old_start":0,"new_start":1,"lines":["+one","\u001b[31m+two"," ctx","?bad"],"mine":[0,1,99,-1]}]}]}}`,
	`{"type":"result","id":"i","ok":false,"error":"nope"}`,
	`{"type":"error","code":"proto","message":"this den speaks proto 1, not 2"}`,
	`{"type":"decision","id":"0123456789abcdef0123456789abcdef","hash":"ff","behavior":"allow","message":"m","answers":{"Which?":"A"}}`,
	`{"type":"decision","id":"i","behavior":"","extra":1}`,
	`{"type":"ping"}`,
	`{"type":"send","id":"s1","session":"11111111-2222-4333-8444-555555555555","text":"hello"}`,
	`{"type":"close","id":"c1","session":"11111111-2222-4333-8444-555555555555","force":true}`,
	`{"type":"diff","id":"d1","session":"11111111-2222-4333-8444-555555555555"}`,
	`{"type":"exec","cmd":"id"}`,
	`{"type":"ping","extra":true}`,
	`{"type":null}`, `{"type":1}`, `{}`, `[]`, `null`, `"x"`, `{"type":"hello","proto":"1"}`, ``, `{`,
	"{\"type\":\"ping\"}\n{\"type\":\"ping\"}",
}

// edgeLines are seeds at the edges the caps are drawn: more models than a
// session uses, a send one byte over its cap, escapes in every string an
// event carries.
func edgeLines() []string {
	models := make([]string, 0, maxModels+1)
	for i := 0; i <= maxModels; i++ {
		models = append(models, fmt.Sprintf(`"m%d":{"input":1}`, i))
	}
	return []string{
		`{"type":"snapshot","agents":[{"session":"11111111-2222-4333-8444-555555555555","tokens":{` + strings.Join(models, ",") + `}}]}`,
		`{"type":"snapshot","agents":[{"session":"11111111-2222-4333-8444-555555555555","context_tokens":10000001,"tokens":{"m":{"input":10000000001}}}]}`,
		`{"type":"event","cursor":{"ino":1,"off":2},"event":{"v":1,"kind":"turn_failed","session":"11111111-2222-4333-8444-555555555555","request":"\u001b[2J","tool":"\u202e","error":"\u001b]52;c;eA==\u0007","reason":"a\u009bb"}}`,
		`{"type":"send","id":"s1","session":"11111111-2222-4333-8444-555555555555","text":"` + strings.Repeat("a", MaxSendText) + `"}`,
		`{"type":"send","id":"s1","session":"11111111-2222-4333-8444-555555555555","text":"` + strings.Repeat("a", MaxSendText+1) + `"}`,
		`{"type":"send","id":"s1","session":"11111111-2222-4333-8444-555555555555","text":"  \n "}`,
	}
}

func addSeedLines(f *testing.F) {
	for _, s := range append(append([]string(nil), seedLines...), edgeLines()...) {
		f.Add([]byte(s))
	}
}

// The line splitter: every line it returns is at most max bytes, has no
// newline, and the lines are the input's, in order; a line over max ends
// the stream with ErrLineTooLong; a cut-off last line is not a message.
func FuzzLineReader(f *testing.F) {
	for _, s := range seedLines {
		f.Add([]byte(s+"\n"), uint16(len(s)))
		f.Add([]byte(s+"\n"), uint16(len(s)-1))
		f.Add([]byte(s+"\n"), uint16(len(s)+1))
		f.Add([]byte(s+"\n"+s+"\n"), uint16(len(s)+1))
		f.Add([]byte(s), uint16(8))
	}
	f.Add(bytes.Repeat([]byte("a"), 70<<10), uint16(100))
	f.Add(bytes.Repeat([]byte("a\n"), 5000), uint16(1))
	f.Fuzz(func(t *testing.T, data []byte, max uint16) {
		m := int(max)
		lr := newLineReader(bytes.NewReader(data), m)
		var got [][]byte
		var end error
		for i := 0; i <= len(data)+1; i++ {
			line, err := lr.next()
			if err != nil {
				end = err
				break
			}
			if len(line) > m {
				t.Fatalf("a line of %d bytes over the cap %d", len(line), m)
			}
			if bytes.IndexByte(line, '\n') >= 0 {
				t.Fatalf("a newline inside a line: %q", line)
			}
			got = append(got, append([]byte(nil), line...))
		}
		if end == nil {
			t.Fatal("the reader never ended")
		}
		// What came back is a prefix of the input split at newlines.
		want := bytes.Split(data, []byte("\n"))
		for i, line := range got {
			if i >= len(want) || !bytes.Equal(line, want[i]) {
				t.Fatalf("line %d = %q, the input has %q", i, line, want[min(i, len(want)-1)])
			}
		}
		switch {
		case errors.Is(end, ErrLineTooLong):
			if len(want) > len(got) && len(want[len(got)]) <= m && len(got) < len(want)-1 {
				t.Fatalf("too long reported for a line of %d bytes with cap %d", len(want[len(got)]), m)
			}
		case errors.Is(end, io.EOF), errors.Is(end, io.ErrUnexpectedEOF):
			if len(got) != len(want)-1 && !(len(data) == 0 && len(got) == 0) && !errors.Is(end, io.ErrUnexpectedEOF) {
				t.Fatalf("EOF after %d lines of %d", len(got), len(want)-1)
			}
		default:
			t.Fatalf("unexpected error %v", end)
		}
	})
}

// peek reports a message's type and the keys its type does not allow; it
// never panics, and no allowed key is ever reported or an unallowed one
// missed.
func FuzzPeek(f *testing.F) {
	addSeedLines(f)
	f.Fuzz(func(t *testing.T, line []byte) {
		for name, list := range map[string]map[string][]string{"to Den": allowed, "to serve": allowedIn} {
			typ, unknown, err := peekIn(line, list)
			var m map[string]json.RawMessage
			if json.Unmarshal(line, &m) != nil {
				if err == nil {
					t.Fatalf("%s: %q is not a JSON object but peeked as %q", name, line, typ)
				}
				continue
			}
			if err != nil {
				continue
			}
			ok := map[string]bool{}
			for _, k := range list[typ] {
				ok[k] = true
			}
			rep := map[string]bool{}
			for _, k := range unknown {
				rep[k] = true
				if ok[k] {
					t.Fatalf("%s: allowed key %q reported unknown for %q", name, k, typ)
				}
			}
			for k := range m {
				if !ok[k] && !rep[k] {
					t.Fatalf("%s: key %q of a %q message was not reported", name, k, typ)
				}
			}
			if list[typ] == nil && len(unknown) == 0 && len(m) > 0 {
				t.Fatalf("%s: unknown type %q reported nothing", name, typ)
			}
		}
	})
}

var hostPolicies = []struct{ configured, applied Policy }{
	{Observe, Observe}, {Approve, Approve}, {Full, Observe}, {Full, Full},
}

// A snapshot from a host: whatever it says, what Den keeps is a list of
// agents with UUID session ids, figures inside the bounds, and only clean
// strings; and checking it again changes nothing.
func FuzzSnapshot(f *testing.F) {
	addSeedLines(f)
	f.Fuzz(func(t *testing.T, line []byte) {
		var sm Snapshot
		if json.Unmarshal(line, &sm) != nil {
			return
		}
		for _, hp := range hostPolicies {
			h := Host{Label: "devbox", Policy: hp.configured, Labels: hp.configured == Full}
			agents, _, _ := checkAgents(sm.Agents, h, hp.applied)
			if len(agents) > len(sm.Agents) {
				t.Fatalf("%d agents out of %d", len(agents), len(sm.Agents))
			}
			for _, a := range agents {
				if !isSessionID(a.Session) {
					t.Fatalf("kept an agent with session %q", a.Session)
				}
				if a.Context < 0 || a.Context > maxContext || a.Window < 0 || a.Window > maxContext {
					t.Fatalf("context %d window %d out of bounds", a.Context, a.Window)
				}
				if len(a.Tokens) > maxModels || len(a.Sub) > maxModels {
					t.Fatalf("tokens for %d and %d models", len(a.Tokens), len(a.Sub))
				}
				for k := range a.Tokens {
					if !modelKey(k) {
						t.Fatalf("model key %q kept", k)
					}
				}
				if hp.configured != Full && a.Full != nil {
					t.Fatalf("full content kept for a host that is %s", hp.configured)
				}
			}
			// What reaches the model is the agent fleet.FromMeta builds, which
			// is also where the full content (title, folder, activity) is
			// sanitised; checkAgents leaves that to it.
			for _, a := range agents {
				plain := a
				plain.Full = nil
				checkClean(t, "checkAgents("+string(hp.configured)+"/"+string(hp.applied)+")", plain, false)
				checkClean(t, "FromMeta("+string(hp.configured)+"/"+string(hp.applied)+")", fleet.FromMeta(a, "devbox", time.Now(), cost.NewTable(nil)), true)
			}
			again, flags, over := checkAgents(agents, h, hp.applied)
			if len(flags) != 0 || over || !reflect.DeepEqual(again, agents) {
				t.Fatalf("checking twice changed the agents or flagged: %v %v", flags, over)
			}
		}
	})
}

// An event from a host is kept only as a known kind of version 1, its host
// is the connection's, and its strings are clean.
func FuzzEvent(f *testing.F) {
	addSeedLines(f)
	f.Fuzz(func(t *testing.T, line []byte) {
		var em Event
		if json.Unmarshal(line, &em) != nil {
			return
		}
		ev, ok := cleanEvent(em.Event, "devbox")
		if !ok {
			return
		}
		if ev.V != events.Version {
			t.Fatalf("version %d kept", ev.V)
		}
		known := false
		for _, k := range events.Kinds {
			known = known || k == ev.Kind
		}
		if !known {
			t.Fatalf("kind %q kept", ev.Kind)
		}
		if ev.Host != "devbox" {
			t.Fatalf("host %q: it must come from remotes.toml", ev.Host)
		}
		checkClean(t, "cleanEvent", ev, false)
	})
}

// A held request is let in only for an approving host, with an id, a UUID
// session and a tool name (ToolName); what it carries to the inbox is Den's own
// PermissionRequest of that session and tool, with a body that is clean, at
// most MaxLine, and hashed as received.
func FuzzHeld(f *testing.F) {
	addSeedLines(f)
	f.Fuzz(func(t *testing.T, line []byte) {
		var hm Held
		if json.Unmarshal(line, &hm) != nil {
			return
		}
		for _, hp := range hostPolicies {
			ctx, cancel := context.WithCancel(context.Background())
			var wrote []any
			var mu sync.Mutex
			hs := newHeldSet(ctx, "devbox", newAnsweredIDs(), func(v any) error {
				mu.Lock()
				wrote = append(wrote, v)
				mu.Unlock()
				return nil
			})
			r, why := hs.add(hm, hp.configured, hp.applied)
			if r == nil {
				cancel()
				continue
			}
			if hp.configured.rank() < Approve.rank() || hp.applied.rank() < Approve.rank() {
				t.Fatalf("a held request taken under %s/%s", hp.configured, hp.applied)
			}
			if why != "" || !isID(hm.ID) || !isSessionID(hm.Session) || !ToolName(hm.Tool) {
				t.Fatalf("taken: id %q session %q tool %q why %q", hm.ID, hm.Session, hm.Tool, why)
			}
			if len(hm.Body) > MaxLine {
				t.Fatalf("a body of %d bytes taken", len(hm.Body))
			}
			var env struct {
				Session string          `json:"session_id"`
				Event   string          `json:"hook_event_name"`
				Tool    string          `json:"tool_name"`
				Input   json.RawMessage `json:"tool_input"`
			}
			if err := json.Unmarshal(r.Payload, &env); err != nil {
				t.Fatalf("payload: %v", err)
			}
			if env.Session != hm.Session || env.Event != "PermissionRequest" || env.Tool != hm.Tool {
				t.Fatalf("envelope %+v from session %q tool %q", env, hm.Session, hm.Tool)
			}
			var body any
			json.Unmarshal(env.Input, &body)
			checkClean(t, "held body", body, true)
			// A decision goes back bound to the hash of the body as received.
			r.Reply(decisionAllow())
			cancel()
			mu.Lock()
			for _, w := range wrote {
				if d, ok := w.(Decision); ok && d.Hash != BodyHash(hm.Body) {
					t.Fatalf("decision hash %s, body hash %s", d.Hash, BodyHash(hm.Body))
				}
			}
			mu.Unlock()
		}
	})
}

// A result's diff is cut to the caps and cleaned before Den shows it.
func FuzzDiff(f *testing.F) {
	addSeedLines(f)
	f.Fuzz(func(t *testing.T, line []byte) {
		var r Result
		if json.Unmarshal(line, &r) != nil || r.Diff == nil {
			return
		}
		d := cleanDiff(r.Diff)
		if len(d.Files) > MaxDiffFiles {
			t.Fatalf("%d files", len(d.Files))
		}
		total := 0
		for _, f := range d.Files {
			n := 0
			for _, h := range f.Hunks {
				for _, l := range h.Lines {
					n++
					total += len(l)
					if len(l) > MaxDiffLine {
						t.Fatalf("a line of %d bytes", len(l))
					}
					if l == "" || !strings.ContainsRune(" +-", rune(l[0])) {
						t.Fatalf("a line that is not a diff line: %q", l)
					}
				}
				for _, i := range h.Mine {
					if i < 0 || i >= len(h.Lines) {
						t.Fatalf("owner index %d of %d lines", i, len(h.Lines))
					}
				}
			}
			if n > MaxDiffFileLines {
				t.Fatalf("%d lines in a file", n)
			}
			if f.Unsafe && n > 0 {
				t.Fatalf("content shown for an unsafe file name %q", f.Path)
			}
		}
		if total > MaxLine+MaxDiffLine {
			t.Fatalf("%d bytes of lines", total)
		}
		checkClean(t, "cleanDiff", d, true)
	})
}

// A fake host for the serve side: one live session with an inbox socket.
const fuzzSession = "11111111-2222-4333-8444-555555555555"

// Serve's reader of what Den sends after its request. A line is accepted
// only for a type of the closed set with known keys; only a send or close
// for the live session ever reaches a session's socket or process, and the
// text that is posted is the message's, within the cap.
func FuzzFromDen(f *testing.F) {
	addSeedLines(f)
	f.Fuzz(func(t *testing.T, line []byte) {
		for _, mode := range []struct{ allowed, full bool }{{false, false}, {true, false}, {true, true}} {
			var mu sync.Mutex
			var results []Result
			var posted [][2]string
			var signalled []int
			agents := []fleet.Agent{{Name: "A", Status: fleet.Idle}}
			agents[0].Session.SessionID, agents[0].Session.Alive, agents[0].Session.PID, agents[0].Session.Socket = fuzzSession, true, 4242, "/nonexistent/inbox.sock"
			a := &actions{
				allowed: mode.allowed, full: mode.full && mode.allowed,
				diffOf:   func(context.Context, fleet.Agent) (*Diff, error) { return nil, errors.New("no git here") },
				diffWait: 0, sessions: func() ([]fleet.Agent, error) { return agents, nil },
				post: func(sock, text string) error {
					mu.Lock()
					posted = append(posted, [2]string{sock, text})
					mu.Unlock()
					return nil
				},
				ops:  recordOps{signal: func(pid int) { mu.Lock(); signalled = append(signalled, pid); mu.Unlock() }},
				self: noSelf(),
				up: func(v any) error {
					mu.Lock()
					defer mu.Unlock()
					if r, ok := v.(Result); ok {
						results = append(results, r)
					}
					return nil
				},
				log: func(string) {},
				sem: make(chan struct{}, MaxActions), diffSem: make(chan struct{}, MaxDiffs),
			}
			ctx, cancel := context.WithCancel(context.Background())
			err := fromDenMsg(ctx, line, nil, a, func(string) {})
			a.wait()
			cancel()
			typ, unknown, perr := peekIn(line, allowedIn)
			if err == nil && (perr != nil || len(unknown) > 0 || allowedIn[typ] == nil) {
				t.Fatalf("accepted %.200q (type %q, unknown keys %v, %v)", line, typ, unknown, perr)
			}
			mu.Lock()
			for _, p := range posted {
				var c SendCmd
				if json.Unmarshal(line, &c) != nil || typ != TypeSend || c.Session != fuzzSession || p[1] != c.Text || len(c.Text) > MaxSendText || strings.TrimSpace(c.Text) == "" || !mode.allowed {
					t.Fatalf("posted %.60q to %q for the line %.200q (mode %+v)", p[1], p[0], line, mode)
				}
			}
			if len(signalled) > 0 && (typ != TypeClose || !mode.allowed) {
				t.Fatalf("signalled %v for %.200q (mode %+v)", signalled, line, mode)
			}
			for _, r := range results {
				if r.Type != TypeResult || r.ID == "" || len(r.ID) > 64 {
					t.Fatalf("result %+v for %.200q", r, line)
				}
				checkClean(t, "serve result", r, false)
				if r.Diff != nil && !(mode.full && mode.allowed) {
					t.Fatalf("a diff under %+v", mode)
				}
			}
			mu.Unlock()
		}
	})
}

// Messages that Den and serve build themselves survive the wire unchanged,
// carry no key the receiver's list does not know, and fit in a line.
func FuzzRoundTrip(f *testing.F) {
	f.Add("id", "11111111-2222-4333-8444-555555555555", "hello", "allow", int64(1), true)
	f.Add("", "", "é☃", "", int64(-5), false)
	f.Add("a\"b\\c", "s", "line\nbreak\ttab", "deny", int64(1<<40), true)
	f.Fuzz(func(t *testing.T, id, session, text, behavior string, n int64, flag bool) {
		for _, s := range []*string{&id, &session, &text, &behavior} {
			*s = strings.ToValidUTF8(*s, "�") // JSON only carries valid UTF-8
		}
		msgs := []struct {
			v    any
			into any
			list map[string][]string
		}{
			{Request{Type: TypeHello, Proto: int(n), Den: text, Policy: Policy(behavior), Labels: flag, Controller: id}, new(Request), nil},
			{Hello{Type: TypeHello, Proto: int(n), Den: text, Hostname: id, Policy: Policy(behavior), Labels: flag, Note: text}, new(Hello), allowed},
			{Error{Type: TypeError, Code: id, Message: text}, new(Error), allowed},
			{Held{Type: TypeHeld, ID: id, Session: session, Tool: behavior, Body: heldBodyOf(text)}, new(Held), allowed},
			{Gone{Type: TypeGone, ID: id}, new(Gone), allowed},
			{Result{Type: TypeResult, ID: id, OK: flag, Killed: !flag, Error: text}, new(Result), allowed},
			{Decision{Type: TypeDecision, ID: id, Hash: session, Behavior: behavior, Message: text, Answers: map[string]string{text: id}}, new(Decision), allowedIn},
			{Ping{Type: TypePing}, new(Ping), allowedIn},
			{SendCmd{Type: TypeSend, ID: id, Session: session, Text: text}, new(SendCmd), allowedIn},
			{CloseCmd{Type: TypeClose, ID: id, Session: session, Force: flag}, new(CloseCmd), allowedIn},
			{DiffCmd{Type: TypeDiff, ID: id, Session: session}, new(DiffCmd), allowedIn},
		}
		for _, m := range msgs {
			var buf bytes.Buffer
			if err := writeMsg(&buf, m.v); err != nil {
				if errors.Is(err, ErrLineTooLong) {
					continue
				}
				t.Fatalf("%T: %v", m.v, err)
			}
			line := buf.Bytes()
			if len(line) > MaxLine+1 || bytes.Count(line, []byte("\n")) != 1 || line[len(line)-1] != '\n' {
				t.Fatalf("%T: %d bytes, not one line", m.v, len(line))
			}
			line = line[:len(line)-1]
			if m.list != nil {
				typ, unknown, err := peekIn(line, m.list)
				if err != nil || len(unknown) > 0 || m.list[typ] == nil {
					t.Fatalf("%T: type %q unknown %v err %v for %s", m.v, typ, unknown, err, line)
				}
			}
			if err := json.Unmarshal(line, m.into); err != nil {
				t.Fatalf("%T: %v", m.v, err)
			}
			if got := reflect.ValueOf(m.into).Elem().Interface(); !reflect.DeepEqual(got, m.v) && !sameJSON(t, got, m.v) {
				t.Fatalf("%T did not round-trip:\n got %+v\nwant %+v", m.v, got, m.v)
			}
		}
	})
}

// sameJSON compares by encoding: a RawMessage body is re-encoded compactly.
func sameJSON(t *testing.T, a, b any) bool {
	t.Helper()
	x, e1 := json.Marshal(a)
	y, e2 := json.Marshal(b)
	return e1 == nil && e2 == nil && bytes.Equal(x, y)
}

// helpers for the serve-side fake

func decisionAllow() (d bridge.Decision) { d.Behavior = "allow"; return }

func noSelf() closer.Self {
	return closer.Self{PID: os.Getpid(), IsAncestor: func(int) bool { return false }}
}

type recordOps struct{ signal func(pid int) }

func (o recordOps) Signal(pid int, sig syscall.Signal) error { o.signal(pid); return nil }
func (o recordOps) Alive(pid int) bool                       { return false }
func (o recordOps) CloseWindow(string) error                 { return nil }

// heldBodyOf is a held body carrying text as its command, escaped as JSON.
func heldBodyOf(text string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"command": text})
	return b
}
