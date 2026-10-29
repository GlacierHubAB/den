package api

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/api/convcases"
)

// closeCard is the close card the settled tables are about; started says
// whether its answer started its action, and by whom.
func closeCard(startedBy string) Card {
	return Card{ID: "c1", Kind: "close", Agent: "Mossy Lemur", Title: "Close Mossy Lemur?", Body: "Mossy Lemur · idle", Class: "destructive", Step: 1,
		Verbs: []string{"allow", "deny"}, DockAnswers: true, Started: startedBy != "", StartedBy: startedBy}
}

// A started card is refused before any check of hash or nonce: with the
// card's own hash and a fresh nonce from card.get, card.answer is
// not_found, the nonce is spent, and no answer reaches Den.
func TestAnswerOnAStartedCardIsNotFound(t *testing.T) {
	h, c := provedConsole(t)
	h.s.Publish(Project(Snapshot{Taken: time.Now(), Cards: []Card{closeCard("console")}}))
	c.next() // its new event
	g := getCard(t, c, "c1")
	if !g.Started || g.StartedBy != "console" || g.DockAnswers || len(g.Verbs) != 0 || g.Hash != convcases.StartedHash {
		t.Fatalf("card.get on a started card: %+v", g.Card)
	}
	for _, verb := range []string{"allow", "deny"} {
		if l := c.call("card.answer", map[string]string{"id": g.ID, "text_sha256": g.Hash, "nonce": g.Nonce, "verb": verb}); code(l) != CodeNotFound {
			t.Errorf("%s on a started card: %+v", verb, l)
		}
	}
	select {
	case a := <-h.s.Acts():
		if a.CardAnswer != nil {
			t.Errorf("an answer reached Den: %+v", a.CardAnswer)
		}
	case <-time.After(50 * time.Millisecond):
	}
	// The nonce was spent by the first refusal.
	h.s.Publish(Project(Snapshot{Taken: time.Now(), Cards: []Card{closeCard("")}}))
	if l := c.call("card.answer", map[string]string{"id": g.ID, "text_sha256": g.Hash, "nonce": g.Nonce, "verb": "allow"}); code(l) != CodeNonceExpired {
		t.Errorf("the refused nonce again: %+v", l)
	}
}

// A console sees a started card's whole life: listed, then changed with
// started and by and the same hash, then settled once with by and its
// outcome; lost too.
func TestStartedCardEvents(t *testing.T) {
	for _, outcome := range []string{"done", "failed", "lost"} {
		h, c := provedConsole(t)
		h.s.Publish(Project(Snapshot{Taken: time.Now(), Cards: []Card{closeCard("")}}))
		var evs []CardEvent
		read := func() {
			var ev CardEvent
			if e := c.next(); e.Event != EventCard || json.Unmarshal(e.Data, &ev) != nil {
				t.Fatalf("%s: %+v", outcome, e)
			}
			evs = append(evs, ev)
		}
		read()
		h.s.Publish(Project(Snapshot{Taken: time.Now(), Cards: []Card{closeCard("console")}}))
		read()
		// Nothing more while it runs.
		h.s.Publish(Project(Snapshot{Taken: time.Now(), Cards: []Card{closeCard("console")}}))
		h.s.Publish(Project(Snapshot{Taken: time.Now(), Settled: []Settled{{ID: "c1", Verb: "allow", By: "console", Outcome: outcome}}}))
		read()
		want := []CardEvent{
			{ID: "c1", Kind: "close", Agent: "Mossy Lemur", Change: "new", Hash: convcases.StartedHash},
			{ID: "c1", Kind: "close", Agent: "Mossy Lemur", Change: "changed", Hash: convcases.StartedHash, Started: true, By: "console"},
			{ID: "c1", Kind: "close", Agent: "Mossy Lemur", Change: "settled", Result: "allowed", By: "console", Outcome: outcome},
		}
		if !reflect.DeepEqual(evs, want) {
			t.Errorf("%s: events\n%+v\nwant\n%+v", outcome, evs, want)
		}
	}
}

// by and outcome take only their closed sets: anything else Den's side
// handed over is dropped, never sent.
func TestStartedFieldsAreClosedSets(t *testing.T) {
	c := projectCard(Card{ID: "c1", Kind: "close", Agent: "Mossy Lemur", Title: "t", Verbs: []string{"allow"}, DockAnswers: true, Started: true, StartedBy: "app-host"})
	if !c.Started || c.StartedBy != "" || c.DockAnswers || len(c.Verbs) != 0 {
		t.Errorf("projected %+v", c)
	}
	v := Project(Snapshot{Taken: time.Now(), Settled: []Settled{{ID: "c1", Verb: "allow", By: "console", Outcome: "it failed: EPERM"}}})
	if st := v.Settled["c1"]; st.Outcome != "" {
		t.Errorf("settled %+v", st)
	}
}

