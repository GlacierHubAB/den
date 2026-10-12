package remote

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/GlacierHubAB/den/internal/config"
)

// Policy is how much a remote host may send (docs/REMOTE.md).
type Policy string

const (
	Observe Policy = "observe" // metadata only
	Approve Policy = "approve" // plus the body of a held permission, plan or question
	Full    Policy = "full"    // plus Tail, titles, cwd and diffs (remote Review)
)

// rank orders policies by how much they let through; 0 is unknown.
func (p Policy) rank() int {
	switch p {
	case Observe:
		return 1
	case Approve:
		return 2
	case Full:
		return 3
	}
	return 0
}

// Valid reports whether p is one of the three policies.
func (p Policy) Valid() bool { return p.rank() > 0 }

// Min is the stricter of two policies; an unknown one counts as observe.
func Min(a, b Policy) Policy {
	if a.rank() == 0 {
		a = Observe
	}
	if b.rank() == 0 {
		b = Observe
	}
	if a.rank() <= b.rank() {
		return a
	}
	return b
}

// ObserveNote is what Den says when it may not act on a host or answer
// its requests because the host is set to observe, and how to change that.
func ObserveNote(host string) string {
	return fmt.Sprintf("%s is set to observe; to act on it or answer its requests here, set policy = \"approve\" for it in remotes.toml", host)
}

// ElsewhereNote is what Den says about a host another Den controls: this
// one sees it, live, but acts on nothing and answers nothing there.
func ElsewhereNote(host string) string {
	return fmt.Sprintf("another Den controls %s, this one only observes it", host)
}

// FullNote is what Den says where a host's content would show (Review, the
// details pane) but its policy keeps it on the host, and how to change that.
func FullNote(host string, p Policy) string {
	if !p.Valid() {
		p = Observe
	}
	return fmt.Sprintf("diff on %s, policy %s; to review it here, set policy = \"full\" for it in remotes.toml", host, p)
}

// Supported is the most this build of `den remote serve` applies: full,
// which adds titles, folders, recent activity and diffs on request.
const Supported = Full

// Host is one [hosts.<label>] table of remotes.toml.
type Host struct {
	Label   string `json:"label"`
	Target  string `json:"target"`             // ssh destination as the human types it: an alias, user@host
	Policy  Policy `json:"policy"`             // what it may send
	Labels  bool   `json:"labels"`             // badge role and task travel; false withholds them
	DenPath string `json:"den_path,omitempty"` // absolute den on the remote; "" finds it through a login shell
	Enabled bool   `json:"enabled"`

	// Spawn is how Den may start agents on the host: in tmux there (tmux),
	// in a local terminal over ssh -t (terminal), tmux when the host has it
	// and the terminal otherwise (auto), or not at all (off, the default).
	Spawn SpawnRoute `json:"spawn,omitempty"`
	// SpawnDirs are the folders, with their subfolders, an agent may start
	// in; ~/ is the host's home. None means no spawn on the host.
	SpawnDirs []string `json:"spawn_dirs,omitempty"`
	// Orchestrator lets the orchestrator and the leads it started spawn,
	// message and close agents on the host. false (the default) leaves the
	// host to the human, from Den's own UI.
	Orchestrator bool `json:"orchestrator,omitempty"`
	// MaxAgents caps the agents Den started that are alive on the host at
	// once; 0 means DefaultMaxAgents.
	MaxAgents int `json:"max_agents,omitempty"`

	extra [][2]string // keys this version does not know, kept as written
}

// SpawnRoute is a host's spawn key.
type SpawnRoute string

const (
	SpawnOff      SpawnRoute = "off"      // no spawn (also "")
	SpawnAuto     SpawnRoute = "auto"     // tmux when usable, else the terminal route, said before starting
	SpawnTmux     SpawnRoute = "tmux"     // only in tmux on the host
	SpawnTerminal SpawnRoute = "terminal" // only in a local terminal over ssh -t
)

// Valid reports whether r is one of the four routes, or unset.
func (r SpawnRoute) Valid() bool {
	switch r {
	case "", SpawnOff, SpawnAuto, SpawnTmux, SpawnTerminal:
		return true
	}
	return false
}

// DefaultMaxAgents is max_agents when a host does not set it.
const DefaultMaxAgents = 4

