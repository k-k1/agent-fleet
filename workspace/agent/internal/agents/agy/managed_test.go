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
	streamHelperEnv  = "AGY_STREAM_TEST_HELPER"
	streamLogEnv     = "AGY_STREAM_TEST_LOG"
	streamGateEnv    = "AGY_STREAM_TEST_GATE"
	streamNoReadEnv  = "AGY_STREAM_TEST_NOREAD_FIRST"
	streamExitEnv    = "AGY_STREAM_TEST_EXIT_AT_START"
	streamLingerEnv  = "AGY_STREAM_TEST_LINGER"
	streamSilentEnv  = "AGY_STREAM_TEST_EXIT_AFTER_READ"
	streamFakePrompt = "hello"
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
//	result-exit   SUCCESS, then exit 1 300 ms later without reading stdin
//	gate          SUCCESS once the file named by AGY_STREAM_TEST_GATE exists
//	slowok        SUCCESS after 500 ms, or "interrupted" and exit 1 on SIGINT
//	stubborn      never answers and ignores SIGINT
//	grandchild    starts a process in its group that ignores TERM and INT, logs its pid, SUCCESS
//	deaf          never answers and ignores SIGINT and SIGTERM
//
// A SIGINT between turns exits 1. With AGY_STREAM_TEST_NOREAD_FIRST=1 the first spawn of a
// test never reads stdin. With AGY_STREAM_TEST_EXIT_AT_START=1 every spawn exits 2 before
// reading anything. With AGY_STREAM_TEST_LINGER=1 a closed stdin is answered by exiting a second
// later. With AGY_STREAM_TEST_EXIT_AFTER_READ=1 a spawn reads one line and exits 2 without a
// word. The log gets "spawn conv=<id> pid=<pid>" per spawn, "turn <content>"
// per message read and "grand <pid>" per grandchild.
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
	logLine := func(format string, a ...any) {
		if p := os.Getenv(streamLogEnv); p != "" {
			f, _ := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			fmt.Fprintf(f, format+"\n", a...)
			f.Close()
		}
	}
	firstSpawn := true
	if b, err := os.ReadFile(os.Getenv(streamLogEnv)); err == nil && strings.Contains(string(b), "spawn ") {
		firstSpawn = false
	}
	logLine("spawn conv=%s pid=%d", conv, os.Getpid())
	if firstSpawn && os.Getenv(streamNoReadEnv) == "1" {
		select {} // never reads stdin; SIGTERM ends it
	}
	if os.Getenv(streamExitEnv) == "1" {
		os.Exit(2)
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
				if os.Getenv(streamLingerEnv) == "1" {
					time.Sleep(time.Second)
				}
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
		logLine("turn %s", msg.Message.Content)
		if os.Getenv(streamSilentEnv) == "1" {
			os.Exit(2)
		}
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
			time.Sleep(300 * time.Millisecond) // room for the next turn to be committed to it
			os.Exit(1)
		case "gate":
			for {
				if _, err := os.Stat(os.Getenv(streamGateEnv)); err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			answer("gated")
			result("SUCCESS", "gated", "")
		case "slowok":
			select {
			case <-sigs:
				result("ERROR", "", "interrupted")
				os.Exit(1)
			case <-time.After(500 * time.Millisecond):
				answer("slow done")
				result("SUCCESS", "slow done", "")
			}
		case "stubborn":
			select {}
		case "deaf":
			signal.Ignore(syscall.SIGINT, syscall.SIGTERM)
			logLine("ready deaf")
			select {}
		case "grandchild":
			gc := exec.Command("sh", "-c", `trap "" TERM INT; sleep 60`)
			if gc.Start() == nil {
				logLine("grand %d", gc.Process.Pid)
			}
			answer("spawned")
			result("SUCCESS", "spawned", "")
		}
	}
}

// fakeStream is a handle whose child is the fake agy, with its spawns logged.
type fakeStream struct {
	h    *streamHandle
	log  string
	gate string
	// env is added to each spawn's environment.
	env []string
}

func newFakeStream(t *testing.T) *fakeStream {
	t.Helper()
	dir := t.TempDir()
	logPath := dir + "/spawns.log"
	f := &fakeStream{log: logPath, gate: dir + "/gate"}
	// Unique per run: the sid store and the ledger persist under the package's scratch HOME, and
	// -count=N would otherwise start a test's second run as a resume of its first.
	name := fmt.Sprintf("agy-managed-%s-%d", strings.ToLower(strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())), time.Now().UnixNano())
	h := newStreamHandle(name, dir, "slot-"+name, "")
	h.sids = agents.NewSidStore("agy-managed-test-sid")
	h.stopGrace = 2 * time.Second
	h.command = func(h *streamHandle, st agents.ThreadSettings, conv string) *exec.Cmd {
		args := []string{"-test.run=^TestStreamHelperProcess$", "--"}
		if conv != "" {
			args = append(args, "--conversation", conv)
		}
		cmd := exec.Command(os.Args[0], args...)
		cmd.Env = append(os.Environ(), streamHelperEnv+"=1", streamLogEnv+"="+logPath, streamGateEnv+"="+f.gate)
		cmd.Env = append(cmd.Env, f.env...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		return cmd
	}
	f.h = h
	h.Start(session.Meta{Name: name, Kind: session.KindAgy, Dir: dir})
	t.Cleanup(func() {
		select {
		case <-h.Detach():
		case <-time.After(10 * time.Second):
			t.Errorf("child did not exit on Detach")
		}
	})
	return f
}

