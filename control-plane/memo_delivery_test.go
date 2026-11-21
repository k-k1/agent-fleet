package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Message queues, step 1c (ADR 0137 section 6, #4163): deliveries, the reservation, the send on
// the tick, reconciliation and the closing rule. Every test runs on SQLite and, with
// AF_TEST_DATABASE_URL, on Postgres. The Agent is a fake that implements the Agent slice's
// contract (#4162) and answers reserve and abandon the way workspace/agent's sessionx does.

// deliveryEnv is an inboxEnv plus a second store handle on the same database: a second
// Control Plane process.
type deliveryEnv struct {
	*inboxEnv
	other *store.SQL
}

func eachDeliveryDialect(t *testing.T, f func(t *testing.T, e *deliveryEnv)) {
	path := filepath.Join(t.TempDir(), "cp.db")
	lite, err := openMigratedSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lite.Close() })
	lite2, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lite2.Close() })
	envs := map[string]*deliveryEnv{"sqlite": {newInboxEnvOn(t, lite), lite2}}
	if url, ok := pgtest.Schema(t); ok {
		pg, err := store.OpenPostgres(url)
		if err != nil {
			t.Fatalf("open postgres: %v", err)
		}
		t.Cleanup(func() { pg.Close() })
		pg2, err := store.OpenPostgres(url)
		if err != nil {
			t.Fatalf("open postgres: %v", err)
		}
		t.Cleanup(func() { pg2.Close() })
		envs["postgres"] = &deliveryEnv{newInboxEnvOn(t, pg), pg2}
	}
	for name, e := range envs {
		e.memoQueuesOn(true)
		t.Run(name, func(t *testing.T) {
			e.t = t
			f(t, e)
		})
	}
}

// fakeQueueAgent is the Agent: POST /sessions under a durable key, the key's reserve and
// abandon, and GET /sessions with each session's queueDelivery.
type fakeQueueAgent struct {
	mu  sync.Mutex
	srv *httptest.Server
	// keys is each durable key's record: reserved, inflight, done, abandoned, refused.
	keys    map[string]string
	bodies  map[string]string // done key -> the create's answer
	creates []map[string]any
	rows    []map[string]any
	// createMode: "" answers the create, "lost" creates and drops the connection, "proxy"
	// creates and answers 502 with no key header (a proxy's error), "invalid" refuses the
	// body before the key (400, no headers), "refuse" refuses it (503, outcome refused),
	// "noreserve" has no reserve route (an older Agent).
	createMode string
	// abandonOverride, when set, is answered to every abandon instead of the record's state.
	abandonOverride func(w http.ResponseWriter, key string)
	// sessionsDown makes GET /sessions fail.
	sessionsDown bool
	// reserveBarrier, when set, holds every reserve until all it counts have arrived.
	reserveBarrier *sync.WaitGroup
	// report makes GET /sessions send the envelope's queueReport marker (an Agent of step 2b).
	report bool
	// reportVersion, when not 0, is the version sent instead of 1.
	reportVersion int
	// refs makes GET /sessions send the envelope's queueRefs marker (an Agent that can show an
	// item that carries a reference).
	refs bool
	// retires is every retire call, in order. retireFail answers that many of them with a
	// session it could not end, retireStatus (when set) answers every call with that status.
	retires      []string
	retireFail   int
	retireStatus int
	// retireCode is the error code of retireStatus's answer, "boom" when empty.
	retireCode string
	// retireRelease names a session a person started after the retire began: it and everything
	// under it are left alone and answered released.
	retireRelease string
	// actMark, when not 0, is the actMark the list envelope carries (an Agent that sends one).
	// retireBodies is the body of every retire call ("" for none), parallel to retires.
	actMark      int64
	retireBodies []string
	// actIncarnation, when not "", is the actIncarnation the list envelope carries beside the mark
	// (an Agent that sends an identity).
	actIncarnation string
	// retireDrop makes that many retire calls lose their answer: the call is recorded and the
	// connection is closed without a reply.
	retireDrop int
	// tidy makes GET /sessions send the envelope's queueTidy marker, and makes the Agent take a
	// curator item on a delivery (an Agent of ADR 0137 step 5).
	tidy bool
	// tidyCalls is every POST /memos/tidy body, tidyTypes the Content-Type of each. tidyReply
	// answers a call: a status and a body (an error body for a code, a reply object for a 200);
	// nil answers 200 with no operations. tidyDelay holds the answer back.
	tidyCalls []map[string]any
	tidyTypes []string
	tidyReply func(req map[string]any) (int, any)
	tidyDelay time.Duration
	// prs makes GET /sessions send the envelope's queuePRs marker, and makes the Agent answer POST
	// /memos/pr-snapshot (an Agent of the addendum's step 2). prCalls is every body; prReply
	// answers one (nil: 503 unanswered). Without prs the route is absent, as on an older Agent.
	prs     bool
	prCalls []memoPRSnapshotRequest
	prReply func(req memoPRSnapshotRequest) (int, any)
	// defers makes GET /sessions send the envelope's queueDefer marker (an Agent that reports the
	// turn starts a hand-back is checked against).
	defers bool
}

// setPRs makes the Agent have the PR snapshot route, answering each read with reply.
func (a *fakeQueueAgent) setPRs(on bool, reply func(req memoPRSnapshotRequest) (int, any)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.prs, a.prReply = on, reply
}

// prCallsSoFar is a copy of the PR snapshot reads answered or begun.
func (a *fakeQueueAgent) prCallsSoFar() []memoPRSnapshotRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]memoPRSnapshotRequest(nil), a.prCalls...)
}

// servePRSnapshot answers POST /memos/pr-snapshot with the route's mark, as the Agent's
// HandleQueuePRSnapshot does on every answer. The caller holds a.mu.
func (a *fakeQueueAgent) servePRSnapshot(w http.ResponseWriter, r *http.Request) {
	var req memoPRSnapshotRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	a.prCalls = append(a.prCalls, req)
	w.Header().Set(agentPRSnapshotHeader, "1")
	if a.prReply == nil {
		agentErr(w, http.StatusServiceUnavailable, "pr_snapshot_unanswered")
		return
	}
	status, body := a.prReply(req)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// setTidy makes the list answers carry the queueTidy marker, or not.
func (a *fakeQueueAgent) setTidy(on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tidy = on
}

// tidyCallCount is how many POST /memos/tidy the Agent has answered or begun to.
func (a *fakeQueueAgent) tidyCallCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.tidyCalls)
}

