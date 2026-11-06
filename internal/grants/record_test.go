package grants

import (
	"reflect"
	"strings"
	"testing"
)

const goodRecord = `{"version":1,"lineage":"0123456789abcdef0123456789abcdef","den_levels":{"routine":4,"destructive":2,"code":1,"desktop":0,"outward":0},"kept":[],"lowered":[{"class":"code","level":0,"at":"2026-10-30T14:02:00+02:00","console":"Drey"}]}`

// A record Den wrote reads back as it was, its time in UTC.
func TestParseRecord(t *testing.T) {
	r, err := ParseRecord([]byte(goodRecord))
	if err != nil {
		t.Fatal(err)
	}
	want := Record{
		Lineage:   "0123456789abcdef0123456789abcdef",
		DenLevels: map[string]int{Routine: 4, Destructive: 2, Code: 1, Desktop: 0, Outward: 0},
		Lowered:   []Lowered{{Class: Code, Level: 0, At: "2026-10-30T12:02:00Z", Console: "Drey"}},
	}
	if !reflect.DeepEqual(r, want) {
		t.Errorf("read %+v", r)
	}
	again, err := ParseRecord(r.Encode())
	// Version 4 writes every class's raise count, 0 here, and an epoch not
	// started ("", which Den's store replaces with a new one).
	want.RaiseN = map[string]int{Routine: 0, Destructive: 0, Code: 0, Desktop: 0, Outward: 0}
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Errorf("Encode does not read back: %v %+v\n%s", err, again, r.Encode())
	}
	if r, err := ParseRecord(Record{}.Encode()); err != nil || r.DenLevels[Routine] != 4 || r.DenLevels[Desktop] != 0 {
		t.Errorf("an empty record encodes the defaults: %v %+v", err, r)
	}
}

// Anything but what Den writes, written plainly, is damaged: never a
// partial read. encoding/json alone would take several of these (keys in
// another case, a key twice, data after the object).
func TestParseRecordRefuses(t *testing.T) {
	rows := map[string]string{
		"a key in another case":          strings.Replace(goodRecord, `"version"`, `"Version"`, 1),
		"a class in another case":        strings.Replace(goodRecord, `"routine":4`, `"ROUTINE":4`, 1),
		"a class twice":                  strings.Replace(goodRecord, `"routine":4`, `"routine":4,"routine":5`, 1),
		"version twice":                  strings.Replace(goodRecord, `"version":1`, `"version":1,"version":1`, 1),
		"data after the object":          goodRecord + `{}`,
		"a field Den does not write":     strings.Replace(goodRecord, `"kept":[]`, `"kept":[],"extra":1`, 1),
		"a missing field":                strings.Replace(goodRecord, `,"kept":[]`, ``, 1),
		"version 4":                      strings.Replace(goodRecord, `"version":1`, `"version":4`, 1),
		"version 3 without set_in_den":   strings.Replace(goodRecord, `"version":1`, `"version":3`, 1),
		"set_in_den in version 1":        strings.Replace(goodRecord, `"kept":[]`, `"kept":[],"set_in_den":{}`, 1),
		"set_in_den of no class":         strings.Replace(strings.Replace(strings.Replace(goodRecord, `"version":1`, `"version":3`, 1), `"kept":[]`, `"kept":[],"set_in_den":{"code":"2026-10-30T12:00:00Z"}`, 1), `"code":"2026`, `"admin":"2026`, 1),
		"set_in_den with no time":        strings.Replace(strings.Replace(strings.Replace(goodRecord, `"version":1`, `"version":3`, 1), `"kept":[]`, `"kept":[],"set_in_den":{"code":"2026-10-30T12:00:00Z"}`, 1), `"2026-10-30T12:00:00Z"`, `"soon"`, 1),
		"set_in_den twice":               strings.Replace(strings.Replace(strings.Replace(goodRecord, `"version":1`, `"version":3`, 1), `"kept":[]`, `"kept":[],"set_in_den":{"code":"2026-10-30T12:00:00Z"}`, 1), `{"code":`, `{"code":"2026-10-30T12:00:00Z","code":`, 1),
		"version 1 with kept":            strings.Replace(goodRecord, `"kept":[]`, `"kept":["close"]`, 1),
		"a kept id Den does not have":    strings.Replace(strings.Replace(goodRecord, `"version":1`, `"version":2`, 1), `"kept":[]`, `"kept":["admin"]`, 1),
		"a kept id twice":                strings.Replace(strings.Replace(goodRecord, `"version":1`, `"version":2`, 1), `"kept":[]`, `"kept":["close","close"]`, 1),
		"a kept id in another case":      strings.Replace(strings.Replace(goodRecord, `"version":1`, `"version":2`, 1), `"kept":[]`, `"kept":["Close"]`, 1),
		"a self-raised kind kept":        strings.Replace(strings.Replace(goodRecord, `"version":1`, `"version":2`, 1), `"kept":[]`, `"kept":["mode_set"]`, 1),
		"version as a string":            strings.Replace(goodRecord, `"version":1`, `"version":"1"`, 1),
		"a class Den does not have":      strings.Replace(goodRecord, `"outward":0`, `"admin":0`, 1),
		"a class missing":                strings.Replace(goodRecord, `,"outward":0`, ``, 1),
		"a level past the class's steps": strings.Replace(goodRecord, `"destructive":2`, `"destructive":3`, 1),
		"level 6":                        strings.Replace(goodRecord, `"routine":4`, `"routine":6`, 1),
		"level below 0":                  strings.Replace(goodRecord, `"routine":4`, `"routine":-1`, 1),
		"level as a string":              strings.Replace(goodRecord, `"routine":4`, `"routine":"2"`, 1),
		"level with a fraction":          strings.Replace(goodRecord, `"routine":4`, `"routine":2.5`, 1),
		"level with an exponent":         strings.Replace(goodRecord, `"routine":4`, `"routine":2e0`, 1),
		"a null level":                   strings.Replace(goodRecord, `"routine":4`, `"routine":null`, 1),
		"null den_levels":                strings.Replace(goodRecord, `{"routine":4,"destructive":2,"code":1,"desktop":0,"outward":0}`, `null`, 1),
		"a lineage in upper case":        strings.Replace(goodRecord, `0123456789abcdef0123456789abcdef`, `0123456789ABCDEF0123456789ABCDEF`, 1),
		"a short lineage":                strings.Replace(goodRecord, `0123456789abcdef0123456789abcdef`, `0123`, 1),
		"kept not empty":                 strings.Replace(goodRecord, `"kept":[]`, `"kept":[{"kind":"Close"}]`, 1),
		"a lowering twice":               strings.Replace(goodRecord, `"lowered":[`, `"lowered":[{"class":"code","level":0,"at":"2026-10-30T12:00:00Z","console":"Drey"},`, 1),
		"a lowering with no time":        strings.Replace(goodRecord, `"at":"2026-10-30T14:02:00+02:00"`, `"at":"yesterday"`, 1),
		"a lowering of no class":         strings.Replace(goodRecord, `"class":"code"`, `"class":"admin"`, 1),
		"a lowering past 5":              strings.Replace(goodRecord, `"class":"code","level":0`, `"class":"code","level":7`, 1),
		"a lowering missing a field":     strings.Replace(goodRecord, `,"console":"Drey"`, ``, 1),
		"a console label of nothing":     strings.Replace(goodRecord, `"console":"Drey"`, `"console":"\u202e\u200f"`, 1),
		"a lowering field in caps":       strings.Replace(goodRecord, `"console":"Drey"`, `"Console":"Drey"`, 1),
		"not an object":                  `[]`,
		"empty":                          ``,
		"cut short":                      goodRecord[:40],
	}
	for name, raw := range rows {
		if r, err := ParseRecord([]byte(raw)); err != ErrDamaged {
			t.Errorf("%s: read %+v, %v", name, r, err)
		}
	}
}

