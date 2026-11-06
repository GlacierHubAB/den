package desksand

import (
	"encoding/binary"
	"errors"
)

// XAuthFor keeps the entries of an Xauthority file for one display
// number: the cookie a sandbox's X apps get, and no other display's.
func XAuthFor(file []byte, display string) ([]byte, error) {
	var out []byte
	for len(file) > 0 {
		start := file
		if len(file) < 2 {
			return nil, errors.New("a short Xauthority entry")
		}
		file = file[2:] // family
		var fields [4][]byte
		for i := range fields {
			if len(file) < 2 {
				return nil, errors.New("a short Xauthority entry")
			}
			n := int(binary.BigEndian.Uint16(file))
			if len(file) < 2+n {
				return nil, errors.New("a short Xauthority entry")
			}
			fields[i], file = file[2:2+n], file[2+n:]
		}
		if string(fields[1]) == display {
			out = append(out, start[:len(start)-len(file)]...)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no cookie for display " + display)
	}
	return out, nil
}
