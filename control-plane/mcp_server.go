package main

// Tenant-distributed MCP servers (docs/log/48 P4 + ADR0031) — the CP half.
//
// Two faces over the same rows:
//
//   - /api/admin/mcp-servers*   tenant_admin (or super_admin) CRUD from the Console
//     admin modal. Audited. Refuses stdio.
//   - GET /internal/mcp-servers per-membership AF_MCP_TOKEN (mcp_server_bridge.go).
//     The Workspace agent polls it and caches the set as mcp-tenant.json, which the
//     registry composes into `builtin ∪ tenant ∪ user`.
//
// Why the definition lives here and not in each workspace: the CP is the only thing
// alive while a member's container is stopped, so it is the only place a set can be held
// that reaches every member (the same reason schedules live here).
//
// ⚠️ Validation is DUPLICATED from workspace/agent/internal/mcpreg/def.go. The two are
// separate Go modules, so the rules cannot be shared as code. The error CODES are kept
// identical on purpose — the Console resolves both through the same "err.<code>" catalog
// — and the agent re-validates everything it receives (mcpreg.acceptTenant), so a rule
// that drifts here is caught there rather than materialized into a member's CLI config.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Validation codes, identical to mcpreg's (docs/log/48 §11.3: one reason = one code).
// 追加・改名時は console/src/lib/i18n/locales/{ja,en}/errors.ts の "err.<code>" も同時に。
const (
	codeMCPNameInvalid    = "mcp_name_invalid"
	codeMCPNameReserved   = "mcp_name_reserved"
	codeMCPNameTaken      = "mcp_name_taken"
	codeMCPTransport      = "mcp_transport_unsupported"
	codeMCPTenantStdio    = "mcp_tenant_stdio"
	codeMCPURLRequired    = "mcp_url_required"
	codeMCPURLInvalid     = "mcp_url_invalid"
	codeMCPURLScheme      = "mcp_url_scheme"
	codeMCPURLHost        = "mcp_url_host"
	codeMCPURLCredentials = "mcp_url_credentials"
	codeMCPHeaderName     = "mcp_header_name_invalid"
	codeMCPHeaderValue    = "mcp_header_value_invalid"
	codeMCPKindUnknown    = "mcp_kind_unknown"
	codeMCPTimeoutRange   = "mcp_timeout_range"
	codeMCPNotFound       = "mcp_not_found"
)

// maskedValue is what a stored header value is replaced with on the wire. Sending it
// back unchanged keeps the stored value, so the admin UI can edit a definition without
// ever handling the real credential (the connections convention, docs/log/48 §5.1).
const maskedValue = "***"

// mcpNameRe is the intersection of what the target CLIs accept as a server key — codex
// writes it as a TOML bare key, the narrowest. Identical to mcpreg.nameRe.
var mcpNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,47}$`)

// mcpReservedNames are the names Agent Fleet itself occupies in the CLIs' configs;
// distributing one would silently shadow the af tools in every member's container.
var mcpReservedNames = map[string]bool{"af": true, "agent-fleet": true}

// mcpKnownKinds mirrors mcpreg.knownKinds — the agent kinds that run an MCP client.
var mcpKnownKinds = map[string]bool{
	"claude": true, "codex": true, "opencode": true,
	"cursor": true, "kiro": true, "agy": true, "copilot": true,
}

// mcpServerStore is the narrow store view this feature needs: the definitions plus the
// audit ledger (every admin mutation is recorded, docs/log/48 §9).
type mcpServerStore interface {
	store.MCPServerStore
	store.AuditStore
}

// mcpServerAPI serves both faces. It holds the manager (for master32 / custodian /
// membership resolution) via memberAuth and a narrow store view.
type mcpServerAPI struct {
	memberAuth
	store mcpServerStore
}

func newMCPServerAPI(m *manager) mcpServerAPI { return mcpServerAPI{memberAuth{m}, m.store} }

// --- wire shapes -----------------------------------------------------------------

// mcpServerBody is the admin request/response shape. Field names follow the agent's
// ServerDef where they overlap (headers / targets / kinds / timeoutMs) so the Console
// can reuse its McpServer type, and snake_case for the CP-only fields (tenant_slug /
// user_secret) to match the rest of the admin API.
type mcpServerBody struct {
	ID         string            `json:"id,omitempty"`
	TenantSlug string            `json:"tenant_slug,omitempty"`
	Name       string            `json:"name"`
	Label      string            `json:"label,omitempty"`
	Transport  string            `json:"transport,omitempty"` // accepted for symmetry; must be http
	URL        string            `json:"url,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Targets    mcpTargets        `json:"targets"`
	Kinds      []string          `json:"kinds,omitempty"`
	TimeoutMS  int               `json:"timeoutMs,omitempty"`
	Enabled    bool              `json:"enabled"`
	UserSecret bool              `json:"user_secret,omitempty"`
	CreatedBy  string            `json:"created_by,omitempty"`
	CreatedAt  string            `json:"created_at,omitempty"`
	UpdatedAt  string            `json:"updated_at,omitempty"`
}

