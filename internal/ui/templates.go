package ui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/spawn"
	"github.com/GlacierHubAB/den/internal/templates"
)

// Templates are saved mission shapes and teams. /template lists them,
// /template <name> <target> asks the orchestrator for a proposal seeded from
// one (the usual card; nothing starts before ^Y), and /template save <name>
// keeps the open mission's shape and team.

// templateDir is added to a proposal request seeded from a template.
const templateDir = `The human started this from a template: a shape, constraints, a done-when and a team of roles with models. Keep the template's shape, constraints and team (each role with its model and "under") for the target the human named, set the done criteria from its done-when and give each sub-goal its owner, and change something only when the target calls for it; say in one line what you changed. Start nothing.`

// templateList loads the templates; err is a problem with the user's file.
func templateList() ([]templates.Template, error) { return templates.Load(templates.Path()) }

// templateInfos is what the orchestrator hears about the templates.
func (m Model) templateInfos() []orch.TemplateInfo {
	list, _ := templateList()
	out := make([]orch.TemplateInfo, 0, len(list))
	for _, t := range list {
		out = append(out, orch.TemplateInfo{Name: t.Name, About: t.About})
	}
	return out
}

// templateCommand runs /template.
func (m Model) templateCommand(args []string, text string) (tea.Model, tea.Cmd) {
	if len(args) == 0 {
		m.tab = tabOverview
		m.ov.listing, m.ov.open, m.ov.scroll = true, "", 0
		return m, nil
	}
	if args[0] == "save" {
		if len(args) != 2 {
			return m.say("Name it: /template save <name>", true)
		}
		return m.saveTemplate(strings.ToLower(args[1]))
	}
	list, err := templateList()
	t, ok := templates.Find(list, args[0])
	if !ok {
		names := make([]string, len(list))
		for i, x := range list {
			names[i] = x.Name
		}
		note := fmt.Sprintf("No template called %s. Have: %s.", args[0], strings.Join(names, ", "))
		if err != nil {
			note += " " + err.Error()
		}
		return m.say(note, true)
	}
	target := strings.Join(args[1:], " ")
	if target == "" && strings.Contains(t.Mission, templates.Target) {
		return m.say("What is it for? /template "+t.Name+" <target or goal>", true)
	}
	// A leading folder picks that project, so "review ~/code/x" lands there.
	if dir, _, _ := strings.Cut(target, " "); strings.HasPrefix(dir, "~") || strings.HasPrefix(dir, "/") {
		if _, err := spawn.ExpandDir(dir); err == nil {
			if _, err := m.pinProject(dir); err != nil {
				return m.say(err.Error(), true)
			}
		}
	}
	if _, ok := m.curProject(); !ok {
		return m.say("Pick a project first: /project <name> or /project add <dir>.", true)
	}
	m.tab = tabOverview
	// Separate statement: the call changes m, and a return list does not
	// promise that happens before m is copied.
	cmd := m.proposeFromTemplate(t, target)
	return m, cmd
}

// proposeFromTemplate puts the template's proposal on the card at once and
// asks the orchestrator to adapt it to the target. Nothing runs before ^Y.
func (m *Model) proposeFromTemplate(t templates.Template, target string) tea.Cmd {
	p, _ := m.curProject()
	now := time.Now()
	words := strings.TrimSpace("template " + t.Name + " " + target)
	ms := &mission.Mission{ID: mission.NewID(now), Project: p.Root, ProjectName: mission.ProjectLabel(p.Root), Text: words, State: mission.Proposed, Template: t.Name, Created: now, Updated: now,
		Name: templateMissionName(t.Name, target), Mission: t.Fill(target), DoneWhen: t.DoneWhen, Constraints: t.Constraints,
		Shape: t.Shape, Cadence: t.Cadence, Team: t.Team, Reason: "from template " + t.Name,
	}
	if ms.Shape == "" {
		ms.Shape = mission.Once
	}
	// The template's done-when is the first criterion and each role's part
	// a sub-goal; the orchestrator adapts both to the target.
	if dw := strings.ToLower(strings.TrimSpace(t.DoneWhen)); dw != "" && dw != "standing" && !strings.HasPrefix(dw, "never") {
		ms.SetCriteria([]string{t.DoneWhen})
	}
	for _, r := range t.Team {
		ms.AddGoal(mission.SubGoal{Text: r.Role + "'s part", Role: r.Role, State: mission.GoalTodo})
	}
	m.missions.Save(ms)
	m.ov.listing = false
	m.ov.proposal = ms.ID
	prompt := fmt.Sprintf("Den made proposal %s for project %s (repository %s, branch %s) from a template.\n%s\n%s\nAdapt it to the target with propose_mission and id %s: the goal, the done criteria and the sub-goals with their owners. The human's words: %s",
		ms.ID, p.Name, p.Root, p.Branch, templateDir, t.Describe(target), ms.ID, words)
	return m.sendMission(ms.ID, words, prompt)
}

