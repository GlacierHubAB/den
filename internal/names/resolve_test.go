package names

import (
	"errors"
	"testing"
)

var fleet = []Candidate{
	{ID: "aaaa1111", Peer: "den-api", Name: "Mossy Lemur", PID: 101},
	{ID: "bbbb2222", Peer: "den-ui", Name: "Mossy Lemur", PID: 102},
	{ID: "cccc3333", Peer: "docs", Name: "technical-writer", Alias: "Pebble Quokka", PID: 103},
	{ID: "dddd4444", Peer: "builder", Name: "Amber Otter", Host: "devbox", PID: 104},
	{ID: "eeee5555", Peer: "amber-otter", Name: "Quiet Heron", PID: 105},
}

func TestResolve(t *testing.T) {
	for _, c := range []struct {
		q    string
		o    Opts
		want string // the id, "" for an error
	}{
		{"den-api", Opts{}, "aaaa1111"},          // a peer handle is exact
		{"@den-ui", Opts{}, "bbbb2222"},          // with an @
		{"technical-writer", Opts{}, "cccc3333"}, // the name
		{"TECHNICAL-WRITER", Opts{}, "cccc3333"}, // in any case
		{"pebble quokka", Opts{}, "cccc3333"},    // the alias
		{"Amber Otter@devbox", Opts{}, "dddd4444"},
		{"builder@devbox", Opts{}, "dddd4444"},
		{"amber-otter", Opts{}, "eeee5555"}, // a peer handle beats a name elsewhere
		{"Mossy Lemur", Opts{}, ""},         // two share it: refused
		{"Mossy Lemur@local", Opts{}, ""},
		{"tech", Opts{}, ""}, // no prefix without Opts.Prefix
		{"tech", Opts{Prefix: true}, "cccc3333"},
		{"pebble", Opts{Prefix: true}, "cccc3333"},
		{"cccc", Opts{}, ""},
		{"cccc", Opts{IDs: true}, "cccc3333"},
		{"103", Opts{IDs: true}, "cccc3333"},
		{"Amber Otter", Opts{}, ""}, // only elsewhere: needs the host
		{"nobody", Opts{Prefix: true, IDs: true}, ""},
		{"@", Opts{}, ""},
		{"x@", Opts{}, ""},
	} {
		got, err := Resolve(c.q, fleet, c.o)
		if got.ID != c.want || (c.want == "") != (err != nil) {
			t.Errorf("Resolve(%q, %+v) = %q, %v; want %q", c.q, c.o, got.ID, err, c.want)
		}
	}
}

func TestResolveSaysWhy(t *testing.T) {
	var amb *Ambiguous
	if _, err := Resolve("Mossy Lemur", fleet, Opts{}); !errors.As(err, &amb) || len(amb.Candidates) != 2 {
		t.Fatalf("ambiguous: %v", err)
	}
	if got := Labels(amb.Candidates); got[0] != "Mossy Lemur (den-api)" || got[1] != "Mossy Lemur (den-ui)" {
		t.Errorf("labels %q", got)
	}
	var nh *NeedsHost
	if _, err := Resolve("Amber Otter", fleet, Opts{}); !errors.As(err, &nh) || len(nh.Candidates) != 1 || Labels(nh.Candidates)[0] != "Amber Otter@devbox" {
		t.Fatalf("needs host: %v", err)
	}
	if _, err := Resolve("nobody", fleet, Opts{}); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("no match: %v", err)
	}
}
