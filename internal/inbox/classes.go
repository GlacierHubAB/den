package inbox

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The permission manager's classes: what a console's "allow" on a card
// does, as Den sees it from its own record of the card. A console's request
// never names a class or a step; Den classes the card when it makes it, and
// the class and step go inside the card's hash (card hash v2).
//
// The table is closed: a Kind, or a tool, it does not name is den_only,
// answered only in Den's own window. Where Den's record cannot settle a
// fact, the stricter answer is taken. "deny" is a lowering and is never
// classed; "always" is den_only on every kind, and a card's class and step
// never cover it.
//
// In this version the class and step gate nothing: they are shown to the
// console and pinned in the hash. Levels are enforced later.

// Class is one of the permission manager's classes.
type Class string

const (
	Routine     Class = "routine"     // everyday answers
	Destructive Class = "destructive" // stop and close
	Code        Class = "code"        // run commands
	Desktop     Class = "desktop"     // desktop control
	Outward     Class = "outward"     // send out
	DenOnly     Class = "den_only"    // only in Den's own window
)

// stepRange is each class's steps, first and last: the bars a class has.
// den_only is step 0 and nothing else; no other class has a step 0.
var stepRange = map[Class][2]int{
	Routine:     {1, 4},
	Destructive: {1, 2},
	Code:        {1, 1},
	Desktop:     {1, 2},
	Outward:     {1, 3},
	DenOnly:     {0, 0},
}

// StepFits reports whether step is one of class's steps; false for a class
// outside the set. It is the one rule both the card's projection and the
// card hash check.
func StepFits(class Class, step int) bool {
	r, ok := stepRange[class]
	return ok && step >= r[0] && step <= r[1]
}

// Facts are what Den knows about a card beyond the inbox item, from the
// record that made it, read again each time the card is shown or answered:
// a fact that changes between the two changes the card's hash.
type Facts struct {
	// CleanupKnown is set when Den holds the Cleanup's plan, and
	// CleanupIgnored is how many ignored files it would delete.
	CleanupKnown   bool
	CleanupIgnored int
}

// codeTools run code or change files.
var codeTools = map[string]bool{"Bash": true, "Write": true, "Edit": true, "MultiEdit": true, "NotebookEdit": true}

// readTools read files, and the input fields that name what they read: a
// path, or a pattern that may hold one.
var readTools = map[string][]string{
	"Read":         {"file_path"},
	"NotebookRead": {"notebook_path"},
	"LS":           {"path"},
	"Glob":         {"path", "pattern"},
	"Grep":         {"path", "glob"},
}

// outwardTools reach the network.
var outwardTools = map[string]bool{"WebFetch": true, "WebSearch": true}

// Classify is the class and step of a console's "allow" on it, from Den's
// own record: the item and f. A card from a remote host, or about one, is
// den_only whatever its kind.
func Classify(it *Item, f Facts) (Class, int) {
	if it.Host != "" || strings.HasPrefix(it.SessionID, HostPrefix) {
		return DenOnly, 0
	}
	switch it.Kind {
	case Permission:
		if !it.toolClassed {
			it.toolClass, it.toolStep = classifyTool(it)
			it.toolClassed = true
		}
		return it.toolClass, it.toolStep
	case Plan:
		return Code, 1
	case Close:
		return Destructive, 1
	case Cleanup:
		if f.CleanupKnown && f.CleanupIgnored == 0 {
			return Destructive, 1
		}
		return Destructive, 2
	case MissionUpdate:
		// Den's record does not show whether the update can be undone.
		return Destructive, 2
	case MissionMove:
		return Routine, 1
	case Merge:
		return Outward, 2
	case Restore:
		// An agent comes back in the lower of its mode at close and the
		// global mode (permmode.Lower), never in more than it had.
		return Destructive, 1
	}
	// ControlAsk carries its tool only as text, and the orchestrator's
	// tools are Den's own MCP tools; Budget, UsageResume, UsageSet and
	// ModeSet spend or widen. Every other kind is the human's.
	return DenOnly, 0
}

// classifyTool classes a permission by its tool and input.
func classifyTool(it *Item) (Class, int) {
	if it.req == nil {
		return DenOnly, 0
	}
	in := it.req.Input
	home, _ := os.UserHomeDir()
	var args map[string]any
	if json.Unmarshal(in.ToolInput, &args) != nil {
		return DenOnly, 0
	}
	switch {
	case in.ToolName == "Bash":
		return Code, 1
	case codeTools[in.ToolName]:
		path, _ := args[editTools[in.ToolName]].(string)
		if editRealInScope(path, in.CWD, home) {
			return Code, 1
		}
		return DenOnly, 0
	case readTools[in.ToolName] != nil:
		if readInScope(in.ToolName, args, in.CWD, home) {
			return Routine, 3
		}
		return DenOnly, 0
	case outwardTools[in.ToolName]:
		return Outward, 3
	}
	return DenOnly, 0
}

