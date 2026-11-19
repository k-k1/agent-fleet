package sessionx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchpr"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// POST /memos/pr-snapshot: the PRs of a queue delivery's session and their states, for the Control
// Plane's step 2 of "a prerequisite is satisfied" (ADR 0137, addendum 2026-11-19, part A).
//
// The dispatcher asks it after the delivery closed, and again until the snapshot is final. The
// first read names the session: the PRs of the branch its working copy is on and of the branch it
// started on, and the PR links it holds as started, created or working. Every later read names the PRs the first one found and no
// session, so the session may be archived or deleted in between.
//
// The answer is only ever made of provider answers given after `since` (the delivery's close):
// branchpr.Cache.FetchSince asks again for anything cached before it, so a "no PR" cached just
// before the close never answers "no PR". Anything the Agent cannot answer for certain is an
// error, never an empty list: the Control Plane reads an empty list as "this session opened no PR".

// prSnapshotHeader marks an answer of this handler, so a 404 of a route an older Agent lacks, or
// of a proxy, is never read as "no such session".
const prSnapshotHeader = "X-AF-PR-Snapshot"

// prSnapshotMax bounds the PRs one read asks about: below one provider batch, and far above what
// one delivery's session opens.
const prSnapshotMax = 30

// Error codes of the route. The Control Plane acts on prSnapshotNoSession alone; every other
// answer that is not a 200 keeps the dependent waiting.
const (
	prSnapshotBadRequest  = "bad_pr_snapshot_request"
	prSnapshotNoSession   = "pr_snapshot_no_session"
	prSnapshotUnanswered  = "pr_snapshot_unanswered"
	prSnapshotUnreadable  = "pr_snapshot_unreadable"
	prSnapshotTooMany     = "pr_snapshot_too_many"
	prSnapshotCallTimeout = 15 * time.Second
)

// PRSnapshotKey names one PR: provider "github" or "bitbucket", key "owner/repo#N".
type PRSnapshotKey struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
}

// PRSnapshotRequest is the route's body. Since is the delivery's close, RFC 3339.
type PRSnapshotRequest struct {
	Session string          `json:"session,omitempty"`
	Since   string          `json:"since"`
	PRs     []PRSnapshotKey `json:"prs,omitempty"`
}

// PRSnapshotPR is one PR of the answer. State is "open", "draft", "merged" or "closed", and ""
// when the provider answered but could not show the PR (it reads as unknown, never as merged).
type PRSnapshotPR struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
	State    string `json:"state"`
	URL      string `json:"url,omitempty"`
}

// PRSnapshotResponse is the 200. PRs is never null: [] is "no PR", read after Since.
type PRSnapshotResponse struct {
	PRs    []PRSnapshotPR `json:"prs"`
	ReadAt string         `json:"readAt"`
}

// prSnapshotFetch asks one provider, with since as the lower bound of every answer. Replaced in
// tests.
var prSnapshotFetch = func(ctx context.Context, provider string, keys []branchpr.Key, since time.Time) (map[branchpr.Key]*branchpr.PR, error) {
	switch provider {
	case "github":
		return sessionPRs.FetchSince(ctx, keys, since)
	case "bitbucket":
		return sessionBBPRs.FetchSince(ctx, keys, since)
	}
	return nil, errors.New("unknown provider")
}

// prSnapshotAsk is one key the read asks about: a PR by number (link set), or the branch.
type prSnapshotAsk struct {
	provider string
	link     string // "owner/repo#N" for a PR asked by number, "" for the branch
	key      branchpr.Key
}

// HandleQueuePRSnapshot is POST /memos/pr-snapshot.
func HandleQueuePRSnapshot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(prSnapshotHeader, "1")
	var req PRSnapshotRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, prSnapshotBadRequest, "invalid JSON body")
		return
	}
	since, err := time.Parse(time.RFC3339Nano, req.Since)
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, prSnapshotBadRequest, "since must be an RFC 3339 time")
		return
	}
	if len(req.PRs) > prSnapshotMax {
		httpx.WriteErr(w, http.StatusBadRequest, prSnapshotTooMany, "too many pull requests")
		return
	}
	var asks []prSnapshotAsk
	var meta session.Meta
	if len(req.PRs) > 0 {
		for _, p := range req.PRs {
			k, ok := prKeyOfLink(p.Key)
			if (p.Provider != "github" && p.Provider != "bitbucket") || !ok {
				httpx.WriteErr(w, http.StatusBadRequest, prSnapshotBadRequest, "a pull request is not provider and owner/repo#N")
				return
			}
			asks = append(asks, prSnapshotAsk{provider: p.Provider, link: p.Key, key: k})
		}
	} else {
		if req.Session == "" {
			httpx.WriteErr(w, http.StatusBadRequest, prSnapshotBadRequest, "a first read names the session")
			return
		}
		m, st := session.ReadMetaState(req.Session)
		switch st {
		case session.MetaAbsent:
			// The one answer that ends the wait: this session can never give a snapshot.
			httpx.WriteErr(w, http.StatusNotFound, prSnapshotNoSession, "no such session")
			return
		case session.MetaUnreadable:
			httpx.WriteErr(w, http.StatusServiceUnavailable, prSnapshotUnreadable, "the session's meta could not be read")
			return
		}
		meta = m
		var ok bool
		if asks, ok = prSnapshotSessionAsks(m); !ok {
			httpx.WriteErr(w, http.StatusServiceUnavailable, prSnapshotUnreadable, "the session's working copy could not be read")
			return
		}
		if len(asks) > prSnapshotMax {
			httpx.WriteErr(w, http.StatusServiceUnavailable, prSnapshotTooMany, "the session holds too many pull requests")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), prSnapshotCallTimeout)
	defer cancel()
	prs, ok := prSnapshotRead(ctx, asks, since, meta, len(req.PRs) == 0)
	if !ok {
		httpx.WriteErr(w, http.StatusServiceUnavailable, prSnapshotUnanswered, "the provider did not answer for every pull request")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, PRSnapshotResponse{PRs: prs, ReadAt: time.Now().UTC().Format(time.RFC3339Nano)})
}

