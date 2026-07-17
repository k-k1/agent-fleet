package claude

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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
