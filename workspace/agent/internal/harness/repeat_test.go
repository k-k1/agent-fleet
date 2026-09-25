package harness

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

// countingTool is echoTool (loop_test.go) plus an execution counter, so a test can
// assert not just what message came back but whether the tool actually ran — the
// repeat gate's warn stage is only meaningful if the intercepted call really did NOT
// execute, not just that its result text changed.
func countingTool(name string, n *atomic.Int32) Tool {
	return Tool{
		Def: ToolDef{Name: name},
		Run: func(_ context.Context, _ *Runtime, args string) (string, error) {
			n.Add(1)
			return "ran:" + name + ":" + args, nil
		},
	}
}

func TestCanonicalCallNormalizesArgumentKeyOrderNotJustBytes(t *testing.T) {
	a := ToolCall{Name: "bash", Arguments: `{"command":"go test ./...","cwd":"."}`}
	b := ToolCall{Name: "bash", Arguments: `{"cwd":".","command":"go test ./..."}`}
	if canonicalCall(a) != canonicalCall(b) {
		t.Fatalf("canonicalCall differs for the same arguments in different key order:\n a=%q\n b=%q", canonicalCall(a), canonicalCall(b))
	}
	c := ToolCall{Name: "bash", Arguments: `{"command":"go build ./...","cwd":"."}`}
	if canonicalCall(a) == canonicalCall(c) {
		t.Fatal("canonicalCall treated two different commands as the same call")
	}
	d := ToolCall{Name: "edit", Arguments: `{"command":"go test ./...","cwd":"."}`}
	if canonicalCall(a) == canonicalCall(d) {
		t.Fatal("canonicalCall ignored the tool name and matched on arguments alone")
	}
}

