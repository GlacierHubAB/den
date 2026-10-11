package ui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/gitinfo"
	"github.com/GlacierHubAB/den/internal/review"
	"github.com/GlacierHubAB/den/internal/transcript"
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
	cur    string // root of the picked project
	loose  int    // agents in no repository
	path   string // where the pick is remembered
	loaded bool
}

var (
	rootMu    sync.Mutex
	rootCache = map[string]string{} // directory → repository root, "" when none
)

// gitRootOf is the repository root above dir, remembered per directory so
// a refresh costs one git call per new folder, not per file.
func gitRootOf(dir string) string {
	rootMu.Lock()
	r, ok := rootCache[dir]
	rootMu.Unlock()
	if ok {
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := review.Root(ctx, dir)
	if err != nil {
		r = ""
	}
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
	m.proj.path = projectPath()
	raw, err := os.ReadFile(m.proj.path)
	if err != nil {
		return
	}
	var v struct {
		Project string `json:"project"`
	}
	if json.Unmarshal(raw, &v) == nil {
		m.proj.cur = v.Project
	}
}

func (m *Model) saveProjectPick() {
	if m.proj.path == "" {
		return
	}
	raw, _ := json.Marshal(map[string]string{"project": m.proj.cur})
	if os.MkdirAll(filepath.Dir(m.proj.path), 0o755) == nil {
		_ = os.WriteFile(m.proj.path, raw, 0o644)
	}
}

// rootsOf lists the repositories an agent works in.
func rootsOf(a fleet.Agent) []string {
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
		dirs := map[string]bool{}
		for _, c := range transcript.Changes(a.Path, time.UnixMilli(a.Session.StartedAt)) {
			if filepath.IsAbs(c.Path) {
				dirs[filepath.Dir(c.Path)] = true
			}
		}
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
			p := byRoot[r]
			if p == nil {
				p = &project{Root: r, Name: filepath.Base(r)}
				if info, ok := gitinfo.Lookup(r); ok {
					if info.Repo != "" {
						p.Name = info.Repo
					}
					p.Branch = info.Branch
				}
				byRoot[r] = p
			}
			p.Agents = append(p.Agents, a.Session.SessionID)
		}
	}
	// Projects with missions stay listed while the missions live, even
	// with nobody on them right now.
	if m.missions != nil {
		for _, ms := range m.missions.All() {
			if ms.Live() && byRoot[ms.Project] == nil {
				p := &project{Root: ms.Project, Name: filepath.Base(ms.Project)}
				if info, ok := gitinfo.Lookup(ms.Project); ok && info.Repo != "" {
					p.Name, p.Branch = info.Repo, info.Branch
				}
				byRoot[ms.Project] = p
			}
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
	if _, ok := m.projectByRoot(m.proj.cur); !ok && len(list) > 0 {
		m.proj.cur = list[0].Root
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