// spawns is the --conversation each spawn was given, in order ("" for a fresh one).
func (f *fakeStream) spawns() []string {
	b, _ := os.ReadFile(f.log)
	var out []string
	for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if rest, ok := strings.CutPrefix(ln, "spawn conv="); ok {
			conv, _, _ := strings.Cut(rest, " pid=")
			out = append(out, conv)
		}
	}
	return out
}

// lines is every log line starting with prefix, prefix removed.
func (f *fakeStream) lines(prefix string) []string {
	b, _ := os.ReadFile(f.log)
	var out []string
	for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if rest, ok := strings.CutPrefix(ln, prefix); ok {
			out = append(out, rest)
		}
	}
	return out
}

// waitRead waits until the fake has read the turn content: before that it may still be in its
// TestMain, where a SIGINT kills it without an answer.
func (f *fakeStream) waitRead(t *testing.T, content string) {
	t.Helper()
	waitFor(t, "the fake to read "+content, func() bool {
		for _, l := range f.lines("turn ") {
			if l == content {
				return true
			}
		}
		return false
	})
}

// drainStates is every turn state emitted so far.
func drainStates(h *streamHandle) []agents.TurnState {
	var out []agents.TurnState
	for {
		select {
		case e := <-h.Events():
			out = append(out, e.TurnState)
		default:
			return out
		}
	}
}

