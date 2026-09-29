---
audience: "誰でも最初に読む章。全体の形を掴みたい人"
source_of_truth: "コード（本書は地図と設計意図）"
updated: "2026-09"
---

# 01. 全体アーキテクチャ

[English](01-architecture.md) | 日本語

## 1.1 何であるか・提供モデル

社内の複数メンバーが CLI コーディングエージェントを共同利用するためのセルフホスト Web サービス。
ユーザーごとの隔離された環境（Workspace）で git リポジトリを扱い、ブラウザの Console から
セッション起動・ターミナル操作・git 操作・ファイル閲覧・チャットを行う。

- **提供モデル**: パッケージ製品を各社が自社インフラでセルフホスト。**1 社 = 1 デプロイ**。
  SaaS は ToS で断念（[decisions/0001](../decisions/0001-self-host-vs-saas.ja.md)）。
- **規模想定**: 同時 〜20 人。1 ユーザー複数セッション。単一ホスト（または単一クラスタ）で足りる。
- **エージェントの資格情報は利用者が持ち込む**: 各自が自分のアカウントでサインインする
  （[08](08-integrations.ja.md)）。
- **デプロイ先は各社が選ぶ**: 単一ホストの Docker（既定）、Docker 無しの単一ユーザー構成、
  または自社 AWS（ECS か、VM 1 台の compose）（[09](09-deploy.ja.md)。ターゲットごとの違いは
  [ref/deploy-targets](../../guide/ref/deploy-targets.ja.md)）。同一コアでデプロイ層だけを
  ポート&アダプタで差し替える（§1.6）。

## 1.2 用語

| 用語 | 定義 |
|------|------|
| Workspace | メンバーシップ（identity×tenant）1 つに対応する永続環境。`native` 以外のターゲットではコンテナ、`native` ではサンドボックス化したホスト上のプロセス。ホーム・暗号ストア・working copy を保持 |
| Working copy | Workspace 内の git リポジトリ（または SVN のチェックアウト）の作業ディレクトリ（`~/repos/<name>`）|
| Session | Working copy（または任意 dir）に紐づく、会話・設定・実行状態の論理単位。kind と driver を持ち、プロセスや tmux pane と 1:1 とは限らない |
| Driver | Session の制御経路。`tui` は tmux 内の CLI 画面。`managed` は Agent が構造化 API で CLI を駆動するもので、Workspace で共有するデーモン（codex・opencode）、セッションごとの子プロセス（ACP の copilot・cursor・kiro と、muse）、Agent 自身の中のコード（lcpp）のいずれか。kind ごとの対応は [04 §4.3](04-agent.ja.md)。利用者向け UI では「実行方式」の「マネージド」「ターミナル（CLI）」と呼ぶ |
| Control Plane (CP) | Workspace の外側で動く常駐バックエンド。認証・オーケストレーション・中継・永続化 |
| Workspace Agent | 各 Workspace 内の常駐プロセス。**tmux・working copy・fs・CLI エージェントを直接操作する唯一の主体** |
| Console | ブラウザで動く SPA（React+Vite）。CP が静的配信 |
| Tenant / Identity / Membership | 部署 / 人 / その結節（多対多）。Workspace は membership 単位で分離（[06](06-data.ja.md)）|
| Engine | デプロイ自身が用意するモデルサーバ（llama.cpp・ComfyUI・VOICEVOX）。Workspace からは CP を通してだけ届く |

## 1.3 3 プロセス構成（Docker が既定）

