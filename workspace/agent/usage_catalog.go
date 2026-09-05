package main

// The price catalog (docs/log/46 §5-c). The built-in table (usage_price.go) carries only
// Anthropic's primary prices, which left codex / opencode consumption unpriced. This fills
// that in.
//
// Do not grow a price table by hand: the models.dev catalog is already inside the workspace
// (`~/.cache/opencode/models.json` — 212 providers / 7,493 models, kept up to date by
// opencode, in $/1M like ours, MIT licensed). Measured: the share of this ledger's
// consumption that can be priced goes from 92.7% with the built-in table alone to 99.3% with
// the catalog as well (the rest is `<synthetic>` and models upstream has no price for).
//
// The same model id appears under 20-34 providers at different prices (resellers and routers:
// one reseller's claude-opus-4-8 is $1.5/$9.25 against the primary $5/$25). Looking up by name
// alone quietly produces a false amount, so the provider to look up is decided by the ledger's
// `kind` (claude→anthropic, codex→openai, opencode→opencode = the gateway the consumption
// actually went through, copilot→github-copilot). Only known providers are indexed, and a
// provider absent from the order tables is never consulted.
//
// This depends on an upstream internal file, so it stays strictly best-effort: a missing,
// reshaped or corrupt file only degrades to "no catalog" and breaks neither the estimate nor
// the series. Writing a check against an upstream file name has broken before (memory:
// copilot-session-db-moved).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// usageCatalogMaxBytes caps what is read as a catalog, so that an upstream file growing
// without bound does not drag this process's memory with it (the shared host is
// memory-constrained).
const usageCatalogMaxBytes = 64 << 20

// usageCatalogProviders is, per kind, the priority order of the providers the consumption
// actually went through. A kind that is absent here consults usageCatalogFallback only.
var usageCatalogProviders = map[string][]string{
	session.KindClaude: {"anthropic"},
	session.KindCodex:  {"openai"},
	// opencode runs through a gateway, so what the user actually pays is opencode's price
	// (decided 2026-08-31).
	session.KindOpencode: {"opencode"},
	session.KindCopilot:  {"github-copilot"},
	// cursor has no provider on models.dev. kiro and agy are resolved from the model name.
	session.KindKiro: {"amazon-bedrock", "anthropic"},
	session.KindAgy:  {"google", "google-vertex"},
}

// usageCatalogFallback is the order of primary providers to consult when the kind does not
// decide one. Resellers and routers (openrouter / nano-gpt / …) are kept out: their prices
// differ from the primary source.
var usageCatalogFallback = []string{
	"anthropic", "openai", "google", "google-vertex", "amazon-bedrock",
	"alibaba", "zhipuai", "moonshotai", "deepseek", "xai", "mistral", "sakana",
	"minimax", "cohere", "github-copilot", "opencode",
}

// usageCatalogIndexed is the set of providers to index (the union of the order tables).
func usageCatalogIndexed() map[string]bool {
	m := make(map[string]bool, len(usageCatalogFallback)+len(usageCatalogProviders))
	for _, p := range usageCatalogFallback {
		m[p] = true
	}
	for _, ps := range usageCatalogProviders {
		for _, p := range ps {
			m[p] = true
		}
	}
	return m
}

// usageCatalog is a loaded catalog. A price key is "provider/normalized model name".
type usageCatalog struct {
	price   map[string]usagePrice
	origin  string    // opencode | file | env (Console owns the wording; no path is exposed)
	modTime time.Time // the source file's mtime = which point in time these prices are from
	models  int
}

// usageCatalogStatTTL is how often the file is looked at again. A price is looked up thousands
// of times while aggregating one series, so a stat per lookup would be thousands of wasted
// syscalls. A catalog update taking effect a few seconds late does no harm: opencode writes it
// on a schedule unrelated to these reads.
const usageCatalogStatTTL = 5 * time.Second

var (
	usageCatalogMu      sync.Mutex
	usageCatalogCache   *usageCatalog
	usageCatalogSrc     string    // the file that was read
	usageCatalogAt      time.Time // its mtime at that point (a change means re-read)
	usageCatalogChecked time.Time // when it was last stat'ed (no lookup within the TTL)
)

// usageCatalogFiles is the search order; the first readable one wins.
//   - AF_USAGE_CATALOG: for tests, and for an operator to point at their own snapshot
//   - usageDir()/catalog.json: a place to put one in a workspace that does not use opencode
//   - opencode's cache: read only, never updated from here
func usageCatalogFiles() []struct{ path, origin string } {
	out := []struct{ path, origin string }{}
	if v := strings.TrimSpace(os.Getenv("AF_USAGE_CATALOG")); v != "" {
		out = append(out, struct{ path, origin string }{v, "env"})
	}
	out = append(out, struct{ path, origin string }{filepath.Join(usagex.Dir(), "catalog.json"), "file"})
	cache := os.Getenv("XDG_CACHE_HOME")
	if cache == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cache = filepath.Join(home, ".cache")
		}
	}
	if cache != "" {
		out = append(out, struct{ path, origin string }{filepath.Join(cache, "opencode", "models.json"), "opencode"})
	}
	return out
}

