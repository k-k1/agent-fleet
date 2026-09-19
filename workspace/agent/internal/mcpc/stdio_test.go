package mcpc

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

// fakeStdioDef points at THIS test binary re-exec'd as the helper process (see
// helper_process_test.go), speaking the given era ("modern" or "legacy") or misbehaving
// ("hang").
func fakeStdioDef(t *testing.T, name, mode string) mcpreg.ServerDef {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return mcpreg.ServerDef{
		Name:      name,
		Transport: mcpreg.TransportStdio,
		Command:   bin,
		Args:      []string{"-test.run=TestHelperProcess"},
		Env: map[string]string{
			helperEnvFlag: "1",
			helperEnvMode: mode,
		},
	}
}

func connectFake(t *testing.T, mode string) *Server {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Connect(ctx, fakeStdioDef(t, "fake", mode))
	if err != nil {
		t.Fatalf("Connect(%s): %v", mode, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStdioModernEra_ListAndCall(t *testing.T) {
	s := connectFake(t, "modern")
	defs := s.ToolDefs()
	if len(defs) != 3 {
		t.Fatalf("ToolDefs = %d entries, want 3 (echo/boom/bump): %+v", len(defs), defs)
	}
	wantName := PrefixToolName("fake", "echo")
	var found bool
	for _, d := range defs {
		if d.Name == wantName {
			found = true
			if len(d.Parameters) == 0 {
				t.Fatalf("echo's Parameters (inputSchema) came through empty")
			}
		}
	}
	if !found {
		t.Fatalf("ToolDefs missing %q: %+v", wantName, defs)
	}

	args, _ := json.Marshal(map[string]any{"msg": "hi"})
	text, isErr, err := s.CallTool(context.Background(), "echo", args)
	if err != nil {
		t.Fatalf("CallTool(echo): %v", err)
	}
	if isErr || text != "hi" {
		t.Fatalf("CallTool(echo) = (%q, isError=%v), want (\"hi\", false)", text, isErr)
	}
}

func TestStdioLegacyEra_ListAndCall(t *testing.T) {
	s := connectFake(t, "legacy")
	defs := s.ToolDefs()
	if len(defs) != 3 {
		t.Fatalf("ToolDefs = %d entries, want 3: %+v", len(defs), defs)
	}
	args, _ := json.Marshal(map[string]any{"msg": "legacy-ok"})
	text, isErr, err := s.CallTool(context.Background(), "echo", args)
	if err != nil || isErr || text != "legacy-ok" {
		t.Fatalf("CallTool(echo) = (%q, %v, %v), want (\"legacy-ok\", false, nil)", text, isErr, err)
	}
}

func TestStdioToolCall_InBandErrorIsNotAGoError(t *testing.T) {
	s := connectFake(t, "modern")
	text, isErr, err := s.CallTool(context.Background(), "boom", nil)
	if err != nil {
		t.Fatalf("CallTool(boom) returned a transport error: %v", err)
	}
	if !isErr {
		t.Fatalf("CallTool(boom) isError=false, want true (the fake always fails this tool)")
	}
	if text != "boom happened" {
		t.Fatalf("CallTool(boom) text = %q", text)
	}
}

func TestStdioCallTool_RefusesUnadvertisedName(t *testing.T) {
	s := connectFake(t, "modern")
	if _, _, err := s.CallTool(context.Background(), "does_not_exist", nil); err == nil {
		t.Fatal("CallTool of an unlisted name should be refused client-side, not sent to the server")
	}
}

func TestStdioListChanged_RefreshesToolCache(t *testing.T) {
	s := connectFake(t, "modern")
	if _, _, err := s.CallTool(context.Background(), "bump", nil); err != nil {
		t.Fatalf("CallTool(bump): %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		found := false
		for _, d := range s.ToolDefs() {
			if d.Name == PrefixToolName("fake", "extra") {
				found = true
			}
		}
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("tools/list_changed notification never refreshed the cache with the new tool")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestStdioClose_KillsAHangingChild is the positive control ADR 0093's spawn brief asks
// for: a stdio child that ignores stdin closing must still be reaped by Close within
// the kill-grace window, or this test times out (proven red by shrinking the grace to
// well under the child's own wait and then, separately, by literally reverting the
// Kill() escalation during development — see the report_back for how that was checked).
func TestStdioClose_KillsAHangingChild(t *testing.T) {
	old := stdioKillGrace
	stdioKillGrace = 200 * time.Millisecond
	defer func() { stdioKillGrace = old }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Connect(ctx, fakeStdioDef(t, "fake", "hang"))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close() = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close() did not return within 5s — the hanging child was never killed (leaked process)")
	}
}

func TestConnect_UnsupportedTransport(t *testing.T) {
	_, err := Connect(context.Background(), mcpreg.ServerDef{Name: "x", Transport: "carrier-pigeon"})
	if err == nil {
		t.Fatal("Connect with an unsupported transport should fail")
	}
}
