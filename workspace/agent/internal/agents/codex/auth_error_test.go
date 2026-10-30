package codex

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// revokedLoginMsg is codex's own wording for a ChatGPT login revoked server side, as a Managed
// and a Terminal turn both ended with it (codex 0.166.0, 2026-10-30).
const revokedLoginMsg = "Your access token could not be refreshed because your refresh token was revoked. Please log out and sign in again."

// Only an auth-classified error part makes the Console offer its re-authentication route, so
// these pin both directions: the revoked login is auth whichever field carries the signal, and
// failures a new sign-in cannot fix are not.
func TestCodexErrorAuthClassification(t *testing.T) {
	for _, c := range []struct {
		name string
		e    codexError
		auth bool
	}{
		{"revoked login, app-server", codexError{label: "unauthorized", message: revokedLoginMsg}, true},
		{"revoked wording without a variant", codexError{message: revokedLoginMsg}, true},
		{"expired refresh token", codexError{label: "other", message: "Your access token could not be refreshed because your refresh token has expired. Please log out and sign in again."}, true},
		{"HTTP 401", codexError{label: "httpConnectionFailed", status: 401, message: "connect failed"}, true},
		{"usage limit", codexError{label: "usageLimitExceeded", message: "You've hit your usage limit."}, false},
		{"HTTP 403", codexError{label: "httpConnectionFailed", status: 403, message: "forbidden"}, false},
		{"server overloaded", codexError{label: "serverOverloaded", message: "Service is overloaded, try again later"}, false},
		{"context window", codexError{label: "contextWindowExceeded", message: "Your input exceeds the context window of this model."}, false},
	} {
		want := ""
		if c.auth {
			want = transcript.CauseAuth
		}
		if got := c.e.part().Cause; got != want {
			t.Errorf("%s: part().Cause = %q, want %q", c.name, got, want)
		}
	}
}

// The Managed path: turn/completed's TurnError for the revoked login decodes to an auth part.
func TestDecodeCodexErrorRevokedLoginIsAuth(t *testing.T) {
	e, ok := decodeCodexError([]byte(`{"message":"` + revokedLoginMsg + `","additionalDetails":null,"codexErrorInfo":"unauthorized"}`))
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if got, want := e.summary(), "[error] unauthorized: "+revokedLoginMsg; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	if p := e.part(); p.Kind != "error" || p.Cause != transcript.CauseAuth {
		t.Fatalf("part = %+v, want an auth error part", p)
	}
}

func taskFailed(ts, id, errJSON string) []byte {
	ln := `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"task_complete","turn_id":"` + id + `","last_agent_message":null`
	if errJSON != "" {
		ln += `,"error":` + errJSON
	}
	return []byte(ln + "}}")
}

// The Terminal path: codex keeps error events out of the rollout, so a failed turn's only
// trace is task_complete's `error`. It must render as an error turn of that turn; a turn that
// completed cleanly must add nothing.
func TestRolloutTaskCompleteErrorRendersAsErrorTurn(t *testing.T) {
	p := newRolloutParser()
	for _, ln := range [][]byte{
		taskStarted("2026-10-30T02:00:00.000Z", "t1"),
		userSays("2026-10-30T02:00:00.100Z", "hello"),
		taskComplete("2026-10-30T02:00:01.000Z", "t1"),
		taskStarted("2026-10-30T02:01:00.000Z", "t2"),
		userSays("2026-10-30T02:01:00.100Z", "again"),
		taskFailed("2026-10-30T02:01:00.900Z", "t2", `{"message":"`+revokedLoginMsg+`","codex_error_info":"unauthorized"}`),
		taskStarted("2026-10-30T02:02:00.000Z", "t3"),
		userSays("2026-10-30T02:02:00.100Z", "once more"),
		taskFailed("2026-10-30T02:02:00.900Z", "t3", `{"message":"stream disconnected","codex_error_info":{"response_stream_disconnected":{"http_status_code":502}}}`),
	} {
		p.feed(ln)
	}
	turns, _, _, _ := p.snapshot(time.Time{})
	var errs []transcript.Turn
	for _, tn := range turns {
		if len(tn.Parts) == 1 && tn.Parts[0].Kind == "error" {
			errs = append(errs, tn)
		}
	}
	if len(errs) != 2 {
		t.Fatalf("error turns = %d, want 2 (t2, t3): %+v", len(errs), turns)
	}
	auth := errs[0]
	if auth.Role != "assistant" || auth.AnchorID != "t2" || auth.Parts[0].Info != "unauthorized" ||
		auth.Parts[0].Text != revokedLoginMsg || auth.Parts[0].Cause != transcript.CauseAuth {
		t.Fatalf("revoked-login turn = %+v", auth)
	}
	if got, want := auth.Text, "[error] unauthorized: "+revokedLoginMsg; got != want {
		t.Fatalf("revoked-login text = %q, want %q", got, want)
	}
	other := errs[1].Parts[0]
	if other.Info != "response_stream_disconnected (HTTP 502)" || other.Cause != "" {
		t.Fatalf("stream error part = %+v, want HTTP 502 and no cause", other)
	}
}

// The Managed transcript is the same rollout plus a synthetic copy of the last failure. Once
// the rollout's own record of that turn has landed, the copy must step aside; a turn/start
// rejection (no turn id, nothing in the rollout) keeps it.
func TestRolloutShowsError(t *testing.T) {
	turns := []transcript.Turn{
		{Role: "user", AnchorID: "t2", Text: "again"},
		{Role: "assistant", AnchorID: "t2", Parts: []transcript.Part{{Kind: "error", Text: revokedLoginMsg}}},
	}
	if !rolloutShowsError(turns, "t2") {
		t.Fatal("the rollout's error turn for t2 was not recognized")
	}
	if rolloutShowsError(turns[:1], "t2") {
		t.Fatal("a user turn alone counted as the error record")
	}
	if rolloutShowsError(turns, "t3") {
		t.Fatal("another turn's error counted")
	}
	if rolloutShowsError(turns, "") {
		t.Fatal("a turn/start rejection (no turn id) must keep its synthetic turn")
	}
}
