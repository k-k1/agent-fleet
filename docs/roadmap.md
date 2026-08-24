# ロードマップ

既存資産（`oauth2-proxy` / `tmux-claude.sh` / `CLAUDE_CONFIG_DIR`）を踏み台に **local-first** で進め、同一コアに
AWS アダプタを後付けする（[ポータビリティ](reference/portability.md)）。各フェーズで「実機検証 → 設計確定」を回す。
**現状の運用詳細は [HANDOFF](HANDOFF.md)、意思決定の経緯は [decisions/](decisions/)。**

## フェーズ一覧

### Phase 0 — PoC（ローカル dev, 既存資産の延長）　✅ 完了
`/login` の対話フローを最小コストで検証。ヘッドレスで **localhost コールバック非依存**と判明し最大リスクが消えた
（[decisions/0002](decisions/0002-claude-auth-onboarding.md)）。記録は [history/phase0-poc](history/phase0-poc.md)。

### Phase 1 — Workspace イメージ + Console MVP（ローカル dev）　✅ 完了
1 ユーザー分のコンテナ化 + 最小 Console を local Docker で完成。Runtime/Volume ポートを実装。
実装結果と実運用の知見は [history/phase1-plan §11.10](history/phase1-plan.md#1110-実装結果と実運用の知見phase-1-完了)。

### Phase 2 — マルチユーザー（ローカル shared）+ ポート確立　✅ 完了
オンプレ 1 台で複数ユーザーが相互不可視に並行利用 + 全ポート抽象化。per-user Workspace / AuthGateway
（`AUTH=proxy`）/ ネットワーク分離（`af-net-<user>`）/ at-rest 暗号化（[dev/07 セキュリティ](dev/07-security.md)）。

### Phase 3 — プロダクト化（パッケージ配布・グループ各社セルフホスト）　▶ 進行中
「AWS 移植」から**プロダクトのパッケージ化**へ再定義。提供モデルの意思決定（SaaS 断念の経緯・ToS 根拠）は
[decisions/0001](decisions/0001-self-host-vs-saas.md)。**P3-1〜P3-7 + Console 刷新は実装済み**（P3-7 残 = KMS custodian・実 AWS 再検証）、
**P3-10（パッケージング）は dist 配布の publish 運用中**（[docs/35](35-packaging.md)）。残 = P3-8・P3-9 の成熟項目・
P3-10 の完了ゲート（第 2 デプロイ E2E）。詳細は本書「Phase 3 詳細設計」章（↓）。

### Phase 4 — 運用の成熟・グループ横展開　— 未着手
社内 showback / ライフサイクル / idle-stop / バックアップ / 観測。Phase 3 の **P3-9** に吸収して進める。

## マイルストーン判定

| Phase | 完了条件 | 状態 |
|-------|----------|------|
| 0 | `/login` がコンソール経由で完了でき、手順が文書化される | ✅ 完了 |
| 1 | 1 ユーザーがローカル Docker で Claude セッション起動 + ターミナル操作（ホーム永続）| ✅ 完了 |
| 2 | オンプレ 1 台で複数ユーザーが相互不可視に並行利用でき、全ポートが抽象化される | ✅ 完了 |
| 3 | **パッケージとして配布でき**、別のグループ会社が**自社で**（オンプレ既定 / 自社 AWS 任意）セルフホストし、自社ユーザーを管理・バジェット強制・per-deployment 鍵で at-rest 暗号化して運用できる | ▶ 進行中（P3-1〜P3-7 実装済・P3-10 は dist publish 運用中、完了ゲート=第 2 デプロイ E2E が残）|
| 4 | 社内 showback・ライフサイクル・idle-stop・バックアップ・観測・（任意）デプロイ内専用分離・MCP 運用が揃い、グループ各社へ無理なく横展開できる | — |

---

# Phase 3 詳細設計（パッケージ化・各社セルフホスト）

Phase 3 を **「AWS 移植」から「プロダクトのパッケージ化（グループ各社が自社でセルフホスト）」へ再定義**した設計。
**提供モデルの意思決定**（商用 SaaS / 中央運用マルチテナント SaaS の断念、各社セルフホスト採用、確定前提一覧、
ToS 根拠・残存リスク）は [decisions/0001](decisions/0001-self-host-vs-saas.md) に集約。本章はその設計と
ワークストリームを扱う。旧 Phase 3（AWS アダプタ）/ Phase 4（堅牢化）は本章の substrate として吸収する。

> 要点: 「我々が運用する基盤」は無い。各デプロイは**その社のもの**——データ・鍵・OAuth 設定・ユーザー管理は
> すべてその社が握る（phone-home なし）。多くの社は**単一テナント**で運用し、マルチテナントは大企業向けの任意拡張。
> DB は **SQLite 既定**（Postgres は AWS/HA 時のみ、`MetadataStore` 港の裏）。

## 12.1 アイデンティティ階層（パッケージ・セルフホスト版）

各デプロイ（= 1 社の自社ホストインスタンス）の中の階層。我々（vendor）は**実行時の階層に登場しない**。
**identity（人）と tenant は多対多**——同一人物が複数テナント（部署）に所属できる（2026-06-27 決定）。

```
Deployment（1 社が自社ホスト。データ・鍵・設定をその社が保有）
  ├── super_admin（その社の情シス。identity.role）            ← デプロイ全体を統治・全テナント横断
  ├── Identity（人。email で一意・デプロイ内グローバル）
  │       └──< Membership >── Tenant                          ← 多対多。role はテナントごと
  └── Tenant（部署/プロジェクト, 既定 1 = 全社）
        ├── tenant_admin / member（membership.role）           ← テナント内ロール
        └── Workspace（コンテナ）＝ Membership ごと（= identity×tenant）  ← テナント完全分離
              ├── Repository
              └── Session
```

- **多対多**: `Identity`（人）×`Tenant` を `Membership` で結ぶ。1 人が Platform と Security の両部署に属し、
  **テナントごとに別 role・別 Workspace（別コンテナ/home/資格情報）= 完全分離**（per-tenant 鍵と整合）。
- **Workspace は Membership 単位**（= identity×tenant）。1 人が N テナントに居れば最大 N コンテナ（RAM。idle-stop が効く・バジェットはテナント別）。
- **作業対象テナントの識別 = 明示選択**: gateway の email で identity を特定 → 作業対象テナントは**リクエストの明示指定**
  （Console のピッカー → `X-AF-Tenant`）を membership で検証。**ネットワーク信号からは推定しない**（[P3-2](#p3-2-アイデンティティ--テナント解決authgateway-拡張)）。
  - 未指定の既定: 所属が 1 件なら自動（**単一テナント運用は摩擦ゼロ**）/ 複数なら last-used or 選択要求。
- **命名**: コンテナ/ボリュームは `workspace.id`（不透明）/ 既定スキーム `af-ws-<tenant>-<user_key>` で命名。
  既存ライブは `container_name`/`data_dir` を DB 保存済みのため**既定テナントの membership は旧名 `af-ws-<key>` を維持**（無改修移行）。
- **ロール（2 スコープ）**: `identity.role`=`super_admin`（情シス・全テナント横断）/ `membership.role`=`tenant_admin|member`（テナント内）。
  RBAC を CP ハンドラで強制。※ 旧 `platform_admin`（=我々）は**廃止**（我々は運用しない）。

## 12.2 ワークストリーム一覧（依存順）

| # | ワークストリーム | 依存 | 旧ロードマップ対応 | 規模配慮（小） |
|---|------------------|------|--------------------|----------------|
| P3-1 | **MetadataStore（SQLite 既定）+ 階層モデル + RBAC** | — | （新規・全ての土台）| SQLite アダプタのみ実装。Postgres は港の裏で AWS/HA 時。Plan 抽象なし Tenant 直付け。 |
| P3-2 | **アイデンティティ & テナント解決**（AuthGateway 拡張）| P3-1 | Phase 2 §6.7 の延長 | 各社が自社 OAuth を設定。emails.txt→DB。 |
| P3-3 | **per-deployment/tenant 封筒暗号鍵**（custodian 抽象）| P3-1 | 旧 §6.8 A3 の昇格 | **オンプレ優先**＝Vault/ファイル KEK。KMS は AWS アダプタ。 |
| P3-4 | **リソースバジェット/クォータ**（テナント+ユーザー）| P3-1 | （新規）| ハードクォータ（block）のみ。 |
| P3-5 | **管理コンソール + 管理 API**（super_admin 主・部署 admin 任意）| P3-1, P3-2 | 旧 Phase 4「管理者画面」| サービス層を 1 本にして API と MCP で共有。単一テナント運用を先に。 |
| P3-6 | **MCP による Fleet 制御** | P3-5 | （新規）| 管理サービス層の薄いラッパ。その社の運用チーム向け。 |
| P3-7 | **デプロイ先アダプタ**（オンプレ Docker 既定 / 自社 AWS 任意）| P3-1 | **旧 Phase 3 本体** | compose 同梱。AWS は ECS/EFS/RDS/ALB を任意で。 |
| P3-8 | **デプロイ内 専用分離**（機微部署・任意）| P3-7 | （新規）| 既定=論理を先に。大企業の内部要件向け。 |
| P3-9 | **運用の成熟**（社内 showback・ライフサイクル・idle-stop・監査・観測・バックアップ）| 全部 | 旧 Phase 4 | 最小から。 |
| **P3-10** | **パッケージング & 配布 & アップグレード** | P3-1〜P3-7 | （新規・提供モデルの核）| compose/ECS+CFN/native + 設定 + マイグレーション + 設置/更新 runbook（Helm は需要が出るまで棚上げ — docs/35）。 |

> **P3-10 が新提供モデルの核**。機能（P3-1〜P3-9）は「中身」、P3-10 は「**他社へ渡せる形にする**」工程。中身が無ければ包めないので依存は後ろだが、優先度は同格。

---

## P3-1. MetadataStore（SQLite 既定）— 全ての土台
> ✅ **完了**。実装プランは [history/p3-1-metadatastore](history/p3-1-metadatastore.md)。データモデルの現在形は [dev/06 データモデル](dev/06-data-model.md)。

**着手時の欠落（当時の記録）**: DB が**一切無い**。フォルダ名=ID、ポートは in-memory map、CP 再起動で再採番されうる。
テナント・バジェット・管理者・クォータ・監査は**すべて永続レコードを要する**。ここが全ワークストリームの gating item。

- **DB 選定 = SQLite 既定**: 1 デプロイ = CP 1 プロセス / 1 ホスト（オンプレ compose 既定）に**埋め込み DB がベストフィット**。外部 DB サーバ不要＝自己ホスト製品（P3-10）と相性最良。
  持つのは制御メタデータのみ（重いのは PTY であり DB ではない）で、数十〜百ユーザーは SQLite の余裕圏。
  **今は SQLite アダプタだけ実装**し、**Postgres は `MetadataStore` 港の裏で AWS/HA 時に後追い**（[dev/09 §9.2](dev/09-deploy.md#92-ポートアダプタ--何をどのノブで差し替えるか)）。投機的に Postgres を作らない（リーン）。
- **SQLite 運用規律**（外すと後で痛い）:
  - 接続: `journal_mode=WAL` / `busy_timeout` / `foreign_keys=ON` / `synchronous=NORMAL`、書き込みは単一ライターに。
  - ドライバ: **pure-Go（`modernc.org/sqlite`）** 推奨（cgo 回避＝静的バイナリ運用と整合）。
  - 方言ポータブル SQL: bool/uuid/jsonb を避け TEXT/INTEGER + JSON は TEXT、`ON CONFLICT` upsert、マイグレーションは **goose**（SQLite/Postgres 両対応）→ Postgres 追加時に港がそのまま効く。
  - バックアップ: online backup API / `VACUUM INTO`（書き込み中の naive cp は不可）。P3-9 の home+DB バックアップに乗る。
- **スキーマ（概略）** — B3 反映: `Plan` 抽象を廃し、limits と isolation を **Tenant に直付け**:
  ```
  Tenant      { id, slug, name, status(active|suspended|deleting),
                limits(json: max_workspaces, max_sessions, disk_gb, mem_mb, cpu),
                isolation(shared|dedicated),     -- 既定 shared。dedicated は P3-8
                key_ref,                         -- このテナントの DEK を解く KEK/CMK の参照（P3-3）
                placement_ref,                   -- dedicated 時の cluster/subnet/EFS（任意）
                created_at }
  -- 注: P3-1 は app_user{ id, tenant_id, email, user_key, role, ... } で出荷（1ユーザー=1テナント）。
  -- P3-2 で identity↔tenant 多対多へ進化（§12.1, docs/14, migration 0002）:
  Identity    { id, email(unique), user_key(unique), role(super_admin|user), status, last_login_at }
  Membership  { id, identity_id, tenant_id, role(tenant_admin|member), status, unique(identity_id,tenant_id) }
  Workspace   { id, tenant_id, membership_id(unique), container_name, network, data_dir,
                agent_port, agent_token, state, last_active_at }   -- = identity×tenant ごと
  Repository  { id, workspace_id, name, remote_url, default_branch, last_status }
  Session     { id, workspace_id, repository_id, tmux_name, claude_session_id, state, started_at, last_active_at }
  UserLimit   { membership_id, max_sessions, disk_gb, mem_mb }   -- 管理者がテナント枠内で設定
  UsageCounter{ tenant_id, running_workspaces, running_sessions, used_disk_gb, allocated_mem_mb, sampled_at }
  WrappedDEK  { workspace_id, ciphertext_dek, key_version } -- P3-3
  AuditLog    { id, tenant_id, actor_user_id, actor_kind(user|admin|mcp|system), action, target, detail, at }
  ```
- **manager.go の昇格**: in-memory map を**廃止し DB を source of truth に**。CP 再起動時は Workspace レコードから rehydrate ＋ Runtime（docker inspect / ECS describe）で reconcile。
  → 「停止中コンテナのポート再採番」問題が原理的に消える。
- **現ライブ環境からの移行（B5）**: 今のライブ（運用者の既存 workspace 等）は**我々の社の「第 1 デプロイ＝リファレンス実装」**になる。
  移行 = **既定テナントを 1 つ作成 → 既存ユーザーを所属 → 既存コンテナ/home から DB をバックフィル**（Phase 2 の home/secrets 移行と同型の one-shot）。
- **規模配慮**: マイグレーションは単純な SQL（goose/atlas 等）。ORM は薄く。分散トランザクション不要。

---

## P3-2. アイデンティティ & テナント解決（AuthGateway 拡張）
> ✅ **完了**。実装プランは [history/p3-2-identity-tenant](history/p3-2-identity-tenant.md)、現状は [dev/07 セキュリティ](dev/07-security.md)。

現状の `AuthGateway.Identify` は email→sanitized user を返すだけ。マルチテナントでは、**email は人（identity）を特定し、作業対象 tenant はリクエストの明示選択**で決める（identity↔tenant 多対多, §12.1）。
詳細な実装プランは [14 P3-2 実装プラン](history/p3-2-identity-tenant.md)。

- **テナントはネットワーク信号から推定しない**: 中央 SaaS の subdomain/path ルーティングは採らない（会社の区別はデプロイ自体）。
  デプロイ内の作業対象テナントは**ユーザーが明示選択**（Console ピッカー → `X-AF-Tenant`）し、CP が membership で検証する。
- **解決アルゴリズム**:
  ```
  email = gateway（L1, 不変）
  identity = upsert(email, key)                       // 人。email で一意
  memberships = list(identity)
    len==0 → provisioning ポリシー（auto: 既定テナントへ membership 作成 / invite-only: 403）
  tenant = request X-AF-Tenant（or 既定: 所属1件なら自動 / last-used / 選択要求 409）
    validate membership(identity, tenant) else 403
  role = identity.role==super_admin か membership.role
  workspace = getOrCreate(identity, tenant)            // テナントごとに別コンテナ
  ```
- **L1（認証）**: 既定は **CP ネイティブ Google OAuth（`AUTH=oauth`）**＝外部ゲートウェイ不要で各社が許可ドメイン/メールを設定（[reference/auth.md](reference/auth.md)、2026-06-29 ライブ採用）。大規模/既存資産がある社は自社の ALB OIDC / oauth2-proxy（`AUTH=proxy`）も選べる。我々は設定方法を文書化（P3-10）。
- **L2-authz（認可）を DB に移す**: emails.txt の静的許可を廃し、**CP が email を DB と突合**し identity/membership を判定。未登録 email は provisioning ポリシー依存（既定 auto-provision / 厳格は 403）。
- **新エンドポイント**: `GET /api/tenants` = 呼び出し元の membership 一覧（tenant slug/name/role）→ Console のピッカー。
- **provisioning ポリシー**（env で切替）: 既定 **auto-provision**（ゲートウェイを通れた=その社の正規メンバー → 既定テナントへ自動）/ 厳格運用は **invite-only**（管理者が招待で membership 先行作成、未知は 403）。マルチテナントの部署割当は招待ベース。
- **role ブートストラップ**: env `SUPER_ADMIN_EMAILS` 一致で `identity.role=super_admin`（最初の管理者の鶏卵問題を解消）。
- **ゲート迂回封じは Phase 2 の規律を継承**: proxy モードでヘッダ欠落＝401、CP は `127.0.0.1` 束縛（[dev/07 セキュリティ](dev/07-security.md)）。
  `--email-domain=*` だと L1 は「正当な Google アカウント」までしか絞らないので、**DB メンバーシップ判定（403）+ レート制限**が実質ゲート。自社ドメイン限定なら `hd` を効かせる方が堅い。

---

## P3-3. per-deployment/tenant 封筒暗号鍵（custodian 抽象・オンプレ優先）
> ✅ **完了**。決定と限界は [decisions/0005](decisions/0005-envelope-custodian.md)、実装プランは [history/p3-3-envelope-crypto](history/p3-3-envelope-crypto.md)。

**現状（Phase 2 A3）**: 単一 `AF_MASTER_KEY`(env) → `HMAC(SHA256(master), user)` で per-user サブ鍵を導出し起動時注入。
→ 不十分: master が単一障害点、テナント単位の鍵ローテ/失効ができない、鍵が CP env に常在。

**新設計（封筒暗号・custodian は環境で差し替え）** — B1 反映で **AWS 非依存**（オンプレが最初の本番のため）:

```
Deployment ルート鍵 / Tenant KEK   ← custodian が保護。AWS=KMS CMK、オンプレ=Vault transit / age + ファイル KEK
   └─ wrap ─▶ per-workspace DEK（256-bit, B6）  ← WrappedDEK に ciphertext で保存
                 └─ AES-256-GCM ─▶ secrets.enc   ← Agent 側は Phase 2 と同一形式（不変）
```

- **custodian 抽象**（`SecretStore`/`KeyCustodian` 港）: オンプレ = **Vault transit engine**（推奨）または **age + ファイル KEK**、AWS = **KMS**。
  封筒構造・crypto-shred・per-tenant 失効は custodian によらず同型。**KMS は「AWS アダプタの一実装」に格下げ**（依存は P3-1 のみ、P3-7 不要）。
- Workspace 起動時: CP が custodian で `Decrypt(wrapped_dek)` → 平文 DEK を**Phase 2 と同じ注入経路**（`AF_SECRET_KEY` 相当）でコンテナへ。
- **Agent 側 `secrets.go` / `workspace-agent cred` は無改修**: AES-GCM で都度復号する仕様は不変。**変わるのは鍵の provisioning だけ**（HMAC 導出 → 封筒復号）。
- **DEK 粒度（B6）**: **per-workspace DEK** を採用（Phase 2 の per-user 分離特性を保つ。1 ユーザーの鍵漏洩が他に波及しない）。per-tenant 1 鍵にしない。
- **運用上の利点**:
  - **鍵ローテ/失効**: テナント KEK を disable → そのデータが暗号的に到達不能＝**クリーンなオフボーディング（crypto-shred）**。鍵はその社が握る。
  - master の単一障害点解消・鍵を CP に常在させない・部署単位の暗号分離（マルチテナント時）。
- **移行**: Phase 2 の HMAC 派生 → 封筒方式へ（旧鍵で復号 → 新 DEK で再暗号、one-shot）。

---

## P3-4. リソースバジェット / クォータ（テナント + ユーザー）
> ✅ **完了**（ハードクォータ・既定無制限）。実装プランは [history/p3-4-quota](history/p3-4-quota.md)、現状は [dev/03 Control Plane](dev/03-control-plane.md)。残: ディスク強制 / showback（P3-9）。

**BYO のため対象はインフラ資源のみ**（Claude 利用量ではない）。その社の自社ホスト資源を守るためのもの。

| 次元 | テナント枠（管理者が設定）| ユーザー枠（テナント枠内）| 強制ポイント |
|------|--------------------------|----------------------------|--------------|
| Workspace 数 | max_workspaces（≒同時稼働ユーザー）| 1 user = 1 workspace（既定）| Workspace.Start |
| セッション数 | max_sessions（テナント合計）| max_sessions/user | Session.Create |
| メモリ/CPU | allocated_mem ≤ tenant cap ✅（メモリ）| workspace の mem サイズ ✅ / cpu は後続 | Start（`--memory`/ECS task size）|
| ディスク | total_disk_gb | disk_gb/user | clone / 定期計測 |

- **強制方式（小規模）**: **ハードクォータ**。超過は `429` + 明示メッセージ。外部課金が無いので overage は社内配分の調整事項。
- **メモリ/CPU**: ✅ メモリは per-workspace で実装済み。2 段クォータ（super_admin が `tenant.limits.max_workspace_mem` cap、tenant_admin が `user_limit.mem_limit`）を Start 時に `[床/テナントcap/ホスト天井 AF_MAX_WORKSPACE_MEM]` へクランプし、docker `--memory` / ECS task size（`fargateSize` で有効な Fargate 組へスナップ）に反映。未設定は既定 `WS_MEMORY`。CPU の per-workspace 化とテナント合計 ≤ cap の 429 は後続。
- **ディスククォータは正直に難しい**（明示）:
  - local bind mount: XFS project quota / `--storage-opt size=`（overlay2+xfs）が要る。MVP は **計測（du）して clone 時/定期に soft-block**。
  - EFS: per-AP ハードクォータが無い。同様に **meter-and-block**。ハード FS クォータは後付け。
- **メータリング**: `UsageCounter` を定期サンプリング → 管理コンソールに可視化、社内 showback（P3-9）の原資料。

---

## P3-5. 管理コンソール + 管理 API
> ✅ **完了**。管理 UI（super_admin の `AdminDialog`）+ メンバー Console（git/ファイル可視化・shell）を実装。
> メンバー Console プランは [history/p3-5-member-console](history/p3-5-member-console.md)、現状は [dev/02 Console](dev/02-console.md)。

その社の中の管理。**単一テナント運用（super_admin が全社を見る）を先に**完成させ、部署 admin は任意拡張。

| ロール | 主体 | できること |
|--------|------|------------|
| **super_admin** | その社の情シス | ユーザー招待/無効化、バジェット設定、全 Workspace/Session 一覧・停止、鍵ローテ、監査閲覧、（任意）テナント作成 |
| **tenant_admin**（任意）| 部署管理者 | 自テナントのユーザー・枠・Workspace/Session・監査（マルチテナント有効時のみ）|

- **API 面**: `/api/admin/tenants*`・`/users*`・`/workspaces*`・`/sessions*`・`/quotas*`・`/audit*`。RBAC で gate。
- **サービス層を 1 本に**: API ハンドラと **MCP（P3-6）が同一の管理サービスパッケージ**を呼ぶ（ロジック重複を作らない）。
- **Console**: メンバー向け Workspace ビューとは別に admin ビュー。単一テナントなら super_admin 用 1 画面で足りる。
- **監査**: すべての管理操作を `AuditLog` に actor_kind 付きで記録（user/admin/mcp/system を区別）。

---

## P3-6. MCP による Agent Fleet 制御（管理面 + 作業面を一体で）
> ◐ **段1（member/drive）ライブ稼働 + admin read/write 実装済（未ライブ検証）/ dangerous 段は残**。
> - **段1 = member 4 ツール**（`list_my_sessions`/`get_session_status`/`get_session_output`/`send_to_session`）+ PAT 発行/失効（Console）+ `/mcp`（Streamable HTTP）を実装・**E2E green でライブ稼働**（現状は [dev/03 §3.5 MCP サーバ](dev/03-control-plane.md#35-mcp-サーバ)）。
> - **admin read/write 実装・ライブ E2E green**（2026-07-01）: read=`list_workspaces`/`get_usage`/`list_sessions`、write=`stop_workspace`/`stop_session`/`set_user_quota`。PAT の tenant に固定し、live role（super_admin / その tenant の tenant_admin）で gate、write は `AuditLog`（`actor_kind=mcp`）へ記録。監査ログ書き込み（migration 0007 `audit_log` + `InsertAudit`/`ListAuditByTenant`）をここで導入。ライブ検証（運用者デプロイ）= super_admin PAT で全10ツール可視・`get_usage` に host stats／tenant_admin は admin ツール可視だが host stats 無し／plain member は member 4ツールのみ・admin ツールは 401／`set_user_quota` の write が `audit_log` へ `actor_kind=mcp` 記録、を確認。
> - **残 = dangerous 段**（`rotate_key`/`recreate_workspace`/`stop_all_idle`、confirm+dry-run）。土台（鍵ローテ実装・idle 検出 P3-9・`tail_audit`）が未整備ゆえ後続。
> - 設計確定は [decisions/0006](decisions/0006-mcp-unified.md)、実装プランは [history/p3-6-mcp](history/p3-6-mcp.md)。

CP に `/mcp` を 1 本生やし、**管理面（運用チーム）と作業面（メンバー自身の遠隔セッション駆動）を同一サーバで** role 出し分けする。
**そもそもの目的 = E**: 1 つの手元 Claude が、自分の Workspace 内の claude/opencode/codex セッション群を束ねて駆動する（フリート運用の MCP 化）。

- **一体化する層は入口だけ**（transport / 認証・RBAC / 監査）。裏は admin=CP 管理サービス層 / member=Agent proxy の 2 本のまま。**新ロジックを足さない薄いラッパ**。
- **transport**: **Streamable HTTP**（旧 HTTP+SSE ではない）。公式 Go SDK・バージョン pin。新プロセス不要。
- **認証 = PAT（各ユーザーが Console で発行・発行者の role を継承）**:
  - トークンは identity+membership 参照、**role は呼び出し毎に live 解決**（降格で即失効）。role が能力の天井。
  - **scope は発行時に選択（≤role）**: `read`（既定）/ `write` / `admin:dangerous`。「読む Claude は read トークン・壊す操作は別トークン」で injection 分離。
  - テナントはトークンに固定（クライアント供給 `X-AF-Tenant` は受けない）。oauth2-proxy（Google forward-auth）と MCP の OAuth2.1/DCR は噛み合わないので PAT 主・OAuth は後（AWS 以降）。
- **ツール（単一レジストリ・principal で capability フィルタ）**:
  - **member/drive（E・主目的）**: `list_my_sessions` / `send_to_session` / `get_session_status` / `get_session_output`。自分の BYO claude が自分の Workspace を駆動＝自己完結ゆえ read/write 厳格分離の対象外。
  - 読み取り（admin）: `get_usage` / `list_workspaces` / `list_sessions` / `tail_audit`
  - 変更（admin, `write`）: `start_workspace` / `stop_workspace` / `stop_session` / `set_user_quota`
  - 強権（admin, `admin:dangerous` ＋ confirm ＋ dry-run）: `rotate_key` / `recreate_workspace` / `stop_all_idle`
- **authz**: RBAC は**必ずサービス層で再検証**（MCP の capability フィルタは UX、権威にしない）。
- **監査**: 全操作を `actor_kind=mcp`（principal・token_id 付き）で記録。
- **固有リスク**: prompt-injection × 変更系の confused-deputy（admin 側）→ read/write 別トークン分離 + dangerous の人手 confirm/dry-run で殺す。E は自己完結ゆえ対象外。
- **配布**: 既定 OFF（`AF_MCP_ENABLED`）で同梱、ingress は `/mcp` を Bearer 通し（oauth2-proxy パス除外 1 点）。phone-home なし（P3-10）。

---

## P3-7. デプロイ先アダプタ（オンプレ Docker 既定 / 自社 AWS 任意）
> ◐ **段1（シーム固め）完了**（実装記録 [p3-7-aws-adapter](history/p3-7-aws-adapter.md)）。`RuntimeFactory` 港を
> 唯一の生成口にし、`&dockerRuntime{}` 直生成を factory 経由へ統一。`AF_RUNTIME=local|ecs` 分岐（unknown=起動時 fail-fast）。
> `ecsRuntime` スケルトン（港は満たすが lifecycle は未実装で fail-loud）。`go build/vet/test` で検証済（`runtime_test.go`）。
> **段2（ecsRuntime 本実装）＝完了**（コードは AWS 非依存で完結、`runtime_ecs.go`＋fake-client `runtime_ecs_test.go`、
> `go build/vet/test` 34 通過）: Service desired 0/1・Service Connect 到達・EFS AP 2 本(CP 動的払出=tag 引き create-or-get)・
> token/DEK は SSM SecureString `valueFrom`(plaintext env 不使用)・deterministic naming ゆえ**スキーマ/Agent/Console/CP コア変更ゼロ**。
> **段4（IaC substrate）＝実証済**: `deploy/aws/ecs/cfn/` 00-network/10-data/20-platform/30-ingress を sandbox で
> deploy→検証→teardown、30 は検証用ドメインで実 Google ログイン到達（**CloudFormation**、ec2-single と一貫・static のみ）。
> **段5（実 AWS E2E）＝到達確認済**: sandbox で 00-30 substrate＋段2 配線 CP を立て、実ブラウザで login→workspace
> Start→shell まで到達。CP が ws ECS サービス＋EFS AP2本(transit 暗号)＋SSM SecureString を動的払出し、CP→Service
> Connect→Agent 到達（`POST /sessions` 受理）、DEK/token は平文 env になし。findings=大容量イメージ cold pull が Start の
> healthz 待ち超過(→(A)対応済=非致命化)/CP SQLite ephemeral ゆえ再デプロイで状態消失(→(B)対応済=**段3a RDS Postgres Store**、
> 共有 sqlStore＋?→$n rebind、Docker Postgres で conformance green、CP→RDS を CFN 配線)。残＝段3b(KMS custodian)・実 AWS 再検証。AWS 構成は [reference/aws](reference/aws.md)。

各社が**自社のデプロイ先を選ぶ**。コアは無改修、周縁アダプタのみ（[09](reference/portability.md)）。我々は両方を同梱（P3-10）。

| 港 | オンプレ（既定）| 自社 AWS（任意）|
|----|-----------------|-----------------|
| Runtime | Docker Engine（compose）| ECS（Fargate）|
| Volume | bind mount | EFS アクセスポイント |
| AuthGateway | **CP ネイティブ OAuth（`AUTH=oauth`）** 既定 / 外部 oauth2-proxy 任意 | ALB OIDC または oauth2-proxy |
| MetadataStore | Postgres/SQLite | RDS(Postgres) 単一 |
| KeyCustodian（P3-3）| Vault transit / ファイル KEK | KMS |
| Ingress/TLS | Caddy（自己署名/社内 CA）| ALB + ACM |
| Agent 認証 | 同一ホスト + Bearer（Phase2 A2）| SG 制限 + Bearer → 将来 mTLS |

- **Agent 契約は不変**（/sessions・/repos・/connections）。Workspace イメージと Agent は両ターゲットで**同一物**（[dev/09 §9.2](dev/09-deploy.md#92-ポートアダプタ--何をどのノブで差し替えるか)）。
- **CP↔Agent 到達**: ECS では publish host:port が無いので Service Connect / 内部 NLB / awsvpc ENI へ。`Runtime.Endpoint` 港が差を吸収。
- 詳細な AWS 構成は [03 AWS](reference/aws.md)。**多くの社はオンプレ compose で足りる**見込み。

---

## P3-8. デプロイ内 専用分離（機微部署・任意）
> ▶ **未着手**（P3-7 後）。

1 デプロイの中で、部署ごとに分離強度を変える。**既定=論理分離を先に**。大企業が内部に機微部署を持つ場合のみ。

| 区分 | Runtime | Network | Volume | 鍵 |
|------|---------|---------|--------|-----|
| 既定（shared）| 共有 | per-tenant/per-user network（Phase 2 `af-net-*` を tenant 込みに拡張）| 共有ストレージの per-tenant/user 領域 | per-tenant KEK |
| dedicated | 専用クラスタ/capacity provider | 専用 subnet/SG（テナント間到達不可）| 専用 EFS/領域 | 専用 KEK |

- **Runtime/Volume/Network 港に `Placement`**（`Tenant.isolation` から導出）。アダプタが領域を選ぶ。
- 「dedicated」は **IaC で対象テナントに別クラスタ + 別ストレージを払い出し `tenant.placement_ref` に記録 → CP がそこを target**、で十分。
- **テナント間ネットワーク不可視**: Phase 2 のユーザー間分離（`af-net-<user>`）を**テナント次元に拡張**。
- 注: 会社間分離は**デプロイ分離**で既に最強。P3-8 は「1 社の中の部署分離」に限った話。

---

## P3-9. 運用の成熟（社内・旧 Phase 4 を吸収）
> ◐ **idle-stop 実装済**（[p3-9-idle-stop](history/p3-9-idle-stop.md)）+ **showback 段1+段2 実装済**（バックエンド + Console 使用量ダッシュボード、[p3-9-showback](history/p3-9-showback.md)、段2 は要目視確認）。
> **auto-start（オンデマンド起動）実装済**（idle-stop の対＝scale-to-zero 完結、`AF_AUTOSTART`, 既定 on）。残＝観測 / egress 統制。バックアップ/復元は P3-10 段3 で実装済。

各社が自社デプロイを運用するための成熟。我々は機能と runbook を提供。

| 項目 | 内容 | 小規模での着地 |
|------|------|----------------|
| **社内 showback** ◐ | 部署別に使用量を可視化（任意の chargeback）。外部課金なし | **段1 実装済**: workspace 占有秒を per-(membership,day) にサンプリング累積（`AF_USAGE_SAMPLE_INTERVAL`, 既定 5m）→ `GET /api/admin/usage`（JSON=days+member 別 totals / CSV）。gate=super_admin（全社）or tenant_admin（自社 scope, `?tenant=`）。段2=Console ダッシュボード。設計 [p3-9-showback](history/p3-9-showback.md)。 |
| **ライフサイクル** | provision は管理者手動 / 停止（部署解散→stop・データ N 日保持）/ オフボード（エクスポート + 鍵 disable で crypto-shred）| crypto-shred は P3-3 で無料。 |
| **idle-stop（scale-to-zero）** ✅ | オンプレ単一ホストは RAM 逼迫（運用メモ host-oom-fleet-risk）ゆえ**実運用上きわめて重要**（旧 Phase 4 C1 を前倒し）| **実装済**: 二段構え（第1段=idle claude を halt で resumable 化 / 第2段=冷えた WS を docker stop）。テナント別 timeout（super_admin 編集）。設計 [p3-9-idle-stop](history/p3-9-idle-stop.md)。**auto-start（停止中 WS をセッション作成/fork/再開・持ち越し回答・SSM 探索で自動起動、`AF_AUTOSTART` 既定 on。端末アタッチは後に対象外へ）実装済**。残= ECS desired=0（P3-7 と共通化）。 |
| **バックアップ/復元** | **価値の本体は永続 home（資格情報・履歴・clone）**。home + DB のバックアップ/復元は必須機能 | オンプレ=ディスクスナップ/rsync、AWS=AWS Backup/S3。runbook 同梱。 |
| **観測** ◐ | メトリクス・アラート。noisy-neighbor 防止（クォータ + cgroup で緩和）| 簡易ダッシュボード + CloudWatch（AWS 時）。**全ユーザーのセッション俯瞰**を admin UI に実装（`GET /api/admin/sessions`＝running は Agent live / stopped は DB ミラー、テナント横断・検索・5s ポーリング。super_admin=全社 / tenant_admin=自社）。 |
| **egress 統制** | 情報持ち出し統制として egress allowlist | github/bitbucket/anthropic/claude.ai。 |

---

## P3-10. パッケージング & 配布 & アップグレード（提供モデルの核）
> ◐ **進行中**（提供モデルの核）。4 ターゲットの設計・実装記録は [docs/35](35-packaging.md)、**dist 配布は publish 運用中**
> （0.1.0〜、リリースノートは `deploy/release/notes/`）。完了判定 = 第 2 デプロイをゼロから立てて E2E 通過
> （[decisions/0001](decisions/0001-self-host-vs-saas.md)）——未達。

「グループ各社が自社でセルフホスト」を成立させる工程。機能（P3-1〜P3-9）を**他社の情シスが設置・運用・更新できる形**にする。

- **配布**: バージョン付きリリース。**Workspace/CP イメージ（タグ付き）+ `docker compose` 一式 + AWS（EC2-Single / ECS+CFN）+ native tar（WSL 向け）+ 設置スクリプト**
  （4 ターゲットの設計は [docs/35](35-packaging.md)）。イメージは各社の自社レジストリ（or 我々の社内レジストリ）から取得。
  **Helm chart（k8s）は需要が出るまで棚上げ** — AWS 希望社への答えは ECS+CFN（2026-07-21 決定、docs/35 §35.9-4）。
- **設定（その社が握る項目を 1 箇所に）**: Google OAuth client、許可ドメイン/ユーザー、公開ドメイン/TLS、**ルート鍵 custodian の指定**（Vault/ファイル/KMS）、リソース上限の既定、データ配置。
  → `.env` / 単一 config + `oauth.env`（Phase 2 の作法を踏襲）として文書化。**秘密は同梱しない**。
- **アップグレード**: 新イメージ取得 → **DB マイグレーション（goose、後方互換）** → 再起動。home/DB は保持。**ダウングレード不可点と移行注意を release note に明記**。
- **運用機能**: ヘルスチェック、構造化ログ、**バックアップ/復元（home + DB）**（P3-9）、設置/更新/障害対応の **runbook**。
- **非依存**: **phone-home しない**。各社デプロイは我々の中央基盤に一切依存しない（ライセンス確認等も持たない or オフライン可）。
- **検証ゲート**: 「第 2 デプロイ（別グループ会社相当）を**クリーンな環境にゼロから立てて E2E 通過**」を Phase 3 完了の実機判定にする（[12.4](#124-推奨シーケンス小規模local-first-継続) step5）。

## 12.3 ToS と分離の留意（自社ホスト前提）

- **ToS posture は最もクリーン**: 各社が**自社の社員を自社インフラでホスト**＝標準的な社内ツール姿勢。「別法人を他社インフラでホスト」のグレーは**デプロイ分離で消滅**。
  なお BYO の堅さのため、各社は**会社所有の Claude Team/Enterprise シート**を推奨（個人 Pro/Max は避ける）。これは各社の調達判断。
- **⚠️ 共有ホストの信頼モデル（B2・本音の残存リスク）**: 1 デプロイ内では CP が `docker.sock`（= ホスト root 相当）を持ち、起動時に平文 DEK をコンテナへ注入する。
  つまり **CP/ホストが侵害されれば、その社・そのデプロイ内の全ユーザーの分離（鍵・ネットワーク含む）が一括で破れる**。
  - これは「単一ホスト論理分離」の原理的限界。**会社間は別デプロイなので波及しない**のが本モデルの強み。
  - デプロイ内でさらに強い分離が要る部署は P3-8（dedicated）/ 別デプロイ / AWS（タスク分離・IMDS 遮断・docker.sock 非共有）へ。
  - 緩和: rootless Docker / ソケットプロキシ（権限絞り）/ CP 最小権限（[dev/07 §7.1](dev/07-security.md#71-脅威モデルと信頼境界)）。
- **データ責任は各社に閉じる**: データ・鍵・OAuth はその社が保有。我々（vendor）は実行時にアクセスしない（phone-home なし）。
- **可用性**: その社の SLA 相応。全社が依存するなら CP 冗長化 + DB バックアップを runbook で案内（[dev/09 §9.7](dev/09-deploy.md#97-バックアップ--リストア--アップグレードの設計前提)）。

## 12.4 推奨シーケンス（小規模・local-first 継続）

各ステップで「実機検証 → 設計確定」を回す。**オンプレ優先**で、AWS と専用化は後。
**完了状況は各 P3-x 節の冒頭バナーを参照**（P3-1〜P3-7 実装済・P3-10 は publish 運用中）。

1. ✅ **P3-1 + P3-2（オンプレ, SQLite）**: 階層モデル/RBAC を CP に入れ、現ライブを**既定テナント 1 + 既存ユーザー**として包む（B5 移行）。既存挙動を壊さず DB 化。
2. ✅ **P3-3（オンプレ）**: 封筒暗号 + custodian（Vault/ファイル KEK）。Phase 2 の HMAC から移行。**KMS は後（P3-7 で AWS アダプタ化）**。
3. ✅ **P3-4（オンプレ）**: クォータ強制を Start/SessionCreate/clone に差す（メモリは既存 `--memory` ですぐ）。
4. ✅/◐ **P3-5 + P3-6（オンプレ）**: 管理サービス層 → admin API（単一テナント）→ MCP を同一層で（管理 UI・MCP admin read/write 済＝残は dangerous 段のみ）。
5. ◐ **P3-10（オンプレ）**: compose パッケージ + 設定ファイル + マイグレーション + 設置/更新 runbook（dist publish 運用中）→ **第 2 デプロイ（別のグループ会社）を実際に立てて検証**（残）。
6. ◐ **P3-7（AWS）**: 希望する社向けに ECS/EFS/RDS/ALB アダプタ + KMS custodian（残 = KMS custodian・実 AWS 再検証）。
7. ▶ **P3-8 / P3-9**: 専用分離・showback・idle-stop・バックアップ・観測を需要に応じ。

（Phase 3/4 の完了条件は冒頭「[マイルストーン判定](#マイルストーン判定)」を参照。）
