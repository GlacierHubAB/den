package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Grant limits.
const (
	MaxGrantBody    = 16 << 10 // a manifest body, in bytes
	MaxGrantLines   = 200
	maxGrantsPerExe = 3  // pending at once
	maxGrantsPerRun = 20 // asked in one Den run
	maxRetained     = 16 // answers kept for an app that is away, per exe
	grantGrace      = 30 * time.Second
)

// Manifest is grant.request's params: what an app asks for. Body is the
// exact text Den shows on the card; the answer carries its hash.
type Manifest struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Version      int      `json:"version"`
	Capabilities []string `json:"capabilities"`
	Provenance   string   `json:"provenance,omitempty"`
	Body         string   `json:"body"`
}

// GrantAnswer is a grant event's data.
type GrantAnswer struct {
	ID      string `json:"id"`
	Answer  string `json:"answer"` // granted, denied, expired
	Version int    `json:"version"`
	Hash    string `json:"hash,omitempty"`
}

// Grant is a request Den puts in its inbox for the human: the act the UI
// gets. Body is stored verbatim and never changes.
type Grant struct {
	At           time.Time
	ID           string
	Exe          string
	PID          int
	Name         string // sanitised, at most 40 characters
	Version      int
	Capabilities []string // each sanitised
	Body         string
	Hash         string
}

// Act is something only the UI can do, handed to it on Acts().
type Act struct {
	Grant   *Grant
	Expired []string // grant ids no longer answerable
	Focus   *FocusReq
	// Control: a claimed code to confirm (Pair), that pairing settled
	// (PairDone, its id), the pairings changed (Paired), the code left
	// Den's screen (CodeGone), an app's turn for the orchestrator (Say).
	Pair     *PairAsk
	PairDone string
	Paired   bool
	CodeGone bool
	Say      *Say
	// Console: a claim for the human to confirm (Console), that claim
	// settled (ConsoleDone, its id), the console changed (ConsoleChanged),
	// the console code left Den's screen (ConsoleCodeGone), a proof for
	// the console from another program (ConsoleIntruder).
	Console         *ConsoleAsk
	ConsoleDone     string
	ConsoleChanged  bool
	ConsoleCodeGone bool
	ConsoleIntruder *ConsoleIntruder
	// CardAnswer is a console's answer to a card, for Den to carry out.
	CardAnswer *CardAnswer
	// Command is a console's command line, for Den to decide.
	Command *CommandRun
	// ModeSet is a console's mode.set, for Den to decide.
	ModeSet *ModeSet
	// ConsolePowers asks the human to give the console its powers again.
	ConsolePowers *ConsolePowersAsk
	// View is a view switch to carry out: only the UI knows the sessions
	// and what is waiting on the human.
	View *ViewReq
}

type grants struct {
	mu       sync.Mutex
	pending  map[string]*Grant        // by id
	retained map[string][]GrantAnswer // by exe: answers not yet delivered
	declined map[string]bool          // body hashes the human declined this run
	asked    int                      // grants asked this run
	away     map[string]time.Time     // exe → when its last connection closed
}

func newGrants() *grants {
	return &grants{pending: map[string]*Grant{}, retained: map[string][]GrantAnswer{}, declined: map[string]bool{}, away: map[string]time.Time{}}
}

// checkBody refuses what the card could not show exactly as it is.
func checkBody(body string) error {
	switch {
	case strings.TrimSpace(body) == "":
		return errors.New("the body is empty")
	case len(body) > MaxGrantBody:
		return fmt.Errorf("the body is over %d bytes", MaxGrantBody)
	case strings.Count(body, "\n")+1 > MaxGrantLines:
		return fmt.Errorf("the body is over %d lines", MaxGrantLines)
	case !utf8.ValidString(body), strings.ContainsRune(body, utf8.RuneError):
		// JSON decoding turns invalid bytes into U+FFFD, so the text Den
		// would show and hash is not the text the app hashed.
		return errors.New("the body is not valid UTF-8")
	case strings.ContainsRune(body, '\t'):
		return errors.New("the body holds a tab")
	case strings.Contains(body, "──"):
		return errors.New("the body draws rules like Den's own frame around it")
	}
	if r, bad := sanitize.Hidden(body); bad {
		return fmt.Errorf("the body holds a control, bidi or invisible character (%U)", r)
	}
	return nil
}

