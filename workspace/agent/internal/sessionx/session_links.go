package sessionx

import (
	"context"
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
	sessionPRs   = newSessionPRs()
	sessionBBPRs = newSessionBitbucketPRs()
	sessionPorts = &listenports.Cache{Root: "/proc", TTL: portsTTL}

	// Replaced in tests: the PR lookups start a goroutine that reads the credential store and
	// calls the provider, and the port scan reads the real /proc.
	lookupPRs          = sessionPRs.Lookup
	lookupBitbucketPRs = sessionBBPRs.Lookup
	lookupPorts        = sessionPorts.Get
	originOf           = cachedOriginRepo
)

func newSessionPRs() *branchpr.Cache {
	c := branchpr.New(githubToken)
	c.Renew = func(rejected string) (string, error) {
		return gitx.RenewRejectedGitHubToken(context.Background(), rejected)
	}
	return c
}

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

func newSessionBitbucketPRs() *branchpr.Cache {
	c := branchpr.NewBitbucket(bitbucketAuth)
	c.Renew = renewBitbucketAuth
	return c
}

// bitbucketAuth is the Authorization value of the bitbucket.org connection, "" when there is
// none. Like githubToken, a store that cannot be read is an error, not a disconnection.
func bitbucketAuth() (string, error) {
	s, err := secrets.Load()
	if err != nil {
		return "", err
	}
	auth, err := gitx.BitbucketAuthHeader(s)
	if err != nil {
		return "", nil // not connected
	}
	return auth, nil
}

// renewBitbucketAuth refreshes an OAuth connection whose token Bitbucket rejected. A pasted API
// token cannot be renewed: the rejected value comes back and the cache asks no second time.
func renewBitbucketAuth(rejected string) (string, error) {
	s, err := secrets.Load()
	if err != nil {
		return "", err
	}
	renewed := rejected
	_ = gitx.RefreshBitbucketAndRetry(s, gitx.ErrBitbucketUnauthorized, func(auth string) error {
		renewed = auth
		return nil
	})
	return renewed, nil
}

// originRepo is a working copy's repository on a host whose PRs a row can show.
type originRepo struct {
	provider string // "github" or "bitbucket"
	repo     string // "owner/name"
}

type originEntry struct {
	repo originRepo
	at   time.Time
}

var (
	originMu    sync.Mutex
	originCache = map[string]originEntry{}
)

// cachedOriginRepo returns dir's github.com or bitbucket.org repository, zero for any other
// origin or none.
func cachedOriginRepo(dir string, now time.Time) originRepo {
	originMu.Lock()
	e, ok := originCache[dir]
	originMu.Unlock()
	if ok && now.Sub(e.at) < originTTL {
		return e.repo
	}
	var repo originRepo
	if origin, ok := gitx.GitOriginURL(dir); ok {
		if r := gitx.GitHubRepoOf(origin); r != "" {
			repo = originRepo{"github", r}
		} else if r := gitx.BitbucketRepoOf(origin); r != "" {
			repo = originRepo{"bitbucket", r}
		}
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
	provider := make([]string, len(sessions))
	ask := map[string][]branchpr.Key{}
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
		o := originOf(s.Dir, now)
		if o.repo == "" {
			continue
		}
		keys[i], provider[i] = branchpr.Key{Repo: o.repo, Branch: branch}, o.provider
		ask[o.provider] = append(ask[o.provider], keys[i])
	}
	prs := map[string]map[branchpr.Key]*branchpr.PR{}
	if k := ask["github"]; len(k) > 0 {
		prs["github"] = lookupPRs(k, now)
	}
	if k := ask["bitbucket"]; len(k) > 0 {
		prs["bitbucket"] = lookupBitbucketPRs(k, now)
	}
	var ports map[string][]int
	for i := range sessions {
		s := &sessions[i]
		if keys[i].Repo != "" {
			s.PR = prs[provider[i]][keys[i]]
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
