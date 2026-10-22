// mcp_oauth_trusted.go — which client hosts the MCP OAuth authorization server trusts (ADR 0118),
// and the super-admin API that edits the list at runtime.
//
// Precedence, decided per request: the value a super admin saved, else AF_MCP_OAUTH_TRUSTED_HOSTS,
// else the built-in default. Nothing is cached: the Control Plane may run several replicas, and a
// per-replica cache invalidated on save would leave the others on the old list.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"slices"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const (
	// mcpOAuthTrustedSetting holds a JSON array of host names. Even "none" is stored ("[]"), so
	// an empty value always means "nothing saved" and the two never share a spelling.
	mcpOAuthTrustedSetting = "mcp_oauth_trusted_hosts"

	trustSourceAdmin   = "admin"
	trustSourceEnv     = "env"
	trustSourceDefault = "default"
)

// trustedHosts is the set of exact host names that may receive a sign-in redirect and serve
// client metadata. The nil set trusts nothing, which leaves loopback clients only.
type trustedHosts map[string]bool

func (t trustedHosts) sorted() []string {
	out := make([]string, 0, len(t))
	for h := range t {
		out = append(out, h)
	}
	slices.Sort(out)
	return out
}

// validateTrustedClientHosts is parseTrustedClientHosts without the leniency: an entry the
// parser would skip is an error here, so a typo is reported instead of silently narrowing (or,
// after a fallback, widening) the list. It returns the lower-cased, de-duplicated, sorted hosts.
// An empty input is valid and means "none".
func validateTrustedClientHosts(in []string) ([]string, error) {
	seen := map[string]bool{}
	for _, raw := range in {
		h := strings.ToLower(strings.TrimSpace(raw))
		switch {
		case h == "":
			return nil, errors.New("a host name is empty")
		case strings.Contains(h, "://"):
			return nil, errors.New(`"` + raw + `": give the host name only, without a scheme (https is implied)`)
		case strings.ContainsAny(h, ":/@?#\\ "):
			return nil, errors.New(`"` + raw + `": give the bare host name, without a port, path or user name`)
		case strings.Contains(h, "*"):
			return nil, errors.New(`"` + raw + `": wildcards are not supported, list each exact host`)
		case h == "none":
			return nil, errors.New(`"none" is not a host: save an empty list to trust no hosted client`)
		case net.ParseIP(h) != nil:
			return nil, errors.New(`"` + raw + `": an IP address is not accepted, use a host name`)
		case !validHostName(h):
			return nil, errors.New(`"` + raw + `" is not a valid host name (letters, digits and hyphens in dot-separated labels)`)
		}
		seen[h] = true
	}
	return trustedHosts(seen).sorted(), nil
}

func validHostName(h string) bool {
	if len(h) > 253 {
		return false
	}
	for _, l := range strings.Split(h, ".") {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, c := range l {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

// envTrustedHosts is the start-up fallback and the source it came from.
func envTrustedHosts() (trustedHosts, string) {
	if v := envx.Or("AF_MCP_OAUTH_TRUSTED_HOSTS", ""); v != "" {
		return parseTrustedClientHosts(v), trustSourceEnv
	}
	return parseTrustedClientHosts(defaultMCPOAuthTrustedHosts), trustSourceDefault
}

// savedTrustedHosts reads the super admin's value. ok is false when nothing is saved.
func (a *mcpOAuthAPI) savedTrustedHosts(ctx context.Context) (hosts []string, ok bool, err error) {
	raw, err := a.mgr.store.GetSetting(ctx, mcpOAuthTrustedSetting)
	if err != nil || raw == "" {
		return nil, false, err
	}
	var list []string
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, true, err
	}
	hosts, err = validateTrustedClientHosts(list)
	return hosts, true, err
}

// trustedNow is the list in force for one request. When the saved value cannot be read or is
// not a valid list it answers the empty set, never the env or default list: a failure must not
// widen what is trusted. Callers take one snapshot per request so a save mid-request cannot make
// two checks of the same request disagree.
func (a *mcpOAuthAPI) trustedNow(ctx context.Context) (trustedHosts, string) {
	saved, ok, err := a.savedTrustedHosts(ctx)
	switch {
	case err != nil:
		log.Printf("mcp oauth: trusted hosts unreadable, trusting none: %v", err)
		return trustedHosts{}, trustSourceAdmin
	case ok:
		t := trustedHosts{}
		for _, h := range saved {
			t[h] = true
		}
		return t, trustSourceAdmin
	}
	return a.fallback, a.fallbackSource
}

// --- admin API (super_admin) --------------------------------------------------------

type mcpOAuthTrustedView struct {
	Hosts []string `json:"hosts"`
	// Source is which value is in force: "admin" (saved in the Console), "env"
	// (AF_MCP_OAUTH_TRUSTED_HOSTS) or "default" (built in).
	Source string `json:"source"`
	// Fallback is what a reset would put back, and FallbackSource says where it comes from.
	Fallback       []string `json:"fallback"`
	FallbackSource string   `json:"fallback_source"`
	// Served is false when this deployment does not run the OAuth server, so the list has no
	// effect yet.
	Served bool `json:"served"`
}

func (a *mcpOAuthAPI) trustedView(ctx context.Context) mcpOAuthTrustedView {
	t, src := a.trustedNow(ctx)
	return mcpOAuthTrustedView{Hosts: t.sorted(), Source: src, Fallback: a.fallback.sorted(),
		FallbackSource: a.fallbackSource, Served: a.issuer != ""}
}

// trustedAdmin serves GET, PUT and DELETE /api/admin/mcp-oauth/trusted-hosts. PUT body:
// {"hosts":[...]}; an empty array saves "none" (loopback clients only). DELETE removes the saved
// value so the env var, then the built-in default, applies again.
func (a *mcpOAuthAPI) trustedAdmin(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	ctx := r.Context()
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, a.trustedView(ctx))
		return
	}
	before := a.trustedView(ctx)
	action := "mcp_oauth.trusted_hosts.reset"
	switch r.Method {
	case http.MethodPut:
		var b struct {
			Hosts *[]string `json:"hosts"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil || b.Hosts == nil {
			writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_body", `body must be {"hosts":[...]}`})
			return
		}
		hosts, err := validateTrustedClientHosts(*b.Hosts)
		if err != nil {
			writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_hosts", err.Error()})
			return
		}
		enc, _ := json.Marshal(hosts)
		if err := a.mgr.store.SetSetting(ctx, mcpOAuthTrustedSetting, string(enc)); err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		action = "mcp_oauth.trusted_hosts.set"
	default:
		if err := a.mgr.store.DeleteSetting(ctx, mcpOAuthTrustedSetting); err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
	}
	after := a.trustedView(ctx)
	detail, _ := json.Marshal(map[string]any{
		"old": before.Hosts, "old_source": before.Source, "new": after.Hosts, "new_source": after.Source,
	})
	if err := a.mgr.store.InsertAudit(ctx, store.AuditLog{
		ID: store.NewID(), ActorKind: "admin", ActorID: ident.ID,
		Action: action, Target: mcpOAuthTrustedSetting, Detail: string(detail), At: store.NowTS(),
	}); err != nil {
		log.Printf("mcp oauth: audit of %s failed: %v", action, err)
	}
	writeJSON(w, http.StatusOK, after)
}
