package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// The proxy stands between a desktop agent's Claude Code and hypruse: the
// agent's MCP config names `den desktop-mcp`, which starts hypruse as its
// own child and relays MCP (JSON-RPC, one message per line) both ways.
// Every tools/call goes to Den first; nothing is forwarded without Den's
// allow, and what is forwarded is the input Den returned. Den gone, an
// error, a cancelled call: nothing is forwarded. tools/list shows only the
// tools Den allows, plus Den's own; hypruse's own requests and
// notifications to the client are dropped.

// Answer is Den's reply to one call, as the proxy reads it.
type Answer struct {
	Allow   bool   `json:"-"`
	Message string `json:"-"` // why not, or a Den tool's result
	// Args is the input to forward (Den's rewrite of the agent's).
	Args json.RawMessage `json:"args,omitempty"`
	// Token lets the proxy forward the rest of an allowed call (the next
	// pieces of a typed text) after Den re-checks the target, without a
	// second card.
	Token string `json:"token,omitempty"`
	// Scope comes with an allowed desktop call: what the result may show.
	Scope *Scope `json:"scope,omitempty"`
	// Bounds comes with an allowed pointer call: its glide stays inside.
	Bounds *Rect `json:"bounds,omitempty"`
}

// Gate is Den, as the proxy asks it. token is "" for a new call; for the
// next piece of an allowed call it is the Answer's Token.
type Gate interface {
	Call(ctx context.Context, tool string, args json.RawMessage, token string) (Answer, error)
}

// message is one JSON-RPC message, either way.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

// Proxy relays between the client (Claude Code) and the child (hypruse).
type Proxy struct {
	Gate     Gate
	Client   io.Reader // what Claude Code sends
	ToClient io.Writer
	Child    io.Reader // what hypruse sends
	ToChild  io.Writer
	// Cursor is where the pointer is now; with it, an allowed pointer call
	// glides there (Glide) instead of jumping. nil: no glide.
	Cursor func() (x, y float64, ok bool)
	// Pointer is the pointer file for the overlay's outline ("" for none).
	Pointer string
	// Seed seeds the glides; 0 picks one.
	Seed uint64
	// Log, when set, is told every text the agent types, so hypruse's
	// stderr never carries it into the mission's log.
	Log *LogFilter
	// Task is a task mission's proxy: it also offers TaskTools (Den's gate
	// still decides each call).
	Task  bool
	shown bool // the outline is showing (the pointer file says so)
	rng   *rand.Rand

	outMu   sync.Mutex // one writer to the client at a time
	childMu sync.Mutex // one writer to the child at a time
	callMu  sync.Mutex // one tool call at a time: no interleaved input

	mu      sync.Mutex
	nextID  int
	pending map[string]chan message       // child request id -> its response
	cancels map[string]context.CancelFunc // client request id -> its call's cancel
	done    chan struct{}                 // closed when the child's output ends
}

// MaxMessage bounds one JSON-RPC line either way (screenshots inline).
const MaxMessage = 32 << 20

// Run relays until the client's input ends or the child's output does.
func (p *Proxy) Run(ctx context.Context) error {
	p.pending = map[string]chan message{}
	p.cancels = map[string]context.CancelFunc{}
	p.done = make(chan struct{})
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go p.readChild()
	// A task may never move the pointer (typing, launching): the
	// overlay's outline shows where the pointer is from the start.
	if p.Task && p.Cursor != nil {
		if x, y, ok := p.Cursor(); ok {
			p.writePointer(x, y, false)
		}
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	sc := bufio.NewScanner(p.Client)
	sc.Buffer(make([]byte, 64<<10), MaxMessage)
	for sc.Scan() {
		var msg message
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue // not JSON-RPC: nothing to answer
		}
		select {
		case <-p.done:
			return errors.New("hypruse ended")
		default:
		}
		switch {
		case msg.Method != "" && len(msg.ID) > 0:
			wg.Add(1)
			go func(msg message) {
				defer wg.Done()
				p.request(ctx, msg)
			}(msg)
		case msg.Method == "notifications/initialized":
			p.toChild(msg)
		case msg.Method == "notifications/cancelled":
			var c struct {
				RequestID json.RawMessage `json:"requestId"`
			}
			if json.Unmarshal(msg.Params, &c) == nil {
				p.mu.Lock()
				if f := p.cancels[string(c.RequestID)]; f != nil {
					f()
				}
				p.mu.Unlock()
			}
		}
		// Anything else from the client (responses to requests hypruse
		// never got to make, other notifications) goes nowhere.
	}
	return sc.Err()
}

