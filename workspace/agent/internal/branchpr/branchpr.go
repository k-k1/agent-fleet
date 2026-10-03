// Package branchpr answers "which pull request is this branch's, and how is it doing" for the
// session rows (#1062): the newest GitHub pull request whose head is the branch a session works
// on, with its state and the CI rollup of its head commit.
//
// The session list is polled every few seconds by every open Console, so the provider must
// never be asked from that path. Lookup only reads a cache; anything missing or stale is
// refreshed in the background, by ONE goroutine at a time, with every branch that needs it in a
// single GraphQL call. A row whose answer has not arrived yet simply shows no PR until a later
// poll.
//
// GitHub only (github.com, the Connections token — the host the gh wrapper and the work-item
// inbox serve). Other providers, and a Workspace with no GitHub connection, show nothing.
package branchpr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PR is what a row shows. State is "open", "merged" or "closed"; Checks is "success",
// "failure", "pending", or "" when nothing ran against the head commit — which must not read
// as green.
type PR struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	Draft  bool   `json:"draft,omitempty"`
	URL    string `json:"url"`
	Checks string `json:"checks,omitempty"`
}

// Key names one branch of one GitHub repository. Repo is "owner/name".
type Key struct {
	Repo   string
	Branch string
}

const (
	// openTTL is how stale an open PR may get. It bounds how long a row says "open" after a
	// merge, and how late a CI result appears.
	openTTL = 2 * time.Minute
	// settledTTL covers merged / closed PRs and branches with none. A closed PR can be reopened
	// and a branch can gain one, so these are re-read too, just less often.
	settledTTL = 10 * time.Minute
	// failBackoff is the wait after a call that failed for any reason but the rate limit.
	failBackoff = 5 * time.Minute
	// rateBackoff is the wait after a rate-limited call that did not say when the limit resets.
	rateBackoff = 15 * time.Minute
	// forgetAfter drops a branch no list has asked about for this long.
	forgetAfter = time.Hour
	// batchSize caps the branches one call asks about. Each is one aliased repository lookup;
	// GitHub's GraphQL cost stays at a point or two for a batch this size.
	batchSize = 40
)

type entry struct {
	pr      *PR
	fetched time.Time // zero: never answered
	asked   time.Time // last time a list wanted this key
}

func (e *entry) fresh(now time.Time) bool {
	if e.fetched.IsZero() {
		return false
	}
	ttl := settledTTL
	if e.pr != nil && e.pr.State == "open" {
		ttl = openTTL
	}
	return now.Sub(e.fetched) < ttl
}

// Cache is the per-Agent store. The zero value is not usable; use New.
type Cache struct {
	// Token returns the GitHub token, "" when GitHub is not connected. Read on every refresh,
	// so connecting or disconnecting takes effect without a restart.
	Token func() string
	// Endpoint is the GraphQL URL; tests point it at a local server.
	Endpoint string
	Client   *http.Client
	// Async runs the refresh. Tests set it to run inline.
	Async func(func())

	mu        sync.Mutex
	entries   map[Key]*entry
	inflight  bool
	holdUntil time.Time
}

// New returns a cache that asks api.github.com.
func New(token func() string) *Cache {
	return &Cache{
		Token:    token,
		Endpoint: "https://api.github.com/graphql",
		Client:   &http.Client{Timeout: 20 * time.Second},
		Async:    func(f func()) { go f() },
		entries:  map[Key]*entry{},
	}
}

// Lookup returns what is known for keys right now and starts one background refresh for the
// ones that are missing or stale. It never waits on the network.
func (c *Cache) Lookup(keys []Key, now time.Time) map[Key]*PR {
	out := map[Key]*PR{}
	var due []Key
	c.mu.Lock()
	for _, k := range keys {
		e := c.entries[k]
		if e == nil {
			e = &entry{}
			c.entries[k] = e
		}
		e.asked = now
		if e.pr != nil {
			out[k] = e.pr
		}
		if !e.fresh(now) {
			due = append(due, k)
		}
	}
	for k, e := range c.entries {
		if now.Sub(e.asked) > forgetAfter {
			delete(c.entries, k)
		}
	}
	start := len(due) > 0 && !c.inflight && !now.Before(c.holdUntil)
	if start {
		c.inflight = true
	}
	c.mu.Unlock()
	if start {
		c.Async(func() { c.refresh(due) })
	}
	return out
}

// refresh asks GitHub about due and stores the answers. On failure the previous answers stay:
// a row keeps the PR it showed rather than blinking out while GitHub is unreachable.
func (c *Cache) refresh(due []Key) {
	var hold time.Duration
	defer func() {
		c.mu.Lock()
		c.inflight = false
		if hold > 0 {
			c.holdUntil = time.Now().Add(hold)
		}
		c.mu.Unlock()
	}()
	token := c.Token()
	if token == "" {
		// Not connected: nothing to show, and nothing to ask until a later poll finds a token.
		hold = failBackoff
		return
	}
	for len(due) > 0 {
		n := min(len(due), batchSize)
		batch := due[:n]
		due = due[n:]
		got, wait, err := c.fetch(token, batch)
		if err != nil {
			hold = wait
			return
		}
		now := time.Now()
		c.mu.Lock()
		for _, k := range batch {
			if e := c.entries[k]; e != nil {
				e.pr = got[k]
				e.fetched = now
			}
		}
		c.mu.Unlock()
	}
}

