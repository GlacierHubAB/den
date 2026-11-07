package grants

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/GlacierHubAB/den/internal/sanitize"
)

// RecordVersion is the version of the stored record Den writes. Version 1,
// from before kept, version 2, from before set_in_den, and version 3, from
// before the raise counter, are still read (with no raise counter: Den
// starts one).
const RecordVersion = 4

// Record is what Den keeps of the levels across its restarts, in
// permissions.json: the Den level of every class, the pairing lineage they
// belong to, the kept ids (never more than the upgrade granted: a file only
// ever lowers them), and this session's lowerings, a notice only (Den never
// reads a level back from it).
type Record struct {
	Lineage   string
	DenLevels map[string]int
	Kept      []string
	Lowered   []Lowered
	// SetInDen is when the human last set each class in Den's window, by
	// class: shown only, never read to decide a level.
	SetInDen map[string]string
	// The raise counter (State's): its epoch, "" in a record from before
	// version 4; each class's count of raises; the last raise's time.
	RaiseEpoch string
	RaiseN     map[string]int
	RaisedAt   map[string]string
}

// Lowered is one of a session's lowerings, as the record keeps it.
type Lowered struct {
	Class   string `json:"class"`
	Level   int    `json:"level"`
	At      string `json:"at"`
	Console string `json:"console"`
}

// stored is the record as written, keys in a fixed order.
type stored struct {
	Version    int               `json:"version"`
	Lineage    string            `json:"lineage"`
	DenLevels  map[string]int    `json:"den_levels"`
	Kept       []string          `json:"kept"`
	Lowered    []Lowered         `json:"lowered"`
	SetInDen   map[string]string `json:"set_in_den"`
	RaiseEpoch string            `json:"raise_epoch"`
	RaiseN     map[string]int    `json:"raise_n"`
	RaisedAt   map[string]string `json:"raised_at"`
}

// Encode is r as Den writes it: every class's Den level (its default when
// r has none) and raise count, the lowerings in the table's order.
func (r Record) Encode() []byte {
	out := stored{Version: RecordVersion, Lineage: r.Lineage, DenLevels: map[string]int{}, Kept: []string{}, Lowered: []Lowered{}, SetInDen: map[string]string{},
		RaiseEpoch: r.RaiseEpoch, RaiseN: map[string]int{}, RaisedAt: map[string]string{}}
	for _, c := range table {
		if at, ok := r.SetInDen[c.id]; ok {
			out.SetInDen[c.id] = at
		}
		out.RaiseN[c.id] = r.RaiseN[c.id]
		if at, ok := r.RaisedAt[c.id]; ok {
			out.RaisedAt[c.id] = at
		}
	}
	st := State{DenLevels: r.DenLevels, Kept: r.Kept}
	for _, k := range keptTable {
		if st.holds(k.id) {
			out.Kept = append(out.Kept, k.id)
		}
	}
	for _, c := range table {
		out.DenLevels[c.id] = st.denLevel(c)
		for _, l := range r.Lowered {
			if l.Class == c.id {
				out.Lowered = append(out.Lowered, l)
			}
		}
	}
	b, _ := json.Marshal(out)
	return b
}

// ErrDamaged is a record Den does not read: anything outside the closed
// sets, written in any way but plainly.
var ErrDamaged = errors.New("damaged")

// ParseRecord reads a stored record strictly: exactly the keys Den writes,
// all of them, in their exact case, none twice at any level, and nothing
// after the object; version 2, or 1 from before kept; every class, its
// level written as a plain integer from 0 to the class's last step; a
// lineage of 32 lowercase hex digits or none; kept ids from the closed
// list, none twice (empty in version 1); at most one lowering per class,
// with a time Den can read and a console label that is not empty once
// sanitized; in version 3, set_in_den times by known class, shown only;
// in version 4, the raise counter: an epoch of 32 lowercase hex digits,
// every class's raise count as a plain integer from 0, and raised_at times
// by known class. Times read as RFC 3339 and are kept in UTC, as Den writes them.
// Anything else is ErrDamaged, never a partial read.
func ParseRecord(raw []byte) (Record, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	top, err := object(dec, "version", "lineage", "den_levels", "kept", "lowered", "set_in_den", "raise_epoch", "raise_n", "raised_at")
	if err != nil {
		return Record{}, ErrDamaged
	}
	if _, err := dec.Token(); err != io.EOF {
		return Record{}, ErrDamaged // something after the object
	}
	version, ok := PlainInt(top["version"])
	if !ok || version < 1 || version > RecordVersion {
		return Record{}, ErrDamaged
	}
	keys := 5 // Den writes every key of its version
	if version >= 3 {
		keys = 6
	}
	if version >= 4 {
		keys = 9
	} else if top["raise_epoch"] != nil || top["raise_n"] != nil || top["raised_at"] != nil {
		return Record{}, ErrDamaged
	}
	if len(top) != keys {
		return Record{}, ErrDamaged
	}
	var r Record
	if raw, ok := top["lineage"]; ok {
		if json.Unmarshal(raw, &r.Lineage) != nil || r.Lineage != "" && !lineageOK(r.Lineage) {
			return Record{}, ErrDamaged
		}
	}
	if raw, ok := top["den_levels"]; ok {
		levels, err := object(decoder(raw), Classes()...)
		if err != nil || len(levels) != len(table) {
			return Record{}, ErrDamaged
		}
		r.DenLevels = map[string]int{}
		for id, raw := range levels {
			l, ok := PlainInt(raw)
			if !ok || l < 0 || l > int64(steps(id)) {
				return Record{}, ErrDamaged
			}
			r.DenLevels[id] = int(l)
		}
	}
	if raw, ok := top["kept"]; ok {
		kept, err := array(decoder(raw))
		if err != nil || version == 1 && len(kept) != 0 {
			return Record{}, ErrDamaged
		}
		seen := map[string]bool{}
		for _, raw := range kept {
			var id string
			if json.Unmarshal(raw, &id) != nil || KeptLabel(id) == "" || seen[id] {
				return Record{}, ErrDamaged
			}
			seen[id] = true
			r.Kept = append(r.Kept, id)
		}
	}
	if raw, ok := top["set_in_den"]; ok {
		if r.SetInDen, err = times(raw); err != nil {
			return Record{}, ErrDamaged
		}
	}
	if raw, ok := top["raised_at"]; ok {
		if r.RaisedAt, err = times(raw); err != nil {
			return Record{}, ErrDamaged
		}
	}
	if raw, ok := top["raise_epoch"]; ok {
		// "" is a counter not started: it starts again from 0, as one from
		// before version 4 does.
		if json.Unmarshal(raw, &r.RaiseEpoch) != nil || r.RaiseEpoch != "" && !lineageOK(r.RaiseEpoch) {
			return Record{}, ErrDamaged
		}
	}
	if raw, ok := top["raise_n"]; ok {
		counts, err := object(decoder(raw), Classes()...)
		if err != nil || len(counts) != len(table) {
			return Record{}, ErrDamaged
		}
		r.RaiseN = map[string]int{}
		for id, raw := range counts {
			n, ok := PlainInt(raw)
			if !ok || n < 0 || n > 1<<31 {
				return Record{}, ErrDamaged
			}
			r.RaiseN[id] = int(n)
		}
	}
	if raw, ok := top["lowered"]; ok {
		items, err := array(decoder(raw))
		if err != nil {
			return Record{}, ErrDamaged
		}
		seen := map[string]bool{}
		for _, raw := range items {
			l, err := lowered(raw)
			if err != nil || seen[l.Class] {
				return Record{}, ErrDamaged
			}
			seen[l.Class] = true
			r.Lowered = append(r.Lowered, l)
		}
	}
	return r, nil
}

