// Package inbox turns hook events into the list of things waiting on the
// human, plus the "done" and "idle" notices that are nice to see once.
package inbox

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/question"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Kind is what an item asks of you.
type Kind int

const (
	Permission      Kind = iota // a tool wants to run; y/n/a answer it
	Plan                        // ExitPlanMode: approve or ask for changes
	Question                    // AskUserQuestion: answer in the terminal, or in Den with hooks.answer_questions
	Done                        // a turn finished while you were elsewhere
	Idle                        // Claude Code's idle notice
	Sent                        // a message you sent, with its delivery status
	Close                       // Den asks: close these agents? y/n answer it
	Skills                      // Den asks: install these catalogue skills? y/n answer it
	Budget                      // Den asks: a mission reached its budget; keep going or stop it? y/n answer it
	Restore                     // Den asks: reopen these agents with claude --resume? y/n answer it
	Stalled                     // working, but no hook event for a while; never "needs you"
	Failed                      // a turn ended on an API error, or tool calls keep failing
	Merge                       // Den asks: merge this agent's worktree branch into the main checkout? y/n answer it
	Cleanup                     // Den asks: remove this agent's worktree (and its branch, if merged)? y/n answer it
	Install                     // Den asks: copy this den to a remote host? y/n answer it
	Message                     // Den asks: send this text, asked for from a script, to an agent on a host? y/n answer it
	Guidance                    // Den asks: load this orchestrator guidance, shown whole with its SHA-256? y/n answer it
	MissionUpdate               // Den asks: the orchestrator says a mission is done, failed or moves on; apply it? y/n answer it
	Usage                       // Den says: the weekly limit reached a warning share, or the fleet was stopped for it
	UsageResume                 // Den asks: let the fleet go on after a usage stop? y/n answer it
	MissionMove                 // Den asks: move this mission to another project, as you typed? y/n answer it
	APIOn                       // Den asks: turn on den-api.sock for apps on this machine? y/n answer it
	Grant                       // an app asks, through den-api.sock: grant these capabilities? y/n answer it, once
	Pair                        // Den asks: may the program that claimed the pairing code control the orchestrator? y/n answer it
	ControlAsk                  // in a paired app's turn the orchestrator wants a tool: allow it? y/n answer it, once
	Console                     // Den asks: make the program that claimed the console code your console? y/n answer it
	ConsoleNote                 // Den says: something happened to the console (another program tried it, it was restored)
	UsageSet                    // Den asks: take this usage figure, as the console sent it? y/n answer it
	ModeSet                     // Den asks: start agents in a mode that asks less? y/n answer it
	DesktopInstall              // Den asks: install hypruse into Den's own venv, as planned? y/n answer it
	DesktopMission              // Den asks: start this desktop mission, in this scope? y/n answer it
	DesktopAct                  // a desktop agent's action needs you: allow this one call? y/n answer it
	YourTurn                    // a desktop mission waits for a step only you do; y says it is done, n ends the mission
	GuardNote                   // Den says: it blocked a message to a guarded agent that Den did not deliver
	MissionCriteria             // Den asks: change a mission's done criteria, as the orchestrator asks? y/n, in Den only
	MissionDone                 // Den asks: every done criterion is claimed; is the mission done? y/n, in Den only
	MirrorAsk                   // Den asks: show your orchestrator conversation in the paired console? y/n, in Den only
)

func (k Kind) String() string {
	switch k {
	case Permission:
		return "permission"
	case Plan:
		return "plan ready"
	case Question:
		return "question"
	case Done:
		return "done"
	case Idle:
		return "idle"
	case Sent:
		return "sent"
	case Close:
		return "close"
	case Skills:
		return "skills"
	case Budget:
		return "budget"
	case MissionUpdate:
		return "mission update"
	case Usage:
		return "usage"
	case UsageResume:
		return "usage resume"
	case Restore:
		return "restore"
	case Stalled:
		return "stalled"
	case Failed:
		return "failed"
	case Merge:
		return "merge"
	case Cleanup:
		return "cleanup"
	case Install:
		return "install"
	case Message:
		return "message"
	case Guidance:
		return "guidance"
	case MissionMove:
		return "move"
	case APIOn:
		return "api"
	case Grant:
		return "app grant"
	case Pair:
		return "pairing"
	case ControlAsk:
		return "app's turn"
	case Console:
		return "console"
	case ModeSet:
		return "mode raise"
	case UsageSet:
		return "usage figure"
	case GuardNote:
		return "blocked message"
	case ConsoleNote:
		return "console note"
	case DesktopInstall:
		return "desktop install"
	case DesktopMission:
		return "desktop mission"
	case DesktopAct:
		return "desktop action"
	case YourTurn:
		return "your turn"
	case MissionCriteria:
		return "mission criteria"
	case MissionDone:
		return "mission done"
	case MirrorAsk:
		return "conversation mirror"
	}
	return "?"
}

// State is where an item is in its life.
type State int

const (
	Waiting    State = iota // Den can answer it
	InTerminal              // only the terminal can answer it now
	Resolved                // answered or superseded; kept briefly for context
)

// Item is one inbox entry.
type Item struct {
	ID        string
	Host      string // the remote host label the agent runs on, "" for this machine
	SessionID string
	Kind      Kind
	State     State
	At        time.Time
	Tool      string
	Title     string   // one line, what is being asked
	Intent    string   // the agent's own words for why, when the tool call carries them
	Body      []string // the thing being decided, in full: the command, the diff, the arguments
	Options   []string // AskUserQuestion choices, numbered from 1 as Den shows them
	Resolved  string   // how it was resolved, for the dim trailer
	// Answer is the verb that settled it (allow, deny or always), "" when
	// it settled otherwise; AnsweredBy is "den" for the human's answer in
	// Den, "console" for the console's.
	Answer     string
	AnsweredBy string
	Role       string // the agent's role when this happened
	Task       string // what the agent was working on when this happened
	Seen       bool   // shown on the Inbox view at least once
	Unread     bool   // kept unread by the human: counts as new until opened or dismissed
	// A question Den holds (hooks.answer_questions): all of the call's
	// questions, the answers so far by question text, and the one asked now.
	Questions []question.Question
	Answers   map[string]string
	Step      int
	req       *bridge.Request
	fromHost  bool // made by a remote host's event stream: counts towards its caps
}

// Answerable reports whether y/n/a act on this item.
func (it *Item) Answerable() bool {
	if it.DenAsks() {
		return it.State == Waiting
	}
	return it.State == Waiting && (it.Kind == Permission || it.Kind == Plan || it.Kind == Question) && it.req != nil && it.req.WantsReply() &&
		!(it.req.Host != "" && it.req.HungUp()) // a remote one let go: its link is lost or its hook gave up
}

// AskerGone reports whether the request behind the item no longer waits
// for its answer (the asker hung up or gave up).
func (it *Item) AskerGone() bool { return it.req != nil && it.req.HungUp() }

// AsksInDen reports whether the item is an agent's question that Den holds
// and the human can answer here, one question at a time.
func (it *Item) AsksInDen() bool {
	return it.Kind == Question && it.Answerable() && it.Step < len(it.Questions)
}

