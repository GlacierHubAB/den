package ui

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"time"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/peer"
	"github.com/GlacierHubAB/den/internal/peermsg"
	"github.com/GlacierHubAB/den/internal/permmode"
)

// Watching guarded agents (God mode's, mediate.go) once they run:
//
//   - a guard counts once its agent shows up with the session id Den drew
//     (registered); then Den self-tests its hook with an inert envelope it
//     never records, which the hook must ask Den about within selfTestFor;
//   - Den reads each guarded agent's transcript as it grows: a peer
//     message that reached the model and was not written by Den's own
//     process got past the guard;
//   - any of these, or a changed settings file or den binary
//     (checkGuards), marks the agent unguarded: Den delivers nothing more
//     to it and offers to close it, through close-safety;
//   - an agent in bypassPermissions Den holds no guard for (one resumed
//     outside Den, or left from an earlier God mode) is listed unguarded.
//
// Within the limit God mode states (a same-uid program can do what the
// user can): the session id Den draws is in the agent's command line, so
// such a program could write a session record with it before the real
// agent registers.

// selfTestFor is how long a new guarded agent's hook has to ask Den about
// the self-test envelope.
const selfTestFor = 15 * time.Second

// selfTestBody is the self-test envelope's body: inert, a fixed line with
// no instruction in it.
const selfTestBody = "Den's self-test of this agent's message guard. This message carries no request; nothing is to be done."

// selfTestFrom names the self-test's sender in its envelope.
const selfTestFrom = "uds:den-self-test"

// guardWatch is what Den watches of a registered guard.
type guardWatch struct {
	socket     string
	transcript string
	offset     int64     // how much of the transcript Den has read
	selfTest   string    // the self-test envelope's hash, "" until sent
	selfSent   time.Time // when it was sent
	selfOK     bool      // the hook asked Den about it
	offered    bool      // Den offered to close the agent, once it was unguarded and close-safety let it
}

// sendLaunchWanted reports whether the orchestrator should run in the
// send_message launch (control mode, SendMessage disallowed): for a paired
// app, and while God mode is on or any guarded agent still runs, so that
// every message to those agents goes through deliverTo.
func (m Model) sendLaunchWanted() bool {
	if m.controlWanted() || m.god.on {
		return true
	}
	for sid := range m.med.guards {
		if a, ok := m.localAgent(sid); ok && a.Session.Alive {
			return true
		}
	}
	return false
}

// localAgent is the live or last-known local agent of a session.
func (m Model) localAgent(sid string) (fleet.Agent, bool) {
	for _, a := range m.base {
		if a.Host == "" && a.Session.SessionID == sid {
			return a, true
		}
	}
	return fleet.Agent{}, false
}

// denPID is Den's own process, as a peer message's verifiedPeerPid names
// its writer; tests replace it.
var denPID = os.Getpid()

// watchGuards is the tick's look at every guard: registration and the
// self-test, the transcripts, and unguarded agents to offer to close.
func (m *Model) watchGuards(now time.Time) {
	for sid, g := range m.med.guards {
		a, found := m.localAgent(sid)
		if g.registered && found && !a.Session.Alive {
			m.forgetGuard(sid) // its agent has gone
			continue
		}
		if !g.registered && !g.launched.IsZero() && found && a.Session.Alive {
			g.registered = true
			g.watch = &guardWatch{socket: a.Session.Socket, transcript: a.Path}
			m.selfTest(sid, g)
		}
		w := g.watch
		if w == nil {
			continue
		}
		if w.transcript == "" && found {
			w.transcript = a.Path
		}
		if g.unguarded == "" && w.selfTest != "" && !w.selfOK && now.Sub(w.selfSent) > selfTestFor {
			m.markUnguarded(sid, "its message guard did not answer Den's self-test")
		}
		if g.unguarded == "" {
			if why := m.readTranscript(w); why != "" {
				m.markUnguarded(sid, why)
			}
		}
		if g.unguarded != "" && !w.offered && found && a.Session.Alive {
			// Through close-safety: an agent it refuses now (working, or
			// waiting on you) is offered again on a later tick.
			if it, _ := m.proposeClose([]fleet.Agent{a}, false, "Den", "it is unguarded ("+g.unguarded+"); reopen it with /reopen to start it in "+m.agentMode(), nil, nil); it != nil {
				w.offered = true
			}
		}
	}
}

