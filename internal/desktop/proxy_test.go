package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHypruse answers MCP like hypruse would, records every tools/call it
// gets, and on start sends the client a request and a notification that
// must never get through.
type fakeHypruse struct {
	mu     sync.Mutex
	calls  []map[string]any
	refuse string        // an action it refuses, as hypruse's guards would
	slow   time.Duration // how long each move takes, as hyprctl's exec does
}

func (f *fakeHypruse) serve(in io.Reader, out io.Writer) {
	w := func(v any) {
		b, _ := json.Marshal(v)
		out.Write(append(b, '\n'))
	}
	w(map[string]any{"jsonrpc": "2.0", "id": "srv-1", "method": "sampling/createMessage", "params": map[string]any{}})
	w(map[string]any{"jsonrpc": "2.0", "method": "notifications/message", "params": map[string]any{"data": "secret log"}})
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var m message
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.Method == "" || len(m.ID) == 0 {
			continue
		}
		var result any
		switch m.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]any{"name": "hypruse"}}
		case "tools/list":
			var tools []map[string]any
			for _, n := range append(append([]string(nil), HypruseTools...), ClosedTools...) {
				tools = append(tools, map[string]any{"name": n, "description": "hypruse " + n})
			}
			result = map[string]any{"tools": tools}
		case "tools/call":
			var c struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			json.Unmarshal(m.Params, &c)
			f.mu.Lock()
			f.calls = append(f.calls, map[string]any{"name": c.Name, "args": c.Arguments})
			f.mu.Unlock()
			text := "ok " + c.Name
			f.mu.Lock()
			refused := f.refuse != "" && c.Arguments["action"] == f.refuse
			slow := f.slow
			f.mu.Unlock()
			if slow > 0 && c.Arguments["action"] == "move" {
				time.Sleep(slow)
			}
			if refused {
				w(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": "refused"}}, "isError": true}})
				continue
			}
			if c.Name == "desktop" {
				text = `{"monitors":[{"name":"DP-1"},{"name":"DP-2"}],"workspaces":[{"id":9}],"windows":[{"address":"0xe","title":"note"},{"address":"0xh","title":"secret"}],"active_window":"0xh","cursor":[5,5]}`
			}
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": false}
		default:
			w(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "no"}})
			continue
		}
		w(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
	}
}

func (f *fakeHypruse) got() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.calls...)
}

// fakeGate answers with fn and records what it was asked.
type fakeGate struct {
	mu    sync.Mutex
	asked []string
	done  []string // the calls reported done, in order
	fn    func(tool string, args json.RawMessage, token string) (Answer, error)
}

func (g *fakeGate) Call(ctx context.Context, tool string, args json.RawMessage, token string) (Answer, error) {
	g.mu.Lock()
	g.asked = append(g.asked, tool+" "+token)
	g.mu.Unlock()
	return g.fn(tool, args, token)
}

func (g *fakeGate) Done(call string) {
	g.mu.Lock()
	g.done = append(g.done, call)
	g.mu.Unlock()
}

func (g *fakeGate) dones() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.done...)
}

// rig runs a proxy against the fake and returns a send-and-wait func.
func rig(t *testing.T, gate Gate) (func(method string, params any) message, *fakeHypruse, func(string) bool) {
	t.Helper()
	send, f, leaked := rigProxy(t, gate, nil)
	return send, f, leaked
}

// rigProxy is rig with the proxy configured by setup before it runs.
func rigProxy(t *testing.T, gate Gate, setup func(*Proxy)) (func(method string, params any) message, *fakeHypruse, func(string) bool) {
	t.Helper()
	cr, cw := io.Pipe()   // client -> proxy
	pr, pw := io.Pipe()   // proxy -> client
	chr, chw := io.Pipe() // proxy -> child
	hr, hw := io.Pipe()   // child -> proxy
	f := &fakeHypruse{}
	go f.serve(chr, hw)
	p := &Proxy{Gate: gate, Client: cr, ToClient: pw, Child: hr, ToChild: chw}
	if setup != nil {
		setup(p)
	}
	go p.Run(context.Background())
	t.Cleanup(func() { cw.Close(); hw.Close(); chw.Close() })
	replies := make(chan message, 16)
	var seen []string
	var seenMu sync.Mutex
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for sc.Scan() {
			seenMu.Lock()
			seen = append(seen, sc.Text())
			seenMu.Unlock()
			var m message
			json.Unmarshal(sc.Bytes(), &m)
			replies <- m
		}
	}()
	id := 0
	send := func(method string, params any) message {
		id++
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		cw.Write(append(b, '\n'))
		select {
		case m := <-replies:
			return m
		case <-time.After(5 * time.Second):
			t.Fatalf("no reply to %s", method)
		}
		return message{}
	}
	leaked := func(s string) bool {
		seenMu.Lock()
		defer seenMu.Unlock()
		for _, l := range seen {
			if strings.Contains(l, s) {
				return true
			}
		}
		return false
	}
	return send, f, leaked
}

