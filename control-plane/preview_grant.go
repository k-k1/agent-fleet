// preview_grant.go — temporary preview grants (#2138, ADR 0062 note on decision 6).
//
// The preview list governs what OTHER people can reach: public-mode visitors and
// tenant-share colleagues. The owner can already reach every port through /preview/{port}/,
// so opening an unlisted port on the owner's own subdomain adds no reach. A grant makes that
// possible for the owner alone, and only while the listener it was made for is still there:
//
//   - made by GET /preview-open-own?port=N on the Console origin (caller's own workspace;
//     there is no owner parameter), after the Agent reported the port's listening sockets and
//     that it answers HTTP;
//   - honoured in serve / handshake / siblingOrigin only with an owner cookie, public mode or
//     not, and answered with a bare 404 (no handshake) to everyone else;
//   - checked against the live listener on every relayed request: the CP sends the granted
//     inodes and the Agent compares them to /proc/net/tcp{,6}. Staleness bound: the Agent's
//     cache, 2 s. A different process binding the port later has a different inode and does
//     not inherit the grant.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// previewOwnOpenPath is the owner-only entry. It is not a link to share: the handler always
// acts on the caller's own workspace.
const previewOwnOpenPath = "/preview-open-own"

// maxPreviewTempPorts caps live grants per workspace.
const maxPreviewTempPorts = 16

// previewListenerHeader names the granted listener on the CP→Agent hop (see the Agent's
// preview_errors.go). The CP deletes any client-supplied copy before relaying.
const previewListenerHeader = "X-AF-Preview-Listener"

// previewListenerClient bounds the Agent call; the Agent's own probe takes up to ~2 s.
var previewListenerClient = &http.Client{
	Timeout:       8 * time.Second,
	Transport:     newAgentTransport(),
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// liveGrant returns the workspace's grant for port when it was made under the CURRENT slug.
// Whether the listener is still alive is the Agent's call, per request.
func (a previewHostAPI) liveGrant(ctx context.Context, ws store.Workspace, port int) (store.PreviewTempPort, bool) {
	return a.mgr.liveGrant(ctx, ws, port)
}

func (m *manager) liveGrant(ctx context.Context, ws store.Workspace, port int) (store.PreviewTempPort, bool) {
	if ws.PreviewSlug == "" {
		return store.PreviewTempPort{}, false
	}
	g, ok, err := m.store.GetPreviewTempPort(ctx, ws.ID, port)
	if err != nil || !ok || g.Slug != ws.PreviewSlug || g.Inodes == "" {
		return store.PreviewTempPort{}, false
	}
	return g, true
}

// previewPortOpenableBy is the port half of the stable-open decision, shared by openStable and
// the dev IdP's redirect ACL so the two cannot drift: a listed port is open to every viewer
// previewViewerAllowed admits; an unlisted one only to the owner, and only under a live grant.
// Lookup errors count as "no grant" (fail closed).
func (m *manager) previewPortOpenableBy(ctx context.Context, ws store.Workspace, st wsSettings, membershipID string, port int) bool {
	if previewPortAllowed(st, port) {
		return true
	}
	if membershipID == "" || membershipID != ws.MembershipID {
		return false
	}
	_, live := m.liveGrant(ctx, ws, port)
	return live
}

// agentListener is the Agent's answer to GET /preview-listener/{port}.
type agentListener struct {
	Inodes []string `json:"inodes"`
	Probe  string   `json:"probe"`
}

// agentPreviewListener asks the Agent for the port's listening sockets and an HTTP probe. A
// non-200 (404 from an older Agent, 400 for a refused port) comes back as status with a nil
// listener, so the caller fails closed.
func agentPreviewListener(ctx context.Context, rt runtime.Runtime, port int, probe bool) (*agentListener, int, error) {
	if rt.Endpoint() == "" {
		return nil, 0, http.ErrServerClosed
	}
	q := ""
	if probe {
		q = "?probe=1"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		rt.Endpoint()+"/preview-listener/"+strconv.Itoa(port)+q, nil)
	if tok := rt.Token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := previewListenerClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, nil
	}
	var out agentListener
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, resp.StatusCode, err
	}
	return &out, resp.StatusCode, nil
}

// openOwn (GET /preview-open-own?port=N[&next=/path]) runs on the CONSOLE origin, inside
// authGate. It resolves the caller's OWN workspace, makes (or renews) a grant for an
// unlisted port, and redirects to the same handshake openStable uses.
//
// A cross-site page can make the owner's browser call this and so create a grant; the grant
// gives that page nothing, because only the owner's cookie can use it. The cost is an audit
// row. Do not turn this into anything a non-owner can trigger.
func (a previewHostAPI) openOwn(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	port, _ := strconv.Atoi(r.URL.Query().Get("port"))
	if port < 1 || port > 65535 {
		writePreviewPage(w, r, http.StatusBadRequest, previewBadPort, port)
		return
	}
	id := a.mgr.resolveIdentity(r)
	if id.key == "" {
		writeAPIErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "no gateway identity"})
		return
	}
	ident, mv, aerr := a.mgr.resolveMembership(ctx, id.key, id.email, tenantSel(r))
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	ws, ok, err := a.mgr.store.GetWorkspaceByMembership(ctx, mv.MembershipID)
	if err != nil {
		http.Error(w, "preview lookup failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		previewNotFound(w)
		return
	}
	if ws.PreviewSlug == "" {
		// The caller is the owner here, so saying "not running" reveals nothing.
		http.Error(w, "your workspace is not running — its preview URL is issued at start", http.StatusConflict)
		return
	}
	st := parseWSSettings(mustSettings(ctx, a.mgr, ws.ID))
	if !previewPortAllowed(st, port) {
		if refusal := a.grant(ctx, ws, ident.ID, port); refusal != nil {
			writePreviewPage(w, r, refusal.status, refusal.kind, port)
			return
		}
	}
	q := url.Values{}
	q.Set("slug", ws.PreviewSlug)
	q.Set("port", strconv.Itoa(port))
	q.Set("next", safePreviewNext(r.URL.Query().Get("next")))
	http.Redirect(w, r, previewHandshakePath+"?"+q.Encode(), http.StatusFound)
}

