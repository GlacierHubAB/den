package api

import "sync"

// A desktop mission's state is set by Den's TUI (internal/ui's desktop
// missions); the server only tells the console, so its overlay shows while
// an agent controls the desktop. No verb reads or changes it.

// EventDesktop carries a desktop mission's state (Desktop) on each change.
const EventDesktop = "desktop"

// Desktop states.
const (
	DesktopActive = "active"
	DesktopIdle   = "idle"
)

// Desktop is a desktop event's data: active while a desktop agent's
// mission runs, with its id, idle once it has ended.
type Desktop struct {
	State   string `json:"state"`
	Mission string `json:"mission,omitempty"`
}

type desktopState struct {
	mu      sync.Mutex
	mission string // the running mission's id, "" for none
}

// state is the state as the wire carries it; d.mu is held.
func (d *desktopState) state() Desktop {
	if d.mission == "" {
		return Desktop{State: DesktopIdle}
	}
	return Desktop{State: DesktopActive, Mission: d.mission}
}

// SetDesktop records the running desktop mission, "" when none runs, from
// Den, and tells the console's subscribed connections when it changed.
// The pushes happen under the state's lock, as console.events' replay
// does (replayDesktop), so a connection's last desktop event is always the
// state as it is.
func (s *Server) SetDesktop(mission string) {
	s.desk.mu.Lock()
	defer s.desk.mu.Unlock()
	prev := s.desk.mission
	s.desk.mission = mission
	if prev == mission {
		return
	}
	var evs []Desktop
	if prev != "" && mission != "" {
		// Another mission took over: the first ended.
		evs = append(evs, Desktop{State: DesktopIdle, Mission: prev})
	}
	if mission == "" {
		evs = append(evs, Desktop{State: DesktopIdle, Mission: prev})
	} else {
		evs = append(evs, Desktop{State: DesktopActive, Mission: mission})
	}
	cur, ok := s.CurrentConsole()
	if !ok {
		return
	}
	for _, c := range s.consoleConns(cur.ID) {
		if c.subscribed(EventConsole) {
			for _, ev := range evs {
				c.push(Event{Event: EventDesktop, Data: ev}, true)
			}
		}
	}
}

// replayDesktop tells a connection that has just subscribed to the
// console's events where the desktop stands, active or idle, as the last
// of console.events' snapshot.
func (s *Server) replayDesktop(c *conn) {
	s.desk.mu.Lock()
	defer s.desk.mu.Unlock()
	c.push(Event{Event: EventDesktop, Data: s.desk.state()}, true)
}

// ConsoleProcs are the processes of the console's proved connections, pid
// to start (clock ticks since boot), as the kernel named them when each
// connected: nil when no console is paired or none of its connections has
// proved itself.
func (s *Server) ConsoleProcs() map[int]uint64 {
	cur, ok := s.CurrentConsole()
	if !ok {
		return nil
	}
	var out map[int]uint64
	for _, c := range s.consoleConns(cur.ID) {
		if c.peer.PID <= 1 {
			continue
		}
		if out == nil {
			out = map[int]uint64{}
		}
		out[c.peer.PID] = c.peer.Start
	}
	return out
}
