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

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// CI and merge-conflict status for the pull request rows of Bitbucket lists (#4840).
//
// Bitbucket Cloud has no bulk endpoint for either, so this is the one place a saved query costs
// more than one provider call (an exception to ADR 0061 decision 17, see its 2026-11-23 note):
// two reads per pull request, spent only where the cache cannot answer.
//
//   - It runs once per refresh over the rows of every query (fetchWorkItems), so the budget, the
//     stop and the deadline are per refresh and not per query.
//   - Cache per connection/workspace/repo/id. Build statuses belong to the source commit and the
//     conflict verdict to the source and destination commits, so each half is re-read only when
//     its own hashes changed. Only a final answer is kept: a pending build, a partial statuses
//     page or an "unknown" verdict is shown (or withheld) and read again next time.
//   - At most bitbucketStatusBudget pull requests are read per refresh, most recently updated
//     first, except that a pull request whose last attempt failed or stayed provisional goes behind
//     those never tried, oldest attempt first (otherwise a persistent 500 on the newest ten starves
//     the rest).
//   - A 429 or X-RateLimit-NearLimit: true on any list or read of the refresh ends the reading (a
//     read notices it when the headers arrive); nothing is retried. The shared deadline keeps a
//     stuck read from costing the list the Control Plane is waiting for, and the reads reuse the
//     list's Authorization so no token refresh can add to the wait.
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

	mergeable string
	mergeOK   bool
	mergeSrc  string
	mergeDst  string

	// unsettled: the last attempt failed or produced an answer not worth keeping. tried is the
	// refresh that made it, so retries go oldest first.
	unsettled bool
	tried     uint64
	// seq is the newest refresh that wrote this entry; an older refresh finishing later is dropped.
	seq uint64
}

var bitbucketStatusCache = struct {
	sync.Mutex
	m   map[string]bitbucketStatusEntry
	seq uint64
}{m: map[string]bitbucketStatusEntry{}}

// bitbucketRateLimitedAt is when any Bitbucket list or status read last saw a 429 or a near-limit
// header (UnixNano). A refresh that started at or before it reads no statuses.
var bitbucketRateLimitedAt atomic.Int64

func bitbucketNoteRateLimit() { bitbucketRateLimitedAt.Store(time.Now().UnixNano()) }

// bitbucketNextRefresh numbers a refresh when it starts, so cache writes are ordered by start.
func bitbucketNextRefresh() uint64 {
	bitbucketStatusCache.Lock()
	defer bitbucketStatusCache.Unlock()
	bitbucketStatusCache.seq++
	return bitbucketStatusCache.seq
}

