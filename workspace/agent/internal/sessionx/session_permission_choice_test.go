package sessionx

// The create-time gate behind Caps.PermissionChoice (docs/log/76): "ask for tool approval" is
// refused for a kind that cannot surface approvals to the Console, because ignoring it would
// leave the caller believing the session runs with approvals on.
//
// Nothing tested it until muse needed it. ADR 0095 P2-6 measured that a muse session raises no
// approval at all — the sandbox waiver gate A forces leaves the host's filesystem unrestricted,
// so every tool call is policy-allowed before the approval layer — which made muse the first
// kind to move from PermissionChoice=true to false, and this the check that the flag reaches the
// route rather than only the capability table.
//
// The claude arm is the control: without it, a gate that refused EVERY kind would pass just as
// well, and the refusal is supposed to be per-kind.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// createResult is the status plus the stable error code, which is what this gate is about —
// the control is expected to fail LATER for reasons that are not this test's business.
type createResult struct {
	code    int
	errCode string
	body    string
}

func postCreate(t *testing.T, srv *httptest.Server, body any) createResult {
	t.Helper()
	code, raw := roundtrip(t, srv, "POST", "/sessions", body)
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &env)
	return createResult{code: code, errCode: env.Error.Code, body: string(raw)}
}

func TestCreateRefusesApprovalsOnlyForKindsThatCannotShowThem(t *testing.T) {
	for _, tc := range []struct {
		kind string
		want int
	}{
		// muse: approvals cannot fire at all in a Workspace, so asking for them is refused.
		{"muse", http.StatusBadRequest},
		// codex and opencode have never had the cap; they share the refusal.
		{"codex", http.StatusBadRequest},
		// claude can answer an approval from the Console, so the same body must get past this
		// gate — a refusal here would mean the branch fires for everyone.
		{"claude", 0},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			isolateAgentState(t)
			mux := http.NewServeMux()
			mux.HandleFunc("POST /sessions", HandleCreateSession)
			srv := httptest.NewServer(mux)
			defer srv.Close()

			skip := false
			body := map[string]any{"dir": t.TempDir(), "kind": tc.kind, "skip_permissions": &skip}
			rec := postCreate(t, srv, body)
			if tc.want == http.StatusBadRequest {
				if rec.code != http.StatusBadRequest || rec.errCode != "permission_choice_unsupported" {
					t.Fatalf("%s: status=%d code=%q body=%s", tc.kind, rec.code, rec.errCode, rec.body)
				}
				return
			}
			// The control only has to get PAST this gate; what it fails on afterwards (a missing
			// CLI, tmux, a clone) is not this test's business.
			if rec.errCode == "permission_choice_unsupported" {
				t.Fatalf("%s was refused by the permission-choice gate: %s", tc.kind, rec.body)
			}
		})
	}
}