// DenAsks reports whether the item is Den's own question, carried out by
// Den on ^Y rather than answered back to a hook.
func (it *Item) DenAsks() bool {
	return it.Kind == Close || it.Kind == Skills || it.Kind == Budget || it.Kind == Restore || it.Kind == Merge || it.Kind == Cleanup || it.Kind == Install || it.Kind == Message || it.Kind == Guidance || it.Kind == MissionUpdate || it.Kind == UsageResume || it.Kind == MissionMove || it.Kind == APIOn || it.Kind == Grant || it.Kind == Pair || it.Kind == ControlAsk || it.Kind == Console || it.Kind == UsageSet || it.Kind == ModeSet ||
		it.Kind == DesktopInstall || it.Kind == DesktopMission || it.Kind == DesktopAct || it.Kind == YourTurn ||
		it.Kind == MissionCriteria || it.Kind == MissionDone || it.Kind == MirrorAsk
}

// needsYou reports whether the item still waits on the human.
func (it *Item) needsYou() bool {
	return it.State != Resolved && (it.Kind <= Question || it.DenAsks())
}

// Request is the bridge request behind the item, nil for Den's own items.
func (it *Item) Request() *bridge.Request { return it.req }

// Store holds items and the per-session "always allow" rules.
type Store struct {
	history int            // see HistoryLimit
	failing int            // see ToolFailureLimit
	streak  map[string]int // agentKey(host, session) -> tool calls failed in a row
	// heldQ is when a host's agent last had a question held for Den: the
	// host's own "has a question" event for it, which can come late, says
	// nothing more for a while (heldQuiet).
	heldQ   map[string]time.Time
	items   []*Item
	always  map[string]map[string]bool // agentKey(host, session) -> allow key
	seq     int
	asks    bool      // hold AskUserQuestion permission requests to be answered here
	reoffer time.Time // when the item a request offered again replaces arrived
	remote  remoteCaps
}

// New returns an empty store.
func New() *Store {
	return &Store{always: map[string]map[string]bool{}, streak: map[string]int{}, failing: DefaultToolFailures}
}

// DefaultToolFailures is how many tool calls must fail in a row before the
// store adds a Failed item, unless ToolFailureLimit says otherwise.
const DefaultToolFailures = 3

// ToolFailureLimit sets how many tool calls must fail in a row before a
// Failed item appears; 0 turns that off.
func (s *Store) ToolFailureLimit(n int) { s.failing = n }

// AnswerQuestions lets the store hold an agent's AskUserQuestion call so
// the human can answer it in Den (hooks.answer_questions). Off, a question
// is a notice to answer in the terminal, as before.
func (s *Store) AnswerQuestions(on bool) { s.asks = on }

// Items returns items newest-last, waiting ones first.
func (s *Store) Items() []*Item {
	out := append([]*Item(nil), s.items...)
	sort.SliceStable(out, func(i, j int) bool {
		wi, wj := out[i].needsYou(), out[j].needsYou()
		if wi != wj {
			return wi
		}
		if wi {
			return out[i].At.Before(out[j].At) // answer in the order they asked
		}
		return out[i].At.After(out[j].At) // newest notice on top
	})
	return out
}

// Waiting counts items a human still has to deal with.
func (s *Store) Waiting() int {
	n := 0
	for _, it := range s.items {
		if it.needsYou() {
			n++
		}
	}
	return n
}

// SessionNeedsYou reports whether a session on this machine has anything
// waiting.
func (s *Store) SessionNeedsYou(sessionID string) bool { return s.NeedsYouOn("", sessionID) }

// WaitingInTerminal reports whether a session on this machine has a
// permission prompt, plan or question that only its own terminal can
// answer: the window it runs in is where the human has to look.
func (s *Store) WaitingInTerminal(sessionID string) bool {
	for _, it := range s.items {
		if it.is("", sessionID) && it.State == InTerminal && it.Kind <= Question {
			return true
		}
	}
	return false
}

// NeedsYouOn reports whether a session on host ("" for this machine) has
// anything waiting.
func (s *Store) NeedsYouOn(host, sessionID string) bool {
	for _, it := range s.items {
		if it.is(host, sessionID) && it.needsYou() {
			return true
		}
	}
	return false
}

// Apply folds a hook request into the store. It returns the item it created,
// if any. Permission requests matching an "always" rule are answered here.
func (s *Store) Apply(r *bridge.Request) *Item {
	it := s.apply(r)
	if it != nil {
		it.clean()
	}
	return it
}

func (s *Store) apply(r *bridge.Request) *Item {
	in := r.Input
	host := r.Host
	// Any sign of life ends a stall.
	s.resolveSession(host, in.SessionID, "active again", func(it *Item) bool { return it.Kind == Stalled })
	switch r.Event {
	case "PermissionRequest":
		s.offeredAgain(r)
		if it := s.holdQuestion(r); it != nil {
			return it
		}
		if in.ToolName == question.Tool {
			// Not held for Den: a bare allow would be ignored by Claude Code,
			// so let its dialog answer it; the PreToolUse notice stays.
			r.Reply(bridge.Decision{})
			return nil
		}
		if key := allowKey(in, host); key != "" && s.always[agentKey(host, in.SessionID)][key] {
			r.Reply(bridge.Decision{Behavior: "allow", Message: "den: allowed by your earlier always-allow"})
			return nil
		}
		if host != "" && !s.heldFits(host) {
			r.Reply(bridge.Decision{}) // to its terminal on the host
			return nil
		}
		it := s.add(&Item{Host: host, SessionID: in.SessionID, Tool: in.ToolName, req: r})
		if in.ToolName == bridge.PlanTool {
			it.Kind = Plan
			it.Title = "Plan ready for review"
			it.Body = append(planExcerpt(in.ToolInput), extraLines(in.ToolName, in.ToolInput)...)
			if _, ok := bridge.PlanApproval(in.ToolInput); !ok {
				s.Release(it, CannotApprovePlan) // not held: the terminal is the only place to answer it
			}
		} else {
			it.Kind = Permission
			it.Title, it.Intent, it.Body = describeTool(in.ToolName, in.ToolInput)
		}
		if host != "" {
			it.Title += " on " + host
			it.clean()
		}
		// Drop the soft "permission_prompt" notice for the same session.
		for _, o := range s.items {
			if o != it && o.is(host, in.SessionID) && (o.Kind == Permission || o.Kind == Plan) && o.req == nil && o.State != Resolved {
				o.State, o.Resolved = Resolved, "superseded"
			}
		}
		return it
	case "PreToolUse":
		if in.ToolName != question.Tool {
			return nil
		}
		if s.holdsQuestion(host, in.SessionID) {
			return nil // already held for Den
		}
		it := s.add(&Item{Host: host, SessionID: in.SessionID, Kind: Question, State: InTerminal, Tool: in.ToolName})
		it.Title, it.Options = describeQuestion(in.ToolInput)
		return it
	case "PostToolUse":
		s.resolveSession(host, in.SessionID, "answered in terminal", func(it *Item) bool {
			return it.Kind == Question || (it.Kind <= Plan && it.State == InTerminal)
		})
		// The call Den still holds ran: its dialog was answered in the
		// terminal first. A PermissionRequest carries no tool_use_id, so the
		// call is matched by tool and input; other calls of the turn run
		// beside a held one. A plan's PostToolUse comes with an empty input
		// (the plan is in its response), and a session has one plan open.
		s.resolveSession(host, in.SessionID, "answered in terminal", func(it *Item) bool {
			return it.Kind <= Plan && it.State == Waiting && it.req != nil && it.req.Input.ToolName == in.ToolName && (it.Kind == Plan || sameJSON(it.req.Input.ToolInput, in.ToolInput))
		})
		delete(s.streak, agentKey(host, in.SessionID))
		s.resolveSession(host, in.SessionID, "a tool call worked again", func(it *Item) bool { return it.Kind == Failed && it.Tool != "" })
		return nil
	case "PostToolUseFailure":
		// Failing commands are part of normal work; only a run of them is
		// worth a look. The item names the tool, never its input or output.
		// An interrupt is the human stopping the tool, so it does not count.
		if in.IsInterrupt {
			return nil
		}
		s.streak[agentKey(host, in.SessionID)]++
		n := s.streak[agentKey(host, in.SessionID)]
		if s.failing <= 0 || n < s.failing {
			return nil
		}
		title := fmt.Sprintf("%d tool calls failed in a row", n)
		for _, it := range s.items {
			if it.is(host, in.SessionID) && it.Kind == Failed && it.Tool != "" && it.State != Resolved {
				it.Title, it.Tool = title, sanitize.Line(in.ToolName)
				return nil
			}
		}
		return s.add(&Item{Host: host, SessionID: in.SessionID, Kind: Failed, Tool: in.ToolName, Title: title})
	case "StopFailure":
		s.resolveSession(host, in.SessionID, "turn failed", func(it *Item) bool { return it.Kind != Done && it.Kind != Failed })
		return s.add(&Item{Host: host, SessionID: in.SessionID, Kind: Failed, Title: FailureTitle(in.FailureType())})
	case "Notification":
		switch in.NotificationType {
		case "permission_prompt":
			if s.hasOpen(host, in.SessionID, Permission, Plan) || s.asking(host, in.SessionID) {
				return nil // Claude Code's notice for a dialog Den already shows
			}
			it := s.add(&Item{Host: host, SessionID: in.SessionID, Kind: Permission, State: InTerminal, Title: firstNonEmpty(in.Message, "Waiting for your permission")})
			return it
		case "idle_prompt":
			if s.hasOpen(host, in.SessionID, Idle) {
				return nil
			}
			return s.add(&Item{Host: host, SessionID: in.SessionID, Kind: Idle, Title: firstNonEmpty(in.Message, "Waiting for your input")})
		case "agent_needs_input", "elicitation_dialog", "elicitation_url_dialog":
			if s.hasOpen(host, in.SessionID, Question) {
				return nil
			}
			return s.add(&Item{Host: host, SessionID: in.SessionID, Kind: Question, State: InTerminal, Title: firstNonEmpty(in.Message, "Needs your input")})
		}
	case "UserPromptSubmit":
		s.resolveSession(host, in.SessionID, "you replied", func(it *Item) bool { return it.Kind != Done })
	case "Stop":
		s.resolveSession(host, in.SessionID, "turn finished", func(it *Item) bool { return it.Kind != Done })
		return s.add(&Item{Host: host, SessionID: in.SessionID, Kind: Done, State: Resolved, Title: Summarize(in.LastAssistant)})
	case "SessionEnd":
		s.cancelMessages(host, in.SessionID)
		s.resolveSession(host, in.SessionID, "session ended", func(it *Item) bool { return true })
	}
	return nil
}

