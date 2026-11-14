package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Message queues, step 1a (ADR 0137, #4156). Every rule runs on SQLite and, with
// AF_TEST_DATABASE_URL, on Postgres, through the three faces: the Console (/api/memos*), the
// token face (/internal/memos*) and /mcp (cpDeps, the CP side of the /mcp memo tools).

// memoQueueEnvs is an inbox test env per dialect.
func memoQueueEnvs(t *testing.T) map[string]*inboxEnv {
	t.Helper()
	lite, err := openMigratedSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lite.Close() })
	out := map[string]*inboxEnv{"sqlite": newInboxEnvOn(t, lite)}
	if url, ok := pgtest.Schema(t); ok {
		pg, err := store.OpenPostgres(url)
		if err != nil {
			t.Fatalf("open postgres: %v", err)
		}
		t.Cleanup(func() { pg.Close() })
		out["postgres"] = newInboxEnvOn(t, pg)
	}
	for _, e := range out {
		e.memoQueuesOn(true)
	}
	return out
}

// memoQueuesOn sets the tenant switch of members a, b and c (tenant T1).
func (e *inboxEnv) memoQueuesOn(on bool) {
	e.t.Helper()
	if err := e.st.SetTenantMemoQueues(context.Background(), e.t1.ID, on); err != nil {
		e.t.Fatal(err)
	}
}

// eachDialect runs f per dialect. The tests that use it are not parallel: dropping several
// test schemas at once runs a local Postgres out of lock memory (SQLSTATE 53200).
func eachDialect(t *testing.T, f func(t *testing.T, e *inboxEnv)) {
	for name, e := range memoQueueEnvs(t) {
		t.Run(name, func(t *testing.T) {
			e.t = t
			f(t, e)
		})
	}
}

// memoFaceClient drives one face as member "a". Each call answers an HTTP-like status (the
// /mcp face answers 200 or 409/400 read from its error, which is all a tool caller sees).
type memoFaceClient struct {
	name string
	e    *inboxEnv
}

func faces(e *inboxEnv) []memoFaceClient {
	return []memoFaceClient{{"console", e}, {"token", e}, {"mcp", e}}
}

func workspaceFaces(e *inboxEnv) []memoFaceClient {
	return []memoFaceClient{{"token", e}, {"mcp", e}}
}

func (c memoFaceClient) http(method, path string, body any) *httptest.ResponseRecorder {
	if c.name == "console" {
		return c.e.do("a", method, "/api"+path, body)
	}
	return c.e.memoTokenDo("a", method, "/internal"+path, body)
}

func (c memoFaceClient) mv() store.MembershipView {
	return store.MembershipView{MembershipID: c.e.members["a"]}
}

// mcpStatus maps a /mcp tool error onto the status the HTTP faces answer, by message.
func mcpStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	return http.StatusConflict
}

func (c memoFaceClient) create(body map[string]any) (int, memoDTO) {
	c.e.t.Helper()
	if c.name == "mcp" {
		s := func(k string) string { v, _ := body[k].(string); return v }
		out, err := cpDeps{c.e.mgr}.MemoCreate(context.Background(), c.mv(), "conn-1", s("repo"), s("category"), s("kind"), s("body"), s("refPath"))
		if err != nil {
			return mcpStatus(err), memoDTO{}
		}
		return http.StatusCreated, out.(memoDTO)
	}
	rec := c.http("POST", "/memos", body)
	var d memoDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	return rec.Code, d
}

func (c memoFaceClient) update(id string, patch map[string]any) (int, memoDTO) {
	c.e.t.Helper()
	if c.name == "mcp" {
		sp := func(k string) *string {
			if v, ok := patch[k].(string); ok {
				return &v
			}
			return nil
		}
		var pos *int
		if v, ok := patch["position"].(int); ok {
			pos = &v
		}
		out, err := cpDeps{c.e.mgr}.MemoUpdate(context.Background(), c.e.members["a"], id, sp("repo"), sp("category"), sp("body"), sp("refPath"), pos)
		if err != nil {
			return mcpStatus(err), memoDTO{}
		}
		return http.StatusOK, out.(memoDTO)
	}
	rec := c.http("PATCH", "/memos/"+id, patch)
	var d memoDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	return rec.Code, d
}

func (c memoFaceClient) del(id string) int {
	if c.name == "mcp" {
		return mcpStatus(cpDeps{c.e.mgr}.MemoDelete(context.Background(), c.e.members["a"], id))
	}
	return c.http("DELETE", "/memos/"+id, nil).Code
}

func (c memoFaceClient) flush(ids ...string) int {
	if c.name == "mcp" {
		_, err := cpDeps{c.e.mgr}.MemoFlush(context.Background(), c.e.mgr.rts[c.e.members["a"]].rt, c.e.members["a"], "s1", ids)
		return mcpStatus(err)
	}
	return c.http("POST", "/memos/flush", map[string]any{"sessionName": "s1", "ids": ids}).Code
}

// row reads a memo as stored.
func (e *inboxEnv) memoRow(id string) store.Memo {
	e.t.Helper()
	m, ok, err := e.st.GetMemo(context.Background(), id)
	if err != nil || !ok {
		e.t.Fatalf("memo %s: ok=%v err=%v", id, ok, err)
	}
	return m
}

// assertPendingReleaseMatches is the invariant: a pending message's release_hash is its
// current content hash. It is checked after every write path below.
func (e *inboxEnv) assertPendingReleaseMatches() {
	e.t.Helper()
	rows, err := e.st.ListMemos(context.Background(), e.members["a"], "")
	if err != nil {
		e.t.Fatal(err)
	}
	for _, m := range rows {
		if m.State == store.MemoStatePending && m.ReleaseHash != memoContentHash(m) {
			e.t.Fatalf("pending memo %s (%s/%s %q) has release_hash %q, content hash %q", m.ID, m.Repo, m.Category, m.Body, m.ReleaseHash, memoContentHash(m))
		}
	}
}

// category creates a category for "a" through the Console and answers its id.
func (e *inboxEnv) category(repo, name string) string {
	e.t.Helper()
	rec := e.do("a", "POST", "/api/memo-categories", map[string]any{"repo": repo, "name": name})
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		e.t.Fatalf("category: %d %s", rec.Code, rec.Body.String())
	}
	return decode[memoCategoryDTO](e.t, rec).ID
}

// queue makes a category a queue through the Console.
func (e *inboxEnv) queue(categoryID string) memoQueueDTO {
	e.t.Helper()
	rec := e.do("a", "PUT", "/api/memo-queues/"+categoryID, map[string]any{"instruction": "implement it and open a PR"})
	if rec.Code != http.StatusOK {
		e.t.Fatalf("put queue: %d %s", rec.Code, rec.Body.String())
	}
	return decode[memoQueueDTO](e.t, rec)
}

func (e *inboxEnv) release(id, hash string) *httptest.ResponseRecorder {
	return e.do("a", "POST", "/api/memos/release", map[string]any{"items": []map[string]any{{"id": id, "contentHash": hash}}})
}

