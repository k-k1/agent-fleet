package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/memoref"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Typed references on queue messages (ADR 0137 section 4): validation, the two faces, the
// release hash, one open message per reference and queue, and the fire. Every test runs on SQLite
// and, with AF_TEST_DATABASE_URL, on Postgres.

func refOf(provider, key, url string) map[string]any {
	return map[string]any{"provider": provider, "key": key, "url": url}
}

const refURL = "https://github.com/acme/app/issues/12"

func refMemo(repo, category, body string, ref map[string]any) map[string]any {
	m := textMemo(repo, category, body)
	if ref != nil {
		m["reference"] = ref
	}
	return m
}

func ghRef(n string) map[string]any {
	return refOf("github", "acme/app#"+n, "https://github.com/acme/app/issues/"+n)
}

func (e *inboxEnv) memoCount(repo, category string) int {
	e.t.Helper()
	rows, err := e.st.ListMemos(context.Background(), e.members["a"], "")
	if err != nil {
		e.t.Fatal(err)
	}
	n := 0
	for _, m := range rows {
		if m.Repo == repo && m.Category == category {
			n++
		}
	}
	return n
}

func TestMemoReferenceValidation(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		long := strings.Repeat("a", memoref.URLMax)
		for _, c := range []struct {
			name string
			body map[string]any
			ok   bool
		}{
			{"github", refMemo("r", "n", "x", refOf("github", "acme/app#12", refURL)), true},
			{"jira", refMemo("r", "n", "x", refOf("jira", "PROJ-12", "https://acme.atlassian.net/browse/PROJ-12")), true},
			{"bitbucket", refMemo("r", "n", "x", refOf("bitbucket", "acme/app#3", "https://bitbucket.org/acme/app/pull-requests/3")), true},
			{"provider case and jira key case are folded", refMemo("r", "n", "x", refOf("JIRA", "proj-12", "http://j.example/browse/PROJ-12")), true},
			{"reference only, no body", refMemo("r", "n", "", refOf("github", "acme/app#13", refURL)), true},
			{"no body and no reference", textMemo("r", "n", ""), false},
			{"unknown provider", refMemo("r", "n", "x", refOf("gitlab", "acme/app#12", refURL)), false},
			{"a part missing: url", refMemo("r", "n", "x", refOf("github", "acme/app#12", "")), false},
			{"a part missing: key", refMemo("r", "n", "x", refOf("github", "", refURL)), false},
			{"a part missing: provider", refMemo("r", "n", "x", refOf("", "acme/app#12", refURL)), false},
			{"bare number has no repository", refMemo("r", "n", "x", refOf("github", "#12", refURL)), false},
			{"github key in the jira shape", refMemo("r", "n", "x", refOf("github", "PROJ-12", refURL)), false},
			{"jira key in the github shape", refMemo("r", "n", "x", refOf("jira", "acme/app#12", refURL)), false},
			{"number zero", refMemo("r", "n", "x", refOf("github", "acme/app#0", refURL)), false},
			{"key with a space", refMemo("r", "n", "x", refOf("github", "acme/app #12", refURL)), false},
			{"key over the bound", refMemo("r", "n", "x", refOf("github", strings.Repeat("a", memoref.KeyMax)+"/b#1", refURL)), false},
			{"url not http", refMemo("r", "n", "x", refOf("github", "acme/app#12", "javascript:alert(1)")), false},
			{"url ftp", refMemo("r", "n", "x", refOf("github", "acme/app#12", "ftp://github.com/x")), false},
			{"url with no host", refMemo("r", "n", "x", refOf("github", "acme/app#12", "https:///x")), false},
			{"url with a space", refMemo("r", "n", "x", refOf("github", "acme/app#12", "https://github.com/a b")), false},
			{"url over the bound", refMemo("r", "n", "x", refOf("github", "acme/app#12", "https://github.com/"+long)), false},
			{"a reference on a file memo", map[string]any{"kind": "file", "repo": "r", "category": "n", "refPath": "p", "reference": ghRef("1")}, false},
		} {
			for _, f := range []string{"console", "token"} {
				rec := memoFaceClient{f, e}.http("POST", "/memos", c.body)
				if c.ok != (rec.Code == http.StatusCreated) {
					t.Errorf("%s/%s: %d %s", c.name, f, rec.Code, rec.Body.String())
				}
				if !c.ok && rec.Code == http.StatusBadRequest && c.body["reference"] != nil && !strings.Contains(c.name, "body") &&
					decode[errResp](t, rec).Error.Code != errCodeMemoRefInvalid {
					t.Errorf("%s/%s: code %s", c.name, f, rec.Body.String())
				}
			}
		}
		// What was stored is the normalized form.
		rec := e.do("a", "POST", "/api/memos", refMemo("r", "n2", "x", refOf("JIRA", " proj-77 ", "http://j.example/browse/PROJ-77")))
		got := decode[memoDTO](t, rec)
		if got.Reference == nil || *got.Reference != (memoRefWire{"jira", "PROJ-77", "http://j.example/browse/PROJ-77"}) {
			t.Fatalf("normalized reference: %+v", got.Reference)
		}
	})
}