// A label is sanitized as it is read: what is left is what Den shows.
func TestParseRecordSanitizesTheLabel(t *testing.T) {
	r, err := ParseRecord([]byte(strings.Replace(goodRecord, `"console":"Drey"`, `"console":"Dr\u202eey"`, 1)))
	if err != nil || r.Lowered[0].Console != "Drey" {
		t.Errorf("%v %+v", err, r.Lowered)
	}
}

// The start rule: a class above its default reads at its default until the
// start confirm; F3 puts each class at the lowest of its default, its Den
// level and its session; a hold puts every level at 0.
func TestStartRuleAndFold(t *testing.T) {
	st := State{Powers: true, DenLevels: map[string]int{Destructive: 2, Routine: 3}}
	if st.Cap(Destructive) != 1 || st.Cap(Routine) != 3 {
		t.Errorf("before the confirm: destructive %d, routine %d", st.Cap(Destructive), st.Cap(Routine))
	}
	st.Confirmed = true
	if st.Cap(Destructive) != 2 {
		t.Errorf("after the confirm: %d", st.Cap(Destructive))
	}
	st.Sessions = map[string]Session{Routine: {Level: 1}, Code: {Level: 0}}
	want := map[string]int{Routine: 1, Destructive: 1, Code: 0, Desktop: 0, Outward: 0}
	if got := Fold(st); !reflect.DeepEqual(got, want) {
		t.Errorf("Fold %v, want %v", got, want)
	}
	st.Held = true
	for _, c := range Read(st).Classes {
		if c.Level != 0 {
			t.Errorf("held: %s at %d", c.Class, c.Level)
		}
	}
	if g := Read(st); g.Classes[1].DenLevel != 2 {
		t.Errorf("den_level reads %d, the level Den keeps is 2", g.Classes[1].DenLevel)
	}
}

// Version 2 keeps the kept ids, from the closed list, in the list's order
// whatever the file's; a version 1 record reads with kept empty.
func TestParseRecordKept(t *testing.T) {
	v2 := strings.Replace(strings.Replace(goodRecord, `"version":1`, `"version":2`, 1), `"kept":[]`, `"kept":["merge","close"]`, 1)
	r, err := ParseRecord([]byte(v2))
	if err != nil || len(r.Kept) != 2 {
		t.Fatalf("%v %+v", err, r)
	}
	again, err := ParseRecord(r.Encode())
	if err != nil || len(again.Kept) != 2 || again.Kept[0] != KeptClose || again.Kept[1] != KeptMerge {
		t.Errorf("Encode: %v %+v\n%s", err, again, r.Encode())
	}
	if r, err := ParseRecord([]byte(goodRecord)); err != nil || len(r.Kept) != 0 {
		t.Errorf("version 1: %v %+v", err, r.Kept)
	}
}

// Version 3 keeps set_in_den by class, in UTC, and reads back.
func TestParseRecordSetInDen(t *testing.T) {
	v3 := strings.Replace(strings.Replace(goodRecord, `"version":1`, `"version":3`, 1), `"kept":[]`, `"kept":[],"set_in_den":{"code":"2026-10-30T14:00:00+02:00"}`, 1)
	r, err := ParseRecord([]byte(v3))
	if err != nil || r.SetInDen[Code] != "2026-10-30T12:00:00Z" {
		t.Fatalf("%v %+v", err, r.SetInDen)
	}
	again, err := ParseRecord(r.Encode())
	if err != nil || again.SetInDen[Code] != "2026-10-30T12:00:00Z" {
		t.Errorf("Encode: %v %+v", err, again.SetInDen)
	}
}