// serveTidy answers POST /memos/tidy the way the Agent's handler checks a request (workspace/agent
// internal/memotidy request.go): the content type, the trigger, the limits and the bounds. The
// caller holds a.mu.
func (a *fakeQueueAgent) serveTidy(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		agentErr(w, http.StatusBadRequest, "bad_tidy_request")
		return
	}
	a.tidyCalls = append(a.tidyCalls, req)
	a.tidyTypes = append(a.tidyTypes, r.Header.Get("Content-Type"))
	items, _ := req["items"].([]any)
	text := 0
	for _, it := range items {
		m, _ := it.(map[string]any)
		t, _ := m["text"].(string)
		f, _ := m["file"].(string)
		text += len(t) + len(f)
	}
	if !tidyRequestShapeOK(req) {
		agentErr(w, http.StatusBadRequest, "bad_tidy_request")
		return
	}
	lim, _ := req["limits"].(map[string]any)
	if r.Header.Get("Content-Type") != "application/json" || req["trigger"] != "auto" || len(items) == 0 || len(items) > 40 ||
		text > 96<<10 || lim["mergeSources"] != float64(10) || lim["splitParts"] != float64(5) ||
		lim["textBytes"] != float64(16384) || lim["operations"] != float64(80) {
		agentErr(w, http.StatusBadRequest, "bad_tidy_request")
		return
	}
	if d := a.tidyDelay; d > 0 {
		a.mu.Unlock()
		time.Sleep(d)
		a.mu.Lock()
	}
	status, body := http.StatusOK, any(map[string]any{"run": req["run"], "operations": []any{}, "kind": "claude", "model": "m"})
	if a.tidyReply != nil {
		status, body = a.tidyReply(req)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if s, ok := body.(string); ok {
		_, _ = io.WriteString(w, s)
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

// tidyRequestShapeOK is the rest of the real decodeRequest (workspace/agent internal/memotidy
// request.go): ids and labels of a bounded shape, distinct item ids, a kind, UTF-8, a group that is
// not negative and at most 100 distinct destinations.
func tidyRequestShapeOK(req map[string]any) bool {
	textOK := func(v any, max int, nonEmpty bool) bool {
		s, ok := v.(string)
		if !ok || len(s) > max || !utf8.ValidString(s) || (nonEmpty && s == "") {
			return false
		}
		for _, r := range s {
			if unicode.IsControl(r) {
				return false
			}
		}
		return true
	}
	idOK := func(v any) bool {
		s, _ := v.(string)
		return textOK(v, 200, true) && !strings.ContainsFunc(s, unicode.IsSpace)
	}
	q, _ := req["queue"].(map[string]any)
	if !idOK(req["run"]) || !idOK(q["id"]) || !textOK(q["label"], 200, false) {
		return false
	}
	seen := map[string]bool{}
	items, _ := req["items"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		id, _ := m["id"].(string)
		g, _ := m["group"].(float64)
		if !idOK(m["id"]) || seen[id] || (m["kind"] != "text" && m["kind"] != "file") || g < 0 ||
			!textOK(m["text"], 1<<30, false) || !textOK(m["file"], 1<<30, false) {
			return false
		}
		seen[id] = true
	}
	dests, _ := req["destinations"].([]any)
	if len(dests) > 100 {
		return false
	}
	seen = map[string]bool{}
	for _, d := range dests {
		m, _ := d.(map[string]any)
		id, _ := m["id"].(string)
		if !idOK(m["id"]) || !textOK(m["name"], 200, false) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

// tidyErrReply is an Agent's error answer for a code.
func tidyErrReply(status int, code string) func(map[string]any) (int, any) {
	return func(map[string]any) (int, any) {
		return status, map[string]any{"error": map[string]any{"code": code, "message": "x"}}
	}
}

// tidyOpsReply answers 200 with operations built from the request's own ids.
func tidyOpsReply(f func(ids []string, req map[string]any) []any) func(map[string]any) (int, any) {
	return func(req map[string]any) (int, any) {
		var ids []string
		items, _ := req["items"].([]any)
		for _, it := range items {
			ids = append(ids, it.(map[string]any)["id"].(string))
		}
		return http.StatusOK, map[string]any{"run": req["run"], "operations": f(ids, req), "kind": "claude", "model": "m"}
	}
}

// setRefs makes the list answers carry the queueRefs marker, or not.
func (a *fakeQueueAgent) setRefs(on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refs = on
}

// setActIncarnation sets the identity the next list answers carry.
func (a *fakeQueueAgent) setActIncarnation(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.actIncarnation = id
}

// setActMark sets the actMark the next list answers carry.
func (a *fakeQueueAgent) setActMark(n int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.actMark = n
}

// retireBody is the body of the nth retire call (0-based).
func (a *fakeQueueAgent) retireBody(n int) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n >= len(a.retireBodies) {
		return "<no call>"
	}
	return a.retireBodies[n]
}

func newFakeQueueAgent(t *testing.T) *fakeQueueAgent {
	a := &fakeQueueAgent{keys: map[string]string{}, bodies: map[string]string{}}
	a.srv = httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(a.srv.Close)
	return a
}

func agentErr(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"error":{"code":"`+code+`","message":"x"}}`)
}

func (a *fakeQueueAgent) serve(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if b := a.reserveBarrier; b != nil && strings.HasSuffix(path, "/reserve") {
		b.Done()
		b.Wait()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case r.Method == "GET" && path == "/sessions":
		if a.sessionsDown {
			agentErr(w, http.StatusBadGateway, "down")
			return
		}
		env := map[string]any{"sessions": a.rows}
		if a.report {
			env["queueReport"] = 1
			if a.reportVersion != 0 {
				env["queueReport"] = a.reportVersion
			}
		}
		if a.refs {
			env["queueRefs"] = 1
		}
		if a.tidy {
			env["queueTidy"] = 1
		}
		if a.prs {
			env["queuePRs"] = 1
		}
		if a.defers {
			env["queueDefer"] = 1
		}
		if a.actMark != 0 {
			env["actMark"] = a.actMark
			if a.actIncarnation != "" {
				env["actIncarnation"] = a.actIncarnation
			}
		}
		_ = json.NewEncoder(w).Encode(env)
	case r.Method == "POST" && strings.HasPrefix(path, "/sessions/") && strings.HasSuffix(path, "/retire"):
		rb, _ := io.ReadAll(r.Body)
		a.retireBodies = append(a.retireBodies, string(rb))
		if a.retireDrop > 0 {
			a.retireDrop--
			a.retires = append(a.retires, "dropped")
			if hj, ok := w.(http.Hijacker); ok {
				if c, _, err := hj.Hijack(); err == nil {
					_ = c.Close()
				}
			}
			return
		}
		a.serveRetire(w, strings.TrimSuffix(strings.TrimPrefix(path, "/sessions/"), "/retire"))
	case r.Method == "POST" && strings.HasPrefix(path, "/sessions-idempotency/") && strings.HasSuffix(path, "/reserve"):
		key := strings.TrimSuffix(strings.TrimPrefix(path, "/sessions-idempotency/"), "/reserve")
		if a.createMode == "noreserve" {
			http.NotFound(w, r)
			return
		}
		if a.keys[key] == "" {
			a.keys[key] = "reserved"
		}
		w.Header().Set(createKeyHeader, key)
		_, _ = io.WriteString(w, `{"status":"reserved"}`)
	case r.Method == "POST" && strings.HasPrefix(path, "/sessions-idempotency/") && strings.HasSuffix(path, "/abandon"):
		key := strings.TrimSuffix(strings.TrimPrefix(path, "/sessions-idempotency/"), "/abandon")
		if a.abandonOverride != nil {
			a.abandonOverride(w, key)
			return
		}
		// session_handlers.go writeCreateState, after createLedger.abandon.
		w.Header().Set(createKeyHeader, key)
		switch a.keys[key] {
		case "reserved", "abandoned":
			a.keys[key] = "abandoned"
			agentErr(w, http.StatusNotFound, "not_created")
		case "refused":
			agentErr(w, http.StatusNotFound, "not_created")
		case "done":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, a.bodies[key])
		case "inflight":
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"status":"creating"}`)
		default:
			agentErr(w, http.StatusServiceUnavailable, "create_outcome_unknown")
		}
	case r.Method == "POST" && path == "/memos/tidy":
		a.serveTidy(w, r)
	case r.Method == "POST" && path == "/memos/pr-snapshot" && a.prs:
		a.servePRSnapshot(w, r)
	case r.Method == "POST" && path == "/sessions":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		key, _ := body["idempotency_key"].(string)
		a.creates = append(a.creates, body)
		// The real Agent checks the delivery before it takes the key (sessionx
		// checkQueueDelivery), and its refusal carries no create headers.
		if a.createMode == "invalid" || !validQueueDelivery(body) || (hasCuratorItem(body) && !a.tidy) {
			agentErr(w, http.StatusBadRequest, "bad_queue_delivery")
			return
		}
		if a.keys[key] == "abandoned" || a.createMode == "refuse" {
			if a.createMode == "refuse" {
				a.keys[key] = "refused"
			}
			w.Header().Set(createKeyHeader, key)
			w.Header().Set(createOutcomeHeader, "refused")
			agentErr(w, http.StatusServiceUnavailable, "memory_pressure")
			return
		}
		if a.keys[key] == "done" {
			w.Header().Set(createKeyHeader, key)
			w.Header().Set(createOutcomeHeader, "created")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, a.bodies[key])
			return
		}
		name := "q-" + strconv.Itoa(len(a.rows)+1)
		qd, _ := body["queue_delivery"].(map[string]any)
		id, _ := qd["id"].(string)
		a.rows = append(a.rows, map[string]any{"name": name, "kind": "claude", "alive": true, "state": "working", "queueDelivery": id})
		a.keys[key] = "done"
		a.bodies[key] = `{"name":"` + name + `"}`
		switch a.createMode {
		case "lost":
			// The connection drops before the answer: no answer at all.
			hj, _ := w.(http.Hijacker)
			c, _, _ := hj.Hijack()
			_ = c.Close()
			return
		case "proxy":
			// A proxy's own error after the Agent created: an answer without the outcome.
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":"upstream timed out"}`)
			return
		}
		w.Header().Set(createKeyHeader, key)
		w.Header().Set(createOutcomeHeader, "created")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, a.bodies[key])
	default:
		http.NotFound(w, r)
	}
}

