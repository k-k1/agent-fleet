package agy

// Tests for the Managed agy handle against a fake agy: this test binary re-executed as
// TestStreamHelperProcess (the os/exec trampoline), speaking the stream-json wire as agy 1.3.5
// was measured to (docs/log/32). A real process is the point: the stop is a SIGINT, the child
// exits after it, and the ordering of a result against that exit is what the handle relies on.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const (
	streamHelperEnv = "AGY_STREAM_TEST_HELPER"
	streamLogEnv    = "AGY_STREAM_TEST_LOG"
)

// TestStreamHelperProcess is the fake agy. Under a plain `go test` it does nothing.
//
// Each stdin message's content picks the turn:
//
//	hello         one agent_response step, SUCCESS
//	deny          a run_command step refused by a permission check; denied_actions keeps a set
//	denyquiet     denied_actions grows with no failing step
//	toolerr       a tool step that fails for an ordinary reason
//	fail          result ERROR with an error text
//	slow          a partial answer, then waits for SIGINT: result "interrupted", exit 1
//	crash         exit 3 with no result
//	result-exit   SUCCESS, then exit 1 at once
func TestStreamHelperProcess(t *testing.T) {
	if os.Getenv(streamHelperEnv) != "1" {
		return
	}
	runFakeAgy(os.Args)
	os.Exit(0)
}

func runFakeAgy(argv []string) {
	conv := ""
	for i, a := range argv {
		if a == "--conversation" && i+1 < len(argv) {
			conv = argv[i+1]
		}
	}
	if p := os.Getenv(streamLogEnv); p != "" {
		f, _ := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		fmt.Fprintf(f, "spawn conv=%s\n", conv)
		f.Close()
	}
	if conv == "" {
		conv = fmt.Sprintf("conv-%d", os.Getpid())
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT)
	w := bufio.NewWriter(os.Stdout)
	emit := func(v map[string]any) {
		b, _ := json.Marshal(v)
		w.Write(append(b, '\n'))
		w.Flush()
	}
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	turns := 0
	var denied []map[string]any
	usage := map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
	stepUsage := map[string]any{"input_tokens": 10, "output_tokens": 2, "total_tokens": 12}
	step := func(st map[string]any) {
		emit(map[string]any{"event": "step_update", "conversation_id": conv, "step_update": st})
	}
	answer := func(text string) {
		step(map[string]any{"step_index": 1, "state": "ACTIVE", "step_type": "agent_response", "text_delta": text})
		step(map[string]any{"step_index": 1, "state": "DONE", "step_type": "agent_response", "text_delta": "\n", "usage": stepUsage})
		usage["input_tokens"] = usage["input_tokens"].(int) + 10
		usage["output_tokens"] = usage["output_tokens"].(int) + 2
		usage["total_tokens"] = usage["total_tokens"].(int) + 12
	}
	result := func(status, response, errText string) {
		turns++
		r := map[string]any{"conversation_id": conv, "status": status, "response": response,
			"num_turns": turns, "usage": usage}
		if errText != "" {
			r["error"] = errText
		}
		if len(denied) > 0 {
			r["denied_actions"] = denied
		}
		emit(map[string]any{"event": "result", "result": r})
	}
	first := true
	for {
		var line string
		var ok bool
		select {
		case line, ok = <-lines:
			if !ok {
				return // stdin closed: exit 0
			}
		case <-sigs:
			os.Exit(1)
		}
		var msg struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		_ = json.Unmarshal([]byte(line), &msg)
		if first {
			first = false
			emit(map[string]any{"event": "init", "conversation_id": conv, "init": map[string]any{"model": "fake"}})
		}
		switch msg.Message.Content {
		case "hello":
			answer("hi")
			result("SUCCESS", "hi", "")
		case "deny":
			step(map[string]any{"step_index": 1, "state": "ERROR", "step_type": "tool", "tool_name": "run_command",
				"tool_info": map[string]any{"error": map[string]any{"type": "permission",
					"message": "permission check failed: user denied permission to run command. Proceed without performing this action."}}})
			if len(denied) == 0 {
				denied = append(denied, map[string]any{"action": "command", "display_name": "RunCommand"})
			}
			answer("could not")
			result("SUCCESS", "could not", "")
		case "denyquiet":
			denied = append(denied, map[string]any{"action": "read_url", "display_name": "ReadUrl"})
			answer("ok")
			result("SUCCESS", "ok", "")
		case "toolerr":
			step(map[string]any{"step_index": 1, "state": "ERROR", "step_type": "tool", "tool_name": "view_file",
				"tool_info": map[string]any{"error": map[string]any{"type": "io", "message": "no such file"}}})
			answer("missing")
			result("SUCCESS", "missing", "")
		case "fail":
			result("ERROR", "", "quota exhausted")
		case "slow":
			step(map[string]any{"step_index": 1, "state": "ACTIVE", "step_type": "agent_response", "text_delta": "part"})
			<-sigs
			result("ERROR", "part", "interrupted")
			// agy takes a moment to exit after answering the stop; input written meanwhile is
			// lost with it.
			time.Sleep(300 * time.Millisecond)
			os.Exit(1)
		case "crash":
			os.Exit(3)
		case "result-exit":
			answer("bye")
			result("SUCCESS", "bye", "")
			os.Exit(1)
		}
	}
}

