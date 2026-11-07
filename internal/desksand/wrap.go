package desksand

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// passNames are the names the net stage passes on to bwrap: Env's, and
// nothing else whatever the service was given.
var passNames = append(slices.Clone(EnvNames), "XDG_SESSION_TYPE", "DISPLAY", "XAUTHORITY",
	"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME")

// PassEnv is the net stage's environment for bwrap, from its own: only
// passNames, and the bus always "disabled:".
func PassEnv(environ []string) []string {
	const bus = "DBUS_SESSION_BUS_ADDRESS=disabled:"
	out, set := []string{}, false
	for _, kv := range environ {
		k, _, ok := strings.Cut(kv, "=")
		switch {
		case ok && k == "DBUS_SESSION_BUS_ADDRESS" && !set:
			out, set = append(out, bus), true
		case ok && slices.Contains(passNames, k):
			out = append(out, kv)
		}
	}
	if !set {
		out = append(out, bus)
	}
	return out
}

// NetArgs reads the net stage's arguments: --host <prefix>, then "--" and
// an absolute argv.
func NetArgs(args []string) (hosts []netip.Prefix, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--host":
			if i+1 >= len(args) {
				return nil, nil, errors.New("--host needs a prefix")
			}
			p, err := netip.ParsePrefix(args[i+1])
			if err != nil {
				return nil, nil, err
			}
			hosts = append(hosts, p)
			i++
		case "--":
			rest = args[i+1:]
			if len(rest) == 0 || !strings.HasPrefix(rest[0], "/") {
				return nil, nil, errors.New("after -- an absolute program")
			}
			return hosts, rest, nil
		default:
			return nil, nil, fmt.Errorf("unknown argument %q", args[i])
		}
	}
	return nil, nil, errors.New("no --")
}

// WalkMax is the most of the walk's output Den reads; more is a failed
// walk.
const WalkMax = 64 << 10

// ParseWalk reads the walk's output: "ok" alone, or finding lines, each
// "socket", "unlisted" or "abstract" and a quoted path, or "timeout".
// Anything else is a failed walk. It returns the findings for the refusal.
func ParseWalk(out []byte) (findings []string, err error) {
	if len(out) > WalkMax {
		return nil, errors.New("the socket walk said too much")
	}
	s := string(out)
	if s == "ok\n" {
		return nil, nil
	}
	if s == "" || !strings.HasSuffix(s, "\n") {
		return nil, errors.New("the socket walk did not finish")
	}
	for _, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		if line == "timeout" {
			findings = append(findings, "the walk took over "+WalkBound.String())
			continue
		}
		kind, q, ok := strings.Cut(line, " ")
		p, err := strconv.Unquote(q)
		if !ok || err != nil || !slices.Contains([]string{"socket", "unlisted", "abstract"}, kind) {
			return nil, errors.New("the socket walk's output is not its own")
		}
		switch kind {
		case "socket":
			findings = append(findings, "a socket at "+strconv.Quote(p))
		case "unlisted":
			findings = append(findings, "a folder that can be entered but not listed at "+strconv.Quote(p))
		case "abstract":
			findings = append(findings, "an abstract socket "+strconv.Quote(p))
		}
	}
	if len(findings) == 0 {
		return nil, errors.New("the socket walk's output is not its own")
	}
	return findings, nil
}

// WalkOutput is what the walk stage prints for its findings.
func WalkOutput(findings []string) string {
	if len(findings) == 0 {
		return "ok\n"
	}
	return strings.Join(findings, "\n") + "\n"
}