func (e *inboxEnv) setState(id, state string) {
	e.t.Helper()
	if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET state=? WHERE id=?`, state, id); err != nil {
		e.t.Fatal(err)
	}
}

func textMemo(repo, category, body string) map[string]any {
	return map[string]any{"kind": "text", "repo": repo, "category": category, "body": body}
}

// --- Settings ------------------------------------------------------------------------------

func TestMemoQueueSettings(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		if r := e.do("a", "GET", "/api/memo-queues", nil); r.Code != 200 || strings.TrimSpace(r.Body.String()) != `{"tenantEnabled":true,"queues":[]}` {
			t.Fatalf("empty list: %d %s", r.Code, r.Body.String())
		}
		cat := e.category("repo-a", "issues")
		put := func(body map[string]any) *httptest.ResponseRecorder {
			return e.do("a", "PUT", "/api/memo-queues/"+cat, body)
		}
		if r := e.do("a", "PUT", "/api/memo-queues/nope", map[string]any{}); r.Code != 404 {
			t.Fatalf("unknown category: %d", r.Code)
		}
		// Another member's category is not found either.
		other := decode[memoCategoryDTO](t, e.do("c", "POST", "/api/memo-categories", map[string]any{"repo": "r", "name": "x"})).ID
		if r := e.do("a", "PUT", "/api/memo-queues/"+other, map[string]any{}); r.Code != 404 {
			t.Fatalf("foreign category: %d", r.Code)
		}
		for _, c := range []struct {
			body map[string]any
			code string
		}{
			{map[string]any{"agentKind": "shell"}, errCodeMemoQueueBadSettings},
			{map[string]any{"agentKind": "ssm"}, errCodeMemoQueueBadSettings},
			{map[string]any{"agentKind": "nope"}, errCodeMemoQueueBadSettings},
			{map[string]any{"maxActive": 0}, errCodeMemoQueueBadSettings},
			{map[string]any{"maxActive": 7}, errCodeMemoQueueBadSettings},
			{map[string]any{"batch": 21}, errCodeMemoQueueBadSettings},
			{map[string]any{"spendCapUsd": -1}, errCodeMemoQueueBadSettings},
			{map[string]any{"enabled": true}, errCodeMemoQueueNoInstruction},
			{map[string]any{"enabled": true, "instruction": "   "}, errCodeMemoQueueNoInstruction},
		} {
			r := put(c.body)
			if r.Code != 400 || decode[errResp](t, r).Error.Code != c.code {
				t.Errorf("PUT %v: %d %s, want 400 %s", c.body, r.Code, r.Body.String(), c.code)
			}
		}
		if e.count("memo_queue") != 0 {
			t.Fatal("a refused PUT wrote settings")
		}
		r := put(map[string]any{"instruction": "do it"})
		got := decode[memoQueueDTO](t, r)
		if r.Code != 200 || got.AgentKind != "claude" || got.MaxActive != 2 || got.Batch != 1 || !got.Worktree || got.Enabled ||
			got.Repo != "repo-a" || got.Category != "issues" || got.Generation != 0 {
			t.Fatalf("defaults: %d %+v", r.Code, got)
		}
		r = put(map[string]any{"instruction": "do it", "enabled": true, "maxActive": 6, "batch": 20, "worktree": false,
			"agentKind": "codex", "spendCapUsd": 5})
		got = decode[memoQueueDTO](t, r)
		if r.Code != 200 || !got.Enabled || got.MaxActive != 6 || got.Batch != 20 || got.Worktree || got.AgentKind != "codex" || got.SpendCapUSD != 5 {
			t.Fatalf("update: %d %s", r.Code, r.Body.String())
		}
		list := decode[memoQueuesWire](t, e.do("a", "GET", "/api/memo-queues", nil)).Queues
		if len(list) != 1 || list[0].CategoryID != cat || !list[0].Enabled {
			t.Fatalf("list: %+v", list)
		}
		if l := decode[memoQueuesWire](t, e.do("c", "GET", "/api/memo-queues", nil)).Queues; len(l) != 0 {
			t.Fatalf("another member sees %d queue(s)", len(l))
		}
		// The category DTO marks the queue on the Console and on the token face.
		for _, rec := range []*httptest.ResponseRecorder{e.do("a", "GET", "/api/memo-categories", nil), e.memoTokenDo("a", "GET", "/internal/memo-categories", nil)} {
			cats := decode[[]memoCategoryDTO](t, rec)
			if len(cats) != 1 || !cats[0].Queue {
				t.Fatalf("categories: %s", rec.Body.String())
			}
		}
		if r := e.do("a", "DELETE", "/api/memo-queues/"+cat, nil); r.Code != 204 {
			t.Fatalf("delete: %d", r.Code)
		}
		if r := e.do("a", "DELETE", "/api/memo-queues/"+cat, nil); r.Code != 204 {
			t.Fatalf("delete again: %d", r.Code)
		}
		if cats := decode[[]memoCategoryDTO](t, e.do("a", "GET", "/api/memo-categories", nil)); cats[0].Queue {
			t.Fatal("still a queue after delete")
		}
	})
}

// Settings and release have no route on the token face: whatever the workspace sends there,
// nothing becomes a queue and nothing is released.
func TestMemoQueueTokenFaceHasNoSettingsOrRelease(t *testing.T) {
	t.Parallel()
	e := newInboxEnv(t)
	e.memoQueuesOn(true)
	cat := e.category("r", "q")
	e.queue(cat)
	_, m := memoFaceClient{"token", e}.create(textMemo("r", "q", "from a session"))
	for _, c := range []struct{ method, path string }{
		{"GET", "/internal/memo-queues"},
		{"PUT", "/internal/memo-queues/" + cat},
		{"DELETE", "/internal/memo-queues/" + cat},
		{"POST", "/internal/memos/release"},
	} {
		body := map[string]any{"items": []map[string]any{{"id": m.ID, "contentHash": m.ContentHash}}, "instruction": "x"}
		if r := e.memoTokenDo("a", c.method, c.path, body); r.Code < 400 {
			t.Errorf("%s %s answered %d", c.method, c.path, r.Code)
		}
	}
	if got := e.memoRow(m.ID); got.State != store.MemoStateHeld {
		t.Fatalf("state after the token face tried: %q", got.State)
	}
	if e.count("memo_queue") != 1 {
		t.Fatal("settings changed")
	}
	// Positive control: the same request on the Console releases it.
	if r := e.release(m.ID, m.ContentHash); r.Code != 200 {
		t.Fatalf("console release: %d %s", r.Code, r.Body.String())
	}
}

// --- Provenance and entering state ---------------------------------------------------------

func TestMemoQueueProvenanceAndEnteringState(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("repo-a", "q"))
		e.category("repo-a", "plain")
		wantProv := map[string]string{"console": "member", "token": "workspace", "mcp": "mcp"}
		wantQueued := map[string]string{"console": "pending", "token": "held", "mcp": "held"}
		for _, f := range faces(e) {
			forged := map[string]any{"kind": "text", "body": "note via " + f.name, "repo": "repo-a", "category": "plain",
				"state": "pending", "provenance": "member", "releaseHash": "x", "releasedAt": "2020-01-01T00:00:00Z",
				"contentHash": "x", "producer": "sess-1", "producerOrigin": "user"}
			code, d := f.create(forged)
			if code != http.StatusCreated {
				t.Fatalf("%s plain create: %d", f.name, code)
			}
			row := e.memoRow(d.ID)
			if row.State != "" || row.Provenance != wantProv[f.name] || row.ReleaseHash != "" || row.ReleasedAt != "" || d.ContentHash != memoContentHash(row) {
				t.Fatalf("%s plain note: %+v", f.name, row)
			}
			wantProducer := map[string]string{"console": "", "token": "sess-1", "mcp": "conn-1"}[f.name]
			if row.Producer != wantProducer {
				t.Errorf("%s producer = %q, want %q", f.name, row.Producer, wantProducer)
			}
			// The unfiled bucket and a category with no settings behave as today.
			if _, u := f.create(textMemo("repo-a", "", "unfiled via "+f.name)); e.memoRow(u.ID).State != "" {
				t.Fatalf("%s unfiled memo has a state", f.name)
			}
			forged["category"], forged["body"] = "q", "queued via "+f.name
			code, d = f.create(forged)
			if code != http.StatusCreated {
				t.Fatalf("%s queue create: %d", f.name, code)
			}
			row = e.memoRow(d.ID)
			if row.State != wantQueued[f.name] || row.Provenance != wantProv[f.name] {
				t.Fatalf("%s queued: %+v", f.name, row)
			}
			if f.name == "console" && (row.ReleaseHash != memoContentHash(row) || row.ReleasedAt == "") {
				t.Fatalf("console create is not released: %+v", row)
			}
			if f.name != "console" && (row.ReleaseHash != "" || row.ReleasedAt != "") {
				t.Fatalf("%s create carries a release: %+v", f.name, row)
			}
		}
		e.assertPendingReleaseMatches()
	})
}

// --- Release -------------------------------------------------------------------------------

func TestMemoQueueReleaseBoundToContentHash(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		for _, f := range workspaceFaces(e) {
			_, m := f.create(textMemo("r", "q", "work from "+f.name))
			if m.State != store.MemoStateHeld {
				t.Fatalf("%s: %+v", f.name, m)
			}
			// A stale hash is refused with 409 and the message stays held.
			r := e.release(m.ID, strings.Repeat("0", 64))
			if r.Code != http.StatusConflict || decode[errResp](t, r).Error.Code != errCodeMemoReleaseStale || !strings.Contains(r.Body.String(), m.ID) {
				t.Fatalf("%s stale release: %d %s", f.name, r.Code, r.Body.String())
			}
			if got := e.memoRow(m.ID); got.State != store.MemoStateHeld || got.ReleaseHash != "" {
				t.Fatalf("%s after stale release: %+v", f.name, got)
			}
			// The hash the card showed releases it.
			r = e.release(m.ID, m.ContentHash)
			if r.Code != 200 {
				t.Fatalf("%s release: %d %s", f.name, r.Code, r.Body.String())
			}
			got := e.memoRow(m.ID)
			if got.State != store.MemoStatePending || got.ReleaseHash != m.ContentHash || got.ReleasedAt == "" {
				t.Fatalf("%s released: %+v", f.name, got)
			}
			// Released twice with the same hash: still pending, nothing changes.
			if r := e.release(m.ID, m.ContentHash); r.Code != 200 || e.memoRow(m.ID) != got {
				t.Fatalf("%s second release: %d %s", f.name, r.Code, r.Body.String())
			}
			// A workspace edit of a hashed field puts it back to held and clears the release...
			code, d := f.update(m.ID, map[string]any{"body": "rewritten by " + f.name})
			if code != 200 {
				t.Fatalf("%s update: %d", f.name, code)
			}
			if got := e.memoRow(m.ID); got.State != store.MemoStateHeld || got.ReleaseHash != "" || got.ReleasedAt != "" {
				t.Fatalf("%s edit of a pending message: %+v", f.name, got)
			}
			// ...so the hash released before no longer releases it.
			if r := e.release(m.ID, m.ContentHash); r.Code != http.StatusConflict {
				t.Fatalf("%s old hash after the edit: %d", f.name, r.Code)
			}
			if r := e.release(m.ID, d.ContentHash); r.Code != 200 {
				t.Fatalf("%s new hash: %d %s", f.name, r.Code, r.Body.String())
			}
			// A workspace edit that changes no hashed field (a reorder) keeps the release.
			if code, _ := f.update(m.ID, map[string]any{"position": 9}); code != 200 || e.memoRow(m.ID).State != store.MemoStatePending {
				t.Fatalf("%s reorder: %d %+v", f.name, code, e.memoRow(m.ID))
			}
			// The Console's own edit keeps it pending and records the new hash.
			code, d = memoFaceClient{"console", e}.update(m.ID, map[string]any{"body": "member fixed a typo"})
			if got := e.memoRow(m.ID); code != 200 || got.State != store.MemoStatePending || got.ReleaseHash != d.ContentHash {
				t.Fatalf("%s then console edit: %d %+v", f.name, code, got)
			}
			e.assertPendingReleaseMatches()
		}
		// All or none: one stale item keeps the others held too.
		tok := memoFaceClient{"token", e}
		_, a := tok.create(textMemo("r", "q", "one"))
		_, b := tok.create(textMemo("r", "q", "two"))
		r := e.do("a", "POST", "/api/memos/release", map[string]any{"items": []map[string]any{
			{"id": a.ID, "contentHash": a.ContentHash}, {"id": b.ID, "contentHash": "stale"}}})
		if r.Code != http.StatusConflict || e.memoRow(a.ID).State != store.MemoStateHeld {
			t.Fatalf("mixed release: %d %s, a=%q", r.Code, r.Body.String(), e.memoRow(a.ID).State)
		}
		// A plain note is not released, and another member's message is not found.
		_, plain := tok.create(textMemo("r", "", "plain"))
		if r := e.release(plain.ID, plain.ContentHash); r.Code != http.StatusConflict {
			t.Fatalf("plain note release: %d", r.Code)
		}
		if r := e.do("c", "POST", "/api/memos/release", map[string]any{"items": []map[string]any{{"id": a.ID, "contentHash": a.ContentHash}}}); r.Code != 404 {
			t.Fatalf("foreign release: %d", r.Code)
		}
		e.assertPendingReleaseMatches()
	})
}

// A workspace edit racing the member's release can never leave a pending message whose
// release does not match its content: the release is one conditional write on the row. Both
// requests must answer as one of the two orders allows, and the edit always lands.
func TestMemoQueueReleaseRacesAnEdit(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		tok := memoFaceClient{"token", e}
		for i := 0; i < 15; i++ {
			_, m := tok.create(textMemo("r", "q", fmt.Sprintf("race %d", i)))
			var wg sync.WaitGroup
			var rel, edit *httptest.ResponseRecorder
			wg.Add(2)
			go func() { defer wg.Done(); rel = e.release(m.ID, m.ContentHash) }()
			go func() {
				defer wg.Done()
				edit = e.memoTokenDo("a", "PATCH", "/internal/memos/"+m.ID, map[string]any{"body": fmt.Sprintf("edited %d", i)})
			}()
			wg.Wait()
			if edit.Code != http.StatusOK {
				t.Fatalf("edit %d: %d %s", i, edit.Code, edit.Body.String())
			}
			// Release first: 200, then the edit puts it back to held. Edit first: the hash
			// is stale, 409 memo_release_stale. Either way the row is held with the edit.
			if rel.Code != http.StatusOK && !(rel.Code == http.StatusConflict && decode[errResp](t, rel).Error.Code == errCodeMemoReleaseStale) {
				t.Fatalf("release %d: %d %s", i, rel.Code, rel.Body.String())
			}
			got := e.memoRow(m.ID)
			if got.Body != fmt.Sprintf("edited %d", i) || got.State != store.MemoStateHeld || got.ReleaseHash != "" {
				t.Fatalf("after the race %d: %+v", i, got)
			}
		}
		e.assertPendingReleaseMatches()
	})
}

// --- What the token face and /mcp may not do -----------------------------------------------

func TestMemoQueueWorkspaceFacesCannotFlushDeleteOrRenameAQueue(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		qcat := e.category("r", "q")
		e.queue(qcat)
		pcat := e.category("r", "plain")
		console := memoFaceClient{"console", e}
		agent := newCapturingAgent(t)
		e.workspaceFor("a", agent)
		for _, f := range workspaceFaces(e) {
			_, m := console.create(textMemo("r", "q", "queued for "+f.name))
			sent := len(agent.prompts)
			if code := f.del(m.ID); code != http.StatusConflict {
				t.Fatalf("%s delete of a queue message: %d", f.name, code)
			}
			if code := f.flush(m.ID); code != http.StatusConflict {
				t.Fatalf("%s flush of a queue message: %d", f.name, code)
			}
			if got := e.memoRow(m.ID); got.SentAt != "" || got.State != store.MemoStatePending {
				t.Fatalf("%s: the refused calls changed the row: %+v", f.name, got)
			}
			if len(agent.prompts) != sent {
				t.Fatalf("%s: a refused flush reached the session: %q", f.name, agent.prompts)
			}
			// Positive control: the same calls on a plain note go through, as today.
			_, n := console.create(textMemo("r", "plain", "plain for "+f.name))
			if code := f.flush(n.ID); code != http.StatusOK {
				t.Fatalf("%s flush of a plain note: %d", f.name, code)
			}
			if code := f.del(n.ID); code != http.StatusOK && code != http.StatusNoContent {
				t.Fatalf("%s delete of a plain note: %d", f.name, code)
			}
		}
		// Category rename, merge onto and delete of a queue, on the token face. (/mcp has no
		// category tool.)
		for _, c := range []struct {
			method, id string
			body       map[string]any
		}{
			{"PATCH", qcat, map[string]any{"name": "renamed"}},
			{"PATCH", pcat, map[string]any{"name": "q"}},
			{"DELETE", qcat, nil},
		} {
			r := e.memoTokenDo("a", c.method, "/internal/memo-categories/"+c.id, c.body)
			if r.Code != http.StatusConflict || decode[errResp](t, r).Error.Code != errCodeMemoQueueCategory {
				t.Fatalf("token %s %s %v: %d %s", c.method, c.id, c.body, r.Code, r.Body.String())
			}
		}
		if e.count("memo_category") != 2 || e.count("memo_queue") != 1 {
			t.Fatal("a refused category call changed the categories")
		}
		// A reorder of a queue is not a rename.
		if r := e.memoTokenDo("a", "PATCH", "/internal/memo-categories/"+qcat, map[string]any{"position": 5}); r.Code != 200 {
			t.Fatalf("token reorder of a queue: %d %s", r.Code, r.Body.String())
		}
		// Positive control: a plain category is still renamed and deleted by the token face.
		if r := e.memoTokenDo("a", "PATCH", "/internal/memo-categories/"+pcat, map[string]any{"name": "plain2"}); r.Code != 200 {
			t.Fatalf("token rename of a plain category: %d", r.Code)
		}
		if r := e.memoTokenDo("a", "DELETE", "/internal/memo-categories/"+pcat, nil); r.Code != 204 {
			t.Fatalf("token delete of a plain category: %d", r.Code)
		}
	})
}

// A message from claimed on is not changed by any face, and a Console flush of queue messages
// still works and marks them delivered.
func TestMemoQueueDeliveredAndClaimedAreLocked(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		e.workspaceFor("a", newCapturingAgent(t))
		tok := memoFaceClient{"token", e}
		_, held := tok.create(textMemo("r", "q", "held one"))
		_, pending := memoFaceClient{"console", e}.create(textMemo("r", "q", "pending one"))
		if r := e.do("a", "POST", "/api/memos/flush", map[string]any{"sessionName": "s1", "ids": []string{held.ID, pending.ID}}); r.Code != 200 {
			t.Fatalf("console flush: %d %s", r.Code, r.Body.String())
		}
		for _, id := range []string{held.ID, pending.ID} {
			if got := e.memoRow(id); got.State != store.MemoStateDelivered || got.SentAt == "" {
				t.Fatalf("after a manual send: %+v", got)
			}
		}
		_, claimed := tok.create(textMemo("r", "q", "claimed one"))
		e.setState(claimed.ID, store.MemoStateClaimed)
		for _, id := range []string{held.ID, claimed.ID} {
			for _, f := range faces(e) {
				if code, _ := f.update(id, map[string]any{"body": "changed"}); code != http.StatusConflict {
					t.Fatalf("%s edit of a %s message: %d", f.name, e.memoRow(id).State, code)
				}
				if code, _ := f.update(id, map[string]any{"position": 3}); code != http.StatusConflict {
					t.Fatalf("%s reorder of a %s message: %d", f.name, e.memoRow(id).State, code)
				}
			}
		}
		if e.memoRow(claimed.ID).Body != "claimed one" {
			t.Fatal("claimed message changed")
		}
		// A category change that would move a claimed message is refused as a whole.
		cat := decode[[]memoCategoryDTO](t, e.do("a", "GET", "/api/memo-categories", nil))[0].ID
		if r := e.do("a", "DELETE", "/api/memo-categories/"+cat, nil); r.Code != http.StatusConflict {
			t.Fatalf("delete of a category with a claimed message: %d", r.Code)
		}
		if got := e.memoRow(held.ID); got.Category != "q" || got.State != store.MemoStateDelivered {
			t.Fatalf("the refused delete moved a message: %+v", got)
		}
	})
}

// --- The invariant on every other write path -----------------------------------------------

func TestMemoQueueInvariantOnEveryWritePath(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		console, tok := memoFaceClient{"console", e}, memoFaceClient{"token", e}

		// Settings created: the member's own notes enter pending, the rest held.
		cat := e.category("r", "later")
		_, own := console.create(textMemo("r", "later", "mine"))
		_, ws := tok.create(textMemo("r", "later", "from a session"))
		e.queue(cat)
		if a, b := e.memoRow(own.ID), e.memoRow(ws.ID); a.State != store.MemoStatePending || b.State != store.MemoStateHeld {
			t.Fatalf("settings created: own=%q workspace=%q", a.State, b.State)
		}
		e.assertPendingReleaseMatches()

		// Console rename of the queue: the settings follow the id, pending stays pending and
		// its release is re-recorded under the new name.
		if r := e.do("a", "PATCH", "/api/memo-categories/"+cat, map[string]any{"name": "renamed"}); r.Code != 200 || !decode[memoCategoryDTO](t, r).Queue {
			t.Fatalf("rename: %d %s", r.Code, r.Body.String())
		}
		if a := e.memoRow(own.ID); a.Category != "renamed" || a.State != store.MemoStatePending {
			t.Fatalf("after rename: %+v", a)
		}
		e.assertPendingReleaseMatches()

		// Merge a plain category into the queue: its notes enter by the Console's rule.
		plain := e.category("r", "plain")
		_, own2 := console.create(textMemo("r", "plain", "mine too"))
		_, ws2 := tok.create(textMemo("r", "plain", "a session's too"))
		if r := e.do("a", "PATCH", "/api/memo-categories/"+plain, map[string]any{"name": "renamed"}); r.Code != 200 || !decode[memoCategoryDTO](t, r).Queue {
			t.Fatalf("merge into the queue: %d %s", r.Code, r.Body.String())
		}
		if a, b := e.memoRow(own2.ID), e.memoRow(ws2.ID); a.State != store.MemoStatePending || b.State != store.MemoStateHeld {
			t.Fatalf("merged in: own=%+v workspace=%+v", a, b)
		}
		e.assertPendingReleaseMatches()

		// Merge the queue into a plain category: the survivor's settings (none) win, the
		// messages are plain notes again, and the old settings row goes.
		target := e.category("r", "notes")
		if r := e.do("a", "PATCH", "/api/memo-categories/"+cat, map[string]any{"name": "notes"}); r.Code != 200 || decode[memoCategoryDTO](t, r).Queue {
			t.Fatalf("merge out: %d %s", r.Code, r.Body.String())
		}
		for _, id := range []string{own.ID, ws.ID, own2.ID, ws2.ID} {
			if m := e.memoRow(id); m.State != "" || m.ReleaseHash != "" || m.Category != "notes" {
				t.Fatalf("merged out: %+v", m)
			}
		}
		if e.count("memo_queue") != 0 {
			t.Fatal("the merged-away queue kept its settings")
		}

		// Category delete: the messages fall to the unfiled bucket and leave the queue.
		e.queue(target)
		if m := e.memoRow(own.ID); m.State != store.MemoStatePending {
			t.Fatalf("re-queued: %+v", m)
		}
		if r := e.do("a", "DELETE", "/api/memo-categories/"+target, nil); r.Code != 204 {
			t.Fatalf("delete: %d", r.Code)
		}
		for _, id := range []string{own.ID, ws.ID} {
			if m := e.memoRow(id); m.State != "" || m.ReleaseHash != "" || m.Category != "" {
				t.Fatalf("after category delete: %+v", m)
			}
		}
		if e.count("memo_queue") != 0 {
			t.Fatal("the deleted category kept its settings")
		}

		// Settings deleted: every message is a plain note again.
		q2 := e.category("r", "q2")
		e.queue(q2)
		_, m := console.create(textMemo("r", "q2", "x"))
		if r := e.do("a", "DELETE", "/api/memo-queues/"+q2, nil); r.Code != 204 {
			t.Fatalf("settings delete: %d", r.Code)
		}
		if got := e.memoRow(m.ID); got.State != "" || got.ReleaseHash != "" {
			t.Fatalf("after settings delete: %+v", got)
		}

		// Moved between repos: out of the queue to another repo's same name (no queue there)
		// it is a note; into a queue of another repo it enters by the face's rule.
		e.queue(q2)
		other := e.category("r2", "q2")
		_, mv := console.create(textMemo("r", "q2", "moving"))
		if code, _ := console.update(mv.ID, map[string]any{"repo": "r2"}); code != 200 || e.memoRow(mv.ID).State != "" {
			t.Fatalf("moved out by repo: %d %+v", code, e.memoRow(mv.ID))
		}
		e.queue(other)
		if code, _ := tok.update(mv.ID, map[string]any{"repo": "r"}); code != 200 || e.memoRow(mv.ID).State != store.MemoStateHeld {
			t.Fatalf("token move into a queue: %d %+v", code, e.memoRow(mv.ID))
		}
		if code, _ := console.update(mv.ID, map[string]any{"repo": "r2"}); code != 200 || e.memoRow(mv.ID).State != store.MemoStateHeld {
			t.Fatalf("console move of a held message between queues: %d %+v", code, e.memoRow(mv.ID))
		}
		_, own3 := console.create(textMemo("r2", "q2", "member's"))
		if code, _ := console.update(own3.ID, map[string]any{"repo": "r"}); code != 200 || e.memoRow(own3.ID).State != store.MemoStatePending {
			t.Fatalf("console move of a pending message between queues: %d %+v", code, e.memoRow(own3.ID))
		}
		if code, _ := tok.update(own3.ID, map[string]any{"category": ""}); code != 200 || e.memoRow(own3.ID).State != "" {
			t.Fatalf("token move out to unfiled: %d %+v", code, e.memoRow(own3.ID))
		}
		e.assertPendingReleaseMatches()
	})
}

// A memo the member already sent by hand enters a queue as delivered, never as pending: it was
// delivered once, and a pending one would be fired again.
func TestMemoQueueSentMemoEntersAsDelivered(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.workspaceFor("a", newCapturingAgent(t))
		cat := e.category("r", "later")
		console := memoFaceClient{"console", e}
		_, sent := console.create(textMemo("r", "later", "already sent"))
		_, other := console.create(textMemo("r", "elsewhere", "sent elsewhere"))
		if r := e.do("a", "POST", "/api/memos/flush", map[string]any{"sessionName": "s1", "ids": []string{sent.ID, other.ID}}); r.Code != 200 {
			t.Fatalf("flush: %d %s", r.Code, r.Body.String())
		}
		e.queue(cat)
		if got := e.memoRow(sent.ID); got.State != store.MemoStateDelivered || got.ReleaseHash != "" {
			t.Fatalf("sent memo after its category became a queue: %+v", got)
		}
		if code, _ := console.update(other.ID, map[string]any{"category": "later"}); code != 200 {
			t.Fatalf("move: %d", code)
		}
		if got := e.memoRow(other.ID); got.State != store.MemoStateDelivered {
			t.Fatalf("sent memo moved into a queue: %+v", got)
		}
	})
}

// An inbox memo is never in a queue: not by a move, a merge, or the category becoming one.
func TestMemoQueueRefusesInboxMemo(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		qcat := e.category("r", "q")
		e.queue(qcat)
		_, m := memoFaceClient{"console", e}.create(textMemo("r", "notes", "taken in"))
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET origin_item_id='item-1', provenance='inbox' WHERE id=?`, m.ID); err != nil {
			t.Fatal(err)
		}
		for _, f := range faces(e) {
			if code, _ := f.update(m.ID, map[string]any{"category": "q"}); code != http.StatusConflict {
				t.Fatalf("%s move of an inbox memo into a queue: %d", f.name, code)
			}
		}
		notes := e.category("r", "notes")
		if r := e.do("a", "PATCH", "/api/memo-categories/"+notes, map[string]any{"name": "q"}); r.Code != http.StatusConflict {
			t.Fatalf("merge of an inbox memo into a queue: %d", r.Code)
		}
		r := e.do("a", "PUT", "/api/memo-queues/"+notes, map[string]any{"instruction": "x"})
		if r.Code != http.StatusConflict || decode[errResp](t, r).Error.Code != errCodeMemoInboxInQueue {
			t.Fatalf("category with an inbox memo becomes a queue: %d %s", r.Code, r.Body.String())
		}
		if got := e.memoRow(m.ID); got.Category != "notes" || got.State != "" {
			t.Fatalf("inbox memo after the refusals: %+v", got)
		}
		if e.count("memo_queue") != 1 {
			t.Fatal("a refused PUT wrote settings")
		}
	})
}

