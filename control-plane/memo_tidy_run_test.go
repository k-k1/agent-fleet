package main

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/memotidy"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Tidying before a fire, slice S3 (ADR 0137 section 8): the dispatcher's reservation stage, the job
// that calls the Agent, and a derived message becoming sendable. Every test runs on SQLite and, with
// AF_TEST_DATABASE_URL, on Postgres. The Agent is the fake of memo_delivery_test.go with the
// queueTidy marker and a POST /memos/tidy that checks a request the way the real one does.

// tidyAuto makes a tidy queue app/impl with n pending messages behind an Agent that sends the
// queue report and the queueTidy marker. It answers the Agent, the queue's id and the message ids,
// oldest first.
func (e *deliveryEnv) tidyAuto(settings map[string]any, n int) (*fakeQueueAgent, string, []string) {
	e.t.Helper()
	s := map[string]any{"tidy": true, "enabled": true, "batch": 10}
	for k, v := range settings {
		s[k] = v
	}
	q := e.queueWith("app", "impl", s)
	var ids []string
	for i := 0; i < n; i++ {
		ids = append(ids, e.pending("app", "impl", fmt.Sprintf("note %d", i)).ID)
	}
	agent := e.useQueueAgent("")
	agent.mu.Lock()
	agent.report, agent.tidy, agent.refs = true, true, true
	agent.mu.Unlock()
	return agent, q, ids
}

// tidyTick is a scheduler tick as the scheduler runs it: settle, dispatch, the sends, then the jobs
// (the wakes and the tidy runs) the dispatch answered.
func (e *deliveryEnv) tidyTick(r *memoQueueRunner, now time.Time) []store.MemoDelivery {
	ctx := context.Background()
	sends := r.settle(ctx, now)
	fired, jobs := r.dispatch(ctx, now, memoQueueSendsPerTick-len(sends))
	sends = append(sends, fired...)
	for _, d := range sends {
		r.send(ctx, d)
	}
	for _, j := range jobs {
		j(ctx)
	}
	return sends
}

func (e *deliveryEnv) tidyRunsOf(q string) []store.MemoTidyRun {
	e.t.Helper()
	runs, err := e.st.ListMemoTidyRuns(context.Background(), e.members["a"], q, 50)
	if err != nil {
		e.t.Fatal(err)
	}
	return runs
}

// onlyRun is the one run of a queue.
func (e *deliveryEnv) onlyRun(q string) store.MemoTidyRun {
	e.t.Helper()
	runs := e.tidyRunsOf(q)
	if len(runs) != 1 {
		e.t.Fatalf("%d runs, want 1: %+v", len(runs), runs)
	}
	return runs[0]
}

// memoSnapshot is every message of the member with its seen mark cleared: what a failed or refused
// run must leave as it was.
func (e *deliveryEnv) memoSnapshot() map[string]store.Memo {
	out := e.allMemos()
	for id, m := range out {
		m.TidySeenHash = ""
		out[id] = m
	}
	return out
}

// createdItems are the items of the nth session create the Agent got.
func createdItems(a *fakeQueueAgent, n int) []map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	qd, _ := a.creates[n]["queue_delivery"].(map[string]any)
	var out []map[string]any
	for _, it := range qd["items"].([]any) {
		out = append(out, it.(map[string]any))
	}
	return out
}

func itemIDs(items []map[string]any) []string {
	var out []string
	for _, it := range items {
		out = append(out, it["id"].(string))
	}
	return out
}

// mergeFirstTwo answers with a merge of the first two messages the request names.
func mergeFirstTwo(text string) func(map[string]any) (int, any) {
	return tidyOpsReply(func(ids []string, _ map[string]any) []any {
		return []any{map[string]any{"op": "merge", "ids": []string{ids[0], ids[1]}, "text": text}}
	})
}

