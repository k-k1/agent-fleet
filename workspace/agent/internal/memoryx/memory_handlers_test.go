package memoryx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// docs/log/39 P1 の REST を実ルート表（buildMux）越しに叩く。CP 側の登録漏れは
// control-plane 側のテストでは拾えないので、こちらは Agent 側の登録と応答形を固定する。
func memoryAPIHandler(t *testing.T) http.Handler {
	t.Helper()
	memoryTestEnv(t)
	t.Setenv("AGENT_TOKEN", "smoke-token")
	return httpx.RequireToken(buildMux())
}

func TestMemoryRoutesRegistered(t *testing.T) {
	mux := buildMux()
	for _, c := range []struct{ method, path, want string }{
		{"GET", "/agents/memory/roots", "GET /agents/memory/roots"},
		{"GET", "/agents/memory/snapshots", "GET /agents/memory/snapshots"},
		{"POST", "/agents/memory/snapshots", "POST /agents/memory/snapshots"},
		{"GET", "/agents/memory/diff", "GET /agents/memory/diff"},
		{"GET", "/agents/memory/tree", "GET /agents/memory/tree"},
		{"POST", "/agents/memory/restore", "POST /agents/memory/restore"},
		{"PUT", "/agents/memory/settings", "PUT /agents/memory/settings"},
		{"GET", "/agents/memory/export", "GET /agents/memory/export"},
		{"POST", "/agents/memory/import", "POST /agents/memory/import"},
		{"POST", "/agents/memory/import/apply", "POST /agents/memory/import/apply"},
		// 既存のパターンルートと共存できていること（{kind} に食われない）。
		{"GET", "/agents/codex/models", "GET /agents/{kind}/models"},
	} {
		req := httptest.NewRequest(c.method, c.path, nil)
		if _, pattern := mux.Handler(req); pattern != c.want {
			t.Errorf("%s %s resolved to %q, want %q", c.method, c.path, pattern, c.want)
		}
	}
}

