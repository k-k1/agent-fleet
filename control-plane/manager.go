package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// manager owns the set of per-membership Workspace runtimes. As of P3-2 (docs/14)
// identity↔tenant is many-to-many: the gateway email identifies the person
// (identity), the active tenant is chosen explicitly per request (X-AF-Tenant)
// and validated against the person's memberships, and a Workspace (container) is
// resolved per membership (= identity × tenant, fully isolated). The DB
// (MetadataStore) is the source of truth; `rts` is an in-memory cache of built
// runtimes keyed by membership id. The Agent contract is unchanged.
type manager struct {
	mu    sync.Mutex
	rts   map[string]cachedRT // cache keyed by membership id; DB is source of truth
	store Store
	conns *connRegistry // P3-9: live activity/attachment tracking for idle-stop

	// template fields shared by every runtime
	image      string
	dataRoot   string
	agentHost  string
	memory     string
	sessionCmd string
	extraEnv   []string

	portBase int

	// user resolution (AuthGateway port). authMode: "dev" | "proxy".
	authMode    string
	devUser     string
	emailHeader string

	// provisioning policy (docs/14): "auto" (gateway-trusted auto-provision into
	// the default tenant) | "invite" (deny unknown identities). superAdmins are
	// emails granted identity.role=super_admin (deployment-wide).
	provisionMode string
	superAdmins   map[string]bool

	// at-rest encryption (P3-3). master32 = SHA-256 of AF_MASTER_KEY (nil in dev).
	// custodian wraps/unwraps per-workspace DEKs; nil in dev (no encryption).
	master32  []byte
	custodian KeyCustodian
}

// apiError carries an HTTP status + machine code for handlers to return.
type apiError struct {
	status  int
	code    string
	message string
}

func internalErr(err error) *apiError {
	return &apiError{status: http.StatusInternalServerError, code: "internal", message: err.Error()}
}

// legacyDEK returns the raw DEK the Phase 2 / pre-P3-3 path derived as
// HMAC(master, userKey). It's used as the *first* DEK for a workspace so any
// existing secrets.enc (encrypted with this exact key) keeps decrypting after the
// move to envelope storage — no re-encryption.
func (m *manager) legacyDEK(userKey string) []byte {
	mac := hmac.New(sha256.New, m.master32)
	mac.Write([]byte(userKey))
	return mac.Sum(nil)
}