// readInScope reports whether a read tool's call reads only inside the
// agent's working directory: every path it names, and the fixed start of
// every pattern, strictly under cwd with symlinks resolved, and nothing
// sensitive. A call with no path reads cwd itself, which is not strictly
// under it. Anything Den cannot parse is out of scope.
func readInScope(tool string, args map[string]any, cwd, home string) bool {
	named, base := false, ""
	for _, field := range readTools[tool] {
		raw, present := args[field]
		if !present {
			continue
		}
		v, ok := raw.(string)
		if !ok {
			return false
		}
		if v == "" {
			continue
		}
		if field == "pattern" || field == "glob" {
			if !globInScope(v, base, cwd, home) {
				return false
			}
			if field == "pattern" && !globReachClear(v, base, cwd, home) {
				return false
			}
			named = named || filepath.IsAbs(v) || strings.HasPrefix(v, "~")
			continue
		}
		named = true
		if !editRealInScope(v, cwd, home) || readSensitive(v, cwd, home) {
			return false
		}
		base, _ = editPath(v, cwd, home)
	}
	return named
}

// globInScope reports whether a glob pattern stays under cwd. Its fixed
// start, up to the first wildcard, is checked as a path with symlinks
// resolved: for an absolute or ~ pattern on its own, for a relative one
// under base, the path the call names (already checked); a relative
// pattern with no base is out. No part is sensitive or "..", and no part
// may reach a hidden name by a wildcard (".g*", "?git", "[.]git"): the
// sensitive names are hidden ones. A backslash is out: ripgrep's globs
// read it as an escape, so `\.git` names .git past every check here.
// Accepted residual: "*" and "**" lean on the tools leaving hidden files
// out, as Glob and Grep (ripgrep) do by default. The symlinks a Glob
// pattern can reach are checked by globReachClear; Grep's ripgrep does not
// follow a symlink it meets while it walks (no --follow, which the tool
// does not offer).
func globInScope(pattern, base, cwd, home string) bool {
	if strings.Contains(pattern, `\`) {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(pattern), "/") {
		lp := strings.ToLower(part)
		if part == ".." || sensitiveParts[lp] || sensitiveNames[lp] {
			return false
		}
		if strings.ContainsAny(part, "*?[{") && strings.ContainsAny(part[:1], ".?[{") {
			return false
		}
	}
	fixed := pattern
	if i := strings.IndexAny(pattern, "*?[{"); i >= 0 {
		fixed = filepath.Dir(pattern[:i] + "x")
	}
	if !filepath.IsAbs(pattern) && !strings.HasPrefix(pattern, "~") {
		if base == "" || strings.ContainsAny(pattern, "{}") { // a brace can name a path outside
			return false
		}
		fixed = filepath.Join(base, fixed)
	}
	return editRealInScope(fixed+"/x", cwd, home) && !readSensitive(fixed+"/x", cwd, home)
}

// reachLimit caps the entries globReachClear looks at, and reachBudget
// the time it takes: past either, Den does not know what the pattern
// reaches. The walk runs once per card (Classify keeps the class), on
// Den's own loop, so a slow disk (NFS, sshfs) may hold Den's screen for
// at most about the budget. Accepted residual: one read of a single huge
// directory, and the resolving of the walk's root, cannot be cut short,
// so the budget can be overrun by one directory read.
const reachLimit = 5000

var reachBudget = 250 * time.Millisecond

// globReachClear reports whether no symlink a Glob pattern can reach is a
// way out. From the existing directory at its fixed start (under base for
// a relative pattern), down as deep as the pattern goes (every depth for
// "**"), every symlink the pattern's parts match must resolve to a file
// strictly under cwd that is not sensitive: one to a directory is out
// too, since the walk does not follow it. Under "**" every entry counts.
// Hidden entries are skipped, as the tool skips them. A pattern
// with no wildcard is a path, already checked; more than reachLimit
// entries, a walk longer than reachBudget, or one that fails, is out.
// Names match the pattern's parts with case ignored on both sides: on a
// filesystem that ignores case, "*.GO" reaches "x.go", and matching more
// only checks more.
func globReachClear(pattern, base, cwd, home string) bool {
	parts := strings.Split(filepath.ToSlash(pattern), "/")
	fixed := 0
	for fixed < len(parts) && !strings.ContainsAny(parts[fixed], "*?[{") {
		fixed++
	}
	if fixed == len(parts) {
		return true
	}
	depth := len(parts) - fixed
	for _, p := range parts[fixed:] {
		if strings.Contains(p, "**") {
			depth = -1
		}
	}
	dir := strings.Join(parts[:fixed], "/")
	if dir == "" && filepath.IsAbs(pattern) {
		dir = "/"
	}
	if !filepath.IsAbs(pattern) && !strings.HasPrefix(pattern, "~") {
		dir = filepath.Join(base, dir)
	}
	p, ok := editPath(dir, cwd, home)
	if !ok {
		return false
	}
	root, err := filepath.EvalSymlinks(p)
	if err != nil {
		return os.IsNotExist(err) // nothing there to reach
	}
	seen, until := 0, time.Now().Add(reachBudget)
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Every entry the walk reads counts against the budget, skipped
		// or not: a huge directory where nothing matches is still read.
		if time.Now().After(until) {
			return errReachTooFar
		}
		if path == root {
			return nil
		}
		skip := strings.HasPrefix(d.Name(), ".")
		k := strings.Count(strings.TrimPrefix(path, root+"/"), "/") + 1 // its depth below root
		if depth >= 0 && !skip {
			// Only what the pattern's part at that depth matches; a part
			// Den cannot match exactly (a brace) matches everything.
			if seg := parts[fixed+k-1]; !strings.Contains(seg, "{") {
				ok, err := filepath.Match(strings.ToLower(seg), strings.ToLower(d.Name()))
				skip = err == nil && !ok
			}
		}
		if skip {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if seen++; seen > reachLimit {
			return errReachTooFar
		}
		if d.Type()&fs.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			if fi, err := os.Stat(target); err != nil || fi.IsDir() {
				return errReachOut
			}
			if !editRealInScope(target, cwd, home) || readSensitive(target, cwd, home) {
				return errReachOut
			}
			return nil
		}
		if d.IsDir() && depth >= 0 && k >= depth {
			return filepath.SkipDir
		}
		return nil
	})
	return err == nil
}

var (
	errReachTooFar = errors.New("the pattern reaches too many entries to check")
	errReachOut    = errors.New("a symlink the pattern reaches leads out")
)

// readSensitive reports whether a read of path reaches what edits never
// would but reads still should not: keys and Den's and Drey's own state.
// The path is checked as written and with symlinks resolved, against each
// directory as named and resolved; a path that cannot be resolved is
// sensitive.
func readSensitive(path, cwd, home string) bool {
	p, ok := editPath(path, cwd, home)
	if !ok {
		return true
	}
	real, ok := realPath(p)
	if !ok {
		return true
	}
	var dirs []string
	for _, d := range readSensitiveDirs(home) {
		dirs = append(dirs, filepath.Clean(d))
		// Resolved through its deepest existing part, so a directory not
		// made yet under a symlinked parent is covered too.
		if r, ok := realPath(d); ok {
			dirs = append(dirs, r)
		}
	}
	for _, q := range []string{strings.ToLower(p), strings.ToLower(real)} {
		for _, d := range dirs {
			ld := strings.ToLower(d)
			if q == ld || strings.HasPrefix(q, ld+"/") {
				return true
			}
		}
	}
	return false
}

// readSensitiveDirs are the directories no read is routine in: GnuPG's,
// and Den's and Drey's state, config and data, wherever XDG puts them.
func readSensitiveDirs(home string) []string {
	var out []string
	if filepath.IsAbs(home) {
		out = append(out, filepath.Join(home, ".gnupg"))
	}
	base := func(env, under string) string {
		if d := os.Getenv(env); filepath.IsAbs(d) {
			return d
		}
		if filepath.IsAbs(home) {
			return filepath.Join(home, under)
		}
		return ""
	}
	for _, b := range []string{base("XDG_STATE_HOME", ".local/state"), base("XDG_CONFIG_HOME", ".config"), base("XDG_DATA_HOME", ".local/share")} {
		if b != "" {
			out = append(out, filepath.Join(b, "den"), filepath.Join(b, "drey"))
		}
	}
	if d := os.Getenv("GNUPGHOME"); filepath.IsAbs(d) {
		out = append(out, d)
	}
	return out
}
