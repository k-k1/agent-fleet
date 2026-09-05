package sessionx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
)

// Managed-runtime launch failures that waiting does not fix (the shared daemon's auth gate)
// are returned with a different code and a different status from transient ones.
//
// While the two were mixed, not being logged in came back as 502 runtime_failed and the
// Console rendered it as "the agent could not be started, please wait and retry" - telling
// the user to wait for a cause waiting never clears, with the actual cause (not logged in)
// shown nowhere. Dropping the 5xx is not only about wording: the Console's isTransientErr
// reads any 5xx as "a failure worth retrying", so leaving it a 5xx keeps it in the retry set
// however the text is fixed.
func TestWriteRuntimeErrSplitsPermanentFromTransient(t *testing.T) {
	call := func(err error) (int, string, string) {
		rec := httptest.NewRecorder()
		writeRuntimeErr(rec, err)
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if e := json.Unmarshal(rec.Body.Bytes(), &body); e != nil {
			t.Fatalf("body is not the error envelope: %s", rec.Body.String())
		}
		return rec.Code, body.Error.Code, body.Error.Message
	}

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"codex not logged in", codex.ErrNotLoggedIn},
		{"opencode not connected", opencode.ErrNotConnected},
		// Wrapping on the way out does not change the classification (the driver returns
		// some of these wrapped with %w).
		{"wrapped codex not logged in", fmt.Errorf("codex thread の作成に失敗しました: %w", codex.ErrNotLoggedIn)},
	} {
		status, code, msg := call(tc.err)
		if status != http.StatusConflict || code != errCodeAgentNotConnected {
			t.Errorf("%s: status/code = %d/%q, want %d/%q", tc.name, status, code, http.StatusConflict, errCodeAgentNotConnected)
		}
		// A generic code cannot carry the cause, so it must survive in message (the Console
		// shows it alongside as errDetail).
		if msg == "" {
			t.Errorf("%s: message is empty - the cause never reaches the screen", tc.name)
		}
	}

	// Everything else stays "transient". Tip this to the permanent side and waiting for a
	// launch looks like a failure that cannot be retried.
	status, code, msg := call(fmt.Errorf("opencode serve が時間内に起動しませんでした"))
	if status != http.StatusBadGateway || code != "runtime_failed" {
		t.Errorf("transient: status/code = %d/%q, want %d/%q", status, code, http.StatusBadGateway, "runtime_failed")
	}
	if msg != "opencode serve が時間内に起動しませんでした" {
		t.Errorf("transient: message = %q, want the server's own reason", msg)
	}
}
