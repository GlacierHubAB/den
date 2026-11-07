package doctor

import (
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/GlacierHubAB/den/internal/hooks"
)

// The setup card's plan: what Den's start-up check found missing in Claude
// Code's setup, and exactly what ^Y on Den's setup card would write. The
// card is made only from this, Den's own reading of its own files, never
// from an agent's or a console's text. The conditions on y are
// den-sec-review's D4 to D7 (docs in ~/research/roadmap/doctor-design.md).

// Plan is what the setup card offers.
type Plan struct {
	Exe      string    // the den the hooks run: this one, resolved as install-hooks resolves it
	Settings string    // settings.json, its real path
	Missing  []string  // the events Den's hooks lack
	Other    bool      // Den's hooks run another den
	Mods     []PlanMod // the mods y copies in
	// NoY is why the card offers no y, "" when it does. Den shows it on the
	// card; the hooks and mods are then the human's to install by hand.
	NoY string
}

// PlanMod is one mod y copies in.
type PlanMod struct {
	Name    string
	Version string
	Target  string // its folder, real path
}

// Hooks reports whether y writes Den's hooks.
func (p Plan) Hooks() bool { return len(p.Missing) > 0 || p.Other }

// Work reports whether there is anything to offer.
func (p Plan) Work() bool { return p.Hooks() || len(p.Mods) > 0 || p.NoY != "" && p.hooksBroken() }

// hooksBroken is a settings.json Den cannot read or parse: a card that
// says so, with no y.
func (p Plan) hooksBroken() bool { return strings.HasPrefix(p.NoY, noYSettings) }

// Same reports whether two plans write the same things (D5: Den checks
// again at y, and writes nothing on any change).
func (p Plan) Same(q Plan) bool {
	a, _ := json.Marshal(p)
	b, _ := json.Marshal(q)
	return string(a) == string(b)
}

const noYSettings = "Den will not rewrite "

// MakePlan reads what Den's setup lacks. It only reads.
func MakePlan(e Env) Plan {
	dir, _ := claudeDir(e)
	p := Plan{Exe: e.Exe, Settings: filepath.Join(dir, "settings.json")}
	raw, err := e.ReadFile(p.Settings)
	switch {
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		p.NoY = noYSettings + p.Settings + ", which it cannot read: install-hooks would not either; fix its permissions, then run den install-hooks"
	default:
		st, err := hooks.StatusOf(raw, e.Exe)
		if err != nil {
			p.NoY = noYSettings + p.Settings + ", which does not parse: fix it by hand, then run den install-hooks"
			break
		}
		p.Missing, p.Other = st.Missing, st.Other
		if len(p.Missing) == len(hooks.Events()) {
			p.Other = false // none there to run another den
		}
	}
	if ms, err := e.Mods(); err == nil {
		for _, m := range ms {
			if !m.Loaded && m.Installed != m.Bundled && m.Target != "" {
				p.Mods = append(p.Mods, PlanMod{Name: m.Name, Version: m.Bundled, Target: m.Target})
			}
		}
	}
	if p.NoY == "" && (p.Hooks() || len(p.Mods) > 0) {
		p.NoY = noY(e, p)
	}
	return p
}

// noY is why y is not offered on a plan with work, "" when it is.
func noY(e Env, p Plan) string {
	if p.Hooks() {
		if hooks.TempBuild(p.Exe) {
			return "this den lives in a temporary build folder, which goes away: build den somewhere stable, then run den install-hooks from there"
		}
		if why := unsafeDen(e, p.Exe); why != "" {
			return why
		}
	}
	for _, m := range p.Mods {
		for _, d := range []string{filepath.Dir(m.Target), m.Target} {
			if fi, err := e.Lstat(d); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
				return d + " is a symbolic link: Den does not write through one; install " + m.Name + " by hand"
			}
		}
	}
	return ""
}

