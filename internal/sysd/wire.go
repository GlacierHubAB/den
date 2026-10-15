// Package sysd reads what Den needs to know about a systemd user service
// from the user's own systemd manager: which process is the service's main
// one, how the manager will run it, and with what. It speaks D-Bus to the
// manager's private socket ($XDG_RUNTIME_DIR/systemd/private), peer to
// peer, so the process at the other end of the socket is the manager
// itself, not a bus broker: Den checks it with SO_PEERCRED.
//
// It only reads: GetUnit, the properties of a unit, and the manager's own
// environment. The wire code here is the small part of D-Bus those calls
// need, both ways, so a test can serve a fake manager with it.
package sysd

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

// The message types.
const (
	msgCall   = 1
	msgReturn = 2
	msgError  = 3
	msgSignal = 4
)

// The header fields.
const (
	fieldPath        = 1
	fieldInterface   = 2
	fieldMember      = 3
	fieldErrorName   = 4
	fieldReplySerial = 5
	fieldDestination = 6
	fieldSender      = 7
	fieldSignature   = 8
	fieldUnixFDs     = 9
)

// The limits Den holds a peer to: far below D-Bus's own, and far above
// what the properties Den reads take. Anything past one is refused, never
// cut.
const (
	maxMessage = 1 << 20 // bytes in one message
	maxString  = 1 << 16 // bytes in one string
	maxItems   = 4096    // items in one array
	maxDepth   = 16      // types nested in one another
)

// ObjectPath is a D-Bus object path (o).
type ObjectPath string

// Signature is a D-Bus signature (g).
type Signature string

// Variant is a value of any type (v), with its signature.
type Variant struct {
	Sig   Signature
	Value any
}

// DictEntry is one entry of a dict (a{..}).
type DictEntry struct {
	Key   any
	Value any
}

// message is one D-Bus message.
type message struct {
	typ    byte
	flags  byte
	serial uint32
	fields map[byte]Variant
	sig    Signature
	body   []any
}

func (m *message) str(f byte) string {
	v, ok := m.fields[f]
	if !ok {
		return ""
	}
	switch s := v.Value.(type) {
	case string:
		return s
	case ObjectPath:
		return string(s)
	case Signature:
		return string(s)
	}
	return ""
}

func (m *message) replySerial() uint32 {
	if v, ok := m.fields[fieldReplySerial]; ok {
		if u, ok := v.Value.(uint32); ok {
			return u
		}
	}
	return 0
}

// alignOf is the alignment of a type by its first character.
func alignOf(c byte) int {
	switch c {
	case 'y', 'g', 'v':
		return 1
	case 'b', 'i', 'u', 's', 'o', 'a':
		return 4
	case 'x', 't', '(', '{':
		return 8
	}
	return 1
}

// nextType splits the first complete type off a signature, and checks it
// whole: only the types Den reads (no 16-bit or floating types, no file
// descriptors), no empty struct, a dict entry only as an array's element
// with a basic key and one value, and nesting at most maxDepth.
func nextType(sig string) (string, string, error) {
	n, err := typeLen(sig, 0, false)
	if err != nil {
		return "", "", err
	}
	return sig[:n], sig[n:], nil
}

// typeLen is the length of the complete type sig starts with. inArray is
// set for an array's element, where a dict entry may stand.
func typeLen(sig string, depth int, inArray bool) (int, error) {
	bad := fmt.Errorf("bad signature %q", sig)
	if sig == "" || depth > maxDepth {
		return 0, bad
	}
	switch sig[0] {
	case 'y', 'b', 'i', 'u', 'x', 't', 's', 'o', 'g', 'v':
		return 1, nil
	case 'a':
		n, err := typeLen(sig[1:], depth+1, true)
		return 1 + n, err
	case '(':
		i := 1
		for i < len(sig) && sig[i] != ')' {
			n, err := typeLen(sig[i:], depth+1, false)
			if err != nil {
				return 0, err
			}
			i += n
		}
		if i >= len(sig) || i == 1 {
			return 0, bad // unclosed, or empty
		}
		return i + 1, nil
	case '{':
		if !inArray || len(sig) < 2 || !strings.ContainsRune("ybiuxtsog", rune(sig[1])) {
			return 0, bad
		}
		n, err := typeLen(sig[2:], depth+1, false)
		if err != nil {
			return 0, err
		}
		if 2+n >= len(sig) || sig[2+n] != '}' {
			return 0, bad
		}
		return 2 + n + 1, nil
	}
	return 0, bad
}

