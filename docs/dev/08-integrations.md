# 08. 外部システム連携

> 正: コード（本書は方式と設計意図の地図）/ 主な更新トリガ: プロバイダ・認証方式の追加/変更 / 最終確認: 2026-07

外部プロバイダとの連携を 1 本に集約する。横断で効く共通パターンは 2 つ:
**(a) コールバック不要方式**（device flow / コード貼り戻し——エッジの認証ゲートと無干渉で成立）と
**(b) CP 所有コールバック方式**（CP が公開 URL を持つ必要がある）。新プロバイダを検討するときは
まず (a) が使えないかを探すのが本リポジトリの定石。

## 8.1 連携一覧

| 相手 | 用途 | 方式 | コールバック | 資格の保存先 |
|------|------|------|--------------|--------------|
| Google | L1 Console ログイン | OAuth Auth Code（CP ネイティブ）| CP `/oauth2/callback` | 署名 cookie（資格は保存しない）|
| GitHub | git 認証 | PAT 貼付 / **Device Flow（CP 実行・テナントのアプリ）** | 不要 | `secrets.enc` |
| Bitbucket | git 認証 | email+API token 貼付 / **OAuth Auth Code（CP 所有 callback・テナントのアプリ）** | CP `/api/oauth/bitbucket/callback` | `secrets.enc`（refresh は専用 cred helper）|
| 内部 git | git ホスティング | per-membership HMAC トークン（Basic）| — | 非保存（都度導出、[91](91-internal-git.md)）|
| Anthropic / claude.ai | claude 認証（L2）| `claude auth login --claudeai`（コード貼り戻し）| 不要 | `CLAUDE_CONFIG_DIR/.credentials.json`（claude 所有）|
| OpenAI | codex 認証 | API キー / **ChatGPT Device Flow** | 不要 | `~/.codex/auth.json`（codex 所有）|
| 各 LLM プロバイダ | opencode 認証 | env キー（プリセット: OpenCode Zen 既定 / Anthropic / OpenAI / OpenRouter / Google / カスタム）| 不要 | `secrets.enc` |
| 外部 Claude クライアント | MCP（遠隔操作）| Bearer PAT | — | PAT はハッシュのみ DB（[06](06-data-model.md)）|
| AWS | ECS/EFS ランタイム 🚧・SSM ログイン | SDK / SSO device flow | 不要 | SSM の短命 cred はコンテナ内キャッシュ（CP 非到達）|
| Tailscale Funnel / Caddy | 入口（TLS 終端）| —（コード外のインフラ）| — | —（[09 §9.3](09-deploy.md)）|

Connections の設計原則: **秘密は CP を素通りするだけで保持・解釈しない**（Agent の `secrets.enc` に
集約、[07 §7.6](07-security.md)）。接続状態は `GET /api/connections` に集約し、アカウント表示用の
プロバイダ API は**接続毎に 1 回だけ**叩いてキャッシュ（ポーリングで都度叩かない）。

## 8.2 Google OAuth（L1）

CP ネイティブ実装。フロー・許可リスト・authGate の防御は [07 §7.3](07-security.md) が正。
連携として押さえる点: 必要 env は `GOOGLE_OAUTH_CLIENT_ID/SECRET`・`PUBLIC_BASE_URL`・
`AF_COOKIE_SECRET`、リダイレクト URI は `<PUBLIC_BASE_URL>/oauth2/callback`（Google Cloud Console に
完全一致で登録）。

## 8.3 GitHub

- **PAT 貼付**: `PUT /api/connections/git/github.com`。`x-access-token` + PAT を cred helper 経由で供給。
- **Device Flow**（OAuth 上位経路・Console は OAuth 主/貼付従）: `POST …/github/oauth/{start,poll}`。
  `GITHUB_OAUTH_CLIENT_ID` のみ必要（client secret 不要・**アプリ設定で Enable Device Flow が前提**）。
  user_code を `github.com/login/device` で承認 → poll → 保存。scope `repo`。
  コールバック不要＝どんなエッジ構成でも成立する。
- リモート列挙（clone 用の repo/branch 一覧）は GraphQL（branch は commit 日降順）。

### gh（GitHub CLI）透過認証

`gh` は git の `credential.helper` を参照せず `GH_TOKEN`/`GITHUB_TOKEN` か `~/.config/gh`
しか見ないため、放置すると Connections でトークンを保存済みでも `gh auth login` が別途必要
になる。これを避けるため、イメージは `/usr/local/bin/gh` を薄いラッパー
（`workspace/gh-auth-wrapper.sh`）にし、実体を `/usr/local/libexec/gh` へ退避している。
ラッパーは呼び出しのたびに git と同一のヘルパー（`workspace-agent cred`）から
`git credential fill` でトークンを取り出し `GH_TOKEN` に注入してから実 gh を exec する。
これで全ユーザーが `gh auth login` なしに gh を使える（git と同じ鮮度で、失効/ローテーション
にも都度取得で自己修復する）。

