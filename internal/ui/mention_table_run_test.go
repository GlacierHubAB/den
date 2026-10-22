package ui

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/GlacierHubAB/den/internal/api/denconformance"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
)

// TestMentionTable runs the mention table Den and Drey's dock share
// (denconformance.MentionCases) through Den's colouring, every hit, and its
// routing, the first hit when the text starts with it. A host after the
// name ("@Lemur@host") is routing's own and not the table's: the chip ends
// at the "@", and routing then looks on that host, so those cases are left
// to TestMentionEndsAtPunctuation.
func TestMentionTable(t *testing.T) {
	for i, c := range denconformance.MentionCases {
		var agents []fleet.Agent
		names := map[string]string{}
		for j, n := range c.Names {
			sid := "s" + strconv.Itoa(j)
			names[sid] = n
			agents = append(agents, fleet.Agent{Name: n, Status: fleet.Working, Session: discover.Session{SessionID: sid, Alive: true}})
		}
		ms := buildMentions(agents, func(fleet.Agent) bool { return false })
		var got []denconformance.Hit
		pos := 0
		for _, sp := range ms.apply([]mdSpan{{text: c.Text}}) {
			n := utf8.RuneCountInString(sp.text)
			if sp.a.mention != "" {
				got = append(got, denconformance.Hit{Name: names[sp.a.mention], Start: pos, End: pos + n})
			}
			pos += n
		}
		if fmt.Sprint(got) != fmt.Sprint(c.Want) && !(len(got) == 0 && len(c.Want) == 0) {
			t.Errorf("%d colour %q names %q: got %v want %v", i, c.Text, c.Names, got, c.Want)
		}
		if !strings.HasPrefix(c.Text, "@") {
			continue
		}
		want := ""
		if len(c.Want) > 0 && c.Want[0].Start == 0 {
			want = c.Want[0].Name
			if rs := []rune(c.Text); c.Want[0].End < len(rs) && rs[c.Want[0].End] == '@' {
				continue // a host follows: routing's own
			}
		}
		r := ""
		if mt := resolveMention(c.Text, agents); mt.target != nil {
			r = mt.target.Name
		}
		if r != want {
			t.Errorf("%d route %q names %q: got %q want %q", i, c.Text, c.Names, r, want)
		}
	}
}
