package chatx

// Unit contract for the muse assistant-chat provider: the JSONL reader and the model gate.
// The end-to-end half (a real reply, --session-id continuity) is the live test in
// chat_providers_muse_live_test.go, which spends subscription quota and is gated on MUSE_LIVE=1.

import (
	"slices"
	"strings"
	"testing"
)

func TestParseMuseExecEventsReadsReplyAndSessionID(t *testing.T) {
	out := []byte(`{"stream":{"kind":"run","id":"r1"},"payload_type":"run.lifecycle.started","payload":{}}
{"stream":{"kind":"session","id":"01a0c951-7372-7661-8514-448b28121d7d"},"payload_type":"run.output.delta","payload":{"text":"PO"}}
{"stream":{"kind":"session","id":"01a0c951-7372-7661-8514-448b28121d7d"},"payload_type":"run.output.delta","payload":{"text":"NG"}}
{"stream":{"kind":"session","id":"01a0c951-7372-7661-8514-448b28121d7d"},"payload_type":"run.terminal.completed","payload":{"terminal":"completed","text":"PONG","reason":null}}
`)
	reply, sid, execErr := parseMuseExecEvents(out)
	if reply != "PONG" {
		t.Errorf("reply = %q, want PONG", reply)
	}
	if sid != "01a0c951-7372-7661-8514-448b28121d7d" {
		t.Errorf("session id = %q", sid)
	}
	if execErr != "" {
		t.Errorf("execErr = %q, want empty", execErr)
	}
}

// The session id must come from a stream.kind=="session" event and from nothing else: the run
// stream carries an id too, and using it would hand --session-id a value muse refuses.
func TestParseMuseExecEventsIgnoresTheRunStreamID(t *testing.T) {
	out := []byte(`{"stream":{"kind":"run","id":"00f5bef4-0cd4-4eed-9052-97f390dcabfe"},"payload_type":"run.output.delta","payload":{"text":"hi"}}
`)
	_, sid, _ := parseMuseExecEvents(out)
	if sid != "" {
		t.Errorf("session id = %q, want empty (that id belongs to the run stream)", sid)
	}
}

// A lost delta must not truncate the answer: the terminal text is authoritative.
func TestParseMuseExecEventsPrefersTheTerminalText(t *testing.T) {
	out := []byte(`{"stream":{"kind":"session","id":"s"},"payload_type":"run.output.delta","payload":{"text":"half"}}
{"stream":{"kind":"session","id":"s"},"payload_type":"run.terminal.completed","payload":{"terminal":"completed","text":"the whole answer"}}
`)
	reply, _, _ := parseMuseExecEvents(out)
	if reply != "the whole answer" {
		t.Errorf("reply = %q, want the terminal text", reply)
	}
}

// Deltas are the fallback, not the primary: a terminal event with no text still has to answer.
func TestParseMuseExecEventsFallsBackToTheDeltas(t *testing.T) {
	out := []byte(`{"stream":{"kind":"session","id":"s"},"payload_type":"run.output.delta","payload":{"text":"a"}}
{"stream":{"kind":"session","id":"s"},"payload_type":"run.output.delta","payload":{"text":"b"}}
{"stream":{"kind":"session","id":"s"},"payload_type":"run.terminal.completed","payload":{"terminal":"completed","text":""}}
`)
	reply, _, _ := parseMuseExecEvents(out)
	if reply != "ab" {
		t.Errorf("reply = %q, want the joined deltas", reply)
	}
}

// A failed turn must surface as an error even though the deltas before it look like an answer.
func TestParseMuseExecEventsReportsAFailedTerminal(t *testing.T) {
	out := []byte(`{"stream":{"kind":"session","id":"s"},"payload_type":"run.output.delta","payload":{"text":"partial"}}
{"stream":{"kind":"session","id":"s"},"payload_type":"run.terminal.completed","payload":{"terminal":"failed","reason":"authRequired"}}
{"not json at all`)
	_, _, execErr := parseMuseExecEvents(out)
	if execErr != "authRequired" {
		t.Errorf("execErr = %q, want authRequired", execErr)
	}
}

func TestParseMuseExecEventsNamesTheTerminalWhenThereIsNoReason(t *testing.T) {
	out := []byte(`{"stream":{"kind":"session","id":"s"},"payload_type":"run.terminal.completed","payload":{"terminal":"cancelled","reason":null}}`)
	_, _, execErr := parseMuseExecEvents(out)
	if !strings.Contains(execErr, "cancelled") {
		t.Errorf("execErr = %q, want it to name the terminal", execErr)
	}
}