```
Browser (Console SPA: React+Vite+zustand, xterm.js + BrowserPane canvas)
   │ HTTPS / WSS
   ▼
[エッジ]  Caddy(自動TLS, compose) / Tailscale Funnel / ALB … 運用者選択（09 §9.3）
   │ 単一ホストでは CP の loopback ポートへ素通し
   ▼
Control Plane (Go 常駐, CP_ADDR 既定 :8080 / イメージは 127.0.0.1:8099 を設定)
   │  ・authGate(L1) → identity/membership 解決 → 認可
   │  ・Console(dist) 静的配信 / REST / WS / SSE
   │  ・Workspace lifecycle (Runtime アダプタ: docker | native | ecs | ecs-ec2)
   │  ・MetadataStore (SQLite 既定 | Postgres)
   │  ・内部 git プロバイダ / MCP / 監査 / egress / memo / reaper
   │  ・エンジン: カタログ・ゲートウェイ（/engine/<key>/v1/*）・ecs-ec2 では
   │    自分で買う GPU インスタンス
   │
   │  中継: REST / SSE / terminal WS / browser REST+WS / preview
   │  認証: Bearer AGENT_TOKEN（Workspace ごと、CP が起動時に注入）
   ▼
Workspace Agent (Go, 各 Workspace 内, AGENT_ADDR 既定 :7700)
   │  ・Session lifecycle / Driver 選択・復旧
   │  ・managed driver: 共有デーモン・セッションごとの子・Agent 内（§1.2）
   │  ・tmux/PTY（ターミナルで動かす kind）
   │  ・git / fs / connections（暗号ストア secrets.enc）
   │  ・チャット（headless CLI）/ transcript / usage
   │  ・BrowserManager（Chromium/CDP、Page、JPEG screencast、入力）
   │  ・/proxy/{port} … Workspace 内サービスへの preview 中継
   ▼
tmux 内または managed driver 配下の CLI エージェント + working copy（~/repos）
```

- `docker` では、コンテナを membership ごとに `af-ws-<slug>-<key>`（既定テナントは slug 無しの
  `af-ws-<key>`）と名付け、専用ネットワーク（`af-net-…`）に入れて相互到達を遮断する
  （`manager.workspaceNames`）。Agent ポートはホストの `127.0.0.1` にだけ publish し、CP からしか
  届かない。AWS のターゲットは同じ境界をセキュリティグループで引く（[07 §7.2](07-security.ja.md)）。
- **ブラウザは常に CP とだけ話す**。CP は tmux にも working copy にも直接触れず、必ず Agent 経由。
  内部 git プロバイダの bare リポジトリは CP 自身が持つ（[91](91-internal-git.ja.md)）。
- ホーム（`~`）は停止・起動・イメージ更新を越えて残る。置き場所はターゲットで違う:
  bind mount したディレクトリ（`docker` では `<WS_DATA>/…/<key>/home`）・ホストのディレクトリ
  （`native`）・EFS アクセスポイント（`ecs`）・利用者ごとの EBS ボリューム（`ecs-ec2`）。
- 任意で egress forward proxy（`AF_EGRESS_LISTEN` 既定 `:3128`）を CP のサブコマンドとして併走
  （[07 §7.8](07-security.ja.md)）。
- **エンジンは Workspace の一部ではない**。AWS では CP がオンデマンドで起動する: VOICEVOX は
  ECS サービスとして（[decisions/0070](../decisions/0070-tts-ondemand-engine.ja.md)）、`ecs-ec2` では
  llama.cpp と ComfyUI を自分で買う GPU インスタンスの上で
  （[decisions/0071](../decisions/0071-self-hosted-inference-engines.ja.md)・
  [0077](../decisions/0077-engine-boxes-bought-by-cp.ja.md)）。`docker` と `native` では、ネットワーク上で
  すでに動いているエンジンを URL で指す（[decisions/0076](../decisions/0076-external-image-engine-on-lan.ja.md)）。
  どの場合も Workspace からは CP を通してだけ届く。冷えたエンジンの起動中、ストリーミングの要求は
  ゲートウェイがハートビートを送って保持し、非ストリーミングの要求には 45 秒（借りた
  エンジンは 75 秒）で `503 engine_waking`（`Retry-After` 付き）を返す。呼び出し側はエンジンが起き続ける間に再試行する。
  読み上げは設定が `auto` なら、VOICEVOX が応答するまで Polly に切り替わる。

## 1.4 認証は 2 層（重要・混同しない）

