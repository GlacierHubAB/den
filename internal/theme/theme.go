// Package theme loads Den's colours from the current Omarchy theme, the same
// way agent-badge does, and falls back to the Deepharness palette.
//
// Deepharness rule: surfaces are near black, text is a white opacity ladder,
// and hue is reserved for status.
package theme

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Palette is the small token set the UI is built from.
type Palette struct {
	Bg, Surface, Sel string
	Fg, FgDim, Muted string
	Bright           string
	Ok, Attn, Err    string

	// Bodies are the critter body colours, in agent-badge's order; Blush
	// is the cheek colour. Both mirror the badge so the faces match.
	Bodies []string
	Blush  string
}

// Deepharness is the fallback palette.
var Deepharness = Palette{
	Bg: "#000000", Surface: "#0d0d0d", Sel: "#2e2e2e",
	Fg: "#dfdfdf", FgDim: "#7c7c7c", Muted: "#575757",
	Bright: "#f8f8f8",
	Ok:     "#66cb8e", Attn: "#e8b84a", Err: "#ea9e9e",
	Bodies: []string{"#66cb8e", "#e8b84a", "#ea9e9e"}, Blush: "#f4c4c4",
}

// Surface2 is the raised surface used for inactive borders and tracks.
func (p Palette) Surface2() string {
	if p.Sel != "" {
		return blend(p.Bg, p.Sel)
	}
	return p.Surface
}

// blend returns the midpoint of two hex colours (#rrggbb).
func blend(a, b string) string {
	pa, okA := parseHex(a)
	pb, okB := parseHex(b)
	if !okA || !okB {
		return a
	}
	return fmt.Sprintf("#%02x%02x%02x", (pa[0]+pb[0])/2, (pa[1]+pb[1])/2, (pa[2]+pb[2])/2)
}

func parseHex(h string) ([3]int, bool) {
	var out [3]int
	h = strings.TrimPrefix(h, "#")
	if len(h) != 6 {
		return out, false
	}
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseInt(h[i*2:i*2+2], 16, 32)
		if err != nil {
			return out, false
		}
		out[i] = int(v)
	}
	return out, true
}

// paletteKeys is the order agent-badge picks body colours from.
var paletteKeys = []string{
	"red", "yellow", "orange", "green", "cyan", "blue", "magenta",
	"bright_red", "bright_yellow", "bright_green", "bright_cyan", "bright_blue", "bright_magenta",
	"accent",
}

func luminance(hex string) float64 {
	c, ok := parseHex(hex)
	if !ok {
		return 0
	}
	return 0.2126*float64(c[0]) + 0.7152*float64(c[1]) + 0.0722*float64(c[2])
}

// ColorsFile is the Omarchy theme file Den reads.
func ColorsFile() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "omarchy", "current", "theme", "colors.toml")
}

// Load returns the Omarchy palette when present, otherwise Deepharness.
func Load() Palette {
	p := Deepharness
	f, err := os.Open(ColorsFile())
	if err != nil {
		return p
	}
	defer f.Close()
	kv := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"`)
		if strings.HasPrefix(v, "#") {
			kv[strings.TrimSpace(k)] = v
		}
	}
	set := func(dst *string, keys ...string) {
		for _, k := range keys {
			if v, ok := kv[k]; ok {
				*dst = v
				return
			}
		}
	}
	set(&p.Bg, "background")
	set(&p.Surface, "dark_background", "lighter_background")
	set(&p.Sel, "selection_background", "selection")
	set(&p.Fg, "foreground")
	set(&p.FgDim, "dark_foreground")
	set(&p.Muted, "muted")
	set(&p.Bright, "bright_foreground", "accent")
	set(&p.Ok, "green")
	set(&p.Attn, "yellow", "orange")
	set(&p.Err, "red")
	// Body colours: every palette hue that stands out against the background.
	var bodies []string
	seen := map[string]bool{}
	for _, k := range paletteKeys {
		v, ok := kv[k]
		if !ok || seen[v] {
			continue
		}
		seen[v] = true
		if d := luminance(v) - luminance(p.Bg); d > 60 || d < -60 {
			bodies = append(bodies, v)
		}
	}
	if len(bodies) > 0 {
		p.Bodies = bodies
	}
	set(&p.Blush, "bright_red", "red")
	return p
}