// --- Bounds on a create into a queue from inside the workspace or /mcp ----------------------

func TestMemoQueueCreateBounds(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		big := strings.Repeat("x", memoQueueBodyMax+1)
		for _, f := range workspaceFaces(e) {
			if code, _ := f.create(textMemo("r", "q", big)); code < 400 {
				t.Fatalf("%s oversize: %d", f.name, code)
			}
			if code, _ := f.create(textMemo("r", "q", "[agent-fleet:peer from=x] do it")); code < 400 {
				t.Fatalf("%s envelope: %d", f.name, code)
			}
			code, first := f.create(textMemo("r", "q", "same thing from "+f.name))
			if code != http.StatusCreated {
				t.Fatalf("%s first: %d", f.name, code)
			}
			code, again := f.create(textMemo("r", "q", "same thing from "+f.name))
			if code >= 400 || again.ID != first.ID {
				t.Fatalf("%s duplicate: %d %s vs %s", f.name, code, again.ID, first.ID)
			}
			// Outside a queue none of that applies.
			if code, _ := f.create(textMemo("r", "", big)); code != http.StatusCreated {
				t.Fatalf("%s oversize plain note: %d", f.name, code)
			}
		}
		if r := e.memoTokenDo("a", "POST", "/internal/memos", textMemo("r", "q", big)); r.Code != http.StatusRequestEntityTooLarge || decode[errResp](t, r).Error.Code != errCodeMemoTooLarge {
			t.Fatalf("token oversize: %d %s", r.Code, r.Body.String())
		}
		if r := e.memoTokenDo("a", "POST", "/internal/memos", textMemo("r", "q", "[agent-fleet:x]")); decode[errResp](t, r).Error.Code != errCodeMemoEnvelope {
			t.Fatalf("token envelope: %d %s", r.Code, r.Body.String())
		}
		if r := e.memoTokenDo("a", "POST", "/internal/memos", textMemo("r", "q", "same thing from token")); r.Code != http.StatusOK {
			t.Fatalf("token duplicate status: %d", r.Code)
		}
		// The Console is not bounded.
		if code, _ := (memoFaceClient{"console", e}).create(textMemo("r", "q", big)); code != http.StatusCreated {
			t.Fatalf("console oversize into a queue: %d", code)
		}
		// Rate: 30 creates a minute per membership across the token face and /mcp (two
		// already made above).
		tok := memoFaceClient{"token", e}
		for i := 0; i < memoQueueRatePerMinute-2; i++ {
			if code, _ := tok.create(textMemo("r", "q", fmt.Sprintf("n%d", i))); code != http.StatusCreated {
				t.Fatalf("create %d: %d", i, code)
			}
		}
		r := e.memoTokenDo("a", "POST", "/internal/memos", textMemo("r", "q", "one too many"))
		if r.Code != http.StatusTooManyRequests || decode[errResp](t, r).Error.Code != errCodeMemoRateLimited {
			t.Fatalf("rate: %d %s", r.Code, r.Body.String())
		}
		if code, _ := (memoFaceClient{"mcp", e}).create(textMemo("r", "q", "mcp one too many")); code < 400 {
			t.Fatalf("mcp rate: %d", code)
		}
		if code, _ := tok.create(textMemo("r", "", "plain is not rate limited")); code != http.StatusCreated {
			t.Fatalf("plain after the rate: %d", code)
		}
	})
}

