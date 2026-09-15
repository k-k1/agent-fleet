package mcpx

// mcp_tool_list_changed_test.go — the tool list is a snapshot the client takes once, so the
// server has to say when it moved (notifications/tools/list_changed).
//
// The defect these guard against was measured on 2026-09-15: an administrator enabled a
// checkpoint, the Agent served it within its own TTL, a freshly spawned server answered
// tools/list with the new `model` enum — and a running claude session, resumed one minute
// earlier, went on advertising the enum from before. Restarting the session did not fix it,
// because the Console's stop/resume is `claude --resume` and the client restores its snapshot.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// captureStdio installs a buffer where the client's stdin would be reading, so a test can see
// what the server sent unprompted. stdioOut is nil in every other test, which is exactly what
// keeps the watcher's goroutine out of them.
func captureStdio(t *testing.T) *bytes.Buffer {
	t.Helper()
	old := stdioOut
	var buf bytes.Buffer
	stdioOut = &stdioWriter{w: bufio.NewWriter(&buf)}
	t.Cleanup(func() { stdioOut = old })
	return &buf
}

// stubMovingStatus answers /imagegen/status with whatever the returned setter last stored, so a
// test can move the catalogue under a server that is already running.
func stubMovingStatus(t *testing.T, first mcpImageGenStatus) func(mcpImageGenStatus) {
	t.Helper()
	var cur atomic.Value
	set := func(st mcpImageGenStatus) {
		b, err := json.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		cur.Store(b)
	}
	set(first)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/imagegen/status" {
			http.NotFound(w, r)
			return
		}
		b, _ := cur.Load().([]byte)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_ADDR", u.Host)
	return set
}

func statusWithModels(ids ...string) mcpImageGenStatus {
	models := make([]mcpImageGenModel, 0, len(ids))
	for _, id := range ids {
		models = append(models, mcpImageGenModel{ID: id})
	}
	return mcpImageGenStatus{Enabled: true, Ready: true, Kind: "claude",
		Providers: []mcpImageGenProvider{{ID: "image", Service: "ComfyUI", Ops: []string{"generate"}, Models: models}}}
}

// notificationsIn returns the methods of every unprompted line the server wrote.
func notificationsIn(buf *bytes.Buffer) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m struct {
			Method string `json:"method"`
		}
		if json.Unmarshal([]byte(line), &m) == nil && m.Method != "" {
			out = append(out, m.Method)
		}
	}
	return out
}

// 🔥 The regression itself: a checkpoint appears, and the client is told — even though it asked
// nothing. The negative control is the second check, which must stay silent: a notification on
// every tick would have the client re-listing once a minute forever.
func TestToolListChangedWhenAModelIsEnabled(t *testing.T) {
	withImageGen(t, true)
	set := stubMovingStatus(t, statusWithModels("sdxl-base", "anima"))
	buf := captureStdio(t)

	rememberAdvertised(mcpStdioToolList())
	if got := notificationsIn(buf); len(got) != 0 {
		t.Fatalf("the first list already notified: %v", got)
	}

	set(statusWithModels("sdxl-base", "anima", "krea2"))
	if !mcpCheckToolListOnce() {
		t.Fatal("a new checkpoint did not register as a change")
	}
	if got := notificationsIn(buf); len(got) != 1 || got[0] != "notifications/tools/list_changed" {
		t.Fatalf("notifications = %v, want one notifications/tools/list_changed", got)
	}

	if mcpCheckToolListOnce() {
		t.Error("an unchanged list notified a second time")
	}
	if got := notificationsIn(buf); len(got) != 1 {
		t.Errorf("notifications after a quiet tick = %v, want the first one only", got)
	}
}

// The withdrawal direction, and the reason the remembered set is updated with it: what the
// call-side check enforces must follow the newest truth, or a session whose client has not
// re-listed yet keeps permission to name a checkpoint that is gone.
func TestToolListChangedWhenAModelIsWithdrawn(t *testing.T) {
	withImageGen(t, true)
	set := stubMovingStatus(t, statusWithModels("sdxl-base", "anima"))
	captureStdio(t)
	rememberAdvertised(mcpStdioToolList())

	set(mcpImageGenStatus{Enabled: true, Ready: true, Kind: "claude",
		Providers: []mcpImageGenProvider{{ID: "image", Service: "ComfyUI", Ops: []string{"generate"}}}})
	if !mcpCheckToolListOnce() {
		t.Fatal("a withdrawn checkpoint did not register as a change")
	}
	if !mcpStdioToolAdvertised(mcpToolGenerateImage) {
		t.Error("generate_image stopped being advertised, but only its enum shrank")
	}
}

// 🔴 The fingerprint has to cover the SCHEMA. Names alone never move when a checkpoint is
// enabled — generate_image is called generate_image either way — so a watcher comparing names
// would report "nothing changed" for the exact defect it exists to catch.
func TestToolListFingerprintCoversTheModelEnum(t *testing.T) {
	withImageGen(t, true)
	set := stubMovingStatus(t, statusWithModels("sdxl-base", "anima"))
	before := mcpStdioToolList()
	set(statusWithModels("sdxl-base", "anima", "krea2"))
	after := mcpStdioToolList()

	if len(before) != len(after) {
		t.Fatalf("the tool COUNT moved (%d -> %d), so this test is no longer about the schema", len(before), len(after))
	}
	if mcpToolListFingerprint(before) == mcpToolListFingerprint(after) {
		t.Error("two lists differing only in the model enum fingerprint the same")
	}
}

// A client ignores notifications/tools/list_changed from a server that did not declare it, so
// the capability and the notification are one feature and have to be tested as one.
func TestToolsListChangedCapabilityIsDeclared(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps map[string]any
	}{
		{"initialize", capsOf(t, dispatchMCPStdio([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)))},
		{"server/discover", capsIn(t, mcpStdioDiscoverResult())},
	} {
		tools, _ := tc.caps["tools"].(map[string]any)
		if on, _ := tools["listChanged"].(bool); !on {
			t.Errorf("%s: capabilities.tools.listChanged = %v, want true", tc.name, tools)
		}
	}
}

// capsIn reads the capabilities off a result the server built in memory.
func capsIn(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("no capabilities in %v", result)
	}
	return caps
}

// capsOf digs the capabilities out of a raw JSON-RPC response.
func capsOf(t *testing.T, resp []byte) map[string]any {
	t.Helper()
	var r struct {
		Result struct {
			Capabilities map[string]any `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		t.Fatalf("decoding %s: %v", resp, err)
	}
	return r.Result.Capabilities
}
