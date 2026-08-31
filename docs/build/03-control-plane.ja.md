# 03. Control Plane

[English](03-control-plane.md) | 日本語

Audience: Control Plane を変える人
Source of truth: コード（本書は地図と設計意図）
Updated: 2026-07

CP は Workspace の外側で動く唯一の常駐バックエンド（Go 単一バイナリ）。ブラウザは常に CP とだけ話し、
CP は tmux にも working copy にも直接触れず必ず Agent 経由で操作する（[01 §1.3](01-architecture.ja.md)）。
本書は「CP に何が住んでいて、どう繋がるか」。ワイヤ契約は [05](05-api.ja.md)、セキュリティ設計は [07](07-security.ja.md)。

## 3.1 責務地図

- **Console 静的配信** — `console/dist` を `/`（catch-all）で `no-store` 配信。デプロイ即反映（[05 §5.4](05-api.ja.md)）。
- **authGate（L1 認証）** — `AUTH=oauth` では CP 自身がエッジ: 全リクエストの署名 cookie 検証、受信
  `X-Forwarded-Email` の削除と検証済み値の再注入、許可リスト fail-closed。dev/proxy モードはゲート無し（[07 §7.3](07-security.ja.md)）。
- **identity / tenant 解決** — 検証済み email → `identity`、`X-AF-Tenant`（→query）→ membership 検証。
  未知 identity のプロビジョニング（`AF_PROVISION` auto|invite）と super_admin 付与（`SUPER_ADMIN_EMAILS`）もここ。§3.2。
- **Workspace ライフサイクル** — membership 1 件 = コンテナ 1 本の払い出し、Start/Stop/Recreate、状態の DB 同期。
  実行基盤は Runtime アダプタ（docker|ecs）越し。§3.3。
- **Agent 中継** — REST / SSE / terminal WS / browser REST+WS / preview の5経路で公開 API をAgentへ転送。経路の特性は [05 §5.3](05-api.ja.md)。
- **MetadataStore** — tenant/identity/workspace/セッションミラー等の永続化。SQLite 既定・Postgres 可（[06](06-data.ja.md)）。
- **監査** — proxy 層（変更系）・admin API・MCP write・システムジョブが `audit_log` へ記録。
  書き込み点は [05 §5.5](05-api.ja.md)、設計は [07 §7.7](07-security.ja.md)。
- **MCP サーバ** — `/mcp` で member/admin ツールを外部の Claude クライアントに公開。§3.5。
- **内蔵 git プロバイダ** — bare リポジトリ + smart-HTTP + LFS を CP 自身がホスト（Agent 非経由・[91](91-internal-git.ja.md)）。
- **egress 統制** — forward proxy への policy 配布・観測イベント集約・admin API。§3.8。
- **memo キュー** — membership 単位のメモ永続と一括送信。コンテナ停止中も使える「CP 完結」機能。§3.6。
- **定時実行（scheduler）** — スケジュール定義を CP DB に永続し、CP 内 goroutine（`scheduler.go`）が cron 評価・発火
  （tz は埋め込み IANA DB で DST 込み解決）。作成/編集はオペレーター経由の `/internal/schedules`
  （`AF_SCHEDULE_TOKEN`）、Console の `/api/schedules` は閲覧・管理のみ（[docs/38](../decisions/0021-scheduled-execution.ja.md)）。
- **通知** — Agent の通知 outbox を取得時に CP ストアへ drain し、`/api/notifications` で一覧・既読管理
  （保持 7 日。`notification.go`）。
- **MCP サーバレジストリ（テナント配布）** — tenant_admin が全メンバーへ配る MCP サーバ定義を CP DB に保持
  （`/api/admin/mcp-servers` + Agent が poll する `/internal/mcp-servers`）。メンバー個人の登録
  （`/api/mcp-servers`）は Agent 側で合成され CP は中継のみ（[docs/48](../decisions/0031-mcp-registry.ja.md)）。
- **バックグラウンドジョブ** — reaper / usage サンプラー / git GC / claude-audit sweep。§3.7。
- なお**掃除**（cleanup: 調査・削除・gz ごみ箱 `/api/sessions/cleanup`・`/api/cleanup/archives*`）と
  **エージェントメモリ管理**（snapshot/restore/export `/api/agents/memory/*`・[docs/39](../decisions/0022-agent-memory-management.ja.md)）は
  **Agent 側の機能**で、CP はそのまま中継する（Agent 中継の一部）。

実装ファイルへの対応は [90-code-map](90-code-map.ja.md)。

## 3.2 リクエストの一生

公開 API はどれも同じ前段を通る（認可の原則・エラー形は [05 §5.4](05-api.ja.md) が正）:

1. **authGate**（oauth モードのみ）— cookie 検証と email 注入。`/oauth2/*`・`/login`・`/healthz` 等は除外、
   `/mcp`（Bearer PAT）と `/git/*`（Basic git token）は自前認証（[07 §7.3](07-security.ja.md)）。
