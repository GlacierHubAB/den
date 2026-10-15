package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Connection limits.
const (
	maxConns       = 16
	maxConnsPerPID = 6
	helloDeadline  = 5 * time.Second
	idleDeadline   = 10 * time.Minute
	writeDeadline  = 5 * time.Second
	outQueue       = 64
)

// Options configure the server.
type Options struct {
	Path    string // den-api.sock; SocketPath() by default
	Version string // Den's release, for hello
	ChatLog string // drey-chat.jsonl
	Lane    LaneConfig
}

// Client is a connected app, as /api and the chip show it.
type Client struct {
	PID     int
	Exe     string // the path Den read from /proc at accept
	Label   string // its base name
	Since   time.Time
	Streams []string
	Asking  bool
	Agent   bool // its ancestry runs through a Den agent: refused
}

// Server serves den-api.sock.
type Server struct {
	opts  Options
	bp    atomic.Pointer[bound] // the socket Den bound; Rebind replaces it
	view  atomic.Pointer[View]
	chat  *chatStore
	lane  *lane
	notes chan string

	paused atomic.Bool // /api off or /api disconnect: answer unavailable

	grants   *grants
	acts     chan Act
	ctl      *control
	lastConn map[string]time.Time // exe -> when its last connection closed

	focusLimit focusLimit
	rebound    bool // Rebind ran since the last /api resume

	mu        sync.Mutex
	conns     map[*conn]bool
	lastFleet []byte
	seenExe   map[string]bool
	refusals  map[string]*refusal // by exe and reason
}

// Listen binds den-api.sock and starts serving it.
func Listen(o Options) (*Server, error) {
	if o.Path == "" {
		o.Path = SocketPath()
	}
	if o.Path == "" {
		return nil, errors.New("den-api.sock needs XDG_RUNTIME_DIR")
	}
	b, err := bind(o.Path)
	if err != nil {
		return nil, err
	}
	s := &Server{opts: o, chat: loadChat(o.ChatLog), notes: make(chan string, 16), conns: map[*conn]bool{}, seenExe: map[string]bool{}, refusals: map[string]*refusal{}}
	s.grants, s.acts, s.lastConn, s.ctl = newGrants(), make(chan Act, 64), map[string]time.Time{}, newControl()
	s.bp.Store(b)
	s.lane = newLane(o.Lane, s.chat, s.view.Load, s.note)
	s.lane.changed = s.asksChanged
	go s.accept(b)
	go s.expireLoop()
	return s, nil
}

// Acts are what only the UI can do: put a grant card in the inbox, settle
// expired ones.
func (s *Server) Acts() <-chan Act { return s.acts }

// GrantAnswered is the human's ^Y (granted) or ^N (denied) on a grant
// card. The answer goes to the app that asked, never to another: to its
// subscribed connections, or kept until it subscribes again.
func (s *Server) GrantAnswered(id, answer string) {
	ev, gr, ok := s.grants.answer(id, answer)
	if !ok {
		return
	}
	s.grantCard(gr, "answered")
	s.deliverGrant(gr.Exe, ev)
}

// grantCard tells the asking app's inbox subscribers that its card came
// or went; no other app hears of it.
func (s *Server) grantCard(gr *Grant, change string) {
	s.mu.Lock()
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		if c.peer.Exe == gr.Exe && c.subscribed(EventInbox) {
			c.push(Event{Event: EventInbox, Data: InboxChange{Change: change, Item: gr.item(gr.At)}}, true)
		}
	}
}

func (s *Server) deliverGrant(exe string, ev GrantAnswer) {
	delivered := false
	s.mu.Lock()
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		if c.peer.Exe == exe && c.subscribed(EventGrant) {
			delivered = c.push(Event{Event: EventGrant, Data: ev}, true) || delivered
		}
	}
	if !delivered {
		s.grants.retain(exe, ev)
	}
}

// connected reports whether an exe has a connection now, or had one in the
// last grantGrace: an app that re-dials keeps its pending grants.
func (s *Server) connected(exe string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		if c.peer.Exe == exe {
			return true
		}
	}
	t, ok := s.lastConn[exe]
	return ok && time.Since(t) < grantGrace
}

func (s *Server) expireLoop() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.lane.stop:
			return
		case <-t.C:
			s.expire(s.connected)
		}
	}
}