| 層 | 対象 | 方式 | 保存先 |
|----|------|------|--------|
| **L1 Console 認証** | 誰が Console を使えるか | `AUTH=oauth`（CP 自身のログイン。Google・GitHub・任意の OIDC プロバイダ。compose と AWS のテンプレートが設定する値）/ `proxy`（外部ゲートウェイのメールヘッダを信頼）/ `dev`（固定 ID。`AUTH` 未設定時の既定で、`native` が受け付ける唯一のモード）| 署名セッション cookie（CP）|
| **L2 エージェント認証** | 各ユーザーのエージェントを誰として動かすか | 各自のプロバイダへのサインイン | Workspace 内: CLI 自身の設定、または `secrets.enc` |

L2 はユーザー本人の作業で、Console は**状態の可視化と接続 UI** を担う。詳細: L1 = [07 §7.3](07-security.ja.md)、
L2 = [08](08-integrations.ja.md)。

## 1.5 主要フロー

### ログイン（L1, AUTH=oauth）
```
Browser → CP /login → /oauth2/login → プロバイダ → /oauth2/callback
  → 入場の可否（fail-closed）: プロバイダ自身の門（GitHub は許可した organization への所属）、
    次に許可リスト（メール/ドメイン。どれも設定していなければ、既存の membership か
    テナントの auto-join ドメインだけが入れる）
  → 署名 cookie 発行 → Console
以降の全リクエスト: authGate が cookie 検証 → メールヘッダを設定（既定 X-Forwarded-Email）
  → resolveIdentity → X-AF-Tenant ヘッダ（ヘッダを付けられない所は query の tenant）の
  テナントを、membership・テナントが許可するプロバイダ・許可する接続元アドレスで検証 → ハンドラ
```

規則の全体は [07 §7.3](07-security.ja.md)。

### Workspace 起動 / アタッチ
```
Console「Start」→ CP POST /api/workspace/start
  stopped → Runtime.Start（Agent に届くようになる前に戻ることがある）
            docker: 停止中の残骸を消し、現在のイメージで run。DEK を unwrap して AF_SECRET_KEY に注入
            ecs / ecs-ec2: 利用者の ECS サービスを起こす（ecs-ec2 ではプールのスロットに
                     利用者の EBS ホームを付けて）。収束するまで状態は `starting` で、
                     Console がポーリングし続ける
  running → そのまま
→ 応答はその時点の状態を返す
```

次に Agent へ届く必要がある要求（セッションの作成・fork・再開、持ち越した回答）は、停止中の
Workspace を自分で起動し（`AF_AUTOSTART`、既定オン）、Agent の応答を最大 55 秒待つ
（`AF_AGENT_READY_WAIT_SEC`。入口の idle timeout の内側に収める）。超えたら `409 workspace_starting`
を返し、起動はそのまま続く。接続追跡で warm を保ち、アイドルが続くと reaper が停止する
（既定 2 時間、テナントごとに設定）。

### セッション作成
```
Console: New session（kind, repo/dir, model, 実行方式, 既定は新しい worktree）
  → CP /api/sessions（クォータ検証・DB ミラー。続いて上のとおり Workspace を起動して
    Agent を待つ）→ Agent /sessions
  → Agent: メタを永続化し、driver ごとに起動
      managed: kind の runtime 上で会話を開くか resume する
      tui: tmux session 内で CLI を起動し、履歴があれば resume
  → Console: managed は会話 API、tui は会話 API または /ws/terminal で操作
```

### ターミナル接続
```
Browser xterm.js ──WSS /ws/terminal?session=&tenant=──▶ CP
  → workspace running 確認（stopped/starting は 409、自動起動しない）
  → Agent /ws/pty へ Bearer 付き Dial → 双方向リレー（binary=PTY出力, text=入力/resize）
切断しても tmux は存続。再接続で同一画面に復帰。複数タブ同時アタッチ可。
```

この経路は `driver=tui` の Session だけが使う。`driver=managed` は pane を持たず、Console は
`POST /sessions/{name}/turn`・`/respond`・`/settings` と transcript API で操作する。Session の停止・
再開・archive・fork は driver 非依存の意味論を持ち、Agent が tmux または runtime handle へ振り分ける。

