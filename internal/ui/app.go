// Package ui is Den's bubbletea application.
package ui

import (
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fsnotify/fsnotify"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/theme"
)

type pane int

const (
	paneAgents pane = iota
	paneInbox
	paneDetail
)

type tickMsg time.Time
type changedMsg struct{}
type snapshotMsg struct {
	agents []fleet.Agent
	err    error
}
type focusedMsg struct {
	note string
	err  error
}
type flashClearMsg struct{}

// Model is the root bubbletea model.
type Model struct {
	st       styles
	agents   []fleet.Agent
	cursor   int
	focus    pane
	width    int
	height   int
	err      error
	flash    string
	flashErr bool
	help     bool
	watch    <-chan struct{}
	lastScan time.Time
}

// New returns the root model.
func New() Model {
	return Model{st: newStyles(theme.Load()), watch: watchDirs(discover.Dir(), badge.StoreDir(), theme.ColorsFile())}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(snapshot, tick(), waitChange(m.watch))
}

func tick() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func snapshot() tea.Msg {
	agents, err := fleet.Snapshot()
	return snapshotMsg{agents: agents, err: err}
}

func waitChange(ch <-chan struct{}) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		<-ch
		return changedMsg{}
	}
}

// watchDirs emits one signal per burst of filesystem changes in the given
// paths (directories are watched as a whole). Returns nil when watching is
// unavailable; the periodic tick still refreshes then.
func watchDirs(paths ...string) <-chan struct{} {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil
	}
	added := 0
	for _, p := range paths {
		dir := p
		if filepath.Ext(p) != "" {
			dir = filepath.Dir(p)
		}
		if w.Add(dir) == nil {
			added++
		}
	}
	if added == 0 {
		w.Close()
		return nil
	}
	out := make(chan struct{}, 1)
	go func() {
		var timer *time.Timer
		for {
			select {
			case _, ok := <-w.Events:
				if !ok {
					return
				}
				if timer != nil {
					timer.Stop()
				}
				timer = time.AfterFunc(250*time.Millisecond, func() {
					select {
					case out <- struct{}{}:
					default:
					}
				})
			case <-w.Errors:
			}
		}
	}()
	return out
}

func (m Model) selected() (fleet.Agent, bool) {
	if m.cursor < 0 || m.cursor >= len(m.agents) {
		return fleet.Agent{}, false
	}
	return m.agents[m.cursor], true
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		return m, tea.Batch(snapshot, tick())
	case changedMsg:
		// Theme may have changed too; reload tokens with the data.
		m.st = newStyles(theme.Load())
		return m, tea.Batch(snapshot, waitChange(m.watch))
	case snapshotMsg:
		m.err = msg.err
		if msg.err == nil {
			// Keep the cursor on the same agent across re-sorts.
			var keep string
			if a, ok := m.selected(); ok {
				keep = a.Session.SessionID
			}
			m.agents = msg.agents
			m.lastScan = time.Now()
			m.cursor = 0
			for i, a := range m.agents {
				if a.Session.SessionID == keep {
					m.cursor = i
				}
			}
		}
	case focusedMsg:
		if msg.err != nil {
			m.flash, m.flashErr = msg.err.Error(), true
		} else {
			m.flash, m.flashErr = "Focused "+msg.note, false
		}
		return m, tea.Tick(3*time.Second, func(time.Time) tea.Msg { return flashClearMsg{} })
	case flashClearMsg:
		m.flash = ""
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.help {
		m.help = false
		return m, nil
	}
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.help = true
	case "j", "down":
		if m.cursor < len(m.agents)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = max(0, len(m.agents)-1)
	case "tab":
		m.focus = (m.focus + 1) % 3
	case "shift+tab":
		m.focus = (m.focus + 2) % 3
	case "r":
		return m, snapshot
	case "enter":
		if a, ok := m.selected(); ok {
			return m, func() tea.Msg {
				note, err := fleet.Focus(a)
				return focusedMsg{note: note, err: err}
			}
		}
	}
	return m, nil
}
