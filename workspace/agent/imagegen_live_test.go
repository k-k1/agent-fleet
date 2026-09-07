//go:build clicontract

// The one check the unit tests cannot make: drive image generation the way a session really
// does — a separate `workspace-agent mcp-stdio` process speaking MCP over a pipe, the real
// route table behind it, the real Codex CLI, and a real picture at the end (ADR 0069).
//
//	AF_IMAGEGEN_LIVE=1 go test -tags clicontract -run TestImagegenLive -timeout 15m .
//
// It is gated and never runs in CI for two reasons. **It spends the user's ChatGPT plan
// quota** — one image, which the plan burns 3-5x faster than a text turn — and it needs a
// Codex login, which a runner does not have.
//
// Nothing here touches the running Agent or the user's real state. HOME, CODEX_HOME and the
// usage ledger all point into a temp directory, the routes are served by an httptest server
// built from buildMux() rather than by starting main() (whose boot writes every CLI's config,
// starts reconcilers and would collide with the live Agent), and the only thing borrowed from
// the real home is a symlink to the Codex login — read, never written through.
package main

import (
	"bufio"
	"encoding/json"
	"image"
	_ "image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func requireImagegenLive(t *testing.T) {
	t.Helper()
	if os.Getenv("AF_IMAGEGEN_LIVE") != "1" {
		t.Skip("AF_IMAGEGEN_LIVE!=1 — the live image-generation tier spends real plan quota")
	}
}

// imagegenSandbox points every path the Agent and the provider read at a temp tree, and
// returns the sandbox home. The Codex login is the one exception: it is symlinked from the
// real home, because there is no other way to be logged in, and the run only reads it.
func imagegenSandbox(t *testing.T, kind string) string {
	t.Helper()
	return imagegenSandboxOrdered(t, kind, nil, true)
}