// loadUsageCatalog returns the first catalog that could be read, or nil. It is reused until
// the mtime changes.
func loadUsageCatalog() *usageCatalog {
	usageCatalogMu.Lock()
	defer usageCatalogMu.Unlock()
	// Within the TTL, reuse the previous conclusion — catalog or no catalog alike.
	if !usageCatalogChecked.IsZero() && time.Since(usageCatalogChecked) < usageCatalogStatTTL {
		return usageCatalogCache
	}
	usageCatalogChecked = time.Now()
	for _, f := range usageCatalogFiles() {
		st, err := os.Stat(f.path)
		if err != nil || st.IsDir() || st.Size() == 0 || st.Size() > usageCatalogMaxBytes {
			continue
		}
		if usageCatalogCache != nil && usageCatalogSrc == f.path && usageCatalogAt.Equal(st.ModTime()) {
			return usageCatalogCache
		}
		b, err := os.ReadFile(f.path)
		if err != nil {
			continue
		}
		cat := parseUsageCatalog(b, f.origin, st.ModTime())
		if cat == nil {
			continue // wrong shape or corrupt: treat this file as if it were not there
		}
		usageCatalogCache, usageCatalogSrc, usageCatalogAt = cat, f.path, st.ModTime()
		return cat
	}
	usageCatalogCache, usageCatalogSrc, usageCatalogAt = nil, "", time.Time{}
	return nil
}

// catalogFile is the shape of just the part of models.dev's api.json that is used here, so
// that fields upstream adds or removes elsewhere leave this working (the decoder skips them).
type catalogFile map[string]struct {
	Models map[string]struct {
		Cost *struct {
			Input      *float64 `json:"input"`
			Output     *float64 `json:"output"`
			CacheRead  *float64 `json:"cache_read"`
			CacheWrite *float64 `json:"cache_write"`
			// tiers / context_over_200k (the long-context surcharge) are not used: the ledger
			// has no per-turn input length to match them against, so the lowest tier is
			// assumed and the estimate can come out low (docs/log/46 §5-c).
		} `json:"cost"`
	} `json:"models"`
}

// parseUsageCatalog extracts only the providers that are indexed. It returns nil when it got
// none, so that a file that parsed but holds an unknown shape is not called "a catalog".
func parseUsageCatalog(b []byte, origin string, mod time.Time) *usageCatalog {
	var f catalogFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil
	}
	want := usageCatalogIndexed()
	price := map[string]usagePrice{}
	// When normalized names collide inside one provider, take the lexicographically smaller
	// model id, so map iteration order cannot make the result wander (measured on 3.3M: an
	// amount that changes on every read is not acceptable).
	winner := map[string]string{}
	for pid, p := range f {
		if !want[pid] {
			continue
		}
		for mid, m := range p.Models {
			if m.Cost == nil || m.Cost.Input == nil || m.Cost.Output == nil {
				continue // upstream has no price for this model, so it stays unpriced
			}
			key := pid + "/" + usageNormalizeModel(mid)
			if cur, ok := winner[key]; ok && cur <= mid {
				continue
			}
			winner[key] = mid
			price[key] = usagePrice{
				In: *m.Cost.Input, Out: *m.Cost.Output,
				CacheRead: derefPrice(m.Cost.CacheRead), CacheWrite: derefPrice(m.Cost.CacheWrite),
			}
		}
	}
	if len(price) == 0 {
		return nil
	}
	return &usageCatalog{price: price, origin: origin, modTime: mod, models: len(price)}
}

func derefPrice(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// usageCatalogOrder is the provider order to consult for a kind: the kind's own providers,
// then the primary ones.
func usageCatalogOrder(kind string) []string {
	head := usageCatalogProviders[kind]
	out := make([]string, 0, len(head)+len(usageCatalogFallback))
	seen := map[string]bool{}
	for _, p := range append(append([]string{}, head...), usageCatalogFallback...) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// usageCatalogLookup looks a price up in the catalog. ref says whose provider's value was
// taken and is shown on screen: an amount whose price source is unknown cannot be checked.
func usageCatalogLookup(kind, model string) (usagePrice, string, bool) {
	cat := loadUsageCatalog()
	if cat == nil {
		return usagePrice{}, "", false
	}
	base := usageNormalizeModel(model)
	if base == "" {
		return usagePrice{}, "", false
	}
	for _, pid := range usageCatalogOrder(kind) {
		if p, ok := cat.price[pid+"/"+base]; ok {
			return p, pid + "/" + base, true
		}
	}
	return usagePrice{}, "", false
}

// usageCatalogMeta is the catalog declaration carried in the response, nil when there is no
// catalog. It always states the fetch date: because nothing is stored, a catalog update also
// changes past estimates, and moving the amounts without saying which point in time they are
// priced from is close to lying in silence.
type usageCatalogMeta struct {
	Origin  string `json:"origin"`            // opencode | file | env
	Models  int    `json:"models"`            // number of indexed models (primary providers)
	Fetched string `json:"fetched,omitempty"` // the source file's mtime (RFC3339)
}

func usageCatalogInfo() *usageCatalogMeta {
	cat := loadUsageCatalog()
	if cat == nil {
		return nil
	}
	m := &usageCatalogMeta{Origin: cat.origin, Models: cat.models}
	if !cat.modTime.IsZero() {
		m.Fetched = cat.modTime.UTC().Format(time.RFC3339)
	}
	return m
}

// keys orders the index keys deterministically, for tests and debugging.
func (c *usageCatalog) keys() []string {
	out := make([]string, 0, len(c.price))
	for k := range c.price {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
