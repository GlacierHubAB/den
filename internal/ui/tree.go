package ui

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/tree"
)

// rowMeta is what the Fleet view knows about an agent's place in the tree
// of who directs whom. Agents outside any tree have the zero value.
type rowMeta struct {
	section string // the agents pane section this row sits in
	depth   int
	guide   string // the guide drawn before the first line: "│  ├─ "
	cont    string // the guide under it, for the task and meta lines
	parent  string
	directs int  // children in the fleet
	folded  bool // its branch is hidden
	hidden  bool // inside a folded branch
}

// treeState is the Fleet view's tree: the edges, what is folded, and the
// layout of the current snapshot.
type treeState struct {
	edges   *tree.Set
	path    string          // the record on disk, "" to keep it in memory
	folded  map[string]bool // parent id → folded
	rows    map[string]rowMeta
	roots   []string            // tree roots in display order; tree.Orchestrator first when it has children
	pending []pendingSpawn      // spawned agents whose session is not in the fleet yet
	spawns  map[string]spawnReq // the orchestrator's spawn calls by tool use id, until they resolve
	mapView bool
	mapTop  int // first canvas row shown in the map
}

// spawnReq is what a spawn_agent call asked for.
type spawnReq struct {
	brief, mission string
	at             time.Time
}

// pendingSpawn is a spawn whose session Den has not seen in the fleet yet.
type pendingSpawn struct {
	peer      string
	parent    string // session id, or tree.Orchestrator
	toolUseID string // the orchestrator's call, "" for a lead's own spawn
	brief     string
	mission   string
	at        time.Time
}

func newTreeState() treeState {
	return treeState{edges: tree.New(), folded: map[string]bool{}, rows: map[string]rowMeta{}, spawns: map[string]spawnReq{}}
}

// ensureTree gives a zero Model (tests build one) a working, in-memory tree.
func (m *Model) ensureTree() {
	if m.tree.edges == nil {
		path := m.tree.path
		m.tree = newTreeState()
		m.tree.path = path
	}
}

// learnEdge records that parent gave child its task and remembers it on
// disk. A no-op when nothing changes.
func (m *Model) learnEdge(e tree.Edge) {
	m.ensureTree()
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if !m.tree.edges.Add(e) {
		return
	}
	_ = tree.Append(m.tree.path, e) // best effort: the tree works without the file
}

