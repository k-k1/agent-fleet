package opencode

// opencode 2 session state through the API (v2state.go). The fixtures under testdata/v2/ are
// captured from a real 2.0.28 serve driven against a loopback stub provider; the fake daemon
// here only replays them.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

func v2Fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "v2", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// v2Daemon replays recorded answers for a v2 serve. Messages are paged like the real one (limit
// and an opaque cursor), so the reader's paging is exercised too.
type v2Daemon struct {
	f        *fakeServe
	mu       sync.Mutex
	sessions map[string]string   // id → GET /api/session/{id} body
	messages map[string][]string // id → one JSON object per message, oldest first
	inbox    map[string]string
	active   []string
	activeSt int // status of /api/session/active (0 = 200)
	pages    int // message requests served
}

func newV2Daemon(t *testing.T) *v2Daemon {
	t.Helper()
	f := newFakeServe(t, 2, "pw")
	if !healthy(f.srv.URL) {
		t.Fatal("fake v2 daemon not healthy")
	}
	// A daemon that just started has never been down, but its port may be one an earlier test's
	// closed server held and a read marked down: the verdict is keyed by address and ports are
	// reused. A stale verdict sends reads to the store although this daemon answers.
	v2Down.mu.Lock()
	clear(v2Down.until)
	v2Down.mu.Unlock()
	v2ActiveTTL = 0
	t.Cleanup(func() { v2ActiveTTL = 500 * time.Millisecond })
	d := &v2Daemon{f: f, sessions: map[string]string{}, messages: map[string][]string{}, inbox: map[string]string{}}
	f.srv.Config.Handler = http.HandlerFunc(d.serve)
	return d
}

func (d *v2Daemon) serve(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	js := func(s string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(s))
	}
	p := r.URL.Path
	switch {
	case p == "/api/session/active":
		if d.activeSt != 0 {
			http.Error(w, "down", d.activeSt)
			return
		}
		var parts []string
		for _, id := range d.active {
			parts = append(parts, `"`+id+`":{"type":"running"}`)
		}
		js(`{"data":{` + strings.Join(parts, ",") + `}}`)
	case p == "/api/session":
		var all []string
		for _, s := range d.sessions {
			all = append(all, strings.TrimSuffix(strings.TrimPrefix(s, `{"data":`), "}"))
		}
		js(`{"data":[` + strings.Join(all, ",") + `]}`)
	case strings.HasPrefix(p, "/api/session/") && strings.HasSuffix(p, "/message"):
		d.pages++
		id := strings.TrimSuffix(strings.TrimPrefix(p, "/api/session/"), "/message")
		msgs, ok := d.messages[id]
		if !ok {
			http.Error(w, `{"_tag":"SessionNotFoundError"}`, http.StatusNotFound)
			return
		}
		q := r.URL.Query()
		desc := q.Get("order") == "desc" || strings.HasPrefix(q.Get("cursor"), "desc:")
		if desc {
			rev := make([]string, len(msgs))
			for i, m := range msgs {
				rev[len(msgs)-1-i] = m
			}
			msgs = rev
		}
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit <= 0 || limit > 200 {
			limit = 200
		}
		off := 0
		if c := q.Get("cursor"); c != "" {
			off, _ = strconv.Atoi(strings.TrimPrefix(c, "desc:")) // the cursor remembers its order, like the real one
		}
		end := min(off+limit, len(msgs))
		if off > len(msgs) {
			off = len(msgs)
		}
		js(`{"data":[` + strings.Join(msgs[off:end], ",") + `],"cursor":{"previous":"p","next":"` + map[bool]string{true: "desc:", false: ""}[desc] + strconv.Itoa(end) + `"}}`)
	case strings.HasPrefix(p, "/api/session/") && strings.HasSuffix(p, "/inbox"):
		id := strings.TrimSuffix(strings.TrimPrefix(p, "/api/session/"), "/inbox")
		if b, ok := d.inbox[id]; ok {
			js(b)
		} else {
			js(`{"data":[]}`)
		}
	case strings.HasPrefix(p, "/api/session/") && strings.Count(p, "/") == 3:
		if b, ok := d.sessions[strings.TrimPrefix(p, "/api/session/")]; ok {
			js(b)
			return
		}
		http.Error(w, `{"_tag":"SessionNotFoundError"}`, http.StatusNotFound)
	default:
		d.mu.Unlock()
		d.f.handle(w, r)
		d.mu.Lock()
	}
}

