package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/mcpsrv"
	"github.com/k-k1/agent-fleet/control-plane/internal/memorecipe"
	"github.com/k-k1/agent-fleet/control-plane/internal/memotidy"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// A run recipe a queue message carries (ADR 0137, addendum 2026-11-23, #4845).

func TestMemoEffectiveRecipe(t *testing.T) {
	q := store.MemoQueue{AgentKind: "claude", Model: "claude-x", Effort: "high", Route: ""}
	for _, tc := range []struct {
		name   string
		recipe memorecipe.Recipe
		want   memorecipe.Recipe
	}{
		{"none uses the category", memorecipe.Recipe{}, memorecipe.Recipe{AgentKind: "claude", Model: "claude-x", Effort: "high"}},
		{"field by field", memorecipe.Recipe{Effort: "low"}, memorecipe.Recipe{AgentKind: "claude", Model: "claude-x", Effort: "low"}},
		{"the same agent keeps the category's model", memorecipe.Recipe{AgentKind: "claude", Model: "claude-y"}, memorecipe.Recipe{AgentKind: "claude", Model: "claude-y", Effort: "high"}},
		{"another agent does not inherit the model or effort", memorecipe.Recipe{AgentKind: "codex"}, memorecipe.Recipe{AgentKind: "codex"}},
		{"another agent with its own model", memorecipe.Recipe{AgentKind: "codex", Model: "m", Effort: "low"}, memorecipe.Recipe{AgentKind: "codex", Model: "m", Effort: "low"}},
	} {
		if got := memorecipe.Effective(q, store.Memo{Recipe: tc.recipe.Encode()}); got != tc.want {
			t.Errorf("%s: got %+v want %+v", tc.name, got, tc.want)
		}
	}
	if got := memorecipe.Effective(store.MemoQueue{}, store.Memo{}); got.AgentKind != "claude" {
		t.Errorf("a queue with no kind runs claude, got %+v", got)
	}
}

// A message without a recipe hashes as it always did; one with a recipe hashes differently for each.
func TestMemoRecipeInTheContentHash(t *testing.T) {
	m := store.Memo{Kind: "text", Body: "do it", Repo: "app", Category: "impl"}
	plain := memoContentHash(m)
	b, _ := json.Marshal([]string{"memo-content/v1", "text", "do it", "", "", "app", "impl"})
	if sum := sha256.Sum256(b); plain != hex.EncodeToString(sum[:]) {
		t.Fatal("a message without a recipe hashes differently than before the column")
	}
	m.Recipe = memorecipe.Recipe{Effort: "high"}.Encode()
	a := memoContentHash(m)
	m.Recipe = memorecipe.Recipe{Effort: "low"}.Encode()
	if plain == a || a == memoContentHash(m) {
		t.Fatal("the recipe is not part of the content hash")
	}
}

func (e *deliveryEnv) recipeMemo(repo, cat, text string, extra map[string]any) *httptest.ResponseRecorder {
	body := textMemo(repo, cat, text)
	for k, v := range extra {
		body[k] = v
	}
	return e.do("a", "POST", "/api/memos", body)
}

// A refused value is a 400 and nothing is stored; a note outside every queue is refused rather than
// silently losing the recipe.
func TestMemoRecipeValidation(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		e.queueWith("app", "impl", nil)
		for _, tc := range []struct {
			name  string
			extra map[string]any
		}{
			{"unknown agent", map[string]any{"agentKind": "nope"}},
			{"shell is no consumer", map[string]any{"agentKind": "shell"}},
			{"bad model", map[string]any{"model": "bad model!"}},
			{"bad effort", map[string]any{"effort": "hi gh"}},
			{"unknown route", map[string]any{"route": "no-such-route"}},
		} {
			rec := e.recipeMemo("app", "impl", "x", tc.extra)
			if rec.Code != http.StatusBadRequest || deliveryErrCode(t, rec) != errCodeMemoRecipeInvalid {
				t.Errorf("%s: %d %s", tc.name, rec.Code, rec.Body.String())
			}
		}
		if rec := e.recipeMemo("app", "plain", "x", map[string]any{"effort": "low"}); rec.Code != http.StatusBadRequest || deliveryErrCode(t, rec) != errCodeMemoRecipeInvalid {
			t.Errorf("outside a queue: %d %s", rec.Code, rec.Body.String())
		}
		rows, _ := e.st.ListMemos(context.Background(), e.members["a"], "")
		if len(rows) != 0 {
			t.Fatalf("a refused create stored %d rows", len(rows))
		}
		rec := e.recipeMemo("app", "impl", "ok", map[string]any{"agentKind": "codex", "model": "m1", "effort": "low"})
		got := decode[memoDTO](t, rec)
		if rec.Code != http.StatusCreated || got.AgentKind != "codex" || got.Model != "m1" || got.Effort != "low" || got.State != store.MemoStatePending {
			t.Fatalf("create: %d %+v", rec.Code, got)
		}
		if listed := e.listed(got.ID); listed.AgentKind != "codex" || listed.Model != "m1" || listed.Effort != "low" || listed.ContentHash != got.ContentHash {
			t.Fatalf("listed %+v", listed)
		}
		// A refused patch changes nothing.
		if rec := e.do("a", "PATCH", "/api/memos/"+got.ID, map[string]any{"effort": "no good"}); rec.Code != http.StatusBadRequest {
			t.Fatalf("bad patch: %d %s", rec.Code, rec.Body.String())
		}
		if row := e.memoRow(got.ID); memorecipe.Of(row).Effort != "low" {
			t.Fatalf("a refused patch changed the recipe: %q", row.Recipe)
		}
	})
}

