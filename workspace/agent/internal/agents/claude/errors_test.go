package claude

import "testing"

// A measured record (2026-08-06, transcript corpus). This one line is the whole body of an
// expired-login failure; leaning the decision on the wording alone breaks across versions, so
// `error` and apiErrorStatus are read alongside it.
const authErrText = "Please run /login · API Error: 401 OAuth access token has expired. Re-authenticate to continue."

func TestAPIErrorAuthRecord(t *testing.T) {
	e := apiError{msg: authErrText, kind: "authentication_failed", status: 401}
	if got, want := e.label(), "authentication_failed (HTTP 401)"; got != want {
		t.Errorf("label = %q, want %q", got, want)
	}
	if got := e.detail(); got != authErrText {
		t.Errorf("detail = %q, want the provider text verbatim", got)
	}
	if got, want := e.cause(), "auth"; got != want {
		t.Errorf("cause = %q, want %q", got, want)
	}
	if got, want := e.summary(), "[error] authentication_failed (HTTP 401): "+authErrText; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
	p := e.part()
	if p.Kind != "error" || p.Info != "authentication_failed (HTTP 401)" || p.Text != authErrText || p.Cause != "auth" {
		t.Errorf("part = %+v", p)
	}
}

// Auth is raised when any one of the three entry points (`error` / 401 / the wording) matches.
// This pins that the re-authentication route survives a version that drops one of the fields.
func TestAPIErrorAuthEntries(t *testing.T) {
	cases := []struct {
		name string
		e    apiError
		want bool
	}{
		{"kind only", apiError{kind: "authentication_failed"}, true},
		{"kind api-style", apiError{kind: "authentication_error"}, true},
		{"status only", apiError{msg: "something went wrong", status: 401}, true},
		{"text only", apiError{msg: authErrText}, true},
		{"invalid api key", apiError{msg: "API Error: Invalid API key · Please run /login"}, true},
		// No re-authentication route for a failure it does not fix - the same real harm as
		// making someone wait for a reset that never comes.
		{"usage limit", apiError{msg: "You've hit your session limit · resets 9:30am (Asia/Tokyo)", kind: "rate_limit", status: 429}, false},
		{"prompt too long", apiError{msg: "Prompt is too long · the request is ~242785 tokens", kind: "invalid_request", status: 400}, false},
		{"server error", apiError{msg: "API Error: 529 Overloaded.", kind: "server_error", status: 529}, false},
		{"forbidden is not auth", apiError{msg: "permission denied", status: 403}, false},
	}
	for _, c := range cases {
		if got := c.e.isAuth(); got != c.want {
			t.Errorf("%s: isAuth = %v, want %v", c.name, got, c.want)
		}
		wantCause := ""
		if c.want {
			wantCause = "auth"
		}
		if got := c.e.cause(); got != wantCause {
			t.Errorf("%s: cause = %q, want %q", c.name, got, wantCause)
		}
	}
}

// A record with an empty body still keeps its label: drop the part and parseTurn's "nothing
// to show" branch drops the whole turn, making the failure invisible again.
func TestAPIErrorEmptyMessageKeepsLabel(t *testing.T) {
	e := apiError{kind: "server_error"}
	if got, want := e.detail(), "server_error"; got != want {
		t.Errorf("detail = %q, want %q", got, want)
	}
	if got, want := e.summary(), "[error] server_error"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
	e2 := apiError{}
	if got, want := e2.label(), "error"; got != want {
		t.Errorf("bare label = %q, want %q", got, want)
	}
}

// parseTurn side: a synthetic record becomes an error part rather than a text part (the same
// bubble as an ordinary answer), and the flattened form carries `[error]` too. Regress here
// and the mirror shows a failure as an answer.
func TestParseTurnAPIErrorBecomesErrorPart(t *testing.T) {
	line := []byte(`{"type":"assistant","timestamp":"2026-08-06T22:12:46.526Z","isApiErrorMessage":true,` +
		`"apiErrorStatus":401,"error":"authentication_failed","message":{"model":"<synthetic>",` +
		`"content":[{"type":"text","text":"` + authErrText + `"}]}}`)
	tn, ok := parseTurn(line, 9)
	if !ok {
		t.Fatal("parseTurn ok = false, want true")
	}
	if len(tn.Parts) != 1 {
		t.Fatalf("parts = %+v, want exactly one error part", tn.Parts)
	}
	p := tn.Parts[0]
	if p.Kind != "error" || p.Cause != "auth" || p.Text != authErrText {
		t.Errorf("part = %+v", p)
	}
	if p.Info != "authentication_failed (HTTP 401)" {
		t.Errorf("info = %q", p.Info)
	}
	if tn.Text != "[error] authentication_failed (HTTP 401): "+authErrText {
		t.Errorf("flattened text = %q", tn.Text)
	}
	if tn.Role != "assistant" || tn.Idx != 9 {
		t.Errorf("turn = %+v", tn)
	}
}

// An ordinary assistant record stays a text part, so a widening error decision does not turn
// normal answers into red blocks.
func TestParseTurnNormalAssistantUnchanged(t *testing.T) {
	line := []byte(`{"type":"assistant","message":{"model":"claude-opus-5","content":[{"type":"text","text":"done"}]}}`)
	tn, ok := parseTurn(line, 3)
	if !ok {
		t.Fatal("parseTurn ok = false")
	}
	if len(tn.Parts) != 1 || tn.Parts[0].Kind != "text" || tn.Text != "done" {
		t.Fatalf("turn = %+v", tn)
	}
}
