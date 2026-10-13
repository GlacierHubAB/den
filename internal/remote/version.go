package remote

import (
	"strconv"
	"strings"
)

// LocalVersion is this den's version; main sets it from the build.
var LocalVersion = "dev"

// versionParts reads "v0.2.0", "v0.1.0-96-g4f1ad22" (git describe: 96
// commits after v0.1.0), "2.1.300" or "den v0.2.0" into numbers: the dotted
// release, then the commit count. ok is false for "dev" and anything else
// without a leading number, and for absurd numbers (over 2^20), so a host
// cannot pass for older or newer with an overflowing version.
func versionParts(v string) (parts []int, ok bool) {
	v = strings.TrimSpace(v)
	if i := strings.LastIndexByte(v, ' '); i >= 0 {
		v = v[i+1:]
	}
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimSuffix(v, "-dirty")
	rel, rest, _ := strings.Cut(v, "-")
	for _, f := range strings.Split(rel, ".") {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 || n > 1<<20 {
			return nil, false
		}
		parts = append(parts, n)
	}
	for len(parts) < 3 {
		parts = append(parts, 0)
	}
	commits := 0
	if n, _, found := strings.Cut(rest, "-"); found {
		if c, err := strconv.Atoi(n); err == nil && c >= 0 && c <= 1<<20 {
			commits = c
		}
	}
	return append(parts, commits), true
}

// versionLess reports a < b; a version that does not parse sorts first.
func versionLess(a, b string) bool {
	pa, oka := versionParts(a)
	pb, okb := versionParts(b)
	if !oka || !okb {
		return !oka && okb
	}
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return len(pa) < len(pb)
}

// Older reports whether remote is known to be older than local: both
// parse and remote sorts first. A "dev" build is never known to be older.
func Older(remote, local string) bool {
	_, okr := versionParts(remote)
	_, okl := versionParts(local)
	return okr && okl && versionLess(remote, local)
}
