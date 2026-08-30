// workspace_stale_test.go — 「停止→起動で走るコードが変わるか」判定の契約テスト。
// ここが誤検出すると WS バーに消えない「要再起動」が出続けて信用を失い、逆に取り
// こぼすと更新が反映されないまま気付けない。判らないときは stale ではない、を固定する。
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// docker は「起動時に控えたイメージの内容」と「いまのタグのイメージの内容」を比べる。
//
// ★ 走っているコンテナの {{.Image}} は見てはいけない。containerd イメージストアでは
//
//	コンテナの {{.Image}}（プラットフォーム config の digest）と image の {{.Id}}
//	（マニフェスト/インデックスの digest）が別物で、同じイメージから起動していても
//	一致しない。旧実装はこれを直接比較していたため、dev フリート（Driver=overlayfs）で
//	何も更新していなくても「要再起動」が恒久点灯した。ここでその二辺比較へ戻るのを防ぐ。
//
// ★ 控える値に digest を選んでもいけない（2026-08-16 の再発）。全層キャッシュヒットの
//
//	docker build が provenance だけ付け直すと、内容が 1 バイトも変わらないのにタグの
//	{{.Id}} が config digest → インデックス digest へ変わり、また恒久点灯する。
//	内容（層チェーン＋config）で比べていればここは黙る、を固定する。
func TestDockerStaleImageStamp(t *testing.T) {
	orig := freshness
	defer func() { freshness = orig }()
	now := time.Unix(1000, 0)
	freshness = &ttlCache{m: map[string]ttlEntry{}, now: func() time.Time { return now }}

	const (
		built    = "sha256:b05a9622 |dev|/home/dev|[/usr/local/bin/entrypoint.sh]|[workspace-agent]|[]|map[]"
		rebuilt  = "sha256:aaaa1111 |dev|/home/dev|[/usr/local/bin/entrypoint.sh]|[workspace-agent]|[]|map[]"
		envOnly  = "sha256:b05a9622 |dev|/home/dev|[/usr/local/bin/entrypoint.sh]|[workspace-agent]|[TZ=Asia/Tokyo]|map[]"
		ctrLocal = "sha256:02a946de" // 同じイメージから起動したコンテナの {{.Image}}
	)
	dir := t.TempDir()
	fp := built
	d := &dockerRuntime{image: "agent-fleet/workspace:dev", name: "af-ws-x", dataDir: dir}
	d.inspect = func(_ context.Context, typ, _, format string) string {
		if typ == "container" {
			t.Errorf("コンテナの {{.Image}} を参照した — 表現差で恒久誤点灯する二辺比較に戻っている")
			return ctrLocal
		}
		if strings.Contains(format, "{{.Id}}") {
			t.Errorf("イメージの {{.Id}} を控えている — 内容が同じでも動く表現で、再び恒久誤点灯する")
		}
		return fp
	}
	ctx := context.Background()

	// 記録が無い（この機能より前に起動したコンテナ／CP 外で起動）＝判らない → false。
	if d.Stale(ctx) {
		t.Fatal("no stamp: stale, want false")
	}

	// Start 相当。以後、同じイメージで走っている限り出してはいけない。
	d.recordImageStamp(ctx)
	if d.Stale(ctx) {
		t.Fatal("same image: stale, want false")
	}

	// 内容が同じままタグだけ貼り直された（全層キャッシュヒットの再ビルド）→ 出さない。
	// これが 2026-08-16 に恒久点灯した経路そのもの。
	now = now.Add(2 * time.Minute) // TTL 越え（4s ポーリングを毎回 inspect しないための帽子）
	if d.Stale(ctx) {
		t.Fatal("cache-hit rebuild (same content, new tag digest): stale, want false")
	}

	// 層が焼き直された（ローカル docker build / 新リリースの pull）→ 出す。
	fp = rebuilt
	now = now.Add(2 * time.Minute)
	if !d.Stale(ctx) {
		t.Fatal("rebuilt layers: not stale, want stale")
	}

	// その状態で再起動 → 新しいイメージを控え直すので、TTL 内でも即座に消えること。
	d.recordImageStamp(ctx)
	if d.Stale(ctx) {
		t.Fatal("restarted onto the new image: stale, want false")
	}

	// 層は同じで ENV だけ変えた Dockerfile（層を生まない変更）も取りこぼさない。
	fp = envOnly
	now = now.Add(2 * time.Minute)
	if !d.Stale(ctx) {
		t.Fatal("config-only change (ENV): not stale, want stale")
	}

	// タグが引けない（レジストリのみのタグ・docker 不在）＝判らない → false。
	fp = ""
	now = now.Add(2 * time.Minute)
	if d.Stale(ctx) {
		t.Fatal("unreadable image: stale, want false")
	}
}