// A reference round-trips through create, update and list on both faces. The state and the
// provenance are the ones the face gives a message without a reference: a reference changes
// neither, and a reference-only message from a session gets no exemption from the press.
func TestMemoReferenceRoundTripAndState(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		for _, c := range []struct {
			face  string
			state string
			prov  string
		}{{"console", store.MemoStatePending, store.MemoProvenanceMember}, {"token", store.MemoStateHeld, store.MemoProvenanceWorkspace}} {
			f := memoFaceClient{c.face, e}
			n := map[string]string{"console": "1", "token": "2"}[c.face]
			code, m := f.create(refMemo("r", "q", "my note", ghRef(n)))
			if code != http.StatusCreated || m.Reference == nil || m.Reference.Key != "acme/app#"+n || m.State != c.state || m.Provenance != c.prov || m.Existing {
				t.Fatalf("%s create: %d %+v", c.face, code, m)
			}
			// A reference-only message: no body, no exemption.
			code, only := f.create(refMemo("r", "q", "", ghRef(n+"0")))
			if code != http.StatusCreated || only.Body != "" || only.State != c.state {
				t.Fatalf("%s reference only: %d %+v", c.face, code, only)
			}
			// Update: set another reference, then clear it.
			code, up := f.update(m.ID, map[string]any{"reference": ghRef(n + "1")})
			if code != http.StatusOK || up.Reference == nil || up.Reference.Key != "acme/app#"+n+"1" {
				t.Fatalf("%s update: %d %+v", c.face, code, up)
			}
			if code, same := f.update(m.ID, map[string]any{"body": "edited"}); code != http.StatusOK || same.Reference == nil || same.Reference.Key != up.Reference.Key {
				t.Fatalf("%s an update without a reference changes it: %d %+v", c.face, code, same)
			}
			code, cleared := f.update(m.ID, map[string]any{"reference": map[string]any{}})
			if code != http.StatusOK || cleared.Reference != nil {
				t.Fatalf("%s clear: %d %+v", c.face, code, cleared)
			}
			// A reference-only message cannot lose its reference.
			if code, _ := f.update(only.ID, map[string]any{"reference": map[string]any{}}); code != http.StatusBadRequest {
				t.Fatalf("%s clearing the only content: %d", c.face, code)
			}
			// A partial reference is refused and changes nothing.
			if code, _ := f.update(only.ID, map[string]any{"reference": map[string]any{"provider": "github"}}); code != http.StatusBadRequest {
				t.Fatalf("%s partial: %d", c.face, code)
			}
			row := e.memoRow(only.ID)
			if row.RefKey != "acme/app#"+n+"0" || row.State != c.state {
				t.Fatalf("%s row after refused updates: %+v", c.face, row)
			}
		}
		// The list shows it on both faces.
		for _, rec := range []*httptest.ResponseRecorder{e.do("a", "GET", "/api/memos", nil), e.memoTokenDo("a", "GET", "/internal/memos", nil)} {
			list := decode[[]memoDTO](t, rec)
			seen := 0
			for _, m := range list {
				if m.Reference != nil {
					seen++
					if m.Reference.Provider != "github" || !strings.HasPrefix(m.Reference.URL, "https://github.com/acme/app/issues/") {
						t.Fatalf("listed reference %+v", m.Reference)
					}
				}
			}
			if seen != 2 {
				t.Fatalf("%d referenced messages listed, want 2: %s", seen, rec.Body.String())
			}
		}
		e.assertPendingReleaseMatches()
	})
}

