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
// github.com (New, one GraphQL call per batch) and bitbucket.org (NewBitbucket, one REST call
// per repository): the Connections credentials, the hosts the work-item inbox serves. Each
// provider is its own Cache. Any other host, and a provider with no connection, shows nothing.
package branchpr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
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
	// Title and Closes are read for the session's links (the Jira keys a title names, the GitHub
	// issues a PR closes, as "owner/repo#N") and never leave the Agent: the row does not draw them.
	Title  string   `json:"-"`
	Closes []string `json:"-"`
	// Mergeable ("MERGEABLE", "CONFLICTING", "UNKNOWN" while GitHub computes it), ReviewDecision
	// ("APPROVED", "CHANGES_REQUESTED", "REVIEW_REQUIRED", or "" when no review is required) and
	// Head (the head commit) decide the ready-to-merge notification; GitHub only, never on the wire.
	Mergeable      string `json:"-"`
	ReviewDecision string `json:"-"`
	Head           string `json:"-"`
	// ClosedAt is when a merged or closed PR was closed (GitHub; "" on Bitbucket), so a PR
	// reopened and closed again tells apart from its first close. Never on the wire.
	ClosedAt string `json:"-"`
	// ChangesRequested and Conflict are what the row's chip adds to the CI mark: a reviewer asked
	// for changes (GitHub's review decision, any Bitbucket participant), and the PR no longer
	// merges cleanly. Both are read off the cache on each list, never stored.
	ChangesRequested bool `json:"changesRequested,omitempty"`
	Conflict         bool `json:"conflict,omitempty"`
	// NodeID names the PR across a repository rename: GitHub's GraphQL node ID, and on Bitbucket
	// the destination repository's UUID with the PR's number. "" when the provider gave none.
	NodeID string `json:"-"`
	// FailingChecks are the failed checks of the head commit, by name, at most maxFailingChecks.
	// UnresolvedThreads counts review threads nobody resolved, of the newest maxReviewThreads
	// (GitHub; Bitbucket reports 0). Both feed the PR feedback notice and never leave the Agent.
	FailingChecks     []Check `json:"-"`
	UnresolvedThreads int     `json:"-"`
	// ChecksPending reports a check of the head commit still running or not yet reported. The
	// rollup says "failure" as soon as one check fails, and a notice sent then would name the
	// first failure of several. Never on the wire.
	ChecksPending bool `json:"-"`
}

// Check is one failed check of a PR's head commit. URL is the provider's, unchecked: whoever
// shows it vets it.
type Check struct {
	Name string
	URL  string
}

const (
	// maxFailingChecks bounds the names a notice quotes: past ten, the list is noise and the PR
	// page says the rest.
	maxFailingChecks = 10
	// maxCheckContexts and maxReviewThreads bound what one PR asks for in the batched call. A head
	// with more check runs than this reports the failures among the first ones only; the rollup
	// state still covers them all.
	maxCheckContexts = 50
	maxReviewThreads = 50
)

