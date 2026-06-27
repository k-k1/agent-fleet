package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
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
	rts   map[string]*dockerRuntime // cache keyed by membership id; DB is source of truth
	store Store

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
	if m.authMode == "proxy" {
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

// resolve maps a request's identity + selected tenant to its runtime, creating
// the workspace record on first use. tenantSel is the X-AF-Tenant value (slug or
// tenant id); empty means "default selection".
func (m *manager) resolve(ctx context.Context, key, email, tenantSel string) (*dockerRuntime, *apiError) {
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

	var mv MembershipView
	switch {
	case tenantSel != "":
		found := false
		for _, x := range ms {
			if x.TenantSlug == tenantSel || x.TenantID == tenantSel {
				mv, found = x, true
				break
			}
		}
		if !found {
			return nil, &apiError{http.StatusForbidden, "forbidden_tenant", "not a member of tenant " + tenantSel}
		}
	case len(ms) == 1:
		mv = ms[0]
	default:
		return nil, &apiError{http.StatusConflict, "tenant_selection_required", "specify X-AF-Tenant; you belong to multiple tenants"}
	}

	if rt, ok := m.rts[mv.MembershipID]; ok {
		return rt, nil
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
	m.rts[mv.MembershipID] = rt
	return rt, nil
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
