package workitems

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// CI and merge-conflict status for the pull request rows of Bitbucket lists (#4840).
//
// Bitbucket Cloud has no bulk endpoint for either, so this is the one place a saved query costs
// more than one provider call (an exception to ADR 0061 decision 17, see its 2026-11-23 note):
// two reads per pull request, spent only where the cache cannot answer.
//
//   - It runs once per /work-items/fetch, over the rows of every query, so the budget, the stop
//     and the deadline are per refresh and not per query.
//   - Cache per connection/workspace/repo/id. Build statuses belong to the source commit and the
//     conflict verdict to the source and destination commits, so each half is re-read only when
//     its own hashes changed. Only a final answer is kept: a pending build or an "unknown" verdict
//     is shown and read again next time.
//   - At most bitbucketStatusBudget pull requests are read per refresh, most recently updated
//     first, except that a pull request whose last attempt failed or stayed provisional goes behind
//     those never tried (otherwise a persistent 500 on the newest ten starves the rest).
//   - A 429 or X-RateLimit-NearLimit: true (on the list or on a read, noticed as soon as the
//     headers arrive) ends the reading; nothing is retried. The shared deadline keeps a stuck read
//     from costing the list the Control Plane is waiting for.
//   - A row shows its status only when both halves were read. A failed half leaves the row "not
//     read": no CI mark, no conflict mark, never green.

const (
	bitbucketStatusBudget      = 10
	bitbucketStatusConcurrency = 3
	bitbucketStatusCacheMax    = 4000
	bitbucketStatusBody        = 1 << 20
)

// bitbucketStatusWindow is how long after the refresh started the reads may still run. The Control
// Plane gives the whole fetch 30 s (workItemsFetchTimeout there). A var for the tests.
var bitbucketStatusWindow = 20 * time.Second

type bitbucketStatusEntry struct {
	checks    workItemChecksOut
	checksOK  bool
	checksSrc string
	checksSeq uint64

	mergeable string
	mergeOK   bool
	mergeSrc  string
	mergeDst  string
	mergeSeq  uint64

	// unsettled: the last attempt failed or produced an answer not worth keeping.
	unsettled bool
}

var bitbucketStatusCache = struct {
	sync.Mutex
	m   map[string]bitbucketStatusEntry
	seq uint64
}{m: map[string]bitbucketStatusEntry{}}

