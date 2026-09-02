// workspace_stale_test.go — 「停止→起動で走るコードが変わるか」判定の契約テスト。
// ここが誤検出すると WS バーに消えない「要再起動」が出続けて信用を失い、逆に取り
// こぼすと更新が反映されないまま気付けない。判らないときは stale ではない、を固定する。
package main

import (
	"context"
	"testing"
)

// staleStubRuntime は Stale を任意に返せる Runtime（workspacePayload 用）。
type staleStubRuntime struct {
	stubRuntime
	state string
	stale bool
}

func (s staleStubRuntime) State(context.Context) string { return s.state }
func (s staleStubRuntime) Stale(context.Context) bool   { return s.stale }

func TestWorkspacePayloadStale(t *testing.T) {
	a := workspaceAPI{}
	ctx := context.Background()

	m := a.workspacePayload(ctx, &resolved{rt: staleStubRuntime{state: "running", stale: true}}, "running")
	if m["stale"] != true {
		t.Fatalf("running+stale: stale = %v, want true", m["stale"])
	}

	// 停止中は次の起動で必ず新しくなるので出さない（押しても意味の無いバッジを出さない）。
	m = a.workspacePayload(ctx, &resolved{rt: staleStubRuntime{state: "none", stale: true}}, "none")
	if _, ok := m["stale"]; ok {
		t.Fatalf("stopped: stale present (%v), want absent", m["stale"])
	}

	// ドリフト無しのときは key ごと出さない（既存ペイロード形状を変えない）。
	m = a.workspacePayload(ctx, &resolved{rt: staleStubRuntime{state: "running", stale: false}}, "running")
	if _, ok := m["stale"]; ok {
		t.Fatalf("fresh: stale present (%v), want absent", m["stale"])
	}
}
