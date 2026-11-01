package branchpr

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Bitbucket Cloud has no batched lookup like GraphQL aliases, so a refresh asks once per
// repository: every branch of that repository due in the batch goes into one `q` filter, joined
// with OR. Calls then scale with the repositories sessions work in, not with the sessions.
//
// The budget is Bitbucket's 1000 requests an hour per account, shared with the work-item inbox.
// One refresh costs, per repository, ceil(branches/10) list calls plus one status call per open
// PR, and the main branch once an hour. An open PR is re-read every bitbucketOpenTTL (12 refreshes
// an hour), settled ones every settledTTL (6), so 10 repositories with 20 open PRs cost about
// (10+20)×12 = 360 calls an hour. Where the commit endpoint does not resolve a short hash, the
// status fallback makes that two calls per open PR: (10+40)×12 = 600.

const (
	// bitbucketOpenTTL is GitHub's openTTL stretched for that budget: an open PR costs a status
	// call on every refresh, where GitHub answers its CI in the same single call.
	bitbucketOpenTTL = 5 * time.Minute
	// bitbucketMainTTL is how long a repository's main branch is trusted. It is asked so a
	// session on it does not show the release PR whose source it is, and it almost never moves.
	bitbucketMainTTL = time.Hour
	// bitbucketBranchesPerCall bounds one filter. The answer is a single page of the newest PRs
	// across those branches; a branch a full page leaves unanswered is asked again on its own.
	bitbucketBranchesPerCall = 10
	// bitbucketPageLen is that page.
	bitbucketPageLen = 50
)

var (
	// errNotVisible is a 404: the repository is gone or this connection cannot see it, which
	// answers "no PR" like GitHub's NOT_FOUND.
	errNotVisible = errors.New("repository not visible")
	// errRefused is a 403. A connection made for cloning lacks the pull request scope, and that
	// will not change until it is reconnected, so the whole provider holds off.
	errRefused = errors.New("bitbucket refused the read")
	// errBadFilter is a 400 on the PR list: Bitbucket could not take the filter. With escaped
	// values that is expected to be one branch's odd name; a 400 for any other reason leaves the
	// branches it hits without a PR until it stops, rather than holding every row off.
	errBadFilter = errors.New("bitbucket could not parse the filter")
)

// NewBitbucket returns a cache that asks api.bitbucket.org. auth returns the whole Authorization
// value ("Bearer …" for OAuth, "Basic …" for an API token), "" when Bitbucket is not connected.
func NewBitbucket(auth func() (string, error)) *Cache {
	c := &Cache{
		Token:    auth,
		Endpoint: "https://api.bitbucket.org",
		Client:   &http.Client{Timeout: 20 * time.Second},
		Async:    func(f func()) { go f() },
		entries:  map[Key]*entry{},
		batch:    batchSize,
		openTTL:  bitbucketOpenTTL,
	}
	b := &bitbucket{c: c, main: map[string]mainEntry{}}
	c.fetch = b.fetch
	return c
}

type mainEntry struct {
	name string
	at   time.Time
}

type bitbucket struct {
	c    *Cache
	mu   sync.Mutex
	main map[string]mainEntry
}

// bbHit is a branch's PR with the commit its build statuses are read for.
type bbHit struct {
	pr   *PR
	head string
}

func (b *bitbucket) fetch(auth string, keys []Key) (map[Key]*PR, map[Key]bool, time.Duration, error) {
	out, failed := map[Key]*PR{}, map[Key]bool{}
	byRepo := map[string][]Key{}
	var repos []string
	for _, k := range keys {
		if _, ok := byRepo[k.Repo]; !ok {
			repos = append(repos, k.Repo)
		}
		byRepo[k.Repo] = append(byRepo[k.Repo], k)
	}
	for _, repo := range repos {
		ks := byRepo[repo]
		main, wait, err := b.mainBranch(auth, repo)
		if errors.Is(err, errNotVisible) {
			continue // no PR for any of its branches
		}
		if err != nil {
			if wait > 0 {
				return nil, nil, wait, err
			}
			markFailed(failed, ks)
			continue
		}
		var branches []Key
		for _, k := range ks {
			// The main branch is the base of everyone's PRs, not a feature branch.
			if k.Branch != main {
				branches = append(branches, k)
			}
		}
		for len(branches) > 0 {
			n := min(len(branches), bitbucketBranchesPerCall)
			chunk := branches[:n]
			branches = branches[n:]
			got, bad, wait, err := b.pulls(auth, repo, chunk)
			if err != nil {
				return nil, nil, wait, err
			}
			markFailed(failed, bad)
			for k, h := range got {
				if h.pr.State == "open" && h.head != "" {
					h.pr.Checks = b.checks(auth, repo, h.pr.Number, h.head)
				}
				out[k] = h.pr
			}
		}
	}
	return out, failed, 0, nil
}

func markFailed(failed map[Key]bool, keys []Key) {
	for _, k := range keys {
		failed[k] = true
	}
}

