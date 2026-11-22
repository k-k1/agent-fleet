package workitems

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// CI and merge-conflict status for the pull request rows of a Bitbucket list (#4840).
//
// Bitbucket Cloud has no bulk endpoint for either, so this is the one place a saved query costs
// more than one provider call (an exception to ADR 0061 decision 17, see its 2026-11-23 note):
// two reads per pull request, spent only where the cache cannot answer.
//
//   - Cache per connection/workspace/repo/id. The build statuses belong to the source commit and
//     the conflict verdict to the source and destination commits, so each half is re-read only when
//     its own hashes changed.
//   - At most bitbucketStatusBudget pull requests are read per refresh, the most recently updated
//     first. The rest keep a valid cached answer or stay "not read" and are tried next refresh.
//     Repository reads are 1,000 per hour per user; a 5-minute rail with this cap spends at most
//     240 of them.
//   - A 429 or X-RateLimit-NearLimit: true ends the reading for this refresh. The list itself is
//     never lost or failed by this, and a failed read draws nothing — never green.

const (
	bitbucketStatusBudget      = 10
	bitbucketStatusConcurrency = 3
	bitbucketStatusCacheMax    = 4000
)

type bitbucketStatusEntry struct {
	checks    workItemChecksOut
	checksOK  bool
	checksSrc string
	mergeable string
	mergeOK   bool
	mergeSrc  string
	mergeDst  string
}

var bitbucketStatusCache = struct {
	sync.Mutex
	m map[string]bitbucketStatusEntry
}{m: map[string]bitbucketStatusEntry{}}

// bitbucketConnKey separates two connections' cached answers: the same repository can be visible
// to two accounts with different permissions.
func bitbucketConnKey(s *secrets.Data) string {
	if s == nil {
		return ""
	}
	if s.Bitbucket != nil && s.Bitbucket.AccessToken != "" {
		return "oauth:" + s.Bitbucket.Account
	}
	if e, ok := s.Git["bitbucket.org"]; ok {
		return "token:" + e.User
	}
	return ""
}

type bitbucketStatusJob struct {
	idx       int
	cacheKey  string
	prBase    string
	needCheck bool
	needMerge bool
}

type bitbucketStatusResult struct {
	idx       int
	cacheKey  string
	checks    *workItemChecksOut
	mergeable string
	mergeOK   bool
}

// bitbucketEnrichPullRequests fills Checks and Mergeable on the open pull requests among rows.
func bitbucketEnrichPullRequests(auth, conn string, rows []workItemOut) {
	var jobs []bitbucketStatusJob
	for i := range rows {
		r := &rows[i]
		if r.Kind != "pr" || (r.State != "open" && r.State != "in_progress") || r.srcHash == "" || r.dstHash == "" || r.Repo == "" {
			continue
		}
		_, num, ok := strings.Cut(r.Key, "#")
		if !ok || num == "" {
			continue
		}
		ws, name, _ := strings.Cut(r.Repo, "/")
		key := conn + "|" + r.Key
		bitbucketStatusCache.Lock()
		e := bitbucketStatusCache.m[key]
		bitbucketStatusCache.Unlock()
		needCheck := !(e.checksOK && e.checksSrc == r.srcHash)
		needMerge := !(e.mergeOK && e.mergeSrc == r.srcHash && e.mergeDst == r.dstHash)
		if !needCheck {
			r.Checks = e.checks
		}
		if !needMerge {
			r.Mergeable = e.mergeable
		}
		if needCheck || needMerge {
			jobs = append(jobs, bitbucketStatusJob{idx: i, cacheKey: key, needCheck: needCheck, needMerge: needMerge,
				prBase: fmt.Sprintf("%s/2.0/repositories/%s/%s/pullrequests/%s", BitbucketAPIBase,
					url.PathEscape(ws), url.PathEscape(name), url.PathEscape(num))})
		}
	}
	if len(jobs) == 0 {
		return
	}
	sort.SliceStable(jobs, func(a, b int) bool { return rows[jobs[a].idx].UpdatedAt > rows[jobs[b].idx].UpdatedAt })
	if len(jobs) > bitbucketStatusBudget {
		jobs = jobs[:bitbucketStatusBudget]
	}

	var stop atomic.Bool
	results := make([]bitbucketStatusResult, len(jobs))
	ch := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < bitbucketStatusConcurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				results[j] = bitbucketReadStatus(auth, jobs[j], &stop)
			}
		}()
	}
	for j := range jobs {
		if stop.Load() {
			break
		}
		ch <- j
	}
	close(ch)
	wg.Wait()

	for j, res := range results {
		if res.cacheKey == "" {
			continue
		}
		r := &rows[jobs[j].idx]
		bitbucketStatusCache.Lock()
		if len(bitbucketStatusCache.m) >= bitbucketStatusCacheMax {
			bitbucketStatusCache.m = map[string]bitbucketStatusEntry{}
		}
		e := bitbucketStatusCache.m[res.cacheKey]
		if res.checks != nil {
			r.Checks = *res.checks
			e.checks, e.checksOK, e.checksSrc = *res.checks, true, r.srcHash
		}
		if res.mergeable != "" {
			r.Mergeable = res.mergeable
			// "unknown" is a timeout or a still-running computation: shown, never cached.
			if res.mergeOK {
				e.mergeable, e.mergeOK, e.mergeSrc, e.mergeDst = res.mergeable, true, r.srcHash, r.dstHash
			}
		}
		bitbucketStatusCache.m[res.cacheKey] = e
		bitbucketStatusCache.Unlock()
	}
}

func bitbucketReadStatus(auth string, job bitbucketStatusJob, stop *atomic.Bool) bitbucketStatusResult {
	res := bitbucketStatusResult{idx: job.idx, cacheKey: job.cacheKey}
	if job.needCheck {
		if body, ok := bitbucketGetOnce(auth, job.prBase+"/statuses?pagelen=50&fields=values.state", stop); ok {
			c := parseBitbucketStatuses(body)
			res.checks = &c
		}
	}
	if job.needMerge && !stop.Load() {
		if body, ok := bitbucketGetOnce(auth, job.prBase+"/mergeability/checks?fields=values.type,values.status,values.reason,values.blocking", stop); ok {
			res.mergeable = parseBitbucketMergeChecks(body).mergeable
			res.mergeOK = res.mergeable != "unknown"
		}
	}
	return res
}

// bitbucketGetOnce is one GET with no retry: gitx.BitbucketGetStatusHeader retries a 429 after a
// back-off, which is the opposite of stopping. It raises stop on a 429 or a near-limit header
// (the response that carried the header is still used) and reports ok only for a 200.
func bitbucketGetOnce(auth, u string, stop *atomic.Bool) ([]byte, bool) {
	if stop.Load() {
		return nil, false
	}
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	resp, err := workItemHTTPClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusTooManyRequests || strings.EqualFold(strings.TrimSpace(resp.Header.Get("X-RateLimit-NearLimit")), "true") {
		stop.Store(true)
	}
	return body, resp.StatusCode == http.StatusOK
}
