package harness

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// scriptedClient answers Send with the next Turn in turns, in order, recording
// every request it was given (tools always included, so tests can assert plan
// mode actually strips the tool list).
type scriptedClient struct {
	turns    []Turn
	i        int
	gotTools [][]ToolDef
	gotMsgs  [][]Message
}

func (c *scriptedClient) Send(_ context.Context, messages []Message, tools []ToolDef) (Turn, error) {
	c.gotMsgs = append(c.gotMsgs, append([]Message(nil), messages...))
	c.gotTools = append(c.gotTools, tools)
	if c.i >= len(c.turns) {
		return Turn{}, errors.New("scriptedClient: ran out of turns")
	}
	t := c.turns[c.i]
	c.i++
	return t, nil
}

func (c *scriptedClient) InputTokens(context.Context, []Message, []ToolDef) (int, error) {
	return 0, nil
}

func echoTool(name string) Tool {
	return Tool{
		Def: ToolDef{Name: name},
		Run: func(_ context.Context, _ *Runtime, args string) (string, error) {
			return "ran:" + name + ":" + args, nil
		},
	}
}

func TestRunStopsWhenNoToolCalls(t *testing.T) {
	client := &scriptedClient{turns: []Turn{{Content: "hello"}}}
	reg := NewRegistry()
	rt := &Runtime{Cwd: t.TempDir()}
	res, err := Run(context.Background(), client, reg, rt, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Final.Content != "hello" {
		t.Fatalf("Final.Content = %q", res.Final.Content)
	}
	if client.i != 1 {
		t.Fatalf("Send called %d times, want 1", client.i)
	}
}

func TestRunExecutesToolCallsAndLoopsBack(t *testing.T) {
	client := &scriptedClient{turns: []Turn{
		{ToolCalls: []ToolCall{{ID: "1", Name: "echo", Arguments: `{"x":1}`}}},
		{Content: "done"},
	}}
	reg := NewRegistry(echoTool("echo"))
	rt := &Runtime{Cwd: t.TempDir()}
	res, err := Run(context.Background(), client, reg, rt, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Final.Content != "done" {
		t.Fatalf("Final.Content = %q", res.Final.Content)
	}
	// history: assistant(tool_calls) -> tool(result) -> assistant(done)
	if len(res.Messages) != 3 {
		t.Fatalf("len(Messages) = %d, want 3: %+v", len(res.Messages), res.Messages)
	}
	toolMsg := res.Messages[1]
	if toolMsg.Role != RoleTool || toolMsg.ToolCallID != "1" {
		t.Fatalf("tool message = %+v", toolMsg)
	}
	if toolMsg.Content != `ran:echo:{"x":1}` {
		t.Fatalf("tool message content = %q", toolMsg.Content)
	}
}

func TestRunExecutesParallelToolCallsPreservingOrder(t *testing.T) {
	client := &scriptedClient{turns: []Turn{
		{ToolCalls: []ToolCall{
			{ID: "a", Name: "echo", Arguments: "A"},
			{ID: "b", Name: "echo", Arguments: "B"},
			{ID: "c", Name: "echo", Arguments: "C"},
		}},
		{Content: "done"},
	}}
	reg := NewRegistry(echoTool("echo"))
	rt := &Runtime{Cwd: t.TempDir()}
	res, err := Run(context.Background(), client, reg, rt, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	toolMsgs := res.Messages[1:4]
	wantIDs := []string{"a", "b", "c"}
	for i, m := range toolMsgs {
		if m.ToolCallID != wantIDs[i] {
			t.Fatalf("toolMsgs[%d].ToolCallID = %q, want %q (order not preserved)", i, m.ToolCallID, wantIDs[i])
		}
	}
}

func TestRunUnknownToolReportsErrorWithoutFailingLoop(t *testing.T) {
	client := &scriptedClient{turns: []Turn{
		{ToolCalls: []ToolCall{{ID: "1", Name: "nope"}}},
		{Content: "done"},
	}}
	reg := NewRegistry()
	rt := &Runtime{Cwd: t.TempDir()}
	res, err := Run(context.Background(), client, reg, rt, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Messages[1].Content == "" {
		t.Fatal("expected an error message for the unknown tool")
	}
}

func TestRunPlanModeStripsMutatingToolsFromAdvertisedList(t *testing.T) {
	client := &scriptedClient{turns: []Turn{{Content: "ok"}}}
	reg := NewRegistry(
		Tool{Def: ToolDef{Name: "read"}},
		Tool{Def: ToolDef{Name: "write"}, Mutates: true},
	)
	rt := &Runtime{Cwd: t.TempDir(), Plan: true}
	if _, err := Run(context.Background(), client, reg, rt, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := client.gotTools[0]
	if len(got) != 1 || got[0].Name != "read" {
		t.Fatalf("plan mode tool defs = %+v, want only [read]", got)
	}
}

func TestRunPlanModeRefusesMutatingCallEvenIfModelAsksAnyway(t *testing.T) {
	var wrote atomic.Bool
	mutTool := Tool{
		Def:     ToolDef{Name: "write"},
		Mutates: true,
		Run: func(_ context.Context, _ *Runtime, _ string) (string, error) {
			wrote.Store(true)
			return "wrote", nil
		},
	}
	client := &scriptedClient{turns: []Turn{
		{ToolCalls: []ToolCall{{ID: "1", Name: "write"}}}, // model calls it despite plan mode
		{Content: "done"},
	}}
	reg := NewRegistry(mutTool)
	rt := &Runtime{Cwd: t.TempDir(), Plan: true}
	if _, err := Run(context.Background(), client, reg, rt, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if wrote.Load() {
		t.Fatal("plan mode did not stop a mutating tool from actually running")
	}
}

func TestRunCancelStopsTheLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &blockingThenCancelClient{cancel: cancel}
	reg := NewRegistry()
	rt := &Runtime{Cwd: t.TempDir()}
	_, err := Run(ctx, client, reg, rt, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// blockingThenCancelClient's first Send cancels the context it was given (as if
// something external cancelled the turn) and blocks briefly so the cancellation
// is observed before returning, then returns a tool call the loop must never
// reach executing.
type blockingThenCancelClient struct {
	cancel context.CancelFunc
}

func (c *blockingThenCancelClient) Send(ctx context.Context, _ []Message, _ []ToolDef) (Turn, error) {
	c.cancel()
	time.Sleep(10 * time.Millisecond)
	return Turn{ToolCalls: []ToolCall{{ID: "1", Name: "echo"}}}, nil
}

func (c *blockingThenCancelClient) InputTokens(context.Context, []Message, []ToolDef) (int, error) {
	return 0, nil
}

func TestRegistryDefsIsSortedAndDeterministic(t *testing.T) {
	reg := NewRegistry(
		Tool{Def: ToolDef{Name: "zeta"}},
		Tool{Def: ToolDef{Name: "alpha"}},
	)
	defs := reg.Defs(false)
	if len(defs) != 2 || defs[0].Name != "alpha" || defs[1].Name != "zeta" {
		t.Fatalf("Defs() = %+v, want sorted [alpha zeta]", defs)
	}
}

func TestDecodeArgsEmptyIsNotAnError(t *testing.T) {
	var v struct {
		X int `json:"x"`
	}
	if err := decodeArgs("", &v); err != nil {
		t.Fatalf("decodeArgs(\"\"): %v", err)
	}
	if err := decodeArgs(`{"x":5}`, &v); err != nil || v.X != 5 {
		t.Fatalf("decodeArgs: v=%+v err=%v", v, err)
	}
}
