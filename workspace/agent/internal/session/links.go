package session

import (
	"net/url"
	"regexp"
	"strings"
)

// Links between a session and the work items it touches (#3134). Meta.WorkItem stays the one
// item a launch was started for, because the branch-name resolver renders from it; every other
// relation lives in Meta.Links. LinksOf merges the two for the wire.
//
// A key has the work-item inbox's shape, so a link and an inbox row name the same item without
// a translation table: "owner/repo#45" on GitHub and Bitbucket, "PROJ-12" on Jira.

// The roles a link can have, strongest first. A session holds one link per item, and a weaker
// role never replaces a stronger one: a child that inherited an item as watching and then names
// its branch after it is working on it, but a session that opened a PR has created it however
// it later refers to it.
const (
	LinkStarted   = "started"
	LinkCreated   = "created"
	LinkWorking   = "working"
	LinkReviewing = "reviewing"
	LinkWatching  = "watching"
)

var linkRank = map[string]int{LinkStarted: 5, LinkCreated: 4, LinkWorking: 3, LinkReviewing: 2, LinkWatching: 1}

// ValidLinkRole reports whether role is one a caller may set. started is not: only a launch
// records it, through Meta.WorkItem.
func ValidLinkRole(role string) bool {
	return role != LinkStarted && linkRank[role] > 0
}

// maxLinkKey matches the launch item's key clip (cleanWorkItemRef): a key is an identifier, and
// anything longer is a paste gone wrong that would ride every session listing.
const maxLinkKey = 200

// LinkRoleRank orders roles as AddLink does: higher replaces lower. 0 for a role it does not know.
func LinkRoleRank(role string) int { return linkRank[role] }

// MaxLinks bounds Meta.Links. The meta is read by every session listing, and an agent that
// links in a loop must not be able to grow it without end.
const MaxLinks = 50

// WorkItemLink is one relation. Only non-confidential coordinates: the same fields the
// work-item cache may hold (ADR 0061 decision 2), never a body.
type WorkItemLink struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
	Role     string `json:"role"`
	Title    string `json:"title,omitempty"`
	URL      string `json:"url,omitempty"`
	At       string `json:"at,omitempty"`
	// State is the last state seen for a pull request link: "open", "draft", "merged" or
	// "closed" ("" = never seen, or not a pull request). The Agent reads a PR's state only while
	// a session is on its branch, so it is kept here for after the session has moved on.
	State string `json:"state,omitempty"`
}

// validLinkState reports whether state is one SetLinkState records.
func validLinkState(state string) bool {
	switch state {
	case "open", "draft", "merged", "closed":
		return true
	}
	return false
}

// SetLinkState records state on m's link to provider/key, and reports whether it changed. The
// launch item has no link of its own, so its state goes to Meta.WorkItemState. An unknown state
// is not recorded.
func SetLinkState(m *Meta, provider, key, state string) bool {
	if !validLinkState(state) {
		return false
	}
	if m.WorkItem != nil && sameItem(WorkItemLink{Provider: m.WorkItem.Provider, Key: m.WorkItem.Key}, provider, key) {
		if m.WorkItemState == state {
			return false
		}
		m.WorkItemState = state
		return true
	}
	for i := range m.Links {
		if sameItem(m.Links[i], provider, key) && m.Links[i].State != state {
			m.Links[i].State = state
			return true
		}
	}
	return false
}

var (
	jiraKeyRe   = regexp.MustCompile(`^[A-Z][A-Z0-9_]*-[1-9][0-9]*$`)
	repoKeyRe   = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+#[1-9][0-9]*$`)
	issueNumRe  = regexp.MustCompile(`^#?[1-9][0-9]*$`)
	linkProvRes = map[string]*regexp.Regexp{"github": repoKeyRe, "bitbucket": repoKeyRe, "jira": jiraKeyRe}
)

// NormalizeLinkKey returns key in the inbox's shape for provider, qualifying a bare GitHub or
// Bitbucket number ("45", "#45") with repo ("owner/name"). ok is false for a provider this
// does not know or a key that cannot be made into that shape — an unqualified number with no
// repo would collide across every repository.
func NormalizeLinkKey(provider, key, repo string) (string, bool) {
	provider, key = strings.ToLower(strings.TrimSpace(provider)), strings.TrimSpace(key)
	if len(key) > maxLinkKey {
		return "", false
	}
	re, known := linkProvRes[provider]
	if !known {
		return "", false
	}
	if provider == "jira" {
		key = strings.ToUpper(key)
	} else if issueNumRe.MatchString(key) {
		if repo == "" {
			return "", false
		}
		key = repo + "#" + strings.TrimPrefix(key, "#")
	}
	return key, re.MatchString(key)
}

