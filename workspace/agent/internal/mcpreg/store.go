package mcpreg

// User-scope CRUD and the effective-registry composition (docs/log/48 §3〜§4).
//
//	effective = builtin(接続済み) ∪ tenant(配布・opt-out を除く) ∪ user
//
// Name collisions resolve to TENANT: an admin-distributed definition must not be
// shadowable by a local one, or "everyone has X" stops being true. A user row that
// collides is kept on disk (nothing is silently deleted) but marked and skipped.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// ErrNotFound is returned for an unknown id.
var ErrNotFound = errors.New("MCP server not found")

// ErrReadOnly is returned when a tenant/builtin row is edited or deleted locally.
var ErrReadOnly = errors.New("this server is not editable (it can only be disabled)")

// ErrNameTaken is returned when a name already exists in the effective registry.
var ErrNameTaken = errors.New("a server with this name is already registered")

func tenantCachePath() string {
	return filepath.Join(paths.AgentConfigDir(), "mcp-tenant.json")
}

func optOutPath() string {
	return filepath.Join(paths.AgentConfigDir(), "mcp-optout.json")
}

// tenantCache is what the CP bridge writes (tenant.go, P4): the distributed set plus
// when it was last confirmed. A missing file means "no tenant servers", which is also
// what a deployment without the bridge looks like.
type tenantCache struct {
	FetchedAt int64       `json:"fetchedAt"`
	Servers   []ServerDef `json:"servers"`
}

func loadTenantCache() tenantCache {
	var c tenantCache
	b, err := os.ReadFile(tenantCachePath())
	if err != nil {
		return c
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return tenantCache{}
	}
	for i := range c.Servers {
		c.Servers[i].Origin = OriginTenant
	}
	return c
}

type optOut struct {
	IDs []string `json:"ids"`
}

func loadOptOut() map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(optOutPath())
	if err != nil {
		return out
	}
	var o optOut
	if err := json.Unmarshal(b, &o); err != nil {
		return out
	}
	for _, id := range o.IDs {
		out[id] = true
	}
	return out
}

func saveOptOut(ids map[string]bool) error {
	list := make([]string, 0, len(ids))
	for id, on := range ids {
		if on {
			list = append(list, id)
		}
	}
	sort.Strings(list)
	b, err := json.Marshal(optOut{IDs: list})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(optOutPath()), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(optOutPath(), b, 0o600)
}

