# 01. 全体アーキテクチャ

> 正: コード（本書は地図と設計意図）/ 主な更新トリガ: プロセス構成・主要フロー・アダプタ境界の変更 / 最終確認: 2026-07

## 1.1 何であるか・提供モデル

社内の複数メンバーが Claude Code 等の CLI コーディングエージェントを共同利用するための
セルフホスト Web サービス。ユーザー毎の隔離コンテナ（Workspace）で git リポジトリを扱い、
ブラウザの Console からセッション起動・ターミナル操作・git 操作・ファイル閲覧・チャットを行う。

- **提供モデル**: パッケージ製品を各社が自社インフラでセルフホスト。**1 社 = 1 デプロイ**。
  SaaS は ToS で断念（[decisions/0001](../decisions/0001-self-host-vs-saas.md)）。
- **規模想定**: 同時 〜20 人。1 ユーザー複数セッション。単一ホスト（または単一クラスタ）で足りる。
- **Claude 認証は BYO**: 各ユーザーが自分のアカウントで `/login`（[08 §8.5](08-integrations.md)）。
- デプロイ先は各社選択: **オンプレ Docker（既定）** / 自社 AWS（🚧 [09](09-deploy.md)）。
  同一コアでデプロイ層だけをポート&アダプタで差し替える（§1.6）。

## 1.2 用語

| 用語 | 定義 |
|------|------|
| Workspace | メンバーシップ（identity×tenant）1 つに対応する永続コンテナ環境。ホーム・暗号ストア・working copy を保持 |
| Working copy | Workspace 内に clone した git リポジトリの作業ディレクトリ（`~/repos/<name>`）|
| Session | Working copy（または任意 dir）に紐づく、会話・設定・実行状態の論理単位。kind と driver を持ち、プロセスや tmux pane と 1:1 とは限らない |
| Driver | Session の制御経路。`managed` は共有 runtime＋構造化 API、`tui` は tmux 内の CLI 画面。利用者向け UI では「実行方式」の「マネージド」「ターミナル（CLI）」と呼ぶ |
| Control Plane (CP) | Workspace の外側で動く常駐バックエンド。認証・オーケストレーション・中継・永続化 |
| Workspace Agent | 各 Workspace コンテナ内の常駐プロセス。tmux・git・fs・CLI エージェントを直接操作する唯一の主体 |
| Console | ブラウザで動く SPA（React+Vite）。CP が静的配信 |
| Tenant / Identity / Membership | 部署 / 人 / その結節（多対多）。Workspace は membership 単位で完全分離（[06](06-data-model.md)）|

## 1.3 3 プロセス構成（local/Docker が既定）

図（同じ内容を draw.io で描いたもの。docker / native の差し替え点つき）:
[`img/architecture-overview.drawio`](../img/architecture-overview.drawio) /
MCP まわりの配線だけを取り出したもの: [`img/mcp-wiring.drawio`](../img/mcp-wiring.drawio)

```
Browser (Console SPA: React+Vite+zustand, xterm.js + BrowserPane canvas)
   │ HTTPS / WSS
   ▼
[エッジ]  Caddy(自動TLS, compose) / Tailscale Funnel / ALB … 運用者選択（09 §9.3）
   │ 127.0.0.1 loopback へ素通し
   ▼
Control Plane (Go 常駐, CP_ADDR 既定 :8080 / compose 127.0.0.1:8099)
   │  ・authGate(L1) → identity/membership 解決 → 認可
   │  ・Console(dist) 静的配信 / REST / WS / SSE
   │  ・Workspace lifecycle (Runtime アダプタ: docker | ecs)
   │  ・MetadataStore (SQLite 既定 | Postgres)
   │  ・内部 git プロバイダ / MCP / 監査 / egress / memo / reaper
   │
   │  中継: REST / SSE / terminal WS / browser REST+WS / preview
   │  認証: Bearer AGENT_TOKEN（per-container、CP が起動時に注入）
   ▼
Workspace Agent (Go, per-user コンテナ内, AGENT_ADDR 既定 :7700)
   │  ・Session lifecycle / Driver 選択・復旧
   │  ・managed runtime（Codex app-server / opencode serve、Workspace ごとに共有）
   │  ・tmux/PTY（Claude、shell、SSM、CLI を選んだ Codex / opencode）
   │  ・git / fs / connections（暗号ストア secrets.enc）
   │  ・チャット（headless CLI）/ transcript / usage
   │  ・BrowserManager（Chromium/CDP、Page、JPEG screencast、入力）
   │  ・/proxy/{port} … コンテナ内サービスへの preview 中継
   ▼
共有 runtime または CLI エージェント + git working copy（~/repos）
```