// resolveDEK returns the hex DEK to inject as AF_SECRET_KEY for a workspace,
// stored wrapped by the tenant KEK (docs/15 P3-3). On first use it mints the
// legacy DEK, wraps it via the custodian, and persists it. Returns "" in dev
// (no master/custodian) so the Agent stores secrets in plaintext as before.
func (m *manager) resolveDEK(ctx context.Context, ws Workspace, userKey string) (string, error) {
	if len(m.master32) == 0 || m.custodian == nil {
		return "", nil
	}
	keyRef := ws.TenantID
	ct, kr, ok, err := m.store.GetWrappedDEK(ctx, ws.ID)
	if err != nil {
		return "", err
	}
	var dek []byte
	if ok {
		if dek, err = m.custodian.Unwrap(ctx, kr, ct); err != nil {
			return "", err
		}
	} else {
		dek = m.legacyDEK(userKey) // preserve existing secrets.enc
		if ct, err = m.custodian.Wrap(ctx, keyRef, dek); err != nil {
			return "", err
		}
		if err := m.store.PutWrappedDEK(ctx, ws.ID, ct, keyRef); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(dek), nil
}

// identity is what the AuthGateway resolves a request to.
type identity struct{ key, email string }

func (m *manager) resolveIdentity(r *http.Request) identity {
	// proxy: trust the upstream oauth2-proxy header. oauth: authGate has verified
	// the Google session and set the same header (stripping any inbound value).
	if m.authMode == "proxy" || m.authMode == "oauth" {
		e := r.Header.Get(m.emailHeader)
		return identity{key: sanitizeUser(e), email: e}
	}
	return identity{key: m.devUser}
}

func (m *manager) resolveUser(r *http.Request) string { return m.resolveIdentity(r).key }

func (m *manager) roleHintFor(email string) string {
	if email != "" && m.superAdmins[strings.ToLower(email)] {
		return "super_admin"
	}
	return ""
}

// tenantAdminFor reports whether ident may administer tenant tID: a deployment
// super_admin (any tenant) or a tenant_admin member of that specific tenant.
// This is the per-tenant admin gate; deployment-wide actions (create tenant,
// tenant quotas, clean-home, host stats, role grants) stay super_admin-only.
func (m *manager) tenantAdminFor(ctx context.Context, ident Identity, tID string) bool {
	if ident.Role == "super_admin" {
		return true
	}
	mem, ok, err := m.store.GetMembership(ctx, ident.ID, tID)
	return err == nil && ok && mem.Role == "tenant_admin"
}

// identityFor upserts and returns the caller's identity (used by /api/tenants and
// admin RBAC). 401 if the gateway gave no identity.
func (m *manager) identityFor(ctx context.Context, r *http.Request) (Identity, *apiError) {
	id := m.resolveIdentity(r)
	if id.key == "" {
		return Identity{}, &apiError{http.StatusUnauthorized, "unauthenticated", "no gateway identity"}
	}
	ident, err := m.store.UpsertIdentity(ctx, id.email, id.key, m.roleHintFor(id.email))
	if err != nil {
		return Identity{}, internalErr(err)
	}
	return ident, nil
}

// membershipsFor returns the caller's memberships, auto-provisioning a default
// membership when the policy allows and the person has none.
func (m *manager) membershipsFor(ctx context.Context, ident Identity) ([]MembershipView, *apiError) {
	ms, err := m.store.ListMemberships(ctx, ident.ID)
	if err != nil {
		return nil, internalErr(err)
	}
	if len(ms) == 0 {
		if m.provisionMode == "invite" {
			return nil, &apiError{http.StatusForbidden, "not_provisioned", "no tenant membership; ask an administrator"}
		}
		t, err := m.store.EnsureDefaultTenant(ctx)
		if err != nil {
			return nil, internalErr(err)
		}
		if _, err := m.store.EnsureMembership(ctx, ident.ID, t.ID, "member"); err != nil {
			return nil, internalErr(err)
		}
		ms, err = m.store.ListMemberships(ctx, ident.ID)
		if err != nil {
			return nil, internalErr(err)
		}
	}
	return ms, nil
}

// cachedRT memoizes a built runtime + its workspace record per membership.
type cachedRT struct {
	rt *dockerRuntime
	ws Workspace
}

// resolved is the full per-request resolution: runtime + workspace record +
// identity + selected membership. Handlers needing tenant/quota context use this.
type resolved struct {
	rt    *dockerRuntime
	ws    Workspace
	ident Identity
	mv    MembershipView
}

// resolveFull maps a request's identity + selected tenant to its runtime and
// records, creating the workspace on first use. tenantSel is the X-AF-Tenant
// value (slug or tenant id); empty means "default selection".
func (m *manager) resolveFull(ctx context.Context, key, email, tenantSel string) (*resolved, *apiError) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ident, err := m.store.UpsertIdentity(ctx, email, key, m.roleHintFor(email))
	if err != nil {
		return nil, internalErr(err)
	}
	ms, aerr := m.membershipsFor(ctx, ident)
	if aerr != nil {
		return nil, aerr
	}
	mv, aerr := selectMembership(ms, tenantSel)
	if aerr != nil {
		return nil, aerr
	}
	return m.buildResolvedLocked(ctx, ident, mv)
}

