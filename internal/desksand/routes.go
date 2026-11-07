package desksand

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Routes is every prefix the net stage closes with a prohibit route:
// Closed, each of the host's own addresses, and each one's on-link prefix,
// so a new address on the same link (an IPv6 temporary address) is closed
// as well. Sorted, without repeats.
func Routes(hosts []netip.Prefix) []netip.Prefix {
	set := map[netip.Prefix]bool{}
	for _, p := range Closed {
		set[p] = true
	}
	for _, h := range hosts {
		a := h.Addr().Unmap()
		set[netip.PrefixFrom(a, a.BitLen())] = true
		if p, err := a.Prefix(h.Bits()); err == nil && h.Bits() > 0 {
			set[p] = true
		}
	}
	out := make([]netip.Prefix, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b netip.Prefix) int {
		if c := a.Addr().Compare(b.Addr()); c != 0 {
			return c
		}
		return a.Bits() - b.Bits()
	})
	return out
}

// routeMsg is an RTM_NEWROUTE for a prohibit route to p in the main table.
func routeMsg(p netip.Prefix, seq uint32) []byte {
	fam := byte(unix.AF_INET)
	if p.Addr().Is6() {
		fam = unix.AF_INET6
	}
	dst := p.Addr().AsSlice()
	attrLen := unix.SizeofRtAttr + len(dst)
	attrSpace := (attrLen + unix.RTA_ALIGNTO - 1) &^ (unix.RTA_ALIGNTO - 1)
	n := unix.SizeofNlMsghdr + unix.SizeofRtMsg + attrSpace
	b := make([]byte, n)
	le := binary.NativeEndian
	le.PutUint32(b[0:], uint32(n))
	le.PutUint16(b[4:], unix.RTM_NEWROUTE)
	le.PutUint16(b[6:], unix.NLM_F_REQUEST|unix.NLM_F_ACK|unix.NLM_F_CREATE|unix.NLM_F_EXCL)
	le.PutUint32(b[8:], seq)
	r := b[unix.SizeofNlMsghdr:]
	r[0] = fam                    // family
	r[1] = byte(p.Bits())         // dst_len
	r[4] = unix.RT_TABLE_MAIN     // table
	r[5] = unix.RTPROT_STATIC     // protocol
	r[6] = unix.RT_SCOPE_UNIVERSE // scope
	r[7] = unix.RTN_PROHIBIT      // type
	at := r[unix.SizeofRtMsg:]
	le.PutUint16(at[0:], uint16(attrLen))
	le.PutUint16(at[2:], unix.RTA_DST)
	copy(at[unix.SizeofRtAttr:], dst)
	return b
}

// parseRoutes reads the prohibit routes of the main table out of an
// RTM_GETROUTE dump's messages.
func parseRoutes(msgs []byte) ([]netip.Prefix, bool, error) {
	var out []netip.Prefix
	for len(msgs) >= unix.SizeofNlMsghdr {
		le := binary.NativeEndian
		l := int(le.Uint32(msgs[0:]))
		typ := le.Uint16(msgs[4:])
		if l < unix.SizeofNlMsghdr || l > len(msgs) {
			return nil, false, errors.New("a short netlink message")
		}
		body := msgs[unix.SizeofNlMsghdr:l]
		switch typ {
		case unix.NLMSG_DONE:
			return out, true, nil
		case unix.NLMSG_ERROR:
			if len(body) >= 4 {
				if e := int32(le.Uint32(body)); e != 0 {
					return nil, false, unix.Errno(-e)
				}
			}
		case unix.RTM_NEWROUTE:
			if len(body) < unix.SizeofRtMsg {
				return nil, false, errors.New("a short route")
			}
			fam, bits, table, kind := body[0], int(body[1]), body[4], body[7]
			attrs := body[unix.SizeofRtMsg:]
			var dst []byte
			for len(attrs) >= unix.SizeofRtAttr {
				al := int(le.Uint16(attrs[0:]))
				if al < unix.SizeofRtAttr || al > len(attrs) {
					break
				}
				if le.Uint16(attrs[2:]) == unix.RTA_DST {
					dst = attrs[unix.SizeofRtAttr:al]
				}
				attrs = attrs[min((al+unix.RTA_ALIGNTO-1)&^(unix.RTA_ALIGNTO-1), len(attrs)):]
			}
			if kind != unix.RTN_PROHIBIT || table != unix.RT_TABLE_MAIN {
				break
			}
			a, ok := netip.AddrFromSlice(dst)
			if !ok || (fam == unix.AF_INET) != a.Is4() {
				break
			}
			out = append(out, netip.PrefixFrom(a, bits))
		}
		msgs = msgs[min((l+unix.NLMSG_ALIGNTO-1)&^(unix.NLMSG_ALIGNTO-1), len(msgs)):]
	}
	return out, false, nil
}