// serveRetire answers POST /sessions/{name}/retire the way the Agent does: the session and every
// session it started (origin "session", any depth), each ended and stopped, the named one first.
// Ending clears a row's wakers and booked resume and stops it. The caller holds a.mu.
func (a *fakeQueueAgent) serveRetire(w http.ResponseWriter, name string) {
	a.retires = append(a.retires, name)
	if a.retireStatus != 0 {
		code := a.retireCode
		if code == "" {
			code = "boom"
		}
		agentErr(w, a.retireStatus, code)
		return
	}
	var tree []map[string]any
	seen := map[string]bool{}
	var walk func(n string)
	walk = func(n string) {
		for _, r := range a.rows {
			if r["name"] == n && !seen[n] {
				seen[n] = true
				tree = append(tree, r)
				for _, c := range a.rows {
					if c["origin"] == "session" && c["originSession"] == n {
						walk(c["name"].(string))
					}
				}
			}
		}
	}
	walk(name)
	if len(tree) == 0 {
		agentErr(w, http.StatusNotFound, "not_found")
		return
	}
	if root := tree[0]; root["origin"] == "session" && root["originSession"] != nil && root["originSession"] != "" {
		agentErr(w, http.StatusConflict, "retire_not_root")
		return
	}
	fail := a.retireFail > 0
	if fail {
		a.retireFail--
	}
	released := map[string]bool{}
	if a.retireRelease != "" {
		var mark func(n string)
		mark = func(n string) {
			released[n] = true
			for _, c := range a.rows {
				if c["origin"] == "session" && c["originSession"] == n {
					mark(c["name"].(string))
				}
			}
		}
		mark(a.retireRelease)
	}
	type rep struct {
		Name  string `json:"name"`
		Ended struct {
			Watches  int `json:"watches"`
			Timers   int `json:"timers"`
			Builtins int `json:"builtins"`
		} `json:"ended"`
		ResumeCancelled bool     `json:"resumeCancelled"`
		Stopped         bool     `json:"stopped"`
		Released        bool     `json:"released"`
		Running         bool     `json:"running"`
		Unended         []string `json:"unended"`
	}
	out := []rep{}
	complete := true
	for _, r := range tree {
		n := r["name"].(string)
		x := rep{Name: n, Stopped: true, Unended: []string{}}
		switch {
		case released[n]:
			x.Stopped, x.Released, x.Running = false, true, true
			complete = false
		case fail:
			x.Stopped, x.Unended = false, []string{"stop: boom"}
			complete = false
		default:
			r["alive"], r["state"] = false, ""
			delete(r, "wakers")
			delete(r, "wakersUnknown")
			delete(r, "rateLimitResumeAt")
		}
		out = append(out, x)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"sessions": out, "complete": complete})
}

func (a *fakeQueueAgent) retireCalls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.retires)
}

// validQueueDelivery is the part of the Agent's checkQueueDelivery the CP has to respect.
func validQueueDelivery(body map[string]any) bool {
	if p, _ := body["initial_prompt"].(string); p != "" {
		return false
	}
	qd, _ := body["queue_delivery"].(map[string]any)
	items, _ := qd["items"].([]any)
	if len(items) == 0 || len(items) > 20 {
		return false
	}
	for _, it := range items {
		m, _ := it.(map[string]any)
		b, _ := m["body"].(string)
		switch m["provenance"] {
		case "member", "workspace", "mcp":
			if _, ok := m["sourceProvenance"]; ok {
				return false
			}
			if n, _ := m["derivedFrom"].(float64); n != 0 {
				return false
			}
		case "curator":
			// checkCuratorItem: a plain text note whose sources' provenance and count are named.
			sp, _ := m["sourceProvenance"].(string)
			n, _ := m["derivedFrom"].(float64)
			path, _ := m["refPath"].(string)
			atts, _ := m["attachments"].([]any)
			if m["kind"] != "text" || m["reference"] != nil || path != "" || len(atts) != 0 || n < 1 || n > 100 ||
				(sp != "member" && sp != "workspace" && sp != "mcp") {
				return false
			}
		default:
			return false
		}
		if len(b) > 64<<10 {
			return false
		}
	}
	return true
}

// hasCuratorItem says a create body carries a derived message, which an Agent without the
// queueTidy marker refuses.
func hasCuratorItem(body map[string]any) bool {
	qd, _ := body["queue_delivery"].(map[string]any)
	items, _ := qd["items"].([]any)
	for _, it := range items {
		if m, _ := it.(map[string]any); m["provenance"] == "curator" {
			return true
		}
	}
	return false
}

func (a *fakeQueueAgent) createCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.creates)
}

// setRow sets fields on the session row named name.
func (a *fakeQueueAgent) setRow(name string, kv map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, r := range a.rows {
		if r["name"] == name {
			for k, v := range kv {
				r[k] = v
			}
		}
	}
}

func (a *fakeQueueAgent) dropRow(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.rows[:0]
	for _, r := range a.rows {
		if r["name"] != name {
			out = append(out, r)
		}
	}
	a.rows = out
}

// useQueueAgent puts the fake behind member "a"'s workspace, in state ("" = running).
func (e *deliveryEnv) useQueueAgent(state string) *fakeQueueAgent {
	e.t.Helper()
	a := newFakeQueueAgent(e.t)
	ws := store.Workspace{ID: "ws-a", TenantID: e.t1.ID, MembershipID: e.members["a"], ContainerName: "a",
		Network: "n", DataDir: "/d", AgentPort: "1", AgentToken: "tok", State: "running", CreatedAt: store.NowTS()}
	if _, ok, _ := e.st.GetWorkspaceByMembership(context.Background(), e.members["a"]); !ok {
		if err := e.st.CreateWorkspace(context.Background(), ws); err != nil {
			e.t.Fatal(err)
		}
	}
	e.setRuntime(a, state)
	return a
}

func (e *deliveryEnv) setRuntime(a *fakeQueueAgent, state string) {
	ws, _, _ := e.st.GetWorkspaceByMembership(context.Background(), e.members["a"])
	e.mgr.mu.Lock()
	e.mgr.rts[e.members["a"]] = cachedRT{rt: stubRuntime{endpoint: a.srv.URL, token: "tok", state: state}, ws: ws}
	e.mgr.mu.Unlock()
}