// MaxSpawned is the host's max_agents, or the default.
func (h Host) MaxSpawned() int {
	if h.MaxAgents > 0 {
		return h.MaxAgents
	}
	return DefaultMaxAgents
}

// SpawnAllowed says whether the host's own entry lets Den start agents on
// it at all, and if not why, in words fit for the human.
func (h Host) SpawnAllowed() error {
	switch {
	case h.Spawn == "" || h.Spawn == SpawnOff:
		return fmt.Errorf("spawn is off for %s; set spawn = \"tmux\" (or auto) for it in remotes.toml", h.Label)
	case len(h.SpawnDirs) == 0:
		return fmt.Errorf("%s lists no spawn_dirs in remotes.toml, so no folder there is allowed", h.Label)
	case h.Policy.rank() < Approve.rank():
		return fmt.Errorf("%s; nothing was started", ObserveNote(h.Label))
	}
	return nil
}

// Config is remotes.toml: which hosts Den connects to and how.
//
// The file is hand-editable and holds no secrets:
//
//	default_policy = "observe"   # for den remote add without --policy; absent means observe
//
//	[hosts.devbox]
//	target = "devbox"            # an ~/.ssh/config alias, or user@host
//	policy = "observe"           # observe | approve | full
//	labels = true                # send badge role and task (capped at 80 characters)
//	den_path = "/opt/den/bin/den" # optional; else a login shell's PATH plus ~/.local/bin
//	enabled = true
//	spawn = "tmux"               # auto | tmux | terminal | off (default off)
//	spawn_dirs = ["~/src"]       # where agents may start; none means no spawn
//	orchestrator = false         # may the orchestrator act here
//	max_agents = 4               # agents Den started, alive at once
//
// `den remote add|remove|enable|disable` rewrite the file: values and keys
// Den does not know are kept, comments are not.
type Config struct {
	DefaultPolicy Policy // "" when the file does not set it
	Hosts         []Host // in file order

	extra [][2]string // top-level keys this version does not know
}

// Default is the policy for a new host: default_policy, else observe.
func (c Config) Default() Policy {
	if c.DefaultPolicy.Valid() {
		return c.DefaultPolicy
	}
	return Observe
}

// Find returns the host with label.
func (c Config) Find(label string) (Host, bool) {
	for _, h := range c.Hosts {
		if h.Label == label {
			return h, true
		}
	}
	return Host{}, false
}

// Enabled lists the hosts Den should connect to.
func (c Config) Enabled() []Host {
	var out []Host
	for _, h := range c.Hosts {
		if h.Enabled {
			out = append(out, h)
		}
	}
	return out
}

// Path is remotes.toml beside config.toml, so DEN_CONFIG moves it too.
func Path() string {
	p := config.Path()
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "remotes.toml")
}

// CheckLabel says what is wrong with a host label, nil when nothing.
func CheckLabel(label string) error {
	switch {
	case label == "":
		return errors.New("a host label cannot be empty")
	case label == "local":
		return errors.New(`"local" is reserved for this machine`)
	case len(label) > 32:
		return fmt.Errorf("host label %q is longer than 32 characters", label)
	case label[0] == '-':
		return fmt.Errorf("host label %q cannot start with '-'", label)
	}
	for _, r := range label {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return fmt.Errorf("host label %q may hold only lower-case letters, digits and '-'", label)
		}
	}
	return nil
}

// CheckTarget refuses an ssh destination that ssh could read as an option,
// or that holds whitespace or control characters.
func CheckTarget(target string) error {
	switch {
	case target == "":
		return errors.New("the ssh target cannot be empty")
	case target[0] == '-':
		return fmt.Errorf("ssh target %q cannot start with '-'", target)
	}
	for _, r := range target {
		if r <= ' ' || r == 0x7f || r == '\'' || r == '"' || r == '\\' {
			return fmt.Errorf("ssh target %q holds a space, quote or control character", target)
		}
	}
	return nil
}

// CheckDenPath accepts an absolute remote path or one under ~/, free of
// quotes and control characters, so it can be placed in the remote command.
func CheckDenPath(p string) error {
	if p == "" {
		return nil
	}
	if !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "~/") {
		return fmt.Errorf("den path %q must be absolute or start with ~/", p)
	}
	for _, r := range p {
		if r < ' ' || r == 0x7f || r == '\'' || r == '"' || r == '\\' || r == '$' || r == '`' {
			return fmt.Errorf("den path %q holds a quote, $, backquote or control character", p)
		}
	}
	return nil
}

