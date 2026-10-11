package cost

import (
	"math"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/transcript"
	"github.com/GlacierHubAB/den/internal/tree"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestPriceLookupUsesTheLongestPrefix(t *testing.T) {
	tb := NewTable(nil)
	for model, input := range map[string]float64{
		"claude-opus-5-5":           4,
		"claude-opus-5":             5,
		"claude-opus-4-8":           5,
		"claude-sonnet-5-5":         2,
		"claude-haiku-4-5-20251001": 1,
		"claude-fable-5-1":          10,
	} {
		p, ok := tb.For(model)
		if !ok || p.Input != input {
			t.Errorf("%s = %+v %v, want input %v", model, p, ok, input)
		}
	}
	if _, ok := tb.For("gpt-x"); ok {
		t.Error("an unknown model must have no price")
	}
}

func TestUSDForTokens(t *testing.T) {
	tb := NewTable(map[string]string{"claude-test": "10, 50, 12.5, 1"})
	usd, partial := tb.USD(map[string]transcript.Tokens{
		"claude-test-1": {Input: 1_000_000, Output: 1_000_000, CacheCreation: 1_000_000, CacheRead: 1_000_000},
	})
	if partial || !near(usd, 73.5) {
		t.Errorf("usd = %v partial = %v, want 73.5", usd, partial)
	}
}

func TestUnknownModelMakesTheFigurePartial(t *testing.T) {
	tb := NewTable(nil)
	usd, partial := tb.USD(map[string]transcript.Tokens{
		"claude-opus-5-5": {Output: 1_000_000},
		"mystery-model":   {Output: 5_000_000},
	})
	if !partial || !near(usd, 20) {
		t.Errorf("usd = %v partial = %v, want 20 and partial", usd, partial)
	}
	if got := Dollars(usd, partial); got != "≈ $20.00+" {
		t.Errorf("Dollars = %q", got)
	}
}

func TestOverrideParsing(t *testing.T) {
	tb := NewTable(map[string]string{"claude-opus-5-5": "8, 40", "bad": "x", "short": "1"})
	p, _ := tb.For("claude-opus-5-5")
	if p.Input != 8 || p.Output != 40 || !near(p.CacheWrite, 10) || !near(p.CacheRead, 0.8) {
		t.Errorf("override = %+v", p)
	}
	if _, ok := tb.For("bad"); ok {
		t.Error("an unparsable override must be skipped")
	}
}

func TestSummaryOfAccountSplitsSubagentsAndKeepsReportedApart(t *testing.T) {
	tb := NewTable(nil)
	rep := &transcript.Reported{USD: 9.99, Unknown: true}
	s := tb.Of(transcript.Account{
		Main:     map[string]transcript.Tokens{"claude-sonnet-5-5": {Output: 1_000_000}},
		Sub:      map[string]transcript.Tokens{"claude-haiku-4-5": {Output: 1_000_000}},
		Reported: rep,
	})
	if s.Tokens.Total() != 1_000_000 || s.Subagents.Total() != 1_000_000 || !near(s.USD, 15) {
		t.Errorf("summary = %+v", s)
	}
	if s.ReportedUSD == nil || *s.ReportedUSD != 9.99 || !s.Partial {
		t.Errorf("reported = %+v", s)
	}
	if sum := s.Add(s); sum.ReportedUSD != nil || !near(sum.USD, 30) {
		t.Errorf("a reported figure must not roll up: %+v", sum)
	}
}

func TestTreeRollUp(t *testing.T) {
	set := tree.New()
	now := time.Now()
	for _, e := range []tree.Edge{
		{Parent: tree.Orchestrator, Child: "lead", At: now},
		{Parent: "lead", Child: "w1", At: now},
		{Parent: "lead", Child: "w2", At: now},
		{Parent: "w2", Child: "w3", At: now},
		{Parent: tree.Orchestrator, Child: "other", At: now},
	} {
		set.Add(e)
	}
	own := map[string]int64{"lead": 100, "w1": 10, "w2": 20, "w3": 5, "other": 1000}
	f := func(id string) Summary {
		return Summary{Tokens: transcript.Tokens{Output: own[id]}, USD: float64(own[id])}
	}
	got := Tree(set, "lead", f)
	if got.Tokens.Output != 135 || got.USD != 135 {
		t.Errorf("lead tree = %+v, want 135", got)
	}
	if got := Tree(set, "w3", f); got.Tokens.Output != 5 {
		t.Errorf("leaf = %+v", got)
	}
	if d := Descendants(set, "lead"); len(d) != 3 {
		t.Errorf("descendants = %v", d)
	}
}

func TestParseBudget(t *testing.T) {
	for in, want := range map[string]int64{"2M": 2_000_000, "500k": 500_000, "1500000": 1_500_000, "1.5m": 1_500_000} {
		if n, usd, err := ParseBudget(in); err != nil || n != want || usd != 0 {
			t.Errorf("%q = %d %v %v", in, n, usd, err)
		}
	}
	if n, usd, err := ParseBudget("$5.50"); err != nil || n != 0 || usd != 5.5 {
		t.Errorf("$5.50 = %d %v %v", n, usd, err)
	}
	for _, bad := range []string{"", "$", "-3", "lots", "$0"} {
		if _, _, err := ParseBudget(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func TestFormatting(t *testing.T) {
	for n, want := range map[int64]string{950: "950", 1200: "1.2k", 12400: "12k", 3_200_000: "3.2M", 1_000_000: "1M"} {
		if got := Tok(n); got != want {
			t.Errorf("Tok(%d) = %q, want %q", n, got, want)
		}
	}
	for usd, want := range map[float64]string{0: "≈ $0", 0.0421: "≈ $0.042", 1.5: "≈ $1.50", 240: "≈ $240"} {
		if got := Dollars(usd, false); got != want {
			t.Errorf("Dollars(%v) = %q, want %q", usd, got, want)
		}
	}
}