// holdQuestion turns an AskUserQuestion permission request into a question
// the human answers in Den, when that is on and the call can be read. The
// question item PreToolUse made a moment earlier becomes it, so it keeps
// its place and its notification. nil leaves the request to the usual path.
func (s *Store) holdQuestion(r *bridge.Request) *Item {
	in := r.Input
	if !s.asks || in.ToolName != question.Tool || !r.WantsReply() {
		return nil
	}
	qs, ok := question.Parse(in.ToolInput)
	if !ok {
		return nil
	}
	var it *Item
	for _, o := range s.items {
		if o.is(r.Host, in.SessionID) && o.Kind == Question && o.State == InTerminal && o.req == nil && o.Tool == question.Tool {
			it = o
		}
	}
	if it == nil {
		it = s.add(&Item{Host: r.Host, SessionID: in.SessionID, Kind: Question, Tool: in.ToolName})
	}
	it.State, it.Resolved, it.req = Waiting, "", r
	it.Questions, it.Answers, it.Step = qs, map[string]string{}, 0
	if r.Host != "" {
		if s.heldQ == nil {
			s.heldQ = map[string]time.Time{}
		}
		s.heldQ[agentKey(r.Host, in.SessionID)] = time.Now()
	}
	it.describeStep()
	// Claude Code's "permission_prompt" notice is this dialog too, and an
	// earlier question of the session is gone from its terminal by now.
	for _, o := range s.items {
		if o != it && o.is(r.Host, in.SessionID) && o.State != Resolved && ((o.Kind == Permission && o.req == nil) || o.Kind == Question) {
			o.State, o.Resolved = Resolved, "superseded"
		}
	}
	return it
}

// asking reports whether an AskUserQuestion of the session is open, held
// or in its terminal: Claude Code's permission notice is that dialog.
func (s *Store) asking(host, sessionID string) bool {
	for _, it := range s.items {
		if it.is(host, sessionID) && it.Kind == Question && it.Tool == question.Tool && it.State != Resolved {
			return true
		}
	}
	return false
}

// holdsQuestion reports whether a question of the session waits in Den.
func (s *Store) holdsQuestion(host, sessionID string) bool {
	for _, it := range s.items {
		if it.is(host, sessionID) && it.AsksInDen() {
			return true
		}
	}
	return false
}

// describeStep shows the question being asked now: its text, where it is
// among the call's questions, its numbered options and the answers given.
func (it *Item) describeStep() {
	if it.Step >= len(it.Questions) {
		return
	}
	q := it.Questions[it.Step]
	it.Title = q.Text
	var intent []string
	if len(it.Questions) > 1 {
		intent = append(intent, fmt.Sprintf("Question %d of %d", it.Step+1, len(it.Questions)))
	}
	if q.Header != "" {
		intent = append(intent, q.Header)
	}
	if q.Multi {
		intent = append(intent, "choose one or more")
	}
	if it.Host != "" {
		intent = append(intent, "on "+it.Host)
	}
	it.Intent = strings.Join(intent, " · ")
	it.Options = nil
	for _, o := range q.Options {
		label := o.Label
		if o.Description != "" {
			label += " — " + o.Description
		}
		it.Options = append(it.Options, label)
	}
	it.Body = nil
	for _, done := range it.Questions[:it.Step] {
		it.Body = append(it.Body, "✓ "+done.Text+" → "+it.Answers[done.Text])
	}
	it.clean()
}

// clean sanitises what an item shows, all of which can come from an agent:
// the tool name, title, intent, body, options and trailer, and the role and
// task. A body line stays one line. The questions keep the agent's own text,
// because the answers go back keyed by it; describeStep shows them cleaned.
func (it *Item) clean() {
	it.Tool = sanitize.Line(it.Tool)
	it.Title = sanitize.Line(it.Title)
	it.Intent = sanitize.Line(it.Intent)
	it.Resolved = sanitize.Line(it.Resolved)
	it.Role = sanitize.Line(it.Role)
	it.Task = sanitize.Line(it.Task)
	for i, l := range it.Body {
		it.Body[i] = sanitize.Code(l)
	}
	for i, o := range it.Options {
		it.Options[i] = sanitize.Line(o)
	}
}