// backfillEdges seeds the tree from the restored chat when the record on
// disk has nothing yet: every dispatch card from the last day is an edge
// from the orchestrator, so a Den upgraded mid-flight still shows who is
// working for it. Pruning drops the ones whose sessions are gone.
func (m *Model) backfillEdges() {
	m.ensureTree()
	if m.tree.edges.Len() > 0 {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, e := range m.orch.chat {
		if e.card == nil || e.card.SessionID == "" || e.card.SentAt.Before(cutoff) {
			continue
		}
		m.learnEdge(tree.Edge{Parent: tree.Orchestrator, Child: e.card.SessionID, Brief: firstLineOf(e.card.Brief), At: e.card.SentAt, Source: "dispatch"})
	}
}

// learnPeer reads a peer message's sender out of a UserPromptSubmit hook:
// the sender directs the receiver from now on, unless the receiver already
// sits above the sender, in which case this is a report back.
func (m *Model) learnPeer(sessionID, prompt string) {
	pid, name, brief, ok := tree.Peer(prompt)
	if !ok {
		return
	}
	parent := ""
	if name == OrchestratorName {
		parent = tree.Orchestrator
	}
	for _, a := range m.base {
		if a.Session.PID != pid {
			continue
		}
		if m.isOrchestrator(a) {
			parent = tree.Orchestrator
		} else {
			parent = a.Session.SessionID
		}
		break
	}
	if parent == "" {
		return
	}
	m.learnEdge(tree.Edge{Parent: parent, Child: sessionID, Brief: brief, Source: "peer"})
}

var spawnedPeer = regexp.MustCompile(`\(peer "([^"]+)"\)`)

// noteSpawn remembers what one of the orchestrator's spawn calls asked for,
// so the agent's card can carry the brief once the session shows up.
func (m *Model) noteSpawn(toolUseID, brief, mission string, at time.Time) {
	m.ensureTree()
	if m.tree.spawns == nil {
		m.tree.spawns = map[string]spawnReq{}
	}
	m.tree.spawns[toolUseID] = spawnReq{brief: brief, mission: mission, at: at}
}

// learnSpawn reads the peer name out of a spawn tool's answer: parent
// started it. The orchestrator's own calls are known by tool use id; a
// lead's calls come through its PostToolUse hook with the brief. The
// session may not be in the fleet yet, so the spawn waits in pending.
func (m *Model) learnSpawn(result, parent, toolUseID, brief string) {
	m.ensureTree()
	sm := spawnedPeer.FindStringSubmatch(result)
	if sm == nil || parent == "" {
		return
	}
	p := pendingSpawn{peer: sm[1], parent: parent, toolUseID: toolUseID, brief: brief, at: time.Now()}
	if req, ok := m.tree.spawns[toolUseID]; ok && toolUseID != "" {
		p.brief, p.mission, p.at = req.brief, req.mission, req.at
		delete(m.tree.spawns, toolUseID)
	}
	m.tree.pending = append(m.tree.pending, p)
	m.resolvePending()
}

func (m *Model) resolvePending() {
	var keep []pendingSpawn
	for _, p := range m.tree.pending {
		name, sid := m.agentForPeer(p.peer)
		if sid == "" {
			if time.Since(p.at) < time.Hour {
				keep = append(keep, p)
			}
			continue
		}
		brief := firstLineOf(p.brief)
		if brief == "" {
			brief = "spawned"
		}
		m.learnEdge(tree.Edge{Parent: p.parent, Child: sid, Brief: brief, Source: "spawn"})
		if p.parent == tree.Orchestrator && p.toolUseID != "" {
			m.cardForSpawn(p, name, sid)
		}
	}
	m.tree.pending = keep
}

// cardForSpawn turns the orchestrator's spawn note into a dispatch card:
// the brief was the agent's first prompt, so it is working on it already.
func (m *Model) cardForSpawn(p pendingSpawn, name, sid string) {
	for i := range m.orch.chat {
		e := &m.orch.chat[i]
		if e.toolUseID != p.toolUseID || e.card != nil {
			continue
		}
		now := time.Now()
		d := &dispatch{ID: p.toolUseID, SessionID: sid, Agent: name, Brief: strings.TrimSpace(p.brief), SentAt: p.at, StartedAt: now, State: cardWorking, Reason: "started"}
		if d.SentAt.IsZero() {
			d.SentAt = now
		}
		e.role, e.card, e.text, e.status = "card", d, "", ""
		m.attachCardTo(d, p.mission)
		m.logCard(d)
		return
	}
}

// subtreeBusy reports whether anyone under the session is still at work:
// a lead's card is not done while its people are.
func (m Model) subtreeBusy(sid string) (busy int) {
	for _, id := range m.descendants(sid) {
		if a, ok := m.agentFor(id); ok && (a.Status == fleet.Working || a.Status == fleet.NeedsYou) {
			busy++
			continue
		}
		for _, d := range m.cards() {
			if d.SessionID == id && d.live() {
				busy++
				break
			}
		}
	}
	return busy
}

// buildTree lays the fleet out as trees: agents under whoever directs them,
// in the order the edges were made, then everyone else as today. It
// reorders m.agents and fills the row metadata the panes draw from.
func (m *Model) buildTree() {
	m.ensureTree()
	m.resolvePending()
	byID := map[string]int{}
	for i, a := range m.agents {
		byID[a.Session.SessionID] = i
	}
	present := func(id string) bool { _, ok := byID[id]; return ok }
	m.tree.edges.Prune(func(id string) bool { return id == tree.Orchestrator || present(id) })
	m.tree.rows = map[string]rowMeta{}
	m.tree.roots = nil
	children := func(parent string) []string {
		var out []string
		for _, e := range m.tree.edges.Children(parent) {
			if present(e.Child) {
				out = append(out, e.Child)
			}
		}
		return out
	}
	inTree := map[string]bool{}
	var ordered []fleet.Agent
	var walk func(id string, depth int, prefix string, last bool, section string, hidden bool)
	walk = func(id string, depth int, prefix string, last bool, section string, hidden bool) {
		kids := children(id)
		meta := rowMeta{section: section, depth: depth, parent: m.parentOf(id), directs: len(kids), folded: m.tree.folded[id] && len(kids) > 0, hidden: hidden}
		if depth > 0 {
			branch := "├─ "
			if last {
				branch = "└─ "
			}
			meta.guide = prefix + branch
			if last {
				meta.cont = prefix + "   "
			} else {
				meta.cont = prefix + "│  "
			}
		}
		m.tree.rows[id] = meta
		inTree[id] = true
		ordered = append(ordered, m.agents[byID[id]])
		for i, kid := range kids {
			walk(kid, depth+1, meta.cont, i == len(kids)-1, section, hidden || meta.folded)
		}
	}
	// The orchestrator's tree first, then agents that direct others and are
	// directed by nobody in the fleet, in the fleet's own order.
	if kids := children(tree.Orchestrator); len(kids) > 0 {
		m.tree.roots = append(m.tree.roots, tree.Orchestrator)
		for i, kid := range kids {
			walk(kid, 1, "", i == len(kids)-1, "orchestrator", m.tree.folded[tree.Orchestrator])
		}
	}
	for _, a := range m.agents {
		id := a.Session.SessionID
		if inTree[id] || len(children(id)) == 0 {
			continue
		}
		if p, ok := m.tree.edges.Parent(id); ok && present(p.Parent) {
			continue
		}
		m.tree.roots = append(m.tree.roots, id)
		walk(id, 0, "", true, strings.ToLower(a.Name), false)
	}
	for _, a := range m.agents {
		if !inTree[a.Session.SessionID] {
			ordered = append(ordered, a)
		}
	}
	m.agents = ordered
}

// parentOf is the id directing the agent, "" when nobody in the fleet does.
func (m Model) parentOf(id string) string {
	if m.tree.edges == nil {
		return ""
	}
	e, ok := m.tree.edges.Parent(id)
	if !ok {
		return ""
	}
	if e.Parent == tree.Orchestrator {
		return tree.Orchestrator
	}
	if _, ok := m.agentFor(e.Parent); ok {
		return e.Parent
	}
	return ""
}

// rowFor is the tree metadata of an agent row.
func (m Model) rowFor(id string) rowMeta { return m.tree.rows[id] }

// sectionOf names the agents pane section an agent belongs to: its tree's
// root, or its status.
func (m Model) sectionOf(a fleet.Agent) string {
	if r, ok := m.tree.rows[a.Session.SessionID]; ok && r.section != "" {
		return r.section
	}
	return a.Status.String()
}

// childrenOf lists the fleet agents the session directs, oldest edge first.
func (m Model) childrenOf(id string) []fleet.Agent {
	if m.tree.edges == nil {
		return nil
	}
	var out []fleet.Agent
	for _, e := range m.tree.edges.Children(id) {
		if a, ok := m.agentFor(e.Child); ok {
			out = append(out, a)
		}
	}
	return out
}

// edgeFor is the edge that put the agent under its parent.
func (m Model) edgeFor(id string) tree.Edge {
	if m.tree.edges == nil {
		return tree.Edge{}
	}
	e, _ := m.tree.edges.Parent(id)
	return e
}

// descendants lists every session under the given one, parents before
// their children.
func (m Model) descendants(id string) []string {
	var out []string
	seen := map[string]bool{id: true}
	var walk func(string)
	walk = func(p string) {
		for _, a := range m.childrenOf(p) {
			sid := a.Session.SessionID
			if seen[sid] {
				continue
			}
			seen[sid] = true
			out = append(out, sid)
			walk(sid)
		}
	}
	walk(id)
	return out
}

// rollup counts what the descendants of a session are up to.
type rollup struct{ needsYou, working, done, gone, total int }

// via names the first descendant that needs the human, "" when none does.
func (m Model) rollupOf(id string) (r rollup, via string) {
	seen := map[string]bool{id: true}
	var walk func(string)
	walk = func(p string) {
		for _, a := range m.childrenOf(p) {
			sid := a.Session.SessionID
			if seen[sid] {
				continue
			}
			seen[sid] = true
			r.total++
			switch a.Status {
			case fleet.NeedsYou:
				r.needsYou++
				if via == "" {
					via = a.Name
				}
			case fleet.Working:
				r.working++
			case fleet.Gone:
				r.gone++
			default:
				r.done++
			}
			walk(sid)
		}
	}
	walk(id)
	return r, via
}

// chainOf lists the names above an agent, root first, each in its colour.
func (m Model) chainOf(id string) []string {
	if m.tree.edges == nil {
		return nil
	}
	var out []string
	for _, p := range m.tree.edges.Chain(id) {
		if p == tree.Orchestrator {
			out = append(out, m.orchStyle().Render("orchestrator"))
			continue
		}
		a, ok := m.agentFor(p)
		if !ok {
			continue
		}
		out = append(out, lipgloss.NewStyle().Foreground(lipgloss.Color(m.agentColor(a))).Render(a.Name))
	}
	return out
}

// chainLine draws the chain of command for the agent: "you › orchestrator
// › Mossy Lemur", ending with the agent itself when withSelf is set.
func (m Model) chainLine(id string, withSelf bool) string {
	s := m.st
	parts := append([]string{s.fg.Render("you")}, m.chainOf(id)...)
	if withSelf {
		if a, ok := m.agentFor(id); ok {
			parts = append(parts, lipgloss.NewStyle().Foreground(lipgloss.Color(m.agentColor(a))).Bold(true).Render(a.Name))
		}
	}
	if len(parts) < 2 {
		return ""
	}
	return strings.Join(parts, s.muted.Render(" › "))
}

// viaLine is the dim "via orchestrator › Mossy Lemur" trailer an inbox item
// or card carries when the agent sits inside a tree.
func (m Model) viaLine(id string) string {
	chain := m.chainOf(id)
	if len(chain) == 0 {
		return ""
	}
	s := m.st
	return s.dim.Render("via ") + strings.Join(chain, s.muted.Render(" › "))
}

// idleTitle is what an idle notice says. Claude Code words it as waiting
// for you, but an agent someone directs is waiting for its lead, who gets
// its report and decides what comes next.
func (m Model) idleTitle(id, title string) string {
	switch p := m.parentOf(id); p {
	case "":
		return title
	case tree.Orchestrator:
		return "Waiting for the orchestrator"
	default:
		return "Waiting for its lead, " + m.nameFor(p)
	}
}

// childLines lists what an agent directs: one line per child with its
// state, name, brief and age. With guides, the lines hang off a card.
func (m Model) childLines(id string, w int, guides bool) []string {
	s := m.st
	kids := m.childrenOf(id)
	out := make([]string, 0, len(kids))
	for i, k := range kids {
		guide := ""
		if guides {
			guide = s.muted.Render("├ ")
			if i == len(kids)-1 {
				guide = s.muted.Render("└ ")
			}
		}
		color := lipgloss.NewStyle().Foreground(lipgloss.Color(m.agentColor(k)))
		edge := m.edgeFor(k.Session.SessionID)
		var glyph, state string
		age := humanAge(time.Since(edge.At))
		switch k.Status {
		case fleet.Working:
			glyph, state = color.Render("●"), color.Render("● working · "+age)
		case fleet.NeedsYou:
			glyph, state = s.attn.Render("◐"), s.attn.Render("◐ needs you")
		case fleet.Gone:
			glyph, state = s.err.Render("✕"), s.err.Render("✕ gone")
		default:
			glyph, state = s.ok.Render("✓"), s.ok.Render("✓ done · "+age)
		}
		brief := edge.Brief
		if brief == "" || brief == "spawned" {
			brief = k.Task()
		}
		left := guide + glyph + " " + color.Bold(true).Render(k.Name) + "  " + s.dim.Render("▸ "+brief)
		leftW := w - ansi.StringWidth(state) - 2
		out = append(out, justify(ansi.Truncate(left, max(8, leftW), "…"), state, w))
	}
	return out
}

// orchStyle is the orchestrator's colour: its session's body colour, or
// the bright text colour before it has one.
func (m Model) orchStyle() lipgloss.Style {
	c := m.st.pal.Bright
	if m.orch.proc != nil && m.orch.proc.SessionID != "" {
		c = m.colorFor(m.orch.proc.SessionID)
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(c))
}

// directsLabel is the "directs n · k needs you" trailer of a parent row.
func (m Model) directsLabel(id string, dim, attn lipgloss.Style) string {
	r := m.rowFor(id)
	if r.directs == 0 {
		return ""
	}
	label := fmt.Sprintf("directs %d", r.directs)
	if r.folded {
		label += " · folded"
	}
	out := dim.Render(label)
	if roll, _ := m.rollupOf(id); roll.needsYou > 0 {
		out += dim.Render(" · ") + attn.Render(fmt.Sprintf("%d needs you", roll.needsYou))
	}
	return out
}

// toggleFold folds or unfolds the selected parent. It reports whether the
// key was consumed.
func (m *Model) toggleFold(fold bool) bool {
	a, ok := m.selected()
	if !ok {
		return false
	}
	id := a.Session.SessionID
	r := m.rowFor(id)
	if r.directs == 0 || r.folded == fold {
		return false
	}
	m.tree.folded[id] = fold
	m.refresh()
	return true
}

// stepVisible moves the cursor by delta over the rows that are not hidden
// inside a folded branch.
func (m *Model) stepVisible(delta int) int {
	if len(m.agents) == 0 {
		return m.cursor
	}
	dir := 1
	if delta < 0 {
		dir = -1
	}
	cur := m.cursor
	for n := delta * dir; n > 0; n-- {
		next := cur
		for {
			next += dir
			if next < 0 || next >= len(m.agents) {
				return cur
			}
			if !m.rowFor(m.agents[next].Session.SessionID).hidden {
				break
			}
		}
		cur = next
	}
	return cur
}

// mapMove walks the map: ← to the parent, → to the first child.
func (m *Model) mapMove(toParent bool) {
	a, ok := m.selected()
	if !ok {
		return
	}
	target := ""
	if toParent {
		target = m.rowFor(a.Session.SessionID).parent
	} else if kids := m.childrenOf(a.Session.SessionID); len(kids) > 0 {
		target = kids[0].Session.SessionID
	}
	for i, b := range m.agents {
		if b.Session.SessionID == target {
			m.cursor = i
			return
		}
	}
}

// ---- the map: a horizontal tree, one column per level ----

const (
	mapNodeW  = 8 + 2 + 20 // avatar, gap, text
	mapYouW   = 8          // "you" and "this terminal" need no avatar
	mapOrchW  = 18         // nor does the orchestrator
	mapNodeH  = 4
	mapGapX   = 4 // "─┬─ " between a node and its children
	mapGapY   = 1
	mapCentre = 1 // the row of a node its connector meets
)

// mapWidth is the columns a node takes.
func mapWidth(id string) int {
	switch id {
	case "you":
		return mapYouW
	case tree.Orchestrator:
		return mapOrchW
	}
	return mapNodeW
}

// canvas is a grid of rendered cells. A styled fragment wider than one
// column occupies its first cell and marks the rest as covered.
type canvas struct {
	w, h  int
	cells [][]string
}

const covered = "\x00"

func newCanvas(w, h int) *canvas {
	c := &canvas{w: w, h: h, cells: make([][]string, h)}
	for y := range c.cells {
		c.cells[y] = make([]string, w)
	}
	return c
}

// put places a rendered fragment of the given width at x,y; anything off
// the canvas is dropped.
func (c *canvas) put(x, y int, s string, width int) {
	if y < 0 || y >= c.h || x >= c.w || x+width <= 0 {
		return
	}
	if x < 0 || x+width > c.w {
		return // partial fragments are not worth the splitting
	}
	c.cells[y][x] = s
	for i := 1; i < width; i++ {
		c.cells[y][x+i] = covered
	}
}

// text places a plain string one cell at a time, so later fragments can
// cross it (the connectors do).
func (c *canvas) text(x, y int, s string, st lipgloss.Style) {
	for _, r := range s {
		if x >= 0 && x < c.w && y >= 0 && y < c.h {
			c.cells[y][x] = st.Render(string(r))
		}
		x++
	}
}

// rows renders the canvas from column left on. A fragment that starts
// before the cut is dropped and its cells become blanks.
func (c *canvas) rows(left int) []string {
	out := make([]string, c.h)
	for y, row := range c.cells {
		var b strings.Builder
		for x := left; x < len(row); x++ {
			switch row[x] {
			case covered:
				if x == left {
					b.WriteByte(' ')
				}
			case "":
				b.WriteByte(' ')
			default:
				b.WriteString(row[x])
			}
		}
		out[y] = strings.TrimRight(b.String(), " ")
	}
	return out
}

// mapNode is a laid-out node.
type mapNode struct {
	id   string // session id, tree.Orchestrator, or "you"
	kids []*mapNode
	h    int // rows the subtree takes
	x, y int
}

func (m Model) mapTree() *mapNode {
	root := &mapNode{id: "you"}
	seen := map[string]bool{}
	var build func(id string) *mapNode
	build = func(id string) *mapNode {
		n := &mapNode{id: id}
		seen[id] = true
		for _, a := range m.childrenOf(id) {
			if !seen[a.Session.SessionID] {
				n.kids = append(n.kids, build(a.Session.SessionID))
			}
		}
		return n
	}
	for _, r := range m.tree.roots {
		root.kids = append(root.kids, build(r))
	}
	for _, a := range m.agents {
		if !seen[a.Session.SessionID] {
			root.kids = append(root.kids, build(a.Session.SessionID))
		}
	}
	var size func(n *mapNode) int
	size = func(n *mapNode) int {
		n.h = mapNodeH
		if len(n.kids) == 0 {
			return n.h
		}
		sum := 0
		for i, k := range n.kids {
			if i > 0 {
				sum += mapGapY
			}
			sum += size(k)
		}
		n.h = max(mapNodeH, sum)
		return n.h
	}
	size(root)
	var place func(n *mapNode, x, y int)
	place = func(n *mapNode, x, y int) {
		n.x, n.y = x, y+(n.h-mapNodeH)/2
		cy := y
		for _, k := range n.kids {
			place(k, x+mapWidth(n.id)+mapGapX, cy)
			cy += k.h + mapGapY
		}
	}
	place(root, 0, 0)
	return root
}

// mapPane draws the fleet as a tree from you to the leaves.
func (m Model) mapPane(w, h int) string {
	s := m.st
	if len(m.agents) == 0 {
		return "\n  " + s.dim.Render("No Claude Code sessions running.")
	}
	root := m.mapTree()
	cw := 0
	var walk func(n *mapNode)
	walk = func(n *mapNode) {
		cw = max(cw, n.x+mapWidth(n.id))
		for _, k := range n.kids {
			walk(k)
		}
	}
	walk(root)
	c := newCanvas(max(cw, w), root.h+1)
	selID := ""
	if a, ok := m.selected(); ok {
		selID = a.Session.SessionID
	}
	selX, selY := 0, 0
	var draw func(n *mapNode)
	draw = func(n *mapNode) {
		m.drawMapNode(c, n, n.id == selID)
		if n.id == selID {
			selX, selY = n.x, n.y
		}
		if len(n.kids) > 0 {
			m.drawConnectors(c, n)
		}
		for _, k := range n.kids {
			draw(k)
		}
	}
	draw(root)
	// Keep the selected node in view: scroll sideways to its column when
	// the tree is deeper than the pane is wide, and down to its row.
	left := 0
	if selX+mapNodeW > w {
		left = selX + mapNodeW - w
	}
	rows := c.rows(left)
	top := m.tree.mapTop
	if selY < top {
		top = selY
	}
	if selY+mapNodeH > top+h {
		top = selY + mapNodeH - h
	}
	top = clamp(top, 0, max(0, len(rows)-h))
	rows = rows[top:min(len(rows), top+h)]
	for i := range rows {
		rows[i] = ansi.Truncate(rows[i], w, "…")
	}
	return strings.Join(rows, "\n")
}

// drawMapNode draws one node: avatar, name and role, task, age and
// context. The selected node carries the marker and the bright name.
func (m Model) drawMapNode(c *canvas, n *mapNode, selected bool) {
	s := m.st
	x, y := n.x, n.y
	textX := x + 8 + 2
	textW := mapNodeW - 10
	marker := " "
	if selected {
		marker = s.bright.Render("▌")
	}
	switch n.id {
	case "you":
		c.text(x, y+mapCentre, "you ────", s.fg.Bold(true))
		c.text(x+3, y+mapCentre, " ────", s.muted)
		c.text(x, y+mapCentre+1, "terminal", s.dim)
		return
	case tree.Orchestrator:
		st := m.orchStyle()
		c.put(x, y+mapCentre, st.Render("● orchestrator"), 14)
		status := m.orch.status
		if status == "" {
			status = "off"
		}
		c.text(x+2, y+mapCentre+1, ansi.Truncate(status, mapOrchW-2, "…"), s.dim)
		return
	}
	a, ok := m.agentFor(n.id)
	if !ok {
		return
	}
	for i, row := range m.smallCritter(n.id, false) {
		c.put(x, y+i, row, 8)
	}
	var glyph string
	switch a.Status {
	case fleet.Working:
		glyph = s.bright.Render("●")
	case fleet.NeedsYou:
		glyph = s.attn.Render("◐")
	case fleet.Gone:
		glyph = s.err.Render("✕")
	default:
		glyph = s.dim.Render("○")
	}
	nameStyle := s.fg
	if selected {
		nameStyle = s.bright.Bold(true)
	}
	role := ""
	if a.Labels.Role != "" {
		role = "  " + lipgloss.NewStyle().Foreground(lipgloss.Color(m.agentColor(a))).Render(strings.ToLower(a.Labels.Role))
	}
	line1 := marker + glyph + " " + nameStyle.Render(a.Name) + role
	c.put(textX-1, y, ansi.Truncate(line1, textW+1, "…"), min(textW+1, ansi.StringWidth(line1)))
	task := ansi.Truncate("▸ "+a.Task(), textW, "…")
	c.put(textX, y+1, s.dim.Render(task), ansi.StringWidth(task))
	meta := humanAge(a.Since())
	if a.HasUsage {
		meta += fmt.Sprintf(" · ctx %d%%", a.Usage.Percent())
	}
	if roll, _ := m.rollupOf(n.id); roll.needsYou > 0 {
		meta += " · " + fmt.Sprintf("%d needs you", roll.needsYou)
	}
	meta = ansi.Truncate(meta, textW, "…")
	c.put(textX, y+2, s.dim.Render(meta), ansi.StringWidth(meta))
}

// drawConnectors joins a node to its children with box drawing: a stub
// out of the parent, a spine down the children, a stub into each child.
func (m Model) drawConnectors(c *canvas, n *mapNode) {
	st := m.st.muted
	px := n.x + mapWidth(n.id) // the parent's stub
	jx := px + 1               // the spine
	py := n.y + mapCentre
	top, bottom := py, py
	at := map[int]bool{}
	for _, k := range n.kids {
		cy := k.y + mapCentre
		at[cy] = true
		top = min(top, cy)
		bottom = max(bottom, cy)
		c.text(jx+1, cy, "─", st)
	}
	c.text(px, py, "─", st)
	for y := top; y <= bottom; y++ {
		up, down := y > top, y < bottom
		left, right := y == py, at[y]
		var ch string
		switch {
		case left && right && up && down:
			ch = "┼"
		case left && right && up:
			ch = "┴"
		case left && right && down:
			ch = "┬"
		case left && right:
			ch = "─"
		case right && up && down:
			ch = "├"
		case right && down:
			ch = "┌"
		case right && up:
			ch = "└"
		case left && up && down:
			ch = "┤"
		case left && down:
			ch = "┐"
		case left && up:
			ch = "┘"
		default:
			ch = "│"
		}
		c.text(jx, y, ch, st)
	}
}
