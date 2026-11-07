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
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const (
	// mcpOAuthTrustedSetting holds a JSON array of entries (host names or https URL prefixes). Even "none" is stored ("[]"), so
	// an empty value always means "nothing saved" and the two never share a spelling.
	mcpOAuthTrustedSetting = "mcp_oauth_trusted_hosts"

	trustSourceAdmin   = "admin"
	trustSourceEnv     = "env"
	trustSourceDefault = "default"
)

// trustedHosts is the set of trusted-client entries. Each key is either a bare host name
// ("claude.ai": any https path on that host) or an https URL prefix
// ("https://claude.ai/api/mcp/auth_callback": that path and the segments below it). The nil set
// trusts nothing, which leaves loopback clients only.
type trustedHosts map[string]bool

func (t trustedHosts) sorted() []string {
	out := make([]string, 0, len(t))
	for h := range t {
		out = append(out, h)
	}
	slices.Sort(out)
	return out
}

const trustedPrefixScheme = "https://"

// splitTrustedEntry returns the host and, for a URL-prefix entry, its path ("" for a bare host).
// Entries are validated or lenient-parsed before they get here, so it does no checking.
func splitTrustedEntry(e string) (host, prefix string) {
	rest, ok := strings.CutPrefix(e, trustedPrefixScheme)
	if !ok {
		return e, ""
	}
	i := strings.IndexByte(rest, '/')
	return rest[:i], rest[i:]
}

// hostTrusted reports whether any entry names this host. It gates fetching a client-metadata
// document and re-admitting a recorded grant host, where only the host is known; the redirect
// itself is checked by admits.
func (t trustedHosts) hostTrusted(host string) bool {
	if t[host] {
		return true
	}
	for e := range t {
		if h, p := splitTrustedEntry(e); p != "" && h == host {
			return true
		}
	}
	return false
}

// admits reports whether an https redirect URI (already canonical: no userinfo, no explicit
// port) is covered by an entry. A bare-host entry covers every path. A prefix entry covers its
// own path and the path segments below it, so "/cb" admits "/cb" and "/cb/x" but never
// "/cbx". The path is compared parsed, and only when it is spelled plainly: a path with
// any percent-escape (%2f, %2e, %20), dot segments or empty segments is refused, because the
// browser and this check could otherwise disagree about where a segment ends.
func (t trustedHosts) admits(u *url.URL) bool {
	if t[u.Host] {
		return true
	}
	if strings.Contains(u.EscapedPath(), "%") || u.Path == "" || u.Path[0] != '/' || strings.Contains(u.Path, "//") {
		return false
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	for e := range t {
		h, p := splitTrustedEntry(e)
		if p == "" || h != u.Host {
			continue
		}
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(u.Path, p) {
				return true
			}
		} else if u.Path == p || strings.HasPrefix(u.Path, p+"/") {
			return true
		}
	}
	return false
}

// hostOnlyEntries lists the bare-host entries: the ones that trust every path on a host.
func (t trustedHosts) hostOnlyEntries() []string {
	var out []string
	for _, e := range t.sorted() {
		if _, p := splitTrustedEntry(e); p == "" {
			out = append(out, e)
		}
	}
	return out
}

// parseTrustedEntry validates one entry and returns its canonical (lower-cased) spelling. A
// bare host trusts every path; an https URL with a path trusts that path prefix.
func parseTrustedEntry(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	h := strings.ToLower(raw)
	if strings.HasPrefix(h, trustedPrefixScheme) {
		return parseTrustedPrefix(raw, h)
	}
	switch {
	case h == "":
		return "", errors.New("a host name is empty")
	case strings.Contains(h, "://"):
		return "", errors.New(`"` + raw + `": only https is supported (give the host name, or an https URL with a path)`)
	case strings.ContainsAny(h, ":/@?#\\ "):
		return "", errors.New(`"` + raw + `": give the bare host name, or an https URL with a path`)
	case strings.Contains(h, "*"):
		return "", errors.New(`"` + raw + `": wildcards are not supported, list each exact host`)
	case h == "none":
		return "", errors.New(`"none" is not a host: save an empty list to trust no hosted client`)
	case net.ParseIP(h) != nil:
		return "", errors.New(`"` + raw + `": an IP address is not accepted, use a host name`)
	case !validHostName(h):
		return "", errors.New(`"` + raw + `" is not a valid host name (letters, digits and hyphens in dot-separated labels)`)
	}
	return h, nil
}

// parseTrustedPrefix canonicalises scheme and host to lower case; the path keeps its spelling,
// because callback paths are case-sensitive and a folded path would trust a different one.
func parseTrustedPrefix(raw, h string) (string, error) {
	bad := func(why string) (string, error) { return "", errors.New(`"` + raw + `": ` + why) }
	if strings.ContainsAny(h, "\\ ?#%@") || strings.Contains(h, "*") {
		return bad("a URL prefix is host and path only: no query, fragment, user name, escapes or wildcards")
	}
	rest := raw[len(trustedPrefixScheme):]
	i := strings.IndexByte(rest, '/')
	if i < 0 || rest[i:] == "/" {
		return bad("give a path (https://host/path), or the bare host name to trust every path")
	}
	host, p := strings.ToLower(rest[:i]), rest[i:]
	if strings.Contains(host, ":") {
		return bad("a port is not accepted")
	}
	if net.ParseIP(host) != nil || !validHostName(host) {
		return bad("not a valid host name")
	}
	for _, c := range []byte(p) {
		if c <= 0x20 || c >= 0x7f {
			return bad("the path must be plain ASCII")
		}
	}
	for _, seg := range strings.Split(strings.TrimSuffix(p, "/")[1:], "/") {
		if seg == "" || seg == "." || seg == ".." {
			return bad("the path must not contain empty or dot segments")
		}
	}
	return trustedPrefixScheme + host + p, nil
}