// addFailing appends one failed check, keeping the list sorted by name, unique and bounded.
func addFailing(list []Check, c Check) []Check {
	if c.Name == "" {
		return list
	}
	for _, have := range list {
		if have.Name == c.Name {
			return list
		}
	}
	list = append(list, c)
	sort.SliceStable(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	if len(list) > maxFailingChecks {
		list = list[:maxFailingChecks]
	}
	return list
}

// Ready reports a PR anyone could merge now: open, not a draft, its checks green, no conflict,
// and approved or needing no review. An unknown mergeability is not ready: GitHub answers
// UNKNOWN while it computes, and a later refresh reads the answer.
func (p *PR) Ready() bool {
	return p != nil && p.State == "open" && !p.Draft && p.Checks == "success" && p.Mergeable == "MERGEABLE" &&
		(p.ReviewDecision == "" || p.ReviewDecision == "APPROVED")
}

// Key names one branch of one repository on the cache's provider, or with Number set one PR of
// it by number (Branch is then ignored). Repo is "owner/name" ("workspace/slug" on Bitbucket). A
// PR asked by number is answered whoever's branch it comes from: the caller already holds it.
type Key struct {
	Repo   string
	Branch string
	Number int
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
	// batchSize caps the branches one call asks about. Each is one aliased repository lookup with
	// up to 5 PRs, each with its last commit's check contexts (maxCheckContexts), its newest review
	// threads (maxReviewThreads) and up to 3 closing issues: about 20,000 nodes a full batch, far
	// below GraphQL's 500,000, and a cost of a few points.
	batchSize = 40
)

type entry struct {
	pr      *PR
	fetched time.Time // zero: never answered
	asked   time.Time // last time a list wanted this key
}

func (e *entry) fresh(now time.Time, open time.Duration) bool {
	if e.fetched.IsZero() {
		return false
	}
	ttl := settledTTL
	if e.pr != nil && e.pr.State == "open" {
		ttl = open
	}
	return now.Sub(e.fetched) < ttl
}

// Cache is the per-Agent store. The zero value is not usable; use New.
type Cache struct {
	// Token returns the provider's credential (the GitHub token; Bitbucket's whole
	// Authorization value), "" when the provider is not connected. Read on every refresh,
	// so connecting or disconnecting takes effect without a restart. An error means the store
	// could not be read, which says nothing about the connection: the answers held are kept.
	Token func() (string, error)
	// Renew, when set, is asked for a replacement after the provider answers a token with 401:
	// the recorded expiry of a renewable token can be wrong. The batch is retried once with
	// the token it returns.
	Renew func(rejected string) (string, error)
	// Endpoint is GitHub's GraphQL URL, or Bitbucket's REST base URL; tests point it at a local
	// server.
	Endpoint string
	Client   *http.Client
	// Async runs the refresh. Tests set it to run inline.
	Async func(func())
	// fetch asks the provider about one batch. failed are the keys it could not answer this
	// time; wait is how long to hold off after an error, and errUnauthorized asks for Renew.
	fetch   func(token string, keys []Key) (map[Key]*PR, map[Key]bool, time.Duration, error)
	batch   int
	openTTL time.Duration

	mu        sync.Mutex
	entries   map[Key]*entry
	inflight  bool
	holdUntil time.Time
}

// errUnauthorized marks a 401 from the API: the token was rejected.
var errUnauthorized = errors.New("token rejected")

// New returns a cache that asks api.github.com.
func New(token func() (string, error)) *Cache {
	c := &Cache{
		Token:    token,
		Endpoint: "https://api.github.com/graphql",
		Client:   &http.Client{Timeout: 20 * time.Second},
		Async:    func(f func()) { go f() },
		entries:  map[Key]*entry{},
		batch:    batchSize,
		openTTL:  openTTL,
	}
	c.fetch = c.fetchGitHub
	return c
}

// Lookup returns what is known for keys right now and starts one background refresh for the
// ones that are missing or stale. It never waits on the network.
func (c *Cache) Lookup(keys []Key, now time.Time) map[Key]*PR {
	out := map[Key]*PR{}
	var due []Key
	queued := map[Key]bool{}
	c.mu.Lock()
	for _, k := range keys {
		// Sessions sharing a working copy ask about the same key; it is one alias in the call.
		if queued[k] {
			continue
		}
		queued[k] = true
		e := c.entries[k]
		if e == nil {
			e = &entry{}
			c.entries[k] = e
		}
		e.asked = now
		if e.pr != nil {
			out[k] = e.pr
		}
		if !e.fresh(now, c.openTTL) {
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
	token, err := c.Token()
	if err != nil {
		hold = failBackoff
		return
	}
	if token == "" {
		// Not connected: nothing to show, and nothing to ask until a later poll finds a token.
		// The answers read with the old connection go too — a row must not keep showing a PR
		// and its CI from a connection the member has removed.
		hold = failBackoff
		c.mu.Lock()
		for _, e := range c.entries {
			e.pr = nil
			e.fetched = time.Time{}
		}
		c.mu.Unlock()
		return
	}
	for len(due) > 0 {
		n := min(len(due), c.batch)
		batch := due[:n]
		due = due[n:]
		got, failed, wait, err := c.fetch(token, batch)
		if errors.Is(err, errUnauthorized) && c.Renew != nil {
			if nt, rerr := c.Renew(token); rerr == nil && nt != "" && nt != token {
				token = nt
				got, failed, wait, err = c.fetch(token, batch)
			}
		}
		if err != nil {
			hold = wait
			return
		}
		if len(failed) > 0 {
			hold = failBackoff
		}
		now := time.Now()
		c.mu.Lock()
		for _, k := range batch {
			if failed[k] {
				continue // keeps its last answer, and stays due
			}
			if e := c.entries[k]; e != nil {
				e.pr = got[k]
				e.fetched = now
			}
		}
		c.mu.Unlock()
	}
}

// prFields is what one PR node answers, for a branch's list and for a PR asked by number alike.
var prFields = fmt.Sprintf("id number title state isDraft url mergeable reviewDecision closedAt headRepositoryOwner{login} "+
	"closingIssuesReferences(first:3){nodes{number repository{nameWithOwner}}} "+
	"reviewThreads(last:%d){nodes{isResolved}} "+
	"commits(last:1){nodes{commit{oid statusCheckRollup{state contexts(first:%d){"+
	"checkRunCountsByState{state count} statusContextCountsByState{state count} nodes{__typename "+
	"... on CheckRun{name status conclusion detailsUrl} ... on StatusContext{context state targetUrl}}}}}}}",
	maxReviewThreads, maxCheckContexts)

// query builds one aliased repository lookup per key. Values travel as variables, never
// spliced into the query text: a branch name is whatever the member typed.
func query(keys []Key) (string, map[string]any) {
	var decl, body strings.Builder
	vars := map[string]any{}
	for i, k := range keys {
		owner, name, _ := strings.Cut(k.Repo, "/")
		o, n, b := fmt.Sprintf("o%d", i), fmt.Sprintf("n%d", i), fmt.Sprintf("b%d", i)
		vars[o], vars[n] = owner, name
		if k.Number > 0 {
			vars[b] = k.Number
			fmt.Fprintf(&decl, "$%s:String!,$%s:String!,$%s:Int!,", o, n, b)
			fmt.Fprintf(&body, "r%d:repository(owner:$%s,name:$%s){owner{login} pullRequest(number:$%s){%s}} ",
				i, o, n, b, prFields)
			continue
		}
		vars[b] = k.Branch
		fmt.Fprintf(&decl, "$%s:String!,$%s:String!,$%s:String!,", o, n, b)
		// first:5 rather than 1: a branch name reused by a fork's PR into this repository
		// also matches headRefName, and parse skips those.
		fmt.Fprintf(&body, "r%d:repository(owner:$%s,name:$%s){owner{login} defaultBranchRef{name} "+
			"pullRequests(headRefName:$%s,first:5,orderBy:{field:CREATED_AT,direction:DESC}){nodes{%s}}} ",
			i, o, n, b, prFields)
	}
	return "query(" + strings.TrimSuffix(decl.String(), ",") + "){" + body.String() + "}", vars
}

// fetchGitHub runs one batch. failed are the keys GitHub could not answer this time (their alias
// carried an error other than NOT_FOUND). wait is how long to hold off after an error.
func (c *Cache) fetchGitHub(token string, keys []Key) (map[Key]*PR, map[Key]bool, time.Duration, error) {
	q, vars := query(keys)
	payload, err := json.Marshal(map[string]any{"query": q, "variables": vars})
	if err != nil {
		return nil, nil, failBackoff, err
	}
	req, err := http.NewRequest("POST", c.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, nil, failBackoff, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "agent-fleet")
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, nil, failBackoff, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			return nil, nil, rateWait(resp.Header, time.Now()), fmt.Errorf("github %d", resp.StatusCode)
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, nil, failBackoff, fmt.Errorf("github %d: %w", resp.StatusCode, errUnauthorized)
		}
		return nil, nil, failBackoff, fmt.Errorf("github %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, nil, failBackoff, err
	}
	got, failed, limited, err := parse(body, keys)
	if limited {
		return nil, nil, rateWait(resp.Header, time.Now()), fmt.Errorf("github rate limit")
	}
	if err != nil {
		return nil, nil, failBackoff, err
	}
	return got, failed, 0, nil
}

// rateWait reads when the provider will take calls again: Retry-After (GitHub's secondary limit,
// Bitbucket's 429), the reset stamp of GitHub's primary one, and rateBackoff when neither says.
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

// parse reads one batch's answer. Keys whose repository GitHub could not resolve (NOT_FOUND:
// renamed, not visible to the token) answer nil — no PR — like a branch that has none. Any
// other error on an alias puts its key in failed: GraphQL answers 200 with partial data, and
// taking a failed alias for "no PR" would wipe a row's PR for a whole settledTTL. An error
// that names no alias, a body that does not decode, or no data at all fails the batch. limited
// reports a rate-limit answer, which GraphQL delivers as 200 with an error of type RATE_LIMITED.
func parse(body []byte, keys []Key) (out map[Key]*PR, failed map[Key]bool, limited bool, err error) {
	type repo struct {
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
		DefaultBranch *struct {
			Name string `json:"name"`
		} `json:"defaultBranchRef"`
		PullRequests struct {
			Nodes []ghPRNode `json:"nodes"`
		} `json:"pullRequests"`
		PullRequest *ghPRNode `json:"pullRequest"`
	}
	var gr struct {
		Data   map[string]*repo `json:"data"`
		Errors []struct {
			Type string `json:"type"`
			Path []any  `json:"path"`
		} `json:"errors"`
	}
	out = map[Key]*PR{}
	failed = map[Key]bool{}
	if err := json.Unmarshal(body, &gr); err != nil {
		return nil, nil, false, fmt.Errorf("github answer: %w", err)
	}
	for _, e := range gr.Errors {
		if e.Type == "RATE_LIMITED" {
			return nil, nil, true, nil
		}
	}
	if gr.Data == nil {
		return nil, nil, false, fmt.Errorf("github answered no data")
	}
	for _, e := range gr.Errors {
		if e.Type == "NOT_FOUND" {
			continue
		}
		i := -1
		if len(e.Path) > 0 {
			if alias, ok := e.Path[0].(string); ok && strings.HasPrefix(alias, "r") {
				if n, err := strconv.Atoi(alias[1:]); err == nil && n >= 0 && n < len(keys) {
					i = n
				}
			}
		}
		if i < 0 {
			return nil, nil, false, fmt.Errorf("github error %s", e.Type)
		}
		failed[keys[i]] = true
	}
	for i, k := range keys {
		r := gr.Data["r"+strconv.Itoa(i)]
		if r == nil || failed[k] {
			continue
		}
		if k.Number > 0 {
			if n := r.PullRequest; n != nil && n.Number == k.Number {
				out[k] = n.pr()
			}
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
			out[k] = n.pr()
			break
		}
	}
	return out, failed, false, nil
}

// ghPRNode is one PR as GitHub answers prFields.
type ghPRNode struct {
	ID      string `json:"id"`
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	IsDraft bool   `json:"isDraft"`
	URL     string `json:"url"`
	// Mergeable and ReviewDecision are GitHub's enums; reviewDecision is null when the
	// repository requires no review.
	Mergeable      string  `json:"mergeable"`
	ReviewDecision *string `json:"reviewDecision"`
	ClosedAt       *string `json:"closedAt"`
	Closing        struct {
		Nodes []struct {
			Number int `json:"number"`
			Repo   struct {
				Name string `json:"nameWithOwner"`
			} `json:"repository"`
		} `json:"nodes"`
	} `json:"closingIssuesReferences"`
	HeadOwner *struct {
		Login string `json:"login"`
	} `json:"headRepositoryOwner"`
	Threads struct {
		Nodes []struct {
			IsResolved bool `json:"isResolved"`
		} `json:"nodes"`
	} `json:"reviewThreads"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				OID    string `json:"oid"`
				Rollup *struct {
					State    string `json:"state"`
					Contexts struct {
						// The counts cover every context, past the first maxCheckContexts read.
						RunCounts    []stateCount   `json:"checkRunCountsByState"`
						StatusCounts []stateCount   `json:"statusContextCountsByState"`
						Nodes        []checkContext `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

// pr converts n to the row's PR.
func (n *ghPRNode) pr() *PR {
	pr := &PR{Number: n.Number, State: strings.ToLower(n.State), Draft: n.IsDraft, URL: n.URL, Title: n.Title,
		Mergeable: n.Mergeable, NodeID: n.ID, Conflict: n.Mergeable == "CONFLICTING"}
	if n.ReviewDecision != nil {
		pr.ReviewDecision = *n.ReviewDecision
		pr.ChangesRequested = pr.ReviewDecision == "CHANGES_REQUESTED"
	}
	for _, t := range n.Threads.Nodes {
		if !t.IsResolved {
			pr.UnresolvedThreads++
		}
	}
	if n.ClosedAt != nil {
		pr.ClosedAt = *n.ClosedAt
	}
	for _, c := range n.Closing.Nodes {
		if c.Number > 0 && c.Repo.Name != "" {
			pr.Closes = append(pr.Closes, c.Repo.Name+"#"+strconv.Itoa(c.Number))
		}
	}
	if len(n.Commits.Nodes) > 0 {
		pr.Head = n.Commits.Nodes[0].Commit.OID
		if r := n.Commits.Nodes[0].Commit.Rollup; r != nil {
			pr.Checks = rollupState(r.State)
			for _, c := range r.Contexts.Nodes {
				if f, ok := c.failed(); ok {
					pr.FailingChecks = addFailing(pr.FailingChecks, f)
				}
				pr.ChecksPending = pr.ChecksPending || c.pending()
			}
			// The counts span every context, so one still running past the first ones read
			// holds the notice too: the rollup says FAILURE as soon as any one has failed.
			for _, c := range r.Contexts.RunCounts {
				switch strings.ToUpper(c.State) {
				case "QUEUED", "IN_PROGRESS", "WAITING", "PENDING", "REQUESTED":
					pr.ChecksPending = pr.ChecksPending || c.Count > 0
				}
			}
			for _, c := range r.Contexts.StatusCounts {
				switch strings.ToUpper(c.State) {
				case "PENDING", "EXPECTED":
					pr.ChecksPending = pr.ChecksPending || c.Count > 0
				}
			}
			pr.ChecksPending = pr.ChecksPending || pr.Checks == "pending"
		}
	}
	return pr
}

// stateCount is one line of a rollup's count of contexts by state.
type stateCount struct {
	State string `json:"state"`
	Count int    `json:"count"`
}

// checkContext is one entry of a rollup: a check run (Actions and other apps) or a commit status.
type checkContext struct {
	Type       string `json:"__typename"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	DetailsURL string `json:"detailsUrl"`
	Context    string `json:"context"`
	State      string `json:"state"`
	TargetURL  string `json:"targetUrl"`
}

// failed reports a context that counts against the rollup, as GitHub's rollup counts it: a
// cancelled or timed-out run did not pass either. A run still going has no conclusion yet.
func (c checkContext) failed() (Check, bool) {
	switch c.Type {
	case "CheckRun":
		switch strings.ToUpper(c.Conclusion) {
		case "FAILURE", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE":
			return Check{Name: c.Name, URL: c.DetailsURL}, true
		}
	case "StatusContext":
		switch strings.ToUpper(c.State) {
		case "FAILURE", "ERROR":
			return Check{Name: c.Context, URL: c.TargetURL}, true
		}
	}
	return Check{}, false
}

// pending reports a context that has not finished: a check run not yet completed (queued,
// in progress, waiting), or a commit status still pending or only expected.
func (c checkContext) pending() bool {
	switch c.Type {
	case "CheckRun":
		return c.Status != "" && !strings.EqualFold(c.Status, "COMPLETED")
	case "StatusContext":
		switch strings.ToUpper(c.State) {
		case "PENDING", "EXPECTED":
			return true
		}
	}
	return false
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
