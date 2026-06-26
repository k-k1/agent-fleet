# 12. Phase 3 — プロダクト化（パッケージ配布・グループ各社セルフホスト）

Phase 2 で「オンプレ 1 台・複数ユーザー相互不可視・at-rest 暗号化」の社内利用 MVP が完成した（[HANDOFF](HANDOFF.md)）。
本書は Phase 3 を **「AWS 移植」から「プロダクトのパッケージ化（グループ各社が自社でセルフホスト）」へ再定義**する設計。
旧 [05 ロードマップ](05-roadmap.md) の Phase 3（AWS アダプタ）と Phase 4（堅牢化）は、本書の要件の**substrate**として吸収する。

> **提供モデルの確定経緯（2026-06-27）**:
> 1. 当初は外部顧客向けの**商用マルチテナント SaaS** を検討 → **BYO（個人サブスクの Claude を第三者ホストの共有サービスで動かす）は Anthropic ToS グレー**で断念。
> 2. 次に**我々が 1 基盤を運用する社内マルチテナント SaaS**（自社+関連会社）を検討 → 関連会社（別法人）の社員を我々のインフラでホストする posture が依然グレー寄り。
> 3. **採用 = プロダクトをパッケージ化し、グループ各社が「自社で」セルフホスト**（1 社 = 1 デプロイ）。
>    各社が**自社の社員を自社インフラでホスト**するだけなので **ToS posture が最もクリーン**。我々は運用者でなく **vendor/maintainer**。

## 12.0 確定した前提（2026-06-27 の意思決定）

| 論点 | 決定 | 設計への効き方 |
|------|------|----------------|
| 提供形態 | **パッケージ製品。グループ各社が自社でセルフホスト**（1 社 = 1 デプロイ）| 我々は vendor/maintainer。中央基盤に各社が依存しない（phone-home なし）。**配布・設定・アップグレードが一級の関心事**（P3-10）。 |
| Claude 認証 | **BYO 継続**（各ユーザー自分の `/login`）| 各社が**自社の社員を自社インフラでホスト**＝最もクリーンな posture。サブスクは**会社所有の Team/Enterprise シート推奨**（個人 Pro/Max は避ける、§12.3）。 |
| 会社間分離 | **デプロイ分離（最強・無料）** | 別法人＝別デプロイ＝別インフラ・別 DB・別ルート鍵。共有ホスト由来のクロステナント懸念が原理的に消える。 |
| デプロイ内マルチテナント | **任意機能（既定=単一テナント=全社）** | 大きい社が**部署/プロジェクト単位**で分割したい時のみ有効化。`tenant` 概念はモデル/API に残し、UI/委譲は後付け。 |
| バジェット | **per-deployment・その社の管理者が設定** | Workspace 数・セッション数・ディスク GB・メモリ/CPU。自社ホスト保護 + 社内 showback（部署別は任意）。外部課金なし。 |
| デプロイ先 | **各社の選択**（既定 = オンプレ Docker/compose、任意で自社 AWS）| ポート&アダプタ（[09](09-portability.md)）はそのまま「**各社が選ぶ**」軸に。我々は両アダプタを同梱。 |
| 想定規模 | **小（1 デプロイ = 数十〜百ユーザー）** | 分散基盤を作り込まない。**DB は SQLite 既定**（CP 1 プロセス/1 ホストに最適・外部依存ゼロ）。Postgres は AWS/HA 時のみ。+ per-deployment 鍵。 |

> **最重要の含意**: 「我々が運用する基盤」は無い。各デプロイは**その社のもの**——データ・鍵・OAuth 設定・ユーザー管理は**すべてその社が握る**。
> 我々の責務は「**正しく動き・安全に隔離し・楽に設置/更新できるパッケージ**」を作ること。多くの社は**単一テナント**で運用し、マルチテナントは大企業向けの任意拡張。

## 12.1 アイデンティティ階層（パッケージ・セルフホスト版）

各デプロイ（= 1 社の自社ホストインスタンス）の中の階層。我々（vendor）は**実行時の階層に登場しない**。

```
Deployment（1 社が自社ホスト。データ・鍵・設定をその社が保有）
  ├── super_admin（その社の情シス）            ← デプロイ全体を統治
  └── Tenant（部署/プロジェクト, 既定 1 = 全社）  ← 任意の内部分割。多くの社は 1 つ
        ├── tenant_admin（部署管理者, 任意）
        └── User（社員）                         ← 既存の "user"。tenant_id と role を持つ
              └── Workspace（コンテナ）           ← tenant_id を継承
                    ├── Repository
                    └── Session
```

- **既存エンティティに `tenant_id` が付く**（既定テナント 1 つに全 User がぶら下がる単一テナント運用がデフォルト）。
- **user キーの変更**: 現状 `af-ws-<email>`（フラット）→ **不透明 ID**（DB の `workspace.id`）でコンテナ/ボリュームを命名。
  単一テナントでも email 直書きを脱し ID 化（改名・同名衝突・将来のマルチテナントに耐える）。
