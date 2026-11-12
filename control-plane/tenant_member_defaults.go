package main

// Tenant defaults for members' Console (ADR 0114 decision 8, P2).
//
//	tenant_admin ──▶ GET/PUT /api/admin/tenants/{slug}/member-defaults
//	member       ──▶ GET /api/me/ui-state                 (carries them as `defaults`)
//
// The agent a new member is pointed at first (tenants differ in which agent they pay for, so the
// first-run card names that one; a pointer, not a restriction), plus the launch defaults, team
// repositories and non-secret ops settings of tenant_launch_defaults.go.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const tenantMemberDefaultsMaxBody = 4 << 10

// memberDefaultAgents are the kinds a tenant may name: the ones a member connects with their own
// account. shell and ssm need no connection, lcpp is governed by engine access, and agy is
// experimental. This is CP policy, not a catalog fact: the Console derives its
// MEMBER_DEFAULT_AGENTS from the registry's memberDefault cap, and
// console/src/agents/derivedKindLists.test.ts reads this line and holds the two equal, in order
// (SESSION_KINDS order, which is also the order of the refusal text below).
var memberDefaultAgents = []string{"claude", "codex", "cursor", "copilot", "kiro", "opencode", "muse"}

// tenantMemberDefaultsWire is the stored document, the admin PUT body and, for members, the
// `defaults` of GET /api/me/ui-state. Read by core/store/uiState.ts and the tenant editor
// (features/settings/tenant/tenantMemberDefaults.tsx). Add a key here, never an open map.
type tenantMemberDefaultsWire struct {
	Agent  string             `json:"agent"` // "" = no default; also the launch dialog's agent
	Launch launchDefaultsWire `json:"launch"`
	Repos  []string           `json:"repos"` // owner/name paths offered first in Start
	Ops    opsSettingsWire    `json:"ops"`
}

// normalize makes an absent list an empty one on the wire, so the Console never meets null.
func (d *tenantMemberDefaultsWire) normalize() {
	if d.Repos == nil {
		d.Repos = []string{}
	}
}

func (d tenantMemberDefaultsWire) validate() error {
	if d.Agent != "" && !inSet(d.Agent, memberDefaultAgents) {
		return fmt.Errorf("agent must be one of %s, or empty", strings.Join(memberDefaultAgents, ", "))
	}
	// The launch agent is the document's own `agent`, so the group carries none of its own here.
	if d.Launch.Agent != "" {
		return fmt.Errorf("launch.agent is not a tenant field: set agent")
	}
	if err := d.Launch.validate(); err != nil {
		return err
	}
	if err := validateTeamRepos(d.Repos); err != nil {
		return err
	}
	return d.Ops.validate()
}

// tenantMemberDefaultsAdminWire is the admin GET/PUT response.
type tenantMemberDefaultsAdminWire struct {
	Tenant    string                   `json:"tenant"`
	Defaults  tenantMemberDefaultsWire `json:"defaults"`
	UpdatedBy string                   `json:"updated_by,omitempty"`
	UpdatedAt string                   `json:"updated_at,omitempty"`
}

// loadTenantMemberDefaults reads the stored document; no row is the zero document. A row that
// no longer decodes (written by a newer CP, then rolled back) is also the zero document: a
// missing pointer costs a member nothing, a failed request would cost them the page.
func loadTenantMemberDefaults(r *http.Request, st store.TenantMemberDefaultsStore, tenantID string) (store.TenantMemberDefaults, tenantMemberDefaultsWire, error) {
	zero := tenantMemberDefaultsWire{}
	zero.normalize()
	row, ok, err := st.GetTenantMemberDefaults(r.Context(), tenantID)
	if err != nil || !ok {
		return row, zero, err
	}
	var d tenantMemberDefaultsWire
	if json.Unmarshal([]byte(row.Defaults), &d) != nil || d.validate() != nil {
		return row, zero, nil
	}
	d.normalize()
	return row, d, nil
}

// tenantMemberDefaults (GET /api/admin/tenants/{slug}/member-defaults).
func (a adminAPI) tenantMemberDefaults(w http.ResponseWriter, r *http.Request) {
	_, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	row, d, err := loadTenantMemberDefaults(r, a.mgr.store, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, tenantMemberDefaultsAdminWire{Tenant: t.Slug, Defaults: d, UpdatedBy: row.UpdatedBy, UpdatedAt: row.UpdatedAt})
}

