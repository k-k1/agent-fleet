# 19b. P3-9 実装記録 — showback（社内使用量の可視化）

> 🗄 **実装記録** — 現状は [HANDOFF](../HANDOFF.md)、設計は [ロードマップ P3-9](../roadmap.md#p3-9-運用の成熟社内旧-phase-4-を吸収)。
> idle-stop は [p3-9-idle-stop](p3-9-idle-stop.md)。

[12 Phase 3](../roadmap.md) の P3-9「運用の成熟」のうち **社内 showback**（部署別の使用量可視化・任意の chargeback、
外部課金なし）を実装。**段1＝バックエンド（累積 + API + CSV）**。Console ダッシュボードは段2（目視前提）。

## 19b.1 何を showback するか — workspace 占有（稼働時間）

**BYO ゆえ Claude 利用料は各ユーザーの個人サブスクで本製品は勘定しない**。操作者にインフラ費（RAM/CPU、AWS なら
Fargate 時間）を実際に発生させるのは **workspace がどれだけ動いていたか**＝占有時間。これは idle-stop の動機と同じ軸で、
showback = **workspace 稼働秒の per-(membership, day) 累積**とする。ディスク（永続 home GB）は後続（`user_limit.disk_gb`
が既にある）。セッション数は瞬間値ゆえ占有指標には非採用。

## 19b.2 計測方法 — サンプリング（イベントでなく）

**背景サンプラー**（`usage.go` `usageSampler`）が一定間隔で全テナントの workspace を走査し、その瞬間 `running` の
ものに**1 間隔ぶんの秒数**を加算する。

- **なぜサンプリング**: Start/Stop イベント差分方式は CP クラッシュ/再起動で in-flight 区間を取りこぼす。サンプリングは
  堅牢で、誤差は**最大 1 間隔**（間隔途中で開始/停止した workspace の過少/過大計上）。**内部 showback で近似は許容**
  （外部課金なし）ゆえこの単純さを採る。既定間隔 `AF_USAGE_SAMPLE_INTERVAL=5m`、`0` で無効。
- **reaper と分離**: reaper は idle-stop 有効テナントしか巡回しない。showback は idle 設定に関係なく**全テナント**を
  勘定する必要があるので独立 goroutine。権威は docker `State`（`runtimeFor(ws,"").State`、既存 `countRunningInTenant` と同じ）。
- **非破壊ゆえ既定 ON**（P3-4/P3-9 idle-stop の「既定は現挙動不変」とは別扱い＝DB 書き込みのみで挙動を変えない）。

## 19b.3 スキーマ（migration 0008）

```sql
CREATE TABLE usage_daily (
  membership_id TEXT NOT NULL,
  tenant_id     TEXT NOT NULL,
  day           TEXT NOT NULL,               -- YYYY-MM-DD (UTC)
  running_secs  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (membership_id, day)
);
CREATE INDEX idx_usage_tenant_day ON usage_daily(tenant_id, day);
```

`AddUsage` は `ON CONFLICT(membership_id, day) DO UPDATE SET running_secs = running_secs + excluded.running_secs` で累積。
`ListUsage(tenantID, from, to)` は `[from,to]` 内の日次行を返し、tenant slug / member key・email を **LEFT JOIN** で付与
（membership/identity が後に消えても行は残る）。`tenantID==""` で全テナント（super_admin）。

## 19b.4 admin API

`GET /api/admin/usage?from=&to=&tenant=&format=json|csv`

- **ゲート**（`usageScope`）: `tenant=<slug>` あり → super_admin **または** その tenant の tenant_admin（当該テナントに scope）。
  なし → super_admin のみ（デプロイ全体、`tenantID=""`）。既存 `requireTenantAdmin`/`requireSuperAdmin` を再利用。
- **範囲**（`usageRange`）: 既定は直近 30 日（UTC）。`from`/`to` は inclusive `YYYY-MM-DD`、不正値は**サイレント既定でなく 400**。
- **JSON**: `days`（日次行＝ダッシュボードのチャート用）+ `totals`（member 別集計＝表用、`running_hours` 併記）。
- **CSV**（`format=csv`）: `tenant,user_key,email,day,running_secs,running_hours` の日次行を `Content-Disposition: attachment` で。

## 19b.5 段割り

- ✅ **段1 — バックエンド（このホストで検証完結）**: migration 0008 / `Store.AddUsage`+`ListUsage` / `usageSampler` /
  admin API（JSON+CSV, gate）。検証: `go test`（store 往復・集計・窓・テナントフィルタ）＋ CP 実起動 smoke（migration 適用・
  sampler 起動・route 登録・super_admin 200 / 非 super 403 / 不正 range 400 / CSV ヘッダ）。
- ✅ **段2 — Console 管理ダッシュボード**（実装済・要目視確認）: admin オーバーレイ最上部に mode トグル（テナント管理 / 使用量）を追加、
  「使用量」= `UsageView`（範囲ピッカー from/to・super_admin はテナント絞り込み select・member 別 running_hours の横棒 + 合計/人数サマリ・
  CSV ダウンロードリンク）。CSV は cookie 認証の素の `<a download>`（endpoint は `?tenant=` で scope ゆえ X-AF-Tenant 不要）。
  `console/src/settings/AdminTab.jsx` + `styles.css`。**data 契約は段1 の JSON keys(tenant/user_key/email/running_secs/running_hours)と一致**
  （`TestSQLiteUsage` が JOIN ラベルを、API smoke が JSON 形を検証済）。React ゆえ最終レイアウトは実ブラウザで目視。
- ▶ **後続**: ディスク占有（home GB）の showback / per-tenant 合計・グラフ / 保持期間（古い `usage_daily` の間引き）。

## 19b.6 触ったファイル（段1）

- `control-plane/migrations/0008_usage.sql` — 新規（`usage_daily`）。
- `control-plane/store.go` — `UsageRow` 型 + `AddUsage`/`ListUsage` を Store IF に追加。
- `control-plane/store_sqlite.go` — `AddUsage`（upsert 累積）/`ListUsage`（窓 + LEFT JOIN 付与）実装。
- `control-plane/usage.go` — 新規（`usageSampler` + `handleAdminUsage` + `usageScope`/`usageRange`/`aggregateUsage`/CSV）。
- `control-plane/main.go` — sampler 起動（`AF_USAGE_SAMPLE_INTERVAL`）+ `GET /api/admin/usage` route。
- `control-plane/store_sqlite_test.go` — usage 往復 + 集計テスト。
- `deploy/compose/.env.example` — `AF_USAGE_SAMPLE_INTERVAL` 記載。

注意: [host-oom-fleet-risk] ゆえ実フリートは起こさず、sampler が実 workspace を勘定する full E2E は段2/実運用で確認。
