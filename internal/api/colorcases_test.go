package api

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/GlacierHubAB/den/internal/api/convcases"
	"github.com/GlacierHubAB/den/internal/fleet"
)

// consoleColor is a console's reading of `color`: "#" and six lower-case
// hex digits, else none.
var consoleColor = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// Den sends each fleet entry of the colour table with the `color` the
// table gives, and leaves the field out where the table has none; a
// console's reading takes exactly that.
func TestColorTable(t *testing.T) {
	for _, c := range convcases.ColorCases {
		s := Snapshot{Names: map[string]string{}, Colors: map[string]string{}}
		for _, a := range c.Agents {
			s.Agents = append(s.Agents, fleet.Agent{Name: a.Name, Status: fleet.Idle, Session: discoverSession(a.ID, 0), Host: a.Host})
			s.Names[a.ID] = a.Name
			s.Colors[a.ID] = a.Drawn
		}
		v := Project(s)
		if len(v.Fleet.Agents) != len(c.Agents) {
			t.Fatalf("%s: %d fleet agents, want %d", c.Name, len(v.Fleet.Agents), len(c.Agents))
		}
		for i, a := range c.Agents {
			raw, err := json.Marshal(v.Fleet.Agents[i])
			if err != nil {
				t.Fatal(err)
			}
			var entry map[string]any
			json.Unmarshal(raw, &entry)
			got, has := entry["color"].(string)
			if entry["id"] != a.ID || got != a.Color || has != (a.Color != "") {
				t.Errorf("%s: %s sent %s, want color %q", c.Name, a.ID, raw, a.Color)
			}
			if read := consoleColor.FindString(got); read != a.Color {
				t.Errorf("%s: %s: a console reads %q, the table %q", c.Name, a.ID, read, a.Color)
			}
		}
	}
}
