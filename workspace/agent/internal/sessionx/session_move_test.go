package sessionx

// The move endpoint (ADR 0115 P2a): the end-to-end order of begin, snapshot, file, create,
// commit and stop, and every way it can stop short. The source is the managed codex fake of
// session_move_gate_test.go; the snapshot and, unless a test says otherwise, the target's create
// are stand-ins, so the file's content and the create's outcome are the test's to choose.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/lifegate"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

type moveRun struct {
	d   *moveFakeDriver
	src session.Meta
	// turns is what the stand-in snapshot reads as the conversation.
	turns   []transcript.Turn
	queued  []agents.QueueItem
	creates []CreateReq
	stops   []string
	// create, when set, answers the target's create instead of the default success.
	create func(req CreateReq) (int, []byte)
	// q, when set, is the source's driver queue the snapshot reads, standing in for the
	// handle's own.
	q   *agents.TurnQueue
	git HandoverGit
	// orphan makes a target without a meta read as running (a pane or runtime left behind);
	// orphanStops records what cancel stopped.
	orphan      bool
	orphanStops []string
}

func moveRunEnv(t *testing.T, name string) *moveRun {
	t.Helper()
	d, m := moveEnv(t, name)
	r := &moveRun{d: d, src: m, turns: []transcript.Turn{
		{Role: "user", Text: "fix the flaky login test", Parts: []transcript.Part{{Kind: "text", Text: "fix the flaky login test"}}},
		{Role: "assistant", Parts: []transcript.Part{{Kind: "text", Text: "Found the race in the fixture."}}},
	}}
	prevRead, prevCreate, prevStop, prevWrite := readMoveSource, createMoveTarget, stopMoveSource, writeMoveHandover
	prevPresence, prevOrphanStop, prevMetaWrite := moveTargetPresence, stopOrphanTarget, writeMoveTargetMeta
	readMoveSource = func(m session.Meta) HandoverInput {
		queued := r.queued
		if r.q != nil {
			queued = r.q.Items()
		}
		return HandoverInput{Source: m, Turns: r.turns, QueuedItems: queued, Git: r.git, PeerMessages: agents.PendingPeers(m.Name)}
	}
	moveTargetPresence = func(rec moveRecord) targetPresence {
		p := prevPresence(rec)
		if p == targetAbsent && r.orphan {
			return targetOrphan
		}
		return p
	}
	stopOrphanTarget = func(rec moveRecord) error {
		r.orphanStops = append(r.orphanStops, rec.Target)
		r.orphan = false
		return nil
	}
	createMoveTarget = func(_ context.Context, req CreateReq) (int, []byte) {
		r.creates = append(r.creates, req)
		if r.create != nil {
			return r.create(req)
		}
		return createFakeTarget(req)
	}
	stopMoveSource = func(m session.Meta) error {
		r.stops = append(r.stops, m.Name)
		return nil
	}
	t.Cleanup(func() {
		readMoveSource, createMoveTarget, stopMoveSource, writeMoveHandover = prevRead, prevCreate, prevStop, prevWrite
		moveTargetPresence, stopOrphanTarget, writeMoveTargetMeta = prevPresence, prevOrphanStop, prevMetaWrite
	})
	return r
}

// createFakeTarget is a create that succeeds: the target's meta is written, as the real create
// does before it answers.
func createFakeTarget(req CreateReq) (int, []byte) {
	m := session.Meta{Name: req.name, Dir: req.Dir, Kind: req.Kind, Driver: req.Driver, Title: req.Title}
	session.WriteMeta(m)
	b, _ := json.Marshal(map[string]string{"name": req.name, "kind": req.Kind})
	return http.StatusCreated, b
}

func postMove(t *testing.T, name, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/move"+path, strings.NewReader(body))
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	switch path {
	case "":
		HandleSessionMove(rec, req)
	case "/retry":
		HandleSessionMoveRetry(rec, req)
	case "/cancel":
		HandleSessionMoveCancel(rec, req)
	case "/reopen":
		HandleSessionMoveReopen(rec, req)
	case "/deliver":
		HandleSessionMoveDeliver(rec, req)
	default:
		t.Fatalf("no handler for %s", path)
	}
	return rec
}

type moveAnswer struct {
	Source        string `json:"source"`
	File          string `json:"file"`
	SourceStopped bool   `json:"sourceStopped"`
	Earlier       []string
	Target        struct {
		Name string `json:"name"`
	} `json:"target"`
	Error moveErrBody `json:"error"`
}

func decodeMove(t *testing.T, rec *httptest.ResponseRecorder) moveAnswer {
	t.Helper()
	var a moveAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatalf("answer %d %q: %v", rec.Code, rec.Body.String(), err)
	}
	return a
}