**注意点 / 制約:**

- **スコープ**: 供給されるのは Device Flow で得た scope `repo` のトークン。`gh pr`/`gh issue`/
  `gh api` の大半は動くが、org 系（`gh api /orgs/...` 等）は `read:org` が無く失敗し得る。
  必要なら connection 側でスコープを追加する。
- **GitHub Enterprise 非対応**: ラッパーは github.com のトークンのみ注入する。GHE を使う
  場合は利用者が従来どおり `GH_HOST`/`GH_ENTERPRISE_TOKEN` 等を自分で設定する。
- **明示トークン優先**: 既に `GH_TOKEN`/`GITHUB_TOKEN` が環境にあればラッパーは上書きしない。
- **コスト**: gh 呼び出しごとに `workspace-agent cred` を1回叩く（git の push/fetch と同等の負荷）。
- **home shadow**: home volume に実体の `~/.local/bin/gh` があると PATH 先頭で焼き込み
  ラッパーを隠す。entrypoint が起動時にシンボリックリンク以外の `~/.local/bin/gh` を除去して
  ラッパーへ PATH を通す（標準イメージは `~/.local/bin` に gh を置かない）。

## 8.4 Bitbucket

- **貼付**: Atlassian の email + API token（Basic）。
- **OAuth（Auth Code Grant）**: 唯一の CP 所有コールバック。`GET /api/connections/git/bitbucket/oauth/start`
  → 承認 → `GET /api/oauth/bitbucket/callback`（state に user を束ねて解決。ブラウザの CP セッション
  cookie で authGate を通過するため**除外設定不要**）→ token を Agent に渡して保存。
  consumer の key/secret は**テナントの行**から読む（[71](../log/71-tenant-git-oauth.md)）。
  `PUBLIC_BASE_URL` は残る（consumer の Callback URL は完全一致が前提）。
  ★ state に **tenant_id** を載せる。コールバックは bitbucket.org からの素のリダイレクトで
  `X-AF-Tenant` を持たないので、そこで解決し直すと別テナントのアプリで code を交換しうる。
- **refresh**: Bitbucket の access token は失効するため、git cred helper（`workspace-agent
  bitbucket-cred`）が保存済み refresh token で自動更新して `x-token-auth`+token を出力。
- リモート列挙は `GET /2.0/user/workspaces` → 各 workspace の repos 集約（`?role=member` は廃止 API・410）。

## 8.4.1 OAuth アプリの持ち主は**テナント**（docs/71 + ADR0052）

GitHub / Bitbucket の OAuth アプリは `tenant_git_oauth` の行で、テナント管理者が
Console（テナント設定 › 連携 › git プロバイダ OAuth）で登録する。**env は読まない**——
`GITHUB_OAUTH_CLIENT_ID` は以降 GitHub **サインイン**専用で、`BITBUCKET_OAUTH_KEY/SECRET` は
どこからも参照されない。

- **GitHub の device flow は CP が回す**（`oauth_github_device.go`）。以前は Agent が
  コンテナ env の client_id で回していたが、env はコンテナ起動時に固まりランタイム毎に
  実装が 4 つあるため、per-tenant 化すると**反映に全員のワークスペース再起動**が要る。
  パス（`POST /api/connections/git/github/oauth/{start,poll}`）は据え置きで、取得した
  token は Agent の `PUT /connections/git/github.com`（PAT 貼付と同じ入口）へ渡す。
- **メンバー向けの可否は `GET /api/git-oauth`**（CP ネイティブ）。`GET /api/connections` は
  Agent へのプロキシで停止中は 502 を返すので、ボタンの出し分けには使えない。
- **Bitbucket の refresh も CP が回す**（`POST /internal/git-oauth/bitbucket/refresh`・
  `git_oauth_bridge.go`）。以前は key/secret を Agent へ渡して自前で refresh させており、
  **テナントの client_secret が全メンバーの `secrets.enc` に複製**されていた。今は Agent が
  refresh token を送り、CP が secret を足して bitbucket.org を叩く。
  ★ **refresh token は動かさない**——ワークスペースに残り CP は保存しない（「CP は秘密を
  素通しさせるだけで保持しない」を保つ）。既存ストアの key/secret は**ブリッジが一度
  成功した時点で破棄**し、それまでは失敗時のフォールバックとして残す。
  ★ ブリッジの座標は env でなく `secrets.Data.GitOAuthBridge` に置く——cred helper は
  git が起動する別プロセスで、その環境変数は保証できない（`seedInternalGit` と同じ理由）。

## 8.5 Claude 認証・オンボーディング（L2 の本丸）