// AnswerQuestion takes the human's answer to the question being asked now:
// an option's number (several for a multi-select question) or their own
// words. With every question answered, the answers go to the waiting hook.
// done reports that; ok is false when the answer could not be read.
func (s *Store) AnswerQuestion(it *Item, typed string) (done, ok bool) {
	if !it.AsksInDen() {
		return false, false
	}
	q := it.Questions[it.Step]
	a, ok := question.Pick(q, typed)
	if !ok {
		return false, false
	}
	it.Answers[q.Text] = a
	it.Step++
	if it.Step < len(it.Questions) {
		it.describeStep()
		return false, true
	}
	answers := make(map[string]string, len(it.Answers))
	for k, v := range it.Answers {
		answers[k] = v
	}
	it.Body = append(it.Body, "✓ "+q.Text+" → "+a)
	it.clean()
	it.req.Reply(bridge.Decision{Behavior: "allow", Answers: answers})
	it.State, it.Resolved = Resolved, "answered in den"
	return true, true
}

// LetGo stops holding requests whose hook stopped waiting, so none stays
// answerable in Den. Claude Code shows its own dialog at once, beside the
// held hook, and ends the hook when that dialog is answered or dismissed:
// a hook gone before wait (hooks.permission_wait) ran out means the
// terminal settled it, and the item closes. One gone at the end of the
// wait (or with wait unknown, 0) gave up itself; the dialog is still up
// and the item says to answer it in the terminal. A remote host's held
// items go the same way when they are let go, saying why ("released to
// the terminal on devbox (link lost)"). It reports whether any changed.
func (s *Store) LetGo(wait time.Duration) bool {
	changed := false
	for _, it := range s.items {
		if it.Kind > Question || it.State != Waiting || it.req == nil || !it.req.HungUp() {
			continue
		}
		switch {
		case it.req.Host != "":
			it.State, it.Resolved = InTerminal, firstNonEmpty(it.req.Why(), "no longer held on "+it.req.Host)
			it.clean()
		case wait > 0 && time.Since(it.At) < wait-letGoSlack:
			it.State, it.Resolved = Resolved, ClosedInTerminal
		default:
			it.State, it.Resolved = InTerminal, "no longer held"
		}
		changed = true
	}
	return changed
}

// ClosedInTerminal is how a held request ends when its dialog was answered
// or dismissed in the terminal first.
const ClosedInTerminal = "answered or dismissed in the terminal"

// letGoSlack is how close to the end of Den's wait a hook may hang up and
// still count as giving up itself rather than being ended by Claude Code.
const letGoSlack = 5 * time.Second

// offeredAgain drops the item of a remote request that its host offers
// again under the same id (after Den reconnected), keeping its place, so
// the new request takes over rather than showing twice.
func (s *Store) offeredAgain(r *bridge.Request) {
	if r.Host == "" || r.RemoteID == "" {
		return
	}
	for i, it := range s.items {
		if it.req != nil && it.req != r && it.req.Host == r.Host && it.req.RemoteID == r.RemoteID && it.State != Resolved {
			s.items = append(s.items[:i], s.items[i+1:]...)
			s.reoffer = it.At
			return
		}
	}
}

// AddStalled adds a Stalled notice for a session that has been working
// with no hook event for quiet. Any hook event from it resolves the notice.
func (s *Store) AddStalled(sessionID string, quiet time.Duration) *Item {
	if s.hasOpen("", sessionID, Stalled) {
		return nil
	}
	mins := int(quiet.Minutes())
	unit := "minutes"
	if mins == 1 {
		unit = "minute"
	}
	return s.add(&Item{SessionID: sessionID, Kind: Stalled, Title: fmt.Sprintf("Working with no sign of progress for %d %s", mins, unit)})
}

// FailureTitle says why a turn ended, from a StopFailure error type.
func FailureTitle(errType string) string {
	switch errType {
	case "rate_limit":
		return "Turn ended: rate limited"
	case "overloaded":
		return "Turn ended: the API is overloaded"
	case "authentication_failed", "oauth_org_not_allowed", "cloud_credential_error":
		return "Turn ended: not logged in or credentials failed"
	case "billing_error", "account_on_hold":
		return "Turn ended: a billing or account problem"
	case "max_output_tokens":
		return "Turn ended: the reply hit the output limit"
	case "", "unknown":
		return "Turn ended on an API error"
	}
	return "Turn ended on an API error: " + strings.ReplaceAll(errType, "_", " ")
}

// Sent records an outgoing message. status is shown as the trailer and can
// be updated through SetStatus.
func (s *Store) Sent(sessionID, text, status string) *Item {
	return s.SentOn("", sessionID, text, status)
}

// SentOn is Sent to the agent on a remote host ("" for this machine).
func (s *Store) SentOn(host, sessionID, text, status string) *Item {
	return s.add(&Item{Host: host, SessionID: sessionID, Kind: Sent, State: InTerminal, Title: "→ " + firstLine(text), Resolved: status})
}

// SetStatus updates a notice's trailer, marking it resolved when final.
func (s *Store) SetStatus(it *Item, status string, final bool) {
	it.Resolved = sanitize.Line(status)
	if final {
		it.State = Resolved
		it.At = time.Now()
	}
}

// PlansOneAtATime is why /always does not answer a plan: a plan is what
// the human reads each time, and Claude Code's own plan dialog has no
// "always" either.
const PlansOneAtATime = "plans are approved one at a time; ^Y approves this one"

// CannotApprovePlan is why a plan Den cannot approve went back to its
// terminal.
const CannotApprovePlan = "Den cannot approve this plan: answer it in the terminal"

// Answer resolves an answerable item. verb is "allow", "deny" or "always".
// It reports whether the answer went to the hook; a plan whose call Den
// cannot approve goes back to the terminal instead (CannotApprovePlan).
func (s *Store) Answer(it *Item, verb string) bool {
	if !it.Answerable() || it.DenAsks() || it.Kind == Question {
		return false // Den's own questions are carried out by Den, an agent's question takes an answer
	}
	if it.req.HungUp() {
		return false // nobody waits for the answer; LetGo says what became of it
	}
	if it.Kind == Plan && verb == "always" {
		return false // PlansOneAtATime
	}
	if it.Kind == Plan && verb != "deny" {
		if _, ok := bridge.PlanApproval(it.req.Input.ToolInput); !ok {
			// The hook could only answer with a bare allow, which Claude
			// Code ignores: never say a plan was approved when it was not.
			s.Release(it, CannotApprovePlan)
			return false
		}
	}
	switch verb {
	case "allow":
		it.req.Reply(bridge.Decision{Behavior: "allow"})
		it.State, it.Resolved = Resolved, "allowed in den"
	case "always":
		if AlwaysRefusal(it) != "" {
			return false
		}
		if key := allowKey(it.req.Input, it.Host); key != "" {
			k := agentKey(it.Host, it.SessionID)
			if s.always[k] == nil {
				s.always[k] = map[string]bool{}
			}
			s.always[k][key] = true
		}
		it.req.Reply(bridge.Decision{Behavior: "allow"})
		it.State, it.Resolved = Resolved, "allowed, and again this session"
	case "deny":
		it.req.Reply(bridge.Decision{Behavior: "deny", Message: "Denied from den by the user."})
		it.State, it.Resolved = Resolved, "denied in den"
	default:
		return false
	}
	return true
}

// AddClose adds Den's own question: close these agents? sessionID is the
// agent it concerns, or the asker's session for a bulk close. req is the
// orchestrator's request to answer when it asked, nil when the human did.
func (s *Store) AddClose(sessionID, title string, body []string, req *bridge.Request) *Item {
	return s.AddCloseOn("", sessionID, title, body, req)
}

// AddCloseOn is AddClose for a plan whose first agent runs on a remote host
// ("" for this machine).
func (s *Store) AddCloseOn(host, sessionID, title string, body []string, req *bridge.Request) *Item {
	return s.add(&Item{Host: host, SessionID: sessionID, Kind: Close, Tool: "close_agent", Title: title, Body: body, req: req})
}

