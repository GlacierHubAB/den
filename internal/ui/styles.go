package ui

import (
	"charm.land/lipgloss/v2"

	"github.com/GlacierHubAB/den/internal/theme"
)

// styles derives every lipgloss style from the theme palette so the UI is a
// pure function of the tokens.
type styles struct {
	pal                                            theme.Palette
	fg, dim, muted, bright, ok, attn, err          lipgloss.Style
	brand, roleChip, sel, section                  lipgloss.Style
	border, borderFocus, paneTitle, paneTitleFocus lipgloss.Style
	tabOn, tabOff, badgeN                          lipgloss.Style
	key, keyLabel                                  lipgloss.Style
}

func newStyles(p theme.Palette) styles {
	c := lipgloss.Color
	s := styles{pal: p}
	s.fg = lipgloss.NewStyle().Foreground(c(p.Fg))
	s.dim = lipgloss.NewStyle().Foreground(c(p.FgDim))
	s.muted = lipgloss.NewStyle().Foreground(c(p.Muted))
	s.bright = lipgloss.NewStyle().Foreground(c(p.Bright))
	s.ok = lipgloss.NewStyle().Foreground(c(p.Ok))
	s.attn = lipgloss.NewStyle().Foreground(c(p.Attn))
	s.err = lipgloss.NewStyle().Foreground(c(p.Err))
	s.brand = lipgloss.NewStyle().Foreground(c(p.Bg)).Background(c(p.Bright)).Bold(true).Padding(0, 1)
	s.roleChip = lipgloss.NewStyle().Foreground(c(p.Bg)).Background(c(p.Bright)).Bold(true).Padding(0, 1)
	s.sel = lipgloss.NewStyle().Background(c(p.Sel))
	s.section = lipgloss.NewStyle().Foreground(c(p.Muted))
	s.border = lipgloss.NewStyle().Foreground(c(p.Surface2()))
	s.borderFocus = lipgloss.NewStyle().Foreground(c(p.Sel))
	s.paneTitle = lipgloss.NewStyle().Foreground(c(p.FgDim))
	s.paneTitleFocus = lipgloss.NewStyle().Foreground(c(p.Fg))
	s.tabOn = lipgloss.NewStyle().Foreground(c(p.Bright)).Underline(true)
	s.tabOff = lipgloss.NewStyle().Foreground(c(p.FgDim))
	s.badgeN = lipgloss.NewStyle().Foreground(c(p.Bg)).Background(c(p.Attn)).Bold(true).Padding(0, 1)
	s.key = lipgloss.NewStyle().Foreground(c(p.Fg))
	s.keyLabel = lipgloss.NewStyle().Foreground(c(p.FgDim))
	return s
}
