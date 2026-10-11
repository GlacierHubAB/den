// Package cost turns the tokens in session transcripts into the figures Den
// shows: token totals first, and dollars as an estimate from a small price
// table. A subscription pays no per-token price, so a dollar figure here is
// what the same tokens would cost at API list prices, never a bill.
package cost

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/GlacierHubAB/den/internal/transcript"
	"github.com/GlacierHubAB/den/internal/tree"
)

// PricesAsOf is when the built-in table was taken.
const PricesAsOf = "2026-09-25"

// Price is dollars per million tokens.
type Price struct {
	Input, Output, CacheWrite, CacheRead float64
}

// builtin is matched by longest model-id prefix. Cache writes are priced at
// 1.25 times the input rate (the five-minute cache). Edit the table with
// [prices] in config.toml when it goes stale.
var builtin = map[string]Price{
	"claude-fable-5":    {10, 50, 12.5, 1.0},
	"claude-fable-5-1":  {10, 50, 12.5, 0.25},
	"claude-mythos-5":   {10, 50, 12.5, 1.0},
	"claude-mythos-5-1": {10, 50, 12.5, 0.25},
	"claude-opus-5-5":   {4, 20, 5, 0.20},
	"claude-opus-5":     {5, 25, 6.25, 0.50},
	"claude-opus-4":     {5, 25, 6.25, 0.50},
	"claude-sonnet-5":   {2, 10, 2.5, 0.20},
	"claude-sonnet-4":   {3, 15, 3.75, 0.30},
	"claude-haiku-4":    {1, 5, 1.25, 0.10},
}

// Table prices models: the built-in rates with the user's overrides on top.
type Table struct {
	rates map[string]Price
}

// NewTable returns the built-in table with overrides applied. An override
// value is "input, output, cache write, cache read" in dollars per million
// tokens; the last two may be left out and then follow the input rate (1.25
// and 0.1 times). A value that does not parse is skipped.
func NewTable(overrides map[string]string) Table {
	t := Table{rates: map[string]Price{}}
	for k, v := range builtin {
		t.rates[k] = v
	}
	for k, v := range overrides {
		if p, ok := parsePrice(v); ok && k != "" {
			t.rates[k] = p
		}
	}
	return t
}

func parsePrice(s string) (Price, bool) {
	var n []float64
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		x, err := strconv.ParseFloat(f, 64)
		if err != nil || x < 0 {
			return Price{}, false
		}
		n = append(n, x)
	}
	if len(n) < 2 || len(n) > 4 {
		return Price{}, false
	}
	p := Price{Input: n[0], Output: n[1], CacheWrite: n[0] * 1.25, CacheRead: n[0] * 0.1}
	if len(n) > 2 {
		p.CacheWrite = n[2]
	}
	if len(n) > 3 {
		p.CacheRead = n[3]
	}
	return p, true
}

// For finds the price of a model by its longest matching prefix.
func (t Table) For(model string) (Price, bool) {
	best := ""
	for k := range t.rates {
		if strings.HasPrefix(model, k) && len(k) > len(best) {
			best = k
		}
	}
	if best == "" {
		return Price{}, false
	}
	return t.rates[best], true
}

// USD prices tokens by model. partial reports that some model had no price
// and its tokens are left out of the figure.
func (t Table) USD(byModel map[string]transcript.Tokens) (usd float64, partial bool) {
	for model, tk := range byModel {
		if tk.Total() == 0 {
			continue
		}
		p, ok := t.For(model)
		if !ok {
			partial = true
			continue
		}
		usd += (float64(tk.Input)*p.Input + float64(tk.Output)*p.Output +
			float64(tk.CacheCreation)*p.CacheWrite + float64(tk.CacheRead)*p.CacheRead) / 1e6
	}
	return usd, partial
}

// Summary is what one agent, one tree or one mission has used.
type Summary struct {
	Tokens      transcript.Tokens `json:"tokens"`
	Subagents   transcript.Tokens `json:"subagent_tokens"`
	USD         float64           `json:"estimated_usd"`
	Partial     bool              `json:"partial,omitempty"`
	ReportedUSD *float64          `json:"claude_code_reported_usd,omitempty"` // Claude Code's own figure; a cross-check, not summed
}

// Total is every token of the agent and its subagents.
func (s Summary) Total() int64 { return s.Tokens.Total() + s.Subagents.Total() }

