package review

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Message is the text one agent receives for its comments: each comment
// cites its file and lines and quotes them, so the agent finds the place
// even after the lines have moved. Den's peer header goes above it.
func Message(comments []Comment) string {
	cs := sortedComments(comments)
	var b strings.Builder
	fmt.Fprintf(&b, "Review of your changes: %s.\n", count(len(cs)))
	b.WriteString("Line numbers are from when I commented; the quoted text is what I mean.\n")
	writeComments(&b, cs)
	b.WriteString("\nFix these, then say which comment each change addresses.")
	return b.String()
}

// OrchestratorMessage is the text the orchestrator receives for comments
// whose agent can no longer be reached, so it can pass them to a live
// agent. name is the agent they were written for.
func OrchestratorMessage(name string, comments []Comment) string {
	cs := sortedComments(comments)
	var b strings.Builder
	who := "for " + name + ", whose session has ended"
	if name == "" {
		who = "on lines no agent wrote"
	}
	fmt.Fprintf(&b, "I wrote %s %s. Pass them to a live agent that can act on them, or tell me none can.\n", count(len(cs)), who)
	b.WriteString("Line numbers are from when I commented; the quoted text is what I mean.\n")
	writeComments(&b, cs)
	return strings.TrimRight(b.String(), "\n")
}

func count(n int) string {
	if n == 1 {
		return "1 comment"
	}
	return strconv.Itoa(n) + " comments"
}

func sortedComments(comments []Comment) []Comment {
	cs := append([]Comment(nil), comments...)
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Root != cs[j].Root {
			return cs[i].Root < cs[j].Root
		}
		if cs[i].Path != cs[j].Path {
			return cs[i].Path < cs[j].Path
		}
		return cs[i].Start < cs[j].Start
	})
	return cs
}

// writeComments writes the comments under one heading per repository.
func writeComments(b *strings.Builder, cs []Comment) {
	root := "\x00"
	for _, c := range cs {
		if c.Root != root {
			root = c.Root
			fmt.Fprintf(b, "\nIn %s:\n", root)
		}
		b.WriteString("\n" + Where(c) + "\n")
		for _, q := range c.Quote {
			kind, text := splitQuote(q)
			if kind == '-' {
				b.WriteString("  > (removed) " + text + "\n")
			} else {
				b.WriteString("  > " + text + "\n")
			}
		}
		for _, l := range strings.Split(strings.TrimSpace(c.Body), "\n") {
			b.WriteString("  " + l + "\n")
		}
	}
}

// Where cites a comment's file and lines: "cmd/list.go:49-51", with a
// note when the lines are numbered in the old file.
func Where(c Comment) string {
	s := c.Path + ":" + strconv.Itoa(c.Start)
	if c.End != c.Start {
		s += "-" + strconv.Itoa(c.End)
	}
	if c.Side == "old" {
		s += " (removed lines, numbered as before the change)"
	}
	return s
}

// splitQuote separates a quoted line's +, - or space marker from its text.
func splitQuote(q string) (byte, string) {
	if q != "" && (q[0] == '+' || q[0] == '-' || q[0] == ' ') {
		return q[0], q[1:]
	}
	return ' ', q
}

// Changed reports whether the lines a comment quotes have changed in the
// file's current content (nil when the file is gone). The lines still in
// the file are counted as one block anywhere in it, so edits that only move
// them do not count: the comment changed when there are fewer copies than
// when it was sent (Copies). When it quotes only removed lines, it changed
// once there are more copies of them: they are back.
func Changed(c Comment, content []byte) bool {
	block, removed := anchor(c)
	if len(block) == 0 {
		return false // nothing to compare with
	}
	n := occurrences(content, block)
	if removed {
		return n > c.Copies
	}
	return n < max(c.Copies, 1)
}

// FileChanged is Changed against the file on disk.
func FileChanged(c Comment) bool {
	if c.Host != "" {
		return false // the file is on another machine
	}
	raw, err := os.ReadFile(filepath.Join(c.Root, c.Path))
	if err != nil {
		if os.IsNotExist(err) {
			return Changed(c, nil)
		}
		return false // unreadable is not a change
	}
	return Changed(c, raw)
}

// FileCopies counts the copies of a comment's quoted lines in the file on
// disk now, for Copies when it is sent.
func FileCopies(c Comment) int {
	if c.Host != "" {
		return 0
	}
	block, _ := anchor(c)
	raw, err := os.ReadFile(filepath.Join(c.Root, c.Path))
	if err != nil || len(block) == 0 {
		return 0
	}
	return occurrences(raw, block)
}

// anchor is what Changed looks for: the quoted lines that are in the new
// file, or the removed ones when there are no others.
func anchor(c Comment) (block []string, removedOnly bool) {
	var kept, removed []string
	for _, q := range c.Quote {
		kind, text := splitQuote(q)
		if kind == '-' {
			removed = append(removed, text)
		} else {
			kept = append(kept, text)
		}
	}
	if len(kept) > 0 {
		return kept, false
	}
	return removed, len(removed) > 0
}

func occurrences(content []byte, block []string) int {
	if content == nil {
		return 0
	}
	lines := strings.Split(string(bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))), "\n")
	n := 0
	for i := 0; i+len(block) <= len(lines); i++ {
		if blockAt(lines, i, block) {
			n++
		}
	}
	return n
}

func blockAt(lines []string, i int, block []string) bool {
	for j, l := range block {
		if lines[i+j] != l {
			return false
		}
	}
	return true
}
