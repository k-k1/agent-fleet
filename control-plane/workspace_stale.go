// workspace_stale.go — 「いま停止→起動したら、走るコードが変わるか」の判定。
//
// Console は自分のバンドル版しか見ていないため、バックエンド（Workspace イメージ /
// rootfs / workspace-agent バイナリ）が更新されたことを知る手段が無かった。デプロイ後は
// Console をリロードすれば FE は新しくなるが、動き続けているコンテナは古いまま走り、
// 反映には停止→起動が要る。ここではその「ずれ」を CP 側だけで判定し、
// GET /api/workspace（と /api/events の push）に stale として載せる。
//
// 判定則はひとつだけ:
//
//	起動時に実際に使った実体（イメージ ID / rootfs / バイナリ）と、
//	いま Start したら使う実体を比べる。
//
// ★ CP の版と Agent の申告版を比べてはならない。native のリリースは rootfs 版 <r> を
//
//	app 版 <v> から意図的に分離しており（docs/35 §35.3、build.sh --rootfs-json の
//	イメージ不変リリース）、CP 1.1.0 × Agent 1.0.0 は正常な状態になり得る。版比較だと
//	そこで「要再起動」が恒久点灯し、再起動しても消えない（バッジごと信用を失う）。
//	実体比較ならこのとき「変わらない」と正しく判定できる。
//
// ★ 「実体」は必ず起動時に自分で控えた値と、現在の値を同じ問い合わせで比べること。
//
//	docker で「走っているコンテナの {{.Image}}」対「タグの image {{.Id}}」のような
//	二辺比較をすると、containerd イメージストアでは同一イメージ由来でも digest の
//	表現が違って恒久点灯する（実測と詳細は runtime_docker.go）。
//
// ★ そして「実体」に digest を選んではならない。digest は内容ではなく表現で、内容が
//
//	変わらなくても動く。全層キャッシュヒットの docker build が provenance だけ付け
//	直すとタグの {{.Id}} は別物になる（2026-08-16 実測・runtime_docker.go）。控えるのは
//	層チェーン＋config のような内容そのものにする。
//
// 実装は Runtime の任意インタフェース staleRuntime。判らないときは必ず false に倒す。
//
//	docker : 起動時に控えたイメージの内容 ≠ いまのタグのイメージの内容（runtime_docker.go）
//	native : 起動時に控えた spawn 実体 ≠ 現在の spawn 実体（runtime_native.go）
//	ecs    : Start が登録したタスク定義に焼いた指紋 ≠ いまタグを ECR に引いた指紋
//	ecs-ec2: 同上（同じ実装に委譲）。どちらも runtime_ecs_stale.go
package main

import (
	"context"
	"sync"
	"time"
)

// staleRuntime は Runtime の任意実装。停止→起動で走るコードが変わるなら true。
// 判定できない（イメージが引けない・記録が無い）ときは false を返す契約。
type staleRuntime interface {
	Stale(ctx context.Context) bool
}

// workspaceStale は running なワークスペースについて判定する。呼び出し元は
// state=="running" のときだけ呼ぶこと（停止中は次の起動で必ず新しくなるので無意味）。
func workspaceStale(ctx context.Context, rt Runtime) bool {
	sr, ok := rt.(staleRuntime)
	return ok && sr.Stale(ctx)
}

// --- 小さな TTL キャッシュ ---------------------------------------------------

// freshness memoizes the probes above. /api/workspace is polled every 4s per open
// Console (and pushed over SSE), so an uncached docker inspect per request would
// multiply across tabs and users. The probed facts change only when an image is
// rebuilt or a container restarts, so a short TTL is plenty.
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

// set overwrites an entry. Used when the caller has just learned the authoritative
// value (docker Start) and a stale cached one would produce a wrong answer.
func (c *ttlCache) set(key, v string) {
	c.mu.Lock()
	c.m[key] = ttlEntry{v: v, at: c.clock()}
	c.mu.Unlock()
}
