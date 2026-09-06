package mcpx

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// stubImageGenStatus points the loopback Agent client at a server answering
// /imagegen/status with st, and /imagegen/generate through generate (nil = 500, which is
// what "the route is not part of this test" should look like).
func stubImageGenStatus(t *testing.T, st mcpImageGenStatus) {
	t.Helper()
	stubAgentForImageGen(t, st, nil)
}

func stubAgentForImageGen(t *testing.T, st mcpImageGenStatus, generate http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/imagegen/status":
			_ = json.NewEncoder(w).Encode(st)
		case "/imagegen/generate":
			if generate == nil {
				http.Error(w, `{"error":{"code":"unexpected","message":"not stubbed"}}`, http.StatusInternalServerError)
				return
			}
			generate(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_ADDR", u.Host)
}

func withImageGen(t *testing.T, on bool) {
	t.Helper()
	oldSelfReport, oldEnabled, oldSource := selfReportOnly(), mcpImageGenEnabled, mcpSourceSession
	setSelfReportOnly(true)
	mcpImageGenEnabled = on
	mcpSourceSession = "slot01"
	t.Cleanup(func() {
		setSelfReportOnly(oldSelfReport)
		mcpImageGenEnabled, mcpSourceSession = oldEnabled, oldSource
	})
}

// The tool literal must spell its name out for the advertised-schema scan (it only reads
// string literals), so the constant the dispatch uses could drift away from it unnoticed.
func TestImageGenToolNameMatchesConstant(t *testing.T) {
	tools := mcpStdioImageGenTools([]string{"generate"})
	if len(tools) != 1 || tools[0]["name"] != mcpToolGenerateImage {
		t.Fatalf("advertised name = %v, want %q", tools[0]["name"], mcpToolGenerateImage)
	}
}

// The one exclusion of ADR 0069 decision 8, and its negative control: a Codex session already
// has the CLI's own image_gen, so routing it through a second codex process would double the
// cost for nothing — but the same session DOES want the fleet tool once the route is not codex.
func TestImageGenAdvertisedByKindAndProvider(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   mcpImageGenStatus
		wantAdv  bool
		wantOps  int
		imageGen bool
	}{
		{
			name:     "codex session on the codex route is excluded",
			status:   mcpImageGenStatus{Enabled: true, Ready: true, Provider: "codex", Kind: "codex", Ops: []string{"generate"}},
			imageGen: true,
		},
		{
			name:     "claude session on the codex route is offered the tool",
			status:   mcpImageGenStatus{Enabled: true, Ready: true, Provider: "codex", Kind: "claude", Ops: []string{"generate", "edit"}},
			imageGen: true, wantAdv: true, wantOps: 2,
		},
		{
			name:     "codex session on another route is offered the tool",
			status:   mcpImageGenStatus{Enabled: true, Ready: true, Provider: "bedrock", Kind: "codex", Ops: []string{"generate"}},
			imageGen: true, wantAdv: true, wantOps: 1,
		},
		{
			name:     "no provider is ready",
			status:   mcpImageGenStatus{Enabled: true, Ready: false, Provider: "", Kind: "claude"},
			imageGen: true,
		},
		{
			name:   "the flag is off, so the Agent is never even asked",
			status: mcpImageGenStatus{Enabled: true, Ready: true, Provider: "codex", Kind: "claude", Ops: []string{"generate"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withImageGen(t, tc.imageGen)
			stubImageGenStatus(t, tc.status)
			ops, ok := mcpImageGenAdvertise()
			if ok != tc.wantAdv {
				t.Fatalf("advertise = %v, want %v", ok, tc.wantAdv)
			}
			if len(ops) != tc.wantOps {
				t.Fatalf("ops = %v, want %d", ops, tc.wantOps)
			}
			if got := advertisedNames(t); tc.wantAdv != got[mcpToolGenerateImage] {
				t.Fatalf("generate_image in tools/list = %v, want %v", got[mcpToolGenerateImage], tc.wantAdv)
			}
		})
	}
}

// An unreachable Agent means the tool could not work anyway; advertising it would produce a
// tool whose every call fails.
func TestImageGenNotAdvertisedWhenAgentUnreachable(t *testing.T) {
	withImageGen(t, true)
	t.Setenv("AGENT_ADDR", ":1") // nothing listens
	if _, ok := mcpImageGenAdvertise(); ok {
		t.Fatal("generate_image was advertised with no Agent to serve it")
	}
}

func advertisedNames(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, tool := range mcpStdioToolList() {
		name, _ := tool["name"].(string)
		out[name] = true
	}
	return out
}

// The advertised set is the scope boundary: a client that guesses the name must not reach a
// route that spends the user's plan quota. Both surfaces refuse — the session one because the
// tool is not in its advertised set, the assistant one because it never gets the flag at all.
func TestGenerateImageRefusedWhenFlagOff(t *testing.T) {
	for _, selfReport := range []bool{true, false} {
		withImageGen(t, false)
		setSelfReportOnly(selfReport)
		resp := callGenerateImage(t, map[string]any{"prompt": "a cat"})
		if !strings.Contains(resp, `"isError":true`) {
			t.Fatalf("selfReport=%v: result = %s, want a refusal", selfReport, resp)
		}
	}
}

func TestGenerateImageReturnsPathAndWarnings(t *testing.T) {
	withImageGen(t, true)
	var got map[string]any
	stubAgentForImageGen(t,
		mcpImageGenStatus{Enabled: true, Ready: true, Provider: "codex", Kind: "claude", Ops: []string{"generate"}},
		func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&got)
			_, _ = w.Write([]byte(`{"files":[{"path":"/home/u/.cache/agent-fleet/generated/sid/image-1.png","name":"image-1.png","mime":"image/png","bytes":848000,"width":1254,"height":1254}],"provider":"codex","model":"gpt-5.4-mini","warnings":["size=1024x1024 requested, 1254x1254 produced"]}`))
		})

	resp := callGenerateImage(t, map[string]any{"prompt": "a cat", "size": "1024x1024", "count": 1})
	if got["session"] != "slot01" || got["prompt"] != "a cat" || got["size"] != "1024x1024" {
		t.Fatalf("forwarded body = %v", got)
	}
	for _, want := range []string{"image-1.png", "1254x1254", "codex"} {
		if !strings.Contains(resp, want) {
			t.Fatalf("result = %s, want it to contain %q", resp, want)
		}
	}
	// The bytes themselves are never returned: a measured PNG is 848 KB, and base64 of it
	// would ride in the session's context for the rest of the conversation.
	if strings.Contains(resp, `"type":"image"`) || strings.Contains(resp, "base64") {
		t.Fatalf("result carried image bytes: %s", resp)
	}
}