// readChild routes hypruse's responses to the requests waiting for them.
// Its own requests are refused and its notifications dropped: nothing it
// says reaches the client except a result Den let through.
func (p *Proxy) readChild() {
	defer close(p.done)
	sc := bufio.NewScanner(p.Child)
	sc.Buffer(make([]byte, 64<<10), MaxMessage)
	for sc.Scan() {
		var msg message
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		switch {
		case msg.Method != "" && len(msg.ID) > 0:
			// Off this loop: a child that writes while it waits must not
			// stall the responses it is waiting for.
			go p.toChild(message{JSONRPC: "2.0", ID: msg.ID, Error: rpcError(-32601, "not offered")})
		case msg.Method == "" && len(msg.ID) > 0:
			p.mu.Lock()
			ch := p.pending[string(msg.ID)]
			delete(p.pending, string(msg.ID))
			p.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
		}
	}
	p.mu.Lock()
	for id, ch := range p.pending {
		close(ch)
		delete(p.pending, id)
	}
	p.mu.Unlock()
}

// request answers one request from the client.
func (p *Proxy) request(ctx context.Context, msg message) {
	switch msg.Method {
	case "initialize":
		res, err := p.roundTrip(ctx, "initialize", msg.Params)
		p.reply(msg.ID, res, err)
	case "prompts/list":
		// Offered as empty, so the client logs no error: still nothing.
		p.reply(msg.ID, json.RawMessage(`{"prompts":[]}`), nil)
	case "resources/list":
		p.reply(msg.ID, json.RawMessage(`{"resources":[]}`), nil)
	case "resources/templates/list":
		p.reply(msg.ID, json.RawMessage(`{"resourceTemplates":[]}`), nil)
	case "ping":
		p.reply(msg.ID, json.RawMessage(`{}`), nil)
	case "tools/list":
		res, err := p.roundTrip(ctx, "tools/list", msg.Params)
		if err == nil {
			res, err = listTools(res, p.Task)
		}
		p.reply(msg.ID, res, err)
	case "tools/call":
		ctx, cancel := context.WithCancel(ctx)
		p.mu.Lock()
		p.cancels[string(msg.ID)] = cancel
		p.mu.Unlock()
		res := p.call(ctx, msg.Params)
		p.mu.Lock()
		delete(p.cancels, string(msg.ID))
		p.mu.Unlock()
		cancel()
		p.reply(msg.ID, res, nil)
	default:
		p.send(message{JSONRPC: "2.0", ID: msg.ID, Error: rpcError(-32601, "method not offered by Den's desktop proxy")})
	}
}

// call handles one tools/call and returns the result to send back: always
// a result, an error result when nothing was done.
func (p *Proxy) call(ctx context.Context, params json.RawMessage) json.RawMessage {
	p.callMu.Lock()
	defer p.callMu.Unlock()
	var in struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(params, &in) != nil {
		return toolError("unreadable call")
	}
	if !offered(in.Name, p.Task) {
		return toolError(in.Name + " is not a tool here")
	}
	ans, err := p.Gate.Call(ctx, in.Name, in.Arguments, "")
	if err != nil {
		return toolError("Den did not allow this (" + err.Error() + "); nothing was done")
	}
	if !ans.Allow {
		return toolError(ans.Message)
	}
	if ctx.Err() != nil {
		return toolError("cancelled; nothing was done")
	}
	if in.Name == "request_approval" || in.Name == "your_turn" {
		return toolText(ans.Message)
	}
	var args map[string]any
	if json.Unmarshal(ans.Args, &args) != nil || args == nil {
		return toolError("Den's answer carried no input; nothing was done")
	}
	if in.Name == "keyboard" {
		if text, ok := args["text"].(string); ok {
			p.Log.Typed(text)
			for _, piece := range Chunks(text) {
				p.Log.Typed(piece.Text)
			}
		}
		p.hideOutline()
	}
	if in.Name == "keyboard" && args["action"] == "type" {
		return p.typeText(ctx, args, ans.Token)
	}
	if in.Name == "pointer" && ans.Bounds != nil {
		if err := p.glide(ctx, args, *ans.Bounds); err != nil {
			return toolError("stopped before acting: " + err.Error())
		}
	}
	res, err := p.callChild(ctx, in.Name, args)
	if err != nil {
		return toolError(err.Error())
	}
	if in.Name == "pointer" {
		if x, y, ok := pointArgs(args); ok {
			p.writePointer(x, y, false)
		}
	}
	if in.Name == "desktop" {
		return filterResult(res, ans.Scope)
	}
	return res
}

