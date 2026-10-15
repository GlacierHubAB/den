package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// FilterDesktop rewrites hypruse's desktop snapshot so the agent sees only
// its mission: the monitor showing the mission workspace, that workspace,
// the mission's windows, the focused window only when it is one of them,
// and the cursor only when it is on that monitor. Layers (bars,
// notifications, launchers) are dropped. A snapshot that does not parse as
// hypruse 0.12.1 writes it is an error: the proxy then denies.
func FilterDesktop(raw []byte, m Mission, s Snapshot) ([]byte, error) {
	var snap struct {
		Monitors []struct {
			Name string `json:"name"`
		} `json:"monitors"`
		Workspaces []map[string]any `json:"workspaces"`
		Windows    []struct {
			Address string `json:"address"`
		} `json:"windows"`
		Active string    `json:"active_window"`
		Cursor []float64 `json:"cursor"`
	}
	var full map[string]json.RawMessage
	if err := json.Unmarshal(raw, &full); err != nil {
		return nil, fmt.Errorf("desktop: not a JSON object: %v", err)
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("desktop: unexpected shape: %v", err)
	}
	var monitors, windows []json.RawMessage
	var rawMons, rawWins []json.RawMessage
	if json.Unmarshal(full["monitors"], &rawMons) != nil || json.Unmarshal(full["windows"], &rawWins) != nil ||
		len(rawMons) != len(snap.Monitors) || len(rawWins) != len(snap.Windows) {
		return nil, errors.New("desktop: unexpected shape")
	}
	mon, shown := s.Shown(m.Workspace)
	for i, mm := range snap.Monitors {
		if shown && mm.Name == mon.Name {
			monitors = append(monitors, rawMons[i])
		}
	}
	for i, w := range snap.Windows {
		if sw, ok := s.Window(w.Address); ok && m.InScope(sw) {
			windows = append(windows, rawWins[i])
		}
	}
	var workspaces []map[string]any
	for _, ws := range snap.Workspaces {
		if fmt.Sprint(ws["id"]) == fmt.Sprint(m.Workspace) {
			workspaces = append(workspaces, ws)
		}
	}
	out := map[string]any{
		"monitors":      nonNil(monitors),
		"workspaces":    workspaces,
		"windows":       nonNil(windows),
		"active_window": nil,
		"cursor":        nil,
		"note":          fmt.Sprintf("Den shows only this mission: workspace %d and its windows.", m.Workspace),
	}
	if workspaces == nil {
		out["workspaces"] = []any{}
	}
	if aw, ok := s.Window(snap.Active); ok && m.InScope(aw) && snap.Active == s.Active {
		out["active_window"] = snap.Active
	}
	if len(snap.Cursor) == 2 && shown && mon.Contains(snap.Cursor[0], snap.Cursor[1]) {
		out["cursor"] = snap.Cursor
	}
	return json.Marshal(out)
}

func nonNil(v []json.RawMessage) []json.RawMessage {
	if v == nil {
		return []json.RawMessage{}
	}
	return v
}

// MaxChunk is the most characters the proxy types in one forwarded call:
// hypruse focuses the window, checks it, then types, so a focus change
// mid-text loses at most one chunk.
const MaxChunk = 32

// Piece is one forwarded keyboard step of a typed text: text, or an Enter
// key press for a line break.
type Piece struct {
	Text  string
	Enter bool
}

// Chunks splits typed text into pieces of at most MaxChunk characters,
// with every line break its own Enter press ("\r\n", "\r" and "\n" alike).
func Chunks(text string) []Piece {
	var out []Piece
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			out = append(out, Piece{Enter: true})
		}
		for line != "" {
			n, cut := 0, len(line)
			for j := range line {
				if n == MaxChunk {
					cut = j
					break
				}
				n++
			}
			out = append(out, Piece{Text: line[:cut]})
			line = line[cut:]
		}
	}
	return out
}

// Describe is the human's view of typed text on a card: line breaks shown
// as ⏎, cut to n characters.
func Describe(text string, n int) string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	text = strings.ReplaceAll(text, "\n", "⏎")
	if utf8.RuneCountInString(text) > n {
		r := []rune(text)
		text = string(r[:n-1]) + "…"
	}
	return text
}