// Add sums two summaries. The reported figure does not roll up.
func (s Summary) Add(o Summary) Summary {
	return Summary{
		Tokens:    s.Tokens.Add(o.Tokens),
		Subagents: s.Subagents.Add(o.Subagents),
		USD:       s.USD + o.USD,
		Partial:   s.Partial || o.Partial,
	}
}

func sum(m map[string]transcript.Tokens) (t transcript.Tokens) {
	for _, v := range m {
		t = t.Add(v)
	}
	return
}

// Of summarizes an account.
func (t Table) Of(a transcript.Account) Summary {
	main, p1 := t.USD(a.Main)
	sub, p2 := t.USD(a.Sub)
	s := Summary{Tokens: sum(a.Main), Subagents: sum(a.Sub), USD: main + sub, Partial: p1 || p2}
	if a.Reported != nil {
		r := a.Reported.USD
		s.ReportedUSD = &r
		if a.Reported.Unknown {
			s.Partial = true
		}
	}
	return s
}

// OfWindow summarizes tokens by model that were counted over a time window.
func (t Table) OfWindow(byModel map[string]transcript.Tokens) Summary {
	usd, partial := t.USD(byModel)
	return Summary{Tokens: sum(byModel), USD: usd, Partial: partial}
}

// Tree adds up an agent and everyone it directs, through the tree. own gives
// one session's summary.
func Tree(set *tree.Set, root string, own func(id string) Summary) Summary {
	seen := map[string]bool{}
	var walk func(id string) Summary
	walk = func(id string) Summary {
		if seen[id] {
			return Summary{}
		}
		seen[id] = true
		s := own(id)
		for _, e := range set.Children(id) {
			s = s.Add(walk(e.Child))
		}
		return s
	}
	return walk(root)
}

// Descendants lists everyone the session directs, directly or not.
func Descendants(set *tree.Set, root string) []string {
	var out []string
	seen := map[string]bool{root: true}
	var walk func(id string)
	walk = func(id string) {
		for _, e := range set.Children(id) {
			if seen[e.Child] {
				continue
			}
			seen[e.Child] = true
			out = append(out, e.Child)
			walk(e.Child)
		}
	}
	walk(root)
	sort.Strings(out)
	return out
}

// Tok writes a token count short: 950, 12.4k, 3.2M.
func Tok(n int64) string {
	switch {
	case n >= 1_000_000:
		return trim(fmt.Sprintf("%.1f", float64(n)/1e6)) + "M"
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1000)
	case n >= 1000:
		return trim(fmt.Sprintf("%.1f", float64(n)/1e3)) + "k"
	}
	return strconv.FormatInt(n, 10)
}

func trim(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// Dollars writes an estimate: "≈ $0.42", with a "+" when a model had no
// price and the figure is only part of the cost.
func Dollars(usd float64, partial bool) string {
	s := "≈ $" + money(usd)
	if partial {
		s += "+"
	}
	return s
}

func money(usd float64) string {
	switch {
	case usd >= 100:
		return fmt.Sprintf("%.0f", usd)
	case usd >= 0.995:
		return fmt.Sprintf("%.2f", usd)
	case usd == 0:
		return "0"
	}
	return fmt.Sprintf("%.3f", usd)
}

// Line is the one-line form of a summary: tokens first, then the estimate.
func (s Summary) Line() string {
	out := Tok(s.Tokens.Total()) + " tok"
	if s.Subagents.Total() > 0 {
		out += " (+" + Tok(s.Subagents.Total()) + " subagents)"
	}
	return out + " · " + Dollars(s.USD, s.Partial)
}

// ParseBudget reads "2M", "500k", "1500000" as tokens and "$5" or "$0.50"
// as dollars. Exactly one of the results is positive.
func ParseBudget(s string) (tokens int64, usd float64, err error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if rest, ok := strings.CutPrefix(s, "$"); ok {
		usd, err = strconv.ParseFloat(rest, 64)
		if err != nil || usd <= 0 {
			return 0, 0, fmt.Errorf("budget %q: use $5 for dollars", s)
		}
		return 0, usd, nil
	}
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "m"):
		mult, s = 1e6, strings.TrimSuffix(s, "m")
	case strings.HasSuffix(s, "k"):
		mult, s = 1e3, strings.TrimSuffix(s, "k")
	}
	f, perr := strconv.ParseFloat(s, 64)
	if perr != nil || f <= 0 {
		return 0, 0, fmt.Errorf("budget: use 2M or 500k for tokens, $5 for dollars")
	}
	return int64(f * mult), 0, nil
}