func resultText(t *testing.T, m message) (string, bool) {
	t.Helper()
	var r struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(m.Result, &r); err != nil || len(r.Content) == 0 {
		t.Fatalf("not a tool result: %s %s", m.Result, m.Error)
	}
	return r.Content[0].Text, r.IsError
}

// The policy as Den would run it, on the fixture desktop.
func policyGate() *fakeGate {
	return &fakeGate{fn: func(tool string, args json.RawMessage, token string) (Answer, error) {
		m, s := fixture()
		v := Decide(tool, args, m, s)
		if v.Kind != Allow {
			return Answer{Message: "Den: " + v.Kind.String() + ": " + v.Reason}, nil
		}
		a := Answer{Allow: true, Token: "t1"}
		a.Args, _ = json.Marshal(v.Args)
		if tool == "desktop" {
			sc := ScopeOf(m, s)
			a.Scope = &sc
		}
		return a, nil
	}}
}

func TestProxyListsOnlyAllowedTools(t *testing.T) {
	send, _, leaked := rig(t, policyGate())
	send("initialize", map[string]any{})
	m := send("tools/list", map[string]any{})
	var list struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	json.Unmarshal(m.Result, &list)
	var names []string
	for _, tl := range list.Tools {
		names = append(names, tl.Name)
	}
	got := strings.Join(names, ",")
	want := strings.Join(append(append([]string(nil), HypruseTools...), DenTools...), ",")
	if got != want {
		t.Fatalf("tools %s, want %s", got, want)
	}
	if leaked("sampling/createMessage") || leaked("secret log") {
		t.Fatal("hypruse's own request or notification reached the client")
	}
}

func TestProxyForwardsOnlyWhatDenAllows(t *testing.T) {
	send, f, _ := rig(t, policyGate())
	send("initialize", map[string]any{})
	// Denied: nothing reaches hypruse.
	text, isErr := resultText(t, send("tools/call", map[string]any{"name": "keyboard", "arguments": map[string]any{"action": "type", "text": "rm -rf ~\n", "window": "0xh"}}))
	if !isErr || !strings.Contains(text, "not a mission window") {
		t.Fatalf("deny came back as %q (error %v)", text, isErr)
	}
	// Closed and unknown tools never reach Den or hypruse.
	text, isErr = resultText(t, send("tools/call", map[string]any{"name": "launch", "arguments": map[string]any{"command": "ghostty"}}))
	if !isErr {
		t.Fatalf("launch: %q", text)
	}
	// Ask is not allow: the fake gate answers it like a deny.
	_, isErr = resultText(t, send("tools/call", map[string]any{"name": "pointer", "arguments": map[string]any{"action": "click", "x": 100, "y": 100}}))
	if !isErr {
		t.Fatal("an unanswered ask was forwarded")
	}
	if n := len(f.got()); n != 0 {
		t.Fatalf("hypruse got %d calls: %v", n, f.got())
	}
	// Allowed: forwarded with Den's rewrite.
	text, isErr = resultText(t, send("tools/call", map[string]any{"name": "pointer", "arguments": map[string]any{"action": "click", "x": 1000, "y": 100, "then": "screenshot"}}))
	if isErr || text != "ok pointer" {
		t.Fatalf("allowed click: %q", text)
	}
	calls := f.got()
	if len(calls) != 1 || calls[0]["args"].(map[string]any)["then"] != "none" {
		t.Fatalf("forwarded %v", calls)
	}
}

