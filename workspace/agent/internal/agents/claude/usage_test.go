package claude

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStatusLineCapture(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	payload := []byte(`{"model":{"id":"x"},"rate_limits":{` +
		`"five_hour":{"used_percentage":23.5,"resets_at":9999999999},` +
		`"seven_day":{"used_percentage":41.2,"resets_at":9999999998}}}`)
	if !captureFromStatusLine(payload) {
		t.Fatal("expected capture to store rate_limits")
	}
	c, at := readCapturedUsage()
	if c == nil || c.FiveHour == nil || c.FiveHour.UsedPercent != 23.5 || c.FiveHour.ResetsAt != 9999999999 {
		t.Fatalf("five_hour not captured: %+v", c)
	}
	if c.SevenDay == nil || c.SevenDay.UsedPercent != 41.2 {
		t.Fatalf("seven_day not captured: %+v", c)
	}
	if at.IsZero() {
		t.Error("capturedAt not set")
	}

	// A payload without rate_limits must NOT clobber the good capture.
	if captureFromStatusLine([]byte(`{"model":{"id":"x"}}`)) {
		t.Error("payload without rate_limits should not report a capture")
	}
	if c2, _ := readCapturedUsage(); c2 == nil || c2.FiveHour == nil {
		t.Error("good capture was clobbered by an empty payload")
	}
}

func TestAdjustWindow(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)

	// Future reset → percent passes through unchanged.
	fw := adjustWindow(30, 5*60, now.Add(2*time.Hour).Unix(), now)
	if fw.Pct != 30 {
		t.Errorf("future: pct=%v want 30", fw.Pct)
	}
	if rt, _ := time.Parse(time.RFC3339, fw.ResetsAt); !rt.Equal(now.Add(2 * time.Hour)) {
		t.Errorf("future: resetsAt=%v want +2h", fw.ResetsAt)
	}

	// Past reset → zeroed and rolled forward to the next boundary after now.
	pw := adjustWindow(80, 5*60, now.Add(-2*time.Hour).Unix(), now)
	if pw.Pct != 0 {
		t.Errorf("past: pct=%v want 0", pw.Pct)
	}
	if rt, _ := time.Parse(time.RFC3339, pw.ResetsAt); !rt.After(now) {
		t.Errorf("past: resetsAt=%v not after now", pw.ResetsAt)
	}
}

func TestHandleUsage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	get := func() map[string]any {
		rec := httptest.NewRecorder()
		HandleUsage(rec, httptest.NewRequest(http.MethodGet, "/claude/usage", nil))
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("bad json: %v", err)
		}
		return out
	}

	// No capture, no token → chip hidden (ok:false, authed:false).
	if out := get(); out["ok"] != false || out["authed"] != false {
		t.Fatalf("empty: ok=%v authed=%v", out["ok"], out["authed"])
	}

	// A token present → authed:true even before any capture (degraded chip stays).
	writeCreds(t, dir, "tok-123")
	if out := get(); out["ok"] != false || out["authed"] != true {
		t.Fatalf("authed-no-capture: ok=%v authed=%v", out["ok"], out["authed"])
	}

	// After a capture with a future reset → ok:true and the window percent surfaces.
	future := time.Now().Add(3 * time.Hour).Unix()
	captureFromStatusLine([]byte(`{"rate_limits":{"five_hour":{"used_percentage":40,"resets_at":` +
		strconv.FormatInt(future, 10) + `}}}`))
	out := get()
	if out["ok"] != true {
		t.Fatalf("post-capture ok=%v", out["ok"])
	}
	fh, _ := out["fiveHour"].(map[string]any)
	if fh == nil || fh["pct"].(float64) != 40 {
		t.Fatalf("fiveHour not surfaced: %+v", out["fiveHour"])
	}
}