// 🔴 Clamp 8. A turn whose model the member never pinned must not fall through to `muse exec`'s
// own default, which is the catalog's `-contributor` row. With no catalog to resolve a safe
// model from, the turn is refused — returning "" here would send an exec with no --model.
func TestMuseChatModelRefusesRatherThanRunOnTheHostDefault(t *testing.T) {
	t.Setenv("AGENT_MUSE_BIN", "/nonexistent/muse") // muse.Installed() false: nothing is spawned
	c := &ChatConversation{ID: "c", Agent: "muse"}
	model, err := museChatModel(c)
	if err == nil {
		t.Fatalf("museChatModel returned %q and no error; an unresolvable model must refuse the turn", model)
	}
	if model != "" {
		t.Errorf("model = %q on the refusal path", model)
	}
}

func TestMuseChatModelKeepsTheMemberSChoice(t *testing.T) {
	t.Setenv("AGENT_MUSE_BIN", "/nonexistent/muse")
	// The contributor twin is a legitimate choice — the refusal above is about AF choosing it
	// for someone who chose nothing, not about forbidding the model.
	c := &ChatConversation{ID: "c", Agent: "muse", Model: "muse-spark-1.3-contributor"}
	model, err := museChatModel(c)
	if err != nil {
		t.Fatalf("museChatModel: %v", err)
	}
	if model != "muse-spark-1.3-contributor" {
		t.Errorf("model = %q, want the pinned one", model)
	}
}

// withMuseHidden gives the chat two safe muse rows (newest first) and hides the given ids through
// the VisibleModel seam, the way the saved hidden-models setting does in production.
func withMuseHidden(t *testing.T, hidden ...string) {
	t.Helper()
	prev := museSafeModels
	museSafeModels = func() []string { return []string{"muse-spark-1.3", "muse-spark-1.2"} }
	d := testDeps()
	d.VisibleModel = func(_, model string) string {
		if slices.Contains(hidden, model) {
			return ""
		}
		return model
	}
	Configure(d)
	t.Cleanup(func() {
		museSafeModels = prev
		Configure(testDeps())
	})
}

// #1023 item 4: hiding the safe default moves the chat to the next safe row, and the
// recommendation the Console shows names that same row.
func TestMuseChatModelSkipsAHiddenSafeDefault(t *testing.T) {
	withMuseHidden(t, "muse-spark-1.3")
	model, err := museChatModel(&ChatConversation{ID: "c", Agent: "muse"})
	if err != nil || model != "muse-spark-1.2" {
		t.Fatalf("museChatModel = %q, %v; want the next safe row muse-spark-1.2", model, err)
	}
	if got := RecommendedModelsWithHidden("muse", []string{"muse-spark-1.3"}, nil).Chat; got != "muse-spark-1.2" {
		t.Errorf("recommended chat = %q, want muse-spark-1.2", got)
	}
}

// Every safe row hidden: refuse, never a contributor row or no --model.
func TestMuseChatModelRefusesWhenEverySafeRowIsHidden(t *testing.T) {
	withMuseHidden(t, "muse-spark-1.3", "muse-spark-1.2")
	if model, err := museChatModel(&ChatConversation{ID: "c", Agent: "muse"}); err == nil {
		t.Fatalf("museChatModel = %q and no error; every safe row is hidden", model)
	}
}

// A conversation pinned to a model the member later hid is refused, as the launch guard
// refuses it for a session.
func TestMuseChatModelRefusesAHiddenPinnedModel(t *testing.T) {
	withMuseHidden(t, "muse-spark-1.3-contributor")
	c := &ChatConversation{ID: "c", Agent: "muse", Model: "muse-spark-1.3-contributor"}
	if model, err := museChatModel(c); err == nil {
		t.Fatalf("museChatModel = %q and no error; the pinned model is hidden", model)
	}
}

// The argv the chat turn is built on. Each flag is a clamp with a reason in the source; a
// mutation that drops one leaves every other test in this package green.
func TestMuseChatBaseArgsCarryTheHeadlessClamps(t *testing.T) {
	args := museChatBaseArgs()
	if args[0] != "exec" {
		t.Errorf("argv does not start with exec: %v", args)
	}
	for _, want := range []string{
		"--json",
		"--disable-shell",
		"--disable-write",
		"--disable-web-tools",
		"--no-foreign-personal-context",
		"--approval-mode",
		"--approval-judge",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("argv is missing %s: %v", want, args)
		}
	}
	if i := slices.Index(args, "--approval-mode"); i < 0 || i+1 >= len(args) || args[i+1] != "never" {
		t.Errorf("--approval-mode is not never: %v", args)
	}
}