func TestProxyFiltersDesktop(t *testing.T) {
	send, _, _ := rig(t, policyGate())
	send("initialize", map[string]any{})
	text, isErr := resultText(t, send("tools/call", map[string]any{"name": "desktop", "arguments": map[string]any{}}))
	if isErr || strings.Contains(text, "secret") || strings.Contains(text, "DP-2") || !strings.Contains(text, "note") {
		t.Fatalf("desktop result %q", text)
	}
}

func TestProxyTypesInCheckedPieces(t *testing.T) {
	g := policyGate()
	send, f, _ := rig(t, g)
	send("initialize", map[string]any{})
	long := strings.Repeat("x", 40) + "\nend"
	text, isErr := resultText(t, send("tools/call", map[string]any{"name": "keyboard", "arguments": map[string]any{"action": "type", "text": long}}))
	if isErr {
		t.Fatalf("typing: %q", text)
	}
	calls := f.got()
	if len(calls) != 4 {
		t.Fatalf("want 4 pieces (32, 8, Return, 3), got %d: %v", len(calls), calls)
	}
	for _, c := range calls {
		if c["args"].(map[string]any)["window"] != "0xe" {
			t.Fatalf("a piece without its window: %v", c)
		}
	}
	if calls[2]["args"].(map[string]any)["keys"] != "Return" {
		t.Fatalf("line break as %v", calls[2])
	}
	g.mu.Lock()
	asked := strings.Join(g.asked, "|")
	g.mu.Unlock()
	if !strings.HasPrefix(asked, "keyboard |keyboard t1|keyboard t1|keyboard t1") {
		t.Fatalf("Den was asked %q", asked)
	}
}

// Every call Den allows with a Call id is reported done once hypruse has
// answered it: a typed text piece by piece, each piece its own call; and
// launch is Den's own, answered with Den's result and never forwarded.
func TestProxyReportsCallsDone(t *testing.T) {
	n := 0
	g := &fakeGate{}
	g.fn = func(tool string, args json.RawMessage, token string) (Answer, error) {
		n++
		id := "c" + strconv.Itoa(n)
		switch tool {
		case "launch":
			return Answer{Allow: true, Message: `{"address":"0xnew","class":"foot"}`}, nil
		case "keyboard":
			if token == "" {
				return Answer{Allow: true, Args: json.RawMessage(`{"action":"type","text":"` + strings.Repeat("x", 40) + `","window":"0xe"}`), Token: "t1", Call: id}, nil
			}
			return Answer{Allow: true, Args: args, Call: id}, nil
		}
		return Answer{Allow: true, Args: args, Call: id}, nil
	}
	send, f, _ := rigProxy(t, g, func(p *Proxy) { p.Task = true })
	send("initialize", map[string]any{})
	if text, isErr := resultText(t, send("tools/call", map[string]any{"name": "pointer", "arguments": map[string]any{"action": "click", "x": 10, "y": 10}})); isErr {
		t.Fatalf("click: %q", text)
	}
	if d := g.dones(); len(d) != 1 || d[0] != "c1" {
		t.Fatalf("after a click, done %v", d)
	}
	if text, isErr := resultText(t, send("tools/call", map[string]any{"name": "keyboard", "arguments": map[string]any{"action": "type", "text": "x"}})); isErr {
		t.Fatalf("typing: %q", text)
	}
	// 40 characters: two pieces (32, 8), the first on the call's own
	// answer (c2), the second on its own (c3).
	if d := strings.Join(g.dones(), " "); d != "c1 c2 c3" {
		t.Fatalf("after typing, done %q", d)
	}
	before := len(f.got())
	text, isErr := resultText(t, send("tools/call", map[string]any{"name": "launch", "arguments": map[string]any{"command": "foot"}}))
	if isErr || !strings.Contains(text, "0xnew") || len(f.got()) != before {
		t.Fatalf("launch: %q (error %v), hypruse got %d more", text, isErr, len(f.got())-before)
	}
	if d := strings.Join(g.dones(), " "); d != "c1 c2 c3" {
		t.Fatalf("launch reported done: %q", d)
	}
}

