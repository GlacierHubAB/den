package remote

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// heldSet is the held requests one session with a host has offered: each
// becomes a bridge.Request whose Reply writes a decision down this
// session's pipe, bound to the id and the hash of the body Den received.
// When the session ends, every one still open is let go as "link lost":
// it is never left looking answerable, and a reply to it goes nowhere.
type heldSet struct {
	host     string
	ctx      context.Context // the session's; done once it ended
	write    func(v any) error
	answered *answeredIDs // the host's, across its sessions

	mu   sync.Mutex
	open map[string]*bridge.Request
}

func newHeldSet(ctx context.Context, host string, answered *answeredIDs, write func(any) error) *heldSet {
	return &heldSet{host: host, ctx: ctx, write: write, answered: answered, open: map[string]*bridge.Request{}}
}

// answeredIDs are the held ids a host's requests were answered under, the
// newest maxAnswered of them, kept for as long as Den runs.
//
// A held id is single use. serve keeps an id when it offers a request
// again after a reconnect, which is how a request still open on the host,
// re-held by the next serve after a handover, becomes answerable again
// here. An id Den has answered is never answerable again: serve forgets a
// request once it delivers the decision, so the same id offered again is
// either a decision that never arrived (the link died with it in the pipe)
// or a host trying to have the human answer twice, perhaps for a different
// body. Either way it is refused, flagged, and sent back to its terminal
// with no decision, where the human answers it on the host.
//
// They also keep the hash of the first body each id came with: an id is
// one request, so offered again with another body it is refused the same
// way, and the body the human read never changes under them.
type answeredIDs struct {
	mu    sync.Mutex
	ids   map[string]*heldID
	order []string
}

// heldID is what a host's held id has been: its first body, and whether
// Den answered it.
type heldID struct {
	hash     string
	answered bool
}

// maxAnswered bounds answeredIDs: a human answers far fewer requests in one
// run of Den, and serve forgets an id long before that many more come.
const maxAnswered = 4096

func newAnsweredIDs() *answeredIDs { return &answeredIDs{ids: map[string]*heldID{}} }

// entry is id's record, made when first seen. Called with a.mu held.
func (a *answeredIDs) entry(id, hash string) *heldID {
	if e := a.ids[id]; e != nil {
		return e
	}
	if len(a.order) >= maxAnswered {
		delete(a.ids, a.order[0])
		a.order = a.order[1:]
	}
	e := &heldID{hash: hash}
	a.ids[id] = e
	a.order = append(a.order, id)
	return e
}

// add records that Den answered id.
func (a *answeredIDs) add(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entry(id, "").answered = true
}

func (a *answeredIDs) has(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	e := a.ids[id]
	return e != nil && e.answered
}

// same reports whether id comes with the body it first came with (hash),
// and keeps hash as that body when id is new.
func (a *answeredIDs) same(id, hash string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	e := a.entry(id, hash)
	if e.hash == "" {
		e.hash = hash
	}
	return e.hash == hash
}

// check says what to flag when a held message cannot become a request: the
// host's policy does not allow it, here or as the host applies it, or the
// message is not what serve sends. "" when it can.
func (s *heldSet) check(hm Held, configured, applied Policy) string {
	switch {
	case configured.rank() < Approve.rank() || applied.rank() < Approve.rank():
		return "a held request under observe"
	case !isID(hm.ID):
		return "a held request with a malformed id"
	case !isSessionID(hm.Session):
		return "a held request whose session id is not a UUID"
	case !plainField(hm.Tool):
		return "a held request with a malformed tool"
	case !checkBody(hm.Tool, hm.Body):
		return "a held request whose body the policy does not allow"
	}
	return ""
}

// parkedHeld is a held request for a session the host has not listed yet,
// and how many snapshots have come since without it.
type parkedHeld struct {
	hm        Held
	snapshots int
}

// unlistedSnapshots is how many snapshots a held request waits for its
// session to be listed: the one already on its way, and the next.
const unlistedSnapshots = 2

// refuse sends a held request back to its terminal on the host with no
// decision, where the human answers it there.
func (s *heldSet) refuse(hm Held) {
	if s.ctx.Err() == nil {
		s.write(Decision{Type: TypeDecision, ID: hm.ID, Hash: BodyHash(hm.Body)})
	}
}

