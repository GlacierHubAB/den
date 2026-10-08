package fleet

import (
	"testing"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/transcript"
)

func f(v float64) *float64 { return &v }

func TestMergeUsagePrefersBadgeFigures(t *testing.T) {
	est := transcript.Usage{Tokens: 196_000, Window: 200_000}
	u, has, fromBadge := mergeUsage(est, true, badge.Usage{Filled: f(201_224), Window: f(1_000_000), Context: f(20)}, true)
	if !has || !fromBadge || u.Window != 1_000_000 || u.Tokens != 201_224 || u.Percent() != 20 {
		t.Errorf("got %+v has=%v fromBadge=%v", u, has, fromBadge)
	}
	u, has, fromBadge = mergeUsage(est, true, badge.Usage{Window: f(1_000_000), Context: f(20)}, true)
	if !has || !fromBadge || u.Tokens != 200_000 {
		t.Errorf("percent-only record: got %+v", u)
	}
	u, has, fromBadge = mergeUsage(est, true, badge.Usage{FiveHour: f(3)}, true)
	if fromBadge || !has || u != est {
		t.Errorf("no context in record: estimate should stand, got %+v", u)
	}
	_, has, _ = mergeUsage(transcript.Usage{}, false, badge.Usage{}, false)
	if has {
		t.Error("nothing known should stay unknown")
	}
}