// expire ends grants whose app is gone, and tells the UI.
func (s *Server) expire(keep func(string) bool) {
	evs, gone := s.grants.expire(keep)
	if len(evs) == 0 {
		return
	}
	for _, gr := range gone {
		s.grantCard(gr, "expired")
	}
	var ids []string
	for _, ev := range evs {
		ids = append(ids, ev.ID)
	}
	select {
	case s.acts <- Act{Expired: ids}:
	default:
	}
}

// LaneState is the read-only lane's state and the asks waiting.
func (s *Server) LaneState() LaneState { return s.lane.State() }

// LanePID is the read-only lane's pid, 0 when it is not running. Den
// keeps it out of the fleet and refuses clients under it.
func (s *Server) LanePID() int { return int(s.lane.pid.Load()) }

// Path is the socket's path.
func (s *Server) Path() string { return s.bp.Load().path }

// Notes are lines Den shows in its chat: a first connection, a refused
// agent, a lane that misbehaved.
func (s *Server) Notes() <-chan string { return s.notes }

func (s *Server) note(text string) {
	select {
	case s.notes <- text:
	default:
	}
}

// refusedAgent is why a process under one of Den's agents is refused.
const refusedAgent = "an agent's process"

// refusalEvery is how often the chat hears again about refusals of one
// program for one reason: a test run that retries under new pids gives one
// note, then a count at most this often. /api has every count.
const refusalEvery = time.Minute

// refusal counts the refused connections of one program for one reason.
type refusal struct {
	Refusal
	unsaid int       // refused since the last note
	said   time.Time // when the last note went up
}

// Refusal is what /api lists about refused connections: of one program
// (the executable Den read), for one reason.
type Refusal struct {
	Exe    string
	Reason string
	Count  int
	First  time.Time
	Last   time.Time
}

// refuse counts a refused connection and returns the chat note it earns:
// the first of its program and reason, with detail (what it matched), then
// at most one a refusalEvery saying how many more; "" for none.
func (s *Server) refuse(p Peer, reason, detail string, now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := p.Exe + "\x00" + reason
	r := s.refusals[key]
	exe := sanitize.Label(p.Label(), 40)
	if r == nil {
		s.refusals[key] = &refusal{Refusal: Refusal{Exe: p.Exe, Reason: reason, Count: 1, First: now, Last: now}, said: now}
		text := reason + " (" + exe + ", pid " + itoa(p.PID) + ") tried den-api.sock: refused"
		if detail != "" {
			text += "; " + detail
		}
		return text + ". /api counts any more."
	}
	r.Count++
	r.Last = now
	r.unsaid++
	if now.Sub(r.said) < refusalEvery {
		return ""
	}
	text := fmt.Sprintf("%d more refused from %s (%s) since %s", r.unsaid, exe, reason, r.said.Format("15:04"))
	r.unsaid, r.said = 0, now
	return text
}

// Refusals lists the refused connections by program and reason, the most
// recent first.
func (s *Server) Refusals() []Refusal {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Refusal, 0, len(s.refusals))
	for _, r := range s.refusals {
		out = append(out, r.Refusal)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	return out
}

// Healthy reports whether the path still holds Den's socket.
func (s *Server) Healthy() bool { return s.bp.Load().ours() }

// Close stops the server: connections, the lane, and the path if it is
// still Den's.
func (s *Server) Close() {
	s.lane.close()
	s.bp.Load().close()
	s.mu.Lock()
	for c := range s.conns {
		c.close()
	}
	s.mu.Unlock()
}

// Pause closes every connection and answers new ones unavailable until
// Resume; the path stays bound, so nothing else can take it.
func (s *Server) Pause() {
	s.paused.Store(true)
	s.expire(func(string) bool { return false })
	s.mu.Lock()
	for c := range s.conns {
		c.close()
	}
	s.mu.Unlock()
}

// Resume accepts clients again and lets a lane that kept failing to start
// try again.
func (s *Server) Resume() {
	s.paused.Store(false)
	s.lane.resume()
	s.mu.Lock()
	s.rebound = false
	s.mu.Unlock()
}

// Rebind puts Den's socket back at the path once, when something replaced
// or removed it; a second replacement is only reported (Healthy) until
// /api resume.
func (s *Server) Rebind() error {
	s.mu.Lock()
	if s.rebound {
		s.mu.Unlock()
		return errors.New("den-api.sock was replaced again")
	}
	s.rebound = true
	s.mu.Unlock()
	if s.bp.Load().ours() {
		return nil
	}
	nb, err := bind(s.bp.Load().path)
	if err != nil {
		return err
	}
	old := s.bp.Swap(nb)
	old.ln.Close()
	go s.accept(nb)
	return nil
}