func mustMove(t *testing.T, name, kind string) moveAnswer {
	t.Helper()
	rec := postMove(t, name, "", `{"kind":"`+kind+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("move %s → %s: %d %s", name, kind, rec.Code, rec.Body.String())
	}
	return decodeMove(t, rec)
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMoveWritesTheFileCreatesTheTargetAndStopsTheSource(t *testing.T) {
	r := moveRunEnv(t, "mvok")
	if err := os.WriteFile(filepath.Join(r.src.Dir, "AGENTS.md"), []byte("rules"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := mustMove(t, "mvok", session.KindClaude)
	target := a.Target.Name
	if len(r.creates) != 1 || r.creates[0].name != target || r.creates[0].Kind != session.KindClaude || r.creates[0].Dir != r.src.Dir {
		t.Fatalf("creates = %+v, answer target %q", r.creates, target)
	}
	want := filepath.Join(paths.SessionWorkDir(target), "handover-mvok.md")
	if a.File != want {
		t.Fatalf("file = %q, want %q (the target's own directory)", a.File, want)
	}
	md := readFile(t, a.File)
	for _, s := range []string{"# Where it stopped", "fix the flaky login test", "Found the race in the fixture.", "AGENTS.md"} {
		if !strings.Contains(md, s) {
			t.Errorf("the file lacks %q:\n%s", s, md)
		}
	}
	prompt := r.creates[0].InitialPrompt
	for _, s := range []string{a.File, "read the head", "Never open the source session's own transcript"} {
		if !strings.Contains(prompt, s) {
			t.Errorf("the first prompt lacks %q:\n%s", s, prompt)
		}
	}
	cur, _ := session.ReadMeta("mvok")
	if cur.MovedTo != target || cur.Moving != "" {
		t.Fatalf("source meta = moving %q movedTo %q, want movedTo %s", cur.Moving, cur.MovedTo, target)
	}
	if len(r.stops) != 1 || r.stops[0] != "mvok" || !a.SourceStopped {
		t.Errorf("stops = %v (sourceStopped %v), want the source stopped once", r.stops, a.SourceStopped)
	}
	if _, ok := moveRecords.Read("mvok"); ok {
		t.Error("the move's record outlived its commit")
	}
	assertMovedRefusal(t, postInput(t, "mvok", `{"prompt":"one more thing"}`), target)
	assertNothingSent(t, r.d)
}

// Every failure before a target can exist leaves the source as it was: input open, nothing
// stopped, nothing sent, its spool and its queue untouched, and no file in a directory of a
// session that does not exist.
func TestMoveFailuresBeforeTheTargetRollBack(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(r *moveRun)
		status   int
		wantCode string
	}{
		{"empty transcript", func(r *moveRun) { r.turns = nil }, http.StatusConflict, "move_empty_transcript"},
		{"write error", func(r *moveRun) {
			writeMoveHandover = func(string, string, string) (string, error) { return "", errors.New("disk full") }
		}, http.StatusInternalServerError, "move_write_failed"},
		{"refused create", func(r *moveRun) {
			r.create = func(CreateReq) (int, []byte) {
				return http.StatusBadRequest, []byte(`{"error":{"code":"bad_model","message":"no such model"}}`)
			}
		}, http.StatusBadRequest, "bad_model"},
		{"create failed with nothing started", func(r *moveRun) {
			r.create = func(CreateReq) (int, []byte) {
				return http.StatusInternalServerError, []byte(`{"error":{"code":"runtime_failed","message":"boom"}}`)
			}
		}, http.StatusInternalServerError, "runtime_failed"},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			name := "mvrb" + string(rune('a'+i))
			r := moveRunEnv(t, name)
			// An ordinary user input still in the driver's queue, and a peer message waiting in
			// the spool: neither may be lost by a move that did not happen.
			r.q = agents.NewTurnQueue(name, nil, agents.LedgerAtAccept)
			qid, _ := r.q.Accept(agents.TurnInput{Prompt: "also update the changelog", Origin: agents.Origin{Kind: "user"}})
			if err := agents.PutPendingPeer(name, agents.PendingPeer{ID: "pp1", From: "peer", Intent: "question",
				Message: "is the fixture shared?", QueuedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			c.setup(r)
			rec := postMove(t, name, "", `{"kind":"claude"}`)
			if a := decodeMove(t, rec); rec.Code != c.status || a.Error.Code != c.wantCode {
				t.Fatalf("got %d %s, want %d %s", rec.Code, rec.Body.String(), c.status, c.wantCode)
			}
			cur, _ := session.ReadMeta(name)
			if cur.Moving != "" || cur.MovedTo != "" {
				t.Fatalf("source meta = moving %q movedTo %q after a rollback", cur.Moving, cur.MovedTo)
			}
			if len(r.stops) != 0 {
				t.Errorf("the source was stopped (%v): a stop drops a cursor/kiro queue", r.stops)
			}
			if p := agents.PendingPeers(name); len(p) != 1 || p[0].ID != "pp1" {
				t.Errorf("pending peers = %+v, want pp1 still spooled", p)
			}
			if _, ok := moveRecords.Read(name); ok {
				t.Error("a rolled-back move left its record")
			}
			for _, cr := range r.creates {
				if _, err := os.Stat(paths.SessionWorkDir(cr.name)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("the target's directory survived the rollback: %v", err)
				}
			}
			assertNothingSent(t, r.d)
			// The queued input is still the same entry, and the pump can take it once input is
			// open again: a pump takes its lease before the entry leaves the queue.
			if items := r.q.Items(); len(items) != 1 || items[0].ID != qid || items[0].Text != "also update the changelog" {
				t.Fatalf("queue after the rollback = %+v, want %s still queued", items, qid)
			}
			testLease(t, name)
			if tk := r.q.Take(); tk == nil || tk.ID() != qid || tk.In.Prompt != "also update the changelog" {
				t.Fatalf("the pump took %+v, want %s", tk, qid)
			}
			if rec := postInput(t, name, `{"prompt":"next instruction"}`); rec.Code != http.StatusOK {
				t.Fatalf("input after the rollback: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// The usage-limit booking fires while the move holds input closed, the move then rolls back:
// the spent booking is booked again rather than left as a dead id in the episode.
func TestBookingThatFiredDuringARolledBackMoveIsBookedAgain(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	r := moveRunEnv(t, "mvbook")
	setRateLimitPref(t, true)
	if err := RateLimitStates.Write("mvbook", rateLimitState{At: now.Add(-time.Hour).Format(time.RFC3339),
		ResumeAt: now.Add(-time.Minute).Format(time.RFC3339), ScheduleID: "sch_spent", ScheduleTries: 1}); err != nil {
		t.Fatal(err)
	}
	r.create = func(req CreateReq) (int, []byte) {
		assertMovedRefusal(t, postInput(t, "mvbook",
			`{"prompt":"`+rateLimitResumePrompt()+`","source":"schedule","schedule_id":"sch_spent"}`), req.name)
		return http.StatusBadRequest, []byte(`{"error":{"code":"bad_model","message":"no"}}`)
	}
	if rec := postMove(t, "mvbook", "", `{"kind":"claude"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("move: %d %s", rec.Code, rec.Body.String())
	}
	st := stateOf(t, "mvbook")
	if f.scheduled != 1 || st.ScheduleID != "sch_test" || st.FiredDuringMove != "" {
		t.Fatalf("after the rollback: booked %d, episode %+v; want the resume booked again", f.scheduled, st)
	}
}

// A peer message waiting in the source's spool is carried into the file exactly once and taken
// out of the spool, so it is neither lost nor delivered to the stopped source later.
func TestSpooledPeerMessagesEnterTheFileOnce(t *testing.T) {
	moveRunEnv(t, "mvpeer")
	if err := agents.PutPendingPeer("mvpeer", agents.PendingPeer{ID: "pp1", From: "reviewer", Intent: "question",
		Message: "did you rerun the suite with -race?", QueuedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	a := mustMove(t, "mvpeer", session.KindClaude)
	if n := strings.Count(readFile(t, a.File), "did you rerun the suite with -race?"); n != 1 {
		t.Fatalf("the peer message appears %d times in the file, want once", n)
	}
	if p := agents.PendingPeers("mvpeer"); len(p) != 0 {
		t.Fatalf("pending peers after the move = %+v, want the spool emptied", p)
	}
}

// A stopped Managed cursor source has no conversation left anywhere; the move refuses it with a
// reason instead of writing an empty file.
func TestMoveRefusesAStoppedManagedCursorSource(t *testing.T) {
	moveRunEnv(t, "mvcur-unused")
	session.WriteMeta(session.Meta{Name: "mvcur", Dir: t.TempDir(), Kind: session.KindCursor, Driver: session.DriverManaged})
	t.Cleanup(func() { lifegate.Reopen("mvcur") })
	rec := postMove(t, "mvcur", "", `{"kind":"claude"}`)
	if a := decodeMove(t, rec); rec.Code != http.StatusConflict || a.Error.Code != "move_cursor_stopped" {
		t.Fatalf("got %d %s, want 409 move_cursor_stopped", rec.Code, rec.Body.String())
	}
	if cur, _ := session.ReadMeta("mvcur"); cur.Moving != "" {
		t.Fatalf("moving = %q after the refusal", cur.Moving)
	}
}

// Kiro read the handover file with no approval prompt, so it is a target; cursor stays refused
// until it has been measured through the Managed driver.
func TestMoveAcceptsKiroAndRefusesCursor(t *testing.T) {
	r := moveRunEnv(t, "mvkiro")
	a := mustMove(t, "mvkiro", session.KindKiro)
	if len(r.creates) != 1 || r.creates[0].Kind != session.KindKiro || r.creates[0].name != a.Target.Name {
		t.Fatalf("creates = %+v, answer target %q", r.creates, a.Target.Name)
	}
	r2 := moveRunEnv(t, "mvcurtgt")
	rec := postMove(t, "mvcurtgt", "", `{"kind":"cursor"}`)
	if e := decodeMove(t, rec); rec.Code != http.StatusBadRequest || e.Error.Code != "move_target_unsupported" || len(r2.creates) != 0 {
		t.Fatalf("cursor: got %d %s, creates %+v", rec.Code, rec.Body.String(), r2.creates)
	}
}

// Only another kind that can read the file is a target (decisions 5 and 9).
func TestMoveRefusesTargetsThatCannotTakeIt(t *testing.T) {
	r := moveRunEnv(t, "mvkind")
	cases := []struct{ kind, code string }{
		{session.KindCodex, "move_same_kind"},
		{session.KindLcpp, "move_target_unsupported"},
		{session.KindCursor, "move_target_unsupported"},
		{session.KindShell, "move_target_unsupported"},
		{"nosuchkind", "bad_kind"},
	}
	for _, c := range cases {
		rec := postMove(t, "mvkind", "", `{"kind":"`+c.kind+`"}`)
		a := decodeMove(t, rec)
		if rec.Code != http.StatusBadRequest || a.Error.Code != c.code {
			t.Errorf("%s: got %d %s, want 400 %s", c.kind, rec.Code, rec.Body.String(), c.code)
		}
		if c.kind == session.KindCodex && a.Error.CanFork != moveCanFork(session.KindCodex) {
			t.Errorf("same kind: canFork = %v, want %v", a.Error.CanFork, moveCanFork(session.KindCodex))
		}
	}
	if len(r.creates) != 0 {
		t.Fatalf("a refused target was created: %+v", r.creates)
	}
	if cur, _ := session.ReadMeta("mvkind"); cur.Moving != "" {
		t.Fatalf("moving = %q after refusals", cur.Moving)
	}
}

// A → B → C: C owns copies of every earlier record, so deleting A and B leaves its files whole,
// and B's file names A's as earlier history.
func TestMoveChainCopiesEarlierFilesForward(t *testing.T) {
	r := moveRunEnv(t, "mva")
	ab := mustMove(t, "mva", session.KindClaude)
	b := ab.Target.Name
	t.Cleanup(func() { lifegate.Reopen(b) })
	bc := mustMove(t, b, session.KindCodex)
	c := bc.Target.Name
	if len(r.stops) != 2 {
		t.Fatalf("stops = %v, want A and B", r.stops)
	}
	if len(bc.Earlier) != 1 || bc.Earlier[0] != "handover-mva.md" {
		t.Fatalf("earlier = %v, want handover-mva.md", bc.Earlier)
	}
	if !strings.Contains(readFile(t, bc.File), "handover-mva.md") {
		t.Error("B's file does not name A's as earlier history")
	}
	for _, n := range []string{"mva", b} {
		if err := os.RemoveAll(paths.SessionWorkDir(n)); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"handover-mva.md", HandoverFileName(b)} {
		if md := readFile(t, filepath.Join(paths.SessionWorkDir(c), f)); !strings.Contains(md, "# Where it stopped") {
			t.Errorf("%s in C's directory is not a handover file:\n%s", f, md)
		}
	}
}

// A create that may have started a target is not guessed at: the source stays closed with its
// file until the user retries (same file, same name) or cancels.
func TestUncertainCreateWaitsForRetryOrCancel(t *testing.T) {
	t.Run("retry creates it again", func(t *testing.T) {
		r := moveRunEnv(t, "mvunc1")
		r.create = func(CreateReq) (int, []byte) { return 0, nil } // the create's outcome is unknown
		a := decodeMove(t, postMove(t, "mvunc1", "", `{"kind":"claude"}`))
		if a.Error.Code != "move_create_uncertain" || a.Error.Target == "" || a.Error.File == "" {
			t.Fatalf("answer = %+v, want move_create_uncertain naming the target and file", a.Error)
		}
		target, file := a.Error.Target, a.Error.File
		assertMovedRefusal(t, postInput(t, "mvunc1", `{"prompt":"hello?"}`), target)
		p := buildMovePreview(mustMeta(t, "mvunc1"), time.Now())
		if p.Pending == nil || p.Pending.Target != target || p.Pending.State != moveUncertain || p.Pending.TargetExists {
			t.Fatalf("preview pending = %+v", p.Pending)
		}
		r.create = nil
		rec := postMove(t, "mvunc1", "/retry", "")
		if rec.Code != http.StatusCreated {
			t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
		}
		if n := len(r.creates); n != 2 || r.creates[1].name != target || !strings.Contains(r.creates[1].InitialPrompt, file) {
			t.Fatalf("creates = %+v, want the second under the same name and file", r.creates)
		}
		if cur := mustMeta(t, "mvunc1"); cur.MovedTo != target {
			t.Fatalf("movedTo = %q after retry", cur.MovedTo)
		}
	})
	t.Run("cancel reopens the source", func(t *testing.T) {
		r := moveRunEnv(t, "mvunc2")
		r.create = func(CreateReq) (int, []byte) { return 0, nil }
		a := decodeMove(t, postMove(t, "mvunc2", "", `{"kind":"claude"}`))
		if rec := postMove(t, "mvunc2", "/cancel", ""); rec.Code != http.StatusOK {
			t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
		}
		if _, err := os.Stat(a.Error.File); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the file survived the cancel: %v", err)
		}
		if cur := mustMeta(t, "mvunc2"); cur.Moving != "" || cur.MovedTo != "" {
			t.Fatalf("meta after cancel = moving %q movedTo %q", cur.Moving, cur.MovedTo)
		}
		if rec := postInput(t, "mvunc2", `{"prompt":"back here"}`); rec.Code != http.StatusOK {
			t.Fatalf("input after cancel: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("a target that exists is committed, never cancelled", func(t *testing.T) {
		r := moveRunEnv(t, "mvunc3")
		r.create = func(req CreateReq) (int, []byte) {
			createFakeTarget(req) // written, then the answer was lost
			return http.StatusGatewayTimeout, nil
		}
		a := decodeMove(t, postMove(t, "mvunc3", "", `{"kind":"claude"}`))
		if a.Error.Code != "move_create_uncertain" {
			t.Fatalf("answer = %+v", a.Error)
		}
		if rec := postMove(t, "mvunc3", "/cancel", ""); rec.Code != http.StatusConflict || decodeMove(t, rec).Error.Code != "move_target_exists" {
			t.Fatalf("cancel with a live target: %d %s", rec.Code, rec.Body.String())
		}
		if rec := postMove(t, "mvunc3", "/retry", ""); rec.Code != http.StatusOK {
			t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
		}
		if len(r.creates) != 1 {
			t.Errorf("retry created the existing target again: %d creates", len(r.creates))
		}
		if cur := mustMeta(t, "mvunc3"); cur.MovedTo != a.Error.Target {
			t.Fatalf("movedTo = %q", cur.MovedTo)
		}
	})
}

func mustMeta(t *testing.T, name string) session.Meta {
	t.Helper()
	m, ok := session.ReadMeta(name)
	if !ok {
		t.Fatalf("no meta for %s", name)
	}
	return m
}

// "Resume here anyway" is the user's way back into a moved source.
func TestMoveReopenOpensAMovedSource(t *testing.T) {
	moveRunEnv(t, "mvre")
	if rec := postMove(t, "mvre", "/reopen", ""); rec.Code != http.StatusConflict {
		t.Fatalf("reopen of a session never moved: %d %s", rec.Code, rec.Body.String())
	}
	mustMove(t, "mvre", session.KindClaude)
	if rec := postMove(t, "mvre", "/reopen", ""); rec.Code != http.StatusOK {
		t.Fatalf("reopen: %d %s", rec.Code, rec.Body.String())
	}
	if rec := postInput(t, "mvre", `{"prompt":"continue here"}`); rec.Code != http.StatusOK {
		t.Fatalf("input after reopen: %d %s", rec.Code, rec.Body.String())
	}
}

// The real create, in-process: the target gets the name the move booked (the file is already in
// its directory) and its first prompt reaches its runtime.
func TestMoveCreatesTheTargetUnderTheBookedName(t *testing.T) {
	r := moveRunEnv(t, "mvreal-unused")
	createMoveTarget = func(ctx context.Context, req CreateReq) (int, []byte) {
		r.creates = append(r.creates, req)
		r, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/sessions", nil)
		c := &captureWriter{header: http.Header{}}
		createSession(c, r, req)
		return c.status, c.body.Bytes()
	}
	// A Terminal claude source moving to the managed codex fake.
	session.WriteMeta(session.Meta{Name: "mvreal", Dir: t.TempDir(), Kind: session.KindClaude})
	t.Cleanup(func() { lifegate.Reopen("mvreal") })
	rec := postMove(t, "mvreal", "", `{"kind":"codex","driver":"managed"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("move: %d %s", rec.Code, rec.Body.String())
	}
	a := decodeMove(t, rec)
	if a.Target.Name != r.creates[0].name {
		t.Fatalf("created %q, booked %q", a.Target.Name, r.creates[0].name)
	}
	tm := mustMeta(t, a.Target.Name)
	if tm.Kind != session.KindCodex || tm.Dir != mustMeta(t, "mvreal").Dir {
		t.Fatalf("target meta = %+v", tm)
	}
	select {
	case in := <-r.d.h.sent:
		if !strings.Contains(in.Prompt, a.File) {
			t.Fatalf("first prompt %q does not name %s", in.Prompt, a.File)
		}
	case <-time.After(moveGuard):
		t.Fatal("the first prompt never reached the target")
	}
}

func TestMovePreviewTellsTheDialogWhatMoves(t *testing.T) {
	r := moveRunEnv(t, "mvpre")
	r.queued = []agents.QueueItem{{ID: "q1", Text: "then bump the version", Origin: agents.Origin{Kind: "user"}}}
	if err := agents.PutPendingPeer("mvpre", agents.PendingPeer{ID: "pp1", From: "peer", Message: "ping", QueuedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	p := buildMovePreview(mustMeta(t, "mvpre"), time.Now())
	if p.Refusal != nil {
		t.Fatalf("refusal = %+v", p.Refusal)
	}
	reasons := map[string]string{}
	for _, tg := range p.Targets {
		reasons[tg.Kind] = tg.Reason
	}
	want := map[string]string{session.KindClaude: "", session.KindCodex: moveTargetSameKind,
		session.KindLcpp: moveTargetCannotRead, session.KindCursor: moveTargetNotMeasured, session.KindKiro: ""}
	for k, v := range want {
		if got, ok := reasons[k]; !ok || got != v {
			t.Errorf("target %s: reason %q (listed %v), want %q", k, got, ok, v)
		}
	}
	if len(p.Queued) != 1 || p.Queued[0].Text != "then bump the version" {
		t.Errorf("queued = %+v", p.Queued)
	}
	if len(p.Peers) != 1 || p.Peers[0].Message != "ping" {
		t.Errorf("peers = %+v", p.Peers)
	}
	if p.SizeBytes == 0 || p.Git == nil {
		t.Errorf("size %d, git %+v", p.SizeBytes, p.Git)
	}
	r.turns = nil
	if p := buildMovePreview(mustMeta(t, "mvpre"), time.Now()); p.Refusal == nil || p.Refusal.Code != "move_empty_transcript" {
		t.Errorf("empty transcript: refusal = %+v", p.Refusal)
	}
	// A managed cursor source is warned about: its mirror is empty once stopped (decision 11.7).
	cur := session.Meta{Name: "mvpre-cur", Dir: t.TempDir(), Kind: session.KindCursor, Driver: session.DriverManaged}
	if p := buildMovePreview(cur, time.Now()); !contains(p.Warnings, moveWarnCursorMirror) || p.Refusal == nil {
		t.Errorf("stopped managed cursor: warnings %v refusal %+v", p.Warnings, p.Refusal)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestInstructionGapNamesWhatTheTargetDoesNotLoad(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"CLAUDE.md", "AGENTS.md"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		from, to string
		want     string
	}{
		{session.KindClaude, session.KindCodex, "CLAUDE.md"},
		{session.KindCodex, session.KindClaude, "AGENTS.md"},
		{session.KindCodex, session.KindOpencode, ""},
		{session.KindCopilot, session.KindCodex, ""}, // .github/copilot-instructions.md is absent
	}
	for _, c := range cases {
		if got := strings.Join(instructionGap(dir, c.from, c.to), ","); got != c.want {
			t.Errorf("%s → %s: %q, want %q", c.from, c.to, got, c.want)
		}
	}
}

func TestRemoveHandoverFilesTouchesNothingElse(t *testing.T) {
	withTempHome(t)
	dir := paths.SessionWorkDir("rmtarget")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"handover-a.md", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := RemoveHandoverFiles("rmtarget", []string{"handover-a.md", "notes.txt", "handover-gone.md"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Fatalf("a file that is not a handover was touched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "handover-a.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("handover-a.md survived: %v", err)
	}
	if err := RemoveHandoverFiles("rmtarget", nil); err != nil || os.Remove(filepath.Join(dir, "notes.txt")) != nil {
		t.Fatal("cleanup")
	}
	if err := RemoveHandoverFiles("rmtarget", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the emptied directory stayed: %v", err)
	}
}

// git status --porcelain is read untrimmed: its first line starts with a status column that can
// be a space (" M": modified, not staged).
func TestReadMoveGitKeepsThePorcelainColumns(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-qm", "one")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g := readMoveGit(dir)
	if !g.Available || g.Status != " M a.txt" {
		t.Fatalf("status = %q (available %v), want \" M a.txt\"", g.Status, g.Available)
	}
	if !strings.Contains(g.DiffStat, "a.txt") || g.Ahead != "(the branch has no upstream)" {
		t.Fatalf("diff stat %q, ahead %q", g.DiffStat, g.Ahead)
	}
	if g := readMoveGit(t.TempDir()); g.Available {
		t.Fatal("a folder that is not a working copy reads as one")
	}
}

// A create that answers 2xx, or a runtime that started, without the target's meta on disk does
// not commit: the source would be stopped and moved to a session nothing lists. The move stays
// pending, and the target was never sent its first prompt.
func TestMoveWithoutTheTargetMetaOnDiskStaysPending(t *testing.T) {
	t.Run("the checked write fails", func(t *testing.T) {
		r := moveRunEnv(t, "mvmeta-unused")
		createMoveTarget = func(ctx context.Context, req CreateReq) (int, []byte) {
			r.creates = append(r.creates, req)
			hr, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/sessions", nil)
			c := &captureWriter{header: http.Header{}}
			createSession(c, hr, req)
			return c.status, c.body.Bytes()
		}
		writeMoveTargetMeta = func(session.Meta) error { return errors.New("no space left on device") }
		session.WriteMeta(session.Meta{Name: "mvmeta", Dir: t.TempDir(), Kind: session.KindClaude})
		t.Cleanup(func() { lifegate.Reopen("mvmeta") })
		a := decodeMove(t, postMove(t, "mvmeta", "", `{"kind":"codex","driver":"managed"}`))
		if a.Error.Code != "move_create_uncertain" || a.Error.File == "" {
			t.Fatalf("answer = %+v, want move_create_uncertain", a.Error)
		}
		if cur := mustMeta(t, "mvmeta"); cur.Moving != a.Error.Target || cur.MovedTo != "" {
			t.Fatalf("source meta = moving %q movedTo %q, want still moving", cur.Moving, cur.MovedTo)
		}
		if _, err := os.Stat(a.Error.File); err != nil {
			t.Fatalf("the file went with the pending move: %v", err)
		}
		if len(r.stops) != 0 {
			t.Fatalf("the source was stopped: %v", r.stops)
		}
		select {
		case in := <-r.d.h.sent:
			t.Fatalf("a target without a meta was sent %q", in.Prompt)
		default:
		}
	})
	t.Run("a 2xx with no meta", func(t *testing.T) {
		r := moveRunEnv(t, "mvmeta2")
		r.create = func(CreateReq) (int, []byte) { return http.StatusCreated, []byte(`{}`) }
		a := decodeMove(t, postMove(t, "mvmeta2", "", `{"kind":"claude"}`))
		if a.Error.Code != "move_create_uncertain" {
			t.Fatalf("answer = %+v", a.Error)
		}
		if cur := mustMeta(t, "mvmeta2"); cur.MovedTo != "" || len(r.stops) != 0 {
			t.Fatalf("committed without a target meta: %+v, stops %v", cur, r.stops)
		}
	})
}

// commitOnly drives a move up to its commit, as one that an Agent stop cut short after movedTo
// was written: the record is there, the peer spool and the source are not yet dealt with.
func commitOnly(t *testing.T, r *moveRun, name string) moveRecord {
	t.Helper()
	target := bookMoveTarget(r.src.Dir)
	mv := mustBegin(t, beginAsync(name, target))
	session.WriteMeta(session.Meta{Name: target, Dir: r.src.Dir, Kind: session.KindClaude})
	if err := agents.PutPendingPeer(name, agents.PendingPeer{ID: "pp1", From: "peer", Message: "carried", QueuedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	rec := moveRecord{Target: target, Kind: session.KindClaude, State: moveRunning, PeerIDs: []string{"pp1"}}
	if err := moveRecords.Write(name, rec); err != nil {
		t.Fatal(err)
	}
	if err := mv.commit(); err != nil {
		t.Fatal(err)
	}
	return rec
}

func assertCompleted(t *testing.T, r *moveRun, name, target string) {
	t.Helper()
	if len(r.stops) != 1 || r.stops[0] != name {
		t.Errorf("stops = %v, want %s stopped once", r.stops, name)
	}
	if p := agents.PendingPeers(name); len(p) != 0 {
		t.Errorf("pending peers = %+v, want the carried one taken", p)
	}
	if _, ok := moveRecords.Read(name); ok {
		t.Error("the record stayed")
	}
	slugMu.Lock()
	booked := slugBooked[target]
	slugMu.Unlock()
	if booked {
		t.Error("the target's name is still booked")
	}
}

// A committed move whose remaining steps an Agent stop cut short is finished: by the boot
// recovery, by retry, and by a reopen, which finishes it before opening input so the target's
// peer messages never reach the source again.
func TestCommittedMoveIsFinishedAfterARestart(t *testing.T) {
	t.Run("boot", func(t *testing.T) {
		r := moveRunEnv(t, "mvrec1")
		rec := commitOnly(t, r, "mvrec1")
		lifegate.ResetForTest()
		if n := RecoverMoves(); n != 1 {
			t.Fatalf("RecoverMoves = %d", n)
		}
		assertCompleted(t, r, "mvrec1", rec.Target)
		assertMovedRefusal(t, postInput(t, "mvrec1", `{"prompt":"x"}`), rec.Target)
	})
	t.Run("retry", func(t *testing.T) {
		r := moveRunEnv(t, "mvrec2")
		rec := commitOnly(t, r, "mvrec2")
		if rec2 := postMove(t, "mvrec2", "/retry", ""); rec2.Code != http.StatusOK {
			t.Fatalf("retry: %d %s", rec2.Code, rec2.Body.String())
		}
		assertCompleted(t, r, "mvrec2", rec.Target)
	})
	t.Run("reopen", func(t *testing.T) {
		r := moveRunEnv(t, "mvrec3")
		rec := commitOnly(t, r, "mvrec3")
		if rec2 := postMove(t, "mvrec3", "/reopen", ""); rec2.Code != http.StatusOK {
			t.Fatalf("reopen: %d %s", rec2.Code, rec2.Body.String())
		}
		assertCompleted(t, r, "mvrec3", rec.Target)
		if cur := mustMeta(t, "mvrec3"); cur.MovedTo != "" {
			t.Fatalf("movedTo = %q after reopen", cur.MovedTo)
		}
	})
}

// Reopen waits its turn behind a move that is still running, instead of opening input between
// the commit and the source's stop.
func TestReopenIsRefusedWhileTheMoveRuns(t *testing.T) {
	moveRunEnv(t, "mvrebusy")
	mustMove(t, "mvrebusy", session.KindClaude)
	unlock, ok := moveOpLock("mvrebusy")
	if !ok {
		t.Fatal("op lock")
	}
	defer unlock()
	if rec := postMove(t, "mvrebusy", "/reopen", ""); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "move_busy") {
		t.Fatalf("reopen during a move: %d %s", rec.Code, rec.Body.String())
	}
	if cur := mustMeta(t, "mvrebusy"); cur.MovedTo == "" {
		t.Fatal("reopened while the move held its lock")
	}
}

// A target that runs without a meta (a pane or runtime a create left) is neither created again
// by retry nor left running by cancel.
func TestOrphanTargetIsStoppedByCancelNotDuplicatedByRetry(t *testing.T) {
	r := moveRunEnv(t, "mvorph")
	r.create = func(CreateReq) (int, []byte) {
		r.orphan = true
		return http.StatusInternalServerError, []byte(`{"error":{"code":"tmux_failed","message":"lost"}}`)
	}
	a := decodeMove(t, postMove(t, "mvorph", "", `{"kind":"claude"}`))
	if a.Error.Code != "move_create_uncertain" {
		t.Fatalf("answer = %+v", a.Error)
	}
	if p := buildMovePreview(mustMeta(t, "mvorph"), time.Now()); p.Pending == nil || !p.Pending.Orphan {
		t.Fatalf("preview pending = %+v, want orphan", p.Pending)
	}
	rec := postMove(t, "mvorph", "/retry", "")
	if rec.Code != http.StatusConflict || decodeMove(t, rec).Error.Code != "move_target_orphaned" || len(r.creates) != 1 {
		t.Fatalf("retry on an orphan: %d %s, %d creates", rec.Code, rec.Body.String(), len(r.creates))
	}
	if rec := postMove(t, "mvorph", "/cancel", ""); rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}
	if len(r.orphanStops) != 1 || r.orphanStops[0] != a.Error.Target {
		t.Fatalf("orphan stops = %v", r.orphanStops)
	}
	if cur := mustMeta(t, "mvorph"); cur.Moving != "" {
		t.Fatalf("moving = %q after cancel", cur.Moving)
	}
}

// fixedSlugs makes randSlug draw from *slugs. The handed-out memory outlives a test (an hour), so
// the second run in a process (-count=N) would find every fixed slug already taken and run the
// list dry: the names in forgotten are cleared before and after.
func fixedSlugs(t *testing.T, slugs *[]string, forgotten ...string) {
	t.Helper()
	forget := func() {
		slugMu.Lock()
		for _, s := range forgotten {
			delete(slugHandedAt, s)
			delete(slugBooked, s)
		}
		slugMu.Unlock()
	}
	forget()
	prev := randSlug
	randSlug = func() string { s := (*slugs)[0]; *slugs = (*slugs)[1:]; return s }
	t.Cleanup(func() { randSlug = prev; forget() })
}

// A booked target name, held in memory or by a pending move's record across a restart, is
// never handed to another session; it is free again once the move ends.
func TestBookedMoveTargetIsNeverHandedOut(t *testing.T) {
	pendingTestEnv(t)
	slugs := []string{"sbooked", "sbooked", "sother1"}
	fixedSlugs(t, &slugs, "sbooked", "sother1", "sother2", "sother3")
	if got := bookMoveTarget(t.TempDir()); got != "sbooked" {
		t.Fatalf("booked %q", got)
	}
	if got := allocSessionName(t.TempDir()); got != "sother1" {
		t.Fatalf("handed out %q while sbooked is booked", got)
	}
	// After a restart only the record holds it.
	releaseMoveTarget("sbooked")
	slugMu.Lock()
	delete(slugHandedAt, "sbooked")
	slugMu.Unlock()
	if err := moveRecords.Write("src1", moveRecord{Target: "sbooked"}); err != nil {
		t.Fatal(err)
	}
	slugs = []string{"sbooked", "sother2"}
	if got := allocSessionName(t.TempDir()); got != "sother2" {
		t.Fatalf("handed out %q, which a pending move's record holds", got)
	}
	moveRecords.Remove("src1")
	slugs = []string{"sbooked"}
	if got := allocSessionName(t.TempDir()); got != "sbooked" {
		t.Fatalf("got %q; a name no move holds should be free", got)
	}
	// A name handed to an ordinary create is not booked for a move while its meta may be on
	// its way.
	slugs = []string{"sbooked", "sother3"}
	if got := bookMoveTarget(t.TempDir()); got != "sother3" {
		t.Fatalf("booked %q, just handed to a create", got)
	}
}

// The released name of a rolled-back move is not held.
func TestRolledBackMoveReleasesItsName(t *testing.T) {
	r := moveRunEnv(t, "mvrel")
	r.turns = nil
	booked := func() int {
		slugMu.Lock()
		defer slugMu.Unlock()
		return len(slugBooked) // other tests' pending moves may hold names too
	}
	before := booked()
	postMove(t, "mvrel", "", `{"kind":"claude"}`)
	if n := booked(); n != before {
		t.Fatalf("%d names booked after a rollback, %d before", n, before)
	}
}

// Instructions typed into the claude turn the limit cut off are read from the jsonl's
// queue-operation records and reach the file once.
func TestClaudeQueuedInstructionsReachTheHandover(t *testing.T) {
	home := withTempHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	dir := t.TempDir()
	m := session.Meta{Name: "mvclq", Dir: dir, Kind: session.KindClaude}
	session.WriteMeta(m)
	jsonl := filepath.Join(home, ".claude", "projects", "p", session.UUID(dir, m.Name)+".jsonl")
	lines := []string{
		`{"type":"user","timestamp":"2026-10-17T10:00:00Z","message":{"role":"user","content":"refactor the parser"}}`,
		`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-10-17T10:00:03Z","content":"and keep the old API"}`,
		`{"type":"assistant","timestamp":"2026-10-17T10:00:05Z","message":{"role":"assistant","content":[{"type":"text","text":"Starting."}]}}`,
	}
	writeFile(t, jsonl, strings.Join(lines, "\n")+"\n")
	in := readMoveSource(m)
	if len(in.Queued) != 1 || in.Queued[0] != "and keep the old API" {
		t.Fatalf("queued = %q", in.Queued)
	}
	in.Now = time.Now()
	h, err := BuildHandover(in)
	if err != nil {
		t.Fatal(err)
	}
	md, _ := RenderHandover(h, "en")
	if n := strings.Count(md, "and keep the old API"); n != 1 {
		t.Fatalf("the queued instruction appears %d times:\n%s", n, md)
	}
}

// Decision 7's scrub covers the preview's answer, not only the file.
func TestMovePreviewAndFileAreScrubbed(t *testing.T) {
	r := moveRunEnv(t, "mvscrub")
	tok := "ghp_" + strings.Repeat("A1b2C3d4", 5)
	r.queued = []agents.QueueItem{{ID: "q1", Text: "use token " + tok}}
	r.git = HandoverGit{Available: true, Status: "?? " + tok + ".txt", Ahead: "abc123 add " + tok}
	if err := agents.PutPendingPeer("mvscrub", agents.PendingPeer{ID: "pp1", From: "peer", Message: "key " + tok, QueuedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(buildMovePreview(mustMeta(t, "mvscrub"), time.Now()))
	if strings.Contains(string(b), tok) {
		t.Fatalf("the preview carries the token: %s", b)
	}
	if !strings.Contains(string(b), "use token") {
		t.Fatalf("the preview lost the queued text: %s", b)
	}
	a := mustMove(t, "mvscrub", session.KindClaude)
	if md := readFile(t, a.File); strings.Contains(md, tok) {
		t.Fatal("the file carries the token")
	}
}

// The git reads are bounded by bytes: a working copy with a huge untracked list is cut, and
// says so.
func TestReadMoveGitIsBounded(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	for i := 0; i < 200; i++ {
		if err := os.WriteFile(filepath.Join(dir, "untracked-file-"+strings.Repeat("x", 20)+string(rune('a'+i%26))+strconv.Itoa(i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prev := moveGitMaxBytes
	moveGitMaxBytes = 512
	t.Cleanup(func() { moveGitMaxBytes = prev })
	g := readMoveGit(dir)
	if !strings.Contains(g.Status, "cut at") || len(g.Status) > 512+100 {
		t.Fatalf("status (%d bytes) = %q", len(g.Status), g.Status)
	}
	if !strings.HasPrefix(g.Status, "?? untracked-file-") {
		t.Fatalf("status lost its columns: %q", g.Status[:40])
	}
}

// A target meta that exists and does not read (here: truncated, as an ordinary rewrite leaves it
// for an instant) is a target that may be working: cancel must not take it for an orphan and
// stop it, and retry must not commit on it either.
func TestUnreadableTargetMetaIsNeverAnOrphan(t *testing.T) {
	r := moveRunEnv(t, "mvunr")
	r.create = func(req CreateReq) (int, []byte) {
		createFakeTarget(req)
		return http.StatusGatewayTimeout, nil
	}
	a := decodeMove(t, postMove(t, "mvunr", "", `{"kind":"claude"}`))
	if a.Error.Code != "move_create_uncertain" {
		t.Fatalf("answer = %+v", a.Error)
	}
	if err := os.WriteFile(session.MetaPath(a.Error.Target), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r.orphan = true // a pane runs under the name too
	if rec := postMove(t, "mvunr", "/cancel", ""); rec.Code != http.StatusConflict || decodeMove(t, rec).Error.Code != "move_target_exists" {
		t.Fatalf("cancel on an unreadable meta: %d %s", rec.Code, rec.Body.String())
	}
	if len(r.orphanStops) != 0 {
		t.Fatalf("stopped %v", r.orphanStops)
	}
	if _, err := os.Stat(a.Error.File); err != nil {
		t.Fatalf("the file went: %v", err)
	}
	if rec := postMove(t, "mvunr", "/retry", ""); rec.Code != http.StatusConflict || decodeMove(t, rec).Error.Code != "move_target_unreadable" {
		t.Fatalf("retry on an unreadable meta: %d %s", rec.Code, rec.Body.String())
	}
	if cur := mustMeta(t, "mvunr"); cur.Moving != a.Error.Target {
		t.Fatalf("moving = %q", cur.Moving)
	}
}

// An Agent that stops after begin wrote moving and before the record exists leaves the target's
// name only in the source's meta. It is still held, and cancel still opens the source.
func TestTargetNameHeldByMovingAloneAndCancelWithoutARecord(t *testing.T) {
	moveRunEnv(t, "mvnorec")
	target := bookMoveTarget(t.TempDir())
	mustBegin(t, beginAsync("mvnorec", target))
	// The restart: memory is gone.
	releaseMoveTarget(target)
	slugMu.Lock()
	delete(slugHandedAt, target)
	slugMu.Unlock()
	slugs := []string{target, "sfresh1"}
	fixedSlugs(t, &slugs, "sfresh1")
	if got := allocSessionName(t.TempDir()); got != "sfresh1" {
		t.Fatalf("handed out %q, which the source's meta names as its move target", got)
	}
	if rec := postMove(t, "mvnorec", "/retry", ""); rec.Code != http.StatusConflict || decodeMove(t, rec).Error.Code != "move_record_missing" {
		t.Fatalf("retry without a record: %d %s", rec.Code, rec.Body.String())
	}
	if rec := postMove(t, "mvnorec", "/cancel", ""); rec.Code != http.StatusOK {
		t.Fatalf("cancel without a record: %d %s", rec.Code, rec.Body.String())
	}
	if cur := mustMeta(t, "mvnorec"); cur.Moving != "" {
		t.Fatalf("moving = %q after cancel", cur.Moving)
	}
}

// A target whose first prompt was lost to an Agent stop (written pending, never sent) is not
// reported as a finished move: the answer says the prompt is not known to have arrived, and the
// deliver step, on the user's word, points the target at its file.
func TestLostFirstPromptIsReportedAndDeliverable(t *testing.T) {
	r := moveRunEnv(t, "mvdlv-unused")
	session.WriteMeta(session.Meta{Name: "mvdlv", Dir: t.TempDir(), Kind: session.KindClaude})
	t.Cleanup(func() { lifegate.Reopen("mvdlv") })
	r.create = func(req CreateReq) (int, []byte) {
		session.WriteMeta(session.Meta{Name: req.name, Dir: req.Dir, Kind: req.Kind, Driver: req.Driver,
			InitialPromptState: session.InitialPromptPending})
		return 0, nil // the Agent stopped here
	}
	a := decodeMove(t, postMove(t, "mvdlv", "", `{"kind":"codex","driver":"managed"}`))
	if a.Error.Code != "move_create_uncertain" {
		t.Fatalf("answer = %+v", a.Error)
	}
	target := a.Error.Target
	RecoverPendingInitialPrompts()
	rec := postMove(t, "mvdlv", "/retry", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
	}
	var res sessionMoveWire
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Delivery == nil || res.Delivery.InitialPromptState != session.InitialPromptUnknown || !res.Delivery.Deliverable {
		t.Fatalf("retry answered delivery %+v; want unknown and deliverable", res.Delivery)
	}
	if p := buildMovePreview(mustMeta(t, "mvdlv"), time.Now()); p.Delivery == nil || p.Delivery.Target != target {
		t.Fatalf("source preview delivery = %+v", p.Delivery)
	}
	assertNothingSent(t, r.d)
	rec = postMove(t, target, "/deliver", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"mayDuplicate":true`) {
		t.Fatalf("deliver: %d %s", rec.Code, rec.Body.String())
	}
	select {
	case in := <-r.d.h.sent:
		if !strings.Contains(in.Prompt, a.Error.File) {
			t.Fatalf("delivered %q, which does not name %s", in.Prompt, a.Error.File)
		}
	case <-time.After(moveGuard):
		t.Fatal("the first prompt never reached the target")
	}
	if st := mustMeta(t, target).InitialPromptState; st != session.InitialPromptDelivered {
		t.Fatalf("initial prompt state = %q", st)
	}
	if p := buildMovePreview(mustMeta(t, "mvdlv"), time.Now()); p.Delivery != nil {
		t.Fatalf("delivery still reported after it arrived: %+v", p.Delivery)
	}
	if rec := postMove(t, target, "/deliver", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("a second deliver: %d %s", rec.Code, rec.Body.String())
	}
}

// A Terminal target's redelivery is not a bare /input: typed keys are not a started turn. An
// outcome without evidence stays unknown and keeps the record, so it can be delivered again.
func TestTerminalRedeliveryNeedsEvidence(t *testing.T) {
	moveRunEnv(t, "mvtdl-unused")
	target := "mvtdl"
	session.WriteMeta(session.Meta{Name: target, Dir: t.TempDir(), Kind: session.KindCodex,
		InitialPromptState: session.InitialPromptUnknown})
	if err := moveDeliveries.Write(target, moveDelivery{Source: "gone", File: "/f", Prompt: "read /f"}); err != nil {
		t.Fatal(err)
	}
	outcome := make(chan string, 1)
	typed := make(chan string, 1)
	prev := typeMoveDelivery
	// quit lets a failed test release a stub still waiting for its outcome, so the cleanup's join
	// cannot wait on it forever.
	quit := make(chan struct{})
	typeMoveDelivery = func(name, prompt string) string {
		select {
		case typed <- prompt:
		case <-quit:
			return session.InitialPromptFailed
		}
		select {
		case o := <-outcome:
			return o
		case <-quit:
			return session.InitialPromptFailed
		}
	}
	t.Cleanup(func() { close(quit); moveDeliverInflight.Wait(); typeMoveDelivery = prev })
	settled := func() string {
		t.Helper()
		deadline := time.Now().Add(moveGuard)
		for {
			// Under the meta lock: settleInitialPrompt rewrites the file in place.
			var m session.Meta
			WithSessionMetaLock(func() { m, _ = session.ReadMeta(target) })
			if st := m.InitialPromptState; st != session.InitialPromptPending {
				return st
			}
			if time.Now().After(deadline) {
				t.Fatal("the delivery never settled")
			}
			time.Sleep(time.Millisecond)
		}
	}
	rec := postMove(t, target, "/deliver", "")
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"initialPromptState":"pending"`) {
		t.Fatalf("deliver: %d %s", rec.Code, rec.Body.String())
	}
	if p := <-typed; p != "read /f" {
		t.Fatalf("typed %q", p)
	}
	outcome <- session.InitialPromptUnknown // typed, no turn seen
	if st := settled(); st != session.InitialPromptUnknown {
		t.Fatalf("state = %q, want unknown", st)
	}
	if d := moveDeliveryState(target); d == nil || !d.Deliverable {
		t.Fatalf("delivery = %+v; the record must stay deliverable", d)
	}
	// With evidence it is delivered, and the record goes.
	postMove(t, target, "/deliver", "")
	<-typed
	outcome <- session.InitialPromptDelivered
	if st := settled(); st != session.InitialPromptDelivered {
		t.Fatalf("state = %q", st)
	}
	moveDeliverInflight.Wait() // the goroutine removes the record after it settles the state
	if _, ok := moveDeliveries.Read(target); ok {
		t.Fatal("the record outlived a confirmed delivery")
	}
}

// The delivery record is written before the target exists. A deliver, or a read, in that window
// must neither drop it nor deliver: the move from the source is still creating the target.
func TestDeliveryRecordSurvivesBeforeTheTargetExists(t *testing.T) {
	moveRunEnv(t, "mvpre-create")
	target := bookMoveTarget(t.TempDir())
	mustBegin(t, beginAsync("mvpre-create", target))
	if err := moveDeliveries.Write(target, moveDelivery{Source: "mvpre-create", File: "/f", Prompt: "read /f"}); err != nil {
		t.Fatal(err)
	}
	rec := postMove(t, target, "/deliver", "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "move_target_not_created") {
		t.Fatalf("deliver before the create: %d %s", rec.Code, rec.Body.String())
	}
	if d := moveDeliveryState(target); d != nil {
		t.Fatalf("delivery = %+v before the target exists", d)
	}
	if _, ok := moveDeliveries.Read(target); !ok {
		t.Fatal("the record was dropped while the move was still creating its target")
	}
	// Once the move is gone (cancelled), a record for a target that never existed is dropped.
	if rec := postMove(t, "mvpre-create", "/cancel", ""); rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}
	moveDeliveryState(target)
	if _, ok := moveDeliveries.Read(target); ok {
		t.Fatal("a record for a target that will never exist stayed")
	}
}

// The row reads a move's two states off the list (ADR 0115 decision 11.7, #2254): a moved
// source says where its conversation went, and a pending one offers retry and cancel. A field
// the meta holds and the wire drops is one the Console can never show.
func TestMoveStatesRideTheSessionWire(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	m := session.Meta{Name: "mvwire", Dir: t.TempDir(), Kind: session.KindClaude, Moving: "mvwire-t1", MovedTo: "mvwire-t0"}
	raw, _ := json.Marshal(wireSession(m, false))
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out["moving"] != "mvwire-t1" || out["movedTo"] != "mvwire-t0" {
		t.Fatalf("moving / movedTo are not on the wire: %s", raw)
	}
	raw, _ = json.Marshal(wireSession(session.Meta{Name: "mvwire2", Kind: session.KindClaude}, false))
	out = nil
	_ = json.Unmarshal(raw, &out)
	if _, ok := out["moving"]; ok {
		t.Fatalf("a session that never moved carries moving: %s", raw)
	}
	if _, ok := out["movedTo"]; ok {
		t.Fatalf("a session that never moved carries movedTo: %s", raw)
	}
}