// The Console's edit re-records the member's own release, a session's edit sends it back to held,
// "" clears a field, and leaving the queue drops the recipe.
func TestMemoRecipeEdits(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		e.queueWith("app", "impl", nil)
		m := e.pending("app", "impl", "one")
		rec := e.do("a", "PATCH", "/api/memos/"+m.ID, map[string]any{"effort": "high", "model": "m1"})
		got := decode[memoDTO](t, rec)
		if rec.Code != http.StatusOK || got.State != store.MemoStatePending || got.Effort != "high" || got.ContentHash == m.ContentHash {
			t.Fatalf("console patch: %d %+v", rec.Code, got)
		}
		e.assertPendingReleaseMatches()
		rec = e.memoTokenDo("a", "PATCH", "/internal/memos/"+m.ID, map[string]any{"effort": "low"})
		if got := decode[memoDTO](t, rec); rec.Code != http.StatusOK || got.State != store.MemoStateHeld || got.Effort != "low" || got.Model != "m1" {
			t.Fatalf("a session's change of the recipe kept the release: %d %+v", rec.Code, got)
		}
		rec = e.do("a", "PATCH", "/api/memos/"+m.ID, map[string]any{"effort": "", "model": ""})
		if got := decode[memoDTO](t, rec); rec.Code != http.StatusOK || got.Effort != "" || got.Model != "" || e.memoRow(m.ID).Recipe != "" {
			t.Fatalf("clear: %d %+v", rec.Code, got)
		}
		e.do("a", "PATCH", "/api/memos/"+m.ID, map[string]any{"effort": "low"})
		rec = e.do("a", "PATCH", "/api/memos/"+m.ID, map[string]any{"category": "notes"})
		if got := decode[memoDTO](t, rec); rec.Code != http.StatusOK || got.State != "" || got.Effort != "" || e.memoRow(m.ID).Recipe != "" {
			t.Fatalf("moved out: %d %+v", rec.Code, got)
		}
	})
}

// The /mcp face sets and refuses a recipe through the same core.
func TestMemoRecipeOverMCP(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		e.queueWith("app", "impl", nil)
		s := func(v string) *string { return &v }
		d := cpDeps{e.mgr}
		out, err := d.MemoCreate(context.Background(), store.MembershipView{MembershipID: e.members["a"]}, "conn-1", "app", "impl", "text", "hello", "", nil, nil, "",
			mcpsrv.MemoRecipe{Effort: s("high")}, false)
		if err != nil || out.(memoDTO).Effort != "high" || out.(memoDTO).State != store.MemoStateHeld {
			t.Fatalf("create: %v %+v", err, out)
		}
		if _, err := d.MemoCreate(context.Background(), store.MembershipView{MembershipID: e.members["a"]}, "conn-1", "app", "impl", "text", "hello2", "", nil, nil, "",
			mcpsrv.MemoRecipe{AgentKind: s("nope")}, false); err == nil {
			t.Fatal("a bad agent was accepted")
		}
		id := out.(memoDTO).ID
		up, err := d.MemoUpdate(context.Background(), e.members["a"], id, nil, nil, nil, nil, nil, nil, nil, nil, mcpsrv.MemoRecipe{Effort: s("")})
		if err != nil || up.(memoDTO).Effort != "" {
			t.Fatalf("update: %v %+v", err, up)
		}
	})
}