// templateMissionName is the row's name: the template and what it is for.
func templateMissionName(name, target string) string {
	words := strings.Fields(target)
	if len(words) == 0 {
		return name
	}
	first := words[0]
	if strings.HasPrefix(first, "~") || strings.HasPrefix(first, "/") {
		first = filepath.Base(first)
	} else if len(words) > 3 {
		first = strings.Join(words[:3], " ")
	} else {
		first = strings.Join(words, " ")
	}
	return name + " · " + first
}

// saveTemplate keeps the shape and team of the pending proposal or the
// open or selected mission under a name.
func (m Model) saveTemplate(name string) (tea.Model, tea.Cmd) {
	if !templates.ValidName(name) {
		return m.say("Use lower-case letters, digits and hyphens for the name.", true)
	}
	ms := m.missions.Get(m.ov.proposal)
	if ms == nil {
		ms = m.ovMission()
	}
	if ms == nil || ms.Mission == "" {
		return m.say("Open or select a mission first; its shape and team are what gets saved.", true)
	}
	list, _ := templateList()
	if old, ok := templates.Find(list, name); ok && old.Builtin {
		return m.say(name+" is built in; pick another name.", true)
	}
	if err := templates.Save(templates.Path(), templates.FromMission(name, ms)); err != nil {
		return m.say("Not saved: "+err.Error(), true)
	}
	return m.say("Saved template "+name+" · /template "+name+" <target> starts one", false)
}

// templateLines is the list the Overview shows for /template.
func (m Model) templateLines(w int) []string {
	s := m.st
	list, err := templateList()
	out := []string{""}
	for _, t := range list {
		src := "yours"
		if t.Builtin {
			src = "built in"
		}
		out = append(out, ansi.Truncate("  "+s.bright.Render(t.Name)+"  "+s.dim.Render(src), w, "…"))
		if t.About != "" {
			for _, l := range strings.Split(ansi.Wrap(t.About, max(10, w-6), ""), "\n") {
				out = append(out, "    "+s.fg.Render(l))
			}
		}
		var roles []string
		for _, r := range t.Team {
			roles = append(roles, r.Role+" "+r.Model)
		}
		line := t.Shape.Glyph() + " " + t.Shape.Label()
		if len(roles) > 0 {
			line += " · " + strings.Join(roles, ", ")
		}
		out = append(out, "    "+s.dim.Render(ansi.Truncate(line, max(10, w-6), "…")))
		out = append(out, "")
	}
	if err != nil {
		out = append(out, "  "+s.err.Render(ansi.Truncate(err.Error(), max(10, w-4), "…")), "")
	}
	out = append(out, "  "+s.key.Render("/template <name> <target or goal>")+s.keyLabel.Render("  propose a mission from one"))
	out = append(out, "  "+s.key.Render("/template save <name>")+s.keyLabel.Render("  keep the open mission's shape and team"))
	out = append(out, "  "+s.dim.Render("yours live in "+shortPath(templates.Path(), ""))+"  "+s.key.Render("Esc")+s.keyLabel.Render(" back"))
	return out
}
