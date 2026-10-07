// Package ui is Den's bubbletea application. Milestone 0: a live list of the
// Claude Code sessions on this machine with badge identity and status.
package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/theme"
)

// Status is Den's own state vocabulary. "needs you" only ever comes from an
// explicit signal (hook bridge, milestone 2); nothing is guessed as idle.
type Status int

const (
	Unknown Status = iota
	Working
	NeedsYou
	Idle
	Gone
)

// Agent is a session joined with its badge identity.
type Agent struct {
	Session discover.Session
	Name    string
	Labels  badge.Labels
	Status  Status
}

type tickMsg time.Time
type scanMsg struct {
	agents []Agent
	err    error
}

// Model is the root bubbletea model.
type Model struct {
	pal     theme.Palette
	agents  []Agent
	cursor  int
	width   int
	height  int
	err     error
	lastRun time.Time
}

// New returns the root model.
func New() Model { return Model{pal: theme.Load()} }

func (m Model) Init() tea.Cmd { return tea.Batch(scan, tick()) }

func tick() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func scan() tea.Msg {
	sessions, err := discover.Scan()
	if err != nil {
		return scanMsg{err: err}
	}
	labels, _ := badge.Load()
	agents := make([]Agent, 0, len(sessions))
	for _, s := range sessions {
		a := Agent{Session: s, Name: badge.Name(s.SessionID), Labels: labels[s.SessionID]}
		switch {
		case !s.Alive:
			a.Status = Gone
		case s.Status == "busy":
			a.Status = Working
		case s.Status == "idle":
			a.Status = Idle
		default:
			a.Status = Unknown
		}
		agents = append(agents, a)
	}
	return scanMsg{agents: agents}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		return m, tea.Batch(scan, tick())
	case scanMsg:
		m.err = msg.err
		if msg.err == nil {
			m.agents = msg.agents
			m.lastRun = time.Now()
			if m.cursor >= len(m.agents) {
				m.cursor = max(0, len(m.agents)-1)
			}
		}
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			if m.cursor < len(m.agents)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "r":
			return m, scan
		}
	}
	return m, nil
}

func (m Model) View() tea.View {
	p := m.pal
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color(p.FgDim))
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color(p.Muted))
	fg := lipgloss.NewStyle().Foreground(lipgloss.Color(p.Fg))
	bright := lipgloss.NewStyle().Foreground(lipgloss.Color(p.Bright))
	brand := lipgloss.NewStyle().Foreground(lipgloss.Color(p.Bg)).Background(lipgloss.Color(p.Bright)).Bold(true).Padding(0, 1)
	sel := lipgloss.NewStyle().Background(lipgloss.Color(p.Sel))

	var b strings.Builder
	counts := map[Status]int{}
	for _, a := range m.agents {
		counts[a.Status]++
	}
	summary := fmt.Sprintf("%d agents · %d working · %d idle", len(m.agents), counts[Working], counts[Idle])
	if counts[NeedsYou] > 0 {
		summary = fmt.Sprintf("%d agents · %s · %d working · %d idle", len(m.agents),
			lipgloss.NewStyle().Foreground(lipgloss.Color(p.Attn)).Render(fmt.Sprintf("%d need you", counts[NeedsYou])), counts[Working], counts[Idle])
	}
	b.WriteString(brand.Render("den") + "  " + dim.Render(summary) + "\n\n")

	if m.err != nil {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(p.Err)).Render("scan failed: "+m.err.Error()) + "\n")
	}
	if len(m.agents) == 0 && m.err == nil {
		b.WriteString(dim.Render("No Claude Code sessions found in "+discover.Dir()) + "\n")
	}

	width := m.width
	if width <= 0 {
		width = 100
	}
	for i, a := range m.agents {
		glyph, gstyle := m.glyph(a.Status)
		role := ""
		if a.Labels.Role != "" {
			role = "  " + muted.Render(strings.ToUpper(a.Labels.Role))
		}
		task := a.Labels.Task
		if task == "" {
			task = a.Session.Name
		}
		age := humanAge(time.Since(a.Session.StatusChanged()))
		line1 := fmt.Sprintf(" %s %s%s", gstyle.Render(glyph), bright.Render(a.Name), role)
		line2 := "   " + fg.Render("▸ "+task)
		line3 := "   " + dim.Render(a.Session.ShortCWD()+"  "+statusWord(a.Status)+" "+age)
		rows := []string{line1, line2, line3}
		for j, r := range rows {
			if i == m.cursor {
				r = sel.Width(width).Render(r)
			}
			rows[j] = r
		}
		b.WriteString(strings.Join(rows, "\n") + "\n")
		if i < len(m.agents)-1 {
			b.WriteString("\n")
		}
	}

	b.WriteString("\n" + dim.Render("j k move · r rescan · q quit"))
	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

func (m Model) glyph(s Status) (string, lipgloss.Style) {
	p := m.pal
	switch s {
	case Working:
		return "●", lipgloss.NewStyle().Foreground(lipgloss.Color(p.Bright))
	case NeedsYou:
		return "◐", lipgloss.NewStyle().Foreground(lipgloss.Color(p.Attn))
	case Idle:
		return "○", lipgloss.NewStyle().Foreground(lipgloss.Color(p.FgDim))
	case Gone:
		return "✕", lipgloss.NewStyle().Foreground(lipgloss.Color(p.Err))
	default:
		return "?", lipgloss.NewStyle().Foreground(lipgloss.Color(p.Muted))
	}
}

func statusWord(s Status) string {
	switch s {
	case Working:
		return "working"
	case NeedsYou:
		return "needs you"
	case Idle:
		return "idle"
	case Gone:
		return "gone"
	}
	return "unknown"
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