func TestEnsureStatusLine(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	ours := statuslineCmd()

	// No prior statusLine → inject ours (capture-only).
	EnsureStatusLine()
	if got := statusLineCommand(t, dir); got != ours {
		t.Fatalf("inject: got %q want %q", got, ours)
	}

	// Idempotent → unchanged.
	EnsureStatusLine()
	if got := statusLineCommand(t, dir); got != ours {
		t.Fatalf("idempotent: got %q", got)
	}

	// A foreign statusLine → wrapped (ours prefix + --delegate '<original>'), not clobbered.
	writeSettings(map[string]any{"statusLine": map[string]any{"type": "command", "command": "my.sh --x", "padding": float64(2)}})
	EnsureStatusLine()
	got := statusLineCommand(t, dir)
	if want := ours + " --delegate 'my.sh --x'"; got != want {
		t.Fatalf("wrap: got %q want %q", got, want)
	}
	// padding preserved through the wrap.
	if sl, _ := readSettings()["statusLine"].(map[string]any); sl["padding"] != float64(2) {
		t.Errorf("wrap dropped padding: %+v", sl)
	}
	// Wrapping is idempotent (already ours prefix → left alone).
	EnsureStatusLine()
	if got2 := statusLineCommand(t, dir); got2 != got {
		t.Errorf("re-wrap changed command: %q", got2)
	}
}

// An install from a DIFFERENT binary path (dev build, e2e binary, scratchpad copy) is
// still ours: it must be peeled and re-pointed at this exe, never wrapped — wrapping
// re-quotes the whole command each round and grows it exponentially until exec(2)
// rejects it (E2BIG) and capture dies.
func TestEnsureStatusLineRepointsForeignPathInstall(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	ours := statuslineCmd()

	for _, prior := range []string{
		"/tmp/wa statusline",                                  // legacy capture-only, other path
		"/tmp/wa statusline " + captureFlag,                   // flagged, other path
		"/tmp/wa statusline --delegate '/tmp/wa2 statusline'", // nested, both ours
	} {
		writeSettings(map[string]any{"statusLine": map[string]any{"type": "command", "command": prior}})
		EnsureStatusLine()
		if got := statusLineCommand(t, dir); got != ours {
			t.Errorf("prior %q: got %q want %q", prior, got, ours)
		}
	}

	// A user command buried under our layers survives the peel exactly once.
	nested := "/tmp/wa statusline --delegate " + shellQuote(ours+" --delegate "+shellQuote("my.sh --x"))
	writeSettings(map[string]any{"statusLine": map[string]any{"type": "command", "command": nested}})
	EnsureStatusLine()
	if want := ours + " --delegate 'my.sh --x'"; statusLineCommand(t, dir) != want {
		t.Errorf("nested foreign: got %q want %q", statusLineCommand(t, dir), want)
	}

	// The pathological chain this fixes: repeated wrapping never grows the command.
	writeSettings(map[string]any{"statusLine": map[string]any{"type": "command", "command": ours}})
	for i := range 20 {
		t.Setenv("AF_TEST_ROUND", strconv.Itoa(i)) // rounds differ only in that they re-run
		EnsureStatusLine()
	}
	if got := statusLineCommand(t, dir); got != ours {
		t.Errorf("20 rounds: len=%d got %q", len(got), got)
	}
}

// A delegate that would push the command past the exec limit is dropped: capture-only
// beats a command that can't run at all.
func TestEnsureStatusLineDropsOversizeDelegate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	huge := "my.sh " + strings.Repeat("x", maxStatusLineCmd)
	writeSettings(map[string]any{"statusLine": map[string]any{"type": "command", "command": huge}})
	EnsureStatusLine()
	if got := statusLineCommand(t, dir); got != statuslineCmd() {
		t.Errorf("oversize: got %d bytes, want capture-only", len(got))
	}
}