// query builds one aliased repository lookup per key. Values travel as variables, never
// spliced into the query text: a branch name is whatever the member typed.
func query(keys []Key) (string, map[string]any) {
	var decl, body strings.Builder
	vars := map[string]any{}
	for i, k := range keys {
		owner, name, _ := strings.Cut(k.Repo, "/")
		o, n, b := fmt.Sprintf("o%d", i), fmt.Sprintf("n%d", i), fmt.Sprintf("b%d", i)
		vars[o], vars[n], vars[b] = owner, name, k.Branch
		fmt.Fprintf(&decl, "$%s:String!,$%s:String!,$%s:String!,", o, n, b)
		// first:5 rather than 1: a branch name reused by a fork's PR into this repository
		// also matches headRefName, and parse skips those.
		fmt.Fprintf(&body, "r%d:repository(owner:$%s,name:$%s){owner{login} defaultBranchRef{name} "+
			"pullRequests(headRefName:$%s,first:5,orderBy:{field:CREATED_AT,direction:DESC}){nodes{"+
			"number state isDraft url headRepositoryOwner{login} "+
			"commits(last:1){nodes{commit{statusCheckRollup{state}}}}}}} ", i, o, n, b)
	}
	return "query(" + strings.TrimSuffix(decl.String(), ",") + "){" + body.String() + "}", vars
}

// fetch runs one batch. wait is how long to hold off after an error.
func (c *Cache) fetch(token string, keys []Key) (map[Key]*PR, time.Duration, error) {
	q, vars := query(keys)
	payload, err := json.Marshal(map[string]any{"query": q, "variables": vars})
	if err != nil {
		return nil, failBackoff, err
	}
	req, err := http.NewRequest("POST", c.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, failBackoff, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "agent-fleet")
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, failBackoff, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			return nil, rateWait(resp.Header, time.Now()), fmt.Errorf("github %d", resp.StatusCode)
		}
		return nil, failBackoff, fmt.Errorf("github %d", resp.StatusCode)
	}
	got, limited := parse(body, keys)
	if limited {
		return nil, rateWait(resp.Header, time.Now()), fmt.Errorf("github rate limit")
	}
	return got, 0, nil
}

// rateWait reads when GitHub will take calls again: Retry-After for the secondary limit, the
// reset stamp for the primary one, and rateBackoff when neither says.
func rateWait(h http.Header, now time.Time) time.Duration {
	if s, err := strconv.Atoi(h.Get("Retry-After")); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	if h.Get("X-RateLimit-Remaining") == "0" {
		if r, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			if d := time.Unix(r, 0).Sub(now); d > 0 {
				return d + time.Second
			}
		}
	}
	return rateBackoff
}

// parse reads one batch's answer. Keys whose repository GitHub could not resolve (renamed,
// not visible to the token) answer nil — no PR — like a branch that has none. limited reports
// a rate-limit answer, which GraphQL delivers as 200 with an error of type RATE_LIMITED.
func parse(body []byte, keys []Key) (out map[Key]*PR, limited bool) {
	type node struct {
		Number    int    `json:"number"`
		State     string `json:"state"`
		IsDraft   bool   `json:"isDraft"`
		URL       string `json:"url"`
		HeadOwner *struct {
			Login string `json:"login"`
		} `json:"headRepositoryOwner"`
		Commits struct {
			Nodes []struct {
				Commit struct {
					Rollup *struct {
						State string `json:"state"`
					} `json:"statusCheckRollup"`
				} `json:"commit"`
			} `json:"nodes"`
		} `json:"commits"`
	}
	type repo struct {
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
		DefaultBranch *struct {
			Name string `json:"name"`
		} `json:"defaultBranchRef"`
		PullRequests struct {
			Nodes []node `json:"nodes"`
		} `json:"pullRequests"`
	}
	var gr struct {
		Data   map[string]*repo `json:"data"`
		Errors []struct {
			Type string `json:"type"`
		} `json:"errors"`
	}
	out = map[Key]*PR{}
	if err := json.Unmarshal(body, &gr); err != nil {
		return out, false
	}
	for _, e := range gr.Errors {
		if e.Type == "RATE_LIMITED" {
			return nil, true
		}
	}
	for i, k := range keys {
		r := gr.Data["r"+strconv.Itoa(i)]
		if r == nil {
			continue
		}
		// The default branch is the base of everyone's PRs, not a feature branch: a session on
		// develop would otherwise show the develop → main release PR as its own.
		if r.DefaultBranch != nil && r.DefaultBranch.Name == k.Branch {
			continue
		}
		for _, n := range r.PullRequests.Nodes {
			if n.HeadOwner == nil || !strings.EqualFold(n.HeadOwner.Login, r.Owner.Login) {
				continue // a fork's branch that happens to share the name
			}
			pr := &PR{Number: n.Number, State: strings.ToLower(n.State), Draft: n.IsDraft, URL: n.URL}
			if len(n.Commits.Nodes) > 0 && n.Commits.Nodes[0].Commit.Rollup != nil {
				pr.Checks = rollupState(n.Commits.Nodes[0].Commit.Rollup.State)
			}
			out[k] = pr
			break
		}
	}
	return out, false
}

// rollupState maps GitHub's StatusState onto the row's three words. EXPECTED is a required
// status nobody has reported yet, so it is still waiting.
func rollupState(s string) string {
	switch strings.ToUpper(s) {
	case "SUCCESS":
		return "success"
	case "FAILURE", "ERROR":
		return "failure"
	case "PENDING", "EXPECTED":
		return "pending"
	}
	return ""
}