// pidGone reports that pid has exited (gone, or a zombie its new parent has not reaped yet).
func pidGone(pid string) bool {
	b, err := os.ReadFile("/proc/" + pid + "/stat")
	if err != nil {
		return true
	}
	_, rest, _ := strings.Cut(string(b), ") ")
	return strings.HasPrefix(rest, "Z")
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
	f.waitRead(t, "slow")
	send(t, f.h, "hello") // waits behind the slow turn
	res, err := f.h.Interrupt(agents.InterruptOpts{})
	if err != nil || res.Stop != agents.StopFirst {
		t.Fatalf("Interrupt = %+v, %v; want a first stop", res, err)
	}
	// The queued turn runs on its own after the stop: no new input is needed.
	waitFor(t, "the queued turn after the stop", func() bool { return len(f.spawns()) == 2 })
	if st := settled(t, f.h); st != agents.TurnCompleted {
		t.Fatalf("queued turn = %s, want completed", st)
	}
	if states := drainStates(f.h); !containsState(states, agents.TurnCancelled) {
		t.Fatalf("states = %v, want the slow turn cancelled", states)
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
	f.waitRead(t, "slow")
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

// gateHook is a test hook that blocks its n-th call until released, and reports when it is
// reached. Other calls pass straight through.
type gateHook struct {
	n       int
	calls   int
	mu      sync.Mutex
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

// waitChan waits for ch to close, failing the test after 20 s.
func waitChan(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func newGateHook(n int) *gateHook {
	return &gateHook{n: n, reached: make(chan struct{}), release: make(chan struct{})}
}

func (g *gateHook) fn() {
	g.mu.Lock()
	g.calls++
	mine := g.calls == g.n
	g.mu.Unlock()
	if mine {
		close(g.reached)
		<-g.release
	}
}

func (g *gateHook) open() { g.once.Do(func() { close(g.release) }) }

func (g *gateHook) wait(t *testing.T, what string) {
	t.Helper()
	select {
	case <-g.reached:
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestPermissionDeniedText(t *testing.T) {
	cases := map[string]bool{
		"permission check failed: user denied permission to run command. Proceed without performing this action.": true,
		"Matches user-configured deny rule.":                                                                  true,
		"permission check failed: Matches user-configured deny rule.":                                         true,
		"permission check failed. Matches user-configured deny rule. Proceed without performing this action.": false, // unmeasured wording: left to the delta
		"open /repo/user denied permission.txt: no such file or directory":                                    false,
		"open /repo/Matches user-configured deny rule.txt: no such file or directory":                         false,
		"stat /x/matches user-configured deny rule.: no such file":                                            false,
		"open /repo/archive Matches user-configured deny rule. notes.txt: no such file or directory":          false,
		"read /repo/a b. Matches user-configured deny rule.":                                                  false,
		"no such file or directory":                                                                           false,
		"permission check failed":                                                                             false,
	}
	for msg, want := range cases {
		if got := permissionDeniedText(msg); got != want {
			t.Errorf("%q: %v, want %v", msg, got, want)
		}
	}
}

// A stop is bound to the child that runs the turn it was aimed at: when that turn answers just
// as the stop comes, the queued turn after it still goes to a new child, and the late SIGINT
// does not reach it.
func TestStreamStopIsBoundToItsChild(t *testing.T) {
	f := newFakeStream(t)
	sig := newGateHook(1)
	f.h.beforeSignal = sig.fn
	t.Cleanup(sig.open)
	send(t, f.h, "gate")
	f.waitRead(t, "gate")
	send(t, f.h, "slowok")
	go func() { _, _ = f.h.Interrupt(agents.InterruptOpts{}) }()
	sig.wait(t, "the stop's signal")
	// The gated turn answers before the signal goes out.
	if err := os.WriteFile(f.gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the gated turn to land", func() bool {
		lt := f.h.LastTurn()
		return lt != nil && lt.Response == "gated"
	})
	time.Sleep(100 * time.Millisecond) // room for a wrong pump to write slowok to the old child
	sig.open()
	if st := settled(t, f.h); st != agents.TurnCompleted {
		t.Fatalf("queued turn = %s, want completed on a new child", st)
	}
	if got := f.h.LastTurn().Response; got != "slow done" {
		t.Fatalf("queued turn answered %q, want it run to the end", got)
	}
	if got := f.spawns(); len(got) != 2 {
		t.Fatalf("spawns = %q, want the queued turn on a second child", got)
	}
}

// A stop that comes after the result is in, before the turn lands, finds nothing to stop: the
// turn lands on its own answer, and the next input is not taken for the ended turn.
func TestStreamStopAfterTheResult(t *testing.T) {
	f := newFakeStream(t)
	land := newGateHook(1)
	f.h.beforeLand = land.fn
	t.Cleanup(land.open)
	send(t, f.h, "fail")
	land.wait(t, "the failed turn's result")
	res, _ := f.h.Interrupt(agents.InterruptOpts{})
	if st := f.h.TurnState(); st == agents.TurnInterrupting {
		t.Fatalf("a stop after the result set %s", st)
	}
	send(t, f.h, "hello")
	land.open()
	if st := settled(t, f.h); st != agents.TurnCompleted {
		t.Fatalf("next turn = %s, want completed", st)
	}
	states := drainStates(f.h)
	if !containsState(states, agents.TurnFailed) || containsState(states, agents.TurnInterrupting) {
		t.Fatalf("states = %v (stop %+v), want the failed turn landed as failed and no interrupting", states, res)
	}
	if got := f.spawns(); len(got) != 1 {
		t.Fatalf("spawns = %q, want no signal and no respawn", got)
	}
}

// A detach while the first turn's child is being started waits for that child and ends it;
// the dropped input never reaches it.
func TestStreamDetachDuringSpawn(t *testing.T) {
	f := newFakeStream(t)
	started := make(chan struct{})
	release := make(chan struct{})
	inner := f.h.command
	f.h.command = func(h *streamHandle, st agents.ThreadSettings, conv string) *exec.Cmd {
		close(started)
		<-release
		return inner(h, st, conv)
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	send(t, f.h, "hello")
	waitChan(t, started, "the spawn")
	detached := f.h.Detach()
	select {
	case <-detached:
		t.Fatal("Detach returned before the child being spawned was ended")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	waitChan(t, detached, "Detach to end the child being spawned")
	if st := settled(t, f.h); st != agents.TurnCancelled {
		t.Fatalf("detached turn = %s, want cancelled", st)
	}
	if got := f.lines("turn "); len(got) != 0 {
		t.Fatalf("the child read %q after the detach", got)
	}
	for _, pid := range f.lines("spawn ") {
		_, p, _ := strings.Cut(pid, "pid=")
		if !pidGone(p) {
			t.Fatalf("child %s is still running", p)
		}
	}
}

// A shutdown while the turn's child is being started ends that child too, and the input stays
// queued rather than being written to it.
func TestStreamDownDuringSpawn(t *testing.T) {
	f := newFakeStream(t)
	started := make(chan struct{})
	release := make(chan struct{})
	inner := f.h.command
	f.h.command = func(h *streamHandle, st agents.ThreadSettings, conv string) *exec.Cmd {
		close(started)
		<-release
		return inner(h, st, conv)
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	send(t, f.h, "hello")
	waitChan(t, started, "the spawn")
	f.h.Down()()
	close(release)
	waitFor(t, "the turn to land", func() bool {
		f.h.mu.Lock()
		defer f.h.mu.Unlock()
		return !f.h.pumping && !f.h.running
	})
	if st := f.h.TurnState(); st != agents.TurnUnknown {
		t.Fatalf("turn cut by the shutdown = %s, want unknown with the input kept", st)
	}
	waitFor(t, "the spawned child to end", func() bool {
		ps := f.lines("spawn ")
		if len(ps) != 1 {
			return false
		}
		_, p, _ := strings.Cut(ps[0], "pid=")
		return pidGone(p)
	})
	if got := f.lines("turn "); len(got) != 0 {
		t.Fatalf("the child read %q after the shutdown", got)
	}
	f.h.mu.Lock()
	kept := f.h.q.Len()
	f.h.mu.Unlock()
	if kept != 1 {
		t.Fatalf("queue after the shutdown = %d, want the input kept", kept)
	}
}

// A detach between the Commit and the write ends the child; the write fails, and the input is
// cancelled, not put back for the next Resume.
func TestStreamDetachAfterCommit(t *testing.T) {
	f := newFakeStream(t)
	w := newGateHook(1)
	f.h.beforeWrite = w.fn
	t.Cleanup(w.open)
	send(t, f.h, "hello")
	w.wait(t, "the write")
	detached := f.h.Detach()
	select {
	case <-detached:
	case <-time.After(10 * time.Second):
		t.Fatal("Detach did not end the child")
	}
	w.open()
	if st := settled(t, f.h); st != agents.TurnCancelled {
		t.Fatalf("detached turn = %s, want cancelled", st)
	}
	f.h.mu.Lock()
	n := f.h.q.Len()
	f.h.mu.Unlock()
	if n != 0 {
		t.Fatalf("queue after the detach = %d, want the input gone", n)
	}
	f.h.Start(session.Meta{Name: f.h.name, Kind: session.KindAgy, Dir: f.h.dir})
	time.Sleep(200 * time.Millisecond)
	if got := f.lines("turn "); len(got) != 0 {
		t.Fatalf("a discarded input ran after Resume: %q", got)
	}
}

// A stop on a committed turn waits for the write and is delivered when the child has the
// prompt; the queue then goes on on a new child.
func TestStreamStopPendingIsDeliveredOnReceipt(t *testing.T) {
	f := newFakeStream(t)
	send(t, f.h, "hello")
	settled(t, f.h)
	conv := f.h.LastTurn().ConversationID
	w := newGateHook(1)
	f.h.beforeWrite = w.fn
	t.Cleanup(w.open)
	send(t, f.h, "slow")
	w.wait(t, "the slow turn's write")
	send(t, f.h, "hello")
	if res, _ := f.h.Interrupt(agents.InterruptOpts{}); res.Stop != agents.StopFirst || res.Discard != nil {
		t.Fatalf("stop on a committed turn = %+v, want a first stop that discards nothing", res)
	}
	w.open()
	waitFor(t, "the queued turn", func() bool { return len(f.spawns()) == 2 })
	if st := settled(t, f.h); st != agents.TurnCompleted {
		t.Fatalf("queued turn = %s, want completed", st)
	}
	if states := drainStates(f.h); !containsState(states, agents.TurnCancelled) {
		t.Fatalf("states = %v, want the slow turn cancelled", states)
	}
	if got := f.spawns(); got[1] != conv {
		t.Fatalf("respawn conversation = %q, want %q", got[1], conv)
	}
	if got := f.lines("turn "); strings.Join(got, ",") != "hello,slow,hello" {
		t.Fatalf("turns read = %q", got)
	}
}

// A stop pending on a write the child never takes ends the child after stopGrace; the turn is
// cancelled and the queue goes on.
func TestStreamStopPendingOnAStuckWrite(t *testing.T) {
	f := newFakeStream(t)
	f.env = []string{streamNoReadEnv + "=1"}
	f.h.stopGrace = 300 * time.Millisecond
	// Larger than any pipe buffer, so the write blocks on a child that does not read.
	send(t, f.h, strings.Repeat("x", 4<<20))
	waitFor(t, "the write to start", func() bool {
		f.h.mu.Lock()
		defer f.h.mu.Unlock()
		return f.h.sending != nil
	})
	send(t, f.h, "hello")
	_, _ = f.h.Interrupt(agents.InterruptOpts{})
	waitFor(t, "the queued turn", func() bool { return len(f.spawns()) == 2 })
	if st := settled(t, f.h); st != agents.TurnCompleted {
		t.Fatalf("queued turn = %s, want completed", st)
	}
	if !f.h.Alive() {
		t.Fatal("a stop on a stuck write took the handle down")
	}
	if states := drainStates(f.h); !containsState(states, agents.TurnCancelled) {
		t.Fatalf("states = %v, want the stuck turn cancelled", states)
	}
}

// A second stop inside the episode discards the queue; nothing runs after it.
func TestStreamSecondStopDiscards(t *testing.T) {
	f := newFakeStream(t)
	sig := newGateHook(1)
	f.h.beforeSignal = sig.fn
	t.Cleanup(sig.open)
	send(t, f.h, "slow")
	f.waitRead(t, "slow")
	send(t, f.h, "hello")
	send(t, f.h, "hello again")
	first := make(chan agents.InterruptResult, 1)
	go func() {
		r, _ := f.h.Interrupt(agents.InterruptOpts{})
		first <- r
	}()
	sig.wait(t, "the first stop's signal")
	res, _ := f.h.Interrupt(agents.InterruptOpts{})
	sig.open()
	if r := <-first; r.Stop != agents.StopFirst {
		t.Fatalf("first stop = %+v", r)
	}
	if res.Stop != agents.StopSecond || res.Discard == nil || len(res.Discard.Items) != 2 {
		t.Fatalf("second stop = %+v, want both queued inputs discarded", res)
	}
	if st := settled(t, f.h); st != agents.TurnCancelled {
		t.Fatalf("slow turn = %s, want cancelled", st)
	}
	time.Sleep(200 * time.Millisecond)
	if got := f.spawns(); len(got) != 1 {
		t.Fatalf("spawns = %q, want no respawn after the queue was discarded", got)
	}
	if got := f.lines("turn "); strings.Join(got, ",") != "slow" {
		t.Fatalf("turns read = %q, want slow alone", got)
	}
}

// DiscardQueue stops the turn and discards the queue in one go.
func TestStreamDiscardQueue(t *testing.T) {
	f := newFakeStream(t)
	send(t, f.h, "slow")
	f.waitRead(t, "slow")
	send(t, f.h, "hello")
	send(t, f.h, "hello again")
	res, _ := f.h.Interrupt(agents.InterruptOpts{DiscardQueue: true})
	if res.Stop != agents.StopDiscard || res.Discard == nil || len(res.Discard.Items) != 2 {
		t.Fatalf("discard = %+v, want both queued inputs", res)
	}
	if st := settled(t, f.h); st != agents.TurnCancelled {
		t.Fatalf("slow turn = %s, want cancelled", st)
	}
	time.Sleep(200 * time.Millisecond)
	if got := f.spawns(); len(got) != 1 {
		t.Fatalf("spawns = %q, want no respawn", got)
	}
}

// A stop before the Commit cancels the taken input: no child is started and nothing is sent.
func TestStreamStopBeforeCommit(t *testing.T) {
	f := newFakeStream(t)
	c := newGateHook(1)
	f.h.beforeCommit = c.fn
	t.Cleanup(c.open)
	send(t, f.h, "hello")
	c.wait(t, "the commit")
	res, _ := f.h.Interrupt(agents.InterruptOpts{})
	c.open()
	if res.Discard == nil || res.Discard.Reason != agents.DiscardFirstStop || len(res.Discard.Items) != 1 {
		t.Fatalf("stop = %+v, want the taken input kept as a first-stop discard", res)
	}
	if st := settled(t, f.h); st != agents.TurnCancelled {
		t.Fatalf("turn = %s, want cancelled", st)
	}
	time.Sleep(300 * time.Millisecond) // a child started anyway would have logged by now
	if got := f.spawns(); len(got) != 0 {
		t.Fatalf("spawns = %q, want none", got)
	}
}

// A child that ignores the SIGINT is terminated after stopGrace; the turn is cancelled and the
// handle stays up.
func TestStreamStubbornChildIsTerminated(t *testing.T) {
	f := newFakeStream(t)
	f.h.stopGrace = 300 * time.Millisecond
	send(t, f.h, "stubborn")
	f.waitRead(t, "stubborn")
	_, _ = f.h.Interrupt(agents.InterruptOpts{})
	if st := settled(t, f.h); st != agents.TurnCancelled {
		t.Fatalf("turn = %s, want cancelled", st)
	}
	if !f.h.Alive() {
		t.Fatal("the stop took the handle down")
	}
	send(t, f.h, "hello")
	if st := settled(t, f.h); st != agents.TurnCompleted {
		t.Fatalf("next turn = %s", st)
	}
}

// What agy leaves in its process group goes with it, even a process that ignores TERM.
func TestStreamChildGroupEndsWithIt(t *testing.T) {
	f := newFakeStream(t)
	send(t, f.h, "grandchild")
	settled(t, f.h)
	gc := f.lines("grand ")
	if len(gc) != 1 {
		t.Fatalf("grandchild log = %q", gc)
	}
	select {
	case <-f.h.Detach():
	case <-time.After(10 * time.Second):
		t.Fatal("Detach did not end the child")
	}
	waitFor(t, "the grandchild to end", func() bool { return pidGone(gc[0]) })
}

// A child that exits on its own after answering is not handed the next prompt it can no longer
// read: the prompt goes to a new child once, and the handle stays up.
func TestStreamPromptToAnExitedChildIsRetried(t *testing.T) {
	f := newFakeStream(t)
	// The second turn is committed to the first child while it is still up; its write then
	// waits until that child has exited.
	calls := 0
	f.h.beforeWrite = func() {
		f.h.mu.Lock()
		calls++
		n, c := calls, f.h.sending
		f.h.mu.Unlock()
		if n == 2 && c != nil {
			<-c.exited
		}
	}
	firstAnswered := make(chan struct{})
	f.h.beforeLand = func() {
		select {
		case <-firstAnswered:
		default:
			close(firstAnswered)
		}
	}
	send(t, f.h, "result-exit")
	waitChan(t, firstAnswered, "the first answer")
	send(t, f.h, "hello")
	if st := settled(t, f.h); st != agents.TurnCompleted {
		t.Fatalf("second turn = %s, want completed on a new child", st)
	}
	if !f.h.Alive() {
		t.Fatal("the handle went down")
	}
	if got := f.spawns(); len(got) != 2 {
		t.Fatalf("spawns = %q, want one retry", got)
	}
}

// A child that exits before it reads anything is a runtime that does not start: one spawn, the
// turn unknown, the input kept, and nothing more until Resume.
func TestStreamChildThatDiesAtStart(t *testing.T) {
	f := newFakeStream(t)
	f.env = []string{streamExitEnv + "=1"}
	// The turn sees its fresh child already gone before it commits.
	f.h.afterEnsure = func() {
		waitFor(t, "the child to exit", func() bool {
			f.h.mu.Lock()
			defer f.h.mu.Unlock()
			return f.h.child == nil || f.h.child.dead
		})
	}
	send(t, f.h, "hello")
	waitFor(t, "the handle to go down", func() bool {
		f.h.mu.Lock()
		defer f.h.mu.Unlock()
		return !f.h.alive && !f.h.pumping && !f.h.running
	})
	if st := f.h.TurnState(); st != agents.TurnUnknown {
		t.Fatalf("turn = %s, want unknown", st)
	}
	time.Sleep(300 * time.Millisecond) // a respawn loop would have logged by now
	if got := f.spawns(); len(got) != 1 {
		t.Fatalf("spawns = %d, want one and no respawn before Resume", len(got))
	}
	f.h.mu.Lock()
	kept := f.h.q.Len()
	f.h.mu.Unlock()
	if kept != 1 {
		t.Fatalf("queue = %d, want the input kept", kept)
	}
	f.env = nil
	f.h.afterEnsure = nil
	f.h.Start(session.Meta{Name: f.h.name, Kind: session.KindAgy, Dir: f.h.dir})
	if st := settled(t, f.h); st != agents.TurnCompleted {
		t.Fatalf("turn after Resume = %s", st)
	}
}

// A fresh child that takes the prompt and goes without a single event never started: the input
// is kept for Resume rather than lost with it.
func TestStreamChildThatDiesSilently(t *testing.T) {
	f := newFakeStream(t)
	f.env = []string{streamSilentEnv + "=1"}
	send(t, f.h, "hello")
	waitFor(t, "the handle to go down", func() bool {
		f.h.mu.Lock()
		defer f.h.mu.Unlock()
		return !f.h.alive && !f.h.pumping && !f.h.running
	})
	f.h.mu.Lock()
	kept := f.h.q.Len()
	f.h.mu.Unlock()
	if kept != 1 {
		t.Fatalf("queue = %d, want the input kept", kept)
	}
	f.env = nil
	f.h.Start(session.Meta{Name: f.h.name, Kind: session.KindAgy, Dir: f.h.dir})
	if st := settled(t, f.h); st != agents.TurnCompleted {
		t.Fatalf("turn after Resume = %s", st)
	}
}

// A turn from before a shutdown does not take down the handle a Resume brought back: its late
// failure to get a child puts its input back and leaves the new generation running.
func TestStreamDownThenResumeKeepsTheNewGeneration(t *testing.T) {
	f := newFakeStream(t)
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	inner := f.h.command
	spawned := 0
	f.h.command = func(h *streamHandle, st agents.ThreadSettings, conv string) *exec.Cmd {
		spawned++
		if spawned == 1 {
			close(started)
			<-release
		}
		return inner(h, st, conv)
	}
	ensured := newGateHook(1)
	f.h.afterEnsure = ensured.fn
	t.Cleanup(ensured.open)
	send(t, f.h, "toolerr")
	waitChan(t, started, "the spawn")
	f.h.Down()()
	close(release)
	ensured.wait(t, "the old turn's failed spawn")
	waitFor(t, "the retired child to be reaped", func() bool {
		f.h.mu.Lock()
		defer f.h.mu.Unlock()
		return len(f.h.kids) == 0
	})
	f.h.Start(session.Meta{Name: f.h.name, Kind: session.KindAgy, Dir: f.h.dir})
	send(t, f.h, "hello")
	ensured.open() // the old turn now lands, its input put back
	if st := settled(t, f.h); st != agents.TurnCompleted {
		t.Fatalf("turns after Resume = %s, want completed", st)
	}
	if !f.h.Alive() {
		t.Fatal("the old turn took the resumed handle down")
	}
	if got := f.lines("turn "); strings.Join(got, ",") != "toolerr,hello" {
		t.Fatalf("turns read = %q, want both inputs run", got)
	}
}

// A second detach waits for a child an earlier detach retired while it was being spawned.
func TestStreamSecondDetachWaitsForARetiringChild(t *testing.T) {
	f := newFakeStream(t)
	f.env = []string{streamLingerEnv + "=1"}
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	inner := f.h.command
	spawned := 0
	f.h.command = func(h *streamHandle, st agents.ThreadSettings, conv string) *exec.Cmd {
		spawned++
		if spawned == 1 {
			close(started)
			<-release
		}
		return inner(h, st, conv)
	}
	send(t, f.h, "hello")
	waitChan(t, started, "the spawn")
	_ = f.h.Detach()
	close(release)
	waitFor(t, "the retired child to start", func() bool { return len(f.lines("spawn ")) == 1 })
	f.h.Start(session.Meta{Name: f.h.name, Kind: session.KindAgy, Dir: f.h.dir})
	second := f.h.Detach()
	waitChan(t, second, "the second Detach")
	_, pid, _ := strings.Cut(f.lines("spawn ")[0], "pid=")
	if !pidGone(pid) {
		t.Fatalf("the second Detach returned while the retired child %s was still running", pid)
	}
}

// A crash a stop comes after stays a crash: the turn is unknown, not cancelled.
func TestStreamCrashThenStopStaysUnknown(t *testing.T) {
	f := newFakeStream(t)
	land := newGateHook(1)
	f.h.beforeLand = land.fn
	t.Cleanup(land.open)
	send(t, f.h, "crash")
	land.wait(t, "the crashed turn")
	_, _ = f.h.Interrupt(agents.InterruptOpts{})
	land.open()
	waitFor(t, "the turn to land", func() bool {
		f.h.mu.Lock()
		defer f.h.mu.Unlock()
		return !f.h.pumping && !f.h.running
	})
	if st := f.h.TurnState(); st != agents.TurnUnknown {
		t.Fatalf("crashed turn = %s, want unknown", st)
	}
	if f.h.Alive() {
		t.Fatal("a crash left the handle up")
	}
}

// A stop on a turn whose result is in opens no stop episode: with two inputs queued, a stop on
// the next turn is a first stop again and the last input still runs.
func TestStreamStopAfterTheResultOpensNoEpisode(t *testing.T) {
	f := newFakeStream(t)
	land := newGateHook(1)
	f.h.beforeLand = land.fn
	t.Cleanup(land.open)
	send(t, f.h, "hello")
	land.wait(t, "the first result")
	send(t, f.h, "slow")
	send(t, f.h, "gate")
	if res, _ := f.h.Interrupt(agents.InterruptOpts{}); res.Discard != nil {
		t.Fatalf("stop after the result = %+v, want nothing discarded", res)
	}
	land.open()
	f.waitRead(t, "slow")
	res, _ := f.h.Interrupt(agents.InterruptOpts{})
	if res.Stop != agents.StopFirst || res.Discard != nil {
		t.Fatalf("stop on the next turn = %+v, want a first stop", res)
	}
	if err := os.WriteFile(f.gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if st := settled(t, f.h); st != agents.TurnCompleted {
		t.Fatalf("last turn = %s, want completed", st)
	}
	if got := f.lines("turn "); strings.Join(got, ",") != "hello,slow,gate" {
		t.Fatalf("turns read = %q", got)
	}
}

// A child that ignores SIGINT and SIGTERM is killed; the turn is cancelled and the queue goes on.
func TestStreamDeafChildIsKilled(t *testing.T) {
	f := newFakeStream(t)
	f.h.stopGrace = 300 * time.Millisecond
	f.h.termGrace = 300 * time.Millisecond
	var first *exec.Cmd
	inner := f.h.command
	f.h.command = func(h *streamHandle, st agents.ThreadSettings, conv string) *exec.Cmd {
		cmd := inner(h, st, conv)
		if first == nil {
			first = cmd
		}
		return cmd
	}
	send(t, f.h, "deaf")
	waitFor(t, "the fake to ignore INT and TERM", func() bool { return len(f.lines("ready deaf")) == 1 })
	send(t, f.h, "hello")
	_, _ = f.h.Interrupt(agents.InterruptOpts{})
	waitFor(t, "the queued turn", func() bool { return len(f.spawns()) == 2 })
	if st := settled(t, f.h); st != agents.TurnCompleted {
		t.Fatalf("queued turn = %s, want completed", st)
	}
	if states := drainStates(f.h); !containsState(states, agents.TurnCancelled) {
		t.Fatalf("states = %v, want the deaf turn cancelled", states)
	}
	ws, ok := first.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("the deaf child ended with %v, want SIGKILL", first.ProcessState)
	}
}

// Where waitid is refused, watch falls back to Wait: a live child is not taken for an exited one
// (and killed), and an exit is still seen.
func TestStreamWaitidRefused(t *testing.T) {
	orig := waitExited
	waitExited = func(int) bool { return false }
	t.Cleanup(func() { waitExited = orig })
	f := newFakeStream(t)
	send(t, f.h, "hello")
	if st := settled(t, f.h); st != agents.TurnCompleted {
		t.Fatalf("turn = %s, want completed on a child watch left alone", st)
	}
	// A stop still reaches the child, and its escalation still ends one that ignores SIGINT.
	f.h.stopGrace = 300 * time.Millisecond
	send(t, f.h, "stubborn")
	f.waitRead(t, "stubborn")
	_, _ = f.h.Interrupt(agents.InterruptOpts{})
	if st := settled(t, f.h); st != agents.TurnCancelled {
		t.Fatalf("stopped turn = %s, want cancelled", st)
	}
	send(t, f.h, "crash")
	waitFor(t, "the crash to land", func() bool { return !f.h.Alive() })
}

// A stop reaches a prompt that is moving to a new child after its write failed: the turn is
// cancelled and the new child never reads it.
func TestStreamStopDuringTheRetry(t *testing.T) {
	f := newFakeStream(t)
	calls := 0
	f.h.beforeWrite = func() {
		f.h.mu.Lock()
		calls++
		n, c := calls, f.h.sending
		f.h.mu.Unlock()
		if n == 2 && c != nil {
			<-c.exited
		}
	}
	respawn := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	inner := f.h.command
	spawned := 0
	f.h.command = func(h *streamHandle, st agents.ThreadSettings, conv string) *exec.Cmd {
		spawned++
		if spawned == 2 {
			close(respawn)
			<-release
		}
		return inner(h, st, conv)
	}
	firstAnswered := make(chan struct{})
	f.h.beforeLand = func() {
		select {
		case <-firstAnswered:
		default:
			close(firstAnswered)
		}
	}
	send(t, f.h, "result-exit")
	waitChan(t, firstAnswered, "the first answer")
	send(t, f.h, "hello")
	waitChan(t, respawn, "the replacement spawn")
	res, _ := f.h.Interrupt(agents.InterruptOpts{})
	if res.Stop != agents.StopFirst {
		t.Fatalf("stop = %+v", res)
	}
	close(release)
	if st := settled(t, f.h); st != agents.TurnCancelled {
		t.Fatalf("retried turn = %s, want cancelled", st)
	}
	time.Sleep(200 * time.Millisecond)
	if got := f.lines("turn "); strings.Join(got, ",") != "result-exit" {
		t.Fatalf("turns read = %q, want the stopped prompt never read", got)
	}
}

// A detach after a fresh child died silently discards the input: a Resume does not run it.
func TestStreamDetachAfterASilentDeath(t *testing.T) {
	f := newFakeStream(t)
	f.env = []string{streamSilentEnv + "=1"}
	land := newGateHook(1)
	f.h.beforeLand = land.fn
	t.Cleanup(land.open)
	send(t, f.h, "hello")
	land.wait(t, "the silent death")
	detached := f.h.Detach()
	land.open()
	waitChan(t, detached, "Detach")
	waitFor(t, "the turn to land", func() bool {
		f.h.mu.Lock()
		defer f.h.mu.Unlock()
		return !f.h.pumping && !f.h.running
	})
	f.env = nil
	f.h.Start(session.Meta{Name: f.h.name, Kind: session.KindAgy, Dir: f.h.dir})
	time.Sleep(300 * time.Millisecond)
	if got := f.lines("turn "); strings.Join(got, ",") != "hello" {
		t.Fatalf("turns read = %q, want the detached input not run again", got)
	}
}

// A detach after a turn's result is in keeps the turn's own outcome.
func TestStreamDetachAfterTheResult(t *testing.T) {
	f := newFakeStream(t)
	land := newGateHook(1)
	f.h.beforeLand = land.fn
	t.Cleanup(land.open)
	send(t, f.h, "fail")
	land.wait(t, "the failed result")
	detached := f.h.Detach()
	land.open()
	waitChan(t, detached, "Detach")
	waitFor(t, "the turn to land", func() bool {
		f.h.mu.Lock()
		defer f.h.mu.Unlock()
		return !f.h.pumping && !f.h.running
	})
	if st := f.h.TurnState(); st != agents.TurnFailed {
		t.Fatalf("turn = %s, want failed as it answered", st)
	}
}

// After a detach during a spawn, a Resume's next turn waits for the retired child to go:
// two agy processes never run for one session at once.
func TestStreamResumeWaitsForARetiringChild(t *testing.T) {
	f := newFakeStream(t)
	f.env = []string{streamLingerEnv + "=1"}
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	inner := f.h.command
	var mu sync.Mutex
	spawned, overlap := 0, ""
	f.h.command = func(h *streamHandle, st agents.ThreadSettings, conv string) *exec.Cmd {
		mu.Lock()
		spawned++
		n := spawned
		mu.Unlock()
		if n == 1 {
			close(started)
			<-release
		} else if ps := f.lines("spawn "); len(ps) > 0 {
			_, pid, _ := strings.Cut(ps[0], "pid=")
			if !pidGone(pid) {
				mu.Lock()
				overlap = pid
				mu.Unlock()
			}
		}
		return inner(h, st, conv)
	}
	send(t, f.h, "hello")
	waitChan(t, started, "the spawn")
	_ = f.h.Detach()
	close(release)
	waitFor(t, "the retired child to start", func() bool { return len(f.lines("spawn ")) == 1 })
	f.h.Start(session.Meta{Name: f.h.name, Kind: session.KindAgy, Dir: f.h.dir})
	send(t, f.h, "hello")
	if st := settled(t, f.h); st != agents.TurnCompleted {
		t.Fatalf("turn after Resume = %s", st)
	}
	mu.Lock()
	defer mu.Unlock()
	if overlap != "" {
		t.Fatalf("a new child was spawned while the retired child %s still ran", overlap)
	}
}

// A stop that comes after the child died but before the prompt's write returned still reaches
// the prompt: it was never delivered, so it is cancelled and not retried on a new child.
func TestStreamStopBeforeAFailedWrite(t *testing.T) {
	f := newFakeStream(t)
	calls := 0
	f.h.beforeWrite = func() {
		f.h.mu.Lock()
		calls++
		n, c := calls, f.h.sending
		f.h.mu.Unlock()
		if n == 2 && c != nil {
			<-c.exited
			if res, _ := f.h.Interrupt(agents.InterruptOpts{}); res.Stop != agents.StopFirst {
				t.Errorf("stop = %+v", res)
			}
		}
	}
	firstAnswered := make(chan struct{})
	f.h.beforeLand = func() {
		select {
		case <-firstAnswered:
		default:
			close(firstAnswered)
		}
	}
	send(t, f.h, "result-exit")
	waitChan(t, firstAnswered, "the first answer")
	send(t, f.h, "hello")
	waitFor(t, "the prompt to land", func() bool {
		f.h.mu.Lock()
		n := calls
		f.h.mu.Unlock()
		return n >= 2 && settledNow(f.h)
	})
	if st := f.h.TurnState(); st != agents.TurnCancelled {
		t.Fatalf("stopped prompt = %s, want cancelled", st)
	}
	time.Sleep(200 * time.Millisecond)
	if got := f.lines("turn "); strings.Join(got, ",") != "result-exit" {
		t.Fatalf("turns read = %q, want the stopped prompt never read", got)
	}
}

func settledNow(h *streamHandle) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.pumping && !h.running && h.q.Len() == 0
}