// setTenantMemberDefaults (PUT /api/admin/tenants/{slug}/member-defaults) replaces the document.
func (a adminAPI) setTenantMemberDefaults(w http.ResponseWriter, r *http.Request) {
	ident, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	d, sent, aerr := decodeTenantMemberDefaults(w, r)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	// A group the body leaves out keeps its stored value: an older Console, or a script written
	// before the groups existed, sends only `agent` and must not wipe them. Clearing a group is
	// explicit: {} for launch and ops, [] for repos.
	if !sent.launch || !sent.repos || !sent.ops {
		_, cur, err := loadTenantMemberDefaults(r, a.mgr.store, t.ID)
		if err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		if !sent.launch {
			d.Launch = cur.Launch
		}
		if !sent.repos {
			d.Repos = cur.Repos
		}
		if !sent.ops {
			d.Ops = cur.Ops
		}
	}
	b, err := json.Marshal(d)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	row := store.TenantMemberDefaults{TenantID: t.ID, Defaults: string(b), UpdatedBy: ident.ID, UpdatedAt: store.NowTS()}
	if err := a.mgr.store.PutTenantMemberDefaults(r.Context(), row); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	_ = a.mgr.store.InsertAudit(r.Context(), store.AuditLog{
		ID: store.NewID(), TenantID: t.ID, ActorKind: "user", ActorID: ident.ID,
		Action: "tenant.member_defaults", Target: t.Slug,
		Detail: d.auditDetail(), At: row.UpdatedAt,
	})
	writeJSON(w, http.StatusOK, tenantMemberDefaultsAdminWire{Tenant: t.Slug, Defaults: d, UpdatedBy: row.UpdatedBy, UpdatedAt: row.UpdatedAt})
}

// groupsSent says which groups the PUT body carried (a JSON null counts as left out).
type groupsSent struct{ launch, repos, ops bool }

// decodeTenantMemberDefaults reads a PUT body strictly: a misspelt key would otherwise be dropped
// and the admin would believe the default says something it does not.
func decodeTenantMemberDefaults(w http.ResponseWriter, r *http.Request) (tenantMemberDefaultsWire, groupsSent, *apiError) {
	var d tenantMemberDefaultsWire
	var in *tenantMemberDefaultsWire
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, tenantMemberDefaultsMaxBody))
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return d, groupsSent{}, &apiError{http.StatusRequestEntityTooLarge, "body_too_large", "member defaults are at most 4 KiB"}
	}
	if err != nil {
		return d, groupsSent{}, &apiError{http.StatusBadRequest, "bad_request", err.Error()}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return d, groupsSent{}, &apiError{http.StatusBadRequest, "invalid_defaults", "invalid member defaults: " + err.Error()}
	}
	// The save replaces the document, so `null` must not read as "clear everything".
	if in == nil {
		return d, groupsSent{}, &apiError{http.StatusBadRequest, "invalid_defaults", "member defaults must be an object"}
	}
	d = *in
	d.normalize()
	// A second Decode must reach the end: dec.More() lets `{"agent":"codex"}]` through.
	if extra := dec.Decode(&struct{}{}); !errors.Is(extra, io.EOF) {
		return d, groupsSent{}, &apiError{http.StatusBadRequest, "invalid_defaults", "unexpected data after the defaults object"}
	}
	if err := d.validate(); err != nil {
		return d, groupsSent{}, &apiError{http.StatusBadRequest, "invalid_defaults", err.Error()}
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(body, &raw) // already decoded once above
	// encoding/json matches struct keys case-insensitively, but presence is detected by exact key:
	// "LAUNCH" would validate and then count as left out, and the save would discard it. Refuse
	// any top-level key that is not spelt canonically.
	for k := range raw {
		if k != "agent" && k != "launch" && k != "repos" && k != "ops" {
			return d, groupsSent{}, &apiError{http.StatusBadRequest, "invalid_defaults", "unknown field " + strconv.Quote(k) + " (keys are lowercase)"}
		}
	}
	present := func(k string) bool { v, ok := raw[k]; return ok && string(bytes.TrimSpace(v)) != "null" }
	return d, groupsSent{present("launch"), present("repos"), present("ops")}, nil
}

