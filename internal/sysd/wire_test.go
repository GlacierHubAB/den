package sysd

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"
)

// Only the types Den reads, whole: no 16-bit, floating or fd types, no
// empty struct, a dict entry only in an array with a basic key, and no
// nesting past the limit.
func TestSignatures(t *testing.T) {
	for _, ok := range []string{"s", "as", "a(sasbttttuii)", "a{sv}", "a(ssba(ss))", "v", "(yv)", "a(yv)", "ao", "g"} {
		if _, err := splitTypes(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"h", "n", "q", "d", "()", "a()", "{sv}", "a{vs}", "a{s}", "a{svv}", "(s", "a", "z", "a{(s)v}", strings.Repeat("a", 40) + "s", strings.Repeat("(", 20) + "s" + strings.Repeat(")", 20)} {
		if _, err := splitTypes(bad); err == nil {
			t.Errorf("%q taken", bad)
		}
	}
}

func roundTrip(t *testing.T, m *message) *message {
	t.Helper()
	raw, err := encodeMessage(m)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readMessage(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// What Den writes, it reads back the same.
func TestRoundTrip(t *testing.T) {
	body := []any{
		ConsoleUnit,
		[]any{[]any{"/bin/x", []any{"/bin/x", "-v"}, false, uint64(1), uint64(2), uint64(3), uint64(4), uint32(5), int32(-6), int32(7)}},
		Variant{Sig: "as", Value: []any{"A=1", "B="}},
		[]DictEntry{{Key: "k", Value: Variant{Sig: "b", Value: true}}},
		ObjectPath("/org/x"),
		byte(9),
		int64(-1),
	}
	m := &message{typ: msgReturn, serial: 3, sig: "sa(sasbttttuii)va{sv}oyx", body: body, fields: map[byte]Variant{fieldReplySerial: {Sig: "u", Value: uint32(2)}}}
	got := roundTrip(t, m)
	// A variant's array comes back as []any.
	if !reflect.DeepEqual(got.body, body) || got.replySerial() != 2 {
		t.Errorf("got %#v", got.body)
	}
}

// Every limit is a refusal, never a cut.
func TestLimits(t *testing.T) {
	long := strings.Repeat("x", maxString+1)
	m := &message{typ: msgReturn, serial: 1, sig: "s", body: []any{long}, fields: map[byte]Variant{}}
	raw, err := encodeMessage(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readMessage(bufio.NewReader(bytes.NewReader(raw))); err == nil {
		t.Error("a string past the limit was read")
	}
	items := make([]any, maxItems+1)
	for i := range items {
		items[i] = byte(1)
	}
	m = &message{typ: msgReturn, serial: 1, sig: "ay", body: []any{items}, fields: map[byte]Variant{}}
	raw, _ = encodeMessage(m)
	if _, err := readMessage(bufio.NewReader(bytes.NewReader(raw))); err == nil {
		t.Error("an array past the limit was read")
	}
	// A header that claims more than the limit is refused before it is
	// read.
	hdr := []byte{'l', msgReturn, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(hdr[4:], maxMessage+1)
	if _, err := readMessage(bufio.NewReader(bytes.NewReader(hdr))); err == nil {
		t.Error("a body past the limit was taken")
	}
	// Big-endian, another version, non-zero padding, fds.
	for _, mod := range []func([]byte){
		func(b []byte) { b[0] = 'B' },
		func(b []byte) { b[3] = 2 },
	} {
		raw, _ := encodeMessage(&message{typ: msgReturn, serial: 1, sig: "y", body: []any{byte(1)}, fields: map[byte]Variant{}})
		mod(raw)
		if _, err := readMessage(bufio.NewReader(bytes.NewReader(raw))); err == nil {
			t.Error("a bad header was read")
		}
	}
	raw, _ = encodeMessage(&message{typ: msgReturn, serial: 1, sig: "", fields: map[byte]Variant{fieldUnixFDs: {Sig: "u", Value: uint32(1)}}})
	if _, err := readMessage(bufio.NewReader(bytes.NewReader(raw))); err == nil {
		t.Error("a message with file descriptors was read")
	}
}

// FuzzReadMessage: no input panics or hangs the reader; whatever it reads,
// it writes back to something it reads the same.
func FuzzReadMessage(f *testing.F) {
	for _, m := range []*message{
		{typ: msgReturn, serial: 1, sig: "v", body: []any{Variant{Sig: "as", Value: []any{"A=1"}}}, fields: map[byte]Variant{fieldReplySerial: {Sig: "u", Value: uint32(1)}}},
		{typ: msgReturn, serial: 2, sig: "o", body: []any{ObjectPath("/x")}, fields: map[byte]Variant{}},
		{typ: msgError, serial: 3, sig: "s", body: []any{"no"}, fields: map[byte]Variant{fieldErrorName: {Sig: "s", Value: "org.x.Err"}}},
		{typ: msgReturn, serial: 4, sig: "v", body: []any{Variant{Sig: "a(sasbttttuii)", Value: []any{[]any{"/b", []any{"/b"}, true, uint64(1), uint64(2), uint64(3), uint64(4), uint32(5), int32(6), int32(7)}}}}, fields: map[byte]Variant{}},
	} {
		raw, err := encodeMessage(m)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := readMessage(bufio.NewReader(bytes.NewReader(data)))
		if err != nil {
			return
		}
		raw, err := encodeMessage(m)
		if err != nil {
			return // a variant of another value type than written, say
		}
		again, err := readMessage(bufio.NewReader(bytes.NewReader(raw)))
		if err != nil {
			t.Fatalf("read back what was written: %v", err)
		}
		if !reflect.DeepEqual(again.body, m.body) {
			t.Fatalf("differs: %#v vs %#v", again.body, m.body)
		}
	})
}