// bitbucketConnKey tells two connections apart by what they authenticate with, so another
// account or a replaced token never reads the previous one's answers.
func bitbucketConnKey(s *secrets.Data) string {
	var raw string
	switch {
	case s == nil:
		return ""
	case s.Bitbucket != nil && s.Bitbucket.AccessToken != "":
		raw = "oauth:" + s.Bitbucket.RefreshToken + ":" + s.Bitbucket.Account
	default:
		e := s.Git["bitbucket.org"]
		raw = "token:" + e.User + ":" + e.Token
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}

// bbGroup is one pull request at one pair of hashes, with every row (of any query) showing it.
type bbGroup struct {
	cacheKey         string
	prBase           string
	srcHash, dstHash string
	updated          string
	idxs             []int

	needCheck, needMerge bool
	unsettled            bool
	checks               workItemChecksOut
	haveChecks           bool
	mergeable            string
	haveMerge            bool
	// what this refresh wrote back to the cache
	newChecks, newMerge   bool
	keepChecks, keepMerge bool
}

// bitbucketEnrichItems fills Checks and Mergeable on the open Bitbucket pull requests among items.
func bitbucketEnrichItems(s *secrets.Data, items []workItemOut, started time.Time) {
	groups := map[string]*bbGroup{}
	order := []*bbGroup{}
	conn := ""
	near := false
	for i := range items {
		r := &items[i]
		if r.Provider != "bitbucket" || r.Kind != "pr" || (r.State != "open" && r.State != "in_progress") ||
			r.srcHash == "" || r.dstHash == "" || r.Repo == "" {
			continue
		}
		_, num, ok := strings.Cut(r.Key, "#")
		if !ok || num == "" {
			continue
		}
		near = near || r.bbListNear
		if conn == "" {
			conn = bitbucketConnKey(s)
		}
		gk := conn + "|" + r.Key + "|" + r.srcHash + "|" + r.dstHash
		g := groups[gk]
		if g == nil {
			ws, name, _ := strings.Cut(r.Repo, "/")
			g = &bbGroup{cacheKey: conn + "|" + r.Key, srcHash: r.srcHash, dstHash: r.dstHash, updated: r.UpdatedAt,
				prBase: fmt.Sprintf("%s/2.0/repositories/%s/%s/pullrequests/%s", BitbucketAPIBase,
					url.PathEscape(ws), url.PathEscape(name), url.PathEscape(num))}
			groups[gk] = g
			order = append(order, g)
		}
		g.idxs = append(g.idxs, i)
	}
	if len(order) == 0 {
		return
	}

	var seq uint64
	bitbucketStatusCache.Lock()
	bitbucketStatusCache.seq++
	seq = bitbucketStatusCache.seq
	for _, g := range order {
		e := bitbucketStatusCache.m[g.cacheKey]
		if e.checksOK && e.checksSrc == g.srcHash {
			g.checks, g.haveChecks = e.checks, true
		} else {
			g.needCheck = true
		}
		if e.mergeOK && e.mergeSrc == g.srcHash && e.mergeDst == g.dstHash {
			g.mergeable, g.haveMerge = e.mergeable, true
		} else {
			g.needMerge = true
		}
		g.unsettled = e.unsettled
	}
	bitbucketStatusCache.Unlock()

	var jobs []*bbGroup
	for _, g := range order {
		if g.needCheck || g.needMerge {
			jobs = append(jobs, g)
		}
	}
	deadline := started.Add(bitbucketStatusWindow)
	if len(jobs) > 0 && !near && time.Now().Before(deadline) {
		sort.SliceStable(jobs, func(a, b int) bool {
			if jobs[a].unsettled != jobs[b].unsettled {
				return !jobs[a].unsettled
			}
			return jobs[a].updated > jobs[b].updated
		})
		if len(jobs) > bitbucketStatusBudget {
			jobs = jobs[:bitbucketStatusBudget]
		}
		auth, err := gitx.BitbucketAuthHeader(s)
		if err == nil {
			bitbucketReadAll(auth, jobs, deadline)
		}
	}

	bitbucketStatusCache.Lock()
	for _, g := range jobs {
		bitbucketStoreGroup(g, seq)
	}
	bitbucketStatusCache.Unlock()

	for _, g := range order {
		if !g.haveChecks || !g.haveMerge {
			continue
		}
		for _, i := range g.idxs {
			items[i].Checks, items[i].Mergeable = g.checks, g.mergeable
		}
	}
}

// bitbucketStoreGroup writes what this refresh learned. A write from a refresh that started before
// the one that wrote the entry is dropped, so a slow older refresh cannot erase a newer answer.
// The caller holds the cache lock.
func bitbucketStoreGroup(g *bbGroup, seq uint64) {
	e, existed := bitbucketStatusCache.m[g.cacheKey]
	if !existed && len(bitbucketStatusCache.m) >= bitbucketStatusCacheMax {
		for k := range bitbucketStatusCache.m {
			delete(bitbucketStatusCache.m, k)
			break
		}
	}
	if g.newChecks && seq >= e.checksSeq {
		e.checks, e.checksOK, e.checksSrc, e.checksSeq = g.checks, true, g.srcHash, seq
	}
	if g.newMerge && seq >= e.mergeSeq {
		e.mergeable, e.mergeOK, e.mergeSrc, e.mergeDst, e.mergeSeq = g.mergeable, true, g.srcHash, g.dstHash, seq
	}
	e.unsettled = g.unsettled
	bitbucketStatusCache.m[g.cacheKey] = e
}

func bitbucketReadAll(auth string, jobs []*bbGroup, deadline time.Time) {
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	var stop atomic.Bool
	ch := make(chan *bbGroup)
	var wg sync.WaitGroup
	for w := 0; w < bitbucketStatusConcurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for g := range ch {
				bitbucketReadGroup(ctx, auth, g, &stop)
			}
		}()
	}
	for _, g := range jobs {
		if stop.Load() || ctx.Err() != nil {
			break
		}
		ch <- g
	}
	close(ch)
	wg.Wait()
}