// hookAgent answers a memo send and runs onTurn while it holds the request, which is the
// window between the flush's check and its answer. status is the answer's status (0 = 200).
type hookAgent struct {
	srv     *httptest.Server
	mu      sync.Mutex
	onTurn  func()
	status  int
	prompts []string
}

// useHookAgent puts a hookAgent behind member "a"'s workspace runtime.
func (e *inboxEnv) useHookAgent() *hookAgent {
	e.t.Helper()
	h := &hookAgent{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		h.mu.Lock()
		p, _ := body["prompt"].(string)
		h.prompts = append(h.prompts, p)
		f, status := h.onTurn, h.status
		h.onTurn = nil
		h.mu.Unlock()
		if f != nil {
			f()
		}
		w.Header().Set("Content-Type", "application/json")
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"code":"not_running","message":"down"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	e.t.Cleanup(h.srv.Close)
	e.workspaceFor("a", newCapturingAgent(e.t))
	c := e.mgr.rts[e.members["a"]]
	c.rt = stubRuntime{endpoint: h.srv.URL, token: "tok"}
	e.mgr.rts[e.members["a"]] = c
	return h
}

// A flush reserves the rows it sends. A note in flight cannot enter a queue, alone or together
// with an edit, so a text nobody sent can never be counted as a delivered message; and a queue
// message the member sends cannot be rewritten while it is in flight.
func TestMemoQueueFlushReservesWhatItSends(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		h := e.useHookAgent()
		console, tok := memoFaceClient{"console", e}, memoFaceClient{"token", e}

		for _, f := range workspaceFaces(e) {
			for _, patch := range []map[string]any{
				{"category": "q"},
				{"category": "q", "body": "B, never sent"},
			} {
				_, m := console.create(textMemo("r", "plain", "A, sent by "+f.name))
				var moved int
				h.onTurn = func() { moved, _ = f.update(m.ID, patch) }
				if code := f.flush(m.ID); code != http.StatusOK {
					t.Fatalf("%s flush of a plain note: %d", f.name, code)
				}
				got := e.memoRow(m.ID)
				if moved != http.StatusConflict || got.Category != "plain" || got.State != "" || got.SentAt == "" || got.Body != m.Body || got.SendToken != "" {
					t.Fatalf("%s %v during the send: moved=%d, row %+v", f.name, patch, moved, got)
				}
			}
		}

		_, m := tok.create(textMemo("r", "q", "the body the member sends"))
		var edited int
		h.onTurn = func() { edited, _ = tok.update(m.ID, map[string]any{"body": "a body nobody sent"}) }
		if code := console.flush(m.ID); code != http.StatusOK {
			t.Fatalf("console flush: %d", code)
		}
		got := e.memoRow(m.ID)
		if edited != http.StatusConflict || got.Body != "the body the member sends" || got.State != store.MemoStateDelivered {
			t.Fatalf("edit during the send: %d, row %+v", edited, got)
		}
		if last := h.prompts[len(h.prompts)-1]; !strings.Contains(last, "the body the member sends") {
			t.Fatalf("sent %q", last)
		}
	})
}

