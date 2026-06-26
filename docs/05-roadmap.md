# 05. ロードマップ

既存資産（`oauth2-proxy` / `tmux-claude.sh` / `CLAUDE_CONFIG_DIR`）を踏み台に段階的に進める。
**local-first** で進め、同一コアに AWS アダプタを後付けする（[09](09-portability.md)）。
各フェーズで「実機検証 → 設計確定」を回す。

## Phase 0 — PoC（ローカル dev, 既存資産の延長）

**目的**: `/login` の対話フローと UX 仮説を最小コストで検証する。

- 手元マシン上で、既存の `oauth2-proxy`（または認証バイパス）+ `tmux-claude.sh` をそのまま動かす。
- 2〜3 名で「コンソール認証 → ターミナル → clone → セッション起動」を手触り確認。
- **重点検証**: ヘッドレスコンテナでの `claude /login`（URL 手動オープン + コード貼り戻し）。
  → [02 §2.6](02-architecture.md#26-claude-login-フロー) と [01 未決 #3](01-requirements.md#17-未決事項今後詰める) の確定。
- 成果物: `/login` 手順の確定、Workspace Agent に必要な操作一覧の洗い出し。

## Phase 1 — Workspace イメージ + Console MVP（ローカル dev）

**目的**: 1 ユーザー分のコンテナ化と最小コンソールを、ローカル Docker で完成。

- Workspace コンテナイメージ（claude CLI / git / tmux / Workspace Agent）を定義（ターゲット共通物）。
- Workspace Agent: PTY ターミナル(WSS) / セッション一覧 / セッション起動・停止。
- Console MVP: ログイン（dev は固定 ID バイパス）、ターミナル表示、セッション一覧。
- Control Plane に **Runtime/Volume ポート**を実装し `local`（Docker）アダプタで疎通。bind mount でホーム永続を確認。

## Phase 2 — マルチユーザー（ローカル shared）+ ポート確立

**目的**: オンプレ 1 台で複数ユーザーを相互不可視に。アダプタ層を固める。

- AuthGateway を oauth2-proxy（Google `hd` 制限）に切替え、複数ユーザー対応。
- 全ポート（Runtime/Volume/AuthGateway/MetadataStore/SecretStore）をインターフェース化（[09 §9.3](09-portability.md#93-ポート定義go-インターフェース概略)）。
- リポジトリ管理 UI（clone / checkout / branch / status）、SSH 鍵の生成・表示。
- `settings.json` エディタ、remote-control トグル、Claude `/login` 状態表示。

## Phase 3 — プロダクト化（パッケージ配布・グループ各社セルフホスト）

**目的**: 「AWS 移植」から**プロダクトのパッケージ化**へ再定義。詳細設計は [12 Phase 3](12-phase3-multitenant.md)。

> **提供モデル確定**: 商用 SaaS（外部 BYO は ToS グレー）も、我々が運用する社内マルチテナント SaaS（別法人ホストがグレー寄り）も断念。
> 採用 = **プロダクトをパッケージ化し、グループ各社が「自社で」セルフホスト**（1 社=1 デプロイ）。各社が自社の社員を自社インフラでホスト＝**ToS 最もクリーン**。我々は vendor/maintainer。

確定前提（2026-06-27）: **パッケージ製品・各社セルフホスト / BYO 継続 / 会社間=デプロイ分離（最強）/ デプロイ内マルチテナント=任意（既定 単一）/ 小規模（1 デプロイ 数十〜百ユーザー）/ デプロイ先は各社選択（オンプレ既定・自社 AWS 任意）**。

- **P3-1** MetadataStore（**SQLite 既定**、Postgres は AWS/HA 時）+ 階層モデル + RBAC — 全ての土台（現状 DB 無し）。Plan 抽象なし、Tenant 直付け。
- **P3-2** アイデンティティ & テナント解決（AuthGateway 拡張、各社 OAuth、emails.txt→DB）。
- **P3-3** per-deployment/tenant 封筒暗号鍵（custodian 抽象＝**オンプレ Vault/ファイル優先**、KMS は AWS アダプタ。Phase 2 の単一 master 昇格）。
- **P3-4** リソースバジェット/クォータ（テナント+ユーザー。**インフラ資源**のみ）。
- **P3-5/6** 管理コンソール + 管理 API（super_admin 主）、**MCP による Fleet 制御**（同一サービス層）。
- **P3-7** デプロイ先アダプタ（オンプレ Docker 既定 / 自社 AWS=ECS/EFS/RDS/ALB/KMS 任意）。旧 Phase 3 本体。
- **P3-8** デプロイ内 専用分離（既定=論理を先に、機微部署のみ dedicated）。
- **P3-10** **パッケージング & 配布 & アップグレード**（提供モデルの核。compose/Helm + 設定 + マイグレーション + runbook）。

## Phase 4 — 運用の成熟・グループ横展開

**目的**: グループ各社へ無理なく横展開できる形へ。詳細は [12 §P3-9](12-phase3-multitenant.md)。

- 社内 showback（部署別。外部課金なし）。
- ライフサイクル（provision は管理者手動 / 停止 / オフボード = 鍵 disable で crypto-shred）。
- scale-to-zero（アイドル検出。オンプレ単一ホストの RAM 逼迫で前倒し重要）。
- バックアップ/復元（永続 home + DB が価値の本体）。
- セキュリティ強化（[04](04-security.md)）: Task Role 最小権限、IMDS 遮断、Egress 制限、監査ログ、rootless/socket-proxy。
- 観測、CP 冗長化、イメージ一括更新、IaC（Terraform / CDK / Helm）。

## マイルストーン判定

| Phase | 完了条件 | 状態 |
|-------|----------|------|
| 0 | `/login` がコンソール経由で完了でき、手順が文書化される | ✅ 完了（Phase 1 の実機検証で同時達成）|
| 1 | 1 ユーザーがローカル Docker で Claude セッション起動 + ターミナル操作できる（ホーム永続）| ✅ 完了（[11 §11.10](11-phase1-plan.md#1110-実装結果と実運用の知見phase-1-完了)）|
| 2 | オンプレ 1 台で複数ユーザーが相互不可視に並行利用でき、全ポートが抽象化される | ✅ 完了 |
| 3 | **パッケージとして配布でき**、別のグループ会社が**自社で**（オンプレ既定 / 自社 AWS 任意）セルフホストして、自社ユーザーを管理・バジェット強制・per-deployment 鍵で at-rest 暗号化して運用できる（[12](12-phase3-multitenant.md)）| 次 |
| 4 | 社内 showback・ライフサイクル・scale-to-zero・バックアップ・観測・（任意）デプロイ内専用分離・MCP 運用が揃い、グループ各社へ無理なく横展開できる | — |

> 注: Phase 1 ではリポジトリ管理(clone 等)は対象外（Phase 2）。セッションは既存ディレクトリに対して張った。

## 当面の次アクション

1. ~~技術スタック / 鍵粒度の決定~~ → 確定（Next.js + Go / ユーザー単位鍵 + 手動登録）。
2. ~~API 仕様 / ポータビリティ設計~~ → [06](06-api-spec.md)・[07](07-workspace-agent.md)・[09](09-portability.md)。
3. ~~Phase 0/1 を実機確認~~ → ✅ 完了。`/login` は localhost 非依存と判明（[02 §2.6](02-architecture.md#26-claude-login-フロー)）。
4. **Phase 2 に着手**: per-user Workspace 払い出し、リポジトリ管理(clone/checkout/branch/status)、
   SSH 鍵（[08](08-bitbucket.md)）、settings.json 編集 UI、Claude 認証状態表示（[06 §6.7](06-api-spec.md#67-claude-認証状態login)）、
   AuthGateway を oauth2-proxy に切替（local shared 形態, [09 §9.5](09-portability.md#95-ローカルの-2-形態authgateway-で切替)）。
5. 残る未決: Control Plane↔Agent 認証（[07 §7.5](07-workspace-agent.md#75-control-plane-との認証)）、scale-to-zero、課金範囲。
