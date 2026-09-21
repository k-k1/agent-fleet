package msp_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
)

// Live verification against a real `muse serve`, run only with MUSE_LIVE=1 (the same shape as
// kiro's live_test.go and codex's live_drift_test.go). Not run in CI: it needs the vendor
// binary, which is proprietary and not in the image.
//
// It stops short of a turn on purpose. `muse exec --provider echo` is credential-free, but
// `--provider` is an exec startup flag and `muse serve` has none: measured on 1.3.0-R3401.1,
// `session/start` accepts and records `providerId: "echo"` and the turn still ends
// `authRequired`. So everything up to and including session creation is free, and a turn
// spends a member's subscription quota — which is msptest's reason to exist.
func liveGate(t *testing.T) string {
	t.Helper()
	if os.Getenv("MUSE_LIVE") != "1" {
		t.Skip("MUSE_LIVE=1 to run against a real muse serve")
	}
	bin := os.Getenv("AF_MUSE_BIN")
	if bin == "" {
		p, err := exec.LookPath("muse")
		if err != nil {
			t.Skip("no muse binary: set AF_MUSE_BIN")
		}
		bin = p
	}
	return bin
}

// startServe spawns a host under a throwaway HOME. The throwaway is not tidiness: with
// foreign personal context left on, a real turn ships the contents of the member's
// ~/.claude/CLAUDE.md to Meta, so a live test must never run against the member's own home
// (ADR 0095 decision 6, clamp 5).
func startServe(t *testing.T, bin string) (*msp.Client, <-chan string) {
	t.Helper()
	home := t.TempDir()
	for _, sub := range []string{"cfg", "data", "ws"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(bin, "serve", "--disable-sandbox")
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, "cfg"),
		"XDG_DATA_HOME="+filepath.Join(home, "data"),
		"MUSE_NO_AUTO_UPDATE=1",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("muse serve: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
	t.Setenv("AF_MUSE_LIVE_WS", filepath.Join(home, "ws"))

	notes := make(chan string, 256)
	client := msp.NewClient(stdin, stdout, msp.Handler{
		OnNotification: func(method string, params json.RawMessage) {
			select {
			case notes <- method:
			default:
			}
		},
	})
	return client, notes
}

func TestLiveHandshake(t *testing.T) {
	bin := liveGate(t)
	client, _ := startServe(t, bin)

	res, err := msp.Handshake(client, "0.1.0", []msp.CapabilityName{msp.CapabilityNameSessionMCP})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if d := msp.SchemaDrift(res); d != "" {
		t.Errorf("the running host disagrees with the generated types: %s", d)
	}
	if !msp.Granted(res, msp.CapabilityNameSessionMCP) {
		t.Error("sessionMcp was not granted; decision 11 puts MCP servers on the wire through it")
	}
	if res.MuseHome == "" {
		t.Error("museHome is empty")
	}
	if res.Schema.Version != msp.SchemaVersion {
		t.Errorf("schema version = %d, these types speak %d", res.Schema.Version, msp.SchemaVersion)
	}
}

// The ids AF mints have to satisfy the host's own validator. It refuses a v4 outright, so this
// is the test that would have caught a hand-rolled generator getting the version nibble wrong.
func TestLiveSessionStartAcceptsOurIDs(t *testing.T) {
	bin := liveGate(t)
	client, _ := startServe(t, bin)
	if _, err := msp.Handshake(client, "0.1.0", nil); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	sessionID := msp.NewCommandID()
	params := msp.SessionStartParams{
		CommandID: msp.NewCommandID(),
		SessionID: &sessionID,
	}
	ws := os.Getenv("AF_MUSE_LIVE_WS")
	params.WorkspaceRoot = &ws

	var res msp.SessionStartResult
	if err := client.CallInto(msp.MethodSessionStart, params, 30*time.Second, &res); err != nil {
		t.Fatalf("session/start: %v", err)
	}
	if res.Session.SessionID != sessionID {
		t.Errorf("the host did not take our id: got %q, sent %q", res.Session.SessionID, sessionID)
	}
	// Decision 4: AF stores this path rather than computing it, because the date partition
	// makes discovery a dated guess.
	if res.Session.Path == "" {
		t.Error("session/start returned no transcript path")
	}
	if _, err := os.Stat(filepath.Dir(res.Session.Path)); err != nil {
		t.Errorf("the returned path does not exist: %v", err)
	}
}

// A v4 must be refused. Without this the id test above could pass against a host that
// validated nothing.
func TestLiveSessionStartRefusesANonV7(t *testing.T) {
	bin := liveGate(t)
	client, _ := startServe(t, bin)
	if _, err := msp.Handshake(client, "0.1.0", nil); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	_, err := client.Call(msp.MethodSessionStart, map[string]any{
		"commandId": "f47ac10b-58cc-4372-a567-0e02b2c3d479", // a v4
	}, 30*time.Second)
	if err == nil {
		t.Fatal("the host accepted a v4 commandId; the UUIDv7 assertion above proves nothing")
	}
	if !msp.HasCode(err, msp.ErrCodeInvalidParams) {
		t.Errorf("err = %v, want invalidParams", err)
	}
}
