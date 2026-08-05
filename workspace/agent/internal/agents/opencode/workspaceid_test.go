package opencode

// 利用枠ページの ID と、上限に当たったときの枠情報（workspaceid.go）。
// 実測の材料は 2 つ: 残高切れの文面に埋まる billing URL（errors_test.go の固定データと
// 同じ形）と、Go の上限が運ぶ responseBody / retry-after（opencode 本体が読むのと同じ場所）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const sampleWorkspace = "wrk_01KQP88ANRPG2VRZDV171TGFJN"

// isolate points the store at a temp HOME and clears the in-process caches.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	wsIDMu.Lock()
	wsIDCache = nil
	wsIDMu.Unlock()
	limitMu.Lock()
	lastLimit = LimitInfo{}
	limitMu.Unlock()
	t.Cleanup(func() {
		wsIDMu.Lock()
		wsIDCache = nil
		wsIDMu.Unlock()
		limitMu.Lock()
		lastLimit = LimitInfo{}
		limitMu.Unlock()
	})
}

func TestValidWorkspaceID(t *testing.T) {
	for _, ok := range []string{sampleWorkspace, "  " + sampleWorkspace + "  "} {
		if !ValidWorkspaceID(ok) {
			t.Errorf("ValidWorkspaceID(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "wrk_", "wrk_short", "org_01KQP88ANRPG2VRZDV171TGFJN", sampleWorkspace + "X"} {
		if ValidWorkspaceID(bad) {
			t.Errorf("ValidWorkspaceID(%q) = true", bad)
		}
	}
}

// 残高切れ: 文面に billing URL が埋まっている（実測の形）。ここから拾えること。
func TestScanLearnsIDFromBalanceMessage(t *testing.T) {
	isolate(t)
	var e messageError
	e.Name = "APIError"
	e.Data.StatusCode = 401
	e.Data.Message = "Insufficient balance. Manage your billing here: https://opencode.ai/workspace/" + sampleWorkspace + "/billing"
	scanFailure(e)

	id, src := WorkspaceID()
	if id != sampleWorkspace || src != "learned" {
		t.Fatalf("WorkspaceID = %q/%q", id, src)
	}
	if got := WorkspaceURL(id, "go"); got != "https://opencode.ai/workspace/"+sampleWorkspace+"/go" {
		t.Errorf("WorkspaceURL = %q", got)
	}
}

// Go の上限: opencode 本体と同じ場所（responseBody の metadata と retry-after）から、
// どの枠か・いつ戻るかを拾う。
func TestScanReadsLimitWindowAndReset(t *testing.T) {
	isolate(t)
	body, _ := json.Marshal(map[string]any{
		"name":     "GoUsageLimitError",
		"metadata": map[string]string{"workspace": sampleWorkspace, "limitName": "weekly"},
	})
	var e messageError
	e.Name = "APIError"
	e.Data.StatusCode = 429
	e.Data.ResponseBody = string(body)
	e.Data.ResponseHeaders = map[string]string{"Retry-After": "3600"}

	info := scanFailure(e)
	if info.Name != "weekly" {
		t.Errorf("limit name = %q, want weekly", info.Name)
	}
	at, err := time.Parse(time.RFC3339, info.ResetAt)
	if err != nil {
		t.Fatalf("reset_at = %q: %v", info.ResetAt, err)
	}
	if d := time.Until(at); d < 55*time.Minute || d > 65*time.Minute {
		t.Errorf("reset は約1時間後のはず: %v", d)
	}
	if id, _ := WorkspaceID(); id != sampleWorkspace {
		t.Errorf("metadata から ID を拾えていない: %q", id)
	}
	if got := LastLimit(); got.Name != "weekly" {
		t.Errorf("LastLimit = %+v", got)
	}
}

// 上限と無関係な失敗では枠情報を作らない（「観測していない」と「上限だった」を混ぜない）。
func TestScanIgnoresUnrelatedFailure(t *testing.T) {
	isolate(t)
	var e messageError
	e.Name = "MessageOutputLengthError"
	if got := scanFailure(e); got.Name != "" || got.ResetAt != "" {
		t.Errorf("scanFailure = %+v, want 空", got)
	}
	if got := LastLimit(); got.Name != "" || got.ResetAt != "" {
		t.Errorf("LastLimit = %+v, want 空", got)
	}
}

// 手入力は学習で上書きしない — 利用者が選んだ workspace のほうが意図に近い。
func TestManualIDWinsOverLearned(t *testing.T) {
	isolate(t)
	if err := SetWorkspaceID(sampleWorkspace); err != nil {
		t.Fatal(err)
	}
	other := "wrk_01ZZZ88ANRPG2VRZDV171TGFJN"
	var e messageError
	e.Data.Message = "https://opencode.ai/workspace/" + other + "/billing"
	scanFailure(e)
	if id, src := WorkspaceID(); id != sampleWorkspace || src != "manual" {
		t.Errorf("手入力が上書きされた: %q/%q", id, src)
	}
}

func TestHandlePutWorkspaceValidatesAndClears(t *testing.T) {
	isolate(t)
	put := func(body string) (int, map[string]any) {
		r := httptest.NewRequest("PUT", "/connections/opencode/workspace", strings.NewReader(body))
		w := httptest.NewRecorder()
		HandlePutWorkspace(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, out := put(`{"id":"nonsense"}`); code != http.StatusBadRequest {
		t.Fatalf("不正な ID は 400 のはず: %d %v", code, out)
	}
	code, out := put(`{"id":"` + sampleWorkspace + `"}`)
	if code != http.StatusOK || out["workspace_id"] != sampleWorkspace {
		t.Fatalf("status=%d out=%v", code, out)
	}
	if out["workspace_url"] != "https://opencode.ai/workspace/"+sampleWorkspace+"/go" {
		t.Errorf("workspace_url = %v", out["workspace_url"])
	}
	if code, _ := put(`{"id":""}`); code != http.StatusOK {
		t.Fatalf("登録解除 status=%d", code)
	}
	if id, _ := WorkspaceID(); id != "" {
		t.Errorf("解除できていない: %q", id)
	}
}

// 保存は Agent のデータディレクトリ。プロセスをまたいでも読めること（キャッシュを捨てて再読込）。
func TestWorkspaceIDPersists(t *testing.T) {
	isolate(t)
	if err := SetWorkspaceID(sampleWorkspace); err != nil {
		t.Fatal(err)
	}
	wsIDMu.Lock()
	wsIDCache = nil
	wsIDMu.Unlock()
	if id, src := WorkspaceID(); id != sampleWorkspace || src != "manual" {
		t.Errorf("再読込 = %q/%q", id, src)
	}
}