// Paused reports whether clients are refused.
func (s *Server) Paused() bool { return s.paused.Load() }

// Clients lists the connected apps.
func (s *Server) Clients() []Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Client
	for c := range s.conns {
		c.mu.Lock()
		cl := Client{PID: c.peer.PID, Exe: c.peer.Exe, Label: c.peer.Label(), Since: c.since, Asking: c.asks > 0, Agent: c.agent}
		for k := range c.subs {
			cl.Streams = append(cl.Streams, k)
		}
		c.mu.Unlock()
		sort.Strings(cl.Streams)
		out = append(out, cl)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}

// Publish hands the server a new view, from the UI after a refresh, and
// pushes fleet and inbox events to subscribers. It never blocks on a
// client.
func (s *Server) Publish(v *View) {
	old := s.view.Swap(v)
	fleetRaw, _ := json.Marshal(v.Fleet)
	s.mu.Lock()
	fleetChanged := !bytes.Equal(fleetRaw, s.lastFleet)
	s.lastFleet = fleetRaw
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	var changes []InboxChange
	if old != nil {
		was := map[string]Item{}
		for _, it := range old.Items {
			was[it.ID] = it
		}
		now := map[string]bool{}
		for _, it := range v.Items {
			now[it.ID] = true
			if _, ok := was[it.ID]; !ok {
				changes = append(changes, InboxChange{Change: "added", Item: it})
			}
		}
		for _, it := range old.Items {
			if !now[it.ID] {
				changes = append(changes, InboxChange{Change: "answered", Item: it})
			}
		}
	}
	for _, c := range conns {
		if fleetChanged && c.subscribed(EventFleet) {
			c.pushFleet(s.fleetFor(c))
		}
		if c.subscribed(EventInbox) {
			for _, ch := range changes {
				c.push(Event{Event: EventInbox, Data: ch}, true)
			}
		}
	}
}

// asksChanged pushes the fleet to its subscribers when an ask's status
// moves, so fleet.asks stays current.
func (s *Server) asksChanged() {
	s.mu.Lock()
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		if c.subscribed(EventFleet) {
			c.pushFleet(s.fleetFor(c))
		}
	}
}

// fleetFor is the fleet a connection sees: the asks in its scope with
// their ask and session ids, every other ask without.
func (s *Server) fleetFor(c *conn) Fleet {
	var f Fleet
	if v := s.view.Load(); v != nil {
		f = v.Fleet
	} else {
		f = Fleet{Taken: time.Now().UTC(), Agents: []Agent{}, Windows: []Window{}, Missions: []MissionRef{}, Cost: Cost{Currency: "USD"}}
	}
	c.mu.Lock()
	scope := c.scope
	c.mu.Unlock()
	f.Asks = s.lane.Asks(scope)
	return f
}

func (s *Server) accept(b *bound) {
	for {
		nc, err := b.ln.Accept()
		if err != nil {
			return
		}
		go s.serve(nc)
	}
}

// conn is one client connection.
type conn struct {
	s     *Server
	nc    net.Conn
	peer  Peer
	since time.Time
	out   chan []byte
	done  chan struct{}
	once  sync.Once

	mu    sync.Mutex
	hello bool
	agent bool
	subs  map[string]bool
	asks  int // asks of this connection not yet done
	// scope is whose asks this connection's are: its own, or those of the
	// session it named in hello.
	scope string
	fleet []byte // a fleet event waiting to be written, latest wins
	// cid names the connection in pair.auth's proof; nonce is its live
	// challenge, challenged when it was given; paired the pairing it
	// proved, "" for none.
	cid        string
	nonce      string
	challenged time.Time
	paired     string
}

func (c *conn) close() {
	c.once.Do(func() {
		close(c.done)
		c.nc.Close()
	})
}

func (c *conn) subscribed(ev string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.subs[ev]
}

// push queues a line. A full queue closes the connection when the line
// must not be lost (inbox, grant, chat); the client re-dials and re-reads.
func (c *conn) push(v any, mustDeliver bool) bool {
	raw, err := json.Marshal(v)
	if err != nil {
		return false
	}
	select {
	case <-c.done:
		return false
	case c.out <- append(raw, '\n'):
		return true
	default:
		if mustDeliver {
			c.close()
		}
		return false
	}
}

