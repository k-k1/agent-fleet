package branchrule

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// ErrNoConnection is what a Fetcher returns when there is no Bitbucket connection. It is
// not a warning: most repositories on bitbucket.org are simply not declared there.
var ErrNoConnection = errors.New("Bitbucket is not connected")

// Fetcher reads `GET /2.0/repositories/{ws}/{repo}/branching-model`.
type Fetcher func(ctx context.Context, workspace, repo string) ([]byte, error)

const (
	stateOK      = "ok"
	statePending = "pending"
	stateError   = "error"
)

// BitbucketCache keeps each repository's branching model for TTL. A resolve with no copy
// waits up to Wait for the first fetch, then answers without the model and says so
// (decision 3): silently resolving without it would branch off `head` after every Agent
// restart with nothing saying why.
type BitbucketCache struct {
	Fetch Fetcher
	TTL   time.Duration
	// ErrTTL is shorter so a transient failure does not hide the model for ten minutes.
	ErrTTL time.Duration
	Wait   time.Duration

	mu      sync.Mutex
	entries map[string]*bbEntry
}

// bbEntry keeps the last good answer apart from the last failure: a failed refresh must
// not take away a model that was read fine, or a transient error would silently drop its
// base and kind set for ErrTTL.
type bbEntry struct {
	body     []byte
	at       time.Time
	err      error
	errAt    time.Time
	inflight chan struct{}
}

// NewBitbucketCache has the ADR's numbers: ten minutes, three seconds.
func NewBitbucketCache(f Fetcher) *BitbucketCache {
	return &BitbucketCache{Fetch: f, TTL: 10 * time.Minute, ErrTTL: time.Minute, Wait: 3 * time.Second}
}

func (c *BitbucketCache) fresh(e *bbEntry) bool {
	if e.err != nil {
		return time.Since(e.errAt) < c.ErrTTL
	}
	return !e.at.IsZero() && time.Since(e.at) < c.TTL
}

// Get returns the cached model for key, fetching it when missing, stale or refresh is
// set. One fetch runs per key at a time, and it outlives the request that started it so
// the next resolve finds the copy.
func (c *BitbucketCache) Get(ctx context.Context, key string, refresh bool) ([]byte, time.Time, string, error) {
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[string]*bbEntry{}
	}
	e := c.entries[key]
	if e == nil {
		e = &bbEntry{}
		c.entries[key] = e
	}
	if !refresh && c.fresh(e) {
		defer c.mu.Unlock()
		return result(e)
	}
	if e.inflight == nil {
		ch := make(chan struct{})
		e.inflight = ch
		ws, repo, _ := strings.Cut(key, "/")
		go func() {
			fctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			body, err := c.Fetch(fctx, ws, repo)
			cancel()
			c.mu.Lock()
			if err != nil {
				e.err, e.errAt = err, time.Now()
			} else {
				e.body, e.at, e.err = body, time.Now(), nil
			}
			e.inflight = nil
			c.mu.Unlock()
			close(ch)
		}()
	}
	wait := e.inflight
	c.mu.Unlock()

	t := time.NewTimer(c.Wait)
	defer t.Stop()
	select {
	case <-wait:
	case <-t.C:
	case <-ctx.Done():
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// A timed-out refresh still has the previous copy, which is better than nothing.
	return result(e)
}

// result answers with the last good copy when there is one; err is then the failure of
// the latest refresh, if any, for the caller to warn about.
func result(e *bbEntry) ([]byte, time.Time, string, error) {
	switch {
	case !e.at.IsZero():
		return e.body, e.at, stateOK, e.err
	case e.err != nil:
		return nil, e.errAt, stateError, e.err
	}
	return nil, time.Time{}, statePending, nil
}

type branchingModel struct {
	Development *struct {
		Name          string `json:"name"`
		UseMainbranch bool   `json:"use_mainbranch"`
		Branch        *struct {
			Name string `json:"name"`
		} `json:"branch"`
	} `json:"development"`
	BranchTypes []struct {
		Kind   string `json:"kind"`
		Prefix string `json:"prefix"`
	} `json:"branch_types"`
}

// stockTypes is Bitbucket's unconfigured answer. The field study's repository, which
// branches off develop, returned exactly this, so it declares nothing.
var stockTypes = map[string]string{"bugfix": "bugfix/", "feature": "feature/", "hotfix": "hotfix/", "release": "release/"}

// bitbucketRule reads the fields that differ from the unconfigured default: development
// only when use_mainbranch is false, branch_types only when they are not the stock set.
// A subset of the stock types counts too: branch_types lists only the enabled types, so a
// missing one was turned off by someone, which is a declaration.
func bitbucketRule(body []byte) (Rule, bool, error) {
	var m branchingModel
	if err := json.Unmarshal(body, &m); err != nil {
		return Rule{}, false, errors.New("unreadable answer")
	}
	r := Rule{Source: "bitbucket", Keys: map[string]string{}, Types: map[string]KindRule{}}
	if d := m.Development; d != nil && !d.UseMainbranch {
		name := d.Name
		if name == "" && d.Branch != nil {
			name = d.Branch.Name
		}
		if ValidBranch(name) {
			r.Base = str(name)
			r.Keys["base"] = "development"
		}
	}
	stock := len(m.BranchTypes) == len(stockTypes)
	for _, t := range m.BranchTypes {
		if stockTypes[t.Kind] != t.Prefix {
			stock = false
		}
	}
	if !stock {
		declared := map[string]bool{}
		for _, t := range m.BranchTypes {
			if !ValidKind(t.Kind) || !ValidPrefix(t.Prefix) {
				continue
			}
			r.Types[t.Kind] = KindRule{Prefix: str(t.Prefix)}
			r.Keys["types."+t.Kind+".prefix"] = "branch_types." + t.Kind
			declared[t.Kind] = true
		}
		r.Declares = sortedKinds(declared)
	}
	return r, !r.empty(), nil
}

// bitbucketRepo splits a bitbucket.org id into workspace and repository.
func bitbucketRepo(id string) (string, string, bool) {
	s := strings.Split(id, "/")
	if len(s) != 3 || s[0] != "bitbucket.org" {
		return "", "", false
	}
	return s[1], s[2], true
}