// imagegenSandboxOrdered is imagegenSandbox with an explicit provider order written into
// ui-prefs, and with the agy login optionally withheld so a test can drive the case where only
// one provider is ready at all.
func imagegenSandboxOrdered(t *testing.T, kind string, order []string, withAgy bool) string {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	codexHome := filepath.Join(root, "codex")
	for _, d := range []string{home, codexHome} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	realCodexAuth := filepath.Join(paths.HomeDir(), ".codex", "auth.json")
	if _, err := os.Stat(realCodexAuth); err != nil {
		t.Skipf("no Codex login to borrow (%v)", err)
	}
	if err := os.Symlink(realCodexAuth, filepath.Join(codexHome, "auth.json")); err != nil {
		t.Fatal(err)
	}
	// The agy login, borrowed the same way and for the same reason: the provider resolves the
	// token from HOME, so the sandbox needs one there or agy is simply not ready. Absent is not
	// a failure — only the agy test skips on it.
	realAgyToken := filepath.Join(paths.HomeDir(), ".gemini", "antigravity-cli", "antigravity-oauth-token")
	if _, err := os.Stat(realAgyToken); err == nil && withAgy {
		agyDir := filepath.Join(home, ".gemini", "antigravity-cli")
		if err := os.MkdirAll(agyDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(realAgyToken, filepath.Join(agyDir, "antigravity-oauth-token")); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	t.Setenv("AF_USAGE_DIR", filepath.Join(root, "usage"))
	t.Setenv("AF_BROWSE_ROOT", home)

	// The opt-in gate, as the Console writes it.
	prefs := filepath.Join(home, ".config", "agent-fleet")
	if err := os.MkdirAll(prefs, 0o700); err != nil {
		t.Fatal(err)
	}
	uiPrefs := map[string]any{"imageGeneration": true}
	if len(order) > 0 {
		uiPrefs["imageProviderOrder"] = order
	}
	prefsJSON, err := json.Marshal(uiPrefs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefs, "ui-prefs.json"), prefsJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	// A session for the tool to belong to: its kind is what the tools/list rule turns on.
	session.WriteMeta(session.Meta{Name: "slot01", Kind: kind, Dir: home})
	return home
}

// mcpPipe is one `workspace-agent mcp-stdio` child, spoken to the way a CLI speaks to it.
type mcpPipe struct {
	cmd *exec.Cmd
	in  *os.File
	out *bufio.Reader
}

func startMCPChild(t *testing.T, bin string, args ...string) *mcpPipe {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, os.Stderr
	cmd.Env = append(os.Environ(), "AF_SESSION_NAME=slot01")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	inR.Close()
	outW.Close()
	t.Cleanup(func() {
		inW.Close()
		// Only this child, by handle — never by pattern; the container is shared.
		_ = cmd.Wait()
		outR.Close()
	})
	return &mcpPipe{cmd: cmd, in: inW, out: bufio.NewReaderSize(outR, 1<<20)}
}

func (p *mcpPipe) send(t *testing.T, req map[string]any) {
	t.Helper()
	b, _ := json.Marshal(req)
	if _, err := p.in.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

// awaitResult reads until the response to id arrives, returning it plus every
// notifications/progress seen on the way.
func (p *mcpPipe) awaitResult(t *testing.T, id float64) (result map[string]any, progress []map[string]any) {
	t.Helper()
	for {
		line, err := p.out.ReadBytes('\n')
		if err != nil {
			t.Fatalf("mcp child closed its pipe before answering %v: %v", id, err)
		}
		var msg map[string]any
		if json.Unmarshal(line, &msg) != nil {
			continue
		}
		if msg["method"] == "notifications/progress" {
			params, _ := msg["params"].(map[string]any)
			progress = append(progress, params)
			continue
		}
		if got, ok := msg["id"].(float64); ok && got == id {
			res, _ := msg["result"].(map[string]any)
			if res == nil {
				t.Fatalf("id %v answered with no result: %s", id, line)
			}
			return res, progress
		}
	}
}

// serveAgentRoutes stands up the real route table without booting main(). buildMux exists
// precisely so a test can do this (routes_golden_test.go relies on the same thing).
func serveAgentRoutes(t *testing.T) {
	t.Helper()
	t.Setenv("AGENT_TOKEN", "live-imagegen-token")
	srv := httptest.NewServer(httpx.RequireToken(buildMux()))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_ADDR", u.Host)
}

// TestImagegenLiveEndToEnd is the whole path: the session-side MCP server asks the Agent
// whether to offer the tool, offers it, the model's call reaches the Codex provider, a real
// image comes back, and the file lands where the Console can open it.
func TestImagegenLiveEndToEnd(t *testing.T) {
	requireImagegenLive(t)
	// Built BEFORE the sandbox takes over HOME: `go build` resolves the module cache from
	// HOME, and under the temp one it re-downloads the whole tree into a directory the
	// cleanup then cannot remove (read-only module files). Measured, once.
	bin := buildAgentBinary(t)
	// codex-first, explicitly: the built-in default now puts agy in front, and this test is
	// about the codex route.
	home := imagegenSandboxOrdered(t, session.KindClaude, []string{"codex", "agy"}, true)
	serveAgentRoutes(t)

	// The Agent's own answer first: nothing else works if this is wrong.
	statusBody := agentGETLive(t, "/imagegen/status?session=slot01")
	var st struct {
		Enabled  bool     `json:"enabled"`
		Ready    bool     `json:"ready"`
		Provider string   `json:"provider"`
		Kind     string   `json:"kind"`
		Ops      []string `json:"ops"`
	}
	if json.Unmarshal(statusBody, &st) != nil {
		t.Fatalf("status is not JSON: %s", statusBody)
	}
	if !st.Enabled || !st.Ready || st.Provider != "codex" || st.Kind != session.KindClaude {
		t.Fatalf("status = %+v, want an enabled, ready codex route for a claude session", st)
	}
	t.Logf("status: %+v", st)

	child := startMCPChild(t, bin, "mcp-stdio", "--self-report", "--image-gen")
	child.send(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}})
	list, _ := child.awaitResult(t, 1)
	if !toolAdvertised(list, "generate_image") {
		t.Fatalf("generate_image is not advertised to a claude session: %v", list["tools"])
	}

	started := time.Now()
	child.send(t, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{
			"name":      "generate_image",
			"arguments": map[string]any{"prompt": "a single small blue triangle centred on a white background, flat vector style", "size": "1024x1024"},
			"_meta":     map[string]any{"progressToken": "live-1"},
		},
	})
	res, progress := child.awaitResult(t, 2)
	t.Logf("generation took %s, %d progress notification(s)", time.Since(started).Round(time.Second), len(progress))

	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("generate_image failed: %v", res["content"])
	}
	// The heartbeat is what keeps opencode's 60 s per-call ceiling from cutting a generation
	// in half; a run measured at ~35 s must produce at least the 10 s and 20 s ticks.
	if len(progress) < 2 {
		t.Errorf("progress notifications = %d, want at least 2 over a ~35 s call", len(progress))
	}
	for i, p := range progress {
		if p["progressToken"] != "live-1" {
			t.Errorf("progress %d addressed to %v, want the request's token", i, p["progressToken"])
		}
	}

	structured, _ := res["structuredContent"].(map[string]any)
	files, _ := structured["files"].([]any)
	if len(files) == 0 {
		t.Fatalf("result carried no file: %v", res)
	}
	first, _ := files[0].(map[string]any)
	path, _ := first["path"].(string)
	if !strings.HasPrefix(path, filepath.Join(home, ".cache", "agent-fleet", "generated")) {
		t.Fatalf("path = %q, want it under the session's generated dir inside the sandbox", path)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("the returned path does not exist: %v", err)
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		t.Fatalf("the returned file is not a decodable image: %v", err)
	}
	t.Logf("image: %s %dx%d, warnings=%v", format, cfg.Width, cfg.Height, structured["warnings"])
	if cfg.Width == 0 || cfg.Height == 0 {
		t.Fatalf("image is %dx%d", cfg.Width, cfg.Height)
	}
}