// pushFleet queues a fleet event; a slow reader gets the latest only.
func (c *conn) pushFleet(f Fleet) {
	raw, err := json.Marshal(Event{Event: EventFleet, Data: f})
	if err != nil {
		return
	}
	c.mu.Lock()
	pending := c.fleet != nil
	c.fleet = append(raw, '\n')
	c.mu.Unlock()
	if pending {
		return // the writer will take the latest
	}
	select {
	case c.out <- nil: // a marker: write the pending fleet
	default:
	}
}

func (c *conn) writer() {
	for {
		select {
		case <-c.done:
			return
		case line := <-c.out:
			if line == nil {
				c.mu.Lock()
				line, c.fleet = c.fleet, nil
				c.mu.Unlock()
				if line == nil {
					continue
				}
			}
			c.nc.SetWriteDeadline(time.Now().Add(writeDeadline))
			if _, err := c.nc.Write(line); err != nil {
				c.close()
				return
			}
		}
	}
}

func (c *conn) reply(id int64, result any) {
	c.push(Reply{ID: id, OK: true, Result: result}, true)
}

func (c *conn) fail(id int64, code, msg string) {
	c.push(Reply{ID: id, OK: false, Error: &WireError{Code: code, Message: msg}}, true)
}

// writeNow writes one line directly, before the writer runs, and closes.
func writeNow(nc net.Conn, code, msg string) {
	raw, _ := json.Marshal(Reply{ID: 0, OK: false, Error: &WireError{Code: code, Message: msg}})
	nc.SetWriteDeadline(time.Now().Add(time.Second))
	nc.Write(append(raw, '\n'))
	nc.Close()
}

func (s *Server) serve(nc net.Conn) {
	// Who is it, before reading a byte.
	peer, err := identify(nc)
	if err != nil || peer.UID != os.Getuid() {
		writeNow(nc, CodeForbidden, "")
		return
	}
	if s.paused.Load() {
		writeNow(nc, CodeUnavailable, "the human disconnected API clients in Den")
		return
	}
	c := &conn{s: s, nc: nc, peer: peer, since: time.Now(), out: make(chan []byte, outQueue), done: make(chan struct{}), subs: map[string]bool{}, scope: "conn:" + randHex(), cid: randHex()}
	s.mu.Lock()
	perPID := 0
	for o := range s.conns {
		if o.peer.PID == peer.PID {
			perPID++
		}
	}
	if len(s.conns) >= maxConns || perPID >= maxConnsPerPID {
		s.mu.Unlock()
		writeNow(nc, CodeUnavailable, "too many connections")
		return
	}
	s.conns[c] = true
	first := peer.Exe != "" && !s.seenExe[peer.Exe]
	if first {
		s.seenExe[peer.Exe] = true
	}
	s.mu.Unlock()
	defer func() {
		c.close()
		s.mu.Lock()
		delete(s.conns, c)
		s.lastConn[peer.Exe] = time.Now()
		s.mu.Unlock()
	}()
	go c.writer()

	r := bufio.NewReaderSize(nc, 64<<10)
	nc.SetReadDeadline(time.Now().Add(helloDeadline))
	for {
		line, err := readLine(r)
		if errors.Is(err, errTooLarge) {
			c.fail(0, CodeTooLarge, "")
			time.Sleep(50 * time.Millisecond)
			return
		}
		if err != nil {
			return
		}
		c.mu.Lock()
		streaming := len(c.subs) > 0 || c.asks > 0
		c.mu.Unlock()
		if streaming {
			nc.SetReadDeadline(time.Time{})
		} else {
			nc.SetReadDeadline(time.Now().Add(idleDeadline))
		}
		var req Request
		if json.Unmarshal(line, &req) != nil {
			c.fail(0, CodeBadRequest, "not a request")
			continue
		}
		s.dispatch(c, req, first)
	}
}

var errTooLarge = errors.New("line too large")

// readLine reads one \n-terminated line of at most MaxLine bytes.
func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(buf)+len(chunk) > MaxLine+1 {
			return nil, errTooLarge
		}
		buf = append(buf, chunk...)
		if err == nil {
			return bytes.TrimRight(buf, "\r\n"), nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}