// 旧 {{.Id}} スタンプが残っているだけの状態（この修正より前に起動したコンテナ）は
// 「判らない」＝ stale ではない。ID と内容指紋を比べれば必ず不一致になるので、読んで
// しまうと修正した当のバグ（恒久点灯）をそのまま引き継ぐ。次の Start で解消する。
func TestDockerStaleLegacyIDStampIgnored(t *testing.T) {
	orig := freshness
	defer func() { freshness = orig }()
	freshness = &ttlCache{m: map[string]ttlEntry{}}

	dir := t.TempDir()
	d := &dockerRuntime{image: "agent-fleet/workspace:dev", name: "af-ws-x", dataDir: dir}
	d.inspect = func(context.Context, string, string, string) string { return "sha256:b05a9622 |dev|" }
	if err := os.WriteFile(d.legacyImageIDStampPath(), []byte("sha256:97f63692"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d.Stale(context.Background()) {
		t.Fatal("legacy id stamp: stale, want false")
	}

	// 次の Start で内容指紋へ移行し、置き去りの旧スタンプは消しておく。
	d.recordImageStamp(context.Background())
	if _, err := os.Stat(d.legacyImageIDStampPath()); !os.IsNotExist(err) {
		t.Fatalf("legacy stamp left behind: err=%v", err)
	}
	if d.Stale(context.Background()) {
		t.Fatal("after migration: stale, want false")
	}
}

// 素の native（dev: AF_NATIVE_AGENT_BIN）は workspace-agent バイナリ自体が実体。
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

	if err := os.WriteFile(n.binStampPath(), []byte(n.spawnStamp()), 0o644); err != nil {
		t.Fatal(err)
	}
	if n.Stale(context.Background()) {
		t.Fatal("unchanged binary: stale, want false")
	}

	// 再ビルドでバイナリが差し替わった状態（内容もサイズも変わる）。
	if err := os.WriteFile(bin, []byte("v2-longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !n.Stale(context.Background()) {
		t.Fatal("replaced binary: not stale, want stale")
	}
}

// パッケージ版 native は rootfs モードで、agentBin は bwrap（リリース間で同一）。
// 実体は版で切られた rootfs ディレクトリのほう — ここを間違えると、
//
//	・bwrap を見る → af update で rootfs が変わっても検出できない
//	・CP 版と Agent 版を比べる → rootfs 版 <r> は app 版 <v> と分離されている
//	  （docs/log/35・build.sh --rootfs-json のイメージ不変リリース）ので恒久誤点灯
//
// の両方を踏む。af update との噛み合わせをここで固定する。
func TestNativeStaleRootfsIdentity(t *testing.T) {
	dir := t.TempDir()
	bwrap := filepath.Join(dir, "bwrap") // リリースをまたいでも同じ中身
	if err := os.WriteFile(bwrap, []byte("bwrap"), 0o755); err != nil {
		t.Fatal(err)
	}
	mkRootfs := func(ver string) string {
		p := filepath.Join(dir, "rootfs", ver)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, ".ok"), []byte("1"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	old, newer := mkRootfs("0.3.0"), mkRootfs("0.4.0")

	running := &nativeRuntime{agentBin: bwrap, rootfs: old, dataDir: dir}
	if err := os.WriteFile(running.binStampPath(), []byte(running.spawnStamp()), 0o644); err != nil {
		t.Fatal(err)
	}
	if running.Stale(context.Background()) {
		t.Fatal("same rootfs: stale, want false")
	}

	// af update apply → CP が新しい rootfs 版で再起動。走っている agent は旧 rootfs 由来。
	if !(&nativeRuntime{agentBin: bwrap, rootfs: newer, dataDir: dir}).Stale(context.Background()) {
		t.Fatal("moved rootfs: not stale, want stale")
	}

	// イメージ不変リリース（app 版だけ上がり rootfs ピンは据え置き）＝再起動しても
	// 走るコードは変わらない → 出してはいけない。
	if (&nativeRuntime{agentBin: bwrap, rootfs: old, dataDir: dir}).Stale(context.Background()) {
		t.Fatal("immutable-rootfs release: stale, want false")
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
