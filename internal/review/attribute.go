package review

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/GlacierHubAB/den/internal/transcript"
)

// Source is one session's file changes, as read from its transcript.
type Source struct {
	SessionID string
	Changes   []transcript.TimedChange
}

// edit is one agent change to one file, flattened for matching.
type edit struct {
	session string
	order   int                 // position in time across all sources, newest highest
	added   map[string]struct{} // text of every "+" line
	removed map[string]struct{} // text of every "-" line
}

// Attribute fills File.Owner for every file under root. A "+" line belongs
// to the newest agent edit that added exactly that text to that file, a
// "-" line to the newest one that removed it. Lines no edit explains (you
// wrote them, or an agent's line was changed by hand afterwards) stay "".
// Bare punctuation such as "}" or a blank line is too common to match on its
// own, so it takes the owner of the nearest matched line in the same run of
// changes.
func Attribute(root string, files []File, sources []Source) {
	type stamped struct {
		session string
		c       transcript.TimedChange
	}
	var all []stamped
	for _, s := range sources {
		for _, c := range s.Changes {
			all = append(all, stamped{s.SessionID, c})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].c.At.Before(all[j].c.At) })

	byPath := map[string][]edit{}
	for i, s := range all {
		p := filepath.Clean(s.c.Real())
		if !filepath.IsAbs(p) {
			continue
		}
		e := edit{session: s.session, order: i, added: map[string]struct{}{}, removed: map[string]struct{}{}}
		for _, h := range s.c.Hunks {
			for _, l := range h.Lines {
				switch {
				case strings.HasPrefix(l, "+"):
					e.added[key(l[1:])] = struct{}{}
				case strings.HasPrefix(l, "-"):
					e.removed[key(l[1:])] = struct{}{}
				}
			}
		}
		byPath[p] = append(byPath[p], e)
	}

	for fi := range files {
		f := &files[fi]
		edits := byPath[filepath.Join(root, f.Path)]
		if f.OldPath != "" {
			edits = append(edits, byPath[filepath.Join(root, f.OldPath)]...)
		}
		f.Owner = make([][]string, len(f.Hunks))
		for hi, h := range f.Hunks {
			own := make([]string, len(h.Lines))
			for li, l := range h.Lines {
				if l == "" || l[0] == ' ' || trivial(l[1:]) {
					continue
				}
				own[li] = newest(edits, l[0] == '+', key(l[1:]))
			}
			fillTrivial(h.Lines, own)
			f.Owner[hi] = own
		}
	}
}

// key normalises a line for matching: trailing whitespace and a stray CR
// differ between what the tool recorded and what git prints.
func key(s string) string { return strings.TrimRight(s, " \t\r") }

// trivial lines carry no identity of their own.
func trivial(s string) bool {
	t := strings.TrimSpace(s)
	if len(t) > 3 {
		return false
	}
	return strings.Trim(t, "{}()[];,)") == ""
}

func newest(edits []edit, added bool, k string) string {
	best, bestOrder := "", -1
	for _, e := range edits {
		set := e.removed
		if added {
			set = e.added
		}
		if _, ok := set[k]; ok && e.order > bestOrder {
			best, bestOrder = e.session, e.order
		}
	}
	return best
}

// fillTrivial gives each trivial changed line the owner of the closest
// matched line of the same kind ("+" or "-") in its run of changes, looking
// up first, since a closing brace usually ends what the line above began.
func fillTrivial(lines, own []string) {
	for i, l := range lines {
		if l == "" || l[0] == ' ' || !trivial(l[1:]) {
			continue
		}
		// Walk outwards until both directions leave the run of changes.
		upDone, downDone := false, false
		for d := 1; !upDone || !downDone; d++ {
			if j := i - d; !upDone {
				if j < 0 || lines[j] == "" || lines[j][0] == ' ' {
					upDone = true
				} else if lines[j][0] == l[0] && own[j] != "" {
					own[i] = own[j]
					break
				}
			}
			if j := i + d; !downDone {
				if j >= len(lines) || lines[j] == "" || lines[j][0] == ' ' {
					downDone = true
				} else if lines[j][0] == l[0] && own[j] != "" && !trivial(lines[j][1:]) {
					own[i] = own[j]
					break
				}
			}
		}
	}
}
