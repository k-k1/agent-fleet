package mcpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin/memberorigintest"
)

// fakeAnswer replaces the relay's answer with one that records what it was handed.
func fakeAnswer(t *testing.T, ret mcpreg.PendingElicitation, err error) *[]mcpreg.ElicitAnswer {
	t.Helper()
	var got []mcpreg.ElicitAnswer
	old := answerElicitation
	answerElicitation = func(id string, a mcpreg.ElicitAnswer) (mcpreg.PendingElicitation, error) {
		if id != "e1" {
			t.Errorf("answered id %q", id)
		}
		got = append(got, a)
		return ret, err
	}
	t.Cleanup(func() { answerElicitation = old })
	return &got
}

// answerRoute is the route as routes.go registers it: behind the member-origin mark.
func answerRoute() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /mcp-elicitations/answer", memberorigin.Require(ElicitAnswerMaxRequest, http.HandlerFunc(HandleElicitationAnswer)))
	return mux
}

func postAnswer(t *testing.T, s *memberorigintest.Signer, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/mcp-elicitations/answer", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if s != nil {
		s.Sign(r, []byte(body))
	}
	w := httptest.NewRecorder()
	answerRoute().ServeHTTP(w, r)
	return w
}

func TestElicitationsGetListsTheHeldCalls(t *testing.T) {
	old := pendingElicitations
	t.Cleanup(func() { pendingElicitations = old })
	pendingElicitations = func() []mcpreg.PendingElicitation { return []mcpreg.PendingElicitation{} }
	w := httptest.NewRecorder()
	HandleElicitationsGet(w, httptest.NewRequest(http.MethodGet, "/mcp-elicitations", nil))
	// An empty list is [], never null: the Console maps over it.
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"elicitations":[]}` || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}

	pendingElicitations = func() []mcpreg.PendingElicitation {
		return []mcpreg.PendingElicitation{{ID: "e1", ServerID: "mcp-x", ServerName: "gmail", Tool: "send_mail", Session: "fix-mail",
			Deadline: time.Now().Add(90 * time.Second), Arguments: json.RawMessage(`{"to":"a@example.com","to":"b@example.com","n":1.0}`), Inputs: []mcpreg.ElicitInput{{Key: "k", Fields: []mcpreg.ElicitField{}}}}}
	}
	w = httptest.NewRecorder()
	HandleElicitationsGet(w, httptest.NewRequest(http.MethodGet, "/mcp-elicitations", nil))
	var out struct {
		Elicitations []map[string]any `json:"elicitations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Elicitations) != 1 {
		t.Fatalf("%s", w.Body.String())
	}
	e := out.Elicitations[0]
	if e["id"] != "e1" || e["serverId"] != "mcp-x" || e["tool"] != "send_mail" || e["session"] != "fix-mail" ||
		e["arguments"] != `{"to":"a@example.com","to":"b@example.com","n":1.0}` ||
		e["expiresInSec"] == nil || e["expiresInSec"].(float64) < 80 || e["expiresInSec"].(float64) > 90 {
		t.Fatalf("listed %v: the arguments go out as their exact text", e)
	}
}

func TestElicitationAnswerNeedsTheMembersMark(t *testing.T) {
	got := fakeAnswer(t, mcpreg.PendingElicitation{ID: "e1"}, nil)
	memberorigintest.Install(t)
	w := postAnswer(t, nil, `{"id":"e1","action":"accept","choices":{"k":{"f":"ALLOW_ONCE"}}}`)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), memberorigin.CodeRequired) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	// Without the wrapper the handler refuses on its own (a route registered without the mark).
	r := httptest.NewRequest(http.MethodPost, "/mcp-elicitations/answer", strings.NewReader(`{"id":"e1","action":"decline"}`))
	w = httptest.NewRecorder()
	HandleElicitationAnswer(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("unwrapped: %d %s", w.Code, w.Body.String())
	}
	if len(*got) != 0 {
		t.Fatalf("an unmarked request reached the relay: %+v", *got)
	}
}

func TestElicitationAnswerHandsTheChoiceToTheRelay(t *testing.T) {
	held := mcpreg.PendingElicitation{ID: "e1", ServerID: "mcp-x", ServerName: "gmail", Tool: "send_mail", Session: "fix-mail",
		Arguments: json.RawMessage(`{"to":"secret@example.com"}`)}
	got := fakeAnswer(t, held, nil)
	s := memberorigintest.Install(t)

	w := postAnswer(t, s, `{"id":"e1","action":"accept","choices":{"k":{"f":"ALLOW_ONCE"}}}`)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	// The answer names the call for the audit and nothing of its content.
	if strings.TrimSpace(w.Body.String()) != `{"id":"e1","serverId":"mcp-x","tool":"send_mail","session":"fix-mail","action":"accept"}` {
		t.Fatalf("answered %s", w.Body.String())
	}
	// "" is a choice like any other when the server offers it.
	if w := postAnswer(t, s, `{"id":"e1","action":"accept","choices":{"k":{"f":""},"j":null}}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	w = postAnswer(t, s, `{"id":"e1","action":"decline"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"action":"decline"`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	want := []mcpreg.ElicitAnswer{
		{Choices: map[string]map[string]string{"k": {"f": "ALLOW_ONCE"}}},
		{Choices: map[string]map[string]string{"k": {"f": ""}, "j": nil}},
		{Decline: true},
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("relay got %+v", *got)
	}
}

func TestElicitationAnswerRefusals(t *testing.T) {
	s := memberorigintest.Install(t)
	for _, tc := range []struct {
		name, body string
		err        error
		status     int
		code       string
		reaches    bool
	}{
		{"no action", `{"id":"e1","choices":{"k":{}}}`, nil, 400, "bad_request", false},
		{"unknown action", `{"id":"e1","action":"allow"}`, nil, 400, "bad_request", false},
		{"decline with choices", `{"id":"e1","action":"decline","choices":{"k":{"f":"ALLOW_ONCE"}}}`, nil, 400, "bad_request", false},
		{"not JSON", `{`, nil, 400, "bad_request", false},
		{"no id", `{"action":"decline"}`, nil, 400, "bad_request", false},
		// A null is not the empty string a server may offer as a choice.
		{"null choice", `{"id":"e1","action":"accept","choices":{"k":{"f":null}}}`, nil, 400, "bad_request", false},
		{"gone", `{"id":"e1","action":"decline"}`, mcpreg.ErrElicitationNotFound, 404, errCodeElicitGone, true},
		{"not a choice", `{"id":"e1","action":"accept","choices":{"k":{"f":"NOPE"}}}`, mcpreg.ErrElicitationAnswer, 400, errCodeElicitAnswer, true},
		{"other", `{"id":"e1","action":"decline"}`, errors.New("boom"), 500, "internal", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := fakeAnswer(t, mcpreg.PendingElicitation{}, tc.err)
			w := postAnswer(t, s, tc.body)
			var out struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &out)
			if w.Code != tc.status || out.Error.Code != tc.code {
				t.Fatalf("%d %s, want %d %s", w.Code, w.Body.String(), tc.status, tc.code)
			}
			if (len(*got) > 0) != tc.reaches {
				t.Fatalf("reached the relay: %v", *got)
			}
		})
	}
}
