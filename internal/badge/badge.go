// Package badge reads the identity that the agent-badge mod gives each
// Claude Code session: a procedurally generated critter name seeded from the
// session id, plus the role, current task and earlier tasks the mod labelled
// and saved in Claude Code's plugin store.
//
// The name generator mirrors agent-badge's hooks/register.tsx so both tools
// agree on who an agent is.
package badge

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
)

var adjectives = []string{
	"Mossy", "Fizzy", "Sleepy", "Brave", "Tiny", "Cosmic", "Fuzzy", "Zippy", "Wobbly", "Sunny",
	"Misty", "Plucky", "Snug", "Bouncy", "Quiet", "Spicy", "Velvet", "Pebble", "Comet", "Maple",
}

var critters = []string{
	"Otter", "Moth", "Newt", "Fox", "Owl", "Axolotl", "Panda", "Gecko", "Quokka", "Hedgehog",
	"Puffin", "Wombat", "Ferret", "Koala", "Lemur", "Capybara", "Badger", "Narwhal", "Sprite", "Tanuki",
}

// Labels is what agent-badge stored for one session.
type Labels struct {
	Role    string   `json:"role"`
	Task    string   `json:"task"`
	History []string `json:"history"`
}

// Name returns the critter name agent-badge shows for a session id.
func Name(sessionID string) string {
	r := rng(fnv1a(sessionID) ^ 0x9e3779b9)
	a := adjectives[int(math.Floor(r()*float64(len(adjectives))))%len(adjectives)]
	c := critters[int(math.Floor(r()*float64(len(critters))))%len(critters)]
	return a + " " + c
}

// fnv1a is the 32-bit FNV-1a hash agent-badge uses, over UTF-16 code units.
func fnv1a(text string) uint32 {
	acc := uint32(0x811c9dc5)
	for _, r := range text {
		acc ^= uint32(r) // session ids are ASCII, so code unit == rune
		acc *= 0x01000193
	}
	return acc
}

// rng is the mulberry32 variant from agent-badge, bit for bit.
func rng(seed uint32) func() float64 {
	a := seed
	if a == 0 {
		a = 1
	}
	return func() float64 {
		a += 0x6d2b79f5
		t := (a ^ (a >> 15)) * (1 | a)
		t = (t + (t^(t>>7))*(61|t)) ^ t
		return float64(t^(t>>14)) / 4294967296
	}
}

// StoreDir is where Claude Code keeps plugin stores.
func StoreDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "plugins", "store")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "plugins", "store")
}

// Load reads every agent-badge store file and returns labels by session id.
// A missing store is not an error: the mod may not be installed.
func Load() (map[string]Labels, error) {
	matches, _ := filepath.Glob(filepath.Join(StoreDir(), "agent-badge_*.json"))
	out := map[string]Labels{}
	for _, m := range matches {
		raw, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var store map[string]json.RawMessage
		if err := json.Unmarshal(raw, &store); err != nil {
			continue
		}
		for key, v := range store {
			id, ok := strings.CutPrefix(key, "badge:")
			if !ok {
				continue
			}
			var l Labels
			if err := json.Unmarshal(v, &l); err == nil {
				out[id] = l
			}
		}
	}
	return out, nil
}

// Usage is what agent-badge records about a session's context window, rate
// limits and spend (stored under "usage:<sessionId>" by agent-badge 1.1+).
type Usage struct {
	Context        *float64 `json:"context"`
	Filled         *float64 `json:"filled"`
	Window         *float64 `json:"window"`
	FiveHour       *float64 `json:"fiveHour"`
	FiveHourResets string   `json:"fiveHourResets"`
	SevenDay       *float64 `json:"sevenDay"`
	SevenDayResets string   `json:"sevenDayResets"`
	USD            *float64 `json:"usd"`
	Now            int64    `json:"now"`
}

// LoadUsage reads every agent-badge store file and returns usage by session id.
func LoadUsage() map[string]Usage {
	matches, _ := filepath.Glob(filepath.Join(StoreDir(), "agent-badge_*.json"))
	out := map[string]Usage{}
	for _, m := range matches {
		raw, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var store map[string]json.RawMessage
		if err := json.Unmarshal(raw, &store); err != nil {
			continue
		}
		for key, v := range store {
			id, ok := strings.CutPrefix(key, "usage:")
			if !ok {
				continue
			}
			var u Usage
			if err := json.Unmarshal(v, &u); err == nil {
				out[id] = u
			}
		}
	}
	return out
}
