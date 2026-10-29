package api

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// MaxConnsPerPID is how many connections one process may hold at once; one
// more is refused with "too many connections".
const MaxConnsPerPID = maxConnsPerPID

// Options configure the server.
type Options struct {
	Path    string // den-api.sock; SocketPath() by default
	Version string // Den's release, for hello
	ChatLog string // api-chat.jsonl
	Lane    LaneConfig
	// ConsoleKey is Den's Ed25519 key for console proofs (console.go);
	// nil leaves console pairing off.
	ConsoleKey ed25519.PrivateKey
	// ConsoleStore is where Den keeps its side of the console across
	// restarts (console_store.go); "" keeps it in memory only.
	ConsoleStore string
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
	// Console is set for a connection proved as the paired console
	// (console.auth): Den knows it from the pairing, never from anything
	// the program says.
	Console bool
}

// Server serves den-api.sock.
type Server struct {
	opts Options
	// con is the console (console.go); ckey Den's half of its proofs, nil
	// when console pairing is off.
	con  consoleState
	ckey *consoleKey
	// conv is the conversation mirror (conversation.go).
	conv convState
	// stopWatch ends the console record's watch; closeOnce makes Close's
	// clean-exit mark once.
	stopWatch chan struct{}
	// cardAnswers counts the console's card answers for their limits.
	cardAnswers answers
	// god is God mode's state as Den last set it (god.go).
	god godState
	// perms is the permission manager's last read and its seq (grants.go).
	perms permsState
	// desk is the running desktop mission as Den last set it (desktop.go).
	desk desktopState
	// overlay is the console's overlay link (overlay.go).
	overlay overlayState
	// cmdLimits counts the console's commands.
	cmdLimits cmdLimits
	closeOnce sync.Once
	bp        atomic.Pointer[bound] // the socket Den bound; Rebind replaces it
	view      atomic.Pointer[View]
	lastView  atomic.Pointer[ViewChange] // how the last view switch ended, for a subscriber's snapshot
	chat      *chatStore
	lane      *lane
	notes     chan string

	paused atomic.Bool // /api off or /api disconnect: answer unavailable
	// draining counts refused connections still reading out what their
	// client sent (turnAway); past maxDraining they are closed at once.
	draining atomic.Int32
	// credHook and idHook stand in for the kernel's peer credentials and
	// for what identify found, in tests only (setPeerCred, setIdentify):
	// per server, and atomic, so a test may change them while the server
	// serves. Production never sets them.
	credHook atomic.Pointer[func(net.Conn) (uid, pid int, err error)]
	idHook   atomic.Pointer[func(Peer) Peer]
	// pairHook runs between a claim's bookkeeping and its card, in tests
	// only, to let a second claim in there.
	pairHook atomic.Pointer[func(string)]
	// askHook runs between an ask's reply and its queueing, with its id,
	// in tests only, to let a cancel in there.
	askHook atomic.Pointer[func(string)]

	grants   *grants
	acts     chan Act
	ctl      *control
	lastConn map[string]time.Time // exe -> when its last connection closed

	focusLimit focusLimit
	viewSwitch viewSwitch
	rebound    bool // Rebind ran since the last /api resume

	mu        sync.Mutex
	conns     map[*conn]bool
	lastFleet []byte
	// lastAppFleet is the fleet as an app other than the console sees it,
	// with no doing.
	lastAppFleet []byte
	doing        map[string]doingSeen // each agent's published Doing, under mu
	seenExe      map[string]bool
	// connectNotes are when programs Den cannot identify earned a connect
	// note (firstConnect).
	connectNotes []time.Time
	refusals     map[string]*refusal // by exe and reason
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
	if o.ConsoleKey != nil {
		s.ckey = newConsoleKey(o.ConsoleKey)
	}
	s.grants, s.acts, s.lastConn, s.ctl = newGrants(), make(chan Act, 64), map[string]time.Time{}, newControl()
	s.stopWatch = make(chan struct{})
	if s.ckey != nil {
		if why := s.loadConsole(); why != "" {
			s.note(why)
		}
		s.con.mu.Lock()
		if s.con.recordHash == "" {
			// No record yet: write Den's (empty) one, so any file that
			// appears later is a change Den did not make.
			s.saveConsoleLocked()
		}
		s.con.mu.Unlock()
		go s.watchConsoleRecord(s.stopWatch)
	}
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

// maxRefusals bounds the programs Refusals keeps apart; past it, they are
// counted together per reason. maxConnectNotes bounds the connect notes
// for programs Den cannot identify, which a restart makes new, in
// connectNoteWindow; seenMax bounds the programs Den remembers seeing.
const (
	maxRefusals       = 64
	maxConnectNotes   = 3
	connectNoteWindow = 10 * time.Minute
	seenMax           = 256
)

// refusalKey is what refusals are counted under: the program and reason;
// every program Den cannot identify counts as one, so restarting one
// earns no new entry or note.
func refusalKey(exe, reason string) string {
	if Unidentified(exe) {
		exe = unreadablePrefix
	}
	return exe + "\x00" + reason
}

// firstConnect reports whether a connection of exe earns the chat's
// connect note: the first of each program Den read, and, for programs it
// cannot identify (each restart a new one), at most maxConnectNotes in
// connectNoteWindow. Called with s.mu held.
func (s *Server) firstConnect(exe string, now time.Time) bool {
	if Unidentified(exe) {
		keep := s.connectNotes[:0]
		for _, t := range s.connectNotes {
			if now.Sub(t) < connectNoteWindow {
				keep = append(keep, t)
			}
		}
		s.connectNotes = keep
		if len(keep) >= maxConnectNotes {
			return false
		}
		s.connectNotes = append(s.connectNotes, now)
		return true
	}
	if s.seenExe[exe] || len(s.seenExe) >= seenMax {
		return false
	}
	s.seenExe[exe] = true
	return true
}

// refuse counts a refused connection and returns the chat note it earns:
// the first of its program and reason, with detail (what it matched), then
// at most one a refusalEvery saying how many more; "" for none.
func (s *Server) refuse(p Peer, reason, detail string, now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := refusalKey(p.Exe, reason)
	r := s.refusals[key]
	if r == nil && len(s.refusals) >= maxRefusals {
		key = "\x00" + reason // any further program: one entry per reason
		r = s.refusals[key]
	}
	exe := sanitize.Label(p.Label(), 40)
	if r == nil {
		shown := p.Exe
		if strings.HasPrefix(key, "\x00") {
			shown = "more programs"
		}
		s.refusals[key] = &refusal{Refusal: Refusal{Exe: shown, Reason: reason, Count: 1, First: now, Last: now}, said: now}
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
	s.closeOnce.Do(func() {
		close(s.stopWatch)
		s.writeCleanMark()
	})
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
	cur, paired := s.CurrentConsole()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Client
	for c := range s.conns {
		c.mu.Lock()
		cl := Client{PID: c.peer.PID, Exe: c.peer.Exe, Label: c.peer.Label(), Since: c.since, Asking: c.asks > 0, Agent: c.agent}
		cl.Console = paired && c.console != "" && c.console == cur.ID
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
	// Each agent's Doing is settled before the view is shared: readers
	// never see it change under them.
	s.mu.Lock()
	if s.doing == nil {
		s.doing = map[string]doingSeen{}
	}
	settleDoing(v.Fleet.Agents, s.doing, time.Now())
	s.mu.Unlock()
	old := s.view.Swap(v)
	var oldCards map[string]Card
	if old != nil {
		oldCards = old.Cards
	}
	s.pushCards(oldCards, v.Cards, v.Settled)
	s.grantsChanged()
	// Whether the fleet changed, as the console sees it and as any other
	// app does (no doing): a change in doing alone tells an app nothing,
	// not even by when its fleet event comes.
	fleetRaw, _ := json.Marshal(v.Fleet)
	appFleet := v.Fleet
	appFleet.Agents = blankDoing(v.Fleet.Agents)
	appRaw, _ := json.Marshal(appFleet)
	s.mu.Lock()
	fleetChanged := !bytes.Equal(fleetRaw, s.lastFleet)
	appChanged := !bytes.Equal(appRaw, s.lastAppFleet)
	s.lastFleet, s.lastAppFleet = fleetRaw, appRaw
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
	modeChanged := v.Mode.Global != "" && (old == nil || old.Mode != v.Mode)
	for _, c := range conns {
		if c.subscribed(EventFleet) {
			if console := s.isConsole(c); console && fleetChanged || !console && appChanged {
				c.pushFleet(s.fleetFor(c))
			}
		}
		if modeChanged && c.subscribed(EventConsole) {
			c.push(Event{Event: EventMode, Data: v.Mode}, true)
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
	if !s.isConsole(c) {
		f.Agents = blankDoing(f.Agents)
	}
	return f
}

// isConsole reports whether c is a connection of the paired console, by
// its proof (c.console is set only once it proved itself), never by
// anything the client sends.
func (s *Server) isConsole(c *conn) bool {
	c.mu.Lock()
	id := c.console
	c.mu.Unlock()
	if id == "" {
		return false
	}
	s.con.mu.Lock()
	defer s.con.mu.Unlock()
	return s.con.cur != nil && s.con.cur.ID == id
}

// blankDoing is agents with every Doing empty, on a copy: the shared view
// is never written. What an agent is doing goes to the console alone.
func blankDoing(agents []Agent) []Agent {
	out := make([]Agent, len(agents))
	copy(out, agents)
	for i := range out {
		out[i].Doing = Doing{}
	}
	return out
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
	// cnonce and claimSalt are its live console challenge, cchallenged
	// when it was given; console the console it proved, "" for none.
	// reads are the cards it read, for card.answer (card.go).
	reads cardReads
	// cardHash is the card hash version it chose in hello: 1, or 2.
	cardHash int
	// answering holds, per card it is answering, the settled events
	// pushed meanwhile: they go out after the answer's reply (card.go).
	answering map[string][]Event
	// completes are its cmd.complete calls in the last second.
	completes   []time.Time
	cnonce      string
	claimSalt   string
	cchallenged time.Time
	crequested  time.Time // its last console.request or console.reconnect
	console     string
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

// cardVersion is the card hash version the connection reads: 2 when it
// chose it in hello, otherwise 1.
func (c *conn) cardVersion() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cardHash == 2 {
		return 2
	}
	return 1
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

// maxDraining, drainFor and drainMax bound how refused connections are
// read out before they close.
const (
	maxDraining = 8
	drainFor    = 200 * time.Millisecond
	drainMax    = 64 << 10
)

// turnAway answers a connection Den will not serve, on that connection's own
// goroutine. Closing a unix socket with bytes it has not read resets the
// client, which then reads ECONNRESET instead of why: so after the error
// line Den shuts its side for writing and discards what the client sent
// (never parsed), for at most drainFor and drainMax, then closes. A
// draining connection is not one of s.conns and counts against no cap;
// past maxDraining of them, it closes at once.
func (s *Server) turnAway(nc net.Conn, code, msg string) {
	if s.draining.Add(1) > maxDraining {
		s.draining.Add(-1)
		writeNow(nc, code, msg)
		return
	}
	defer s.draining.Add(-1)
	defer nc.Close()
	raw, _ := json.Marshal(Reply{ID: 0, OK: false, Error: &WireError{Code: code, Message: msg}})
	nc.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := nc.Write(append(raw, '\n')); err != nil {
		return
	}
	if uc, ok := nc.(*net.UnixConn); ok {
		uc.CloseWrite()
	}
	nc.SetReadDeadline(time.Now().Add(drainFor))
	io.Copy(io.Discard, io.LimitReader(nc, drainMax))
}

func (s *Server) serve(nc net.Conn) {
	// Who is it, before reading a byte.
	peer, err := s.identify(nc)
	if err != nil || peer.UID != os.Getuid() {
		writeNow(nc, CodeForbidden, "")
		return
	}
	if s.paused.Load() {
		s.turnAway(nc, CodeUnavailable, "the human disconnected API clients in Den")
		return
	}
	c := &conn{s: s, nc: nc, peer: peer, since: time.Now(), out: make(chan []byte, outQueue), done: make(chan struct{}), subs: map[string]bool{}, scope: "conn:" + randHex(), cid: randHex()}
	// The console's process may hold one more, so that a connection past
	// its proved ones hears console_in_use at console.auth.
	perPIDMax := maxConnsPerPID
	if s.isConsoleProcess(peer) {
		perPIDMax = consoleConnsMax + 1
	}
	s.mu.Lock()
	perPID := 0
	for o := range s.conns {
		if o.peer.PID == peer.PID {
			perPID++
		}
	}
	if len(s.conns) >= maxConns || perPID >= perPIDMax {
		s.mu.Unlock()
		s.turnAway(nc, CodeUnavailable, "too many connections")
		return
	}
	s.conns[c] = true
	first := s.firstConnect(peer.Exe, time.Now())
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
		// A reconnecting console may ask first, so that it sends nothing
		// before Den has proved itself (den_sig); the agent check hello
		// makes is made here too.
		if req.Op == "console.reconnect" && req.V == V {
			// No view yet means Den cannot tell an agent's process: refuse.
			v := s.view.Load()
			if v == nil {
				c.fail(req.ID, CodeUnavailable, "Den is starting")
				return
			}
			if _, isAgent := underAgent(c.peer.PID, v.AgentProcs); isAgent {
				c.fail(req.ID, CodeForbidden, "an agent's process may not use den-api.sock")
				return
			}
			s.consoleReconnect(c, req)
			return
		}
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
			// CardHash chooses the card hash version for the whole
			// connection: 2, or 1 (or none) for v1. Anything else, a
			// string or 0 included, is refused, never read as v1.
			CardHash json.RawMessage `json:"card_hash"`
		}
		json.Unmarshal(req.Params, &p)
		cardHash := 1
		if p.CardHash != nil {
			switch string(p.CardHash) {
			case "1":
			case "2":
				cardHash = 2
			default:
				c.fail(req.ID, CodeBadRequest, "card_hash: 1 or 2")
				return
			}
		}
		c.mu.Lock()
		again, had := c.hello, c.cardHash
		c.mu.Unlock()
		if again && had != cardHash {
			c.fail(req.ID, CodeBadRequest, "card_hash is chosen once, in the connection's first hello")
			return
		}
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
		c.cardHash = cardHash
		if p.Group != "" {
			// The program is part of the scope: another program naming
			// the same group shares nothing with it. The group itself is
			// the app's secret (random hex): an identity two programs could
			// share is not what keeps groups apart.
			c.scope = "group:" + c.peer.Exe + "\x00" + p.Group
		}
		c.mu.Unlock()
		if isAgent {
			if text := s.refuse(c.peer, refusedAgent, "it runs under "+sanitize.Label(match.Name, 60)+" (pid "+itoa(match.PID)+")", time.Now()); text != "" {
				s.note(text)
			}
		} else if first {
			s.note("an app connected to den-api.sock: " + sanitize.Label(ExeShownShort(c.peer.Exe), 120) + " (pid " + itoa(c.peer.PID) + "). /api lists apps; /api disconnect drops them.")
		}
		g := s.god.get()
		info := Info{Version: s.opts.Version, Versions: []int{V}, Host: host, Control: s.ControlState(), God: &g}
		if s.ckey != nil {
			info.Features = []string{FeatureConsole}
			// Public: the dock shows its fingerprint while the human
			// matches it with Den's card (console.request).
			info.ConsolePublicKey = s.ckey.pubHex
		}
		if v := s.view.Load(); v != nil {
			if v.Mode.Global != "" {
				mode := v.Mode
				info.Mode = &mode
			}
			if v.Fleet.View != "" {
				info.View, info.ViewWorkspaces = v.Fleet.View, []string{ViewWorkspace}
			}
		}
		c.reply(req.ID, info)
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
	case "console.challenge":
		s.consoleChallenge(c, req)
	case "console.claim":
		s.consoleClaim(c, req)
	case "console.request":
		s.consoleRequest(c, req)
	case "console.reconnect":
		s.consoleReconnect(c, req)
	case "console.auth":
		s.consoleAuth(c, req)
	case "console.events":
		s.consoleEvents(c, req)
	case "console.unpair":
		s.consoleUnpair(c, req)
	case "conversation.history":
		s.conversationHistory(c, req)
	case "overlay.linked":
		s.overlayVerb(c, req, true)
	case "overlay.unlinked":
		s.overlayVerb(c, req, false)
	case "card.get":
		s.cardGet(c, req)
	case "grants.get":
		s.grantsGet(c, req)
	case "card.answer":
		s.cardAnswer(c, req)
	case "cmd.list":
		s.cmdList(c, req)
	case "cmd.complete":
		s.cmdComplete(c, req)
	case "god.off":
		s.godOff(c, req)
	case "cmd.run":
		s.cmdRun(c, req)
	case "mode.set":
		s.modeSet(c, req)
	case "focus":
		s.focus(c, req)
	case "view.show":
		s.viewVerb(c, req, ViewAgent)
	case "view.hide":
		s.viewVerb(c, req, ViewUser)
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
	// Findable by its id before the app is told it: an ask.cancel sent the
	// moment the reply is read finds it, and its stream then ends with
	// "cancelled" once it is taken from the queue.
	s.lane.register(j)
	c.reply(req.ID, map[string]string{"session": session, "ask": j.id})
	if f := s.askHook.Load(); f != nil {
		(*f)(j.id)
	}
	if err := s.lane.enqueue(j); err != nil {
		s.lane.forget(j)
		if j.isCancelled() {
			j.end(j.why())
		} else {
			j.end(err.Error())
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
