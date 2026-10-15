package ui

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/procs"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// The console (internal/api's console.go) in Den: the code on Den's
// screen, the card the human answers, the header's line, and den pair and
// den unpair over den.sock.

// consoleUI is the console's part of the model.
type consoleUI struct {
	code      []string               // the console code on Den's screen, nil for none
	codeUntil time.Time              // when it stops being live
	cards     map[*inbox.Item]string // open console cards → claim id
	powers    map[*inbox.Item]string // open powers cards → the console's pairing
	keyErr    string                 // why there is no console key, "" when there is
}

// consoleKeyFile is Den's console key in its state folder.
const consoleKeyFile = "local.key"

// loadConsoleKey reads Den's Ed25519 key for console proofs from state,
// making one the first time: 32 random bytes, the seed, in a 0600 file. A
// key that is not a plain file of the user only, or not 32 bytes, is
// refused rather than trusted.
func loadConsoleKey(state string) (ed25519.PrivateKey, error) {
	if fi, err := os.Stat(state); err == nil && fi.Mode().Perm()&0o077 != 0 {
		// Den's own state folder: no one else needs into it.
		os.Chmod(state, 0o700)
	}
	path := filepath.Join(state, consoleKeyFile)
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) // a FIFO cannot block the open
	if errors.Is(err, os.ErrNotExist) {
		seed := make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return nil, err
		}
		f, err := os.CreateTemp(state, "."+consoleKeyFile+".*")
		if err != nil {
			return nil, err
		}
		tmp := f.Name()
		defer os.Remove(tmp)
		_, werr := f.Write(seed)
		if err := errors.Join(werr, f.Chmod(0o600), f.Sync(), f.Close()); err != nil {
			return nil, err
		}
		if err := os.Link(tmp, path); err != nil {
			return nil, err // another Den made one meanwhile: try again next start
		}
		return ed25519.NewKeyFromSeed(seed), nil
	}
	if err != nil {
		// A link is refused here (ELOOP), never followed.
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	defer f.Close()
	fi, err := f.Stat() // the file opened, not whatever the name is now
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s is not a file only you can read", path)
	}
	seed := make([]byte, ed25519.SeedSize+1)
	n, _ := io.ReadFull(f, seed)
	if n != ed25519.SeedSize {
		return nil, fmt.Errorf("%s is not a console key", path)
	}
	return ed25519.NewKeyFromSeed(seed[:n]), nil
}

// consoleCommand is /api console: show a console code for two minutes.
func (m Model) consoleCommand() (tea.Model, tea.Cmd) {
	if why := m.startConsoleCode(); why != "" {
		return m.say(why, true)
	}
	return m.say("Console code shown above for two minutes. Type it only into the app's own console window.", false)
}

// startConsoleCode puts a console code on Den's screen; "" when it did, else
// why not.
func (m *Model) startConsoleCode() string {
	if m.api.srv == nil || m.api.srv.Paused() {
		return "The API is off: /api on turns it on first."
	}
	if m.con.keyErr != "" {
		return "Console pairing is off: " + m.con.keyErr
	}
	m.con.code = m.api.srv.StartConsoleCode()
	m.con.codeUntil = time.Now().Add(2 * time.Minute)
	return ""
}

// consoleCodeLine is the console code line Den shows while one is live; ""
// for none. It is never logged.
func (m Model) consoleCodeLine() string {
	if m.con.code == nil || m.api.srv == nil {
		return ""
	}
	left := time.Until(m.con.codeUntil).Round(time.Second)
	if _, live := m.api.srv.ConsoleCodeLive(); !live || left <= 0 {
		return ""
	}
	return "Console code (" + left.String() + "): " + strings.Join(m.con.code, " ") + " · Type this only into the app's own console window."
}

// isConsoleAct reports whether act is the console's.
func isConsoleAct(act api.Act) bool {
	return act.Console != nil || act.ConsoleDone != "" || act.ConsoleChanged || act.ConsoleCodeGone || act.ConsoleIntruder != nil || act.ConsolePowers != nil
}

// procWhen is "started HH:MM:SS", or that Den does not know.
func procWhen(t time.Time) string {
	if t.IsZero() {
		return "start unknown"
	}
	return "started " + t.Local().Format("15:04:05")
}