func TestUnwrapOurs(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"/usr/local/bin/workspace-agent statusline", ""},
		{"/usr/local/bin/workspace-agent statusline " + captureFlag, ""},
		{"my.sh --x", "my.sh --x"},                                             // foreign → untouched
		{"/x/wa statusline --delegate 'my.sh --x'", "my.sh --x"},               // one layer
		{"/x/wa statusline --delegate '/y/wa statusline'", ""},                 // ours all the way down
		{"/x/wa statusline --delegate 'a'\\''b'", "a'b"},                       // shellQuote's escape round-trips
		{"/x/wa statusline --unknown-flag", "/x/wa statusline --unknown-flag"}, // unrecognised shape → left alone
		{"/x/wa statusline --delegate 'unterminated", "/x/wa statusline --delegate 'unterminated"},
	}
	for _, c := range cases {
		if got := unwrapOurs(c.in); got != c.want {
			t.Errorf("unwrapOurs(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

func statusLineCommand(t *testing.T, dir string) string {
	t.Helper()
	sl, _ := readSettings()["statusLine"].(map[string]any)
	cmd, _ := sl["command"].(string)
	return cmd
}

func writeCreds(t *testing.T, dir, token string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": token}})
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDelegateArg(t *testing.T) {
	if got := delegateArg([]string{"--delegate", "my-script.sh --flag"}); got != "my-script.sh --flag" {
		t.Errorf("delegate: got %q", got)
	}
	if got := delegateArg([]string{}); got != "" {
		t.Errorf("no args: got %q want empty", got)
	}
	if got := delegateArg([]string{"--delegate"}); got != "" {
		t.Errorf("dangling --delegate: got %q want empty", got)
	}
}

// The statusLine we install must never pin a volatile path: settings.json outlives any
// single build of the agent, and claude fails such a command silently — capture stops,
// and the rollforward below then reports a confident, wrong 0%.
func TestStatusLineCmdAvoidsVolatileExe(t *testing.T) {
	installed := filepath.Join(t.TempDir(), "workspace-agent")
	if err := os.WriteFile(installed, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_AGENT_INSTALLED_BIN", installed)
	// The test binary runs from the build cache under the temp dir — the very shape
	// this guards against — so the installed path must win.
	if want := installed + " statusline " + captureFlag; statuslineCmd() != want {
		t.Errorf("statuslineCmd()=%q want %q", statuslineCmd(), want)
	}
}

// A stale window's 0% is an assumption (nothing spent since the capture), not a reading.
// The Console needs to be told which one it has: a fabricated "0%, resets 20:50" is
// indistinguishable from a real one, which is how a dead capture went unnoticed for
// six hours.
func TestAdjustWindowFlagsStale(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	if w := adjustWindow(30, 5*60, now.Add(2*time.Hour).Unix(), now); w.Stale {
		t.Error("a window still in the future is a real reading, not stale")
	}
	w := adjustWindow(80, 5*60, now.Add(-2*time.Hour).Unix(), now)
	if !w.Stale || w.Pct != 0 {
		t.Errorf("rolled-forward window: %+v want pct 0 + stale", w)
	}
}

func TestHandleUsageMarksStaleWindow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	writeCreds(t, dir, "tok-123")

	// The incident's shape: a capture whose 5h window expired hours ago (statusLine
	// dead), while the weekly window is still live.
	past := time.Now().Add(-3 * time.Hour).Unix()
	future := time.Now().Add(12 * time.Hour).Unix()
	captureFromStatusLine([]byte(`{"rate_limits":{` +
		`"five_hour":{"used_percentage":17,"resets_at":` + strconv.FormatInt(past, 10) + `},` +
		`"seven_day":{"used_percentage":65,"resets_at":` + strconv.FormatInt(future, 10) + `}}}`))

	rec := httptest.NewRecorder()
	HandleUsage(rec, httptest.NewRequest(http.MethodGet, "/claude/usage", nil))
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	fh, _ := out["fiveHour"].(map[string]any)
	if fh == nil || fh["stale"] != true || fh["pct"].(float64) != 0 {
		t.Errorf("expired window must be flagged stale: %+v", out["fiveHour"])
	}
	sd, _ := out["sevenDay"].(map[string]any)
	if sd == nil || sd["stale"] == true || sd["pct"].(float64) != 65 {
		t.Errorf("live window must pass through unflagged: %+v", out["sevenDay"])
	}
}
