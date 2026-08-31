# 13. P3-1 実装プラン — MetadataStore（SQLite）導入

> 🗄 **歴史的記録（完了）** — 現状は [HANDOFF §6.9](../HANDOFF.md)、設計は [ロードマップ P3-1](../roadmap.md#p3-1-metadatastoresqlite-既定-全ての土台)。以下は当時の実装プラン。

[12 Phase 3](../roadmap.md) の最初のワークストリーム **P3-1（DB 化）** の実装計画。
現状は `control-plane/manager.go` の `rts`（in-memory map）が source of truth で、port/token を
`docker inspect` から都度再構成し、`nextPort` カウンタで採番する（CP 再起動で停止中コンテナの
port が再採番されうる弱点 = [HANDOFF §6.7 末尾](../HANDOFF.md)）。本作業でこれを **SQLite を
source of truth** に置き換える。

## 13.1 ゴールと不変条件

- **SQLite を source of truth 化**: workspace の name/network/home/**port/token を永続**
  （`docker inspect` 依存と `nextPort` 採番レースを解消）。
- **後方互換**: `AUTH=dev`（`devUser`）/ `AUTH=proxy`（email）とも従来通り。
  **稼働中の `af-ws-*` は再作成しない**（port/token を inspect で採用し DB へ取り込む）。
- **Agent 契約は不変**（/sessions・/repos・/connections）。proxy 系（`proxy.go`）は無改修。
- **DB 既定 = SQLite**（[12 P3-1](../roadmap.md)）。pure-Go `modernc.org/sqlite`・WAL・goose 相当の冪等マイグレーション。

### スコープ外（P3-1 では「やらない」）

| 項目 | フェーズ |
|------|----------|
| クォータ強制 | P3-4 |
| 管理 API・UI | P3-5 |
| 封筒鍵（HMAC 据置） | P3-3 |
| Postgres アダプタ | P3-7（AWS/HA 時） |
| RBAC 強制（role は保存のみ） | P3-5 |
| session・repo の永続（proxy のまま） | P3-4/5 |

## 13.2 スキーマ（migration `0001_init.sql`, `//go:embed`）

```sql
CREATE TABLE tenant (
  id TEXT PRIMARY KEY, slug TEXT UNIQUE NOT NULL, name TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  limits TEXT NOT NULL DEFAULT '{}',          -- JSON。P3-4 が埋める
  isolation TEXT NOT NULL DEFAULT 'shared',   -- P3-8
  key_ref TEXT,                                -- P3-3
  created_at TEXT NOT NULL);

CREATE TABLE app_user (                        -- "user" は予約語ゆえ app_user（Postgres 互換）
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL REFERENCES tenant(id),
  email TEXT NOT NULL,
  user_key TEXT NOT NULL,                       -- 現行の sanitize 済みキー（コンテナ名安全）
  role TEXT NOT NULL DEFAULT 'member',          -- 保存のみ。強制は P3-5
  status TEXT NOT NULL DEFAULT 'active',
  last_login_at TEXT,
  UNIQUE(tenant_id, email));

CREATE TABLE workspace (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL REFERENCES tenant(id),
  user_id TEXT NOT NULL UNIQUE REFERENCES app_user(id),  -- 1 user = 1 workspace
  container_name TEXT NOT NULL,                 -- 既存名を保存（再計算しない=互換の肝）
  network TEXT NOT NULL, data_dir TEXT NOT NULL,
  agent_port TEXT NOT NULL,                     -- 永続=再採番しない
  agent_token TEXT NOT NULL,                    -- 永続=inspect 非依存
  state TEXT NOT NULL DEFAULT 'stopped',
  created_at TEXT NOT NULL, last_active_at TEXT);

CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
```

## 13.3 実装ステップ

### S0. 依存と土台
- go.mod に `modernc.org/sqlite`（pure-Go, cgo 無し → 静的バイナリ運用と整合）。
- 新ファイル: `control-plane/store.go`（interface=港）/ `store_sqlite.go`（実装）/
  `control-plane/migrations/0001_init.sql`（`//go:embed`）+ 小さな applier（`schema_migrations` で冪等適用）。
- DB オープン時 pragma: `journal_mode=WAL` `busy_timeout=5000` `foreign_keys=ON` `synchronous=NORMAL`。

### S1. Store 港（最小）
```go
type Store interface {
    EnsureDefaultTenant(ctx) (Tenant, error)
    UpsertUser(ctx, tenantID, email, key string) (User, error)
    GetOrCreateWorkspace(ctx, u User, alloc func() (port, token string)) (Workspace, error)
    SetWorkspaceState(ctx, id, state string) error
    ListWorkspaces(ctx, tenantID string) ([]Workspace, error)  // 管理用の布石
    Close() error
}
```

### S2. `manager` を DB 化（`manager.go` の中核改修）
- `manager` に `store Store`。`rts` map は **DB から再構成する in-mem キャッシュ**に格下げ（source of truth は DB）。
- `forUser`（manager.go:64）→ 新フロー:
  1. `resolveIdentity(r)` → (email, key)。dev: `devUser` を両方に / proxy: header→`sanitizeUser`。
  2. `EnsureDefaultTenant`（P3-1 は常に既定テナント）。
  3. `UpsertUser(tenant, email, key)`。
  4. `GetOrCreateWorkspace`: 新規なら port=`max(agent_port)+1`（DB 採番）/ token=`randHex(24)` /
     name=`af-ws-<key>`（既存方式維持）を**確定し永続**。
  5. workspace レコード + テンプレ（image/memory/sessionCmd/extraEnv/agentHost/secretKey）から `dockerRuntime` を構築。
- `secretKey` は **HMAC(master,key) 据置**（P3-3 で封筒化）。挙動不変。
- `nextPort` カウンタ廃止 → DB 採番。`rtFor`（runtime.go:143）は API 不変。

### S3. 起動時バックフィル（B5・現ライブ移行）
- schema migrate → `EnsureDefaultTenant` → 既存実態を走査して workspace レコードを冪等生成:
  - `dataRoot/*`（各ディレクトリ=user_key）と `docker ps -a --filter name=af-ws-` を突合。
  - 各キーに app_user 作成（email 不明なら key を仮置き）+ workspace 作成。
    **container_name/network/data_dir は既存名を採用**、`agent_port`/`agent_token` は既存ヘルパ
    `dockerPublishedPort`/`dockerEnvValue`（manager.go）で**採用**。
  - 運用者 `af-ws-k1-kami-gmail-com` → 既定テナント所属・**稼働中コンテナの port/token をそのまま DB へ**（再作成なし）。

### S4. config 配線（`main.go`）
- 新 env `AF_DB`（既定 `<WS_DATA>/control-plane.db`）。`openStore` → `migrate` → `backfill` → `mgr.store = store`。
- 既存 env は全維持。

### S5. 検証（OOM 注意・`free -h` で数 GiB 確保 — ホストのメモリ枯渇は稼働中フリート全体を巻き込む）
- 事前に home + 現状を退避。
- DB 起動 → 既定テナント + 運用者 user/workspace が**同一 port/token で**バックフィル（コンテナ再作成なし）
  → `/api/whoami`・workspace=running・sessions/connections 一覧 OK。
- **CP 再起動 → DB から解決・port 不変**（再採番レース解消の実証）。
- throwaway proxy user → DB 採番で別 port の workspace 永続。

## 13.4 主なリスク（コード根拠つき）

- **運用者コンテナを再作成しない**: 移行は inspect 採用のみ。`start`（runtime.go:51）だけが再作成し、それはユーザー操作時。
- **`user` 予約語** → `app_user`。
- **SQLite 並行**: CP は並行ハンドラ → 単一 `*sql.DB` + WAL + busy_timeout、書き込みは直列化（modernc で十分）。
- **port 永続 vs 実体差**: 手動削除済みなら DB の port で publish。競合時は start が明示 fail。小規模で許容。

## 13.5 成果物

`store.go` / `store_sqlite.go` / `migrations/0001_init.sql` / `manager.go` 改修 / バックフィル / `main.go` 配線 / go.mod 依存追加。