// LabelFor suggests a label from an ssh target: the host part, lower-cased,
// with anything else made '-'.
func LabelFor(target string) string {
	h := target
	if i := strings.LastIndex(h, "@"); i >= 0 {
		h = h[i+1:]
	}
	h = strings.Trim(h, "[]")
	var b strings.Builder
	for _, r := range strings.ToLower(h) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// Validate checks every host and that labels are unique.
func (c Config) Validate() error {
	if c.DefaultPolicy != "" && !c.DefaultPolicy.Valid() {
		return fmt.Errorf("default_policy %q is not observe, approve or full", c.DefaultPolicy)
	}
	seen := map[string]bool{}
	for _, h := range c.Hosts {
		if err := CheckLabel(h.Label); err != nil {
			return err
		}
		if seen[h.Label] {
			return fmt.Errorf("host label %q is used twice", h.Label)
		}
		seen[h.Label] = true
		if err := CheckTarget(h.Target); err != nil {
			return fmt.Errorf("[hosts.%s]: %v", h.Label, err)
		}
		if !h.Policy.Valid() {
			return fmt.Errorf("[hosts.%s]: policy %q is not observe, approve or full", h.Label, h.Policy)
		}
		if err := CheckDenPath(h.DenPath); err != nil {
			return fmt.Errorf("[hosts.%s]: %v", h.Label, err)
		}
		if !h.Spawn.Valid() {
			return fmt.Errorf("[hosts.%s]: spawn %q is not auto, tmux, terminal or off", h.Label, h.Spawn)
		}
		if h.MaxAgents < 0 || h.MaxAgents > 64 {
			return fmt.Errorf("[hosts.%s]: max_agents %d is not between 1 and 64", h.Label, h.MaxAgents)
		}
		for _, d := range h.SpawnDirs {
			if err := CheckSpawnDir(d); err != nil {
				return fmt.Errorf("[hosts.%s]: spawn_dirs: %v", h.Label, err)
			}
		}
	}
	return nil
}

// Load reads remotes.toml. A missing file is an empty Config. A malformed
// file is an error, and Den then connects to no host rather than guess.
func Load() (Config, error) { return LoadFile(Path()) }

// LoadFile reads a remotes.toml at path.
func LoadFile(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || path == "" {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	c, err := parseConfig(raw)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %v", path, err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %v", path, err)
	}
	return c, nil
}

func parseConfig(raw []byte) (Config, error) {
	var c Config
	var cur *Host
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(stripComment(sc.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			label, ok := strings.CutPrefix(name, "hosts.")
			if !ok || !strings.HasSuffix(line, "]") {
				return c, fmt.Errorf("line %d: only [hosts.<label>] tables are known, not %s", n, line)
			}
			label = strings.Trim(label, `"`)
			c.Hosts = append(c.Hosts, Host{Label: label, Policy: "", Labels: true, Enabled: true})
			cur = &c.Hosts[len(c.Hosts)-1]
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return c, fmt.Errorf("line %d: expected key = value", n)
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if cur == nil {
			if k == "default_policy" {
				s, err := str(v)
				if err != nil {
					return c, fmt.Errorf("line %d: %v", n, err)
				}
				c.DefaultPolicy = Policy(s)
			} else {
				c.extra = append(c.extra, [2]string{k, v})
			}
			continue
		}
		var err error
		switch k {
		case "target":
			cur.Target, err = str(v)
		case "policy":
			var s string
			s, err = str(v)
			cur.Policy = Policy(s)
		case "den_path":
			cur.DenPath, err = str(v)
		case "labels":
			cur.Labels, err = strconv.ParseBool(v)
		case "enabled":
			cur.Enabled, err = strconv.ParseBool(v)
		case "spawn":
			var s string
			s, err = str(v)
			cur.Spawn = SpawnRoute(s)
		case "spawn_dirs":
			cur.SpawnDirs, err = config.StringList(v)
		case "orchestrator":
			cur.Orchestrator, err = strconv.ParseBool(v)
		case "max_agents":
			cur.MaxAgents, err = strconv.Atoi(v)
		default:
			cur.extra = append(cur.extra, [2]string{k, v})
		}
		if err != nil {
			return c, fmt.Errorf("line %d: %s: %v", n, k, err)
		}
	}
	for i := range c.Hosts {
		if c.Hosts[i].Policy == "" {
			c.Hosts[i].Policy = c.Default()
		}
	}
	return c, sc.Err()
}

// str unquotes a TOML basic string.
func str(v string) (string, error) {
	if len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' {
		return "", fmt.Errorf("%s is not a quoted string", v)
	}
	return strconv.Unquote(v)
}

