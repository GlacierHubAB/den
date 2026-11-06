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
	Address   string // Hyprland's, for telling a surface that opened later
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
// one in console, on each enabled monitor of the snapshot that mirrors no
// other. Read it before ExemptConsole, which drops those surfaces.
func (s Snapshot) ConsoleGlow(console map[int]bool) bool {
	on, own := map[string]bool{}, 0
	for _, l := range s.Layers {
		if l.Level == OverlayLevel && l.Namespace == PointerNamespace && l.PID > 1 && console[l.PID] {
			on[l.Monitor] = true
		}
	}
	for _, mon := range s.Monitors {
		if mon.Mirror {
			continue
		}
		own++
		if !on[mon.Name] {
			return false
		}
	}
	return own > 0
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

// Under reports whether the point is on the surface, whatever its level.
func (l Layer) Under(x, y float64) bool {
	return l.W > 0 && l.H > 0 && x >= float64(l.X) && x < float64(l.X+l.W) && y >= float64(l.Y) && y < float64(l.Y+l.H)
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
			Address   string `json:"address"`
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
				out = append(out, Layer{Address: s.Address, Monitor: mon, Level: level, X: s.X, Y: s.Y, W: s.W, H: s.H, Namespace: s.Namespace, PID: s.PID})
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
		if k == "" || strings.HasPrefix(k, "switch:") {
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
	if rs := []rune(k); len(rs) == 1 {
		if p, ok := punct[rs[0]]; ok {
			return p[0]
		}
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

// boundText names a key of typed text that one of the human's binds
// with no modifier (or with Shift alone, for a capital or a shifted
// symbol) is on, "" when none: typing it would run the bind instead.
// Each character is matched by its keysym names (its own, and the key it
// is shifted from on a US layout). When such a bind is on a key Den can't
// map to a character (a dead key, "odiaeresis", a code:), only letters,
// digits, space, Tab and Return may be typed, and with a code: bind
// nothing may.
func boundText(text string, binds []Bind) string {
	unmapped, code := false, false
	on := map[string]bool{}
	for _, b := range binds {
		if b.Mods&^modShift != 0 {
			continue
		}
		on[b.Key] = true
		switch {
		case strings.HasPrefix(b.Key, "code:"):
			code = true
		case !typable[b.Key] && !untypable(b.Key):
			unmapped = true
		}
	}
	if len(on) == 0 {
		return ""
	}
	if code && text != "" {
		return "a key bound by its code"
	}
	for _, r := range text {
		for _, k := range charKeys(r) {
			if on[k] {
				return k
			}
		}
		if unmapped && !plain(r) {
			return string(r) + " (a bind is on a key Den can't map to a character)"
		}
	}
	return ""
}

// plain is a character every layout types without a dead key or a level
// Den can't see.
func plain(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

// punct is each ASCII symbol's keysym name, and the key it is typed with
// Shift on a US layout ("" when it has its own key).
var punct = map[rune][2]string{
	'-': {"minus", ""}, '=': {"equal", ""}, '[': {"bracketleft", ""}, ']': {"bracketright", ""},
	'\\': {"backslash", ""}, ';': {"semicolon", ""}, '\'': {"apostrophe", ""}, '`': {"grave", ""},
	',': {"comma", ""}, '.': {"period", ""}, '/': {"slash", ""},
	'!': {"exclam", "1"}, '@': {"at", "2"}, '#': {"numbersign", "3"}, '$': {"dollar", "4"},
	'%': {"percent", "5"}, '^': {"asciicircum", "6"}, '&': {"ampersand", "7"}, '*': {"asterisk", "8"},
	'(': {"parenleft", "9"}, ')': {"parenright", "0"}, '_': {"underscore", "minus"}, '+': {"plus", "equal"},
	'{': {"braceleft", "bracketleft"}, '}': {"braceright", "bracketright"}, '|': {"bar", "backslash"},
	':': {"colon", "semicolon"}, '"': {"quotedbl", "apostrophe"}, '~': {"asciitilde", "grave"},
	'<': {"less", "comma"}, '>': {"greater", "period"}, '?': {"question", "slash"},
}

// typable are the key names a typed character can produce.
var typable = func() map[string]bool {
	t := map[string]bool{"space": true, "return": true, "tab": true}
	for r := 'a'; r <= 'z'; r++ {
		t[string(r)] = true
	}
	for r := '0'; r <= '9'; r++ {
		t[string(r)] = true
	}
	for _, p := range punct {
		t[p[0]] = true
	}
	return t
}()

// untypable are keys no typed text reaches: modifiers, function and
// navigation keys, media keys.
func untypable(k string) bool {
	for _, p := range []string{"xf86", "super_", "alt_", "control_", "shift_", "meta_", "hyper_"} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	if len(k) >= 2 && k[0] == 'f' && strings.Trim(k[1:], "0123456789") == "" {
		return true
	}
	switch k {
	case "escape", "delete", "backspace", "insert", "home", "end", "prior", "next", "up", "down", "left", "right",
		"print", "pause", "menu", "caps_lock", "num_lock", "scroll_lock", "sys_req", "break":
		return true
	}
	return strings.HasPrefix(k, "mouse") // mouse binds are the pointer's
}

// charKeys are the key names a typed character is matched by.
func charKeys(r rune) []string {
	switch {
	case r == ' ':
		return []string{"space"}
	case r == '\n' || r == '\r':
		return []string{"return"}
	case r == '\t':
		return []string{"tab"}
	case r >= 'A' && r <= 'Z':
		return []string{string(r + 'a' - 'A')}
	case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
		return []string{string(r)}
	}
	if p, ok := punct[r]; ok {
		if p[1] != "" {
			return []string{p[0], p[1]}
		}
		return []string{p[0]}
	}
	return []string{strings.ToLower(string(r))}
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