// A send the Agent refuses leaves every row as it was: a note unsent, a pending message
// pending under its release, a held one held, and no reservation behind.
func TestMemoQueueRefusedFlushLeavesRowsAsTheyWere(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		h := e.useHookAgent()
		console, tok := memoFaceClient{"console", e}, memoFaceClient{"token", e}
		_, plain := console.create(textMemo("r", "", "a note"))
		_, pending := console.create(textMemo("r", "q", "released"))
		_, held := tok.create(textMemo("r", "q", "waiting"))
		h.status = http.StatusConflict
		if code := console.flush(plain.ID, pending.ID, held.ID); code != http.StatusConflict {
			t.Fatalf("failed flush: %d", code)
		}
		if got := e.memoRow(plain.ID); got.SentAt != "" || got.State != "" {
			t.Fatalf("plain after a failed send: %+v", got)
		}
		if got := e.memoRow(pending.ID); got.SentAt != "" || got.State != store.MemoStatePending || got.ReleaseHash != pending.ContentHash {
			t.Fatalf("pending after a failed send: %+v", got)
		}
		if got := e.memoRow(held.ID); got.SentAt != "" || got.State != store.MemoStateHeld {
			t.Fatalf("held after a failed send: %+v", got)
		}
		e.assertPendingReleaseMatches()
	})
}

