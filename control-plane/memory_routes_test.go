package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// エージェントメモリの版管理（docs/39 P1・P2）の中継登録を固定する。CP は明示許可リスト
// 方式なので、Agent 側にだけ足すと Console からは 404 になる（この漏れは繰り返し起きて
// いる）。ここは「CP のルート表に載っていること」だけを見る — 実際の中継先の応答は
// Agent 側のテストが担保する。
func TestMemoryRoutesProxiedByCP(t *testing.T) {
	_, mux := smokeEnv(t)
	for _, c := range []struct{ method, path, want string }{
		{"GET", "/api/agents/memory/roots", "GET /api/agents/memory/roots"},
		{"GET", "/api/agents/memory/snapshots", "GET /api/agents/memory/snapshots"},
		{"POST", "/api/agents/memory/snapshots", "POST /api/agents/memory/snapshots"},
		{"GET", "/api/agents/memory/diff", "GET /api/agents/memory/diff"},
		{"GET", "/api/agents/memory/tree", "GET /api/agents/memory/tree"},
		{"POST", "/api/agents/memory/restore", "POST /api/agents/memory/restore"},
		{"PUT", "/api/agents/memory/settings", "PUT /api/agents/memory/settings"},
		// 既存のパターンルートに食われていないこと（/api/agents/{kind}/models と共存）。
		{"GET", "/api/agents/codex/models", "GET /api/agents/{kind}/models"},
	} {
		req := httptest.NewRequest(c.method, c.path, nil)
		if _, pattern := mux.Handler(req); pattern != c.want {
			t.Errorf("%s %s resolved to %q, want %q", c.method, c.path, pattern, c.want)
		}
	}
}

// 手動 snapshot と restore は変更操作なので監査に載る（docs/39 の監査要件。import /
// export は P3 で同じ表へ足す）。target は URL 由来のみで body は読まない。
func TestMemorySnapshotIsAudited(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/agents/memory/snapshots", nil)
	action, _, ok := auditActionTarget(req)
	if !ok || action != "memory.snapshot" {
		t.Fatalf("auditActionTarget = (%q, ok=%v), want memory.snapshot", action, ok)
	}
	// restore は戻し元 rev をクエリのヒントから target に採る（body は読まない）。
	req = httptest.NewRequest(http.MethodPost, "/api/agents/memory/restore?rev=deadbeef", nil)
	action, target, ok := auditActionTarget(req)
	if !ok || action != "memory.restore" || target != "deadbeef" {
		t.Fatalf("auditActionTarget = (%q, %q, ok=%v), want memory.restore/deadbeef", action, target, ok)
	}
	// 読み取り系は監査しない。
	for _, p := range []string{"/api/agents/memory/snapshots", "/api/agents/memory/tree", "/api/agents/memory/diff"} {
		if _, _, ok := auditActionTarget(httptest.NewRequest(http.MethodGet, p, nil)); ok {
			t.Errorf("GET %s should not be audited", p)
		}
	}
}