// addSession registers a conversation whose messages come from a testdata file.
func (d *v2Daemon) addSession(t *testing.T, id, parent, created string, msgFile string) {
	t.Helper()
	d.sessions[id] = fmt.Sprintf(`{"data":{"id":%q,"parentID":%q,"projectID":"p","cost":0,"time":{"created":%s,"updated":%s},"location":{"directory":"/work"}}}`,
		id, parent, created, created)
	if msgFile != "" {
		var w struct {
			Data []json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(v2Fixture(t, msgFile), &w); err != nil {
			t.Fatal(err)
		}
		d.messages[id] = nil
		for _, m := range w.Data {
			d.messages[id] = append(d.messages[id], string(m))
		}
	}
}

func v2Meta(t *testing.T, ses string) session.Meta {
	t.Helper()
	m := session.Meta{Name: "v2slot", Dir: "/work", Kind: session.KindOpencode, Driver: session.DriverManaged}
	sids.Write(session.UUID(m.Dir, m.Name), ses)
	t.Cleanup(func() { sids.Remove(session.UUID(m.Dir, m.Name)) })
	return m
}

func v2Slot(t *testing.T, msgFile string) (*v2Daemon, session.Meta) {
	d := newV2Daemon(t)
	d.addSession(t, "ses_a", "", "1791931000000", msgFile)
	return d, v2Meta(t, "ses_a")
}

func errorParts(turns []transcript.Turn) (n int, last transcript.Part) {
	for _, tn := range turns {
		for _, p := range tn.Parts {
			if p.Kind == "error" {
				n++
				last = p
			}
		}
	}
	return
}

// A v2 conversation has no v1 rows: the whole transcript must come through the API, and the
// steps of one turn (a tool call, then the closing text) are one turn, as they were on v1.
func TestV2TranscriptComesFromTheAPI(t *testing.T) {
	d, m := v2Slot(t, "tool.messages.json")
	_ = d
	td, ok := readTranscript(m)
	if !ok || len(td.Turns) != 2 {
		t.Fatalf("turns = %d, want user + one merged assistant turn: %+v", len(td.Turns), td.Turns)
	}
	u, a := td.Turns[0], td.Turns[1]
	if u.Role != "user" || u.Text != "please TOOL" || u.AnchorID == "" {
		t.Errorf("user turn = %+v", u)
	}
	if a.Role != "assistant" || a.Model != "m" || a.Text != "working tool done" {
		t.Errorf("assistant turn = %+v", a)
	}
	var tool *transcript.Part
	for i := range a.Parts {
		if a.Parts[i].Kind == "tool" {
			tool = &a.Parts[i]
		}
	}
	if tool == nil || tool.Tool != "shell" || tool.Info != "echo hi" || tool.Output != "hi" {
		t.Errorf("tool part = %+v", tool)
	}
	// Two steps at 0.00014 each; tokens are the last step's, as v1 stored them.
	if a.CostUSD < 0.00027 || a.CostUSD > 0.00029 || a.InTok != 100 || a.OutTok != 20 {
		t.Errorf("cost/tokens = %v %d %d", a.CostUSD, a.InTok, a.OutTok)
	}
	if a.EndTS == "" || a.TS == "" {
		t.Errorf("timestamps missing: %+v", a)
	}
	if td.Mode != "normal" {
		t.Errorf("mode = %q", td.Mode)
	}
}

// v1's workaround read the error off the message row. v2 reports the failure explicitly, and a
// failed turn must show once even though the assistant message and the idle message both carry it.
func TestV2FailedTurnIsVisibleOnce(t *testing.T) {
	_, m := v2Slot(t, "fail-auth.messages.json")
	td, _ := readTranscript(m)
	n, e := errorParts(td.Turns)
	if n != 1 {
		t.Fatalf("error parts = %d, want 1: %+v", n, td.Turns)
	}
	if e.Info != "provider.auth (HTTP 401)" || !strings.Contains(e.Text, "invalid api key") {
		t.Errorf("error part = %+v", e)
	}
	last := td.Turns[len(td.Turns)-1]
	if !strings.HasPrefix(last.Text, "[error]") {
		t.Errorf("flattened text = %q", last.Text)
	}
}

func TestV2ServerErrorShowsStatusAfterRetries(t *testing.T) {
	_, m := v2Slot(t, "fail-server-error.messages.json")
	td, _ := readTranscript(m)
	if n, e := errorParts(td.Turns); n != 1 || e.Info != "provider.internal (HTTP 500)" {
		t.Fatalf("error parts = %d %+v", n, e)
	}
}

// An interrupt is not a failure: the partial answer shows and nothing is flagged.
func TestV2InterruptedTurnIsNotAnError(t *testing.T) {
	_, m := v2Slot(t, "interrupted.messages.json")
	td, _ := readTranscript(m)
	if n, _ := errorParts(td.Turns); n != 0 {
		t.Fatalf("an interrupt rendered %d error parts", n)
	}
	var partial bool
	for _, tn := range td.Turns {
		partial = partial || strings.HasPrefix(tn.Text, "working")
	}
	if !partial {
		t.Fatalf("the partial answer is missing: %+v", td.Turns)
	}
}

// A failure with no assistant message (the model was never reached) lives only on the idle
// message; it must not vanish from the mirror.
func TestV2IdleFailureWithoutAssistantMessageIsShown(t *testing.T) {
	d := newV2Daemon(t)
	d.addSession(t, "ses_a", "", "1000", "")
	d.messages["ses_a"] = []string{
		`{"id":"msg_1","type":"user","text":"hi","time":{"created":1000}}`,
		`{"id":"msg_2","type":"idle","outcome":"failed","time":{"created":1001},"error":{"type":"provider.auth","message":"no key","status":401}}`,
	}
	td, _ := readTranscript(v2Meta(t, "ses_a"))
	if n, e := errorParts(td.Turns); n != 1 || e.Text != "no key" {
		t.Fatalf("error parts = %d %+v", n, e)
	}
}

// Cost: the session total is the assistant steps plus the compactions, each counted once. The
// compaction's cost is on its own message, and the chat shows no compaction turn.
func TestV2UsageTurnsCountCompactionCostOnce(t *testing.T) {
	_, m := v2Slot(t, "compaction-ok.messages.json")
	var info struct {
		Data struct {
			Cost float64 `json:"cost"`
		} `json:"data"`
	}
	if err := json.Unmarshal(v2Fixture(t, "session-after-success.json"), &info); err != nil {
		t.Fatal(err)
	}
	var sum float64
	var costOnly int
	for _, tn := range usageTurns(m) {
		sum += tn.CostUSD
		if tn.CostOnly {
			costOnly++
			if tn.AnchorID == "" {
				t.Error("a cost-only turn has no stable identity")
			}
		}
	}
	if costOnly != 1 || sum < info.Data.Cost-1e-9 || sum > info.Data.Cost+1e-9 {
		t.Fatalf("usage sum = %v over %d cost-only turns, session cost = %v", sum, costOnly, info.Data.Cost)
	}
	td, _ := readTranscript(m)
	for _, tn := range td.Turns {
		if tn.CostOnly {
			t.Fatal("the chat shows a cost-only turn")
		}
	}
}

func TestV2FailedCompactionIsShownAndBilled(t *testing.T) {
	_, m := v2Slot(t, "compaction-failed.messages.json")
	td, _ := readTranscript(m)
	if n, e := errorParts(td.Turns); n != 1 || e.Info != "compaction.failed" {
		t.Fatalf("error parts = %d %+v", n, e)
	}
	var billed float64
	for _, tn := range usageTurns(m) {
		if tn.CostOnly {
			billed += tn.CostUSD
		}
	}
	if billed < 0.00027 || billed > 0.00029 {
		t.Fatalf("failed compaction billed %v, want its own 0.00028", billed)
	}
}

// A fork copies messages under new ids but keeps their time.created, so a copy older than its
// session is not billed again.
func TestV2ForkedMessagesAreNotBilledAgain(t *testing.T) {
	d := newV2Daemon(t)
	d.addSession(t, "ses_f", "", "1791999999999", "tool.messages.json")
	m := v2Meta(t, "ses_f")
	for _, tn := range usageTurns(m) {
		if tn.CostUSD != 0 {
			t.Fatalf("a copied turn kept its cost: %+v", tn)
		}
	}
}

func TestV2SubagentTurnsAreSidechains(t *testing.T) {
	d, m := v2Slot(t, "subagent.messages.json")
	var w struct {
		Data []struct {
			Content []struct {
				State struct {
					Metadata struct {
						SessionID string `json:"sessionID"`
					} `json:"metadata"`
				} `json:"state"`
			} `json:"content"`
		} `json:"data"`
	}
	_ = json.Unmarshal(v2Fixture(t, "subagent.messages.json"), &w)
	var child string
	for _, mm := range w.Data {
		for _, c := range mm.Content {
			if c.State.Metadata.SessionID != "" {
				child = c.State.Metadata.SessionID
			}
		}
	}
	if child == "" {
		t.Fatal("fixture has no subagent session")
	}
	d.addSession(t, child, "ses_a", "1791931541411", "")
	d.messages[child] = []string{
		`{"id":"msg_c1","type":"user","text":"subagent task: say hi","time":{"created":1791931541420}}`,
		`{"id":"msg_c2","type":"assistant","agent":"general","model":{"id":"m","providerID":"stub"},"time":{"created":1791931541430,"completed":1791931541440},"content":[{"type":"text","text":"hi from the child"}]}`,
	}
	td, _ := readTranscript(m)
	var side, deleg int
	for _, tn := range td.Turns {
		if tn.Sidechain {
			side++
		}
		for _, p := range tn.Parts {
			if p.Kind == "delegation" {
				deleg++
				if p.AgentType != "general" || p.Info != "do sub" || strings.Contains(p.Output, "<subagent") {
					t.Errorf("delegation part = %+v", p)
				}
			}
		}
	}
	if side != 2 || deleg != 1 {
		t.Fatalf("sidechain turns = %d, delegations = %d: %+v", side, deleg, td.Turns)
	}
	for i, tn := range td.Turns {
		if tn.Idx != i {
			t.Fatalf("turn %d has Idx %d", i, tn.Idx)
		}
	}
}

func TestV2QueuedInputComesFromTheInbox(t *testing.T) {
	d := newV2Daemon(t)
	d.addSession(t, "ses_a", "", "1000", "")
	m := v2Meta(t, "ses_a")
	d.inbox["ses_a"] = string(v2Fixture(t, "inbox-while-running.json"))
	td, _ := readTranscript(m)
	if strings.Join(td.Queued, "|") != "second queued|third queued" {
		t.Fatalf("queued = %q", td.Queued)
	}
}

func TestV2ReadsEveryPageOfALongConversation(t *testing.T) {
	d := newV2Daemon(t)
	d.addSession(t, "ses_a", "", "1000", "")
	for i := 0; i < 450; i++ {
		d.messages["ses_a"] = append(d.messages["ses_a"],
			fmt.Sprintf(`{"id":"msg_%04d","type":"user","text":"q%d","time":{"created":%d}}`, i, i, 2000+i))
	}
	td, _ := readTranscript(v2Meta(t, "ses_a"))
	if len(td.Turns) != 450 || td.Turns[449].Text != "q449" || d.pages != 3 {
		t.Fatalf("turns = %d (last %q) after %d requests", len(td.Turns), td.Turns[len(td.Turns)-1].Text, d.pages)
	}
}

func TestV2LiveStateFollowsTheActiveList(t *testing.T) {
	d, m := v2Slot(t, "tool.messages.json")
	d.active = []string{"ses_a"}
	if got := LiveState(m); got != "working" {
		t.Fatalf("running session reads %q", got)
	}
	d.active = []string{"ses_other"}
	if got := LiveState(m); got != "idle" {
		t.Fatalf("a session the daemon does not list reads %q", got)
	}
	// Cannot ask: unknown, never idle (the caller falls back to the status plugin).
	d.activeSt = http.StatusBadGateway
	if got := LiveState(m); got != "" {
		t.Fatalf("unreachable active list reads %q, want unknown", got)
	}
	// No conversation yet is idle, as on v1.
	d.activeSt = 0
	fresh := session.Meta{Name: "fresh", Dir: "/work", Kind: session.KindOpencode, Driver: session.DriverManaged}
	if got := LiveState(fresh); got != "idle" {
		t.Fatalf("slot without a conversation reads %q", got)
	}
}

// The sids mapping can be missing (the driver writes it on create): the slot's own conversation
// is then the newest root one created after the slot, never an older conversation of the directory.
func TestV2FindsTheConversationWithoutAMapping(t *testing.T) {
	d := newV2Daemon(t)
	d.addSession(t, "ses_old", "", "1000", "")
	d.addSession(t, "ses_new", "", "9000", "tool.messages.json")
	d.addSession(t, "ses_child", "ses_new", "9500", "")
	m := session.Meta{Name: "unmapped", Dir: "/work", Kind: session.KindOpencode, Driver: session.DriverManaged,
		CreatedAt: time.UnixMilli(5000).UTC().Format(time.RFC3339)}
	td, _ := readTranscript(m)
	if len(td.Turns) != 2 {
		t.Fatalf("turns = %d: picked the wrong conversation", len(td.Turns))
	}
}

func TestV2RecallSettingsReadTheSessionRecord(t *testing.T) {
	d := newV2Daemon(t)
	d.sessions["ses_a"] = `{"data":{"id":"ses_a","agent":"plan","model":{"id":"mm","providerID":"pp","variant":"high"},"time":{"created":1,"updated":1},"location":{"directory":"/work"}}}`
	got := (agentImpl{}).RecallSettings(v2Meta(t, "ses_a"))
	if got.Model != "pp/mm" || got.Mode != "plan" {
		t.Fatalf("recalled %+v", got)
	}
}

func TestV2ResolveForkAt(t *testing.T) {
	_, m := v2Slot(t, "tool.messages.json")
	var w struct {
		Data []struct {
			ID, Type string
		} `json:"data"`
	}
	_ = json.Unmarshal(v2Fixture(t, "tool.messages.json"), &w)
	var user string
	for _, mm := range w.Data {
		if mm.Type == "user" {
			user = mm.ID
		}
	}
	a := agentImpl{}
	if got, err := a.ResolveForkAt(m, agents.ForkPoint{Anchor: user}); err != nil || got != user {
		t.Fatalf("anchor = %q, %v", got, err)
	}
	// Last exchange: keeping everything is what "include" asks for.
	if got, err := a.ResolveForkAt(m, agents.ForkPoint{Anchor: user, Include: true}); err != nil || got != "" {
		t.Fatalf("include on the last exchange = %q, %v", got, err)
	}
	if _, err := a.ResolveForkAt(m, agents.ForkPoint{Anchor: "msg_elsewhere"}); err == nil {
		t.Fatal("an anchor from another conversation was accepted")
	}
}

// --- events ---------------------------------------------------------------------

func TestV2CompactionEventsToggleTheFlag(t *testing.T) {
	_, srv := newMockServe(t)
	h := newTestHandle(t, srv)
	registerTestHandle(t, h)
	ev := func(typ string) {
		handleServeEvent([]byte(`{"id":"e","type":"` + typ + `","data":{"sessionID":"` + h.ses + `"}}`))
	}
	ev("session.compaction.started")
	if !h.isCompacting() {
		t.Fatal("compaction.started did not set the flag")
	}
	ev("session.compaction.ended")
	if h.isCompacting() {
		t.Fatal("compaction.ended left the flag on")
	}
	ev("session.compaction.started")
	ev("session.compaction.failed")
	if h.isCompacting() {
		t.Fatal("compaction.failed left the flag on")
	}
	ev("session.compaction.started")
	ev("session.execution.interrupted")
	if h.isCompacting() {
		t.Fatal("an interrupted execution left the flag on")
	}
	// The same events for another session (a subagent) leave this handle alone.
	handleServeEvent([]byte(`{"id":"e","type":"session.compaction.started","data":{"sessionID":"ses_child"}}`))
	if h.isCompacting() {
		t.Fatal("another session's compaction reached this handle")
	}
}

func TestV2ExecutionEventsWakeOnlyTheirSession(t *testing.T) {
	parent, stopP := watchExec("ses_p")
	defer stopP()
	child, stopC := watchExec("ses_c")
	defer stopC()
	handleServeEvent([]byte(`{"id":"e","type":"session.execution.failed","data":{"sessionID":"ses_c","error":` + authErr + `}}`))
	select {
	case <-child:
	case <-time.After(time.Second):
		t.Fatal("event not delivered")
	}
	select {
	case <-parent:
		t.Fatal("the parent's watch got the child's event")
	default:
	}
}

// --- turn outcome ---------------------------------------------------------------

// turnDaemon is a v2 daemon for one turn: /prompt answers with the input's id, the session is
// listed while running is set, and the message list carries whatever idle message the test set.
type turnDaemon struct {
	*v2Daemon
	prompted chan struct{}
}

func newTurnDaemon(t *testing.T) *turnDaemon {
	d := newV2Daemon(t)
	v2Fast(t)
	td := &turnDaemon{v2Daemon: d, prompted: make(chan struct{}, 4)}
	inner := d.f.srv.Config.Handler
	d.f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/prompt") {
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = w.Write([]byte(`{"data":{"id":"msg_5"}}`))
			td.prompted <- struct{}{}
			return
		}
		inner.ServeHTTP(w, r)
	})
	d.sessions["ses_1"] = `{"data":{"id":"ses_1","time":{"created":1,"updated":1}}}`
	d.messages["ses_1"] = []string{}
	return td
}

