package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
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
)

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
	mu  sync.Mutex
	m   map[string]jiraProjectsEntry
	now func() time.Time
}

var jiraProjects = &jiraProjectsCache{m: map[string]jiraProjectsEntry{}, now: time.Now}

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
		if fresh, ok := fetchJiraProjects(r.Context(), res); ok {
			jiraProjects.put(mid, fresh)
			writeJSON(w, http.StatusOK, fresh)
			return
		}
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
	if out.Keys == nil {
		out.Keys = []string{}
	}
	return out, true
}