// splitTypes is a signature's complete types.
func splitTypes(sig string) ([]string, error) {
	var out []string
	for sig != "" {
		t, rest, err := nextType(sig)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
		sig = rest
	}
	return out, nil
}

// encoder writes values by signature, little-endian, aligned from the
// start of what it writes (a body starts 8-aligned in a message).
type encoder struct {
	buf bytes.Buffer
}

func (e *encoder) pad(n int) {
	for e.buf.Len()%n != 0 {
		e.buf.WriteByte(0)
	}
}

func (e *encoder) u32(v uint32) {
	e.pad(4)
	binary.Write(&e.buf, binary.LittleEndian, v)
}

func (e *encoder) str(s string) error {
	if strings.IndexByte(s, 0) >= 0 {
		return errors.New("a string holds NUL")
	}
	e.u32(uint32(len(s)))
	e.buf.WriteString(s)
	e.buf.WriteByte(0)
	return nil
}

func (e *encoder) sigStr(s string) error {
	if len(s) > 255 {
		return errors.New("signature too long")
	}
	e.buf.WriteByte(byte(len(s)))
	e.buf.WriteString(s)
	e.buf.WriteByte(0)
	return nil
}

// value writes v as type t (one complete type).
func (e *encoder) value(t string, v any, depth int) error {
	if depth > maxDepth {
		return errors.New("nested too deep")
	}
	bad := func() error { return fmt.Errorf("cannot write %T as %s", v, t) }
	switch t[0] {
	case 'y':
		b, ok := v.(byte)
		if !ok {
			return bad()
		}
		e.buf.WriteByte(b)
	case 'b':
		b, ok := v.(bool)
		if !ok {
			return bad()
		}
		var u uint32
		if b {
			u = 1
		}
		e.u32(u)
	case 'i':
		x, ok := v.(int32)
		if !ok {
			return bad()
		}
		e.pad(4)
		binary.Write(&e.buf, binary.LittleEndian, x)
	case 'u':
		x, ok := v.(uint32)
		if !ok {
			return bad()
		}
		e.u32(x)
	case 'x':
		x, ok := v.(int64)
		if !ok {
			return bad()
		}
		e.pad(8)
		binary.Write(&e.buf, binary.LittleEndian, x)
	case 't':
		x, ok := v.(uint64)
		if !ok {
			return bad()
		}
		e.pad(8)
		binary.Write(&e.buf, binary.LittleEndian, x)
	case 's':
		s, ok := v.(string)
		if !ok {
			return bad()
		}
		return e.str(s)
	case 'o':
		s, ok := v.(ObjectPath)
		if !ok {
			return bad()
		}
		return e.str(string(s))
	case 'g':
		s, ok := v.(Signature)
		if !ok {
			return bad()
		}
		return e.sigStr(string(s))
	case 'v':
		vv, ok := v.(Variant)
		if !ok {
			return bad()
		}
		if _, rest, err := nextType(string(vv.Sig)); err != nil || rest != "" {
			return errors.New("a variant holds one complete type")
		}
		if err := e.sigStr(string(vv.Sig)); err != nil {
			return err
		}
		return e.value(string(vv.Sig), vv.Value, depth+1)
	case 'a':
		elem := t[1:]
		e.pad(4)
		lenAt := e.buf.Len()
		e.buf.Write([]byte{0, 0, 0, 0})
		e.pad(alignOf(elem[0]))
		start := e.buf.Len()
		if elem[0] == '{' {
			entries, ok := v.([]DictEntry)
			if !ok {
				return bad()
			}
			kv, err := splitTypes(elem[1 : len(elem)-1])
			if err != nil || len(kv) != 2 {
				return fmt.Errorf("bad dict %q", elem)
			}
			for _, d := range entries {
				e.pad(8)
				if err := e.value(kv[0], d.Key, depth+1); err != nil {
					return err
				}
				if err := e.value(kv[1], d.Value, depth+1); err != nil {
					return err
				}
			}
		} else {
			items, err := asSlice(v)
			if err != nil {
				return bad()
			}
			for _, it := range items {
				if err := e.value(elem, it, depth+1); err != nil {
					return err
				}
			}
		}
		binary.LittleEndian.PutUint32(e.buf.Bytes()[lenAt:], uint32(e.buf.Len()-start))
	case '(':
		fields, ok := v.([]any)
		if !ok {
			return bad()
		}
		types, err := splitTypes(t[1 : len(t)-1])
		if err != nil || len(types) != len(fields) {
			return bad()
		}
		e.pad(8)
		for i, ft := range types {
			if err := e.value(ft, fields[i], depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("type %q not supported", t)
	}
	return nil
}

// asSlice reads the items of an array value: []any, or the slices of the
// basic types a caller would naturally write.
func asSlice(v any) ([]any, error) {
	switch s := v.(type) {
	case []any:
		return s, nil
	case []string:
		out := make([]any, len(s))
		for i, x := range s {
			out[i] = x
		}
		return out, nil
	case []byte:
		out := make([]any, len(s))
		for i, x := range s {
			out[i] = x
		}
		return out, nil
	}
	return nil, errors.New("not an array")
}

// decoder reads values by signature from data, aligned from its start.
type decoder struct {
	data []byte
	off  int
	end  int // the end of the current array, or len(data)
}

var errShort = errors.New("message too short")

func (d *decoder) align(n int) error {
	for d.off%n != 0 {
		if d.off >= d.end {
			return errShort
		}
		if d.data[d.off] != 0 {
			return errors.New("padding not zero")
		}
		d.off++
	}
	return nil
}

func (d *decoder) take(n int) ([]byte, error) {
	if n < 0 || d.off+n > d.end {
		return nil, errShort
	}
	b := d.data[d.off : d.off+n]
	d.off += n
	return b, nil
}

func (d *decoder) u32() (uint32, error) {
	if err := d.align(4); err != nil {
		return 0, err
	}
	b, err := d.take(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

func (d *decoder) str() (string, error) {
	n, err := d.u32()
	if err != nil {
		return "", err
	}
	if n > maxString {
		return "", errors.New("string too long")
	}
	b, err := d.take(int(n) + 1)
	if err != nil {
		return "", err
	}
	if b[n] != 0 || bytes.IndexByte(b[:n], 0) >= 0 {
		return "", errors.New("bad string")
	}
	return string(b[:n]), nil
}

func (d *decoder) sigStr() (string, error) {
	nb, err := d.take(1)
	if err != nil {
		return "", err
	}
	b, err := d.take(int(nb[0]) + 1)
	if err != nil {
		return "", err
	}
	if b[nb[0]] != 0 {
		return "", errors.New("bad signature")
	}
	return string(b[:nb[0]]), nil
}

// value reads one value of type t.
func (d *decoder) value(t string, depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("nested too deep")
	}
	fixed := func(n int) ([]byte, error) {
		if err := d.align(n); err != nil {
			return nil, err
		}
		return d.take(n)
	}
	switch t[0] {
	case 'y':
		b, err := d.take(1)
		if err != nil {
			return nil, err
		}
		return b[0], nil
	case 'b':
		u, err := d.u32()
		if err != nil {
			return nil, err
		}
		if u > 1 {
			return nil, errors.New("bad boolean")
		}
		return u == 1, nil
	case 'i':
		b, err := fixed(4)
		if err != nil {
			return nil, err
		}
		return int32(binary.LittleEndian.Uint32(b)), nil
	case 'u':
		return d.u32()
	case 'x':
		b, err := fixed(8)
		if err != nil {
			return nil, err
		}
		return int64(binary.LittleEndian.Uint64(b)), nil
	case 't':
		b, err := fixed(8)
		if err != nil {
			return nil, err
		}
		return binary.LittleEndian.Uint64(b), nil
	case 's':
		return d.str()
	case 'o':
		s, err := d.str()
		return ObjectPath(s), err
	case 'g':
		s, err := d.sigStr()
		return Signature(s), err
	case 'v':
		sig, err := d.sigStr()
		if err != nil {
			return nil, err
		}
		if _, rest, err := nextType(sig); err != nil || rest != "" {
			return nil, errors.New("a variant holds one complete type")
		}
		v, err := d.value(sig, depth+1)
		return Variant{Sig: Signature(sig), Value: v}, err
	case 'a':
		n, err := d.u32()
		if err != nil {
			return nil, err
		}
		elem := t[1:]
		if err := d.align(alignOf(elem[0])); err != nil {
			return nil, err
		}
		if int(n) > d.end-d.off {
			return nil, errShort
		}
		outer := d.end
		d.end = d.off + int(n)
		defer func() { d.end = outer }()
		if elem[0] == '{' {
			kv, err := splitTypes(elem[1 : len(elem)-1])
			if err != nil || len(kv) != 2 {
				return nil, fmt.Errorf("bad dict %q", elem)
			}
			out := []DictEntry{}
			for d.off < d.end {
				if err := d.align(8); err != nil {
					return nil, err
				}
				k, err := d.value(kv[0], depth+1)
				if err != nil {
					return nil, err
				}
				v, err := d.value(kv[1], depth+1)
				if err != nil {
					return nil, err
				}
				out = append(out, DictEntry{Key: k, Value: v})
				if len(out) > maxItems {
					return nil, errors.New("array too long")
				}
			}
			return out, nil
		}
		out := []any{}
		for d.off < d.end {
			v, err := d.value(elem, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			if len(out) > maxItems {
				return nil, errors.New("array too long")
			}
		}
		return out, nil
	case '(':
		types, err := splitTypes(t[1 : len(t)-1])
		if err != nil {
			return nil, err
		}
		if err := d.align(8); err != nil {
			return nil, err
		}
		out := make([]any, 0, len(types))
		for _, ft := range types {
			v, err := d.value(ft, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	}
	return nil, fmt.Errorf("type %q not supported", t)
}

// encodeMessage is m on the wire.
func encodeMessage(m *message) ([]byte, error) {
	var body encoder
	types, err := splitTypes(string(m.sig))
	if err != nil {
		return nil, err
	}
	if len(types) != len(m.body) {
		return nil, errors.New("body does not match its signature")
	}
	for i, t := range types {
		if err := body.value(t, m.body[i], 0); err != nil {
			return nil, err
		}
	}
	fields := []any{}
	for _, f := range []byte{fieldPath, fieldInterface, fieldMember, fieldErrorName, fieldReplySerial, fieldDestination, fieldSender, fieldSignature, fieldUnixFDs} {
		if v, ok := m.fields[f]; ok {
			fields = append(fields, []any{f, v})
		}
	}
	if m.sig != "" {
		fields = append(fields, []any{byte(fieldSignature), Variant{Sig: "g", Value: m.sig}})
	}
	var h encoder
	h.buf.Write([]byte{'l', m.typ, m.flags, 1})
	h.u32(uint32(body.buf.Len()))
	h.u32(m.serial)
	if err := h.value("a(yv)", fields, 0); err != nil {
		return nil, err
	}
	h.pad(8)
	out := append(h.buf.Bytes(), body.buf.Bytes()...)
	if len(out) > maxMessage {
		return nil, errors.New("message too long")
	}
	return out, nil
}

// readMessage reads one message.
func readMessage(r *bufio.Reader) (*message, error) {
	fixed := make([]byte, 16)
	if _, err := io.ReadFull(r, fixed); err != nil {
		return nil, err
	}
	if fixed[0] != 'l' {
		return nil, errors.New("only little-endian messages are read")
	}
	if fixed[3] != 1 {
		return nil, errors.New("protocol version")
	}
	bodyLen := binary.LittleEndian.Uint32(fixed[4:])
	fieldsLen := binary.LittleEndian.Uint32(fixed[12:])
	if bodyLen > maxMessage || fieldsLen > maxMessage {
		return nil, errors.New("message too long")
	}
	headLen := 16 + int(fieldsLen)
	padded := (headLen + 7) &^ 7
	total := padded + int(bodyLen)
	if total > maxMessage {
		return nil, errors.New("message too long")
	}
	data := make([]byte, total)
	copy(data, fixed)
	if _, err := io.ReadFull(r, data[16:]); err != nil {
		return nil, err
	}
	m := &message{typ: fixed[1], flags: fixed[2], serial: binary.LittleEndian.Uint32(fixed[8:]), fields: map[byte]Variant{}}
	hd := &decoder{data: data[:headLen], off: 12, end: headLen}
	fv, err := hd.value("a(yv)", 0)
	if err != nil {
		return nil, fmt.Errorf("header: %w", err)
	}
	for _, f := range fv.([]any) {
		st := f.([]any)
		code, _ := st[0].(byte)
		v, _ := st[1].(Variant)
		m.fields[code] = v
	}
	for i := headLen; i < padded; i++ {
		if data[i] != 0 {
			return nil, errors.New("padding not zero")
		}
	}
	if s, ok := m.fields[fieldSignature]; ok {
		sig, ok := s.Value.(Signature)
		if !ok {
			return nil, errors.New("bad signature field")
		}
		m.sig = sig
		delete(m.fields, fieldSignature)
	}
	if fds, ok := m.fields[fieldUnixFDs]; ok {
		if n, _ := fds.Value.(uint32); n != 0 {
			return nil, errors.New("file descriptors are not taken")
		}
	}
	types, err := splitTypes(string(m.sig))
	if err != nil {
		return nil, err
	}
	bd := &decoder{data: data[padded:], end: int(bodyLen)}
	for _, t := range types {
		v, err := bd.value(t, 0)
		if err != nil {
			return nil, fmt.Errorf("body: %w", err)
		}
		m.body = append(m.body, v)
	}
	if bd.off != bd.end {
		return nil, errors.New("body longer than its signature")
	}
	return m, nil
}
