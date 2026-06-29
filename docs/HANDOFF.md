# HANDOFF — 次セッションへの引き継ぎ

Phase 1 MVP 完了（2026-06-26, commit `dd2330e`）以降、Phase 2 完了・Phase 3 進行中。
このファイルは**現在の正しい状態を表す構造化リファレンス**。時系列の作業ログは [CHANGELOG-handoff.md](CHANGELOG-handoff.md)。
プロジェクトの背景と確定事項はメモリ（`agent-fleet-overview`）と [README](../README.md) / [docs 索引](README.md) を参照。
ドキュメントはジャンル別: 不変設計＝[reference/](reference/)、意思決定＝[decisions/](decisions/)、計画＝[roadmap](roadmap.md)、使い終わった計画＝[history/](history/)。
**まず読む順**: この HANDOFF（§1〜§3 → §6 フェーズ状況 → §6.10 機能リファレンス）→ [phase1-plan §11.10](history/phase1-plan.md#1110-実装結果と実運用の知見phase-1-完了) → [ロードマップ](roadmap.md)。

## 1. いま動いているもの（このホスト）

- **Control Plane**: `:8099` で稼働中（**React+Vite Console（`console/dist`）** + REST/WS プロキシ + Docker Runtime）。バイナリ `/tmp/af-cp`。Console の作りは **§6.10.1**。
- **形態**: **shared（`AUTH=oauth`）でライブ稼働**（2026-06-29 刷新, commit `fca6592`）。**CP が Google OAuth を内蔵**（oauth2-proxy + Caddy は廃止）。`authGate` が署名セッション cookie を検証して email を解決（§6.7）。CP は `127.0.0.1:8099` 束縛＝Funnel 経由のみ。設定は git-ignored の `deploy/local/oauth.env`（`AUTH=oauth`/`CP_ADDR=127.0.0.1:8099`/`GOOGLE_OAUTH_CLIENT_ID|SECRET`/`AF_COOKIE_SECRET`/許可リスト）。
- **Workspace コンテナ**: 運用者は `af-ws-k1-kami-gmail-com`（image `agent-fleet/workspace:dev`）。`~`= bind mount `/tmp/af-data/<user>/home`（永続・`/login` 済み）。許可ユーザー追加は `deploy/local/allowed-emails.txt` に1行（メール or `@domain`）→ ログイン毎にライブ反映、その Google ログインで `af-ws-<email>` が自動払い出し（相互不可視: 別 home/別ネットワーク/別トークン）。dev 形態に戻すには oauth.env の `AUTH` 行を外す。
- **外部アクセス**: `https://af.example.ts.net/`（**ルート配信**、旧 `/agent-fleet` プレフィクス廃止）
  （Tailscale Funnel → CP `:8099` 直結。未認証は `/login` → Google → Console）。設定と切替手順は [`docs/reference/auth.md`](reference/auth.md)。
- **イメージ**: `agent-fleet/workspace:dev`（最新, 約2.8G, 焼き込み内容は §6.10.7）。**Java は image 外**＝ホスト共有 dir `WS_DATA/shared/jvm`（Temurin 8/21/25）を `/usr/lib/jvm:ro` でマウント（§6.10.7）。

## 2. ツールチェーン / 実行の作法

- **Go**: user-local。`export PATH="$HOME/.local/go/bin:$HOME/go/bin:$PATH"`（go1.26）。
- **Node**: nvm（`~/.nvm/versions/node/v22.23.1`）。ログインシェルで有効。
- **Docker**: `k1` は `docker` グループだが**非ログインシェルでは未反映**。コマンドは `sg docker -c '...'` で実行する（または `sudo docker`）。
- **CP 起動 / 再起動（推奨）**（host で）:
  ```bash
  cd ~/workspace-private/agent-fleet
  pkill -x af-cp 2>/dev/null
  sg docker -c "cd $PWD && nohup ./deploy/local/run-dev.sh > /tmp/af-cp.log 2>&1 &"
  ```
  `run-dev.sh` は イメージ build + CP build + 起動を一括で行い、**`deploy/local/oauth.env`（git管理外）を自動 source して
  OAuth env（git プロバイダ: `GITHUB_OAUTH_CLIENT_ID`/`BITBUCKET_OAUTH_KEY`/`BITBUCKET_OAUTH_SECRET`/`PUBLIC_BASE_URL`、
  コンソール認証 `AUTH=oauth`: `GOOGLE_OAUTH_CLIENT_ID`/`GOOGLE_OAUTH_CLIENT_SECRET`/`AF_COOKIE_SECRET`/`AF_SESSION_TTL`/
  `AF_OAUTH_ALLOWED_EMAILS`/`AF_OAUTH_ALLOWED_DOMAINS`/`AF_OAUTH_ALLOWED_EMAILS_FILE`）を CP に渡す**。
  Go PATH もスクリプト内で前置するので `sg docker -c` でも動く。**この env を渡さないと Console の「OAuth 接続」が
  「未設定」になり token 貼付にフォールバック**（設定は `deploy/local/oauth.env.example` 参照）。
  - 手動で CP だけ起動する場合も OAuth を効かせるには先に `set -a; . deploy/local/oauth.env; set +a` してから `/tmp/af-cp` を起動する。
- **CP 停止**: `pkill -x af-cp`（`pkill -f /tmp/af-cp` は自分のシェルも巻き込むので使わない）。
- **反映タイミングの早見表**（変更の種類ごとにどこまで再起動が要るか）:

  | 変更したもの | 反映に必要な操作 |
  |---|---|
  | Console フロント（`console/src/**`） | ビルド（`npm --prefix console run dev`＝`vite build --watch`）→ ブラウザ**リロードのみ**。CP は `console/dist` を `no-store` 配信 |
  | CP の Go（proxy ルート追加 / `--init` / recreate 等） | **CP 再起動**（§2 の手順）。image/agent 再ビルド不要 |
  | Agent の Go / image 焼き込み（新エンドポイント・denylist・plugin seed・python/vim 等） | **image 再ビルド + Workspace を Stop→Start** |
  | Claude 設定・環境（toolchains / timezone / ui-prefs サーバー保存） | entrypoint が適用＝**Stop→Start** |
  | 共有 JVM（JDK 版変更・cacerts 修正） | `jvm.Dockerfile` 編集 → 共有 dir を `rm -rf` して**再 provision** |

  - **Stop→Start の要点**: `start()` は `docker rm -f`→新 image で `docker run`＝確実に新 image。**`docker run` は既に running だと no-op**＝`start` 単独では新 image を反映できない（必ず Stop→Start）。ホーム（`/login`・接続・repos）は永続。

## 3. ⚠️ 最重要の落とし穴（メモリ / フリート）

このホストは `tmux-claude.sh` のライブ claude フリート（現 12〜、`MEM_HIGH=1G/MEM_MAX=2G` の cgroup 上限つき）を抱え、
**ベースラインで RAM がほぼ埋まる**。重い Docker ビルド / コンテナ / `go build` を重ねると **OOM でフリート（と現セッション）が落ちる**
（実際に2回発生）。詳細はメモリ `host-oom-fleet-risk`。

- 重い作業の前に `free -h` で **available 数 GiB** を確保。足りなければフリートを縮小（`claude_AgentFleet_*` 等この会話のセッションは残す → 他を `tmux kill-session`）、後で `/tmux-claude`（冪等 resume）で復帰。
- コンテナは `--memory` を付ける（CP は `WS_MEMORY` 既定 `1g`）。
- 「検証」も負荷源（dry-run が tmux サーバを起こす等）。

## 4. リポジトリ構成（Phase 1 実装）

```
workspace/          Workspace イメージ
  Dockerfile        multi-stage(golang→node:22-slim)。claude/opencode/codex/rtk/python3/vim/git-lfs を焼き込み。
  entrypoint.sh     claude install/update → settings.json seed → opencode plugin seed → TZ → exec agent。
  jvm.Dockerfile    共有 JVM の抽出元（image 外）。
  opencode-plugin/  agent-fleet-status.js（opencode の状態通知プラグイン）。
  agent/            Workspace Agent(Go)。main/session/terminal/git/connections/claude_auth/opencode_auth/codex_auth/
                    fs/secrets/session_status/ui_prefs ほか。HTTP:/sessions・/repos・/connections・/fs・/env, WS:/ws/pty。
control-plane/      Control Plane(Go)。main/runtime/proxy/manager/store(_sqlite)/custodian/tenants/oauth_bitbucket。
  migrations/       0001_init 〜 0005_session。`//go:embed` 冪等マイグレータ。
console/            React+Vite Console（src/→console/dist）。旧 vanilla は console/legacy-phase1/。
deploy/local/run-dev.sh   dev 起動スクリプト。provision-jvm.sh 共有 JVM 展開。
docs/               reference/(設計) + decisions/(ADR) + roadmap.md + history/(済プラン) + 本書 + CHANGELOG-handoff。
phase0/             /login 検証 PoC(参考)。
```

API/契約は [06](reference/api-agent.md)・[07](reference/api-agent.md)。CP↔Agent は内部HTTP/WS（per-container `AGENT_TOKEN` で Bearer 認証、§6.8 A2）。

## 5. 検証で確定した重要事実

- **`/login` は localhost 非依存**: サブスク認証(方式A)の `redirect_uri=https://platform.claude.com/oauth/code/callback`。
  ヘッドレス/リモートで無条件に成立。コードを `platform.claude.com` で表示→ターミナルに貼り戻し。→ [02 §2.6](reference/architecture.md#26-claude-login-フロー)。
- 認証/設定は永続ホーム（`~/.claude/.credentials.json`, `settings.json`）に集約。再起動後も維持。
- 実運用で潰した点（再発防止）: base-path 相対化 / `LANG=C.UTF-8` / `skipDangerousModePermissionPrompt` seed /
  Console no-store / 端末描画(unicode11+WebGL+JetBrainsMono) / `/login` URL はヘッダ「⧉ sign-in URL」でオンデマンドCopy。
  詳細 [11 §11.10](history/phase1-plan.md#1110-実装結果と実運用の知見phase-1-完了)。

## 6. フェーズ状況

目標: オンプレ 1 台で**複数ユーザーが相互不可視**に並行利用 + アダプタ層を固める（[05](roadmap.md) / [09 §9.5](reference/portability.md#95-ローカルの-2-形態authgateway-で切替)）。

> **▶ いまの起点 = Phase 3 のプロダクト化（P3-7 以降、§6.9）。** Phase 2（MVP: 相互不可視）は完了（§6.7/§6.8）。
> **このホストは RAM 逼迫（`host-oom-fleet-risk`）→ コンテナを増やす検証は OOM 注意**（`free -h` で数 GiB 確保 / `--memory` 必須 / フリート縮小）。

Phase 2 の各項目（per-user 化 / AuthGateway / リポジトリ管理 / 認証統合 / Claude 認証表示）はすべて実装・検証済（§6.5〜§6.8）。
SSH 鍵は HTTPS トークン方式へ格下げ（任意の後付け、§6.6 Connections）。

## 6.5 Phase 2 進捗（リポジトリ管理 — 実装済）

`af-ws-<user>` 化の前に、新規コンテナ0でこのホストのRAM制約に安全な**リポジトリ管理**を先行実装。

- **モデル**: リポジトリ = `~/repos/<name>` の working copy。**フォルダ名が id**（MetadataStore はまだ無いので不要。docs [09 §9.6](reference/portability.md#96-ローカル構成compose-概略) の `repos/` 配置と一致）。
- **Agent**（`workspace/agent/git.go`）: `GET/POST /repos`・`DELETE /repos/{name}`・`GET /repos/{name}/status|branches`・`POST /repos/{name}/checkout|fetch`。
  status は `git status --porcelain=v2 --branch` を解析（branch/dirty/ahead/behind/staged/unstaged/untracked）。clone/fetch は `GIT_TERMINAL_PROMPT=0` で対話プロンプトに詰まらず fail-fast。name は `^[A-Za-z0-9][A-Za-z0-9._-]{0,59}$` で traversal 防御。
- **CP**（`control-plane/main.go`）: `/api/repos*` を追加。既存 `proxyAgentREST`（`/api` 剥がし）でそのまま Agent へ委譲。
- **Console**: サイドバーに **Repos パネル**（現状は §6.10.5）。
- **検証済**（CP経由 E2E）: list→clone(`octocat/Hello-World`)→status→branches→checkout(test)→fetch→dirty検出→409/400エラー系→delete。`docs/06 §6.4` のレスポンス形に整合。
- **clone-then-start**: `POST /sessions` が `remote_url`+`branch` を受け、clone（既存なら再利用＋checkout）→その working copy を CWD に claude 起動。
- per-user 化したら repos は各ユーザーのホーム配下に自然に分離される（Agent 契約は不変）。

## 6.6 Phase 2 進捗（Connections — WebUI 駆動の統合認証・実装済）

CP 利用者がプロバイダごとに **WebUI で認証**し、得た資格情報を**コンテナ home に保存→コンテナ内の git/claude が利用**。
ターミナル CLI 認証は不要。前段の **Google 認証（現 CP ネイティブ OAuth、旧 oauth2-proxy）は「閉鎖空間の周縁ゲート」にすぎず別レイヤ**
（funnel 配下は全リクエストが Google 認証を要するため、GitHub は **コールバック不要の Device Flow** を採用。Bitbucket は CP がコールバックを
所有し、ブラウザの CP セッション cookie で `authGate` を通過＝§6.7）。設計詳細は
`~/.claude/plans/abundant-honking-scroll.md`。参考: `../git-reader`(CodeLeaf) の HTTPS トークン束縛。
※ Claude 接続の現行方式は**続10〜12 の訂正を経た最終結論が §6.10.3** にある（ここは git/Bitbucket 中心）。

- **モデル**: 接続 = CP 利用者 × プロバイダ。真の利用者識別は AuthGateway（§6.7）。
- **git**（`connections.go`）: `PUT/DELETE /connections/git/{host}`（host∈`github.com|bitbucket.org`）。
  `~/.git-credentials` に相当する資格を upsert し clone/fetch/**push** が透過認証。
  GitHub=`x-access-token`+PAT、Bitbucket=Atlassian email+API token（CodeLeaf 準拠）。任意で `user.name/email` も設定。
  **検証済**: `git credential fill` が stored token を返す＝git が実利用することを実証。
- **git OAuth**（`git_oauth.go` / CP `oauth_bitbucket.go`）— トークン貼付の上位として OAuth を追加（Console は OAuth 主・貼付従）:
  - **GitHub = Device Flow**（`POST /connections/git/github/oauth/{start,poll}`）。`GITHUB_OAUTH_CLIENT_ID`(CP→コンテナ env 注入、Enable Device Flow 必須)。
    user_code を `github.com/login/device` で承認→poll→保存。scope `repo`、トークン実質無期限。**実承認まで検証済**。
  - **Bitbucket = Auth Code Grant**（CP ネイティブ: `GET /api/connections/git/bitbucket/oauth/start`・`GET /api/oauth/bitbucket/callback`）。
    `BITBUCKET_OAUTH_KEY/SECRET`・`PUBLIC_BASE_URL`(CP env、現在はルート `https://<host>`)。callback はブラウザの CP セッション cookie で `authGate` を通過（**除外設定不要**）。
    トークンは失効 → **git credential helper `workspace-agent bitbucket-cred`**（agent バイナリのサブコマンド）が `bitbucket.json` を読み
    refresh して `x-token-auth`+token を出力。bitbucket.org の helper は store をリセットして本 helper のみに。
    **実 OAuth 検証済（2026-06-27）**: 承認→callback→保存→`git credential fill` が token 返却→expiry 強制失効で helper 実行→**自動 refresh で expiry 更新**を確認。consumer の Callback URL 完全一致が前提。
- **CP**: `/api/connections*` を `proxyAgentREST` で委譲（**秘密は CP を保持・解釈しない**）。**Console**: Connections パネル（§6.10.6 の設定→接続）。
- **保存は暗号ストア `secrets.enc`**（AES-256-GCM、0600、§6.8 A3）。CP マスタ鍵から per-user サブ鍵を導出し起動時注入。統一 cred helper `workspace-agent cred` が都度復号して出力（平文ファイルを作らない）。起動時に旧 home 平文を自動移行。claude 自身の `/login` 資格（`.credentials.json`）は claude が書くので範囲外。

## 6.7 Phase 2 進捗（per-user Workspace 化 + AuthGateway — 実装・検証済）

CP が**利用者ごとに独立した Workspace コンテナ**を払い出す。Agent 契約（/sessions, /repos, /connections）は**完全に不変**——
CP のルーティングが user→対象コンテナを解決するだけ。

- **`manager`**（`control-plane/manager.go`）: user/membership ごとに `name=af-ws-<user>` / `home=<WS_DATA>/<user>/home` / **専用 agent ポート**（base `WS_AGENT_PORT`=7700 から順次）を払い出す。
  既存コンテナがあれば `docker inspect` で publish 済みポートを**採用**（CP 再起動耐性）。
  - **⚠️ stale 注意**: 当初（P3-1）は `forUser(user)` + in-memory map だったが、**P3-2 以降は membership 解決に再編**（`resolveIdentity`/`resolveUser`、`runtimeFor(ws, secretKey)`、CP ハンドラは `rtFor(w,r)`＝runtime.go）。`forUser`/`config.rt` は廃止。ポート/トークンは DB 永続（§6.9 P3-1）。
- **AuthGateway（user 解決）**: `AUTH` env で分岐。いずれも email を sanitize（小文字化・非英数を `-`・40字上限。例 `Alice.B@example.com`→`alice-b-example-com`）。
  - `AUTH=dev`（既定）= 固定 `DEV_USER`（既定 `dev`）。従来の dev 形態と同一挙動。
  - `AUTH=oauth`（**ライブ採用**, `control-plane/oauth_google.go`）= **CP ネイティブ Google OAuth**。`authGate` ミドルウェアが署名セッション cookie（HMAC、`AF_COOKIE_SECRET`）を検証し、**受信 `X-Forwarded-Email` を必ず削除**してから検証済み email を同ヘッダに注入（Funnel 直結の成りすまし防止）→ 以降は `resolveIdentity` 以下を proxy と共有・無改修。`/oauth2/{login,callback,logout}`・`/login` ランディング。許可リスト=`AF_OAUTH_ALLOWED_EMAILS`/`_DOMAINS`/`_FILE`（ファイルはログイン毎ライブ読込・`@domain` 行可、全空＝全拒否）。除外パス=`/oauth2/*` `/login` `/healthz` `/brand/*` `/mcp`(Bearer PAT)。Google client/secret/cookie_secret は旧 oauth2-proxy から流用可（redirect_uri `/oauth2/callback` 同一）。詳細 [`reference/auth.md`](reference/auth.md)。
  - `AUTH=proxy` = 外部ゲートウェイ（oauth2-proxy / ALB OIDC 等）の `X-Forwarded-Email`（`AUTH_EMAIL_HEADER` で変更可）を信頼。**proxy モードはヘッダ欠落＝401**（DEV_USER フォールバック廃止＝ゲート迂回封じ、§6.8 B1）。CP は loopback 束縛前提。
  - Bitbucket OAuth callback は state に user を束ねて解決（`oauth_bitbucket.go`）。GitHub Device Flow / Claude は proxyAgentREST 経由で自然に per-user。
- **ネットワーク分離（A1）**: 各コンテナを専用 network `af-net-<user>` に載せ相互到達を遮断（`control-plane/runtime.go` `ensureNetwork`）。Agent は host `127.0.0.1` publish 経由で CP からのみ到達、egress は NAT で維持。検証: 別ユーザーの Agent IP:7700 は timeout・名前解決失敗、自分の Agent は OPEN、github:443 egress OK。
- **検証済（2026-06-27）**: dev は移行後も mount/port/connections 不変＝従来と無差別。proxy で alice/bob が別コンテナ・同一メール再訪はポート安定。実コンテナ E2E で別ポート・別 home・dev 無影響→teardown。
- **注意**: RAM: per-user はコンテナを増やす → `--memory`（既定 `WS_MEMORY=1g`）必須・`free -h` 確認（`host-oom-fleet-risk`）。

## 6.8 MVP（Phase 2 完了）残チェックリスト

完了条件（[05](roadmap.md)）= 「オンプレ1台で複数ユーザーが**相互不可視**に並行利用でき、**全ポートが抽象化**される」。

**A. 相互不可視＝セキュリティ境界（MVP必須）**
- [x] **A1 コンテナ間ネットワーク分離** — `af-net-<user>`（§6.7）。実装・検証済。
- [x] **A2 CP↔Agent 認証**（[07 §7.5](reference/api-agent.md#75-control-plane-との認証)）— per-container `AGENT_TOKEN` を CP が `-e` 注入、proxy(REST/WS)+Bitbucket callback に Bearer 付与、Agent は `requireToken`（`/healthz` 除く・定数時間比較）。CP 再起動時は inspect で採用。実装・検証済（no/wrong=401, correct=200, /ws/pty も同様）。
- [x] **A3 資格情報の at-rest 暗号化** — 全資格を単一暗号ストア `secrets.enc`（AES-256-GCM）へ集約（`workspace/agent/secrets.go`）。鍵は CP 注入の per-user `AF_SECRET_KEY`（=HMAC(SHA256(`AF_MASTER_KEY`),user)、master はデータ領域外）。git は統一 helper `workspace-agent cred` が都度復号出力（平文ファイルを作らない）。起動時に旧平文を自動移行。実装・実機検証済。`AF_MASTER_KEY` 未設定の dev は `secrets.json` 平文（同一経路）。封筒化は §6.9 P3-3。

**B. shared 形態を実際に通す（MVP必須・軽い）**
- [x] **B1 `AUTH=proxy` 実機検証 + 有効化** — `GET /api/whoami`（`control-plane/runtime.go`）で実チェーンが `X-Forwarded-Email`（=k1.kami@gmail.com, sanitized `k1-kami-gmail-com`）を CP まで届けることを確認。`AUTH=proxy` を有効化し dev home を email キーへ移行、Console 実機 OK。**セキュリティ修正**: proxy モードはヘッダ欠落＝401、CP は `127.0.0.1` 束縛。`x_forwarded_user` は Google 数値 subject ID なので user キーは email を採用。
- [x] **B2 複数ユーザー許可** — oauth2-proxy `emails.txt` から **CP ネイティブ OAuth の許可リスト**へ移行（`AUTH=oauth`, 2026-06-29）。`deploy/local/allowed-emails.txt` にメール or `@domain` を1行追記で即時反映（`af-ws-<email>` 自動払い出し）。env の `AF_OAUTH_ALLOWED_EMAILS`/`_DOMAINS` も可。

**C. 運用に欲しいが MVP では妥協可**
- [ ] C1 per-user アイドル stop（RAM 逼迫ホストでは実質重要だがロードマップ上 Phase 4）。
- [ ] C2 settings.json 編集 UI / remote-control トグル（§6.10.3/§6.10.6 で実装済——ここは旧未了項目の記録）。

**D. 後回し / 格下げ済み**
- SSH 鍵 → HTTPS トークンに格下げ済（任意）。全ポートの Go interface 整形は Phase 3(AWS) 着手時。AWS アダプタ = Phase 3。

> **MVP（Phase 2 完了条件）達成**: A1・A2・B1・A3 すべて完了。相互不可視＋実ユーザー識別＋秘密の at-rest 暗号化が揃った。残 C/D は任意・後続フェーズ。

## 6.9 次フェーズ = Phase 3 プロダクト化（パッケージ配布・グループ各社セルフホスト）

社内利用 MVP は完了。次は**プロダクトのパッケージ化**。設計は [12 Phase 3](roadmap.md)。
**提供モデル確定**: 商用 SaaS も我々が運用する社内マルチテナント SaaS も断念。採用 = **プロダクトをパッケージ化し、グループ各社が「自社で」セルフホスト**（1 社=1 デプロイ）＝**ToS posture が最もクリーン**、我々は vendor/maintainer。
確定前提（2026-06-27）: **パッケージ製品・各社セルフホスト / BYO 継続 / 会社間=デプロイ分離 / デプロイ内マルチテナント=任意（既定 単一） / 小規模 / デプロイ先は各社選択（オンプレ既定・自社 AWS 任意）**。

- **構造**: 1 デプロイ = 1 社。中は `super_admin`（その社情シス）/ `Tenant`（部署, 既定 1）/ `User`。旧 `platform_admin`(=我々)は廃止。
- ✅ **P3-1 DB 化**（[13](history/p3-1-metadatastore.md)）。**SQLite 既定**の MetadataStore（pure-Go `modernc.org/sqlite`・WAL・`//go:embed` 冪等マイグレータ）。`manager.go` の in-memory map + docker-inspect 再構成 + nextPort 採番を置換し port/token を永続。既存 `af-ws-*` は inspect で採用＝**再作成しない**。CP は `AF_DB`（既定 `<WS_DATA>/control-plane.db`）。`control-plane/store.go`・`store_sqlite.go`・`migrations/0001_init.sql`。
- ✅ **P3-2 identity↔tenant 多対多**（[14](history/p3-2-identity-tenant.md)）。email で identity 特定 → 作業対象テナントは `X-AF-Tenant` を membership で検証（所属1件は自動、複数で未指定=409、非所属=403）。Workspace は membership 単位で完全分離（`af-ws-<slug>-<key>`、既定テナントは旧 `af-ws-<key>` 維持）。migration 0002（`app_user`→`identity`+`membership`、`store_sqlite.go` `migrateMemberships`）。`GET /api/tenants` + 最小 super_admin API（`POST /api/admin/tenants|memberships`、RBAC=`identity.role`）。env `AF_PROVISION`(auto|invite)/`SUPER_ADMIN_EMAILS`。Console テナントピッカー（所属1件は非表示、`window.fetch` ラップで `X-AF-Tenant` 付与、端末 WS は `&tenant=<slug>`、`rtFor` は header→query 順で解決）。
- ✅ **P3-3 封筒暗号 + custodian 抽象**（[15](history/p3-3-envelope-crypto.md)）。per-workspace DEK を per-tenant KEK で wrap し `wrapped_dek` に保存→CP が unwrap して `AF_SECRET_KEY` 注入（**Agent 無改修**）。`custodian.go` `KeyCustodian{Wrap,Unwrap}` + `localCustodian`（KEK=`HMAC(master,"af-kek:"+tenantID)`・AES-GCM・AAD=keyRef）。Vault/KMS は同 IF で後追い。migration 0003。移行: 初回 DEK=legacy `HMAC(master,userKey)` を wrap 保存＝既存 `secrets.enc` 再暗号化なし。⚠️ on-prem localCustodian は KEK が master 由来ゆえ強度は単一 master と同等（真の失効は Vault/KMS 時、[15 §15.2](history/p3-3-envelope-crypto.md#152-honest-な限界on-prem-localcustodian)）。
- ✅ **P3-4 リソースバジェット/クォータ**（[16](history/p3-4-quota.md)）。ハードクォータ（超過 `429`・**既定無制限**）。Workspace 数/テナント（`max_workspaces`）+ セッション数/ユーザー（`max_sessions`、user_limit→tenant 既定）。`tenant.limits` JSON + `user_limit` 表（migration 0004）。`PUT /api/admin/tenants/{slug}/limits`・`PUT /api/admin/user-limits`（super_admin）。**`workspace.state` DB 同期もここで配線**（Start→running/Stop→stopped）。残: ディスク強制 / per-tenant セッション合計 / mem 個別サイズ / showback（P3-9）。
- ✅ **P3-5 メンバー Console**（[17](history/p3-5-member-console.md)）。段1=git ソース管理 + shell セッション、段2=ファイルブラウザ + 機微状態の home 外退避。詳細は §6.10.5。**段2 の退避(D)**: `runtime.go` が 2nd mount `<dataDir>/claude-config:/var/lib/af/claude` ＋ `CLAUDE_CONFIG_DIR` 注入で平文 claude 状態を browse ルート外へ、`entrypoint.sh` が claude 実行前に `~/.claude` を移行。暗号化済み `secrets.enc` は home 据置＋denylist。**限界**: 同一 uid shell 完全不可視は原理的に不可（本人の BYO トークン）→ ブラウザ不可視＋at-rest 暗号＋env 注入で実用十分。
- ✅ **管理 UI（super_admin）**。store `ListTenants`/`ListMembersByTenant`、manager `workspaceStateByMembership`/`stopWorkspaceByMembership`。admin API（super_admin gate）: `GET /api/admin/tenants`・`GET /api/admin/tenants/{slug}/members`・`POST /api/admin/stop-workspace`。Console は独立モーダル `AdminDialog`（§6.10.6）。**CP のみ変更**。
- ✅ **Console 全面刷新（React+Vite）+ Claude/環境設定 + ツールチェーン共有**（[18](history/console-redesign.md) を実施）。詳細は **§6.10**。
- **次は P3-7（AWS アダプタ）/ P3-8（専用分離）/ P3-9（運用成熟: idle-stop/showback/backup/観測）/ P3-10（パッケージング）**。Console の残は §6.10.8。
- **P3-10 パッケージング**=提供モデルの核（compose/Helm + 設定 + マイグレーション + runbook、phone-home なし）。完了判定=**第2デプロイをゼロから立てて E2E 通過**。
- **MCP（P3-6 段1 完了・ライブ稼働）**: `/mcp`（Streamable HTTP）で管理面+作業面を一体公開。PAT（Console 発行・発行者 role を live 継承・scope≤role）認証。**主目的 E=手元 Claude が自分の遠隔 claude セッション群を駆動**。CP（migration 0006/PAT/`/mcp`/member-drive 4 ツール）+Agent（`/sessions/{name}/input|status|output`）+Console（設定→MCP タブ）。**ライブ E2E green**（運用者デプロイで PAT→send_to_session→status→output で遠隔 claude 駆動・2 並行も確認・revoke→401）。**現在 `AF_MCP_ENABLED=true` で稼働中**（`deploy/local/oauth.env` に設定、run-dev.sh が渡す）。⚠️ 駆動の send-keys は **pane id（%N）解決**が必須（`=session` は target-pane でリテラル化し失敗、`sessionPaneID`）。残=外部公開時に **ingress で `/mcp` を Bearer 通し**（oauth2-proxy パス除外）。次段=段2（member read 拡張+admin read）。設計 [decisions/0006](decisions/0006-mcp-unified.md)、実装プラン [history/p3-6-mcp](history/p3-6-mcp.md)。
- **⚠️ 残存リスク**: 1 デプロイ内は CP が docker.sock（=ホスト root）+ 平文 DEK 注入 → CP/ホスト侵害でそのデプロイ内分離が一括崩壊。**会社間は別デプロイゆえ波及しない**のが本モデルの強み（[12 §12.3](roadmap.md#123-tos-と分離の留意自社ホスト前提)）。
- **推奨シーケンス**: オンプレで P3-1→3→4→5/6→**P3-10(第2デプロイ検証)** → 希望社向け AWS で P3-7 → P3-8/9（[12 §12.4](roadmap.md#124-推奨シーケンス小規模local-first-継続)）。

## 6.10 機能リファレンス（テーマ別・現状）

ここは「変更のたびに追記」してきた内容を**現在の正しい状態**にテーマ別へ統合したもの（旧 続き1〜19）。
時系列の 1 行サマリは [CHANGELOG-handoff.md](CHANGELOG-handoff.md)。反映タイミングは §2 の早見表。

### 6.10.1 Console アーキテクチャ（React+Vite）

`console/` は Vite プロジェクト（`src/`、ビルド→`console/dist` を CP が `no-store` 配信）。旧 vanilla は `console/legacy-phase1/`。
`run-dev.sh` が `NODE_OPTIONS=--max-old-space-size=3072 npm run build`（mermaid で heap OOM 回避）し `CONSOLE_DIR=console/dist`。
**フロントだけの調整は `npm --prefix console run dev`（=`vite build --watch`）→ リロードで反映、CP 再起動不要**。
依存: react/react-dom・@xterm/*・highlight.js・marked・dompurify・mermaid（遅延 import チャンク）・@vscode/codicons。

- **IA**: 2 段バー（TOP=アプリ名/テナント picker/`whoami`/⚙設定/`shield`管理[super_admin]、WS=状態●/Start/Stop/更新/プレビュー。**作り直す**は設定>環境へ移設）＋ 左ペイン3セクション常駐（Sessions / Repos / Files）＋ メインが選択で切替（端末 / Source Control / ファイルビュアー）。端末は常駐（非表示でも WS 維持）。**接続中セッション/現セッションの repo はピン留め（先頭固定/バッジ/sticky）を廃止し、選択ハイライト（`.active`）のみ**（順序が入れ替わり使いづらいため）。
- **設定モーダル**（`SettingsDialog.jsx`）: セグメント（接続 / Claude / 環境 / **MCP** / 表示）。**MCP**（`TokensTab.jsx`）= MCP 用 PAT の発行/一覧/失効（token は発行時 1 回だけ表示、scope=read|write|admin:dangerous を発行者 role 以下で選択、日付は `YYYY-MM-DD`）。管理は別モーダル `AdminDialog.jsx`（super_admin のみ、TopBar の `shield`）。
- **共通資産**: `src/api.js`（`api()/rel()`/`X-AF-Tenant` 注入）・`src/term.js`（attach・コピペ）・`src/lib/settings.js`。
- **新規セッション/clone はモーダル**（`NewSessionModal.jsx`/`NewRepoModal.jsx`）。shell を左・既定（shell 時はモデル/リポジトリ/dir 非表示）。**セッション名は自動入力**（`GET /api/sessions` の既存名から衝突回避 `-2`,`-3`）。モデル選択（既定/Opus/Sonnet/Haiku → `--model`、claude のみ）。
- **CP バックエンド**: `POST /api/workspace/recreate`（`runtime.go` `handleWorkspaceRecreate`: 停止→**`home/repos` のみ破棄**→最新 image で再生成。login(別 mount)/接続(`secrets.enc`)は保持。**設定>環境の危険ゾーン**（`EnvTab` の `WorkspaceDangerZone`）が警告ダイアログ付きで呼ぶ＝WS バーからは撤去）。Agent の各 UI 系エンドポイント（`/claude/settings`・`/env/toolchains`・`/env/ui-prefs`・`/connections/...`・`/fs/...`・`/repos/.../show`）は CP が `/api/...` で proxy。
- **履歴ナビ**（History API、`pushState`＋`popstate`、戻る/進む）。スマホはドロワーから項目を開く際に `drawer:true` の履歴を差し込み、戻るでドロワーを再オープン（`pushDrawerEntry`/`navOpen`）。

### 6.10.2 セッション管理

セッションは tmux 1 本（claude / opencode / shell）。決定的 sid `sessionUUID(dir,name)`（uuidv5）。

- **メタ永続**（`session.go`）: per-session メタを `~/.config/agent-fleet/sessions`（denylist 配下・home volume）に保存＝**Stop→Start を跨いで一覧＋再開可能**。`AF_SESSIONS_DIR` で変更可。停止 TTL=`AF_SESSION_STOPPED_TTL`（既定 **7d**）。`kind`/`dir`/`model`/`repo`/`createdAt`/`stoppedAt`/`Archived` を持つ。
- **一覧**（`handleListSessions`）: メタ駆動 + live tmux マージ。終了済みは `alive:false` で残し（クリックで再開）、`stoppedAt` を刻み TTL 超過で剪定。`createdAt` 降順。Archived は除外（prune もスキップ）。
- **孤児セッションも列挙**: meta が無い生 `claude_*` tmux も一覧に追加（`Alive/Resumable=true`、kind は `paneKind()` でペイン起動コマンドから sniff）。可視化＝名前重複回避＋アーカイブ可（旧「`session already running` だが一覧に出ない」手詰まりの解消）。
- **再開**: attach は `ensureSessionTmux`→`startSessionTmux` がメタから再起動。claude/opencode は **作業 dir 消失で再開不可**（`wireSession` が `resumable=false`、home フォールバックせず、Console は打消し線+クリック無効）。shell は home フォールバック。
- **claude の resume 判定**（`jsonlResumable(sid)`）: jsonl に `"type":"user"`/`"type":"assistant"` の実会話行があるかで判定。⚠️ **RC 既定 ON では会話前でも `{"type":"bridge-session",...}` の1行だけが書かれる** → 存在＝true で `--resume` すると「No conversation found」で即終了。non-resumable なら **jsonl 削除して `--session-id`（新規）** で起動。
- **⚠️ tmux `-t` 前方一致**（重要 gotcha）: tmux の `-t <target>` は exact→prefix→fnmatch で解決＝`has-session -t claude_agent-fleet` が `claude_agent-fleet-sh` に前方一致して存在扱い（`session already running` の一因）。`kill-session`/`list-panes` も兄弟を誤 kill/誤読しうる。**修正**: `exactT(tn)="="+tn` で tmux の target 参照を全て exact 化（`has-session`/`kill-session`×3/`list-panes`）。`new-session -A -s` は別名なら新規作成（prefix attach しないと実機確認）。
- **ラベル**（`sessionLabel(dir)`）: claude `--name` = `[AF] {basename(dir)} @0102-1504`。作成時に確定し meta 保存＝再起動でも不変。`--remote-control` は token 非対応で hard-error するため不使用、`--name` は hard-error しない（RC は `remoteControlAtStartup` で別途）。
- **recreate / archive / 削除**: `POST /sessions/{name}/recreate`（過去 jsonl を捨て同一スロットで新規会話。opencode は `removeOpencodeSid`）。`POST /sessions/{name}/archive`・`/restore`・`GET /sessions/archived`（meta+jsonl 保持で非表示↔stopped 復帰）。stop（`handleStopSession`）= tmux kill + meta 破棄（jsonl は残）。
- **DB ミラー（B 案）**: CP `GET /api/sessions` は running 時 agent 取得→DB に `ReplaceSessions`、stopped/agent 不達時は DB から `alive:false` 配信（**Workspace 停止中でも一覧が見える**）。migration `0005_session.sql`。⚠️ **migrator は `;` で分割するためマイグレーションのコメントに `;` を入れない**。
- **状態バッジ**（`session_status.go`）: claude の hooks が `workspace-agent session-status <state> <sid>` を発火し `~/.config/agent-fleet/session-status/<sid>.json`＝`{state,ts}`。状態: **`working`（UserPromptSubmit）/ `idle`（Stop＝入力待ち）/ `question`（PreToolUse matcher `AskUserQuestion`）**。`PostToolUse(AskUserQuestion)`→working。`--dangerously-skip-permissions` でツール許可 QA は出ないが AskUserQuestion は別物で検出可。`ensureStatusHooks()` が加算マージ、`PreToolUse` は **matcher 単位**で rtk(`Bash`) と状態(`AskUserQuestion`) が共存（`ensurePreToolUseMatcher`/`removePreToolUseMatcher`＝RTK トグルが状態フックを壊さない）。stop/recreate でクリア。
- **Console**（`SessionsSection.jsx`）: 2 行表示（1 行目=表示名＋起動日時、2 行目=name＋kind バッジ＋状態チップ）。**4 秒ポーリングだが list を JSON 化し変化時のみ `setState`（`lastSer` ref）＝カーソルちらつき回避**。状態チップ: ● 進行中（pulse）/ ❓ 質問あり / ✓ 入力待ち / 停止中。`Stop`(working→idle) と `question` 到着で**ブラウザ通知**（`Notification`、閲覧中は抑止）。接続中セッションは **📌 ピン留め**（`position:sticky`）。ヘッダ 🧹 で停止中を一括削除、🗄 で ArchivedModal（復帰/完全削除）。
- **⚠️ 自戒**: 検証の後始末で `tmux kill-server` ＋ `rm ~/.config/agent-fleet/sessions/*.json` を実行すると**運用者の生きたセッションを巻き込む**。本番コンテナでの広域 kill/rm は避け、対象セッションのみ操作する。

### 6.10.3 claude 認証・オンボーディング

**最終結論（続10→11→12 の訂正連鎖の到達点）**: claude が「Select login method」を出す真因は**認証情報ではなく `.claude.json` の `hasCompletedOnboarding` 未設定**。`claude auth status` が `loggedIn:true`（本物 creds）でも、対話 TUI はオンボード・ウィザードを再実行し、その先頭ステップが「ログイン方式選択」なので認証済みでもログイン画面に見える。

- **trust + onboarding seed**（`claude_settings.go` `ensureFolderTrusted`）: claude セッション起動毎（create/recreate/resume）に `.claude.json` へ `projects[dir].hasTrustDialogAccepted=true` ＋ **`hasCompletedOnboarding=true`**（＋`theme` 未設定なら `dark`）を seed。`--dangerously-skip-permissions` では trust もオンボードも飛ばせないため明示 seed が必須。
- **⚠️ `CLAUDE_CONFIG_DIR` 設定下では claude は `.claude.json` を home でなく CCD（`/var/lib/af/claude`）配下で読む**（`claudeConfigDir()`＝`CLAUDE_CONFIG_DIR`→無ければ `~/.claude`。`claudeSettingsPath`・`sessionJSONLExists` 双方が準拠）。
- **認証本体**（`claude_auth.go`）: Connections「Claude 接続」= **`claude auth login --claudeai`（本物のサブスク OAuth）**。claude 自身が `.credentials.json`（**refreshToken 付き・サブスク**）を CCD に書く＝対話 TUI が認証され RC 等サブスク機能も維持。authorize URL は PTY 駆動で抽出→Console が表示→コード貼付。成功判定 `claude auth status`（JSON `loggedIn`）、切断 `claude auth logout`。`GET /connections` の `claude.connected`=`claudeLoggedIn()`。
- **過去に誤った途中経過（教訓のみ）**:
  - `setup-token` のトークンを env `CLAUDE_CODE_OAUTH_TOKEN` で注入 → これは `claude -p`（headless）専用で**対話 TUI は読まない**。
  - 合成 `.credentials.json`（refreshToken 空）→ headless は通るが**対話 TUI は拒否しログイン画面**（refresh 不可）。続11 で撤去。
  - `ANTHROPIC_AUTH_TOKEN`（env, 優先#2）→ 対話を認証できるが claude が「API Usage Billing」扱いになりサブスク（RC 等）を殺す恐れ→**不採用**。
- **⚠️ `tmux new-session -e VAR=val` はセッション環境にしか入らずペインのプロセスに伝播しない**（`/proc/<pid>/environ` に出ず `show-environment` にだけ出る）。claude の旧 env 注入は実は claude プロセスに届いていなかった（運用者は永続 creds で動いていて顕在化せず）。**env はコマンド前置**（`NAME='val' … claude/opencode`、`shellQuote`）で確実に渡す。claude は `auth login` 方式採用後に env 前置を廃止（秘密の cmdline 露出も解消）。opencode は env 注入が必要なため前置のまま（同一 uid・同一コンテナ＝本人 BYO の範囲で許容）。
- **教訓（自戒）**: claude の認証可否は `claude auth status` でも「Welcome to Claude Code」バナーでも判定できない（前者は creds ファイルのみ・後者はログイン中でも出る）。**`send-keys` で実プロンプト→応答**でのみ確証する。**auth と onboarding は別物**。

### 6.10.4 追加コーディングエージェント（opencode / codex）

claude と並ぶ session `kind`。**opencode** と **codex** が実装済み、**Antigravity** は未着手（§6.10.8）。
雛形は opencode（kind 分岐・Console 種別・denylist・即起動・認証・状態通知）。codex は状態通知が
**claude のフック方式**（opencode の plugin と違う）に乗る点が要。

- **image**: `workspace/Dockerfile` の global npm に `opencode-ai`（claude と同 RUN・root 焼き込み）。auth/セッションは `~/.local/share/opencode`（home volume・再起動跨ぎ永続）。
- **agent**（`session.go` `buildOpencodeProgram(model, envs, ocid)`）: claude 専用処理（OAuth/sid/jsonl resume/`--name`/RC URL/状態フック）は無効。model 指定時のみ `--model`（`provider/model` 形式）。
- **⚠️ per-slot 独立**: `opencode --continue` は**プロジェクト最新セッションを継続**＝同 dir 2 枚目が 1 枚目の会話を掴む。`opencode --session <id>` は任意 ID 新規不可。→ 同梱プラグインが `session.created` の `event.properties.sessionID`（`ses_…`）を捕捉し `~/.config/agent-fleet/opencode-sid/<AF_SESSION_SID>` に保存。`session.go` は保存があれば `opencode --session <id>`（そのスロット専用を再開）、無ければ素の `opencode`（TUI が初回で新規）。`readOpencodeSid`/`removeOpencodeSid`、recreate は remove で次回新規。
- **認証（設定から、claude 流）**（`opencode_auth.go`）: `secretsData.Opencode map[env]key`（`secrets.enc` 同梱・at-rest 暗号）。`PUT /connections/opencode {env,key}`（env は `^[A-Z][A-Z0-9_]+$`）・`DELETE /connections/opencode/{env}`・`GET /connections` に `opencode:{connected,envs}`。起動時に `opencodeEnv()` を**コマンド前置で注入**（auth.json 平文を書かない）。Console: 設定→接続の opencode 行（プリセット **OpenCode Zen=既定**（`OPENCODE_API_KEY`）/ Anthropic / OpenAI / OpenRouter / Google / カスタム ENV）。
- **状態**: `workspace/opencode-plugin/agent-fleet-status.js`（image 同梱→entrypoint が `~/.config/opencode/plugin/` に毎起動 seed）が `event` を購読し `message.*→working` / `session.idle→idle`（遷移時のみ）を `workspace-agent session-status <state> <sid>` に通知。sid は起動時注入の `AF_SESSION_SID`。`wireSession` の opencode 分岐が同 sid で status を読む。question 状態は無し。
- **denylist**: `fs.go` で `~/.local/share/opencode`（auth.json/db）を非表示。`~/.config/opencode`（プラグイン・設定）は非機微で表示のまま。
- **Console**: New session に opencode 種別（リポ/dir 選択、モデル選択は非表示、初回認証チップ）。Repos 行 ▶opencode 即起動（suffix `-oc`）。バッジ `hubot`。「作り直す」は per-slot 独立化で表示（実際に新会話になる）。

#### codex（OpenAI Codex CLI、`kind="codex"`）

opencode を雛形に追加。**ただし codex のフックは Claude Code とほぼ同型**（`UserPromptSubmit`/`Stop`/`PermissionRequest`、stdin に `session_id` の JSON）なので、状態通知は opencode の plugin 方式ではなく **claude のフック方式（`session_status.go`）** に乗る。実 CLI `codex-cli 0.142.3` で hooks 形式・device-auth 出力・login 経路を実検証済（認証完了は要 OpenAI 資格）。

- **image**: `workspace/Dockerfile` の global npm に `@openai/codex`（claude/opencode と同 RUN）。auth/セッションは `~/.codex`（home volume・永続）。
- **agent 起動**（`session.go` `buildCodexProgram(model, slotSid, resumeID)`）: `codex <flags>`。flags 既定 `AGENT_CODEX_FLAGS`＝`--dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust`（claude の `--dangerously-skip-permissions` 相当＝コンテナが sandbox。**bypass-hook-trust が無いと自前注入フックが発火しない**）。auth は codex 所有の `~/.codex/auth.json` ゆえ env/token 注入なし。
  - **状態フックを起動時 `-c` 注入**: `-c 'hooks.UserPromptSubmit=[{hooks=[{type="command",command="workspace-agent session-status working <slotSid> codex"}]}]'`（同様に `Stop→idle`）。**per-slot sid をコマンドに直接埋める**＝global な `~/.codex/config.toml` を触らず、env 継承前提も不要。`--dangerously-bypass-approvals-and-sandbox` 下では承認が出ないので `PermissionRequest`（question 相当）は発火せず＝working/idle のみ（opencode と同等）。
    - **⚠️ 落とし穴（resume が新規になる真因・2026-06-29 修正）**: codex のフックは **claude と同じ入れ子スキーマ**＝`hooks.<Event>=[{hooks=[{type,command}]}]`。`[{type,command}]`（フラット）は**パースは通るが発火しない**（TUI に bypass-hook-trust 警告は出るのに無音）。フラットだとフック未発火→`writeCodexSid` されず codex の session_id を捕捉できず→resume で id 無し→新規セッション化していた。入れ子に修正して発火を実機確認（hook stdin の `session_id`/`cwd`/`hook_event_name` を確認）。
  - **⚠️ codex は独自の session id を生成**（`--session-id` 相当のピン留め不可）。status は埋め込んだ slot sid で keying し、resume 用に codex 自身の `session_id` を**フックの stdin から捕捉**（`runSessionStatusHook` の `codex` マーカー分岐 → `writeCodexSid`）。`session.go` は保存があれば `codex resume <id>`、無ければ素の `codex`（新規）。recreate は `removeCodexSid` で次回新規。opencode のスロット独立と同型。
- **認証（設定から、2 経路）**（`codex_auth.go`）: env 注入は codex では効かない（`codex login status`＝Not logged in）ため**両経路とも `codex login` で auth.json を書く**。claude と同じく codex が資格ファイルを所有＝secrets.enc 対象外・denylist。
  - **API キー**: `POST /connections/codex/api-key` → `codex login --with-api-key`（鍵を stdin パイプ）。
  - **サブスク（ChatGPT）**: `POST /connections/codex/device/start` が `codex login --device-auth` を PTY 駆動し検証 URL（`https://auth.openai.com/codex/device`）+ ワンタイムコード（`XXXX-XXXXX`）をスクレイプ → Console が表示、`/device/poll` で `codex login status` を polling。codex プロセスが OpenAI 側を自前 polling（**コールバック不要**＝前段 oauth2-proxy と無干渉）。⚠️ device code ログインは ChatGPT 設定で各自/管理者の有効化が必要（openai/codex#9253）。`GET /connections` の `codex.connected`＝`codexLoggedIn()`、切断 `DELETE /connections/codex`＝`codex logout`。
- **denylist**: `fs.go` に `~/.codex`（auth.json トークン + sessions + helper bin）。
- **Console**: New session に codex 種別（モデル選択は非表示、認証案内チップ）。Repos 行 ▶codex 即起動（suffix `-cx`）。バッジ `rocket`。設定→接続に **Codex 行**（`ChatGPT で接続`＝device flow / `API キー`貼付、`ConnectionsTab.jsx` `CodexRow`）。**接続を「エージェント / git ホスティング」にカテゴリ分け**（`ConnectionsTab.jsx` `.conn-cat`）。**接続済みは認証アカウントを表示**:
- claude＝email・plan（`claudeStatus`＝`claude auth status` の `email`/`subscriptionType`）。codex＝`auth.json` の `auth_mode` + id_token claims から email・plan（例 `…@gmail.com · plus`、`codexStatus`/`codexIDTokenInfo`）。
- GitHub/Bitbucket は **ID（ハンドル）+ email** を表示。GitHub＝`/user` の `login`+`email`、Bitbucket＝`/2.0/user` の `username` + `/2.0/user/emails` の primary（`githubAccount`/`bitbucketAccount`）。**API は接続毎に1回だけ叩き store にキャッシュ**（`gitEntry.{Login,Email}`/`bitbucketCreds.{Account,Email}`、polled な `GET /connections` で都度叩かない＝`Login=="" || Email==""` で解決）。失敗時は email/プレースホルダにフォールバック。Bitbucket の実名/メール解決は token の `account`/`email` スコープが必要。
- **表示**: アイコンセット選択をフォントと同じ**折り返しチップ**（`ChipChoice`＝`.font-choices`）に（旧 1 行セグメントはスマホで見切れた）。モーダルヘッダの余白を拡げ ✕ ボタンのタップ域を確保（`.modal-head` padding + スマホは `safe-area-inset-top` でノッチ回避・40px ターゲット）。
- **CP**: `/api/connections/codex/*` を `proxyAgentREST` で委譲（device-auth はコンテナ内で OpenAI を直接 polling＝CP ネイティブ callback 不要、Bitbucket と異なる）。

### 6.10.5 git / リポジトリ / SCM / ファイルブラウザ

- **Repos パネル**（`ReposSection.jsx`）: clone URL+branch / 一覧+dirty● / **`▶ 起動 ▾`**（claude/opencode/shell ドロップダウン、ユニーク採番。**名前の右＝旧ブランチ位置**へ移動）。**fetch / 🗑削除 / ブランチ切替は右ペイン（ソース管理ヘッダ）へ移設**＝行は名前+起動のみに簡素化。**repo 名クリック = FILES ツリー展開（`revealInFiles`）＋右ペインに SCM（`showSCM`）を同時に開く**（`onOpen` 統合）。現セッションの repo は**選択ハイライトのみ**（ピン留め＝先頭固定/バッジ/sticky は廃止）。
- **ソース管理ビュー**（⎇、`SourceControlView.jsx`）: ヘッダに**ブランチ切替（`BranchModal`）/ fetch / 🗑削除**（Repos 行から移設、削除後は端末へ戻る）。変更一覧→diff・stage/unstage/discard・commit・履歴・ブランチ。**diff はファイル毎に折り畳み＋旧/新行番号ガター**（codeleaf 風、`splitDiffFiles`/`diffRows`/`FileDiff`、`diff --git` 単位分割・`@@` から行番号算出・冗長 meta 行は畳む）。スマホは `scmbody` を縦積みし変更/履歴を上部（最大38vh）＋ diff 全幅。**履歴行クリックで commit diff**（`GET /api/repos/{name}/show?sha=`、`git_view.go` `handleRepoShow`、`sha` は `shaRe` で hex 検証、`git log -1`＋`show --name-status`＋`show --format=` を `maxViewBytes` でキャップ）。
- **read/write git**（`git_view.go`）: GET changes/diff/log・POST stage/unstage/discard/commit・GET show。traversal 防御・サイズ上限。
- **リポジトリ管理**（`git.go`）: §6.5 参照（status は porcelain=v2、`GIT_TERMINAL_PROMPT=0`、name 検証）。
- **ブランチ選択**（`BranchModal.jsx`/`BranchList.jsx`/`RepoPicker.jsx`）: select→モーダル化。共通 `BranchList`（フィルタ＋**最終コミット降順**＋相対時刻＋subject）をローカル（`BranchModal`）とリモート clone/起動（`RepoPicker`）で共用。ローカル `api/repos/{name}/branches`=`for-each-ref`（name+committerdate+subject・降順・local+remote dedup）。リモート `api/connections/git/{host}/branches`=GitHub GraphQL（`refs orderBy TAG_COMMIT_DATE`）/ Bitbucket REST（`sort=-target.date`）。clone provider は Bitbucket→GitHub 順・既定 Bitbucket。軽微: Bitbucket の `default` バッジは付かない場合あり。
- **Bitbucket repo/branch 列挙**（`git_remote.go`）: 廃止 API（`?role=member`=410）を避け `GET /2.0/user/workspaces`→各 slug の repos 集約。認証は OAuth Bearer 優先・token 貼付は email:token Basic。参照 CodeLeaf `BitbucketApi.kt`。
- **clone 時 submodule**（`git.go`）: ⚠️ `--recurse-submodules` を親 clone に付けると SSH 登録 submodule で `Host key verification failed`＝**親 clone ごと失敗**。→ 親 clone から外し、clone 後に best-effort（`submodule init`→`.git/config` の SSH URL を `sshToHTTPS`（host 非依存・scp/`ssh://`）で HTTPS 書換→`update --recursive`）。private は統一 cred helper 透過・失敗は非致命。`git_submodule_test.go`。
- **ファイルブラウザ**（`fs.go`: `GET /fs/tree`・`/fs/file`、home ルート・traversal 防御・サイズ上限・バイナリ判定）。**denylist**（一覧非表示＋直アクセス 400）: `.claude`・`.claude.json`・`.config/agent-fleet`・`.ssh`・`.git-credentials`・`~/.local/share/opencode`・`~/.codex`。Console（`FilesSection.jsx`）: 遅延ツリー＋ビュアー、「すべて畳む（⊟）」、**compact folders**（単一サブフォルダの中間 dir を `a/b/c` に畳む VS Code 流）＋展開で空 dir を自動潜行。clone 後・Workspace stop/start 後に **FILES 自動更新**（`filesKey`/`bumpFiles`）。
- **ファイルビュアー**（`FileView.jsx`）: 構文ハイライト(highlight.js)・行番号・ミニマップ・**Markdown プレビュー＋Mermaid**（marked+DOMPurify）・リンク（外部=新タブ↗ / リポ内=ファイラで開く / `#anchor`=スクロール）。
- **Marp スライドプレビュー**（`MarpView.jsx`、`@marp-team/marp-core`）: frontmatter `marp: true` の `.md` で **スライド/プレビュー/ソース** の3トグル（marp 文書は既定でスライド表示。判定 `isMarpDoc()` は先頭 `---…---` の `marp:\s*true` を sniff）。marp-core を**遅延 import**（mermaid 同様メインバンドルから分離）し、出力 HTML+テーマ CSS を **Shadow DOM** に注入（marp テーマと Console スタイルの相互汚染を遮断）。1スライドずつのステッパー（◀▶・`X / N`・←/→/Space/PgUp/Dn/Home/End）＋**全画面**（Fullscreen API）。`Marp({html:false,script:false,math:false})` で安全側。⚠️**ビルド注意**: marp-core は `mathjax-full`(~43MB)/`katex` を**静的 require** するため、`math:false` でも素のままだと **Vite ビルドがミニファイ段でハング**（>9分）。`math:false` 時は実行時にこれらへ**一切アクセスしない**ことを trap-proxy で検証済み → `vite.config.js` の `resolve.alias` で `mathjax-full/*`・`katex`・`katex/package.json` を `marp-math-stub.js` に差し替えバンドル除外（ビルド 28s 復帰、marp チャンク gzip ~450KB）。
- **Git LFS**（`Dockerfile` git-lfs 3.3.0 + `git lfs install --system`）: clone/checkout で smudge 取得（HTTPS cred helper 再利用）。残ポインタは `fs.go` が検出（`lfs:true`）→ ビュアーに「LFS ポインタ」バッジ。**既存 working copy はポインタが残るので手動 `git lfs pull`**。

### 6.10.6 UI / 表示（アイコン・テーマ・コピペ・設定保存）

- **アイコンの役割分担**: **クローム＝VS Code codicon の単色**（`@vscode/codicons`、共通 `Icon.jsx`＝`<i class="codicon codicon-NAME">`、currentColor 追従・spin 対応）。**ファイル種別＝カラー SVG**（codeleaf の `assets/vscode_icons` を `console/src/assets/fileicons/` に取込、`lib/fileicons.js` の ext/name→typeKey、`FileIcon.jsx`。AI=`sparkle`/secret=`key` は codicon で強調）。
- **カラーテーマ**（`styles.css`）: `:root` dark 既定、`:root[data-theme="light"]` で override。region 用 `--topbar-bg`/`--leftpane-bg` で上部・左ペインを独立着色（view-head/modal は不影響）。`settings.js` の `theme`/`topbarColor`/`leftpaneColor`、`SURFACE_COLORS` は per-theme tint（ライトで暗色バー＝文字潰れ を回避）。`applyTheme(state)` が `<html data-theme>` と変数を設定。設定は DisplayTab。highlight.js（ファイルビュアー）は `--hl-*` 変数で**テーマ追従**（GitHub Dark/Light、`github-dark.css` 固定 import を廃止）。**既知の限界**: xterm（term.js）はライトでも暗いまま。
- **端末コピペ**（`term.js`）: フォーカス端末では Ctrl+C/V は PTY（SIGINT/`^V`）＝素通し。**コピー**=左ドラッグ選択の `mouseup` で自動コピー＋`Ctrl+Shift+C`/`Ctrl+Insert`/`⌘C`。**ペースト**=右/中クリック・`Shift+Insert`・`Ctrl+Shift+V`・`⌘V`（`clipboard.readText()`→`term.paste()`＝bracketed-paste）。Keyboard Lock の KEYS に `KeyC`/`KeyV` を追加（全画面で Ctrl+Shift+C が DevTools を開かず端末へ）。
- **端末**: フォーカス時 Keyboard Lock（⛶全画面・Chromium のみ）＋接続中 `beforeunload` ガード。
- **キーボード**: ファイルツリー ↑↓←→Enter・**Ctrl+↑↓=フォルダ移動**・**Shift+↑↓=ビュアースクロール**・**Ctrl+PgUp/Dn=セッション切替**。
- **表示設定**（`lib/settings.js`）: 端末/ビュアー別フォント（Source Code Pro/JetBrains Mono/Fira Code/IBM Plex Mono）・サイズ stepper・行番号/折返し/ミニマップ/タブ幅。**per-user でサーバー保存**（`ui_prefs.go`: `GET/PUT /env/ui-prefs`、`~/.config/agent-fleet/ui-prefs.json`(0600)＝denylist 配下・home 永続、JSON object 検証＋64KiB cap）。localStorage を即時キャッシュにしつつ `setSetting` で 600ms デバウンス PUT、boot 時 `hydrateUIPrefs()` が GET→マージ（**server が localStorage に優先**、不達は localStorage で動作）＝別ブラウザ/端末でも追従。
- **管理の分離**: 管理（super_admin）は SettingsDialog から撤去し独立モーダル `AdminDialog.jsx`（`AdminTab` 内包）に。TopBar に `shield` ボタン（super_admin のみ）。
- **スマホ対応（監視＋軽操作・2026-06-28）**: 全分岐を **`@media (max-width:760px)`** に閉じ込め、デスクトップは不変。左ペイン（Sessions/Repos/Files）を**オフキャンバスのドロワー**化（TopBar のハンバーガー `codicon-menu` で開閉、バックドロップでクローズ、項目選択で自動クローズ＝`state.jsx` の `navOpen` を `show*` に配線）。メイン全幅・**モーダル全画面**（`100vw`×`100dvh`）・リスト行 40px タッチターゲット・ドロワー頭に `safe-area-inset` 余白。**端末**: 最小コントロールキー列 `Esc/Tab/矢印/Ctrl-C/Enter`（`TermKeys.jsx`、`onMouseDown preventDefault` でフォーカスを奪わない、`sendInput` が WS 直送＝**Gboard を不要に呼ばない**）、`visualViewport` resize で `fit()`（ソフトキーボード追従）、**1 本指縦スワイプ→`scrollLines`** でスクロールバックを遡れる。範囲外（据置）: フル・タッチ端末の全キー/タッチ選択コピー、PWA/バックグラウンド push（通知は前景 `Notification` のまま）。

### 6.10.7 インフラ（image 同梱・JVM・tz・ゾンビ reap）

- **image**（`workspace/Dockerfile`、約 2.8G、multi-stage golang→node:22-slim）。焼き込み: rtk（`workspace/vendor/rtk` 静的バイナリ、git 管理外＝`run-dev.sh` がホスト `~/.local/bin/rtk` を vendor）/ claude CLI（`npm i -g @anthropic-ai/claude-code`）/ opencode-ai / codex（`@openai/codex`）/ python3 / vim / git-lfs。entrypoint は `~/.local` の claude のみ自動更新、焼いた版は固定。
  - **言語ツールチェーン**（2026-06-28 追加）: **Go**（公式 tarball を `ARG GO_VERSION`（=1.26.4、go.mod の `go` 指定と歩調を合わせる）+ アーキ検出で導入。`COPY --from` ではなくビルダーから独立。`go install` 先 / モジュールキャッシュは `~/go`＝home volume 永続。PATH に `/usr/local/go/bin`・`~/go/bin` を ENV と profile.d 双方で前置）。**C/C++ ビルド基盤** `build-essential`+`pkg-config`+`python3-dev`（cgo / node-gyp / ソースビルド wheel / Makefile が動く）。**定番ツール** jq/unzip/zip/wget/gnupg/htop、`fd-find`/`bat`（Debian 命名 `fdfind`/`batcat`→ `fd`/`bat` シンボリックリンク）。**git-delta は bookworm 非収録で除外**（必要なら GitHub の .deb）、**sudo は隔離維持で非導入**。Java は依然 image 外（共有 JVM dir マウント、下記）。
- **Dockerfile レイヤ順**（2026-06-29）: 重い・滅多に変わらない RUN（Go toolchain DL / `npm i -g` claude+opencode+codex / リネーム）を**前段**、変更頻度の高い COPY（agent binary / entrypoint / opencode-plugin / workspace-notes / vendor）を **`USER dev` 直前へ集約**。entrypoint や notes の小修正で go/npm レイヤがキャッシュ無効化されない（画像内容は不変）。
- **エージェント常時読込の利用ガイド**（2026-06-29、`workspace/workspace-notes.md` 単一ソース・英語＝やってはいけないこと/注意点）。claude=`/etc/claude-code/CLAUDE.md`（**managed policy**・毎セッション読込・除外不可、image 焼込）/ codex=`~/.codex/AGENTS.md` / opencode=`~/.config/opencode/AGENTS.md`（後二者は `entrypoint.sh` が毎起動 `cp -f` で refresh）。**CLAUDE_CONFIG_DIR がユーザーメモリ参照に追従する保証がない**ため Claude は managed policy を採用。`.dockerignore` は `**/*.md` 除外に `!workspace-notes.md` 例外。編集は notes ファイル→再ビルド→作り直しで反映。
- **settings.json は「無い時のみ」seed**（`skipDangerousModePermissionPrompt`/`remoteControlAtStartup`/`agentPushNotifEnabled` ＋ rtk フック。RC/通知は seed 既定 true）。以後は ⚙→Claude が真実（毎起動 force だと UI と喧嘩する）。`PUT /api/claude/settings {rtk:true}` で後付け補填可。
- **rtk を全 3 エージェントへ・各々 Console からトグル**（vendor 時のみ）。機構はエージェント毎に違う: **claude**＝settings.json の `PreToolUse/Bash → rtk hook claude`（透過。トグルは従来通り `PUT /claude/settings {rtk}`＝`setRTK`）。**opencode**＝`workspace/opencode-plugin/rtk.ts`（rtk 公式 `rtk init -g --opencode` を vendor。`tool.execute.before` で bash/shell を `rtk rewrite` 置換＝透過）。**codex**＝コマンド書換フックが無いため `~/.codex/AGENTS.md` にマーカ付き rtk 利用ブロックを追記（**指示ベース＝ベストエフォート**、rtk 自身の `rtk init --codex` と同方式）。opencode は `plugin/`（単数）と `plugins/`（複数）両方を読む（`opencode debug config` で実証）。
  - **codex/opencode の on/off 実体は artifact の有無**（claude のように settings.json に無い）＝ opencode は `~/.config/opencode/plugin/rtk.ts` の有無、codex は AGENTS.md の `<!-- agent-fleet:rtk -->`〜`<!-- /agent-fleet:rtk -->` ブロックの有無。entrypoint が毎起動 base を再 seed するためトグルは**永続 pref `~/.config/agent-fleet/rtk.json`**（`{codex,opencode}`・不在キー＝既定 on）に保存し、**agent が所有**: 起動時 `reconcileAgentRTK()`（`agent_rtk.go`、entrypoint の base seed 直後・`exec workspace-agent` で連続）と `GET/PUT /agents/rtk` の両方が pref→artifact を適用。rtk 不在なら強制 off（stale artifact 除去）。entrypoint は base（status `.js` / workspace-notes AGENTS.md）のみ seed、rtk artifact は触らない。
  - **Console**: 設定の「Claude」タブ→**「エージェント」タブに改称**（`AgentsTab.jsx`、`ClaudeTab.jsx` は廃止）。claude（RC/通知/RTK）・Codex（RTK＋ベストエフォート注記）・opencode（RTK）をエージェント別セクションで一覧。claude は `api/claude/settings`、codex/opencode は `api/agents/rtk` を読む。CP は両ルートを `proxyAgentREST`。
- **opencode web（ブラウザ版 opencode・トグル式）**（設計 [decisions/0007](decisions/0007-opencode-web-via-pk-webui.md)）。core opencode web はルート前提でサブパス・プレビューに乗らない（実機確認）ため、**prefix 対応の `pk-opencode-webui`（MIT/Bun/SolidJS、v0.9.2 ピン）をイメージに焼き込み**（`oven/bun` 多段 build→`/opt/opencode-web`、ランタイム bun は `npm i -g bun`＝apt 不可）、`opencode serve`(:4096) の前段に置く。**ワークスペースに 1 つ**・既定オフ。
  - **agent**（`opencode_web.go`）: 永続トグル `~/.config/agent-fleet/opencode-web.json`（`{enabled,base_prefix}`）。`opencode serve` ＋ `bun serve-ui.ts`（`PORT`:4097/`BASE_PATH={extPrefix}/ocweb/`/`API_URL`:4096）をプロセス群監視で起動・停止（片方落ちたら対で停止）。`GET/PUT /agents/opencode-web`、起動時 `reconcileOpencodeWeb()`。`/ocweb/{rest...}` を 127.0.0.1:port へ**パス保存**リバースプロキシ（httputil＝WS 可）。
  - **CP**（`ocweb.go`）: `/ocweb/{rest...}` 専用プロキシ＝`handlePreview` と違い**パス保存＋WS/SSE 対応**（httputil リバースプロキシ）。`PUT /api/agents/opencode-web` は body に `base_prefix=externalPrefix` を注入して agent へ（agent は extPrefix を知れない）。`/ocweb`→`/ocweb/` リダイレクト。
  - **Console**: 「エージェント」タブ opencode 節に Web UI トグル＋「開く ↗」（`ocwebURL()`＝`/ocweb/` を新タブ、`?tenant=` fallback）。
  - **WS バー導線**: opencode web が enabled なら WS バーに「opencode web ↗」（`ocwebURL()`＝新タブ、running 時のみ活性）。状態は `useApp()` 共有 state（`ocweb`/`refreshOcweb`、boot・start/stop で更新）で設定タブと同期。
  - **検証**: テストタグ `agent-fleet/workspace:ocweb-test` を build（exit 0）→ image 内に bun 1.3.14・opencode・`/opt/opencode-web/{serve-ui.ts,dist(30M)}`・`/opt/shared` 確認、`bun serve-ui.ts` を image 内起動して HTTP 200・`<base href="/ocweb/">`・相対アセットを確認（プレフィクス追従）。**サイズ +860MB**（:dev 3.45GB→4.31GB、主に `npm i -g bun`。`npm cache clean` で削減余地）。
  - **制約/follow-up**: extPrefix≠"" の Caddy strip デプロイ／マルチテナント時の pk-webui 内部リクエストの tenant 解決（単一テナントは rtFor 自動解決で可）は follow-up。**全チェーン実機 E2E（CP→agent→pk-webui の WS トンネル＋`opencode serve` をトグル起動）は未検証**（新 image を running workspace へ展開＝ライブ反映が要るため）。
- **Gradle 既定 seed**（2026-06-29、`entrypoint.sh`、**無い時のみ** `~/.gradle/gradle.properties`）: 共有・メモリ制約ホストでビルドが RAM を食い潰しデーモンが居座る実害（`daemon.idletimeout` 既定3h）への対策。`org.gradle.jvmargs=-Xmx768m`/`daemon.idletimeout=120000`/`parallel=false`/`workers.max=2`/`caching=true`。プロジェクト側 gradle.properties で上書き可。**Node/JS ビルドは適正ヒープがビルド依存ゆえ強制キャップは置かず**、ガイドに指針（OOM 時のみコマンド単位 `NODE_OPTIONS=--max-old-space-size`、テストランナー並列抑制、watcher 放置禁止）。
- **Java は image 外で共有**: `workspace/jvm.Dockerfile`＋`deploy/local/provision-jvm.sh`（冪等）でホスト共有 dir `WS_DATA/shared/jvm` に Temurin **8/21/25** を展開→`runtime.go` が **`WS_JVM_DIR` を `/usr/lib/jvm:ro`** でマウント。**image は JVM 外出しで 2.1G→1.0G**（その後 §言語ツールチェーン追加で約 2.8G）。**JDK 版変更**＝`jvm.Dockerfile` 編集→共有 dir を `rm -rf` 再 provision。
  - **⚠️ cacerts**: Temurin の cacerts は `/etc/ssl/certs/adoptium/cacerts`（`/usr/lib/jvm` の外）への symlink。共有 dir には `/usr/lib/jvm/.` だけ抽出するためマウント先で **dangling＝空トラストストア→SSL 失敗**。修正＝`jvm.Dockerfile` が install 後に `find /usr/lib/jvm -name cacerts -type l` を `readlink -f` の実体で置換。**運用反映には共有 JVM dir の再 provision が必要**（既存 dir があると provision はスキップ）。
- **Python 3 同梱**: `python3 python3-pip python3-venv python-is-python3`（bookworm = 3.11.2）。PEP 668 対策に `/etc/pip.conf` で `break-system-packages = true`＝`pip install --user` が永続 home `~/.local`（PATH 済）に入る。将来 pyenv で版選択は未実施。
- **node**: nvm（home volume・オンデマンド）。
- **タイムゾーン（per-user・既定 JST）**: image に `tzdata`、`entrypoint.sh` が `toolchains.json` の `timezone`（無ければ `Asia/Tokyo`）を **`export TZ`** → agent・各セッション label・shell・claude が同一ローカル時刻。`env_toolchains.go` `GET/PUT /env/toolchains` に `timezone`＋`tz_options`（IANA 名検証 `tzNameRe`）、Console 環境タブで選択。反映は Stop→Start。
- **ゾンビ reap**: コンテナ PID 1 = `workspace-agent`(Go) は zombie を reap しない → `runtime.go` の `docker run` に **`--init`**（tini を PID 1）。検証: PID 1=`/sbin/docker-init`、session create+stop 後のゾンビ 0 件。CP のみ変更。
- **claude symlink repair**（node→dev リネームの後始末）: 永続 home の `~/.local/bin/claude` が旧 `/home/node/...` を指す壊れた symlink になり「claude command missing or broken」警告。claude は `.claude.json` の `installMethod:"native"` を見て自己チェックするため除去だけでは警告が残る → `entrypoint.sh` が dangling/欠落かつ native 記録時に `/usr/local/bin/claude install` で repair（fresh home は非 native ゆえ skip し焼き込み版）。
  - **注意**: OS ユーザーは `node`→`dev`（uid/gid=1000 維持）。claude の旧 project キーは `-home-node-*`、新規は `-home-dev-*`＝**旧会話は resume 不可**（既知・許容）。

### 6.10.8 次セッションの未調査 / 任意 TODO

- ✅ **codex CLI 追加済**（`kind="codex"`、§6.10.4）。残るは認証完了後の実運用検証（status バッジ発火 / resume）＝要 OpenAI 資格。
- **Antigravity CLI をエージェント追加**（opencode/codex と同枠の kind 追加）。Antigravity（`agy`）= Google の Go 製ターミナルエージェント（2026-05 登場、Gemini CLI の後継、`antigravity.google/download`）。インストール（npm でなくバイナリ）・認証（Google ログイン vs `GEMINI_API_KEY` vs gcloud）・resume・フック仕様は一次ドキュメントが薄く**スパイク先行が必要**。env キー経路があれば opencode 型、無く Google OAuth ブラウザ必須なら funnel コールバック問題と同種の壁。
- **（任意）ライトテーマの精度向上**: xterm テーマ（term.js）とファイルビュアーの highlight.js をライト時に明色へ。danger hover 等の残ハードコード色。
- **（任意）アイコンセット拡張**: 現在 4 セット（vscode/material/devicon/seti）。選択セットのみ動的ロードでバンドル削減も可。
- **（任意）codicon 化の残り**: 設定タブ（ConnectionsTab/AdminTab/DisplayTab/EnvTab/ClaudeTab）内の絵文字・ステータス●。
- **（任意）セッション識別の完全 ID 化**: tmux 名・meta・API・Console を name でなくランダムな一意 ID で keying し、表示名は単なるラベル（重複可）に。現状は内部 ID（`sessionUUID`/opencode `ses_…`）はユニーク・**表示名がルーティングキー**。
- **（任意）opencode 状態の `question` 相当**: opencode の permission/質問イベントを拾えれば claude の ❓質問あり に相当する状態を足せる（現状 working/idle のみ）。
- **Remote Control 実機接続の確認 + カスタムセッション名 / trust**（Console 残）。

### 6.10.9 サービスプレビュー（コンテナ内 HTTP サービスを Console から開く）

ユーザーが Workspace 内で起動した HTTP サービス（Spring Boot / dev server / 任意 Web アプリ）を、**追加のホスト公開ポートもコンテナ再作成も無し**で新タブから確認する経路。設計の詳細は [reference/preview](reference/preview.md)。

- **経路**: ブラウザ `…/preview/<port>/<path>` → Funnel → **CP `GET /preview/<port>/<rest>`**（ルート配信。旧 Caddy `/agent-fleet` strip は廃止）（`control-plane/preview.go` `handlePreview`。`rtFor` で他ルートと同じ gateway identity 認証、CP↔Agent の `Bearer <AGENT_TOKEN>` 付与、`X-Forwarded-Prefix/Host/Proto` 付与）→ **Agent `/proxy/<port>/…`**（`workspace/agent/preview.go`。内部 Authorization を除去し `httputil.ReverseProxy` で転送）→ コンテナ内 `127.0.0.1:<port>`。Agent はコンテナ netns 共有ゆえ loopback で届く＝**隔離（専用ネットワーク・相互不可視）は不変**。
- **Console**（`WsBar.jsx`）: WS バー右のポート入力＋「プレビュー」。Workspace running 時のみ。新タブ遷移は `X-AF-Tenant` を運べないため `?tenant=<slug>` を付与（CP の query fallback で解決＝terminal WS と同方式）。`/preview/<port>`→`…/<port>/` への 301（`handlePreviewRedirect`）で相対資産がサブパス配下に解決。
- **アプリ側**: JSON REST / 静的配信は無設定で動く。Spring Boot のリンク/リダイレクトは `server.forward-headers-strategy=framework`（or `native`）で `X-Forwarded-Prefix` を尊重させる。
- **現状の制約**: **HTTP のみ。WebSocket / SSE 未対応**＝Vite/React の HMR は不可（WS ブリッジは次段）。ポートは手入力（listen 自動検出なし）。`forward-headers` 非対応アプリは絶対パス資産が 404 → アプリ側で base path 設定。
- **反映**: CP 経路＝**CP 再起動**、Agent `/proxy` エンドポイント＝**image 再ビルド + Stop→Start**（§2 早見表）。2026-06-28 に CP/Console/image へ反映済み。

## 7. 動作確認の最短手順

```bash
# CP が落ちていたら §2 の手順で起動
curl -s http://127.0.0.1:8099/api/workspace            # {"state":"running"|"stopped"}
# ブラウザ: https://af.example.ts.net/  (ハードリロード。未認証は /login → Google)
#   Start
#   設定→接続: [Claude 接続]→URL承認→コード貼付 / [GitHub 接続]→PAT or Device Flow / [Bitbucket]→email+token or OAuth
#   Repos: clone URL→Clone（private は上の git 接続が前提）
#   New session（shell 既定 / claude は接続済なら追加ログイン不要）
# 旧来の手動経路: 端末で claude → /login（⧉ sign-in URL でURL取得）も併用可
```

## 8. コミット規約

main 直 push 可。コミット末尾に:
```
Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
Claude-Session: <自分のセッションURL>
```
GitHub: `git@github.com:k-k1/agent-fleet.git`。