// typeText forwards an allowed text in pieces (Chunks), each one a call
// that names the window, so hypruse focuses and checks it again before
// typing; before every piece after the first, Den re-checks the target.
func (p *Proxy) typeText(ctx context.Context, args map[string]any, token string) json.RawMessage {
	text, _ := args["text"].(string)
	window, _ := args["window"].(string)
	pieces := Chunks(text)
	typed := 0
	for i, piece := range pieces {
		step := map[string]any{"action": "type", "text": piece.Text, "window": window, "then": "none"}
		if piece.Enter {
			step = map[string]any{"action": "key", "keys": "Return", "window": window, "then": "none"}
		}
		if i > 0 {
			raw, _ := json.Marshal(step)
			ans, err := p.Gate.Call(ctx, "keyboard", raw, token)
			if err != nil || !ans.Allow {
				why := ans.Message
				if err != nil {
					why = err.Error()
				}
				return toolError(fmt.Sprintf("stopped after %d of %d characters: %s", typed, len([]rune(text)), why))
			}
		}
		res, err := p.callChild(ctx, "keyboard", step)
		if err != nil {
			return toolError(fmt.Sprintf("stopped after %d of %d characters: %v", typed, len([]rune(text)), err))
		}
		if isError(res) {
			return res
		}
		if piece.Enter {
			typed++
		} else {
			typed += len([]rune(piece.Text))
		}
	}
	return toolText(fmt.Sprintf("typed %d characters into %s", typed, window))
}

// callChild sends one tools/call to hypruse and returns its result; a
// JSON-RPC error becomes an error.
func (p *Proxy) callChild(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	return p.roundTrip(ctx, "tools/call", params)
}

// roundTrip sends a request to the child under the proxy's own id and
// waits for its response.
func (p *Proxy) roundTrip(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	ch := make(chan message, 1)
	p.mu.Lock()
	p.nextID++
	id := json.RawMessage(strconv.Itoa(p.nextID))
	p.pending[string(id)] = ch
	p.mu.Unlock()
	select {
	case <-p.done:
		return nil, errors.New("hypruse is not running")
	default:
	}
	p.toChild(message{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	select {
	case res, ok := <-ch:
		if !ok {
			return nil, errors.New("hypruse ended")
		}
		if len(res.Error) > 0 {
			var e struct {
				Message string `json:"message"`
			}
			json.Unmarshal(res.Error, &e)
			return nil, errors.New("hypruse: " + e.Message)
		}
		return res.Result, nil
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.pending, string(id))
		p.mu.Unlock()
		return nil, errors.New("cancelled")
	case <-p.done:
		return nil, errors.New("hypruse ended")
	}
}

func (p *Proxy) toChild(msg message) {
	b, _ := json.Marshal(msg)
	p.childMu.Lock()
	defer p.childMu.Unlock()
	p.ToChild.Write(append(b, '\n'))
}

func (p *Proxy) send(msg message) {
	b, _ := json.Marshal(msg)
	p.outMu.Lock()
	defer p.outMu.Unlock()
	p.ToClient.Write(append(b, '\n'))
}

func (p *Proxy) reply(id json.RawMessage, res json.RawMessage, err error) {
	if err != nil {
		p.send(message{JSONRPC: "2.0", ID: id, Error: rpcError(-32603, err.Error())})
		return
	}
	p.send(message{JSONRPC: "2.0", ID: id, Result: res})
}

func offered(name string, task bool) bool {
	open, _ := tools(task)
	for _, t := range append(open, DenTools...) {
		if t == name {
			return true
		}
	}
	return false
}

// listTools keeps hypruse's tools that Den offers, in hypruse's own
// words, and adds Den's.
func listTools(res json.RawMessage, task bool) (json.RawMessage, error) {
	var list struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(res, &list); err != nil {
		return nil, fmt.Errorf("tools/list: %v", err)
	}
	var kept []map[string]any
	open, _ := tools(task)
	for _, t := range list.Tools {
		name, _ := t["name"].(string)
		for _, h := range open {
			if name == h {
				kept = append(kept, t)
			}
		}
	}
	kept = append(kept, denToolDefs()...)
	return json.Marshal(map[string]any{"tools": kept})
}

// denToolDefs are the two tools the proxy serves itself.
func denToolDefs() []map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	return []map[string]any{
		{
			"name":        "request_approval",
			"description": "Ask the human, in Den, to approve the next step before you take it: anything outward or hard to undo (sending, submitting, deleting, paying, signing in, saving over a file). Say plainly what you are about to do and why. Returns once they answer; a no is final for that step.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"intent": str("What you are about to do, in one or two sentences.")}, "required": []string{"intent"}},
		},
		{
			"name":        "your_turn",
			"description": "Hand a step of the plan to the human: signing in, entering a password or a code, anything with credentials. All desktop tools are paused until they finish; this returns when they say it is done, or with an error when they end the mission.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"step": str("The step the human does, as the plan names it.")}, "required": []string{"step"}},
		},
	}
}

