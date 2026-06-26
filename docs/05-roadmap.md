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

## Phase 3 — AWS アダプタ（同一コアの移植）

**目的**: コアを変えずに `aws` ターゲットで稼働。

- アダプタ実装: Runtime=ECS、Volume=EFS AP、AuthGateway=ALB OIDC、Metadata=RDS/DynamoDB、Secret=Secrets Manager。
- ECR へイメージ配信、ALB + ACM、VPC/ネットワーク（[03](03-aws-deployment.md)）。
- ローカルで検証済みのコアをそのまま載せ替え、差分はアダプタに限定。

## Phase 4 — 本番化・堅牢化

**目的**: 運用に耐える形へ。

- scale-to-zero（アイドル検出。Runtime アダプタが docker stop / ECS desired=0 を吸収）。
- セキュリティ強化（[04](04-security.md)）: Task Role 最小権限、IMDS 遮断、Egress 制限、監査ログ。
- バックアップ（AWS Backup / S3）、イメージ一括更新フロー、管理者画面。
- IaC（Terraform / CDK）で全構成を再現可能に。

## マイルストーン判定

| Phase | 完了条件 | 状態 |
|-------|----------|------|
| 0 | `/login` がコンソール経由で完了でき、手順が文書化される | ✅ 完了（Phase 1 の実機検証で同時達成）|
| 1 | 1 ユーザーがローカル Docker で Claude セッション起動 + ターミナル操作できる（ホーム永続）| ✅ 完了（[11 §11.10](11-phase1-plan.md#1110-実装結果と実運用の知見phase-1-完了)）|
| 2 | オンプレ 1 台で複数ユーザーが相互不可視に並行利用でき、全ポートが抽象化される | 次 |
| 3 | コア無改修で `aws` ターゲットに載り、AWS 上で同等動作する | — |
| 4 | scale-to-zero + 監査 + バックアップ + IaC が揃い本番投入可能 | — |

> 注: Phase 1 ではリポジトリ管理(clone 等)は対象外（Phase 2）。セッションは既存ディレクトリに対して張った。

## 当面の次アクション

1. ~~技術スタック / 鍵粒度の決定~~ → 確定（Next.js + Go / ユーザー単位鍵 + 手動登録）。
2. ~~API 仕様 / ポータビリティ設計~~ → [06](06-api-spec.md)・[07](07-workspace-agent.md)・[09](09-portability.md)。
3. ~~Phase 0/1 を実機確認~~ → ✅ 完了。`/login` は localhost 非依存と判明（[02 §2.6](02-architecture.md#26-claude-login-フロー)）。
4. **Phase 2 に着手**: per-user Workspace 払い出し、リポジトリ管理(clone/checkout/branch/status)、
   SSH 鍵（[08](08-bitbucket.md)）、settings.json 編集 UI、Claude 認証状態表示（[06 §6.7](06-api-spec.md#67-claude-認証状態login)）、
   AuthGateway を oauth2-proxy に切替（local shared 形態, [09 §9.5](09-portability.md#95-ローカルの-2-形態authgateway-で切替)）。
5. 残る未決: Control Plane↔Agent 認証（[07 §7.5](07-workspace-agent.md#75-control-plane-との認証)）、scale-to-zero、課金範囲。