// fakeStream is a handle whose child is the fake agy, with its spawns logged.
type fakeStream struct {
	h   *streamHandle
	log string
}

func newFakeStream(t *testing.T) *fakeStream {
	t.Helper()
	dir := t.TempDir()
	logPath := dir + "/spawns.log"
	name := "agy-managed-" + strings.ToLower(strings.NewReplacer("/", "-", " ", "-").Replace(t.Name()))
	h := newStreamHandle(name, dir, "slot-"+name, "")
	h.sids = agents.NewSidStore("agy-managed-test-sid")
	h.stopGrace = 2 * time.Second
	h.command = func(h *streamHandle, st agents.ThreadSettings, conv string) *exec.Cmd {
		args := []string{"-test.run=^TestStreamHelperProcess$", "--"}
		if conv != "" {
			args = append(args, "--conversation", conv)
		}
		cmd := exec.Command(os.Args[0], args...)
		cmd.Env = append(os.Environ(), streamHelperEnv+"=1", streamLogEnv+"="+logPath)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		return cmd
	}
	h.Start(session.Meta{Name: name, Kind: session.KindAgy, Dir: dir})
	t.Cleanup(func() {
		select {
		case <-h.Detach():
		case <-time.After(10 * time.Second):
			t.Errorf("child did not exit on Detach")
		}
	})
	return &fakeStream{h: h, log: logPath}
}

// spawns is the --conversation each spawn was given, in order ("" for a fresh one).
func (f *fakeStream) spawns() []string {
	b, _ := os.ReadFile(f.log)
	var out []string
	for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if ln != "" {
			out = append(out, strings.TrimPrefix(ln, "spawn conv="))
		}
	}
	return out
}