// selectMembership picks the active membership for a request. tenantSel (slug or
// id) is required when the person belongs to more than one tenant; a single
// membership is auto-selected.
func selectMembership(ms []MembershipView, tenantSel string) (MembershipView, *apiError) {
	switch {
	case tenantSel != "":
		for _, x := range ms {
			if x.TenantSlug == tenantSel || x.TenantID == tenantSel {
				return x, nil
			}
		}
		return MembershipView{}, &apiError{http.StatusForbidden, "forbidden_tenant", "not a member of tenant " + tenantSel}
	case len(ms) == 1:
		return ms[0], nil
	default:
		return MembershipView{}, &apiError{http.StatusConflict, "tenant_selection_required", "specify X-AF-Tenant; you belong to multiple tenants"}
	}
}

// buildResolvedLocked maps an (identity, membership) to its runtime + workspace,
// creating the workspace on first use. Assumes m.mu is held.
func (m *manager) buildResolvedLocked(ctx context.Context, ident Identity, mv MembershipView) (*resolved, *apiError) {
	if c, ok := m.rts[mv.MembershipID]; ok {
		return &resolved{rt: c.rt, ws: c.ws, ident: ident, mv: mv}, nil
	}
	ws, ok, err := m.store.GetWorkspaceByMembership(ctx, mv.MembershipID)
	if err != nil {
		return nil, internalErr(err)
	}
	if !ok {
		ws, err = m.createWorkspace(ctx, mv, ident.UserKey)
		if err != nil {
			return nil, internalErr(err)
		}
	}
	dekHex, err := m.resolveDEK(ctx, ws, ident.UserKey)
	if err != nil {
		return nil, internalErr(err)
	}
	rt := m.runtimeFor(ws, dekHex)
	m.rts[mv.MembershipID] = cachedRT{rt: rt, ws: ws}
	return &resolved{rt: rt, ws: ws, ident: ident, mv: mv}, nil
}

// resolveByMembership resolves a runtime from a PAT's stored identity+membership
// (the MCP path, which has no gateway headers). The membership must still be an
// active membership of the identity — so a revoked membership 403s here.
func (m *manager) resolveByMembership(ctx context.Context, identityID, membershipID string) (*resolved, *apiError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ident, ok, err := m.store.GetIdentityByID(ctx, identityID)
	if err != nil {
		return nil, internalErr(err)
	}
	if !ok {
		return nil, &apiError{http.StatusUnauthorized, "unauthenticated", "identity not found"}
	}
	ms, err := m.store.ListMemberships(ctx, identityID)
	if err != nil {
		return nil, internalErr(err)
	}
	for _, mv := range ms {
		if mv.MembershipID == membershipID {
			return m.buildResolvedLocked(ctx, ident, mv)
		}
	}
	return nil, &apiError{http.StatusForbidden, "forbidden_tenant", "membership not active"}
}

// resolve returns just the runtime (proxy/terminal handlers).
func (m *manager) resolve(ctx context.Context, key, email, tenantSel string) (*dockerRuntime, *apiError) {
	res, aerr := m.resolveFull(ctx, key, email, tenantSel)
	if aerr != nil {
		return nil, aerr
	}
	return res.rt, nil
}

// tenantLimits is the parsed tenant.limits JSON (0 = unlimited for the int
// quotas). The idle timeouts are human-editable duration strings (e.g. "30m"):
// "" => fall back to the deployment default (env); "0" => idle-stop disabled for
// this tenant. See idleTimeout for resolution.
type tenantLimits struct {
	MaxWorkspaces int `json:"max_workspaces"`
	MaxSessions   int `json:"max_sessions"`
	// P3-9 idle-stop (docs/19): per-tenant, super_admin-editable.
	SessionIdleTimeout string `json:"session_idle_timeout,omitempty"` // tier-1: idle claude -> halt
	WSIdleTimeout      string `json:"ws_idle_timeout,omitempty"`      // tier-2: cold workspace -> docker stop
}

func parseLimits(s string) tenantLimits {
	var l tenantLimits
	if s != "" {
		_ = json.Unmarshal([]byte(s), &l)
	}
	return l
}