// A tick reserves a run instead of claiming, the job calls the Agent exactly as the contract says,
// the apply merges, and the next tick claims the derived message with the one note left.
func TestMemoTidyDispatchMergesThenFires(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		e.category("app", "notes")
		agent, q, ids := e.tidyAuto(nil, 3)
		agent.tidyReply = mergeFirstTwo("merged note")
		queueBefore := e.queueRow(q)
		r := e.runner()
		now := time.Now()

		if sends := e.tidyTick(r, now); len(sends) != 0 || agent.createCount() != 0 {
			t.Fatalf("the first tick claimed (%d sends, %d creates): it should have tidied", len(sends), agent.createCount())
		}
		run := e.onlyRun(q)
		if run.State != store.TidyRunApplied || run.Trigger != store.TidyTriggerAuto {
			t.Fatalf("run: %+v", run)
		}
		if agent.tidyCallCount() != 1 {
			t.Fatalf("%d tidy calls", agent.tidyCallCount())
		}
		agent.mu.Lock()
		req, ctype := agent.tidyCalls[0], agent.tidyTypes[0]
		agent.mu.Unlock()
		if ctype != "application/json" || req["run"] != run.ID || req["trigger"] != "auto" {
			t.Fatalf("request: %v %v", ctype, req)
		}
		if lim := req["limits"]; !reflect.DeepEqual(lim, map[string]any{"mergeSources": 10.0, "splitParts": 5.0, "textBytes": 16384.0, "operations": 80.0}) {
			t.Fatalf("limits: %v", lim)
		}
		if qq := req["queue"].(map[string]any); qq["id"] != q || qq["label"] != "app/impl" {
			t.Fatalf("queue: %v", qq)
		}
		var sent []string
		for _, it := range req["items"].([]any) {
			m := it.(map[string]any)
			sent = append(sent, m["id"].(string))
			if m["kind"] != "text" || m["group"] != 0.0 || m["merge"] != true || m["move"] != true {
				t.Fatalf("item: %v", m)
			}
		}
		if !reflect.DeepEqual(sent, ids) {
			t.Fatalf("items %v, want oldest first %v", sent, ids)
		}
		// The queue itself is never offered; its sibling category is, as a plain category.
		var dests []map[string]any
		for _, d := range req["destinations"].([]any) {
			dests = append(dests, d.(map[string]any))
		}
		if len(dests) != 1 || dests[0]["name"] != "app/notes" || dests[0]["queue"] != false {
			t.Fatalf("destinations: %v", dests)
		}
		// A run changes no setting of the queue, and a pending message's release still matches.
		if after := e.queueRow(q); !reflect.DeepEqual(after, queueBefore) {
			t.Fatalf("the run changed the queue: %+v -> %+v", queueBefore, after)
		}
		e.assertPendingReleaseMatches()

		// The claim is the next tick's, and it carries the derived message and the note left over.
		if sends := e.tidyTick(r, now); len(sends) != 1 {
			t.Fatalf("second tick: %d sends", len(sends))
		}
		if agent.tidyCallCount() != 1 {
			t.Fatalf("the derived message was tidied again: %d calls", agent.tidyCallCount())
		}
		items := createdItems(agent, 0)
		if len(items) != 2 {
			t.Fatalf("items: %v", items)
		}
		var cur, plain map[string]any
		for _, it := range items {
			if it["provenance"] == "curator" {
				cur = it
			} else {
				plain = it
			}
		}
		if cur == nil || plain == nil || plain["id"] != ids[2] || cur["body"] != "merged note" ||
			cur["sourceProvenance"] != "member" || cur["derivedFrom"] != 2.0 {
			t.Fatalf("delivery items: %v", items)
		}
		for _, id := range ids[:2] {
			if m := e.memoRow(id); m.State != store.MemoStateSuperseded {
				t.Fatalf("source %s is %q", id, m.State)
			}
		}
	})
}

// Every way a run can end without a change leaves the messages exactly as they were but for the seen
// mark, and the next tick fires the original items. The fake answers each Agent code the contract names.
func TestMemoTidyDispatchFailuresFireTheOriginals(t *testing.T) {
	type tc struct {
		name         string
		reply        func(map[string]any) (int, any)
		delay        time.Duration
		state, why   string
		noTidyAfter  bool
		callWait     time.Duration
		replyContent string
	}
	cases := []tc{
		{name: "malformed 422", reply: tidyErrReply(422, "tidy_malformed"), state: "dropped", why: "tidy_malformed"},
		{name: "operations not a list", reply: func(req map[string]any) (int, any) {
			return 200, map[string]any{"run": req["run"], "operations": "x", "kind": "claude", "model": "m"}
		}, state: "dropped", why: "bad_shape"},
		{name: "unknown field in an operation", reply: tidyOpsReply(func(ids []string, _ map[string]any) []any {
			return []any{map[string]any{"op": "keep", "id": ids[0], "extra": 1}}
		}), state: "dropped", why: "bad_shape"},
		{name: "unknown id", reply: tidyOpsReply(func(ids []string, _ map[string]any) []any {
			return []any{map[string]any{"op": "keep", "id": "nope"}}
		}), state: "dropped", why: "unknown_id"},
		{name: "an id in two operations", reply: tidyOpsReply(func(ids []string, _ map[string]any) []any {
			return []any{map[string]any{"op": "merge", "ids": []string{ids[0], ids[1]}, "text": "x"}, map[string]any{"op": "keep", "id": ids[0]}}
		}), state: "dropped", why: "duplicate_id"},
		{name: "another run's id", reply: func(map[string]any) (int, any) {
			return 200, map[string]any{"run": "someone-else", "operations": []any{}, "kind": "claude", "model": "m"}
		}, state: "dropped", why: "bad_shape"},
		{name: "busy 409", reply: tidyErrReply(409, "tidy_busy"), state: "skipped", why: "tidy_busy"},
		{name: "deny-all unavailable 503", reply: tidyErrReply(503, "deny_all_unavailable"), state: "skipped", why: "deny_all_unavailable", noTidyAfter: true},
		{name: "policy unconfirmed 503", reply: tidyErrReply(503, "policy_unconfirmed"), state: "skipped", why: "policy_unconfirmed"},
		{name: "route forbidden 403", reply: tidyErrReply(403, "route_forbidden"), state: "skipped", why: "route_forbidden"},
		{name: "agent disabled 403", reply: tidyErrReply(403, "agent_disabled"), state: "skipped", why: "agent_disabled"},
		{name: "bad request 400", reply: tidyErrReply(400, "bad_tidy_request"), state: "skipped", why: "bad_tidy_request"},
		{name: "failed 502", reply: tidyErrReply(502, "tidy_failed"), state: "failed", why: "tidy_failed"},
		{name: "timeout 504", reply: tidyErrReply(504, "tidy_timeout"), state: "failed", why: "tidy_timeout"},
		{name: "an answer with no code", reply: func(map[string]any) (int, any) { return 500, "not json" }, state: "failed", why: "agent_error"},
		{name: "no answer in time", delay: 400 * time.Millisecond, callWait: 80 * time.Millisecond, state: "failed", why: "timeout"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
				agent, q, ids := e.tidyAuto(nil, 3)
				agent.tidyReply, agent.tidyDelay = c.reply, c.delay
				r := e.runner()
				r.tidyCallWait = c.callWait
				before, queueBefore := e.memoSnapshot(), e.queueRow(q)
				now := time.Now()

				e.tidyTick(r, now)
				run := e.onlyRun(q)
				if run.State != c.state || run.Reason != c.why {
					t.Fatalf("run %s %q, want %s %q", run.State, run.Reason, c.state, c.why)
				}
				if agent.createCount() != 0 {
					t.Fatal("the failed tick still started a session")
				}
				if after := e.memoSnapshot(); !reflect.DeepEqual(after, before) {
					t.Fatalf("rows changed:\n%+v\n%+v", before, after)
				}
				queueAfter := e.queueRow(q)
				if c.noTidyAfter != (queueAfter.NoTidyAt != "") {
					t.Fatalf("noTidyAt = %q, want set %v", queueAfter.NoTidyAt, c.noTidyAfter)
				}
				queueAfter.NoTidyAt = queueBefore.NoTidyAt
				if !reflect.DeepEqual(queueAfter, queueBefore) {
					t.Fatalf("the queue changed: %+v -> %+v", queueBefore, queueAfter)
				}
				// Every message of the input is seen: the run's input is what it marks.
				for _, id := range ids {
					if m := e.memoRow(id); m.TidySeenHash != memoContentHash(m) {
						t.Fatalf("%s is still unseen", id)
					}
				}

				// The next tick fires the original items, and does not ask the Agent again.
				if sends := e.tidyTick(r, now); len(sends) != 1 {
					t.Fatalf("next tick: %d sends", len(sends))
				}
				if got := itemIDs(createdItems(agent, 0)); !reflect.DeepEqual(got, ids) {
					t.Fatalf("delivered %v, want the originals %v", got, ids)
				}
				if agent.tidyCallCount() != 1 || len(e.tidyRunsOf(q)) != 1 {
					t.Fatalf("a second run was made: %d calls, %d runs", agent.tidyCallCount(), len(e.tidyRunsOf(q)))
				}
			})
		})
	}
}

