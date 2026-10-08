package ui

import (
	"context"
	"path/filepath"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/gitinfo"
	"github.com/GlacierHubAB/den/internal/review"
	"github.com/GlacierHubAB/den/internal/transcript"
)

// The Review view: what each agent changed, as git sees it on disk, with
// every changed line coloured by the session that wrote it. Read-only for
// now; comments and sending come next (docs/CODE-REVIEW.md).

// reviewPane is which of the three Review panes has the arrow keys.
type reviewPane int

const (
	revWork reviewPane = iota
	revFiles
	revDiff
)

// reviewReload is how stale the Review data may get while the tab is open.
const reviewReload = 15 * time.Second

// work is one agent's changes in one repository.
type work struct {
	SessionID string
	Root      string // repository root
	Repo      string // repository and branch, for the row
	Files     []review.File
	Added     int // lines this agent added, across Files
	Removed   int
	Last      time.Time // newest change the agent made
}

type reviewState struct {
	works    []work
	cur      int
	fileCur  int
	scroll   int // diff rows scrolled off the top
	focus    reviewPane
	loading  bool
	loadedAt time.Time
	errs     []string // repositories that could not be read

	// The line cursor in the diff, as an index into diffLine order, and the
	// other end of a selected range when ranged is set.
	line     int
	anchor   int
	ranged   bool
	dragging bool

	comments     []review.Comment
	commentsPath string
	editing      string // id of the comment the command line is editing
}

// reviewInput is what the loader needs from one agent, copied so the
// goroutine never touches the model.
type reviewInput struct {
	SessionID string
	CWD       string
	Path      string // transcript
	Started   time.Time
}

type reviewLoadedMsg struct {
	works []work
	errs  []string
}

// reviewInputs lists the agents whose work can be reviewed: a transcript to
// attribute from and a git checkout to diff.
func (m Model) reviewInputs() []reviewInput {
	var in []reviewInput
	for _, a := range m.agents {
		if a.Path == "" || !a.HasGit || a.Session.CWD == "" {
			continue
		}
		in = append(in, reviewInput{
			SessionID: a.Session.SessionID,
			CWD:       a.Session.CWD,
			Path:      a.Path,
			Started:   time.UnixMilli(a.Session.StartedAt),
		})
	}
	return in
}

// loadReview starts a background load unless one is running.
func (m *Model) loadReview() tea.Cmd {
	if m.rev.loading {
		return nil
	}
	m.rev.loading = true
	in := m.reviewInputs()
	return func() tea.Msg { return loadWorks(in) }
}

// loadWorks diffs each repository once, attributes its lines to every
// session working in it, and splits the result into one work per agent.
func loadWorks(in []reviewInput) reviewLoadedMsg {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	byRoot := map[string][]reviewInput{}
	var roots []string
	var errs []string
	for _, a := range in {
		root, err := review.Root(ctx, a.CWD)
		if err != nil {
			continue
		}
		if _, ok := byRoot[root]; !ok {
			roots = append(roots, root)
		}
		byRoot[root] = append(byRoot[root], a)
	}

	var works []work
	for _, root := range roots {
		agents := byRoot[root]
		since := agents[0].Started
		for _, a := range agents[1:] {
			if a.Started.Before(since) {
				since = a.Started
			}
		}
		base, err := review.Base(ctx, root, since)
		if err == nil {
			var files []review.File
			files, err = review.Diff(ctx, root, base)
			if err == nil {
				var sources []review.Source
				for _, a := range agents {
					sources = append(sources, review.Source{SessionID: a.SessionID, Changes: transcript.Changes(a.Path, since)})
				}
				review.Attribute(root, files, sources)
				works = append(works, splitWorks(root, files, sources)...)
			}
		}
		if err != nil {
			errs = append(errs, filepath.Base(root)+": "+err.Error())
		}
	}
	sort.SliceStable(works, func(i, j int) bool { return works[i].Last.After(works[j].Last) })
	return reviewLoadedMsg{works: works, errs: errs}
}

