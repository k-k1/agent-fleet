# Agent Fleet — Claude マルチユーザー運用コンソール

社内の複数メンバーが Claude Code を効率良く共同利用するための Web サービス。
ユーザー毎の隔離環境（コンテナ）で Bitbucket リポジトリを扱い、Claude セッションを
Web から起動・操作・管理する。同一コアを**ローカル（Docker）でも AWS でも**動かせるよう
デプロイ層をポート&アダプタで分離する（[portability](docs/dev/09-deploy.md)）。

**状態: Phase 2 完了・Phase 3 進行中。** オンプレ 1 台で複数ユーザーが相互不可視に並行利用でき
（per-user Workspace / AuthGateway / ネットワーク分離 / at-rest 暗号化）、Phase 3 のプロダクト化は
P3-1〜P3-5 + Console 全面刷新（React+Vite）まで完了。次は P3-7（AWS アダプタ）以降（[docs/roadmap.md](docs/roadmap.md)）。
**現状の運用詳細・落とし穴は [docs/HANDOFF.md](docs/HANDOFF.md)（次セッションはまず読む）。**
コード: [`workspace/`](workspace/)（Agent + イメージ）/ [`control-plane/`](control-plane/) /
[`console/`](console/) / 起動は [`deploy/local/run-dev.sh`](deploy/local/run-dev.sh)。

## セルフホスト（オンプレ / Docker Compose）

各社が自社インフラで 1 デプロイを立てる想定。イメージ一式を `compose up` するだけ
（Caddy が Let's Encrypt で自動 TLS、CP ネイティブ Google OAuth でログイン）。

```bash
cd deploy/compose
cp .env.example .env     # 秘密を生成して記入（AF_MASTER_KEY 等）
docker compose up -d --build
```

手順・鍵生成・バックアップ/復元・アップグレード・障害対応・DooD 制約は
**[deploy/compose/README.md](deploy/compose/README.md)（runbook）** に集約。ローカル dev
（ホストプロセス起動）は従来どおり [`deploy/local/run-dev.sh`](deploy/local/run-dev.sh)。

## 確定済みの前提（v1）

| 論点 | 決定 | 理由・補足 |
|------|------|-----------|
| Claude 認証 | 各ユーザーが自分のアカウントで `/login` | コンソールで各自の認証状況を可視化・再ログイン誘導する |
| ユーザー分離 | ユーザー毎コンテナ | 移植性・隔離が高く AWS と相性が良い |
| 想定規模 | 〜20 人（同時） | 単一クラスタ + オーケストレーション層で十分 |
| 永続化 | `local`=bind mount / `aws`=EBS/EFS | ホーム・clone・認証情報・履歴をディスク保持 |
| git 認証 | Console から HTTPS トークン/OAuth（Connections）| SSH 鍵から格下げ。秘密を CP は保持しない（[decisions/0003](docs/decisions/0003-ssh-to-connections.md)）|
| 技術スタック | Console=React+Vite / Backend=Go | 常駐・WS プロキシ・コンテナ制御に Go が好適（[decisions/0004](docs/decisions/0004-vanilla-to-react.md)）|
| 提供モデル | パッケージ製品・各社セルフホスト | 1 社=1 デプロイ。SaaS は ToS で断念（[decisions/0001](docs/decisions/0001-self-host-vs-saas.md)）|
| デプロイ層 | local / aws を同一コアで切替 | ポート&アダプタで分離（local は Docker、local-first で進める）|

## ドキュメント構成

索引は [docs/README.md](docs/README.md)。**仕様の正＝[docs/dev/](docs/dev/README.md)（開発者向け）とコード、
操作の正＝[docs/guide/](docs/guide/README.md)（利用者向け）、稼働状態＝[HANDOFF](docs/HANDOFF.md)。**
意思決定（なぜ）＝`decisions/`、前向きの計画＝`roadmap.md`、使い終わった計画・完了した機能設計＝`history/`。

**開発者向け [docs/dev/](docs/dev/README.md)**（コードに追従する設計と契約）
| ファイル | 内容 |
|----------|------|
| [01-architecture](docs/dev/01-architecture.md) | 提供モデル・用語・3プロセス構成・認証2層・主要フロー・アダプタ |
| [02-console](docs/dev/02-console.md) / [03-control-plane](docs/dev/03-control-plane.md) / [04-workspace-agent](docs/dev/04-workspace-agent.md) | 各コンポーネントの設計 |
| [05-api-contracts](docs/dev/05-api-contracts.md) / [06-data-model](docs/dev/06-data-model.md) | API 境界・中継 / データモデル |
| [07-security](docs/dev/07-security.md) / [08-integrations](docs/dev/08-integrations.md) | 脅威モデル・認証・暗号 / 外部連携 |
| [09-deploy](docs/dev/09-deploy.md) / [10-development](docs/dev/10-development.md) | デプロイ・ポータビリティ / 開発作法 |
| [90-code-map](docs/dev/90-code-map.md) / [91-internal-git](docs/dev/91-internal-git.md) | コード地図 / 内部 git プロバイダ |