// An Agent that does not send queueTidy is never asked: a running workspace shows it, the mark is
// set, and the messages fire as they are. Found missing only after the reservation (the workspace
// was stopped, so nothing had asked it), the run ends skipped and the messages fire the next tick.
func TestMemoTidyDispatchNoMarker(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q, ids := e.tidyAuto(nil, 2)
		agent.setTidy(false)
		r := e.runner()
		if sends := e.tidyTick(r, time.Now()); len(sends) != 1 {
			t.Fatalf("%d sends", len(sends))
		}
		if agent.tidyCallCount() != 0 || len(e.tidyRunsOf(q)) != 0 {
			t.Fatal("an Agent without the marker was tidied for")
		}
		if e.queueRow(q).NoTidyAt == "" {
			t.Fatal("the mark of an Agent that cannot tidy was not set")
		}
		if got := itemIDs(createdItems(agent, 0)); !reflect.DeepEqual(got, ids) {
			t.Fatalf("delivered %v", got)
		}

		// The marker disappears between the reservation and the job.
		q2 := e.queueWith("app", "second", map[string]any{"tidy": true, "enabled": true, "batch": 10})
		for i := 0; i < 2; i++ {
			e.pending("app", "second", fmt.Sprintf("two %d", i))
		}
		agent.setTidy(true)
		_, jobs := e.runner().dispatch(context.Background(), time.Now(), 4)
		agent.setTidy(false)
		for _, j := range jobs {
			j(context.Background())
		}
		run := e.onlyRun(q2)
		if run.State != store.TidyRunSkipped || run.Reason != "no_marker" {
			t.Fatalf("run: %+v", run)
		}
		if agent.tidyCallCount() != 0 || e.queueRow(q2).NoTidyAt == "" {
			t.Fatal("the Agent was called, or the mark is not set")
		}
	})
}

