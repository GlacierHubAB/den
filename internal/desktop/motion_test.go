package desktop

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A glide never leaves its bounds, wherever it starts or aims, and ends
// exactly on the (clamped) target, at whole pixels.
func TestGlideStaysInBounds(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	b := Rect{X: 960, Y: 0, W: 960, H: 540}
	for i := 0; i < 500; i++ {
		start := [2]float64{r.Float64()*4000 - 1000, r.Float64()*3000 - 1000}
		end := [2]float64{float64(b.X) + r.Float64()*float64(b.W-1), float64(b.Y) + r.Float64()*float64(b.H-1)}
		s := Glide(start, end, b, r, 60)
		for _, p := range s {
			if !b.Contains(p.X, p.Y) {
				t.Fatalf("glide %d left its bounds at %.0f,%.0f", i, p.X, p.Y)
			}
			if p.X != math.Round(p.X) || p.Y != math.Round(p.Y) {
				t.Fatal("not whole pixels")
			}
		}
		last := s[len(s)-1]
		if last.X != math.Round(end[0]) || last.Y != math.Round(end[1]) {
			t.Fatalf("glide %d ends at %.0f,%.0f, not %.0f,%.0f", i, last.X, last.Y, end[0], end[1])
		}
		if last.T > 1.5 {
			t.Fatalf("glide %d takes %.2f s", i, last.T)
		}
	}
	// A seed fixes the path.
	a := Glide([2]float64{1000, 10}, [2]float64{1800, 500}, b, rand.New(rand.NewPCG(7, 7)), 60)
	c := Glide([2]float64{1000, 10}, [2]float64{1800, 500}, b, rand.New(rand.NewPCG(7, 7)), 60)
	if len(a) != len(c) || a[len(a)/2] != c[len(c)/2] {
		t.Fatal("same seed, another path")
	}
}

// The proxy's glide: every step a hypruse move inside the bounds Den
// sent, then the approved click; the pointer file follows, private.
func TestProxyGlides(t *testing.T) {
	g := policyGate()
	inner := g.fn
	g.fn = func(tool string, args json.RawMessage, token string) (Answer, error) {
		a, err := inner(tool, args, token)
		if a.Allow && tool == "pointer" {
			a.Bounds = &Rect{X: 960, Y: 0, W: 960, H: 540}
		}
		return a, err
	}
	pointer := filepath.Join(t.TempDir(), "den-agent-pointer")
	send, f, _ := rigProxy(t, g, func(p *Proxy) {
		p.Cursor = func() (float64, float64, bool) { return 100, 900, true } // outside the window
		p.Pointer = pointer
		p.Seed = 3
	})
	send("initialize", map[string]any{})
	text, isErr := resultText(t, send("tools/call", map[string]any{"name": "pointer", "arguments": map[string]any{"action": "click", "x": 1500, "y": 300}}))
	if isErr {
		t.Fatalf("click: %s", text)
	}
	calls := f.got()
	if len(calls) < 5 {
		t.Fatalf("no glide: %v", calls)
	}
	b := Rect{X: 960, Y: 0, W: 960, H: 540}
	for _, c := range calls[:len(calls)-1] {
		a := c["args"].(map[string]any)
		if a["action"] != "move" || !b.Contains(a["x"].(float64), a["y"].(float64)) {
			t.Fatalf("a step outside the window or not a move: %v", c)
		}
	}
	if last := calls[len(calls)-1]["args"].(map[string]any); last["action"] != "click" {
		t.Fatalf("the click came last? %v", last)
	}
	st, err := os.Stat(pointer)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("pointer file %v %v", st, err)
	}
	raw, _ := os.ReadFile(pointer)
	if strings.TrimSpace(string(raw)) != "1500 300" {
		t.Fatalf("pointer file says %q", raw)
	}
	// Typing hides the outline first.
	old := hideFade
	hideFade = time.Millisecond
	defer func() { hideFade = old }()
	resultText(t, send("tools/call", map[string]any{"name": "keyboard", "arguments": map[string]any{"action": "type", "text": "hi"}}))
	raw, _ = os.ReadFile(pointer)
	if !strings.HasSuffix(strings.TrimSpace(string(raw)), "hidden") {
		t.Fatalf("pointer file while typing: %q", raw)
	}
}

// A step hypruse refuses stops the glide, and the click never happens.
func TestProxyGlideStopsOnRefusal(t *testing.T) {
	g := policyGate()
	inner := g.fn
	g.fn = func(tool string, args json.RawMessage, token string) (Answer, error) {
		a, err := inner(tool, args, token)
		if a.Allow && tool == "pointer" {
			a.Bounds = &Rect{X: 960, Y: 0, W: 960, H: 540}
		}
		return a, err
	}
	send, f, _ := rigProxy(t, g, func(p *Proxy) {
		p.Cursor = func() (float64, float64, bool) { return 1000, 10, true }
	})
	f.mu.Lock()
	f.refuse = "move"
	f.mu.Unlock()
	send("initialize", map[string]any{})
	text, isErr := resultText(t, send("tools/call", map[string]any{"name": "pointer", "arguments": map[string]any{"action": "click", "x": 1800, "y": 500}}))
	if !isErr || !strings.Contains(text, "stopped before acting") {
		t.Fatalf("got %q", text)
	}
	for _, c := range f.got() {
		if c["args"].(map[string]any)["action"] == "click" {
			t.Fatal("clicked after a refused step")
		}
	}
}