// TestImagegenLiveAgyEndToEnd is the same path over the SECOND provider, and it exists to
// settle the one thing the Codex route could never do: whether a requested aspect ratio
// survives a driver model. It also proves the sandbox shape the provider depends on — the
// isolated HOME with an allow-list of one tool — really does let generate_image through, which
// no unit test with a stub `agy` can show.
//
//	AF_IMAGEGEN_LIVE=1 go test -tags clicontract -run TestImagegenLiveAgy -timeout 15m .
//
// It spends one image against the user's Antigravity plan.
func TestImagegenLiveAgyEndToEnd(t *testing.T) {
	requireImagegenLive(t)
	if _, err := exec.LookPath("agy"); err != nil {
		t.Skipf("no agy on PATH (%v)", err)
	}
	if _, err := os.Stat(filepath.Join(paths.HomeDir(), ".gemini", "antigravity-cli", "antigravity-oauth-token")); err != nil {
		t.Skipf("no agy login to borrow (%v)", err)
	}
	bin := buildAgentBinary(t) // before the sandbox — see the note in the codex test
	home := imagegenSandboxOrdered(t, session.KindClaude, []string{"agy", "codex"}, true)
	serveAgentRoutes(t)

	statusBody := agentGETLive(t, "/imagegen/status?session=slot01")
	var st struct {
		Enabled      bool     `json:"enabled"`
		Ready        bool     `json:"ready"`
		Provider     string   `json:"provider"`
		Kind         string   `json:"kind"`
		Model        string   `json:"model"`
		Ops          []string `json:"ops"`
		AspectRatios []string `json:"aspectRatios"`
		Order        []string `json:"order"`
	}
	if json.Unmarshal(statusBody, &st) != nil {
		t.Fatalf("status is not JSON: %s", statusBody)
	}
	if !st.Enabled || !st.Ready || st.Provider != "agy" {
		t.Fatalf("status = %+v, want the preference order to have put agy in front", st)
	}
	if len(st.AspectRatios) == 0 {
		t.Fatalf("status = %+v, want the route's aspect ratios", st)
	}
	t.Logf("status: %+v", st)

	child := startMCPChild(t, bin, "mcp-stdio", "--self-report", "--image-gen")
	child.send(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}})
	list, _ := child.awaitResult(t, 1)
	if !toolAdvertised(list, "generate_image") {
		t.Fatalf("generate_image is not advertised: %v", list["tools"])
	}
	// The parameter only exists where it reaches the tool, so its presence here is the
	// difference between this route and the codex one, visible to the model.
	if !toolHasProperty(list, "generate_image", "aspect_ratio") {
		t.Fatalf("the agy route advertised no aspect_ratio: %v", list["tools"])
	}
	// With both logins present this claude session may name either service — the argument that
	// makes "generate the same prompt on both and compare" possible.
	if !toolHasProperty(list, "generate_image", "provider") {
		t.Fatalf("two ready providers were not offered as a choice: %v", list["tools"])
	}

	started := time.Now()
	child.send(t, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{
			"name": "generate_image",
			"arguments": map[string]any{
				"prompt":       "a single small blue triangle centred on a white background, flat vector style",
				"aspect_ratio": "16:9",
				// Named explicitly rather than left to the order: this is the path a comparison
				// takes, and an explicit choice must reach exactly that provider and never fall
				// through to the other account.
				"provider": "agy",
			},
			"_meta": map[string]any{"progressToken": "live-agy-1"},
		},
	})
	res, progress := child.awaitResult(t, 2)
	t.Logf("generation took %s, %d progress notification(s)", time.Since(started).Round(time.Second), len(progress))

	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("generate_image failed: %v", res["content"])
	}
	structured, _ := res["structuredContent"].(map[string]any)
	if structured["provider"] != "agy" {
		t.Fatalf("provider = %v, want the run attributed to agy", structured["provider"])
	}
	files, _ := structured["files"].([]any)
	if len(files) == 0 {
		t.Fatalf("result carried no file: %v", res)
	}
	first, _ := files[0].(map[string]any)
	path, _ := first["path"].(string)
	if !strings.HasPrefix(path, filepath.Join(home, ".cache", "agent-fleet", "generated")) {
		t.Fatalf("path = %q, want it under the session's generated dir inside the sandbox", path)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("the returned path does not exist: %v", err)
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		t.Fatalf("the returned file is not a decodable image: %v", err)
	}
	t.Logf("image: %s %dx%d, warnings=%v", format, cfg.Width, cfg.Height, structured["warnings"])
	if cfg.Width == 0 || cfg.Height == 0 {
		t.Fatalf("image is %dx%d", cfg.Width, cfg.Height)
	}
	// The claim under test. The tolerance is the provider's own: the ratio is honoured
	// approximately (measured 1376x768 for 16:9), and a square answer would mean it was not
	// honoured at all — which is what the codex route does with every size it is given.
	if got, want := float64(cfg.Width)/float64(cfg.Height), 16.0/9.0; got < want*0.9 || got > want*1.1 {
		t.Errorf("aspect ratio = %.3f (%dx%d), want ~%.3f — the request did not reach the tool",
			got, cfg.Width, cfg.Height, want)
	}
	// No throwaway home may outlive the call: the provider deletes the one it made, and a leak
	// would accumulate a whole agy state tree per generation.
	if leaked := leftoverAgyHomes(t); len(leaked) > 0 {
		t.Errorf("throwaway agy homes survived the run: %v", leaked)
	}
}