- **ロール**: `super_admin`（その社の情シス）/ `tenant_admin`（部署管理者・任意）/ `member`。RBAC を CP ハンドラで強制。
  ※ 旧設計の `platform_admin`（=我々）は**廃止**。我々は運用しないため実行時ロールを持たない。

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
| **P3-10** | **パッケージング & 配布 & アップグレード** | P3-1〜P3-7 | （新規・提供モデルの核）| compose/Helm + 設定 + マイグレーション + 設置/更新 runbook。 |

> **P3-10 が新提供モデルの核**。機能（P3-1〜P3-9）は「中身」、P3-10 は「**他社へ渡せる形にする**」工程。中身が無ければ包めないので依存は後ろだが、優先度は同格。

---

## P3-1. MetadataStore（SQLite 既定）— 全ての土台

**現状の欠落**: DB が**一切無い**。フォルダ名=ID、ポートは in-memory map、CP 再起動で再採番されうる（[HANDOFF §6.7 末尾の注意](HANDOFF.md)）。
テナント・バジェット・管理者・クォータ・監査は**すべて永続レコードを要する**。ここが全ワークストリームの gating item。

- **DB 選定 = SQLite 既定**: 1 デプロイ = CP 1 プロセス / 1 ホスト（オンプレ compose 既定）に**埋め込み DB がベストフィット**。外部 DB サーバ不要＝自己ホスト製品（P3-10）と相性最良。
  持つのは制御メタデータのみ（重いのは PTY であり DB ではない）で、数十〜百ユーザーは SQLite の余裕圏。
  **今は SQLite アダプタだけ実装**し、**Postgres は `MetadataStore` 港の裏で AWS/HA 時に後追い**（[09 §9.4](09-portability.md#94-プロファイル別アダプタ対応表)）。投機的に Postgres を作らない（リーン）。
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
  User        { id, tenant_id, email, display_name, role, status, last_login_at }
  Workspace   { id, tenant_id, user_id, runtime_handle(task_arn/container_id),
                endpoint(agent host:port / service), state, cpu, mem, disk_gb, last_active_at }
  Repository  { id, workspace_id, name, remote_url, default_branch, last_status }
  Session     { id, workspace_id, repository_id, tmux_name, claude_session_id, state, started_at, last_active_at }
  UserLimit   { user_id, max_sessions, disk_gb, mem_mb }   -- 管理者がテナント枠内で設定
  UsageCounter{ tenant_id, running_workspaces, running_sessions, used_disk_gb, allocated_mem_mb, sampled_at }
  WrappedDEK  { workspace_id, ciphertext_dek, key_version } -- P3-3
  AuditLog    { id, tenant_id, actor_user_id, actor_kind(user|admin|mcp|system), action, target, detail, at }
  ```
- **manager.go の昇格**: in-memory map を**廃止し DB を source of truth に**。CP 再起動時は Workspace レコードから rehydrate ＋ Runtime（docker inspect / ECS describe）で reconcile。
  → 「停止中コンテナのポート再採番」問題が原理的に消える。
- **現ライブ環境からの移行（B5）**: 今のライブ（`af-ws-k1-kami-gmail-com` 等）は**我々の社の「第 1 デプロイ＝リファレンス実装」**になる。
  移行 = **既定テナントを 1 つ作成 → 既存ユーザーを所属 → 既存コンテナ/home から DB をバックフィル**（Phase 2 の home/secrets 移行と同型の one-shot）。
- **規模配慮**: マイグレーションは単純な SQL（goose/atlas 等）。ORM は薄く。分散トランザクション不要。

---

## P3-2. アイデンティティ & テナント解決（AuthGateway 拡張）

現状の `AuthGateway.Identify` は email→sanitized user を返すだけ。マルチテナントでは **Identity = {tenant_id, user_id, role}** を返す。

- **L1（認証）は不変**: 各社が**自社の** oauth2-proxy（Google）/ ALB OIDC を設定（自社ドメイン `hd` 制限が自然）。我々は設定方法を文書化（P3-10）。
- **L2-authz（認可）を DB に移す**: emails.txt の静的許可を廃し、**CP が email を DB と突合**し「provisioned user か / どのテナントか」を判定。未登録 email は **403（not provisioned）**。
- **テナント解決**（単一テナントが既定なので通常は自明。マルチテナント時のみ）:
  1. **招待ベース（主）**: 管理者が email を招待 → `User` レコード先行作成。consumer ドメイン（gmail 等）も扱える。
  2. **ドメインマッピング（従）**: `Tenant.domains[]` に部署ドメインを登録 → 自動 provision（任意）。
- **単一テナント運用**: 認証済みユーザーは自動的に唯一のテナントに属す。テナント解決ロジックは「テナント数 1 なら即決」で軽い。
- **ゲート迂回封じは Phase 2 の規律を継承**: proxy モードでヘッダ欠落＝401、CP は `127.0.0.1` 束縛（[HANDOFF §6.8 B1](HANDOFF.md)）。
- **注意**: oauth2-proxy を `--email-domain=*` にすると L1 が「正当な Google アカウント」までしか絞らないので、**CP の DB メンバーシップ判定（403）と そのレート制限**が実質ゲート。自社ドメイン限定で運用するなら `hd`/`--email-domain` を効かせる方が堅い。

---

## P3-3. per-deployment/tenant 封筒暗号鍵（custodian 抽象・オンプレ優先）

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

**BYO のため対象はインフラ資源のみ**（Claude 利用量ではない）。その社の自社ホスト資源を守るためのもの。

| 次元 | テナント枠（管理者が設定）| ユーザー枠（テナント枠内）| 強制ポイント |
|------|--------------------------|----------------------------|--------------|
| Workspace 数 | max_workspaces（≒同時稼働ユーザー）| 1 user = 1 workspace（既定）| Workspace.Start |
| セッション数 | max_sessions（テナント合計）| max_sessions/user | Session.Create |
| メモリ/CPU | allocated_mem ≤ tenant cap | workspace の mem/cpu サイズ | Start（`--memory`/ECS task size）|
| ディスク | total_disk_gb | disk_gb/user | clone / 定期計測 |

- **強制方式（小規模）**: **ハードクォータ**。超過は `429` + 明示メッセージ。外部課金が無いので overage は社内配分の調整事項。
- **メモリ/CPU**: 既に `--memory`（docker, 既定 `WS_MEMORY=1g`）/ ECS task size で実装可能。テナント合計 ≤ cap を Start 時に検査。
- **ディスククォータは正直に難しい**（明示）:
  - local bind mount: XFS project quota / `--storage-opt size=`（overlay2+xfs）が要る。MVP は **計測（du）して clone 時/定期に soft-block**。
  - EFS: per-AP ハードクォータが無い。同様に **meter-and-block**。ハード FS クォータは後付け。
- **メータリング**: `UsageCounter` を定期サンプリング → 管理コンソールに可視化、社内 showback（P3-9）の原資料。

---

## P3-5. 管理コンソール + 管理 API

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

## P3-6. MCP による Agent Fleet 制御

CP の管理サービスを **MCP サーバ**として公開し、**その社の運用チーム**が Claude 経由で自社 Fleet を運用・トリアージできる。

- **形**: P3-5 の管理サービス層の**薄いラッパ**（Go の HTTP/SSE MCP サーバ）。新ロジックを足さない。
- **ツール例**:
  - 読み取り: `get_usage` / `list_workspaces` / `list_sessions` / `tail_audit`
  - 変更: `start_workspace` / `stop_workspace` / `stop_session` / `set_user_quota` / `rotate_key`
- **authz**: MCP は **super_admin 相当の scoped service principal**。RBAC を必ず通す。
  **既定は読み取り専用**、変更系は別 principal の opt-in + dry-run/confirm（鍵ローテ等は強力なため）。
- **監査**: MCP 経由の全操作を `actor_kind=mcp` で記録。
- **用途**: 「idle workspace を止めて」「監査ログの異常を要約」等、自社運用を Claude に委譲。

---

## P3-7. デプロイ先アダプタ（オンプレ Docker 既定 / 自社 AWS 任意）

各社が**自社のデプロイ先を選ぶ**。コアは無改修、周縁アダプタのみ（[09](09-portability.md)）。我々は両方を同梱（P3-10）。

| 港 | オンプレ（既定）| 自社 AWS（任意）|
|----|-----------------|-----------------|
| Runtime | Docker Engine（compose）| ECS（Fargate）|
| Volume | bind mount | EFS アクセスポイント |
| AuthGateway | oauth2-proxy（各社の Google）| ALB OIDC または oauth2-proxy |
| MetadataStore | Postgres/SQLite | RDS(Postgres) 単一 |
| KeyCustodian（P3-3）| Vault transit / ファイル KEK | KMS |
| Ingress/TLS | Caddy（自己署名/社内 CA）| ALB + ACM |
| Agent 認証 | 同一ホスト + Bearer（Phase2 A2）| SG 制限 + Bearer → 将来 mTLS |

- **Agent 契約は不変**（/sessions・/repos・/connections）。Workspace イメージと Agent は両ターゲットで**同一物**（[09 §9.2](09-portability.md#92-移植可能なコア-vs-差し替える周縁)）。
- **CP↔Agent 到達**: ECS では publish host:port が無いので Service Connect / 内部 NLB / awsvpc ENI へ。`Runtime.Endpoint` 港が差を吸収。
- 詳細な AWS 構成は [03 AWS](03-aws-deployment.md)。**多くの社はオンプレ compose で足りる**見込み。

---

## P3-8. デプロイ内 専用分離（機微部署・任意）

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

各社が自社デプロイを運用するための成熟。我々は機能と runbook を提供。

| 項目 | 内容 | 小規模での着地 |
|------|------|----------------|
| **社内 showback** | 部署別に使用量を可視化（任意の chargeback）。外部課金なし | UsageCounter → ダッシュボード + CSV。 |
| **ライフサイクル** | provision は管理者手動 / 停止（部署解散→stop・データ N 日保持）/ オフボード（エクスポート + 鍵 disable で crypto-shred）| crypto-shred は P3-3 で無料。 |
| **idle-stop（scale-to-zero）** | オンプレ単一ホストは RAM 逼迫（[[host-oom-fleet-risk]]）ゆえ**実運用上きわめて重要**（旧 Phase 4 C1 を前倒し）| アイドル検出 → docker stop / ECS desired=0。判定は両ターゲット共通。 |
| **バックアップ/復元** | **価値の本体は永続 home（資格情報・履歴・clone）**。home + DB のバックアップ/復元は必須機能 | オンプレ=ディスクスナップ/rsync、AWS=AWS Backup/S3。runbook 同梱。 |
| **観測** | メトリクス・アラート。noisy-neighbor 防止（クォータ + cgroup で緩和）| 簡易ダッシュボード + CloudWatch（AWS 時）。 |
| **egress 統制** | 情報持ち出し統制として egress allowlist | github/bitbucket/anthropic/claude.ai。 |

---

## P3-10. パッケージング & 配布 & アップグレード（提供モデルの核）

「グループ各社が自社でセルフホスト」を成立させる工程。機能（P3-1〜P3-9）を**他社の情シスが設置・運用・更新できる形**にする。

- **配布**: バージョン付きリリース。**Workspace/CP イメージ（タグ付き）+ `docker compose` 一式 + Helm chart（AWS/k8s 希望社向け）+ 設置スクリプト**。
  イメージは各社の自社レジストリ（or 我々の社内レジストリ）から取得。
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
  - 緩和: rootless Docker / ソケットプロキシ（権限絞り）/ CP 最小権限（[09 §9.8](09-portability.md#98-ローカル特有のセキュリティ留意)）。
- **データ責任は各社に閉じる**: データ・鍵・OAuth はその社が保有。我々（vendor）は実行時にアクセスしない（phone-home なし）。
- **可用性**: その社の SLA 相応。全社が依存するなら CP 冗長化 + DB バックアップを runbook で案内（[01 非機能](01-requirements.md#15-非機能要件)）。

## 12.4 推奨シーケンス（小規模・local-first 継続）

各ステップで「実機検証 → 設計確定」を回す（[05](05-roadmap.md) の流儀踏襲）。**オンプレ優先**で、AWS と専用化は後。

1. **P3-1 + P3-2（オンプレ, SQLite）**: 階層モデル/RBAC を CP に入れ、現ライブを**既定テナント 1 + 既存ユーザー**として包む（B5 移行）。既存挙動を壊さず DB 化。
2. **P3-3（オンプレ）**: 封筒暗号 + custodian（Vault/ファイル KEK）。Phase 2 の HMAC から移行。**KMS は後（P3-7 で AWS アダプタ化）**。
3. **P3-4（オンプレ）**: クォータ強制を Start/SessionCreate/clone に差す（メモリは既存 `--memory` ですぐ）。
4. **P3-5 + P3-6（オンプレ）**: 管理サービス層 → admin API（単一テナント）→ MCP を同一層で。
5. **P3-10（オンプレ）**: compose パッケージ + 設定ファイル + マイグレーション + 設置/更新 runbook → **第 2 デプロイ（別のグループ会社）を実際に立てて検証**。
6. **P3-7（AWS）**: 希望する社向けに ECS/EFS/RDS/ALB アダプタ + KMS custodian。
7. **P3-8 / P3-9**: 専用分離・showback・idle-stop・バックアップ・観測を需要に応じ。

> **マイルストーン判定（改訂）**
>
> | Phase | 完了条件 | 状態 |
> |-------|----------|------|
> | 3 | **パッケージとして配布でき**、別のグループ会社が**自社で**（オンプレ既定 / 自社 AWS 任意）セルフホストして、自社ユーザーを管理・バジェット強制・per-deployment 鍵で at-rest 暗号化して運用できる | 次 |
> | 4 | 社内 showback・ライフサイクル・idle-stop・バックアップ・観測・（任意）デプロイ内専用分離・MCP 運用が揃い、**グループ各社へ無理なく横展開できる** | — |