// filterResult rebuilds a desktop result from the filtered snapshot; an
// unexpected result shows nothing.
func filterResult(res json.RawMessage, sc *Scope) json.RawMessage {
	if sc == nil {
		return toolError("Den sent no scope for the desktop; nothing is shown")
	}
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Structured json.RawMessage `json:"structuredContent"`
		IsError    bool            `json:"isError"`
	}
	if json.Unmarshal(res, &r) != nil || r.IsError {
		return toolError("desktop failed")
	}
	raw := []byte(nil)
	if len(r.Structured) > 0 {
		raw = r.Structured
		var wrapped struct {
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(raw, &wrapped) == nil && len(wrapped.Result) > 0 {
			raw = wrapped.Result
		}
	} else if len(r.Content) == 1 && r.Content[0].Type == "text" {
		raw = []byte(r.Content[0].Text)
	}
	out, err := FilterDesktop(raw, *sc)
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(string(out))
}

func toolText(text string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": false})
	return b
}

func toolError(text string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": true})
	return b
}

func isError(res json.RawMessage) bool {
	var r struct {
		IsError bool `json:"isError"`
	}
	return json.Unmarshal(res, &r) == nil && r.IsError
}

func rpcError(code int, msg string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"code": code, "message": msg})
	return b
}

// glide moves the pointer to the call's point along a human-like path,
// every step a hypruse pointer move inside bounds, before the call itself.
// The steps carry no authority of their own: they lead only to the point
// Den allowed, stay in the window (or monitor) Den named, go through
// hypruse's guards, and stop at the first refusal or a cancel.
func (p *Proxy) glide(ctx context.Context, args map[string]any, bounds Rect) error {
	if p.Cursor == nil {
		return nil
	}
	x, y, ok := pointArgs(args)
	if !ok {
		return nil
	}
	cx, cy, ok := p.Cursor()
	if !ok {
		return nil
	}
	if p.rng == nil {
		seed := p.Seed
		if seed == 0 {
			seed = uint64(time.Now().UnixNano())
		}
		p.rng = rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	}
	samples := Glide([2]float64{cx, cy}, [2]float64{x, y}, bounds, p.rng, 60)
	began := time.Now()
	// The last sample is the point itself: the approved call moves there,
	// exactly, before it clicks. A step whose time has already passed (a
	// hypruse move costs about 30 ms, more than a 60 Hz step) is skipped,
	// so the glide keeps its planned duration.
	steps := samples[:len(samples)-1]
	for i, s := range steps {
		if err := ctx.Err(); err != nil {
			return errors.New("cancelled")
		}
		if i+1 < len(steps) && time.Since(began) > time.Duration(steps[i+1].T*float64(time.Second)) {
			continue
		}
		res, err := p.callChild(ctx, "pointer", map[string]any{"action": "move", "x": s.X, "y": s.Y, "then": "none"})
		if err != nil {
			return err
		}
		if isError(res) {
			return errors.New("hypruse refused a step")
		}
		p.writePointer(s.X, s.Y, false)
		if wait := time.Duration(s.T*float64(time.Second)) - time.Since(began); wait > 0 {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return errors.New("cancelled")
			}
		}
	}
	return nil
}

// pointArgs is where a pointer call acts first: x, y.
func pointArgs(args map[string]any) (float64, float64, bool) {
	x, okx := args["x"].(float64)
	y, oky := args["y"].(float64)
	return x, y, okx && oky
}

// writePointer writes the overlay's pointer file: "x y", or "x y hidden"
// while typing hides the real pointer. 0600, replaced atomically.
func (p *Proxy) writePointer(x, y float64, hidden bool) {
	if p.Pointer == "" {
		return
	}
	line := fmt.Sprintf("%.0f %.0f", x, y)
	if hidden {
		line += " hidden"
	}
	tmp := p.Pointer + ".tmp"
	if os.WriteFile(tmp, []byte(line+"\n"), 0o600) == nil && os.Rename(tmp, p.Pointer) == nil {
		p.shown = !hidden
	}
}

// hideOutline tells the overlay to fade the outline out before keys are
// pressed (Hyprland hides the real pointer on a key press), and waits for
// the fade.
func (p *Proxy) hideOutline() {
	if p.Pointer == "" || !p.shown {
		return
	}
	raw, err := os.ReadFile(p.Pointer)
	if err != nil {
		return
	}
	var x, y float64
	if _, err := fmt.Sscanf(string(raw), "%f %f", &x, &y); err != nil {
		return
	}
	p.writePointer(x, y, true)
	time.Sleep(hideFade)
}

// hideFade is how long the outline takes to fade; tests shorten it.
var hideFade = 550 * time.Millisecond

// PointerPath is the overlay's pointer file.
func PointerPath() string {
	return filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "den-agent-pointer")
}