// splitWorks gives each session the files in which it owns at least one
// changed line, with its own added and removed counts.
func splitWorks(root string, files []review.File, sources []review.Source) []work {
	// A linked worktree's folder is often named after the task; show the
	// repository and branch instead.
	repo := filepath.Base(root)
	if info, ok := gitinfo.Lookup(root); ok && info.Repo != "" {
		repo = info.Repo
		if info.Branch != "" {
			repo += " · " + info.Branch
		}
	}
	var out []work
	for _, s := range sources {
		w := work{SessionID: s.SessionID, Root: root, Repo: repo}
		for _, c := range s.Changes {
			if c.At.After(w.Last) {
				w.Last = c.At
			}
		}
		for _, f := range files {
			added, removed := ownedCounts(f, s.SessionID)
			if added+removed == 0 {
				continue
			}
			w.Files = append(w.Files, f)
			w.Added += added
			w.Removed += removed
		}
		if len(w.Files) > 0 {
			// Top-level files first, then folder by folder, so each file
			// sits under its own folder heading in the tree.
			sort.SliceStable(w.Files, func(i, j int) bool {
				di, dj := filepath.Dir(w.Files[i].Path), filepath.Dir(w.Files[j].Path)
				if (di == ".") != (dj == ".") {
					return di == "."
				}
				return w.Files[i].Path < w.Files[j].Path
			})
			out = append(out, w)
		}
	}
	return out
}

func ownedCounts(f review.File, session string) (added, removed int) {
	for hi, h := range f.Hunks {
		if hi >= len(f.Owner) {
			break
		}
		for li, l := range h.Lines {
			if li >= len(f.Owner[hi]) || f.Owner[hi][li] != session || l == "" {
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
	return
}

// applyReview takes a finished load and keeps the selection on the same
// agent and file where they still exist.
func (m *Model) applyReview(msg reviewLoadedMsg) {
	var keepWork, keepFile string
	if w := m.curWork(); w != nil {
		keepWork = w.SessionID + "\x00" + w.Root
		if f := m.curFile(); f != nil {
			keepFile = f.Path
		}
	}
	m.rev.loading = false
	m.rev.loadedAt = time.Now()
	m.rev.works, m.rev.errs = msg.works, msg.errs
	m.rev.cur, m.rev.fileCur = 0, 0
	for i, w := range m.rev.works {
		if w.SessionID+"\x00"+w.Root != keepWork {
			continue
		}
		m.rev.cur = i
		for j, f := range w.Files {
			if f.Path == keepFile {
				// Same file: keep the cursor, clamped if the file shrank.
				m.rev.fileCur = j
				if n := len(diffLines(f)); m.rev.line >= n {
					m.rev.line, m.rev.ranged = max(0, n-1), false
				}
				m.followCursor()
				return
			}
		}
		break
	}
	m.resetCursor()
}

// paneArea is where the panes go on screen: the first row below the header
// and how many rows they get. View lays the screen out with it, and the
// Review view hit-tests mouse clicks with it, so the two always agree.
func (m Model) paneArea() (top, h int) {
	w, height := m.width, m.height
	if w <= 0 {
		w = 120
	}
	if height <= 0 {
		height = 40
	}
	top = len(m.header(w))
	h = height - top - len(m.prompt(w)) - 1 - len(m.completionRows(w))
	return top, max(h, 6)
}

func (m Model) curWork() *work {
	if m.rev.cur < 0 || m.rev.cur >= len(m.rev.works) {
		return nil
	}
	return &m.rev.works[m.rev.cur]
}

func (m Model) curFile() *review.File {
	w := m.curWork()
	if w == nil || m.rev.fileCur < 0 || m.rev.fileCur >= len(w.Files) {
		return nil
	}
	return &w.Files[m.rev.fileCur]
}
