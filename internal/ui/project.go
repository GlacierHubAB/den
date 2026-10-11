package ui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/gitinfo"
	"github.com/GlacierHubAB/den/internal/spawn"
	"github.com/GlacierHubAB/den/internal/transcript"
	"github.com/GlacierHubAB/den/internal/worktree"
)

// A project is a repository agents work in: found through the files their
// transcripts say they edited, and through session folders that are
// checkouts. The Overview and Review views show one project at a time.
type project struct {
	Root   string
	Name   string // repository name
	Branch string
	Agents []string // session ids working in it
}

// projectState is the pick and what it is chosen from.
type projectState struct {
	list   []project
	cur    string   // root of the picked project
	pinned []string // roots listed whether or not anyone works there
	loose  int      // agents in no repository
	path   string   // where the pick is remembered
	loaded bool
}

var (
	rootMu    sync.Mutex
	rootCache = map[string]string{} // directory → repository root, "" when none
)

// gitRootOf is the repository root above dir, remembered per directory so
// a refresh costs one git call per new folder, not per file. A linked
// worktree maps to the main checkout it belongs to, so an agent working in
// one shows under its project.
func gitRootOf(dir string) string {
	rootMu.Lock()
	r, ok := rootCache[dir]
	rootMu.Unlock()
	if ok {
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r = worktree.MainRoot(ctx, dir)
	rootMu.Lock()
	rootCache[dir] = r
	rootMu.Unlock()
	return r
}

// projectPath is where the picked project is remembered between runs.
func projectPath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "den", "overview.json")
}

func (m *Model) loadProjectPick() {
	if m.proj.path == "" {
		m.proj.path = projectPath()
	}
	raw, err := os.ReadFile(m.proj.path)
	if err != nil {
		return
	}
	var v struct {
		Project string   `json:"project"`
		Pinned  []string `json:"pinned"`
	}
	if json.Unmarshal(raw, &v) == nil {
		m.proj.cur = v.Project
		m.proj.pinned = v.Pinned
	}
}

func (m *Model) saveProjectPick() {
	if m.proj.path == "" {
		return
	}
	raw, _ := json.Marshal(map[string]any{"project": m.proj.cur, "pinned": m.proj.pinned})
	if os.MkdirAll(filepath.Dir(m.proj.path), 0o755) == nil {
		_ = os.WriteFile(m.proj.path, raw, 0o644)
	}
}

// pinProject lists a folder as a project whether or not anyone works there
// (its repository root when it is inside one), picks it, and remembers it.
func (m *Model) pinProject(dir string) (project, error) {
	dir, err := spawn.ExpandDir(dir)
	if err != nil {
		return project{}, err
	}
	root := gitRootOf(dir)
	if root == "" {
		root = dir
	}
	if !slices.Contains(m.proj.pinned, root) {
		m.proj.pinned = append(m.proj.pinned, root)
	}
	m.proj.cur = root
	m.ov.cur, m.ov.open, m.ov.scroll = 0, "", 0
	m.buildProjects()
	m.saveProjectPick()
	m.filterReview()
	p, _ := m.projectByRoot(root)
	return p, nil
}

// unpinProject forgets the picked project's pin; it stays listed while
// anyone works there. It reports whether it was pinned.
func (m *Model) unpinProject() bool {
	i := slices.Index(m.proj.pinned, m.proj.cur)
	if i < 0 {
		return false
	}
	m.proj.pinned = slices.Delete(m.proj.pinned, i, i+1)
	m.buildProjects()
	m.saveProjectPick()
	m.filterReview()
	return true
}

// addProject lists a project found by root when it is not listed yet.
func addProject(byRoot map[string]*project, root string) *project {
	p := byRoot[root]
	if p != nil {
		return p
	}
	p = &project{Root: root, Name: filepath.Base(root)}
	if info, ok := gitinfo.Lookup(root); ok {
		if info.Repo != "" {
			p.Name = info.Repo
		}
		p.Branch = info.Branch
	}
	byRoot[root] = p
	return p
}

// recentEdits is how long an edit keeps an agent on a project that is not
// its own folder's. A side errand (a skill dropped into another repository,
// a fix in a sibling checkout) should not list the agent there all session.
const recentEdits = 30 * time.Minute

// rootsOf lists the repositories an agent works in: the one its folder is
// in, the one it edited last, and any it edited in the last recentEdits.
func rootsOf(a fleet.Agent) []string {
	return rootsAt(a, time.Now())
}