func leftoverAgyHomes(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "af-imagegen-agy-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// toolHasProperty reports whether an advertised tool's inputSchema offers a property.
func toolHasProperty(list map[string]any, tool, prop string) bool {
	tools, _ := list["tools"].([]any)
	for _, raw := range tools {
		def, _ := raw.(map[string]any)
		if def["name"] != tool {
			continue
		}
		schema, _ := def["inputSchema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		_, ok := props[prop]
		return ok
	}
	return false
}

// TestImagegenLiveNotOfferedToACodexSession is the exclusion of ADR 0069 decision 8, driven
// through the real server. Both halves are checked because the rule is now per PROVIDER: a
// Codex session may not route to codex, but it still wants the tool for any other route, and
// only when nothing else is left does the tool disappear. It generates nothing, so it costs no
// quota.
func TestImagegenLiveNotOfferedToACodexSession(t *testing.T) {
	requireImagegenLive(t)
	bin := buildAgentBinary(t) // before the sandbox — see the note in the test above

	t.Run("with agy also ready it keeps the tool, minus codex", func(t *testing.T) {
		if _, err := os.Stat(filepath.Join(paths.HomeDir(), ".gemini", "antigravity-cli", "antigravity-oauth-token")); err != nil {
			t.Skipf("no agy login to borrow (%v)", err)
		}
		imagegenSandboxOrdered(t, session.KindCodex, nil, true)
		serveAgentRoutes(t)

		child := startMCPChild(t, bin, "mcp-stdio", "--self-report", "--image-gen")
		child.send(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}})
		list, _ := child.awaitResult(t, 1)
		if !toolAdvertised(list, "generate_image") {
			t.Fatalf("a codex session lost the tool although agy could have served it: %v", list["tools"])
		}
		// With one provider left there is no choice to offer, so the argument is absent —
		// which is itself the proof that codex was taken out of the offer.
		if toolHasProperty(list, "generate_image", "provider") {
			t.Errorf("a provider choice was advertised with only one provider left: %v", list["tools"])
		}
	})

	t.Run("with only codex ready the tool disappears", func(t *testing.T) {
		imagegenSandboxOrdered(t, session.KindCodex, nil, false)
		serveAgentRoutes(t)

		child := startMCPChild(t, bin, "mcp-stdio", "--self-report", "--image-gen")
		child.send(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}})
		list, _ := child.awaitResult(t, 1)
		// The negative needs its own positive control: an empty or failed tools/list would pass
		// this check without proving anything.
		if !toolAdvertised(list, "af_report") {
			t.Fatalf("the tool list is not a real one: %v", list["tools"])
		}
		if toolAdvertised(list, "generate_image") {
			t.Fatal("a codex session was offered the fleet tool as well as its own")
		}
	})
}

func toolAdvertised(list map[string]any, name string) bool {
	tools, _ := list["tools"].([]any)
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		if tool["name"] == name {
			return true
		}
	}
	return false
}

func agentGETLive(t *testing.T, path string) []byte {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+os.Getenv("AGENT_ADDR")+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("AGENT_TOKEN"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b := make([]byte, 1<<16)
	n, _ := resp.Body.Read(b)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, resp.StatusCode, b[:n])
	}
	return b[:n]
}