func (e *deliveryEnv) runner() *memoQueueRunner {
	f := &wakeFirer{mgr: e.mgr, wsAPI: newWorkspaceAPI(e.mgr, true), settle: time.Millisecond, readyTimeout: time.Second}
	return newMemoQueueRunner(e.mgr, f)
}

// tick is one scheduler tick of queue work: settle, then the sends it answered.
func (e *deliveryEnv) tick(r *memoQueueRunner, now time.Time) {
	for _, d := range r.settle(context.Background(), now) {
		r.send(context.Background(), d)
	}
}

// queueWith makes (repo, name) a queue with these settings over the defaults.
func (e *deliveryEnv) queueWith(repo, name string, settings map[string]any) string {
	e.t.Helper()
	id := e.category(repo, name)
	body := map[string]any{"instruction": "implement it and open a PR"}
	for k, v := range settings {
		body[k] = v
	}
	if rec := e.do("a", "PUT", "/api/memo-queues/"+id, body); rec.Code != http.StatusOK {
		e.t.Fatalf("put queue: %d %s", rec.Code, rec.Body.String())
	}
	return id
}

// pending adds a message the member wrote through the Console: it enters pending.
func (e *deliveryEnv) pending(repo, category, text string) memoDTO {
	e.t.Helper()
	rec := e.do("a", "POST", "/api/memos", textMemo(repo, category, text))
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	m := decode[memoDTO](e.t, rec)
	if m.State != store.MemoStatePending {
		e.t.Fatalf("a Console note in a queue entered %q", m.State)
	}
	return m
}

func (e *deliveryEnv) fire(queueID string) *httptest.ResponseRecorder {
	return e.do("a", "POST", "/api/memo-queues/"+queueID+"/fire", nil)
}

func (e *deliveryEnv) fired(queueID string) memoDeliveryDTO {
	e.t.Helper()
	rec := e.fire(queueID)
	if rec.Code != http.StatusAccepted {
		e.t.Fatalf("fire: %d %s", rec.Code, rec.Body.String())
	}
	return decode[memoDeliveryDTO](e.t, rec)
}

func (e *deliveryEnv) delivery(id string) store.MemoDelivery {
	e.t.Helper()
	d, ok, err := e.st.GetMemoDelivery(context.Background(), id)
	if err != nil || !ok {
		e.t.Fatalf("delivery %s: ok=%v err=%v", id, ok, err)
	}
	return d
}

func deliveryErrCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	return decode[errResp](t, rec).Error.Code
}

// --- Fire now --------------------------------------------------------------------------------

// One press starts exactly one session, in a new worktree off the queue's repository, with the
// queue's consumer settings and its messages as data. A press while no slot is free is refused,
// and later ticks start nothing more. enabled is not required.
func TestMemoDeliveryFireNowStartsOneSessionPerPress(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"maxActive": 1, "model": "claude-x", "effort": "high", "spendCapUsd": 3})
		m1 := e.pending("app", "impl", "first")
		m2 := e.pending("app", "impl", "second")
		agent := e.useQueueAgent("")

		d := e.fired(q)
		if d.State != store.DeliveryClaimed || len(d.MessageIDs) != 1 || d.MessageIDs[0] != m1.ID {
			t.Fatalf("delivery %+v, want claimed with the oldest message", d)
		}
		if got := e.memoRow(m1.ID); got.State != store.MemoStateClaimed || got.DeliveryID != d.ID {
			t.Fatalf("claimed message %+v", got)
		}
		if rec := e.fire(q); rec.Code != http.StatusConflict || deliveryErrCode(t, rec) != errCodeMemoQueueNoSlot {
			t.Fatalf("second press with no free slot: %d %s", rec.Code, rec.Body.String())
		}

		r := e.runner()
		e.tick(r, time.Now())
		if agent.createCount() != 1 {
			t.Fatalf("%d creates, want 1", agent.createCount())
		}
		c := agent.creates[0]
		qd, _ := c["queue_delivery"].(map[string]any)
		items, _ := qd["items"].([]any)
		if c["source"] != "queue" || c["worktree"] != true || c["dir"] != "repos/app" || c["idempotency_key"] != "q_"+d.ID ||
			c["idempotency_durable"] != true || c["kind"] != "claude" || c["model"] != "claude-x" || c["effort"] != "high" ||
			c["spend_cap_usd"] != 3.0 || qd["id"] != d.ID || qd["queue"] != q || qd["label"] != "app/impl" ||
			qd["instruction"] != "implement it and open a PR" || len(items) != 1 {
			t.Fatalf("create body %v", c)
		}
		it := items[0].(map[string]any)
		if it["id"] != m1.ID || it["body"] != "first" || it["provenance"] != "member" || it["kind"] != "text" {
			t.Fatalf("item %v", it)
		}
		got := e.delivery(d.ID)
		if got.State != store.DeliveryOpen || got.Session != "q-1" || got.SentAt == "" {
			t.Fatalf("after the send: %+v", got)
		}
		if m := e.memoRow(m1.ID); m.State != store.MemoStateDelivered || m.SentAt == "" {
			t.Fatalf("sent message %+v", m)
		}
		if m := e.memoRow(m2.ID); m.State != store.MemoStatePending || m.DeliveryID != "" {
			t.Fatalf("the message not fired %+v", m)
		}
		for i := 0; i < 3; i++ {
			e.tick(r, time.Now())
		}
		if agent.createCount() != 1 {
			t.Fatalf("later ticks created %d sessions", agent.createCount()-1)
		}
		if rec := e.fire(q); rec.Code != http.StatusConflict || deliveryErrCode(t, rec) != errCodeMemoQueueNoSlot {
			t.Fatalf("a press while the open delivery holds the slot: %d %s", rec.Code, rec.Body.String())
		}
	})
}

// The refusals of a press, and the batch.
func TestMemoDeliveryFireRefusals(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent := e.useQueueAgent("")
		q := e.queueWith("app", "impl", map[string]any{"batch": 3})
		if rec := e.fire(q); rec.Code != http.StatusConflict || deliveryErrCode(t, rec) != errCodeMemoQueueNothingPending {
			t.Fatalf("nothing pending: %d %s", rec.Code, rec.Body.String())
		}
		// A held message is not fired: it waits for the member's release.
		held := memoFaceClient{"token", e.inboxEnv}
		if code, _ := held.create(textMemo("app", "impl", "from a session")); code != http.StatusCreated {
			t.Fatalf("token create: %d", code)
		}
		if rec := e.fire(q); rec.Code != http.StatusConflict || deliveryErrCode(t, rec) != errCodeMemoQueueNothingPending {
			t.Fatalf("only a held message: %d %s", rec.Code, rec.Body.String())
		}
		for i := 0; i < 4; i++ {
			e.pending("app", "impl", fmt.Sprintf("m%d", i))
		}
		if rec := e.do("a", "POST", "/api/memo-queues/nope/fire", nil); rec.Code != http.StatusNotFound {
			t.Fatalf("unknown queue: %d", rec.Code)
		}
		if rec := e.do("a", "POST", "/api/memo-queues/"+e.category("app", "plain")+"/fire", nil); rec.Code != http.StatusNotFound {
			t.Fatalf("a category that is not a queue: %d", rec.Code)
		}
		// No instruction.
		noInstr := e.category("app", "bare")
		if rec := e.do("a", "PUT", "/api/memo-queues/"+noInstr, map[string]any{}); rec.Code != http.StatusOK {
			t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
		}
		e.pending("app", "bare", "x")
		if rec := e.fire(noInstr); rec.Code != http.StatusConflict || deliveryErrCode(t, rec) != errCodeMemoQueueNoInstruction {
			t.Fatalf("no instruction: %d %s", rec.Code, rec.Body.String())
		}
		// The tenant switch.
		e.memoQueuesOn(false)
		if rec := e.fire(q); rec.Code != http.StatusConflict || deliveryErrCode(t, rec) != errCodeMemoQueuesDisabled {
			t.Fatalf("switch off: %d %s", rec.Code, rec.Body.String())
		}
		e.memoQueuesOn(true)
		// The max_sessions quota: one running session and a cap of one.
		if err := e.st.PutUserLimit(context.Background(), e.members["a"], store.UserQuota{MaxSessions: 1}); err != nil {
			t.Fatal(err)
		}
		agent.rows = append(agent.rows, map[string]any{"name": "busy", "kind": "claude", "alive": true, "state": "working"})
		if rec := e.fire(q); rec.Code != http.StatusTooManyRequests || deliveryErrCode(t, rec) != errCodeQuotaSessions {
			t.Fatalf("quota: %d %s", rec.Code, rec.Body.String())
		}
		agent.rows = nil
		d := e.fired(q)
		if len(d.MessageIDs) != 3 {
			t.Fatalf("batch 3 claimed %d", len(d.MessageIDs))
		}
		// A delivery not yet sent counts toward the quota: it has no session to count yet.
		if rec := e.fire(q); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("quota with a delivery starting: %d %s", rec.Code, rec.Body.String())
		}
	})
}