func (d *turnDaemon) setIdle(outcome, errJSON string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e := ""
	if errJSON != "" {
		e = `,"error":` + errJSON
	}
	d.messages["ses_1"] = []string{`{"id":"msg_9","type":"idle","outcome":"` + outcome + `","time":{"created":9}` + e + `}`}
}

func (d *turnDaemon) setActive(ids ...string) {
	d.mu.Lock()
	d.active = ids
	d.mu.Unlock()
}

func outcomeOf(t *testing.T, res *http.Response) (messageError, bool) {
	t.Helper()
	defer res.Body.Close()
	return decodeTurnError(res.Body)
}

const authErr = `{"type":"provider.auth","message":"stub: invalid api key","status":401,"response":{"body":"{}"}}`

// The idle message left behind is the record of how the turn ended; it covers a turn the event
// stream never reported (the stream was down, or the turn ended before the first poll).
func TestV2TurnOutcomeFromTheIdleMessage(t *testing.T) {
	for _, tc := range []struct {
		outcome, err string
		wantErr      string
		abort        bool
	}{
		{"succeeded", "", "", false},
		{"failed", authErr, "provider.auth", false},
		{"interrupted", "", abortedErrorName, true},
	} {
		d := newTurnDaemon(t)
		d.setIdle(tc.outcome, tc.err)
		res, err := v2Turn(t, d.f)
		if err != nil {
			t.Fatalf("%s: %v", tc.outcome, err)
		}
		me, has := outcomeOf(t, res)
		if tc.wantErr == "" && has || tc.wantErr != "" && (!has || me.Name != tc.wantErr) {
			t.Errorf("%s: error = %+v (%v), want %q", tc.outcome, me, has, tc.wantErr)
		}
		if tc.wantErr == "provider.auth" && (me.Data.StatusCode != 401 || me.retryable() || !me.ok()) {
			t.Errorf("failed outcome mapped to %+v", me)
		}
		if me.isAbort() != tc.abort {
			t.Errorf("%s: isAbort = %v", tc.outcome, me.isAbort())
		}
	}
}

