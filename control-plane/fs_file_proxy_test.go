package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type fsStateRuntime struct {
	stubRuntime
	state string
}

func (r fsStateRuntime) State(context.Context) string { return r.state }

func newFSProxyTest(t *testing.T, agent http.Handler) (agentProxyAPI, *resolved, *sqlStore, func()) {
	t.Helper()
	server := httptest.NewServer(agent)
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		server.Close()
		st.Close()
		t.Fatal(err)
	}
	ctx := context.Background()
	tenant, _ := st.EnsureDefaultTenant(ctx)
	identity, _ := st.UpsertIdentity(ctx, "fs@example.com", "fs", "")
	membership, _ := st.EnsureMembership(ctx, identity.ID, tenant.ID, "member")
	workspace := Workspace{ID: "ws1", TenantID: tenant.ID, MembershipID: membership.ID, ContainerName: "fs", Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t", State: "running", CreatedAt: nowTS()}
	if err := st.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	mgr := &manager{store: st, conns: newConnRegistry()}
	proxy := newAgentProxyAPI(mgr)
	res := &resolved{
		rt:    stubRuntime{endpoint: server.URL, token: "agent-token"},
		ws:    workspace,
		ident: Identity{ID: "user1"},
		mv:    MembershipView{MembershipID: membership.ID},
	}
	return proxy, res, st, func() {
		server.Close()
		st.Close()
	}
}

func cpPutBody(path, content string) string {
	body, _ := json.Marshal(map[string]string{
		"path": path, "content": content,
		"baseDiskRevision": "sha256:" + strings.Repeat("0", 64),
	})
	return string(body)
}

func doCPFSFilePut(proxy agentProxyAPI, res *resolved, body, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, "/api/fs/file", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	proxy.fsFilePut(rec, req, res)
	return rec
}

func TestCPFSFilePutProxiesExactBodyAndAuditsOnlyPath(t *testing.T) {
	body := cpPutBody("repos/app/a.txt", "secret source\n")
	var called atomic.Int32
	proxy, res, st, cleanup := newFSProxyTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		if r.Method != http.MethodPut || r.URL.Path != "/fs/file" {
			t.Errorf("upstream request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer agent-token" {
			t.Errorf("Authorization=%q", got)
		}
		got, _ := ioReadAll(r)
		if string(got) != body {
			t.Errorf("forwarded body=%q want=%q", got, body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"path":"repos/app/a.txt","size":7,"revision":"sha256:` + strings.Repeat("1", 64) + `"}`))
	}))
	defer cleanup()

	rec := doCPFSFilePut(proxy, res, body, "application/json; charset=utf-8")
	if rec.Code != http.StatusOK || called.Load() != 1 {
		t.Fatalf("status=%d called=%d body=%s", rec.Code, called.Load(), rec.Body.String())
	}
	rows, err := st.ListAuditByTenant(context.Background(), res.ws.TenantID, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("audit rows=%+v err=%v", rows, err)
	}
	if rows[0].Action != "fs.file.put" || rows[0].Target != "repos/app/a.txt" ||
		rows[0].Detail != "" || rows[0].HTTPStatus != http.StatusOK {
		t.Fatalf("audit=%+v", rows[0])
	}
	if strings.Contains(rows[0].Target+rows[0].Detail, "secret source") {
		t.Fatal("audit record contains file content")
	}
}

func ioReadAll(r *http.Request) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r.Body)
	return buf.Bytes(), err
}

func TestCPFSFilePutAuditsWriteStateUnknown(t *testing.T) {
	proxy, res, st, cleanup := newFSProxyTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"write_state_unknown","message":"directory fsync failed"}}`))
	}))
	defer cleanup()

	rec := doCPFSFilePut(proxy, res, cpPutBody("a.txt", "new\n"), "application/json")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "write_state_unknown") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	rows, err := st.ListAuditByTenant(context.Background(), res.ws.TenantID, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("audit rows=%+v err=%v", rows, err)
	}
	if rows[0].Action != "fs.file.put" || rows[0].Target != "a.txt" ||
		rows[0].Detail != "write_state_unknown" || rows[0].HTTPStatus != http.StatusInternalServerError {
		t.Fatalf("audit=%+v", rows[0])
	}
}

func TestCPFSFilePutDoesNotAuditOrdinaryFailure(t *testing.T) {
	proxy, res, st, cleanup := newFSProxyTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"revision_conflict","message":"changed"}}`))
	}))
	defer cleanup()
	rec := doCPFSFilePut(proxy, res, cpPutBody("a.txt", "new\n"), "application/json")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	rows, err := st.ListAuditByTenant(context.Background(), res.ws.TenantID, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("ordinary failure audit rows=%+v err=%v", rows, err)
	}
}

func TestCPFSFilePutInvalidAuditPathIsFixedToken(t *testing.T) {
	proxy, res, st, cleanup := newFSProxyTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"write_state_unknown","message":"injected"}}`))
	}))
	defer cleanup()
	rec := doCPFSFilePut(proxy, res, cpPutBody("../secret", "new\n"), "application/json")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	rows, err := st.ListAuditByTenant(context.Background(), res.ws.TenantID, 10)
	if err != nil || len(rows) != 1 || rows[0].Target != "<invalid-path>" {
		t.Fatalf("audit rows=%+v err=%v", rows, err)
	}
}

