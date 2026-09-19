package mcpc

// This is the "実サーバ1つ以上" evidence ADR 0093 segment F's brief asks for: a real,
// already-existing MCP server (`workspace-agent mcp-stdio --self-report`, the exact
// binary/args every interactive session already launches af through — see cli.go's
// "mcp-stdio" subcommand and mcpreg/builtin.go's BuiltinAF spec) driven end to end
// through initialize → tools/list → tools/call by THIS package's stdio client, over
// the 2026-07-28 stateless era (dispatchMCPStdio answers server/discover — see
// mcp_stdio.go:257).
//
// Why af specifically, and why stdio: ADR 0093 decision 6 / this segment's spawn brief
// concluded af cannot be called in-process (dispatchMCPStdio depends on process-wide
// state — owning session, stdout writer, watchers) and that internal/mcpx must not be
// gain new exported entry points for this segment. The registry (mcpreg/builtin.go)
// already materializes af as an ordinary stdio ServerDef — Command: the agent binary,
// Args: ["mcp-stdio", "--self-report", ...] — exactly like every other kind's config
// points at it. So this test spawns it exactly that way, through mcpc's own generic
// stdio transport, with no af-specific code anywhere in this package.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

var buildAgentBinOnce struct {
	sync.Once
	path string
	err  error
}

// buildAgentBin compiles the real workspace-agent binary once per test run (~tens of
// seconds — mirrors mcp_handoff_e2e_test.go's buildAgentBinary, which pays the same
// cost for the same reason: a fake standing in for `dispatchMCPStdio` would not have
// caught the process-boundary bugs that function's own history is full of).
func buildAgentBin(t *testing.T) string {
	t.Helper()
	buildAgentBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mcpc-af-live-bin-")
		if err != nil {
			buildAgentBinOnce.err = err
			return
		}
		bin := filepath.Join(dir, "workspace-agent")
		build := exec.Command("go", "build", "-o", bin, ".")
		build.Dir = ".." + string(os.PathSeparator) + ".." // internal/mcpc -> workspace/agent
		build.Env = os.Environ()
		if out, err := build.CombinedOutput(); err != nil {
			buildAgentBinOnce.err = err
			buildAgentBinOnce.path = string(out)
			return
		}
		buildAgentBinOnce.path = bin
	})
	if buildAgentBinOnce.err != nil {
		t.Fatalf("build workspace-agent: %v\n%s", buildAgentBinOnce.err, buildAgentBinOnce.path)
	}
	return buildAgentBinOnce.path
}

func TestAFLive_Stdio_ModernEra_InitializeListCallReport(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles the real workspace-agent binary — skipped under -short")
	}
	bin := buildAgentBin(t)

	var gotReport map[string]string
	agentStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat/report" {
			_ = json.NewDecoder(r.Body).Decode(&gotReport)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer agentStub.Close()

	home := t.TempDir()
	def := mcpreg.ServerDef{
		Name:      "af_live",
		Transport: mcpreg.TransportStdio,
		Command:   bin,
		Args:      []string{"mcp-stdio", "--self-report"},
		Env: map[string]string{
			"HOME":            home,
			"AF_SESSION_NAME": "mcpc-live-test",
			"AF_SESSIONS_DIR": filepath.Join(home, "sessions"),
			"AGENT_ADDR":      agentStub.Listener.Addr().String(),
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := Connect(ctx, def)
	if err != nil {
		t.Fatalf("Connect to the real af mcp-stdio server: %v", err)
	}
	defer func() { _ = s.Close() }()

	defs := s.ToolDefs()
	if len(defs) == 0 {
		t.Fatal("real af server's tools/list came back empty")
	}
	var haveReport bool
	for _, d := range defs {
		if d.Name == PrefixToolName("af_live", "af_report") {
			haveReport = true
		}
	}
	if !haveReport {
		t.Fatalf("--self-report should advertise af_report; got %d tools", len(defs))
	}

	args, _ := json.Marshal(map[string]any{"session": "mcpc-live-test"})
	text, isErr, err := s.CallTool(context.Background(), "af_report", args)
	if err != nil {
		t.Fatalf("tools/call af_report against the real server: %v", err)
	}
	if isErr {
		t.Fatalf("af_report answered isError=true: %s", text)
	}
	if gotReport["name"] != "mcpc-live-test" {
		t.Fatalf("the real af server never reached AGENT_ADDR's /chat/report (or with the wrong body): %+v", gotReport)
	}
}