// AddSkills adds Den's question: install these catalogue skills? req is
// the request of whoever proposed them.
func (s *Store) AddSkills(sessionID, title string, body []string, req *bridge.Request) *Item {
	return s.add(&Item{SessionID: sessionID, Kind: Skills, Tool: "propose_skills", Title: title, Body: body, req: req})
}

// AddBudget adds Den's question: a mission reached its budget, keep going or
// stop it? sessionID is "mission:<id>".
func (s *Store) AddBudget(sessionID, title string, body []string) *Item {
	return s.add(&Item{SessionID: sessionID, Kind: Budget, Tool: "mission_budget", Title: title, Body: body})
}

// AddRestore adds Den's question: reopen these agents? sessionID is the
// first agent it lists.
func (s *Store) AddRestore(sessionID, title string, body []string) *Item {
	return s.add(&Item{SessionID: sessionID, Kind: Restore, Tool: "restore", Title: title, Body: body})
}

// AddInstall adds Den's question: copy this den to a remote host? host is
// its label; req is the request of the den remote command that asked.
func (s *Store) AddInstall(host, title string, body []string, req *bridge.Request) *Item {
	return s.add(&Item{SessionID: HostPrefix + host, Kind: Install, Tool: "remote_install", Title: title, Body: body, req: req})
}

// DesktopSession is the SessionID of Den's questions about desktop control
// that concern no one agent yet: the hypruse install, a mission's start.
const DesktopSession = "den:desktop"

// AddDesktopInstall adds Den's question: install hypruse as planned? req
// is the request of the den install-hypruse that asked; it gets the
// outcome. Only the human's ^Y answers it.
func (s *Store) AddDesktopInstall(title string, body []string, req *bridge.Request) *Item {
	return s.add(&Item{SessionID: DesktopSession, Kind: DesktopInstall, Tool: "install_hypruse", Title: title, Body: body, req: req})
}

// AddDesktopAct adds a desktop agent's action that needs the human: allow
// this one call? req is the proxy's request; SettleWith answers it.
func (s *Store) AddDesktopAct(sessionID, tool, title string, body []string, req *bridge.Request) *Item {
	return s.add(&Item{SessionID: sessionID, Kind: DesktopAct, Tool: tool, Title: title, Body: body, req: req})
}

// AddYourTurn adds a desktop mission's step only the human does: ^Y says it
// is done, ^N ends the mission. req is the agent's your_turn call.
func (s *Store) AddYourTurn(sessionID, title string, body []string, req *bridge.Request) *Item {
	return s.add(&Item{SessionID: sessionID, Kind: YourTurn, Tool: "your_turn", Title: title, Body: body, req: req})
}

// AddDesktopMission adds Den's question: start this desktop mission, in
// this scope? req is the dispatcher's request.
func (s *Store) AddDesktopMission(title string, body []string, req *bridge.Request) *Item {
	return s.add(&Item{SessionID: DesktopSession, Kind: DesktopMission, Tool: "desktop_mission", Title: title, Body: body, req: req})
}

// AddMessageOn adds Den's question: send this text to an agent on host?
// req is the request of the den send that asked; it gets the outcome.
func (s *Store) AddMessageOn(host, sessionID, title string, body []string, req *bridge.Request) *Item {
	return s.add(&Item{Host: host, SessionID: sessionID, Kind: Message, Tool: "den_send", Title: title, Body: body, req: req})
}

// GuidanceSession is the SessionID of Den's questions about orchestrator
// guidance, which concern no agent.
const GuidanceSession = "den:guidance"

// AddGuidance adds Den's question: load this orchestrator guidance? Only
// the human's ^Y answers it; nothing else can.
func (s *Store) AddGuidance(title string, body []string) *Item {
	return s.add(&Item{SessionID: GuidanceSession, Kind: Guidance, Tool: "orchestrator_guidance", Title: title, Body: body})
}

// UsageSession is the SessionID of Den's notices and questions about the
// weekly limit, which concern no one agent.
const UsageSession = "den:usage"

// AddUsage adds Den's notice about the weekly limit.
func (s *Store) AddUsage(title string, body []string) *Item {
	return s.add(&Item{SessionID: UsageSession, Kind: Usage, Title: title, Body: body})
}

// AddUsageResume adds Den's question: let the fleet go on after a usage
// stop? Only the human's ^Y answers it, once its whole text was shown.
func (s *Store) AddUsageResume(title string, body []string) *Item {
	if s.hasOpen("", UsageSession, UsageResume) {
		return nil
	}
	return s.add(&Item{SessionID: UsageSession, Kind: UsageResume, Tool: "usage_resume", Title: title, Body: body})
}

// AddUsageSet adds Den's question: take the usage figure the console
// sent? One at a time; nil while one is open.
func (s *Store) AddUsageSet(title string, body []string) *Item {
	if s.hasOpen("", UsageSession, UsageSet) {
		return nil
	}
	return s.add(&Item{SessionID: UsageSession, Kind: UsageSet, Tool: "usage_set", Title: title, Body: body})
}

// ModeSession is the session id of Den's mode cards.
const ModeSession = "den:mode"

// AddModeSet adds Den's question: start agents in a mode that asks less?
// One at a time; nil while one is open, which the caller settles first to
// put a newer request in its place.
func (s *Store) AddModeSet(title string, body []string) *Item {
	if s.hasOpen("", ModeSession, ModeSet) {
		return nil
	}
	return s.add(&Item{SessionID: ModeSession, Kind: ModeSet, Tool: "mode_set", Title: title, Body: body})
}

// AddMissionMove adds Den's question: move this mission to another
// project? sessionID is "mission:<id>". Only the human's /mission move
// raises it, and only the human's ^Y answers it.
func (s *Store) AddMissionMove(sessionID, title string, body []string) *Item {
	return s.add(&Item{SessionID: sessionID, Kind: MissionMove, Tool: "mission_move", Title: title, Body: body})
}

// APISession is the SessionID of Den's question about turning on
// den-api.sock.
const APISession = "den:api"

// AddAPIOn adds Den's question: turn on den-api.sock? Only the human's ^Y
// answers it.
func (s *Store) AddAPIOn(title string, body []string) *Item {
	return s.add(&Item{SessionID: APISession, Kind: APIOn, Tool: "api_on", Title: title, Body: body})
}

// AddGrant adds an app's grant request. body is Den's frame around the
// app's text, which is never cut or cleaned (den-api.sock refuses text it
// could not show as it is). Only the human's ^Y or ^N answers it.
func (s *Store) AddGrant(title string, body []string) *Item {
	return s.add(&Item{SessionID: APISession, Kind: Grant, Tool: "app_grant", Title: title, Body: body})
}

// AddPair adds Den's question: may the program that claimed the pairing
// code control the orchestrator?
func (s *Store) AddPair(title string, body []string) *Item {
	return s.add(&Item{SessionID: APISession, Kind: Pair, Tool: "app_pairing", Title: title, Body: body})
}

// AddConsole adds Den's question: make the program that claimed the
// console code the human's console? Only the human's ^Y answers it.
func (s *Store) AddConsole(title string, body []string) *Item {
	return s.add(&Item{SessionID: APISession, Kind: Console, Tool: "console_pairing", Title: title, Body: body})
}

// AddGuardNote adds Den's notice that it blocked a message to the guarded
// session sessionID that Den did not deliver.
func (s *Store) AddGuardNote(sessionID, title string, body []string) *Item {
	return s.add(&Item{SessionID: sessionID, Kind: GuardNote, Title: title, Body: body})
}

