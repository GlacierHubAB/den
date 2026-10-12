package remote

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/gitinfo"
	"github.com/GlacierHubAB/den/internal/review"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/transcript"
)

// diff is the third command, for remote Review: what a session's work changed
// in its checkout on the host, as Review shows it, for a host whose policy is
// full on both sides. Den names the session only; serve finds its folder in
// the host's own session file, never from the wire, and reads the checkout
// with git through gitsafe, so the repository cannot make serve run anything.

// DiffCmd asks serve for the diff of a session's work.
type DiffCmd struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Session string `json:"session"`
}

// Diff is a session's work on its host: the files it changed in the
// checkout it works in, against the commit Review compares with there.
// Every string is sanitised and every part capped on the host, and again
// on arrival.
type Diff struct {
	Root     string     `json:"root"` // the checkout's folder on the host, shown, never read
	Repo     string     `json:"repo"`
	Branch   string     `json:"branch,omitempty"`
	Worktree bool       `json:"worktree,omitempty"` // a linked worktree, not the main checkout
	Ahead    int        `json:"ahead,omitempty"`    // commits the worktree's branch is ahead of the main checkout's
	Files    []DiffFile `json:"files"`
	// Unowned says the checkout has changes but none matches what the
	// session wrote; Files is then empty.
	Unowned bool   `json:"unowned,omitempty"`
	Cut     string `json:"cut,omitempty"` // what was left out, and why
}

// DiffFile is one changed file, relative to Root.
type DiffFile struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
	Created bool   `json:"created,omitempty"`
	Deleted bool   `json:"deleted,omitempty"`
	Binary  bool   `json:"binary,omitempty"`
	Cut     bool   `json:"cut,omitempty"` // lines were left out
	// Unsafe says the real name has control characters: Path is cleaned,
	// and the content is not sent.
	Unsafe bool       `json:"unsafe,omitempty"`
	Hunks  []DiffHunk `json:"hunks,omitempty"`
	// Added and Removed count the session's own lines in an Unsafe file,
	// whose lines are not sent, so Review's counts still include it.
	Added   int `json:"added,omitempty"`
	Removed int `json:"removed,omitempty"`
}

// maxUnsafeCount bounds the line counts a host gives for an Unsafe file.
const maxUnsafeCount = 1 << 20

// DiffHunk is a review hunk; Mine lists the indexes of the lines the
// session wrote.
type DiffHunk struct {
	OldStart int      `json:"old_start"`
	NewStart int      `json:"new_start"`
	Lines    []string `json:"lines"`
	Mine     []int    `json:"mine,omitempty"`
}

// Caps of a diff. It travels as one message, so its size is counted as it
// will be encoded and kept well inside MaxLine.
const (
	MaxDiffBytes     = 200 << 10
	MaxDiffFiles     = 300
	MaxDiffFileLines = 2000
	MaxDiffLine      = 1000 // bytes of one line, the rest cut with "…"
	maxDiffPath      = 1024
)

// hostDiff reads the diff of ag's work on this host.
func hostDiff(ctx context.Context, ag fleet.Agent) (*Diff, error) {
	cwd := ag.Session.CWD
	if cwd == "" {
		return nil, errors.New("this session has no folder")
	}
	if !filepath.IsAbs(cwd) {
		// git would read it relative to serve's own folder.
		return nil, errors.New("this session's folder is not an absolute path")
	}
	root, err := review.Root(ctx, cwd)
	if err != nil || root == "" {
		return nil, errors.New("its folder is not in a git repository")
	}
	var transcripts []string
	if ag.Path != "" {
		transcripts = []string{ag.Path}
	}
	started := ag.Session.Started()
	base, err := review.BaseFor(ctx, root, transcripts, started)
	if err != nil {
		return nil, errors.New("git found no branch to compare its work with")
	}
	files, err := review.Diff(ctx, root, base)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("git took too long to read its checkout")
		}
		return nil, errors.New("git could not read its checkout")
	}
	sid := ag.Session.SessionID
	var changes []transcript.TimedChange
	if ag.Path != "" {
		changes = transcript.Changes(ag.Path, started)
	}
	review.Attribute(root, files, []review.Source{{SessionID: sid, Changes: changes}})
	// The session's work: files with a changed line it wrote, and files it
	// changed by path (a binary file has no lines to match).
	touched := map[string]bool{}
	for _, c := range changes {
		touched[filepath.Clean(c.Real())] = true
	}
	var mine []review.File
	for _, f := range files {
		if owns(f, sid) || touched[filepath.Join(root, f.Path)] {
			mine = append(mine, f)
		}
	}
	d := &Diff{Root: capLine(root, maxDiffPath), Repo: filepath.Base(root)}
	// Only the session's own work leaves the host, as local Review lists
	// only what an agent wrote: never the rest of a dirty checkout, which
	// may hold anything (a home directory's dotfiles, an untracked .env).
	d.Unowned = len(mine) == 0 && len(files) > 0
	if info, ok := gitinfo.Lookup(root); ok && info.Repo != "" {
		d.Repo, d.Branch = info.Repo, info.Branch
	}
	c := review.CheckoutOf(ctx, root)
	d.Worktree, d.Ahead = c.Worktree, c.Ahead
	if c.Branch != "" {
		d.Branch = c.Branch
	}
	d.Repo, d.Branch = sanitize.Label(d.Repo, fleet.LabelMax), sanitize.Label(d.Branch, fleet.LabelMax)
	packDiff(d, mine, sid)
	return d, nil
}

