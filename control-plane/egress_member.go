package main

// Member-facing egress allowlist face — the safety valve that ties the MCP registry to
// egress control (docs/log/48 §9 / P5, docs/log/20 M3).
//
// A remote MCP server IS an outbound destination. When the deployment routes workspace
// egress through the forward proxy (AF_EGRESS_PROXY_ADDR), a server whose host is not on
// the allowlist either fails outright (enforce) or works today and stops working the day
// an operator flips enforce (log-only). Neither failure explains itself from inside a CLI
// session — the member sees an MCP server that "just doesn't connect" — so the place to
// say it is registration time, in the form that carries the URL.
//
// Two endpoints, both open to any authenticated member, because the person registering an
// MCP server is usually not an admin:
//
//	GET  /api/egress/check?host=…  is this destination allowed / already requested?
//	POST /api/egress/propose       request it
//
// The write can ONLY ever produce state=proposed. Approval stays super_admin
// (POST /api/admin/egress/allowlist/{id}/state), so this gives a member a way to ASK
// without giving them a way to widen the deployment's egress — the same split the M4
// agent tool takes (mcp.go propose_allowlist_change).

import (
	"encoding/json"
	"net"
	"net/http"
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Wire codes for the member face. When one is added or renamed, update the matching
// "err.<code>" in console/src/lib/i18n/locales/{ja,en}/errors.ts at the same time
// (docs/log/48 §11.3: one reason = one code).
const (
	codeEgressEntryInvalid = "egress_entry_invalid"
	codeEgressEntryBroad   = "egress_entry_too_broad"
	codeEgressTooMany      = "egress_too_many_proposals"
)

// maxEgressCheckHosts bounds one check request. The Console asks about the hosts on one
// screen, so a few dozen is generous; the cap exists to keep a malformed caller from
// turning one request into an unbounded loop.
const maxEgressCheckHosts = 50

// maxOpenEgressProposals caps the outstanding queue. Duplicate entries are collapsed
// below, so this only bites on a client inventing distinct hosts — at which point the
// approval queue is the thing being damaged, and refusing is kinder than burying the
// real requests.
const maxOpenEgressProposals = 200

// maxEgressReasonLen keeps a free-text reason to something an admin can read in a list.
const maxEgressReasonLen = 500

// egressEntryRe is the shape of an allowlist entry: a host, or a ".suffix" covering a
// domain and its subdomains. Deliberately narrower than "anything the policy would
// accept" — a scheme, port or path in an entry silently never matches, and a silent
// never-match is exactly the failure this whole feature exists to prevent.
var egressEntryRe = regexp.MustCompile(`^\.?[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

// normalizeEgressEntry validates a proposed entry and returns it in the policy's own
// normal form (lowercase, "*.x" as ".x", no trailing dot).
func normalizeEgressEntry(raw string) (string, *apiError) {
	e := strings.ToLower(strings.TrimSpace(raw))
	e = strings.TrimPrefix(e, "*") // "*.x" -> ".x", the form newEgressPolicy stores
	e = strings.TrimSuffix(e, ".") // a trailing FQDN dot never appears in an entry
	if e == "" || strings.Contains(e, "..") || !egressEntryRe.MatchString(e) {
		return "", &apiError{http.StatusBadRequest, codeEgressEntryInvalid,
			"entry must be a host or a .suffix, with no scheme / port / path: " + raw}
	}
	// ".com" would open an entire TLD to every workspace in the deployment. A member
	// asking for a destination never needs that, and an admin approving a queue of
	// requests should not have to spot it.
	if strings.HasPrefix(e, ".") && strings.Count(e, ".") < 2 {
		return "", &apiError{http.StatusBadRequest, codeEgressEntryBroad,
			"a suffix entry must cover a domain, not a whole TLD: " + raw}
	}
	return e, nil
}

// normalizeEgressHost reduces whatever the caller sent — a bare host, a host:port, or a
// whole URL — to the host the proxy would match on. Being forgiving here means the
// Console can pass a URL straight through without re-implementing the parsing.
func normalizeEgressHost(raw string) string {
	h := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	h = strings.SplitN(h, "/", 2)[0]
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	// A bracketed IPv6 literal with no port survives SplitHostPort untouched; the
	// allowlist holds the bare address, so unwrap it here (the Console does the same).
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	return strings.TrimSuffix(h, ".")
}

// egressHostVerdict is one destination's answer.
type egressHostVerdict struct {
	Host    string `json:"host"`    // the normalized host the verdict is about
	Allowed bool   `json:"allowed"` // the effective allowlist covers it
	// Proposed reports that a pending request already covers this host — so the Console
	// shows "awaiting approval" instead of offering to file the same request again.
	Proposed bool `json:"proposed"`
}

// checkHosts (GET /api/egress/check?host=a&host=b) answers, per destination, whether the
// deployment's egress policy lets a workspace reach it.
//
// `configured` is the honest gate: with no forward proxy wired (the default — docs/log/20 M2
// ships the container wiring OFF), nothing is constrained and the Console must stay quiet
// rather than warn about a restriction that does not exist in this deployment.
func (a egressAPI) checkHosts(w http.ResponseWriter, r *http.Request, _ store.Identity, _ store.MembershipView) {
	entries, enforce := a.effectivePolicy(r.Context())
	pol := newEgressPolicy(entries)
	// Pending requests are matched with the same policy machinery, so a proposed
	// ".example.com" correctly covers "mcp.example.com".
	var pending []string
	if rows, err := a.store.ListAllowlist(r.Context(), "proposed", 500); err == nil {
		for _, e := range rows {
			pending = append(pending, e.Entry)
		}
	}
	pendingPol := newEgressPolicy(pending)

	hosts := r.URL.Query()["host"]
	if len(hosts) > maxEgressCheckHosts {
		hosts = hosts[:maxEgressCheckHosts]
	}
	// Keyed by the string the caller sent, not by the normalized host: the Console looks
	// the answer up by exactly what it asked about, so the two sides cannot drift apart
	// over a normalisation detail.
	out := make(map[string]egressHostVerdict, len(hosts))
	for _, raw := range hosts {
		h := normalizeEgressHost(raw)
		if h == "" {
			continue
		}
		out[raw] = egressHostVerdict{Host: h, Allowed: pol.allows(h), Proposed: pendingPol.allows(h)}
	}
	mode := "log-only"
	if enforce {
		mode = "enforce"
	}
	writeJSON(w, http.StatusOK, egressCheckWire{
		Configured: a.proxy != "", Mode: mode, Enforce: enforce, Hosts: out,
	})
}

// egressCheckWire is the response of GET /api/egress/check (the Console's `EgressCheck`,
// console/src/features/settings/mcp/egressCheck.ts).
//
// was: map[string]any{"configured":…, "mode":…, "enforce":…, "hosts":…}
// All four keys were unconditionally present, so no omitempty: with it, configured=false
// or mode="" would drop the key and change the wire. Hosts is always make'd and never
// nil, so `{}` is emitted. TestWireEquivEgressCheck (wiremap_equiv_test.go) shows the
// equivalence against the old map.
type egressCheckWire struct {
	Configured bool                         `json:"configured"`
	Mode       string                       `json:"mode"`
	Enforce    bool                         `json:"enforce"`
	Hosts      map[string]egressHostVerdict `json:"hosts"`
}

// propose (POST /api/egress/propose) files a request to allow one destination. Body:
// {entry, reason}. It creates a PROPOSED entry only — never an active one — which is what
// makes this safe to expose to an ordinary member.
//
// The row is deployment-global (TenantID ""), matching what approval actually does:
// EffectiveAllowlist ignores scope, so recording the requester's tenant on the row would
// promise a per-tenant rule the proxy does not implement. The tenant is carried on the
// audit entry instead, where it is context rather than a claim about effect.
func (a egressAPI) propose(w http.ResponseWriter, r *http.Request, ident store.Identity, mv store.MembershipView) {
	var b struct{ Entry, Reason string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_body", "invalid JSON"})
		return
	}
	entry, aerr := normalizeEgressEntry(b.Entry)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	rows, err := a.store.ListAllowlist(r.Context(), "", 1000)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	open := 0
	for _, e := range rows {
		if e.State == "proposed" {
			open++
		}
		if e.Entry != entry {
			continue
		}
		switch e.State {
		case "active":
			// Nothing to ask for. Answering with the state (rather than filing a duplicate)
			// lets the Console just re-check and drop its warning.
			writeJSON(w, http.StatusOK, map[string]any{"entry": entry, "state": "active", "already": true})
			return
		case "proposed":
			writeJSON(w, http.StatusOK, map[string]any{"id": e.ID, "entry": entry, "state": "proposed", "already": true})
			return
		}
		// retired: a previously rejected entry may legitimately be asked for again — a new
		// request with a new reason is the point of asking twice.
	}
	if open >= maxOpenEgressProposals {
		writeAPIErr(w, &apiError{http.StatusTooManyRequests, codeEgressTooMany,
			"too many pending allowlist requests — ask an administrator to work through the queue"})
		return
	}
	reason := strings.TrimSpace(b.Reason)
	if len(reason) > maxEgressReasonLen {
		reason = reason[:maxEgressReasonLen]
	}
	e := store.AllowlistEntry{
		ID: store.NewID(), TenantID: "", Entry: entry, State: "proposed",
		Reason: reason, AddedBy: ident.Email, AddedAt: store.NowTS(),
	}
	if err := a.store.AddAllowlist(r.Context(), e); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// actor_kind=user (not admin): this is a member asking, and the audit view should not
	// read as if an administrator had changed egress policy.
	_ = a.store.InsertAudit(r.Context(), store.AuditLog{
		ID: store.NewID(), TenantID: mv.TenantID, ActorKind: "user", ActorID: ident.ID,
		Action: "egress.propose", Target: entry, Detail: "reason=" + reason, At: store.NowTS(),
	})
	writeJSON(w, http.StatusOK, map[string]any{"id": e.ID, "entry": entry, "state": "proposed"})
}