func send(t *testing.T, h *streamHandle, prompt string) {
	t.Helper()
	if err := h.Send(agents.TurnInput{Prompt: prompt, Origin: agents.Origin{Kind: agents.OriginMember}}); err != nil {
		t.Fatalf("Send(%q): %v", prompt, err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// settled waits for the queue to drain and the last turn to land, and returns its state.
func settled(t *testing.T, h *streamHandle) agents.TurnState {
	t.Helper()
	waitFor(t, "the turn to settle", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return !h.pumping && !h.running && h.q.Len() == 0
	})
	return h.TurnState()
}

func TestStreamTurnsShareOneChild(t *testing.T) {
	f := newFakeStream(t)
	send(t, f.h, "hello")
	if st := settled(t, f.h); st != agents.TurnCompleted {
		t.Fatalf("first turn = %s, want completed", st)
	}
	first := f.h.LastTurn()
	send(t, f.h, "hello")
	if st := settled(t, f.h); st != agents.TurnCompleted {
		t.Fatalf("second turn = %s, want completed", st)
	}
	second := f.h.LastTurn()
	if got := f.spawns(); len(got) != 1 || got[0] != "" {
		t.Fatalf("spawns = %q, want one fresh child for both turns", got)
	}
	if first.Response != "hi" || second.Response != "hi" {
		t.Fatalf("responses = %q, %q", first.Response, second.Response)
	}
	// result.usage is cumulative over the process; each turn reports its own.
	if second.Usage.TotalTokens != 12 || second.Usage.InputTokens != 10 {
		t.Fatalf("second turn usage = %+v, want the turn's own 12 tokens", second.Usage)
	}
	conv := second.ConversationID
	if !strings.HasPrefix(conv, "conv-") {
		t.Fatalf("conversation = %q", conv)
	}
	if got := f.h.sids.Read(f.h.slotSid); got != conv {
		t.Fatalf("sid store = %q, want %q", got, conv)
	}
}

func TestStreamSoftDenyIsReportedPerTurn(t *testing.T) {
	f := newFakeStream(t)
	run := func(prompt string) *TurnOutcome {
		t.Helper()
		send(t, f.h, prompt)
		if st := settled(t, f.h); st != agents.TurnCompleted {
			t.Fatalf("%s turn = %s, want completed (a refusal does not fail the turn)", prompt, st)
		}
		return f.h.LastTurn()
	}
	d1 := run("deny")
	if len(d1.PermissionDenied) != 2 {
		t.Fatalf("first deny = %+v, want the result delta and the step", d1.PermissionDenied)
	}
	// The same command again: denied_actions is a set and does not grow, so only the step says so.
	d2 := run("deny")
	if len(d2.PermissionDenied) != 1 || d2.PermissionDenied[0].Source != DenialFromStep || d2.PermissionDenied[0].Tool != "run_command" {
		t.Fatalf("repeat deny = %+v, want the failing step alone", d2.PermissionDenied)
	}
	// Earlier denials stay in denied_actions; a clean turn must not inherit them.
	if d := run("hello"); len(d.PermissionDenied) != 0 {
		t.Fatalf("clean turn = %+v, want no denial", d.PermissionDenied)
	}
	if d := run("toolerr"); len(d.PermissionDenied) != 0 {
		t.Fatalf("ordinary tool error = %+v, want no denial", d.PermissionDenied)
	}
	d := run("denyquiet")
	if len(d.PermissionDenied) != 1 || d.PermissionDenied[0].Source != DenialFromResult || d.PermissionDenied[0].Action != "read_url" {
		t.Fatalf("denial with no step = %+v, want the result delta", d.PermissionDenied)
	}
}

func TestStreamFailedTurn(t *testing.T) {
	f := newFakeStream(t)
	send(t, f.h, "fail")
	if st := settled(t, f.h); st != agents.TurnFailed {
		t.Fatalf("turn = %s, want failed", st)
	}
	if !f.h.Alive() {
		t.Fatal("a failed turn took the handle down")
	}
}

func TestStreamStopRespawnsForTheQueue(t *testing.T) {
	f := newFakeStream(t)
	send(t, f.h, "hello")
	settled(t, f.h)
	conv := f.h.LastTurn().ConversationID

	send(t, f.h, "slow")
	waitFor(t, "the slow turn to run", func() bool {
		f.h.mu.Lock()
		defer f.h.mu.Unlock()
		return f.h.state == agents.TurnRunning && f.h.child != nil && f.h.child.tr.text.Len() > 0
	})
	send(t, f.h, "hello") // waits behind the slow turn
	res, err := f.h.Interrupt(agents.InterruptOpts{})
	if err != nil || res.Stop != agents.StopFirst {
		t.Fatalf("Interrupt = %+v, %v; want a first stop", res, err)
	}
	var states []agents.TurnState
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case e := <-f.h.Events():
				mu.Lock()
				states = append(states, e.TurnState)
				mu.Unlock()
			case <-time.After(3 * time.Second):
				return
			}
		}
	}()
	// The queued turn runs on its own after the stop: no new input is needed.
	waitFor(t, "the queued turn after the stop", func() bool { return len(f.spawns()) == 2 })
	if st := settled(t, f.h); st != agents.TurnCompleted {
		t.Fatalf("queued turn = %s, want completed", st)
	}
	<-done
	mu.Lock()
	defer mu.Unlock()
	if !containsState(states, agents.TurnCancelled) {
		t.Fatalf("states after the stop = %v, want the slow turn cancelled", states)
	}
	if got := f.spawns(); got[1] != conv {
		t.Fatalf("respawn conversation = %q, want %q", got[1], conv)
	}
	if !f.h.Alive() {
		t.Fatal("a stop took the handle down")
	}
}

func containsState(states []agents.TurnState, want agents.TurnState) bool {
	for _, s := range states {
		if s == want {
			return true
		}
	}
	return false
}

func TestStreamResultOutranksExit(t *testing.T) {
	f := newFakeStream(t)
	send(t, f.h, "result-exit")
	if st := settled(t, f.h); st != agents.TurnCompleted {
		t.Fatalf("turn = %s, want completed (its result arrived before the exit)", st)
	}
	if f.h.LastTurn().Response != "bye" {
		t.Fatalf("response = %q", f.h.LastTurn().Response)
	}
	if !f.h.Alive() {
		t.Fatal("an exit after a delivered result took the handle down")
	}
	waitFor(t, "the exited child to be let go", func() bool {
		f.h.mu.Lock()
		defer f.h.mu.Unlock()
		return f.h.child == nil
	})
	send(t, f.h, "hello")
	if st := settled(t, f.h); st != agents.TurnCompleted {
		t.Fatalf("next turn = %s, want completed on a new child", st)
	}
}

