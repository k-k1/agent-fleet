package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"
)

// TestAssistantInputErrorsAreCodes pins that every applyInput refusal reaches the Console as
// its own err.<code> (localized there) rather than one generic code with a fixed-language
// message, on both create and update. The catalogue side is errcodes_catalog_test.go.
func TestAssistantInputErrorsAreCodes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	existing := &assistants.Assistant{ID: "0b6f3c1e-8d2a-4e5b-9c7f-1a2b3c4d5e6f", Name: "x", Agent: "claude", Tools: assistants.ToolsNone}
	if err := assistants.SaveUser(existing); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, body, code, inMsg, integration string
	}{
		{"blank name", `{"name":"  ","agent":"claude"}`, errCodeAssistantNameRequired, "", ""},
		{"unknown agent", `{"name":"a","agent":"nope"}`, errCodeAssistantAgentUnsupported, "nope", ""},
		{"unknown tools", `{"name":"a","agent":"claude","tools":"root"}`, errCodeAssistantToolsUnsupported, "root", ""},
		{"unknown integration", `{"name":"a","agent":"claude","integrations":["no-such-mcp"]}`, errCodeAssistantIntegrationUnsupported, "no-such-mcp", "no-such-mcp"},
	}
	for _, c := range cases {
		for _, method := range []string{"POST", "PUT"} {
			t.Run(c.name+"/"+method, func(t *testing.T) {
				r := httptest.NewRequest(method, "/api/assistants", strings.NewReader(c.body))
				w := httptest.NewRecorder()
				if method == "POST" {
					handleAssistantCreate(w, r)
				} else {
					r.SetPathValue("id", existing.ID)
					handleAssistantUpdate(w, r)
				}
				if w.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body)
				}
				var got struct {
					Error struct{ Code, Message, Integration string }
				}
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got.Error.Code != c.code {
					t.Errorf("code = %q, want %q", got.Error.Code, c.code)
				}
				if !strings.Contains(got.Error.Message, c.inMsg) {
					t.Errorf("message %q does not name %q", got.Error.Message, c.inMsg)
				}
				if got.Error.Integration != c.integration {
					t.Errorf("integration = %q, want %q", got.Error.Integration, c.integration)
				}
				for _, r := range got.Error.Message {
					if r > 0x7f {
						t.Fatalf("developer message is not English/ASCII: %q", got.Error.Message)
					}
				}
			})
		}
	}

	loaded, err := assistants.LoadUser(existing.ID)
	if err != nil || loaded.Name != "x" {
		t.Fatalf("a refused update must leave the stored assistant alone: %+v, %v", loaded, err)
	}
}
