package ui

import (
	"strings"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// The Fleet by project (/fleet project, /fleet flat to go back): the agents
// under a header per project, each with the trees whose root is on it, in
// the project list's order, then those in no project. A tree is never
// split: the agents under a lead show where the lead does. The
// orchestrator's trees come up a level, each one its own, as the
// orchestrator has no row here.

// projectSection starts the section name of a project's group; what
// follows is the project's name, "" for no project.
const projectSection = "project\x00"

// groupByProject reorders the fleet into project groups when the Fleet is
// shown by project. It runs after buildTree and buildProjects.
func (m *Model) groupByProject() {
	if !m.proj.byProject {
		return
	}
	of := map[string]int{} // session id → index in the project list
	for i, p := range m.proj.list {
		for _, sid := range p.Agents {
			of[sid] = i
		}
	}
	type block struct {
		proj int
		rows []fleet.Agent
	}
	var blocks []block
	var stale []fleet.Agent
	for _, a := range m.agents {
		id := a.Session.SessionID
		if a.Stale {
			stale = append(stale, a) // under its host's row, as ever
			continue
		}
		r := m.tree.rows[id]
		if r.section == "orchestrator" {
			r.depth--
			r.guide, r.cont = dropGuide(r.guide), dropGuide(r.cont)
			// The orchestrator's fold has no row to open it here.
			r.hidden = false
			if r.depth > 0 {
				if p, ok := m.tree.rows[r.parent]; ok {
					r.hidden = p.hidden || p.folded
				}
			}
		}
		if r.depth <= 0 || len(blocks) == 0 {
			r.depth = max(r.depth, 0)
			p, ok := of[id]
			if !ok {
				p = -1
			}
			blocks = append(blocks, block{proj: p})
		}
		m.tree.rows[id] = r
		blocks[len(blocks)-1].rows = append(blocks[len(blocks)-1].rows, a)
	}
	ordered := make([]fleet.Agent, 0, len(m.agents))
	place := func(proj int, name string) {
		for _, b := range blocks {
			if b.proj != proj {
				continue
			}
			for _, a := range b.rows {
				r := m.tree.rows[a.Session.SessionID]
				r.section = projectSection + name
				m.tree.rows[a.Session.SessionID] = r
				ordered = append(ordered, a)
			}
		}
	}
	for i, p := range m.proj.list {
		place(i, sanitize.Line(p.Name))
	}
	place(-1, "")
	m.agents = append(ordered, stale...)
}

// dropGuide takes one level (three cells) off a tree guide.
func dropGuide(g string) string {
	r := []rune(g)
	if len(r) < 3 {
		return ""
	}
	return string(r[3:])
}

// projectHeader is a project group's header: its name, or "no project".
func projectHeader(sec string) (string, bool) {
	name, ok := strings.CutPrefix(sec, projectSection)
	if !ok {
		return "", false
	}
	if name == "" {
		name = "no project"
	}
	return name, true
}