// Messages of different provenance, or of one provenance and different producers, are never merged:
// the reply is refused whole and nothing is written but the seen marks.
func TestMemoTidyDispatchNeverMergesAcrossProvenance(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"tidy": true, "enabled": true, "batch": 10})
		member := e.pending("app", "impl", "from the member")
		ws := e.tidyRow("app", "impl", "from a session", "workspace", "s1", "", store.MemoStatePending)
		ws2 := e.tidyRow("app", "impl", "from another session", "workspace", "s2", "", store.MemoStatePending)
		agent := e.useQueueAgent("")
		agent.mu.Lock()
		agent.report, agent.tidy, agent.refs = true, true, true
		agent.mu.Unlock()
		for i, pair := range [][2]string{{member.ID, ws.ID}, {ws.ID, ws2.ID}} {
			agent.tidyReply = tidyOpsReply(func([]string, map[string]any) []any {
				return []any{map[string]any{"op": "merge", "ids": []string{pair[0], pair[1]}, "text": "mixed"}}
			})
			// A run is reserved only for what no run has looked at.
			if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET tidy_seen_hash=''`); err != nil {
				t.Fatal(err)
			}
			before := e.memoSnapshot()
			_, jobs := e.runner().dispatch(context.Background(), time.Now(), 4)
			for _, j := range jobs {
				j(context.Background())
			}
			runs := e.tidyRunsOf(q)
			if len(runs) != i+1 || runs[0].State != store.TidyRunDropped || runs[0].Reason != memotidy.CodeMixedGroup {
				t.Fatalf("pair %d: runs %+v", i, runs)
			}
			if after := e.memoSnapshot(); !reflect.DeepEqual(after, before) {
				t.Fatalf("pair %d: a refused merge changed rows", i)
			}
		}
	})
}

// A move to a queue the settings do not list ends held with the category it came from, a move to a
// listed one (which may be in another repository) is pending there with a matching release, and a
// move to a plain category leaves auto-run. The listing is read when the reply is applied, so a
// change of it between the call and the apply is what counts.
func TestMemoTidyDispatchMoves(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		listed := e.queueWith("other", "inbox", nil)
		unlisted := e.queueWith("app", "unlisted", nil)
		plain := e.category("app", "plain")
		agent, q, ids := e.tidyAuto(map[string]any{"tidyDestinations": []string{listed}}, 3)
		agent.tidyReply = tidyOpsReply(func(ids []string, _ map[string]any) []any {
			return []any{
				map[string]any{"op": "move", "id": ids[0], "to": unlisted},
				map[string]any{"op": "move", "id": ids[1], "to": listed},
				map[string]any{"op": "move", "id": ids[2], "to": plain},
			}
		})
		e.tidyTick(e.runner(), time.Now())
		if run := e.onlyRun(q); run.State != store.TidyRunApplied {
			t.Fatalf("run: %+v", run)
		}
		agent.mu.Lock()
		offered := map[string]bool{}
		for _, d := range agent.tidyCalls[0]["destinations"].([]any) {
			offered[d.(map[string]any)["id"].(string)] = true
		}
		agent.mu.Unlock()
		if !offered[listed] || !offered[unlisted] || !offered[plain] || offered[q] {
			t.Fatalf("offered %v", offered)
		}
		if m := e.memoRow(ids[0]); m.State != store.MemoStateHeld || m.TidyFrom != q || m.Category != "unlisted" {
			t.Fatalf("a move to an unlisted queue: %+v", m)
		}
		if m := e.memoRow(ids[1]); m.State != store.MemoStatePending || m.Repo != "other" || m.ReleaseHash != memoContentHash(m) || m.ReleasedBy != store.MemoReleasedByTidy {
			t.Fatalf("a move to a listed queue: %+v", m)
		}
		if m := e.memoRow(ids[2]); m.State != "" || m.Category != "plain" {
			t.Fatalf("a move to a plain category: %+v", m)
		}
		// The release card of the unlisted destination names where it came from.
		rec := e.do("a", "GET", "/api/memos", nil)
		var list struct {
			Memos []memoDTO `json:"memos"`
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("list: %d", rec.Code)
		}
		list.Memos = decode[[]memoDTO](t, rec)
		found := false
		for _, m := range list.Memos {
			if m.ID == ids[0] && m.TidyFrom != nil && m.TidyFrom.ID == q {
				found = true
			}
		}
		if !found {
			t.Fatal("the held message does not name the queue it came from")
		}
		e.assertPendingReleaseMatches()
	})
}

// The destinations a run was offered are a snapshot, and what a move does is decided by the stored
// settings when the reply is applied.
func TestMemoTidyDispatchDestinationToggledBeforeApply(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		listed := e.queueWith("other", "inbox", nil)
		agent, q, ids := e.tidyAuto(map[string]any{"tidyDestinations": []string{listed}}, 2)
		agent.tidyReply = tidyOpsReply(func(ids []string, _ map[string]any) []any {
			return []any{map[string]any{"op": "move", "id": ids[0], "to": listed}}
		})
		r := e.runner()
		_, jobs := r.dispatch(context.Background(), time.Now(), 4)
		if len(jobs) != 1 {
			t.Fatalf("%d jobs", len(jobs))
		}
		// The member takes the destination off the list while the run is in flight.
		if rec := e.do("a", "PUT", "/api/memo-queues/"+q, map[string]any{"instruction": "do it", "tidy": true, "enabled": true, "tidyDestinations": []string{}}); rec.Code != http.StatusOK {
			t.Fatalf("settings: %d %s", rec.Code, rec.Body.String())
		}
		jobs[0](context.Background())
		if m := e.memoRow(ids[0]); m.State != store.MemoStateHeld || m.TidyFrom != q {
			t.Fatalf("a destination taken off the list still released the message: %+v", m)
		}
	})
}

// Two Control Planes (runners over one store) never reserve two runs for one member, even for two
// different queues, and the member's other tidy queue waits with the reason tidying.
func TestMemoTidyDispatchOneRunPerMember(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q1, _ := e.tidyAuto(nil, 2)
		q2 := e.queueWith("app", "second", map[string]any{"tidy": true, "enabled": true, "batch": 10})
		e.pending("app", "second", "x")
		e.pending("app", "second", "y")
		var wg sync.WaitGroup
		jobs := make([]int, 4)
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, js := e.runner().dispatch(context.Background(), time.Now(), 6)
				jobs[i] = len(js)
			}()
		}
		wg.Wait()
		total := 0
		for _, n := range jobs {
			total += n
		}
		running := 0
		for _, q := range []string{q1, q2} {
			for _, run := range e.tidyRunsOf(q) {
				if run.State == store.TidyRunRunning {
					running++
				}
			}
		}
		if running != 1 || total != 1 || agent.createCount() != 0 {
			t.Fatalf("%d running runs, %d jobs, %d creates", running, total, agent.createCount())
		}
		// The queue that did not get the run says so.
		other := q1
		if len(e.tidyRunsOf(q1)) > 0 {
			other = q2
		}
		w := e.waitingOf(other, time.Now())
		if w == nil || w.Reason != waitTidying {
			t.Fatalf("the other queue's waiting: %+v", w)
		}
	})
}

// A run that has been running past the bound is ended by the next tick that meets it as failed
// (takeover), its input is marked seen so the messages fire as they are, and the first process's late
// apply finds the run no longer running and changes nothing.
func TestMemoTidyDispatchTakeover(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q, ids := e.tidyAuto(nil, 3)
		agent.tidyReply = mergeFirstTwo("late merge")
		first := e.runner()
		now := time.Now()
		_, jobs := first.dispatch(context.Background(), now, 4)
		if len(jobs) != 1 || e.onlyRun(q).State != store.TidyRunRunning {
			t.Fatal("no run was reserved")
		}
		before := e.memoSnapshot()

		// A tick within the bound leaves it, and the queue waits.
		if sends, _ := e.runner().dispatch(context.Background(), now.Add(memoTidyTakeoverAfter/2), 4); len(sends) != 0 {
			t.Fatal("the queue fired while its run was in flight")
		}
		if e.onlyRun(q).State != store.TidyRunRunning {
			t.Fatal("a run was taken over early")
		}

		// The member pauses the queue, so the takeover is not followed by a claim in the same tick.
		if won, err := e.pauseQueue(q, "test"); err != nil || !won {
			t.Fatalf("pause: %v %v", won, err)
		}
		e.runner().dispatch(context.Background(), now.Add(memoTidyTakeoverAfter+time.Second), 4)
		run := e.onlyRun(q)
		if run.State != store.TidyRunFailed || run.Reason != "takeover" {
			t.Fatalf("run: %+v", run)
		}
		for _, id := range ids {
			if m := e.memoRow(id); m.TidySeenHash != memoContentHash(m) {
				t.Fatalf("%s was not marked seen by the takeover", id)
			}
		}

		// The first process answers late.
		jobs[0](context.Background())
		if got := e.onlyRun(q); got.State != store.TidyRunFailed || got.Reason != "takeover" {
			t.Fatalf("the late apply changed the run: %+v", got)
		}
		if after := e.memoSnapshot(); !reflect.DeepEqual(after, before) {
			t.Fatal("the late apply changed messages")
		}
	})
}

// A takeover that meets a queue able to fire lets the messages fire as they are in the same tick.
func TestMemoTidyDispatchTakeoverThenFires(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q, ids := e.tidyAuto(nil, 2)
		now := time.Now()
		if _, jobs := e.runner().dispatch(context.Background(), now, 4); len(jobs) != 1 {
			t.Fatal("no run was reserved")
		}
		sends := e.tidyTick(e.runner(), now.Add(memoTidyTakeoverAfter+time.Second))
		if len(sends) != 1 || agent.tidyCallCount() != 0 {
			t.Fatalf("%d sends, %d tidy calls", len(sends), agent.tidyCallCount())
		}
		if got := itemIDs(createdItems(agent, 0)); !reflect.DeepEqual(got, ids) {
			t.Fatalf("delivered %v", got)
		}
		if run := e.onlyRun(q); run.Reason != "takeover" {
			t.Fatalf("run: %+v", run)
		}
	})
}

// The day's cap: ten runs reserved in 24 hours and the messages fire as they are, with no run
// reserved and no call made.
func TestMemoTidyDispatchDailyCap(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q, ids := e.tidyAuto(nil, 2)
		fill := func(n int) {
			err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
				for i := 0; i < n; i++ {
					at := memoNow()
					if err := tx.InsertTidyRun(store.MemoTidyRun{ID: store.NewID(), MembershipID: e.members["a"], QueueID: q,
						Trigger: store.TidyTriggerAuto, State: store.TidyRunKept, Input: "{}", CreatedAt: at, FinishedAt: at, UpdatedAt: at}); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		fill(memotidy.DailyCap)
		if sends := e.tidyTick(e.runner(), time.Now()); len(sends) != 1 {
			t.Fatalf("%d sends", len(sends))
		}
		if agent.tidyCallCount() != 0 || len(e.tidyRunsOf(q)) != memotidy.DailyCap {
			t.Fatalf("a run was made past the cap: %d calls, %d runs", agent.tidyCallCount(), len(e.tidyRunsOf(q)))
		}
		if got := itemIDs(createdItems(agent, 0)); !reflect.DeepEqual(got, ids) {
			t.Fatalf("delivered %v", got)
		}
	})
}

// One run below the cap still runs.
func TestMemoTidyDispatchBelowTheCapStillRuns(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q, _ := e.tidyAuto(nil, 2)
		err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
			for i := 0; i < memotidy.DailyCap-1; i++ {
				at := memoNow()
				if err := tx.InsertTidyRun(store.MemoTidyRun{ID: store.NewID(), MembershipID: e.members["a"], QueueID: q,
					Trigger: store.TidyTriggerAuto, State: store.TidyRunKept, Input: "{}", CreatedAt: at, FinishedAt: at, UpdatedAt: at}); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		e.tidyTick(e.runner(), time.Now())
		if agent.tidyCallCount() != 1 {
			t.Fatalf("%d calls", agent.tidyCallCount())
		}
	})
}

// A threshold queue that opened on N messages still fires after a merge dropped the count below N:
// the reservation makes it draining, as the claim would have.
func TestMemoTidyDispatchThresholdQueueKeepsDraining(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q, _ := e.tidyAuto(map[string]any{"openAfter": 3, "maxActive": 1}, 3)
		agent.tidyReply = mergeFirstTwo("merged")
		later := time.Now().Add(2 * time.Minute) // nothing was added for a minute
		r := e.runner()
		e.tidyTick(r, later)
		if run := e.onlyRun(q); run.State != store.TidyRunApplied {
			t.Fatalf("run: %+v", run)
		}
		if !e.queueRow(q).Draining {
			t.Fatal("the reservation did not start the drain")
		}
		stats, err := e.st.ListQueuePendingStats(context.Background())
		if err != nil || stats[q].Count != 2 {
			t.Fatalf("pending after the merge: %+v %v", stats[q], err)
		}
		if agent.createCount() != 0 {
			t.Fatal("fired in the tidy tick")
		}
		if sends := e.tidyTick(r, later); len(sends) != 1 {
			t.Fatalf("a queue that opened on 3 messages did not fire its 2: %d sends", len(sends))
		}
	})
}

// "Run now" during a run claims what the person sees, and the run's apply then drops whole.
func TestMemoTidyDispatchRunNowDuringARun(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q, ids := e.tidyAuto(nil, 3)
		agent.tidyReply = mergeFirstTwo("merged")
		_, jobs := e.runner().dispatch(context.Background(), time.Now(), 4)
		if len(jobs) != 1 {
			t.Fatalf("%d jobs", len(jobs))
		}
		d := e.fired(q)
		if len(d.MessageIDs) != 3 {
			t.Fatalf("the press claimed %v", d.MessageIDs)
		}
		jobs[0](context.Background())
		run := e.onlyRun(q)
		if run.State != store.TidyRunDropped || run.Reason != memoTidyReasonStaleInput {
			t.Fatalf("run: %+v", run)
		}
		for _, id := range ids {
			if m := e.memoRow(id); m.State != store.MemoStateClaimed {
				t.Fatalf("%s is %q", id, m.State)
			}
		}
	})
}

// "Run now" never tidies: the press claims what is there, however unseen.
func TestMemoTidyRunNowNeverTidies(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q, _ := e.tidyAuto(nil, 2)
		e.fired(q)
		e.tick(e.runner(), time.Now())
		if agent.tidyCallCount() != 0 || len(e.tidyRunsOf(q)) != 0 {
			t.Fatal("the press tidied")
		}
		if agent.createCount() != 1 {
			t.Fatalf("%d creates", agent.createCount())
		}
	})
}

// A run marks every message of its input seen, so the same messages are not tidied twice, and an
// edit makes one unseen again with no other write, so the next tick tidies the lot once more.
func TestMemoTidyDispatchUnseenRule(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q, ids := e.tidyAuto(nil, 2)
		r := e.runner()
		now := time.Now()
		_, jobs := r.dispatch(context.Background(), now, 4)
		jobs[0](context.Background()) // a valid reply with no operation: kept
		if run := e.onlyRun(q); run.State != store.TidyRunKept {
			t.Fatalf("run: %+v", run)
		}
		// A person's edit: new content, released again for it.
		m := e.memoRow(ids[0])
		m.Body = "edited"
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET body=?, release_body=?, release_hash=? WHERE id=?`, m.Body, m.Body, memoContentHash(m), m.ID); err != nil {
			t.Fatal(err)
		}
		_, jobs = r.dispatch(context.Background(), now, 4)
		if len(jobs) != 1 {
			t.Fatalf("an edited message was not tidied again: %d jobs", len(jobs))
		}
		jobs[0](context.Background())
		agent.mu.Lock()
		items := agent.tidyCalls[1]["items"].([]any)
		agent.mu.Unlock()
		if len(items) != 2 || agent.tidyCallCount() != 2 {
			t.Fatalf("the second run read %d items in %d calls, want both messages", len(items), agent.tidyCallCount())
		}
		// Nothing is unseen now: the next tick claims.
		sends, jobs := r.dispatch(context.Background(), now, 4)
		if len(sends) != 1 || len(jobs) != 0 {
			t.Fatalf("%d sends, %d jobs", len(sends), len(jobs))
		}
	})
}