func TestStreamCrashTakesTheHandleDown(t *testing.T) {
	f := newFakeStream(t)
	send(t, f.h, "hello")
	settled(t, f.h)
	conv := f.h.LastTurn().ConversationID
	send(t, f.h, "crash")
	send(t, f.h, "hello") // queued behind the crash
	waitFor(t, "the crash to land", func() bool { return !f.h.Alive() })
	settledLost := func() bool {
		f.h.mu.Lock()
		defer f.h.mu.Unlock()
		return !f.h.pumping && !f.h.running
	}
	waitFor(t, "the pump to stop", settledLost)
	if st := f.h.TurnState(); st != agents.TurnUnknown {
		t.Fatalf("crashed turn = %s, want unknown", st)
	}
	if err := f.h.Send(agents.TurnInput{Prompt: "hello"}); err == nil {
		t.Fatal("Send on a lost runtime was accepted")
	}
	if n := len(f.spawns()); n != 1 {
		t.Fatalf("spawns after the crash = %d, want no respawn before Resume", n)
	}
	// Resume runs what was queued, on the same conversation.
	f.h.Start(session.Meta{Name: f.h.name, Kind: session.KindAgy, Dir: f.h.dir})
	if st := settled(t, f.h); st != agents.TurnCompleted {
		t.Fatalf("queued turn after Resume = %s, want completed", st)
	}
	if got := f.spawns(); len(got) != 2 || got[1] != conv {
		t.Fatalf("spawns = %q, want a respawn on %q", got, conv)
	}
}

func TestStreamIdleCloseRespawns(t *testing.T) {
	f := newFakeStream(t)
	f.h.idleAfter = 50 * time.Millisecond
	send(t, f.h, "hello")
	settled(t, f.h)
	conv := f.h.LastTurn().ConversationID
	waitFor(t, "the idle child to exit", func() bool {
		f.h.mu.Lock()
		defer f.h.mu.Unlock()
		return f.h.child == nil
	})
	if !f.h.Alive() {
		t.Fatal("an idle close took the handle down")
	}
	send(t, f.h, "hello")
	if st := settled(t, f.h); st != agents.TurnCompleted {
		t.Fatalf("turn after the idle close = %s", st)
	}
	if got := f.spawns(); len(got) != 2 || got[1] != conv {
		t.Fatalf("spawns = %q, want a respawn on %q", got, conv)
	}
}

func TestStreamDetachStopsARunningTurn(t *testing.T) {
	f := newFakeStream(t)
	send(t, f.h, "slow")
	waitFor(t, "the slow turn to run", func() bool { return f.h.TurnState() == agents.TurnRunning })
	select {
	case <-f.h.Detach():
	case <-time.After(10 * time.Second):
		t.Fatal("Detach did not end the child")
	}
	if st := settled(t, f.h); st != agents.TurnCancelled {
		t.Fatalf("detached turn = %s, want cancelled", st)
	}
}

func TestStreamRefusesWhatTheWireCannotCarry(t *testing.T) {
	h := newStreamHandle("agy-refuse", t.TempDir(), "slot-agy-refuse", "")
	h.Start(session.Meta{Name: "agy-refuse", Kind: session.KindAgy})
	if err := h.Send(agents.TurnInput{Prompt: "look", Attachments: []string{"/tmp/x.png"}}); err == nil {
		t.Fatal("an attachment was accepted")
	}
	if err := h.UpdateSettings(agents.ThreadSettings{Model: "other"}); err == nil {
		t.Fatal("a model change was accepted")
	}
	if err := h.UpdateSettings(agents.ThreadSettings{}); err != nil {
		t.Fatalf("an empty update = %v", err)
	}
	if err := h.Respond(agents.InteractionReply{ID: "x"}); err == nil {
		t.Fatal("Respond answered an interaction print mode cannot raise")
	}
}

func TestAwaitResultPrefersTheResult(t *testing.T) {
	exited := make(chan struct{})
	close(exited)
	for i := 0; i < 200; i++ {
		done := make(chan streamResult, 1)
		done <- streamResult{Status: "SUCCESS"}
		if r, ok := awaitResult(done, exited); !ok || r.Status != "SUCCESS" {
			t.Fatalf("round %d: the exit won over a delivered result", i)
		}
	}
	if _, ok := awaitResult(make(chan streamResult, 1), exited); ok {
		t.Fatal("an exit with no result reported one")
	}
}

