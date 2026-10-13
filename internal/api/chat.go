package api

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// chatStore keeps the lane's sessions and their messages in drey-chat.jsonl
// (0600, in Den's state folder). It holds lane sessions only: the human's
// own orchestrator chat is never served.
type chatStore struct {
	mu       sync.Mutex
	path     string
	sessions map[string]*chatSession
	current  string
}

type chatSession struct {
	id       string
	title    string
	last     time.Time
	messages []Message
	started  bool // claude has a session by this id: resume it, never --session-id again
}

// chatLine is one line of drey-chat.jsonl.
type chatLine struct {
	Session string    `json:"session"`
	Kind    string    `json:"kind"` // "session", "started", "message", "current"
	Role    string    `json:"role,omitempty"`
	Text    string    `json:"text,omitempty"`
	At      time.Time `json:"at"`
}

// maxChatLog is where drey-chat.jsonl is rotated, keeping one previous file.
const maxChatLog = 10 << 20

func loadChat(path string) *chatStore {
	c := &chatStore{path: path, sessions: map[string]*chatSession{}}
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64<<10), MaxLine)
		for sc.Scan() {
			var l chatLine
			if json.Unmarshal(sc.Bytes(), &l) != nil || l.Session == "" {
				continue
			}
			s := c.sessions[l.Session]
			if s == nil {
				s = &chatSession{id: l.Session}
				c.sessions[l.Session] = s
			}
			switch l.Kind {
			case "started":
				s.started = true
			case "current":
				c.current = l.Session
			case "message":
				s.messages = append(s.messages, Message{Role: l.Role, Text: l.Text, At: l.At})
				if s.title == "" && l.Role == "user" {
					s.title = title(l.Text)
				}
			}
			if l.At.After(s.last) {
				s.last = l.At
			}
		}
		f.Close()
	}
	if c.sessions[c.current] == nil {
		c.mint()
	}
	return c
}

// mint starts a new current session: chat.sessions always lists exactly one
// current session, before any ask too.
func (c *chatStore) mint() string {
	id := newUUID()
	now := time.Now().UTC()
	c.sessions[id] = &chatSession{id: id, last: now}
	c.current = id
	c.append(chatLine{Session: id, Kind: "session", At: now})
	c.append(chatLine{Session: id, Kind: "current", At: now})
	return id
}

func (c *chatStore) append(l chatLine) {
	if c.path == "" {
		return
	}
	if fi, err := os.Stat(c.path); err == nil && fi.Size() > maxChatLog {
		os.Rename(c.path, c.path+".1")
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(c.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	raw, _ := json.Marshal(l)
	f.Write(append(raw, '\n'))
}

// Current is the current session id.
func (c *chatStore) Current() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

// Known reports whether id is a lane session.
func (c *chatStore) Known(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessions[id] != nil
}

// Use makes id the current session.
func (c *chatStore) Use(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessions[id] == nil || c.current == id {
		return
	}
	c.current = id
	c.append(chatLine{Session: id, Kind: "current", At: time.Now().UTC()})
}

// Fresh replaces the current session with a new one (a resume that failed).
func (c *chatStore) Fresh() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mint()
}

// Started records that claude now has a session by that id.
func (c *chatStore) Started(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s := c.sessions[id]; s != nil && !s.started {
		s.started = true
		c.append(chatLine{Session: id, Kind: "started", At: time.Now().UTC()})
	}
}

// IsStarted reports whether claude has a session by that id.
func (c *chatStore) IsStarted(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.sessions[id]
	return s != nil && s.started
}

// Add records a message in a session.
func (c *chatStore) Add(id, role, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.sessions[id]
	if s == nil {
		return
	}
	now := time.Now().UTC()
	s.messages = append(s.messages, Message{Role: role, Text: text, At: now})
	s.last = now
	if s.title == "" && role == "user" {
		s.title = title(text)
	}
	c.append(chatLine{Session: id, Kind: "message", Role: role, Text: text, At: now})
}

// Sessions lists the sessions, newest first, exactly one current.
func (c *chatStore) Sessions() []Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []Session{}
	for _, s := range c.sessions {
		out = append(out, Session{ID: s.id, Title: s.title, Last: s.last, Current: s.id == c.current})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Last.Equal(out[j].Last) {
			return out[i].Last.After(out[j].Last)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Turns is how many questions a session holds.
func (c *chatStore) Turns(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.sessions[id]
	if s == nil {
		return 0
	}
	n := 0
	for _, m := range s.messages {
		if m.Role == "user" {
			n++
		}
	}
	return n
}

// History is a session's messages, nil and false for an unknown session.
func (c *chatStore) History(id string) ([]Message, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.sessions[id]
	if s == nil {
		return nil, false
	}
	return append([]Message{}, s.messages...), true
}

func title(text string) string {
	r := []rune(text)
	if len(r) > 60 {
		return string(r[:59]) + "…"
	}
	return string(r)
}

// newUUID is a random version-4 UUID, the form claude --session-id takes.
// randHex is 128 random bits in hex: an ask id, which is what cancels the
// ask, and a connection's own scope.
func randHex() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// validGroup reports whether a hello group is 128 bits in lowercase hex,
// as a client that drew it at random sends it.
func validGroup(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

// spendLine is drey-spend.json: what asks spent on one day.
type spendLine struct {
	Day string  `json:"day"`
	USD float64 `json:"usd"`
}

func loadSpend(path string) (string, float64) {
	if path == "" {
		return "", 0
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", 0
	}
	var l spendLine
	if json.Unmarshal(raw, &l) != nil || l.Day != time.Now().Format("2006-01-02") {
		return "", 0
	}
	return l.Day, l.USD
}

func saveSpend(path, day string, usd float64) {
	if path == "" {
		return
	}
	raw, _ := json.Marshal(spendLine{Day: day, USD: usd})
	tmp := path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		os.Rename(tmp, path)
	}
}
