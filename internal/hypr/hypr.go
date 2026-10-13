// Package hypr talks to Hyprland through hyprctl: list windows, find the one
// a session lives in, and focus it.
//
// Ghostty runs every window in one process, so pid ancestry cannot tell
// windows apart. Den matches on the window title instead: Claude Code sets
// the terminal title to its AI-generated session title behind a status
// glyph, and that title is also recorded in the transcript.
package hypr

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"unicode"

	"github.com/GlacierHubAB/den/internal/procs"
)

// Client is one Hyprland window.
type Client struct {
	Address string `json:"address"`
	PID     int    `json:"pid"`
	Class   string `json:"class"`
	// InitialClass is the class the window was created with; a program
	// cannot change it afterwards, so Den's marker is read from here.
	InitialClass string `json:"initialClass"`
	Title        string `json:"title"`
	FocusHist    int    `json:"focusHistoryID"`
	Workspace    struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"workspace"`
}

// Available reports whether hyprctl can be used in this environment.
func Available() bool {
	if os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") == "" {
		return false
	}
	_, err := exec.LookPath("hyprctl")
	return err == nil
}

// Clients lists windows.
func Clients() ([]Client, error) {
	out, err := exec.Command("hyprctl", "clients", "-j").Output()
	if err != nil {
		return nil, err
	}
	var cs []Client
	if err := json.Unmarshal(out, &cs); err != nil {
		return nil, err
	}
	return cs, nil
}

// Focus switches to the window's workspace and focuses it.
//
// Hyprland 0.56+ takes dispatchers as Lua (`hl.dsp.focus({ window = "address:…" })`);
// older releases take the classic `focuswindow address:…` form. Den tries
// the Lua form first and falls back when the compositor does not speak it.
func Focus(address string) error {
	if address == "" {
		return errors.New("no window address")
	}
	lua := fmt.Sprintf(`hl.dsp.focus({ window = "address:%s" })`, address)
	out, err := exec.Command("hyprctl", "dispatch", lua).CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if err == nil && (msg == "ok" || msg == "") {
		return nil
	}
	if strings.Contains(msg, "window not found") {
		return errors.New("hyprland: window not found (it may have closed)")
	}
	// Not a Lua-speaking Hyprland: use the legacy dispatcher.
	out, err = exec.Command("hyprctl", "dispatch", "focuswindow", "address:"+address).CombinedOutput()
	msg = strings.TrimSpace(string(out))
	if err != nil || (msg != "ok" && msg != "") {
		return fmt.Errorf("hyprctl focuswindow: %s", firstLine(msg))
	}
	return nil
}

// CloseWindow asks Hyprland to close one window by address, the Lua
// dispatcher first and the legacy one as the fallback, like Focus. A window
// that is already gone is reported as not found.
func CloseWindow(address string) error {
	if address == "" {
		return errors.New("no window address")
	}
	lua := fmt.Sprintf(`hl.dsp.window.close({ window = "address:%s" })`, address)
	out, err := exec.Command("hyprctl", "dispatch", lua).CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if err == nil && (msg == "ok" || msg == "") {
		return nil
	}
	if strings.Contains(msg, "window not found") {
		return errors.New("hyprland: window not found (it may have closed)")
	}
	out, err = exec.Command("hyprctl", "dispatch", "closewindow", "address:"+address).CombinedOutput()
	msg = strings.TrimSpace(string(out))
	if err != nil || (msg != "ok" && msg != "") {
		if strings.Contains(msg, "not found") {
			return errors.New("hyprland: window not found (it may have closed)")
		}
		return fmt.Errorf("hyprctl closewindow: %s", firstLine(msg))
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// MarkerPrefix starts the window class Den gives the terminal of an agent
// it spawns; the rest is the agent's peer name.
const MarkerPrefix = "io.glacierhub.den."

// Marker is the window class for the terminal of the agent with this peer
// name: lower case letters, digits and dashes, at most 40 of them after
// the prefix. "" when nothing is left of the name.
func Marker(peer string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(peer) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	name := strings.TrimRight(b.String(), "-")
	if len(name) > 40 {
		name = strings.TrimRight(name[:40], "-")
	}
	if name == "" {
		return ""
	}
	return MarkerPrefix + name
}

// ActiveAddress returns the focused window's address.
func ActiveAddress() string {
	c, _ := Active()
	return c.Address
}

// Active returns the focused window.
func Active() (Client, bool) {
	out, err := exec.Command("hyprctl", "activewindow", "-j").Output()
	if err != nil {
		return Client{}, false
	}
	var c Client
	if json.Unmarshal(out, &c) != nil || c.Address == "" {
		return Client{}, false
	}
	return c, true
}

// StripStatusGlyph removes Claude Code's leading status symbol from a title,
// so "✳ Fix login" and "◑ Fix login" both become "Fix login".
func StripStatusGlyph(title string) string {
	t := strings.TrimSpace(title)
	for len(t) > 0 {
		r := []rune(t)[0]
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '~' || r == '/' || r == '.' || r == '@' || r == '"' || r == '\'' || r == '(' || r == '[' {
			break
		}
		t = strings.TrimSpace(string([]rune(t)[1:]))
	}
	return t
}

// IsTerminal reports whether a window class looks like a terminal emulator.
func IsTerminal(class string) bool {
	c := strings.ToLower(class)
	for _, k := range []string{"ghostty", "alacritty", "kitty", "foot", "wezterm", "org.wezfurlong", "konsole", "gnome-terminal", "xterm", "st-", "rio", "tilix", "terminator"} {
		if strings.Contains(c, k) {
			return true
		}
	}
	return false
}

// Match describes which window a session was found in.
type Match struct {
	Client Client
	How    string // "title", "cwd" or "herdr"
}

// Find looks for the terminal window showing a session. title is the
// session's transcript title (may be ""), cwd its short working directory,
// and hostPrefix the "<hostname>: " herdr puts in its window title when the
// session runs inside herdr ("" otherwise).
func Find(clients []Client, title, cwd, hostPrefix string) (Match, bool) {
	var terms []Client
	for _, c := range clients {
		if IsTerminal(c.Class) {
			terms = append(terms, c)
		}
	}
	// Most recently focused first, so ties resolve to the window you used last.
	sort.Slice(terms, func(i, j int) bool { return terms[i].FocusHist < terms[j].FocusHist })

	if hostPrefix != "" {
		for _, c := range terms {
			if strings.HasPrefix(c.Title, hostPrefix) {
				return Match{Client: c, How: "herdr"}, true
			}
		}
	}
	if title != "" {
		want := strings.ToLower(title)
		for _, c := range terms {
			if strings.ToLower(StripStatusGlyph(c.Title)) == want {
				return Match{Client: c, How: "title"}, true
			}
		}
	}
	if cwd != "" {
		for _, c := range terms {
			if c.Title == cwd || strings.HasSuffix(c.Title, " "+cwd) {
				return Match{Client: c, How: "cwd"}, true
			}
		}
	}
	return Match{}, false
}

// Workspace is one Hyprland workspace as `hyprctl activeworkspace -j` reports it.
type Workspace struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Monitor string `json:"monitor"`
}

// ActiveWorkspace returns the workspace the human is looking at, or ok false
// when Hyprland cannot be asked.
func ActiveWorkspace() (ws Workspace, ok bool) {
	out, err := exec.Command("hyprctl", "activeworkspace", "-j").Output()
	if err != nil {
		return Workspace{}, false
	}
	if json.Unmarshal(out, &ws) != nil || ws.ID == 0 {
		return Workspace{}, false
	}
	return ws, true
}

// MoveToWorkspace moves a window to a workspace (by id or name) without
// changing which workspace is shown. Lua first (Hyprland 0.56+), then the
// classic dispatcher.
func MoveToWorkspace(address, workspace string) error {
	if address == "" {
		return errors.New("no window address")
	}
	lua := fmt.Sprintf(`hl.dsp.window.move({ workspace = %q, window = "address:%s", silent = true })`, workspace, address)
	out, err := exec.Command("hyprctl", "dispatch", lua).CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if err == nil && (msg == "ok" || msg == "") {
		return nil
	}
	out, err = exec.Command("hyprctl", "dispatch", "movetoworkspacesilent", workspace+",address:"+address).CombinedOutput()
	msg = strings.TrimSpace(string(out))
	if err != nil || (msg != "ok" && msg != "") {
		return fmt.Errorf("hyprctl movetoworkspacesilent: %s", firstLine(msg))
	}
	return nil
}

// NewTerminal returns the terminal window that is in after but not in
// before, if exactly one appeared.
func NewTerminal(before, after []Client) (Client, bool) {
	seen := map[string]bool{}
	for _, c := range before {
		seen[c.Address] = true
	}
	var found []Client
	for _, c := range after {
		if !seen[c.Address] && IsTerminal(c.Class) {
			found = append(found, c)
		}
	}
	if len(found) == 1 {
		return found[0], true
	}
	return Client{}, false
}

// ActiveHolds reports whether the focused window belongs to one of pid's
// ancestors: the terminal a process runs in is in front. When that terminal
// process owns several windows (Ghostty runs them all in one) or a
// multiplexer breaks the chain, Den cannot tell, and the answer is false.
func ActiveHolds(pid int) bool {
	if !Available() {
		return false
	}
	c, ok := Active()
	if !ok || c.PID <= 1 {
		return false
	}
	all, err := Clients()
	if err != nil {
		return false
	}
	n := 0
	for _, o := range all {
		if o.PID == c.PID {
			n++
		}
	}
	if n != 1 {
		return false
	}
	for _, p := range procs.Ancestors(pid) {
		if p.PID == c.PID {
			return true
		}
	}
	return false
}