// A failed generation must keep the Agent's own reason, or the model is told "it failed" with
// nothing to act on.
func TestGenerateImageKeepsAgentReason(t *testing.T) {
	withImageGen(t, true)
	stubAgentForImageGen(t,
		mcpImageGenStatus{Enabled: true, Ready: true, Provider: "codex", Kind: "claude", Ops: []string{"generate"}},
		func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"error":{"code":"imagegen_no_provider","message":"codex is not logged in"}}`, http.StatusServiceUnavailable)
		})
	resp := callGenerateImage(t, map[string]any{"prompt": "a cat"})
	if !strings.Contains(resp, "codex is not logged in") {
		t.Fatalf("result = %s, want the Agent's reason", resp)
	}
}

func callGenerateImage(t *testing.T, args map[string]any) string {
	t.Helper()
	raw, _ := json.Marshal(args)
	params, _ := json.Marshal(map[string]any{"name": mcpToolGenerateImage, "arguments": json.RawMessage(raw)})
	return string(mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params}))
}

// The heartbeat is what lifts opencode's 60 s per-call ceiling (measured: a 90 s call
// succeeded with a notification every 10 s, and was cut at 60.0 s without one).
func TestProgressHeartbeatWritesNotifications(t *testing.T) {
	var buf bytes.Buffer
	old, oldEvery := stdioOut, mcpProgressEvery
	stdioOut, mcpProgressEvery = &stdioWriter{w: bufio.NewWriter(&buf)}, 5*time.Millisecond
	t.Cleanup(func() { stdioOut, mcpProgressEvery = old, oldEvery })

	params, _ := json.Marshal(map[string]any{"_meta": map[string]any{"progressToken": "tok-1"}})
	stop := startProgressHeartbeat(mcpReq{ID: json.RawMessage(`1`), Params: params}, "生成中")
	time.Sleep(40 * time.Millisecond)
	stop()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("wrote %d notifications, want at least 2: %q", len(lines), buf.String())
	}
	var first struct {
		Method string `json:"method"`
		Params struct {
			ProgressToken string `json:"progressToken"`
			Progress      int    `json:"progress"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("first line is not JSON: %v (%q)", err, lines[0])
	}
	if first.Method != "notifications/progress" || first.Params.ProgressToken != "tok-1" || first.Params.Progress != 1 {
		t.Fatalf("first notification = %+v", first)
	}
	// Nothing may be written after stop returns: a heartbeat emitted after the tools/call
	// result would arrive out of order on the wire.
	before := buf.Len()
	time.Sleep(30 * time.Millisecond)
	if buf.Len() != before {
		t.Fatal("the heartbeat kept writing after stop()")
	}
}

// No token means the client is not listening for progress; an unaddressed notification is
// dropped, and writing one anyway is noise on the same pipe the responses use.
func TestProgressHeartbeatSilentWithoutToken(t *testing.T) {
	var buf bytes.Buffer
	old, oldEvery := stdioOut, mcpProgressEvery
	stdioOut, mcpProgressEvery = &stdioWriter{w: bufio.NewWriter(&buf)}, time.Millisecond
	t.Cleanup(func() { stdioOut, mcpProgressEvery = old, oldEvery })

	stop := startProgressHeartbeat(mcpReq{ID: json.RawMessage(`1`), Params: json.RawMessage(`{}`)}, "生成中")
	time.Sleep(10 * time.Millisecond)
	stop()
	if buf.Len() != 0 {
		t.Fatalf("wrote %q with no progressToken", buf.String())
	}
}