// TestRepeatGateWarnStageInterceptsWithoutExecuting is this gate's core behavior:
// once the same call (name + arguments) has run RepeatWarnAfter times in an unbroken
// row, the NEXT identical call must not reach the tool's Run func at all — the warn
// stage's whole point (task instructions, design point 4a) is that the call is
// answered with a synthetic error instead of actually running again.
func TestRepeatGateWarnStageInterceptsWithoutExecuting(t *testing.T) {
	var executed atomic.Int32
	reg := NewRegistry(countingTool("echo", &executed))
	sameArgs := func(id string) ToolCall { return ToolCall{ID: id, Name: "echo", Arguments: `{"x":1}`} }
	otherArgs := func(id string) ToolCall { return ToolCall{ID: id, Name: "echo", Arguments: `{"x":2}`} }
	client := &scriptedClient{turns: []Turn{
		{ToolCalls: []ToolCall{sameArgs("1")}},  // streak 1: runs
		{ToolCalls: []ToolCall{sameArgs("2")}},  // streak 2: runs
		{ToolCalls: []ToolCall{sameArgs("3")}},  // streak 3 == warnAfter: intercepted
		{ToolCalls: []ToolCall{sameArgs("4")}},  // streak 4: still intercepted
		{ToolCalls: []ToolCall{otherArgs("5")}}, // different args: streak resets, runs
		{Content: "done"},
	}}
	rt := &Runtime{Cwd: t.TempDir(), RepeatWarnAfter: 3, RepeatAbortAfter: 100}
	res, err := Run(context.Background(), client, reg, rt, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := executed.Load(); got != 3 {
		t.Fatalf("tool actually ran %d times, want 3 (calls 1, 2, and 5 — not the two intercepted repeats)", got)
	}
	if res.RepeatWarnings != 2 {
		t.Fatalf("RepeatWarnings = %d, want 2", res.RepeatWarnings)
	}
	// The intercepted calls' RoleTool messages must read as an error the model
	// reacts to, not a normal "ran:..." result.
	var sawWarnFor3, sawWarnFor4 bool
	for _, m := range res.Messages {
		if m.Role != RoleTool {
			continue
		}
		switch m.ToolCallID {
		case "3":
			sawWarnFor3 = true
			if m.Content == "" || m.Content[:6] != "error:" {
				t.Fatalf("call 3's tool message = %q, want it to start with \"error:\"", m.Content)
			}
		case "4":
			sawWarnFor4 = true
		case "1", "2", "5":
			if m.Content[:4] != "ran:" {
				t.Fatalf("call %s's tool message = %q, want a real \"ran:...\" result", m.ToolCallID, m.Content)
			}
		}
	}
	if !sawWarnFor3 || !sawWarnFor4 {
		t.Fatalf("missing a tool message for an intercepted call: sawWarnFor3=%v sawWarnFor4=%v", sawWarnFor3, sawWarnFor4)
	}
	if res.Final.Content != "done" {
		t.Fatalf("Final.Content = %q, want %q (the loop must keep going after a warn, not stop)", res.Final.Content, "done")
	}
}

// TestRepeatGateAbortStageStopsRunWithSentinelError covers design point 4b: past
// RepeatAbortAfter, Run stops instead of feeding the loop back to the model again,
// and the error is distinguishable from any other Run failure via errors.Is.
func TestRepeatGateAbortStageStopsRunWithSentinelError(t *testing.T) {
	var executed atomic.Int32
	reg := NewRegistry(countingTool("echo", &executed))
	sameArgs := func(id string) ToolCall { return ToolCall{ID: id, Name: "echo", Arguments: `{"x":1}`} }
	var turns []Turn
	for i := 1; i <= 6; i++ {
		turns = append(turns, Turn{ToolCalls: []ToolCall{sameArgs(fmt.Sprintf("%d", i))}})
	}
	client := &scriptedClient{turns: turns}
	rt := &Runtime{Cwd: t.TempDir(), RepeatWarnAfter: 2, RepeatAbortAfter: 4}
	res, err := Run(context.Background(), client, reg, rt, nil)
	if !errors.Is(err, ErrRepeatedToolCall) {
		t.Fatalf("err = %v, want an error wrapping ErrRepeatedToolCall", err)
	}
	if got := executed.Load(); got != 1 {
		t.Fatalf("tool actually ran %d times, want 1 (only the first call, before the streak reached warnAfter)", got)
	}
	if res.RepeatWarnings != 2 {
		t.Fatalf("RepeatWarnings = %d, want 2 (streaks 2 and 3 warned before streak 4 aborted)", res.RepeatWarnings)
	}
	// hist must not end on an unanswered tool_calls turn (design point 3): the
	// aborting call gets its own RoleTool message instead of being left hanging,
	// so Result.Messages stays a sendable history.
	last := res.Messages[len(res.Messages)-1]
	if last.Role != RoleTool || last.ToolCallID != "4" {
		t.Fatalf("last message = %+v, want a RoleTool message answering call 4 (the one that tripped the abort)", last)
	}
	if last.Content == "" || last.Content[:6] != "error:" {
		t.Fatalf("aborted call's message = %q, want it to start with \"error:\"", last.Content)
	}
}

// TestRepeatGateAbortAnswersEveryCallInTheAbortingTurn covers the rest of design
// point 3: when several calls sit in the SAME turn and one of them trips the abort
// stage, EVERY call in that turn gets a RoleTool message — not just the one whose own
// streak crossed the threshold — so a turn never ends up partly-answered.
func TestRepeatGateAbortAnswersEveryCallInTheAbortingTurn(t *testing.T) {
	var executed atomic.Int32
	reg := NewRegistry(countingTool("echo", &executed))
	sameArgs := func(id string) ToolCall { return ToolCall{ID: id, Name: "echo", Arguments: `{"x":1}`} }
	client := &scriptedClient{turns: []Turn{
		{ToolCalls: []ToolCall{sameArgs("0")}},                // streak 1: runs
		{ToolCalls: []ToolCall{sameArgs("a"), sameArgs("b")}}, // streak 2 (a), 3 (b): abort trips at "a"
	}}
	rt := &Runtime{Cwd: t.TempDir(), RepeatWarnAfter: 100, RepeatAbortAfter: 2}
	res, err := Run(context.Background(), client, reg, rt, nil)
	if !errors.Is(err, ErrRepeatedToolCall) {
		t.Fatalf("err = %v, want an error wrapping ErrRepeatedToolCall", err)
	}
	if got := executed.Load(); got != 1 {
		t.Fatalf("tool actually ran %d times, want 1 (only call 0, from before the aborting turn)", got)
	}
	byID := map[string]Message{}
	for _, m := range res.Messages {
		if m.Role == RoleTool {
			byID[m.ToolCallID] = m
		}
	}
	a, okA := byID["a"]
	b, okB := byID["b"]
	if !okA || !okB {
		t.Fatalf("missing a RoleTool message for one of the aborting turn's calls: a=%v b=%v", okA, okB)
	}
	if a.Content == "" || a.Content[:6] != "error:" {
		t.Fatalf("call a's message = %q, want it to start with \"error:\"", a.Content)
	}
	if b.Content == "" || b.Content[:6] != "error:" {
		t.Fatalf("call b's message = %q, want it to start with \"error:\" (it must be answered even though it wasn't the call whose streak tripped the abort)", b.Content)
	}
}

// TestRepeatGateDisabledNeverIntervenes covers the opt-out: RepeatGateDisabled must
// leave the loop exactly as it behaved before this gate existed, however long an
// identical streak runs.
func TestRepeatGateDisabledNeverIntervenes(t *testing.T) {
	var executed atomic.Int32
	reg := NewRegistry(countingTool("echo", &executed))
	sameArgs := func(id string) ToolCall { return ToolCall{ID: id, Name: "echo", Arguments: `{"x":1}`} }
	var turns []Turn
	for i := 1; i <= 20; i++ {
		turns = append(turns, Turn{ToolCalls: []ToolCall{sameArgs(fmt.Sprintf("%d", i))}})
	}
	turns = append(turns, Turn{Content: "done"})
	client := &scriptedClient{turns: turns}
	rt := &Runtime{Cwd: t.TempDir(), RepeatGateDisabled: true}
	res, err := Run(context.Background(), client, reg, rt, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := executed.Load(); got != 20 {
		t.Fatalf("tool actually ran %d times, want 20 (RepeatGateDisabled must never intervene)", got)
	}
	if res.RepeatWarnings != 0 || res.RepeatNameWarnings != 0 {
		t.Fatalf("RepeatWarnings = %d, RepeatNameWarnings = %d, want 0 for both", res.RepeatWarnings, res.RepeatNameWarnings)
	}
}

// TestRepeatGateZeroValueRuntimeKeepsDefaultOn is the constraint the driving task
// called out explicitly: a Runtime{} literal that never mentions the repeat-gate
// fields at all must still have the gate ON, the same fail-closed-by-default posture
// as Runtime.Approve's nil check (approval.go) — not silently disabled the way a
// naive "0 means off" reading of the zero value would produce.
func TestRepeatGateZeroValueRuntimeKeepsDefaultOn(t *testing.T) {
	var turns []Turn
	for i := 1; i <= defaultRepeatAbortAfter+2; i++ {
		turns = append(turns, Turn{ToolCalls: []ToolCall{{ID: fmt.Sprintf("%d", i), Name: "echo", Arguments: `{"x":1}`}}})
	}
	client := &scriptedClient{turns: turns}
	reg := NewRegistry(echoTool("echo"))
	rt := &Runtime{Cwd: t.TempDir()} // repeat-gate fields left at their zero value
	_, err := Run(context.Background(), client, reg, rt, nil)
	if !errors.Is(err, ErrRepeatedToolCall) {
		t.Fatalf("err = %v, want ErrRepeatedToolCall — a bare Runtime{} must not leave the gate off", err)
	}
}

// TestRepeatGateWouldHaveCaughtTheLiveCoderIncident replays the shape of the actual
// failure this gate was built for (ADR 0093 lcpp harness trial,
// /home/dev/lcpp-live/log-coder.txt, qwen3-coder-30b-a3b: todo_write's tool NAME
// repeated in an unbroken row from turn 50 through turn 121 — 72 turns straight,
// verified by counting the log's own `names=[...]` column — no other tool call in
// between, session ballooning to 162 assistant turns total). This test does not have
// that run's actual argument JSON (the log only records tool NAMES and the
// assistant's own prose, not the raw tool_call arguments — whether the arguments
// were themselves identical each time is UNVERIFIED), so it stands in a constant
// todo_write payload repeated verbatim as a stand-in for "the arguments genuinely
// never changed", which is the one reading of the log this gate can actually catch
// (see repeat.go's own top comment for the risk that if the real arguments varied,
// this gate would NOT have caught the real incident). With the package DEFAULTS (a
// bare Runtime{}, nothing configured), Run must abort long before anything like 72
// repeats.
func TestRepeatGateWouldHaveCaughtTheLiveCoderIncident(t *testing.T) {
	const sameTodoList = `{"todos":[{"content":"Fix all bugs","status":"completed"},{"content":"Add Max and Min","status":"completed"}]}`
	var turns []Turn
	for i := 1; i <= 20; i++ {
		turns = append(turns, Turn{
			Content:   "## Task Completion Summary\n\n(wording varies turn to turn in the real log; the tool_call underneath did not)",
			ToolCalls: []ToolCall{{ID: fmt.Sprintf("%d", i), Name: "todo_write", Arguments: sameTodoList}},
		})
	}
	client := &scriptedClient{turns: turns}
	reg := NewRegistry(Tool{
		Def: ToolDef{Name: "todo_write"},
		Run: func(_ context.Context, _ *Runtime, _ string) (string, error) { return "ok", nil },
	})
	rt := &Runtime{Cwd: t.TempDir()} // package defaults, exactly as a caller that never heard of this incident would leave it
	_, err := Run(context.Background(), client, reg, rt, nil)
	if !errors.Is(err, ErrRepeatedToolCall) {
		t.Fatalf("err = %v, want ErrRepeatedToolCall well before all 20 scripted repeats", err)
	}
	if client.i > defaultRepeatAbortAfter {
		t.Fatalf("Send was called %d times before Run aborted, want at most %d (defaultRepeatAbortAfter) — the real incident ran to 72 straight repeats before anything noticed", client.i, defaultRepeatAbortAfter)
	}
}

// TestRepeatGateIgnoresTheQwen38NegativeControlSequence replays the TOOL-NAME
// sequence of the comparison run that finished cleanly in 30 turns
// (/home/dev/lcpp-live/log-qwen38.txt, qwen3.8-27b-uncensored-q4_k_m, same task as
// the coder incident above). That log records tool names and turn-level parallel
// groupings but not raw argument JSON, so the arguments below are reconstructed to
// match what each turn's own prose says it was doing (which file was read, which
// shell command ran) — deliberately keeping same-named calls that sit next to each
// other in the flattened call order (turn 2's six parallel reads, turn 9's two
// parallel reads, turn 19's two parallel bash calls) genuinely DIFFERENT in their
// arguments, exactly as the log implies, rather than trivially numbering every call.
// The point of this test is that the gate must not fire on a session that never
// actually repeated a call — including one where the same tool NAME recurs
// constantly (read appears 14 times across this sequence, bash 9 times, edit 8
// times) without ever repeating the same call twice running.
func TestRepeatGateIgnoresTheQwen38NegativeControlSequence(t *testing.T) {
	rc := func(id, name, argsJSON string) ToolCall { return ToolCall{ID: id, Name: name, Arguments: argsJSON} }
	readArg := func(path string) string { return fmt.Sprintf(`{"path":%q}`, path) }
	bashArg := func(cmd string) string { return fmt.Sprintf(`{"command":%q}`, cmd) }
	editArg := func(path, note string) string { return fmt.Sprintf(`{"path":%q,"note":%q}`, path, note) }

	seq := 0
	next := func() string { seq++; return fmt.Sprintf("c%d", seq) }
	turn := func(calls ...ToolCall) Turn { return Turn{ToolCalls: calls} }
	final := Turn{Content: "done"}

	turns := []Turn{
		// task-0
		turn(rc(next(), "glob", `{"pattern":"**/*.go"}`), rc(next(), "ls", `{"path":"."}`)),
		turn(
			rc(next(), "read", readArg("go.mod")),
			rc(next(), "read", readArg("main.go")),
			rc(next(), "read", readArg("mathutil/mathutil.go")),
			rc(next(), "read", readArg("mathutil/mathutil_test.go")),
			rc(next(), "read", readArg("stack/stack.go")),
			rc(next(), "read", readArg("stack/stack_test.go")),
		),
		turn(rc(next(), "bash", bashArg("go build ./..."))),
		turn(rc(next(), "todo_write", `{"todos":[{"content":"4 bugs","status":"pending"}]}`)),
		turn(
			rc(next(), "edit", editArg("main.go", "fix Divide call")),
			rc(next(), "edit", editArg("mathutil/mathutil.go", "fix Divide")),
			rc(next(), "edit", editArg("mathutil/mathutil.go", "fix Average")),
			rc(next(), "edit", editArg("stack/stack.go", "fix Pop")),
		),
		turn(rc(next(), "todo_write", `{"todos":[{"content":"4 bugs","status":"completed"}]}`), rc(next(), "bash", bashArg("go build ./..."))),
		turn(rc(next(), "read", readArg("mathutil/mathutil.go"))),
		turn(rc(next(), "write", `{"path":"mathutil/mathutil.go","content":"..."}`)),
		turn(rc(next(), "read", readArg("main.go")), rc(next(), "read", readArg("stack/stack.go"))),
		turn(rc(next(), "bash", bashArg("go build ./... && go vet ./... && go test ./..."))),
		turn(rc(next(), "bash", bashArg("go run . && gofmt -l ."))),
		turn(rc(next(), "todo_write", `{"todos":[{"content":"4 bugs","status":"completed"},{"content":"verified","status":"completed"}]}`)),
		final,
		// task-1
		turn(rc(next(), "read", readArg("mathutil/mathutil.go")), rc(next(), "ls", `{"path":"mathutil"}`)),
		turn(rc(next(), "read", readArg("mathutil/mathutil_test.go"))),
		turn(rc(next(), "edit", editArg("mathutil/mathutil.go", "add Max/Min")), rc(next(), "edit", editArg("mathutil/mathutil_test.go", "add TestMax/TestMin"))),
		turn(rc(next(), "bash", bashArg("go test ./... -v"))),
		final,
		// task-2
		turn(rc(next(), "bash", bashArg("go vet ./...")), rc(next(), "bash", bashArg("gofmt -l ."))),
		turn(rc(next(), "bash", bashArg("go vet ./... ; echo exit=$?"))),
		turn(rc(next(), "bash", bashArg("find . -name '*.go' | xargs gofmt -l"))),
		final,
		// task-3
		turn(rc(next(), "read", readArg("main.go")), rc(next(), "read", readArg("mathutil/mathutil.go")), rc(next(), "read", readArg("stack/stack.go"))),
		turn(rc(next(), "write", `{"path":"README.md","content":"..."}`)),
		final,
		// task-4
		turn(rc(next(), "read", readArg("mathutil/mathutil.go")), rc(next(), "read", readArg("mathutil/mathutil_test.go"))),
		turn(rc(next(), "edit", editArg("mathutil/mathutil.go", "add Sum")), rc(next(), "edit", editArg("mathutil/mathutil_test.go", "add TestSum"))),
		turn(rc(next(), "bash", bashArg("go build ./... && go test ./... && gofmt -l ."))),
		final,
		// recall (no tool calls at all)
		final,
	}

	client := &scriptedClient{turns: turns}
	reg := NewRegistry(
		Tool{Def: ToolDef{Name: "glob"}, Run: okTool},
		Tool{Def: ToolDef{Name: "ls"}, Run: okTool},
		Tool{Def: ToolDef{Name: "read"}, Run: okTool},
		Tool{Def: ToolDef{Name: "bash"}, Run: okTool},
		Tool{Def: ToolDef{Name: "edit"}, Run: okTool},
		Tool{Def: ToolDef{Name: "write"}, Run: okTool},
		Tool{Def: ToolDef{Name: "todo_write"}, Run: okTool},
	)
	rt := &Runtime{Cwd: t.TempDir()} // package defaults — the same config the positive control above uses
	res, err := Run(context.Background(), client, reg, rt, nil)
	if err != nil {
		t.Fatalf("Run: %v, want no error — this sequence never actually repeats a call", err)
	}
	if res.RepeatWarnings != 0 {
		t.Fatalf("RepeatWarnings = %d, want 0", res.RepeatWarnings)
	}
}

func okTool(_ context.Context, _ *Runtime, args string) (string, error) { return "ok:" + args, nil }

func TestNameStreakTrackerCountsTurnsNotCalls(t *testing.T) {
	call := func(name string) ToolCall { return ToolCall{Name: name} }
	var tr nameStreakTracker
	steps := []struct {
		calls []ToolCall
		want  int
	}{
		{[]ToolCall{call("read"), call("read"), call("read")}, 1}, // parallel reads are one step
		{[]ToolCall{call("read")}, 2},
		{[]ToolCall{call("read"), call("bash")}, 0}, // mixed names break the streak
		{[]ToolCall{call("read")}, 1},
		{[]ToolCall{call("bash")}, 1},
		{[]ToolCall{call("bash")}, 2},
	}
	for i, st := range steps {
		if got := tr.note(st.calls); got != st.want {
			t.Fatalf("step %d: streak = %d, want %d", i, got, st.want)
		}
	}
}

// varyingCall is one tool called with arguments that differ on every turn — the shape
// the exact-call gate cannot see (ADR 0093 debt 2).
func varyingCall(name string, i int) ToolCall {
	return ToolCall{ID: fmt.Sprintf("%s-%d", name, i), Name: name, Arguments: fmt.Sprintf(`{"path":"f.go","offset":%d}`, i*100)}
}

func TestRepeatNameGateWarnStageStillRunsAndPrefixesNotice(t *testing.T) {
	var executed atomic.Int32
	reg := NewRegistry(countingTool("read", &executed))
	var turns []Turn
	for i := 1; i <= 4; i++ {
		turns = append(turns, Turn{ToolCalls: []ToolCall{varyingCall("read", i)}})
	}
	turns = append(turns, Turn{Content: "done"})
	client := &scriptedClient{turns: turns}
	rt := &Runtime{Cwd: t.TempDir(), RepeatNameWarnAfter: 3, RepeatNameAbortAfter: 100}
	res, err := Run(context.Background(), client, reg, rt, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := executed.Load(); got != 4 {
		t.Fatalf("tool actually ran %d times, want 4 — the name warn stage must not suppress calls", got)
	}
	if res.RepeatNameWarnings != 2 || res.RepeatWarnings != 0 {
		t.Fatalf("RepeatNameWarnings = %d, RepeatWarnings = %d, want 2 and 0", res.RepeatNameWarnings, res.RepeatWarnings)
	}
	for _, m := range res.Messages {
		if m.Role != RoleTool {
			continue
		}
		warned := m.ToolCallID == "read-3" || m.ToolCallID == "read-4"
		if got := strings.HasPrefix(m.Content, "note: read has now been the only tool called"); got != warned {
			t.Fatalf("call %s: content %q, notice present = %v, want %v", m.ToolCallID, m.Content, got, warned)
		}
		if !strings.Contains(m.Content, "ran:read:") {
			t.Fatalf("call %s: content %q lost the real result", m.ToolCallID, m.Content)
		}
	}
}

// TestRepeatNameGateCatchesTheIncidentWithVaryingArguments is the positive control for
// ADR 0093 debt 2: the 72-turn todo_write run, replayed with arguments that change
// every turn, must be stopped by the package defaults.
func TestRepeatNameGateCatchesTheIncidentWithVaryingArguments(t *testing.T) {
	var executed atomic.Int32
	reg := NewRegistry(countingTool("todo_write", &executed))
	var turns []Turn
	for i := 1; i <= 72; i++ {
		turns = append(turns, Turn{ToolCalls: []ToolCall{{
			ID:        fmt.Sprintf("%d", i),
			Name:      "todo_write",
			Arguments: fmt.Sprintf(`{"todos":[{"content":"Fix all bugs (pass %d)","status":"completed"}]}`, i),
		}}})
	}
	client := &scriptedClient{turns: turns}
	rt := &Runtime{Cwd: t.TempDir()}
	res, err := Run(context.Background(), client, reg, rt, nil)
	if !errors.Is(err, ErrRepeatedToolCall) {
		t.Fatalf("err = %v, want ErrRepeatedToolCall", err)
	}
	if client.i != defaultRepeatNameAbortAfter {
		t.Fatalf("Send was called %d times before Run aborted, want %d (defaultRepeatNameAbortAfter)", client.i, defaultRepeatNameAbortAfter)
	}
	if got := executed.Load(); got != defaultRepeatNameAbortAfter-1 {
		t.Fatalf("tool actually ran %d times, want %d (every turn before the aborting one)", got, defaultRepeatNameAbortAfter-1)
	}
	if want := defaultRepeatNameAbortAfter - defaultRepeatNameWarnAfter; res.RepeatNameWarnings != want {
		t.Fatalf("RepeatNameWarnings = %d, want %d", res.RepeatNameWarnings, want)
	}
	last := res.Messages[len(res.Messages)-1]
	if last.Role != RoleTool || last.ToolCallID != fmt.Sprintf("%d", defaultRepeatNameAbortAfter) || !strings.HasPrefix(last.Content, "error:") {
		t.Fatalf("last message = %+v, want an error answering the aborting call so the history stays sendable", last)
	}
}

// TestRepeatNameGateIgnoresTheLongestPassingRuns is the negative control: the longest
// same-name runs in the passing live sessions (6 turns of bash, 6 of read, each with
// different arguments) and a run just short of the warn default must go untouched.
func TestRepeatNameGateIgnoresTheLongestPassingRuns(t *testing.T) {
	var turns []Turn
	for i := 1; i <= 6; i++ {
		turns = append(turns, Turn{ToolCalls: []ToolCall{{ID: fmt.Sprintf("b%d", i), Name: "bash", Arguments: fmt.Sprintf(`{"command":"go test ./pkg%d"}`, i)}}})
	}
	turns = append(turns, Turn{ToolCalls: []ToolCall{{ID: "e", Name: "edit", Arguments: `{"path":"a.go"}`}}})
	for i := 1; i < defaultRepeatNameWarnAfter; i++ {
		turns = append(turns, Turn{ToolCalls: []ToolCall{varyingCall("read", i)}})
	}
	turns = append(turns, Turn{Content: "done"})
	client := &scriptedClient{turns: turns}
	reg := NewRegistry(
		Tool{Def: ToolDef{Name: "bash"}, Run: okTool},
		Tool{Def: ToolDef{Name: "edit"}, Run: okTool},
		Tool{Def: ToolDef{Name: "read"}, Run: okTool},
	)
	res, err := Run(context.Background(), client, reg, &Runtime{Cwd: t.TempDir()}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.RepeatNameWarnings != 0 || res.RepeatWarnings != 0 {
		t.Fatalf("RepeatNameWarnings = %d, RepeatWarnings = %d, want 0 for both", res.RepeatNameWarnings, res.RepeatWarnings)
	}
}