func recipeCreate(c map[string]any) (kind, model, effort string) {
	kind, _ = c["kind"].(string)
	model, _ = c["model"].(string)
	effort, _ = c["effort"].(string)
	return
}

// A run has one recipe: a batch groups only messages whose effective recipes are equal, and a
// message with another one waits for its own run, oldest first otherwise. The session is created
// with the effective recipe: the message's fields over the category's.
func TestMemoRecipeBatchingAndDelivery(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"batch": 5, "maxActive": 3, "model": "claude-x", "effort": "high"})
		a := e.pending("app", "impl", "a")
		time.Sleep(1100 * time.Millisecond)
		b := decode[memoDTO](t, e.recipeMemo("app", "impl", "b", map[string]any{"effort": "low"}))
		time.Sleep(1100 * time.Millisecond)
		c := e.pending("app", "impl", "c")
		// Spelling the category's own value out is the same effective recipe.
		time.Sleep(1100 * time.Millisecond)
		d := decode[memoDTO](t, e.recipeMemo("app", "impl", "d", map[string]any{"effort": "high"}))
		agent := e.useQueueAgent("")

		first := e.fired(q)
		if !sameIDs(first.MessageIDs, a.ID, c.ID, d.ID) {
			t.Fatalf("first batch %v, want a, c and d", first.MessageIDs)
		}
		second := e.fired(q)
		if !sameIDs(second.MessageIDs, b.ID) {
			t.Fatalf("second batch %v, want b alone", second.MessageIDs)
		}
		r := e.runner()
		e.tick(r, time.Now())
		if agent.createCount() != 2 {
			t.Fatalf("%d creates", agent.createCount())
		}
		got := map[string]string{}
		for _, c := range agent.creates {
			k, m, ef := recipeCreate(c)
			qd, _ := c["queue_delivery"].(map[string]any)
			got[qd["id"].(string)] = k + "/" + m + "/" + ef
		}
		if got[first.ID] != "claude/claude-x/high" || got[second.ID] != "claude/claude-x/low" {
			t.Fatalf("creates %v", got)
		}
	})
}

// A message that names another agent runs on it, without the category's model, and not with the
// category's other messages.
func TestMemoRecipeOtherAgent(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"batch": 5, "maxActive": 3, "model": "claude-x", "effort": "high"})
		x := decode[memoDTO](t, e.recipeMemo("app", "impl", "x", map[string]any{"agentKind": "codex"}))
		time.Sleep(1100 * time.Millisecond)
		y := e.pending("app", "impl", "y")
		agent := e.useQueueAgent("")
		d1 := e.fired(q)
		if !sameIDs(d1.MessageIDs, x.ID) {
			t.Fatalf("batch %v", d1.MessageIDs)
		}
		d2 := e.fired(q)
		if !sameIDs(d2.MessageIDs, y.ID) {
			t.Fatalf("batch %v", d2.MessageIDs)
		}
		e.tick(e.runner(), time.Now())
		seen := 0
		for _, c := range agent.creates {
			qd, _ := c["queue_delivery"].(map[string]any)
			k, m, ef := recipeCreate(c)
			switch qd["id"] {
			case d1.ID:
				seen++
				if k != "codex" || m != "" || ef != "" {
					t.Errorf("codex run: %s/%s/%s", k, m, ef)
				}
			case d2.ID:
				seen++
				if k != "claude" || m != "claude-x" || ef != "high" {
					t.Errorf("default run: %s/%s/%s", k, m, ef)
				}
			}
		}
		if seen != 2 {
			t.Fatalf("saw %d creates", seen)
		}
	})
}