// The stream wakes the wait at once, without the next poll tick; what it reads is still the API.
func TestV2TurnEndsOnTheEventWithoutWaitingForATick(t *testing.T) {
	d := newTurnDaemon(t)
	d.setActive("ses_1")
	prev := v2PollEvery
	v2PollEvery = time.Minute
	t.Cleanup(func() { v2PollEvery = prev })
	done := make(chan *http.Response, 1)
	go func() {
		res, err := v2Turn(t, d.f)
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	<-d.prompted
	time.Sleep(50 * time.Millisecond) // the first poll has seen it running and is now waiting
	d.setActive()
	d.setIdle("failed", authErr)
	handleServeEvent([]byte(`{"type":"session.execution.failed","data":{"sessionID":"ses_1","error":` + authErr + `}}`))
	select {
	case res := <-done:
		if me, ok := outcomeOf(t, res); !ok || me.Name != "provider.auth" {
			t.Fatalf("outcome = %+v %v", me, ok)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the event did not wake the turn")
	}
}

// An event that belongs to an earlier execution only costs one extra look: the daemon still
// lists the session as running, so the turn goes on.
func TestV2TurnIgnoresALateEventFromThePreviousExecution(t *testing.T) {
	d := newTurnDaemon(t)
	d.setActive("ses_1")
	done := make(chan error, 1)
	go func() {
		_, err := v2Turn(t, d.f)
		done <- err
	}()
	<-d.prompted
	handleServeEvent([]byte(`{"type":"session.execution.failed","data":{"sessionID":"ses_1","error":` + authErr + `}}`))
	select {
	case <-done:
		t.Fatal("a stale terminal event ended the turn")
	case <-time.After(100 * time.Millisecond):
	}
	d.setActive()
	d.setIdle("succeeded", "")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestV2TurnNeverSeenRunningIsUnknown(t *testing.T) {
	d := newTurnDaemon(t)
	d.mu.Lock()
	d.messages["ses_1"] = []string{`{"id":"msg_4","type":"idle","outcome":"succeeded","time":{"created":4}}`} // older than our input
	d.mu.Unlock()
	if _, err := v2Turn(t, d.f); err == nil {
		t.Fatal("a turn that never ran was reported as finished")
	}
}

// runTurn on the explicit outcomes: a failure is a failed turn with its reason, an interrupt we
// did not ask for is a lost turn, and one we asked for is a stop.
func TestV2RunTurnLandsTheExplicitOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, outcome, err string
		asked              bool
		want               agents.TurnState
	}{
		{"succeeded", "succeeded", "", false, agents.TurnCompleted},
		{"failed", "failed", authErr, false, agents.TurnFailed},
		{"retryable", "failed", `{"type":"provider.internal","message":"boom","status":500}`, false, agents.TurnAborted},
		{"stop asked", "interrupted", "", true, agents.TurnCancelled},
		{"lost", "interrupted", "", false, agents.TurnAborted},
		{"finished despite the stop", "succeeded", "", true, agents.TurnCompleted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newTurnDaemon(t)
			d.setIdle(tc.outcome, tc.err)
			h := newTestHandle(t, d.f.srv)
			h.ocSid = "sid-v2run-" + strings.NewReplacer(" ", "-", "/", "-").Replace(tc.name)
			h.ses = "ses_1"
			h.mu.Lock()
			h.tq().Accept(memberInput("go", "msg_go"))
			tk := h.tq().Take()
			h.running = true
			h.mu.Unlock()
			if !h.commit(tk) {
				t.Fatal("commit refused")
			}
			h.mu.Lock()
			h.abortAsked = tc.asked
			h.mu.Unlock()
			h.runTurn(tk, pumpLease(t, h))
			h.mu.Lock()
			h.tq().Settle(tk)
			h.running = false
			got := h.state
			h.mu.Unlock()
			if got != tc.want {
				t.Fatalf("state = %s, want %s", got, tc.want)
			}
		})
	}
}

