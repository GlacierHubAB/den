package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Layer is a layer-shell surface (a bar, a notification, a launcher, an
// on-screen keyboard): not a window, but it takes clicks over the windows
// under it when it is on the top or overlay layer.
type Layer struct {
	Monitor   string
	Level     int // 0 background, 1 bottom, 2 top, 3 overlay
	X, Y      int
	W, H      int
	Namespace string
	PID       int // the client's process, as Hyprland has it from the kernel
}

// OverlayLevel is the overlay layer, above the top layer.
const OverlayLevel = 3

// PointerNamespace is the layer namespace of the console's pointer
// outline (Drey's, one full-monitor surface per monitor with an empty
// input region): the one surface ExemptConsole lets through.
const PointerNamespace = "drey-agent-pointer"

// ConsoleGlow reports whether the console's overlay (its glow and pointer
// outline, one full-monitor surface per monitor) is on every monitor: a
// surface on the overlay level, named PointerNamespace, whose process is
// one in console, on each monitor of the snapshot. Read it before
// ExemptConsole, which drops those surfaces.
func (s Snapshot) ConsoleGlow(console map[int]bool) bool {
	if len(s.Monitors) == 0 {
		return false
	}
	on := map[string]bool{}
	for _, l := range s.Layers {
		if l.Level == OverlayLevel && l.Namespace == PointerNamespace && l.PID > 1 && console[l.PID] {
			on[l.Monitor] = true
		}
	}
	for _, mon := range s.Monitors {
		if !on[mon.Name] {
			return false
		}
	}
	return true
}

// ExemptConsole drops from the snapshot the console's pointer outline: a
// surface on the overlay level, named PointerNamespace, whose process is
// one in console (the processes of the console's proved den-api.sock
// connections, still the processes that connected). Its input region is
// empty, so a click goes to what is beneath it. Any other surface of the
// console's, or a pointer outline from another process, still refuses.
func (s *Snapshot) ExemptConsole(console map[int]bool) {
	if len(console) == 0 {
		return
	}
	kept := s.Layers[:0:0]
	for _, l := range s.Layers {
		if l.Level == OverlayLevel && l.Namespace == PointerNamespace && l.PID > 1 && console[l.PID] {
			continue
		}
		kept = append(kept, l)
	}
	s.Layers = kept
}

// Covers reports whether a top or overlay layer surface holds the point.
func (l Layer) Covers(x, y float64) bool {
	return l.Level >= 2 && l.W > 0 && l.H > 0 &&
		x >= float64(l.X) && x < float64(l.X+l.W) && y >= float64(l.Y) && y < float64(l.Y+l.H)
}

// Overlaps reports whether a top or overlay layer surface overlaps the
// window's rectangle.
func (l Layer) Overlaps(w Window) bool {
	return l.Level >= 2 && l.W > 0 && l.H > 0 &&
		l.X < w.At[0]+w.Size[0] && w.At[0] < l.X+l.W && l.Y < w.At[1]+w.Size[1] && w.At[1] < l.Y+l.H
}

// Bind is one of the human's Hyprland key binds: its modifier mask (as
// hyprctl reports it) and key.
type Bind struct {
	Mods int
	Key  string
	// Dispatcher and Arg are what the bind runs ("exec", "killactive",
	// ...): task mode refuses a short fixed list of them.
	Dispatcher string
	Arg        string
}

// Modifier bits as Hyprland reports them.
const (
	modShift = 1
	modCaps  = 2
	modCtrl  = 4
	modAlt   = 8
	modNum   = 16 // MOD2, Num Lock
	modSuper = 64
)

// readLayers parses `hyprctl layers -j`: monitor -> levels -> surfaces.
func readLayers(raw []byte) ([]Layer, error) {
	var mons map[string]struct {
		Levels map[string][]struct {
			X         int    `json:"x"`
			Y         int    `json:"y"`
			W         int    `json:"w"`
			H         int    `json:"h"`
			Namespace string `json:"namespace"`
			PID       int    `json:"pid"`
		} `json:"levels"`
	}
	if err := json.Unmarshal(raw, &mons); err != nil {
		return nil, err
	}
	var out []Layer
	for mon, m := range mons {
		for lvl, surfaces := range m.Levels {
			var level int
			if _, err := fmt.Sscanf(lvl, "%d", &level); err != nil {
				return nil, fmt.Errorf("layer level %q", lvl)
			}
			for _, s := range surfaces {
				out = append(out, Layer{Monitor: mon, Level: level, X: s.X, Y: s.Y, W: s.W, H: s.H, Namespace: s.Namespace, PID: s.PID})
			}
		}
	}
	return out, nil
}

// readBinds parses `hyprctl binds -j`. Binds on the mouse or a switch
// cannot be pressed from a keyboard and are left out.
func readBinds(raw []byte) ([]Bind, error) {
	var binds []struct {
		Modmask    int    `json:"modmask"`
		Key        string `json:"key"`
		Mouse      bool   `json:"mouse"`
		Dispatcher string `json:"dispatcher"`
		Arg        string `json:"arg"`
	}
	if err := json.Unmarshal(raw, &binds); err != nil {
		return nil, err
	}
	var out []Bind
	for _, b := range binds {
		k := strings.ToLower(b.Key)
		if b.Mouse || k == "" || strings.HasPrefix(k, "mouse") || strings.HasPrefix(k, "switch:") {
			continue
		}
		out = append(out, Bind{Mods: b.Modmask &^ (modCaps | modNum), Key: keyName(k), Dispatcher: strings.ToLower(strings.TrimSpace(b.Dispatcher)), Arg: b.Arg})
	}
	return out, nil
}

// keyName is the one spelling Den compares keys by.
func keyName(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	switch k {
	case "enter":
		return "return"
	case "esc":
		return "escape"
	case "del":
		return "delete"
	case "pgup":
		return "prior"
	case "pgdn":
		return "next"
	case "space", " ":
		return "space"
	}
	return k
}

// comboMods splits an agent's combo ("ctrl+shift+t") into Hyprland's
// modifier mask and the key.
func comboMods(keys string) (mods int, key string) {
	parts := strings.Split(strings.ToLower(keys), "+")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if i == len(parts)-1 {
			key = keyName(p)
			break
		}
		switch p {
		case "ctrl", "control":
			mods |= modCtrl
		case "shift":
			mods |= modShift
		case "alt":
			mods |= modAlt
		case "super", "logo", "mod4", "win", "meta", "hyper", "cmd":
			mods |= modSuper
		}
	}
	return mods, key
}

