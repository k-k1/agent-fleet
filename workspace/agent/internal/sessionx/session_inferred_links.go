package sessionx

import (
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/workitems"
)

// Links a session's branch and PR name without anyone linking them by hand (#3134): the Jira
// keys in the branch name or the PR title, the convention Jira's development panel reads, and the
// GitHub issues the PR closes. Like the PR itself they are linked as working, or watching for a
// child sharing its parent's working copy, and an item removed by hand stays removed.

// maxInferredKeys bounds what one branch name or title can link: a title listing twenty tickets
// is a release note, not twenty tickets this session works on.
const maxInferredKeys = 3

// jiraKeyRe is a Jira key: uppercase only, as Jira writes keys and as the development-panel
// convention expects them in a branch. A lowercase reading would turn branch words into tickets
// (fix-it-2 is IT-2 on a site with an IT project). The boundaries are checked by hand because Go's
// regexp has no lookaround; they differ from the Console's (workitems/refs.ts) on purpose: in a
// branch name a key commonly follows "/" and is followed by "_" or "-".
var jiraKeyRe = regexp.MustCompile(`[A-Z][A-Z0-9_]{1,9}-[1-9][0-9]{0,8}`)

// jiraKeysIn returns the Jira keys in s whose project is one of projects, in order, at most
// maxInferredKeys. A key glued to a letter or digit on either side (XPROJ-1, PROJ-12a) is not one.
func jiraKeysIn(s string, projects map[string]bool) []string {
	var out []string
	for _, m := range jiraKeyRe.FindAllStringIndex(s, -1) {
		if m[0] > 0 && isAlnum(s[m[0]-1]) || m[1] < len(s) && isAlnum(s[m[1]]) {
			continue
		}
		key := s[m[0]:m[1]]
		project, _, _ := strings.Cut(key, "-")
		if !projects[project] || containsFold(out, key) {
			continue
		}
		out = append(out, key)
		if len(out) == maxInferredKeys {
			break
		}
	}
	return out
}

func isAlnum(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// recordInferredLinks links each session to the Jira keys of its branch and PR title, and to the
// issues its GitHub PR closes. The branch is the one the working copy is on, as annotateLinks
// reads it (dirBranch): a detached HEAD reads "(detached)", which names no key, and the start
// branch stands in only for an unread copy. Only metas that lack a link (at this role or above)
// are written, so a steady list writes nothing.
// Like the PR, an inferred item stays after the session leaves the branch: the work happened.
func recordInferredLinks(sessions []session.Session, dirBranch map[string]string, now time.Time) {
	projects := jiraProjectsOf(now)
	dirOf := make(map[string]string, len(sessions))
	for _, s := range sessions {
		dirOf[s.Name] = s.Dir
	}
	for i := range sessions {
		s := &sessions[i]
		type item struct{ provider, key string }
		var items []item
		if len(projects) > 0 {
			branch := dirBranch[s.Dir]
			if branch == "" {
				branch = s.Branch
			}
			keys := jiraKeysIn(branch, projects)
			if s.PR != nil {
				for _, k := range jiraKeysIn(s.PR.Title, projects) {
					if !containsFold(keys, k) {
						keys = append(keys, k)
					}
				}
			}
			for _, k := range keys {
				items = append(items, item{"jira", k})
			}
		}
		if s.PR != nil {
			for n, c := range s.PR.Closes {
				if n == maxInferredKeys {
					break
				}
				if k, ok := session.NormalizeLinkKey("github", c, ""); ok {
					items = append(items, item{"github", k})
				}
			}
		}
		if len(items) == 0 {
			continue
		}
		role := session.LinkWorking
		if s.OriginSession != "" && dirOf[s.OriginSession] == s.Dir {
			role = session.LinkWatching
		}
		var due []session.WorkItemLink
		for _, it := range items {
			key, ok := session.NormalizeLinkKey(it.provider, it.key, "")
			if !ok {
				continue
			}
			if cur, held := heldLink(s.Links, it.provider, key); held &&
				session.LinkRoleRank(cur.Role) >= session.LinkRoleRank(role) {
				continue
			}
			due = append(due, session.WorkItemLink{Provider: it.provider, Key: key, Role: role, At: now.UTC().Format(time.RFC3339)})
		}
		if len(due) == 0 {
			continue
		}
		// An item removed by hand stays removed. Checked on the meta itself, as for the PR: the wire
		// does not carry the removals, so such a row costs one meta read per poll and no write.
		if m, wrote := UpdateSessionMeta(s.Name, func(m *session.Meta) bool {
			changed := false
			for _, l := range due {
				if !session.IsUnlinked(*m, l.Provider, l.Key) && session.AddLink(m, l) {
					changed = true
				}
			}
			return changed
		}); wrote {
			s.Links = session.LinksOf(m)
		}
	}
}

// jiraProjectsOf is replaced in tests: the real one reads the credential store and calls Jira.
var jiraProjectsOf = jiraProjects.known

var jiraProjects = &jiraProjectCache{read: workitems.JiraProjectKeys, async: func(f func()) { go f() }, now: time.Now}

// jiraProjectCache holds the member's Jira project keys for the session list, which every open
// Console polls: known never waits on Jira, and one background read at a time refreshes it.
type jiraProjectCache struct {
	read  func() ([]string, bool, error)
	async func(func())
	now   func() time.Time

	mu       sync.Mutex
	keys     map[string]bool
	at       time.Time // last refresh, successful or not
	ok       bool      // the last refresh succeeded
	inflight bool
}

const (
	// jiraProjectsTTL: a project is created rarely, and until it shows its keys stay unlinked.
	jiraProjectsTTL = time.Hour
	// jiraProjectsRetry is the wait after a failed read; the keys held stay in use meanwhile.
	jiraProjectsRetry = 10 * time.Minute
)

// known returns the project keys held now (nil before the first answer, or without a Jira
// connection) and starts a refresh when they are due.
func (c *jiraProjectCache) known(now time.Time) map[string]bool {
	c.mu.Lock()
	ttl := jiraProjectsTTL
	if !c.ok {
		ttl = jiraProjectsRetry
	}
	start := !c.inflight && (c.at.IsZero() || now.Sub(c.at) >= ttl)
	if start {
		c.inflight = true
	}
	c.mu.Unlock()
	if start {
		c.async(c.refresh)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.keys
}

func (c *jiraProjectCache) refresh() {
	keys, connected, err := c.read()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inflight = false
	c.at = c.now()
	// Not connected counts as not ok: a connection made later is picked up within the retry, not
	// the hour.
	c.ok = err == nil && connected
	switch {
	case err != nil:
		// Keep what was read before: a Jira outage must not stop recognising known projects.
	case !connected:
		c.keys = nil
	default:
		set := make(map[string]bool, len(keys))
		for _, k := range keys {
			set[k] = true
		}
		c.keys = set
	}
}
