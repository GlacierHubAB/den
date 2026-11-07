package api

import (
	"errors"
	"time"

	perm "github.com/GlacierHubAB/den/internal/grants"
)

// What only Den's own window does with the levels (the /permissions
// panel): no den-api verb and no cmd.run reaches these. Lowering a class,
// removing a kept entry and the start confirm are the human's; a raise is
// the human's alone, through RaiseDenLevel, which the panel calls only
// from its question's ^Y.

// The panel's refusals.
var (
	// ErrNoClass is a class or a level outside the closed sets.
	ErrNoClass = errors.New("no such class or level")
	// ErrRaise is SetDenLevel asked for a level that would raise the
	// class's cap: only RaiseDenLevel raises.
	ErrRaise = errors.New("that raises the class: it needs your ^Y")
	// ErrMoved is the read moved under a question: it is asked again.
	ErrMoved = errors.New("the levels changed: ask again")
	// ErrNotKept is a kept id that is not held.
	ErrNotKept = errors.New("that is not kept")
	// ErrHeld is the start confirm asked while the levels are held after
	// another program changed the stored file: only the powers card's
	// Ctrl+Y ends that hold.
	ErrHeld = errors.New("another program changed the stored levels: answer the powers card")
)

// GrantsRead is the read now, for Den's own window.
func (s *Server) GrantsRead() perm.Grants {
	var g perm.Grants
	s.permsDo(func() { g = s.grantsLocked(nil) })
	return g
}

// SetDenLevel is a class set in Den's window to a level that does not
// raise it: its cap, min(den_level, session) within the start rule, never
// rises. A console's session stays when it is below the new level, so a
// lowering here never lifts a lower session; at or above it, the human's
// setting replaces it. A level that would raise the cap is ErrRaise.
func (s *Server) SetDenLevel(class string, level int) error {
	if !perm.Valid(class, level) || level > perm.LastStep(class) {
		return ErrNoClass
	}
	var err error
	s.permsDo(func() {
		s.grantsLocked(nil) // a new pairing is folded in first (F3)
		st, _ := s.grantsInput()
		after := st
		after.DenLevels = map[string]int{}
		for k, v := range st.DenLevels {
			after.DenLevels[k] = v
		}
		after.DenLevels[class] = level
		after.Sessions = map[string]perm.Session{}
		for k, v := range st.Sessions {
			if k != class || v.Level < level {
				after.Sessions[k] = v
			}
		}
		// A raise is the cap rising, or den_level rising under a lower
		// session: the session ends with Den, and the higher den_level
		// would then hold without the human's ^Y.
		if after.Cap(class) > st.Cap(class) || level > denLevelOf(st, class) {
			err = ErrRaise
			return
		}
		s.setDenLevelLocked(class, level, hasSession(after, class)) // kept only when below level
	})
	return err
}

// RaiseDenLevel is the human's ^Y on a raise question in Den's window:
// the class's den_level becomes level and the console's session on it is
// cleared, only while the read is still the one the question was asked on
// (seq). Anything else is ErrMoved, and nothing changes.
func (s *Server) RaiseDenLevel(class string, level int, seq uint64) error {
	if !perm.Valid(class, level) || level > perm.LastStep(class) {
		return ErrNoClass
	}
	var err error
	s.permsDo(func() {
		if now := s.grantsLocked(nil); now.Seq != seq {
			err = ErrMoved
			return
		}
		// The raise counter: only here does a count go up, by one, in the
		// same step as the raise, so the read (and its seq) always moves.
		if s.perms.raiseN == nil {
			s.perms.raiseN = map[string]int{}
		}
		if s.perms.raisedAt == nil {
			s.perms.raisedAt = map[string]string{}
		}
		s.perms.raiseN[class]++
		s.perms.raisedAt[class] = time.Now().UTC().Format(time.RFC3339)
		s.setDenLevelLocked(class, level, false)
	})
	return err
}

// setDenLevelLocked sets class's den_level, keeps or clears its session,
// and marks it set in Den now. s.perms.mu is held.
func (s *Server) setDenLevelLocked(class string, level int, keepSession bool) {
	if s.perms.denLevels == nil {
		s.perms.denLevels = map[string]int{}
	}
	s.perms.denLevels[class] = level
	if !keepSession {
		delete(s.perms.caps, class)
	}
	if s.perms.setInDen == nil {
		s.perms.setInDen = map[string]string{}
	}
	s.perms.setInDen[class] = time.Now().UTC().Format(time.RFC3339)
	s.perms.gen++
	s.grantsLocked(nil)
}

// denLevelOf is class's den_level in st, as the read gives it.
func denLevelOf(st perm.State, class string) int {
	for _, c := range perm.Read(st).Classes {
		if c.Class == class {
			return c.DenLevel
		}
	}
	return 0
}

func hasSession(st perm.State, class string) bool {
	_, ok := st.Sessions[class]
	return ok
}

// GrantsStart is what the powers card says of the levels, for Den's
// window: held after another program changed the stored file, and the
// card's lines (permsCardLines: what Den found at start first), empty once
// confirmed.
func (s *Server) GrantsStart() (held bool, lines []string) {
	return s.permsHeld(), s.permsCardLines()
}

// ConfirmLevels is the start confirm from Den's window (the panel's c,
// then ^Y), for the levels and kept only, never the console's powers, and
// only while the read is the one the question was asked on (seq). While
// the levels are held after another program's change it is ErrHeld: that
// hold ends only on the powers card.
func (s *Server) ConfirmLevels(seq uint64) error {
	var err error
	s.permsDo(func() {
		if s.perms.held {
			err = ErrHeld
			return
		}
		if now := s.grantsLocked(nil); now.Seq != seq {
			err = ErrMoved
			return
		}
		s.perms.confirmed, s.perms.held, s.perms.startWhy = true, false, ""
		s.grantsLocked(nil)
	})
	return err
}

// RemoveKept removes a kept entry for good from Den's window ("*" every
// one: "Use the recommended settings"), as a console's removal does.
func (s *Server) RemoveKept(id string) error {
	if id != "*" && perm.KeptLabel(id) == "" {
		return ErrNoClass
	}
	var err error
	s.permsDo(func() {
		var left []string
		for _, k := range s.perms.kept {
			if id != "*" && k != id {
				left = append(left, k)
			}
		}
		if len(left) == len(s.perms.kept) {
			err = ErrNotKept
			return
		}
		s.perms.kept = left
		s.perms.gen++
		s.grantsLocked(nil)
	})
	return err
}
