package desktop

import (
	"encoding/json"
	"math/rand/v2"
	"testing"
	"time"
)

// With hypruse taking 30 ms a move (as measured on Hyprland 0.56), a glide
// skips late steps to keep its planned duration, still makes some moves,
// and the approved click lands exactly on the target.
func TestGlideKeepsItsDuration(t *testing.T) {
	g := policyGate()
	inner := g.fn
	bounds := Rect{X: 960, Y: 0, W: 960, H: 540}
	g.fn = func(tool string, args json.RawMessage, token string) (Answer, error) {
		a, err := inner(tool, args, token)
		if a.Allow && tool == "pointer" {
			a.Bounds = &bounds
		}
		return a, err
	}
	send, f, _ := rigProxy(t, g, func(p *Proxy) {
		p.Cursor = func() (float64, float64, bool) { return 965, 5, true }
		p.Seed = 11
	})
	f.mu.Lock()
	f.slow = 30 * time.Millisecond
	f.mu.Unlock()
	planned := Glide([2]float64{965, 5}, [2]float64{1900, 530}, bounds, rand.New(rand.NewPCG(11, 11^0x9e3779b97f4a7c15)), 60)
	plan := time.Duration(planned[len(planned)-1].T * float64(time.Second))
	send("initialize", map[string]any{})
	began := time.Now()
	text, isErr := resultText(t, send("tools/call", map[string]any{"name": "pointer", "arguments": map[string]any{"action": "click", "x": 1900, "y": 530}}))
	took := time.Since(began)
	if isErr {
		t.Fatalf("click: %s", text)
	}
	calls := f.got()
	moves := len(calls) - 1
	if moves < 3 || moves >= len(planned)-1 {
		t.Fatalf("%d moves of %d planned steps: nothing skipped, or nothing moved", moves, len(planned)-1)
	}
	if took > plan+600*time.Millisecond { // generous: CI runs loaded, with -race
		t.Fatalf("the glide took %v, planned %v", took, plan)
	}
	last := calls[len(calls)-1]["args"].(map[string]any)
	if last["action"] != "click" || last["x"] != 1900.0 || last["y"] != 530.0 {
		t.Fatalf("the click did not land on the target: %v", last)
	}
}