// owns says whether session wrote any changed line of f.
func owns(f review.File, session string) bool {
	for hi, h := range f.Hunks {
		for li, l := range h.Lines {
			if hi < len(f.Owner) && li < len(f.Owner[hi]) && f.Owner[hi][li] == session && l != "" && l[0] != ' ' {
				return true
			}
		}
	}
	return false
}

// packDiff puts files into d within the caps, sanitising every string, and
// says in d.Cut what it left out.
func packDiff(d *Diff, files []review.File, session string) {
	used := 512 + fleet.WireLen(d.Root) + fleet.WireLen(d.Repo) + fleet.WireLen(d.Branch)
	var cuts []string
	cutLines, cutBytes := false, false
	for i, f := range files {
		if i == MaxDiffFiles {
			cuts = append(cuts, fmt.Sprintf("%d more files past %d", len(files)-i, MaxDiffFiles))
			break
		}
		df := DiffFile{
			Path: capLine(f.Path, maxDiffPath), Created: f.Created, Deleted: f.Deleted, Binary: f.Binary, Unsafe: f.Unsafe,
		}
		if f.OldPath != "" {
			df.OldPath = capLine(f.OldPath, maxDiffPath)
		}
		used += 96 + fleet.WireLen(df.Path) + fleet.WireLen(df.OldPath)
		if used > MaxDiffBytes {
			cuts = append(cuts, fmt.Sprintf("%d more files past %d KB", len(files)-i, MaxDiffBytes>>10))
			break
		}
		if f.Unsafe {
			df.Added, df.Removed = ownedLines(f, session)
			d.Files = append(d.Files, df)
			continue
		}
		n := 0
	hunks:
		for hi, h := range f.Hunks {
			dh := DiffHunk{OldStart: h.OldStart, NewStart: h.NewStart}
			used += 48
			for li, l := range h.Lines {
				l = sanitize.Code(l)
				if l == "" || !strings.ContainsRune(" +-", rune(l[0])) {
					continue
				}
				l = capBytes(l, MaxDiffLine)
				if n == MaxDiffFileLines {
					df.Cut, cutLines = true, true
				} else if used+fleet.WireLen(l)+8 > MaxDiffBytes {
					df.Cut, cutBytes = true, true
				}
				if df.Cut {
					if len(dh.Lines) > 0 {
						df.Hunks = append(df.Hunks, dh)
					}
					break hunks
				}
				if hi < len(f.Owner) && li < len(f.Owner[hi]) && f.Owner[hi][li] == session {
					dh.Mine = append(dh.Mine, len(dh.Lines))
					used += 6
				}
				dh.Lines = append(dh.Lines, l)
				used += fleet.WireLen(l) + 8
				n++
			}
			df.Hunks = append(df.Hunks, dh)
		}
		d.Files = append(d.Files, df)
	}
	if cutLines {
		cuts = append([]string{fmt.Sprintf("files over %d lines", MaxDiffFileLines)}, cuts...)
	}
	if cutBytes {
		cuts = append([]string{fmt.Sprintf("lines past %d KB", MaxDiffBytes>>10)}, cuts...)
	}
	if len(cuts) > 0 {
		d.Cut = "cut: " + strings.Join(cuts, "; ")
	}
}

