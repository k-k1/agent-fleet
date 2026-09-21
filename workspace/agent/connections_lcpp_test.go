package main

// docs/log/107 — PUT/DELETE/GET/check for the member's own llama.cpp connection.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

func TestNormalizeLcppURL(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"http://192.168.0.113:28080", "http://192.168.0.113:28080", false},
		{"http://192.168.0.113:28080/", "http://192.168.0.113:28080", false},
		// A URL pasted straight out of an OpenAI-compatible client's config carries /v1 — that
		// must still be accepted, and stripped, not rejected (docs/log/107 decision).
		{"http://192.168.0.113:28080/v1", "http://192.168.0.113:28080", false},
		{"http://192.168.0.113:28080/v1/", "http://192.168.0.113:28080", false},
		{"https://box.local", "https://box.local", false},
		{"  http://box:9931  ", "http://box:9931", false},
		{"", "", true},
		{"   ", "", true},
		{"ftp://box:9931", "", true},
		{"box:9931", "", true}, // no scheme
		{"http://", "", true},  // no host
	}
	for _, c := range cases {
		got, err := normalizeLcppURL(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("normalizeLcppURL(%q) = %q, nil; want an error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeLcppURL(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("normalizeLcppURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func lcppPut(url, apiKey string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(lcppConnReq{URL: url, APIKey: apiKey})
	w := httptest.NewRecorder()
	handlePutLcppConn(w, httptest.NewRequest("PUT", "/connections/lcpp", strings.NewReader(string(body))))
	return w
}

// A bad URL is rejected and nothing is written — the same "reject before store" contract
// Jira's connect flow already has (TestJiraConnectVerifiesBeforeSaving), even though lcpp's
// own PUT verifies only the URL's SHAPE, never dials out.
func TestHandlePutLcppConnRejectsBadURL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if w := lcppPut("", ""); w.Code != http.StatusBadRequest {
		t.Errorf("empty URL: status = %d, want 400", w.Code)
	}
	if w := lcppPut("not-a-url", ""); w.Code != http.StatusBadRequest {
		t.Errorf("bad scheme: status = %d, want 400", w.Code)
	}
	s, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	if s.Lcpp != nil {
		t.Error("a rejected connection was still written to the store")
	}
}

// Round trip: PUT stores the connection (normalized, without /v1); GET's status echoes
// connected+url but never the key; DELETE clears it back to disconnected.
func TestHandlePutGetDeleteLcppConnRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	w := lcppPut("http://192.168.0.113:28080/v1", "sk-member")
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", w.Code, w.Body.String())
	}
	var putResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &putResp); err != nil {
		t.Fatal(err)
	}
	if putResp["connected"] != true || putResp["url"] != "http://192.168.0.113:28080" {
		t.Errorf("PUT response = %+v", putResp)
	}
	if _, leaked := putResp["apiKey"]; leaked {
		t.Error("PUT response leaks apiKey")
	}

	s, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	if s.Lcpp == nil || s.Lcpp.URL != "http://192.168.0.113:28080" || s.Lcpp.APIKey != "sk-member" {
		t.Errorf("stored = %+v", s.Lcpp)
	}

	getW := httptest.NewRecorder()
	handleConnectionsGet(getW, httptest.NewRequest("GET", "/connections", nil))
	body := getW.Body.String()
	if strings.Contains(body, "sk-member") {
		t.Errorf("GET /connections leaks the API key: %s", body)
	}
	var getResp struct {
		Lcpp map[string]any `json:"lcpp"`
	}
	if err := json.Unmarshal(getW.Body.Bytes(), &getResp); err != nil {
		t.Fatal(err)
	}
	if getResp.Lcpp["connected"] != true || getResp.Lcpp["url"] != "http://192.168.0.113:28080" {
		t.Errorf("GET /connections lcpp = %+v", getResp.Lcpp)
	}

	delW := httptest.NewRecorder()
	handleDeleteLcppConn(delW, httptest.NewRequest("DELETE", "/connections/lcpp", nil))
	if delW.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d", delW.Code)
	}
	s2, _ := secrets.Load()
	if s2.Lcpp != nil {
		t.Errorf("connection survived DELETE: %+v", s2.Lcpp)
	}
}

