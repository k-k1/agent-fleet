package session

// The per-session spend budget (#1054), as values and predicates over Meta.
//
// It lives here for the same reason the stop-after-turn arm does: sessionx sets the cap and
// halts, chatx's reconciler drives the sweep, the Agent's main package prices the transcript,
// and all three have to agree on what "over budget" means without seeing each other.

import "math"

// SpendCapHardFactor is the multiple of the cap at which a session is halted at once, even in
// the middle of a turn. Below it the stop waits for the turn to end, because killing a turn
// loses the answer being written (docs/log/75 §75.10); one turn that alone runs to twice the
// budget is the runaway the budget exists for, and waiting for it to end could take hours.
const SpendCapHardFactor = 2.0

// SpendCapMaxUSD bounds what the API accepts. Not a policy: a typo of a few extra zeros should
// read as an error, not as "no budget".
const SpendCapMaxUSD = 100_000

// Spend is a session's spend as the budget sees it.
//
// USD sums, per logical turn, the cost the CLI itself reported when it reported one, else the
// list-price estimate of the turn's tokens — never both for one turn. Turns stamped before the
// session's own CreatedAt are not counted: a fork starts from a copy of its source's history,
// and charging that copy to the fork would trip its budget on the first tick.
type Spend struct {
	USD float64 `json:"spendUsd"`
	// Priced: at least one turn was priced (reported or estimated). False with Unpriced also
	// false means nothing measurable was recorded at all — a kind with no token counts.
	Priced bool `json:"priced"`
	// Unpriced: some turn had tokens but no price (unknown model, a kind with no catalog
	// provider). Those tokens are not in USD, so the budget under-counts them; the UI says so.
	Unpriced bool `json:"unpriced,omitempty"`
	// Reported: at least one turn's cost came from the CLI rather than the price table.
	Reported bool `json:"reported,omitempty"`
}

// SpendCapDefaultPref is the user's default budget for new sessions (ui-prefs
// sessionSpendCapUsd), installed by uiprefs, which depends on this package and so cannot be
// imported from it. Nil or a non-positive answer means "no default".
var SpendCapDefaultPref func() float64

// NormalizeSpendCap clamps a requested cap: negative, NaN and Inf read as "no cap" (0), and
// cents are the finest unit kept.
func NormalizeSpendCap(usd float64) (float64, bool) {
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 || usd > SpendCapMaxUSD {
		return 0, false
	}
	return math.Round(usd*100) / 100, true
}

// DefaultSpendCap is the cap a new session gets when its launch named none.
func DefaultSpendCap() float64 {
	if SpendCapDefaultPref == nil {
		return 0
	}
	v, ok := NormalizeSpendCap(SpendCapDefaultPref())
	if !ok {
		return 0
	}
	return v
}

// OverSpendCap reports whether spend has reached the cap. A session with no cap never is.
func OverSpendCap(m Meta, usd float64) bool {
	return m.SpendCapUSD > 0 && usd >= m.SpendCapUSD
}

// OverHardSpendCap reports whether spend has reached SpendCapHardFactor × the cap.
func OverHardSpendCap(m Meta, usd float64) bool {
	return m.SpendCapUSD > 0 && usd >= m.SpendCapUSD*SpendCapHardFactor
}
