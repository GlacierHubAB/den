package api

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/GlacierHubAB/den/internal/permmode"
)

// mode.set: a console sets the global permission mode agents start in.
// den-api.sock checks the shape and the limits; Den decides. A lowering
// applies at once; any other change waits on a card for the human's ^Y,
// which the console may answer like any card it may answer.

// ModeSet is a console's request for Den to decide; Den replies on Reply.
type ModeSet struct {
	Mode    string
	Console string
	Reply   chan ModeSetResult
}

// ModeSetResult is Den's outcome: Code "" for done, with Applied for a
// lowering (or no change) and Card for a raise waiting on its card; else a
// refusal code and why.
type ModeSetResult struct {
	Code    string
	Note    string
	Applied bool
	Card    string
}

// modeSet is mode.set{mode}: on a proved console connection only, under
// the command limits, with a mode spelled exactly as Claude Code spells
// it and on the ranked line (never bypassPermissions).
func (s *Server) modeSet(c *conn, req Request) {
	if s.consoleOf(c) == "" {
		c.fail(req.ID, CodeForbidden, "prove first")
		return
	}
	if !s.consolePowers(c) {
		c.fail(req.ID, CodePowersLocked, PowersLocked)
		return
	}
	var p struct {
		Mode string `json:"mode"`
	}
	if json.Unmarshal(req.Params, &p) != nil || !permmode.Valid(p.Mode) {
		c.fail(req.ID, CodeBadRequest, "mode: one of dontAsk, plan, default, acceptEdits, auto")
		return
	}
	if !s.cmdLimits.allow(time.Now()) {
		c.fail(req.ID, CodeUnavailable, "too many commands")
		return
	}
	r := &ModeSet{Mode: p.Mode, Console: ConsoleLabel, Reply: make(chan ModeSetResult, 1)}
	if !s.act(Act{ModeSet: r}) {
		c.fail(req.ID, CodeUnavailable, ClaimBusy)
		return
	}
	go func() {
		select {
		case res := <-r.Reply:
			switch {
			case res.Code != "":
				c.fail(req.ID, res.Code, res.Note)
			case res.Card != "":
				c.reply(req.ID, map[string]any{"ok": true, "card": res.Card, "note": res.Note})
			default:
				c.reply(req.ID, map[string]any{"ok": true, "applied": true, "note": res.Note})
			}
		case <-time.After(10 * time.Second):
			c.fail(req.ID, CodeUnavailable, "Den did not answer in time")
		case <-c.done:
		}
	}()
}

// modeState is the global mode as Den last told the server (SetMode): ""
// until Den has read it.
type modeState struct {
	mu   sync.Mutex
	mode string
}

func (m *modeState) get() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mode
}

// SetMode records the global permission mode, from Den, and pushes the
// read to the console's subscribed connections when that changes it. The
// server never decides a mode: it only shows Den's.
func (s *Server) SetMode(mode string) {
	s.mode.mu.Lock()
	changed := s.mode.mode != mode
	s.mode.mode = mode
	s.mode.mu.Unlock()
	if !changed {
		return
	}
	if _, ok := s.CurrentConsole(); !ok {
		// No console to tell; the next grants.get works the read out
		// afresh, as for God mode.
		return
	}
	s.grantsChanged()
}

// ModeNow is the global mode as the server last heard it from Den.
func (s *Server) ModeNow() string { return s.mode.get() }
