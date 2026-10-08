package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/critter"
	"github.com/GlacierHubAB/den/internal/fleet"
)

// animTickMs matches agent-badge's frame rate.
const animTickMs = 160

type animTickMsg time.Time

func animTick() tea.Cmd {
	return tea.Tick(animTickMs*time.Millisecond, func(t time.Time) tea.Msg { return animTickMsg(t) })
}

// critterRows renders the agent's creature as 8 lines of half-blocks.
func (m Model) critterRows(a fleet.Agent) []string {
	seed := critter.Hash(a.Session.SessionID)
	colors := critter.Colors{Bodies: []string{m.agentColor(a)}, Dark: m.st.pal.Bg, Blush: m.st.pal.Blush}
	pose := critter.PoseAt(seed, m.anim, a.Status == fleet.Working)
	px := critter.Pixels(seed, colors, pose)
	rows := critter.Rows(px)
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		var b strings.Builder
		for _, c := range row {
			switch {
			case c.Char == " ":
				b.WriteString(" ")
			case c.Top != "" && c.Bottom != "":
				b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(c.Top)).Background(lipgloss.Color(c.Bottom)).Render("▀"))
			case c.Top != "":
				b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(c.Top)).Render("▀"))
			default:
				b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(c.Bottom)).Render("▄"))
			}
		}
		out = append(out, b.String())
	}
	return out
}

// gauge draws a labelled bar the way the badge does, with the warning and
// alert colours at 75% and 90%.
func (m Model) gauge(label string, percent int, detail string, w int) []string {
	s := m.st
	barW := clamp(w-4, 6, 16)
	full := percent * barW / 100
	if full > barW {
		full = barW
	}
	fill := s.bright
	if percent >= 90 {
		fill = s.err
	} else if percent >= 75 {
		fill = s.attn
	}
	bar := fill.Render(strings.Repeat("█", full)) + s.muted.Render(strings.Repeat("░", barW-full))
	return []string{
		s.dim.Render(label),
		bar + " " + s.fg.Render(fmt.Sprintf("%d%%", percent)),
		s.dim.Render(detail),
	}
}

// limitGauges renders the badge's 5-hour and weekly limits and spend, if known.
func (m Model) limitGauges(u badge.Usage, w int) [][]string {
	var out [][]string
	if u.FiveHour != nil {
		out = append(out, m.gauge("5-hour limit", int(*u.FiveHour), untilText(u.FiveHourResets), w))
	}
	if u.SevenDay != nil {
		out = append(out, m.gauge("Weekly limit", int(*u.SevenDay), untilText(u.SevenDayResets), w))
	}
	return out
}

func untilText(iso string) string {
	if iso == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return ""
	}
	d := time.Until(t)
	switch {
	case d <= 0:
		return "resets now"
	case d < time.Hour:
		return fmt.Sprintf("resets in %dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("resets in %dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("resets in %dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}