// prSnapshotSessionAsks is what a first read asks about: the branches of the session's working
// copy and its held PR links. ok is false when the working copy exists in the meta but cannot be
// read: its branch PR is then unknown, which must not read as none.
//
// Both the branch the copy is on now and the branch the session started on are asked: a session
// that opened its PR and then checked out another branch, or detached HEAD, still has that PR. A
// default branch asked this way has no PR, as the provider ignores it.
func prSnapshotSessionAsks(m session.Meta) ([]prSnapshotAsk, bool) {
	var asks []prSnapshotAsk
	if m.Dir != "" {
		if _, err := os.Stat(m.Dir); err != nil {
			return nil, false
		}
		o, ok := prSnapshotOrigin(m.Dir)
		if !ok {
			return nil, false
		}
		// A copy that is not on github.com or bitbucket.org has no PR this Agent can read.
		if o.repo != "" {
			cur, _ := gitx.GitDirInfo(m.Dir)
			seen := map[string]bool{}
			for _, b := range []string{cur, m.Branch} {
				if b == "" || b == "(detached)" || seen[b] {
					continue
				}
				seen[b] = true
				asks = append(asks, prSnapshotAsk{provider: o.provider, key: branchpr.Key{Repo: o.repo, Branch: b}})
			}
		}
	}
	seen := map[string]bool{}
	for _, l := range session.LinksOf(m) {
		if (l.Provider != "github" && l.Provider != "bitbucket") || !prFeedbackOwns(l.Role) || !prLinkIsPR(l) {
			continue
		}
		k, ok := prKeyOfLink(l.Key)
		id := l.Provider + ":" + strings.ToLower(l.Key)
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		asks = append(asks, prSnapshotAsk{provider: l.Provider, link: l.Key, key: k})
	}
	return asks, true
}

// prSnapshotOrigin is the working copy's repository on github.com or bitbucket.org, zero for a
// copy that is not a git working tree, has no origin, or has one on another host. ok is false
// when the copy is a git working tree and its origin could not be read: the session list's
// cached origin (originOf) cannot tell that from "no origin", and a snapshot must. Replaced in
// tests.
var prSnapshotOrigin = func(dir string) (originRepo, bool) {
	if origin, ok := gitx.GitOriginURL(dir); ok {
		if r := gitx.GitHubRepoOf(origin); r != "" {
			return originRepo{"github", r}, true
		}
		if r := gitx.BitbucketRepoOf(origin); r != "" {
			return originRepo{"bitbucket", r}, true
		}
		return originRepo{}, true
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); errors.Is(err, fs.ErrNotExist) {
		return originRepo{}, true // not a working tree
	}
	out, err := gitx.Run(dir, "remote")
	if err != nil {
		return originRepo{}, false
	}
	for _, name := range strings.Fields(out) {
		if name == "origin" {
			return originRepo{}, false // it has one, and reading it failed
		}
	}
	return originRepo{}, true
}

// prSnapshotRead asks every key, per provider, and builds the answer. ok is false when any key
// went unanswered: a partial set would read as fewer PRs than the session has.
func prSnapshotRead(ctx context.Context, asks []prSnapshotAsk, since time.Time, m session.Meta, first bool) ([]PRSnapshotPR, bool) {
	byProvider := map[string][]branchpr.Key{}
	for _, a := range asks {
		byProvider[a.provider] = append(byProvider[a.provider], a.key)
	}
	got := map[string]map[branchpr.Key]*branchpr.PR{}
	for provider, keys := range byProvider {
		ans, err := prSnapshotFetch(ctx, provider, keys, since)
		if err != nil {
			return nil, false
		}
		got[provider] = ans
	}
	out := []PRSnapshotPR{}
	seen := map[string]bool{}
	add := func(p PRSnapshotPR) {
		id := p.Provider + ":" + strings.ToLower(p.Key)
		if seen[id] {
			return
		}
		seen[id] = true
		out = append(out, p)
	}
	for _, a := range asks {
		pr, answered := got[a.provider][a.key]
		if !answered {
			return nil, false
		}
		if a.link == "" {
			// The branch: no PR is a real "none", read after since.
			if pr == nil || pr.Number <= 0 {
				continue
			}
			p, k, ok := prLinkKey(pr)
			if !ok || p != a.provider {
				return nil, false // a PR this Agent cannot name is not "no PR"
			}
			if first && session.IsUnlinked(m, p, k) {
				continue // a PR the member unlinked by hand is no longer the session's
			}
			add(PRSnapshotPR{Provider: p, Key: k, State: prLinkState(pr), URL: linkURL(pr.URL)})
			continue
		}
		snap := PRSnapshotPR{Provider: a.provider, Key: a.link}
		if pr != nil && pr.Number == a.key.Number {
			snap.State, snap.URL = prLinkState(pr), linkURL(pr.URL)
		}
		add(snap)
	}
	return out, true
}