// unsafeDen says why exe is not one every later session's hooks should
// run (D4), "" when it is: it and every folder above it must be owned by
// the user (or root) and writable by no one else, and it must not sit in a
// git checkout, which agents rebuild.
//
// Both the path the hooks name and the file it resolves to are walked, up
// to /: a symbolic link on the way (as /home on some systems) is checked
// for its owner, since its own mode means nothing, and what it leads to is
// checked on the resolved walk.
func unsafeDen(e Env, exe string) string {
	if !filepath.IsAbs(exe) {
		return "Den cannot tell where this den lives"
	}
	real, err := e.EvalSymlinks(exe)
	if err != nil {
		return "Den cannot check " + exe + ", which every session's hooks would run"
	}
	for _, start := range []string{filepath.Clean(exe), real} {
		for p := start; ; p = filepath.Dir(p) {
			fi, err := e.Lstat(p)
			if err != nil {
				return "Den cannot check " + p + ", which every session's hooks would depend on"
			}
			if owner, ok := ownerOf(fi); !ok || owner != e.UID && owner != 0 {
				return p + " is not yours (nor root's): build den somewhere you own"
			}
			if fi.Mode()&fs.ModeSymlink == 0 && fi.Mode().Perm()&0o022 != 0 {
				return p + " is writable by others than you: whoever can write there changes what every session's hooks run; build den somewhere only you can write"
			}
			if p != start {
				if _, err := e.Lstat(filepath.Join(p, ".git")); err == nil {
					return "this den sits inside a git checkout (" + p + "): agents that rebuild this checkout change what every session's hooks run; build den somewhere stable"
				}
			}
			if p == filepath.Dir(p) {
				break
			}
		}
	}
	return ""
}

func ownerOf(fi fs.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}

// Lines is the card's body. full is Den's own window: real paths, as y
// writes them (D6). Otherwise it is the copy den-api.sock lists, paste-safe
// as den doctor is: paths from ~ or the variable that moved them, the den
// as ~/…/den or "a den outside your home".
func (p Plan) Lines(e Env, full bool) []string {
	settings, den := p.Settings, hooks.Command(p.Exe)
	target := func(m PlanMod) string { return m.Target }
	if !full {
		_, shown := claudeDir(e)
		settings, den = shown+"/settings.json", denShown(e, p.Exe)+" hook"
		target = func(m PlanMod) string { return shownTarget(e, m.Target) }
	}
	var out []string
	if p.Hooks() {
		switch {
		case len(p.Missing) == len(hooks.Events()):
			out = append(out, "Den's hooks are not in "+settings+": nothing reaches Den's inbox.")
		case len(p.Missing) > 0:
			out = append(out, "Den's hooks in "+settings+" miss "+strings.Join(p.Missing, ", ")+".")
		default:
			out = append(out, "Den's hooks in "+settings+" run another den than this one.")
		}
	}
	for _, m := range p.Mods {
		out = append(out, m.Name+" "+shownVer(m.Version)+" is not installed, or is another version.")
	}
	if p.hooksBroken() {
		out = append(out, "Den's hooks may be missing: "+strings.TrimPrefix(strings.Replace(p.NoY, p.Settings, settings, 1), "Den will not rewrite "))
		out = append(out, "", "No ^Y on this card. ^N hides it until a newer Den; den doctor still reports it.")
		return out
	}
	out = append(out, "", "^Y does exactly this, and nothing else:")
	if p.Hooks() {
		out = append(out,
			"  · copies "+settings+" to "+settings+".bak.den.<time> first,",
			"  · then writes Den's hooks into it for "+strings.Join(hooks.Events(), ", ")+", replacing Den's old ones and keeping every other hook;",
			"    each runs: "+den)
	}
	for _, m := range p.Mods {
		out = append(out, "  · copies "+m.Name+" "+shownVer(m.Version)+", built into this den, into "+target(m)+", replacing what is there.")
	}
	out = append(out,
		"Claude Code sessions started from now on use them; restart the ones running now.",
		"Undo: den uninstall-hooks, den uninstall-mods.")
	if p.NoY != "" {
		msg := p.NoY
		if !full {
			msg = "Den found a reason not to install from here; Den's window says which."
		}
		out = append(out, "", "No ^Y on this card: "+msg)
	}
	out = append(out, "^N hides this card until a newer Den; den doctor still reports it.")
	return out
}

// shownTarget is a mod folder as printed: ~/… under home, else only its
// name under a folder outside home.
func shownTarget(e Env, p string) string {
	home := strings.TrimSuffix(e.Home, "/")
	if home != "" && strings.HasPrefix(p, home+"/") {
		return "~" + strings.TrimPrefix(p, home)
	}
	return "a folder outside your home"
}

// Dismissed is the file ^N on the setup card writes: Den's version, so the
// card stays hidden until a newer Den. den install-hooks removes it.
func Dismissed(e Env) string {
	dir, _ := stateDir(e)
	return filepath.Join(dir, "setup-dismissed")
}
