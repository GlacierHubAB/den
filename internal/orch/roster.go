package orch

import (
	"fmt"
	"strings"
)

// RosterAgent is what Den tells the orchestrator about one agent.
type RosterAgent struct {
	Critter  string // the human's name for it
	Peer     string // Claude Code's session name, for SendMessage
	Role     string
	Status   string
	Task     string
	Earlier  []string // earlier tasks this session worked on, newest first
	Where    string
	Title    string   // the terminal title Claude Code gave the session
	Files    []string // folders of files the agent changed recently
	Recent   []string // its latest words and tool call, newest first
	Waiting  []string // inbox items waiting on the human, one line each
	IsSelf   bool     // the orchestrator itself
	Terminal bool     // has a terminal window
}

// Compose builds the user turn: roster block, then the human's text.
func Compose(agents []RosterAgent, text string) string {
	var b strings.Builder
	b.WriteString("<den_fleet>\n")
	n := 0
	for _, a := range agents {
		if a.IsSelf {
			continue
		}
		n++
		fmt.Fprintf(&b, "- %s → peer %q", a.Critter, a.Peer)
		if a.Role != "" {
			b.WriteString(" · " + a.Role)
		}
		b.WriteString(" · " + a.Status)
		if a.Task != "" {
			b.WriteString(" · task: " + a.Task)
		}
		if a.Where != "" {
			b.WriteString(" · in " + a.Where)
		}
		b.WriteString("\n")
		if a.Title != "" {
			b.WriteString("    title: " + a.Title + "\n")
		}
		if len(a.Files) > 0 {
			b.WriteString("    recently changed files in: " + strings.Join(a.Files, ", ") + "\n")
		}
		if len(a.Earlier) > 0 {
			b.WriteString("    earlier: " + strings.Join(a.Earlier, "; ") + "\n")
		}
		for _, r := range a.Recent {
			b.WriteString("    recent: " + r + "\n")
		}
		for _, w := range a.Waiting {
			b.WriteString("    waiting on the human: " + w + "\n")
		}
	}
	if n == 0 {
		b.WriteString("(no other agents running)\n")
	}
	b.WriteString("</den_fleet>\n\n")
	b.WriteString(strings.TrimSpace(text))
	return b.String()
}