// AddConsoleNote adds Den's notice about the console.
func (s *Store) AddConsoleNote(title string, body []string) *Item {
	return s.add(&Item{SessionID: APISession, Kind: ConsoleNote, Title: title, Body: body})
}

// AddControlAsk adds a tool the orchestrator wants in a paired app's turn.
func (s *Store) AddControlAsk(title string, body []string) *Item {
	return s.add(&Item{SessionID: APISession, Kind: ControlAsk, Tool: "app_turn_tool", Title: title, Body: body})
}

// HostPrefix makes a remote host's label an item's SessionID, for Den's
// questions about the host rather than one of its agents.
const HostPrefix = "host:"

// AddMissionCriteria adds Den's question: change a mission's done criteria
// as the orchestrator asks? Answered in Den only.
func (s *Store) AddMissionCriteria(sessionID, title string, body []string) *Item {
	return s.add(&Item{SessionID: sessionID, Kind: MissionCriteria, Tool: "update_mission", Title: title, Body: body})
}

// AddMirrorAsk adds Den's question: show the human's orchestrator
// conversation in the paired console?
func (s *Store) AddMirrorAsk(title string, body []string) *Item {
	return s.add(&Item{SessionID: "den:mirror", Kind: MirrorAsk, Tool: "mirror", Title: title, Body: body})
}

// AddMissionDone adds Den's question: every criterion is claimed; is the
// mission done? Answered in Den only.
func (s *Store) AddMissionDone(sessionID, title string, body []string) *Item {
	return s.add(&Item{SessionID: sessionID, Kind: MissionDone, Tool: "update_mission", Title: title, Body: body})
}

// AddMissionUpdate adds Den's question: apply the orchestrator's update to a
// mission? sessionID names the mission ("mission:<id>").
func (s *Store) AddMissionUpdate(sessionID, title string, body []string) *Item {
	return s.add(&Item{SessionID: sessionID, Kind: MissionUpdate, Tool: "update_mission", Title: title, Body: body})
}

// AddMerge adds Den's question: merge this agent's worktree branch? sessionID
// is the agent that works in the worktree.
func (s *Store) AddMerge(sessionID, title string, body []string) *Item {
	return s.add(&Item{SessionID: sessionID, Kind: Merge, Tool: "worktree_merge", Title: title, Body: body})
}

// AddCleanup adds Den's question: remove this agent's worktree?
func (s *Store) AddCleanup(sessionID, title string, body []string) *Item {
	return s.add(&Item{SessionID: sessionID, Kind: Cleanup, Tool: "worktree_cleanup", Title: title, Body: body})
}

// Settle resolves a Close or Skills item with the outcome and sends it to whoever
// asked. allowed says whether the close ran.
func (s *Store) Settle(it *Item, allowed bool, outcome string) {
	it.State, it.Resolved = Resolved, sanitize.Line(outcome)
	if it.req == nil {
		return
	}
	d := bridge.Decision{Behavior: "deny", Message: outcome}
	if allowed {
		d.Behavior = "allow"
	}
	it.req.Reply(d)
}

// SettleWith resolves an item with the outcome and answers whoever asked
// with d as it is (its Data included).
func (s *Store) SettleWith(it *Item, d bridge.Decision, outcome string) {
	it.State, it.Resolved = Resolved, sanitize.Line(outcome)
	if it.req != nil {
		it.req.Reply(d)
	}
}

// SessionClosed resolves everything open for an agent Den closed.
func (s *Store) SessionClosed(sessionID string) { s.SessionClosedOn("", sessionID) }

// SessionClosedOn is SessionClosed for an agent on a remote host.
func (s *Store) SessionClosedOn(host, sessionID string) {
	s.resolveSession(host, sessionID, "agent closed", func(it *Item) bool { return !it.DenAsks() })
}

// Release hands a waiting permission back to the terminal dialog.
func (s *Store) Release(it *Item, why string) {
	if it.req != nil {
		it.req.Reply(bridge.Decision{})
	}
	if it.State == Waiting {
		it.State = InTerminal
		it.Resolved = why
	}
}

// KeepUnread flags an item so it counts as new again, until it is opened,
// dismissed or marked read. It reports the new state.
func (s *Store) KeepUnread(it *Item) bool {
	it.Unread = !it.Unread
	it.Seen = !it.Unread
	return it.Unread
}

// MarkRead clears a kept-unread flag, as opening the item does.
func (s *Store) MarkRead(it *Item) {
	if it.Unread {
		it.Unread, it.Seen = false, true
	}
}

// Dismiss removes an item that needs no answer, or a resolved one.
func (s *Store) Dismiss(it *Item) {
	if it.Answerable() {
		return
	}
	for i, o := range s.items {
		if o == it {
			s.items = append(s.items[:i], s.items[i+1:]...)
			return
		}
	}
}

// MaxHistory is how many finished turns the inbox keeps as history unless
// the store says otherwise.
const MaxHistory = 100

// HistoryLimit overrides MaxHistory for this store when positive.
func (s *Store) HistoryLimit(n int) { s.history = n }

func (s *Store) historyLimit() int {
	if s.history > 0 {
		return s.history
	}
	return MaxHistory
}