2. **resolveIdentity** — email → `identity`（dev は固定 `DEV_USER`）。未知なら `AF_PROVISION=auto` で
   既定テナントへ自動プロビジョン、`invite` なら拒否。
3. **membership 検証** — `X-AF-Tenant` ヘッダ → `?tenant=` query の順で解決し membership と突き合わせ。
4. **rtFor（workspace runtime 解決）** — membership → `workspace` 行（無ければ払い出し §3.3）→ DEK 解決（§3.4）
   → RuntimeFactory で Runtime 構築（membership id キーの in-memory キャッシュ、DB が正）。
   全 ingress が接続追跡（conns）に活性を記録する。
5. **handler or proxy** — CP 完結（memo / pat / ssm / admin / ws-settings / 内蔵 git）はここで処理、
   他は5経路（[05 §5.3](05-api.ja.md)）で Agent へ。中継は workspace running が前提（stopped=409）だが、
   意図の明確な操作（セッション作成 / fork / start）だけは `AF_AUTOSTART`（既定 on）が冷えた workspace を
   起こしてから通す。端末接続や読み取りは自動起動しない。auto-start 側は `ensureWorkspaceReady`
   （起動 → Agent 到達待ち、既定 55 秒 = ingress idle timeout の内側）を通り、間に合わなければ
   409 `workspace_starting` で返す — 起動自体は裏で続くので、再試行が次に通る（docs/38 ★6 恒久対応）。

## 3.3 manager と Runtime 抽象

- **manager** が per-membership の資材を初回に払い出し DB へ永続する: コンテナ名 `af-ws-<slug>-<key>`・
  専用ネットワーク `af-net-<slug>-<key>`・home `<WS_DATA>/<slug>/<key>/home`
  （**既定テナントは slug 無し**の `af-ws-<key>` / `af-net-<key>` / `<WS_DATA>/<key>/home` —
  既存デプロイをそのまま使い続けるための互換分岐。`workspaceNames`）・Agent ポート（`WS_AGENT_PORT` 基点で採番）・
  `AGENT_TOKEN`。CP 再起動では DB の行が正で、既存コンテナは inspect で採用し**再作成しない**（再起動耐性）。
- **Runtime / RuntimeFactory interface** が実行基盤を抽象化し、全呼び出し点（handler・reaper・admin・MCP）が
  factory 経由で構築する。docker / ecs はプロファイル 1 箇所の切替（`AF_RUNTIME`）。対応表は
  [01 §1.6](01-architecture.ja.md)、デプロイ選定は [09](09-deploy.ja.md)。
- **Start** = docker run 相当: home と claude-config の 2 マウント、専用ネットワーク（`af-net-*`）の ensure、
  `AGENT_TOKEN` / `AF_SECRET_KEY` / `CLAUDE_CONFIG_DIR` 等の env 注入、Agent healthy 待ち。
  **Stop** は二段の graceful stop: SIGTERM →猶予（`AF_STOP_GRACE_SEC` 既定 30s。Agent には安全マージンを
  差し引いた `AGENT_STOP_GRACE_SEC` を渡し、pane の Ctrl-C → tmux 終了を先に済ませる）→ SIGKILL。
- **接続追跡（conns）** — 端末/preview/browser viewerのlong-lived接続数・セッション別アタッチ・最終リクエスト時刻を
  in-memory で記録。開いている接続がある限り workspace は warm に保たれ、reaper（§3.7）の判定材料になる。
  browser viewerは`visibility=false`中と切断後の猶予Pageを接続数へ含めない。

## 3.4 起動時の鍵配線

暗号設計そのもの（封筒暗号・KEK 導出・crypto-shred の限界）は [07 §7.6](07-security.ja.md)。CP 側の配線だけ書く:

- **boot 時**: `AF_MASTER_KEY` があればハッシュして master 鍵とし、KeyCustodian（現実装 localCustodian）を
  構成する。無ければ暗号なし（dev、Agent は平文保存）。
- **workspace 解決時**: `wrapped_dek` を custodian で unwrap（初回はレガシー DEK を導出して wrap 保存 —
  既存 `secrets.enc` を再暗号化しないための互換点）→ 平文 DEK を `AF_SECRET_KEY` としてコンテナ起動時に
  env 注入。Agent は暗号方式に無関心で、鍵の出自を知らない。

## 3.5 MCP サーバ

設計と決定は [decisions/0006](../decisions/0006-mcp-unified.ja.md)。`AF_MCP_ENABLED=true` のときだけ `/mcp` を登録する。

- **トランスポート**は Streamable HTTP の最小形: POST の JSON-RPC 2.0（単発 + batch）に `application/json` で
  応答（SSE なし）。エッジは `/mcp` を Bearer のまま素通しする必要がある。
- **認証は PAT**（Console 発行・DB はハッシュのみ・[06](06-data.ja.md)）。role は発行時に凍結せず
  **呼び出しごとに live 再解決**、tenant はトークン固定でクライアント供給を受けない。
