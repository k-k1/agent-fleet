package branchrule

// The tenant layer (ADR 0103 decision 10): the rules a tenant admin keeps in the CP.
//
//	tenant_admin ──▶ CP PUT /api/admin/tenants/{slug}/branch-rules
//	                     │
//	agent ──(AF_BRANCH_RULES_TOKEN)──▶ CP GET /internal/branch-rules   (every 5 minutes)
//	                     │
//	                 ~/.config/agent-fleet/branch-rules-tenant.json   (last copy)
//
// The cache is fail-open, like the tenant MCP servers' mcp-tenant.json: an unreachable CP
// keeps the last copy. Rules only ever advise (decision 8), so a stale copy names a branch
// the way it was named five minutes ago, while dropping it on a CP blip would silently
// switch every launch back to the built-in names.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cpurl"
)

// TenantPollInterval is how often the Agent pulls the tenant rules.
const TenantPollInterval = 5 * time.Minute

// ErrTenantBridgeOff reports a deployment that injects no bridge (no PUBLIC_BASE_URL). It is
// a normal state, so callers stay quiet about it.
var ErrTenantBridgeOff = errors.New("tenant branch rules are not distributed in this deployment")

// TenantRules is the cached copy. FetchedAt is when the CP last answered (Unix seconds),
// 0 when it never has.
type TenantRules struct {
	FetchedAt int64  `json:"fetchedAt"`
	Rules     []Rule `json:"rules"`
}

// TenantFetchResult describes one pull, for the log.
type TenantFetchResult struct {
	Rules int
	// Dropped counts rules the local checks refused. The CP runs the same checks, so
	// non-zero means the two disagree (a different git, an older Agent's vocabulary).
	Dropped int
	Changed bool
}

var tenantMu sync.Mutex

// ReadTenant loads the cache; a missing or unreadable file is no rules.
func ReadTenant(path string) TenantRules {
	tenantMu.Lock()
	defer tenantMu.Unlock()
	return readTenantLocked(path)
}

func readTenantLocked(path string) TenantRules {
	var t TenantRules
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &t) != nil {
		return TenantRules{Rules: []Rule{}}
	}
	if t.Rules == nil {
		t.Rules = []Rule{}
	}
	return t
}

// AcceptTenant keeps the rules that pass the same key and ref-name checks as a user rule
// (decision 3), one by one: a single bad rule must not take the tenant's other rules with it.
// The bare-`*` name refusal is the user store's alone; a tenant `*` rule is the tenant's
// default template.
func AcceptTenant(in []Rule) (kept []Rule, dropped int) {
	kept = []Rule{}
	for _, r := range in {
		if ValidateRules([]Rule{r}, false) != nil {
			dropped++
			continue
		}
		kept = append(kept, r)
	}
	return kept, dropped
}

// FetchTenant pulls the rules from the CP and replaces the cache. It returns an error only
// when the CP could not be asked or did not answer properly; the cache is then untouched.
// An answer with no rules is a real answer and empties it.
func FetchTenant(ctx context.Context, path string) (TenantFetchResult, error) {
	base := cpurl.Request()
	token := os.Getenv("AF_BRANCH_RULES_TOKEN")
	if base == "" || token == "" {
		return TenantFetchResult{}, ErrTenantBridgeOff
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/internal/branch-rules", nil)
	if err != nil {
		return TenantFetchResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return TenantFetchResult{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return TenantFetchResult{}, fmt.Errorf("CP branch rules API error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var wire struct {
		Rules *[]Rule `json:"rules"`
	}
	if err := json.Unmarshal(body, &wire); err != nil || wire.Rules == nil {
		// A body without the list is not "the tenant has no rules": emptying the cache on a
		// proxy's error page would be fail-closed by accident.
		return TenantFetchResult{}, fmt.Errorf("CP branch rules response has no rules list")
	}
	kept, dropped := AcceptTenant(*wire.Rules)
	changed, err := saveTenant(path, TenantRules{FetchedAt: time.Now().Unix(), Rules: kept})
	return TenantFetchResult{Rules: len(kept), Dropped: dropped, Changed: changed}, err
}

// saveTenant writes the copy and reports whether the rules differ from the cached ones. It
// writes even when they do not, so FetchedAt says when the CP last confirmed them.
func saveTenant(path string, t TenantRules) (bool, error) {
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return false, err
	}
	tenantMu.Lock()
	defer tenantMu.Unlock()
	prev := readTenantLocked(path)
	changed := !reflect.DeepEqual(prev.Rules, t.Rules)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	tmp := path + ".af-tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return changed, nil
}

// TenantLayer is the cached rules as layer 3, below the user and above the built-in
// (decision 2). The rules were checked when they were fetched; checking them again here
// would run git once per value on every resolve.
func TenantLayer(t TenantRules) Layer {
	l := Layer{Name: "tenant"}
	for _, r := range t.Rules {
		r.Source = r.Match
		l.Rules = append(l.Rules, r)
	}
	return l
}