- コンテナは `af-ws-<slug>-<key>` 命名、専用ネットワーク `af-net-<slug>-<key>` で相互到達を遮断
  （**既定テナントのみ slug 無し**の `af-ws-<key>` / `af-net-<key>` — 既存デプロイ互換。`manager.workspaceNames`）。
  Agent ポートはホスト `127.0.0.1` publish 経由で CP からのみ到達（[07 §7.2](07-security.md)）。
- ブラウザは常に CP とだけ話す。CP は tmux にも git にも直接触れず、必ず Agent 経由。
- ホーム（`~`）は bind mount（`<WS_DATA>/<user>/home`）で永続。イメージ更新はホームに影響しない。
- 任意で egress forward proxy（`AF_EGRESS_LISTEN` 既定 `:3128`）を CP のサブコマンドとして併走（[07 §7.8](07-security.md)）。

## 1.4 認証は 2 層（重要・混同しない）

| 層 | 対象 | 方式 | 保存先 |
|----|------|------|--------|
| **L1 Console 認証** | 誰が Console を使えるか | `AUTH=oauth`（CP ネイティブ Google OAuth・既定）/ `proxy`（外部ゲートウェイのヘッダ信頼）/ `dev`（固定 ID）| 署名セッション cookie（CP）|
| **L2 エージェント認証** | 各ユーザーの Claude/codex/opencode を誰として動かすか | 各自の OAuth（Claude はコード貼り戻し方式）| Workspace の `CLAUDE_CONFIG_DIR` / `secrets.enc` |

L2 はユーザー本人の作業で、Console は**状態の可視化と接続 UI** を担う。詳細: L1 = [07 §7.3](07-security.md)、
L2 = [08](08-integrations.md)。

## 1.5 主要フロー

### ログイン（L1, AUTH=oauth）
```
Browser → CP /login → /oauth2/login → Google → /oauth2/callback
  → 許可リスト検証（メール/ドメイン, fail-closed）→ 署名 cookie 発行 → Console
以降の全リクエスト: authGate が cookie 検証 → X-Forwarded-Email を注入 → resolveIdentity
  → X-AF-Tenant ヘッダ（→query fallback）を membership で検証 → ハンドラ
```

### Workspace 起動 / アタッチ
```
Console「Start」→ CP: workspace.state 確認
  stopped → Runtime.Start（docker rm -f → docker run。DEK unwrap → AF_SECRET_KEY 注入）→ running 待ち
  running → そのまま
→ 以降 CP は Agent へ中継可能に。接続追跡（conns）で warm 維持、アイドルは reaper が停止
```

### セッション作成
```
Console: New session（kind, repo/dir, model, worktree 既定）
  → CP /api/sessions（クォータ検証・DB ミラー）→ Agent /sessions
  → Agent: メタを永続化し、driver ごとに起動
      managed（Codex / opencode の既定）: 共有 runtime に thread を作成または resume
      tui（Claude の既定、shell / SSM、明示選択した Codex / opencode）:
        tmux session 内で CLI を起動し、履歴があれば resume
  → Console: managed は会話 API、tui は会話 API または /ws/terminal で操作
```

