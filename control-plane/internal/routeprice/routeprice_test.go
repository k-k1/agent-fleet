package routeprice

import (
	"encoding/json"
	"flag"
	"math"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var updateFrom = flag.String("update-bedrock-prices", "",
	"rewrite bedrock.json from this models.dev catalogue file (its amazon-bedrock provider)")

// familyID is the ids the table carries: Anthropic and OpenAI models on Bedrock, bare or behind a
// system inference profile prefix (us., eu., global., …). These are the families a harness can
// speak to on a provided route (ADR 0129 H).
var familyID = regexp.MustCompile(`^([a-z][a-z-]*\.)?(anthropic|openai)\.[a-z0-9][a-z0-9.:-]*$`)

// TestTable checks every row of the embedded table, or regenerates it with -update-bedrock-prices.
func TestTable(t *testing.T) {
	if *updateFrom != "" {
		regenerate(t, *updateFrom)
		return
	}
	ms := Models()
	if len(ms) == 0 {
		t.Fatal("the table is empty")
	}
	ids := make([]string, len(ms))
	for i, m := range ms {
		ids[i] = m.ID
		if !familyID.MatchString(m.ID) || strings.HasPrefix(m.ID, "arn:") {
			t.Errorf("%q is not a foundation-model or system inference profile id", m.ID)
		}
		if m.Input <= 0 || m.Output <= 0 || m.ContextWindow <= 0 || m.MaxOutput <= 0 || m.MaxOutput > m.ContextWindow {
			t.Errorf("%s: incomplete row %+v", m.ID, m)
		}
		last := int64(0)
		for _, tr := range m.Tiers {
			if tr.AboveInputTokens <= last || tr.AboveInputTokens >= m.ContextWindow || tr.Input <= 0 || tr.Output <= 0 {
				t.Errorf("%s: tier %+v out of order or incomplete", m.ID, tr)
			}
			last = tr.AboveInputTokens
		}
		if strings.Contains(m.ID, "anthropic.") && m.CacheWrite1h != 2*m.Input {
			t.Errorf("%s: one-hour cache write %v, want 2x input", m.ID, m.CacheWrite1h)
		}
		if got, ok := Lookup(m.ID); !ok || got.ID != m.ID {
			t.Errorf("Lookup(%q) = %+v %v", m.ID, got, ok)
		}
	}
	if !slices.IsSorted(ids) || len(slices.Compact(slices.Clone(ids))) != len(ids) {
		t.Error("ids must be sorted and unique (Lookup searches them)")
	}
	if v := Version(); !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(v) {
		t.Errorf("Version() = %q", v)
	}
	for _, id := range []string{"", "claude-opus-5-5", "US.anthropic.claude-opus-5-5",
		"arn:aws:bedrock:us-east-1:123456789012:application-inference-profile/abc"} {
		if _, ok := Lookup(id); ok {
			t.Errorf("Lookup(%q) found a row; matching must be verbatim", id)
		}
	}
}

// The bound counts the whole context window at the dearest input-side rate (tier, cache writes)
// and the cap at the dearest output rate, rounded up, and refuses what it cannot bound.
func TestUpperBound(t *testing.T) {
	m := Model{ID: "x.anthropic.test", Rates: Rates{Input: 0.1, Output: 0.5, CacheRead: 0.01, CacheWrite: 0.125, CacheWrite1h: 0.2},
		Tiers:         []Tier{{AboveInputTokens: 100_000, Rates: Rates{Input: 0.5, Output: 2.5, CacheWrite: 0.625, CacheWrite1h: 1}}},
		ContextWindow: 1_000_000, MaxOutput: 128_000}
	if r := m.MaxInputRate(); r != 1 {
		t.Errorf("MaxInputRate = %v, want the tier's one-hour write 1", r)
	}
	if r := m.MaxOutputRate(); r != 2.5 {
		t.Errorf("MaxOutputRate = %v, want the tier's 2.5", r)
	}
	got, err := m.UpperBoundMicroUSD(1000)
	if err != nil || got != 1_000_000*1+1000*2.5 {
		t.Errorf("UpperBoundMicroUSD(1000) = %d %v", got, err)
	}
	for _, c := range []int64{0, -1, 128_001} {
		if _, err := m.UpperBoundMicroUSD(c); err == nil {
			t.Errorf("cap %d was bounded", c)
		}
	}
	// A write rate the source leaves empty is 1.25x input, never zero.
	n := Model{ID: "openai.x", Rates: Rates{Input: 2, Output: 10}, ContextWindow: 10, MaxOutput: 5}
	if r := n.MaxInputRate(); r != 2.5 {
		t.Errorf("empty cache write: MaxInputRate = %v, want 2.5", r)
	}
	if got, _ := (Model{ID: "y", Rates: Rates{Input: 0.3, Output: 0.3}, ContextWindow: 1, MaxOutput: 1}).UpperBoundMicroUSD(1); got != 1 {
		t.Errorf("a fraction of a micro-dollar must round up: %d", got)
	}
	if _, err := (Model{ID: "z", MaxOutput: 5}).UpperBoundMicroUSD(1); err == nil {
		t.Error("a row with no prices was bounded")
	}
	// Every real row is boundable at its own maximum output.
	for _, m := range Models() {
		if b, err := m.UpperBoundMicroUSD(m.MaxOutput); err != nil || b <= 0 {
			t.Errorf("%s: %d %v", m.ID, b, err)
		}
	}
}

// A known row, pinned so a regeneration that moves it is seen in review.
func TestKnownRow(t *testing.T) {
	m, ok := Lookup("us.anthropic.claude-opus-5-5")
	if !ok {
		t.Fatal("us.anthropic.claude-opus-5-5 is not in the table")
	}
	if m.Input != 4.4 || m.Output != 22 || m.CacheWrite != 5.5 || m.CacheWrite1h != 8.8 || m.ContextWindow != 1_000_000 || m.MaxOutput != 128_000 {
		t.Errorf("row = %+v", m)
	}
}

type mdCost struct {
	Input      *float64 `json:"input"`
	Output     *float64 `json:"output"`
	CacheRead  float64  `json:"cache_read"`
	CacheWrite float64  `json:"cache_write"`
	Tiers      []struct {
		Input      float64 `json:"input"`
		Output     float64 `json:"output"`
		CacheRead  float64 `json:"cache_read"`
		CacheWrite float64 `json:"cache_write"`
		Tier       struct {
			Type string `json:"type"`
			Size int64  `json:"size"`
		} `json:"tier"`
	} `json:"tiers"`
	Over200k *struct {
		Input      float64 `json:"input"`
		Output     float64 `json:"output"`
		CacheRead  float64 `json:"cache_read"`
		CacheWrite float64 `json:"cache_write"`
	} `json:"context_over_200k"`
}

func regenerate(t *testing.T, path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var cat map[string]struct {
		Models map[string]struct {
			Cost  *mdCost `json:"cost"`
			Limit struct {
				Context int64 `json:"context"`
				Output  int64 `json:"output"`
			} `json:"limit"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &cat); err != nil {
		t.Fatal(err)
	}
	r6 := func(x float64) float64 { return math.Round(x*1e6) / 1e6 }
	out := table{Source: "models.dev, provider amazon-bedrock", Fetched: fi.ModTime().UTC().Format("2006-01-02")}
	for id, row := range cat["amazon-bedrock"].Models {
		c := row.Cost
		if !familyID.MatchString(id) || c == nil || c.Input == nil || c.Output == nil || *c.Input <= 0 || *c.Output <= 0 ||
			row.Limit.Context <= 0 || row.Limit.Output <= 0 {
			continue // unpriced or unbounded: no row, so a provided route refuses it
		}
		anthropic := strings.Contains(id, "anthropic.")
		rates := func(in, o, cr, cw float64) Rates {
			r := Rates{Input: r6(in), Output: r6(o), CacheRead: r6(cr), CacheWrite: r6(cw)}
			if anthropic {
				r.CacheWrite1h = r6(2 * in)
			}
			return r
		}
		m := Model{ID: id, Rates: rates(*c.Input, *c.Output, c.CacheRead, c.CacheWrite),
			ContextWindow: row.Limit.Context, MaxOutput: min(row.Limit.Output, row.Limit.Context)}
		for _, tr := range c.Tiers {
			if tr.Tier.Type != "context" {
				t.Fatalf("%s: tier type %q is not understood; extend the generator before using this catalogue", id, tr.Tier.Type)
			}
			m.Tiers = append(m.Tiers, Tier{AboveInputTokens: tr.Tier.Size, Rates: rates(tr.Input, tr.Output, tr.CacheRead, tr.CacheWrite)})
		}
		if len(m.Tiers) == 0 && c.Over200k != nil {
			o := c.Over200k
			m.Tiers = []Tier{{AboveInputTokens: 200_000, Rates: rates(o.Input, o.Output, o.CacheRead, o.CacheWrite)}}
		}
		slices.SortFunc(m.Tiers, func(a, b Tier) int { return int(a.AboveInputTokens - b.AboveInputTokens) })
		out.Models = append(out.Models, m)
	}
	slices.SortFunc(out.Models, func(a, b Model) int { return strings.Compare(a.ID, b.ID) })
	enc, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("bedrock.json", append(enc, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d models", len(out.Models))
}
