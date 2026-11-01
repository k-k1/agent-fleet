package opencode

// The question tool as a form on opencode 2 (v2form.go). The fixtures testdata/v2/form-* are
// captured from a real 2.0.28 serve; the fake daemon only replays them.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func fixtureForms(t *testing.T, name string) []v2Form {
	t.Helper()
	var w struct {
		Data []v2Form `json:"data"`
	}
	if err := json.Unmarshal(v2Fixture(t, name), &w); err != nil {
		t.Fatal(err)
	}
	return w.Data
}

// fixtureEvents returns the recorded events of one file, one raw SSE payload per line, with the
// recorded session id replaced by ses.
func fixtureEvents(t *testing.T, name, ses string) []string {
	t.Helper()
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(v2Fixture(t, name)))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var e struct {
			Data struct {
				SessionID string `json:"sessionID"`
				Form      struct {
					SessionID string `json:"sessionID"`
				} `json:"form"`
			} `json:"data"`
		}
		line := sc.Text()
		if json.Unmarshal([]byte(line), &e) != nil {
			t.Fatal("bad fixture line")
		}
		rec := e.Data.SessionID
		if rec == "" {
			rec = e.Data.Form.SessionID
		}
		out = append(out, strings.ReplaceAll(line, rec, ses))
	}
	return out
}

func eventOfType(t *testing.T, lines []string, typ string) string {
	t.Helper()
	for _, l := range lines {
		if strings.Contains(l, `"type":"`+typ+`"`) {
			return l
		}
	}
	t.Fatalf("no %s event in the fixture", typ)
	return ""
}

// formDaemon is a v2 daemon that also answers the form routes from fixtures.
type formDaemon struct {
	*v2Daemon
	fmu          sync.Mutex
	list         map[string]string // session → GET …/form body
	detail       map[string]string // form id → GET …/form/{id} body
	replyHook    func()            // runs inside a reply, before it is answered
	replyStatus  int               // answered instead of 204 when set
	deleteStatus int               // same for a cancel
	replies      []string          // "POST path body"
	deletes      []string          // "DELETE uri"
}

func newFormDaemon(t *testing.T) *formDaemon {
	d := newV2Daemon(t)
	fd := &formDaemon{v2Daemon: d, list: map[string]string{}, detail: map[string]string{}}
	inner := d.f.srv.Config.Handler
	d.f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if !strings.HasPrefix(p, "/api/session/") || !strings.Contains(p, "/form") {
			inner.ServeHTTP(w, r)
			return
		}
		rest := strings.TrimPrefix(p, "/api/session/")
		ses, tail, _ := strings.Cut(rest, "/form")
		id := strings.TrimPrefix(tail, "/")
		fd.fmu.Lock()
		defer fd.fmu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && id == "":
			if b, ok := fd.list[ses]; ok {
				_, _ = io.WriteString(w, b)
			} else {
				_, _ = io.WriteString(w, `{"data":[]}`)
			}
		case r.Method == "GET":
			if b, ok := fd.detail[id]; ok {
				_, _ = io.WriteString(w, b)
				return
			}
			http.Error(w, `{"_tag":"FormNotFoundError"}`, http.StatusNotFound)
		case r.Method == "POST" && strings.HasSuffix(id, "/reply"):
			b, _ := io.ReadAll(r.Body)
			fd.replies = append(fd.replies, p+" "+string(b))
			if fd.replyHook != nil {
				fd.replyHook()
			}
			if fd.replyStatus != 0 {
				w.WriteHeader(fd.replyStatus)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "DELETE":
			fd.deletes = append(fd.deletes, r.URL.RequestURI())
			if fd.deleteStatus != 0 {
				w.WriteHeader(fd.deleteStatus)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected", http.StatusTeapot)
		}
	})
	return fd
}

// --- mapping ----------------------------------------------------------------------

func TestV2FormMapsToQuestions(t *testing.T) {
	f := fixtureForms(t, "form-multi.pending.json")[0]
	in := f.interaction()
	if in.Kind != agents.InteractionQuestion || in.ID != f.ID || len(in.Questions) != 2 {
		t.Fatalf("interaction = %+v", in)
	}
	q0, q1 := in.Questions[0], in.Questions[1]
	if q0.Header != "Color" || q0.Question != "Which color?" || q0.MultiSelect || len(q0.Options) != 2 || q0.Options[1].Label != "Blue" || q0.Options[1].Description != "b" {
		t.Errorf("single choice = %+v", q0)
	}
	if q1.Header != "Toppings" || !q1.MultiSelect || len(q1.Options) != 3 || q1.Options[2].Label != "Egg" {
		t.Errorf("multiple choice = %+v", q1)
	}
	for _, q := range in.Questions {
		if q.ID != f.ID {
			t.Errorf("question id = %q, want the form id", q.ID)
		}
	}
}