// stripComment drops a trailing # comment that is not inside quotes.
func stripComment(line string) string {
	inQuote, escaped := false, false
	for i, r := range line {
		switch {
		case escaped:
			escaped = false
		case r == '\\' && inQuote:
			escaped = true
		case r == '"':
			inQuote = !inQuote
		case r == '#' && !inQuote:
			return line[:i]
		}
	}
	return line
}

// Marshal renders the config as remotes.toml.
func (c Config) Marshal() []byte {
	var b bytes.Buffer
	b.WriteString("# Den's remote hosts. See `den remote --help`. `den remote` rewrites this\n# file: keys are kept, comments are not.\n")
	if c.DefaultPolicy != "" {
		fmt.Fprintf(&b, "default_policy = %q\n", string(c.DefaultPolicy))
	}
	for _, kv := range c.extra {
		fmt.Fprintf(&b, "%s = %s\n", kv[0], kv[1])
	}
	for _, h := range c.Hosts {
		fmt.Fprintf(&b, "\n[hosts.%s]\ntarget = %q\npolicy = %q\n", h.Label, h.Target, string(h.Policy))
		if !h.Labels {
			b.WriteString("labels = false\n")
		}
		if h.DenPath != "" {
			fmt.Fprintf(&b, "den_path = %q\n", h.DenPath)
		}
		if !h.Enabled {
			b.WriteString("enabled = false\n")
		}
		if h.Spawn != "" {
			fmt.Fprintf(&b, "spawn = %q\n", string(h.Spawn))
		}
		if h.SpawnDirs != nil {
			q := make([]string, len(h.SpawnDirs))
			for i, d := range h.SpawnDirs {
				q[i] = strconv.Quote(d)
			}
			fmt.Fprintf(&b, "spawn_dirs = [%s]\n", strings.Join(q, ", "))
		}
		if h.Orchestrator {
			b.WriteString("orchestrator = true\n")
		}
		if h.MaxAgents != 0 {
			fmt.Fprintf(&b, "max_agents = %d\n", h.MaxAgents)
		}
		for _, kv := range h.extra {
			fmt.Fprintf(&b, "%s = %s\n", kv[0], kv[1])
		}
	}
	return b.Bytes()
}

// Save validates c and writes it to path atomically.
func (c Config) Save(path string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	return writeAtomic(path, c.Marshal(), 0o644)
}

// writeAtomic writes data to a temporary file beside path and renames it
// over path, so a crash never leaves half a file.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// Add appends a host after checking it, filling the policy from the
// default when it is empty.
func (c *Config) Add(h Host) error {
	if _, ok := c.Find(h.Label); ok {
		return fmt.Errorf("a host labelled %q already exists", h.Label)
	}
	if h.Policy == "" {
		h.Policy = c.Default()
	}
	next := *c
	next.Hosts = append(append([]Host(nil), c.Hosts...), h)
	if err := next.Validate(); err != nil {
		return err
	}
	*c = next
	return nil
}

// Remove drops the host with label.
func (c *Config) Remove(label string) error {
	for i, h := range c.Hosts {
		if h.Label == label {
			c.Hosts = append(c.Hosts[:i:i], c.Hosts[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("no host labelled %q", label)
}

// SetEnabled turns a host on or off.
func (c *Config) SetEnabled(label string, on bool) error {
	for i := range c.Hosts {
		if c.Hosts[i].Label == label {
			c.Hosts[i].Enabled = on
			return nil
		}
	}
	return fmt.Errorf("no host labelled %q", label)
}

// Labels lists the host labels, sorted.
func (c Config) Labels() []string {
	var out []string
	for _, h := range c.Hosts {
		out = append(out, h.Label)
	}
	sort.Strings(out)
	return out
}
