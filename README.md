# Agent Fleet — Claude マルチユーザー運用コンソール

社内の複数メンバーが Claude Code を効率良く共同利用するための Web サービス。
ユーザー毎の隔離環境（コンテナ）で Bitbucket リポジトリを扱い、Claude セッションを
Web から起動・操作・管理する。最終的に AWS 上でホストする。

このリポジトリは現時点では**設計ドキュメント置き場**。実装はまだ無い。

## 確定済みの前提（v1）

| 論点 | 決定 | 理由・補足 |
|------|------|-----------|
| Claude 認証 | 各ユーザーが自分のアカウントで `/login` | コンソールで各自の認証状況を可視化・再ログイン誘導する |
| ユーザー分離 | ユーザー毎コンテナ | 移植性・隔離が高く AWS と相性が良い |
| 想定規模 | 〜20 人（同時） | 単一クラスタ + オーケストレーション層で十分 |
| 永続化 | EBS/EFS で永続化 | ホーム・clone・認証情報・履歴をディスク保持 |

## ドキュメント構成

| ファイル | 内容 |
|----------|------|
| [docs/01-requirements.md](docs/01-requirements.md) | 用語、機能要件、非機能要件、確定事項と未決事項 |
| [docs/02-architecture.md](docs/02-architecture.md) | 全体構成、コンポーネント、データモデル、主要フロー |
| [docs/03-aws-deployment.md](docs/03-aws-deployment.md) | AWS 構成、ネットワーク、コスト試算 |
| [docs/04-security.md](docs/04-security.md) | 脅威モデル、隔離境界、権限設計 |
| [docs/05-roadmap.md](docs/05-roadmap.md) | 段階的な実装計画（PoC → MVP → 本番） |

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