// A cancelled call is reported done only once hypruse has answered it:
// until then hypruse may still be acting (sec's S2-2).
func TestProxyDoneWaitsForACancelledCall(t *testing.T) {
	g := &fakeGate{fn: func(tool string, args json.RawMessage, _ string) (Answer, error) {
		return Answer{Allow: true, Args: args, Call: "c1"}, nil
	}}
	cr, cw := io.Pipe()
	pr, pw := io.Pipe()
	chr, chw := io.Pipe()
	hr, hw := io.Pipe()
	f := &fakeHypruse{slow: 500 * time.Millisecond}
	go f.serve(chr, hw)
	p := &Proxy{Gate: g, Client: cr, ToClient: pw, Child: hr, ToChild: chw}
	go p.Run(context.Background())
	t.Cleanup(func() { cw.Close(); hw.Close(); chw.Close() })
	replies := make(chan message, 16)
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			var m message
			json.Unmarshal(sc.Bytes(), &m)
			replies <- m
		}
	}()
	write := func(v any) {
		b, _ := json.Marshal(v)
		cw.Write(append(b, '\n'))
	}
	write(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	<-replies
	write(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "pointer", "arguments": map[string]any{"action": "move", "x": 1, "y": 1}}})
	time.Sleep(100 * time.Millisecond) // hypruse has it, and is slow
	write(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": 2}})
	select {
	case <-replies:
	case <-time.After(5 * time.Second):
		t.Fatal("no reply to the cancelled call")
	}
	if d := g.dones(); len(d) != 0 {
		t.Fatalf("done %v before hypruse answered", d)
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(g.dones()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if d := g.dones(); len(d) != 1 || d[0] != "c1" {
		t.Errorf("after hypruse answered: done %v", d)
	}
}

// A piece Den refuses stops the text there.
func TestProxyStopsTypingWhenDenRefuses(t *testing.T) {
	g := policyGate()
	inner := g.fn
	g.fn = func(tool string, args json.RawMessage, token string) (Answer, error) {
		if token != "" {
			return Answer{Message: "the window lost focus"}, nil
		}
		return inner(tool, args, token)
	}
	send, f, _ := rig(t, g)
	send("initialize", map[string]any{})
	text, isErr := resultText(t, send("tools/call", map[string]any{"name": "keyboard", "arguments": map[string]any{"action": "type", "text": strings.Repeat("y", 50)}}))
	if !isErr || !strings.Contains(text, "stopped after 32 of 50") {
		t.Fatalf("got %q", text)
	}
	if len(f.got()) != 1 {
		t.Fatalf("pieces after the refusal: %v", f.got())
	}
}

// Den gone (an error from the gate) forwards nothing.
func TestProxyFailsClosed(t *testing.T) {
	g := &fakeGate{fn: func(string, json.RawMessage, string) (Answer, error) { return Answer{}, io.ErrUnexpectedEOF }}
	send, f, _ := rig(t, g)
	send("initialize", map[string]any{})
	_, isErr := resultText(t, send("tools/call", map[string]any{"name": "desktop", "arguments": map[string]any{}}))
	if !isErr || len(f.got()) != 0 {
		t.Fatal("a call went through without Den")
	}
	g.fn = func(string, json.RawMessage, string) (Answer, error) { return Answer{Allow: true}, nil }
	_, isErr = resultText(t, send("tools/call", map[string]any{"name": "pointer", "arguments": map[string]any{"action": "move", "x": 1, "y": 1}}))
	if !isErr || len(f.got()) != 0 {
		t.Fatal("an allow with no input was forwarded")
	}
	// Listing prompts and resources gets empty lists (no client error,
	// still nothing); anything else unoffered is an error.
	for method, key := range map[string]string{"prompts/list": "prompts", "resources/list": "resources", "resources/templates/list": "resourceTemplates"} {
		m := send(method, map[string]any{})
		var r map[string][]any
		if len(m.Error) != 0 || json.Unmarshal(m.Result, &r) != nil || r[key] == nil || len(r[key]) != 0 {
			t.Fatalf("%s: %s %s", method, m.Result, m.Error)
		}
	}
	if m := send("resources/read", map[string]any{"uri": "file:///etc/passwd"}); len(m.Error) == 0 {
		t.Fatal("an unoffered method was answered")
	}
	if m := send("prompts/get", map[string]any{"name": "x"}); len(m.Error) == 0 {
		t.Fatal("prompts/get was answered")
	}
}
