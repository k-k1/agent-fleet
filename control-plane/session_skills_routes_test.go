package main

import (
	"net/http/httptest"
	"testing"
)

// ミラーのスキルピッカー（docs/log/50 / ADR0034）の中継登録を固定する。CP は明示許可リスト
// 方式なので、Agent 側にだけ足すと Console からは 404 になる（この漏れは繰り返し起きて
// いる）。実際の応答形は Agent 側 session_skills_test.go が担保する。
func TestSessionSkillsRouteProxiedByCP(t *testing.T) {
	_, mux := smokeEnv(t)
	req := httptest.NewRequest("GET", "/api/sessions/x/skills", nil)
	if _, pattern := mux.Handler(req); pattern != "GET /api/sessions/{name}/skills" {
		t.Errorf("resolved to %q, want GET /api/sessions/{name}/skills", pattern)
	}
}
