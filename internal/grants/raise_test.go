package grants

import "testing"

// The raise counter decides nothing (P6): two states that differ only in
// the epoch, the counts and the raise times give the same cap for every
// class and the same answer for every class, step, verb and kept id,
// under every combination of the state's switches.
func TestRaiseCounterDecidesNothing(t *testing.T) {
	bools := []bool{false, true}
	for _, powers := range bools {
		for _, locked := range bools {
			for _, god := range bools {
				for _, confirmed := range bools {
					for _, held := range bools {
						base := State{Powers: powers, Locked: locked, God: god, Confirmed: confirmed, Held: held,
							DenLevels: map[string]int{Routine: 2, Destructive: 2, Outward: 3},
							Sessions:  map[string]Session{Code: {Level: 0}},
							Kept:      []string{KeptClose, KeptMerge}}
						raised := base
						raised.RaiseEpoch = "0123456789abcdef0123456789abcdef"
						raised.RaiseN = map[string]int{Routine: 5, Destructive: 1, Code: 9, Desktop: 2, Outward: 7}
						raised.RaisedAt = map[string]string{Routine: "2026-11-06T10:00:00Z", Outward: "2026-11-06T11:00:00Z"}
						for _, id := range Classes() {
							if base.Cap(id) != raised.Cap(id) {
								t.Errorf("%+v: %s caps %d, with a raise counter %d", base, id, base.Cap(id), raised.Cap(id))
							}
						}
						for _, id := range Classes() {
							for step := 0; step <= Steps; step++ {
								for _, verb := range []string{"allow", "deny", "always"} {
									for _, kept := range append([]string{""}, KeptAll()...) {
										for _, gt := range bools {
											a := Answer{Kept: kept, Class: id, Step: step, Verb: verb, GodTTY: gt}
											if x, y := Allow(base, a), Allow(raised, a); x != y {
												t.Errorf("%+v %+v: %q, with a raise counter %q", base, a, x, y)
											}
										}
									}
								}
							}
						}
						// The read differs only in the counter's own fields.
						g, h := Read(base), Read(raised)
						if g.RaiseEpoch == h.RaiseEpoch {
							t.Error("the epoch is not in the read")
						}
						for i := range g.Classes {
							a, b := g.Classes[i], h.Classes[i]
							b.RaiseN, b.RaisedAt = a.RaiseN, a.RaisedAt
							if a.Level != b.Level || a.DenLevel != b.DenLevel {
								t.Errorf("%s: level %d/%d, den level %d/%d", a.Class, a.Level, b.Level, a.DenLevel, b.DenLevel)
							}
						}
					}
				}
			}
		}
	}
}

// A version 4 record carries the counter and reads it back; one from
// before has none, and an epoch that is not one is damaged.
func TestParseRecordRaiseCounter(t *testing.T) {
	r := Record{DenLevels: map[string]int{Outward: 2}, RaiseEpoch: "0123456789abcdef0123456789abcdef",
		RaiseN: map[string]int{Outward: 3}, RaisedAt: map[string]string{Outward: "2026-11-06T10:00:00Z"}}
	back, err := ParseRecord(r.Encode())
	if err != nil || back.RaiseEpoch != r.RaiseEpoch || back.RaiseN[Outward] != 3 || back.RaiseN[Routine] != 0 || back.RaisedAt[Outward] != "2026-11-06T10:00:00Z" {
		t.Fatalf("%v %+v", err, back)
	}
	if old, err := ParseRecord([]byte(goodRecord)); err != nil || old.RaiseEpoch != "" || old.RaiseN != nil {
		t.Errorf("a version 1 record: %v %+v", err, old)
	}
	good := string(r.Encode())
	for name, bad := range map[string]string{
		"an epoch not hex":       replace(good, `"raise_epoch":"0123456789abcdef0123456789abcdef"`, `"raise_epoch":"0123456789ABCDEF0123456789ABCDEF"`),
		"an epoch too short":     replace(good, `"raise_epoch":"0123456789abcdef0123456789abcdef"`, `"raise_epoch":"0123"`),
		"a count below 0":        replace(good, `"outward":3`, `"outward":-1`),
		"a count not plain":      replace(good, `"outward":3`, `"outward":"3"`),
		"a count missing":        replace(good, `"code":0,`, ``),
		"a time that is not one": replace(good, `"raised_at":{"outward":"2026-11-06T10:00:00Z"}`, `"raised_at":{"outward":"soon"}`),
		"an unknown class":       replace(good, `"raised_at":{"outward"`, `"raised_at":{"nosuch"`),
		"the counter in v3":      replace(good, `"version":4`, `"version":3`),
	} {
		if bad == good {
			t.Fatalf("%s: the replacement did not apply", name)
		}
		if _, err := ParseRecord([]byte(bad)); err == nil {
			t.Errorf("%s: read", name)
		}
	}
}

func replace(s, old, new string) string {
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}
	return s
}