**採用方式**: Connections「Claude 接続」= `claude auth login --claudeai`（本物のサブスク OAuth）。
Agent が PTY 駆動で authorize URL を抽出 → Console が表示 → ユーザーが自分のブラウザで承認 →
表示されたコードを貼付 → claude 自身が `.credentials.json`（refreshToken 付き）を `CLAUDE_CONFIG_DIR`
に書く。成功判定は `claude auth status`、切断は `claude auth logout`。端末内で手動 `/login` する
旧経路も併用可。

- **検証で確定した土台**: サブスク認証の `redirect_uri` は `https://platform.claude.com/oauth/code/callback`
  （コード表示方式）＝ **localhost コールバックに一切依存しない**。ヘッドレス/リモートで無条件に成立。
- **「Select login method が出る」の真因は認証ではなくオンボーディング**: `claude auth status` が
  loggedIn でも、`.claude.json` の `hasCompletedOnboarding` が無いと対話 TUI はウィザードを再実行し、
  先頭がログイン方式選択なので「未認証に見える」。→ セッション起動毎に
  `hasTrustDialogAccepted` + `hasCompletedOnboarding` を seed する（`--dangerously-skip-permissions`
  でも trust/onboarding は飛ばせない）。⚠️ `CLAUDE_CONFIG_DIR` 設定下では `.claude.json` も
  その配下を読む——home 側を書いても効かない。
- **教訓（過去に誤った経路・再採用しない）**:
  1. `setup-token` を `CLAUDE_CODE_OAUTH_TOKEN` で注入 → headless 専用で**対話 TUI は読まない**。
  2. 合成 `.credentials.json`（refreshToken 空）→ 対話 TUI は拒否（refresh 不可）。
  3. `ANTHROPIC_AUTH_TOKEN` → 認証は通るが「API Usage Billing」扱いになりサブスク機能（RC 等）を殺す恐れ。
  - 判定の教訓: 認証可否は `claude auth status` でもバナーでも確証できない。**実プロンプト→応答**でのみ確証。
    **auth と onboarding は別物**（[decisions/0002](../decisions/0002-claude-auth-onboarding.md)）。

## 8.6 codex / opencode

- **codex**: env 注入は効かない（`codex login status` が Not logged in のまま）ため、両経路とも
  `codex login` で auth.json を書かせる。①API キー（stdin パイプ）②ChatGPT Device Flow
  （`codex login --device-auth` を PTY 駆動して検証 URL + ワンタイムコードをスクレイプ→Console 表示→
  poll。codex プロセスが OpenAI 側を自前ポーリング＝コールバック不要）。
  ⚠️ device code ログインは ChatGPT 組織設定で有効化が必要。接続済み表示は auth.json の
  auth_mode + id_token claims から email・plan を解決。
- **opencode**: `PUT /api/connections/opencode {env,key}`（env 名は `^[A-Z][A-Z0-9_]+$`）で
  `secrets.enc` に保存し、セッション起動時にコマンド前置で注入（auth.json 平文を作らない）。
  スロット独立・状態通知は [04 §4.3](04-workspace-agent.md)。

## 8.7 MCP（対外契約のみ・実装は 03/04）

| 面 | 入口 | 認証 | スコープ |
|----|------|------|----------|
| CP `/mcp`（Streamable HTTP）| 外部の Claude Code / Desktop | Bearer PAT（authGate 除外パス・Google セッション非依存）| member ツール（自分の遠隔セッション駆動）+ admin read/write（role を呼び出し時に live 再解決）|
| Agent `mcp-stdio`（アシスタント面）| コンテナ内アシスタントチャット | 不要（自コンテナ・localhost）| read-only 既定・`--write` で送信/相談を含むフリート操作を広告 |
| Agent `mcp-stdio`（対話セッション面）| mcpreg builtin `af`をmaterializeできる各CLI | 不要（自コンテナ・localhost、Agent tokenを子processへ転送）| `af_report`＋Chromium Attach View 7種だけ。その他のフリートtoolは広告・callとも拒否 |

クライアント設定は `{"type":"http","url":"<PUBLIC_BASE_URL>/mcp","headers":{"Authorization":"Bearer <PAT>"}}`。
`AF_MCP_ENABLED=true` のときだけ `/mcp` が有効。設計判断は [decisions/0006](../decisions/0006-mcp-unified.md)。

## 8.8 AWS 🚧

- **ECS/EFS ランタイム**（🚧 実装済・実運用実績なし）: TaskDef 登録・Service upsert・EFS アクセス
  ポイント・Secrets 注入。SDK interface（ecsAPI/efsAPI/ssmAPI）を seam にテスト可能化（[09 §9.5](09-deploy.md)）。
- **SSM ログイン**（kind=`ssm`）: プロファイル（共通 SSO 束）+ ホスト（個別インスタンス）の 2 層
  （[06 §6.2](06-data-model.md)）。セッション開始時にコンテナ内 `aws sso login`（device flow）→
  `aws ssm start-session`。**AWS の秘密は CP に保存も到達もしない**。
- KMS custodian は 📋（seam のみ、[07 §7.6](07-security.md)）。