// A released message whose content no longer matches its release is not fired: it goes back
// to held for a new press, and that sticks although the press is refused.
func TestMemoDeliveryClaimSkipsAStaleRelease(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", nil)
		stale := e.pending("app", "impl", "released once")
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET release_hash='not-this-content' WHERE id=?`, stale.ID); err != nil {
			t.Fatal(err)
		}
		if rec := e.fire(q); rec.Code != http.StatusConflict || deliveryErrCode(t, rec) != errCodeMemoQueueNothingPending {
			t.Fatalf("fire of a stale release: %d %s", rec.Code, rec.Body.String())
		}
		if got := e.memoRow(stale.ID); got.State != store.MemoStateHeld || got.ReleaseHash != "" || got.DeliveryID != "" {
			t.Fatalf("stale release after the press: %+v", got)
		}
		fresh := e.pending("app", "impl", "released now")
		d := e.fired(q)
		if len(d.MessageIDs) != 1 || d.MessageIDs[0] != fresh.ID {
			t.Fatalf("claimed %v, want only the fresh one", d.MessageIDs)
		}
	})
}

// Two Control Planes that see the same free slot cannot both reserve it, also when they pick
// different messages, and the member-wide limit holds the same way.
func TestMemoDeliveryReservationHoldsAcrossControlPlanes(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		mid := e.members["a"]
		qs := []string{
			e.queueWith("app", "a", map[string]any{"maxActive": 2, "batch": 1}),
			e.queueWith("app", "b", map[string]any{"maxActive": 6, "batch": 1}),
			e.queueWith("app", "c", map[string]any{"maxActive": 6, "batch": 1}),
		}
		for _, c := range []string{"a", "b", "c"} {
			for i := 0; i < 8; i++ {
				e.pending("app", c, fmt.Sprintf("%s%d", c, i))
			}
		}
		fire := func(st *store.SQL, q string) (bool, error) {
			var aerr *apiError
			err := st.MemoTx(context.Background(), mid, func(tx store.MemoTxOps) error {
				var err error
				_, aerr, err = memoReserveAndClaim(tx, q, -1, 0, false)
				return err
			})
			if err != nil {
				return false, err
			}
			if aerr != nil && aerr.code != errCodeMemoQueueNoSlot && aerr.code != errCodeMemoQueueMemberLimit {
				return false, fmt.Errorf("refusal %s", aerr.code)
			}
			return aerr == nil, nil
		}
		race := func(q []string, n int) (int, []error) {
			var wg sync.WaitGroup
			var mu sync.Mutex
			won, errs := 0, []error{}
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					st := e.st
					if i%2 == 1 {
						st = e.other
					}
					ok, err := fire(st, q[i%len(q)])
					mu.Lock()
					defer mu.Unlock()
					if err != nil {
						errs = append(errs, err)
					} else if ok {
						won++
					}
				}(i)
			}
			wg.Wait()
			return won, errs
		}
		if won, errs := race(qs[:1], 8); won != 2 || len(errs) != 0 {
			t.Fatalf("one queue with 2 slots: %d reservations, errors %v", won, errs)
		}
		if won, errs := race(qs[1:], 12); won != memoQueueMemberLimit-2 || len(errs) != 0 {
			t.Fatalf("member-wide: %d more reservations, want %d, errors %v", won, memoQueueMemberLimit-2, errs)
		}
		rows, _ := e.st.DB().QueryContext(context.Background(), `SELECT COUNT(*) FROM memo WHERE membership_id=? AND state='claimed'`, mid)
		defer rows.Close()
		var claimed int
		for rows.Next() {
			_ = rows.Scan(&claimed)
		}
		if claimed != memoQueueMemberLimit {
			t.Fatalf("%d messages claimed, want %d", claimed, memoQueueMemberLimit)
		}
	})
}

// --- Reconciliation ---------------------------------------------------------------------------

// stale is a clock far enough ahead that every claimed or sending delivery is past its budget.
func (e *deliveryEnv) stale(r *memoQueueRunner) time.Time {
	return time.Now().Add(r.budget + time.Minute)
}

// A claimed delivery past its budget closes as not sent and its messages are pending again. A
// slow worker whose write to sending won first keeps it: the close loses and returns nothing.
func TestMemoDeliveryStaleClaimClosesOnlyByItsState(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", nil)
		m := e.pending("app", "impl", "one")
		agent := e.useQueueAgent("")
		r := e.runner()

		d := e.fired(q)
		r.settle(context.Background(), e.stale(r))
		if got := e.delivery(d.ID); got.State != store.DeliveryClosed || got.Outcome != store.DeliveryNotSent || got.ClosedAt == "" {
			t.Fatalf("stale claim: %+v", got)
		}
		if got := e.memoRow(m.ID); got.State != store.MemoStatePending || got.DeliveryID != "" {
			t.Fatalf("message after the close: %+v", got)
		}
		e.assertPendingReleaseMatches()
		if agent.createCount() != 0 {
			t.Fatal("a closed claim was sent")
		}

		// The slow worker moves to sending first; the reconciliation's close of the claim loses.
		d2 := e.fired(q)
		won, err := memoDeliveryMove(context.Background(), e.st, e.delivery(d2.ID), store.DeliveryClaimed, store.DeliverySending, "", "", nil)
		if err != nil || !won {
			t.Fatalf("move to sending: %v %v", won, err)
		}
		r.closeNotSent(context.Background(), e.delivery(d2.ID), store.DeliveryClaimed)
		if got := e.delivery(d2.ID); got.State != store.DeliverySending {
			t.Fatalf("a close of a claim moved a sending delivery: %+v", got)
		}
		if got := e.memoRow(m.ID); got.State != store.MemoStateClaimed || got.DeliveryID != d2.ID {
			t.Fatalf("the losing close returned the message: %+v", got)
		}
		// And the other way round: once the close won, the slow worker cannot move to sending.
		d3 := store.MemoDelivery{}
		{
			e.pending("app", "impl", "two")
			// The single default batch claims one; the queue has two slots and d2 holds one.
			d3 = e.delivery(e.fired(q).ID)
		}
		r.closeNotSent(context.Background(), d3, store.DeliveryClaimed)
		if won, _ := memoDeliveryMove(context.Background(), e.st, d3, store.DeliveryClaimed, store.DeliverySending, "", "", nil); won {
			t.Fatal("a worker moved a closed delivery to sending")
		}
	})
}

// sendingDelivery fires and sends one delivery whose create answer is lost: it is left sending.
func (e *deliveryEnv) sendingDelivery(agent *fakeQueueAgent, r *memoQueueRunner, q string, mode string) store.MemoDelivery {
	e.t.Helper()
	d := e.fired(q)
	agent.mu.Lock()
	agent.createMode = mode
	agent.mu.Unlock()
	e.tick(r, time.Now())
	agent.mu.Lock()
	agent.createMode = ""
	agent.mu.Unlock()
	got := e.delivery(d.ID)
	if got.State != store.DeliverySending {
		e.t.Fatalf("a create with mode %q left %+v", mode, got)
	}
	return got
}

// A Control Plane that lost the create's answer, or restarted between the claim and the
// create, starts nothing twice: reconciliation acts on each abandon answer as the Agent gives
// it, and on none.
func TestMemoDeliveryReconcileSendingOnEachAbandonAnswer(t *testing.T) {
	type tc struct {
		name string
		// mode is how the create went: "lost" created a session and its answer was lost,
		// "crash" never reached the Agent (the CP died after the move to sending).
		mode     string
		override func(w http.ResponseWriter, key string)
		state    string // the workspace state during reconciliation
		want     string // delivery state
		outcome  string
		msg      string // message state
	}
	cases := []tc{
		{name: "200 done", mode: "lost", want: store.DeliveryOpen, msg: store.MemoStateDelivered},
		{name: "202 in flight", mode: "crash", want: store.DeliverySending, msg: store.MemoStateClaimed,
			override: func(w http.ResponseWriter, key string) {
				w.Header().Set(createKeyHeader, key)
				w.WriteHeader(http.StatusAccepted)
			}},
		{name: "404 not created", mode: "crash", want: store.DeliveryClosed, outcome: store.DeliveryNotSent, msg: store.MemoStatePending},
		{name: "503 session found", mode: "lost", want: store.DeliveryOpen, msg: store.MemoStateDelivered,
			override: func(w http.ResponseWriter, key string) {
				w.Header().Set(createKeyHeader, key)
				agentErr(w, http.StatusServiceUnavailable, "create_outcome_unknown")
			}},
		{name: "503 no session", mode: "crash", want: store.DeliveryClosed, outcome: store.DeliveryUnknown, msg: store.MemoStateUnconfirmed,
			override: func(w http.ResponseWriter, key string) {
				w.Header().Set(createKeyHeader, key)
				agentErr(w, http.StatusServiceUnavailable, "create_outcome_unknown")
			}},
		{name: "a proxy's 503 without the key", mode: "crash", want: store.DeliverySending, msg: store.MemoStateClaimed,
			override: func(w http.ResponseWriter, key string) {
				agentErr(w, http.StatusServiceUnavailable, "create_outcome_unknown")
			}},
		{name: "no answer", mode: "crash", want: store.DeliverySending, msg: store.MemoStateClaimed,
			override: func(w http.ResponseWriter, key string) {
				hj, _ := w.(http.Hijacker)
				c, _, _ := hj.Hijack()
				_ = c.Close()
			}},
		{name: "workspace stopped", mode: "lost", state: "stopped", want: store.DeliverySending, msg: store.MemoStateClaimed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
				q := e.queueWith("app", "impl", nil)
				m := e.pending("app", "impl", "one")
				agent := e.useQueueAgent("")
				r := e.runner()
				var d store.MemoDelivery
				if c.mode == "lost" {
					d = e.sendingDelivery(agent, r, q, "lost")
				} else {
					// The CP reserved the key and moved to sending, then died before the create.
					d = e.delivery(e.fired(q).ID)
					res, aerr := e.mgr.resolveByMembership(context.Background(), mustIdentity(e, "a"), e.members["a"])
					if aerr != nil {
						t.Fatal(aerr.message)
					}
					if err := reserveDeliveryCreate(context.Background(), res, d.ID); err != nil {
						t.Fatal(err)
					}
					if won, err := memoDeliveryMove(context.Background(), e.st, d, store.DeliveryClaimed, store.DeliverySending, "", "", nil); !won || err != nil {
						t.Fatalf("move: %v %v", won, err)
					}
				}
				creates := agent.createCount()
				agent.mu.Lock()
				agent.abandonOverride = c.override
				agent.mu.Unlock()
				e.setRuntime(agent, c.state)
				// A second process's runner: what a restarted Control Plane does.
				e.tick(e.runner(), e.stale(r))

				got := e.delivery(d.ID)
				if got.State != c.want || got.Outcome != c.outcome {
					t.Fatalf("delivery %s/%s, want %s/%s", got.State, got.Outcome, c.want, c.outcome)
				}
				if c.want == store.DeliveryOpen && got.Session != "q-1" {
					t.Fatalf("opened with session %q", got.Session)
				}
				if mm := e.memoRow(m.ID); mm.State != c.msg {
					t.Fatalf("message %s, want %s", mm.State, c.msg)
				}
				e.assertPendingReleaseMatches()
				if agent.createCount() != creates {
					t.Fatalf("reconciliation sent %d creates", agent.createCount()-creates)
				}
			})
		})
	}
}

func mustIdentity(e *deliveryEnv, who string) string {
	id, ok, err := e.st.IdentityIDForMembership(context.Background(), e.members[who])
	if err != nil || !ok {
		e.t.Fatalf("identity of %s: %v %v", who, ok, err)
	}
	return id
}

// The create's own answer settles a send: a refusal is provably not sent, a lost answer leaves
// the slot until the abandon answer, and an Agent without the reserve route gets no create.
func TestMemoDeliverySendSettlesOnTheCreateAnswer(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"maxActive": 1})
		m := e.pending("app", "impl", "one")
		agent := e.useQueueAgent("")
		r := e.runner()

		d := e.fired(q)
		agent.createMode = "refuse"
		e.tick(r, time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryClosed || got.Outcome != store.DeliveryNotSent {
			t.Fatalf("refused create: %+v", got)
		}
		if got := e.memoRow(m.ID); got.State != store.MemoStatePending {
			t.Fatalf("message after a refusal: %+v", got)
		}

		agent.createMode = "noreserve"
		d = e.fired(q)
		creates := agent.createCount()
		e.tick(r, time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryClosed || got.Outcome != store.DeliveryNotSent || agent.createCount() != creates {
			t.Fatalf("an Agent without reserve: %+v, %d creates", got, agent.createCount()-creates)
		}

		agent.createMode = ""
		d2 := e.sendingDelivery(agent, r, q, "lost")
		if rec := e.fire(q); rec.Code != http.StatusConflict {
			t.Fatalf("a delivery with no answer must keep its slot: %d", rec.Code)
		}
		e.tick(r, time.Now()) // not past its budget: nothing is decided
		if got := e.delivery(d2.ID); got.State != store.DeliverySending {
			t.Fatalf("decided before the budget: %+v", got)
		}
		e.tick(r, e.stale(r))
		if got := e.delivery(d2.ID); got.State != store.DeliveryOpen || got.Session == "" {
			t.Fatalf("after reconciliation: %+v", got)
		}
		if n := agent.createCount(); n != creates+1 {
			t.Fatalf("%d creates for one press", n-creates)
		}
	})
}

// The tick bounds the queue sends it starts, so the schedules keep their share of the fires.
func TestMemoDeliverySettleBoundsSendsPerTick(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		e.useQueueAgent("")
		for _, c := range []string{"a", "b", "c"} {
			q := e.queueWith("app", c, nil)
			e.pending("app", c, "x")
			e.fired(q)
		}
		if n := len(e.runner().settle(context.Background(), time.Now())); n != memoQueueSendsPerTick {
			t.Fatalf("settle answered %d sends, want %d", n, memoQueueSendsPerTick)
		}
	})
}

// --- Closing ----------------------------------------------------------------------------------

// An open delivery closes when its session is archived, deleted, stopped without a carried
// interaction, or idle on two consecutive ticks, also across a Control Plane restart. Working
// and a person's turn keep it open.
func TestMemoDeliveryClosingRule(t *testing.T) {
	type step struct {
		row   map[string]any // fields set on the session row; nil = the row is deleted
		state string         // the workspace state
		down  bool           // the Agent does not answer GET /sessions
		want  string
	}
	idle := map[string]any{"alive": true, "state": "idle"}
	cases := map[string][]step{
		"working then question then idle twice": {
			{row: map[string]any{"alive": true, "state": "working"}, want: store.DeliveryOpen},
			{row: map[string]any{"alive": true, "state": "question"}, want: store.DeliveryOpen},
			{row: idle, want: store.DeliveryOpen},
			{row: idle, want: store.DeliveryClosed},
		},
		"idle, working, idle is not two in a row": {
			{row: idle, want: store.DeliveryOpen},
			{row: map[string]any{"alive": true, "state": "working"}, want: store.DeliveryOpen},
			{row: idle, want: store.DeliveryOpen},
		},
		"limited keeps the slot": {
			{row: map[string]any{"alive": true, "state": "limited"}, want: store.DeliveryOpen},
			{row: map[string]any{"alive": true, "state": "limited"}, want: store.DeliveryOpen},
		},
		"archived":      {{row: map[string]any{"archived": true}, want: store.DeliveryClosed}},
		"deleted":       {{row: nil, want: store.DeliveryClosed}},
		"stopped clean": {{row: map[string]any{"alive": false, "state": ""}, want: store.DeliveryClosed}},
		"stopped with a question carried": {
			{row: map[string]any{"alive": false, "state": "", "carried": "question"}, want: store.DeliveryOpen},
		},
		"workspace stopped, mirror carries a question": {
			{row: map[string]any{"carried": "question"}, state: "stopped", want: store.DeliveryOpen},
		},
		"workspace stopped, mirror carries nothing": {
			{row: map[string]any{}, state: "stopped", want: store.DeliveryClosed},
		},
		"workspace stopped, not in the mirror": {
			{row: nil, state: "stopped", want: store.DeliveryOpen},
		},
		// A running workspace whose Agent does not answer proves nothing, whatever the mirror
		// holds, and the idle ticks around the gap are not consecutive.
		"running, the Agent does not answer": {
			{row: idle, want: store.DeliveryOpen},
			{row: map[string]any{}, down: true, want: store.DeliveryOpen},
			{row: idle, want: store.DeliveryOpen},
		},
		"starting, the mirror shows it stopped": {
			{row: map[string]any{}, state: "starting", want: store.DeliveryOpen},
		},
	}
	for name, steps := range cases {
		t.Run(name, func(t *testing.T) {
			eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
				q := e.queueWith("app", "impl", nil)
				e.pending("app", "impl", "one")
				agent := e.useQueueAgent("")
				d := e.fired(q)
				e.tick(e.runner(), time.Now())
				open := e.delivery(d.ID)
				if open.State != store.DeliveryOpen {
					t.Fatalf("not open: %+v", open)
				}
				for i, s := range steps {
					if s.row == nil {
						agent.dropRow(open.Session)
					} else {
						agent.setRow(open.Session, s.row)
					}
					agent.mu.Lock()
					agent.sessionsDown = s.down
					agent.mu.Unlock()
					if s.state == "stopped" || s.state == "starting" || s.down {
						ws, _, _ := e.st.GetWorkspaceByMembership(context.Background(), e.members["a"])
						var rows []store.SessionRow
						if s.row != nil {
							carried, _ := s.row["carried"].(string)
							state := "stopped"
							if s.down {
								state = "running"
							}
							rows = append(rows, store.SessionRow{Name: open.Session, Kind: "claude", State: state, Carried: carried})
						}
						if err := e.st.ReplaceSessions(context.Background(), ws.ID, rows); err != nil {
							t.Fatal(err)
						}
					}
					e.setRuntime(agent, s.state)
					// A fresh runner every tick: the idle mark is in the database, not in memory.
					e.runner().settle(context.Background(), time.Now())
					if got := e.delivery(open.ID); got.State != s.want {
						t.Fatalf("step %d: delivery %s, want %s (%+v)", i, got.State, s.want, got)
					}
				}
			})
		})
	}
}

// --- Wire -------------------------------------------------------------------------------------

// The deliveries list is newest first, at most 50, never null, and the memo DTO names its
// delivery.
func TestMemoDeliveryListWire(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", nil)
		rec := e.do("a", "GET", "/api/memo-queues/"+q+"/deliveries", nil)
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Fatalf("empty list: %d %s", rec.Code, rec.Body.String())
		}
		e.useQueueAgent("")
		m := e.pending("app", "impl", "one")
		d1 := e.fired(q)
		e.pending("app", "impl", "two")
		d2 := e.fired(q)
		list := decode[[]memoDeliveryDTO](t, e.do("a", "GET", "/api/memo-queues/"+q+"/deliveries", nil))
		if len(list) != 2 || list[0].ID != d2.ID || list[1].ID != d1.ID || list[1].MessageIDs[0] != m.ID {
			t.Fatalf("list %+v", list)
		}
		memos := decode[[]memoDTO](t, e.do("a", "GET", "/api/memos", nil))
		found := false
		for _, mm := range memos {
			if mm.ID == m.ID {
				found = mm.DeliveryID == d1.ID && mm.State == store.MemoStateClaimed
			}
		}
		if !found {
			t.Fatalf("memo DTO without its delivery: %+v", memos)
		}
		// Another member sees none of them.
		if rec := e.do("c", "GET", "/api/memo-queues/"+q+"/deliveries", nil); strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Fatalf("another member's list: %s", rec.Body.String())
		}
		// Bounded at memoDeliveryListMax.
		for i := 0; i < memoDeliveryListMax+3; i++ {
			at := store.DeliveryTS(time.Now().Add(-time.Duration(i+10) * time.Hour))
			if err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
				return tx.InsertDelivery(store.MemoDelivery{ID: store.NewID(), QueueID: q, State: store.DeliveryClosed,
					Outcome: store.DeliveryClean, CreatedAt: at, UpdatedAt: at, ClosedAt: at})
			}); err != nil {
				t.Fatal(err)
			}
		}
		list = decode[[]memoDeliveryDTO](t, e.do("a", "GET", "/api/memo-queues/"+q+"/deliveries", nil))
		if len(list) != memoDeliveryListMax || list[0].ID != d2.ID {
			t.Fatalf("bounded list: %d, first %s", len(list), list[0].ID)
		}
		// An open run older than everything the list holds is left out of it, and ?include=open
		// brings it back without losing the order.
		oldAt := store.DeliveryTS(time.Now().Add(-1000 * time.Hour))
		old := store.MemoDelivery{ID: store.NewID(), QueueID: q, State: store.DeliveryOpen, CreatedAt: oldAt, UpdatedAt: oldAt}
		if err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error { return tx.InsertDelivery(old) }); err != nil {
			t.Fatal(err)
		}
		list = decode[[]memoDeliveryDTO](t, e.do("a", "GET", "/api/memo-queues/"+q+"/deliveries", nil))
		for _, d := range list {
			if d.ID == old.ID {
				t.Fatalf("the old open run is inside the bound: %d", len(list))
			}
		}
		list = decode[[]memoDeliveryDTO](t, e.do("a", "GET", "/api/memo-queues/"+q+"/deliveries?include=open", nil))
		if len(list) != memoDeliveryListMax+1 || list[0].ID != d2.ID || list[len(list)-1].ID != old.ID {
			t.Fatalf("include=open: %d rows, first %s, last %s", len(list), list[0].ID, list[len(list)-1].ID)
		}
	})
}

// --- Review round 1 ---------------------------------------------------------------------------

// The quota holds while the workspace is stopped (the deliveries not yet sent are a lower
// bound), and two sends of one member that each counted no running session cannot both
// create: the second sees the first in the move to sending.
func TestMemoDeliveryQuotaHoldsForParallelSends(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", nil) // two slots
		m1 := e.pending("app", "impl", "one")
		m2 := e.pending("app", "impl", "two")
		agent := e.useQueueAgent("stopped")
		if err := e.st.PutUserLimit(context.Background(), e.members["a"], store.UserQuota{MaxSessions: 1}); err != nil {
			t.Fatal(err)
		}
		d1 := e.fired(q)
		if rec := e.fire(q); rec.Code != http.StatusTooManyRequests || deliveryErrCode(t, rec) != errCodeQuotaSessions {
			t.Fatalf("a second press while stopped with max_sessions 1: %d %s", rec.Code, rec.Body.String())
		}
		// The cap is raised for the presses and lowered again before the sends.
		if err := e.st.PutUserLimit(context.Background(), e.members["a"], store.UserQuota{MaxSessions: 2}); err != nil {
			t.Fatal(err)
		}
		d2 := e.fired(q)
		if err := e.st.PutUserLimit(context.Background(), e.members["a"], store.UserQuota{MaxSessions: 1}); err != nil {
			t.Fatal(err)
		}
		e.setRuntime(agent, "")
		var b sync.WaitGroup
		b.Add(2)
		agent.reserveBarrier = &b
		r := e.runner()
		var wg sync.WaitGroup
		for _, id := range []string{d1.ID, d2.ID} {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				r.send(context.Background(), e.delivery(id))
			}(id)
		}
		wg.Wait()
		if n := agent.createCount(); n != 1 {
			t.Fatalf("max_sessions 1, but parallel sends created %d sessions", n)
		}
		states := map[string]int{}
		for _, id := range []string{d1.ID, d2.ID} {
			got := e.delivery(id)
			states[got.State+"/"+got.Outcome]++
		}
		if states["open/"] != 1 || states["closed/not_sent"] != 1 {
			t.Fatalf("deliveries %v", states)
		}
		pending := 0
		for _, id := range []string{m1.ID, m2.ID} {
			if e.memoRow(id).State == store.MemoStatePending {
				pending++
			}
		}
		if pending != 1 {
			t.Fatalf("%d messages pending again, want 1", pending)
		}
		e.assertPendingReleaseMatches()
	})
}

// A message the Agent would refuse is refused at the press, where the member can still edit
// it, instead of holding a slot and coming back on every fire.
func TestMemoDeliveryFireRefusesAnItemTheAgentWouldRefuse(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"batch": 2})
		big := e.pending("app", "impl", strings.Repeat("x", memoDeliveryItemBodyMax+1))
		agent := e.useQueueAgent("")
		rec := e.fire(q)
		if rec.Code != http.StatusConflict || deliveryErrCode(t, rec) != errCodeMemoQueueItemInvalid || !strings.Contains(rec.Body.String(), big.ID) {
			t.Fatalf("fire with an oversized message: %d %s", rec.Code, rec.Body.String())
		}
		if got := e.memoRow(big.ID); got.State != store.MemoStatePending || got.DeliveryID != "" {
			t.Fatalf("the refused message: %+v", got)
		}
		if rec := e.do("a", "PATCH", "/api/memos/"+big.ID, map[string]any{"body": "short now"}); rec.Code != http.StatusOK {
			t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
		}
		e.fired(q)
		e.tick(e.runner(), time.Now())
		if agent.createCount() != 1 {
			t.Fatalf("%d creates", agent.createCount())
		}

		// A message the Agent would refuse behind one it would take: the refusal claims
		// neither, so nothing is left claimed without a delivery.
		good := e.pending("app", "impl", "good")
		bad := e.pending("app", "impl", strings.Repeat("y", memoDeliveryItemBodyMax+1))
		e.st.DB().ExecContext(context.Background(), `UPDATE memo_delivery SET state='closed'`)
		if rec := e.fire(q); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), bad.ID) {
			t.Fatalf("fire of good then bad: %d %s", rec.Code, rec.Body.String())
		}
		if got := e.memoRow(good.ID); got.State != store.MemoStatePending || got.DeliveryID != "" {
			t.Fatalf("the good message after a refused press: %+v", got)
		}
	})
}

// A create the Agent refuses before it takes the key carries no create headers. The abandon
// answer settles it at once: the key was only reserved, so not_created closes the delivery as
// not sent without holding the slot until its budget.
func TestMemoDeliveryRefusalWithoutHeadersSettlesAtOnce(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"maxActive": 1})
		m := e.pending("app", "impl", "one")
		agent := e.useQueueAgent("")
		d := e.fired(q)
		agent.createMode = "invalid"
		e.tick(e.runner(), time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryClosed || got.Outcome != store.DeliveryNotSent {
			t.Fatalf("a header-less refusal: %+v", got)
		}
		if got := e.memoRow(m.ID); got.State != store.MemoStatePending {
			t.Fatalf("message %+v", got)
		}
		// A proxy's error after the Agent created: abandon reports the session at once.
		agent.createMode = "proxy"
		d = e.fired(q)
		e.tick(e.runner(), time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.Session == "" {
			t.Fatalf("a proxy error after a create: %+v", got)
		}
		e.st.DB().ExecContext(context.Background(), `UPDATE memo_delivery SET state='closed' WHERE id=?`, d.ID)
		e.pending("app", "impl", "two")
		// A proxy's error while the create runs is not settled as not sent: abandon says the
		// create is in flight, and the slot is kept.
		agent.createMode = "proxy"
		agent.abandonOverride = func(w http.ResponseWriter, key string) {
			w.Header().Set(createKeyHeader, key)
			w.WriteHeader(http.StatusAccepted)
		}
		d = e.fired(q)
		e.tick(e.runner(), time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliverySending {
			t.Fatalf("a proxy error with a create in flight: %+v", got)
		}
	})
}

// The settle pass keeps its deadline in its writes: a membership lock another Control Plane
// holds does not hold the tick, and the schedules behind it, past the budget.
func TestMemoDeliverySettleKeepsItsDeadline(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", nil)
		e.pending("app", "impl", "one")
		e.useQueueAgent("")
		d := e.fired(q)
		tx, err := e.other.DB().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(context.Background(), `UPDATE membership SET status=status WHERE id=?`, e.members["a"]); err != nil {
			t.Fatal(err)
		}
		release := sync.OnceFunc(func() { _ = tx.Rollback() })
		defer release()
		r := e.runner()
		r.settleBudget = 2 * time.Second
		done := make(chan time.Duration, 1)
		go func() {
			start := time.Now()
			r.settle(context.Background(), e.stale(r))
			done <- time.Since(start)
		}()
		select {
		case took := <-done:
			if took > 10*time.Second {
				t.Fatalf("settle took %s with a 2s budget", took)
			}
		case <-time.After(30 * time.Second):
			release()
			<-done
			t.Fatal("settle waited on the lock past its budget")
		}
		release()
		if got := e.delivery(d.ID); got.State != store.DeliveryClaimed {
			t.Fatalf("a write that timed out decided %+v", got)
		}
	})
}
