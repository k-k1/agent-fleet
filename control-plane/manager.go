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

// manager owns the set of per-user Workspace runtimes. Each Control Plane user
// gets one container (af-ws-<user>) with an isolated home (<dataRoot>/<user>/home)
// and its own host-published Agent port. This is the `local` Runtime adapter
// scaled from a single fixed container to a per-user map (docs/09 §9.3); the
// Agent contract (/sessions, /repos, /connections) is unchanged.
//
// As of P3-1 (docs/13) the MetadataStore (SQLite) is the source of truth for the
// tenant/user/workspace records; `rts` is now just an in-memory cache of built
// runtimes keyed by user_key. Container name / network / home / Agent port /
// token are persisted, so a CP restart no longer re-derives them from docker and
// no longer re-numbers ports.
type manager struct {
	mu    sync.Mutex
	rts   map[string]*dockerRuntime // cache keyed by user_key; DB is source of truth
	store Store

	// template fields shared by every per-user runtime
	image      string
	dataRoot   string // host path; <dataRoot>/<user>/home is bind-mounted to ~
	agentHost  string
	memory     string
	sessionCmd string
	extraEnv   []string

	portBase int // base host port for a freshly allocated Agent (7700)

	// user resolution (AuthGateway port). authMode: "dev" (fixed id) | "proxy"
	// (read the authenticated email from the gateway header).
	authMode    string
	devUser     string
	emailHeader string

	// at-rest encryption (SecretStore). master32 = SHA-256 of AF_MASTER_KEY, or
	// nil when unset (then no AF_SECRET_KEY is injected and the Agent stores
	// secrets as plaintext JSON — dev). Per-user subkey = HMAC(master32, user).
	// P3-3 will replace this HMAC derivation with envelope encryption; the
	// injection path is unchanged.
	master32 []byte
}

// secretKeyFor derives the per-user encryption subkey (hex) injected as
// AF_SECRET_KEY, or "" when no master key is configured.
func (m *manager) secretKeyFor(user string) string {
	if len(m.master32) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, m.master32)
	mac.Write([]byte(user))
	return hex.EncodeToString(mac.Sum(nil))
}

// identity is what the AuthGateway resolves a request to. key is the
// container-name-safe user key (sanitized email / fixed dev id); email is the
// raw address when known (proxy mode), used only as user metadata.
type identity struct{ key, email string }

// resolveIdentity maps a request to an identity. In dev mode the key is a fixed
// id (no email). In proxy mode it is the sanitized gateway email; a missing/empty
// header yields key=="" — treated as unauthenticated and denied, so a request
// that bypasses the gateway can NOT fall through to a real workspace.
func (m *manager) resolveIdentity(r *http.Request) identity {
	if m.authMode == "proxy" {
		e := r.Header.Get(m.emailHeader)
		return identity{key: sanitizeUser(e), email: e}
	}
	return identity{key: m.devUser}
}

// resolveUser returns just the user key (used by whoami and the Bitbucket start).
func (m *manager) resolveUser(r *http.Request) string { return m.resolveIdentity(r).key }

// forUser returns the runtime for a user key, resolving (and creating on first
// use) the tenant/user/workspace records in the store. It does not start the
// container — that's dockerRuntime.start().
func (m *manager) forUser(ctx context.Context, key, email string) (*dockerRuntime, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rt, ok := m.rts[key]; ok {
		return rt, nil
	}
	t, err := m.store.EnsureDefaultTenant(ctx)
	if err != nil {
		return nil, err
	}
	u, err := m.store.UpsertUser(ctx, t.ID, email, key)
	if err != nil {
		return nil, err
	}
	ws, ok, err := m.store.GetWorkspaceByUser(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	if !ok {
		ws, err = m.createWorkspace(ctx, t, u)
		if err != nil {
			return nil, err
		}
	}
	rt := m.runtimeFor(ws, u.UserKey)
	m.rts[key] = rt
	return rt, nil
}

// createWorkspace allocates a new workspace record for a user. If a container of
// the conventional name already exists (e.g. the live deployment before DB
// backfill), its published port and AGENT_TOKEN are adopted so the running Agent
// keeps working; otherwise a fresh port (DB max + 1, floored at portBase) and
// token are minted. Names follow the existing af-ws-<key> / af-net-<key> scheme
// so existing containers and homes are reused unchanged.
func (m *manager) createWorkspace(ctx context.Context, t Tenant, u User) (Workspace, error) {
	name := "af-ws-" + u.UserKey
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
		TenantID:      t.ID,
		UserID:        u.ID,
		ContainerName: name,
		Network:       "af-net-" + u.UserKey,
		DataDir:       filepath.Join(m.dataRoot, u.UserKey),
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

// runtimeFor builds the docker Runtime adapter from a persisted workspace record.
func (m *manager) runtimeFor(ws Workspace, userKey string) *dockerRuntime {
	return &dockerRuntime{
		image:      m.image,
		name:       ws.ContainerName,
		network:    ws.Network,
		dataDir:    ws.DataDir,
		agentHost:  m.agentHost,
		agentPort:  ws.AgentPort,
		token:      ws.AgentToken,
		secretKey:  m.secretKeyFor(userKey),
		memory:     m.memory,
		sessionCmd: m.sessionCmd,
		extraEnv:   m.extraEnv,
	}
}

// backfill records existing on-disk users into the store on boot (docs/13 S3),
// so the live deployment becomes the default tenant + existing users without
// recreating containers. Each <dataRoot>/<key>/home directory is one user; the
// get-or-create path adopts any running container's port/token. Best-effort.
func (m *manager) backfill(ctx context.Context) error {
	entries, err := os.ReadDir(m.dataRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		key := e.Name()
		if _, err := os.Stat(filepath.Join(m.dataRoot, key, "home")); err != nil {
			continue // not a user home layout; skip (e.g. db files)
		}
		if _, err := m.forUser(ctx, key, ""); err != nil {
			return err
		}
	}
	return nil
}

var userInvalid = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeUser turns an email (or any id) into a container-name-safe key:
// lowercase, non-alphanumerics collapsed to '-', trimmed, length-capped.
// e.g. "Alice.B@example.com" -> "alice-b-example-com".
func sanitizeUser(s string) string {
	s = userInvalid.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}

// dockerPublishedPort returns the host port mapped to the container's 7700/tcp,
// or "" if the container does not exist / has no mapping.
func dockerPublishedPort(name string) string {
	out, err := exec.Command("docker", "inspect", "-f",
		`{{with index .NetworkSettings.Ports "7700/tcp"}}{{(index . 0).HostPort}}{{end}}`, name).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// dockerEnvValue returns the value of an env var baked into a container's config
// (e.g. AGENT_TOKEN), or "" if the container does not exist or lacks the var.
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