// A claimed message belongs to its delivery: the Console neither deletes nor sends it.
func TestMemoQueueClaimedIsNotDeletedOrSent(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		h := e.useHookAgent()
		for _, f := range faces(e) {
			_, m := memoFaceClient{"console", e}.create(textMemo("r", "q", "claimed for "+f.name))
			e.setState(m.ID, store.MemoStateClaimed)
			if code := f.del(m.ID); code != http.StatusConflict {
				t.Fatalf("%s delete of a claimed message: %d", f.name, code)
			}
			if code := f.flush(m.ID); code != http.StatusConflict {
				t.Fatalf("%s flush of a claimed message: %d", f.name, code)
			}
			if got := e.memoRow(m.ID); got.State != store.MemoStateClaimed || got.SentAt != "" {
				t.Fatalf("%s: %+v", f.name, got)
			}
		}
		if len(h.prompts) != 0 {
			t.Fatalf("a claimed message reached a session: %q", h.prompts)
		}
	})
}

// During a rollout a Control Plane that predates migration 0131 still writes memos with its
// own statements, which set the content and never the state. Against a pending message they
// are refused by the database, so what the member released cannot change under it; against a
// held message or a plain note they work as before.
func TestMemoQueueOlderWriterCannotChangeAPendingMessage(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		console, tok := memoFaceClient{"console", e}, memoFaceClient{"token", e}
		_, pending := console.create(textMemo("r", "q", "member released"))
		_, held := tok.create(textMemo("r", "q", "waiting"))
		_, plain := console.create(textMemo("r", "", "note"))
		mid := e.members["a"]
		ctx := context.Background()
		// The statements of the binaries before this change (store_memo.go UpdateMemo and
		// ReassignMemoCategory on origin/main), verbatim.
		olderUpdate := func(id, body string) error {
			_, err := e.st.DB().ExecContext(ctx, `UPDATE memo SET repo=?, category=?, kind=?, body=?, ref_path=?, attachments=?, position=?
		   WHERE id=? AND membership_id=?`, "r", e.memoRow(id).Category, "text", body, "", "", 0, id, mid)
			return err
		}
		olderReassign := func(from, to string) error {
			_, err := e.st.DB().ExecContext(ctx, `UPDATE memo SET category=? WHERE membership_id=? AND repo=? AND category=?`, to, mid, "r", from)
			return err
		}
		if err := olderUpdate(pending.ID, "older token writer"); err == nil {
			t.Fatal("an older binary changed the content of a pending message")
		}
		if err := olderReassign("q", "elsewhere"); err == nil {
			t.Fatal("an older binary moved a pending message out of its queue")
		}
		if got := e.memoRow(pending.ID); got.Body != "member released" || got.Category != "q" {
			t.Fatalf("pending after the older writes: %+v", got)
		}
		if err := olderUpdate(held.ID, "older edit of a held one"); err != nil {
			t.Fatalf("older edit of a held message: %v", err)
		}
		if err := olderUpdate(plain.ID, "older edit of a note"); err != nil {
			t.Fatalf("older edit of a note: %v", err)
		}
		e.assertPendingReleaseMatches()
		// The new paths keep the release columns in step: a Console edit, a release and a
		// Console rename of the queue all still go through.
		if code, _ := console.update(pending.ID, map[string]any{"body": "member edited"}); code != 200 {
			t.Fatalf("console edit of a pending message: %d", code)
		}
		if r := e.release(held.ID, memoContentHash(e.memoRow(held.ID))); r.Code != 200 {
			t.Fatalf("release: %d %s", r.Code, r.Body.String())
		}
		cat := decode[[]memoCategoryDTO](t, e.do("a", "GET", "/api/memo-categories", nil))[0].ID
		if r := e.do("a", "PATCH", "/api/memo-categories/"+cat, map[string]any{"name": "q2"}); r.Code != 200 {
			t.Fatalf("console rename: %d %s", r.Code, r.Body.String())
		}
		e.assertPendingReleaseMatches()
	})
}