// Prune drops resolved items older than keep and dismissible notices beyond
// max. Finished turns are history: they stay, newest MaxHistory of them.
func (s *Store) Prune(keep time.Duration, maxNotices int) {
	var out []*Item
	notices, history := 0, 0
	for i := len(s.items) - 1; i >= 0; i-- {
		it := s.items[i]
		if it.Kind == Done {
			history++
			if history > s.historyLimit() {
				continue
			}
			out = append(out, it)
			continue
		}
		if it.State == Resolved && time.Since(it.At) > keep && !it.Unread {
			continue
		}
		if it.State == InTerminal && it.Host != "" && time.Since(it.At) > keep && !it.Unread {
			// A host's request it let go: only its terminal there answers
			// it, and a host churning held requests would otherwise keep
			// every one here for good.
			continue
		}
		if it.Kind > Done {
			notices++
			if notices > maxNotices {
				continue
			}
		}
		out = append(out, it)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	s.items = out
}

// Unseen counts items that have not been shown on the Inbox view yet, plus
// anything still waiting for an answer.
func (s *Store) Unseen() int {
	n := 0
	for _, it := range s.items {
		if !it.Seen || it.Answerable() {
			n++
		}
	}
	return n
}

// MarkSeen records that the Inbox view showed everything it holds.
func (s *Store) MarkSeen() {
	for _, it := range s.items {
		if !it.Unread {
			it.Seen = true
		}
	}
}

// Restore puts history loaded from disk in front of everything else, already
// seen.
func (s *Store) Restore(items []*Item) {
	for _, it := range items {
		s.seq++
		it.ID = fmt.Sprintf("h%d", s.seq)
		it.Seen = true
		it.clean() // written by an older Den, or edited on disk
		if it.Kind == 0 {
			it.Kind = Done
		}
		it.State = Resolved
	}
	s.items = append(items, s.items...)
}

func (s *Store) add(it *Item) *Item {
	it.clean()
	s.seq++
	it.ID = fmt.Sprintf("i%d", s.seq)
	if it.At.IsZero() && !s.reoffer.IsZero() {
		it.At = s.reoffer
	}
	s.reoffer = time.Time{}
	if it.At.IsZero() {
		it.At = time.Now()
	}
	s.items = append(s.items, it)
	return it
}

func (s *Store) hasOpen(host, sessionID string, kinds ...Kind) bool {
	for _, it := range s.items {
		if !it.is(host, sessionID) || it.State == Resolved {
			continue
		}
		for _, k := range kinds {
			if it.Kind == k {
				return true
			}
		}
	}
	return false
}

// resolveSession resolves what match picks of an agent's open items. A den
// send question is never among them: whether a message may reach the agent
// stays a question through its turns, until the human answers, the asker
// gives up, the link is lost or the session ends (cancelMessages).
func (s *Store) resolveSession(host, sessionID, why string, match func(*Item) bool) {
	for _, it := range s.items {
		if it.is(host, sessionID) && it.State != Resolved && it.Kind != Message && match(it) {
			if it.req != nil {
				it.req.Reply(bridge.Decision{})
			}
			it.State, it.Resolved = Resolved, why
		}
	}
}

// cancelMessages ends the den send questions for an agent whose session
// ended; the den send that asked hears why, never an empty answer.
func (s *Store) cancelMessages(host, sessionID string) {
	where := "here"
	if host != "" {
		where = "on " + host
	}
	for _, it := range s.items {
		if it.is(host, sessionID) && it.State != Resolved && it.Kind == Message {
			it.State, it.Resolved = Resolved, "cancelled: the session "+where+" ended; nothing was sent"
			if it.req != nil {
				it.req.Reply(bridge.Decision{Behavior: bridge.SendCancelled, Message: "the request was " + it.Resolved})
			}
		}
	}
}

// is reports whether the item belongs to the agent with this session on
// host ("" for this machine). Session ids are only unique per host.
func (it *Item) is(host, sessionID string) bool {
	return it.Host == host && it.SessionID == sessionID
}

// agentKey keys per-agent state by host and session.
func agentKey(host, sessionID string) string { return host + "\x00" + sessionID }

// shellOperators are what /always refuses in a command: each lets one
// approved command carry another, or write where it was not shown to.
var shellOperators = []string{";", "&", "|", "$(", "`", "<", ">", "\n", "\r"}

// AlwaysRefusal says why /always cannot answer the item, "" when it can.
// A command with shell operators, substitutions or redirections is
// allowed once at a time, never always: the rule would allow exactly that
// compound line, and an approval of it should be read each time.
func AlwaysRefusal(it *Item) string {
	switch {
	case it.Kind == Plan:
		return "plans are approved one at a time; ^Y approves this one"
	case it.Kind != Permission || it.req == nil:
		return "/always answers a permission only"
	case editTools[it.Tool] != "" && it.Host != "":
		return "edits on " + it.Host + " are approved one at a time"
	case editTools[it.Tool] != "" && allowKey(it.req.Input, it.Host) == "":
		return "/always covers edits inside the agent's working directory, never this file: ^Y allows it once"
	case allowKey(it.req.Input, it.Host) == "":
		return "/always cannot tell this call from others like it: ^Y allows it once"
	case it.Tool != "Bash":
		return ""
	}
	var in struct {
		Command string `json:"command"`
	}
	json.Unmarshal(it.req.Input.ToolInput, &in)
	var found []string
	for _, op := range shellOperators {
		if strings.Contains(in.Command, op) {
			found = append(found, strings.NewReplacer("\n", "a newline", "\r", "a carriage return").Replace(op))
		}
	}
	if len(found) == 0 {
		return ""
	}
	return "/always takes a single plain command, and this one has " + strings.Join(found, " ") + ": ^Y allows it once"
}

// allowKey is the "always" rule key: what a later call must match to be
// allowed without asking. "" is no rule. For Bash, the command exactly as
// it will run: a prefix such as "git status" would let "git status && curl
// … | sh" by. For a file edit, the session's working directory, when the
// file is inside it and not sensitive (editscope.go). For WebFetch, the
// URL's host. A plan or a question has none.
// Anything else is the tool by its full name (an MCP tool with its
// server), as Claude Code's own "allow during this session" is: AlwaysScope
// says so when the rule is made. host is where the agent runs, "" for this
// machine: an edit on a host takes no rule, as its paths and symlinks
// cannot be resolved here. The rule itself is kept per host and session.
func allowKey(in bridge.HookInput, host string) string {
	tool, input := in.ToolName, in.ToolInput
	if field := editTools[tool]; field != "" {
		if host != "" {
			return ""
		}
		var args map[string]any
		json.Unmarshal(input, &args)
		path, _ := args[field].(string)
		home, _ := os.UserHomeDir()
		if !editRealInScope(path, in.CWD, home) {
			return ""
		}
		return tool + ":under:" + filepath.Clean(in.CWD)
	}
	switch tool {
	case "", "ExitPlanMode", question.Tool:
		return ""
	case "Bash":
		var in struct {
			Command string `json:"command"`
		}
		json.Unmarshal(input, &in)
		if strings.TrimSpace(in.Command) == "" {
			return ""
		}
		return "Bash:" + in.Command
	case "WebFetch":
		if host := fetchHost(input); host != "" {
			return "WebFetch:" + host
		}
		return ""
	}
	return tool
}

// fetchHost is the host a WebFetch call asks for, lower-cased, "" when its
// URL has none.
func fetchHost(input json.RawMessage) string {
	var in struct {
		URL string `json:"url"`
	}
	json.Unmarshal(input, &in)
	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// AlwaysScope says what /always on the item allows from then on, for the
// line that confirms it.
func AlwaysScope(it *Item) string {
	if it.req == nil {
		return ""
	}
	key := allowKey(it.req.Input, it.Host)
	switch {
	case key == "":
		return ""
	case editTools[it.Tool] != "":
		return "every " + it.Tool + " under " + sanitize.Line(filepath.Clean(it.req.Input.CWD)) + ", except sensitive files"
	case it.Tool == "Bash":
		return "this exact command, " + sanitize.Line(strings.TrimPrefix(key, "Bash:"))
	case it.Tool == "WebFetch":
		return "every WebFetch to " + sanitize.Line(strings.TrimPrefix(key, "WebFetch:"))
	}
	if server, name, ok := mcpTool(it.Tool); ok {
		return "every " + name + " call on " + server
	}
	return "every " + it.Tool + " call, whatever it touches"
}

// bodyLimit caps how much of a diff, a file or a payload an item carries.
// Enough to decide on; the terminal has the rest.
const bodyLimit = 40

// laidOut are, per tool, the input keys describeTool shows in its own
// layout. Every other key but the quiet ones is listed under the body
// (extraLines), so a flag that changes what the call does (replace_all,
// dangerouslyDisableSandbox, a subagent type) is never hidden.
var laidOut = map[string][]string{
	"Bash": {"command", "description"}, "Edit": {"file_path", "old_string", "new_string"},
	"MultiEdit": {"file_path", "edits"}, "Write": {"file_path", "content"},
	"NotebookEdit": {"notebook_path", "new_source"}, "Read": {"file_path"},
	"WebFetch": {"url", "prompt"}, "WebSearch": {"query"},
	"Agent": {"description", "prompt"}, "Task": {"description", "prompt"},
	"SendMessage": {"to", "message"}, "ExitPlanMode": {"plan"},
}

// quietKeys change nothing worth asking about.
var quietKeys = map[string]bool{"timeout": true, "run_in_background": true, "offset": true, "limit": true, "pages": true}

// extraLines lists a call's arguments its tool's layout does not show, one
// "key: value" line each, in key order.
func extraLines(tool string, input json.RawMessage) []string {
	shown, ok := laidOut[tool]
	if !ok {
		return nil // shown whole as JSON
	}
	var in map[string]any
	json.Unmarshal(input, &in)
	var keys []string
	for k := range in {
		if !quietKeys[k] && !slices.Contains(shown, k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		v, isStr := in[k].(string)
		if !isStr {
			b, _ := json.Marshal(in[k])
			v = string(b)
		}
		out = append(out, sanitize.Line(k)+": "+sanitize.Line(v))
	}
	return out
}

// describeTool says what a permission request is for: a short title, the
// agent's stated intent when the call carries one, and the thing being
// decided in full, line by line, so the human can approve or reject
// knowing exactly what runs.
func describeTool(tool string, input json.RawMessage) (title, intent string, body []string) {
	title, intent, body = describeLayout(tool, input)
	return title, intent, append(body, extraLines(tool, input)...)
}

// describeLayout is describeTool without the arguments its layout leaves
// out.
func describeLayout(tool string, input json.RawMessage) (title, intent string, body []string) {
	var in map[string]any
	json.Unmarshal(input, &in)
	// Sanitised before it is split into lines, so a carriage return
	// starts a line of its own rather than overwriting one.
	str := func(k string) string {
		if v, ok := in[k].(string); ok {
			return sanitize.Text(v)
		}
		return ""
	}
	switch tool {
	case "Bash":
		return "Wants to run", str("description"), commandLines(str("command"))
	case "Edit":
		return "Wants to edit " + str("file_path"), "", diffLines(str("old_string"), str("new_string"))
	case "MultiEdit":
		return "Wants to edit " + str("file_path"), "", multiEditLines(in["edits"])
	case "Write":
		return "Wants to write " + str("file_path"), "", limitLines(str("content"), bodyLimit)
	case "NotebookEdit":
		return "Wants to edit notebook " + str("notebook_path"), "", limitLines(str("new_source"), bodyLimit)
	case "Read":
		return "Wants to read " + str("file_path"), "", nil
	case "WebFetch":
		return "Wants to fetch " + str("url"), str("prompt"), nil
	case "WebSearch":
		return "Wants to search the web", "", []string{str("query")}
	case "Agent", "Task":
		return "Wants to start an agent", str("description"), limitLines(str("prompt"), 12)
	case "SendMessage":
		return "Wants to message " + str("to"), "", limitLines(str("message"), 12)
	}
	if server, name, ok := mcpTool(tool); ok {
		return "Wants to call " + name + " on " + server, "", jsonLines(in)
	}
	return "Wants to use " + tool, "", jsonLines(in)
}

// multiEditLines shows each of a MultiEdit's edits as a diff, numbered,
// with its replace_all, within the body limit.
func multiEditLines(v any) []string {
	edits, _ := v.([]any)
	var out []string
	for i, e := range edits {
		em, _ := e.(map[string]any)
		s := func(k string) string {
			str, _ := em[k].(string)
			return sanitize.Text(str)
		}
		head := fmt.Sprintf("edit %d of %d", i+1, len(edits))
		if all, _ := em["replace_all"].(bool); all {
			head += " (replace_all: every occurrence)"
		}
		out = append(out, head)
		out = append(out, diffLines(s("old_string"), s("new_string"))...)
		if len(out) > bodyLimit {
			return append(out[:bodyLimit], fmt.Sprintf("… %d more edits", len(edits)-i-1))
		}
	}
	return out
}

// mcpTool splits an MCP tool name, mcp__server__tool, into its parts.
func mcpTool(tool string) (server, name string, ok bool) {
	rest, ok := strings.CutPrefix(tool, "mcp__")
	if !ok {
		return "", "", false
	}
	server, name, ok = strings.Cut(rest, "__")
	if !ok || server == "" || name == "" {
		return "", "", false
	}
	return server, name, true
}

// commandLines is a shell command as the terminal would show it: the
// first line after a prompt, the rest indented under it.
func commandLines(cmd string) []string {
	lines := strings.Split(strings.TrimSpace(cmd), "\n")
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		if i == 0 {
			out = append(out, "$ "+l)
		} else {
			out = append(out, "  "+l)
		}
	}
	return out
}

// jsonLines is a tool's arguments as indented JSON, within the body limit.
func jsonLines(in map[string]any) []string {
	if len(in) == 0 {
		return nil
	}
	b, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return nil
	}
	return limitLines(string(b), bodyLimit)
}

// limitLines keeps the first n lines of a text and says how many follow.
func limitLines(s string, n int) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return lines
	}
	return append(lines[:n], fmt.Sprintf("… %d more lines", len(lines)-n))
}

