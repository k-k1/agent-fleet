package sessionx

import (
	"errors"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchpr"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/listenports"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The two things a session row links out to (#1062): the pull request of the branch it works
// on, and the ports its processes serve. Both ride the session list, which every open Console
// polls every few seconds, so neither may cost anything per poll: the PR comes out of a cache
// refreshed in the background (branchpr), the ports out of one /proc walk held for
// portsTTL (listenports).

// portsTTL bounds how late a server that just started shows on its row.
const portsTTL = 10 * time.Second

// originTTL is how long a working copy's origin is trusted. It almost never changes, and
// reading it is a git call per working copy.
const originTTL = 5 * time.Minute

var (
	sessionPRs   = branchpr.New(githubToken)
	sessionPorts = &listenports.Cache{Root: "/proc", TTL: portsTTL}

	// Replaced in tests: the PR lookup starts a goroutine that reads the credential store and
	// calls GitHub, and the port scan reads the real /proc.
	lookupPRs   = sessionPRs.Lookup
	lookupPorts = sessionPorts.Get
	originOf    = cachedGitHubRepo
)

// githubToken is the Connections token for github.com, "" when GitHub is not connected. A store
// that cannot be read is an error, not a disconnection: the cache then keeps what it showed.
func githubToken() (string, error) {
	s, err := secrets.Load()
	if err != nil {
		return "", err
	}
	tok, err := gitx.GitHubToken(s)
	if errors.Is(err, gitx.ErrGitHubReconnect) {
		return "", nil // a connection that cannot be renewed looks up nothing
	}
	return tok, err
}

type originEntry struct {
	repo string
	at   time.Time
}

var (
	originMu    sync.Mutex
	originCache = map[string]originEntry{}
)

// cachedGitHubRepo returns dir's github.com "owner/name", "" for any other origin or none.
func cachedGitHubRepo(dir string, now time.Time) string {
	originMu.Lock()
	e, ok := originCache[dir]
	originMu.Unlock()
	if ok && now.Sub(e.at) < originTTL {
		return e.repo
	}
	repo := ""
	if origin, ok := gitx.GitOriginURL(dir); ok {
		repo = gitx.GitHubRepoOf(origin)
	}
	originMu.Lock()
	for d, old := range originCache {
		if now.Sub(old.at) >= originTTL {
			delete(originCache, d)
		}
	}
	originCache[dir] = originEntry{repo: repo, at: now}
	originMu.Unlock()
	return repo
}

// annotateLinks fills PR and Ports. dirBranch is each working copy's current branch, as
// annotateSessions already read it.
//
// The branch asked about is the one the working copy is on now: after a drift that is where
// the session's commits go. A detached HEAD has no PR to find — not even the start branch's,
// which the copy is no longer on. Only an unread working copy falls back to the start branch.
func annotateLinks(sessions []session.Session, dirBranch map[string]string, now time.Time) {
	keys := make([]branchpr.Key, len(sessions))
	var ask []branchpr.Key
	for i := range sessions {
		s := &sessions[i]
		if s.Dir == "" {
			continue
		}
		branch := dirBranch[s.Dir]
		if branch == "(detached)" {
			continue
		}
		if branch == "" {
			branch = s.Branch
		}
		if branch == "" {
			continue
		}
		repo := originOf(s.Dir, now)
		if repo == "" {
			continue
		}
		keys[i] = branchpr.Key{Repo: repo, Branch: branch}
		ask = append(ask, keys[i])
	}
	var prs map[branchpr.Key]*branchpr.PR
	if len(ask) > 0 {
		prs = lookupPRs(ask, now)
	}
	var ports map[string][]int
	for i := range sessions {
		s := &sessions[i]
		if keys[i].Repo != "" {
			s.PR = prs[keys[i]]
		}
		if !s.Alive {
			continue
		}
		if ports == nil {
			ports = lookupPorts(now)
		}
		s.Ports = ports[s.Name]
	}
}
