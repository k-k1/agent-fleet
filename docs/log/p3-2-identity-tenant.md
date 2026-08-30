# 14. P3-2 実装プラン — アイデンティティ & テナント解決（多対多）

> 🗄 **歴史的記録（完了）** — 現状は [HANDOFF §6.9](../HANDOFF.md)、設計は [ロードマップ §12.1](../roadmap.md#121-アイデンティティ階層パッケージセルフホスト版)。以下は当時の実装プラン。

[12 Phase 3](../roadmap.md) の P3-2。P3-1（[13](../log/p3-1-metadatastore.md)）で SQLite MetadataStore を入れ、
`app_user(tenant_id)`（1 ユーザー=1 テナント）まで来た。P3-2 で **identity↔tenant を多対多**にし、
**email で人を特定 → 作業対象テナントは明示選択**という解決に進化させる（2026-06-27 決定: 同一人物が複数テナントに所属、
テナントごとに別 role・**別 Workspace（完全分離）**）。

## 14.1 ゴールと不変条件

- **多対多**: `identity`（人, email 一意）×`tenant` を `membership` で結ぶ。Workspace は **membership 単位**（= identity×tenant）。
- **テナント識別 = 明示選択**: gateway email → identity、作業対象テナントは `X-AF-Tenant`（Console ピッカー）を membership で検証。
  ネットワーク信号からは推定しない。
- **単一テナント運用は摩擦ゼロ**: 所属 1 件なら `X-AF-Tenant` 不要で自動。**現ライブ（運用者 1 名）は無改修で動き続ける**。
- **既存ライブ無傷**: `container_name`/`data_dir` は DB 保存済み → 既定テナントの membership は旧名 `af-ws-k1-kami-gmail-com` を維持。
- **後方互換**: `AUTH=dev/proxy` 据置。Agent 契約・proxy 系・封筒鍵（HMAC 据置, P3-3 で昇格）は不変。

### スコープ外（P3-2 では「やらない」）

| 項目 | フェーズ |
|------|----------|
| 管理コンソール UI（テナント/メンバー CRUD）| P3-5（P3-2 は最小 super_admin API + seed のみ）|
| クォータ強制 | P3-4 |
| 封筒鍵 custodian（HMAC 据置）| P3-3 |
| RBAC の全面強制（role 保存 + 主要ガードのみ）| P3-5 |

## 14.2 スキーマ（migration `0002`）

`app_user` を `identity` + `membership` に分割、`workspace.user_id` → `membership_id`。

```sql
CREATE TABLE identity (
  id TEXT PRIMARY KEY, email TEXT NOT NULL DEFAULT '', user_key TEXT UNIQUE NOT NULL,
  role TEXT NOT NULL DEFAULT 'user',         -- user | super_admin（デプロイ横断）
  status TEXT NOT NULL DEFAULT 'active', last_login_at TEXT);
CREATE UNIQUE INDEX idx_identity_email ON identity(email) WHERE email <> '';

CREATE TABLE membership (
  id TEXT PRIMARY KEY,
  identity_id TEXT NOT NULL REFERENCES identity(id),
  tenant_id   TEXT NOT NULL REFERENCES tenant(id),
  role TEXT NOT NULL DEFAULT 'member',        -- member | tenant_admin（テナント内）
  status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL,
  last_tenant INTEGER NOT NULL DEFAULT 0,     -- 任意: 既定選択の last-used 印
  UNIQUE(identity_id, tenant_id));

-- workspace は membership_id へ。SQLite ゆえテーブル再構築（ライブは1行で軽い）。
-- 新 workspace: { id, tenant_id, membership_id UNIQUE, container_name, network, data_dir,
--                 agent_port, agent_token, state, created_at, last_active_at }
```

**データ移行は Go（boot 時・冪等）**: ID 生成と既存 home/コンテナ採用が要るため SQL のみでは行わない。
`app_user` 各行 → `identity`（user_key で upsert）+ `membership`（identity×tenant, role 継承）、
旧 `workspace.user_id` → 対応 `membership_id`。`SUPER_ADMIN_EMAILS` 一致は `identity.role=super_admin`。

## 14.3 Store 港の変更（`store.go` / `store_sqlite.go`）

```go
UpsertIdentity(ctx, email, key string) (Identity, error)         // role は別途 SetRole/bootstrap
ListMemberships(ctx, identityID string) ([]Membership, error)    // /api/tenants と既定選択に
EnsureMembership(ctx, identityID, tenantID, role string) (Membership, error) // auto-provision/招待
GetMembership(ctx, identityID, tenantID string) (Membership, bool, error)    // X-AF-Tenant 検証
GetWorkspaceByMembership(ctx, membershipID string) (Workspace, bool, error)
CreateWorkspace(ctx, ws Workspace) error                          // membership_id 付き
CreateTenant(ctx, slug, name string) (Tenant, error)             // super_admin/seed 用
```
（`UpsertUser`/`GetWorkspaceByUser` を上記へ置換。`MaxAgentPort`/`ListWorkspaces` は維持。）

## 14.4 manager / handler の変更

- **`forUser` → `resolve(ctx, key, email, tenantSel)`**:
  1. `identity = UpsertIdentity(email, key)`（+ `SUPER_ADMIN_EMAILS` で role）。
  2. `ms = ListMemberships(identity)`。空なら provisioning ポリシー（auto→`EnsureMembership(default, member)` / invite→403）。
  3. **テナント選択**: `tenantSel`(=`X-AF-Tenant`) があれば `GetMembership` で検証（無ければ 403）。
     無指定なら: `len(ms)==1`→それ / `last_tenant`→それ / それ以外→`409 tenant_selection_required`。
  4. `ws = GetOrCreateWorkspace(membership)`。新規命名 = `af-ws-<tenant_slug>-<user_key>`（既定テナントは旧 `af-ws-<key>` 維持）、
     `data_dir` = `<dataRoot>/<tenant_slug>/<key>`（既定は旧 `<dataRoot>/<key>`）。port/token は P3-1 同様（inspect 採用 or DB 採番 / mint）。
  5. `runtimeFor(ws, user_key)` を返す（secretKey は HMAC 据置）。
- **`rtFor`（runtime.go）**: `X-AF-Tenant` を読み `resolve` に渡す。403/409 を JSON で返す。
- **新 `GET /api/tenants`**: 呼び出し元 identity の membership 一覧（slug/name/role）。Console ピッカー用。
- **Bitbucket callback**: state に user だけでなく **tenant も束ねる**（`bbState{identity_key, tenant_id}`）→ callback で同じ membership 解決。
- **最小 super_admin API（テスト/seed 用, P3-5 で UI 化）**: `POST /api/admin/tenants`（create）・`POST /api/admin/memberships`（identity×tenant 追加）。RBAC: `identity.role==super_admin` のみ。
- **env**: `SUPER_ADMIN_EMAILS`（CSV）/ `AF_PROVISION=auto|invite`（既定 auto）。

## 14.5 Console（最小）

- ヘッダにテナント・ピッカー（`GET /api/tenants` → select）。選択を `X-AF-Tenant` で全 API に付与（fetch ラッパ）。
- 所属 1 件なら自動選択・ピッカー非表示（単一テナントは見た目も従来通り）。
- 409 受信時はピッカーを促す。フル管理 UI は P3-5。

## 14.6 検証（OOM 注意 — ホストのメモリ枯渇は稼働中フリート全体を巻き込む）

1. **単一テナント回帰**: 運用者（所属1）は `X-AF-Tenant` 無しで従来通り（旧コンテナ・port 維持、sessions/connections OK）。
2. **多対多 E2E**: super_admin API で 2nd テナント作成 + 運用者を member 追加 → `GET /api/tenants` が 2 件 →
   `X-AF-Tenant: <t2>` で操作 → **別コンテナ `af-ws-<t2>-<key>`・別 home** 生成・1st テナントと完全分離 → teardown。
3. **選択既定**: 無指定で 1st（last-used）or 409。不正テナント指定は 403。
4. **CP 再起動**: membership/workspace が DB から安定解決、port 不変。
5. **データ移行**: 0002 後に identity=1 / membership=1 / workspace が旧名のまま、を確認。

## 14.7 成果物

`migrations/0002_*.sql` + Go データ移行 / `store.go`・`store_sqlite.go` 改修 / `manager.go`・`runtime.go`・`oauth_bitbucket.go` /
`GET /api/tenants` + 最小 admin API / Console ピッカー。
