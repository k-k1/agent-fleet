# HANDOFF — 次セッションへの引き継ぎ

Phase 1 MVP 完了時点（2026-06-26, commit `dd2330e`）の運用状態・落とし穴・Phase 2 入口。
プロジェクトの背景と確定事項はメモリ（`agent-fleet-overview`）と [README](../README.md) / [docs/01〜11](.) を参照。
**まず読む順**: この HANDOFF → [11 §11.10](11-phase1-plan.md#1110-実装結果と実運用の知見phase-1-完了) → [05 ロードマップ](05-roadmap.md)。

## 1. いま動いているもの（このホスト）

- **Control Plane**: `:8099` で稼働中（静的 Console + REST/WS プロキシ + Docker Runtime）。バイナリ `/tmp/af-cp`。
- **Workspace コンテナ**: `af-ws-dev`（image `agent-fleet/workspace:dev`, 498MB）。`~`= bind mount `/tmp/af-data/home`（永続。`/login` 済み）。
- **外部アクセス**: `https://af.example.ts.net/agent-fleet/`
  （Tailscale Funnel → oauth2-proxy(Google) → Caddy(strip `/agent-fleet`, :8888) → CP :8099）。設定は `~/docs/funnel-auth-setup.md`。
- **イメージ**: `agent-fleet/workspace:dev`（最新）/ `:m3`（旧, 削除可）。

## 2. ツールチェーン / 実行の作法

- **Go**: user-local。`export PATH="$HOME/.local/go/bin:$HOME/go/bin:$PATH"`（go1.26）。
- **Node**: nvm（`~/.nvm/versions/node/v22.23.1`）。ログインシェルで有効。
- **Docker**: `k1` は `docker` グループだが**非ログインシェルでは未反映**。コマンドは `sg docker -c '...'` で実行する（または `sudo docker`）。
- **CP 起動**（host で）:
  ```bash
  cd ~/workspace-private/agent-fleet
  ( cd control-plane && PATH="$HOME/.local/go/bin:$PATH" go build -o /tmp/af-cp . )
  sg docker -c 'CP_ADDR=:8099 WS_IMAGE=agent-fleet/workspace:dev \
    CONSOLE_DIR=$PWD/console WS_DATA=/tmp/af-data /tmp/af-cp'
  # もしくは: sg docker -c "$PWD/deploy/local/run-dev.sh"   # イメージbuild+CP起動
  ```
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

1. **per-user Workspace 化** — 今は単一 `af-ws-dev`。CP が user→container を払い出し（`af-ws-<user>`）、
   ホームを `/tmp/af-data/<user>/home` に分離。Runtime/Volume/AuthGateway を [09 §9.3](09-portability.md#93-ポート定義go-インターフェース概略) のインターフェースに整理。
2. **AuthGateway = oauth2-proxy** — CP は `X-Forwarded-...`/oauth2-proxy のヘッダ（認証済みメール）から user を解決（dev 固定IDを置換）。
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
- **保存は home 平文**（0600、`.credentials.json` と同レベル＝コンテナ home が隔離境界）。**shared 形態では CP マスタ鍵で暗号化が必要（別タスク）**。
- **実 OAuth 検証済**（2026-06-26）: ブラウザ承認→コード貼付→トークン捕捉→保存まで成功し、
  `CLAUDE_CODE_OAUTH_TOKEN=<captured> claude -p …` が応答（=`/login` 無しで認証）を確認。
  ハマり: コードと Enter を**同一書き込み**で送ると Ink が Enter を認識せず未送信になる →
  コード送信→300ms→`\r` を**別送**する必要（`claude_auth.go`）。コード誤り/失効時は
  setup-token が `OAuth error: …` を出すので `errRe` で検出し明示。トークンは `sk-ant-oat…`。
- **後続**: GitHub Device Flow（要 OAuth App、PAT 作成を不要化）/ Bitbucket OAuth / トークン暗号化 / SSH 鍵（任意）。

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