// A tidy run never merges messages whose effective recipes differ, and what it derives keeps the
// recipe of its sources.
func TestMemoRecipeInTidy(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.category("r", "q")
		e.tidyQueue(q)
		row := func(body, effort string) store.Memo {
			m := store.Memo{ID: store.NewID(), MembershipID: e.members["a"], Repo: "r", Category: "q", Kind: "text", Body: body,
				CreatedAt: store.NowTS(), State: store.MemoStatePending, Provenance: "member", Recipe: memorecipe.Recipe{Effort: effort}.Encode()}
			m.ReleaseHash, m.ReleasedAt = memoContentHash(m), store.NowTS()
			if err := e.st.CreateMemo(context.Background(), m); err != nil {
				t.Fatal(err)
			}
			return e.memoRow(m.ID)
		}
		a, b, c := row("one", "low"), row("two", "high"), row("three", "low")
		out, err := memoTidyApply(context.Background(), e.st, memoTidyApplyIn{MembershipID: e.members["a"], QueueID: q, Trigger: store.TidyTriggerManual,
			Input: tidyItems(a, b), Ops: []memotidy.Op{merge("both", a.ID, b.ID)}})
		if err != nil || out.Run.State != store.TidyRunDropped || out.Run.Reason != memotidy.CodeMixedGroup {
			t.Fatalf("a merge across recipes: %v %+v", err, out.Run)
		}
		out = e.tidyApply(q, store.TidyTriggerManual, []store.Memo{a, c}, nil, merge("both", a.ID, c.ID))
		if out.Run.State != store.TidyRunApplied || len(out.Results) != 1 {
			t.Fatalf("a merge of equal recipes: %+v", out.Run)
		}
		if m := e.memoRow(out.Results[0]); memorecipe.Of(m).Effort != "low" || m.ReleaseHash != memoContentHash(m) {
			t.Fatalf("merged %+v", m)
		}
		out = e.tidyApply(q, store.TidyTriggerManual, []store.Memo{b}, nil, memotidy.Op{Op: memotidy.OpSplit, ID: b.ID, Texts: []string{"x", "y"}})
		if out.Run.State != store.TidyRunApplied || len(out.Results) != 2 {
			t.Fatalf("split: %+v", out.Run)
		}
		for _, id := range out.Results {
			if m := e.memoRow(id); memorecipe.Of(m).Effort != "high" {
				t.Fatalf("a split lost the recipe: %+v", m)
			}
		}
		e.assertPendingReleaseMatches()
	})
}

// A continuation runs as the message it continues unless it names a recipe of its own.
func TestMemoRecipeContinuationInherits(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.contQueue("impl", -1, nil)
		body := contMemo("impl", "part 1", "acme/app#1")
		body["effort"], body["model"] = "high", "m1"
		rec := e.do("a", "POST", "/api/memos", body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("root: %d %s", rec.Code, rec.Body.String())
		}
		root := e.memoRow(decode[memoDTO](t, rec).ID)
		d := e.contRun(q, "run-1", store.DeliveryOpen, root.ID)
		next := e.contSend("impl", "part 2", "acme/app#1", "queue", "run-1", d)
		if next.State != store.MemoStatePending || next.Recipe != root.Recipe || next.ReleaseHash != memoContentHash(next) {
			t.Fatalf("inherited: %+v (root recipe %q)", next, root.Recipe)
		}
		// One that names its own recipe keeps it, whole: the root's fields are not mixed in.
		body3 := contMemo("impl", "root 3", "acme/app#3")
		body3["effort"], body3["model"] = "high", "m1"
		root3 := e.memoRow(decode[memoDTO](t, e.do("a", "POST", "/api/memos", body3)).ID)
		d3 := e.contRun(q, "run-3", store.DeliveryOpen, root3.ID)
		m := contMemo("impl", "part 2", "acme/app#3")
		m["producer"], m["producerOrigin"], m["continuation"], m["queueDelivery"] = "run-3", "queue", true, d3
		m["effort"] = "low"
		rec = e.memoTokenDo("a", "POST", "/internal/memos", m)
		if rec.Code != http.StatusCreated {
			t.Fatalf("own recipe: %d %s", rec.Code, rec.Body.String())
		}
		own := e.memoRow(decode[memoDTO](t, rec).ID)
		if r := memorecipe.Of(own); own.State != store.MemoStatePending || r.Effort != "low" || r.Model != "" {
			t.Fatalf("own recipe: %+v", own)
		}
	})
}

// With the queue's accept-from-own-sessions switch on, a session's change of a released message's
// recipe still sends it back to held: the switch accepted the message that was released, not the
// settings a later edit chose. A change of the text alone keeps the switch's release.
func TestMemoRecipeEditUnderTheSwitchGoesBackToHeld(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		e.queueWith("app", "impl", map[string]any{"acceptOwnSessions": true})
		body := textMemo("app", "impl", "from a session")
		body["producer"], body["producerOrigin"] = "run-1", "user"
		rec := e.memoTokenDo("a", "POST", "/internal/memos", body)
		m := decode[memoDTO](t, rec)
		if rec.Code != http.StatusCreated || m.State != store.MemoStatePending {
			t.Fatalf("create: %d %+v", rec.Code, m)
		}
		rec = e.memoTokenDo("a", "PATCH", "/internal/memos/"+m.ID, map[string]any{"body": "reworded"})
		if got := decode[memoDTO](t, rec); got.State != store.MemoStatePending {
			t.Fatalf("a text edit lost the switch's release: %+v", got)
		}
		rec = e.memoTokenDo("a", "PATCH", "/internal/memos/"+m.ID, map[string]any{"effort": "max"})
		if got := decode[memoDTO](t, rec); rec.Code != http.StatusOK || got.State != store.MemoStateHeld || got.Effort != "max" {
			t.Fatalf("a recipe change kept the release: %d %+v", rec.Code, got)
		}
		e.assertPendingReleaseMatches()
	})
}