// The rate bound counts creates, not the messages still in a queue: moving a message out, or
// deleting it, does not give its create back.
func TestMemoQueueRateCountsCreates(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		tok := memoFaceClient{"token", e}
		for i := 0; i < memoQueueRatePerMinute; i++ {
			code, m := tok.create(textMemo("r", "q", fmt.Sprintf("create %d", i)))
			if code != http.StatusCreated {
				t.Fatalf("create %d: %d", i, code)
			}
			if code, _ := tok.update(m.ID, map[string]any{"category": "plain"}); code != 200 {
				t.Fatalf("move %d: %d", i, code)
			}
			if i%2 == 0 && tok.del(m.ID) != http.StatusNoContent {
				t.Fatalf("delete %d", i)
			}
		}
		if code, _ := tok.create(textMemo("r", "q", "one too many")); code != http.StatusTooManyRequests {
			t.Fatalf("create after %d creates: %d", memoQueueRatePerMinute, code)
		}
	})
}

// Concurrent creates cannot pass the bound together: the count and the log are in the
// transaction that holds the membership lock.
func TestMemoQueueRateHoldsUnderConcurrentCreates(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		var wg sync.WaitGroup
		var mu sync.Mutex
		created := 0
		for i := 0; i < memoQueueRatePerMinute+10; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				r := e.memoTokenDo("a", "POST", "/internal/memos", textMemo("r", "q", fmt.Sprintf("c%d", i)))
				if r.Code == http.StatusCreated {
					mu.Lock()
					created++
					mu.Unlock()
				} else if r.Code != http.StatusTooManyRequests {
					t.Errorf("create %d: %d %s", i, r.Code, r.Body.String())
				}
			}(i)
		}
		wg.Wait()
		if created != memoQueueRatePerMinute {
			t.Fatalf("%d creates passed, want %d", created, memoQueueRatePerMinute)
		}
	})
}

// A flush in flight owns its rows: a second flush of them is refused, so two flushes can never
// settle each other's rows, and the first one's outcome is the row's.
func TestMemoQueueOverlappingFlushesDoNotSettleEachOther(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		h := e.useHookAgent()
		console := memoFaceClient{"console", e}
		for _, outcome := range []int{0, http.StatusConflict} {
			_, m := console.create(textMemo("r", "q", fmt.Sprintf("overlap %d", outcome)))
			_, plain := console.create(textMemo("r", "", fmt.Sprintf("plain overlap %d", outcome)))
			var second, secondPlain int
			h.status = outcome
			h.onTurn = func() {
				second = console.flush(m.ID)
				secondPlain = console.flush(plain.ID)
			}
			code := console.flush(m.ID, plain.ID)
			if second != http.StatusConflict || secondPlain != http.StatusConflict {
				t.Fatalf("outcome %d: second flush %d / %d, want 409", outcome, second, secondPlain)
			}
			got, gotPlain := e.memoRow(m.ID), e.memoRow(plain.ID)
			if outcome == 0 {
				if code != 200 || got.State != store.MemoStateDelivered || got.SentAt == "" || gotPlain.SentAt == "" {
					t.Fatalf("sent: %d %+v %+v", code, got, gotPlain)
				}
			} else if code != http.StatusConflict || got.State != store.MemoStatePending || got.SentAt != "" || gotPlain.SentAt != "" {
				t.Fatalf("refused: %d %+v %+v", code, got, gotPlain)
			}
			if got.SendToken != "" || gotPlain.SendToken != "" {
				t.Fatalf("a settled flush left its reservation: %+v %+v", got, gotPlain)
			}
		}
		e.assertPendingReleaseMatches()
	})
}

// A send whose outcome is unknown (the Agent was not reached) leaves a queue message
// unconfirmed, never pending again, so it is not fired a second time; a plain note stays
// unsent as before.
func TestMemoQueueUnknownSendOutcomeIsUnconfirmed(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		e.workspaceFor("a", newCapturingAgent(t))
		c := e.mgr.rts[e.members["a"]]
		c.rt = stubRuntime{endpoint: "http://127.0.0.1:1", token: "tok"}
		e.mgr.rts[e.members["a"]] = c
		console, tok := memoFaceClient{"console", e}, memoFaceClient{"token", e}
		_, pending := console.create(textMemo("r", "q", "released"))
		_, held := tok.create(textMemo("r", "q", "waiting"))
		_, plain := console.create(textMemo("r", "", "note"))
		if code := console.flush(pending.ID, held.ID, plain.ID); code < 500 {
			t.Fatalf("unreachable agent: %d", code)
		}
		for _, id := range []string{pending.ID, held.ID} {
			if got := e.memoRow(id); got.State != store.MemoStateUnconfirmed || got.SentAt != "" || got.SendToken != "" || got.ReleaseHash != "" {
				t.Fatalf("queue message after an unknown outcome: %+v", got)
			}
		}
		if got := e.memoRow(plain.ID); got.State != "" || got.SentAt != "" || got.SendToken != "" {
			t.Fatalf("plain note after an unknown outcome: %+v", got)
		}
		// An unconfirmed message cannot be released back into the queue's hands.
		if r := e.release(held.ID, held.ContentHash); r.Code != http.StatusConflict {
			t.Fatalf("release of an unconfirmed message: %d", r.Code)
		}
	})
}