// selfTest sends the guard's inert self-test envelope, never recorded, so
// the hook must block it, and ask Den.
func (m *Model) selfTest(sid string, g *agentGuard) {
	w := g.watch
	if w.socket == "" {
		m.markUnguarded(sid, "it has no inbox socket for Den's self-test")
		return
	}
	w.selfTest = peermsg.Hash(peer.Envelope(selfTestFrom, "den-self-test", selfTestBody))
	w.selfSent = time.Now()
	sock := w.socket
	go func() { _ = sendAs(sock, selfTestFrom, "den-self-test", selfTestBody) }()
}

// markUnguarded marks a guarded agent unguarded, loudly, once.
func (m *Model) markUnguarded(sid, why string) {
	g := m.med.guards[sid]
	if g == nil || g.unguarded != "" {
		return
	}
	g.unguarded = why
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "UNGUARDED: " + m.nameFor(sid) + ": " + why + ". Den delivers no more messages to it.", at: time.Now()}))
	m.flash, m.flashErr = "UNGUARDED: "+m.nameFor(sid)+" ("+why+")", true
}

// maxTranscriptLine is the longest transcript line Den parses; a longer
// one is only searched for a peer message's origin.
const maxTranscriptLine = 4 << 20

// peerKind finds a peer message's origin kind in a transcript line too
// long to parse, in any key order and spacing: loose on purpose, since a
// false match only marks the agent unguarded, a missed one would be blind.
var peerKind = regexp.MustCompile(`"kind"\s*:\s*"peer"`)

// peerKindSpan bounds how far a peerKind match can run, for the overlap
// kept between chunks.
const peerKindSpan = 256

// readTranscript reads what a guarded agent's transcript gained since the
// last look, and says why the guard failed when a peer message reached the
// model that Den's own process did not write. A transcript that shrank was
// rewritten: Den reads it again from the start.
func (m Model) readTranscript(w *guardWatch) string {
	if w.transcript == "" {
		return ""
	}
	f, err := os.Open(w.transcript)
	if err != nil {
		return ""
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() < w.offset {
		w.offset = 0
	}
	if _, err := f.Seek(w.offset, io.SeekStart); err != nil {
		return ""
	}
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, n, long, err := readLine(r, maxTranscriptLine)
		if err != nil {
			return "" // a partial line waits for the next look
		}
		w.offset += n
		if long {
			// Too long to parse: a peer message's origin in it is enough,
			// since Den cannot check who wrote it.
			if line != nil {
				return "a message Den could not check reached it"
			}
			continue
		}
		var e struct {
			Type   string `json:"type"`
			Origin *struct {
				Kind string `json:"kind"`
				PID  int    `json:"verifiedPeerPid"`
			} `json:"origin"`
		}
		if json.Unmarshal(line, &e) != nil || e.Type != "user" || e.Origin == nil || e.Origin.Kind != "peer" {
			continue
		}
		if e.Origin.PID != denPID {
			return "a message Den did not deliver reached it"
		}
	}
}

// readLine reads one line of at most max bytes. A longer one is read
// through, not kept: long is set, and line is non-nil only when the line
// held a peer message's origin. n is the bytes read, err is set for an
// unfinished last line.
func readLine(r *bufio.Reader, max int) (line []byte, n int64, long bool, err error) {
	var buf []byte
	var tail []byte
	found := false
	for {
		chunk, e := r.ReadSlice('\n')
		n += int64(len(chunk))
		if !long && len(buf)+len(chunk) > max {
			long = true
			found = peerKind.Match(buf)
			tail = append(tail[:0], lastBytes(buf, peerKindSpan)...)
			buf = nil
		}
		if long {
			if !found {
				probe := append(append([]byte{}, tail...), chunk...)
				found = peerKind.Match(probe)
				tail = append(tail[:0], lastBytes(probe, peerKindSpan)...)
			}
		} else {
			buf = append(buf, chunk...)
		}
		switch e {
		case nil:
			if long {
				if found {
					return []byte{}, n, true, nil
				}
				return nil, n, true, nil
			}
			return buf, n, false, nil
		case bufio.ErrBufferFull:
			continue
		default:
			return nil, n, long, e
		}
	}
}

func lastBytes(b []byte, k int) []byte {
	if len(b) <= k {
		return b
	}
	return b[len(b)-k:]
}

// unguardedAgents are the local live agents that run without asking and
// whose guard does not hold: marked unguarded, or in bypassPermissions
// with no guard Den wrote.
func (m Model) unguardedAgents() []string {
	var out []string
	for _, a := range m.base {
		if a.Host != "" || !a.Session.Alive {
			continue
		}
		sid := a.Session.SessionID
		g := m.med.guards[sid]
		switch {
		case g != nil && g.unguarded != "":
		case g == nil && m.modeOfAgent(sid) == permmode.Bypass:
		default:
			continue
		}
		out = append(out, a.Name)
	}
	return out
}