// ownedLines counts the lines of f that session added and removed.
func ownedLines(f review.File, session string) (added, removed int) {
	for hi, h := range f.Hunks {
		for li, l := range h.Lines {
			if hi >= len(f.Owner) || li >= len(f.Owner[hi]) || f.Owner[hi][li] != session || l == "" {
				continue
			}
			switch l[0] {
			case '+':
				added++
			case '-':
				removed++
			}
		}
	}
	return min(added, maxUnsafeCount), min(removed, maxUnsafeCount)
}

func capLine(s string, max int) string { return capBytes(sanitize.Line(s), max) }

// capBytes cuts s to at most max bytes on a rune boundary, ending in "…".
func capBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max - len("…")
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// cleanDiff is the local half: whatever the host sent, sanitised and held
// to the same caps, lines that are not diff lines dropped, owner indexes
// out of range dropped.
func cleanDiff(d *Diff) *Diff {
	out := &Diff{
		Root: capLine(d.Root, maxDiffPath), Repo: sanitize.Label(d.Repo, fleet.LabelMax),
		Branch: sanitize.Label(d.Branch, fleet.LabelMax), Worktree: d.Worktree, Ahead: max(0, d.Ahead),
		Unowned: d.Unowned, Cut: sanitize.Label(d.Cut, 200),
	}
	used := 0
	dropped := len(d.Files) > MaxDiffFiles
	for _, f := range d.Files[:min(len(d.Files), MaxDiffFiles)] {
		cf := DiffFile{Path: capLine(f.Path, maxDiffPath), OldPath: capLine(f.OldPath, maxDiffPath),
			Created: f.Created, Deleted: f.Deleted, Binary: f.Binary, Cut: f.Cut, Unsafe: f.Unsafe}
		if cf.Unsafe {
			cf.Added, cf.Removed = min(max(0, f.Added), maxUnsafeCount), min(max(0, f.Removed), maxUnsafeCount)
		}
		n := 0
		for _, h := range f.Hunks {
			if cf.Unsafe {
				break // its content is not shown, whatever the host sent
			}
			if n >= MaxDiffFileLines || used > MaxLine {
				cf.Cut, dropped = true, true
				break
			}
			ch := DiffHunk{OldStart: max(0, h.OldStart), NewStart: max(0, h.NewStart)}
			mine := map[int]bool{}
			for _, i := range h.Mine {
				mine[i] = true
			}
			for i, l := range h.Lines {
				l = capBytes(sanitize.Code(l), MaxDiffLine)
				if l == "" || !strings.ContainsRune(" +-", rune(l[0])) {
					continue
				}
				if n == MaxDiffFileLines || used > MaxLine {
					cf.Cut, dropped = true, true
					break
				}
				if mine[i] {
					ch.Mine = append(ch.Mine, len(ch.Lines))
				}
				ch.Lines = append(ch.Lines, l)
				used += len(l)
				n++
			}
			if len(ch.Lines) > 0 {
				cf.Hunks = append(cf.Hunks, ch)
			}
		}
		out.Files = append(out.Files, cf)
	}
	if dropped && out.Cut == "" {
		out.Cut = "cut: over Den's limits for a diff"
	}
	return out
}

// defaultDiffWait is how long Den waits for a diff; serve's own limit is
// shorter, so a slow git reports itself before Den gives up.
const defaultDiffWait = 30 * time.Second

// Diff asks host for the diff of the session's work there. It needs the
// host live and its policy full both in remotes.toml and as the host
// applies it; otherwise it says why, and nothing is asked.
func (m *Manager) Diff(host, sessionID string) (*Diff, error) {
	if err := m.Reviewable(host); err != nil {
		return nil, err
	}
	r, err := m.act(host, "diff", m.o.DiffWait, func(id string) any {
		return DiffCmd{Type: TypeDiff, ID: id, Session: sessionID}
	})
	if err != nil {
		return nil, err
	}
	if r.Diff == nil {
		return nil, fmt.Errorf("%s sent no diff", host)
	}
	return r.Diff, nil
}

// Reviewable says whether Diff would be tried now, and if not why, in
// words fit for the human.
func (m *Manager) Reviewable(host string) error {
	m.mu.Lock()
	st, ok := m.state[host]
	if ok && st.link != nil && st.status.State == Live && (st.status.Policy != Full || st.link.applied != Full) {
		p := Min(st.status.Policy, st.link.applied)
		m.mu.Unlock()
		return errors.New(FullNote(host, p))
	}
	m.mu.Unlock()
	_, err := m.linkFor(host, "diff")
	return err
}