// The release is bound to the reference: a change of it from inside the workspace puts a
// released message back to held, the Console's own change keeps the state, and the card's old
// hash no longer releases anything.
func TestMemoReferenceIsPartOfTheRelease(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		console, tok := memoFaceClient{"console", e}, memoFaceClient{"token", e}
		seq := 100
		released := func() memoDTO {
			seq++
			_, m := tok.create(refMemo("r", "q", "same body", ghRef(strconv.Itoa(seq))))
			if r := e.release(m.ID, m.ContentHash); r.Code != 200 {
				t.Fatalf("release: %d %s", r.Code, r.Body.String())
			}
			if got := e.memoRow(m.ID); got.State != store.MemoStatePending {
				t.Fatalf("not released: %+v", got)
			}
			return m
		}
		// The hash moves with the reference, and not otherwise with a reference-less message.
		a, b := memoContentHash(store.Memo{Kind: "text", Body: "x"}), memoContentHash(store.Memo{Kind: "text", Body: "x", RefProvider: "github", RefKey: "a/b#1", RefURL: refURL})
		if a == b || memoContentHash(store.Memo{Kind: "text", Body: "x", RefProvider: "github", RefKey: "a/b#2", RefURL: refURL}) == b ||
			memoContentHash(store.Memo{Kind: "text", Body: "x", RefProvider: "github", RefKey: "a/b#1", RefURL: refURL + "x"}) == b {
			t.Fatal("the content hash does not cover the reference")
		}

		for _, via := range []string{"token", "mcp"} {
			m := released()
			v := memoVia{face: memoFaceToken}
			if via == "mcp" {
				v = memoVia{face: memoFaceMCP}
			}
			if via == "token" {
				if code, _ := tok.update(m.ID, map[string]any{"reference": ghRef("90")}); code != 200 {
					t.Fatalf("token update: %d", code)
				}
			} else if _, aerr := memoUpdateFor(context.Background(), e.st, e.members["a"], m.ID, memoPatch{Reference: &memoRefWire{"github", "acme/app#91", refURL}}, v); aerr != nil {
				t.Fatalf("mcp update: %v", aerr.message)
			}
			got := e.memoRow(m.ID)
			if got.State != store.MemoStateHeld || got.ReleaseHash != "" || got.ReleasedAt != "" {
				t.Fatalf("%s: a changed reference left the message %+v", via, got)
			}
			// The card that showed the old content releases nothing.
			if r := e.release(m.ID, m.ContentHash); r.Code != http.StatusConflict {
				t.Fatalf("%s: release with the old hash: %d %s", via, r.Code, r.Body.String())
			}
		}
		// Clearing the reference counts the same.
		m := released()
		if code, _ := tok.update(m.ID, map[string]any{"reference": map[string]any{}}); code != 200 {
			t.Fatal("token clear")
		}
		if got := e.memoRow(m.ID); got.State != store.MemoStateHeld {
			t.Fatalf("cleared reference: %+v", got)
		}
		// The same change from the Console is the member's own: the state stays, the hash follows.
		m = released()
		if code, up := console.update(m.ID, map[string]any{"reference": ghRef("95")}); code != 200 || up.State != store.MemoStatePending {
			t.Fatalf("console update: %d %+v", code, up)
		}
		if got := e.memoRow(m.ID); got.State != store.MemoStatePending || got.RefKey != "acme/app#95" || got.ReleaseHash != memoContentHash(got) {
			t.Fatalf("console update left %+v", got)
		}
		// An update that changes nothing in the content keeps a released message released.
		m = released()
		if code, _ := tok.update(m.ID, map[string]any{"reference": ghRef(m.Reference.Key[len("acme/app#"):])}); code != 200 {
			t.Fatal("update")
		}
		e.assertPendingReleaseMatches()
	})
}

