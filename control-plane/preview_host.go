// preview_host.go — host-based preview (docs/log/81 / ADR 0062).
//
// Unlike the path form (/preview/{port}/… in preview.go), this one picks the
// workspace from the Host header alone:
//
//	https://{slug}-{port}.{AF_PREVIEW_DOMAIN}/…  →  Agent /proxy/{port}/…
//
// The slug is redrawn on every workspace start (workspace_lifecycle.go) and dies
// with the stop. The name is one label deep because an ACM wildcard certificate
// only covers one label — `{port}.{slug}.…` would need `*.*.…`, which cannot be
// issued (ADR 0062 decision 2).
package main

import (
	"context"
	"crypto/rand"
	"net/url"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// previewSlugAlphabet is the subset of DNS-label characters a slug may use. `-` is
// excluded because it separates the slug from the port (`{slug}-{port}`). Look-alike
// characters are kept because nobody reads this string aloud or copies it by hand.
const previewSlugAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// previewSlugLen gives 36^20 ≈ 2^103. The URL is not the only barrier facing an
// unauthenticated visitor (auth is required by default), but in public mode
// (docs/log/81 §6.1) it is the key itself.
const previewSlugLen = 20

// newPreviewSlug mints one start's slug. A crypto/rand failure is never swallowed —
// better to issue nothing than a weak slug, and the caller falls back to starting
// without a preview.
func newPreviewSlug() (string, error) {
	b := make([]byte, previewSlugLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// 36 does not divide 256, so a plain modulo skews slightly towards 0-3. The bias
	// is negligible for unguessability, but rejection sampling removes it and with it
	// the need to argue later about why a biased slug is acceptable.
	out := make([]byte, 0, previewSlugLen)
	for len(out) < previewSlugLen {
		if len(b) == 0 {
			b = make([]byte, previewSlugLen)
			if _, err := rand.Read(b); err != nil {
				return "", err
			}
		}
		c := b[0]
		b = b[1:]
		if int(c) >= 252 { // 252 = 36*7; anything above is discarded
			continue
		}
		out = append(out, previewSlugAlphabet[int(c)%len(previewSlugAlphabet)])
	}
	return string(out), nil
}

// previewHost is what one preview hostname carries.
type previewHost struct {
	slug string
	port int
}

// parsePreviewHost reads a Host header as {slug}-{port}.{domain}. An empty domain
// (AF_PREVIEW_DOMAIN unset) never matches — the host form simply does not exist then.
//
// A near miss is not reported as one: callers use this as a pass-through test, so the
// ALB health check (Host is the task IP) and the Console's own host both come back
// false here and continue to the normal mux.
func parsePreviewHost(host, domain string) (previewHost, bool) {
	if domain == "" || host == "" {
		return previewHost{}, false
	}
	// Host may carry a port (127.0.0.1:8080 in development, or a non-standard
	// public port).
	if i := strings.LastIndexByte(host, ':'); i >= 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	suffix := "." + strings.ToLower(strings.TrimPrefix(domain, "."))
	if !strings.HasSuffix(host, suffix) {
		return previewHost{}, false
	}
	label := host[:len(host)-len(suffix)]
	if label == "" || strings.Contains(label, ".") {
		return previewHost{}, false // one label only — the certificate covers no more
	}
	i := strings.LastIndexByte(label, '-')
	if i <= 0 || i == len(label)-1 {
		return previewHost{}, false
	}
	slug, portStr := label[:i], label[i+1:]
	if !validPreviewSlug(slug) {
		return previewHost{}, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 || portStr != strconv.Itoa(port) {
		return previewHost{}, false
	}
	return previewHost{slug: slug, port: port}, true
}

// validPreviewSlug only checks that the shape is one we could have issued. Rejecting
// before the DB lookup keeps junk hostnames aimed at the wildcard from costing a query
// each.
func validPreviewSlug(s string) bool {
	if len(s) != previewSlugLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// previewHostname builds the public hostname from an issued slug and a port.
func previewHostname(slug string, port int, domain string) string {
	if slug == "" || domain == "" {
		return ""
	}
	return slug + "-" + strconv.Itoa(port) + "." + strings.TrimPrefix(domain, ".")
}

// previewURLFor is the same name as an https URL. The preview entrance is always TLS
// (that is what the wildcard certificate is for), so the scheme can be fixed.
func previewURLFor(slug string, port int, domain string) string {
	h := previewHostname(slug, port, domain)
	if h == "" {
		return ""
	}
	return "https://" + h
}

// previewOpenPathFor is the relative path of a pasteable link that stays valid across
// starts (decision 17). This is the only place the shape is assembled: build it in the
// Console too and renaming a parameter breaks the links yet to be made, not the old
// ones already pasted.
func previewOpenPathFor(ownerUserKey string, port int) string {
	if ownerUserKey == "" {
		return ""
	}
	q := url.Values{}
	q.Set("owner", ownerUserKey)
	q.Set("port", strconv.Itoa(port))
	return previewOpenPath + "?" + q.Encode()
}

// defaultPreviewPorts is the allow-list used when the workspace setting is empty
// (docs/log/81 §5, ADR 0062 decision 6). The default is what was actually asked for:
// React on 3000, Spring Boot on 8080.
var defaultPreviewPorts = []int{3000, 8080}

// maxPreviewPorts caps how many ports the setting may hold. It is the line against
// the pull towards "just open everything": an enumeration exists to keep incidentally
// running services (DB admin UIs, debuggers, MCP servers) off the internet, and that
// purpose is gone the moment the list can grow without bound.
const maxPreviewPorts = 8

// previewPortsOf returns the workspace's allowed ports (empty = the defaults).
func previewPortsOf(st wsSettings) []int {
	if len(st.PreviewPorts) == 0 {
		return defaultPreviewPorts
	}
	return st.PreviewPorts
}

// previewPortAllowed reports whether this workspace may expose that port.
func previewPortAllowed(st wsSettings, port int) bool {
	for _, p := range previewPortsOf(st) {
		if p == port {
			return true
		}
	}
	return false
}

// auditPreviewPublic records the public-mode toggle (ADR 0062 decision 12). What the
// audit keeps is who opened it, not who looked: an exposure accident bites long after
// the switch was flipped, so it has to stay traceable.
func auditPreviewPublic(ctx context.Context, m *manager, res *resolved, on bool) {
	state := "off"
	if on {
		state = "on"
	}
	_ = m.store.InsertAudit(ctx, store.AuditLog{
		ID: store.NewID(), TenantID: res.ws.TenantID, ActorKind: "user", ActorID: res.ident.ID,
		Action: "workspace.preview_public", Target: res.ws.ID,
		Detail: "public=" + state, At: store.NowTS(),
	})
}

// auditPreviewShare records the tenant-share toggle (ADR 0062 decision 14), for the same
// reason as auditPreviewPublic: the accident is not the moment it is switched on, it is
// the months afterwards when nobody remembers it is.
//
// Write only when the value changes. Unlike public mode this setting survives restarts,
// so appending a row every time the settings dialog is saved would drown the audit log.
//
// Never record a viewer's accesses: that is one row per static asset. What is kept is
// who opened it, not who looked (docs/log/81 §14.7).
func auditPreviewShare(ctx context.Context, m *manager, res *resolved, on bool) {
	state := "off"
	if on {
		state = "on"
	}
	_ = m.store.InsertAudit(ctx, store.AuditLog{
		ID: store.NewID(), TenantID: res.ws.TenantID, ActorKind: "user", ActorID: res.ident.ID,
		Action: "workspace.preview_tenant_share", Target: res.ws.ID,
		Detail: "tenant=" + state, At: store.NowTS(),
	})
}

// previewViewerAllowed reports whether `membershipID` may open THIS workspace's preview
// right now — the one place the question is answered, for both halves of the handshake.
//
// Re-deciding on every request is the point (ADR 0062 decision 15). The caller only
// hands over the membership baked into a cookie; permission to view is not in that
// cookie. Turning sharing off, or removing someone from the tenant, closes the door on
// the next request.
//
// GetMembershipByID returns only active rows, so a revoked membership fails here
// (git_http.go uses the same function for the same reason).
func previewViewerAllowed(ctx context.Context, m *manager, ws store.Workspace, st wsSettings, membershipID string) bool {
	if membershipID == "" {
		return false
	}
	if membershipID == ws.MembershipID {
		return true // the owner themselves
	}
	if !st.PreviewTenantShare {
		return false
	}
	mv, ok, err := m.store.GetMembershipByID(ctx, membershipID)
	return err == nil && ok && mv.TenantID == ws.TenantID
}

// sanitizePreviewPorts normalizes what the Console sent: 1..65535, duplicates
// collapsed, truncated at the cap. Bad entries are dropped rather than rejected —
// a saved value that always means something is easier to live with than a settings
// dialog that refuses to save over a trivial difference in the list.
func sanitizePreviewPorts(in []int) []int {
	var out []int
	seen := map[int]bool{}
	for _, p := range in {
		if p < 1 || p > 65535 || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
		if len(out) >= maxPreviewPorts {
			break
		}
	}
	return out
}