func writeFileAtomic(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".af-tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Registry is one composed snapshot. TenantFetchedAt is surfaced so the Console can
// show how stale the distributed set is (the bridge is fail-open: an unreachable CP
// keeps the last cache rather than dropping everyone's servers).
type Registry struct {
	Servers         []ServerDef `json:"servers"`
	TenantFetchedAt int64       `json:"tenantFetchedAt,omitempty"`
	// Shadowed lists user-scope names that lost a collision against a tenant row.
	Shadowed []string `json:"shadowed,omitempty"`
}

// Load composes the effective registry. Secrets are NOT masked — callers that hand
// the result to the Console must map through Masked first (Handlers do).
func Load() (Registry, error) {
	s, err := secrets.Load()
	if err != nil {
		return Registry{}, err
	}
	return compose(s, loadTenantCache(), loadOptOut()), nil
}

func compose(s *secrets.Data, tc tenantCache, opted map[string]bool) Registry {
	reg := Registry{TenantFetchedAt: tc.FetchedAt}
	taken := map[string]bool{}

	for _, d := range builtinDefs(s) {
		taken[strings.ToLower(d.Name)] = true
		reg.Servers = append(reg.Servers, d)
	}
	for _, d := range tc.Servers {
		d.Origin = OriginTenant
		if d.UserSecret {
			d.Headers = withMemberSecrets(d.Headers, s.MCPSecrets[d.ID])
		}
		if opted[d.ID] {
			d.Enabled = false
		}
		taken[strings.ToLower(d.Name)] = true
		reg.Servers = append(reg.Servers, d)
	}
	for _, d := range s.MCP {
		d.Origin = OriginUser
		if taken[strings.ToLower(d.Name)] {
			reg.Shadowed = append(reg.Shadowed, d.Name)
			continue
		}
		taken[strings.ToLower(d.Name)] = true
		reg.Servers = append(reg.Servers, d)
	}
	sort.SliceStable(reg.Servers, func(i, j int) bool {
		return strings.ToLower(reg.Servers[i].Name) < strings.ToLower(reg.Servers[j].Name)
	})
	return reg
}

// Get resolves one definition from the effective registry, secrets intact.
func Get(id string) (ServerDef, error) {
	reg, err := Load()
	if err != nil {
		return ServerDef{}, err
	}
	for _, d := range reg.Servers {
		if d.ID == id {
			return d, nil
		}
	}
	return ServerDef{}, ErrNotFound
}

// ForAssistant returns the enabled, ready definitions an assistant running on the
// given backend kind may attach, keyed by id for the chat's integration lookup.
// A kind of "" skips the scope filter (used for listing, not for attaching).
func ForAssistant(kind string) (map[string]ServerDef, error) {
	reg, err := Load()
	if err != nil {
		return nil, err
	}
	out := map[string]ServerDef{}
	for _, d := range reg.Servers {
		if !d.Targets.Assistant || !Ready(d) {
			continue
		}
		if kind != "" && !KindAllowed(d, kind) {
			continue
		}
		out[d.ID] = d
	}
	return out, nil
}

// Known reports whether id names something an assistant may hold. It deliberately
// looks past enabled/ready: an assistant keeps its selection while the underlying
// connection is missing or the server is switched off (the pre-registry behavior for
// the builtins — a disconnected PagerDuty left the SRE assistant's id in place), so
// re-connecting restores the tools instead of silently having dropped them on save.
func Known(id string) bool {
	if IsBuiltin(id) {
		return true
	}
	s, err := secrets.Load()
	if err != nil {
		return false
	}
	for _, d := range s.MCP {
		if d.ID == id {
			return true
		}
	}
	for _, d := range loadTenantCache().Servers {
		if d.ID == id {
			return true
		}
	}
	return false
}

// ForSession returns the definitions to materialize into the given agent kind's
// native config, in stable name order.
func ForSession(kind string) ([]ServerDef, error) {
	reg, err := Load()
	if err != nil {
		return nil, err
	}
	var out []ServerDef
	for _, d := range reg.Servers {
		if AppliesTo(d, kind) && Ready(d) {
			out = append(out, d)
		}
	}
	return out, nil
}

// --- user-scope CRUD -------------------------------------------------------

// Create stores a new user-scope definition and returns it (secrets intact).
func Create(in ServerDef) (ServerDef, error) {
	in.Origin = OriginUser
	in.ID = newID()
	in.CreatedAt = time.Now().Unix()
	in.UpdatedAt = in.CreatedAt
	if err := Validate(in); err != nil {
		return ServerDef{}, err
	}
	s, err := secrets.Load()
	if err != nil {
		return ServerDef{}, err
	}
	reg := compose(s, loadTenantCache(), loadOptOut())
	for _, d := range reg.Servers {
		if strings.EqualFold(d.Name, in.Name) {
			return ServerDef{}, fmt.Errorf("%w: %s", ErrNameTaken, in.Name)
		}
	}
	// A brand-new definition has no stored counterpart, so a masked value here is a
	// client bug rather than "keep the old one" — drop it the same way.
	in = MergeSecrets(in, ServerDef{})
	s.MCP = append(s.MCP, in)
	if err := s.Save(); err != nil {
		return ServerDef{}, err
	}
	return in, nil
}

// Update replaces a user-scope definition. Masked secret values keep their stored
// counterparts. Tenant and builtin rows are read-only (use SetEnabled).
func Update(id string, in ServerDef) (ServerDef, error) {
	s, err := secrets.Load()
	if err != nil {
		return ServerDef{}, err
	}
	idx := -1
	for i := range s.MCP {
		if s.MCP[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		if _, gerr := Get(id); gerr == nil {
			return ServerDef{}, ErrReadOnly
		}
		return ServerDef{}, ErrNotFound
	}
	stored := s.MCP[idx]
	in.ID = stored.ID
	in.Origin = OriginUser
	in.CreatedAt = stored.CreatedAt
	in.UpdatedAt = time.Now().Unix()
	if err := Validate(in); err != nil {
		return ServerDef{}, err
	}
	reg := compose(s, loadTenantCache(), loadOptOut())
	for _, d := range reg.Servers {
		if d.ID != id && strings.EqualFold(d.Name, in.Name) {
			return ServerDef{}, fmt.Errorf("%w: %s", ErrNameTaken, in.Name)
		}
	}
	in = MergeSecrets(in, stored)
	s.MCP[idx] = in
	if err := s.Save(); err != nil {
		return ServerDef{}, err
	}
	return in, nil
}

// Delete removes a user-scope definition. Tenant rows can only be opted out of.
func Delete(id string) error {
	s, err := secrets.Load()
	if err != nil {
		return err
	}
	for i := range s.MCP {
		if s.MCP[i].ID == id {
			s.MCP = append(s.MCP[:i], s.MCP[i+1:]...)
			return s.Save()
		}
	}
	if _, gerr := Get(id); gerr == nil {
		return ErrReadOnly
	}
	return ErrNotFound
}

// withMemberSecrets fills a user_secret definition's header values from the member's
// own store (docs/log/48 §5.2). Only the NAMES the tenant distributed are filled: a stale
// local value for a header the tenant has since removed is ignored rather than sent, so
// the tenant stays in control of WHICH headers the server sees.
func withMemberSecrets(names map[string]string, mine map[string]string) map[string]string {
	if len(names) == 0 {
		return nil
	}
	out := make(map[string]string, len(names))
	for k, v := range names {
		if own, ok := mine[k]; ok && own != "" {
			out[k] = own
			continue
		}
		out[k] = v // still empty — Ready() holds the server back until it is filled
	}
	return out
}

// SetTenantSecrets stores the member's own header values for a tenant-distributed
// user_secret definition. Masked values keep what is already stored (the same round-trip
// contract as MergeSecrets), and a name the tenant did not distribute is refused —
// otherwise the local store would accumulate values that nothing can ever use.
//
// It is the ONE write a member has into a tenant row's content, and it stays in the
// member's own encrypted store: the tenant definition itself is never modified.
func SetTenantSecrets(id string, incoming map[string]string) error {
	var def ServerDef
	found := false
	for _, d := range loadTenantCache().Servers {
		if d.ID == id {
			def, found = d, true
			break
		}
	}
	if !found {
		return ErrNotFound
	}
	if !def.UserSecret {
		return ErrReadOnly
	}
	s, err := secrets.Load()
	if err != nil {
		return err
	}
	stored := s.MCPSecrets[id]
	next := map[string]string{}
	for k, v := range incoming {
		if _, ok := def.Headers[k]; !ok {
			continue // not a header this tenant server asks for
		}
		if v == MaskedValue {
			if old, ok := stored[k]; ok {
				next[k] = old
			}
			continue
		}
		if strings.TrimSpace(v) == "" {
			continue // blank clears the value
		}
		if strings.ContainsAny(v, "\r\n") {
			return invalid(CodeHeaderValue, "a header value cannot contain a newline")
		}
		next[k] = v
	}
	if s.MCPSecrets == nil {
		s.MCPSecrets = map[string]map[string]string{}
	}
	if len(next) == 0 {
		delete(s.MCPSecrets, id)
	} else {
		s.MCPSecrets[id] = next
	}
	return s.Save()
}

// SetEnabled toggles a definition. For a user row it flips the stored flag; for a
// tenant row it records a local opt-out, which is the ONLY local edit a member has
// over distributed servers — the escape hatch for a broken one (docs/log/48 §4).
// A builtin's availability is its connection status, so it has no toggle.
func SetEnabled(id string, on bool) error {
	s, err := secrets.Load()
	if err != nil {
		return err
	}
	for i := range s.MCP {
		if s.MCP[i].ID == id {
			s.MCP[i].Enabled = on
			s.MCP[i].UpdatedAt = time.Now().Unix()
			return s.Save()
		}
	}
	for _, d := range loadTenantCache().Servers {
		if d.ID == id {
			opted := loadOptOut()
			opted[id] = !on
			return saveOptOut(opted)
		}
	}
	if IsBuiltin(id) {
		return ErrReadOnly
	}
	return ErrNotFound
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("mcp-%d", time.Now().UnixNano())
	}
	return "mcp-" + hex.EncodeToString(b)
}
