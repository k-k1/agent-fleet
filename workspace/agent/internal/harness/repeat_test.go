package harness

import (
	"context"
	"errors"
	"fmt"
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
		{ToolCalls: []ToolCall{sameArgs("1")}}, // streak 1: runs
		{ToolCalls: []ToolCall{sameArgs("2")}}, // streak 2: runs
		{ToolCalls: []ToolCall{sameArgs("3")}}, // streak 3 == warnAfter: intercepted
		{ToolCalls: []ToolCall{sameArgs("4")}}, // streak 4: still intercepted
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
	if res.RepeatWarnings != 0 {
		t.Fatalf("RepeatWarnings = %d, want 0", res.RepeatWarnings)
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

// TestRepeatGateWouldHaveCaughtTheLiveColderIncident replays the shape of the actual
// failure this gate was built for (ADR 0093 lcpp harness trial,
// /home/dev/lcpp-live/log-coder.txt, qwen3-coder-30b-a3b: todo_write called with the
// same arguments in an unbroken row from turn 58 through turn 122 — 65 turns, no
// other tool call in between, session ballooning to 162 assistant turns total). This
// test does not have that run's actual argument JSON (the log only records tool
// NAMES and the assistant's own prose, not the raw tool_call arguments), so it
// stands in a plausible, constant todo_write payload repeated verbatim — the
// incident's own defining property, regardless of the exact JSON. With the package
// DEFAULTS (a bare Runtime{}, nothing configured), Run must abort long before
// anything like 65 repeats.
func TestRepeatGateWouldHaveCaughtTheLiveColderIncident(t *testing.T) {
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
		t.Fatalf("Send was called %d times before Run aborted, want at most %d (defaultRepeatAbortAfter) — the real incident ran to 65 straight repeats before anything noticed", client.i, defaultRepeatAbortAfter)
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
