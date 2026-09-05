package kiro

// Live verification, run only with KIRO_LIVE=1 (same shape as codex live_drift_test.go and
// opencode live_contract_test.go). Against the real binary, a real v2 store and a real tmux
// pane, it measures whether the read layer's contract (discovery, parsing, state
// classification, models) survives version drift. Not run in CI: it depends on the
// environment. For example:
//
//	KIRO_LIVE=1 go test ./internal/agents/kiro/ -run Live -v

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

func liveGate(t *testing.T) {
	if os.Getenv("KIRO_LIVE") != "1" {
		t.Skip("set KIRO_LIVE=1 to run the live kiro contract test")
	}
	if _, err := exec.LookPath(bin()); err != nil {
		t.Skipf("%s not on PATH", bin())
	}
}

// TestLiveModelsAndAuth exercises the real CLI: whoami + --list-models JSON.
func TestLiveModelsAndAuth(t *testing.T) {
	liveGate(t)
	if !LoggedIn() {
		t.Fatal("expected a logged-in kiro (Builder ID) for the live test")
	}
	st := Status()
	if st["connected"] != true || st["supported"] != true {
		t.Fatalf("Status not connected/supported: %+v", st)
	}
	models := Models()
	if len(models) == 0 {
		t.Fatal("expected a non-empty live model catalog")
	}
	for _, m := range models {
		if m.ID == "auto" {
			t.Errorf("auto must be excluded from the catalog: %+v", models)
		}
	}
	t.Logf("live: %d models, first=%+v, email=%v", len(models), models[0], st["email"])
}

// TestLiveTUIRoundTrip launches a real kiro TUI in tmux, sends a prompt, and asserts
// the state contract (working→idle) and that discovery + the parser pick up the real
// v2 JSONL the CLI writes. This is the end-to-end read-layer proof.
func TestLiveTUIRoundTrip(t *testing.T) {
	liveGate(t)
	dir, err := os.MkdirTemp("", "kiro-live-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	ensureSettings()
	tn := "aftest-kiro-live"
	_ = tmuxx.Cmd("kill-session", "-t", tn).Run()
	if err := tmuxx.Cmd("new-session", "-d", "-s", tn, "-x", "200", "-y", "50").Run(); err != nil {
		t.Fatal(err)
	}
	defer tmuxx.Cmd("kill-session", "-t", tn).Run()

	prog := buildProgram("", "", "", "", true) // fresh, auto model, default posture
	_ = tmuxx.Cmd("send-keys", "-t", tn, "cd "+dir+" && "+prog, "Enter").Run()

	// Wait for the composer (idle footer) to draw.
	if !waitFor(t, tn, "ask a question or describe a task", 20*time.Second) {
		t.Fatal("composer never drew the idle footer")
	}
	if got := classifyPane(tmuxx.CapturePane(tn)); got != "idle" {
		t.Fatalf("fresh composer should be idle, got %q", got)
	}

	// Send a prompt that runs a shell tool so we exercise toolUse+ToolResults parsing.
	_ = tmuxx.Cmd("send-keys", "-t", tn, "Run the shell command: echo kirolive42", "Enter").Run()
	if !waitFor(t, tn, "Kiro is working", 10*time.Second) {
		t.Log("did not observe the working footer (fast turn?) — continuing")
	} else if got := classifyPane(tmuxx.CapturePane(tn)); got != "working" {
		t.Errorf("mid-turn should be working, got %q", got)
	}
	// Back to idle when the turn completes.
	if !waitFor(t, tn, "ask a question or describe a task", 40*time.Second) {
		t.Fatal("turn never returned to idle")
	}

	// Discovery must find the session by cwd, and the parser must render the turn with
	// the tool output attached.
	sid := discoverSid(dir, time.Time{}) // unfenced: this temp dir has only our session
	if sid == "" {
		t.Fatal("discoverSid found no session for the live cwd")
	}
	turns := parseTranscript(transcriptPath(sid))
	if len(turns) == 0 {
		t.Fatal("parser returned no turns for the live transcript")
	}
	var sawUser, sawToolOut bool
	for _, tn := range turns {
		if tn.Role == "user" && strings.Contains(tn.Text, "kirolive42") {
			sawUser = true
		}
		for _, p := range tn.Parts {
			if p.Kind == "tool" && strings.Contains(p.Output, "kirolive42") {
				sawToolOut = true
			}
		}
	}
	if !sawUser {
		t.Errorf("user prompt not parsed from live transcript: %+v", turns)
	}
	if !sawToolOut {
		t.Logf("tool output not attached (model may not have used shell): %+v", turns)
	}
	t.Logf("live: sid=%s turns=%d sawUser=%v sawToolOut=%v", sid, len(turns), sawUser, sawToolOut)
}

func waitFor(t *testing.T, tn, marker string, d time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if strings.Contains(tmuxx.CapturePane(tn), marker) {
			return true
		}
		time.Sleep(400 * time.Millisecond)
	}
	return false
}
