package chatx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"
)

// askSpy records the conversation the handler hands to a provider and answers a fixed reply.
type askSpy struct{ got []*ChatConversation }

func (s *askSpy) Send(_ context.Context, c *ChatConversation, _ string) (string, error) {
	cp := *c
	s.got = append(s.got, &cp)
	return "OK", nil
}

// postChatAsk drives the real handler with a raw JSON body. Every provider kind is swapped for
// the spy, so whichever kind the handler picks, no real CLI runs.
func postChatAsk(t *testing.T, body string) (*httptest.ResponseRecorder, *askSpy) {
	t.Helper()
	withTempHome(t)
	spy := &askSpy{}
	for k := range ChatProviders {
		stubChatProvider(t, k, spy)
	}
	req := httptest.NewRequest(http.MethodPost, "/chat/ask", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	HandleChatAsk(rr, req)
	return rr, spy
}

// The Console's one-shot callers send a prompt and no assistant. That has to be a plain advisory
// turn: a 404 here silently breaks the memo Tidy dialogs and the spoken summary, and the
// Console's own tests stub the call.
func TestHandleChatAskWithoutAssistantIsAPlainAdvisoryTurn(t *testing.T) {
	for name, body := range map[string]string{
		"field absent": `{"prompt":"Reply with the single word OK."}`,
		"empty string": `{"prompt":"Reply with the single word OK.","assistant":""}`,
		"blank":        `{"prompt":"Reply with the single word OK.","assistant":"  "}`,
	} {
		t.Run(name, func(t *testing.T) {
			rr, spy := postChatAsk(t, body)
			if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"reply":"OK"`) {
				t.Fatalf("code = %d body = %s, want 200 with the reply", rr.Code, rr.Body.String())
			}
			if len(spy.got) != 1 {
				t.Fatalf("provider calls = %d, want 1", len(spy.got))
			}
			c := spy.got[0]
			if c.Persona != "" || len(c.Knowledge) != 0 || c.Tools != assistants.ToolsNone {
				t.Fatalf("conversation carries persona=%q knowledge=%v tools=%q, want none of them", c.Persona, c.Knowledge, c.Tools)
			}
			if c.Agent == "" || c.Model == "" {
				t.Fatalf("agent=%q model=%q, want the default headless agent and its default model", c.Agent, c.Model)
			}
		})
	}
}

func TestHandleChatAskUnknownAssistantIsNotFound(t *testing.T) {
	rr, spy := postChatAsk(t, `{"prompt":"hi","assistant":"no-such-assistant"}`)
	if rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), "chat_assistant_not_found") {
		t.Fatalf("code = %d body = %s, want 404 chat_assistant_not_found", rr.Code, rr.Body.String())
	}
	if len(spy.got) != 0 {
		t.Fatalf("provider was called %d times for an unknown assistant", len(spy.got))
	}
}

func TestHandleChatAskEmptyPromptIsBadRequest(t *testing.T) {
	rr, spy := postChatAsk(t, `{"prompt":"  ","assistant":"af"}`)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "chat_prompt_empty") {
		t.Fatalf("code = %d body = %s, want 400 chat_prompt_empty", rr.Code, rr.Body.String())
	}
	if len(spy.got) != 0 {
		t.Fatalf("provider was called for an empty prompt")
	}
}

// A named builtin still carries its persona, so the plain turn is not the only path.
func TestHandleChatAskNamedAssistantKeepsItsPersona(t *testing.T) {
	rr, spy := postChatAsk(t, `{"prompt":"hi","assistant":"af"}`)
	if rr.Code != http.StatusOK || len(spy.got) != 1 {
		t.Fatalf("code = %d body = %s calls = %d", rr.Code, rr.Body.String(), len(spy.got))
	}
	if spy.got[0].Persona == "" || spy.got[0].Tools != assistants.ToolsNone {
		t.Fatalf("persona=%q tools=%q, want the af persona with tools none", spy.got[0].Persona, spy.got[0].Tools)
	}
}
