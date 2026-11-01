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
// with OR. Calls then scale with the repositories sessions work in, not with the sessions, which
// is what keeps the list inside Bitbucket's hourly request budget.

const (
	// bitbucketMainTTL is how long a repository's main branch is trusted. It is asked so a
	// session on it does not show the release PR whose source it is, and it almost never moves.
	bitbucketMainTTL = time.Hour
	// bitbucketBranchesPerCall bounds one filter. The answer is a single page of the newest PRs
	// across those branches; with more branches, one busy branch could push another's PR off it.
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
			got, wait, err := b.pulls(auth, repo, chunk)
			if errors.Is(err, errNotVisible) {
				continue
			}
			if err != nil {
				if wait > 0 {
					return nil, nil, wait, err
				}
				markFailed(failed, chunk)
				continue
			}
			for k, pr := range got {
				out[k] = pr
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
	b.main[repo] = mainEntry{name: name, at: now}
	b.mu.Unlock()
	return name, 0, nil
}

// pulls answers the newest PR of each branch in keys, all of one repository, in one call. An
// open PR's build statuses are a second call each; one that fails leaves Checks empty rather
// than failing the PR.
func (b *bitbucket) pulls(auth, repo string, keys []Key) (map[Key]*PR, time.Duration, error) {
	params := url.Values{
		"q":       {bitbucketFilter(keys)},
		"sort":    {"-created_on"},
		"pagelen": {strconv.Itoa(bitbucketPageLen)},
		// A PR object carries its whole description; only the row's columns are asked for.
		"fields": {"values.id,values.state,values.draft,values.links.html.href," +
			"values.source.branch.name,values.source.repository.full_name"},
	}
	body, wait, err := b.get(auth, repoPath(repo)+"/pullrequests?"+params.Encode())
	if err != nil {
		return nil, wait, err
	}
	got, open, err := parseBitbucketPulls(body, repo, keys)
	if err != nil {
		return nil, 0, err
	}
	for _, k := range open {
		pr := got[k]
		if sb, _, err := b.get(auth, fmt.Sprintf("%s/pullrequests/%d/statuses?pagelen=%d&fields=values.state",
			repoPath(repo), pr.Number, bitbucketPageLen)); err == nil {
			pr.Checks = bitbucketRollup(sb)
		}
	}
	return got, 0, nil
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
// fork whose branch shares the name is skipped, as on GitHub. open lists the keys whose PR is
// open, for the status call.
func parseBitbucketPulls(body []byte, repo string, keys []Key) (map[Key]*PR, []Key, error) {
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
			} `json:"source"`
		} `json:"values"`
	}
	if err := json.Unmarshal(body, &br); err != nil {
		return nil, nil, fmt.Errorf("bitbucket answer: %w", err)
	}
	want := map[string]Key{}
	for _, k := range keys {
		want[k.Branch] = k
	}
	out := map[Key]*PR{}
	var open []Key
	for _, v := range br.Values {
		k, ok := want[v.Source.Branch.Name]
		if !ok || out[k] != nil || v.ID <= 0 {
			continue
		}
		if v.Source.Repository == nil || !strings.EqualFold(v.Source.Repository.FullName, repo) {
			continue
		}
		state, ok := bitbucketState(v.State)
		if !ok {
			continue
		}
		out[k] = &PR{Number: v.ID, State: state, Draft: state == "open" && v.Draft, URL: v.Links.HTML.Href}
		if state == "open" {
			open = append(open, k)
		}
	}
	return out, open, nil
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

// bitbucketRollup folds a PR's build statuses into the row's three words. A stopped build did
// not pass, so it counts as a failure; no status at all is "", which must not read as green.
func bitbucketRollup(body []byte) string {
	var sr struct {
		Values []struct {
			State string `json:"state"`
		} `json:"values"`
	}
	if json.Unmarshal(body, &sr) != nil {
		return ""
	}
	var failed, pending, passed bool
	for _, v := range sr.Values {
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
	case http.StatusUnauthorized:
		return nil, failBackoff, fmt.Errorf("bitbucket %d: %w", resp.StatusCode, errUnauthorized)
	case http.StatusForbidden:
		return nil, settledTTL, errRefused
	case http.StatusTooManyRequests:
		return nil, rateWait(resp.Header, time.Now()), fmt.Errorf("bitbucket rate limit")
	}
	return nil, 0, fmt.Errorf("bitbucket %d", resp.StatusCode)
}