// applyConsoleAct handles the console's acts from den-api.sock.
func (m *Model) applyConsoleAct(act api.Act) tea.Cmd {
	if m.con.cards == nil {
		m.con.cards = map[*inbox.Item]string{}
	}
	if m.con.powers == nil {
		m.con.powers = map[*inbox.Item]string{}
	}
	if pw := act.ConsolePowers; pw != nil {
		// One at a time: a newer one replaces an older.
		for it := range m.con.powers {
			if it.State != inbox.Resolved {
				m.store.Settle(it, false, "replaced")
			}
			delete(m.con.powers, it)
		}
		it := m.store.AddConsole("Let your console answer cards and run commands again?", []string{
			"Your console (pid " + strconv.Itoa(pw.PID) + ", " + procWhen(pw.Started) + ") is paired, but has no powers: " + sanitize.Label(pw.Why, 120) + ".",
			"Until you press Ctrl+Y here, it can read, chat and show status, but not answer Den's cards or run Den's commands.",
			"Only Den's own window answers this card. /api unpair or den unpair all ends the console instead.",
		})
		m.con.powers[it] = pw.ID
		m.refresh()
	}
	if act.ConsoleCodeGone {
		m.con.code = nil
	}
	if p := act.Console; p != nil {
		parent := p.Parent
		if parent == "" {
			parent = "unknown"
		}
		title := "Make " + appName(p.Exe) + " (pid " + strconv.Itoa(p.PID) + ", " + procWhen(p.Started) + ") your console?"
		body := []string{
			"The program below claimed the console code you showed. Make it your console only if you just typed the code into the app's own console window, and the app shows this same pid and start.",
			"program: " + sanitize.Label(api.ExeShown(p.Exe), 160),
			"pid " + strconv.Itoa(p.PID) + " · " + procWhen(p.Started) + " · parent " + sanitize.Label(parent, 40),
			"command line, as the program reports it (it can be spoofed): " + sanitize.Label(p.Cmdline, 200),
		}
		if p.Codeless {
			unit := sanitize.Label(p.Unit, 60)
			title = "Pair " + unit + " (pid " + strconv.Itoa(p.PID) + ", " + procWhen(p.Started) + ") as your console?"
			sha := p.ProgramSHA
			if len(sha) > 16 {
				sha = sha[:16]
			}
			body = []string{
				unit + " asked to be your console, with no code. Pair it only if the dock shows this same pid, start time and both keys' fingerprints.",
				"pid " + strconv.Itoa(p.PID) + " · " + procWhen(p.Started) + " · parent " + sanitize.Label(parent, 40),
				"its key:   " + sanitize.Label(p.AppFP, 19),
				"Den's key: " + sanitize.Label(p.DenFP, 19),
				"program, as your systemd user manager runs it: " + sanitize.Label(p.Program, 160) + " (sha256 " + sanitize.Label(sha, 16) + "…)",
				"Den checked the unit's program, settings and environment through your systemd user manager. Any program running as you could stand in for that manager, so this is a check, not proof.",
			}
			if p.Reconnect {
				title = "Re-pair " + unit + " (pid " + strconv.Itoa(p.PID) + ", " + procWhen(p.Started) + ") as your console?"
				body[0] = unit + " restarted and asks to be your console again. Den asks you because:"
				var changes []string
				for _, c := range p.Changes {
					changes = append(changes, "  - "+sanitize.Label(c, 200))
				}
				body = append(body[:1], append(changes, body[1:]...)...)
				body = append(body, "Ctrl+Y re-pairs it and lets it answer cards and run commands. Pair it only if the dock shows this same pid, start time and both fingerprints.")
			}
		}
		if r := p.Replaces; r != nil {
			body = append(body, "This replaces your current console ("+appName(r.Exe)+" pid "+strconv.Itoa(r.PID)+").")
		}
		body = append(body, "As your console it may answer Den's cards and send Den's commands for you. /api unpair or den unpair all ends it.")
		it := m.store.AddConsole(title, body)
		m.con.cards[it] = p.ID
		m.refresh()
	}
	if id := act.ConsoleDone; id != "" {
		for it, cid := range m.con.cards {
			if cid == id && it.State != inbox.Resolved {
				m.store.Settle(it, false, "no longer waiting")
				delete(m.con.cards, it)
			}
		}
		m.refresh()
	}
	if x := act.ConsoleIntruder; x != nil {
		m.store.AddConsoleNote("Another program tried to use your console", []string{
			appName(x.Exe) + " (pid " + strconv.Itoa(x.PID) + ") tried to prove itself as your console and was refused.",
			"Three such tries in ten minutes unpair the console. If it was not your app, unpair it: /api unpair or den unpair all.",
		})
		m.refresh()
	}
	if act.ConsoleChanged {
		m.refresh()
	}
	return nil
}

