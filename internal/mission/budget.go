package mission

// RunCost is what a run used, frozen when the run ended so the figure
// outlives the transcripts it came from.
type RunCost struct {
	Tokens  int64   `json:"tokens"`
	USD     float64 `json:"usd"`
	Partial bool    `json:"partial,omitempty"`
}

// Budget is an optional ceiling on what a mission's runs may use together,
// in tokens or in estimated dollars. The zero value is no budget.
type Budget struct {
	Tokens int64   `json:"tokens,omitempty"`
	USD    float64 `json:"usd,omitempty"`
	// Asked is set once the ceiling was crossed and the human was asked, so
	// the question comes up once; Waived once they chose to go on.
	Asked  bool `json:"asked,omitempty"`
	Waived bool `json:"waived,omitempty"`
}

// Set reports whether a ceiling is set.
func (b Budget) Set() bool { return b.Tokens > 0 || b.USD > 0 }

// Crossed reports whether the usage is at or over the ceiling.
func (b Budget) Crossed(tokens int64, usd float64) bool {
	return (b.Tokens > 0 && tokens >= b.Tokens) || (b.USD > 0 && usd >= b.USD)
}

// SetBudget replaces the ceiling; a new ceiling is asked about afresh.
func (m *Mission) SetBudget(tokens int64, usd float64) { m.Budget = Budget{Tokens: tokens, USD: usd} }