// dispatch answers one request. The verbs are an allow-list; anything else
// is unsupported, including every verb that would approve, answer, spawn,
// close or send.
func (s *Server) dispatch(c *conn, req Request, first bool) {
	c.mu.Lock()
	hello, agent := c.hello, c.agent
	c.mu.Unlock()
	if req.Op != "hello" && !hello {
		c.fail(req.ID, CodeHelloFirst, "")
		return
	}
	if req.Op != "hello" && agent {
		c.fail(req.ID, CodeForbidden, "an agent's process may not use den-api.sock")
		return
	}
	if req.V != V && req.Op != "hello" {
		c.fail(req.ID, CodeUnsupported, "protocol version")
		return
	}
	switch req.Op {
	case "hello":
		var p struct {
			V     int    `json:"v"`
			Group string `json:"group"`
		}
		json.Unmarshal(req.Params, &p)
		v := p.V
		if v == 0 {
			v = req.V
		}
		if v != V {
			c.fail(req.ID, CodeUnsupported, "protocol version")
			return
		}
		if p.Group != "" && !validGroup(p.Group) {
			c.fail(req.ID, CodeBadRequest, "group: 32 lowercase hex digits, 128 random bits")
			return
		}
		host, _ := os.Hostname()
		isAgent, match := false, AgentProc{}
		if view := s.view.Load(); view != nil {
			match, isAgent = underAgent(c.peer.PID, view.AgentProcs)
		}
		c.mu.Lock()
		c.hello, c.agent = true, isAgent
		if p.Group != "" {
			// The program is part of the scope: another program naming
			// the same group shares nothing with it.
			c.scope = "group:" + c.peer.Exe + "\x00" + p.Group
		}
		c.mu.Unlock()
		if isAgent {
			if text := s.refuse(c.peer, refusedAgent, "it runs under "+sanitize.Label(match.Name, 60)+" (pid "+itoa(match.PID)+")", time.Now()); text != "" {
				s.note(text)
			}
		} else if first {
			s.note("an app connected to den-api.sock: " + sanitize.Label(c.peer.Exe, 120) + " (pid " + itoa(c.peer.PID) + "). /api lists apps; /api disconnect drops them.")
		}
		c.reply(req.ID, Info{Version: s.opts.Version, Versions: []int{V}, Host: host})
	case "fleet.snapshot":
		c.reply(req.ID, s.fleetFor(c))
	case "fleet.events":
		c.mu.Lock()
		c.subs[EventFleet] = true
		c.mu.Unlock()
		c.reply(req.ID, map[string]bool{"subscribed": true})
		c.pushFleet(s.fleetFor(c))
	case "inbox.list":
		items := []Item{}
		if v := s.view.Load(); v != nil {
			items = append(items, v.Items...)
		}
		for _, gr := range s.grants.mine(c.peer.Exe) {
			items = append(items, gr.item(gr.At))
		}
		c.reply(req.ID, items)
	case "inbox.events":
		c.mu.Lock()
		c.subs[EventInbox] = true
		c.mu.Unlock()
		c.reply(req.ID, map[string]bool{"subscribed": true})
	case "inbox.body":
		var p struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(req.Params, &p) != nil || p.ID == "" {
			c.fail(req.ID, CodeBadRequest, "id")
			return
		}
		for _, gr := range s.grants.mine(c.peer.Exe) {
			if gr.ID == p.ID {
				c.reply(req.ID, Body{ID: gr.ID, Kind: KindGrant, Text: gr.Body, Hash: gr.Hash})
				return
			}
		}
		v := s.view.Load()
		if v == nil {
			c.fail(req.ID, CodeNotFound, "no item "+sanitize.Label(p.ID, 80))
			return
		}
		if b, ok := v.Bodies[p.ID]; ok {
			c.reply(req.ID, b)
			return
		}
		for _, it := range v.Items {
			if it.ID == p.ID {
				c.fail(req.ID, CodeUnavailable, "a request from another machine is not served")
				return
			}
		}
		c.fail(req.ID, CodeNotFound, "no item "+sanitize.Label(p.ID, 80))
	case "mission.list":
		ms := []Mission{}
		if v := s.view.Load(); v != nil {
			ms = v.Missions
		}
		c.reply(req.ID, ms)
	case "chat.sessions":
		c.reply(req.ID, sessionsOut(s.chat.Sessions()))
	case "chat.history":
		var p struct {
			Session string `json:"session"`
		}
		if json.Unmarshal(req.Params, &p) != nil || p.Session == "" {
			c.fail(req.ID, CodeBadRequest, "session")
			return
		}
		h, ok := s.chat.History(p.Session)
		if !ok {
			c.fail(req.ID, CodeNotFound, "no such session")
			return
		}
		c.reply(req.ID, h)
	case "ask":
		s.ask(c, req)
	case "pair.claim":
		s.claim(c, req)
	case "pair.challenge":
		s.challenge(c, req)
	case "pair.auth":
		s.auth(c, req)
	case "orch.say":
		s.say(c, req)
	case "focus":
		s.focus(c, req)
	case "ask.cancel":
		var p struct {
			Ask string `json:"ask"`
		}
		if json.Unmarshal(req.Params, &p) != nil || p.Ask == "" {
			c.fail(req.ID, CodeBadRequest, "ask")
			return
		}
		if !s.lane.cancel(p.Ask, c.peer.Exe) {
			c.fail(req.ID, CodeNotFound, "no such ask")
			return
		}
		c.reply(req.ID, map[string]bool{"cancelled": true})
	case "grant.request":
		gr, err := s.grants.request(req.Params, c.peer)
		if err != nil {
			var e errAsk
			if errors.As(err, &e) {
				c.fail(req.ID, e.code, e.msg)
			} else {
				c.fail(req.ID, CodeInternal, "")
			}
			return
		}
		select {
		case s.acts <- Act{Grant: gr}:
			c.reply(req.ID, map[string]string{"id": gr.ID})
			s.grantCard(gr, "added")
		default:
			s.grants.cancel(gr.ID)
			c.fail(req.ID, CodeUnavailable, "Den is busy; ask again")
		}
	case "grant.events":
		c.mu.Lock()
		c.subs[EventGrant] = true
		c.mu.Unlock()
		c.reply(req.ID, map[string]bool{"subscribed": true})
		for _, ev := range s.grants.take(c.peer.Exe) {
			c.push(Event{Event: EventGrant, Data: ev}, true)
		}
	default:
		c.fail(req.ID, CodeUnsupported, "")
	}
}