// decideConsole is the human's ^Y or ^N on a console card, after the whole
// card was shown (answer's readGate).
func (m Model) decideConsole(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	if pid, ok := m.con.powers[it]; ok && m.api.srv != nil {
		if verb == "always" {
			return m.say("This card is answered once: ^Y gives the console its powers, ^N leaves it without.", true)
		}
		delete(m.con.powers, it)
		m.api.srv.ConsolePowersAnswered(pid, verb == "allow")
		m.store.Settle(it, verb == "allow", map[bool]string{true: "powers given", false: "no powers"}[verb == "allow"])
		m.refresh()
		if verb != "allow" {
			return m.say("The console keeps reading and chat, without powers.", false)
		}
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "the console may answer cards and run commands again", at: time.Now()}))
		return m.say("The console has its powers again.", false)
	}
	id, ok := m.con.cards[it]
	if !ok || m.api.srv == nil {
		m.store.Settle(it, false, "stale; no console")
		m.refresh()
		return m.say("That console request is stale; nothing was paired.", true)
	}
	if verb == "always" {
		return m.say("A console is answered once: ^Y makes it your console, ^N refuses.", true)
	}
	delete(m.con.cards, it)
	m.api.srv.ConsoleAnswered(id, verb == "allow")
	m.store.Settle(it, verb == "allow", map[bool]string{true: "made your console", false: "refused"}[verb == "allow"])
	m.refresh()
	if verb != "allow" {
		return m.say("Not your console.", false)
	}
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "den-api.sock: a console was paired; /api unpair or den unpair all ends it", at: time.Now()}))
	return m.say("Paired as your console.", false)
}

// consoleChip is the header's console part: who it is and since when; ""
// for none.
func (m Model) consoleChip() string {
	if m.api.srv == nil {
		return ""
	}
	c, ok := m.api.srv.CurrentConsole()
	if !ok {
		return ""
	}
	who := appName(c.Exe) + " pid "
	if api.Unidentified(c.Exe) {
		// Den knows the console from the pairing; which program it is,
		// it cannot read.
		who = "program unverified, pid "
	}
	chip := consoleWord + ": " + who + strconv.Itoa(c.PID) + " since " + c.Since.Local().Format("15:04")
	if !c.Powers {
		chip += " · no powers until ^Y"
	}
	return m.st.attn.Render(chip)
}

// pairCallerRefusal is why Den refuses den pair from pid: it runs under
// Claude Code, one of Den's agents' sessions, den mcp or den hook. A
// terminal proves no human, and an agent could open one; "" lets it ask.
func (m Model) pairCallerRefusal(pid int) string {
	if pid <= 0 {
		return "Den cannot tell who asked"
	}
	sessions := map[int]bool{}
	for _, a := range m.agentProcs() {
		sessions[a.PID] = true
	}
	for _, p := range procs.Ancestors(pid) {
		if sessions[p.PID] {
			return "it runs under one of your agents' sessions"
		}
		if exe := procs.Exe(p.PID); p.Comm == "claude" || filepath.Base(exe) == "claude" || strings.Contains(exe, "/claude/versions/") {
			return "it runs under Claude Code"
		}
		if p.Comm == "den" {
			raw, _ := os.ReadFile("/proc/" + strconv.Itoa(p.PID) + "/cmdline")
			args := strings.Split(string(raw), "\x00")
			if len(args) > 1 && (args[1] == "mcp" || args[1] == "hook") {
				return "it runs under den " + args[1]
			}
		}
	}
	return ""
}

// handleConsolePair is den pair over den.sock: a console code on Den's
// screen, unless the caller is refused.
func (m *Model) handleConsolePair(r *bridge.Request) tea.Cmd {
	if why := pairCaller(*m, r.PeerPID); why != "" {
		r.Reply(bridge.Decision{Behavior: "deny", Message: "refused: " + why + ". Run den pair in a terminal of your own, or type /api console in Den."})
		return nil
	}
	if why := m.startConsoleCode(); why != "" {
		r.Reply(bridge.Decision{Behavior: "deny", Message: why})
		return nil
	}
	r.Reply(bridge.Decision{Behavior: "allow", Message: "Den shows a console code for two minutes: type it into the app's own console window, then confirm the card in Den."})
	return nil
}

// pairCaller is pairCallerRefusal; tests replace it.
var pairCaller = func(m Model, pid int) string { return m.pairCallerRefusal(pid) }

// handleConsoleUnpair is den unpair [all] over den.sock: it only lowers,
// so it is never refused for who asks.
func (m *Model) handleConsoleUnpair(r *bridge.Request) tea.Cmd {
	if m.api.srv == nil {
		r.Reply(bridge.Decision{Behavior: "allow", Message: "Nothing was paired: the API is off."})
		return nil
	}
	var p struct {
		All bool `json:"all"`
	}
	json.Unmarshal(r.Envelope.Payload, &p)
	n := m.api.srv.UnpairConsole("")
	if p.All {
		n += len(m.api.srv.Pairings())
		m.api.srv.Unpair("", "")
	}
	who := "pid " + strconv.Itoa(r.PeerPID)
	if exe := procs.Exe(r.PeerPID); exe != "" {
		who = sanitize.Label(filepath.Base(exe), 40) + " " + who
	}
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "den unpair from " + who + ": " + strconv.Itoa(n) + " pairing(s) ended", at: time.Now()}))
	m.restartForControl()
	m.refresh()
	r.Reply(bridge.Decision{Behavior: "allow", Message: "Unpaired " + strconv.Itoa(n) + "."})
	return nil
}