// A reservation is durable but not permanent. While it is fresh it holds the rows; once it is
// older than the bound (the CP that made it died before the outcome) the next read or write
// takes it back: a queue message becomes unconfirmed, a plain note is unsent again, and
// nothing is marked sent that was never sent.
func TestMemoQueueReservationLeftByACrashIsTakenBack(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		console := memoFaceClient{"console", e}
		e.workspaceFor("a", newCapturingAgent(t))
		_, m := console.create(textMemo("r", "q", "reserved when the CP died"))
		_, plain := console.create(textMemo("r", "", "a note reserved when the CP died"))
		reserve := func(at string) {
			for _, id := range []string{m.ID, plain.ID} {
				if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET send_token='dead', send_started_at=? WHERE id=?`, at, id); err != nil {
					t.Fatal(err)
				}
			}
		}
		reserve(store.NowTS())
		if code, _ := console.update(m.ID, map[string]any{"body": "changed"}); code != http.StatusConflict {
			t.Fatalf("edit of a reserved queue message: %d", code)
		}
		if code := console.flush(m.ID); code != http.StatusConflict {
			t.Fatalf("flush of a reserved message: %d", code)
		}
		reserve(time.Now().UTC().Add(-store.MemoSendStaleAfter - time.Minute).Format(time.RFC3339))
		list := decode[[]memoDTO](t, e.do("a", "GET", "/api/memos", nil))
		if len(list) != 2 {
			t.Fatalf("list: %+v", list)
		}
		if got := e.memoRow(m.ID); got.State != store.MemoStateUnconfirmed || got.SentAt != "" || got.SendToken != "" {
			t.Fatalf("queue message after the stale reservation: %+v", got)
		}
		if got := e.memoRow(plain.ID); got.State != "" || got.SentAt != "" || got.SendToken != "" {
			t.Fatalf("plain note after the stale reservation: %+v", got)
		}
		// The member can still send it by hand.
		if code := console.flush(m.ID, plain.ID); code != http.StatusOK {
			t.Fatalf("manual send after the sweep: %d", code)
		}
		if got := e.memoRow(m.ID); got.State != store.MemoStateDelivered || got.SentAt == "" {
			t.Fatalf("after the manual send: %+v", got)
		}
	})
}

// No category becomes a queue while the tenant switch is off, which is every tenant's default:
// that is what keeps queue rows away from a Control Plane binary without the queue rules during
// a rollout. Existing queues are listed, deleted and released whatever the switch says, and
// turning it off deletes nothing.
func TestMemoQueueTenantSwitchGatesCreatingAQueue(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		if on, err := e.st.GetTenantMemoQueues(context.Background(), e.t2.ID); err != nil || on {
			t.Fatalf("an untouched tenant: on=%v err=%v, want off", on, err)
		}
		cat := e.category("r", "q")
		e.memoQueuesOn(false)
		r := e.do("a", "PUT", "/api/memo-queues/"+cat, map[string]any{"instruction": "x"})
		if r.Code != http.StatusConflict || decode[errResp](t, r).Error.Code != errCodeMemoQueuesDisabled {
			t.Fatalf("PUT with the switch off: %d %s", r.Code, r.Body.String())
		}
		if e.count("memo_queue") != 0 {
			t.Fatal("a refused PUT wrote settings")
		}
		if got := decode[memoQueuesWire](t, e.do("a", "GET", "/api/memo-queues", nil)); got.TenantEnabled || got.Queues == nil {
			t.Fatalf("GET with the switch off: %+v", got)
		}
		e.memoQueuesOn(true)
		e.queue(cat)
		_, m := memoFaceClient{"token", e}.create(textMemo("r", "q", "held"))
		// Off again: the queue stays, its rules still hold, and the Console can still release
		// and remove it; only creating or changing settings is refused.
		e.memoQueuesOn(false)
		got := decode[memoQueuesWire](t, e.do("a", "GET", "/api/memo-queues", nil))
		if got.TenantEnabled || len(got.Queues) != 1 {
			t.Fatalf("GET after turning it off: %+v", got)
		}
		if code := (memoFaceClient{"token", e}).del(m.ID); code != http.StatusConflict {
			t.Fatalf("token delete with the switch off: %d", code)
		}
		if r := e.do("a", "PUT", "/api/memo-queues/"+cat, map[string]any{"instruction": "y"}); r.Code != http.StatusConflict {
			t.Fatalf("settings change with the switch off: %d", r.Code)
		}
		if r := e.release(m.ID, m.ContentHash); r.Code != http.StatusOK {
			t.Fatalf("release with the switch off: %d %s", r.Code, r.Body.String())
		}
		if r := e.do("a", "DELETE", "/api/memo-queues/"+cat, nil); r.Code != http.StatusNoContent {
			t.Fatalf("delete with the switch off: %d", r.Code)
		}
	})
}

// The settle never marks a queue message delivered when its text is not what the flush sent,
// whatever let the text change: the reservation is the first guard, this is the second.
func TestMemoQueueSettleDeliversOnlyTheSentText(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		_, m := memoFaceClient{"token", e}.create(textMemo("r", "q", "B, never sent"))
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET send_token='t1', send_started_at=? WHERE id=?`, store.NowTS(), m.ID); err != nil {
			t.Fatal(err)
		}
		sent := e.memoRow(m.ID)
		sent.Body = "A, the text that was sent"
		if err := memoSettleSend(context.Background(), e.st, e.members["a"], "t1", []store.Memo{sent}, nil, store.NowTS()); err != nil {
			t.Fatal(err)
		}
		if got := e.memoRow(m.ID); got.State != store.MemoStateHeld || got.SentAt != "" || got.SendToken != "" {
			t.Fatalf("after settling a send of other text: %+v", got)
		}
	})
}

// R3-1, as a matrix: while a send is in flight, no change of the body, the ref, the
// attachments, the repo or the category can carry a memo into, out of or within a queue, for
// a memo that starts as a plain note and for one that starts in a queue, on the Console and
// the token face. Each such change is refused, and the settle records exactly what was sent.
// A change that keeps a plain note plain still goes through, as it always did.
func TestMemoQueueNoChangeRidesASendInFlight(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		e.queue(e.category("r2", "plain"))
		h := e.useHookAgent()
		console := memoFaceClient{"console", e}
		fileMemo := func(repo, category, body string) memoDTO {
			_, d := console.create(map[string]any{"kind": "file", "repo": repo, "category": category, "body": body, "refPath": "~/repos/r/a.go"})
			return d
		}
		att := []map[string]any{{"path": "/home/dev/.cache/agent-fleet/memo-images/x.png"}}
		cases := []struct {
			name, startCat string
			patch          map[string]any
			refused        bool
		}{
			{"plain: body into a queue", "plain", map[string]any{"category": "q", "body": "B"}, true},
			{"plain: ref into a queue", "plain", map[string]any{"category": "q", "refPath": "~/repos/r/b.go"}, true},
			{"plain: attachments into a queue", "plain", map[string]any{"category": "q", "attachments": att}, true},
			{"plain: repo into a queue", "plain", map[string]any{"repo": "r2"}, true},
			{"plain: category into a queue", "plain", map[string]any{"category": "q"}, true},
			{"plain: body, staying plain", "plain", map[string]any{"body": "B"}, false},
			{"queue: body", "q", map[string]any{"body": "B"}, true},
			{"queue: ref", "q", map[string]any{"refPath": "~/repos/r/b.go"}, true},
			{"queue: attachments", "q", map[string]any{"attachments": att}, true},
			{"queue: repo out", "q", map[string]any{"repo": "r3"}, true},
			{"queue: category out", "q", map[string]any{"category": "plain"}, true},
		}
		for _, f := range []memoFaceClient{console, {"token", e}} {
			for _, c := range cases {
				if f.name == "token" && c.patch["attachments"] != nil {
					continue // the token face sends attachments too, but one face is enough for them
				}
				m := fileMemo("r", c.startCat, "A, the text that is sent")
				var code int
				var rec *httptest.ResponseRecorder
				h.onTurn = func() { rec = f.http("PATCH", "/memos/"+m.ID, c.patch); code = rec.Code }
				if r := e.do("a", "POST", "/api/memos/flush", map[string]any{"sessionName": "s1", "ids": []string{m.ID}}); r.Code != http.StatusOK {
					t.Fatalf("%s %s: flush %d %s", f.name, c.name, r.Code, r.Body.String())
				}
				got := e.memoRow(m.ID)
				if got.SendToken != "" || got.SentAt == "" {
					t.Fatalf("%s %s: not settled as sent: %+v", f.name, c.name, got)
				}
				if !c.refused {
					if code != http.StatusOK || got.Body != "B" || got.State != "" {
						t.Fatalf("%s %s: %d %+v", f.name, c.name, code, got)
					}
					continue
				}
				if code != http.StatusConflict || decode[errResp](t, rec).Error.Code != errCodeMemoSending {
					t.Fatalf("%s %s: change during the send answered %d %s", f.name, c.name, code, rec.Body.String())
				}
				wantState := ""
				if c.startCat == "q" {
					wantState = store.MemoStateDelivered
				}
				if got.Body != m.Body || got.RefPath != m.RefPath || got.Attachments != "" || got.Repo != "r" || got.Category != c.startCat || got.State != wantState {
					t.Fatalf("%s %s: row after the send %+v", f.name, c.name, got)
				}
			}
		}
		e.assertPendingReleaseMatches()
	})
}
