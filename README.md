# Agent Fleet — Claude マルチユーザー運用コンソール

社内の複数メンバーが Claude Code を効率良く共同利用するための Web サービス。
ユーザー毎の隔離環境（コンテナ）で Bitbucket リポジトリを扱い、Claude セッションを
Web から起動・操作・管理する。同一コアを**ローカル（Docker）でも AWS でも**動かせるよう
デプロイ層をポート&アダプタで分離する（[docs/09](docs/09-portability.md)）。

**状態: Phase 1 MVP 完了（実機検証済み, 2026-06-26）。** ローカル Docker 上で Workspace Agent +
Control Plane + 最小 Console が動作し、Tailscale Funnel 越しのブラウザから各自アカウントの `/login` を含む
フルチェーンを確認済み（[docs/11 §11.10](docs/11-phase1-plan.md#1110-実装結果と実運用の知見phase-1-完了)）。
コード: [`workspace/`](workspace/)（Agent + イメージ）/ [`control-plane/`](control-plane/) /
[`console/`](console/) / 起動は [`deploy/local/run-dev.sh`](deploy/local/run-dev.sh)。次は Phase 2。

## 確定済みの前提（v1）

| 論点 | 決定 | 理由・補足 |
|------|------|-----------|
| Claude 認証 | 各ユーザーが自分のアカウントで `/login` | コンソールで各自の認証状況を可視化・再ログイン誘導する |
| ユーザー分離 | ユーザー毎コンテナ | 移植性・隔離が高く AWS と相性が良い |
| 想定規模 | 〜20 人（同時） | 単一クラスタ + オーケストレーション層で十分 |
| 永続化 | EBS/EFS で永続化 | ホーム・clone・認証情報・履歴をディスク保持 |
| Bitbucket 鍵 | ユーザー単位の鍵 + 手動登録 | トークンを預からず責任範囲を限定 |
| 技術スタック | Console=Next.js / Backend=Go | 常駐・WS プロキシ・ECS 制御に Go が好適 |
| デプロイ層 | local / aws を同一コアで切替 | ポート&アダプタで分離（local は Docker、local-first で進める）|

## ドキュメント構成

| ファイル | 内容 |
|----------|------|
| [docs/01-requirements.md](docs/01-requirements.md) | 用語、機能要件、非機能要件、確定事項と未決事項 |
| [docs/02-architecture.md](docs/02-architecture.md) | 全体構成、コンポーネント、データモデル、主要フロー |
| [docs/03-aws-deployment.md](docs/03-aws-deployment.md) | AWS 構成、ネットワーク、コスト試算 |
| [docs/04-security.md](docs/04-security.md) | 脅威モデル、隔離境界、権限設計 |
| [docs/05-roadmap.md](docs/05-roadmap.md) | 段階的な実装計画（PoC → MVP → 本番） |
| [docs/06-api-spec.md](docs/06-api-spec.md) | Control Plane の REST API と WebSocket プロトコル |
| [docs/07-workspace-agent.md](docs/07-workspace-agent.md) | Workspace Agent のインターフェースとセッション制御 |
| [docs/08-bitbucket.md](docs/08-bitbucket.md) | Bitbucket 連携（SSH 鍵・clone・ブランチ・status）|
| [docs/09-portability.md](docs/09-portability.md) | デプロイ層の分離（ポート&アダプタ、local/aws 両対応）|
| [docs/10-phase0-poc.md](docs/10-phase0-poc.md) | Phase 0 PoC 手順書（`/login` 検証）。実体は [`phase0/`](phase0/)|
| [docs/11-phase1-plan.md](docs/11-phase1-plan.md) | Phase 1 実装プラン + 実装結果（§11.10）|
| [docs/12-phase3-multitenant.md](docs/12-phase3-multitenant.md) | **Phase 3 設計**: プロダクト化（パッケージ配布・グループ各社セルフホスト／DB/鍵/バジェット/管理者/MCP/パッケージング）|
| [docs/13-p3-1-plan.md](docs/13-p3-1-plan.md) | **P3-1 実装プラン**: MetadataStore（SQLite）導入。現ライブを既定テナントで包む DB 化（実装・検証済）|
| [docs/14-p3-2-plan.md](docs/14-p3-2-plan.md) | **P3-2 実装プラン**: identity↔tenant 多対多。email で人を特定／作業対象テナントは明示選択（実装・検証済）|
| [docs/15-p3-3-plan.md](docs/15-p3-3-plan.md) | **P3-3 実装プラン**: 封筒暗号 + custodian 抽象（オンプレ KEK／将来 Vault・KMS）。Agent 無改修・ライブ無傷（実装・検証済）|
| [docs/16-p3-4-plan.md](docs/16-p3-4-plan.md) | **P3-4 実装プラン**: リソースバジェット/クォータ（Workspace数/セッション数、ハード block、既定無制限、state 同期）（実装・検証済）|
| [docs/17-p3-5-plan.md](docs/17-p3-5-plan.md) | **P3-5 実装プラン**: メンバー Console UX（shell セッション / git 閲覧・操作 / ファイルブラウザ / 機微状態退避）|
| [docs/HANDOFF.md](docs/HANDOFF.md) | **引き継ぎ**: 稼働状態・実行作法・落とし穴・次フェーズ入口（次セッションはまず読む）|

## 既存プロトタイプ資産（再利用元）

個人用フリート運用の仕組みがすでに存在し、これをサービス化する。

- **`oauth2-proxy`** — Google ドメイン制限の認証ゲート（`emails.txt` ホワイトリスト運用済み）
- **`scripts/tmux-claude.sh`** — detached tmux で複数 Claude CLI を冪等起動・resume・世代管理
- **`CLAUDE_CONFIG_DIR` プロファイル分離** — ディレクトリ配下で別 `~/.claude` を使い分ける仕組み
- **`~/.claude/settings.json`** — `remoteControlAtStartup` / `skipDangerousModePermissionPrompt` 設定済み

## 用語

- **Workspace** — ユーザー 1 人に対応する永続コンテナ環境。ホームボリュームと稼働プロセスを持つ。
- **Working copy** — Workspace 内に clone した Bitbucket リポジトリの作業ディレクトリ。
- **Session** — Working copy に紐づく Claude CLI プロセス（tmux セッション 1 本）。
