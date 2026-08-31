# 16. P3-4 実装プラン — リソースバジェット / クォータ

> 🗄 **歴史的記録（完了）** — 現状は [HANDOFF §6.9](../HANDOFF.md)、設計は [ロードマップ P3-4](../roadmap.md#p3-4-リソースバジェット--クォータテナント--ユーザー)。以下は当時の実装プラン。

[12 Phase 3](../roadmap.md) の P3-4。BYO ゆえ対象は**インフラ資源のみ**。各社が自社ホスト資源を守るための
ハードクォータ（超過は `429`）。**既定は無制限**（limits 未設定 → 現挙動のまま。ライブ運用者は無影響）。

## 16.1 ゴールと不変条件

- **ハードクォータ**（block, `429` + 明示コード）。overage は内部配分の話で不要。
- **既定無制限**: `tenant.limits` が空/0 の次元は無制限。**limits を設定するまで一切の挙動変化なし**。
- **enforcement 用に state を正にする**: P3-1 で先送りした `workspace.state` の DB 同期を**ここで入れる**
  （稼働中 workspace 数を数えるのに必須）。`SetWorkspaceState` を Start/Stop に配線。

### 次元（この increment）

| 次元 | 上限 | 強制ポイント | 数え方 |
|------|------|--------------|--------|
| Workspace 数 / テナント | `max_workspaces` | `Workspace.Start` | DB の running workspace 数（state 同期後）|
| セッション数 / ユーザー | `max_sessions` | `Session.Create` (POST /api/sessions) | その workspace の Agent `GET /sessions` を数える |
| メモリ | （`max_workspaces` で代替）| — | 全 workspace が同一 `WS_MEMORY` ゆえ Workspace 数が実質メモリ上限。個別 mem サイズは後続 |
| ディスク | （後続）| — | du ベースは重く、計測のみ後続（P3-9/メータリング）|

### スコープ外（P3-4 では「やらない」）

ディスク強制 / per-tenant セッション合計（Agent 横断 sum）/ 個別 mem・cpu サイズ / UsageCounter サンプリング・showback（P3-9）/ Console 表示（P3-5）。

## 16.2 スキーマ（migration `0004`）

`tenant.limits`（既存 JSON 列）に `{"max_workspaces":N,"max_sessions":N}`。per-user 上書きは新表:

```sql
CREATE TABLE user_limit (
    membership_id TEXT PRIMARY KEY REFERENCES membership(id),
    max_sessions  INTEGER NOT NULL DEFAULT 0,   -- 0 = テナント既定/無制限
    disk_gb       INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL
);
```

## 16.3 Store 追加

```go
SetWorkspaceState(ctx, workspaceID, state string) error
CountRunningWorkspaces(ctx, tenantID string) (int, error)   // state='running'
GetUserLimit(ctx, membershipID string) (UserLimit, bool, error)
PutUserLimit(ctx, membershipID string, maxSessions, diskGB int) error
SetTenantLimits(ctx, tenantID, limitsJSON string) error
```
`tenant.limits` のパースは CP 側ヘルパ `parseLimits(json) {MaxWorkspaces, MaxSessions int}`。

## 16.4 manager / handler の変更

- **resolve の戻りを拡張**: 現 `resolve` は `*dockerRuntime` のみ。Start/Stop/Session 強制には ws/tenant/membership が要る →
  `resolved{ rt, ws, ident, mv }` を返す内部版を作り、`rtFor`（proxy 用）はそこから `rt` を取り出す。
- **`Workspace.Start`**: `limits=parseLimits(tenant.limits)`; `max_workspaces>0 && 当 ws が未 running && CountRunningWorkspaces(tenant) >= max_workspaces` → `429 quota_workspaces`。起動成功後 `SetWorkspaceState(ws.id,"running")`。
- **`Workspace.Stop`**: 停止後 `SetWorkspaceState(ws.id,"stopped")`。
- **`Session.Create`**（POST /api/sessions の proxy 前にフック）: `lim = user_limit.max_sessions || tenant.max_sessions`; `lim>0` なら Agent `GET /sessions` を数え `>= lim` → `429 quota_sessions`。OK なら通常 proxy。
- 注: CP 再起動時の state 整合は `backfill`/起動時に docker 実態から補正（best-effort）。

## 16.5 管理 API（super_admin、最小）

- `PUT /api/admin/tenants/{slug}/limits` `{max_workspaces, max_sessions}` → `SetTenantLimits`。
- `PUT /api/admin/user-limits` `{user_key, tenant_slug, max_sessions, disk_gb}` → membership 解決 → `PutUserLimit`。
- 取得は `GET /api/tenants` 拡張 or `GET /api/admin/...`（P3-5 で UI 化、ここは最小）。

## 16.6 検証（OOM 注意 — ホストのメモリ枯渇は稼働中フリート全体を巻き込む）

1. **無制限既定**: 運用者は limits 未設定 → Start / session 作成が従来どおり。state 同期で DB state=running。
2. **workspace クォータ**: throwaway テナントに `max_workspaces=1` + 2 メンバー → 1人目 Start OK・running 記録、2人目 Start → `429`。
3. **session クォータ**: あるユーザーに `max_sessions=1` → 2 本目 POST /api/sessions → `429`（Agent /sessions カウント）。
4. **state 同期**: Start→DB running / Stop→stopped、CountRunningWorkspaces が反映。teardown で掃除。

## 16.7 成果物

`migrations/0004_user_limit.sql` / `store.go`・`store_sqlite.go`（5 メソッド + limits パース）/ `manager.go`（resolved 拡張・Start 強制・state 同期）/ `runtime.go`（Start/Stop の state 同期）/ session 強制フック / 最小 admin limits API。