// boundCombo says which of the human's binds a combo would trigger, ""
// when none.
func boundCombo(keys string, binds []Bind) string {
	mods, key := comboMods(keys)
	for _, b := range binds {
		if b.Mods == mods && b.Key == key {
			return keys
		}
	}
	return ""
}

// boundText names a character of typed text that is bound with no
// modifier (or with Shift alone, for a capital or a shifted symbol), ""
// when none: typing it would run the human's bind instead.
func boundText(text string, binds []Bind) string {
	for _, b := range binds {
		if b.Mods&^modShift != 0 || len([]rune(b.Key)) != 1 {
			if b.Mods&^modShift == 0 && b.Key == "space" && strings.ContainsRune(text, ' ') {
				return "space"
			}
			continue
		}
		if strings.Contains(strings.ToLower(text), b.Key) {
			return b.Key
		}
	}
	return ""
}

// readExtras adds the layers and the binds to a snapshot.
func readExtras(ctx context.Context, s *Snapshot) error {
	out, err := hyprctl(ctx, "layers", "-j")
	if err != nil {
		return fmt.Errorf("hyprctl layers: %v", err)
	}
	if s.Layers, err = readLayers(out); err != nil {
		return fmt.Errorf("hyprctl layers: %v", err)
	}
	out, err = hyprctl(ctx, "binds", "-j")
	if err != nil {
		return fmt.Errorf("hyprctl binds: %v", err)
	}
	if s.Binds, err = readBinds(out); err != nil {
		return fmt.Errorf("hyprctl binds: %v", err)
	}
	return nil
}