// A managed session is read through af's daemon; a Terminal session that is not on the shared
// serve (--standalone) is read from the store, since its own server is private.
func TestV2TargetFollowsTheRoute(t *testing.T) {
	managed := session.Meta{Name: "a", Dir: "/d", Kind: session.KindOpencode, Driver: session.DriverManaged}
	terminal := session.Meta{Name: "a", Dir: "/d", Kind: session.KindOpencode}
	f := newFakeServe(t, 2, "pw")
	if tg := v2TargetFor(managed); tg.offline || tg.Addr != f.srv.URL {
		t.Fatalf("managed: %+v", tg)
	}
	if tg := v2TargetFor(terminal); !tg.offline {
		t.Fatalf("a standalone Terminal session was routed to af's daemon: %+v", tg)
	}
}

// The first idle message after OUR input ends our turn. A queued input's execution that starts
// right behind it (still running here, with a different result later) must not be read instead.
func TestV2TurnTakesItsOwnExecutionsOutcomeNotALaterOnes(t *testing.T) {
	for _, fillers := range []int{0, 450} { // 450: our idle is past the first page of the newest-first read
		d := newTurnDaemon(t)
		d.setActive("ses_1") // the queued input's execution is running the whole time
		msgs := []string{
			`{"id":"msg_4","type":"idle","outcome":"failed","time":{"created":4},"error":{"type":"provider.internal","message":"before our input","status":500}}`,
			`{"id":"msg_6","type":"idle","outcome":"failed","time":{"created":6},"error":` + authErr + `}`,
		}
		for i := 0; i < fillers; i++ {
			msgs = append(msgs, fmt.Sprintf(`{"id":"msg_7%04d","type":"user","text":"q","time":{"created":7}}`, i))
		}
		msgs = append(msgs, `{"id":"msg_9","type":"idle","outcome":"succeeded","time":{"created":9}}`)
		d.mu.Lock()
		d.messages["ses_1"] = msgs
		d.mu.Unlock()
		res, err := v2Turn(t, d.f)
		if err != nil {
			t.Fatal(err)
		}
		if me, ok := outcomeOf(t, res); !ok || me.Name != "provider.auth" {
			t.Fatalf("fillers=%d: outcome = %+v %v, want the first execution's 401", fillers, me, ok)
		}
	}
}