func TestOutcomeState(t *testing.T) {
	denied := []Denial{{Source: DenialFromStep, Tool: "run_command"}}
	cases := []struct {
		name    string
		out     TurnOutcome
		got     bool
		stopped bool
		want    agents.TurnState
	}{
		{"success", TurnOutcome{Status: "SUCCESS"}, true, false, agents.TurnCompleted},
		{"success with a denial", TurnOutcome{Status: "SUCCESS", PermissionDenied: denied}, true, false, agents.TurnCompleted},
		{"success after a late stop", TurnOutcome{Status: "SUCCESS"}, true, true, agents.TurnCompleted},
		{"interrupted", TurnOutcome{Status: "ERROR", Error: "interrupted"}, true, true, agents.TurnCancelled},
		{"stopped before init", TurnOutcome{Status: "ERROR", Error: "context canceled"}, true, false, agents.TurnCancelled},
		{"error", TurnOutcome{Status: "ERROR", Error: "quota"}, true, false, agents.TurnFailed},
		{"canceled by a denial", TurnOutcome{Status: "CANCELED", PermissionDenied: denied}, true, false, agents.TurnCompleted},
		{"canceled otherwise", TurnOutcome{Status: "CANCELED"}, true, false, agents.TurnFailed},
		{"no result, stopped", TurnOutcome{}, false, true, agents.TurnCancelled},
		{"no result", TurnOutcome{}, false, false, agents.TurnUnknown},
	}
	for _, c := range cases {
		if got := outcomeState(c.out, c.got, c.stopped); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestTurnTrackerResumedFirstResult(t *testing.T) {
	old := []deniedAction{{Action: "command", DisplayName: "RunCommand"}}
	tr := newTurnTracker(true)
	tr.begin()
	// A resumed child's first result may carry the conversation's earlier denials; with no
	// failing step this turn denied nothing.
	if out := tr.finish(streamResult{Status: "SUCCESS", DeniedActions: old}); len(out.PermissionDenied) != 0 {
		t.Fatalf("first result after resume = %+v, want no denial", out.PermissionDenied)
	}
	// It still sets the baseline: the next growth counts.
	next := append(append([]deniedAction(nil), old...), deniedAction{Action: "read_url"})
	if out := tr.finish(streamResult{Status: "SUCCESS", DeniedActions: next}); len(out.PermissionDenied) != 1 || out.PermissionDenied[0].Action != "read_url" {
		t.Fatalf("second result = %+v, want read_url", out.PermissionDenied)
	}
	// A fresh child's first result is all its own.
	fresh := newTurnTracker(false)
	if out := fresh.finish(streamResult{Status: "SUCCESS", DeniedActions: old}); len(out.PermissionDenied) != 1 {
		t.Fatalf("fresh child's first result = %+v, want the denial", out.PermissionDenied)
	}
}

func TestDeniedDeltaIsAMultisetDifference(t *testing.T) {
	a := deniedAction{Action: "command"}
	b := deniedAction{Action: "read_url"}
	if got := deniedDelta([]deniedAction{a}, []deniedAction{a, a, b}); len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("appended repeat = %+v, want [a b]", got)
	}
	if got := deniedDelta([]deniedAction{a, b}, []deniedAction{b, a}); len(got) != 0 {
		t.Fatalf("reordered set = %+v, want none", got)
	}
}

func TestParseStreamLine(t *testing.T) {
	for _, ln := range []string{"", "   ", "not json", `{"no_event":1}`} {
		if _, ok := parseStreamLine([]byte(ln)); ok {
			t.Errorf("%q parsed as an event", ln)
		}
	}
	ev, ok := parseStreamLine([]byte(`{"event":"init","init":{"conversation_id":"c1","model":"m"}}`))
	if !ok || ev.conversationID() != "c1" {
		t.Fatalf("init = %+v, %v", ev, ok)
	}
	var line struct {
		Event   string `json:"event"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(userLine("a\nb \"q\""), &line); err != nil || line.Event != "user" || line.Message.Content != "a\nb \"q\"" {
		t.Fatalf("userLine round trip = %+v, %v", line, err)
	}
	if b := userLine("x"); b[len(b)-1] != '\n' || strings.Count(string(b), "\n") != 1 {
		t.Fatalf("userLine is not one line: %q", b)
	}
}
