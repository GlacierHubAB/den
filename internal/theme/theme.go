// Package theme loads Den's colours from the current Omarchy theme, the same
// way agent-badge does, and falls back to the Deepharness palette.
//
// Deepharness rule: surfaces are near black, text is a white opacity ladder,
// and hue is reserved for status.
package theme

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Palette is the small token set the UI is built from.
type Palette struct {
	Bg, Surface, Sel      string
	Fg, FgDim, Muted      string
	Bright                string
	Ok, Attn, Err         string
}

// Deepharness is the fallback palette.
var Deepharness = Palette{
	Bg: "#000000", Surface: "#0d0d0d", Sel: "#2e2e2e",
	Fg: "#dfdfdf", FgDim: "#7c7c7c", Muted: "#575757",
	Bright: "#f8f8f8",
	Ok: "#66cb8e", Attn: "#e8b84a", Err: "#ea9e9e",
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
	return p
}