// mainBranch is repo's main branch, from a cache held for bitbucketMainTTL.
func (b *bitbucket) mainBranch(auth, repo string) (string, time.Duration, error) {
	now := time.Now()
	b.mu.Lock()
	e, ok := b.main[repo]
	b.mu.Unlock()
	if ok && now.Sub(e.at) < bitbucketMainTTL {
		return e.name, 0, nil
	}
	body, wait, err := b.get(auth, repoPath(repo)+"?fields=mainbranch.name")
	if err != nil {
		return "", wait, err
	}
	var r struct {
		Main *struct {
			Name string `json:"name"`
		} `json:"mainbranch"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", 0, fmt.Errorf("bitbucket answer: %w", err)
	}
	name := ""
	if r.Main != nil {
		name = r.Main.Name
	}
	b.mu.Lock()
	// Repositories no session works in any more go with the next answer.
	for k, old := range b.main {
		if now.Sub(old.at) >= bitbucketMainTTL {
			delete(b.main, k)
		}
	}
	b.main[repo] = mainEntry{name: name, at: now}
	b.mu.Unlock()
	return name, 0, nil
}

// pulls answers the newest PR of each branch in keys, all of one repository. failed are the
// branches Bitbucket could not answer this time; an error (with its wait) holds the whole
// provider off.
//
// Two answers are re-asked one branch at a time rather than taken for the whole chunk: a 400,
// which a single odd branch name causes and which would otherwise fail its nine neighbours on
// every refresh; and a full page, past which a branch's PR may lie, so that "absent" would read
// as "no PR" for a settledTTL.
func (b *bitbucket) pulls(auth, repo string, keys []Key) (map[Key]bbHit, []Key, time.Duration, error) {
	params := url.Values{
		"q":       {bitbucketFilter(keys)},
		"sort":    {"-created_on"},
		"pagelen": {strconv.Itoa(bitbucketPageLen)},
		// A PR object carries its whole description; only the row's columns are asked for.
		"fields": {"values.id,values.state,values.draft,values.links.html.href," +
			"values.source.branch.name,values.source.repository.full_name,values.source.commit.hash"},
	}
	body, wait, err := b.get(auth, repoPath(repo)+"/pullrequests?"+params.Encode())
	switch {
	case errors.Is(err, errNotVisible):
		return nil, nil, 0, nil
	case errors.Is(err, errBadFilter) && len(keys) > 1:
		return b.oneByOne(auth, repo, keys)
	case errors.Is(err, errBadFilter):
		return nil, nil, 0, nil // a name Bitbucket's filter cannot take has no PR to show
	case err != nil && wait > 0:
		return nil, nil, wait, err
	case err != nil:
		return nil, keys, 0, nil
	}
	got, full, err := parseBitbucketPulls(body, repo, keys)
	if err != nil {
		return nil, keys, 0, nil
	}
	if !full || len(keys) == 1 {
		return got, nil, 0, nil
	}
	var rest []Key
	for _, k := range keys {
		if _, ok := got[k]; !ok {
			rest = append(rest, k)
		}
	}
	more, failed, wait, err := b.oneByOne(auth, repo, rest)
	if err != nil {
		return nil, nil, wait, err
	}
	for k, h := range more {
		got[k] = h
	}
	return got, failed, 0, nil
}

func (b *bitbucket) oneByOne(auth, repo string, keys []Key) (map[Key]bbHit, []Key, time.Duration, error) {
	got := map[Key]bbHit{}
	var failed []Key
	for _, k := range keys {
		one, bad, wait, err := b.pulls(auth, repo, []Key{k})
		if err != nil {
			return nil, nil, wait, err
		}
		failed = append(failed, bad...)
		for kk, h := range one {
			got[kk] = h
		}
	}
	return got, failed, 0, nil
}

// checks folds the build statuses of an open PR's head commit. Asked of the commit, not of the
// PR: the PR's own status list spans every commit it has had, and an old failure would keep a
// fixed PR red. A failed call leaves the checks empty rather than failing the PR.
//
// The PR list gives the head as a short hash. Should the commit endpoint not resolve one (404),
// the PR's list is read instead and only the statuses whose commit link starts with that hash are
// folded: one more call, never a stale verdict.
func (b *bitbucket) checks(auth, repo string, pr int, head string) string {
	if !isHex(head) {
		return ""
	}
	body, _, err := b.get(auth, fmt.Sprintf("%s/commit/%s/statuses?pagelen=%d&fields=values.state",
		repoPath(repo), head, bitbucketPageLen))
	if err == nil {
		return bitbucketRollup(body, "")
	}
	if !errors.Is(err, errNotVisible) {
		return ""
	}
	body, _, err = b.get(auth, fmt.Sprintf("%s/pullrequests/%d/statuses?pagelen=%d&fields=values.state,values.links.commit.href",
		repoPath(repo), pr, bitbucketPageLen))
	if err != nil {
		return ""
	}
	return bitbucketRollup(body, head)
}

func isHex(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return s != ""
}

// bitbucketFilter is the `q` for keys' branches in every state: without a state term
// Bitbucket answers open PRs only, and a merged one has to read as merged.
func bitbucketFilter(keys []Key) string {
	terms := make([]string, len(keys))
	for i, k := range keys {
		terms[i] = `source.branch.name = "` + bbqlEscape(k.Branch) + `"`
	}
	return "(" + strings.Join(terms, " OR ") + `) AND (state = "OPEN" OR state = "MERGED" OR ` +
		`state = "DECLINED" OR state = "SUPERSEDED")`
}

// bbqlEscape quotes a branch name for a BBQL string literal. A branch name is whatever the member
// typed, and git allows a double quote in one.
func bbqlEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}

// parseBitbucketPulls picks each branch's newest PR from a page sorted newest first. A PR from a
// fork whose branch shares the name is skipped, as on GitHub. full reports a page Bitbucket
// filled, past which more PRs may lie.
func parseBitbucketPulls(body []byte, repo string, keys []Key) (map[Key]bbHit, bool, error) {
	var br struct {
		Values []struct {
			ID    int    `json:"id"`
			State string `json:"state"`
			Draft bool   `json:"draft"`
			Links struct {
				HTML struct {
					Href string `json:"href"`
				} `json:"html"`
			} `json:"links"`
			Source struct {
				Branch struct {
					Name string `json:"name"`
				} `json:"branch"`
				Repository *struct {
					FullName string `json:"full_name"`
				} `json:"repository"`
				Commit *struct {
					Hash string `json:"hash"`
				} `json:"commit"`
			} `json:"source"`
		} `json:"values"`
	}
	if err := json.Unmarshal(body, &br); err != nil {
		return nil, false, fmt.Errorf("bitbucket answer: %w", err)
	}
	want := map[string]Key{}
	for _, k := range keys {
		want[k.Branch] = k
	}
	out := map[Key]bbHit{}
	for _, v := range br.Values {
		k, ok := want[v.Source.Branch.Name]
		if _, done := out[k]; !ok || done || v.ID <= 0 {
			continue
		}
		if v.Source.Repository == nil || !strings.EqualFold(v.Source.Repository.FullName, repo) {
			continue
		}
		state, ok := bitbucketState(v.State)
		if !ok {
			continue
		}
		h := bbHit{pr: &PR{Number: v.ID, State: state, Draft: state == "open" && v.Draft, URL: v.Links.HTML.Href}}
		if v.Source.Commit != nil {
			h.head = v.Source.Commit.Hash
		}
		out[k] = h
	}
	return out, len(br.Values) >= bitbucketPageLen, nil
}

// bitbucketState maps Bitbucket's states onto the row's. A declined or superseded PR is closed:
// nobody is still working on it.
func bitbucketState(s string) (string, bool) {
	switch strings.ToUpper(s) {
	case "OPEN":
		return "open", true
	case "MERGED":
		return "merged", true
	case "DECLINED", "SUPERSEDED":
		return "closed", true
	}
	return "", false
}

// bitbucketRollup folds build statuses into the row's three words. A stopped build did not pass,
// so it counts as a failure; no status at all is "", which must not read as green. With head set,
// only the statuses of the commit whose hash starts with it count.
func bitbucketRollup(body []byte, head string) string {
	var sr struct {
		Values []struct {
			State string `json:"state"`
			Links struct {
				Commit struct {
					Href string `json:"href"`
				} `json:"commit"`
			} `json:"links"`
		} `json:"values"`
	}
	if json.Unmarshal(body, &sr) != nil {
		return ""
	}
	var failed, pending, passed bool
	for _, v := range sr.Values {
		if head != "" {
			_, hash, _ := strings.Cut(v.Links.Commit.Href, "/commit/")
			if !strings.HasPrefix(strings.ToLower(hash), strings.ToLower(head)) {
				continue
			}
		}
		switch strings.ToUpper(v.State) {
		case "FAILED", "STOPPED":
			failed = true
		case "INPROGRESS":
			pending = true
		case "SUCCESSFUL":
			passed = true
		}
	}
	switch {
	case failed:
		return "failure"
	case pending:
		return "pending"
	case passed:
		return "success"
	}
	return ""
}

func repoPath(repo string) string {
	ws, slug, _ := strings.Cut(repo, "/")
	return "/2.0/repositories/" + url.PathEscape(ws) + "/" + url.PathEscape(slug)
}

// get performs one GET. A non-zero wait comes with an error that holds the whole provider off
// (the rate limit, a refused scope); any other error is about this one call.
func (b *bitbucket) get(auth, path string) ([]byte, time.Duration, error) {
	req, err := http.NewRequest("GET", b.c.Endpoint+path, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "agent-fleet")
	resp, err := b.c.Client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, 0, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return body, 0, nil
	case http.StatusNotFound:
		return nil, 0, errNotVisible
	case http.StatusBadRequest:
		return nil, 0, errBadFilter
	case http.StatusUnauthorized:
		return nil, failBackoff, fmt.Errorf("bitbucket %d: %w", resp.StatusCode, errUnauthorized)
	case http.StatusForbidden:
		return nil, settledTTL, errRefused
	case http.StatusTooManyRequests:
		return nil, rateWait(resp.Header, time.Now()), fmt.Errorf("bitbucket rate limit")
	}
	return nil, 0, fmt.Errorf("bitbucket %d", resp.StatusCode)
}