// A record that cannot be read says nothing about the outcome: it must never become a success.
func TestV2TurnWithUnreadableOutcomeIsUnknown(t *testing.T) {
	d := newTurnDaemon(t)
	var calls int
	inner := d.f.srv.Config.Handler
	d.f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/message") {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/active") {
			calls++
			if calls == 1 {
				_, _ = w.Write([]byte(`{"data":{"ses_1":{"type":"running"}}}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":{}}`))
			return
		}
		inner.ServeHTTP(w, r)
	})
	res, err := v2Turn(t, d.f)
	if err == nil {
		res.Body.Close()
		t.Fatal("an unreadable outcome was returned as a finished turn")
	}
}

// The session list polls state for every slot in turn: a daemon that hangs on one endpoint must
// cost the whole list one timeout, not one per slot.
func TestV2HungDaemonCostsOneTimeoutNotOnePerSlot(t *testing.T) {
	d := newV2Daemon(t)
	d.addSession(t, "ses_a", "", "1000", "")
	inner := d.f.srv.Config.Handler
	d.f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/active") {
			time.Sleep(400 * time.Millisecond)
		}
		inner.ServeHTTP(w, r)
	})
	pt, pd := v2StateTimeout, v2DownFor
	v2StateTimeout, v2DownFor = 80*time.Millisecond, time.Minute
	t.Cleanup(func() { v2StateTimeout, v2DownFor = pt, pd })
	m := v2Meta(t, "ses_a")
	start := time.Now()
	for i := 0; i < 5; i++ {
		if got := LiveState(m); got != "" {
			t.Fatalf("hung daemon read %q, want unknown", got)
		}
	}
	if el := time.Since(start); el > 250*time.Millisecond {
		t.Fatalf("5 readers took %v: each paid the timeout", el)
	}
}

