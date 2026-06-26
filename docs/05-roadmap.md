# 05. ロードマップ

既存資産（`oauth2-proxy` / `tmux-claude.sh` / `CLAUDE_CONFIG_DIR`）を踏み台に段階的に進める。
各フェーズで「実機検証 → 設計確定」を回す。

## Phase 0 — PoC（単一ホスト, 既存資産の延長）

**目的**: `/login` の対話フローと UX 仮説を最小コストで検証する。

- 単一 EC2 上で、既存の `oauth2-proxy` + `tmux-claude.sh` をそのまま動かす。
- 2〜3 名で「コンソール認証 → ターミナル → clone → セッション起動」を手触り確認。
- **重点検証**: ヘッドレス環境での `claude /login`（URL 手動オープン + コード貼り戻し）。
  → [02 §2.6](02-architecture.md#26-claude-login-フロー) と [01 未決 #6](01-requirements.md#17-未決事項今後詰める) の確定。
- 成果物: `/login` 手順の確定、Workspace Agent に必要な操作一覧の洗い出し。

## Phase 1 — Workspace イメージ + Console MVP

**目的**: 1 ユーザー分のコンテナ化と最小コンソール。

- Workspace コンテナイメージ（claude CLI / git / tmux / Workspace Agent）を ECR に。
- Workspace Agent: PTY ターミナル(WSS) / セッション一覧 / セッション起動・停止。
- Console MVP: Google OAuth ログイン、ターミナル表示、セッション一覧。
- 単一 Workspace を手動起動して疎通。EFS マウントでホーム永続を確認。

## Phase 2 — マルチユーザー・オーケストレーション

**目的**: 〜20 人を ECS + EFS で運用可能に。

- Control Plane: ユーザー/Workspace ライフサイクル、ECS 制御、メタデータ DB。
- EFS アクセスポイントによる per-user ホーム自動払い出し。
- リポジトリ管理 UI（clone / checkout / branch / status）。
- SSH 鍵の生成・表示、（任意で）Bitbucket API 自動登録。
- `settings.json` エディタ、remote-control トグル、Claude `/login` 状態表示。

## Phase 3 — AWS 本番化・堅牢化

**目的**: 運用に耐える形へ。

- scale-to-zero（アイドル検出・自動停止/起動）でコスト最適化。
- セキュリティ強化（[04](04-security.md)）: Task Role 最小権限、IMDS 遮断、Egress 制限、監査ログ。
- バックアップ（AWS Backup / S3）、イメージ一括更新フロー、管理者画面。
- IaC（Terraform / CDK）で全構成を再現可能に。

## マイルストーン判定

| Phase | 完了条件 |
|-------|----------|
| 0 | `/login` がコンソール経由で完了でき、手順が文書化される |
| 1 | 1 ユーザーが Web から clone + Claude セッション起動 + ターミナル操作できる（ホーム永続）|
| 2 | 複数ユーザーが相互不可視に並行利用できる |
| 3 | scale-to-zero + 監査 + バックアップ + IaC が揃い本番投入可能 |

## 当面の次アクション

1. [01 未決事項](01-requirements.md#17-未決事項今後詰める) のうち #5（技術スタック）#3/#4（鍵粒度・自動登録）を決める。
2. Phase 0 を着手し `/login` フローを実機確認する。
3. Console/Control Plane の API 仕様（REST + WS）をドラフト化する。