// A derived message is sent only to an Agent that shows queueTidy. By itself the claim leaves it
// where it is and the queue says no_tidy; the press claims it and the send refuses before anything
// is sent; with the marker it goes, as a curator item with its sources' provenance and count.
func TestMemoTidyCuratorSendableOnlyWithTheMarker(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"enabled": true, "maxActive": 3, "batch": 10})
		a := e.pending("app", "impl", "one")
		b := e.pending("app", "impl", "two")
		plain := e.pending("app", "impl", "three")
		out := e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{e.memoRow(a.ID), e.memoRow(b.ID)}, nil, merge("one and two", a.ID, b.ID))
		derived := out.Results[0]
		agent := e.useQueueAgent("")
		agent.mu.Lock()
		agent.report, agent.tidy, agent.refs = true, false, true
		agent.mu.Unlock()
		r := e.runner()

		// By itself: the plain note goes, the derived one waits.
		if sends := e.tidyTick(r, time.Now()); len(sends) != 1 {
			t.Fatalf("%d sends", len(sends))
		}
		if got := itemIDs(createdItems(agent, 0)); !reflect.DeepEqual(got, []string{plain.ID}) {
			t.Fatalf("delivered %v", got)
		}
		if m := e.memoRow(derived); m.State != store.MemoStatePending || e.queueRow(q).NoTidyAt == "" {
			t.Fatalf("derived %+v, noTidyAt %q", m, e.queueRow(q).NoTidyAt)
		}
		if w := e.waitingOf(q, time.Now()); w == nil || w.Reason != waitNoTidy {
			t.Fatalf("waiting %+v", w)
		}

		// The press claims it; the send finds no marker and sends nothing.
		d := e.fired(q)
		if len(d.MessageIDs) != 1 || d.MessageIDs[0] != derived {
			t.Fatalf("the press claimed %v", d.MessageIDs)
		}
		e.tick(r, time.Now())
		if agent.createCount() != 1 {
			t.Fatalf("a derived message was sent to an Agent without the marker (%d creates)", agent.createCount())
		}
		if got := e.delivery(d.ID); got.State != store.DeliveryClosed || got.Outcome != store.DeliveryNotSent {
			t.Fatalf("delivery: %+v", got)
		}
		if m := e.memoRow(derived); m.State != store.MemoStatePending {
			t.Fatalf("the message did not return to pending: %+v", m)
		}

		// With the marker the press is sent, and the item says what it is.
		agent.setTidy(true)
		d = e.fired(q)
		e.tick(r, time.Now())
		if agent.createCount() != 2 {
			t.Fatalf("%d creates", agent.createCount())
		}
		items := createdItems(agent, 1)
		if len(items) != 1 || items[0]["id"] != derived || items[0]["provenance"] != "curator" ||
			items[0]["sourceProvenance"] != "member" || items[0]["derivedFrom"] != 2.0 || items[0]["body"] != "one and two" {
			t.Fatalf("items: %v", items)
		}
		if e.queueRow(q).NoTidyAt != "" {
			t.Fatal("a send that found the marker did not clear the mark")
		}
	})
}

