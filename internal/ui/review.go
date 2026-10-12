package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/gitinfo"
	"github.com/GlacierHubAB/den/internal/review"
	"github.com/GlacierHubAB/den/internal/transcript"
)

// The Review view: what each agent changed, as git sees it on disk, with
// every changed line coloured by the session that wrote it, with comments
// on lines that /send delivers to the agents (review_send.go).

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

	// Main is the repository's primary checkout, which is what the project
	// pick is made of; Root is where the diff is read, a linked worktree for
	// an agent that works in one.
	Main     string
	Worktree bool   // Root is a linked worktree
	Branch   string // the worktree's branch
	Ahead    int    // commits on Branch that the main checkout's branch lacks

	// Host is the remote host the work is on, "" for this machine. Its Root
	// is then a key, never a folder Den reads (review_remote.go).
	Host string
	Cut  string // what the host left out of the diff, and why
}

// project is the repository the work belongs to for the project pick.
func (w work) project() string {
	if w.Main != "" {
		return w.Main
	}
	return w.Root
}

type reviewState struct {
	works    []work                   // the picked project's, or all when nothing is picked
	all      []work                   // local and remote
	local    []work                   // as the last local load found them
	remote   map[string]*remoteReview // by agent key
	cur      int
	fileCur  int
	scroll   int // diff rows scrolled off the top
	focus    reviewPane
	loading  bool
	loadedAt time.Time
	errs     []string // repositories that could not be read
	note     string   // why there is nothing to review, when there is nothing

	// The line cursor in the diff, as an index into diffLine order, and the
	// other end of a selected range when ranged is set.
	line     int
	anchor   int
	ranged   bool
	dragging bool

	comments     []review.Comment
	commentsPath string
	editing      string          // id of the comment the command line is editing
	sending      map[string]bool // ids of drafts on their way, held or in flight
}

// reviewInput is what the loader needs from one agent, copied so the
// goroutine never touches the model.
type reviewInput struct {
	SessionID string
	Name      string
	CWD       string
	HasGit    bool   // the session's folder is a checkout
	Path      string // transcript
	Started   time.Time
}

type reviewLoadedMsg struct {
	works     []work
	errs      []string
	note      string
	addressed []string // ids of sent comments whose quoted lines changed
}