// One open message per (queue, reference): every way a second one could appear.
func TestMemoReferenceOneOpenMessagePerQueue(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.category("r", "q")
		e.queue(q)
		console, tok := memoFaceClient{"console", e}, memoFaceClient{"token", e}

		// Create twice: the answer is the message already there, said to be existing, on both faces.
		code, first := console.create(refMemo("r", "q", "note one", ghRef("1")))
		if code != http.StatusCreated || first.Existing {
			t.Fatalf("first: %d %+v", code, first)
		}
		for _, f := range []memoFaceClient{console, tok} {
			code, again := f.create(refMemo("r", "q", "another note", refOf("github", "ACME/app#1", refURL)))
			if code != http.StatusOK || again.ID != first.ID || !again.Existing || again.Body != "note one" {
				t.Fatalf("%s add again: %d %+v", f.name, code, again)
			}
		}
		if n := e.memoCount("r", "q"); n != 1 {
			t.Fatalf("%d messages, want 1", n)
		}
		// Another queue, another provider and another number are other items.
		e.queue(e.category("r", "q2"))
		for _, m := range []map[string]any{
			refMemo("r", "q2", "x", ghRef("1")),
			refMemo("r", "q", "x", refOf("bitbucket", "acme/app#1", refURL)),
			refMemo("r", "q", "x", ghRef("2")),
		} {
			if code, got := console.create(m); code != http.StatusCreated || got.Existing {
				t.Fatalf("not a duplicate: %d %+v", code, got)
			}
		}

		// An update that sets or changes a reference onto an open one is refused.
		_, other := console.create(textMemo("r", "q", "plain note"))
		for _, f := range []memoFaceClient{console, tok} {
			code, _ := f.update(other.ID, map[string]any{"reference": ghRef("1")})
			rec := f.http("PATCH", "/memos/"+other.ID, map[string]any{"reference": ghRef("1")})
			if code != http.StatusConflict || decode[errResp](t, rec).Error.Code != errCodeMemoRefDuplicate {
				t.Fatalf("%s update onto an open reference: %d %s", f.name, code, rec.Body.String())
			}
		}
		if row := e.memoRow(other.ID); row.RefKey != "" {
			t.Fatalf("a refused update wrote %+v", row)
		}

		// A move into the queue's category.
		_, loose := console.create(refMemo("r", "elsewhere", "x", ghRef("1")))
		for _, f := range []memoFaceClient{console, tok} {
			rec := f.http("PATCH", "/memos/"+loose.ID, map[string]any{"category": "q"})
			if rec.Code != http.StatusConflict || decode[errResp](t, rec).Error.Code != errCodeMemoRefDuplicate {
				t.Fatalf("%s move into the queue: %d %s", f.name, rec.Code, rec.Body.String())
			}
		}
		if row := e.memoRow(loose.ID); row.Category != "elsewhere" {
			t.Fatalf("the refused move left %+v", row)
		}

		// A category that becomes a queue while it holds two notes with the same reference is
		// refused, and stays what it was. One that holds distinct ones becomes a queue.
		dup := e.category("r", "dup")
		console.create(refMemo("r", "dup", "a", ghRef("5")))
		console.create(refMemo("r", "dup", "b", ghRef("5")))
		if rec := e.do("a", "PUT", "/api/memo-queues/"+dup, map[string]any{"instruction": "do it"}); rec.Code != http.StatusConflict || decode[errResp](t, rec).Error.Code != errCodeMemoRefDuplicate {
			t.Fatalf("a queue over two notes with one reference: %d %s", rec.Code, rec.Body.String())
		}
		if e.count("memo_queue") != 2 {
			t.Fatalf("%d queues, the refused one was made", e.count("memo_queue"))
		}
		for _, m := range e.listMemos("r", "dup") {
			if m.State != "" {
				t.Fatalf("the refused conversion left %+v", m)
			}
		}
		// Sent notes are closed and do not count.
		sent := e.category("r", "sent")
		_, s1 := console.create(refMemo("r", "sent", "a", ghRef("6")))
		_, s2 := console.create(refMemo("r", "sent", "b", ghRef("6")))
		e.markSent(s1.ID)
		if rec := e.do("a", "PUT", "/api/memo-queues/"+sent, map[string]any{"instruction": "do it"}); rec.Code != http.StatusOK {
			t.Fatalf("a queue over one open and one sent note: %d %s", rec.Code, rec.Body.String())
		}
		if e.memoRow(s1.ID).State != store.MemoStateDelivered || e.memoRow(s2.ID).State != store.MemoStatePending {
			t.Fatal("states after the conversion")
		}

		// A rename that merges two categories (the survivor's settings win).
		src := e.category("r", "src")
		console.create(refMemo("r", "src", "x", ghRef("1")))
		rec := e.do("a", "PATCH", "/api/memo-categories/"+src, map[string]any{"name": "q"})
		if rec.Code != http.StatusConflict || decode[errResp](t, rec).Error.Code != errCodeMemoRefDuplicate {
			t.Fatalf("merge: %d %s", rec.Code, rec.Body.String())
		}
		if n := e.memoCount("r", "src"); n != 1 {
			t.Fatalf("the refused merge moved %d rows out of src", 1-n)
		}
		// A merge of notes that do not collide goes through, and the merged rows count afterwards.
		src2 := e.category("r", "src2")
		console.create(refMemo("r", "src2", "x", ghRef("77")))
		if rec := e.do("a", "PATCH", "/api/memo-categories/"+src2, map[string]any{"name": "q"}); rec.Code != http.StatusOK {
			t.Fatalf("merge without a collision: %d %s", rec.Code, rec.Body.String())
		}
		if code, got := console.create(refMemo("r", "q", "x", ghRef("77"))); code != http.StatusOK || !got.Existing {
			t.Fatalf("a merged message is not counted: %d %+v", code, got)
		}
		// Two categories merged where both rows are not in a queue stay unconstrained (no queue).
		a1, a2 := e.category("r", "pa"), e.category("r", "pb")
		console.create(refMemo("r", "pa", "x", ghRef("8")))
		console.create(refMemo("r", "pb", "x", ghRef("8")))
		_ = a1
		if rec := e.do("a", "PATCH", "/api/memo-categories/"+a2, map[string]any{"name": "pa"}); rec.Code != http.StatusOK {
			t.Fatalf("a merge of two plain categories: %d %s", rec.Code, rec.Body.String())
		}

		// A message returned from a delivery that was not sent is the same row and never a second.
		// Closed states free the reference: delivered, unconfirmed and superseded.
		for _, st := range []string{store.MemoStateDelivered, store.MemoStateUnconfirmed, store.MemoStateSuperseded} {
			e.queue(e.category("r", "c-"+st))
			_, m := console.create(refMemo("r", "c-"+st, "x", ghRef("9")))
			e.setState(m.ID, st)
			if code, again := console.create(refMemo("r", "c-"+st, "x", ghRef("9"))); code != http.StatusCreated || again.ID == m.ID {
				t.Fatalf("after %s: %d %+v", st, code, again)
			}
		}
		// Open states hold it: held, pending and claimed.
		for _, st := range []string{store.MemoStateHeld, store.MemoStatePending, store.MemoStateClaimed} {
			e.queue(e.category("r", "o-"+st))
			_, m := console.create(refMemo("r", "o-"+st, "x", ghRef("9")))
			e.setStateKeepingRelease(m.ID, st)
			if code, again := console.create(refMemo("r", "o-"+st, "y", ghRef("9"))); code != http.StatusOK || again.ID != m.ID || !again.Existing {
				t.Fatalf("while %s: %d %+v", st, code, again)
			}
		}
		e.assertPendingReleaseMatches()
	})
}