- **member 4 ツール**（`list_my_sessions` / `get_session_status` / `get_session_output` / `send_to_session`）
  — 主目的は「手元の Claude が自分の遠隔 claude セッション群を駆動する」こと。
- **admin ツール** — read（`list_workspaces` / `get_usage` / `list_sessions` / `tail_audit` / egress 観測系）+
  write（`stop_workspace` / `stop_session` / `set_user_quota` / `propose_allowlist_change`＝提案のみ）。
  super_admin / tenant_admin で gate し、write は `audit_log` に `actor_kind=mcp` で記録する。
- 残: dangerous ツール（鍵ローテ・recreate 等、confirm + dry-run 前提）📋 — [decisions/0006](../decisions/0006-mcp-unified.ja.md)。

## 3.6 memo キュー

「溜めて一括でセッションへ送る」メモ（テーブルは [06](06-data.ja.md)）。
実装済み・main マージ済み（CP CRUD / flush / 整理用 `/api/chat/ask` 露出 / Console UI の全フェーズ。
docs/21 冒頭の「未実装」注記は設計時点のもの）。

- **CRUD は CP 完結**: membership 解決だけで済み workspace を起動しない — 停止中でも別端末から追加・整理できる。
  repo × category の 2 段でグルーピング。同期はサーバプッシュが無いためポーリング粒度。
- **flush**（`POST /api/memos/flush`）: `ids` リストで選択（レポ全体 / カテゴリ / 個別の 3 粒度を統一表現）→
  category 見出しで 1 メッセージに連結 → 対象セッションの input へ **1 回だけ**送信 → `sent_at` 打刻。
  送信だけは Agent 経由なので runtime 解決（autostart 対象）。
- **retention**: 送信済みは削除せず 7 日残し、一覧取得時に lazy sweep で掃除する。

## 3.7 バックグラウンドジョブ

いずれも CP 内の goroutine。間隔は env で、`0` は無効化（安全側の既定を持つものが多い）。

- **reaper（idle-stop）** — `AF_IDLE_SWEEP_INTERVAL`（既定 1m。タイムアウト自体は既定無効で、テナント limits
  か env で opt-in）。二段構え: **tier 1** = アタッチされていない idle な claude セッションが
  `session_idle_timeout` を超えたら halt（jsonl があるので再開可能。shell は halt しない）。**tier 2** =
  活性のない workspace（開いた接続 0・working/question セッション無し・最終リクエストから
  `ws_idle_timeout` 経過）を docker stop。判定材料は接続追跡（§3.3）。starting 状態は触らない。
- **usage サンプラー（showback）** — `AF_USAGE_SAMPLE_INTERVAL`（既定 5m）。running な workspace に占有秒を
  日次バケツ（`usage_daily`）へ加算。BYO モデルで運用者コストなのは Claude 使用量でなく占有時間、という設計。
- **git GC** — `AF_GIT_GC_INTERVAL`（既定 24h）。内蔵 git の bare を `git gc --auto` + LFS 孤児 prune
  （`AF_LFS_GC_GRACE` 既定 14d で進行中 push と競合しない）。共有ホストの RAM を守るため逐次実行（[91](91-internal-git.ja.md)）。
- **claude-audit sweep** — `AF_CLAUDE_AUDIT_INTERVAL`（既定 0=off・opt-in）。コンテナ内 claude の直接操作は
  CP proxy を通らず見えないが、Agent→CP 方向は塞いであるので **CP が pull** する: 各 running claude
  セッションの transcript を読み Write/Edit/Bash を `actor_kind=claude` で監査。セッション毎 cursor で増分、
  初見は baseline のみ（過去分を遡って監査しない）。
- なお **metrics** は常駐ジョブではなく on-demand: CP がホストプロセスとして /proc と cgroup v2 を直読みし、
  自分の workspace の mem/CPU チップ（全ユーザー）とホスト統計（super_admin 限定 — 相互不可視のため他
  テナントの混み具合を漏らさない）を返す。

## 3.8 egress 統制の CP 側

設計・段階運用（log-only → allowlist → enforce 🚧）は [07 §7.8](07-security.ja.md) と
[07 §7.8](07-security.ja.md)。CP に住んでいるのは次の 4 点だけ:

- **egress-proxy サブコマンド** — 同一バイナリ/イメージを `control-plane egress-proxy` で起動すると
  forward proxy として併走する（FQDN 判定・TLS 非復号）。
- **policy 配布** — `GET /internal/egress/policy` が実効 allowlist + mode を proxy へ返す。
- **ingest** — `POST /internal/egress`（`AF_EGRESS_TOKEN` 認証）で観測イベントを受け、`egress_daily` へ
  日次集計（would-block は day×host で dedup して監査にも記録）。
- **admin API** — `/api/admin/egress*` で観測統計・allowlist（active/proposed/retired）・mode 切替（super_admin）。
- コンテナ側の配線は `AF_EGRESS_PROXY_ADDR` 設定時のみ全 workspace に proxy env を注入（既定 off = 何も変わらない）。