// The mark of an Agent that cannot tidy is cleared by a running workspace that shows the marker, and
// the queue's settings do not touch it.
func TestMemoTidyNoTidyMarkLifecycle(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, q, _ := e.tidyAuto(map[string]any{"maxActive": 3}, 0)
		agent.setTidy(false)
		r := e.runner()
		e.pending("app", "impl", "x")
		e.tidyTick(r, time.Now())
		if e.queueRow(q).NoTidyAt == "" {
			t.Fatal("not set")
		}
		if rec := e.do("a", "PUT", "/api/memo-queues/"+q, map[string]any{"instruction": "do it", "tidy": true, "enabled": true, "maxActive": 3}); rec.Code != http.StatusOK {
			t.Fatalf("settings: %d", rec.Code)
		}
		if e.queueRow(q).NoTidyAt == "" {
			t.Fatal("the settings PUT cleared the mark")
		}
		agent.setTidy(true)
		e.pending("app", "impl", "y")
		e.tidyTick(r, time.Now())
		if e.queueRow(q).NoTidyAt != "" {
			t.Fatal("a running workspace that shows the marker did not clear it")
		}
	})
}

// What a request says of each message: its kind, a group per (source provenance, producer, origin)
// that a derived message shares with its sources, merge only for a plain text note, and no move for
// a message a run already moved.
func TestMemoTidyDispatchRequestShape(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"tidy": true, "enabled": true, "batch": 10})
		member := e.tidyRow("app", "impl", "member note", "member", "", "", store.MemoStatePending)
		ws := e.tidyRow("app", "impl", "session note", "workspace", "s1", "", store.MemoStatePending)
		file := store.Memo{ID: store.NewID(), MembershipID: e.members["a"], Repo: "app", Category: "impl", Kind: "file", Body: "see",
			RefPath: "/home/dev/repos/app/x.md", CreatedAt: store.NowTS(), State: store.MemoStatePending, Provenance: "member"}
		file.ReleaseHash, file.ReleasedAt = memoContentHash(file), store.NowTS()
		if err := e.st.CreateMemo(context.Background(), file); err != nil {
			t.Fatal(err)
		}
		moved := e.tidyRow("app", "impl", "came from elsewhere", "member", "", "", store.MemoStatePending)
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET tidy_from='elsewhere' WHERE id=?`, moved.ID); err != nil {
			t.Fatal(err)
		}
		d1 := e.tidyRow("app", "impl", "d1", "member", "", "", store.MemoStatePending)
		d2 := e.tidyRow("app", "impl", "d2", "member", "", "", store.MemoStatePending)
		derived := e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{d1, d2}, nil, merge("d", d1.ID, d2.ID)).Results[0]
		agent := e.useQueueAgent("")
		agent.mu.Lock()
		agent.report, agent.tidy, agent.refs = true, true, true
		agent.mu.Unlock()
		_, jobs := e.runner().dispatch(context.Background(), time.Now(), 4)
		for _, j := range jobs {
			j(context.Background())
		}
		if agent.tidyCallCount() != 1 {
			t.Fatalf("%d calls", agent.tidyCallCount())
		}
		agent.mu.Lock()
		items := map[string]map[string]any{}
		for _, it := range agent.tidyCalls[0]["items"].([]any) {
			m := it.(map[string]any)
			items[m["id"].(string)] = m
		}
		agent.mu.Unlock()
		if len(items) != 5 {
			t.Fatalf("items: %v", items)
		}
		if items[member.ID]["group"] != items[derived]["group"] || items[member.ID]["group"] == items[ws.ID]["group"] {
			t.Fatalf("groups: member %v derived %v session %v", items[member.ID]["group"], items[derived]["group"], items[ws.ID]["group"])
		}
		if items[member.ID]["merge"] != true || items[derived]["merge"] != true || items[file.ID]["merge"] != false {
			t.Fatalf("merge flags: %v", items)
		}
		if f := items[file.ID]; f["kind"] != "file" || f["file"] != file.RefPath {
			t.Fatalf("file item: %v", f)
		}
		if items[moved.ID]["move"] != false || items[member.ID]["move"] != true {
			t.Fatalf("move flags: %v", items)
		}
	})
}

// A run reads at most 40 messages, oldest first, and at most 96 KiB of text. The rest are marked seen
// and fire as they are, and a run is not made for messages that lie beyond what it could read.
func TestMemoTidyDispatchInputBounds(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"tidy": true, "enabled": true, "batch": 10})
		var ids []string
		for i := 0; i < 45; i++ {
			m := e.tidyRow("app", "impl", fmt.Sprintf("n%02d", i), "member", "", "", store.MemoStatePending)
			ids = append(ids, m.ID)
			at := time.Now().Add(-time.Hour + time.Duration(i)*time.Second).UTC().Format(time.RFC3339)
			if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET created_at=? WHERE id=?`, at, m.ID); err != nil {
				t.Fatal(err)
			}
		}
		agent := e.useQueueAgent("")
		agent.mu.Lock()
		agent.report, agent.tidy, agent.refs = true, true, true
		agent.mu.Unlock()
		_, jobs := e.runner().dispatch(context.Background(), time.Now(), 4)
		for _, j := range jobs {
			j(context.Background())
		}
		agent.mu.Lock()
		var sent []string
		for _, it := range agent.tidyCalls[0]["items"].([]any) {
			sent = append(sent, it.(map[string]any)["id"].(string))
		}
		agent.mu.Unlock()
		if !reflect.DeepEqual(sent, ids[:40]) {
			t.Fatalf("sent %d items, want the 40 oldest", len(sent))
		}
		for _, id := range ids {
			if m := e.memoRow(id); m.TidySeenHash != memoContentHash(m) {
				t.Fatalf("%s is unseen after the run", id)
			}
		}
		if sends, jobs := e.runner().dispatch(context.Background(), time.Now(), 4); len(sends) == 0 || len(jobs) != 0 {
			t.Fatalf("%d sends, %d jobs after the run", len(sends), len(jobs))
		}
		_ = q
	})
}