// times reads an object of RFC 3339 times by known class, in UTC;
// nil for none.
func times(raw json.RawMessage) (map[string]string, error) {
	at, err := object(decoder(raw), Classes()...)
	if err != nil {
		return nil, ErrDamaged
	}
	out := map[string]string{}
	for id, raw := range at {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil, ErrDamaged
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return nil, ErrDamaged
		}
		out[id] = t.UTC().Format(time.RFC3339) // as Den writes them: so they read back unchanged
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// lowered reads one lowering of a stored record.
func lowered(raw json.RawMessage) (Lowered, error) {
	f, err := object(decoder(raw), "class", "level", "at", "console")
	if err != nil || len(f) != 4 {
		return Lowered{}, ErrDamaged
	}
	var l Lowered
	level, ok := PlainInt(f["level"])
	if json.Unmarshal(f["class"], &l.Class) != nil || !ok || !Valid(l.Class, int(level)) {
		return Lowered{}, ErrDamaged
	}
	l.Level = int(level)
	var at, console string
	if json.Unmarshal(f["at"], &at) != nil || json.Unmarshal(f["console"], &console) != nil {
		return Lowered{}, ErrDamaged
	}
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return Lowered{}, ErrDamaged
	}
	l.At = t.UTC().Format(time.RFC3339)
	if l.Console = sanitize.Label(console, 40); l.Console == "" {
		return Lowered{}, ErrDamaged
	}
	return l, nil
}

// LastStep is class id's highest level (its last step), 0 for no class.
func LastStep(id string) int { return steps(id) }

// steps is how many steps class id has: its highest level.
func steps(id string) int {
	for _, c := range table {
		if c.id == id {
			return len(c.steps)
		}
	}
	return 0
}

func decoder(raw json.RawMessage) *json.Decoder {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	return d
}

// object reads one JSON object from dec, keyed exactly by one of keys and
// none twice, as each key's raw value.
func object(dec *json.Decoder, keys ...string) (map[string]json.RawMessage, error) {
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, ErrDamaged
	}
	out := map[string]json.RawMessage{}
	for dec.More() {
		t, err := dec.Token()
		k, ok := t.(string)
		if err != nil || !ok || !oneOf(k, keys) {
			return nil, ErrDamaged
		}
		if _, twice := out[k]; twice {
			return nil, ErrDamaged
		}
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			return nil, ErrDamaged
		}
		out[k] = v
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return nil, ErrDamaged
	}
	return out, nil
}

// array reads one JSON array from dec, as each item's raw value.
func array(dec *json.Decoder) ([]json.RawMessage, error) {
	if t, err := dec.Token(); err != nil || t != json.Delim('[') {
		return nil, ErrDamaged
	}
	var out []json.RawMessage
	for dec.More() {
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			return nil, ErrDamaged
		}
		out = append(out, v)
	}
	if t, err := dec.Token(); err != nil || t != json.Delim(']') {
		return nil, ErrDamaged
	}
	return out, nil
}

func oneOf(k string, keys []string) bool {
	for _, x := range keys {
		if k == x {
			return true
		}
	}
	return false
}

func lineageOK(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// PlainInt is raw as an integer, if it is one written plainly: no string,
// no fraction, no exponent, no null.
func PlainInt(raw json.RawMessage) (int64, bool) {
	s := string(bytes.TrimSpace(raw))
	if s == "" || s == "-" {
		return 0, false
	}
	for i, r := range s {
		if !(r >= '0' && r <= '9' || i == 0 && r == '-') {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}