func rootsAt(a fleet.Agent, now time.Time) []string {
	seen := map[string]bool{}
	var out []string
	add := func(dir string) {
		if dir == "" {
			return
		}
		if r := gitRootOf(dir); r != "" && !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	if a.HasGit {
		add(a.Session.CWD)
	}
	if a.Path != "" {
		var last string
		var lastAt time.Time
		dirs := map[string]bool{}
		for _, c := range transcript.Changes(a.Path, time.UnixMilli(a.Session.StartedAt)) {
			if !filepath.IsAbs(c.Path) {
				continue
			}
			dir := filepath.Dir(c.Path)
			if !c.At.Before(lastAt) {
				last, lastAt = dir, c.At
			}
			if c.At.IsZero() || now.Sub(c.At) <= recentEdits {
				dirs[dir] = true
			}
		}
		add(last)
		for d := range dirs {
			add(d)
		}
	}
	return out
}

// buildProjects lists the projects of the current fleet and keeps the pick
// on one that exists: the remembered one, else the busiest.
func (m *Model) buildProjects() {
	byRoot := map[string]*project{}
	m.proj.loose = 0
	for _, a := range m.agents {
		if a.Status == fleet.Gone {
			continue
		}
		roots := rootsOf(a)
		if len(roots) == 0 {
			m.proj.loose++
		}
		for _, r := range roots {
			p := addProject(byRoot, r)
			p.Agents = append(p.Agents, a.Session.SessionID)
		}
	}
	// Projects with missions stay listed while the missions live, and
	// pinned ones always, even with nobody on them right now.
	if m.missions != nil {
		for _, ms := range m.missions.All() {
			if ms.Live() {
				addProject(byRoot, ms.Project)
			}
		}
	}
	for _, root := range m.proj.pinned {
		if st, err := os.Stat(root); err == nil && st.IsDir() {
			addProject(byRoot, root)
		}
	}
	var list []project
	for _, p := range byRoot {
		list = append(list, *p)
	}
	// Two checkouts of one repository (a worktree beside the main one)
	// share a name; tell them apart by branch, else by folder.
	names := map[string]int{}
	for _, p := range list {
		names[p.Name]++
	}
	for i, p := range list {
		if names[p.Name] > 1 {
			if p.Branch != "" {
				list[i].Name = p.Name + " · " + p.Branch
			} else {
				list[i].Name = p.Name + " · " + filepath.Base(p.Root)
			}
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		if len(list[i].Agents) != len(list[j].Agents) {
			return len(list[i].Agents) > len(list[j].Agents)
		}
		return list[i].Name < list[j].Name
	})
	m.proj.list = list
	if _, ok := m.projectByRoot(m.proj.cur); !ok {
		m.proj.cur = ""
		if len(list) > 0 {
			m.proj.cur = list[0].Root
		}
	}
}

func (m Model) projectByRoot(root string) (project, bool) {
	for _, p := range m.proj.list {
		if p.Root == root {
			return p, true
		}
	}
	return project{}, false
}

// curProject is the picked project.
func (m Model) curProject() (project, bool) { return m.projectByRoot(m.proj.cur) }

// switchProject moves the pick by delta through the list.
func (m *Model) switchProject(delta int) {
	n := len(m.proj.list)
	if n == 0 {
		return
	}
	i := 0
	for j, p := range m.proj.list {
		if p.Root == m.proj.cur {
			i = j
		}
	}
	m.proj.cur = m.proj.list[(i+delta+n)%n].Root
	m.ov.cur, m.ov.open, m.ov.scroll = 0, "", 0
	m.saveProjectPick()
	m.filterReview()
}

// pickProject picks a project by name or root; it reports whether one
// matched.
func (m *Model) pickProject(name string) bool {
	for _, p := range m.proj.list {
		if p.Name == name || p.Root == name || filepath.Base(p.Root) == name {
			m.proj.cur = p.Root
			m.ov.cur, m.ov.open, m.ov.scroll = 0, "", 0
			m.saveProjectPick()
			m.filterReview()
			return true
		}
	}
	return false
}

// projectAgents lists the fleet agents on the picked project, in fleet
// order.
func (m Model) projectAgents() []fleet.Agent {
	p, ok := m.curProject()
	if !ok {
		return nil
	}
	in := map[string]bool{}
	for _, sid := range p.Agents {
		in[sid] = true
	}
	var out []fleet.Agent
	for _, a := range m.agents {
		if in[a.Session.SessionID] {
			out = append(out, a)
		}
	}
	return out
}

// filterReview keeps the Review view on the picked project.
func (m *Model) filterReview() {
	if m.rev.all == nil {
		return
	}
	m.applyReview(reviewLoadedMsg{works: m.rev.all, errs: m.rev.errs, note: m.rev.note})
}
