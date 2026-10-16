// Package hypr talks to Hyprland through hyprctl: list windows, find the one
// a session lives in, and focus it.
//
// Ghostty runs every window in one process, so pid ancestry cannot tell
// windows apart. Den matches on the window title instead: Claude Code sets
// the terminal title to its AI-generated session title behind a status
// glyph, and that title is also recorded in the transcript.
package hypr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/GlacierHubAB/den/internal/procs"
	"github.com/GlacierHubAB/den/internal/sanitize"
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
func Clients() ([]Client, error) { return ClientsContext(context.Background()) }

// ClientsContext is Clients that gives up when ctx ends.
func ClientsContext(ctx context.Context) ([]Client, error) {
	out, err := exec.CommandContext(ctx, "hyprctl", "clients", "-j").Output()
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
	if err := checkAddress(address); err != nil {
		return err
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
	if err := checkAddress(address); err != nil {
		return err
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

// checkAddress refuses anything but what Hyprland gives a window, "0x" and
// up to 16 hex digits: an address is put into a dispatcher's text, and it
// comes from a list the compositor wrote, so nothing else belongs there.
func checkAddress(a string) error {
	if a == "" {
		return errors.New("no window address")
	}
	if !addressRE.MatchString(a) {
		return errors.New("not a window address")
	}
	return nil
}

var addressRE = regexp.MustCompile(`^0x[0-9a-f]{1,16}$`)

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
	return ActiveWorkspaceContext(context.Background())
}

// MoveToWorkspace moves a window to a workspace (by id or name) without
// changing which workspace is shown: the focus stays where it is, and a
// special workspace stays hidden. Lua first (Hyprland 0.56+, whose move takes
// follow = false for that), then the classic movetoworkspacesilent.
func MoveToWorkspace(address, workspace string) error {
	return MoveToWorkspaceContext(context.Background(), address, workspace)
}

// MoveToWorkspaceContext is MoveToWorkspace that gives up when ctx ends.
func MoveToWorkspaceContext(ctx context.Context, address, workspace string) error {
	if err := checkAddress(address); err != nil {
		return err
	}
	lua := fmt.Sprintf(`hl.dsp.window.move({ workspace = %q, window = "address:%s", follow = false })`, workspace, address)
	out, err := exec.CommandContext(ctx, "hyprctl", "dispatch", lua).CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if err == nil && (msg == "ok" || msg == "") {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	out, err = exec.CommandContext(ctx, "hyprctl", "dispatch", "movetoworkspacesilent", workspace+",address:"+address).CombinedOutput()
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

// MarkerHolds reports whether pid runs in the one window that carries
// marker (its initial class): exactly one window has it, its terminal
// process has no other window, and pid descends from that process. When
// any of that fails Den cannot tell whose window it is, and the answer is
// false.
func MarkerHolds(marker string, pid int) bool {
	if marker == "" || pid <= 1 || !Available() {
		return false
	}
	all, err := Clients()
	if err != nil {
		return false
	}
	return markerHolds(all, marker, procs.Ancestors(pid))
}

func markerHolds(all []Client, marker string, chain []procs.Proc) bool {
	_, ok := HeldWindow(all, marker, chain)
	return ok
}

// AgentTitle is the window title Den gives the terminal of the agent named
// peer when it spawns one.
func AgentTitle(peer string) string { return sanitize.Line("den · " + peer) }

// TitledWindow finds the window of an agent that has no marker, by the
// title Den gave it at spawn (AgentTitle): the one window with exactly that
// title, whose terminal process has no other window and is one the agent's
// process (chain lists the processes from it upwards) descends from. It is
// for a window that is only to be hidden and shown again, never for
// anything that cannot be undone: a program can set its window's title, so
// the title alone proves nothing, and that the agent runs under the
// terminal is what ties the two. No window, two, or one run by someone
// else gives false.
func TitledWindow(all []Client, title string, chain []procs.Proc) (Client, bool) {
	if title == "" {
		return Client{}, false
	}
	var found []Client
	for _, c := range all {
		if c.Title == title {
			found = append(found, c)
		}
	}
	if len(found) != 1 || found[0].PID <= 1 {
		return Client{}, false
	}
	for _, c := range all {
		if c.PID == found[0].PID && c.Address != found[0].Address {
			return Client{}, false
		}
	}
	for _, p := range chain {
		if p.PID == found[0].PID {
			return found[0], true
		}
	}
	return Client{}, false
}

// HeldWindow is the window marker names when it is provably the agent's
// own: it is the one window with that initial class, its terminal process
// has no other window, and the agent's process (chain lists the processes
// from it upwards) descends from that terminal. Anything else, such as
// no window, two, or a marked window run by someone else, gives false.
func HeldWindow(all []Client, marker string, chain []procs.Proc) (Client, bool) {
	if marker == "" {
		return Client{}, false
	}
	var found []Client
	for _, c := range all {
		if c.InitialClass == marker {
			found = append(found, c)
		}
	}
	if len(found) != 1 || found[0].PID <= 1 {
		return Client{}, false
	}
	for _, c := range all {
		if c.PID == found[0].PID && c.Address != found[0].Address {
			return Client{}, false
		}
	}
	for _, p := range chain {
		if p.PID == found[0].PID {
			return found[0], true
		}
	}
	return Client{}, false
}

// OwnWindow is the window Den itself runs in: the one window of a process
// above Den's (chain lists them, nearest first), when that terminal
// process has no other window. It is the rule the closer uses to never
// close Den's own window. When no window, or more than one, qualifies, Den
// cannot say which is its own, and the answer is false.
func OwnWindow(all []Client, chain []procs.Proc) (Client, bool) {
	var found []Client
	for _, c := range all {
		if c.PID <= 1 {
			continue
		}
		above := false
		for _, p := range chain {
			if p.PID == c.PID {
				above = true
				break
			}
		}
		if !above {
			continue
		}
		n := 0
		for _, o := range all {
			if o.PID == c.PID {
				n++
			}
		}
		if n == 1 {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		return Client{}, false
	}
	return found[0], true
}

// ActiveWorkspaceContext is ActiveWorkspace that gives up when ctx ends.
func ActiveWorkspaceContext(ctx context.Context) (ws Workspace, ok bool) {
	out, err := exec.CommandContext(ctx, "hyprctl", "activeworkspace", "-j").Output()
	if err != nil {
		return Workspace{}, false
	}
	if json.Unmarshal(out, &ws) != nil || ws.ID == 0 {
		return Workspace{}, false
	}
	return ws, true
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
