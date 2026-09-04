package mcpreg

// Tenant distribution — the Workspace half of docs/log/48 §6 / P4.
//
//	tenant_admin ──▶ CP /api/admin/mcp-servers        (control-plane/mcp_server.go)
//	                     │
//	agent ──(AF_MCP_TOKEN)──▶ CP GET /internal/mcp-servers
//	                     │
//	                 ~/.config/agent-fleet/mcp-tenant.json (0600 cache)
//
// Three properties this file exists to guarantee:
//
//   - **fail-open.** An unreachable CP keeps the previous cache. Fail-closed would mean
//     a CP blip silently strips MCP servers out of every member's next session, which is
//     worse than serving a slightly stale set. FetchedAt is surfaced so the Console can
//     show HOW stale (docs/log/48 §6).
//   - **the tenant cannot ship a command.** Every incoming definition is re-validated
//     locally and anything that is not a well-formed remote server is dropped. ADR0031
//     decision 2 is enforced three times over — the CP table has no command columns, the CP
//     API refuses stdio, and this drops it if it ever arrives anyway. The last one is the
//     only check that runs on the machine that would execute the command.
//   - **the member's own values survive.** For a user_secret definition the CP sends
//     header NAMES only; the values come from the local encrypted store (see compose).
//
// The cache is written only when the set actually changed, so a poll that finds nothing
// new does not churn the file (and does not trigger a materialize).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// TenantPollInterval is how often the agent re-pulls the distributed set. Five minutes
// is the same order as the other CP-backed caches: long enough to be invisible in CP
// load, short enough that an admin's edit lands without anyone asking for a refresh.
const TenantPollInterval = 5 * time.Minute

// ErrTenantBridgeOff reports that this deployment has no distribution bridge — the CP
// injects AF_CP_BASE_URL / AF_MCP_TOKEN only when it has a public base URL. It is a
// normal state (single-node dev), not a failure, so callers stay quiet about it.
var ErrTenantBridgeOff = fmt.Errorf("tenant MCP distribution is not configured in this deployment")

// TenantFetchResult describes one poll for the log and for the Console's refresh button.
type TenantFetchResult struct {
	Servers int `json:"servers"`
	// Dropped counts definitions refused by local validation. Non-zero means the CP and
	// this agent disagree about what a legal definition is — worth surfacing, since the
	// affected servers simply will not appear for the member.
	Dropped int `json:"dropped"`
	// Unreadable is the CP's count of rows whose headers would not decrypt, so the
	// Console can say the set is incomplete for a reason that is not the member's fault.
	Unreadable int   `json:"unreadable,omitempty"`
	Changed    bool  `json:"changed"`
	FetchedAt  int64 `json:"fetchedAt"`
}

// FetchTenant pulls the tenant set from the CP and updates the cache. It returns an
// error only for a real failure to talk to the CP; a successful fetch of zero servers is
// a legitimate result (the tenant distributes nothing) and clears the cache.
func FetchTenant() (TenantFetchResult, error) {
	base := strings.TrimRight(os.Getenv("AF_CP_BASE_URL"), "/")
	token := os.Getenv("AF_MCP_TOKEN")
	if base == "" || token == "" {
		return TenantFetchResult{}, ErrTenantBridgeOff
	}
	req, err := http.NewRequest(http.MethodGet, base+"/internal/mcp-servers", nil)
	if err != nil {
		return TenantFetchResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return TenantFetchResult{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return TenantFetchResult{}, fmt.Errorf("CP MCP registry API error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var wire struct {
		Servers    []ServerDef `json:"servers"`
		Unreadable int         `json:"unreadable"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return TenantFetchResult{}, fmt.Errorf("CP MCP registry response is not JSON: %w", err)
	}
	kept, dropped := acceptTenant(wire.Servers)
	res := TenantFetchResult{
		Servers: len(kept), Dropped: dropped, Unreadable: wire.Unreadable,
		FetchedAt: time.Now().Unix(),
	}
	changed, err := saveTenantCache(kept, res.FetchedAt)
	if err != nil {
		return res, err
	}
	res.Changed = changed
	return res, nil
}

// acceptTenant filters a distributed set down to what is safe to use here. A definition
// is dropped when it is not a valid remote server — most importantly a stdio one, which
// would be an admin executing a command inside this container (ADR0031 decision 2).
//
// A user_secret definition arrives with empty header values, which Validate allows (it
// checks shape, not completeness) and Ready() later refuses until the member fills them
// in — so it is kept here and simply stays inert until it is usable.
func acceptTenant(in []ServerDef) (kept []ServerDef, dropped int) {
	for _, d := range in {
		d.Origin = OriginTenant
		d.Enabled = true // the CP only distributes enabled rows; local opt-out is applied in compose
		if d.Transport != TransportHTTP {
			dropped++
			continue
		}
		if err := Validate(d); err != nil {
			dropped++
			continue
		}
		kept = append(kept, d)
	}
	return kept, dropped
}

// saveTenantCache writes the cache only when the server set differs from what is already
// there, and reports whether it wrote. The comparison ignores FetchedAt on purpose:
// stamping a new time on an unchanged set would make every poll look like a change and
// re-materialize every CLI config for nothing.
func saveTenantCache(servers []ServerDef, fetchedAt int64) (bool, error) {
	prev := loadTenantCache()
	same := reflect.DeepEqual(normalizeDefs(prev.Servers), normalizeDefs(servers))
	if same && prev.FetchedAt != 0 {
		// Still refresh the stamp so the Console does not show a set as stale when it was
		// just confirmed — but say "unchanged" so no materialize is triggered.
		_ = writeTenantCache(prev.Servers, fetchedAt)
		return false, nil
	}
	if err := writeTenantCache(servers, fetchedAt); err != nil {
		return false, err
	}
	return !same, nil
}

func writeTenantCache(servers []ServerDef, fetchedAt int64) error {
	b, err := json.Marshal(tenantCache{FetchedAt: fetchedAt, Servers: servers})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(tenantCachePath()), 0o700); err != nil {
		return err
	}
	// 0600: the cache holds the tenant's header values in plaintext (docs/log/48 §5.1 —
	// unavoidable, since the CLIs have to be able to read them; the mitigation is the
	// mode and staying inside home).
	return writeFileAtomic(tenantCachePath(), b, 0o600)
}

// normalizeDefs makes two sets comparable regardless of nil-vs-empty map/slice
// differences that a JSON round-trip introduces.
func normalizeDefs(in []ServerDef) []ServerDef {
	out := make([]ServerDef, 0, len(in))
	for _, d := range in {
		if len(d.Args) == 0 {
			d.Args = nil
		}
		if len(d.Env) == 0 {
			d.Env = nil
		}
		if len(d.Headers) == 0 {
			d.Headers = nil
		}
		if len(d.Kinds) == 0 {
			d.Kinds = nil
		}
		out = append(out, d)
	}
	return out
}
