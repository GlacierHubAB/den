package remote

import (
	"time"

	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/fleet"
)

// Limits on what one host's events may cost this machine. A host under
// MaxRate can still send a thousand events a second, each one a line in
// events.jsonl, an inbox item and a notification; these keep that bounded.
const (
	// EventRate is how many events a second a host may have logged and
	// shown after its first EventBurst.
	EventRate = 20
	// EventBurst is how many events a host may send at once, such as the
	// backfill after a reconnect.
	EventBurst = 1000
	// MaxAnnounced is how many sessions a host may announce with a
	// session_start event before a snapshot lists them.
	MaxAnnounced = 64
	// FloodDrops is how many events Den may drop from a host within
	// FloodWindow before it disables the host until /remote connect.
	FloodDrops  = 2000
	FloodWindow = time.Minute
)

// Disabled is a host Den stopped for flooding: not retried until the human
// reconnects it, and connected again when Den restarts.
const Disabled State = "disabled"

// verdict is what the gate does with one event.
type verdict int

const (
	pass    verdict = iota // log it and show it
	logOnly                // log it, within the budget, but show nothing
	drop                   // neither
)

// gate decides, for one host, which of its events Den takes. The bucket
// and the flood count carry over from one session to the next, so a host
// that keeps reconnecting gets no fresh allowance each time. An event must belong to a session the host listed in its last
// snapshot or announced with session_start, and fit in a token bucket.
// An event from before the link (the backfill) for a session the host no
// longer lists is history: logged, never shown. Drops after the backfill
// count towards FloodDrops, ten times as much as those during it.
type gate struct {
	rate, burst float64
	tokens      float64
	last        time.Time
	start       time.Time      // when the hello arrived: an event from before it is backfill
	live        bool           // a current event has arrived: the backfill is over
	announced   map[string]int // sessions started lately, at most MaxAnnounced, by the snapshot count then
	snapshots   int
	floodMax    int
	floodWin    time.Duration
	window      time.Time
	inWindow    int
}

func newGate(o Options, now time.Time) *gate {
	return &gate{rate: float64(o.EventRate), burst: float64(o.EventBurst), tokens: float64(o.EventBurst), last: now, start: now,
		announced: map[string]int{}, floodMax: o.FloodDrops, floodWin: o.FloodWindow, window: now}
}

// begin starts a session whose hello arrived at now: what came before is
// its backfill, and nothing is announced yet.
func (g *gate) begin(now time.Time) {
	g.start, g.live, g.snapshots = now, false, 0
	clear(g.announced)
}

// forgive clears the flood count: the human reconnected a disabled host.
func (g *gate) forgive() { g.inWindow = 0 }

// snapshot counts a snapshot, and forgets sessions announced before the
// one before it: by now the host lists them, or they are gone.
func (g *gate) snapshot() {
	g.snapshots++
	for s, n := range g.announced {
		if n < g.snapshots-1 {
			delete(g.announced, s)
		}
	}
}

// check decides on ev, already sanitised and on the local clock, from a
// host whose last snapshot listed agents. why is set when it drops.
func (g *gate) check(ev events.Event, agents []fleet.Meta, now time.Time) (verdict, string) {
	old := ev.Time.Before(g.start)
	if !old {
		g.live = true
	}
	v := pass
	if !g.listed(ev.Session, agents) {
		switch {
		case ev.Kind == events.SessionStart && !old && len(g.announced) < MaxAnnounced:
			g.announced[ev.Session] = g.snapshots
		case old:
			v = logOnly
		default:
			return drop, "events for sessions the host never listed"
		}
	}
	g.tokens = min(g.burst, g.tokens+now.Sub(g.last).Seconds()*g.rate)
	g.last = now
	if g.tokens < 1 {
		return drop, "more events than Den takes from one host"
	}
	g.tokens--
	return v, ""
}

func (g *gate) listed(session string, agents []fleet.Meta) bool {
	if _, ok := g.announced[session]; ok {
		return true
	}
	for _, a := range agents {
		if a.Session == session {
			return true
		}
	}
	return false
}

// dropped counts a drop at now and reports whether the host is now over
// the flood threshold. A drop during the backfill counts a tenth: a host
// that was away a long time sends a lot of history at once.
func (g *gate) dropped(now time.Time) bool {
	if now.Sub(g.window) >= g.floodWin {
		g.window, g.inWindow = now, 0
	}
	if g.live {
		g.inWindow += 10
	} else {
		g.inWindow++
	}
	return g.inWindow >= 10*g.floodMax
}
