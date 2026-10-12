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
	"github.com/GlacierHubAB/den/internal/question"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// heldSet is the held requests one session with a host has offered: each
// becomes a bridge.Request whose Reply writes a decision down this
// session's pipe, bound to the id and the hash of the body Den received.
// When the session ends, every one still open is let go as "link lost":
// it is never left looking answerable, and a reply to it goes nowhere.
type heldSet struct {
	host  string
	ctx   context.Context // the session's; done once it ended
	write func(v any) error

	mu   sync.Mutex
	open map[string]*bridge.Request
}

func newHeldSet(ctx context.Context, host string, write func(any) error) *heldSet {
	return &heldSet{host: host, ctx: ctx, write: write, open: map[string]*bridge.Request{}}
}

// add turns a held message into a request for the inbox. It fails (with
// what to flag) when the host's policy does not allow it, here or as the
// host applies it, or the message is not what serve sends.
func (s *heldSet) add(hm Held, configured, applied Policy) (*bridge.Request, string) {
	switch {
	case configured.rank() < Approve.rank() || applied.rank() < Approve.rank():
		return nil, "a held request under observe"
	case !isID(hm.ID):
		return nil, "a held request with a malformed id"
	case !plainField(hm.Session) || !plainField(hm.Tool):
		return nil, "a held request with a malformed session or tool"
	case !checkBody(hm.Tool, hm.Body):
		return nil, "a held request whose body the policy does not allow"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return nil, ""
	}
	if s.open[hm.ID] != nil {
		return nil, "a held request offered twice"
	}
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

// checkBody is the local half of the body filter: a held body may carry
// only what heldBody lets through for its tool, as sanitised strings.
func checkBody(tool string, body json.RawMessage) bool {
	if len(body) > MaxLine {
		return false
	}
	var m map[string]any
	if json.Unmarshal(body, &m) != nil || m == nil {
		return false
	}
	if tool == question.Tool {
		qs, ok := question.Parse(body)
		if !ok || len(qs) > maxQuestions || len(m) != 1 {
			return false
		}
		for _, q := range qs {
			if sanitize.Text(q.Text) != q.Text || len(q.Options) > maxOptions {
				return false
			}
			for _, o := range q.Options {
				if sanitize.Line(o.Label) != o.Label || sanitize.Text(o.Description) != o.Description {
					return false
				}
			}
		}
		return true
	}
	fields, known := heldFields[tool]
	if !known {
		return len(body) <= argsMax && cleanArgs(m, 0) != nil
	}
	for k, v := range m {
		str, isStr := v.(string)
		ok := false
		for _, f := range fields {
			ok = ok || f.key == k
		}
		if !ok || !isStr || sanitize.Text(str) != str {
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