func grantID() string {
	var b [16]byte
	rand.Read(b[:])
	return "g-" + hex.EncodeToString(b[:])
}

// request checks a manifest and makes the grant, or says why not.
func (g *grants) request(raw json.RawMessage, peer Peer) (*Grant, error) {
	var p struct {
		Manifest Manifest `json:"manifest"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return nil, errAsk{CodeBadRequest, "manifest"}
	}
	m := p.Manifest
	if err := checkBody(m.Body); err != nil {
		return nil, errAsk{CodeBadRequest, err.Error()}
	}
	if strings.TrimSpace(m.Name) == "" || len(m.Capabilities) == 0 || len(m.Capabilities) > 32 {
		return nil, errAsk{CodeBadRequest, "a manifest needs a name and 1 to 32 capabilities"}
	}
	gr := &Grant{At: time.Now().UTC(), ID: grantID(), Exe: peer.Exe, PID: peer.PID, Name: sanitize.Label(m.Name, 40), Version: m.Version, Body: m.Body, Hash: HashOf(m.Body)}
	for _, c := range m.Capabilities {
		gr.Capabilities = append(gr.Capabilities, sanitize.Label(c, 80))
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.declined[gr.Hash] {
		return nil, errAsk{CodeUnavailable, "the human declined this request already"}
	}
	n := 0
	for _, o := range g.pending {
		if o.Exe == peer.Exe {
			n++
		}
	}
	if n >= maxGrantsPerExe {
		return nil, errAsk{CodeUnavailable, "this app has 3 requests waiting"}
	}
	if g.asked >= maxGrantsPerRun {
		return nil, errAsk{CodeUnavailable, "too many requests in this run of Den"}
	}
	g.asked++
	g.pending[gr.ID] = gr
	return gr, nil
}

// item is a pending grant as inbox.list shows it to the app that asked.
func (gr *Grant) item(since time.Time) Item {
	return Item{ID: gr.ID, Kind: KindGrant, Agent: gr.Name, Since: since, Title: sanitize.Label("App grant · "+gr.Name, labelMax), Digest: digest(gr.Body)}
}

// mine lists an exe's pending grants: only the app that asked sees them.
func (g *grants) mine(exe string) []*Grant {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []*Grant
	for _, gr := range g.pending {
		if gr.Exe == exe {
			out = append(out, gr)
		}
	}
	return out
}

// cancel drops a grant the UI could not take.
func (g *grants) cancel(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if gr := g.pending[id]; gr != nil {
		delete(g.pending, id)
		g.asked--
	}
}

// answer settles a pending grant and returns the event and the exe it goes
// to; ok is false for a grant that is not pending (expired, unknown).
func (g *grants) answer(id, answer string) (GrantAnswer, *Grant, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	gr := g.pending[id]
	if gr == nil {
		return GrantAnswer{}, nil, false
	}
	delete(g.pending, id)
	if answer == "denied" {
		g.declined[gr.Hash] = true
	}
	ev := GrantAnswer{ID: gr.ID, Answer: answer, Version: gr.Version, Hash: gr.Hash}
	return ev, gr, true
}

// retain keeps an answer for an app that is not subscribed now.
func (g *grants) retain(exe string, ev GrantAnswer) {
	g.mu.Lock()
	defer g.mu.Unlock()
	r := append(g.retained[exe], ev)
	if len(r) > maxRetained {
		r = r[len(r)-maxRetained:]
	}
	g.retained[exe] = r
}

// take hands an app's retained answers to its new subscription.
func (g *grants) take(exe string) []GrantAnswer {
	g.mu.Lock()
	defer g.mu.Unlock()
	r := g.retained[exe]
	delete(g.retained, exe)
	return r
}

// expire ends the pending grants of the exes for which keep is false.
func (g *grants) expire(keep func(exe string) bool) ([]GrantAnswer, []*Grant) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []GrantAnswer
	var gone []*Grant
	for id, gr := range g.pending {
		if keep(gr.Exe) {
			continue
		}
		delete(g.pending, id)
		ev := GrantAnswer{ID: gr.ID, Answer: "expired", Version: gr.Version, Hash: gr.Hash}
		out = append(out, ev)
		gone = append(gone, gr)
		r := append(g.retained[gr.Exe], ev)
		if len(r) > maxRetained {
			r = r[len(r)-maxRetained:]
		}
		g.retained[gr.Exe] = r
	}
	return out, gone
}
