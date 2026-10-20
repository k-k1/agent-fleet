package chatx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
)

// An assistant chat turn runs the opt-in's opencode 2 binary, with its own database, once the
// workspace has switched; without the switch it runs the `opencode` on PATH as before.
func TestOpencodeChatFollowsTheOptIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(opencode.MajorEnv, "")
	prev := opencode.UsagePref
	opencode.UsagePref = func() string { return opencode.UsageFree }
	t.Cleanup(func() { opencode.UsagePref = prev })

	// A recorder script: writes "<who> <OPENCODE_DB>" to the log, answers with one text event.
	log := filepath.Join(home, "ran.log")
	script := func(who string) string {
		return "#!/bin/sh\necho \"" + who + " ${OPENCODE_DB:-none}\" >> " + log +
			"\necho '{\"type\":\"text\",\"sessionID\":\"ses_1\",\"part\":{\"type\":\"text\",\"text\":\"ok\"}}'\n"
	}
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte(script("v1")), 0o755); err != nil {
		t.Fatal(err)
	}
	v2dir := filepath.Join(opencode.V2Prefix(), "node_modules", "@opencode", "cli", "bin")
	if err := os.MkdirAll(v2dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v2dir, "opencode.exe"), []byte(script("v2")), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// The opt-in only exists next to a 1.x on PATH. Without a manifest beside the stub, the major
	// falls back to the host image's pin and a 2.x image makes the whole opt-in inert.
	if err := os.WriteFile(filepath.Join(home, "package.json"), []byte(`{"name":"opencode-ai","version":"1.19.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(opencode.SetBinaryForTest(filepath.Join(bin, "opencode")))

	send := func() string {
		c := &ChatConversation{ID: "c1"}
		if _, err := (opencodeChat{}).Send(context.Background(), c, "hi"); err != nil {
			t.Fatalf("send: %v", err)
		}
		b, _ := os.ReadFile(log)
		_ = os.Remove(log)
		return strings.TrimSpace(string(b))
	}

	if got := send(); got != "v1 none" {
		t.Fatalf("opt-in off: ran %q, want the PATH opencode without OPENCODE_DB", got)
	}
	if err := opencode.SetWantsV2(true); err != nil {
		t.Fatal(err)
	}
	if got, want := send(), "v2 "+opencode.V2DBPath(); got != want {
		t.Fatalf("opt-in on: ran %q, want %q", got, want)
	}
}

// opencode 2 reports a failed turn as {"type":"error","error":{"type":"…","message":"…"}} (measured
// 2.0.29); 1.x nests it under error.data. Both must reach the user as the turn error.
func TestParseOpencodeRunEventsReadsTheV2ErrorShape(t *testing.T) {
	_, _, _, got, _ := parseOpencodeRunEvents([]byte(`{"type":"error","sessionID":"ses_1","error":{"type":"compaction.failed","message":"Compaction produced no summary"}}` + "\n"))
	if got != "Compaction produced no summary" {
		t.Fatalf("v2 error = %q", got)
	}
	_, _, _, got, _ = parseOpencodeRunEvents([]byte(`{"type":"error","error":{"name":"APIError","data":{"message":"boom","ref":"r1"}}}` + "\n"))
	if got != "boom (ref: r1)" {
		t.Fatalf("v1 error = %q", got)
	}
}
