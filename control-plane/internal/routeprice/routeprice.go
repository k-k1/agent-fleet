// Package routeprice is the Control Plane's price-and-limits table for the models a provided
// route may call (ADR 0129 D4): per model, the largest output a request may ask for, the context
// window, and every billed rate, plus one version for the whole table.
//
// It exists beside the Agent's ledger pricing (workspace/agent usage_price.go, usage_catalog.go)
// rather than reusing it, because the two answer different questions. The ledger re-prices every
// read with whatever catalogue the workspace holds today, and nothing in it is versioned. A budget
// admission has to price a call before it runs, with a table the member cannot edit, and freeze
// which table it used on the call's reservation so a settlement after a price change still adds
// up. Both read the same source: the amazon-bedrock provider of the models.dev catalogue, which
// is the list the ledger prices route.gateway rows with, so on the day the table is generated the
// CP's figures are the ledger's.
//
// Never edit bedrock.json by hand. Regenerate it from a models.dev catalogue file (opencode keeps
// one at ~/.cache/opencode/models.json) and review the diff:
//
//	go test ./internal/routeprice -run TestTable -update-bedrock-prices=$HOME/.cache/opencode/models.json
//
// A model with no row is unpriced: a provided route refuses it, and it is never priced at zero.
package routeprice

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
)

//go:embed bedrock.json
var bedrockJSON []byte

// Rates are USD per million tokens, the unit of every published price list. A zero CacheWrite
// means the source states none and is read through cacheWriteRate, never as free.
type Rates struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read,omitempty"`
	CacheWrite float64 `json:"cache_write,omitempty"`
	// CacheWrite1h is the one-hour cache write. The catalogue states only the five-minute one, so
	// the generator fills Anthropic's published multiplier (2x input) for Anthropic models, where
	// a request can ask for that TTL. Zero where the model has no such write.
	CacheWrite1h float64 `json:"cache_write_1h,omitempty"`
}

// Tier is a long-context price: the rates that apply once a request's input exceeds
// AboveInputTokens.
type Tier struct {
	AboveInputTokens int64 `json:"above_input_tokens"`
	Rates
}

// Model is one row: a Bedrock foundation-model id or system inference profile id, verbatim as a
// request names it (anthropic.claude-…, us.anthropic.claude-…, global.openai.…).
type Model struct {
	ID string `json:"id"`
	Rates
	Tiers         []Tier `json:"tiers,omitempty"`
	ContextWindow int64  `json:"context_window"`
	MaxOutput     int64  `json:"max_output"`
}

// cacheWriteMult is the five-minute cache write against input, the multiplier usage_price.go
// applies when the source states no write rate. Used only for a rate the table leaves empty.
const cacheWriteMult = 1.25

func (r Rates) cacheWriteRate() float64 {
	if r.CacheWrite > 0 {
		return r.CacheWrite
	}
	return r.Input * cacheWriteMult
}

// table is the file's shape. Source and Fetched say where the figures came from, for the reviewer
// of a regenerated table.
type table struct {
	Source  string  `json:"source"`
	Fetched string  `json:"fetched"`
	Models  []Model `json:"models"`
}

var (
	loaded  = mustLoad(bedrockJSON)
	version = func() string {
		sum := sha256.Sum256(bedrockJSON)
		return hex.EncodeToString(sum[:8])
	}()
)

func mustLoad(b []byte) table {
	var t table
	// A table that does not parse is a build defect, not a runtime condition.
	if err := json.Unmarshal(b, &t); err != nil {
		panic("routeprice: bedrock.json: " + err.Error())
	}
	return t
}

// Version identifies the table: a hash of its bytes, so any change to any figure is a new version
// without anyone having to remember to bump one. A reservation records it (D4).
func Version() string { return version }

// Lookup finds a model by its exact id. Matching is verbatim, as the allow-list's is: a spelling
// the table does not carry is unpriced, not folded onto a neighbour.
func Lookup(id string) (Model, bool) {
	i, ok := slices.BinarySearchFunc(loaded.Models, id, func(m Model, id string) int {
		switch {
		case m.ID < id:
			return -1
		case m.ID > id:
			return 1
		}
		return 0
	})
	if !ok {
		return Model{}, false
	}
	m := loaded.Models[i]
	m.Tiers = slices.Clone(m.Tiers)
	return m, true
}

// Models returns every row, sorted by id.
func Models() []Model {
	out := make([]Model, len(loaded.Models))
	for i, m := range loaded.Models {
		m.Tiers = slices.Clone(m.Tiers)
		out[i] = m
	}
	return out
}

// MaxInputRate is the most expensive rate any input token of this model can be billed at: plain
// input, either cache write, in the base price or any long-context tier. Cache reads are cheaper
// than input on every list, and counting them at the input-side maximum is still an upper bound.
func (m Model) MaxInputRate() float64 {
	r := m.Rates.maxInput()
	for _, t := range m.Tiers {
		r = max(r, t.Rates.maxInput())
	}
	return r
}

func (r Rates) maxInput() float64 {
	return max(r.Input, r.cacheWriteRate(), r.CacheWrite1h, r.CacheRead)
}

// MaxOutputRate is the most expensive output rate across the base price and every tier.
func (m Model) MaxOutputRate() float64 {
	r := m.Output
	for _, t := range m.Tiers {
		r = max(r, t.Output)
	}
	return r
}

// UpperBoundMicroUSD is the proven upper bound D4 reserves for one call: the whole context window
// at MaxInputRate plus outputCap tokens at MaxOutputRate, in micro-USD, rounded up. outputCap is
// the request's own output cap; a request without one, or above the model's maximum output, has no
// bound and is refused here.
func (m Model) UpperBoundMicroUSD(outputCap int64) (int64, error) {
	if outputCap <= 0 {
		return 0, fmt.Errorf("model %s: the request carries no output cap", m.ID)
	}
	if outputCap > m.MaxOutput {
		return 0, fmt.Errorf("model %s: output cap %d is above the model's maximum output %d", m.ID, outputCap, m.MaxOutput)
	}
	if m.ContextWindow <= 0 || m.MaxInputRate() <= 0 || m.MaxOutputRate() <= 0 {
		return 0, fmt.Errorf("model %s: the table has no complete limits and prices", m.ID)
	}
	// Tokens times USD per million tokens is micro-USD.
	return int64(math.Ceil(float64(m.ContextWindow)*m.MaxInputRate() + float64(outputCap)*m.MaxOutputRate())), nil
}

// Usage is a call's complete terminal token counts, as the upstream reported them at its end.
// CacheWrite does not say which TTL was written, so it is priced at the dearer of the two.
type Usage struct {
	In, Out, CacheRead, CacheWrite int64
}

// PriceMicroUSD is what u costs at this row's list price, in micro-USD, rounded up. The rates are
// the long-context tier's once the request's whole input (plain, cache read and cache write)
// exceeds the tier's threshold, as Bedrock bills it. A settlement prices complete totals only
// (ADR 0129 D4); anything less settles at the reservation and never reaches this.
func (m Model) PriceMicroUSD(u Usage) int64 {
	r := m.Rates
	total := u.In + u.CacheRead + u.CacheWrite
	for _, t := range m.Tiers {
		if total > t.AboveInputTokens {
			r = t.Rates
		}
	}
	write := max(r.cacheWriteRate(), r.CacheWrite1h)
	return int64(math.Ceil(float64(u.In)*r.Input + float64(u.Out)*r.Output + float64(u.CacheRead)*r.CacheRead +
		float64(u.CacheWrite)*write))
}