// bitbucketConnKey tells two connections apart by what they authenticate with, so another
// account or a replaced token never reads the previous one's answers. The refresh token is the
// stable identity of an OAuth connection; the access token, which rotates, stands in only when
// there is none.
func bitbucketConnKey(s *secrets.Data) string {
	var raw string
	switch {
	case s == nil:
		return ""
	case s.Bitbucket != nil && s.Bitbucket.AccessToken != "":
		id := s.Bitbucket.RefreshToken
		if id == "" {
			id = "access:" + s.Bitbucket.AccessToken
		}
		raw = "oauth:" + id
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
	tried                uint64
	attempted            bool
	checks               workItemChecksOut
	haveChecks           bool
	mergeable            string
	haveMerge            bool
	// what this refresh learned and may keep
	newChecks, newMerge bool
}

// bitbucketEnrichItems fills Checks and Mergeable on the open Bitbucket pull requests among items.
func bitbucketEnrichItems(s *secrets.Data, items []workItemOut, started time.Time, seq uint64) {
	groups := map[string]*bbGroup{}
	order := []*bbGroup{}
	conn, auth := "", ""
	for i := range items {
		r := &items[i]
		if r.Provider != "bitbucket" || r.Kind != "pr" || (r.State != "open" && r.State != "in_progress") ||
			r.srcHash == "" || r.dstHash == "" || r.Repo == "" || r.bbAuth == "" {
			continue
		}
		_, num, ok := strings.Cut(r.Key, "#")
		if !ok || num == "" {
			continue
		}
		if conn == "" {
			conn, auth = bitbucketConnKey(s), r.bbAuth
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

	bitbucketStatusCache.Lock()
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
		g.unsettled, g.tried = e.unsettled, e.tried
	}
	bitbucketStatusCache.Unlock()

	// One job per pull request: when two queries saw it at different commits, the newest listing
	// is read and the other keeps only what the cache already holds for its hashes.
	newest := map[string]*bbGroup{}
	for _, g := range order {
		if !g.needCheck && !g.needMerge {
			continue
		}
		if o := newest[g.cacheKey]; o == nil || g.updated >= o.updated {
			newest[g.cacheKey] = g
		}
	}
	jobs := make([]*bbGroup, 0, len(newest))
	for _, g := range newest {
		jobs = append(jobs, g)
	}
	deadline := started.Add(bitbucketStatusWindow)
	limited := bitbucketRateLimitedAt.Load() >= started.UnixNano()
	if len(jobs) > 0 && !limited && time.Now().Before(deadline) {
		sort.SliceStable(jobs, func(a, b int) bool {
			x, y := jobs[a], jobs[b]
			if x.unsettled != y.unsettled {
				return !x.unsettled
			}
			if x.unsettled && x.tried != y.tried {
				return x.tried < y.tried
			}
			if x.updated != y.updated {
				return x.updated > y.updated
			}
			return x.cacheKey < y.cacheKey
		})
		if len(jobs) > bitbucketStatusBudget {
			jobs = jobs[:bitbucketStatusBudget]
		}
		bitbucketReadAll(auth, jobs, deadline)
	}

	bitbucketStatusCache.Lock()
	for _, g := range jobs {
		if g.attempted {
			bitbucketStoreGroup(g, seq)
		}
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

// bitbucketStoreGroup writes what this refresh learned. A refresh that started before the one that
// last wrote the entry is dropped whole, so a slow older refresh cannot erase a newer answer or
// its retry order. The caller holds the cache lock.
func bitbucketStoreGroup(g *bbGroup, seq uint64) {
	e, existed := bitbucketStatusCache.m[g.cacheKey]
	if existed && seq < e.seq {
		return
	}
	if !existed && len(bitbucketStatusCache.m) >= bitbucketStatusCacheMax {
		for k := range bitbucketStatusCache.m {
			delete(bitbucketStatusCache.m, k)
			break
		}
	}
	if g.newChecks {
		e.checks, e.checksOK, e.checksSrc = g.checks, true, g.srcHash
	}
	if g.newMerge {
		e.mergeable, e.mergeOK, e.mergeSrc, e.mergeDst = g.mergeable, true, g.srcHash, g.dstHash
	}
	e.unsettled, e.tried, e.seq = g.unsettled, seq, seq
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
	g.attempted, g.unsettled = true, false
	if g.needCheck {
		if body, ok := bitbucketGetOnce(ctx, auth, g.prBase+"/statuses?pagelen=50&fields=values.state,next", stop); ok {
			if c, final, ok := parseBitbucketStatusesPage(body); ok {
				g.checks, g.haveChecks = c, true
				g.newChecks = final
				g.unsettled = g.unsettled || !final
			}
		}
	}
	if g.needMerge && !stop.Load() {
		if body, ok := bitbucketGetOnce(ctx, auth, g.prBase+"/mergeability/checks?fields=values.type,values.status,values.reason,values.blocking", stop); ok && bitbucketValidMergeChecks(body) {
			g.mergeable, g.haveMerge = parseBitbucketMergeChecks(body).mergeable, true
			g.newMerge = g.mergeable != "unknown"
			g.unsettled = g.unsettled || !g.newMerge
		}
	}
	if !g.haveChecks || !g.haveMerge {
		g.unsettled = true
	}
}

// bitbucketValidMergeChecks accepts a `values` array of objects that each name a check type and a
// status. The detail parser turns anything it cannot read into "unknown", which a list row must
// not take for a read answer.
func bitbucketValidMergeChecks(body []byte) bool {
	var r struct {
		Values *[]struct {
			Type   *string `json:"type"`
			Status *string `json:"status"`
		} `json:"values"`
	}
	if json.Unmarshal(body, &r) != nil || r.Values == nil {
		return false
	}
	for _, v := range *r.Values {
		if v.Type == nil || v.Status == nil {
			return false
		}
	}
	return true
}

// parseBitbucketStatusesPage folds one page of build statuses. ok is false for a body that is not
// a statuses page, or whose entries have no state. final says the answer may be kept for this
// commit: no build still running and no further page.
func parseBitbucketStatusesPage(body []byte) (c workItemChecksOut, final, ok bool) {
	var p struct {
		Values *[]struct {
			State *string `json:"state"`
		} `json:"values"`
		Next string `json:"next"`
	}
	if json.Unmarshal(body, &p) != nil || p.Values == nil {
		return workItemChecksOut{}, false, false
	}
	for _, v := range *p.Values {
		if v.State == nil || strings.TrimSpace(*v.State) == "" {
			return workItemChecksOut{}, false, false
		}
	}
	c = parseBitbucketStatuses(body)
	if p.Next != "" && c.Failed == 0 {
		// Page one says nothing about the rest, so neither green nor pending can be claimed.
		return workItemChecksOut{}, false, false
	}
	// A failure on page one is a failure whatever follows, but the counts are partial: show, keep nothing.
	return c, c.Pending == 0 && p.Next == "", true
}

// bitbucketGetOnce is one GET with no retry: gitx.BitbucketGetStatusHeader retries a 429 after a
// back-off, which is the opposite of stopping. It raises stop (and the refresh-wide note) on a 429
// or a near-limit header as soon as the headers arrive, before the body, and reports ok only for a
// complete 200.
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
		bitbucketNoteRateLimit()
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