// auditDetail lists what the save set. Everything in the document is non-secret by validation, so
// the values themselves are recorded: that is what an admin asks the log for.
func (d tenantMemberDefaultsWire) auditDetail() string {
	parts := []string{"agent=" + d.Agent}
	add := func(k, v string) {
		if v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	add("launch.model", d.Launch.Model)
	add("launch.driver", d.Launch.Driver)
	add("launch.permission_mode", d.Launch.PermissionMode)
	if b := d.Launch.BudgetUSD; b != nil {
		parts = append(parts, fmt.Sprintf("launch.budget_usd=%g", *b))
	}
	if len(d.Repos) > 0 {
		parts = append(parts, "repos="+strings.Join(d.Repos, ","))
	}
	add("ops.grafana_url", d.Ops.GrafanaURL)
	add("ops.aws_sso_start_url", d.Ops.AWSSSOStartURL)
	add("ops.aws_account_id", d.Ops.AWSAccountID)
	add("ops.aws_role_name", d.Ops.AWSRoleName)
	return strings.Join(parts, " ")
}

// ---- P3: launch defaults, team repositories and ops settings ----
//// The three groups a tenant adds to its member defaults beyond the agent pointer (ADR 0114 P3):
//// launch defaults, team repositories and non-secret ops settings, plus the member's own override
//// of the first and last and the effective value the CP computes from the layers.
////
//// Launch defaults only PREFILL the launch dialog and the Start hub. They never seed or write the
//// Agent's ui-prefs, which the Agent keeps enforcing on its own (decision 1: one home per setting).
//// Nothing here is a secret: the ops fields are URLs and identifiers that are validated narrowly
//// enough that a token cannot ride in them.

const (
	maxTeamRepos      = 20
	maxTeamRepoLen    = 100
	maxOpsURLLen      = 200
	maxLaunchBudgetUS = 10000
)

// launchDriverValues and launchPermissionModes are closed sets. "terminal" is the Terminal (CLI)
// driver; the Console maps it to its own empty driver value.
var (
	launchDriverValues    = []string{"managed", "terminal"}
	launchPermissionModes = []string{"ask", "skip"}
)

var (
	// A model id is a short token (claude-opus-4-7, gpt-5.2, bedrock-style ids with : and /).
	launchModelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@#\[\]-]{0,99}$`)
	// "owner/name", optionally with subgroups (GitLab): the path half of a remote, no host.
	teamRepoRe = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+){1,3}$`)
	awsAcctRe  = regexp.MustCompile(`^[0-9]{12}$`)
	awsRoleRe  = regexp.MustCompile(`^[A-Za-z0-9+=,.@_-]{1,64}$`)
)

// launchDefaultsWire is the tenant's launch defaults and, in the member's ui-state, the member's
// own override; "" / null mean "not set at this layer". The agent is the document's `agent`
// (decision 1: one home), overridable by the member through `launch.agent`.
// Read by core/store/uiState.ts and features/repos/LaunchModal.tsx.
type launchDefaultsWire struct {
	Agent          string   `json:"agent"`
	Model          string   `json:"model"`
	Driver         string   `json:"driver"`          // "", managed, terminal
	PermissionMode string   `json:"permission_mode"` // "", ask, skip
	BudgetUSD      *float64 `json:"budget_usd"`      // null = unset, 0 = no budget
}

func inSet(v string, set []string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

func (l launchDefaultsWire) validate() error {
	if l.Agent != "" && !inSet(l.Agent, memberDefaultAgents) {
		return fmt.Errorf("launch.agent must be one of %s, or empty", strings.Join(memberDefaultAgents, ", "))
	}
	if l.Model != "" && !launchModelRe.MatchString(l.Model) {
		return fmt.Errorf("launch.model is not a model id")
	}
	if l.Driver != "" && !inSet(l.Driver, launchDriverValues) {
		return fmt.Errorf("launch.driver must be one of %s, or empty", strings.Join(launchDriverValues, ", "))
	}
	if l.PermissionMode != "" && !inSet(l.PermissionMode, launchPermissionModes) {
		return fmt.Errorf("launch.permission_mode must be one of %s, or empty", strings.Join(launchPermissionModes, ", "))
	}
	if b := l.BudgetUSD; b != nil && (*b < 0 || *b > maxLaunchBudgetUS || *b != *b) {
		return fmt.Errorf("launch.budget_usd must be between 0 and %d", maxLaunchBudgetUS)
	}
	return nil
}

// opsSettingsWire holds the non-secret operations settings (#2033 reads them). Every field is
// checked narrowly: a URL carries no credentials, query or fragment, an identifier no free text.
type opsSettingsWire struct {
	GrafanaURL     string `json:"grafana_url"`
	AWSSSOStartURL string `json:"aws_sso_start_url"`
	AWSAccountID   string `json:"aws_account_id"`
	AWSRoleName    string `json:"aws_role_name"`
}

// plainHTTPSURL accepts an https URL that cannot carry a secret: no userinfo, query or fragment.
func plainHTTPSURL(field, v string) error {
	if v == "" {
		return nil
	}
	u, err := url.Parse(v)
	if err != nil || len(v) > maxOpsURLLen || u.Scheme != "https" || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(v, " \t\r\n") {
		return fmt.Errorf("%s must be an https URL without credentials, query or fragment", field)
	}
	return nil
}

func (o opsSettingsWire) validate() error {
	if err := plainHTTPSURL("ops.grafana_url", o.GrafanaURL); err != nil {
		return err
	}
	if err := plainHTTPSURL("ops.aws_sso_start_url", o.AWSSSOStartURL); err != nil {
		return err
	}
	if o.AWSAccountID != "" && !awsAcctRe.MatchString(o.AWSAccountID) {
		return fmt.Errorf("ops.aws_account_id must be 12 digits")
	}
	if o.AWSRoleName != "" && !awsRoleRe.MatchString(o.AWSRoleName) {
		return fmt.Errorf("ops.aws_role_name is not a role name")
	}
	return nil
}

// validateTeamRepos checks the list offered first in Start: "owner/name" paths, bounded.
func validateTeamRepos(repos []string) error {
	if len(repos) > maxTeamRepos {
		return fmt.Errorf("repos holds at most %d entries", maxTeamRepos)
	}
	seen := map[string]bool{}
	for _, r := range repos {
		if len(r) > maxTeamRepoLen || !teamRepoRe.MatchString(r) {
			return fmt.Errorf("repos entries must be owner/name paths")
		}
		if k := strings.ToLower(r); seen[k] {
			return fmt.Errorf("repos lists %s twice", r)
		} else {
			seen[k] = true
		}
	}
	return nil
}

// effectiveWire is what the member's Console uses: built-in < tenant default < member's own
// choice (decision 8), merged field by field here so the Console never re-derives it. A field
// no layer set stays empty and the built-in behaviour applies.
type effectiveWire struct {
	Launch launchDefaultsWire `json:"launch"`
	Repos  []string           `json:"repos"`
	Ops    opsSettingsWire    `json:"ops"`
}

func pick(member, tenant string) string {
	if member != "" {
		return member
	}
	return tenant
}

func effectiveDefaults(t tenantMemberDefaultsWire, m memberUIStateWire) effectiveWire {
	l := launchDefaultsWire{Agent: t.Agent, Model: t.Launch.Model, Driver: t.Launch.Driver, PermissionMode: t.Launch.PermissionMode, BudgetUSD: t.Launch.BudgetUSD}
	o := t.Ops
	if m.Launch != nil {
		l.Agent, l.Model = pick(m.Launch.Agent, l.Agent), pick(m.Launch.Model, l.Model)
		l.Driver, l.PermissionMode = pick(m.Launch.Driver, l.Driver), pick(m.Launch.PermissionMode, l.PermissionMode)
		if m.Launch.BudgetUSD != nil {
			l.BudgetUSD = m.Launch.BudgetUSD
		}
	}
	if m.Ops != nil {
		o.GrafanaURL, o.AWSSSOStartURL = pick(m.Ops.GrafanaURL, o.GrafanaURL), pick(m.Ops.AWSSSOStartURL, o.AWSSSOStartURL)
		o.AWSAccountID, o.AWSRoleName = pick(m.Ops.AWSAccountID, o.AWSAccountID), pick(m.Ops.AWSRoleName, o.AWSRoleName)
	}
	return effectiveWire{Launch: l, Repos: append([]string{}, t.Repos...), Ops: o}
}
