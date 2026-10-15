package ui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/gitinfo"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/spawn"
	"github.com/GlacierHubAB/den/internal/transcript"
	"github.com/GlacierHubAB/den/internal/worktree"
)

// A project is a repository agents work in. Each agent is under one
// project: that of the live mission it serves, else of the mission Den
// last put it on, else the checkout its folder is in. Projects with live
// missions and pinned ones are listed with nobody on them too. The
// Overview and Review views show one project at a time.
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
	// byProject shows the Fleet grouped by project (/fleet project),
	// remembered with the pick.
	byProject bool
	loaded    bool
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
		Fleet   string   `json:"fleet"`
	}
	if json.Unmarshal(raw, &v) == nil {
		m.proj.cur = v.Project
		m.proj.pinned = v.Pinned
		m.proj.byProject = v.Fleet == "project"
	}
}

func (m *Model) saveProjectPick() {
	if m.proj.path == "" {
		return
	}
	v := map[string]any{"project": m.proj.cur, "pinned": m.proj.pinned}
	if m.proj.byProject {
		v["fleet"] = "project"
	}
	raw, _ := json.Marshal(v)
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
// The close guard checks them all; the project list uses one (agentRoot).
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
	byCard := m.cardMissions()
	assigned := m.assignments()
	for _, a := range m.agents {
		if a.Status == fleet.Gone {
			continue
		}
		if a.Host != "" {
			// A remote checkout is its own project, (host, repository),
			// even of a repository that is also checked out here.
			if !a.HasGit || a.Git.Repo == "" {
				m.proj.loose++
				continue
			}
			root := remoteProject(a.Host, a.Git.Repo)
			p := byRoot[root]
			if p == nil {
				p = &project{Root: root, Name: root, Branch: a.Git.Branch}
				byRoot[root] = p
			}
			p.Agents = append(p.Agents, a.Session.SessionID)
			continue
		}
		root := m.agentRoot(a, byCard, assigned)
		if root == "" {
			m.proj.loose++
			continue
		}
		p := addProject(byRoot, root)
		p.Agents = append(p.Agents, a.Session.SessionID)
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
		if names[p.Name] > 1 && projectHost(p.Root) == "" {
			if p.Branch != "" {
				list[i].Name = p.Name + " · " + p.Branch
			} else {
				list[i].Name = p.Name + " · " + filepath.Base(p.Root)
			}
		}
	}
	// A folder's name, a branch and a remote repository's name are
	// anyone's to choose: shown, they are one clean line.
	for i := range list {
		list[i].Name, list[i].Branch = sanitize.Line(list[i].Name), sanitize.Line(list[i].Branch)
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

// held is one mission a session was put on, from the mission's record.
type held struct {
	ms    *mission.Mission
	since time.Time
	open  bool
}

// assignments indexes the missions' records by the local session they put
// on a mission. Only missions in a project on this machine count.
func (m Model) assignments() map[string][]held {
	out := map[string][]held{}
	if m.missions == nil {
		return out
	}
	for _, ms := range m.missions.All() {
		if ms.Project == "" || projectHost(ms.Project) != "" || ms.State == mission.Dropped {
			continue
		}
		for _, as := range ms.Assigned {
			if as.Host == "" {
				out[as.Session] = append(out[as.Session], held{ms, as.Since, as.Open()})
			}
		}
	}
	return out
}

// agentRoot is the one project a local agent shows under: that of the live
// mission it serves that started last for it (Den put it on the mission,
// or it works for an agent Den did); else that of the mission Den last put
// it on; else the checkout its folder is in. "" when it is in none.
func (m Model) agentRoot(a fleet.Agent, byCard map[string]string, assigned map[string][]held) string {
	sid := a.Session.SessionID
	var best *mission.Mission
	var bestAt time.Time
	consider := func(ms *mission.Mission, at time.Time) {
		if ms == nil || !ms.Live() || ms.Project == "" || projectHost(ms.Project) != "" {
			return
		}
		if best == nil || at.After(bestAt) {
			best, bestAt = ms, at
		}
	}
	mine := map[*mission.Mission]bool{}
	for _, h := range assigned[sid] {
		if h.open {
			consider(h.ms, h.since)
			mine[h.ms] = true
		}
	}
	// Through a card or the agent that directs it, when Den's record has
	// no open assignment of its own: since the mission's current run.
	if m.missions != nil {
		if ms := m.missions.Get(m.missionVia(sid, byCard)); ms != nil && !mine[ms] {
			at := ms.Created
			if r := ms.CurrentRun(); r != nil {
				at = r.StartedAt
			}
			consider(ms, at)
		}
	}
	if best != nil {
		return best.Project
	}
	var last held
	for _, h := range assigned[sid] {
		if last.ms == nil || h.since.After(last.since) {
			last = h
		}
	}
	if last.ms != nil {
		return last.ms.Project
	}
	if a.HasGit {
		return gitRootOf(a.Session.CWD)
	}
	return ""
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
	m.mergeReview()
}

// remoteProject is the root, and the name, of a repository on a remote
// host: "den @devbox". It is no path, so nothing local ever runs in it.
func remoteProject(host, repo string) string { return repo + " @" + host }

// projectHost is the host of a remote project's root, "" for a local one.
func projectHost(root string) string {
	if strings.HasPrefix(root, "/") {
		return ""
	}
	if i := strings.LastIndex(root, " @"); i >= 0 {
		return root[i+2:]
	}
	return ""
}