// The text one run reads is bounded: messages past it are marked seen without a run for them.
func TestMemoTidyDispatchTextBound(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"tidy": true, "enabled": true, "batch": 10})
		big := string(make([]byte, 0))
		for i := 0; i < 40<<10; i++ {
			big += "x"
		}
		var ids []string
		for i := 0; i < 4; i++ {
			ids = append(ids, e.tidyRow("app", "impl", fmt.Sprintf("%02d", i)+big, "member", "", "", store.MemoStatePending).ID)
		}
		agent := e.useQueueAgent("")
		agent.mu.Lock()
		agent.report, agent.tidy, agent.refs = true, true, true
		agent.mu.Unlock()
		_, jobs := e.runner().dispatch(context.Background(), time.Now(), 4)
		for _, j := range jobs {
			j(context.Background())
		}
		agent.mu.Lock()
		n := len(agent.tidyCalls[0]["items"].([]any))
		agent.mu.Unlock()
		if n != 2 {
			t.Fatalf("a run read %d messages of 40 KiB, want 2 (96 KiB)", n)
		}
		for _, id := range ids {
			if m := e.memoRow(id); m.TidySeenHash != memoContentHash(m) {
				t.Fatalf("%s is unseen after the run", id)
			}
		}
		_ = q
	})
}
