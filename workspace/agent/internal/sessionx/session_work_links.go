package sessionx

// Session ↔ work item links (#3134): which tickets and PRs a session started from, works on,
// created, reviews or watches. The launch item stays Meta.WorkItem; everything else is
// Meta.Links, written here and nowhere else.
//
// Writers: POST /sessions/{name}/links (the link_work_item tool, and branch_name naming a
// branch after an item), a create from a session (the child inherits its parent's items as
// watching), and the PR lookup of the session list (the branch's PR as working).

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchpr"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

type linkReq struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
	Role     string `json:"role"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Remove   bool   `json:"remove"`
}

// HandleSessionLink (POST /sessions/{name}/links) adds or, with remove, drops one link. A bare
// GitHub or Bitbucket number is qualified with the session's own repository, so an agent can
// say "45" about the repository it works in.
func HandleSessionLink(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var req linkReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	key, ok := session.NormalizeLinkKey(provider, req.Key, linkRepoOf(m.Dir, provider))
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_key",
			"provider must be github, jira or bitbucket, and key owner/repo#N, #N inside that repository, or PROJ-12")
		return
	}
	role := strings.ToLower(strings.TrimSpace(req.Role))
	if role == "" {
		role = session.LinkWorking
	}
	if !req.Remove && !session.ValidLinkRole(role) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_role", "role must be working, reviewing, watching or created")
		return
	}
	changed, found := false, false
	m, _ = UpdateSessionMeta(name, func(m *session.Meta) bool {
		found = true
		if req.Remove {
			changed = session.RemoveLink(m, provider, key)
		} else {
			// Linking an item by hand undoes an earlier removal of it.
			forgot := session.ForgetUnlinked(m, key)
			changed = session.AddLink(m, session.WorkItemLink{
				Provider: provider, Key: key, Role: role,
				Title: clipRunes(strings.TrimSpace(req.Title), 300), URL: linkURL(req.URL),
				At: time.Now().UTC().Format(time.RFC3339),
			}) || forgot
		}
		return changed
	})
	if !found {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, linkResp{Name: name, Changed: changed, Links: linksOrEmpty(session.LinksOf(m))})
}

// linkResp is POST /sessions/{name}/links's answer: the session's links after the call.
type linkResp struct {
	Name    string                 `json:"name"`
	Changed bool                   `json:"changed"`
	Links   []session.WorkItemLink `json:"links"`
}

func linksOrEmpty(l []session.WorkItemLink) []session.WorkItemLink {
	if l == nil {
		return []session.WorkItemLink{}
	}
	return l
}

// linkURL keeps an https URL with no userinfo, clipped; anything else is dropped. The Console
// decides per provider what it will actually open.
func linkURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return ""
	}
	return clipRunes(u.String(), 500)
}

// linkRepoOf is replaced in tests: the origin parse reaches gitx's internal-host dependency,
// which only package main wires.
var linkRepoOf = repoForProvider

// repoForProvider is the "owner/name" of dir's origin on provider's host, "" when the origin
// is elsewhere or unreadable.
func repoForProvider(dir, provider string) string {
	if dir == "" {
		return ""
	}
	origin, ok := gitx.GitOriginURL(dir)
	if !ok {
		return ""
	}
	switch provider {
	case "github":
		return gitx.GitHubRepoOf(origin)
	case "bitbucket":
		return gitx.BitbucketRepoOf(origin)
	}
	return ""
}

// applyCreateLinks sets a new session's links: a moved session keeps the source's (carried in
// the request by moveCreateReq), any other inherits its parent's (inheritLinks).
func applyCreateLinks(meta *session.Meta, req *CreateReq) {
	if len(req.links) > 0 || len(req.unlinked) > 0 {
		meta.Links, meta.Unlinked = req.links, req.unlinked
		return
	}
	inheritLinks(meta)
}

// inheritLinks gives a session-spawned child its parent's items as watching, unless the child
// was started for an item of its own. Best effort: a parent whose meta cannot be read gives
// nothing, and the child is created regardless.
func inheritLinks(child *session.Meta) {
	if child.OriginSession == "" || child.WorkItem != nil {
		return
	}
	parent, ok := session.ReadMeta(child.OriginSession)
	if !ok {
		return
	}
	// Links are stamped in UTC like every other writer, but CreatedAt is in local time. Re-format
	// the same instant rather than stamping now; an unparsable CreatedAt falls back to now.
	at := time.Now().UTC()
	if t, err := time.Parse(time.RFC3339, child.CreatedAt); err == nil {
		at = t.UTC()
	}
	for _, l := range session.InheritedLinks(parent, at.Format(time.RFC3339)) {
		session.AddLink(child, l)
	}
}

// recordBranchPRs links each session to the PR of the branch it is on, as working: whoever is
// on a PR's head branch is the one its CI and review feedback is for, whether they opened it or
// took it over. Recording it keeps the PR on the row after the session moves to another branch.
// A child that shares its parent's working copy is on the parent's branch without working on
// it (a reviewer, typically), so it only watches. The PR's state is recorded on the link too, so
// a row can tell a merged PR from one still in flight after the session has left its branch.
// Only metas that lack the link or hold an older state are written, so a steady list writes
// nothing.
func recordBranchPRs(sessions []session.Session, now time.Time) {
	dirOf := make(map[string]string, len(sessions))
	for _, s := range sessions {
		dirOf[s.Name] = s.Dir
	}
	for i := range sessions {
		s := &sessions[i]
		if s.PR == nil || s.PR.Number <= 0 {
			continue
		}
		key, ok := prLinkKey(s.PR)
		if !ok {
			continue
		}
		role := session.LinkWorking
		if s.OriginSession != "" && dirOf[s.OriginSession] == s.Dir {
			role = session.LinkWatching
		}
		state := prLinkState(s.PR)
		// Already linked at this role or above, with this state: nothing to write. A weaker link
		// (watching from when the copy was shared, reviewing before the session took the branch
		// over) goes through AddLink, which raises it; an unknown role from a hand-edited meta
		// ranks lowest.
		// The launch item is its own record and carries no state, so it is never rewritten here.
		if cur, held := heldLink(s.Links, "github", key); held && (cur.Role == session.LinkStarted ||
			session.LinkRoleRank(cur.Role) >= session.LinkRoleRank(role) && cur.State == state) {
			continue
		}
		l := session.WorkItemLink{Provider: "github", Key: key, Role: role, URL: linkURL(s.PR.URL), At: now.UTC().Format(time.RFC3339), State: state}
		// A PR unlinked by hand stays unlinked. Checked on the meta itself: the wire does not carry
		// the removals, so such a row costs one meta read per poll and no write.
		if m, wrote := UpdateSessionMeta(s.Name, func(m *session.Meta) bool {
			if session.IsUnlinked(*m, key) {
				return false
			}
			added := session.AddLink(m, l)
			return session.SetLinkState(m, "github", key, state) || added
		}); wrote {
			s.Links = session.LinksOf(m)
		}
	}
}

// prLinkKey is the inbox key of a GitHub PR, read from its URL: the PR row carries no repo of
// its own, and the URL is the provider's.
func prLinkKey(pr *branchpr.PR) (string, bool) {
	u, err := url.Parse(pr.URL)
	if err != nil || u.Host != "github.com" {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 || parts[2] != "pull" {
		return "", false
	}
	return session.NormalizeLinkKey("github", parts[0]+"/"+parts[1]+"#"+parts[3], "")
}

// heldLink is links' link to provider/key, and whether it holds one at all.
func heldLink(links []session.WorkItemLink, provider, key string) (session.WorkItemLink, bool) {
	for _, l := range links {
		if session.SameLinkItem(l, provider, key) {
			return l, true
		}
	}
	return session.WorkItemLink{}, false
}

// prLinkState is the state a PR link records: GitHub's state, with an open draft told apart as
// the row's PR chip tells it.
func prLinkState(pr *branchpr.PR) string {
	if pr.State == "open" && pr.Draft {
		return "draft"
	}
	return pr.State
}