// GET /connections must never carry an API key anywhere in the response body, whatever the
// key's own content — this pins the shape rather than one specific string.
func TestConnectionsGetNeverLeaksLcppAPIKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Lcpp = &secrets.LcppConn{URL: "http://box:9931", APIKey: "super-secret-marker"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handleConnectionsGet(w, httptest.NewRequest("GET", "/connections", nil))
	if strings.Contains(w.Body.String(), "super-secret-marker") {
		t.Errorf("leaked: %s", w.Body.String())
	}
}

func lcppCheckServer(t *testing.T, propsBody, modelsBody string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			if r.Header.Get("Authorization") != "Bearer sk-member" {
				t.Errorf("props request missing bearer: %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(propsBody))
		case "/v1/models":
			_, _ = w.Write([]byte(modelsBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHandleCheckLcppConnReportsBuildInfoWindowAndModels(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := lcppCheckServer(t,
		`{"build_info":"b11067-932a68e06","default_generation_settings":{"n_ctx":24064}}`,
		`{"data":[{"id":"gemma-4-12b-it-q4_k_m","meta":{"n_ctx":24064}}]}`)
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Lcpp = &secrets.LcppConn{URL: srv.URL, APIKey: "sk-member"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handleCheckLcppConn(w, httptest.NewRequest("POST", "/connections/lcpp/check", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		OK        bool     `json:"ok"`
		BuildInfo string   `json:"build_info"`
		NCtx      int      `json:"n_ctx"`
		Models    []string `json:"models"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.BuildInfo != "b11067-932a68e06" || resp.NCtx != 24064 {
		t.Errorf("resp = %+v", resp)
	}
	if len(resp.Models) != 1 || resp.Models[0] != "gemma-4-12b-it-q4_k_m" {
		t.Errorf("models = %v", resp.Models)
	}
	if strings.Contains(w.Body.String(), "sk-member") {
		t.Errorf("check response leaks the API key: %s", w.Body.String())
	}
}

func TestHandleCheckLcppConnNoConnectionConfigured(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	w := httptest.NewRecorder()
	handleCheckLcppConn(w, httptest.NewRequest("POST", "/connections/lcpp/check", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleCheckLcppConnUnreachable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Lcpp = &secrets.LcppConn{URL: "http://127.0.0.1:1"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handleCheckLcppConn(w, httptest.NewRequest("POST", "/connections/lcpp/check", nil))
	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.Code)
	}
}

// --- lcppStatus's "reachable" field --------------------------------------------------------

// No connection configured at all: the response is byte-for-byte what it was before this
// field existed — no "reachable" key, whatever the (irrelevant) observation state happens to
// hold in this process.
func TestLcppStatusUnsetConnectionUnchanged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	lcppMemberCacheReset()
	lcppMemberRecordReachable(true) // must be ignored: no connection means no "reachable" key at all

	s, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	out := lcppStatus(s)
	if out["connected"] != false {
		t.Errorf("connected = %v, want false", out["connected"])
	}
	if _, present := out["reachable"]; present {
		t.Errorf("reachable = %v, want absent when there is no connection", out["reachable"])
	}
	if _, present := out["url"]; present {
		t.Error("url present with no connection")
	}
}

// A connection is configured, but nothing has ever observed it (fresh workspace, no launch
// menu built yet, no check pressed): "reachable" is ABSENT, not false — this is the
// unknown-vs-unreachable distinction the whole feature exists to preserve.
func TestLcppStatusConnectedNeverObservedOmitsReachable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	lcppMemberCacheReset() // known=false
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Lcpp = &secrets.LcppConn{URL: "http://box:9931"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	out := lcppStatus(s)
	if out["connected"] != true {
		t.Fatalf("connected = %v, want true", out["connected"])
	}
	if _, present := out["reachable"]; present {
		t.Errorf("reachable = %v, want absent (unknown), not a false", out["reachable"])
	}
}

// Once something has observed the connection, lcppStatus reads that observation straight —
// true and false are both distinguishable from "absent".
func TestLcppStatusConnectedReflectsObservation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Lcpp = &secrets.LcppConn{URL: "http://box:9931"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	lcppMemberRecordReachable(true)
	s, _ := secrets.Load()
	if out := lcppStatus(s); out["reachable"] != true {
		t.Errorf("reachable = %v, want true", out["reachable"])
	}

	lcppMemberRecordReachable(false)
	s, _ = secrets.Load()
	if out := lcppStatus(s); out["reachable"] != false {
		t.Errorf("reachable = %v, want false", out["reachable"])
	}
}

// handleCheckLcppConn's own dial is itself an observation: a successful check leaves
// lcppStatus reporting reachable=true even without the launch-menu path ever having run.
func TestHandleCheckLcppConnRecordsObservationOnSuccess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	lcppMemberCacheReset()
	srv := lcppCheckServer(t, `{"build_info":"b1","default_generation_settings":{"n_ctx":1024}}`, `{"data":[]}`)
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Lcpp = &secrets.LcppConn{URL: srv.URL, APIKey: "sk-member"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handleCheckLcppConn(w, httptest.NewRequest("POST", "/connections/lcpp/check", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	s, _ := secrets.Load()
	if out := lcppStatus(s); out["reachable"] != true {
		t.Errorf("reachable = %v after a successful check, want true", out["reachable"])
	}
}

// A failed check also records — false, not silence — so the card/pill can say "did not
// answer" rather than staying in the "never observed" state after somebody explicitly tried.
func TestHandleCheckLcppConnRecordsObservationOnFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	lcppMemberCacheReset()
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Lcpp = &secrets.LcppConn{URL: "http://127.0.0.1:1"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handleCheckLcppConn(w, httptest.NewRequest("POST", "/connections/lcpp/check", nil))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
	s, _ := secrets.Load()
	ok, known := lcppMemberObservedReachable()
	if !known || ok {
		t.Errorf("observed ok=%v known=%v, want false, true", ok, known)
	}
	if out := lcppStatus(s); out["reachable"] != false {
		t.Errorf("reachable = %v after a failed check, want false", out["reachable"])
	}
}

// --- lcppStatus's "model"/"model_count" fields (2026-09-21 addendum) -----------------------
//
// A member can swap the LAN box under the same saved URL. A live run measured that a
// single-model llama-server ignores the request's own `model` field entirely (any string, or
// none, answers 200) — so without this, a member who kept the old model selected would be
// served by whatever is actually loaded now, with nothing on screen saying it changed.

// No model observed yet: the field is absent, not an empty string — same "unknown, not a lie"
// rule as reachable.
func TestLcppStatusModelOmittedWhenUnknown(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	lcppMemberCacheReset()
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Lcpp = &secrets.LcppConn{URL: "http://box:9931"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s, _ := secrets.Load()
	out := lcppStatus(s)
	if _, present := out["model"]; present {
		t.Errorf("model = %v, want absent", out["model"])
	}
	if _, present := out["model_count"]; present {
		t.Errorf("model_count = %v, want absent", out["model_count"])
	}
}

// A single-model observation: model is set, model_count is OMITTED (not 1) — the count only
// earns its place on the wire when it says something a member could not assume.
func TestLcppStatusModelPresentSingle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	lcppMemberCacheReset()
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Lcpp = &secrets.LcppConn{URL: "http://box:9931"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	lcppMemberRecordModel([]lcppMemberModel{{ID: "gemma-4-12b-it-q4_k_m"}})

	s, _ := secrets.Load()
	out := lcppStatus(s)
	if out["model"] != "gemma-4-12b-it-q4_k_m" {
		t.Errorf("model = %v, want gemma-4-12b-it-q4_k_m", out["model"])
	}
	if _, present := out["model_count"]; present {
		t.Errorf("model_count = %v, want absent for a single model", out["model_count"])
	}
}

// A router observation (docs/log/106 §axis 2): model_count rides along so a member reading the
// first id knows it is one of several, not the only one.
func TestLcppStatusModelPresentMultiple(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	lcppMemberCacheReset()
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Lcpp = &secrets.LcppConn{URL: "http://box:9931"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	lcppMemberRecordModel([]lcppMemberModel{{ID: "gemma-4-12b-it-q4_k_m"}, {ID: "qwen3.8-27b"}})

	s, _ := secrets.Load()
	out := lcppStatus(s)
	if out["model"] != "gemma-4-12b-it-q4_k_m" {
		t.Errorf("model = %v, want the first id", out["model"])
	}
	if out["model_count"] != 2 {
		t.Errorf("model_count = %v, want 2", out["model_count"])
	}
}

// handleCheckLcppConn's own dial records the model too — the explicit "check connection" button
// teaches the observation just like the launch-menu path does.
func TestHandleCheckLcppConnRecordsModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	lcppMemberCacheReset()
	srv := lcppCheckServer(t,
		`{"build_info":"b1","default_generation_settings":{"n_ctx":1024}}`,
		`{"data":[{"id":"gemma-4-12b-it-q4_k_m"}]}`)
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Lcpp = &secrets.LcppConn{URL: srv.URL, APIKey: "sk-member"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handleCheckLcppConn(w, httptest.NewRequest("POST", "/connections/lcpp/check", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	s, _ := secrets.Load()
	if out := lcppStatus(s); out["model"] != "gemma-4-12b-it-q4_k_m" {
		t.Errorf("model = %v after a successful check, want gemma-4-12b-it-q4_k_m", out["model"])
	}
}

// The swap itself, end to end through lcppStatus: an observed model A, then a fresh check
// against a box now answering with model B — lcppStatus must report B, never a stale A.
func TestLcppStatusReflectsASwappedModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	lcppMemberCacheReset()
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Lcpp = &secrets.LcppConn{URL: "http://box:9931"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	lcppMemberRecordModel([]lcppMemberModel{{ID: "gemma-4-12b-it-q4_k_m"}})
	s, _ := secrets.Load()
	if out := lcppStatus(s); out["model"] != "gemma-4-12b-it-q4_k_m" {
		t.Fatalf("setup: model = %v, want the old model", out["model"])
	}

	lcppMemberRecordModel([]lcppMemberModel{{ID: "gemma-4-e4b-uncensored-hauhaucs-balanced-q4_k_m"}})
	if out := lcppStatus(s); out["model"] != "gemma-4-e4b-uncensored-hauhaucs-balanced-q4_k_m" {
		t.Errorf("model = %v, want the NEW model — the observation must not carry the old name forward", out["model"])
	}
}

// --- acceptance: GET /connections never dials the member's own lcpp connection -------------

// The absolute condition the parent task set: handleConnectionsGet must be able to answer
// "reachable" without ever adding a request to the box itself. A fake server with a request
// counter is the only reliable way to pin "zero dials" — asserting on timing would flake.
func TestHandleConnectionsGetNeverDialsLcppConnection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Lcpp = &secrets.LcppConn{URL: srv.URL}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Positive control: without this test's own guard, a naive lcppStatus that dialed on every
	// GET would fail obviously — call it several times and require the counter to stay at 0.
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		handleConnectionsGet(w, httptest.NewRequest("GET", "/connections", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET /connections status = %d", w.Code)
		}
	}
	if hits != 0 {
		t.Errorf("hits = %d, want 0 — GET /connections must never dial the member's lcpp connection", hits)
	}
}
