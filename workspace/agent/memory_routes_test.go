package main

// memory_routes_test.go — memory 家系のうち **package main に残した 2 本**。
//
// 家系の本体は internal/memoryx へ移送したが、この 2 本だけは移すと空回りする:
// どちらも memoryx の未公開シンボルを 1 つも使わず（＝残しても公開要求は増えない）、
// 見ているのは **本物のルート表でしか確かめられないこと**である——
// memory の 10 本が既存のパターンルート `/agents/{kind}/models` を食い潰していないこと。
// memoryx が自前で組む mux（internal/memoryx/mux_test.go）には `{kind}` のルートが
// 無いので、向こうへ持っていくとその主張が消える。
//
// 写しの側（memoryx のテスト用 mux が routes.go とズレていないこと）は
// internal/memoryx/mux_test.go の TestMemoryRoutesMatchAgentRouteTable が見ている。

import (
	"net/http/httptest"
	"testing"
)

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

func TestMemoryP2RoutesRegistered(t *testing.T) {
	mux := buildMux()
	for _, c := range []struct{ method, path, want string }{
		{"GET", "/agents/memory/tree", "GET /agents/memory/tree"},
		{"POST", "/agents/memory/restore", "POST /agents/memory/restore"},
		{"PUT", "/agents/memory/settings", "PUT /agents/memory/settings"},
	} {
		req := httptest.NewRequest(c.method, c.path, nil)
		if _, pattern := mux.Handler(req); pattern != c.want {
			t.Errorf("%s %s resolved to %q, want %q", c.method, c.path, pattern, c.want)
		}
	}
}