func describeQuestion(input json.RawMessage) (string, []string) {
	var in struct {
		Questions []struct {
			Question string `json:"question"`
			Options  []struct {
				Label string `json:"label"`
			} `json:"options"`
		} `json:"questions"`
	}
	json.Unmarshal(input, &in)
	if len(in.Questions) == 0 {
		return "Has a question for you", nil
	}
	q := in.Questions[0]
	var opts []string
	for _, o := range q.Options {
		opts = append(opts, o.Label)
	}
	title := q.Question
	if len(in.Questions) > 1 {
		title += fmt.Sprintf(" (+%d more)", len(in.Questions)-1)
	}
	return title, opts
}

func planExcerpt(input json.RawMessage) []string {
	var in struct {
		Plan string `json:"plan"`
	}
	json.Unmarshal(input, &in)
	return limitLines(sanitize.Text(in.Plan), bodyLimit)
}

// diffLines shows an edit as removed lines then added lines, the whole
// change up to the body limit split between the two halves.
func diffLines(oldS, newS string) []string {
	half := bodyLimit / 2
	var out []string
	for _, l := range limitLines(oldS, half) {
		out = append(out, "- "+l)
	}
	for _, l := range limitLines(newS, half) {
		out = append(out, "+ "+l)
	}
	return out
}

// Summarize turns an assistant reply into one plain sentence for a notice:
// no code blocks, no markdown furniture, cut at the first sentence end.
// summaryMax bounds a summary only against a runaway sentence: Den shows
// the summary whole, wrapped, never cut to a line.
const summaryMax = 1200

func Summarize(text string) string {
	text = strings.TrimSpace(sanitize.Text(text))
	if text == "" {
		return "Turn finished."
	}
	// Drop fenced code blocks wholesale.
	var kept []string
	inFence := false
	for _, l := range strings.Split(text, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if strings.HasPrefix(t, "#") {
			continue // headings name sections, they do not say what was done
		}
		t = strings.TrimLeft(t, ">-*• ")
		t = strings.TrimSpace(strings.NewReplacer("**", "", "__", "", "`", "").Replace(t))
		if t != "" {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		return "Turn finished."
	}
	flat := strings.Join(strings.Fields(strings.Join(kept, " ")), " ")
	// First sentence: a terminator followed by a space, or the whole thing.
	for i := 0; i+1 < len(flat); i++ {
		if (flat[i] == '.' || flat[i] == '!' || flat[i] == '?') && flat[i+1] == ' ' && i >= 12 {
			flat = flat[:i+1]
			break
		}
	}
	// Whole, for Den wraps it to the pane; only a runaway sentence is cut,
	// at a word.
	if r := []rune(flat); len(r) > summaryMax {
		cut := string(r[:summaryMax-1])
		if i := strings.LastIndexByte(cut, ' '); i > summaryMax/2 {
			cut = cut[:i]
		}
		flat = strings.TrimSpace(cut) + "…"
	}
	return flat
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

// sameJSON reports whether two JSON texts hold the same value, whatever
// their key order and spacing.
func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}