// Failed outcome reads are counted on their own: a next execution that keeps the session running
// must not keep resetting the count, or the turn never settles while /message is down.
func TestV2UnreadableOutcomeGivesUpWhileTheNextExecutionRuns(t *testing.T) {
	d := newTurnDaemon(t)
	d.setActive("ses_1") // running for the whole test
	var reads int
	inner := d.f.srv.Config.Handler
	d.f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/message") {
			d.mu.Lock()
			reads++
			d.mu.Unlock()
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		inner.ServeHTTP(w, r)
	})
	// seen must be true before outcome reads start: let the first poll see it running.
	if res, err := v2Turn(t, d.f); err == nil {
		res.Body.Close()
		t.Fatal("an unreadable outcome ended the turn as finished")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if reads != v2PollMisses {
		t.Fatalf("gave up after %d outcome reads, want %d", reads, v2PollMisses)
	}
}

// ToDo on opencode 2, from payloads captured on 2.1.4 (TestContractV2Todo): the `todowrite` tool
// exists only with experimental.todo on; its newest completed call is the list.
func TestV2TasksFromTodowrite(t *testing.T) {
	_, m := v2Slot(t, "todo-on.messages.json")
	td, _ := readTranscript(m)
	want := []struct{ subject, status string }{
		{"write the probe", "completed"}, {"read the result", "in_progress"}, {"report", "pending"},
	}
	if len(td.Tasks) != len(want) {
		t.Fatalf("tasks = %+v", td.Tasks)
	}
	for i, w := range want {
		if td.Tasks[i].Subject != w.subject || td.Tasks[i].Status != w.status || td.Tasks[i].ID != fmt.Sprint(i+1) {
			t.Fatalf("task %d = %+v, want %+v", i, td.Tasks[i], w)
		}
	}
}