func TestMemoryAPIRoundTrip(t *testing.T) {
	h := memoryAPIHandler(t)

	// roots: claude は常時、codex は memories dir がある時だけ現れる。
	w := smokeDo(t, h, "GET", "/agents/memory/roots", "smoke-token", "")
	if w.Code != http.StatusOK {
		t.Fatalf("roots: %d %s", w.Code, w.Body.String())
	}
	var roots struct {
		Roots []memoryRootView `json:"roots"`
		Auto  bool             `json:"auto"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &roots); err != nil {
		t.Fatalf("roots decode: %v (%s)", err, w.Body.String())
	}
	if len(roots.Roots) != 2 || !roots.Auto {
		t.Fatalf("roots = %+v auto=%v", roots.Roots, roots.Auto)
	}
	if roots.Roots[0].Kind != "claude" || !roots.Roots[0].Scopes || roots.Roots[0].Files != 3 {
		t.Errorf("claude root = %+v", roots.Roots[0])
	}
	if len(roots.Roots[0].Projects) != 1 || roots.Roots[0].Projects[0].Display != "demo" {
		t.Errorf("claude projects = %+v", roots.Roots[0].Projects)
	}
	if roots.Roots[1].Kind != "codex" || roots.Roots[1].Scopes || roots.Roots[1].Files != 2 {
		t.Errorf("codex root = %+v", roots.Roots[1])
	}
	// docs/log/39 P4: memories を有効化して codex がワークスペースを作ると、ルートは
	// inactive から active へ移る。切り戻しの導線が消えないよう、有効なルートにも
	// トグルの状態を載せる。
	if !roots.Roots[1].Toggleable {
		t.Errorf("the active codex root lost its enable toggle: %+v", roots.Roots[1])
	}

	// snapshot が 1 件も無いうちは一覧が空、diff は 404。
	if w := smokeDo(t, h, "GET", "/agents/memory/snapshots", "smoke-token", ""); w.Code != http.StatusOK ||
		w.Body.String() != "{\"snapshots\":[]}\n" {
		t.Fatalf("empty snapshots: %d %q", w.Code, w.Body.String())
	}
	if w := smokeDo(t, h, "GET", "/agents/memory/diff?to=HEAD", "smoke-token", ""); w.Code != http.StatusNotFound {
		t.Fatalf("diff before any snapshot: %d %s", w.Code, w.Body.String())
	}

	// 手動 snapshot → 一覧に現れる。2 回目は無変更なので committed=false。
	var created memorySnapshotResult
	w = smokeDo(t, h, "POST", "/agents/memory/snapshots", "smoke-token", `{"trigger":"manual"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("manual snapshot: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || !created.Committed || created.Rev == "" {
		t.Fatalf("manual snapshot result: %+v err=%v", created, err)
	}
	w = smokeDo(t, h, "POST", "/agents/memory/snapshots", "smoke-token", `{}`)
	var again memorySnapshotResult
	if err := json.Unmarshal(w.Body.Bytes(), &again); err != nil || again.Committed {
		t.Fatalf("unchanged manual snapshot committed: %+v err=%v", again, err)
	}

	var listed struct {
		Snapshots []memorySnapshotInfo `json:"snapshots"`
	}
	w = smokeDo(t, h, "GET", "/agents/memory/snapshots?limit=5", "smoke-token", "")
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || len(listed.Snapshots) != 1 {
		t.Fatalf("snapshots: %s err=%v", w.Body.String(), err)
	}
	if listed.Snapshots[0].Trigger != memoryTriggerManual || listed.Snapshots[0].Rev != created.Rev {
		t.Fatalf("listed snapshot = %+v", listed.Snapshots[0])
	}

	// diff: 初回 snapshot の中身が読める。
	var diff struct {
		Diff string `json:"diff"`
	}
	w = smokeDo(t, h, "GET", "/agents/memory/diff?to="+created.Rev, "smoke-token", "")
	if w.Code != http.StatusOK {
		t.Fatalf("diff: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &diff); err != nil || diff.Diff == "" {
		t.Fatalf("diff payload: %s err=%v", w.Body.String(), err)
	}
}

// 外から渡る値の検証: 契機の詐称・不正な rev・宣言済みルート外のパスは弾く。
func TestMemoryAPIRejectsBadInput(t *testing.T) {
	h := memoryAPIHandler(t)
	if w := smokeDo(t, h, "POST", "/agents/memory/snapshots", "smoke-token", `{"trigger":"restore"}`); w.Code != http.StatusBadRequest {
		t.Errorf("forged trigger: %d %s", w.Code, w.Body.String())
	}
	if w := smokeDo(t, h, "GET", "/agents/memory/snapshots?limit=-1", "smoke-token", ""); w.Code != http.StatusBadRequest {
		t.Errorf("negative limit: %d %s", w.Code, w.Body.String())
	}
	if w := smokeDo(t, h, "GET", "/agents/memory/snapshots?before=yesterday", "smoke-token", ""); w.Code != http.StatusBadRequest {
		t.Errorf("non-RFC3339 before: %d %s", w.Code, w.Body.String())
	}
	// 以降は snapshot が 1 件ある状態で見る（無い場合は 404 が先に返るため）。
	if w := smokeDo(t, h, "POST", "/agents/memory/snapshots", "smoke-token", ""); w.Code != http.StatusOK {
		t.Fatalf("seed snapshot: %d %s", w.Code, w.Body.String())
	}
	for _, q := range []string{
		"?to=--upload-pack=evil",
		"?to=HEAD~1..HEAD",
		"?to=nope",
		"?at=not-a-time",
	} {
		if w := smokeDo(t, h, "GET", "/agents/memory/diff"+q, "smoke-token", ""); w.Code != http.StatusBadRequest {
			t.Errorf("diff%s: %d %s", q, w.Code, w.Body.String())
		}
	}
	for _, q := range []string{
		"?to=HEAD&path=../../etc",
		"?to=HEAD&path=/etc/passwd",
		"?to=HEAD&path=notaroot/x",
	} {
		if w := smokeDo(t, h, "GET", "/agents/memory/diff"+q, "smoke-token", ""); w.Code != http.StatusBadRequest {
			t.Errorf("diff%s: %d %s", q, w.Code, w.Body.String())
		}
	}
}