type grantRefusal struct {
	status int
	kind   string
}

// grant checks the port with the Agent and records the grant. nil = granted.
//
// The count, the write and the audit row are decided in one store transaction under the
// workspace lock (SavePreviewTempPort), which also refuses a grant whose slug was rotated
// while the probe ran.
func (a previewHostAPI) grant(ctx context.Context, ws store.Workspace, actorID string, port int) *grantRefusal {
	// Captured BEFORE the probe: a stop / start / reissue while it runs changes the epoch and
	// makes the save below refuse (a fixed slug comes back as the same string, so the slug alone
	// cannot tell).
	epoch, err := a.mgr.store.PreviewEpoch(ctx, ws.ID)
	if err != nil {
		return &grantRefusal{http.StatusInternalServerError, "preview_failed"}
	}
	rt := a.mgr.runtimeFor(ws, noSecretKeys)
	got, status, err := agentPreviewListener(ctx, rt, port, true)
	switch {
	case err != nil:
		return &grantRefusal{http.StatusBadGateway, previewWorkspaceUnreachable}
	case status == http.StatusBadRequest:
		return &grantRefusal{http.StatusBadRequest, previewBadPort}
	case got == nil:
		// An Agent without the endpoint (404 on an older image) or any other failure: fail
		// closed, nothing is opened unchecked.
		return &grantRefusal{http.StatusConflict, previewGrantUnavailable}
	case len(got.Inodes) == 0:
		return &grantRefusal{http.StatusConflict, "preview_unreachable"}
	case got.Probe != "http":
		kind := got.Probe
		if !previewErrorCodes[kind] {
			kind = "preview_failed"
		}
		return &grantRefusal{http.StatusConflict, kind}
	}
	g := store.PreviewTempPort{
		WorkspaceID: ws.ID, Port: port, Slug: ws.PreviewSlug,
		Inodes: strings.Join(got.Inodes, ","), CreatedAt: store.NowTS(),
	}
	audit := store.AuditLog{
		ID: store.NewID(), TenantID: ws.TenantID, ActorKind: "user", ActorID: actorID,
		Action: "workspace.preview_temp_port", Target: ws.ID,
		Detail: "port=" + strconv.Itoa(port), At: store.NowTS(),
	}
	res, err := a.mgr.store.SavePreviewTempPort(ctx, g, epoch, maxPreviewTempPorts, audit)
	if err == nil && res == store.PreviewTempCap {
		// Rows outlive their servers (nothing probes in the background), so a full table may
		// be all stale. Drop the ones whose listener is gone, then try once more.
		a.pruneDeadGrants(ctx, ws, rt)
		audit.ID = store.NewID()
		res, err = a.mgr.store.SavePreviewTempPort(ctx, g, epoch, maxPreviewTempPorts, audit)
	}
	switch {
	case err != nil:
		return &grantRefusal{http.StatusInternalServerError, "preview_failed"}
	case res == store.PreviewTempCap:
		return &grantRefusal{http.StatusConflict, previewGrantCap}
	case res == store.PreviewTempStale:
		// The workspace restarted or stopped while the port was being checked.
		return &grantRefusal{http.StatusConflict, "preview_failed"}
	}
	return nil
}

// pruneDeadGrants deletes the workspace's grants whose listener no longer matches, using the
// Agent's cheap listing (no HTTP probe). Only run when the cap is hit.
func (a previewHostAPI) pruneDeadGrants(ctx context.Context, ws store.Workspace, rt runtime.Runtime) {
	grants, err := a.mgr.store.ListPreviewTempPorts(ctx, ws.ID)
	if err != nil {
		return
	}
	for _, g := range grants {
		cur, status, err := agentPreviewListener(ctx, rt, g.Port, false)
		if err != nil || status != http.StatusOK || cur == nil {
			continue // cannot tell: keep it
		}
		if !grantStillMatches(g.Inodes, cur.Inodes) {
			_ = a.mgr.store.DeletePreviewTempPortIf(ctx, ws.ID, g.Port, g.Inodes)
		}
	}
}

// grantStillMatches mirrors the Agent's rule: something listens, and every listening socket
// on the port is one the grant was made for.
func grantStillMatches(granted string, current []string) bool {
	if len(current) == 0 {
		return false
	}
	ok := map[string]bool{}
	for _, in := range strings.Split(granted, ",") {
		ok[in] = true
	}
	for _, in := range current {
		if !ok[in] {
			return false
		}
	}
	return true
}