// A held message with a recipe is released with its current hash (a stale card is refused), and a
// claim that is returned, or a delivery that is not sent, puts it back pending under the CHECK.
func TestMemoRecipeReleaseAndReturn(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"maxActive": 2})
		body := textMemo("app", "impl", "held one")
		body["producer"], body["producerOrigin"] = "run-1", "queue"
		body["effort"], body["route"] = "low", "anthropic-subscription"
		rec := e.memoTokenDo("a", "POST", "/internal/memos", body)
		m := decode[memoDTO](t, rec)
		if rec.Code != http.StatusCreated || m.State != store.MemoStateHeld {
			t.Fatalf("create: %d %+v", rec.Code, m)
		}
		stale := m.ContentHash
		if rec := e.do("a", "PATCH", "/api/memos/"+m.ID, map[string]any{"effort": "high"}); rec.Code != http.StatusOK {
			t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
		}
		release := func(hash string) int {
			return e.do("a", "POST", "/api/memos/release", map[string]any{"items": []map[string]any{{"id": m.ID, "contentHash": hash}}}).Code
		}
		if code := release(stale); code == http.StatusOK && e.memoRow(m.ID).State == store.MemoStatePending {
			t.Fatal("a stale card released the message")
		}
		cur := e.listed(m.ID)
		if code := release(cur.ContentHash); code != http.StatusOK || e.memoRow(m.ID).State != store.MemoStatePending {
			t.Fatalf("release: %d %+v", code, e.memoRow(m.ID))
		}
		agent := e.useQueueAgent("")
		d := e.fired(q)
		if !sameIDs(d.MessageIDs, m.ID) {
			t.Fatalf("batch %v", d.MessageIDs)
		}
		// The release copy is empty while the row is claimed (a row that is not pending may hold
		// none): the return has to copy it back for the row to be pending again under the CHECK.
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET release_recipe='' WHERE id=?`, m.ID); err != nil {
			t.Fatal(err)
		}
		if err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error { return tx.ReturnDeliveryMessages(d.ID) }); err != nil {
			t.Fatalf("return: %v", err)
		}
		if row := e.memoRow(m.ID); row.State != store.MemoStatePending || row.ReleaseHash != memoContentHash(row) {
			t.Fatalf("returned row %+v", row)
		}
		e.assertPendingReleaseMatches()
		// A message with a route is sent with it.
		d2 := e.fired(q)
		e.tick(e.runner(), time.Now())
		sent := false
		for _, c := range agent.creates {
			if qd, _ := c["queue_delivery"].(map[string]any); qd["id"] == d2.ID {
				r, _ := c["route"].(map[string]any)
				sent = r["route"] == "anthropic-subscription" && c["effort"] == "high"
			}
		}
		if !sent {
			t.Fatalf("the create did not carry the route: %v", agent.creates)
		}
	})
}

// A change of the category's defaults between the claim and the send, which would split a batch
// that shared a recipe, sends nothing: the messages go back and are grouped again.
func TestMemoRecipeBatchSplitByASettingsChangeIsNotSent(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"batch": 5, "effort": "high"})
		a := e.pending("app", "impl", "a")
		time.Sleep(1100 * time.Millisecond)
		b := decode[memoDTO](t, e.recipeMemo("app", "impl", "b", map[string]any{"effort": "high"}))
		agent := e.useQueueAgent("")
		d := e.fired(q)
		if !sameIDs(d.MessageIDs, a.ID, b.ID) {
			t.Fatalf("batch %v", d.MessageIDs)
		}
		e.queueWith("app", "impl", map[string]any{"batch": 5, "effort": "low"})
		e.tick(e.runner(), time.Now())
		if agent.createCount() != 0 {
			t.Fatalf("a split batch was sent: %v", agent.creates)
		}
		for _, id := range []string{a.ID, b.ID} {
			if row := e.memoRow(id); row.State != store.MemoStatePending || row.DeliveryID != "" {
				t.Fatalf("%s not returned: %+v", id, row)
			}
		}
	})
}

// A message another recipe's problem cannot stop this run: the oversized message with a different
// recipe is skipped before its text is checked.
func TestMemoRecipeOtherRecipeIsSkippedBeforeItsTextIsChecked(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"batch": 5, "maxActive": 3})
		a := e.pending("app", "impl", "a")
		time.Sleep(1100 * time.Millisecond)
		b := decode[memoDTO](t, e.recipeMemo("app", "impl", "b", map[string]any{"effort": "low"}))
		big := strings.Repeat("x", memoDeliveryItemBodyMax+1)
		row := e.memoRow(b.ID)
		row.Body = big
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET body=?, release_body=?, release_hash=? WHERE id=?`, big, big, memoContentHash(row), b.ID); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1100 * time.Millisecond)
		c := e.pending("app", "impl", "c")
		d := e.fired(q)
		if !sameIDs(d.MessageIDs, a.ID, c.ID) {
			t.Fatalf("batch %v", d.MessageIDs)
		}
	})
}