type mcpTargets struct {
	Assistant bool `json:"assistant"`
	Session   bool `json:"session"`
}

// distDef is the definition as the Workspace agent consumes it — deliberately the
// agent's mcpreg.ServerDef JSON shape (camelCase, origin/enabled included) so the agent
// decodes it straight into its own type with no translation layer.
type distDef struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Label      string            `json:"label,omitempty"`
	Origin     string            `json:"origin"`
	Transport  string            `json:"transport"`
	URL        string            `json:"url,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Enabled    bool              `json:"enabled"`
	Targets    mcpTargets        `json:"targets"`
	Kinds      []string          `json:"kinds,omitempty"`
	TimeoutMS  int               `json:"timeoutMs,omitempty"`
	UserSecret bool              `json:"userSecret,omitempty"`
}

// --- header sealing ---------------------------------------------------------------

// sealHeaders encrypts the header map with the tenant KEK through the key custodian
// (custodian.go), returning the ciphertext and the key reference. Wrap/Unwrap are
// AES-256-GCM over opaque bytes with the keyRef as AAD — nominally a DEK envelope, used
// here for the secret field itself, which is exactly the shape docs/log/48 §3.2 asks for.
//
// With no master key (dev / single-node without a configured secret) the map is stored
// as plaintext JSON with an empty KeyRef, the same way the Agent's secret store degrades
// rather than refusing to work.
func (a mcpServerAPI) sealHeaders(ctx context.Context, tenantID string, h map[string]string) (enc, keyRef string, err error) {
	if len(h) == 0 {
		return "", "", nil
	}
	b, err := json.Marshal(h)
	if err != nil {
		return "", "", err
	}
	if len(a.mgr.master32) == 0 || a.mgr.custodian == nil {
		return string(b), "", nil
	}
	ct, err := a.mgr.custodian.Wrap(ctx, tenantID, b)
	if err != nil {
		return "", "", err
	}
	return ct, tenantID, nil
}

// openHeaders reverses sealHeaders. An unreadable row is an error, never an empty map:
// silently distributing a server with NO headers would turn an auth failure into a
// confusing "the server rejects everything" instead of naming the real cause.
func (a mcpServerAPI) openHeaders(ctx context.Context, enc, keyRef string) (map[string]string, error) {
	if enc == "" {
		return nil, nil
	}
	raw := []byte(enc)
	if keyRef != "" {
		// A row sealed under a tenant key, read back by a process that has no custodian
		// (the master key was removed / this is a different deployment). Say so instead of
		// dereferencing nil — a whole tenant's distribution failing over a key change is
		// exactly the case an operator needs named.
		if a.mgr.custodian == nil {
			return nil, errors.New("row is sealed with a tenant key but no key custodian is configured")
		}
		b, err := a.mgr.custodian.Unwrap(ctx, keyRef, enc)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	var h map[string]string
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, err
	}
	return h, nil
}

// --- validation --------------------------------------------------------------------

// validateMCPBody enforces the tenant-distribution rules. transport is pinned to http:
// ADR0031 決定 2 refuses tenant stdio, and the table has no command columns to hold one,
// so this is the API half of a constraint the schema also carries.
func validateMCPBody(b mcpServerBody) *apiError {
	name := strings.TrimSpace(b.Name)
	if !mcpNameRe.MatchString(name) {
		return &apiError{http.StatusBadRequest, codeMCPNameInvalid,
			"name must be 1-48 chars of [a-zA-Z0-9_-] starting alphanumeric: " + b.Name}
	}
	if mcpReservedNames[strings.ToLower(name)] {
		return &apiError{http.StatusBadRequest, codeMCPNameReserved, name + " is a name reserved by Agent Fleet"}
	}
	switch strings.TrimSpace(b.Transport) {
	case "", "http":
	case "stdio":
		return &apiError{http.StatusBadRequest, codeMCPTenantStdio,
			"tenant-distributed MCP servers cannot use stdio (remote only)"}
	default:
		return &apiError{http.StatusBadRequest, codeMCPTransport, "unsupported transport: " + b.Transport}
	}
	if aerr := validateMCPURL(b.URL); aerr != nil {
		return aerr
	}
	for k, v := range b.Headers {
		if strings.TrimSpace(k) == "" || strings.ContainsAny(k, "\r\n:") {
			return &apiError{http.StatusBadRequest, codeMCPHeaderName, "invalid header name: " + k}
		}
		if strings.ContainsAny(v, "\r\n") {
			return &apiError{http.StatusBadRequest, codeMCPHeaderValue, "a header value cannot contain a newline"}
		}
	}
	for _, k := range b.Kinds {
		if !mcpKnownKinds[k] {
			return &apiError{http.StatusBadRequest, codeMCPKindUnknown, "unknown agent kind: " + k}
		}
	}
	if b.TimeoutMS != 0 && (b.TimeoutMS < 1000 || b.TimeoutMS > 120000) {
		return &apiError{http.StatusBadRequest, codeMCPTimeoutRange, "timeout must be between 1000 and 120000 ms"}
	}
	return nil
}

// validateMCPURL keeps a distributed definition to an absolute http(s) URL with no
// embedded credentials — those would land in every member's materialized config file in
// plain sight, where the masking contract cannot reach them.
func validateMCPURL(raw string) *apiError {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return &apiError{http.StatusBadRequest, codeMCPURLRequired, "a remote server needs a URL"}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return &apiError{http.StatusBadRequest, codeMCPURLInvalid, "cannot parse URL: " + err.Error()}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return &apiError{http.StatusBadRequest, codeMCPURLScheme, "URL must be http / https: " + raw}
	}
	if u.Host == "" {
		return &apiError{http.StatusBadRequest, codeMCPURLHost, "URL has no host: " + raw}
	}
	if u.User != nil {
		return &apiError{http.StatusBadRequest, codeMCPURLCredentials, "do not embed credentials in the URL (use a header)"}
	}
	return nil
}

// --- row <-> wire ------------------------------------------------------------------

func joinTargets(t mcpTargets) string {
	var on []string
	if t.Assistant {
		on = append(on, "assistant")
	}
	if t.Session {
		on = append(on, "session")
	}
	return strings.Join(on, ",")
}

func splitTargets(s string) mcpTargets {
	var t mcpTargets
	for _, p := range strings.Split(s, ",") {
		switch strings.TrimSpace(p) {
		case "assistant":
			t.Assistant = true
		case "session":
			t.Session = true
		}
	}
	return t
}

func splitKinds(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// mergeHeaders resolves an incoming (masked) header map against the stored one: a value
// still equal to maskedValue keeps its stored counterpart, anything else is a new
// secret, and a key absent from the incoming map is a deletion. A masked value with
// nothing behind it is DROPPED rather than stored — otherwise the literal "***" would be
// sent to the MCP server as if it were a credential.
func mergeHeaders(incoming, stored map[string]string) map[string]string {
	if len(incoming) == 0 {
		return nil
	}
	out := make(map[string]string, len(incoming))
	for k, v := range incoming {
		if v == maskedValue {
			if old, ok := stored[k]; ok {
				out[k] = old
			}
			continue
		}
		out[k] = v
	}
	return out
}

// stripValues keeps the header NAMES and drops every value. It is what user_secret=1
// stores: the tenant describes WHICH headers the server needs, each member supplies the
// values into their own encrypted store (docs/log/48 §5.2). Dropping values on the way in
// (rather than only on the way out) matters — a token the admin pasted before flipping
// the flag would otherwise sit in the DB forever with nothing ever reading it.
func stripValues(h map[string]string) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k := range h {
		out[k] = ""
	}
	return out
}

func maskHeaders(h map[string]string) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		if v == "" {
			out[k] = "" // user_secret: no value is stored, so there is nothing to mask
			continue
		}
		out[k] = maskedValue
	}
	return out
}

// --- admin face --------------------------------------------------------------------

// adminList (GET /api/admin/mcp-servers?tenant=<slug>) lists a tenant's distributed
// definitions with every header value masked.
func (a mcpServerAPI) adminList(w http.ResponseWriter, r *http.Request) {
	_, t, ok := a.tenantAdminFor(w, r, mcpTenantSlug(r, ""))
	if !ok {
		return
	}
	rows, err := a.store.ListMCPServers(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]mcpServerBody, 0, len(rows))
	for _, row := range rows {
		h, herr := a.openHeaders(r.Context(), row.HeadersEnc, row.KeyRef)
		body := rowToBody(row, maskHeaders(h))
		if herr != nil {
			// A row whose headers will not decrypt is still listed — hiding it would leave
			// an admin unable to see or delete the thing that is failing for every member.
			body.Headers = nil
			body.Label = strings.TrimSpace(body.Label + " (headers unreadable)")
		}
		out = append(out, body)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenant": t.Slug, "servers": out})
}

func rowToBody(row store.MCPServerRow, headers map[string]string) mcpServerBody {
	return mcpServerBody{
		ID: row.ID, Name: row.Name, Label: row.Label, Transport: row.Transport, URL: row.URL,
		Headers: headers, Targets: splitTargets(row.Targets), Kinds: splitKinds(row.Kinds),
		TimeoutMS: row.TimeoutMS, Enabled: row.Enabled, UserSecret: row.UserSecret,
		CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

// mcpTenantSlug picks the tenant slug: the query param (GET / DELETE, which have no
// body) or the decoded body's tenant_slug (POST / PUT). Falling back to the header-based
// tenant selection lets the Console omit it entirely when the admin is working in the
// tenant already selected in the UI.
func mcpTenantSlug(r *http.Request, fromBody string) string {
	if v := strings.TrimSpace(fromBody); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.URL.Query().Get("tenant")); v != "" {
		return v
	}
	return tenantSel(r)
}

// adminUpsert handles POST (create) and PUT /{id} (replace).
func (a mcpServerAPI) adminUpsert(w http.ResponseWriter, r *http.Request) {
	var b mcpServerBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<18)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_body", "invalid JSON"})
		return
	}
	ident, t, ok := a.tenantAdminFor(w, r, mcpTenantSlug(r, b.TenantSlug))
	if !ok {
		return
	}
	if aerr := validateMCPBody(b); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	id := r.PathValue("id")
	rows, err := a.store.ListMCPServers(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	name := strings.TrimSpace(b.Name)
	var stored *store.MCPServerRow
	for i := range rows {
		if rows[i].ID == id && id != "" {
			stored = &rows[i]
			continue
		}
		if strings.EqualFold(rows[i].Name, name) {
			writeAPIErr(w, &apiError{http.StatusConflict, codeMCPNameTaken,
				"a server named " + name + " is already distributed in this tenant"})
			return
		}
	}
	if id != "" && stored == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, codeMCPNotFound, "unknown MCP server"})
		return
	}

	// Resolve the incoming (masked) headers against what is stored, then apply the
	// user_secret rule. Order matters: merge first so an untouched "***" survives, strip
	// second so flipping the flag on actually discards the stored values.
	var storedHeaders map[string]string
	if stored != nil {
		if storedHeaders, err = a.openHeaders(r.Context(), stored.HeadersEnc, stored.KeyRef); err != nil {
			// Unreadable stored headers must not silently become the incoming set: say so,
			// so the admin re-enters the values instead of shipping a half-merged map.
			writeAPIErr(w, &apiError{http.StatusConflict, "mcp_headers_unreadable",
				"the stored headers cannot be decrypted — re-enter every header value"})
			return
		}
	}
	headers := mergeHeaders(b.Headers, storedHeaders)
	if b.UserSecret {
		headers = stripValues(headers)
	}
	enc, keyRef, err := a.sealHeaders(r.Context(), t.ID, headers)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}

	sort.Strings(b.Kinds)
	row := store.MCPServerRow{
		ID: id, TenantID: t.ID, Name: name, Label: strings.TrimSpace(b.Label),
		Transport: "http", URL: strings.TrimSpace(b.URL), HeadersEnc: enc, KeyRef: keyRef,
		Targets: joinTargets(b.Targets), Kinds: strings.Join(b.Kinds, ","), TimeoutMS: b.TimeoutMS,
		Enabled: b.Enabled, UserSecret: b.UserSecret, UpdatedAt: store.NowTS(),
	}
	if stored == nil {
		row.ID = store.NewID()
		row.CreatedBy = ident.Email
		row.CreatedAt = row.UpdatedAt
		err = a.store.CreateMCPServer(r.Context(), row)
	} else {
		row.CreatedBy = stored.CreatedBy
		row.CreatedAt = stored.CreatedAt
		err = a.store.UpdateMCPServer(r.Context(), row)
	}
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// Audit detail names the server and the flags, never a header value (docs/log/48 §9).
	a.auditMCP(r.Context(), ident, t.ID, "mcp.upsert", row.Name,
		"url="+row.URL+" targets="+row.Targets+" enabled="+boolStr(row.Enabled)+" user_secret="+boolStr(row.UserSecret))
	writeJSON(w, http.StatusOK, rowToBody(row, maskHeaders(headers)))
}

// adminDelete (DELETE /api/admin/mcp-servers/{id}?tenant=<slug>).
func (a mcpServerAPI) adminDelete(w http.ResponseWriter, r *http.Request) {
	ident, t, ok := a.tenantAdminFor(w, r, mcpTenantSlug(r, ""))
	if !ok {
		return
	}
	id := r.PathValue("id")
	row, found, err := a.store.GetMCPServer(r.Context(), t.ID, id)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found {
		writeAPIErr(w, &apiError{http.StatusNotFound, codeMCPNotFound, "unknown MCP server"})
		return
	}
	if err := a.store.DeleteMCPServer(r.Context(), t.ID, id); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	a.auditMCP(r.Context(), ident, t.ID, "mcp.delete", row.Name, "")
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id, "name": row.Name})
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// auditMCP records one admin mutation. Tenant-scoped so a tenant_admin sees their own
// changes in the audit view. Best-effort: a ledger failure must not fail the edit.
func (a mcpServerAPI) auditMCP(ctx context.Context, ident store.Identity, tenantID, action, target, detail string) {
	_ = a.store.InsertAudit(ctx, store.AuditLog{
		ID: store.NewID(), TenantID: tenantID, ActorKind: "admin", ActorID: ident.ID,
		Action: action, Target: target, Detail: detail, At: store.NowTS(),
	})
}

// --- distribution face -------------------------------------------------------------

// distribute (GET /internal/mcp-servers) serves the caller's tenant's ENABLED
// definitions to their Workspace agent, which caches them (docs/log/48 §6). The tenant comes
// from the token's membership, never the request, so this can never read another
// tenant's headers.
//
// A row whose headers will not decrypt is OMITTED rather than sent without them: the
// agent would otherwise materialize a server that authenticates with nothing, and the
// member would debug the MCP server instead of the key configuration. The count of such
// rows is reported so the Console can say the set is incomplete.
func (a mcpServerAPI) distribute(w http.ResponseWriter, r *http.Request, mv store.MembershipView) {
	rows, err := a.store.ListMCPServers(r.Context(), mv.TenantID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]distDef, 0, len(rows))
	broken := 0
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		h, herr := a.openHeaders(r.Context(), row.HeadersEnc, row.KeyRef)
		if herr != nil {
			broken++
			continue
		}
		if row.UserSecret {
			h = stripValues(h)
		}
		out = append(out, distDef{
			ID: row.ID, Name: row.Name, Label: row.Label, Origin: "tenant",
			Transport: "http", URL: row.URL, Headers: h, Enabled: true,
			Targets: splitTargets(row.Targets), Kinds: splitKinds(row.Kinds),
			TimeoutMS: row.TimeoutMS, UserSecret: row.UserSecret,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": out, "unreadable": broken})
}