// Den sends the settled table's rows exactly, and each row's reading
// follows the rules: running on a started changed event or card, final on
// settled, an unknown outcome read as lost.
func TestSettledTable(t *testing.T) {
	jsonOf := func(v any) string {
		raw, _ := json.Marshal(v)
		return string(raw)
	}
	perm := projectCard(Card{ID: "p1", Kind: "permission", Agent: "Mossy Lemur", Title: "Bash"})
	started := func(by string) CardEvent { return cardEvent(projectCard(closeCard(by)), "changed", 1) }
	settled := func(by, outcome string) CardEvent {
		return settledEvent(projectCard(closeCard("")), Settled{ID: "c1", Verb: "allow", By: by, Outcome: outcome}, true)
	}
	sends := map[string]string{
		"a started card's changed event":        jsonOf(started("console")),
		"a card Den's own answer started":       jsonOf(started("den")),
		"a started card in the snapshot":        jsonOf(projectCard(closeCard("console"))),
		"an answer on a started card":           jsonOf(CodeNotFound),
		"its action was done":                   jsonOf(settled("console", "done")),
		"its action failed":                     jsonOf(settled("console", "failed")),
		"Den lost track of its action":          jsonOf(settled("den", "lost")),
		"a permission card's settled event":     jsonOf(settledEvent(perm, Settled{ID: "p1", Verb: "allow", By: "console"}, true)),
		"a card that settled without an answer": jsonOf(settledEvent(perm, Settled{}, false)),
	}
	sent := 0
	for _, c := range convcases.SettledCases {
		var want any
		if err := json.Unmarshal([]byte(c.JSON), &want); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		if m, ok := want.(map[string]any); ok && c.Shape != "refusal" {
			change, _ := m["change"].(string)
			isStarted, _ := m["started"].(bool)
			result, _ := m["result"].(string)
			by, _ := m["by"].(string)
			outcome, _ := m["outcome"].(string)
			if outcome != "" && !outcomes[outcome] {
				outcome = "lost"
			}
			running := isStarted && (c.Shape == "card" || change == "changed")
			if c.Running != running || c.Final != (change == "settled") || c.Result != result || c.By != by || c.Outcome != outcome {
				t.Errorf("%s: the table reads %+v", c.Name, c)
			}
		}
		raw, ok := sends[c.Name]
		if ok != c.DenSends {
			t.Errorf("%s: DenSends %v, but Den's sends say %v", c.Name, c.DenSends, ok)
		}
		if !ok {
			continue
		}
		sent++
		var got any
		json.Unmarshal([]byte(raw), &got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: Den sends\n%s\nthe table says\n%s", c.Name, raw, c.JSON)
		}
	}
	if sent != len(sends) {
		t.Errorf("%d of %d sends matched a row", sent, len(sends))
	}
}

// Den sends the card-end table's lines exactly, from the agents' names
// only, and refuses an ending outside its set or on another origin.
func TestCardEndTable(t *testing.T) {
	h, con := provedConsole(t)
	cur, _ := h.s.CurrentConsole()
	if !cur.Powers {
		h.s.ConsolePowersAnswered(cur.ID, true)
	}
	h.s.SetMirror(cur.ID, true)
	end := func(card, action string, done, failed []string, lost bool) Converse {
		return Converse{Origin: OriginCard, Speaker: SpeakerDen, Card: card, Text: "ignored: " + action, End: &CardEnd{Action: action, Done: done, Failed: failed, Lost: lost}}
	}
	sends := map[string]Converse{
		"a close that was done":      end("c1", "close", []string{"Mossy Lemur"}, nil, false),
		"a close that partly failed": end("c1", "close", []string{"Mossy Lemur"}, []string{"Quiet Panda"}, false),
		"a reopen that failed":       end("c2", "reopen", nil, []string{"Comet Newt"}, false),
		"a close Den lost track of":  end("c1", "close", []string{"Mossy Lemur"}, nil, true),
	}
	refuse := map[string]Converse{
		"an ending outside the set":       {Origin: OriginCard, Speaker: SpeakerDen, Card: "c1", Ending: "later", End: &CardEnd{Action: "close", Done: []string{"Mossy Lemur"}}},
		"a card ending on another origin": {Origin: OriginReport, Speaker: SpeakerDen, Agent: "Mossy Lemur", AgentID: "s1", Ending: EndingFailed, Text: "x"},
	}
	for _, c := range convcases.CardEndCases {
		var want map[string]any
		if err := json.Unmarshal([]byte(c.Entry), &want); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		ending, _ := want["ending"].(string)
		origin, _ := want["origin"].(string)
		readsRefused := origin == OriginCard && ending != "" && ending != EndingFailed && ending != EndingLost ||
			origin != OriginCard && (ending == EndingFailed || ending == EndingLost)
		if c.Refused != readsRefused || !c.Refused && (c.Ending != ending || c.Card != want["card"]) {
			t.Errorf("%s: the table reads %+v", c.Name, c)
		}
		if c.Refused {
			in, ok := refuse[c.Name]
			if !ok {
				t.Errorf("%s: no refusal tried", c.Name)
			} else if h.s.Converse(in) {
				t.Errorf("%s: Den sent it", c.Name)
			}
			continue
		}
		in, ok := sends[c.Name]
		if !ok {
			t.Fatalf("%s: Den has no send for it", c.Name)
		}
		if !h.s.Converse(in) {
			t.Fatalf("%s: refused", c.Name)
		}
		e := nextConversation(t, con)
		raw, _ := json.Marshal(e)
		var got map[string]any
		json.Unmarshal(raw, &got)
		delete(got, "id")
		delete(got, "at")
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: Den sends\n%s\nthe table says\n%s", c.Name, raw, c.Entry)
		}
	}
	// An origin card entry with no end, or an action Den has no words
	// for, is refused.
	for _, in := range []Converse{
		{Origin: OriginCard, Speaker: SpeakerDen, Card: "c1", Text: "Closed everything."},
		end("c1", "install", []string{"Mossy Lemur"}, nil, false),
		end("", "close", []string{"Mossy Lemur"}, nil, false),
		{Origin: OriginCard, Speaker: SpeakerOrchestrator, Card: "c1", End: &CardEnd{Action: "close", Done: []string{"Mossy Lemur"}}},
	} {
		if h.s.Converse(in) {
			t.Errorf("sent %+v", in)
		}
	}
}
