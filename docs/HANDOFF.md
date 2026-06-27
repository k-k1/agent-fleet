# HANDOFF — 次セッションへの引き継ぎ

Phase 1 MVP 完了時点（2026-06-26, commit `dd2330e`）の運用状態・落とし穴・Phase 2 入口。
プロジェクトの背景と確定事項はメモリ（`agent-fleet-overview`）と [README](../README.md) / [docs/01〜11](.) を参照。
**まず読む順**: この HANDOFF → [11 §11.10](11-phase1-plan.md#1110-実装結果と実運用の知見phase-1-完了) → [05 ロードマップ](05-roadmap.md)。

## 1. いま動いているもの（このホスト）

- **Control Plane**: `:8099` で稼働中（**React+Vite Console（`console/dist`）** + REST/WS プロキシ + Docker Runtime）。バイナリ `/tmp/af-cp`。Console の作りは **§6.10**。
- **形態**: **shared（`AUTH=proxy`）でライブ稼働**。CP は oauth2-proxy の `X-Forwarded-Email` から user を解決（§6.7/§6.8 B1）。CP は `127.0.0.1:8099` 束縛＝Caddy 経由のみ。設定は git-ignored の `deploy/local/oauth.env`（`AUTH=proxy`/`CP_ADDR=127.0.0.1:8099`）。
- **Workspace コンテナ**: 運用者は `af-ws-k1-kami-gmail-com`（image `agent-fleet/workspace:dev`）。`~`= bind mount `/tmp/af-data/<user>/home`（永続・`/login` 済み）。許可ユーザー追加は `~/oauth2-proxy/emails.txt` に1行追記 → その Google ログインで `af-ws-<email>` が自動払い出し（相互不可視: 別 home/別ネットワーク/別トークン）。dev 形態に戻すには oauth.env の `AUTH` 行を外す。
- **外部アクセス**: `https://af.example.ts.net/agent-fleet/`
  （Tailscale Funnel → oauth2-proxy(Google) → Caddy(strip `/agent-fleet`, :8888) → CP :8099）。設定は `~/docs/funnel-auth-setup.md`。
- **イメージ**: `agent-fleet/workspace:dev`（最新, **rtk + claude CLI 焼き込み**, 約1.0G）。**Java は image 外**＝ホスト共有 dir `WS_DATA/shared/jvm`（Temurin 8/21/25）を `/usr/lib/jvm:ro` でマウント（§6.10）。`:m3`（旧, 削除可）。

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
  OAuth env（`GITHUB_OAUTH_CLIENT_ID`/`BITBUCKET_OAUTH_KEY`/`BITBUCKET_OAUTH_SECRET`/`PUBLIC_BASE_URL`）を CP に渡す**。
  Go PATH もスクリプト内で前置するので `sg docker -c` でも動く。**この env を渡さないと Console の「OAuth 接続」が
  「未設定」になり token 貼付にフォールバック**（設定は `deploy/local/oauth.env.example` 参照）。
  - 手動で CP だけ起動する場合も OAuth を効かせるには先に `set -a; . deploy/local/oauth.env; set +a` してから `/tmp/af-cp` を起動する。
- **CP 停止**: `pkill -x af-cp`（`pkill -f /tmp/af-cp` は自分のシェルも巻き込むので使わない）。
- Console は CP がディスクから配信し `Cache-Control: no-store`。**編集はリロードだけで反映**（再ビルド不要）。
- Go/Agent やイメージを変えたら: イメージ再ビルド → CP の UI で **Stop→Start**（または `docker rm -f af-ws-dev`）。ホーム(`/login`)は永続。

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
  Dockerfile        multi-stage(golang→node:22-slim)。claudeは焼かず entrypoint で起動時 install。
  entrypoint.sh     最新claude install/update → settings.json seed → exec agent。CLAUDE_INSTALL/CLAUDE_AUTO_UPDATE で制御。
  agent/            Workspace Agent(Go)。main/session/terminal/uuid/git/connections/claude_auth.go。HTTP:/sessions・/repos・/connections, WS:/ws/pty。
control-plane/      Control Plane(Go)。main(routing+no-store)/runtime(docker)/proxy(REST+WS)。
console/            最小Console(xterm.js: fit/web-links/unicode11/webgl)。index.html/app.js/style.css。
deploy/local/run-dev.sh   dev 起動スクリプト。
docs/               設計 01〜11 + 本書。
phase0/             /login 検証 PoC(参考)。
```

API/契約は [06](06-api-spec.md)・[07](07-workspace-agent.md)。CP↔Agent は今は内部HTTP/WS（dev は publish `127.0.0.1:7700`、認証なし）。

## 5. 検証で確定した重要事実

- **`/login` は localhost 非依存**: サブスク認証(方式A)の `redirect_uri=https://platform.claude.com/oauth/code/callback`。
  ヘッドレス/リモートで無条件に成立。コードを `platform.claude.com` で表示→ターミナルに貼り戻し。→ [02 §2.6](02-architecture.md#26-claude-login-フロー)。
- 認証/設定は永続ホーム（`~/.claude/.credentials.json`, `settings.json`）に集約。再起動後も維持。
- 実運用で潰した点（再発防止）: base-path 相対化 / `LANG=C.UTF-8` / `skipDangerousModePermissionPrompt` seed /
  Console no-store / 端末描画(unicode11+WebGL+JetBrainsMono) / `/login` URL はヘッダ「⧉ sign-in URL」でオンデマンドCopy。
  詳細 [11 §11.10](11-phase1-plan.md#1110-実装結果と実運用の知見phase-1-完了)。

## 6. Phase 2 でやること（次の作業）

目標: オンプレ 1 台で**複数ユーザーが相互不可視**に並行利用 + アダプタ層を固める（[05](05-roadmap.md) / [09 §9.5](09-portability.md#95-ローカルの-2-形態authgateway-で切替)）。

> **▶ 次セッションの起点 = #3 トークン暗号化 / shared 形態の実機検証。** per-user 化(#1)+AuthGateway(#2) は
> 実装・検証まで完了（§6.7）。リポジトリ管理(§6.5)・Connections/git+Claude OAuth(§6.6) も完了。**このホストは
> RAM 逼迫（`host-oom-fleet-risk`）→ コンテナを増やす検証は OOM 注意**（`free -h` で数 GiB 確保 / `--memory` 必須 / フリート縮小）。

1. ~~**per-user Workspace 化**~~ ✅ **実装・検証済**（§6.7）。`manager`（`control-plane/manager.go`）が user→`af-ws-<user>` を払い出し、home を `/tmp/af-data/<user>/home` に分離。
2. ~~**AuthGateway = oauth2-proxy**~~ ✅ **実装・検証済**（§6.7）。`AUTH=proxy` で `X-Forwarded-Email` から user 解決（既定 `AUTH=dev` は固定 `DEV_USER`）。
3. ~~**リポジトリ管理** — clone/checkout/branch/status~~ ✅ **実装済**（§6.5）。clone-then-start 込み。private は §6.6 の git コネクタで解決。
4. ~~**SSH 鍵**~~ → **HTTPS トークン方式に変更**（§6.6 Connections）。ホスト型・多人数では token/OAuth が運用素直（失効・スコープ・API 一覧）。SSH ed25519 は任意の後付けに格下げ。
5. ~~**Claude 認証状態表示**~~ ✅ **実装済**（§6.6。`GET /connections` の claude.connected = トークンファイル有無）。残: settings.json 編集 UI。
6. （任意）**claude 終了時にシェルへフォールバック** — セッション突然切断の体験を改善（session.go の tmux 起動を `claude …; exec bash -l` 等）。

## 6.5 Phase 2 進捗（リポジトリ管理 — 実装済）

`af-ws-<user>` 化の前に、新規コンテナ0でこのホストのRAM制約に安全な**リポジトリ管理**を先行実装。

- **モデル**: リポジトリ = `~/repos/<name>` の working copy。**フォルダ名が id**（MetadataStore はまだ無いので不要。docs [09 §9.6](09-portability.md#96-ローカル構成compose-概略) の `repos/` 配置と一致）。
- **Agent**（`workspace/agent/git.go`）: `GET/POST /repos`・`DELETE /repos/{name}`・`GET /repos/{name}/status|branches`・`POST /repos/{name}/checkout|fetch`。
  status は `git status --porcelain=v2 --branch` を解析（branch/dirty/ahead/behind/staged/unstaged/untracked）。clone/fetch は `GIT_TERMINAL_PROMPT=0` で対話プロンプトに詰まらず fail-fast。name は `^[A-Za-z0-9][A-Za-z0-9._-]{0,59}$` で traversal 防御。
- **CP**（`control-plane/main.go`）: `/api/repos*` を追加。既存 `proxyAgentREST`（`/api` 剥がし）でそのまま Agent へ委譲。
- **Console**: サイドバーに **Repos パネル**（clone URL+branch / 一覧+dirty●/branch切替select(遅延ロード)/fetch⤓/`▶`そのdirでsession起動/✕delete）。
- **検証済**（CP経由 E2E）: list→clone(`octocat/Hello-World`)→status→branches→checkout(test)→fetch→dirty検出→409/400エラー系→delete。`docs/06 §6.4` のレスポンス形に整合。
- **clone-then-start**（追加実装）: `POST /sessions` が `remote_url`+`branch` を受け、clone（既存なら再利用＋checkout）→その working copy を CWD に claude 起動。Console の New session フォームに clone URL/branch 欄。
- per-user 化したら repos は各ユーザーのホーム配下に自然に分離される（Agent 契約は不変）。

## 6.6 Phase 2 進捗（Connections — WebUI 駆動の統合認証・実装済）

CP 利用者がプロバイダごとに **WebUI で認証**し、得た資格情報を**コンテナ home に保存→コンテナ内の git/claude が利用**。
ターミナル CLI 認証は不要。前段の **Google oauth2-proxy は「閉鎖空間の周縁ゲート」にすぎず別レイヤ**（funnel が全リクエストを
Google 認証ゲートするため、リダイレクト型 OAuth コールバックは壁に当たる → **コールバック不要の方式**を採用）。設計詳細は
`~/.claude/plans/abundant-honking-scroll.md`。参考: `../git-reader`(CodeLeaf) の HTTPS トークン束縛。

- **モデル**: 接続 = CP 利用者 × プロバイダ。dev は単一固定ユーザー（ストアはコンテナ home そのもの）。**真の利用者識別は AuthGateway（#2）の別タスク**。
- **git**（`connections.go`）: `PUT/DELETE /connections/git/{host}`（host∈`github.com|bitbucket.org`）。
  `~/.git-credentials` に `https://user:token@host` を upsert + `credential.helper=store` を保証 → clone/fetch/**push** が透過認証。
  GitHub=`x-access-token`+PAT、Bitbucket=Atlassian email+API token（CodeLeaf 準拠）。任意で `user.name/email` も設定。
  **検証済**: `git credential fill` が stored token を返す＝git が実利用することを実証。
- **git OAuth**（`git_oauth.go` / CP `oauth_bitbucket.go`）— トークン貼付の上位として OAuth を追加（Console は OAuth 主・貼付従）:
  - **GitHub = Device Flow**（`POST /connections/git/github/oauth/{start,poll}`）。`GITHUB_OAUTH_CLIENT_ID`(CP→コンテナ env 注入、Enable Device Flow 必須)。
    user_code を `github.com/login/device` で承認→poll→`~/.git-credentials` に保存。scope `repo`、トークン実質無期限。**実承認まで検証済（github ok）**。
  - **Bitbucket = Auth Code Grant**（CP ネイティブ: `GET /api/connections/git/bitbucket/oauth/start`・`GET /api/oauth/bitbucket/callback`）。
    `BITBUCKET_OAUTH_KEY/SECRET`・`PUBLIC_BASE_URL`(CP env)。callback はブラウザの Google cookie で oauth2-proxy を素通り（**前段改変不要**）。
    トークンは失効 → **git credential helper `workspace-agent bitbucket-cred`**（agent バイナリのサブコマンド）が `bitbucket.json` を読み
    refresh して `x-token-auth`+token を出力。bitbucket.org の helper は store をリセットして本 helper のみに。
    **実 OAuth 検証済（2026-06-27）**: 承認→callback→`bitbucket.json`(0600)保存→`git credential fill` が token 返却→
    expiry 強制失効で helper 実行→**自動 refresh で expiry 更新**を確認。consumer の Callback URL 完全一致が前提。
- **Claude**（`claude_auth.go`）: `POST /connections/claude/{start,complete}`・`DELETE /connections/claude`。
  Agent が `claude setup-token` を **PTY 駆動**（広い PTY で Ink 折返し回避→ANSI 除去で authorize URL を1行抽出）。
  Console が URL 表示→ユーザーがブラウザ承認→コード貼付→**長命トークン(`sk-ant-oat…`, 1年, サブスク, 可搬)** を捕捉し
  `~/.config/agent-fleet/claude-oauth-token`(0600) に保存。`session.go` が新規 tmux セッションへ `-e CLAUDE_CODE_OAUTH_TOKEN`
  を注入 → `/login` 不要で claude 認証（コンテナ再起動不要）。`CLAUDE_CODE_OAUTH_TOKEN` は claude-code-guide で挙動確認済み。
- **CP**: `/api/connections*` を `proxyAgentREST` で委譲（**秘密は CP を保持・解釈しない**）。**Console**: Connections パネル（Claude/GitHub/Bitbucket、接続/切断/状態●）。
- **保存は暗号ストア `secrets.enc`**（AES-256-GCM、0600、§6.8 A3）。CP マスタ鍵から per-user サブ鍵を導出し起動時注入。旧 home 平文（`.git-credentials`/`bitbucket.json`/`claude-oauth-token`）は廃止し起動時に自動移行。claude 自身の `/login` 資格（`.claude/.credentials.json`）は claude が書くので範囲外。
- **実 OAuth 検証済**（2026-06-26）: ブラウザ承認→コード貼付→トークン捕捉→保存まで成功し、
  `CLAUDE_CODE_OAUTH_TOKEN=<captured> claude -p …` が応答（=`/login` 無しで認証）を確認。
  ハマり: コードと Enter を**同一書き込み**で送ると Ink が Enter を認識せず未送信になる →
  コード送信→300ms→`\r` を**別送**する必要（`claude_auth.go`）。コード誤り/失効時は
  setup-token が `OAuth error: …` を出すので `errRe` で検出し明示。トークンは `sk-ant-oat…`。
- **後続**: GitHub Device Flow（要 OAuth App、PAT 作成を不要化）/ Bitbucket OAuth / トークン暗号化 / SSH 鍵（任意）。

## 6.7 Phase 2 進捗（per-user Workspace 化 + AuthGateway — 実装・検証済）

CP が**利用者ごとに独立した Workspace コンテナ**を払い出す。単一固定 `af-ws-dev` を user キーの map 化した。
コア未着手の本丸（複数ユーザー相互不可視）が解消。Agent 契約（/sessions, /repos, /connections）は**完全に不変**——
CP のルーティングが user→対象コンテナを解決するだけ。

- **`manager`**（新規 `control-plane/manager.go`）: `map[user]*dockerRuntime`。`forUser(user)` が初回アクセスで
  `name=af-ws-<user>` / `home=<WS_DATA>/<user>/home` / **専用 agent ポート**（base `WS_AGENT_PORT`=7700 から順次）を払い出す。
  既存コンテナがあれば `docker inspect` で publish 済みポートを**採用**（CP 再起動耐性）。`dockerRuntime`（runtime.go）は不変のまま、
  manager がインスタンスを構築。`config.rt` を廃し、各ハンドラは `c.rtFor(r)`（=`mgr.forUser(mgr.resolveUser(r))`）で解決。
- **AuthGateway（user 解決）**: `resolveUser(r)` が `AUTH` env で分岐。
  - `AUTH=dev`（既定）= 固定 `DEV_USER`（既定 `dev`）。**従来の dev 形態と完全に同一挙動**（user=dev → af-ws-dev → 7700）。
  - `AUTH=proxy` = oauth2-proxy の `X-Forwarded-Email`（`AUTH_EMAIL_HEADER` で変更可）を sanitize（小文字化・非英数を `-`・40字上限。
    例 `Alice.B@example.com`→`alice-b-example-com`）。ヘッダ欠落時は `DEV_USER` にフォールバック。
  - Bitbucket OAuth callback は state に user を束ねて解決（`oauth_bitbucket.go`）。GitHub Device Flow / Claude は proxyAgentREST 経由で自然に per-user。
- **home 移行（実施済）**: 既存 dev home を `/tmp/af-data/home`→`/tmp/af-data/dev/home` へ `mv`。`/login`・Connections（github/bitbucket/claude）・repos すべて保持を確認。
- **検証済（2026-06-27）**:
  - dev（`AUTH=dev`）: 移行後も mount=`/tmp/af-data/dev/home`・port 7700・connections 3件 connected を確認。**従来と無差別**。
  - 複数ユーザー（`AUTH=proxy`, throwaway CP）: alice/bob が別コンテナ名で解決、同一メール再訪はポート安定、ヘッダ無しは dev フォールバック。
  - 実コンテナ E2E: `tester@example.com` を Start→**別ポート 7710**（dev は 7700）・**別home** `/tmp/af-data/tester-example-com/home` 作成・
    dev コンテナは無影響を確認→teardown。
- **shared 形態の有効化**: CP に `AUTH=proxy` を渡し、前段 oauth2-proxy が `X-Forwarded-Email` を注入すればよい（funnel 構成は既存）。
  `deploy/local/run-dev.sh` は既定 `AUTH=dev`。複数ユーザーで本番運用する前の残課題: **home 平文→CP マスタ鍵で暗号化**（§6.6 末尾, #3）/ コンテナ間ネットワーク分離 / アイドル stop の per-user スケジューリング。
- **ネットワーク分離（A1, 実装・検証済）**: 各コンテナを専用 network `af-net-<user>` に載せ、コンテナ間の相互到達を遮断（`control-plane/runtime.go` `ensureNetwork`）。Agent は host `127.0.0.1` publish 経由で CP からのみ到達、egress は NAT で維持。stop で空ネットワークを best-effort 削除。検証: 別ユーザーの Agent IP:7700 は timeout、名前解決も失敗、自分の Agent は OPEN、github:443 egress OK。
- **注意**: ポートは in-memory 順次採用。多数ユーザー同時 + CP 再起動が絡む極端ケースでは inspect 採用で吸収するが、停止中コンテナのポートは再起動後に再採番されうる（再 Start で publish し直すので実害は薄い）。RAM: per-user はコンテナを増やす → `--memory`（既定 `WS_MEMORY=1g`）必須・`free -h` 確認（`host-oom-fleet-risk`）。

## 6.8 MVP（Phase 2 完了）残チェックリスト

完了条件（[05](05-roadmap.md)）= 「オンプレ1台で複数ユーザーが**相互不可視**に並行利用でき、**全ポートが抽象化**される」。
コード実態に基づく棚卸し（2026-06-27 整理）。

**A. 相互不可視＝セキュリティ境界（MVP必須）**
- [x] **A1 コンテナ間ネットワーク分離** — `af-net-<user>`（§6.7 末尾）。実装・検証済。
- [x] **A2 CP↔Agent 認証**（[07 §7.5](07-workspace-agent.md#75-control-plane-との認証)）— per-container `AGENT_TOKEN` を CP が `-e` 注入、proxy(REST/WS)+Bitbucket callback に Bearer 付与、Agent は `requireToken`（`/healthz` 除く・定数時間比較）。CP 再起動時は inspect で採用。実装・検証済（no/wrong=401, correct=200, /ws/pty も同様）。
- [x] **A3 資格情報の at-rest 暗号化** — 全資格を単一暗号ストア `secrets.enc`（AES-256-GCM）へ集約（`workspace/agent/secrets.go`）。鍵は CP 注入の per-user `AF_SECRET_KEY`（=HMAC(SHA256(`AF_MASTER_KEY`),user)、master はデータ領域外）。git は統一 helper `workspace-agent cred` が都度復号出力（平文ファイルを作らない）。起動時に旧平文を自動移行。実装・実機検証済（ディスクは ciphertext のみ、`git credential fill` 復号OK、運用者コンテナ移行済）。`AF_MASTER_KEY` 未設定の dev は `secrets.json` 平文（同一経路）。

**B. shared 形態を実際に通す（MVP必須・軽い）**
- [x] **B1 `AUTH=proxy` 実機検証 + 有効化** — `GET /api/whoami`（`control-plane/runtime.go`）で実チェーンが `X-Forwarded-Email`（=k1.kami@gmail.com, sanitized `k1-kami-gmail-com`）を CP まで届けることを確認。`AUTH=proxy` を有効化し dev home を email キーへ移行、Console 実機 OK。**併せてセキュリティ修正**: proxy モードはヘッダ欠落＝401（DEV_USER フォールバック廃止＝ゲート迂回封じ）、CP は `127.0.0.1` 束縛。`x_forwarded_user` は Google 数値 subject ID なので user キーは email を採用。
- [ ] **B2 oauth2-proxy 複数ユーザー許可**（`hd`/emails 運用）— 既存資産、設定のみ。現状 `emails.txt` は運用者1名。追加は1行追記で即時反映（`af-ws-<email>` 自動払い出し）。

**C. 運用に欲しいが MVP では妥協可**
- [ ] C1 per-user アイドル stop（RAM 逼迫ホストでは実質重要だがロードマップ上 Phase 4）。
- [ ] C2 settings.json 編集 UI / remote-control トグル（Phase 2 項目、体験向上どまり）。

**D. 後回し / 格下げ済み**
- SSH 鍵 → HTTPS トークンに格下げ済（任意）。全ポートの Go interface 整形（MetadataStore/SecretStore 形式化）は Phase 3(AWS) 着手時。AWS アダプタ = Phase 3。

> **MVP（Phase 2 完了条件）達成**: A1（ネットワーク分離）・A2（CP↔Agent 認証）・B1（shared ライブ）・A3（at-rest 暗号化）すべて完了。相互不可視＋実ユーザー識別＋秘密の at-rest 暗号化が揃った。残 C/D は任意・後続フェーズ。

## 6.9 次フェーズ = Phase 3 プロダクト化（パッケージ配布・グループ各社セルフホスト）

社内利用 MVP は完了。次は**プロダクトのパッケージ化**。設計は [12 Phase 3](12-phase3-multitenant.md)。
**提供モデル確定**: 商用 SaaS（ToS グレー）も我々が運用する社内マルチテナント SaaS（別法人ホストがグレー寄り）も断念。
採用 = **プロダクトをパッケージ化し、グループ各社が「自社で」セルフホスト**（1 社=1 デプロイ）。各社が自社の社員を自社インフラでホスト＝**ToS posture が最もクリーン**、我々は vendor/maintainer。
確定前提（2026-06-27）: **パッケージ製品・各社セルフホスト / BYO 継続 / 会社間=デプロイ分離（最強）/ デプロイ内マルチテナント=任意（既定 単一） / 小規模 / デプロイ先は各社選択（オンプレ既定・自社 AWS 任意）**。

- **構造**: 1 デプロイ = 1 社。中は `super_admin`（その社情シス）/ `Tenant`（部署, 既定 1）/ `User`。旧 `platform_admin`(=我々)は廃止（我々は運用しない）。
- ✅ **P3-1 DB 化 = 実装・ライブ検証済**（[13 実装プラン](13-p3-1-plan.md)）。**SQLite 既定**の MetadataStore（pure-Go `modernc.org/sqlite`・WAL・`//go:embed` 冪等マイグレータ）を導入し、`manager.go` の in-memory map + docker-inspect 再構成 + nextPort 採番を置換。port/token を永続（再採番レース解消）、既存 `af-ws-*` は inspect で採用＝**再作成しない**。CP は `AF_DB`（既定 `<WS_DATA>/control-plane.db`）。新ファイル `control-plane/store.go`・`store_sqlite.go`・`migrations/0001_init.sql`。**S5 ライブ検証**: 現 `/tmp/af-cp` を差し替え→既定テナント+運用者をバックフィル（コンテナ ID 不変）、sessions/connections を adopted token で proxy 成功、CP 再起動で port 7700 不変、初回ログインで email 記録。残: DB の `workspace.state` は未同期（ライブ状態は docker から読む、P3-5 で `SetWorkspaceState` 配線）。
- ✅ **P3-2 = backend 実装・ライブ検証済**（[14 実装プラン](14-p3-2-plan.md)）。identity↔tenant 多対多: email で identity 特定 → 作業対象テナントは `X-AF-Tenant` を membership で検証（所属1件は自動=単一テナント無改修、複数で未指定=409、非所属=403）。Workspace は membership(=identity×tenant)単位で完全分離（`af-ws-<slug>-<key>`、既定テナントは旧 `af-ws-<key>` 維持）。migration 0002 が `app_user`→`identity`+`membership`、`workspace.user_id`→`membership_id`（`store_sqlite.go` `migrateMemberships`、ライブ DB 適用済・運用者コンテナ非再作成）。`GET /api/tenants`（Console ピッカー用）+ 最小 super_admin API（`POST /api/admin/tenants|memberships`、RBAC=`identity.role`）。env `AF_PROVISION`(auto|invite)/`SUPER_ADMIN_EMAILS`（`run-dev.sh` 受け渡し、運用者を super_admin 化済み）。secretKey は HMAC 据置（P3-3 で封筒化）。**Console テナントピッカーも実装済**（14.5）: ヘッダの picker（`GET /api/tenants`）、所属1件は自動選択して非表示、`window.fetch` ラップで全 API に `X-AF-Tenant` 付与、端末 WS は `&tenant=<slug>`（`rtFor` は header→query の順で解決）。現運用者は単一所属ゆえ picker 非表示で従来通り。
- **P3-2 完了**（backend + Console）。
- ✅ **P3-3 = 封筒暗号 + custodian 抽象 実装・ライブ検証済**（[15 実装プラン](15-p3-3-plan.md)）。単一 `AF_MASTER_KEY`→HMAC 直注入を**封筒方式**へ: per-workspace DEK を per-tenant KEK で wrap し `wrapped_dek` に保存→CP が unwrap して `AF_SECRET_KEY` 注入（**Agent 無改修**）。`custodian.go` `KeyCustodian{Wrap,Unwrap}` + `localCustodian`（KEK=`HMAC(master,"af-kek:"+tenantID)`・AES-GCM・AAD=keyRef）。Vault/KMS は同 IF で後追い＝**真の per-tenant crypto-shred の継ぎ目**。migration 0003。**移行保全**: 初回 DEK=legacy `HMAC(master,userKey)` を wrap 保存＝既存 `secrets.enc` 再暗号化なし（ライブ検証で unwrap==legacy 確認）。⚠️ on-prem localCustodian は KEK が master 由来ゆえ強度は単一 master と同等（真の失効は Vault/KMS 時、[15 §15.2](15-p3-3-plan.md#152-honest-な限界on-prem-localcustodian)）。
- ✅ **P3-4 = リソースバジェット/クォータ 実装・ライブ検証済**（[16 実装プラン](16-p3-4-plan.md)）。インフラ資源のハードクォータ（超過 `429`・**既定無制限**で limits 設定まで挙動変化なし）。**Workspace 数/テナント**（`max_workspaces`、`Workspace.Start` で docker 権威カウント）+ **セッション数/ユーザー**（`max_sessions`、`Session.Create` で Agent `/sessions` カウント、user_limit→tenant 既定）。`tenant.limits` JSON + `user_limit` 表（migration 0004）。最小 admin API: `PUT /api/admin/tenants/{slug}/limits`・`PUT /api/admin/user-limits`（super_admin）。**P3-1 で先送りの `workspace.state` DB 同期もここで配線**（Start→running/Stop→stopped）。ライブ検証で 429×2・運用者無影響・state 同期を確認。残: ディスク強制 / per-tenant セッション合計 / mem 個別サイズ / showback（P3-9）。
- ✅ **P3-5 段1 = メンバー Console（git ソース管理 + shell セッション）実装・ライブ検証済**（[17 実装プラン](17-p3-5-plan.md)）。P3-5 を**メンバー(開発者)向け Console UX** に再定義（管理 UI は別 increment）。Agent に read/write git エンドポイント（`workspace/agent/git_view.go`: GET changes/diff/log・POST stage/unstage/discard/commit、traversal 防御・サイズ上限）、`session.go` に `kind`(claude|shell、shell=`bash -l`)、CP は proxy、Console に **⎇ ソース管理パネル**（変更一覧→色付き diff・stage/unstage/discard・commit・履歴・ブランチ）＋ New session の kind セレクタ。ライブ検証: 運用者コンテナを**新イメージで再起動（connections 維持＝P3-3 封筒鍵が実再起動跨ぎで復号 OK）**、Hello-World で changes/diff/stage/commit/log・shell・配信を確認。
- ✅ **P3-5 段2 = ファイルブラウザ + 機微状態の home 外退避 実装・ライブ検証済**。Agent `fs.go`（`GET /fs/tree`・`/fs/file`、home ルート・traversal 防御・サイズ上限・バイナリ判定、**denylist** `.claude`/`.claude.json`/`.config/agent-fleet`/`.ssh`/`.git-credentials` は一覧非表示＋直アクセス 400）。退避(D): `runtime.go` が 2nd mount `<dataDir>/claude-config:/var/lib/af/claude` ＋ `CLAUDE_CONFIG_DIR` 注入で平文 claude 状態を browse ルート外へ、`entrypoint.sh` が claude 実行前に `~/.claude` を移行。暗号化済み `secrets.enc` は home 据置＋denylist。Console: 端末ヘッダ 🗂 files → エクスプローラ（遅延ツリー＋ビューア）。**限界**: 同一 uid shell 完全不可視は原理的に不可（本人の BYO トークン）→ ブラウザ不可視＋at-rest 暗号＋env 注入で実用十分。ライブ検証: 運用者再起動で connections 維持・home ツリーに claude 成果物が出ない・denylist 400 を確認。**P3-5 完了**（段1+段2）。
- ✅ **管理 UI（テナント/メンバー/クォータ/使用量、super_admin）実装・ライブ検証済**。store `ListTenants`/`ListMembersByTenant`、manager `workspaceStateByMembership`/`stopWorkspaceByMembership`。CP admin API（super_admin gate）: `GET /api/admin/tenants`（users/running/limits）・`GET /api/admin/tenants/{slug}/members`・`POST /api/admin/stop-workspace`（既存 create-tenant/add-member/set-limits 併用）。Console: ヘッダ **⚙ admin**（super_admin のみ）→ オーバーレイ（左=テナント一覧+新規、右=limits 編集・メンバー表[state/上限/強制停止]・メンバー追加）。**CP のみ変更**（Agent/イメージ再ビルド不要）。ライブ検証: list/create/add/limits/user-limit・非admin 403・カウント反映を確認。
- ✅ **Console 全面刷新（React+Vite）+ Claude/環境 設定 + ツールチェーン共有 = 実装・ライブ反映済**（[18 ハンドオフ](18-console-ui-redesign.md) を実施）。**詳細は §6.10**。React 採用（決定: vanilla→React）、`console/` は Vite プロジェクト（`src/`→`console/dist` を CP が no-store 配信、旧 vanilla は `console/legacy-phase1/`）。
- 次は **P3-7（AWS アダプタ）/ P3-8（専用分離）/ P3-9（運用成熟: idle-stop/showback/backup/観測）/ P3-10（パッケージング）**。Console の残（後述 §6.10 末尾の保留: Remote Control 実機検証 + カスタム名 / trust）。
- **バジェット**=インフラ資源（Workspace/セッション/ディスク/メモリ）。各社の自社ホスト資源保護 + 社内 showback。外部課金なし。
- **鍵**: 単一 `AF_MASTER_KEY`→HMAC を **封筒暗号 + custodian 抽象**へ昇格（**オンプレ=Vault/ファイル KEK 優先、KMS は AWS アダプタ**）。per-workspace DEK。Agent `secrets.go` 無改修。会社/部署離脱は鍵 disable で crypto-shred。
- **P3-10 パッケージング**=提供モデルの核（compose/Helm + 設定 + マイグレーション + runbook、phone-home なし）。完了判定=**第2デプロイをゼロから立てて E2E 通過**。
- **MCP**: 管理サービス層を MCP 化し、その社の運用チームが自社 Fleet を Claude で運用。
- **⚠️ 残存リスク**: 1 デプロイ内は CP が docker.sock（=ホスト root）+ 平文 DEK 注入 → CP/ホスト侵害でそのデプロイ内分離が一括崩壊。**会社間は別デプロイゆえ波及しない**のが本モデルの強み（[12 §12.3](12-phase3-multitenant.md#123-tos-と分離の留意自社ホスト前提)）。
- **推奨シーケンス**: オンプレで P3-1→3→4→5/6→**P3-10(第2デプロイ検証)** → 希望社向け AWS で P3-7 → P3-8/9（[12 §12.4](12-phase3-multitenant.md#124-推奨シーケンス小規模local-first-継続)）。

## 6.10 Console 全面刷新（React+Vite）+ Claude/環境設定 + ツールチェーン共有（このセッション）

**Console を React+Vite で全面刷新**（docs/18 を実施）。`console/` は Vite プロジェクト（`src/`、ビルド→`console/dist` を CP が `no-store` 配信）。旧 vanilla は `console/legacy-phase1/`。`run-dev.sh` が `NODE_OPTIONS=--max-old-space-size=3072 npm run build`（mermaid で heap OOM 回避）し `CONSOLE_DIR=console/dist`。**フロントだけの調整は `npm --prefix console run dev`（=`vite build --watch`）→ ブラウザ・リロードで反映、CP 再起動不要**。依存: react/react-dom・@xterm/*・highlight.js・marked・dompurify・mermaid（mermaid は遅延 import チャンク）。

**IA**: 2 段バー（TOP=アプリ名/テナント picker/`whoami`/⚙設定、WS=状態●/Start/Stop/**作り直す**）＋ 左ペイン3セクション常駐（Sessions / Repos / Files）＋ メインが選択で切替（端末 / Source Control / ファイルビュアー）。⚙設定はモーダル＋**セグメント選択（接続 / Claude / 環境 / 表示 / 管理[super_admin]）**。端末は常駐（非表示でも WS 維持）。`api()/rel()/X-AF-Tenant 注入/attach` は資産として `src/api.js`・`src/term.js` に再編。

**主な機能（フロント; 既存 API は不変が基本）**:
- 新規セッション/clone は**モーダル**。**repo/branch はドロップダウン**（接続済み GitHub のリモートリポ/ブランチを列挙、git-reader 準拠、branch は遅延）。**モデル選択**（既定/Opus/Sonnet/Haiku → `--model`）。
- Repos 各行に **▶claude / ▶shell 即起動**（`api/sessions` を見てユニーク採番＝同 repo 複製可）。
- ファイルビュアー: 構文ハイライト(highlight.js)・行番号・情報バー・**ミニマップ**・**Markdown プレビュー＋Mermaid**（marked+DOMPurify サニタイズ）・**リンク**（外部=新タブ↗ / リポ内=ファイラで開く / `#anchor`=スクロール）。
- **履歴ナビ**（History API、URL 据置の `pushState`＋`popstate` 復元、戻る/進む）。
- 表示設定（端末/ビュアー別フォント＝Source Code Pro/JetBrains Mono/Fira Code/IBM Plex Mono、サイズ stepper、行番号/折返し/ミニマップ/タブ幅、`localStorage`、`src/lib/settings.js`）。
- キーボード: ファイルツリー ↑↓←→Enter・**Ctrl+↑↓=フォルダ移動**・**Shift+↑↓=ビュアースクロール**、**Ctrl+PgUp/Dn=セッション切替**。端末はフォーカス時 **Keyboard Lock**（⛶全画面・Chromium のみ Ctrl+W 等捕捉）＋セッション接続中 **beforeunload ガード**。

**バックエンド追加（image/agent 再ビルド要、CP は無印は再起動のみ）**:
- Agent: `GET /connections/git/{host}/repos|branches`（GitHub 実装、Bitbucket は 501）／`GET-PUT /claude/settings`（`claude_settings.go`: RC/通知/RTK フック）／`GET-PUT /env/toolchains`（`env_toolchains.go`: node/java、`availableJava` は `/usr/lib/jvm` 走査）。CP は全て proxy（`/api/...`）。
- CP: `POST /api/workspace/recreate`（`runtime.go` `handleWorkspaceRecreate`: 停止→**`home/repos` のみ破棄**→最新 image で再生成。login(別 mount)/接続(`secrets.enc`)は保持。Console「作り直す」が警告ダイアログ付きで呼ぶ）。
- **image インストール**: rtk（`workspace/vendor/rtk` 静的バイナリを Dockerfile で `/usr/local/bin`、git 管理外＝`run-dev.sh` がホスト `~/.local/bin/rtk` を vendor）＋ claude CLI（`npm i -g @anthropic-ai/claude-code`）。entrypoint は `~/.local` の claude のみ自動更新・焼いた版は固定。
- **settings.json は「無い時のみ」seed**（`skipDangerousModePermissionPrompt`/`remoteControlAtStartup`/`agentPushNotifEnabled` ＋ rtk あればフック）。以後は ⚙→Claude が真実（毎起動 force だと UI と喧嘩するため）。
- **Java は image 外で共有**: `workspace/jvm.Dockerfile`＋`deploy/local/provision-jvm.sh`（冪等）でホスト共有 dir `WS_DATA/shared/jvm` に Temurin **8/21/25** を1回展開→ `runtime.go` が **`WS_JVM_DIR` を `/usr/lib/jvm:ro`** で各コンテナにマウント。**image は 2.1G→1.0G**。node は **nvm**（home volume・オンデマンド）。**JDK 版変更**＝`jvm.Dockerfile` 編集→共有 dir を `rm -rf` 再 provision。

**反映タイミング**: Claude設定/環境(toolchains) は entrypoint が適用＝**Stop→Start で反映**。Console フロントのみの変更はリロード即時。

**⚠️ 未解決/保留**:
- **セッション表示名 `[AF] {repo} @MMDD-HHMM` 実装済**（2026-06-27）: `claude --remote-control` の hard-error を避け、**`-n/--name`**（RC picker＋端末タイトルの表示名、RC は `remoteControlAtStartup` で別途有効）で実装。`session.go` `sessionLabel(dir)`＝`[AF] {basename(dir)} @{time "0102-1504"}`（{repo}=作業 dir ベース名、時分）。作成時に確定し meta に保存＝**再起動でも不変**。claude セッションのみ付与。RC 実機接続は要確認だが `--name` は token 非対応でも hard-error しない（検証済）。
- **タイムゾーン = per-user・既定 JST 実装済**（2026-06-27）: image に `tzdata`、`entrypoint.sh` が `toolchains.json` の `timezone`（無ければ `Asia/Tokyo`）を **`export TZ`** → agent・各セッション label・shell・claude が同一ローカル時刻。Agent `GET/PUT /env/toolchains` に `timezone`＋`tz_options`（IANA 名検証 `tzNameRe`）、Console **環境タブにタイムゾーン選択**。反映は Stop→Start。**検証**: 既定で label が JST（UTC 12:48→`@…-2148`）、agent PID env `TZ=Asia/Tokyo`。
- ~~**ディレクトリ trust の無条件化は非対話の手段が無い**~~ → **解決済（2026-06-27）**: claude の「Is this a project you trust?」ダイアログは **`--dangerously-skip-permissions` でも飛ばせない**ため、未 trust の dir（全 repo、`node→dev` リネーム後の `/home/dev`）で**対話セッションが毎回ここで停止**していた（「破棄後に再開できない」の真因＝sid ではない）。`agent` が起動前に **`$CLAUDE_CONFIG_DIR/.claude.json` の `projects[dir].hasTrustDialogAccepted=true` を seed**（`ensureFolderTrusted`、`claude_settings.go`）→ 起動 dir を自動 trust。**⚠️ `CLAUDE_CONFIG_DIR` 設定下では claude は `.claude.json` を home でなく CCD 配下で読む**（fs.go の旧コメントは誤り）。`startSessionTmux` の claude 分岐で `ensureFolderTrusted(cwd)` を呼ぶ（create/recreate/resume 全経路）。検証: `/home/dev` 新規/recreate/resume すべて trust プロンプト無しで入力到達。

**適用手順（運用者）**: WS BAR の **Stop→Start**（スリム image＋JVM マウント＋新 agent。**repos/home 保持**。「作り直す」は repos も消す）→ ⚙設定 → 環境/Claude で選択 → 再度 Stop→Start で有効化（java は即・node は初回 nvm DL）。

**🔧 修正（2026-06-27）— セッションが即死するリグレッション**:
- **症状**: 端末セッションを開始しても claude が即終了（ペイン空→attach の `tmux new-session -A` が bash で作り直し→ `[claude] <defunct>` ゾンビ蓄積）。
- **原因**: P3-5 段2 で claude 状態を `CLAUDE_CONFIG_DIR=/var/lib/af/claude` へ退避したが、`session.go` の `sessionJSONLExists` は依然 `~/.claude/projects` を走査。claude は jsonl を `$CLAUDE_CONFIG_DIR/projects` に書くため**同一 (dir,name) の2回目以降に jsonl を見落とし** → `--resume` ではなく `--session-id <既存ID>` を渡し、claude が `Session ID is already in use` で即終了。
- **修正**: `claude_settings.go` に `claudeConfigDir()`（`CLAUDE_CONFIG_DIR`→無ければ `~/.claude`）を切り出し、`claudeSettingsPath` と `sessionJSONLExists` の双方を準拠化（`workspace/agent/session.go`/`claude_settings.go`）。image 再ビルド＋運用者コンテナ Stop→Start 済み。**検証**: 既存 sid の repo セッションが `--resume` で生存（`cmd=claude dead=0`/`/rc active`）、接続3件は実再起動跨ぎで復号 OK。
- **既定 ON 化**: RC/通知は seed 既定で true（運用者も true）。**RTK は焼き込み済 rtk(`/usr/local/bin/rtk` v0.40.0) があるのに運用者の settings.json が rtk 導入前 seed でフック欠落** → `PUT /api/claude/settings {rtk:true}` で補填（settings.json に `rtk hook claude` の PreToolUse/Bash 追加）。新規コンテナは seed が3種とも付与。
- **副次（解決済）**: コンテナ PID 1 = `workspace-agent`(Go) は zombie を reap せず、セッション停止毎に tmux/claude のゾンビが残っていた → `control-plane/runtime.go` の `docker run` に **`--init`** を追加（tini を PID 1 に注入＝orphan を reap）。CP 再ビルド＋運用者コンテナ Stop→Start で適用。**検証**: PID 1=`/sbin/docker-init`、session create+stop 後のゾンビ 0件。CP のみ変更（image/agent 再ビルド不要）。

**🔧 改善（2026-06-27 続き）— セッション再起動 / モーダル / ユーザー名**:
- **claude 終了後のセッション復帰**: claude を端末で終了すると tmux セッションが破棄され、左ペインで再クリックすると `new-session -A` が**素の bash で作り直し**ていた。`session.go` に **per-session メタ永続**（`/tmp/af-sessions/<name>.json` = kind/dir/model、`AF_SESSIONS_DIR` で変更可、ライフサイクルは tmux と一致＝コンテナ再起動でクリア）を導入。`handleListSessions` は live tmux と**メタをマージ**し、終了済みは `alive:false` で**一覧に残す**。`terminal.go` の attach は `ensureSessionTmux` で**メタから claude を再起動**（決定的 sid=`uuidv5(dir|name)` が同一＝jsonl があれば `--resume` で会話継続）。`Session` に `kind` を追加（一覧の sh/ai タグが正しく出る）。stop はメタ削除＋終了済みも 200。**検証**: 作成→kill→一覧に `alive:false` 残存→WS 再接続で `cmd=claude`（bash でなく）復帰を確認。
- **新規セッションモーダル刷新**（`console/src/components/NewSessionModal.jsx`）: 種類は **shell を左・既定**に。**shell 時はモデル/リポジトリ/ディレクトリを非表示**（repo 不要・dir 不要）。**セッション名は自動入力**（`GET /api/sessions` で既存名を取得し `shell`/`claude`/repo 名 → 衝突時 `-2`,`-3`、ユーザー編集で固定）。claude 時のみ従来のモデル＋リポジトリ（接続/URL/なし）を表示。
- **コンテナ OS ユーザー `node`→`dev`**（Node.js と紛らわしいため）: `workspace/Dockerfile` で `usermod -l dev -d /home/dev -m node && groupmod -n dev node`（**uid/gid=1000 維持**＝host uid 1000 と一致）、`control-plane/runtime.go` の home マウント先を **`/home/dev`** に。`$HOME=/home/dev`・repos/connections は保持（secrets 鍵は email 由来でユーザー名非依存）。**注意**: claude の旧 project は `-home-node-*` キーで、新規は `-home-dev-*`＝**旧会話は resume 不可**（既知・許容）。**image/CP 再ビルド＋ Stop→Start で適用済**。

**🔧 改善（2026-06-27 続き2）— セッション一覧の刷新（× 廃止 / 停止状態 / 再開 / 2 行表示）**:
- **× ボタン廃止**: 「同一 repo で複数セッション時に × で全終了」との指摘（実機ではバックエンドの stop は分離＝再現せずだが UI として撤廃）。**停止は端末で claude/shell を終了**＝そのセッションが `停止中` 表示になり、クリックで**再開**（`ensureSessionTmux`＝同一 sid で `--resume`）。
- **停止状態の保持＋自動失効**: `sessionMeta` に `repo`/`createdAt`/`stoppedAt` を追加。`handleListSessions` を**メタ駆動**に変更（live tmux で `stoppedAt` クリア、終了済みは初回観測時に `stoppedAt` を刻み、**TTL 超過で剪定**）。TTL=`AF_SESSION_STOPPED_TTL`（既定 24h、`/tmp` ゆえコンテナ再起動でも消える）。一覧は `createdAt` 降順。
- **2 行表示**（`console/src/components/sections/SessionsSection.jsx`）: 1 行目=**表示名 `[AF|SH] {repo}` ＋ 起動日時**（`started`="MM/DD HH:mm" ローカル時刻）、2 行目=**セッション名 ＋ kind ＋ 状態（● 起動中 / 停止中—クリックで再開）**。**4 秒ポーリング**で 起動中⇄停止中 と TTL 剪定を自動反映。`Session` に `repo`/`label`/`started`/`createdAt` を追加。CSS は `styles.css` の `.session-*`。
- **検証**: claude×2＋shell を同一 dir に作成→1 つ kill で当該のみ `alive:false`・他は生存、WS 再接続で `cmd=claude` 復帰、`stoppedAt` を古く偽装→**剪定**を確認。

**🔧 改善（2026-06-27 続き3）— セッション永続（Stop→Start 跨ぎ）/ DB ミラー / セッション recreate**:
- **メタを永続 home へ**: `sessionsMetaDir` を `/tmp/af-sessions` →**`~/.config/agent-fleet/sessions`**（denylist 済）に変更。home は Stop→Start で残るので**停止セッションが Workspace 再起動を跨いで一覧＋再開可能**（jsonl も別マウントで残る）。TTL 既定 **24h→7d**（`AF_SESSION_STOPPED_TTL`）。`startSessionTmux` は dir 消失時に cwd を home フォールバック（sid は元 dir 由来で不変）。
- **DB ミラー（B 案）**: agent が一次、CP が `GET /api/sessions` で **running 時は agent 取得→DB に `ReplaceSessions`**、**stopped/agent 不達時は DB から `alive:false` で配信**。これで**Workspace 停止中でも一覧が見える**（コンテナ横断ビューの素地）。migration `0005_session.sql`（`session` 表、PK=workspace_id+name）、`store.ReplaceSessions/ListSessions`、`runtime.handleSessionsList`/`manager.agentSessions`。**⚠️ migration はコメント文中の `;` で壊れる**（マイグレータは `;` 分割）＝コメントに `;` を入れない。
- **セッション recreate（≠ 再開）**: 過去 jsonl を捨てて**同一スロットで新規会話**。Agent `POST /sessions/{name}/recreate`（kill tmux→`jsonlPaths(sid)` 削除→meta の createdAt/label 刷新・stoppedAt クリア→`startSessionTmux`＝jsonl 無で `--session-id` の新規）。CP は proxy。Console は各行の **↻ ボタン**（確認ダイアログ＝会話破棄）。
- **検証**: Stop→Start 後も停止セッション表示＋WS で `cmd=claude` 復帰、**Workspace 停止中に DB から一覧配信**、recreate 後に `--session-id`（新規会話）で claude 生存。**注意**: `docker run` は既に running だと **no-op**＝新イメージ反映には **Stop→Start**（start 単独は不可）。

## 7. 動作確認の最短手順

```bash
# CP が落ちていたら 2. の手順で起動
curl -s http://127.0.0.1:8099/api/workspace            # {"state":"running"|"stopped"}
# ブラウザ: https://af.example.ts.net/agent-fleet/  (ハードリロード)
#   Start
#   Connections: [Claude 接続]→URL承認→コード貼付 / [GitHub 接続]→PAT / [Bitbucket]→email+token
#   Repos: clone URL→Clone（private は上の git 接続が前提）
#   New(name=main, dir空 または clone URL 指定) → 端末で claude（接続済なら /login 不要）
# 旧来の手動経路: 端末で claude → /login（⧉ sign-in URL でURL取得）も併用可
```

## 8. コミット規約

main 直 push 可。コミット末尾に:
```
Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
Claude-Session: <自分のセッションURL>
```
GitHub: `git@github.com:k-k1/agent-fleet.git`。