func TestCPFSFilePutRejectsMalformedBodyBeforeProxy(t *testing.T) {
	var called atomic.Int32
	proxy, res, st, cleanup := newFSProxyTest(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called.Add(1)
	}))
	defer cleanup()
	bodies := []string{
		`{"path":"a.txt","content":"\ud800","baseDiskRevision":"sha256:` + strings.Repeat("0", 64) + `"}`,
		cpPutBody("a.txt", "") + `{}`,
		`{"path":"a.txt","content":"","baseDiskRevision":"bad"}`,
	}
	for _, body := range bodies {
		rec := doCPFSFilePut(proxy, res, body, "application/json")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	rec := doCPFSFilePut(proxy, res, cpPutBody("a.txt", ""), "text/plain")
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("media status=%d body=%s", rec.Code, rec.Body.String())
	}
	oversize := httptest.NewRequest(http.MethodPut, "/api/fs/file", strings.NewReader(`{}`))
	oversize.Header.Set("Content-Type", "application/json")
	oversize.ContentLength = maxFSFilePUTBodyBytes + 1
	oversizeRec := httptest.NewRecorder()
	proxy.fsFilePut(oversizeRec, oversize, res)
	if oversizeRec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("wire limit status=%d body=%s", oversizeRec.Code, oversizeRec.Body.String())
	}
	streamingOversize := httptest.NewRequest(
		http.MethodPut, "/api/fs/file", bytes.NewReader(make([]byte, maxFSFilePUTBodyBytes+1)),
	)
	streamingOversize.Header.Set("Content-Type", "application/json")
	streamingOversize.ContentLength = -1
	streamingRec := httptest.NewRecorder()
	proxy.fsFilePut(streamingRec, streamingOversize, res)
	if streamingRec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("streaming wire limit status=%d body=%s", streamingRec.Code, streamingRec.Body.String())
	}
	if called.Load() != 0 {
		t.Fatalf("malformed requests reached Agent %d times", called.Load())
	}
	rows, err := st.ListAuditByTenant(context.Background(), res.ws.TenantID, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("malformed body audit rows=%+v err=%v", rows, err)
	}
}

func TestCPFSFilePutWorkspaceRunningGatePrecedesBodyValidation(t *testing.T) {
	var called atomic.Int32
	proxy, res, _, cleanup := newFSProxyTest(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called.Add(1)
	}))
	defer cleanup()
	baseRuntime := res.rt.(stubRuntime)
	for _, tc := range []struct {
		state, code string
	}{
		{"starting", "workspace_starting"},
		{"stopped", "workspace_stopped"},
		{"none", "workspace_stopped"},
	} {
		res.rt = fsStateRuntime{stubRuntime: baseRuntime, state: tc.state}
		rec := doCPFSFilePut(proxy, res, `not json`, "text/plain")
		if rec.Code != http.StatusConflict {
			t.Errorf("%s: status=%d body=%s", tc.state, rec.Code, rec.Body.String())
			continue
		}
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &envelope) != nil || envelope.Error.Code != tc.code {
			t.Errorf("%s: body=%s", tc.state, rec.Body.String())
		}
	}
	if called.Load() != 0 {
		t.Fatalf("non-running requests reached Agent %d times", called.Load())
	}
}

func TestCPStrictJSONSurrogateCompatibility(t *testing.T) {
	rev := "sha256:" + strings.Repeat("0", 64)
	cases := []struct {
		name, content string
		ok            bool
	}{
		{"pair", `"\ud83d\ude00"`, true},
		{"literal replacement", `"�"`, true},
		{"escaped replacement", `"\ufffd"`, true},
		{"escaped backslash", `"\\ud800"`, true},
		{"lone high", `"\ud800"`, false},
		{"lone low", `"\udc00"`, false},
	}
	for _, tc := range cases {
		body := `{"path":"a.txt","content":` + tc.content + `,"baseDiskRevision":"` + rev + `"}`
		req := httptest.NewRequest(http.MethodPut, "/api/fs/file", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		_, target, aerr := decodeCPFSFilePUT(req)
		if tc.ok && (aerr != nil || target != "a.txt") {
			t.Errorf("%s: target=%q error=%+v", tc.name, target, aerr)
		}
		if !tc.ok && (aerr == nil || aerr.code != errCodeFSBadRequest) {
			t.Errorf("%s: error=%+v", tc.name, aerr)
		}
	}
}

func TestCPAndAgentRegisterFSFilePutRoutes(t *testing.T) {
	cfg, mux := smokeEnv(t)
	_ = cfg
	req := httptest.NewRequest(http.MethodPut, "/api/fs/file", nil)
	_, pattern := mux.Handler(req)
	if pattern != "PUT /api/fs/file" {
		t.Fatalf("CP route pattern=%q", pattern)
	}
}