// validateTrustedClientHosts is the lenient parser without the leniency: an entry the parser
// would skip is an error here, so a typo is reported instead of silently narrowing (or, after a
// fallback, widening) the list. It returns the lower-cased, de-duplicated, sorted entries. An
// empty input is valid and means "none".
func validateTrustedClientHosts(in []string) ([]string, error) {
	seen := map[string]bool{}
	for _, raw := range in {
		e, err := parseTrustedEntry(raw)
		if err != nil {
			return nil, err
		}
		seen[e] = true
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

// errTrustedCorrupt marks a saved value that is there but is not a valid list. It is told apart
// from a store failure because an admin must still be able to overwrite or reset it.
var errTrustedCorrupt = errors.New("saved trusted-host list is not valid")

// savedTrustedHosts reads the super admin's value. ok is false when nothing is saved. A store
// failure is returned as is; an unparsable value wraps errTrustedCorrupt.
func (a *mcpOAuthAPI) savedTrustedHosts(ctx context.Context) (hosts []string, ok bool, err error) {
	raw, err := a.mgr.store.GetSetting(ctx, mcpOAuthTrustedSetting)
	if err != nil || raw == "" {
		return nil, false, err
	}
	var list []string
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, true, fmt.Errorf("%w: %v", errTrustedCorrupt, err)
	}
	hosts, err = validateTrustedClientHosts(list)
	if err != nil {
		err = fmt.Errorf("%w: %v", errTrustedCorrupt, err)
	}
	return hosts, true, err
}

// trustedNow is the list in force for one request. A saved value that is not a valid list
// answers the empty set, never the env or default list: a failure must not widen what is
// trusted. A store failure is returned so the caller can answer 5xx instead of a misleading
// refusal. Callers take one snapshot per request so a save mid-request cannot make two checks of
// the same request disagree.
func (a *mcpOAuthAPI) trustedNow(ctx context.Context) (trustedHosts, string, error) {
	saved, ok, err := a.savedTrustedHosts(ctx)
	switch {
	case errors.Is(err, errTrustedCorrupt):
		log.Printf("mcp oauth: trusted hosts unusable, trusting none: %v", err)
		return trustedHosts{}, trustSourceAdmin, nil
	case err != nil:
		return nil, "", err
	case ok:
		t := trustedHosts{}
		for _, h := range saved {
			t[h] = true
		}
		return t, trustSourceAdmin, nil
	}
	return a.fallback, a.fallbackSource, nil
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
	// HostOnly lists the entries in force that name a bare host and therefore trust every path
	// on it. The Console warns about them: whoever controls any path there can register a
	// client that is accepted.
	HostOnly []string `json:"host_only"`
	// Served is false when this deployment does not run the OAuth server, so the list has no
	// effect yet.
	Served bool `json:"served"`
	// Unreadable is true when the saved value is not a valid list: nothing is trusted until it
	// is overwritten or reset.
	Unreadable bool `json:"unreadable,omitempty"`
}

// trustedView reports the list in force. A store failure is an error: a view built from it
// would show an empty saved list that was never read.
func (a *mcpOAuthAPI) trustedView(ctx context.Context) (mcpOAuthTrustedView, error) {
	_, _, serr := a.savedTrustedHosts(ctx)
	t, src, err := a.trustedNow(ctx)
	if err != nil {
		return mcpOAuthTrustedView{}, err
	}
	hostOnly := t.hostOnlyEntries()
	if hostOnly == nil {
		hostOnly = []string{}
	}
	return mcpOAuthTrustedView{Hosts: t.sorted(), HostOnly: hostOnly, Source: src, Fallback: a.fallback.sorted(),
		FallbackSource: a.fallbackSource, Served: a.issuer != "", Unreadable: errors.Is(serr, errTrustedCorrupt)}, nil
}

// trustedAdmin serves GET, PUT and DELETE /api/admin/mcp-oauth/trusted-hosts. PUT body:
// {"hosts":[...]}; an empty array saves "none" (loopback clients only). DELETE removes the saved
// value so the env var, then the built-in default, applies again.
func (a *mcpOAuthAPI) trustedAdmin(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	ctx := r.Context()
	before, err := a.trustedView(ctx)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, before)
		return
	}
	action := "mcp_oauth.trusted_hosts.reset"
	switch r.Method {
	case http.MethodPut:
		var b struct {
			Hosts *[]string `json:"hosts"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
		if err := dec.Decode(&b); err != nil || b.Hosts == nil || dec.Decode(&struct{}{}) != io.EOF {
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
		// Nothing was saved, so nothing changed: no audit row for a no-op.
		if before.Source != trustSourceAdmin {
			writeJSON(w, http.StatusOK, before)
			return
		}
	}
	after, err := a.trustedView(ctx)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	detail, _ := json.Marshal(map[string]any{
		"old": before.Hosts, "old_source": before.Source, "old_unreadable": before.Unreadable, "new": after.Hosts, "new_source": after.Source,
	})
	if err := a.mgr.store.InsertAudit(ctx, store.AuditLog{
		ID: store.NewID(), ActorKind: "admin", ActorID: ident.ID,
		Action: action, Target: mcpOAuthTrustedSetting, Detail: string(detail), At: store.NowTS(),
	}); err != nil {
		log.Printf("mcp oauth: audit of %s failed: %v", action, err)
	}
	writeJSON(w, http.StatusOK, after)
}