// A recipe the run could not be created with (a route another agent does not take) holds the
// message for a person at the claim, with a reason, instead of being claimed again and again. The
// message is moved into a queue of another agent, which is not checked at the move.
func TestMemoRecipeInvalidAtTheClaimHoldsTheMessage(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		e.queueWith("app", "impl", map[string]any{})
		other := e.queueWith("app", "other", map[string]any{"agentKind": "codex"})
		m := decode[memoDTO](t, e.recipeMemo("app", "impl", "m", map[string]any{"route": "anthropic-subscription"}))
		if rec := e.do("a", "PATCH", "/api/memos/"+m.ID, map[string]any{"category": "other"}); rec.Code != http.StatusOK {
			t.Fatalf("move: %d %s", rec.Code, rec.Body.String())
		}
		if row := e.memoRow(m.ID); row.State != store.MemoStatePending || row.Category != "other" {
			t.Fatalf("before the claim: %+v", row)
		}
		if rec := e.fire(other); rec.Code != http.StatusConflict {
			t.Fatalf("fire: %d %s", rec.Code, rec.Body.String())
		}
		row := e.memoRow(m.ID)
		if row.State != store.MemoStateHeld || !strings.Contains(row.HeldReason, "recipe_invalid") {
			t.Fatalf("row %+v", row)
		}
	})
}

// A move out of every queue by a tidy run drops the recipe, and the undo puts it back. Messages
// that ask for different recipes are never merged, even when they would run the same.
func TestMemoRecipeTidyMoveOutAndExplicitMerge(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.category("r", "q")
		plain := e.category("r", "plain")
		e.tidyQueue(q)
		mk := func(body, effort string) store.Memo {
			m := store.Memo{ID: store.NewID(), MembershipID: e.members["a"], Repo: "r", Category: "q", Kind: "text", Body: body,
				CreatedAt: store.NowTS(), State: store.MemoStatePending, Provenance: "member", Recipe: memorecipe.Recipe{Effort: effort}.Encode()}
			m.ReleaseHash, m.ReleasedAt = memoContentHash(m), store.NowTS()
			if err := e.st.CreateMemo(context.Background(), m); err != nil {
				t.Fatal(err)
			}
			return e.memoRow(m.ID)
		}
		a, b := mk("one", "high"), mk("two", "")
		out, err := memoTidyApply(context.Background(), e.st, memoTidyApplyIn{MembershipID: e.members["a"], QueueID: q, Trigger: store.TidyTriggerManual,
			Input: tidyItems(a, b), Ops: []memotidy.Op{merge("both", a.ID, b.ID)}})
		if err != nil || out.Run.State != store.TidyRunDropped {
			t.Fatalf("an explicit and an omitted recipe merged: %v %+v", err, out.Run)
		}
		out = e.tidyApply(q, store.TidyTriggerManual, []store.Memo{a}, []string{plain}, move(a.ID, plain))
		if got := e.memoRow(a.ID); got.Recipe != "" || got.State != "" {
			t.Fatalf("moved out: %+v", got)
		}
		if rec := e.undo(out.Run.ID); rec.Code != http.StatusOK {
			t.Fatalf("undo: %d %s", rec.Code, rec.Body.String())
		}
		if got := e.memoRow(a.ID); got.Recipe != a.Recipe || got.Category != "q" {
			t.Fatalf("undone: %+v", got)
		}
		e.assertPendingReleaseMatches()
	})
}