func sessionsOut(in []Session) []Session {
	for i := range in {
		in[i].Title = sanitize.Label(in[i].Title, 80)
	}
	return in
}

// ask queues a question for the lane and streams its chunks on this
// connection.
func (s *Server) ask(c *conn, req Request) {
	var p struct {
		Text    string `json:"text"`
		Session string `json:"session"`
	}
	if json.Unmarshal(req.Params, &p) != nil || strings.TrimSpace(p.Text) == "" {
		c.fail(req.ID, CodeBadRequest, "text")
		return
	}
	if len(p.Text) > MaxAskText {
		c.fail(req.ID, CodeBadRequest, "the question is over 16 KiB")
		return
	}
	if r, bad := sanitize.Hidden(p.Text); bad {
		c.fail(req.ID, CodeBadRequest, fmt.Sprintf("the question holds a control, bidi or invisible character (%U)", r))
		return
	}
	if s.leaked(p.Text) {
		c.fail(req.ID, CodeBadRequest, "the question holds the pairing code; Den cancelled it")
		return
	}
	session := p.Session
	if session == "" {
		session = s.chat.Current()
	} else if !s.chat.Known(session) {
		c.fail(req.ID, CodeNotFound, "no such session")
		return
	} else {
		s.chat.Use(session)
	}
	c.mu.Lock()
	c.asks++
	scope := c.scope
	c.mu.Unlock()
	j := &askJob{id: "a-" + randHex(), session: session, text: p.Text, exe: c.peer.Exe, scope: scope, cancelled: make(chan struct{})}
	// The asking connection going away stops the turn.
	go func() {
		select {
		case <-c.done:
			j.cancel()
		case <-j.cancelled:
		}
	}()
	j.send = func(ch Chunk) bool {
		ok := c.push(Event{Event: EventChat, Data: ch}, true)
		if ch.Type == "status" {
			s.asksChanged()
		}
		if ch.Type == "done" || ch.Type == "error" {
			c.mu.Lock()
			c.asks--
			c.mu.Unlock()
			s.asksChanged()
		}
		return ok
	}
	c.reply(req.ID, map[string]string{"session": session, "ask": j.id})
	if err := s.lane.enqueue(j); err != nil {
		j.end(err.Error())
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