func (e *inboxEnv) listMemos(repo, category string) []store.Memo {
	e.t.Helper()
	rows, err := e.st.ListMemos(context.Background(), e.members["a"], "")
	if err != nil {
		e.t.Fatal(err)
	}
	var out []store.Memo
	for _, m := range rows {
		if m.Repo == repo && m.Category == category {
			out = append(out, m)
		}
	}
	return out
}

func (e *inboxEnv) markSent(id string) {
	e.t.Helper()
	if err := e.st.MarkMemosSent(context.Background(), e.members["a"], []string{id}, store.NowTS()); err != nil {
		e.t.Fatal(err)
	}
}

// setStateKeepingRelease moves a row to a state the way the delivery statements do, keeping the
// pending row's copy of its release (the CHECK of migration 0131 and 0138).
func (e *inboxEnv) setStateKeepingRelease(id, state string) {
	e.t.Helper()
	if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET state=? WHERE id=?`, state, id); err != nil {
		e.t.Fatal(err)
	}
}

// Two adds of one reference at once leave one message, and every answer names it. The database's
// partial unique index holds even when the check of the API is skipped.
func TestMemoReferenceConcurrentAddsLeaveOne(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		const n = 8
		var wg sync.WaitGroup
		type res struct {
			code int
			id   string
		}
		out := make([]res, n)
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				face := []string{"console", "token"}[i%2]
				code, m := memoFaceClient{face, e}.create(refMemo("r", "q", "note "+strconv.Itoa(i), ghRef("40")))
				out[i] = res{code, m.ID}
			}(i)
		}
		close(start)
		wg.Wait()
		created := 0
		for _, r := range out {
			if r.code == http.StatusCreated {
				created++
			} else if r.code != http.StatusOK {
				t.Fatalf("answers %+v", out)
			}
			if r.id != out[0].id {
				t.Fatalf("the answers name different messages: %+v", out)
			}
		}
		if created != 1 || e.memoCount("r", "q") != 1 {
			t.Fatalf("%d created, %d rows: %+v", created, e.memoCount("r", "q"), out)
		}

		// The index: a second open row with the same reference is refused by the database itself,
		// and a closed one is not.
		row := e.memoRow(out[0].id)
		ins := func(id, state string) error {
			_, err := e.st.DB().ExecContext(context.Background(), `INSERT INTO memo(id, membership_id, repo, category, kind, body, created_at, state, provenance, ref_provider, ref_key, ref_url)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, id, row.MembershipID, row.Repo, row.Category, "text", "x", store.NowTS(), state, "workspace", row.RefProvider, row.RefKey, row.RefURL)
			return err
		}
		for _, st := range []string{store.MemoStateHeld, store.MemoStatePending, store.MemoStateClaimed} {
			if err := ins(store.NewID(), st); err == nil {
				t.Fatalf("the database took a second open (%s) message with the same reference", st)
			}
		}
		// The index compares the key in lower case too, for a writer that skips the API.
		if _, err := e.st.DB().ExecContext(context.Background(), `INSERT INTO memo(id, membership_id, repo, category, kind, body, created_at, state, provenance, ref_provider, ref_key, ref_url)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, store.NewID(), row.MembershipID, row.Repo, row.Category, "text", "x", store.NowTS(), store.MemoStateHeld, "workspace",
			row.RefProvider, strings.ToUpper(row.RefKey), row.RefURL); err == nil {
			t.Fatal("the database took the same key in another case")
		}
		for _, st := range []string{store.MemoStateDelivered, store.MemoStateUnconfirmed, store.MemoStateSuperseded, ""} {
			if err := ins(store.NewID(), st); err != nil {
				t.Fatalf("a %q row was refused: %v", st, err)
			}
		}
	})
}

// A pending row's copy of the released reference is held by the database: a write that leaves
// another reference under a release is refused, the way it is for the body.
func TestMemoReferenceReleaseCopyIsChecked(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		_, m := memoFaceClient{"console", e}.create(refMemo("r", "q", "x", ghRef("3")))
		if got := e.memoRow(m.ID); got.State != store.MemoStatePending {
			t.Fatalf("not pending: %+v", got)
		}
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET ref_key='acme/app#4' WHERE id=?`, m.ID); err == nil {
			t.Fatal("the database took a new reference under a release")
		}
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET ref_url='https://x.example/y' WHERE id=?`, m.ID); err == nil {
			t.Fatal("the database took a new url under a release")
		}
	})
}

// A hand-sent message that carries a reference says so in the text, so a session sees it.
func TestMemoReferenceInTheHandSentText(t *testing.T) {
	m := store.Memo{Kind: "text", Body: "look at this", RefProvider: "github", RefKey: "acme/app#1", RefURL: refURL}
	if got := buildFlushMessage([]store.Memo{m}); !strings.Contains(got, "look at this") || !strings.Contains(got, "acme/app#1") || !strings.Contains(got, refURL) {
		t.Fatalf("with a body: %q", got)
	}
	m.Body = ""
	if got := buildFlushMessage([]store.Memo{m}); !strings.Contains(got, "acme/app#1") || strings.Contains(got, "（画像）") {
		t.Fatalf("reference only: %q", got)
	}
}

// --- The fire --------------------------------------------------------------------------------

func (e *deliveryEnv) createdItems(a *fakeQueueAgent, n int) (map[string]any, []map[string]any) {
	e.t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.creates) <= n {
		e.t.Fatalf("%d creates, want more than %d", len(a.creates), n)
	}
	c := a.creates[n]
	qd, _ := c["queue_delivery"].(map[string]any)
	var items []map[string]any
	for _, it := range qd["items"].([]any) {
		items = append(items, it.(map[string]any))
	}
	return c, items
}

// The reference travels with the item. Exactly one reference between the items is the session's
// work item, and none or two are not.
func TestMemoReferenceFireWorkItem(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"maxActive": 6, "batch": 3})
		agent := e.useQueueAgent("")
		agent.setRefs(true)
		r := e.runner()
		console := memoFaceClient{"console", e.inboxEnv}
		add := func(body string, ref map[string]any) memoDTO {
			code, m := console.create(refMemo("app", "impl", body, ref))
			if code != http.StatusCreated {
				t.Fatalf("create: %d", code)
			}
			return m
		}
		fire := func() {
			e.fired(q)
			e.tick(r, time.Now())
		}

		// Zero references.
		add("plain", nil)
		fire()
		c, items := e.createdItems(agent, 0)
		if _, has := c["work_item"]; has || items[0]["reference"] != nil {
			t.Fatalf("no reference: %v", c)
		}
		// Exactly one, with a plain note beside it.
		ref := add("", ghRef("12"))
		add("a note", nil)
		fire()
		c, items = e.createdItems(agent, 1)
		wi, _ := c["work_item"].(map[string]any)
		if len(wi) != 2 || wi["provider"] != "github" || wi["key"] != "acme/app#12" {
			t.Fatalf("one reference: work_item %v", c["work_item"])
		}
		var carried map[string]any
		for _, it := range items {
			if it["id"] == ref.ID {
				carried, _ = it["reference"].(map[string]any)
				if it["body"] != "" {
					t.Fatalf("a reference-only item carries a body: %v", it)
				}
			}
		}
		if carried["provider"] != "github" || carried["key"] != "acme/app#12" || carried["url"] != "https://github.com/acme/app/issues/12" {
			t.Fatalf("item reference %v", carried)
		}
		// Two: none is the work item, both travel.
		add("", ghRef("13"))
		add("", ghRef("14"))
		fire()
		c, items = e.createdItems(agent, 2)
		if _, has := c["work_item"]; has {
			t.Fatalf("two references made %v the work item", c["work_item"])
		}
		n := 0
		for _, it := range items {
			if it["reference"] != nil {
				n++
			}
		}
		if n != 2 {
			t.Fatalf("%d items carry a reference, want 2", n)
		}
	})
}

// An Agent without the marker is never handed a reference. The messages stay where they are, the
// queue says why, and the rest of the queue still fires. Once the Agent sends the marker the
// messages go.
func TestMemoReferenceNeverSentToAnAgentWithoutTheMarker(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"maxActive": 6, "enabled": true})
		agent := e.useQueueAgent("")
		agent.mu.Lock()
		agent.report = true // it reports the queue, and still cannot show a reference
		agent.mu.Unlock()
		r := e.runner()
		_, ref := memoFaceClient{"console", e.inboxEnv}.create(refMemo("app", "impl", "", ghRef("21")))

		// The press claims it (nothing yet says the Agent cannot), and the send finds out.
		d := e.fired(q)
		e.tick(r, time.Now())
		if agent.createCount() != 0 {
			t.Fatalf("a reference was sent to an Agent without the marker: %v", agent.creates)
		}
		if got := e.delivery(d.ID); got.State != store.DeliveryClosed || got.Outcome != store.DeliveryNotSent {
			t.Fatalf("delivery %+v", got)
		}
		if m := e.memoRow(ref.ID); m.State != store.MemoStatePending || m.DeliveryID != "" {
			t.Fatalf("the message did not stay where it was: %+v", m)
		}
		queue := e.queueRow(q)
		if queue.NoRefsAt == "" {
			t.Fatal("the queue does not say the Agent cannot show a reference")
		}
		// The member sees why: the list says so, and a press now has nothing to send.
		list := decode[memoQueuesWire](t, e.do("a", "GET", "/api/memo-queues", nil)).Queues
		if len(list) != 1 || list[0].NoRefsAt == "" {
			t.Fatalf("list: %+v", list)
		}
		// A press is a try of the member's own: it claims the reference, the send asks the Agent
		// again, and it still has nothing to send.
		d1 := e.fired(q)
		e.tick(r, time.Now())
		if got := e.delivery(d1.ID); got.State != store.DeliveryClosed || got.Outcome != store.DeliveryNotSent || agent.createCount() != 0 {
			t.Fatalf("the second press: %+v, %d creates", got, agent.createCount())
		}
		// A fire by itself skips it while the mark is set, and fires what else is waiting.
		_, plain := memoFaceClient{"console", e.inboxEnv}.create(textMemo("app", "impl", "plain"))
		later := time.Now().Add(time.Hour) // past the back-off of the refused sends
		auto, _ := r.dispatch(context.Background(), later, 4)
		if len(auto) != 1 || len(auto[0].MessageIDs) != 1 || auto[0].MessageIDs[0] != plain.ID {
			t.Fatalf("the plain note was not fired alone: %+v", auto)
		}
		for _, d := range auto {
			r.send(context.Background(), d)
		}
		if agent.createCount() != 1 {
			t.Fatalf("%d creates, want the plain note's", agent.createCount())
		}
		if m := e.memoRow(ref.ID); m.State != store.MemoStatePending {
			t.Fatalf("the reference moved: %+v", m)
		}

		// The Agent is upgraded: a running workspace read clears the mark and the message goes.
		agent.setRefs(true)
		r = e.runner() // a new tick reads the workspace again
		auto, _ = r.dispatch(context.Background(), later, 4)
		if got := e.queueRow(q); got.NoRefsAt != "" {
			t.Fatalf("the mark stayed after the Agent sent the marker: %+v", got)
		}
		for _, d := range auto {
			r.send(context.Background(), d)
		}
		if agent.createCount() != 2 {
			t.Fatalf("%d creates, want the reference's as the second: %v", agent.createCount(), agent.creates)
		}
		_, items := e.createdItems(agent, 1)
		if items[0]["id"] != ref.ID || items[0]["reference"] == nil {
			t.Fatalf("items %v", items)
		}

		// The queue's own reason, with no back-off from the refused send in the way: only
		// references wait, and the mark is set.
		q2 := e.queueWith("app", "impl2", map[string]any{"enabled": true})
		memoFaceClient{"console", e.inboxEnv}.create(refMemo("app", "impl2", "", ghRef("22")))
		if err := e.st.SetMemoQueueNoRefs(context.Background(), q2, store.NowTS()); err != nil {
			t.Fatal(err)
		}
		for _, w := range decode[memoQueuesWire](t, e.do("a", "GET", "/api/memo-queues", nil)).Queues {
			if w.CategoryID == q2 && (w.Waiting == nil || w.Waiting.Reason != waitNoRefs) {
				t.Fatalf("waiting %+v, want %s", w.Waiting, waitNoRefs)
			}
		}
	})
}

// A flush settles a queue message as delivered only when the row still holds what was sent, and
// a reference is part of that.
func TestMemoReferenceIsPartOfWhatWasSent(t *testing.T) {
	sent := store.Memo{Kind: "text", Body: "x", RefProvider: "github", RefKey: "a/b#1", RefURL: refURL}
	if !memoSameSentContent(sent, sent) {
		t.Fatal("the same row differs from itself")
	}
	for _, edit := range []func(*store.Memo){
		func(m *store.Memo) { m.RefKey = "a/b#2" },
		func(m *store.Memo) { m.RefURL += "x" },
		func(m *store.Memo) { m.RefProvider, m.RefKey, m.RefURL = "", "", "" },
	} {
		cur := sent
		edit(&cur)
		if memoSameSentContent(cur, sent) {
			t.Fatalf("a changed reference counts as what was sent: %+v", cur)
		}
	}
}

// A later version of the marker still means the Agent can show a reference, and a clear of the
// queue's mark is written even when the queue row the tick read is older than the database.
func TestMemoReferenceMarkerVersionAndStaleClear(t *testing.T) {
	if (agentSessionsEnvelope{}).showsReferences() || !(agentSessionsEnvelope{QueueRefs: 1}).showsReferences() || !(agentSessionsEnvelope{QueueRefs: 2}).showsReferences() {
		t.Fatal("showsReferences")
	}
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"enabled": true})
		if err := e.st.SetMemoQueueNoRefs(context.Background(), q, store.NowTS()); err != nil {
			t.Fatal(err)
		}
		stale := store.MemoQueue{CategoryID: q} // read before the mark was set
		e.runner().setNoRefs(context.Background(), stale, false)
		if got := e.queueRow(q); got.NoRefsAt != "" {
			t.Fatalf("the mark stayed: %+v", got)
		}
	})
}

// The /mcp memo tools take the same reference as the other faces, through the same validation:
// set, change, clear, refuse, and the duplicate. Whatever they write enters held with provenance
// mcp, a reference-only message included.
func TestMemoReferenceMCPTools(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		f := memoFaceClient{"mcp", e}

		code, m := f.create(refMemo("r", "q", "", ghRef("1")))
		if code != http.StatusCreated || m.Reference == nil || m.Reference.Key != "acme/app#1" || m.Body != "" || m.Existing {
			t.Fatalf("create: %d %+v", code, m)
		}
		if row := e.memoRow(m.ID); row.State != store.MemoStateHeld || row.Provenance != store.MemoProvenanceMCP || row.RefProvider != "github" {
			t.Fatalf("a reference from /mcp entered %+v", row)
		}

		// The same item again in an auto-run category answers the one already there.
		code, again := f.create(refMemo("r", "q", "other words", refOf("GitHub", "ACME/app#1", refURL)))
		if code != http.StatusOK || again.ID != m.ID || !again.Existing {
			t.Fatalf("duplicate: %d %+v", code, again)
		}
		if n := e.memoCount("r", "q"); n != 1 {
			t.Fatalf("%d messages, want 1", n)
		}

		// Invalid: the Control Plane's own validation, as on the other faces.
		for _, bad := range []map[string]any{
			refOf("gitlab", "acme/app#2", refURL),
			refOf("github", "#2", refURL),
			refOf("github", "acme/app#2", ""),
			refOf("github", "acme/app#2", "javascript:alert(1)"),
		} {
			if code, _ := f.create(refMemo("r", "q", "x", bad)); code == http.StatusCreated {
				t.Fatalf("an invalid reference %v was stored", bad)
			}
		}
		if n := e.memoCount("r", "q"); n != 1 {
			t.Fatalf("an invalid reference left %d messages", n)
		}

		// Update: absent leaves it, a full object sets it, {} clears it.
		if code, up := f.update(m.ID, map[string]any{"body": "note"}); code != http.StatusOK || up.Reference == nil || up.Reference.Key != "acme/app#1" {
			t.Fatalf("absent: %d %+v", code, up)
		}
		if code, up := f.update(m.ID, map[string]any{"reference": ghRef("3")}); code != http.StatusOK || up.Reference == nil || up.Reference.Key != "acme/app#3" {
			t.Fatalf("set: %d %+v", code, up)
		}
		if code, _ := f.update(m.ID, map[string]any{"reference": refOf("github", "acme/app#3", "")}); code == http.StatusOK {
			t.Fatal("a partial reference was accepted")
		}
		// Another message holding the reference makes a set a duplicate.
		_, other := f.create(refMemo("r", "q", "y", ghRef("4")))
		if code, _ := f.update(other.ID, map[string]any{"reference": ghRef("3")}); code != http.StatusConflict {
			t.Fatalf("update onto an open reference: %d", code)
		}
		if code, up := f.update(m.ID, map[string]any{"reference": map[string]any{}}); code != http.StatusOK || up.Reference != nil || up.Body != "note" {
			t.Fatalf("clear: %d %+v", code, up)
		}
		if row := e.memoRow(m.ID); row.RefKey != "" || row.State != store.MemoStateHeld {
			t.Fatalf("after clear: %+v", row)
		}
	})
}