// LinksOf is every link of m, the launch item first as started.
func LinksOf(m Meta) []WorkItemLink {
	if m.WorkItem == nil && len(m.Links) == 0 {
		return nil
	}
	out := make([]WorkItemLink, 0, len(m.Links)+1)
	if wi := m.WorkItem; wi != nil {
		out = append(out, WorkItemLink{Provider: strings.ToLower(wi.Provider), Key: wi.Key, Role: LinkStarted, Title: wi.Title, State: m.WorkItemState})
	}
	for _, l := range m.Links {
		if m.WorkItem != nil && sameItem(l, m.WorkItem.Provider, m.WorkItem.Key) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// sameItem compares case-insensitively on both parts: GitHub and Bitbucket owner/repo names are,
// and a Jira key is upper-cased by NormalizeLinkKey anyway, so "O/R#45" from a PR URL and
// "o/r#45" typed by an agent are one item.
func sameItem(l WorkItemLink, provider, key string) bool {
	return strings.EqualFold(l.Provider, provider) && strings.EqualFold(l.Key, key)
}

// SameLinkItem is sameItem for callers outside the package.
func SameLinkItem(l WorkItemLink, provider, key string) bool { return sameItem(l, provider, key) }

// AddLink records l on m. It reports whether m changed: an item already held with an equal or
// stronger role only fills a missing title or URL, and the launch item is never duplicated.
func AddLink(m *Meta, l WorkItemLink) bool {
	if m.WorkItem != nil && sameItem(l, m.WorkItem.Provider, m.WorkItem.Key) {
		return false
	}
	for i := range m.Links {
		cur := &m.Links[i]
		if !sameItem(*cur, l.Provider, l.Key) {
			continue
		}
		changed := false
		if linkRank[l.Role] > linkRank[cur.Role] {
			cur.Role, cur.At, changed = l.Role, l.At, true
		}
		if cur.Title == "" && l.Title != "" {
			cur.Title, changed = l.Title, true
		}
		if cur.URL == "" && l.URL != "" {
			cur.URL, changed = l.URL, true
		}
		return changed
	}
	if len(m.Links) >= MaxLinks {
		return false
	}
	m.Links = append(m.Links, l)
	return true
}

// RemoveLink drops the link to provider/key. The launch item cannot be removed here: the
// branch-name resolver still renders from it. A removed item is also remembered in
// Meta.Unlinked, so the session list does not link the branch's PR again on its next poll.
func RemoveLink(m *Meta, provider, key string) bool {
	if m.WorkItem != nil && strings.EqualFold(m.WorkItem.Provider, provider) && strings.EqualFold(m.WorkItem.Key, key) {
		return false
	}
	changed := false
	if !IsUnlinked(*m, provider, key) {
		if len(m.Unlinked) >= MaxLinks {
			m.Unlinked = m.Unlinked[1:]
		}
		m.Unlinked = append(m.Unlinked, unlinkedEntry(provider, key))
		changed = true
	}
	for i, l := range m.Links {
		if sameItem(l, provider, key) {
			m.Links = append(m.Links[:i:i], m.Links[i+1:]...)
			return true
		}
	}
	return changed
}

// unlinkedEntry is provider/key as Meta.Unlinked holds it: a GitHub key bare, as metas written
// before other providers were recorded hold it, any other prefixed with its provider. The same
// owner/repo#N can exist on github.com and bitbucket.org, and removing one must not hide the
// other. No key of any provider contains a colon, so the two forms cannot collide.
func unlinkedEntry(provider, key string) string {
	if p := strings.ToLower(provider); p != "github" {
		return p + ":" + key
	}
	return key
}

// IsUnlinked reports whether provider/key was removed by hand (RemoveLink).
func IsUnlinked(m Meta, provider, key string) bool {
	e := unlinkedEntry(provider, key)
	for _, k := range m.Unlinked {
		if strings.EqualFold(k, e) {
			return true
		}
	}
	return false
}

// ForgetUnlinked drops provider/key from Meta.Unlinked: linking an item again by hand undoes the
// removal.
func ForgetUnlinked(m *Meta, provider, key string) bool {
	e := unlinkedEntry(provider, key)
	for i, k := range m.Unlinked {
		if strings.EqualFold(k, e) {
			m.Unlinked = append(m.Unlinked[:i:i], m.Unlinked[i+1:]...)
			return true
		}
	}
	return false
}

// InheritedLinks is what a child started by parent watches: the items the parent was started
// for or is working on. What the parent created or only watches is not the child's business.
func InheritedLinks(parent Meta, at string) []WorkItemLink {
	var out []WorkItemLink
	for _, l := range LinksOf(parent) {
		if l.Role == LinkStarted || l.Role == LinkWorking {
			out = append(out, WorkItemLink{Provider: l.Provider, Key: l.Key, Role: LinkWatching, Title: l.Title, URL: l.URL, At: at, State: l.State})
		}
	}
	return out
}

// PRsAllMerged reports a session whose own pull requests are all finished, at least one of them
// merged (#3131): the PRs it started from, worked on or created, as their links last recorded
// them. A PR it only reviews or watches is someone else's to finish. A PR link with no recorded
// state (one linked before states were) is not known to be finished, so it blocks; an item with
// no state that is not a PR (an issue, a ticket) neither counts nor blocks. The Console's
// prsAllMerged (sessionLinks.ts) reads the same rule off the row.
func PRsAllMerged(m Meta) bool {
	merged := false
	for _, l := range LinksOf(m) {
		if l.Role != LinkStarted && l.Role != LinkWorking && l.Role != LinkCreated {
			continue
		}
		switch l.State {
		case "merged":
			merged = true
		case "open", "draft":
			return false
		case "":
			if isPRURL(l.URL) {
				return false
			}
		}
	}
	return merged
}

// isPRURL reports a github.com or bitbucket.org pull request page, the URLs PR links carry.
func isPRURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 {
		return false
	}
	return u.Host == "github.com" && parts[2] == "pull" || u.Host == "bitbucket.org" && parts[2] == "pull-requests"
}
