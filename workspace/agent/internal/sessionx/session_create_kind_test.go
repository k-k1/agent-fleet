package sessionx

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The create paths refuse an unregistered kind instead of folding it to claude (#2297). The
// spellings are the ones a caller actually gets wrong: wrong case, padding, a CLI's binary name,
// a product name.
var unregisteredKinds = []string{"Codex", " codex", "kiro-cli", "llamacpp", "CLAUDE", "claude "}

func TestValidateCreateKind(t *testing.T) {
	for _, k := range append(RegisteredKinds(), "") {
		if err := ValidateCreateKind(k); err != nil {
			t.Errorf("ValidateCreateKind(%q) = %v, want nil", k, err)
		}
	}
	for _, k := range unregisteredKinds {
		err := ValidateCreateKind(k)
		if err == nil {
			t.Errorf("ValidateCreateKind(%q) = nil, want a refusal", k)
			continue
		}
		for _, reg := range RegisteredKinds() {
			if !strings.Contains(err.Error(), reg) {
				t.Errorf("ValidateCreateKind(%q) = %q, does not list %q", k, err, reg)
			}
		}
	}
	// The read side keeps its fallback: the validator must not have changed it.
	for _, k := range append([]string{""}, unregisteredKinds...) {
		if got := NormalizeKind(k); got != "claude" {
			t.Errorf("NormalizeKind(%q) = %q, want the claude fallback", k, got)
		}
	}
}

func postCreateKind(t *testing.T, kind string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"dir": filepath.Join(t.TempDir(), "absent"), "kind": kind})
	rec := httptest.NewRecorder()
	HandleCreateSession(rec, httptest.NewRequest(http.MethodPost, "/sessions", bytes.NewReader(body)))
	return rec.Code, rec.Body.String()
}

// POST /sessions is where every caller's create ends up (Console, CP MCP, scheduler, in-container
// MCP), so the refusal is held here with a status, a code and the accepted kinds.
func TestCreateSessionRefusesUnregisteredKind(t *testing.T) {
	isolateAgentState(t)
	for _, kind := range unregisteredKinds {
		code, body := postCreateKind(t, kind)
		if code != http.StatusBadRequest || !strings.Contains(body, `"bad_kind"`) {
			t.Errorf("kind %q: %d %s, want 400 bad_kind", kind, code, body)
		}
		if !strings.Contains(body, "accepted kinds") || !strings.Contains(body, "codex") {
			t.Errorf("kind %q: %s does not name the accepted kinds", kind, body)
		}
	}
	// Nothing was written: the refusal precedes the idempotency claim and every side effect.
	if ents, _ := os.ReadDir(os.Getenv("AF_SESSIONS_DIR")); len(ents) != 0 {
		t.Errorf("a refused create left %d entries in the sessions dir", len(ents))
	}
}

// A registered kind and the empty kind (= claude) get past the kind gate. The dir does not
// exist, so the create stops on a later, different refusal — which is all this asserts.
func TestCreateSessionAcceptsRegisteredAndEmptyKind(t *testing.T) {
	isolateAgentState(t)
	for _, kind := range []string{"", "claude", "codex", "shell"} {
		code, body := postCreateKind(t, kind)
		if strings.Contains(body, `"bad_kind"`) {
			t.Errorf("kind %q refused as bad_kind: %d %s", kind, code, body)
		}
	}
}
