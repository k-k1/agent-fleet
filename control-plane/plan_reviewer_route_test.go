package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPlanReviewerRouteIsJSONSameOriginOnly(t *testing.T) {
	for name, c := range map[string]struct {
		ctype  string
		header map[string]string
		want   int
	}{
		"console json":          {"application/json", map[string]string{"Sec-Fetch-Site": "same-origin"}, 200},
		"non browser json":      {"application/json", nil, 200},
		"text/plain no-cors":    {"text/plain", map[string]string{"Sec-Fetch-Site": "same-site"}, 415},
		"json from same-site":   {"application/json", map[string]string{"Sec-Fetch-Site": "same-site"}, 403},
		"json from cross-site":  {"application/json", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		"foreign Origin header": {"application/json", map[string]string{"Origin": "https://preview.example.com"}, 403},
	} {
		reached := false
		h := jsonSameOriginOnly(func(w http.ResponseWriter, r *http.Request) { reached = true })
		req := httptest.NewRequest("POST", "https://console.example.com/api/sessions/s1/plan-reviewer", strings.NewReader(`{"path":"/p.md","reviewer":"s2"}`))
		req.Header.Set("Content-Type", c.ctype)
		for k, v := range c.header {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h(rec, req)
		got := rec.Code
		if reached {
			got = 200
		}
		if got != c.want || reached != (c.want == 200) {
			t.Errorf("%s: status %d reached=%v, want %d", name, got, reached, c.want)
		}
	}
}

// The relay must tell an Agent that predates planReviewers (field absent) from one that holds
// no link ([]): the Console trusts its local cache only in the first case.
func TestSessionWireKeepsPlanReviewersAbsentVersusEmpty(t *testing.T) {
	var old, empty, full sessionWire
	for in, out := range map[string]*sessionWire{
		`{"name":"a"}`:                    &old,
		`{"name":"a","planReviewers":[]}`: &empty,
		`{"name":"a","planReviewers":[{"path":"/p","reviewer":"b"}]}`: &full,
	} {
		if err := json.Unmarshal([]byte(in), out); err != nil {
			t.Fatal(err)
		}
	}
	enc := func(s sessionWire) string { b, _ := json.Marshal(s); return string(b) }
	if strings.Contains(enc(old), "planReviewers") {
		t.Errorf("absent became present: %s", enc(old))
	}
	if !strings.Contains(enc(empty), `"planReviewers":[]`) {
		t.Errorf("empty list dropped: %s", enc(empty))
	}
	if !strings.Contains(enc(full), `"reviewer":"b"`) {
		t.Errorf("link dropped: %s", enc(full))
	}
}

// The automatic re-review state (#2810) rides the same relay: a field sessionWire does not
// carry vanishes between the Agent and the Console, and the Console would show a loop that
// has no count and no reason for stopping.
func TestSessionWireRelaysThePlanAutoState(t *testing.T) {
	in := `{"name":"a","planReviewers":[{"path":"/p","reviewer":"b","auto":{"enabled":false,"max":3,"round":2,"verdict":"changes-requested","verdictAt":"2026-11-01T10:00:00Z","plan":"abc","stop":"max_rounds","prompt":"R {path}"}}]}`
	var w sessionWire
	if err := json.Unmarshal([]byte(in), &w); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(w)
	for _, want := range []string{`"max":3`, `"round":2`, `"stop":"max_rounds"`, `"verdict":"changes-requested"`, `"verdictAt":"2026-11-01T10:00:00Z"`, `"plan":"abc"`, `"prompt":"R {path}"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("relay dropped %s: %s", want, b)
		}
	}
	var bare sessionWire
	_ = json.Unmarshal([]byte(`{"name":"a","planReviewers":[{"path":"/p","reviewer":"b"}]}`), &bare)
	if b, _ := json.Marshal(bare); strings.Contains(string(b), `"auto"`) {
		t.Errorf("a link that never had the setting gained one: %s", b)
	}
}