// idleTimeout resolves a tenant idle-timeout field to a duration and whether
// idle-stop is enabled. Empty => the deployment default (def); an explicit "0"
// (or any non-positive value) disables idle-stop for that tenant; a bad string
// falls back to the default rather than silently disabling.
func idleTimeout(tenantVal string, def time.Duration) (d time.Duration, enabled bool) {
	d = def
	if tenantVal != "" {
		if p, err := time.ParseDuration(tenantVal); err == nil {
			d = p
		}
	}
	return d, d > 0
}

// countRunningInTenant counts running Workspace containers in a tenant via docker
// (authoritative — independent of the DB state column).
func (m *manager) countRunningInTenant(ctx context.Context, tenantID string) (int, error) {
	wss, err := m.store.ListWorkspaces(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ws := range wss {
		if (&dockerRuntime{name: ws.ContainerName}).state(ctx) == "running" {
			n++
		}
	}
	return n, nil
}

// workspaceStateByMembership returns a membership's container name + live docker
// state ("none" if no workspace record) for the admin UI.
func (m *manager) workspaceStateByMembership(ctx context.Context, membershipID string) (string, string) {
	ws, ok, err := m.store.GetWorkspaceByMembership(ctx, membershipID)
	if err != nil || !ok {
		return "", "none"
	}
	return ws.ContainerName, (&dockerRuntime{name: ws.ContainerName}).state(ctx)
}

// stopWorkspaceByMembership force-stops a member's workspace (admin action).
func (m *manager) stopWorkspaceByMembership(ctx context.Context, membershipID string) error {
	ws, ok, err := m.store.GetWorkspaceByMembership(ctx, membershipID)
	if err != nil || !ok {
		return err
	}
	if err := (&dockerRuntime{name: ws.ContainerName, network: ws.Network}).stop(ctx); err != nil {
		return err
	}
	return m.store.SetWorkspaceState(ctx, ws.ID, "stopped")
}

// cleanHomeByMembership wipes a member's workspace home except auth/connection state
// (admin action). Stops the container first and leaves it stopped; the home is
// recreated on the next start.
func (m *manager) cleanHomeByMembership(ctx context.Context, membershipID string) error {
	ws, ok, err := m.store.GetWorkspaceByMembership(ctx, membershipID)
	if err != nil || !ok {
		return err
	}
	_ = (&dockerRuntime{name: ws.ContainerName, network: ws.Network}).stop(ctx) // best-effort
	if err := cleanHome(ws.DataDir); err != nil {
		return err
	}
	return m.store.SetWorkspaceState(ctx, ws.ID, "stopped")
}

// countSessions asks the Agent how many sessions are currently running. The quota
// caps concurrency, so only live (alive) sessions count — stopped/resumable ones,
// which the Agent keeps listed for the stopped-TTL window, do not occupy a slot.
func (m *manager) countSessions(ctx context.Context, rt *dockerRuntime) (int, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", rt.agentBase()+"/sessions", nil)
	if rt.token != "" {
		req.Header.Set("Authorization", "Bearer "+rt.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var body struct {
		Sessions []struct {
			Alive bool `json:"alive"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, err
	}
	n := 0
	for _, s := range body.Sessions {
		if s.Alive {
			n++
		}
	}
	return n, nil
}

// agentSessions fetches the Agent's full session list (for the DB mirror).
func (m *manager) agentSessions(ctx context.Context, rt *dockerRuntime) ([]sessionWire, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", rt.agentBase()+"/sessions", nil)
	if rt.token != "" {
		req.Header.Set("Authorization", "Bearer "+rt.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Sessions []sessionWire `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Sessions, nil
}

// workspaceNames derives the container/network/home for a (tenant, user). The
// default tenant keeps the flat af-ws-<key> scheme so the existing live
// deployment is reused unchanged; other tenants are scoped by slug.
func (m *manager) workspaceNames(slug, key string) (name, network, dataDir string) {
	if slug == "default" {
		return "af-ws-" + key, "af-net-" + key, filepath.Join(m.dataRoot, key)
	}
	return "af-ws-" + slug + "-" + key, "af-net-" + slug + "-" + key, filepath.Join(m.dataRoot, slug, key)
}

// createWorkspace allocates a new workspace record for a membership. An existing
// container of the conventional name has its port/AGENT_TOKEN adopted; otherwise
// a fresh port (DB max+1, floored at portBase) and token are minted.
func (m *manager) createWorkspace(ctx context.Context, mv MembershipView, userKey string) (Workspace, error) {
	name, network, dataDir := m.workspaceNames(mv.TenantSlug, userKey)
	port := dockerPublishedPort(name)
	if port == "" {
		mx, err := m.store.MaxAgentPort(ctx)
		if err != nil {
			return Workspace{}, err
		}
		next := m.portBase
		if mx+1 > next {
			next = mx + 1
		}
		port = strconv.Itoa(next)
	}
	token := dockerEnvValue(name, "AGENT_TOKEN")
	if token == "" {
		token = randHex(24)
	}
	ws := Workspace{
		ID:            newID(),
		TenantID:      mv.TenantID,
		MembershipID:  mv.MembershipID,
		ContainerName: name,
		Network:       network,
		DataDir:       dataDir,
		AgentPort:     port,
		AgentToken:    token,
		State:         "stopped",
		CreatedAt:     nowTS(),
	}
	if err := m.store.CreateWorkspace(ctx, ws); err != nil {
		return Workspace{}, err
	}
	return ws, nil
}

func (m *manager) runtimeFor(ws Workspace, secretKey string) *dockerRuntime {
	return &dockerRuntime{
		image:      m.image,
		name:       ws.ContainerName,
		network:    ws.Network,
		dataDir:    ws.DataDir,
		agentHost:  m.agentHost,
		agentPort:  ws.AgentPort,
		token:      ws.AgentToken,
		secretKey:  secretKey,
		memory:     m.memory,
		sessionCmd: m.sessionCmd,
		extraEnv:   m.extraEnv,
	}
}

// backfill records existing on-disk default-tenant users into the store on boot
// (docs/13 S3). Each top-level <dataRoot>/<key>/home is one default-tenant user;
// non-default tenants nest under <dataRoot>/<slug>/<key> and are created on demand.
func (m *manager) backfill(ctx context.Context) error {
	entries, err := os.ReadDir(m.dataRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	t, err := m.store.EnsureDefaultTenant(ctx)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		key := e.Name()
		if _, err := os.Stat(filepath.Join(m.dataRoot, key, "home")); err != nil {
			continue // tenant dir or non-user layout; skip
		}
		ident, err := m.store.UpsertIdentity(ctx, "", key, "")
		if err != nil {
			return err
		}
		mem, err := m.store.EnsureMembership(ctx, ident.ID, t.ID, "member")
		if err != nil {
			return err
		}
		if _, ok, err := m.store.GetWorkspaceByMembership(ctx, mem.ID); err != nil {
			return err
		} else if !ok {
			mv := MembershipView{MembershipID: mem.ID, TenantID: t.ID, TenantSlug: t.Slug, TenantName: t.Name, Role: mem.Role}
			if _, err := m.createWorkspace(ctx, mv, key); err != nil {
				return err
			}
		}
	}
	return nil
}

var userInvalid = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeUser turns an email (or any id) into a container-name-safe key.
func sanitizeUser(s string) string {
	s = userInvalid.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}

// dockerPublishedPort returns the host port mapped to the container's 7700/tcp.
func dockerPublishedPort(name string) string {
	out, err := exec.Command("docker", "inspect", "-f",
		`{{with index .NetworkSettings.Ports "7700/tcp"}}{{(index . 0).HostPort}}{{end}}`, name).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// dockerEnvValue returns the value of an env var baked into a container's config.
func dockerEnvValue(name, key string) string {
	out, err := exec.Command("docker", "inspect", "-f",
		`{{range .Config.Env}}{{println .}}{{end}}`, name).Output()
	if err != nil {
		return ""
	}
	prefix := key + "="
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}
