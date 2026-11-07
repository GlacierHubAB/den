package api

import (
	"encoding/json"
	"testing"
	"time"
)

// setupCard is Den's setup card as internal/ui hands it over: den_only,
// listed with its paste-safe body, and never one the console answers
// (internal/ui pins that the setup kind always comes with DockAnswers
// false, and TTYOnly while God mode is on).
func setupCard(ttyOnly bool) Card {
	return Card{ID: "s-1", Kind: "setup", Agent: "Den", Tool: "setup", Title: "Install Den's hooks and mods into Claude Code?",
		Class: "den_only", Body: "Den's hooks are not in ~/.claude/settings.json: nothing reaches Den's inbox.",
		Verbs: []string{"allow", "deny"}, DockAnswers: false, TTYOnly: ttyOnly}
}

// den-sec-review on P10 PR 2: a console with its powers, answering the
// setup card with the right hash and a fresh nonce, is refused for the
// card's kind alone: refused_from_dock with God mode off, tty_only with it
// on, for every verb. Nothing reaches Den, and the card is listed as
// needing Den's screen.
func TestSetupCardRefusedFromTheConsole(t *testing.T) {
	for _, god := range []bool{false, true} {
		h, c := provedConsole(t)
		h.s.Publish(Project(Snapshot{Taken: time.Now(), Cards: []Card{setupCard(god)}}))
		ev := c.next()
		var ce CardEvent
		if ev.Event != EventCard || json.Unmarshal(ev.Data, &ce) != nil || ce.ID != "s-1" || !ce.NeedsDenScreen {
			t.Fatalf("god %v: card event %+v", god, ev)
		}
		want := CodeRefusedFromDock
		if god {
			want = CodeTTYOnly
		}
		for _, verb := range []string{"allow", "deny", "always"} {
			g := getCard(t, c, "s-1")
			if g.DockAnswers || g.Class != "den_only" || g.Step != 0 {
				t.Errorf("god %v: listed %+v", god, g.Card)
			}
			if l := c.replyTo(answerCard(c, g, verb)); code(l) != want {
				t.Errorf("god %v, %s with the right hash and nonce: %+v, want %s", god, verb, l, want)
			}
		}
		select {
		case a := <-h.s.Acts():
			if a.CardAnswer != nil {
				t.Errorf("god %v: an answer reached Den: %+v", god, a.CardAnswer)
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
}
