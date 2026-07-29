// workspace_stale_test.go — 「停止→起動で走るコードが変わるか」判定の契約テスト。
// ここが誤検出すると WS バーに消えない「要再起動」が出続けて信用を失い、逆に取り
// こぼすと更新が反映されないまま気付けない。判らないときは stale ではない、を固定する。
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
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

	m := a.workspacePayload(ctx, &resolved{rt: staleStubRuntime{state: "running", stale: true}})
	if m["stale"] != true {
		t.Fatalf("running+stale: stale = %v, want true", m["stale"])
	}

	// 停止中は次の起動で必ず新しくなるので出さない（押しても意味の無いバッジを出さない）。
	m = a.workspacePayload(ctx, &resolved{rt: staleStubRuntime{state: "none", stale: true}})
	if _, ok := m["stale"]; ok {
		t.Fatalf("stopped: stale present (%v), want absent", m["stale"])
	}

	// ドリフト無しのときは key ごと出さない（既存ペイロード形状を変えない）。
	m = a.workspacePayload(ctx, &resolved{rt: staleStubRuntime{state: "running", stale: false}})
	if _, ok := m["stale"]; ok {
		t.Fatalf("fresh: stale present (%v), want absent", m["stale"])
	}
}

func TestAgentVersionStale(t *testing.T) {
	agentVer := "1.0.0"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"version":"` + agentVer + `"}`))
	}))
	defer srv.Close()

	old := buildVersion
	defer func() { buildVersion = old }()
	ctx := context.Background()
	rt := stubRuntime{endpoint: srv.URL}

	// dev の CP は比較しない（開発中に毎回「要再起動」を出さない）。
	buildVersion = "dev"
	freshness = &ttlCache{m: map[string]ttlEntry{}}
	if agentVersionStale(ctx, rt) {
		t.Fatal("dev CP: stale, want false")
	}

	// リリース版同士で食い違う＝コンテナが更新前のまま。
	buildVersion = "1.1.0"
	freshness = &ttlCache{m: map[string]ttlEntry{}}
	if !agentVersionStale(ctx, rt) {
		t.Fatal("1.1.0 CP vs 1.0.0 agent: not stale, want stale")
	}

	// 一致していれば当然 false。
	agentVer = "1.1.0"
	freshness = &ttlCache{m: map[string]ttlEntry{}}
	if agentVersionStale(ctx, rt) {
		t.Fatal("same version: stale, want false")
	}

	// 版を申告しない旧 Agent は「判らない」→ 誤警告しない。
	agentVer = ""
	freshness = &ttlCache{m: map[string]ttlEntry{}}
	if agentVersionStale(ctx, rt) {
		t.Fatal("agent without version: stale, want false")
	}
}

// 到達不能な Agent（停止直後・起動中）でバッジを出さない。
func TestAgentVersionStaleUnreachable(t *testing.T) {
	old := buildVersion
	buildVersion = "1.1.0"
	defer func() { buildVersion = old }()
	freshness = &ttlCache{m: map[string]ttlEntry{}}
	if agentVersionStale(context.Background(), stubRuntime{endpoint: "http://127.0.0.1:1"}) {
		t.Fatal("unreachable agent: stale, want false")
	}
}

func TestNativeStaleBinaryStamp(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "workspace-agent")
	if err := os.WriteFile(bin, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	n := &nativeRuntime{agentBin: bin, dataDir: dir}

	// 記録が無い（この機能より前に起動したプロセス）＝判らない → false。
	if n.Stale(context.Background()) {
		t.Fatal("no stamp: stale, want false")
	}

	if err := os.WriteFile(n.binStampPath(), []byte(binStamp(bin)), 0o644); err != nil {
		t.Fatal(err)
	}
	if n.Stale(context.Background()) {
		t.Fatal("unchanged binary: stale, want false")
	}

	// af update がバイナリを差し替えた状態（内容もサイズも変わる）。
	if err := os.WriteFile(bin, []byte("v2-longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !n.Stale(context.Background()) {
		t.Fatal("replaced binary: not stale, want stale")
	}
}

func TestTTLCache(t *testing.T) {
	now := time.Unix(1000, 0)
	c := &ttlCache{m: map[string]ttlEntry{}, now: func() time.Time { return now }}
	calls := 0
	load := func() string {
		calls++
		return "v"
	}

	if got := c.get("k", time.Minute, load); got != "v" || calls != 1 {
		t.Fatalf("first: %q calls=%d", got, calls)
	}
	now = now.Add(30 * time.Second)
	if got := c.get("k", time.Minute, load); got != "v" || calls != 1 {
		t.Fatalf("within TTL re-probed: calls=%d", calls)
	}
	now = now.Add(31 * time.Second)
	if got := c.get("k", time.Minute, load); got != "v" || calls != 2 {
		t.Fatalf("after TTL: calls=%d, want 2", calls)
	}
	// 失敗（""）もキャッシュする — docker が落ちている間に毎回叩き直さない。
	c2 := &ttlCache{m: map[string]ttlEntry{}, now: func() time.Time { return now }}
	fails := 0
	empty := func() string { fails++; return "" }
	c2.get("k", time.Minute, empty)
	c2.get("k", time.Minute, empty)
	if fails != 1 {
		t.Fatalf("empty result not cached: fails=%d", fails)
	}
}