### リポジトリ clone
```
Console: Repos → URL 入力 → CP /api/repos → Agent: git clone
  （統一 cred helper で透過認証。GIT_TERMINAL_PROMPT=0 で fail-fast）
  → status（git status --porcelain=v2 の解析）を返して表示
```

## 1.6 ポート&アダプタ（プラットフォーム依存の差し替え点）

コア（Console / CP コアロジック / Agent / Workspace イメージ）は全ターゲット共通。
差し替わるのは CP 内の interface seam のみ。対応表と選定は [09](09-deploy.ja.md)、ターゲットごとに
できること・できないことは [ref/deploy-targets](../../guide/ref/deploy-targets.ja.md)。

| ポート | interface | 単一ホスト（`docker` が既定・`native`）| AWS（`ecs`・`ecs-ec2`）|
|--------|-----------|---------------|-----|
| Workspace の実行 | `RuntimeFactory`（`AF_RUNTIME` で選ぶ）| Docker Engine / サンドボックス化したホストのプロセス | Fargate の ECS タスク / プールの EC2 スロット上の ECS タスク |
| 永続ホーム | Runtime 内 | bind mount したディレクトリ / ホストのディレクトリ | EFS アクセスポイント / 利用者ごとの EBS ボリューム |
| L1 認証 | `AUTH` env 分岐 | `oauth` か `dev` / `dev` のみ | `oauth`（テンプレートは `dev` も受け付ける）|
| メタデータ | `Store` | SQLite（既定・pure-Go）| Postgres（RDS）|
| at-rest 鍵 | `KeyCustodian` | localCustodian（master 由来 KEK）| 同じ。KMS custodian は seam のみ（[decisions/0005](../decisions/0005-envelope-custodian.ja.md)・#969）|
| 入口/TLS | （CP 外）| Caddy / Funnel | ALB + ACM |
| エンジン | エンジン表 | ネットワーク上ですでに動いているものを URL で | CP がオンデマンドで起動（GPU のものは `ecs-ec2` のみ）|

## 1.7 できていること・いないこと

画面とエージェントごとの機能は [ref/features](../../guide/ref/features.ja.md) と
[ref/agents](../../guide/ref/agents.ja.md) にある。この表はアーキテクチャの状態。

| 領域 | 状態 |
|------|------|
| デプロイターゲット（`docker`・`native`・`ecs`・`ecs-ec2`、VM 1 台の compose）| ✅ 本番配備は `ecs-ec2` で動いている（[09](09-deploy.ja.md)）|
| マルチテナント（identity↔tenant 多対多・クォータ・監査・showback）| ✅ |
| 内部 git プロバイダ（bare + smart-HTTP + LFS）| ✅（[91](91-internal-git.ja.md)）|
| MCP（CP `/mcp` + コンテナ内 stdio サーバ）| ✅ admin の dangerous ツールは予定しない（[decisions/0006](../decisions/0006-mcp-unified.ja.md)）|
| エージェント kind: claude・codex・cursor・opencode・agy・copilot・kiro・lcpp・muse | ✅ 組み込み方は [04 §4.3](04-agent.ja.md) |
| 自前の推論エンジン | ✅ AWS ではオンデマンドで起動（GPU のものは `ecs-ec2` のみ）。`docker` / `native` ではネットワーク上ですでに動いているもの（§1.3）|
| コンテナ内ブラウザペイン | ✅（[decisions/0018](../decisions/0018-container-browser-pane.ja.md)）|
| egress 統制 | ◐ 観測・版付きの許可リストと人の承認・proxy の enforce スイッチまで。Workspace の通信を proxy に通す配線はまだ無い（[07 §7.8](07-security.ja.md)）|
| KMS custodian | 📋 seam のみ（#969）|
| Go 内部リファクタ | ✅ 完了（[decisions/0012](../decisions/0012-go-internal-refactor.ja.md)・[0067](../decisions/0067-parallel-refactor.ja.md)）。現配置は [90](90-code-map.ja.md) |