// add turns a held message into a request for the inbox. It fails (with
// what to flag) when check does, or the id is spent or is another request's.
func (s *heldSet) add(hm Held, configured, applied Policy) (*bridge.Request, string) {
	if why := s.check(hm, configured, applied); why != "" {
		return nil, why
	}
	if s.answered.has(hm.ID) {
		// Single use (answeredIDs): back to its terminal, with no decision.
		s.refuse(hm)
		return nil, "a held request offered again after it was answered"
	}
	s.mu.Lock()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		return nil, ""
	}
	if s.open[hm.ID] != nil {
		s.mu.Unlock()
		return nil, "a held request offered twice"
	}
	if !s.answered.same(hm.ID, BodyHash(hm.Body)) {
		// One id is one request: the body the human was shown stays.
		s.mu.Unlock()
		s.refuse(hm)
		return nil, "a held request offered again with a different body"
	}
	defer s.mu.Unlock()
	if len(s.open) >= MaxHeld {
		return nil, "more held requests than a serve holds"
	}
	// The envelope is Den's own: a PermissionRequest, whatever the host
	// sent, with only the session, the tool and the filtered body.
	payload, _ := json.Marshal(map[string]any{
		"session_id": hm.Session, "hook_event_name": "PermissionRequest", "tool_name": hm.Tool, "tool_input": hm.Body,
	})
	env := bridge.Envelope{Event: "PermissionRequest", SessionID: hm.Session, Payload: payload}
	id, hash := hm.ID, BodyHash(hm.Body)
	var r *bridge.Request
	r = bridge.NewRemoteRequest(s.host, id, env, func(d bridge.Decision) {
		s.mu.Lock()
		open := s.open[id] == r
		delete(s.open, id)
		s.mu.Unlock()
		if !open || s.ctx.Err() != nil {
			return // gone, or the link it came on is lost
		}
		s.answered.add(id)
		s.write(Decision{Type: TypeDecision, ID: id, Hash: hash, Behavior: d.Behavior, Message: d.Message, Answers: d.Answers})
	})
	s.open[id] = r
	return r, ""
}

// gone lets the request with id go: its hook on the host stopped waiting.
func (s *heldSet) gone(id string) *bridge.Request {
	s.mu.Lock()
	r := s.open[id]
	delete(s.open, id)
	s.mu.Unlock()
	if r != nil {
		r.LetGo("no longer waiting on " + s.host + ": answered or dismissed there")
	}
	return r
}

// releaseAll lets every open request go, once the session ended.
func (s *heldSet) releaseAll() []*bridge.Request {
	s.mu.Lock()
	var out []*bridge.Request
	for id, r := range s.open {
		out = append(out, r)
		delete(s.open, id)
	}
	s.mu.Unlock()
	for _, r := range out {
		r.LetGo("released to the terminal on " + s.host + " (link lost)")
	}
	return out
}

// plainField is a session id or tool name as serve sends it: short, one
// line, nothing sanitising would change.
func plainField(s string) bool {
	return s != "" && len(s) <= 80 && sanitize.Line(s) == s
}

// isSessionID reports whether s is a Claude Code session id: a UUID in its
// 8-4-4-4-12 hex form. Every session id a host sends must be one; it keys
// the agent, names its critter and ends up in titles and the event log.
func isSessionID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F'):
			return false
		}
	}
	return true
}

// ControllerPath is where this machine's Den keeps its controller id.
func ControllerPath(stateDir string) string { return filepath.Join(stateDir, "controller") }

// LoadController is this machine's controller id, made on first use: 128
// random bits in hex, in StateDir (0600). It is an identity marker, not a
// secret. A host's serve compares it to tell this Den reconnecting (which
// takes its held requests back) from another Den (which stays read-only);
// a process that could read it on this machine could already act as this
// Den. "" when it cannot be kept, so no takeover happens.
func LoadController(stateDir string) string {
	if stateDir == "" {
		return ""
	}
	p := ControllerPath(stateDir)
	if b, err := os.ReadFile(p); err == nil {
		if id := strings.TrimSpace(string(b)); isID(id) {
			return id
		}
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	id := hex.EncodeToString(b[:])
	if os.MkdirAll(stateDir, 0o700) != nil || writeAtomic(p, []byte(id+"\n"), 0o600) != nil {
		return ""
	}
	return id
}