// The person's return and a session's hand-back of a delivered message put it back pending under
// the release_recipe CHECK, whatever the release copy held while the message was delivered (a
// rename of the category clears the copies of a row that is not pending).
func TestMemoRecipeReturnAndDeferRestoreTheReleaseCopy(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{})
		for _, person := range []bool{true, false} {
			m := decode[memoDTO](t, e.recipeMemo("app", "impl", "one", map[string]any{"effort": "low", "model": "m1"}))
			if m.State != store.MemoStatePending {
				t.Fatalf("create: %+v", m)
			}
			d := e.contRun(q, "run-1", store.DeliveryOpen, m.ID)
			if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET release_recipe='' WHERE id=?`, m.ID); err != nil {
				t.Fatal(err)
			}
			err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
				res, err := tx.ReturnDelivered(d, store.MemoReturn{Person: person, Hash: memoContentHash, At: memoNow(),
					NotBefore: store.DeliveryTS(time.Now().Add(time.Hour))})
				if err == nil && len(res.Pending) != 1 {
					t.Errorf("person=%v: result %+v", person, res)
				}
				return err
			})
			if err != nil {
				t.Fatalf("person=%v: %v", person, err)
			}
			row := e.memoRow(m.ID)
			if row.State != store.MemoStatePending || row.Recipe == "" {
				t.Fatalf("person=%v: returned row %+v", person, row)
			}
			var rel string
			if err := e.st.DB().QueryRowContext(context.Background(), `SELECT release_recipe FROM memo WHERE id=?`, m.ID).Scan(&rel); err != nil || rel != row.Recipe {
				t.Fatalf("person=%v: release_recipe %q (%v), want %q", person, rel, err, row.Recipe)
			}
		}
	})
}

// A pending message the claim would hold for its recipe does not count towards an open threshold:
// with openAfter 2 and one valid message, nothing fires; the next valid message opens the queue.
func TestMemoRecipeInvalidDoesNotOpenAThreshold(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"enabled": true, "openAfter": 2, "batch": 5})
		ok1 := e.pending("app", "impl", "valid")
		bad := decode[memoDTO](t, e.recipeMemo("app", "impl", "bad", map[string]any{"route": "anthropic-subscription"}))
		e.queueWith("app", "impl", map[string]any{"enabled": true, "openAfter": 2, "batch": 5, "agentKind": "codex"})
		// The change of the category's agent held the message that can no longer run, at once.
		if row := e.memoRow(bad.ID); row.State != store.MemoStateHeld {
			t.Fatalf("the invalid message is %s after the settings change", row.State)
		}
		if w := e.queueWaiting(q); w == nil || w.Reason != waitBelowThreshold || w.Detail == nil || w.Detail.Waiting != 1 || w.Detail.Needed != 2 {
			t.Fatalf("the list before any tick: %+v", w)
		}
		agent := e.useQueueAgent("")
		agent.mu.Lock()
		agent.report = true
		agent.mu.Unlock()
		later := time.Now().Add(10 * time.Minute)
		e.autoTick(later)
		if agent.createCount() != 0 || e.memoRow(ok1.ID).State != store.MemoStatePending {
			t.Fatalf("one valid message opened a threshold of two: creates %d, %+v (bad %s)", agent.createCount(), e.memoRow(ok1.ID), bad.ID)
		}
		if w := e.queueWaiting(q); w == nil || w.Reason != waitBelowThreshold || w.Detail == nil || w.Detail.Waiting != 1 || w.Detail.Needed != 2 {
			t.Fatalf("the list after the tick: %+v", w)
		}
		ok2 := e.pending("app", "impl", "second valid")
		e.autoTick(later.Add(10 * time.Minute))
		if agent.createCount() != 1 {
			t.Fatalf("two valid messages did not open it: %d creates (second %s)", agent.createCount(), ok2.ID)
		}
	})
}

// With the category's default effort high, an explicit "high" and an omitted effort are not merged
// (the merge would drop the explicit choice), and a message that names another agent gets none of
// the category's route while the category's own messages keep it.
func TestMemoRecipeExplicitIsKeptAndRouteIsNotInherited(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"batch": 5, "maxActive": 3, "effort": "high", "route": "anthropic-subscription"})
		x := decode[memoDTO](t, e.recipeMemo("app", "impl", "x", map[string]any{"agentKind": "codex"}))
		time.Sleep(1100 * time.Millisecond)
		y := e.pending("app", "impl", "y")
		agent := e.useQueueAgent("")
		d1, d2 := e.fired(q), e.fired(q)
		if !sameIDs(d1.MessageIDs, x.ID) || !sameIDs(d2.MessageIDs, y.ID) {
			t.Fatalf("batches %v %v", d1.MessageIDs, d2.MessageIDs)
		}
		e.tick(e.runner(), time.Now())
		seen := map[string]int{}
		for _, c := range agent.creates {
			qd, _ := c["queue_delivery"].(map[string]any)
			seen[qd["id"].(string)]++
		}
		if seen[d1.ID] != 1 || seen[d2.ID] != 1 || agent.createCount() != 2 {
			t.Fatalf("creates per delivery %v", seen)
		}
		for _, c := range agent.creates {
			qd, _ := c["queue_delivery"].(map[string]any)
			r, hasRoute := c["route"].(map[string]any)
			switch qd["id"] {
			case d1.ID:
				if hasRoute || c["kind"] != "codex" {
					t.Errorf("codex create kept the category's route: %v", c)
				}
			case d2.ID:
				if !hasRoute || r["route"] != "anthropic-subscription" || c["kind"] != "claude" {
					t.Errorf("claude create lost the route: %v", c)
				}
			}
		}
	})
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.category("r", "q")
		e.tidyQueue(q)
		if rec := e.do("a", "PUT", "/api/memo-queues/"+q, map[string]any{"instruction": "do it", "tidy": true, "effort": "high"}); rec.Code != http.StatusOK {
			t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
		}
		mk := func(body, effort string) store.Memo {
			m := store.Memo{ID: store.NewID(), MembershipID: e.members["a"], Repo: "r", Category: "q", Kind: "text", Body: body,
				CreatedAt: store.NowTS(), State: store.MemoStatePending, Provenance: "member", Recipe: memorecipe.Recipe{Effort: effort}.Encode()}
			m.ReleaseHash, m.ReleasedAt = memoContentHash(m), store.NowTS()
			if err := e.st.CreateMemo(context.Background(), m); err != nil {
				t.Fatal(err)
			}
			return e.memoRow(m.ID)
		}
		a, b := mk("one", "high"), mk("two", "")
		for _, rows := range [][]store.Memo{{a, b}, {b, a}} {
			out, err := memoTidyApply(context.Background(), e.st, memoTidyApplyIn{MembershipID: e.members["a"], QueueID: q, Trigger: store.TidyTriggerManual,
				Input: tidyItems(rows...), Ops: []memotidy.Op{merge("both", rows[0].ID, rows[1].ID)}})
			if err != nil || out.Run.State != store.TidyRunDropped || out.Run.Reason != memotidy.CodeMixedGroup {
				t.Fatalf("an explicit and an omitted effort under a default of the same value merged: %v %+v", err, out.Run)
			}
		}
	})
}

// An undo whose home category is no longer a queue leaves a plain note with no recipe, even when
// the row carried one in the queue it was moved to.
func TestMemoRecipeUndoIntoAPlainHomeCarriesNoRecipe(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		home, dest := e.category("r", "home"), e.category("r", "dest")
		e.queue(dest)
		e.tidyQueue(home, dest)
		m := store.Memo{ID: store.NewID(), MembershipID: e.members["a"], Repo: "r", Category: "home", Kind: "text", Body: "x",
			CreatedAt: store.NowTS(), State: store.MemoStatePending, Provenance: "member", Recipe: memorecipe.Recipe{Effort: "low"}.Encode()}
		m.ReleaseHash, m.ReleasedAt = memoContentHash(m), store.NowTS()
		if err := e.st.CreateMemo(context.Background(), m); err != nil {
			t.Fatal(err)
		}
		row := e.memoRow(m.ID)
		out := e.tidyApply(home, store.TidyTriggerManual, []store.Memo{row}, []string{dest}, move(m.ID, dest))
		if got := e.memoRow(m.ID); got.Category != "dest" || got.Recipe == "" {
			t.Fatalf("moved: %+v", got)
		}
		// The home loses its auto-run settings, then the run is undone.
		if rec := e.do("a", "DELETE", "/api/memo-queues/"+home, nil); rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
			t.Fatalf("delete settings: %d %s", rec.Code, rec.Body.String())
		}
		if rec := e.undo(out.Run.ID); rec.Code != http.StatusOK {
			t.Fatalf("undo: %d %s", rec.Code, rec.Body.String())
		}
		if got := e.memoRow(m.ID); got.Category != "home" || got.Recipe != "" || got.State != "" {
			t.Fatalf("undone into a plain home: %+v", got)
		}
	})
}
