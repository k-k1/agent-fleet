# 19. P3-9 実装プラン — アイドル自動停止（scale-to-zero, 二段構え）

> 🗄 **実装記録** — 現状は [HANDOFF](../HANDOFF.md)、設計は [ロードマップ P3-9](../roadmap.md#p3-9-運用の成熟社内旧-phase-4-を吸収)。

[12 Phase 3](../roadmap.md) の P3-9 のうち **idle-stop（scale-to-zero）** を前倒し実装。オンプレ単一ホストは
RAM 逼迫で live fleet を OOM させる（[host-oom-fleet-risk]）ため、実運用上きわめて重要。**BYO ゆえ止めるのは
インフラ資源のみ**（会話・home・資格は永続、次アクセスで復元）。

## 19.1 ゴールと不変条件

- **二段構え**（軽→重の段階的回収）:
  - **第1段 — セッション自動停止**: 放置された **claude セッション**を `POST /sessions/{name}/halt` で
    **停止中（resumable）** に畳む。会話は jsonl に永続化されており `claude --resume` で文脈ごと復元＝実質
    hibernate。重い claude プロセスの RAM をコンテナ稼働のまま回収。
  - **第2段 — Workspace 停止**: 接続も稼働も無く冷え切った workspace を `docker stop`（= 既存 `stop()`＝`docker rm -f`、
    home は bind-mount で永続）でコンテナごと停止し、残りの RAM を回収。
- **shell は第1段の対象外**: halt = `tmux kill-session`＝ハード kill。shell の生プロセス・実行中ジョブ・環境は
  永続でないため自動 halt しない（走行中のビルド等を殺さない）。shell は第2段の WS 停止に委ねる
  （どのみち `docker stop` で tmux ごと消える）。**hibernate（CRIU）は不採用**（コンテナ前提で重い）。
- **既定は無効・opt-in**（P3-4 クォータと同じ「既定は現挙動不変」思想）: shipped 既定は **0=無効**
  （実ユーザー稼働中の live デプロイを不意に停止しない）。super_admin がテナント別 limits で opt-in（再起動
  不要）、または env でデプロイ全体に既定を与える。`"0"` で明示無効化。
- **稼働・視聴中は止めない**: working/question のセッションがある、または端末/preview/ocweb 接続が開いている
  workspace は対象外。視聴中の claude セッションは第1段でも halt しない。

## 19.2 設定（テナント別・super_admin 編集可）

`tenant.limits`（既存 JSON 列、P3-4）に 2 フィールド追加。人間可読の duration 文字列:

```json
{ "max_workspaces": 0, "max_sessions": 0,
  "session_idle_timeout": "30m", "ws_idle_timeout": "60m" }
```

- 空文字 → deployment 既定（env、shipped 既定は 0=無効）にフォールバック。`"0"`（非正）→ その次元を無効化。不正文字列 → 既定。
- 解決: `idleTimeout(tenantVal, def) (time.Duration, enabled bool)`（`manager.go`）。
- admin API: 既存 `PUT /api/admin/tenants/{slug}/limits`（super_admin gate）を拡張。書式不正は `400 bad_duration`。
- admin UI: `AdminTab` テナント詳細に「Session halt まで / Workspace 停止まで」入力。
- deployment 既定 env: `AF_SESSION_IDLE_TIMEOUT`(既定 0=無効) / `AF_WS_IDLE_TIMEOUT`(既定 0=無効) /
  `AF_IDLE_SWEEP_INTERVAL`(60s、`0` で reaper 自体を停止)。推奨運用値は 30m/60m 目安だがテナント別 opt-in が既定運用。

## 19.3 実装（CP 集中・Agent 無改修）

テナント別 timeout は CP の DB にあるため、**両段とも CP の単一 reaper goroutine が駆動**（Agent の
per-session live state を読むだけ、Agent は無改修）。

### connRegistry（`reaper.go`）— DB が持たないライブ信号を in-memory 追跡

- `wsConns[wsID]` … 開いている長命接続数（端末/preview/ocweb）＝「視聴中」。
- `attached[wsID][session]` … 端末アタッチ中セッション＝第1段で halt しない。
- `lastSeen[wsID]` … 最後の**ユーザー起点**アクティビティ時刻。
- ingress で更新: `proxyTerminal`=addConn/doneConn（session 付き）、`handlePreview`/`handleOcweb`=touch、
  `proxyAgentREST`=**mutating（非 GET/HEAD）のみ** touch。**背景ポーリング（GET /api/sessions・/api/workspace）は
  touch しない**——さもないと開きっぱなしのタブが RAM を永久固定し idle-stop を無効化する。真の在席は端末
  アタッチか busy セッションで表現。

### reaper.sweep（`AF_IDLE_SWEEP_INTERVAL` 毎）

各テナント → limits 解決 → running な各 workspace について:

1. `docker inspect` で running のみ対象。`GET /sessions`（Agent）を1回取得＝両段の材料。
2. **第1段**: `kind==claude && alive && state==idle && 端末未アタッチ` のセッションが `session_idle_timeout`
   継続 idle なら halt。idle 開始時刻は reaper 内 `idleSince[wsID|name]` で追跡（busy/アタッチ/消滅でリセット）。
3. **第2段**: `wsConns==0 && busy でない` かつ idle 基準時刻から `ws_idle_timeout` 経過なら `docker stop` ＋
   `SetWorkspaceState(stopped)`。idle 基準 = max(in-memory lastSeen, DB last_active_at, **reaper boot time**)。
   boot time を下限に含めることで **CP 再起動直後に全 WS を即停止しない**猶予窓を与える。

段の staging: `session(30m) < ws(60m)` により、まず claude が畳まれ（RAM の大半を回収）、その後コンテナが停止。

## 19.4 auto-start（オンデマンド起動）= 実装済（scale-to-zero 完結）

idle-stop の**対**。停止中 WS へユーザーが**意図的にアクセス**した時、透過的にコンテナを起動する
（手動 Start ボタン不要に）。これで「冷えたら止まる → 使う時に自動で戻る」が完結。

- **トリガは 2 つの明確な "今から使う" 動作のみ**: 端末 WS アタッチ（`proxyTerminal`）とセッション作成
  （`handleSessionCreate`）。**背景 GET ポーリング（session list / workspace state）は通さない**ので idle-stop の
  意味論（開きっぱなしタブで温め続けない）を壊さない。
  - > 🗄 **その後、端末アタッチは外れた**（現行 = `proxy.go` の `proxyTerminal`「No auto-start」）。
    > セッションを 1 つクリックしただけで（= `/ws/pty` が開くだけで）WS 全体が黙って起き上がるため。
    > 現在の auto-start はセッション作成 / fork / start / 持ち越し回答 / SSM ノード探索の 5 本で、
    > 停止中の端末は 409 `workspace_stopped` を返して「起動してから」に倒す。現行の正は
    > [dev/03 §3.2](../build/03-control-plane.ja.md)。
- **共有コア** `config.ensureWorkspaceStarted`: State!=running なら max_workspaces クォータ（P3-4）を課してから
  `Runtime.Start`（healthz 待ち）→ DB state=running。手動 start/recreate もこれに集約。`res.rt` は DEK 付きで
  ビルド済ゆえ Start が `AF_SECRET_KEY` を正しく注入。
- **既定 on**、`AF_AUTOSTART=0` で無効（明示 Start のみに戻す）。Runtime 港越しゆえ ECS では desired=0→1 が同じ
  seam に載る（P3-7 と共通化）。
- 検証: 隔離した使い捨て CP + テスト WS で start→stop→意図的 POST の後に state が **stopped→running** へ復帰する
  ことを確認（セッション作成ペイロードが不正でも auto-start は先に発火＝proxy 前）。運用者の実 WS には非接触。
- 残（スコープ外）: ディスク強制 / 観測アラート / egress 統制（P3-9 の残項目）。

## 19.5 触れたファイル

- `control-plane/reaper.go`（新規: connRegistry + reaper）、`manager.go`（tenantLimits 拡張・idleTimeout・
  manager.conns）、`proxy.go`/`preview.go`/`ocweb.go`（ingress 配線）、`tenants.go`（limits API 拡張）、
  `main.go`（env 既定 + reaper 起動）、`console/src/settings/AdminTab.jsx`（編集 UI）。
- **スキーマ変更なし**（既存 `tenant.limits` JSON 列を再利用）。**Agent 無改修**（既存 halt / `GET /sessions` を利用）。
- auto-start（19.4）: `runtime.go`（`ensureWorkspaceStarted` 抽出・`startResolved` 委譲・`handleSessionCreate` 注入）、
  `proxy.go`（`proxyTerminal` 注入 — **後に撤去。19.4 の注記参照**）、
  `main.go`（`config.autostart` + `envBool` + `AF_AUTOSTART`）。**Agent 無改修**。