func TestV2FormAnswerFollowsTheFieldTypes(t *testing.T) {
	f := fixtureForms(t, "form-multi.pending.json")[0]
	got, err := f.answer([]agents.InteractionAnswer{{Options: []int{1}}, {Options: []int{0, 2}, Text: " Pineapple "}})
	want := map[string]any{"q0": "Blue", "q1": []string{"Cheese", "Egg", "Pineapple"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("answer = %v, %v; want %v", got, err, want)
	}
	// Free text answers a single choice (custom:true), and a multiselect may select nothing.
	got, err = f.answer([]agents.InteractionAnswer{{Text: "Green"}, {}})
	want = map[string]any{"q0": "Green", "q1": []string{}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("free text = %v, %v; want %v", got, err, want)
	}
	for name, bad := range map[string][]agents.InteractionAnswer{
		"one answer for two questions": {{Options: []int{0}}},
		"option out of range":          {{Options: []int{2}}, {}},
	} {
		if _, err := f.answer(bad); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// An optional field left blank is omitted, not an error: the form decides what it needs.
	if got, err := f.answer([]agents.InteractionAnswer{{}, {Options: []int{0}}}); err != nil || got["q0"] != nil || got["q1"] == nil {
		t.Errorf("blank optional field: %v, %v", got, err)
	}
}

func TestV2FormAnswerOtherFieldTypes(t *testing.T) {
	f := v2Form{Fields: []v2FormField{
		{Key: "n", Type: "number", Required: true},
		{Key: "i", Type: "integer"},
		{Key: "b", Type: "boolean"},
		{Key: "s", Type: "string", Required: true},
		{Key: "x", Type: "external", URL: "https://example.invalid"},
		{Key: "h", Type: "string", Hidden: true},
	}}
	got, err := f.answer([]agents.InteractionAnswer{{Text: "1.5"}, {Text: "7"}, {Options: []int{1}}, {Text: "hi"}, {}})
	want := map[string]any{"n": 1.5, "i": int64(7), "b": false, "s": "hi"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("answer = %v, %v; want %v", got, err, want)
	}
	if _, err := f.answer([]agents.InteractionAnswer{{Text: "x"}, {}, {}, {Text: "hi"}, {}}); err == nil {
		t.Error("a non-number was accepted for a number field")
	}
	if _, err := f.answer([]agents.InteractionAnswer{{}, {}, {}, {Text: "hi"}, {}}); err == nil {
		t.Error("a required field left blank was accepted")
	}
}

// --- the mirror -------------------------------------------------------------------

// A turn waiting on a form is active as far as the daemon is concerned (measured), so it read as
// working with no question: the form list is what makes it a question, the way v1's running
// question tool part did.
func TestV2PendingFormReadsAsQuestion(t *testing.T) {
	fd := newFormDaemon(t)
	fd.addSession(t, "ses_a", "", "1791931000000", "form-waiting.messages.json")
	fd.active = []string{"ses_a"}
	m := v2Meta(t, "ses_a")

	if got := LiveState(m); got != "working" {
		t.Fatalf("no form: LiveState = %q, want working", got)
	}
	fd.list["ses_a"] = string(v2Fixture(t, "form-multi.pending.json"))
	if got := LiveState(m); got != "question" {
		t.Fatalf("form pending: LiveState = %q, want question", got)
	}
	td, _ := readTranscript(m)
	if len(td.Pending) != 2 || td.Pending[0].Header != "Color" || !td.Pending[1].MultiSelect {
		t.Fatalf("pending = %+v", td.Pending)
	}
	if id := fixtureForms(t, "form-multi.pending.json")[0].ID; td.Pending[0].ID != id || td.Pending[1].ID != id {
		t.Errorf("a Managed question must carry the form id /respond takes: %+v", td.Pending)
	}
	// The running question tool part is the question itself, not a turn part.
	for _, tn := range td.Turns {
		for _, p := range tn.Parts {
			if p.Kind == "question" || p.Tool == "question" {
				t.Errorf("an unanswered question tool shows as a part: %+v", p)
			}
		}
	}
	if pm, ok := (agentImpl{}).PendingModal(m); !ok || pm.Kind != "question" || len(pm.Questions) != 2 {
		t.Errorf("PendingModal = %+v, %v", pm, ok)
	}

	// Nothing running: a form the daemon no longer holds, or a stale "running" tool part left by
	// a killed daemon, is not a question.
	fd.active = nil
	if got := LiveState(m); got != "idle" {
		t.Errorf("idle session: LiveState = %q", got)
	}
	if td, _ := readTranscript(m); len(td.Pending) != 0 {
		t.Errorf("idle session still has a pending question: %+v", td.Pending)
	}
}

// A Terminal slot on the shared serve shows the pending form (so the question chip and the card
// light up) with the form id, the address /respond takes. A read never answers or cancels it.
func TestV2TerminalShowsTheFormWithItsAddress(t *testing.T) {
	fd, m := terminalForm(t)

	if got := LiveState(m); got != "question" {
		t.Fatalf("LiveState = %q, want question", got)
	}
	td, _ := readTranscript(m)
	id := fixtureForms(t, "form-single.pending.json")[0].ID
	if len(td.Pending) != 1 || td.Pending[0].Question != "Pick one" || td.Pending[0].ID != id {
		t.Fatalf("pending = %+v", td.Pending)
	}
	fd.fmu.Lock()
	defer fd.fmu.Unlock()
	if len(fd.replies)+len(fd.deletes) != 0 {
		t.Errorf("a read answered or cancelled a form: %v %v", fd.replies, fd.deletes)
	}
}

// terminalForm is a Terminal slot on the shared serve whose session waits on the form-single form.
func terminalForm(t *testing.T) (*formDaemon, session.Meta) {
	fd := newFormDaemon(t)
	fd.addSession(t, "ses_a", "", "1791931000000", "form-waiting.messages.json")
	fd.active = []string{"ses_a"}
	fd.list["ses_a"] = string(v2Fixture(t, "form-single.pending.json"))
	fd.detail[fixtureForms(t, "form-single.pending.json")[0].ID] = string(v2Fixture(t, "form-detail.pending.json"))
	prev := v2TargetFor
	v2TargetFor = func(session.Meta) apiTarget {
		return apiTarget{Addr: fd.f.srv.URL, Client: serveClient}
	}
	t.Cleanup(func() { v2TargetFor = prev })
	m := session.Meta{Name: "v2term", Dir: "/work", Kind: session.KindOpencode, Driver: session.DriverTUI}
	sids.Write(session.UUID(m.Dir, m.Name), "ses_a")
	t.Cleanup(func() { sids.Remove(session.UUID(m.Dir, m.Name)) })
	fd.tag("ses_a", m)
	return fd, m
}

// tag makes the daemon's session carry slot m's ownership tags, as the shared launch writes them.
func (fd *formDaemon) tag(ses string, m session.Meta) {
	fd.sessions[ses] = fmt.Sprintf(`{"data":{"id":%q,"parentID":"","projectID":"p","cost":0,"time":{"created":1791931000000,"updated":1791931000000},"location":{"directory":"/work"},"metadata":{"af.sid":%q,"af.name":%q}}}`,
		ses, session.UUID(m.Dir, m.Name), m.Name)
}

// Another slot's form is never reachable from this one: the write follows the recorded
// conversation only, and only while it carries this slot's tags. Nothing is posted otherwise.
func TestV2RespondTerminalOnlyTouchesItsOwnConversation(t *testing.T) {
	fd, m := terminalForm(t)
	id := fixtureForms(t, "form-single.pending.json")[0].ID
	reply := agents.InteractionReply{ID: id, Decision: agents.DecisionAnswer, Answers: []agents.InteractionAnswer{{Options: []int{0}}}}
	posted := func() int {
		fd.fmu.Lock()
		defer fd.fmu.Unlock()
		return len(fd.replies) + len(fd.deletes)
	}
	sid := session.UUID(m.Dir, m.Name)

	sids.Remove(sid) // no recorded conversation: the directory guess would find ses_a
	if err := (managedDriver{}).RespondTerminal(m, reply); err == nil || posted() != 0 {
		t.Fatalf("no recorded sid: %v, posted %d", err, posted())
	}
	sids.Write(sid, "ses_deleted") // recorded but gone (404): same
	if err := (managedDriver{}).RespondTerminal(m, reply); err == nil || posted() != 0 {
		t.Fatalf("404 sid: %v, posted %d", err, posted())
	}
	sids.Write(sid, "ses_a")
	other := session.Meta{Name: "someone-else", Dir: "/work", Kind: session.KindOpencode, Driver: session.DriverTUI}
	fd.tag("ses_a", other) // the conversation belongs to another slot
	if err := (managedDriver{}).RespondTerminal(m, reply); err == nil || posted() != 0 {
		t.Fatalf("foreign tags: %v, posted %d", err, posted())
	}
	fd.tag("ses_a", m) // control: the rightful owner is answered
	if err := (managedDriver{}).RespondTerminal(m, reply); err != nil || posted() != 1 {
		t.Fatalf("owner: %v, posted %d", err, posted())
	}
}

func TestV2RespondTerminalAnswersAndDismisses(t *testing.T) {
	fd, m := terminalForm(t)
	id := fixtureForms(t, "form-single.pending.json")[0].ID
	d := managedDriver{}

	if err := d.RespondTerminal(m, agents.InteractionReply{ID: "frm_stale", Decision: agents.DecisionAnswer}); !errors.Is(err, errFormSettled) {
		t.Fatalf("a form the daemon does not hold: %v", err)
	}
	if err := d.RespondTerminal(m, agents.InteractionReply{ID: id, Decision: agents.DecisionAnswer,
		Answers: []agents.InteractionAnswer{{Options: []int{1}}}}); err != nil {
		t.Fatal(err)
	}
	if err := d.RespondTerminal(m, agents.InteractionReply{ID: id, Decision: agents.DecisionCancel}); err != nil {
		t.Fatal(err)
	}
	fd.fmu.Lock()
	defer fd.fmu.Unlock()
	if len(fd.replies) != 1 || !strings.HasSuffix(fd.replies[0], "/form/"+id+"/reply "+`{"answer":{"q0":"B"}}`) {
		t.Errorf("replies = %q", fd.replies)
	}
	if len(fd.deletes) != 1 || !strings.Contains(fd.deletes[0], "/form/"+id+"?message=") {
		t.Errorf("deletes = %q", fd.deletes)
	}
}

// The person answering in the TUI first: the daemon says the form is settled, or has already
// dropped it from its list, and nothing is posted.
func TestV2RespondTerminalNeverDoubleAnswers(t *testing.T) {
	fd, m := terminalForm(t)
	id := fixtureForms(t, "form-single.pending.json")[0].ID
	reply := agents.InteractionReply{ID: id, Decision: agents.DecisionAnswer, Answers: []agents.InteractionAnswer{{Options: []int{0}}}}

	fd.detail[id] = string(v2Fixture(t, "form-detail.answered.json")) // listed, but settled meanwhile
	if err := (managedDriver{}).RespondTerminal(m, reply); !errors.Is(err, errFormSettled) {
		t.Fatalf("settled form: %v", err)
	} else if !errors.Is(err, agents.ErrInteractionNotPending) || strings.ContainsFunc(err.Error(), func(r rune) bool { return r > 0x7f }) {
		// The wire code is interaction_not_pending and the text reaches logs and toasts verbatim.
		t.Fatalf("settled form error must be the English not-pending sentinel: %v", err)
	}
	fd.list["ses_a"] = `{"data":[]}` // gone from the list (the detail alone would still say pending)
	fd.detail[id] = string(v2Fixture(t, "form-detail.pending.json"))
	if err := (managedDriver{}).RespondTerminal(m, reply); !errors.Is(err, errFormSettled) {
		t.Fatalf("form gone from the list: %v", err)
	} else if strings.ContainsFunc(err.Error(), func(r rune) bool { return r > 0x7f }) {
		t.Fatalf("form gone from the list: non-English message: %v", err)
	}
	fd.replyStatus = http.StatusConflict // settled between the re-read and the post
	fd.list["ses_a"] = string(v2Fixture(t, "form-single.pending.json"))
	if err := (managedDriver{}).RespondTerminal(m, reply); !errors.Is(err, errFormSettled) {
		t.Fatalf("409: %v", err)
	}
	fd.fmu.Lock()
	defer fd.fmu.Unlock()
	if len(fd.replies) != 1 { // only the 409 one was ever posted; the list check kept the other two back
		t.Errorf("replies = %q", fd.replies)
	}
}

// A slot that is not on the shared serve (--standalone) has no API route: the Console keeps keys.
func TestV2RespondTerminalRefusesWhatIsNotOnTheServe(t *testing.T) {
	prev := v2TargetFor
	v2TargetFor = func(session.Meta) apiTarget { return apiTarget{offline: true} }
	t.Cleanup(func() { v2TargetFor = prev })
	m := session.Meta{Name: "v2standalone", Dir: "/work", Kind: session.KindOpencode, Driver: session.DriverTUI}
	err := (managedDriver{}).RespondTerminal(m, agents.InteractionReply{ID: "frm", Decision: agents.DecisionAnswer})
	if !errors.Is(err, agents.ErrTerminalRespond) {
		t.Fatalf("err = %v", err)
	}
}

// --- the driver -------------------------------------------------------------------

func formHandle(t *testing.T, fd *formDaemon) *threadHandle {
	h := newTestHandle(t, fd.f.srv)
	registerTestHandle(t, h)
	// The tests mark the handle running without a pump behind it; the cleanup newTestHandle
	// registered waits for the pump to be idle, so put the flag back first (cleanups run LIFO).
	t.Cleanup(func() {
		h.mu.Lock()
		h.running = false
		h.mu.Unlock()
	})
	return h
}

// waitOn puts the handle where the events would have: running a turn that waits on these forms.
func waitOn(h *threadHandle, forms ...v2Form) {
	h.mu.Lock()
	h.state, h.running = agents.TurnWaitingInteraction, true
	h.setForms(forms)
	h.mu.Unlock()
}

func createdEvent(t *testing.T, f v2Form) []byte {
	b, err := json.Marshal(map[string]any{"type": "form.created", "data": map[string]any{"form": f}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func settledEvent(typ, ses, id string) []byte {
	return []byte(`{"type":"` + typ + `","data":{"id":"` + id + `","sessionID":"` + ses + `"}}`)
}

func TestV2FormEventsMoveTheHandle(t *testing.T) {
	fd := newFormDaemon(t)
	h := formHandle(t, fd)
	h.mu.Lock()
	h.state, h.running = agents.TurnRunning, true
	h.mu.Unlock()
	evs := fixtureEvents(t, "form-multi-reply.events.jsonl", h.ses)

	handleServeEvent([]byte(eventOfType(t, evs, "form.created")))
	snap, _ := h.Snapshot()
	if snap.TurnState != agents.TurnWaitingInteraction || snap.Interaction == nil || len(snap.Interaction.Questions) != 2 {
		t.Fatalf("after form.created: %+v", snap)
	}
	id := snap.Interaction.ID
	if err := h.Send(agents.TurnInput{Prompt: "stray", ClientMessageID: "msg_stray"}); !ErrQuestionPending(err) {
		t.Errorf("free text while a form waits = %v, want question_pending", err)
	}

	// Answered elsewhere (an attached TUI): cleared, and the turn is running again.
	handleServeEvent([]byte(eventOfType(t, evs, "form.replied")))
	snap, _ = h.Snapshot()
	if snap.TurnState != agents.TurnRunning || snap.Interaction != nil {
		t.Fatalf("after form.replied: %+v", snap)
	}

	// Another session's form, and another form's settlement, leave the handle alone.
	handleServeEvent([]byte(strings.ReplaceAll(eventOfType(t, evs, "form.created"), h.ses, "ses_child")))
	handleServeEvent([]byte(eventOfType(t, evs, "form.created")))
	handleServeEvent([]byte(strings.ReplaceAll(eventOfType(t, evs, "form.replied"), id, "frm_other")))
	if snap, _ = h.Snapshot(); snap.Interaction == nil || snap.TurnState != agents.TurnWaitingInteraction {
		t.Fatalf("an unrelated form.replied cleared the question: %+v", snap)
	}

	// An interrupt cancels the form while the handle is already interrupting: the state stays.
	h.mu.Lock()
	h.state = agents.TurnInterrupting
	h.mu.Unlock()
	cancelled := eventOfType(t, fixtureEvents(t, "form-interrupt.events.jsonl", h.ses), "form.cancelled")
	var rec struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(cancelled), &rec)
	handleServeEvent([]byte(strings.ReplaceAll(cancelled, rec.Data.ID, id)))
	snap, _ = h.Snapshot()
	if snap.TurnState != agents.TurnInterrupting || snap.Interaction != nil {
		t.Fatalf("after the interrupt's form.cancelled: %+v", snap)
	}
}

// A form that was already waiting when the Agent (re)attached is found by reconcile, not by an event.
func TestV2ReconcileFindsAWaitingForm(t *testing.T) {
	fd := newFormDaemon(t)
	h := formHandle(t, fd)
	fd.active = []string{h.ses}
	fd.list[h.ses] = string(v2Fixture(t, "form-single.pending.json"))
	h.reconcile()
	snap, _ := h.Snapshot()
	if snap.TurnState != agents.TurnWaitingInteraction || snap.Interaction == nil || len(snap.Interaction.Questions) != 1 {
		t.Fatalf("reconcile = %+v", snap)
	}
}

func TestV2RespondAnswersTheForm(t *testing.T) {
	fd := newFormDaemon(t)
	h := formHandle(t, fd)
	f := fixtureForms(t, "form-single.pending.json")[0]
	fd.detail[f.ID] = string(v2Fixture(t, "form-detail.pending.json"))
	waitOn(h, f)

	if err := h.Respond(agents.InteractionReply{ID: "frm_stale", Decision: agents.DecisionAnswer}); err == nil {
		t.Fatal("a reply to an id that is not waiting was accepted")
	}
	if err := h.Respond(agents.InteractionReply{ID: f.ID, Decision: agents.DecisionAnswer,
		Answers: []agents.InteractionAnswer{{Options: []int{1}}}}); err != nil {
		t.Fatal(err)
	}
	fd.fmu.Lock()
	replies := append([]string(nil), fd.replies...)
	fd.fmu.Unlock()
	if len(replies) != 1 || !strings.HasSuffix(replies[0], "/form/"+f.ID+"/reply "+`{"answer":{"q0":"B"}}`) {
		t.Fatalf("replies = %q", replies)
	}
	snap, _ := h.Snapshot()
	if snap.Interaction != nil || snap.TurnState != agents.TurnRunning {
		t.Errorf("after the reply: %+v", snap)
	}
}

// Dismissing maps to v1's reject: the form is cancelled, with a reason the model reads.
func TestV2RespondCancelDismissesTheForm(t *testing.T) {
	fd := newFormDaemon(t)
	h := formHandle(t, fd)
	f := fixtureForms(t, "form-single.pending.json")[0]
	waitOn(h, f)
	if err := h.Respond(agents.InteractionReply{ID: f.ID, Decision: agents.DecisionCancel}); err != nil {
		t.Fatal(err)
	}
	fd.fmu.Lock()
	defer fd.fmu.Unlock()
	if len(fd.deletes) != 1 || !strings.Contains(fd.deletes[0], "/form/"+f.ID+"?message=") {
		t.Fatalf("deletes = %q", fd.deletes)
	}
	if len(fd.replies) != 0 {
		t.Errorf("a dismissal posted a reply: %q", fd.replies)
	}
}

// Answering a form somebody else settled first (the TUI) must not post a second answer.
func TestV2RespondDoesNotDoubleAnswer(t *testing.T) {
	fd := newFormDaemon(t)
	h := formHandle(t, fd)
	f := fixtureForms(t, "form-single.pending.json")[0]
	fd.detail[f.ID] = string(v2Fixture(t, "form-detail.answered.json"))
	waitOn(h, f)
	err := h.Respond(agents.InteractionReply{ID: f.ID, Decision: agents.DecisionAnswer, Answers: []agents.InteractionAnswer{{Options: []int{0}}}})
	if err == nil || !strings.Contains(err.Error(), "answered") {
		t.Fatalf("Respond on a settled form = %v", err)
	}
	fd.fmu.Lock()
	defer fd.fmu.Unlock()
	if len(fd.replies) != 0 {
		t.Errorf("a settled form was answered again: %q", fd.replies)
	}
}

// secondForm is the form-single fixture under another id, the same session's next question.
func secondForm(t *testing.T, h *threadHandle) (a, b v2Form) {
	a = fixtureForms(t, "form-single.pending.json")[0]
	a.SessionID = h.ses
	b = a
	b.ID = "frm_second"
	return a, b
}

// Two questions waiting at once: the first stays the card, and settling it (or the second, from
// outside) hands over to whatever still waits instead of leaving a card nobody can answer.
func TestV2TwoPendingFormsAreAnsweredInTurn(t *testing.T) {
	fd := newFormDaemon(t)
	h := formHandle(t, fd)
	a, b := secondForm(t, h)
	fd.detail[a.ID] = string(v2Fixture(t, "form-detail.pending.json"))
	bd := strings.ReplaceAll(string(v2Fixture(t, "form-detail.pending.json")), a.ID, b.ID)
	fd.detail[b.ID] = bd
	h.mu.Lock()
	h.state, h.running = agents.TurnRunning, true
	h.mu.Unlock()
	handleServeEvent(createdEvent(t, a))
	handleServeEvent(createdEvent(t, b))
	if snap, _ := h.Snapshot(); snap.Interaction == nil || snap.Interaction.ID != a.ID {
		t.Fatalf("the second form replaced the first: %+v", snap.Interaction)
	}
	reply := func(id string) error {
		return h.Respond(agents.InteractionReply{ID: id, Decision: agents.DecisionAnswer, Answers: []agents.InteractionAnswer{{Options: []int{0}}}})
	}
	// B is settled from outside (the TUI): A stays.
	handleServeEvent(settledEvent("form.replied", h.ses, b.ID))
	if snap, _ := h.Snapshot(); snap.Interaction == nil || snap.Interaction.ID != a.ID || snap.TurnState != agents.TurnWaitingInteraction {
		t.Fatalf("after B was settled: %+v", snap)
	}
	handleServeEvent(createdEvent(t, b))
	if err := reply(b.ID); err != nil { // either one may be answered
		t.Fatal(err)
	}
	snap, _ := h.Snapshot()
	if snap.Interaction == nil || snap.Interaction.ID != a.ID || snap.TurnState != agents.TurnWaitingInteraction {
		t.Fatalf("after answering B, A must still be the question: %+v", snap)
	}
	if err := reply(a.ID); err != nil {
		t.Fatalf("A could not be answered: %v", err)
	}
	if snap, _ = h.Snapshot(); snap.Interaction != nil || snap.TurnState != agents.TurnRunning {
		t.Fatalf("after both: %+v", snap)
	}
}

// The next question can arrive on the event stream while the reply to the last one is still in
// flight (two connections): finishing the reply must not take the new one away.
func TestV2ReplyDoesNotEraseTheNextForm(t *testing.T) {
	fd := newFormDaemon(t)
	h := formHandle(t, fd)
	a, b := secondForm(t, h)
	fd.detail[a.ID] = string(v2Fixture(t, "form-detail.pending.json"))
	waitOn(h, a)
	fd.replyHook = func() {
		handleServeEvent(settledEvent("form.replied", h.ses, a.ID))
		handleServeEvent(createdEvent(t, b))
	}
	if err := h.Respond(agents.InteractionReply{ID: a.ID, Decision: agents.DecisionAnswer, Answers: []agents.InteractionAnswer{{Options: []int{0}}}}); err != nil {
		t.Fatal(err)
	}
	snap, _ := h.Snapshot()
	if snap.Interaction == nil || snap.Interaction.ID != b.ID || snap.TurnState != agents.TurnWaitingInteraction {
		t.Fatalf("the reply to A erased B: %+v", snap)
	}
}

// A settlement the stream dropped: the daemon says the form is answered (or 409s the POST), and
// the list is empty. The handle must let go of it (accept's question_pending guard reads the same
// field) and the mirror must not bring the card back.
func TestV2SettledFormWhoseEventWasMissed(t *testing.T) {
	for name, setup := range map[string]func(*formDaemon, v2Form){
		"detail says answered": func(fd *formDaemon, f v2Form) {
			fd.detail[f.ID] = string(v2Fixture(t, "form-detail.answered.json"))
		},
		"daemon forgot it": func(fd *formDaemon, f v2Form) {},
	} {
		t.Run(name, func(t *testing.T) {
			fd := newFormDaemon(t)
			fd.addSession(t, "ses_a", "", "1791931000000", "form-waiting.messages.json")
			m := v2Meta(t, "ses_a")
			h := formHandle(t, fd)
			h.name, h.ses = m.Name, "ses_a"
			registerTestHandle(t, h) // again, under the slot's name: that is what the mirror looks up
			f := fixtureForms(t, "form-single.pending.json")[0]
			fd.active = []string{"ses_a"} // the list stays empty: the form is gone
			setup(fd, f)
			waitOn(h, f)
			if td, _ := readTranscript(m); len(td.Pending) != 0 {
				// the read itself reconciles: a card for a form the daemon does not list is stale
				t.Fatalf("a settled form is still on the card: %+v", td.Pending)
			}
			if snap, _ := h.Snapshot(); snap.Interaction != nil || snap.TurnState != agents.TurnRunning {
				t.Fatalf("the read left the handle waiting: %+v", snap)
			}
		})
	}
	// Respond reaches the same end without any mirror read in between.
	fd := newFormDaemon(t)
	h := formHandle(t, fd)
	f := fixtureForms(t, "form-single.pending.json")[0]
	fd.detail[f.ID] = string(v2Fixture(t, "form-detail.answered.json"))
	waitOn(h, f)
	err := h.Respond(agents.InteractionReply{ID: f.ID, Decision: agents.DecisionAnswer, Answers: []agents.InteractionAnswer{{Options: []int{0}}}})
	if !errors.Is(err, errFormSettled) {
		t.Fatalf("Respond = %v, want errFormSettled", err)
	}
	if snap, _ := h.Snapshot(); snap.Interaction != nil || snap.TurnState != agents.TurnRunning {
		t.Fatalf("Respond left the handle waiting: %+v", snap)
	}
}

// Respond itself recovers from a refusal that says "settled", with no mirror read to do it for it:
// the request really went out (or, for a 404 on the detail, stopped before it), the error is
// errFormSettled, and the handle has already let go of that form and kept the one behind it.
func TestV2RespondRecoversFromARefusalThatSaysSettled(t *testing.T) {
	answer := []agents.InteractionAnswer{{Options: []int{0}}}
	pending := func(fd *formDaemon, f v2Form) { fd.detail[f.ID] = string(v2Fixture(t, "form-detail.pending.json")) }
	cases := map[string]struct {
		setup   func(fd *formDaemon, f v2Form)
		reply   agents.InteractionReply
		posts   int // replies that must have gone out
		cancels int // cancels that must have gone out
	}{
		"POST 409 after a pending detail": {
			setup: func(fd *formDaemon, f v2Form) { pending(fd, f); fd.replyStatus = http.StatusConflict },
			reply: agents.InteractionReply{Decision: agents.DecisionAnswer, Answers: answer}, posts: 1,
		},
		"POST 404 after a pending detail": {
			setup: func(fd *formDaemon, f v2Form) { pending(fd, f); fd.replyStatus = http.StatusNotFound },
			reply: agents.InteractionReply{Decision: agents.DecisionAnswer, Answers: answer}, posts: 1,
		},
		"GET 404 on the detail": {
			setup: func(fd *formDaemon, f v2Form) {},
			reply: agents.InteractionReply{Decision: agents.DecisionAnswer, Answers: answer},
		},
		"DELETE 409": {
			setup: func(fd *formDaemon, f v2Form) { fd.deleteStatus = http.StatusConflict },
			reply: agents.InteractionReply{Decision: agents.DecisionCancel}, cancels: 1,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fd := newFormDaemon(t)
			h := formHandle(t, fd)
			a, b := secondForm(t, h)
			c.setup(fd, a)
			waitOn(h, a, b)
			c.reply.ID = a.ID
			err := h.Respond(c.reply)
			if !errors.Is(err, errFormSettled) {
				t.Fatalf("Respond = %v, want errFormSettled", err)
			}
			fd.fmu.Lock()
			posts, cancels := len(fd.replies), len(fd.deletes)
			fd.fmu.Unlock()
			if posts != c.posts || cancels != c.cancels {
				t.Fatalf("requests sent: %d replies, %d cancels; want %d, %d", posts, cancels, c.posts, c.cancels)
			}
			h.mu.Lock()
			forms, inter, state := len(h.forms), h.inter, h.state
			h.mu.Unlock()
			if forms != 1 || inter == nil || inter.ID != b.ID || state != agents.TurnWaitingInteraction {
				t.Fatalf("after the refusal: %d forms, inter %+v, state %s; want only %s left", forms, inter, state, b.ID)
			}
		})
	}
}

// A Terminal pane on the shared serve whose turn waits on a form shows the question modal, so the
// Console's free text is refused instead of answering the highlighted option. The modal comes from
// the daemon's form list.
func TestTerminalModalReadsAFormOnTheSharedServe(t *testing.T) {
	_, m := terminalForm(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("AF_TMUX_SOCKET", "")
	if got := TerminalModal(m); got != "question" {
		t.Fatalf("TerminalModal = %q, want question", got)
	}
}