**利用者向け [docs/guide/](docs/guide/README.md)**: ペルソナ別分冊（member / admin / operator / lite）。

**引き継ぎ・計画**
| ファイル | 内容 |
|----------|------|
| [docs/HANDOFF.md](docs/HANDOFF.md) | このホストの稼働状態・実行作法・落とし穴・現在地 |
| [docs/CHANGELOG-handoff.md](docs/CHANGELOG-handoff.md) | 時系列ログ（日付＋1行）|
| [docs/roadmap.md](docs/roadmap.md) | フェーズ一覧・マイルストーン + Phase 3 詳細設計（P3-1〜P3-10）|

> 旧 `docs/reference/` は dev/ へ再編済み（読み替え表は [docs/README.md](docs/README.md)）。

**decisions/ — 意思決定（なぜ・捨てた選択肢）**
| ファイル | 内容 |
|----------|------|
| [0001-self-host-vs-saas.md](docs/decisions/0001-self-host-vs-saas.md) | 提供モデル: SaaS 断念・各社セルフホスト採用（ToS 根拠・残存リスク）|
| [0002-claude-auth-onboarding.md](docs/decisions/0002-claude-auth-onboarding.md) | Claude 認証: auth と onboarding は別物（ログイン画面の真因）|
| [0003-ssh-to-connections.md](docs/decisions/0003-ssh-to-connections.md) | git 認証: SSH 鍵 → Connections（HTTPS トークン/OAuth）|
| [0004-vanilla-to-react.md](docs/decisions/0004-vanilla-to-react.md) | Console スタック: React + Vite を採用 |
| [0005-envelope-custodian.md](docs/decisions/0005-envelope-custodian.md) | at-rest 鍵: 封筒暗号 + custodian 抽象（on-prem の限界を明記）|

**history/ — 使い終わった実装プラン（完了・記録）**
| ファイル | 内容 |
|----------|------|
| [phase0-poc.md](docs/history/phase0-poc.md) | Phase 0 PoC 手順書（`/login` 検証）|
| [phase1-plan.md](docs/history/phase1-plan.md) | Phase 1 実装プラン + 実装結果（§11.10 は今も有効な知見）|
| [p3-1-metadatastore.md](docs/history/p3-1-metadatastore.md) | P3-1: MetadataStore（SQLite）導入 |
| [p3-2-identity-tenant.md](docs/history/p3-2-identity-tenant.md) | P3-2: identity↔tenant 多対多 |
| [p3-3-envelope-crypto.md](docs/history/p3-3-envelope-crypto.md) | P3-3: 封筒暗号 + custodian 抽象 |
| [p3-4-quota.md](docs/history/p3-4-quota.md) | P3-4: リソースバジェット/クォータ |
| [p3-5-member-console.md](docs/history/p3-5-member-console.md) | P3-5: メンバー Console UX（git/ファイル可視化）|
| [console-redesign.md](docs/history/console-redesign.md) | Console UI 刷新ブリーフ（vanilla→React の診断）|

## 既存プロトタイプ資産（再利用元）

個人用フリート運用の仕組みがすでに存在し、これをサービス化する。

- **`oauth2-proxy`** — Google ドメイン制限の認証ゲート（`emails.txt` ホワイトリスト）。**現行はこれを廃し CP ネイティブ Google OAuth（`AUTH=oauth`）に集約**——許可リストは `deploy/local/allowed-emails.txt`（メール / `@domain`）。設計 [docs/dev/07 §7.3](docs/dev/07-security.md)
- **`scripts/tmux-claude.sh`** — detached tmux で複数 Claude CLI を冪等起動・resume・世代管理
- **`CLAUDE_CONFIG_DIR` プロファイル分離** — ディレクトリ配下で別 `~/.claude` を使い分ける仕組み
- **`~/.claude/settings.json`** — `remoteControlAtStartup` / `skipDangerousModePermissionPrompt` 設定済み

## 用語

- **Workspace** — ユーザー 1 人に対応する永続コンテナ環境。ホームボリュームと稼働プロセスを持つ。
- **Working copy** — Workspace 内に clone した Bitbucket リポジトリの作業ディレクトリ。
- **Session** — Working copy に紐づく Claude CLI プロセス（tmux セッション 1 本）。

## ライセンス

[Apache License 2.0](LICENSE)（特許グラント付き permissive）。資格情報を扱うツールの
ソースを公開し各社が暗号/隔離実装を監査できることを採用の強みとする。貢献は
[CONTRIBUTING.md](CONTRIBUTING.md)、脆弱性報告と脅威モデルは [SECURITY.md](SECURITY.md)。