// reviewInputs lists the agents whose work can be reviewed: every agent
// with a transcript. Where the work is comes from the transcript, not the
// session's folder: an agent started in ~ edits files in whatever
// repositories it is asked to.
func (m Model) reviewInputs() []reviewInput {
	var in []reviewInput
	for _, a := range m.agents {
		if a.Path == "" {
			continue
		}
		in = append(in, reviewInput{
			SessionID: a.Session.SessionID,
			Name:      a.Name,
			CWD:       a.Session.CWD,
			HasGit:    a.HasGit,
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
	remote := m.loadRemoteReviews()
	in := m.reviewInputs()
	var open []review.Comment
	for _, c := range m.rev.comments {
		if c.Open() {
			open = append(open, c)
		}
	}
	return tea.Batch(remote, func() tea.Msg {
		msg := loadWorks(in)
		msg.addressed = addressedComments(open)
		return msg
	})
}

// loadWorks reads each agent's file changes from its transcript, finds the
// repositories those files live in, diffs each repository once, attributes
// its lines to every session that touched it, and splits the result into
// one work per agent. When nothing comes out it says why.
func loadWorks(in []reviewInput) reviewLoadedMsg {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	type member struct {
		reviewInput
		changes []transcript.TimedChange
	}
	byRoot := map[string][]member{}
	var roots []string
	var errs []string
	rootOf := map[string]string{} // directory → repository root, "" when none
	lookup := func(dir string) string {
		if r, ok := rootOf[dir]; ok {
			return r
		}
		r, err := review.Root(ctx, dir)
		if err != nil {
			r = ""
		}
		rootOf[dir] = r
		return r
	}
	join := func(root string, mem member) {
		for i := range byRoot[root] {
			if byRoot[root][i].SessionID == mem.SessionID {
				return
			}
		}
		if _, ok := byRoot[root]; !ok {
			roots = append(roots, root)
		}
		byRoot[root] = append(byRoot[root], mem)
	}
	changed, outside := 0, 0
	for _, a := range in {
		mem := member{reviewInput: a, changes: transcript.Changes(a.Path, a.Started)}
		changed += len(mem.changes)
		for _, c := range mem.changes {
			if !filepath.IsAbs(c.Path) {
				continue
			}
			if root := lookup(filepath.Dir(c.Path)); root != "" {
				join(root, mem)
			} else {
				outside++
			}
		}
		if a.HasGit && a.CWD != "" {
			if root := lookup(a.CWD); root != "" {
				join(root, mem)
			}
		}
	}

	var works []work
	diffed := 0
	for _, root := range roots {
		agents := byRoot[root]
		since := agents[0].Started
		for _, a := range agents[1:] {
			if a.Started.Before(since) {
				since = a.Started
			}
		}
		var paths []string
		for _, a := range agents {
			paths = append(paths, a.Path)
		}
		base, err := review.BaseFor(ctx, root, paths, since)
		if err == nil {
			var files []review.File
			files, err = review.Diff(ctx, root, base)
			if err == nil {
				diffed += len(files)
				var sources []review.Source
				for _, a := range agents {
					sources = append(sources, review.Source{SessionID: a.SessionID, Changes: a.changes})
				}
				review.Attribute(root, files, sources)
				ws := splitWorks(root, files, sources)
				annotateWorktree(ctx, root, ws)
				works = append(works, ws...)
			}
		}
		if err != nil {
			errs = append(errs, filepath.Base(root)+": "+err.Error())
		}
	}
	sort.SliceStable(works, func(i, j int) bool { return works[i].Last.After(works[j].Last) })
	return reviewLoadedMsg{works: works, errs: errs, note: emptyNote(len(in), changed, outside, len(roots), diffed, len(works))}
}

// emptyNote explains an empty Review view from what the load found.
func emptyNote(agents, changed, outside, repos, diffed, works int) string {
	switch {
	case works > 0:
		return ""
	case agents == 0:
		return "No agent has a transcript to read changes from."
	case changed == 0:
		return "No agent has changed a file since it started."
	case repos == 0:
		return fmt.Sprintf("Agents changed %d files, none inside a git repository, so there is no diff to show.", changed)
	case diffed == 0:
		return fmt.Sprintf("Agents changed %d files in %d repositories, but the working trees match the branch they left from: the work is merged or reverted.", changed, repos)
	default:
		return fmt.Sprintf("%d files differ in %d repositories, but none of the changed lines match what an agent wrote; they may have been edited by hand since.", diffed, repos)
	}
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
	m.rev.loading = false
	m.rev.loadedAt = time.Now()
	m.markAddressed(msg.addressed)
	m.rev.local, m.rev.errs, m.rev.note = msg.works, msg.errs, msg.note
	m.mergeReview()
}

// mergeReview lists the local work and the remote work of live hosts,
// filtered to the picked project, keeping the selection on the same agent
// and file where they still exist.
func (m *Model) mergeReview() {
	var keepWork, keepFile string
	if w := m.curWork(); w != nil {
		keepWork = w.SessionID + "\x00" + w.Root
		if f := m.curFile(); f != nil {
			keepFile = f.Path
		}
	}
	remote := m.remoteWorks()
	sort.Slice(remote, func(i, j int) bool { return remote[i].SessionID < remote[j].SessionID })
	m.rev.all = append(append([]work(nil), m.rev.local...), remote...)
	m.rev.works = nil
	for _, w := range m.rev.all {
		if m.proj.cur == "" || w.project() == m.proj.cur {
			m.rev.works = append(m.rev.works, w)
		}
	}
	if len(m.rev.works) == 0 && len(m.rev.all) > 0 {
		if p, ok := m.curProject(); ok {
			m.rev.note = "No agent has changed anything in " + p.Name + " since it started. ← → or /project picks another project."
		}
	}
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
