//go:build contract

// The question tool as a form (v2form.go) against the REAL opencode 2 binary on PATH, with the
// loopback stub provider asking the question (no credentials, no billing). Skipped on a v1 binary.
//
//	PATH=<dir with opencode 2>:$PATH go test -tags contract -run V2Form ./internal/agents/opencode/
//
// Like contract_v2state_test.go it is not run in CI: opencode-contract.yml pins opencode-ai only.
package opencode

import (
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestContractV2FormQuestionRoundTrip(t *testing.T) {
	_, addr, dir := startStubbedV2(t)
	st := agents.ThreadSettings{Model: "stub/m"}
	api := apiTarget{Addr: addr, Client: serveClient}
	v2ActiveTTL = 0 // a state read right after a turn ended must not reuse the answer from before it
	t.Cleanup(func() { v2ActiveTTL = 500 * time.Millisecond })

	// ask starts a turn that makes the question tool ask, and returns once the session reads as
	// waiting on it. done delivers the turn's end.
	ask := func(name, prompt string) (m session.Meta, ses string, h *threadHandle, done chan struct{}) {
		t.Helper()
		ses, err := serveCreateSession(addr, dir, name)
		if err != nil {
			t.Fatal(err)
		}
		m = session.Meta{Name: name, Dir: dir, Kind: session.KindOpencode, Driver: session.DriverManaged}
		sids.Write(session.UUID(m.Dir, m.Name), ses)
		t.Cleanup(func() { sids.Remove(session.UUID(m.Dir, m.Name)) })
		h = &threadHandle{name: name, dir: dir, ocSid: "sid-" + name, addr: addr, ses: ses, alive: true,
			events: make(chan agents.Event, 64), settings: st}
		done = make(chan struct{})
		go func() {
			defer close(done)
			req, err := newTurnRequest(addr, ses, agents.TurnInput{Prompt: prompt})
			if err != nil {
				return
			}
			if res, err := sendTurn(addr, ses, req, st); err == nil {
				res.Body.Close()
			}
		}()
		for deadline := time.Now().Add(30 * time.Second); LiveState(m) != "question"; time.Sleep(200 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("a turn waiting on the question tool reads %q, want question", LiveState(m))
			}
		}
		return m, ses, h, done
	}
	finished := func(done chan struct{}) {
		t.Helper()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("the turn did not end")
		}
	}

	// Two questions, the second multiple-choice; a typed answer and a pick together.
	m, ses, h, done := ask("v2formrt", "ASKMULTI please")
	td, _ := readTranscript(m)
	if len(td.Pending) != 2 || td.Pending[0].Header != "Pick" || td.Pending[0].ID == "" || !td.Pending[1].MultiSelect || len(td.Pending[1].Options) != 2 {
		t.Fatalf("pending = %+v", td.Pending)
	}
	forms, err := api.pendingForms(ses)
	if err != nil || len(forms) != 1 {
		t.Fatalf("forms = %v, %v", forms, err)
	}
	h.setForms(forms)
	if err := h.Respond(agents.InteractionReply{ID: forms[0].ID, Decision: agents.DecisionAnswer,
		Answers: []agents.InteractionAnswer{{Text: "my own answer"}, {Options: []int{1}, Text: "Pineapple"}}}); err != nil {
		t.Fatal(err)
	}
	finished(done)
	td, _ = readTranscript(m)
	var answered string
	for _, tn := range td.Turns {
		for _, p := range tn.Parts {
			if p.Kind == "question" {
				answered = p.Answer
			}
		}
	}
	if answered != "my own answer" || len(td.Pending) != 0 || LiveState(m) != "idle" {
		t.Fatalf("after the reply: answer=%q pending=%+v state=%q", answered, td.Pending, LiveState(m))
	}
	// The tool result the model read carries both the typed answer and the picks.
	msgs, _ := api.messages(ses)
	var raw strings.Builder
	for _, mm := range msgs {
		for _, c := range mm.Content {
			for _, cc := range c.State.Content {
				raw.WriteString(cc.Text)
			}
		}
	}
	if !strings.Contains(raw.String(), `"Pick one"="my own answer"`) || !strings.Contains(raw.String(), `"Which toppings?"="Ham, Pineapple"`) {
		t.Errorf("the answer did not reach the model: %q", raw.String())
	}

	// Dismiss: the form is cancelled and the turn goes on to its end.
	m, ses, h, done = ask("v2formdismiss", "ASK me")
	forms, _ = api.pendingForms(ses)
	h.setForms(forms)
	if err := h.Respond(agents.InteractionReply{ID: forms[0].ID, Decision: agents.DecisionCancel}); err != nil {
		t.Fatal(err)
	}
	finished(done)
	if f, _ := api.pendingForms(ses); len(f) != 0 || LiveState(m) != "idle" {
		t.Fatalf("after the dismissal: forms=%v state=%q", f, LiveState(m))
	}
	// A second answer to the settled form is refused by the daemon and by our own re-read.
	if err := respondForm(addr, ses, agents.InteractionReply{ID: forms[0].ID, Decision: agents.DecisionAnswer,
		Answers: []agents.InteractionAnswer{{Options: []int{0}}}}); err == nil {
		t.Error("a settled form was answered again")
	}

	// Interrupt while waiting: the form goes with the turn, and the stop is not a failure.
	m, ses, _, done = ask("v2forminterrupt", "ASK again")
	if err := serveAbort(addr, ses); err != nil {
		t.Fatal(err)
	}
	finished(done)
	if f, _ := api.pendingForms(ses); len(f) != 0 || LiveState(m) != "idle" {
		t.Fatalf("after the interrupt: forms=%v state=%q", f, LiveState(m))
	}
	if td, _ := readTranscript(m); len(td.Pending) != 0 {
		t.Errorf("pending after the interrupt: %+v", td.Pending)
	}
}