// Missing is what of want a dump does not show.
func Missing(want, have []netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	for _, p := range want {
		if !slices.Contains(have, p) {
			out = append(out, p)
		}
	}
	return out
}

// Close adds a prohibit route for each of want, then dumps the table and
// fails unless every one is there: all or nothing.
func Close(want []netip.Prefix) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}
	buf := make([]byte, 1<<16)
	// A namespace with IPv6 switched off has no v6 to close: its routes
	// are left out, and only then.
	if v6Off() {
		want = slices.DeleteFunc(slices.Clone(want), func(p netip.Prefix) bool { return p.Addr().Is6() })
	}
	for i, p := range want {
		if err := unix.Sendto(fd, routeMsg(p, uint32(i+1)), 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
			return fmt.Errorf("closing %s: %v", p, err)
		}
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			return fmt.Errorf("closing %s: %v", p, err)
		}
		if _, _, err := parseRoutes(buf[:n]); err != nil && !errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("closing %s: %v", p, err)
		}
	}
	var have []netip.Prefix
	for _, fam := range []byte{unix.AF_INET, unix.AF_INET6} {
		got, err := dump(fd, fam, buf)
		if err != nil {
			return err
		}
		have = append(have, got...)
	}
	if miss := Missing(want, have); len(miss) > 0 {
		return fmt.Errorf("prohibit routes missing after adding them: %v", miss)
	}
	return nil
}

// HasV6Default reports whether a /proc/net/ipv6_route has a usable default
// route: ::/0, up, and not a reject (the kernel keeps reject defaults on
// lo when there is none).
func HasV6Default(ipv6Route string) bool {
	const up, reject = 0x0001, 0x0200
	for _, line := range strings.Split(ipv6Route, "\n") {
		f := strings.Fields(line)
		if len(f) < 10 || f[0] != strings.Repeat("0", 32) || f[1] != "00" {
			continue
		}
		flags, err := strconv.ParseUint(f[8], 16, 32)
		if err == nil && flags&up != 0 && flags&reject == 0 {
			return true
		}
	}
	return false
}

// v6Off reports whether this network namespace has IPv6 switched off.
func v6Off() bool {
	raw, err := os.ReadFile("/proc/sys/net/ipv6/conf/all/disable_ipv6")
	if errors.Is(err, fs.ErrNotExist) {
		return true // no IPv6 in this kernel at all
	}
	return err == nil && strings.TrimSpace(string(raw)) == "1"
}

func dump(fd int, fam byte, buf []byte) ([]netip.Prefix, error) {
	n := unix.SizeofNlMsghdr + unix.SizeofRtMsg
	b := make([]byte, n)
	le := binary.NativeEndian
	le.PutUint32(b[0:], uint32(n))
	le.PutUint16(b[4:], unix.RTM_GETROUTE)
	le.PutUint16(b[6:], unix.NLM_F_REQUEST|unix.NLM_F_DUMP)
	le.PutUint32(b[8:], 1<<20)
	b[unix.SizeofNlMsghdr] = fam
	if err := unix.Sendto(fd, b, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return nil, err
	}
	var out []netip.Prefix
	for {
		k, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			return nil, err
		}
		got, done, err := parseRoutes(buf[:k])
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
		if done {
			return out, nil
		}
	}
}
