package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"
)

// The Jira project keys a member can browse, kept so the mirror can link PROJ-12 for a project
// no saved query reaches (docs/log/80 §80.25, ADR 0061 decision 28). Project keys are metadata,
// like the inbox rows; nothing of the credential and no project name is held.
//
// Lifetime, and why these numbers:
//   - fresh for jiraProjectsFresh: project lists change rarely, and a page load asks once per
//     browser, so an hour keeps the Agent (and Jira) out of the loop for most reads.
//   - past that, a running workspace is asked again; a stopped one is NOT started, and keeps
//     answering from the entry for up to jiraProjectsStale. A stale key only ever costs a click
//     that ends on the modal's "af has no details" note, never a wrong ticket.
//   - older than that, or never read: an empty list, i.e. today's behaviour (plain text).
//
// Held in memory, per membership (tenant and member together), so a member can only be handed
// their own projects, and a CP restart just means one more read. Bounded at jiraProjectsMax
// entries.
const (
	jiraProjectsFresh = time.Hour
	jiraProjectsStale = 24 * time.Hour
	jiraProjectsMax   = 2000
	// After a failed read the Agent is left alone this long (per membership): a 404 from an old
	// Agent, a 429 or a 502 would otherwise be amplified by every tab, reload and direct call.
	jiraProjectsBackoff = time.Minute
	// The Agent keeps 500 keys; the CP holds no more than that per member whatever it is sent.
	jiraProjectsKeyCap = 500
)

var jiraProjectKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,9}$`)

type jiraProjectsWire struct {
	Connected bool     `json:"connected"`
	Keys      []string `json:"keys"`
	Truncated bool     `json:"truncated"`
}

type jiraProjectsEntry struct {
	wire jiraProjectsWire
	at   time.Time
}

type jiraProjectsCache struct {
	mu       sync.Mutex
	m        map[string]jiraProjectsEntry
	failed   map[string]time.Time     // last failed Agent read per membership
	inflight map[string]chan struct{} // one Agent read at a time per membership
	now      func() time.Time
}

func newJiraProjectsCache() *jiraProjectsCache {
	return &jiraProjectsCache{m: map[string]jiraProjectsEntry{}, failed: map[string]time.Time{},
		inflight: map[string]chan struct{}{}, now: time.Now}
}

var jiraProjects = newJiraProjectsCache()

// begin claims the Agent read for a membership. The caller that gets lead=true must call finish;
// the others get the channel that closes when the read is over and then read the cache again.
// A membership inside its failure backoff gets neither: skip=true.
func (c *jiraProjectsCache) begin(membership string) (lead, skip bool, wait <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch, ok := c.inflight[membership]; ok {
		return false, false, ch
	}
	if at, ok := c.failed[membership]; ok && c.now().Sub(at) < jiraProjectsBackoff {
		return false, true, nil
	}
	c.inflight[membership] = make(chan struct{})
	return true, false, nil
}

func (c *jiraProjectsCache) finish(membership string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ok {
		delete(c.failed, membership)
	} else {
		if len(c.failed) >= jiraProjectsMax {
			c.failed = map[string]time.Time{} // a backoff lost early costs one extra read
		}
		c.failed[membership] = c.now()
	}
	if ch, found := c.inflight[membership]; found {
		close(ch)
		delete(c.inflight, membership)
	}
}

func (c *jiraProjectsCache) get(membership string) (jiraProjectsEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[membership]
	if ok && c.now().Sub(e.at) > jiraProjectsStale {
		delete(c.m, membership)
		return jiraProjectsEntry{}, false
	}
	return e, ok
}

func (c *jiraProjectsCache) put(membership string, w jiraProjectsWire) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[membership]; !ok && len(c.m) >= jiraProjectsMax {
		for k, e := range c.m {
			if c.now().Sub(e.at) > jiraProjectsStale {
				delete(c.m, k)
			}
		}
		for k := range c.m {
			if len(c.m) < jiraProjectsMax {
				break
			}
			delete(c.m, k)
		}
	}
	c.m[membership] = jiraProjectsEntry{wire: w, at: c.now()}
}

// jiraProjectsList — GET /api/work-items/jira-projects. Always 200 with a (possibly empty) list:
// the caller is a renderer deciding whether to draw links, and every failure means "none".
func (a workItemsAPI) jiraProjectsList(w http.ResponseWriter, r *http.Request, res *resolved) {
	mid := res.mv.MembershipID
	cached, have := jiraProjects.get(mid)
	if have && jiraProjects.now().Sub(cached.at) <= jiraProjectsFresh {
		writeJSON(w, http.StatusOK, cached.wire)
		return
	}
	if res.rt.State(r.Context()) == "running" {
		lead, skip, wait := jiraProjects.begin(mid)
		switch {
		case lead:
			fresh, ok := fetchJiraProjects(r.Context(), res)
			if ok {
				jiraProjects.put(mid, fresh)
			}
			jiraProjects.finish(mid, ok)
			if ok {
				writeJSON(w, http.StatusOK, fresh)
				return
			}
		case !skip:
			select {
			case <-wait:
			case <-r.Context().Done():
			}
		}
		// Not the reader (or the read failed): whatever the cache holds now, which the reader may
		// just have filled.
		cached, have = jiraProjects.get(mid)
	}
	if have {
		writeJSON(w, http.StatusOK, cached.wire)
		return
	}
	writeJSON(w, http.StatusOK, jiraProjectsWire{Keys: []string{}})
}

func fetchJiraProjects(ctx context.Context, res *resolved) (jiraProjectsWire, bool) {
	var none jiraProjectsWire
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, res.rt.Endpoint()+"/work-items/jira-projects", bytes.NewReader([]byte("{}")))
	if err != nil {
		return none, false
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := res.rt.Token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		return none, false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out jiraProjectsWire
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &out) != nil {
		return none, false
	}
	if len(out.Keys) > jiraProjectsKeyCap {
		return none, false
	}
	for _, k := range out.Keys {
		if !jiraProjectKeyRe.MatchString(k) {
			return none, false
		}
	}
	if out.Keys == nil {
		out.Keys = []string{}
	}
	return out, true
}
