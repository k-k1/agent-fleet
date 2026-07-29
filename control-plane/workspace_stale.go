// workspace_stale.go — 「いま停止→起動したら、走るコードが変わるか」の判定。
//
// Console は自分のバンドル版しか見ていないため、バックエンド（Workspace イメージ /
// workspace-agent バイナリ）が更新されたことを知る手段が無かった。デプロイ後は
// Console をリロードすれば FE は新しくなるが、動き続けているコンテナは古いイメージの
// ままで、反映には停止→起動が要る。ここではその「ずれ」を CP 側だけで判定し、
// GET /api/workspace（と /api/events の push）に stale として載せる。
//
// 判定は二段構え。どちらも「判らないときは stale ではない」に倒す（誤警告を出すと
// バッジ自体が信用されなくなるため）。
//
//	① Runtime 固有の実測（staleRuntime）
//	   docker: 走っているコンテナの Image ID ≠ いまのタグが指す Image ID。
//	           版スタンプを持たないローカル再ビルドも拾えるので開発フリートで効く。
//	   native: 起動時に控えた workspace-agent バイナリのスタンプ ≠ 現在のスタンプ。
//	② Agent 申告版と CP 版の比較（ECS など ① を持たない Runtime の保険）
//	   リリースパイプラインが両方に同じ版を焼くので、食い違い＝古いまま。
//	   dev ビルド同士は常に "dev" なので比較しない。
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// staleRuntime は Runtime の任意実装。停止→起動で走るコードが変わるなら true。
// 判定できない（イメージが引けない・記録が無い）ときは false を返す契約。
type staleRuntime interface {
	Stale(ctx context.Context) bool
}

// workspaceStale は running なワークスペースについて①②を順に見る。呼び出し元は
// state=="running" のときだけ呼ぶこと（停止中は次の起動で必ず新しくなるので無意味）。
func workspaceStale(ctx context.Context, rt Runtime) bool {
	if sr, ok := rt.(staleRuntime); ok && sr.Stale(ctx) {
		return true
	}
	return agentVersionStale(ctx, rt)
}

// --- ② Agent 申告版 vs CP 版 -------------------------------------------------

// agentVersionStale は Agent の GET /healthz が申告する版と CP の buildVersion を
// 比べる。リリース版でだけ意味を持つ（dev 同士は常に一致扱い）。旧 Agent は version を
// 返さないので、この機能の導入直後の一回だけ検出できない（次の更新から効く）。
func agentVersionStale(ctx context.Context, rt Runtime) bool {
	if buildVersion == "" || buildVersion == "dev" {
		return false
	}
	v := freshness.get("ver:"+rt.Name(), 60*time.Second, func() string {
		return agentReportedVersion(ctx, rt)
	})
	return v != "" && v != "dev" && v != buildVersion
}

// agentReportedVersion asks the Agent for its own build version. Any failure
// (unreachable, old Agent without the field) yields "" → not stale.
func agentReportedVersion(ctx context.Context, rt Runtime) string {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, "GET", rt.Endpoint()+"/healthz", nil)
	if err != nil {
		return ""
	}
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token())
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var body struct {
		Version string `json:"version"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return ""
	}
	return body.Version
}

// --- 小さな TTL キャッシュ ---------------------------------------------------

// freshness memoizes the probes above. /api/workspace is polled every 4s per open
// Console (and pushed over SSE), so an uncached docker inspect / HTTP call per
// request would multiply across tabs and users. The probed facts change only when
// an image is rebuilt or a container restarts, so a short TTL is plenty.
var freshness = &ttlCache{m: map[string]ttlEntry{}}

type ttlEntry struct {
	v  string
	at time.Time
}

type ttlCache struct {
	mu  sync.Mutex
	m   map[string]ttlEntry
	now func() time.Time // tests
}

func (c *ttlCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// get returns the cached value for key, or runs load (OUTSIDE the lock, so one
// slow docker inspect never blocks another user's poll) and caches the result.
func (c *ttlCache) get(key string, ttl time.Duration, load func() string) string {
	c.mu.Lock()
	e, ok := c.m[key]
	fresh := ok && c.clock().Sub(e.at) < ttl
	c.mu.Unlock()
	if fresh {
		return e.v
	}
	v := load()
	c.mu.Lock()
	c.m[key] = ttlEntry{v: v, at: c.clock()}
	c.mu.Unlock()
	return v
}
