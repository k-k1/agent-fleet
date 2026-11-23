package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// An SSM create naming a space is refused before the rewrite rebuilds the body from fixed
// fields, which would otherwise drop the space and launch the SSM session anyway (ADR 0142).
func TestRewriteSSMCreateRefusesASpace(t *testing.T) {
	cases := []struct {
		body string
		code string
	}{
		{`{"kind":"ssm","ssm_host_id":"h1","space":{"name":"x"}}`, "bad_space"},
		{`{"kind":"ssm","ssm_host_id":"h1","space":{"folder":"a-abcdef"}}`, "bad_space"},
		// null is no space: the request goes on to the usual checks.
		{`{"kind":"ssm","space":null}`, "bad_request"},
		{`{"kind":"ssm"}`, "bad_request"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/api/sessions", strings.NewReader(c.body))
		aerr := workspaceAPI{}.rewriteSSMCreate(context.Background(), nil, r)
		if aerr == nil || aerr.code != c.code {
			t.Errorf("rewriteSSMCreate(%s) = %+v, want code %q", c.body, aerr, c.code)
		}
	}
	// A non-SSM create keeps its space: the Agent decides.
	body := `{"kind":"claude","space":{"name":"x"}}`
	r := httptest.NewRequest("POST", "/api/sessions", strings.NewReader(body))
	if aerr := (workspaceAPI{}).rewriteSSMCreate(context.Background(), nil, r); aerr != nil {
		t.Fatalf("a claude create was refused: %+v", aerr)
	}
	if rest, _ := readAllBody(r); string(rest) != body {
		t.Fatalf("body = %q, want it untouched", rest)
	}
}