// With the flag off the call fails ("No tool named todowrite ..."): that is not a list.
func TestV2TasksIgnoreAFailedTodowrite(t *testing.T) {
	_, m := v2Slot(t, "todo-off.messages.json")
	if td, _ := readTranscript(m); len(td.Tasks) != 0 {
		t.Fatalf("tasks = %+v from a todowrite that never ran", td.Tasks)
	}
}

func TestV2TasksLatestCallWinsAndEmptyIsNil(t *testing.T) {
	call := func(status, todos string) v2Msg {
		var c v2Content
		_ = json.Unmarshal([]byte(`{"type":"tool","name":"todowrite","state":{"status":"`+status+`","input":{"todos":`+todos+`}}}`), &c)
		return v2Msg{Type: "assistant", Content: []v2Content{c}}
	}
	got := v2Tasks([]v2Msg{
		call("completed", `[{"content":"old","status":"pending"}]`),
		call("completed", `[{"content":"new","status":""},{"content":"","status":"pending"}]`),
		call("running", `[{"content":"in flight","status":"pending"}]`),
		{Type: "user", Text: "hi"},
	})
	if len(got) != 1 || got[0].Subject != "new" || got[0].Status != "pending" {
		t.Fatalf("tasks = %+v; want the newest completed list, blank status = pending", got)
	}
	if v2Tasks(nil) != nil {
		t.Fatal("no messages must give nil")
	}
}