// bitbucketReadGroup reads the stale halves of one pull request. Each worker owns its group.
func bitbucketReadGroup(ctx context.Context, auth string, g *bbGroup, stop *atomic.Bool) {
	g.unsettled = false
	if g.needCheck {
		if body, ok := bitbucketGetOnce(ctx, auth, g.prBase+"/statuses?pagelen=50&fields=values.state,next", stop); ok {
			if c, final, ok := parseBitbucketStatusesPage(body); ok {
				g.checks, g.haveChecks = c, true
				// Pending builds change under an unchanged hash, and an empty page may not be the
				// whole answer: show them, keep nothing.
				g.newChecks = final
				g.unsettled = g.unsettled || !final
			}
		}
		if !g.haveChecks {
			g.unsettled = true
		}
	}
	if g.needMerge && !stop.Load() {
		if body, ok := bitbucketGetOnce(ctx, auth, g.prBase+"/mergeability/checks?fields=values.type,values.status,values.reason,values.blocking", stop); ok && bitbucketHasValues(body) {
			g.mergeable, g.haveMerge = parseBitbucketMergeChecks(body).mergeable, true
			g.newMerge = g.mergeable != "unknown"
			g.unsettled = g.unsettled || !g.newMerge
		}
		if !g.haveMerge {
			g.unsettled = true
		}
	}
	if !g.haveChecks || !g.haveMerge {
		g.unsettled = true
	}
}

func bitbucketHasValues(body []byte) bool {
	var r struct {
		Values *[]json.RawMessage `json:"values"`
	}
	return json.Unmarshal(body, &r) == nil && r.Values != nil
}

// parseBitbucketStatusesPage folds one page of build statuses. ok is false for a body that is not
// a statuses page. final says the answer may be kept for this commit: no build still running, and
// no further page that could still hold a failure while this one is all green.
func parseBitbucketStatusesPage(body []byte) (c workItemChecksOut, final, ok bool) {
	var p struct {
		Values *[]json.RawMessage `json:"values"`
		Next   string             `json:"next"`
	}
	if json.Unmarshal(body, &p) != nil || p.Values == nil {
		return workItemChecksOut{}, false, false
	}
	c = parseBitbucketStatuses(body)
	if p.Next != "" && c.Failed == 0 {
		// Page one says nothing about the rest, so neither green nor pending can be claimed.
		return workItemChecksOut{}, false, false
	}
	return c, c.Pending == 0, true
}

// bitbucketGetOnce is one GET with no retry: gitx.BitbucketGetStatusHeader retries a 429 after a
// back-off, which is the opposite of stopping. It raises stop on a 429 or a near-limit header as
// soon as the headers arrive, before the body, and reports ok only for a complete 200.
func bitbucketGetOnce(ctx context.Context, auth, u string, stop *atomic.Bool) ([]byte, bool) {
	if stop.Load() || ctx.Err() != nil {
		return nil, false
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
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
	if resp.StatusCode == http.StatusTooManyRequests || strings.EqualFold(strings.TrimSpace(resp.Header.Get("X-RateLimit-NearLimit")), "true") {
		stop.Store(true)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, bitbucketStatusBody+1))
	if err != nil || len(body) > bitbucketStatusBody {
		return nil, false
	}
	return body, true
}
