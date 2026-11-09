package memconsol

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memengine"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// Config holds every figure of ADR 0130 decisions 6 and 7. The defaults are the ADR's starting
// values; step 3 tunes them from measurement, so none is a constant of the code. Durations are
// Go duration strings in the file (config.json beside the queue), numbers are plain.
type Config struct {
	// IdleAfter: no session has run a turn for this long before a run may start.
	IdleAfter time.Duration
	// MinInterval: at most one run per this long (counted from the start of the last one).
	MinInterval time.Duration
	// NewObservations: this many open observations ripen a topic.
	NewObservations int
	// MaxWait: an observation that has waited this long ripens its topic.
	MaxWait time.Duration
	// TokenBudget: the model input+output tokens one run may spend (estimated before a call,
	// measured after it when the runner reports usage).
	TokenBudget int
	// MaxObservationsPerTopic bounds the classification calls one topic costs per run; the
	// oldest observations go first and the rest wait for the next run.
	MaxObservationsPerTopic int
	// ProposalTTL: a proposal undecided this long expires (its record is kept).
	ProposalTTL time.Duration
	// NoticeEvery: at most one "proposals are waiting" notice per this long (the hook only).
	NoticeEvery time.Duration
	// CallTimeout bounds one model call.
	CallTimeout time.Duration
}

// DefaultConfig is the ADR's figures: 15 min idle, 3 h between runs, 3 observations, 24 h wait,
// 14 days to expiry, one notice a day.
func DefaultConfig() Config {
	return Config{
		IdleAfter:               15 * time.Minute,
		MinInterval:             3 * time.Hour,
		NewObservations:         3,
		MaxWait:                 24 * time.Hour,
		TokenBudget:             60000,
		MaxObservationsPerTopic: 5,
		ProposalTTL:             14 * 24 * time.Hour,
		NoticeEvery:             24 * time.Hour,
		CallTimeout:             2 * time.Minute,
	}
}

type configFile struct {
	IdleAfter               string `json:"idleAfter,omitempty"`
	MinInterval             string `json:"minInterval,omitempty"`
	NewObservations         int    `json:"newObservations,omitempty"`
	MaxWait                 string `json:"maxWait,omitempty"`
	TokenBudget             int    `json:"tokenBudget,omitempty"`
	MaxObservationsPerTopic int    `json:"maxObservationsPerTopic,omitempty"`
	ProposalTTL             string `json:"proposalTTL,omitempty"`
	NoticeEvery             string `json:"noticeEvery,omitempty"`
	CallTimeout             string `json:"callTimeout,omitempty"`
}

// LoadConfig reads config.json from the consolidation directory over the defaults. A value that
// does not parse or is not positive keeps its default, and the file's absence is the defaults.
func LoadConfig() Config {
	c := DefaultConfig()
	b, err := os.ReadFile(filepath.Join(Dir(), "config.json"))
	if err != nil {
		return c
	}
	var f configFile
	if json.Unmarshal(b, &f) != nil {
		return c
	}
	dur := func(s string, into *time.Duration) {
		if d, err := time.ParseDuration(s); s != "" && err == nil && d > 0 {
			*into = d
		}
	}
	num := func(n int, into *int) {
		if n > 0 {
			*into = n
		}
	}
	dur(f.IdleAfter, &c.IdleAfter)
	dur(f.MinInterval, &c.MinInterval)
	dur(f.MaxWait, &c.MaxWait)
	dur(f.ProposalTTL, &c.ProposalTTL)
	dur(f.NoticeEvery, &c.NoticeEvery)
	dur(f.CallTimeout, &c.CallTimeout)
	num(f.NewObservations, &c.NewObservations)
	num(f.TokenBudget, &c.TokenBudget)
	num(f.MaxObservationsPerTopic, &c.MaxObservationsPerTopic)
	return c
}

// Dir is the consolidation state directory under the memory store's work directory (the same
// directory the exported 0117 queue lives in, which this package only reads).
func Dir() string { return filepath.Join(paths.ClaudeConfigDir(), "af-memory.work", "consolidation") }

//go:embed models.default.json
var defaultModels []byte

// LoadEngineConfig is the role→model mapping and the runner templates: models.json in the
// consolidation directory when the member put one there, the embedded default otherwise. The
// model identifiers are data, never code (a lineup change is an edit of this file).
func LoadEngineConfig() (memengine.Config, error) {
	b := defaultModels
	if o, err := os.ReadFile(filepath.Join(Dir(), "models.json")); err == nil {
		b = o
	}
	var c memengine.Config
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("memconsol: models config: %w", err)
	}
	if c.Runners == nil {
		c.Runners = map[string]memengine.RunnerSpec{}
	}
	for k, v := range memengine.DefaultRunners() {
		if _, ok := c.Runners[k]; !ok {
			c.Runners[k] = v
		}
	}
	return c, nil
}