### ターミナル接続
```
Browser xterm.js ──WSS /ws/terminal?session=&tenant=──▶ CP(proxyTerminal)
  → workspace running 確認（stopped/starting は 409、自動起動しない）
  → Agent /ws/pty へ Bearer 付き Dial → 双方向リレー（binary=PTY出力, text=入力/resize）
切断しても tmux は存続。再接続で同一画面に復帰。複数タブ同時アタッチ可。
```

この経路は `driver=tui` の Session だけが使う。`driver=managed` は pane を持たず、Console は
`POST /sessions/{name}/turn|respond|settings` と transcript API で操作する。Session の停止・再開・
archive・fork は driver 非依存の意味論を持ち、Agent が tmux または runtime handle へ振り分ける。

### リポジトリ clone
```
Console: Repos → URL 入力 → CP /api/repos → Agent: git clone
  （統一 cred helper で透過認証。GIT_TERMINAL_PROMPT=0 で fail-fast）
  → status（porcelain=v2 解析）を返して表示
```

## 1.6 ポート&アダプタ（プラットフォーム依存の差し替え点）

コア（Console / CP コアロジック / Agent / Workspace イメージ）は全ターゲット共通。
差し替わるのは CP 内の interface seam のみ。対応表と選定は [09](09-deploy.md)。

| ポート | interface | local（既定） | aws |
|--------|-----------|---------------|-----|
| コンテナ実行 | `Runtime` / `RuntimeFactory` | Docker Engine（socket）| ECS（RunTask/Service）🚧 |
| 永続ホーム | Runtime 内 | bind mount | EFS アクセスポイント 🚧 |
| L1 認証 | `AUTH` env 分岐 | oauth / dev | proxy（ALB OIDC）|
| メタデータ | `Store` | SQLite（既定・pure-Go）| Postgres（実装済）|
| at-rest 鍵 | `KeyCustodian` | localCustodian（master 由来 KEK）| KMS/Vault 📋（seam のみ、[decisions/0005](../decisions/0005-envelope-custodian.md)）|
| 入口/TLS | （CP 外）| Caddy / Funnel | ALB + ACM 🚧 |

## 1.7 現状 vs 計画の総覧

| 領域 | 状態 |
|------|------|
| local/Docker デプロイ（dev / compose）| ✅ 運用中 |
| マルチテナント（identity↔tenant 多対多・クォータ・監査・showback）| ✅ |
| 内部 git プロバイダ（bare + smart-HTTP + LFS）| ✅（[91](91-internal-git.md)）|
| MCP（CP `/mcp` + コンテナ内 stdio）| ✅（admin dangerous ツールは残）|
| egress 統制 | 🚧 log-only + allowlist 運用まで。enforce は後続（[docs/20](../20-container-audit-egress.md)）|
| AWS アダプタ（ECS/EFS/SSM・CFN）| 🚧 実装済・実運用実績なし（[09](09-deploy.md)）|
| KMS/Vault custodian | 📋 seam のみ |
| agy（Antigravity CLI）kind | ✅ 実装済み（[32](../32-agy-agent-kind.md)、採用判断は [decisions/0008](../decisions/0008-antigravity-cli-agent-kind.md)）|
| copilot（GitHub Copilot CLI）kind | ✅ 実装済み・Terminal+Managed 両対応（[36](../36-copilot-agent-kind.md) / [decisions/0019](../decisions/0019-copilot-agent-kind.md)）|
| kiro（Kiro・旧 Amazon Q Developer CLI）kind | ✅ 実装済み・Terminal+Managed 両対応（[43](../43-kiro-agent-kind.md) / [decisions/0026](../decisions/0026-kiro-agent-kind.md)）|
| コンテナ内ブラウザペイン | ✅ MVP実装・W5ライブ結線検証済み（[decisions/0018](../decisions/0018-container-browser-pane.md) / [設計31](../31-container-browser-pane.md)）|
| Go 内部リファクタ | 大半を統合済み（CP 分割、Agent `internal/`・エージェント縦割り）。残作業は [docs/23](../23-go-refactor.md)、現配置は [90](90-code-map.md) |
