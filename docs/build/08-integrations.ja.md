---
audience: "外部プロバイダや CLI エージェントを足す人"
source_of_truth: "コード（本書は方式と設計意図の地図）"
updated: "2026-09"
---

# 08. 外部システム連携

[English](08-integrations.md) | 日本語

外部プロバイダとの連携を 1 本に集約する。**横断で効く共通パターンは 2 つ**で、どちらに
当たるかで設計の大半が決まる:

- **(a) コールバック不要方式** — device flow、コード貼り戻し、トークン貼付、またはワーク
  スペースから外向きに張りっぱなしにする接続。エッジに何があっても成立する。
- **(b) CP 所有コールバック方式** — CP が到達可能な公開 URL（`PUBLIC_BASE_URL`）を持ち、
  リダイレクト URI をプロバイダに完全一致で登録する必要がある。

**新プロバイダを検討するときは、まず (a) が使えないかを探すのが本リポジトリの定石。**
いまあるコールバックは 3 つだけ: Console サインイン（`/oauth2/callback`）、Bitbucket
（`/api/oauth/bitbucket/callback`）、Jira（`/api/oauth/jira/callback`）。エージェント CLI は
すべて (a) に収まる。

## 8.1 連携一覧

| 相手 | 用途 | 方式 | コールバック | 資格の保存先 |
|---|---|---|---|---|
| Google・GitHub・任意の OIDC プロバイダ | L1 Console サインイン | OAuth Auth Code / OIDC（CP ネイティブ）| CP | 署名 cookie。アプリの client secret は CP の環境変数か封緘したテナントの行（§8.2）|
| GitHub | git 認証 | トークン貼付 / **Device Flow（CP 実行・テナントのアプリ）** | 不要 | 暗号化ストア |
| Bitbucket | git 認証 | email + トークン貼付 / **Auth Code（CP 所有 callback・テナントのアプリ）** | CP | 暗号化ストア。refresh は CP 経由（§8.4.1）|
| Jira | 作業項目インボックス | email + API トークン貼付 / **Atlassian OAuth（3LO、CP 所有 callback・テナントのアプリ）** | CP | 暗号化ストア（§8.4.2）|
| SVN サーバ | チェックアウト | ユーザー名 + パスワード貼付 | 不要 | 暗号化ストア（ラッパーが `svn` に渡す）|
| 内部 git | git ホスティング | per-membership HMAC トークン | — | CP が都度導出し CP には保存しない。Agent のストアへ seed（[91](91-internal-git.ja.md)）|
| エージェント CLI（claude・codex・opencode・cursor・kiro・agy・muse・copilot）| L2 エージェント認証 | 各 CLI 自身のサインインを Agent が駆動 | 不要 | CLI 自身の資格ファイル、または暗号化ストア（§8.5・§8.6）|
| 外部 MCP クライアント | フリートの操作 | Bearer PAT | — | ハッシュのみ DB（[06](06-data.ja.md)）|
| 利用者・テナントが足す MCP サーバ | エージェントのツール | サーバの求めるもの（各 CLI の設定に書き込む）| — | 各 CLI 自身の設定ファイル。利用者ごとのヘッダ秘密は暗号化ストア（§8.7）|
| AWS（利用者のアカウント）| SSM セッション・`af-aws-exec` | SSO device code（Console から開始）| 不要 | SSO キャッシュは**ワークスペース内。CP は見ない**（§8.8）|
| AWS（配備自身のアカウント）| ECS ランタイム・エンジン・コスト・音声 | CP の IAM ロールで SDK | — | 何も保存しない（§8.8）|
| エンジン（自配備・外部・別配備）| ローカル LLM・画像生成 | CP が発行するワークスペース単位のトークン。上流の資格は CP が足す | 不要 | 上流キーは CP の環境変数か SSM。借用トークンは CP の環境変数（§8.9）|
| Hugging Face・Civitai | モデル取り込み | API トークン | 不要 | 封緘した配備全体の設定（§8.9）|
| Discord・Slack | チャットブリッジ | ボットトークン（Slack はアプリレベルトークンも）、外向き WebSocket | 不要 | 暗号化ストア（§8.10）|
| PagerDuty・Grafana | 運用ツールの MCP サーバ | API トークン | 不要 | 暗号化ストア（§8.10）|
| models.dev | モデル単価 | 匿名 GET（1 日 1 回）| 不要 | — |
| Caddy / ALB / Tailscale Funnel | 入口と TLS | コード外のインフラ | — | —（[09 §9.3](09-deploy.ja.md)）|

「暗号化ストア」はメンバーシップごとの Agent の `secrets.enc`（[04 §4.8](04-agent.ja.md)）。

Connections の設計原則: **メンバーの秘密は CP を素通りするだけで、CP は保持も解釈も
しない**（[07 §7.6](07-security.ja.md)）。CP が保持するのは配備とテナント自身のアプリの
資格——OAuth の client secret、モデル取り込みのトークン——で、マスター鍵で封緘する。
接続状態は `GET /api/connections` に集約する。これは Agent への中継なので、ワークスペース
停止中は 502 を返す。表示専用のプロバイダ API（アカウント名）は**接続ごとに 1 回だけ**
叩いてストアにキャッシュし、ポーリングで都度叩かない。

## 8.2 Console サインイン（L1）

CP ネイティブ実装。フロー・許可リスト・プロバイダ・authGate の防御は
[07 §7.3](07-security.ja.md) が正。ここで押さえる点:

- `AUTH=oauth` は `AF_COOKIE_SECRET`・`PUBLIC_BASE_URL`・プロバイダ 1 つ以上が無いと起動を
  拒む。プロバイダは Google（`GOOGLE_OAUTH_CLIENT_ID` / `_SECRET`）、任意の OIDC 発行者
  （`AF_OIDC_PROVIDERS` と `AF_OIDC_<ID>_{ISSUER,CLIENT_ID,CLIENT_SECRET,TRUST}`）、GitHub
  （`AF_GITHUB_LOGIN_CLIENT_ID` / `_SECRET`。無ければ `GITHUB_OAUTH_CLIENT_*` にフォール
  バックし、`AF_GITHUB_ALLOWED_ORGS` があるときだけ有効）。テナントは独自の IdP を行として
  足せ、その secret は封緘される（[07 §7.3.1](07-security.ja.md)）。
- リダイレクト URI は全プロバイダ共通の `<PUBLIC_BASE_URL>/oauth2/callback` で、プロバイダに
  **完全一致**で登録する。
- サインイン後にプロバイダの資格は残さない。例外は 1 つで、GitHub アダプタは org 所属の
  再確認のため利用者のアクセストークンを**メモリ上に**持つ。
- GitHub の**サインイン**用アプリは、§8.3 の GitHub **git** 用アプリとは無関係。

## 8.3 GitHub

- **トークン貼付**: `PUT /api/connections/git/github.com`。cred helper が git に
  `x-access-token` + トークンで供給する。
- **Device Flow**（主経路）: `POST /api/connections/git/github/oauth/{start,poll}`。CP が
  **テナントの行**の client id で回す（§8.4.1）。secret は不要で、**アプリ設定で device flow
  の有効化が前提**。利用者はブラウザでコードを承認し、Console が CP をポーリングして、その
  1 回ごとに CP が GitHub へトークン要求を 1 回出す。scope は `repo workflow`
  （`ghDeviceScope`）で、以前に発行されたトークンは当時の scope のまま。得たトークンは
  貼付と同じ入口で Agent へ渡す。コールバック不要なので**どんなエッジ構成でも成立する**。
- フローは CP のメモリ上（`ghDeviceFlows`）にあり、開始した本人しか poll できず、開始した
  CP インスタンスで完了させる必要がある。
- リモートのリポジトリ列挙は REST（`/user/repos`、更新の新しい順）、ブランチ列挙は GraphQL
  （コミットの新しい順）。
- 接続が受け付けるホストは `github.com` と `bitbucket.org` だけで、それ以外は `bad_host`。
  **GitHub Enterprise と GitLab の接続は無い。**

### `gh` を別ログインなしで動かす

`gh` は git の credential helper を参照せず、`GH_TOKEN` / `GITHUB_TOKEN` か自分の設定しか
見ない。放置すると、Connections でトークンを保存済みの利用者でも `gh auth login` が別途
必要になる。

これを避けるため、イメージは `/usr/local/bin/gh` を**薄いラッパー**
（`workspace/gh-auth-wrapper.sh`）にし、実体を `/usr/local/libexec/gh` へ退避している。
ラッパーは呼び出しのたびに `github.com` について `git credential fill` を実行し——**git と
同じヘルパー** `workspace-agent cred`——トークンを `GH_TOKEN` に入れてから実 `gh` を exec
する。全員が追加ログインなしに `gh` を使え、**git と同じ鮮度**で、都度取得するので
ローテーションにも自己修復する。

**知っておくべき制約:**

- **スコープ**: トークンの scope は device flow のもの。`gh` の大半は動くが、org 系の呼び出しは
  `read:org` が無く失敗し得る。広げる設定は無く、必要な人は scope の広いトークンを貼る。
- **GitHub Enterprise 非対応** — ラッパーは github.com のトークンだけを注入する。
- **明示トークン優先**: `GH_TOKEN` か `GITHUB_TOKEN` が既にあればラッパーは上書きしない。
- **コスト**: `gh` 呼び出しごとにヘルパーを 1 回起動する（git の push / fetch と同程度）。
- **home の影**: 利用者自身の `~/.local/bin` に実体の `gh` があると PATH で先に当たって
  ラッパーを隠すので、entrypoint が起動時にシンボリックリンク以外のそれを除去する。
- **イメージ内だけ**: `native` ターゲットにはラッパーが無い。

## 8.4 Bitbucket

- **貼付**: Atlassian の email + API トークン（REST API は Basic 認証）。git では email の
  ユーザー名を `x-bitbucket-api-token-auth` に書き換え、アプリパスワードのアカウント名は
  そのまま渡す。
- **OAuth**: `GET /api/connections/git/bitbucket/oauth/start` が authorize URL を返し、
  プロバイダは `GET /api/oauth/bitbucket/callback` へリダイレクトする。consumer の key と
  secret は**テナントの行**から読む
  （[decisions/0052](../decisions/0052-tenant-git-oauth.ja.md)）。`PUBLIC_BASE_URL` が無いと
  start は `no_public_base_url` で失敗する。ブラウザ自身の CP セッションでゲートを通るので
  **除外設定は不要**。トークンは key・secret 抜きで Agent へ渡す
  （`PUT /connections/git/bitbucket/oauth`）。
  **テナントは state と一緒に運ぶ。** コールバックはプロバイダからの素のリダイレクトで
  テナントヘッダを持たないため、CP は利用者とテナントを state をキーにメモリ上に持つ
  （`bbFlows`）。他の方法でテナントを解決すると**別テナントのアプリ**で code を交換しうる。
- **refresh**: アクセストークンは失効するので、cred helper が残り 2 分を切ったら保存済みの
  refresh token で更新し、`x-token-auth` + トークンを出す。ヘルパーは全ホスト共通の
  `workspace-agent cred` 1 つで、`bitbucket-cred` は古い git 設定のための別名として残るだけ。
- リモート列挙は `GET /2.0/user/workspaces` → 各 workspace のリポジトリ（上限 500、更新の
  新しい順）。旧 `?role=member` の列挙は 410 を返す。

### 8.4.1 OAuth アプリの持ち主は**テナント**

アプリは `tenant_git_oauth` の行——`github`・`bitbucket`・`jira` に 1 つずつ——で、テナント
管理者が Console から登録する（`/api/admin/tenants/{slug}/git-oauth`）。secret は書き込み
専用でマスター鍵で封緘する。**環境変数は読まない**: `GITHUB_OAUTH_CLIENT_ID` は以降
**サインイン**専用（§8.2）で、`BITBUCKET_OAUTH_KEY` / `_SECRET` はどこからも参照されない。

- **GitHub の device flow は CP が回す。** 以前は Agent がコンテナ環境の client id で回して
  いたが、**その環境はコンテナ起動時に固まり、ランタイム実装が 4 つある**ため、テナント
  ごとにすると変更の反映に全員のワークスペース再起動が要った。
- **ボタンを出すかどうかは CP ネイティブのエンドポイント** `GET /api/git-oauth` が答える。
  `GET /api/connections` は Agent への中継でワークスペース停止中は失敗するので、それには
  使えない。得たトークンの保存には、やはり起動中のワークスペースが要る。
- **refresh も CP が回す**: `POST /internal/git-oauth/bitbucket/refresh` と
  `POST /internal/git-oauth/jira/refresh`（`git_oauth_bridge.go`）。認証は per-membership の
  `AF_GIT_OAUTH_TOKEN` で、テナントはリクエストでなくトークンから決まる。以前は key と
  secret を Agent に渡していたため、**テナントの client secret が全メンバーの暗号化ストアに
  複製**されていた。今は Agent が refresh token を送り、CP が secret を足す。
  **refresh token は動かさない** — ワークスペースに残り、CP は保存しない（「CP は秘密を
  素通しさせるだけで保持しない」を保つ）。key と secret の古い写しは**ブリッジが一度成功
  した時点で破棄**し、それまではフォールバックとして残す。テナントのアプリを削除すると、
  次の refresh は `not_configured` で失敗する。
  ブリッジの座標は環境変数でなく暗号化ストア（`GitOAuthBridge`）に置く。cred helper は git が
  起動する別プロセスで、その環境は保証できないため。

### 8.4.2 Jira

Jira は git でなく作業項目インボックス
（[decisions/0061](../decisions/0061-work-item-inbox.ja.md)）のためにあるが、上の仕組みを
共有する。

- **貼付**: `PUT /api/connections/jira` に email と API トークン。
- **OAuth（3LO）**: `POST /api/connections/jira/oauth/start` → プロバイダ →
  `GET /api/oauth/jira/callback`（`oauth_jira.go`）。scope に `offline_access` を含み、refresh
  token はローテーションし、refresh は CP のブリッジを通る。Bitbucket とは別の Atlassian
  アプリ。
- API は `api.atlassian.com/ex/jira/{cloudId}` 経由で、`PUT /api/connections/jira/site` が
  サイトを選ぶ。

インボックスは **Agent が既に持つ資格**——GitHub のトークン、Bitbucket の接続、Jira の
接続——で、CP が駆動する周期で取得する。CP は機密でないメタデータ（`work_item_cache`）だけを
持ち、webhook はどこにも無い。

## 8.5 Claude 認証・オンボーディング（L2 の本丸）

**方式**: CLI 自身のサブスクリプションサインイン `claude auth login --claudeai`。Agent が
PTY を駆動して authorize URL を抽出し、Console が表示し、利用者が自分のブラウザで承認して
コードを貼り戻すと、**CLI 自身が資格ファイルを書く**（`$CLAUDE_CONFIG_DIR/.credentials.json`、
refresh token 付き）。エンドポイントは `POST /api/connections/claude/{start,complete}` と
`DELETE /api/connections/claude`。状態は `claude auth status`、切断は `claude auth logout`。
端末で手動の `/login` も引き続き使える。

- **検証で確定した土台**: サブスクのフローのリダイレクト URI は**ホストされたコード表示
  ページで、localhost コールバックに一切依存しない**。ヘッドレス・リモートで無条件に成立する。
- **「ログイン方式の選択が出る」は認証でなくオンボーディングの問題。** `claude auth status` が
  ログイン済みでも `hasCompletedOnboarding` が無いと対話 TUI はウィザードを再実行し、その
  先頭がログイン方式の選択なので**未認証に見える**。対策はセッション起動ごとに
  `hasCompletedOnboarding` とフォルダの `hasTrustDialogAccepted` を seed すること
  （`ensureFolderTrusted`）。**権限スキップでもこれらは飛ばせない**。
  `CLAUDE_CONFIG_DIR` を設定していると **`.claude.json` もその配下から読む** — home 側を
  書いても効かない。
- **失効は見える。** `CredentialExpiry` が資格ファイルの期限を読み、状態に
  `expires_at` / `days_left` / `expired` が載る。失効したセッションは `auth` 状態になり、
  送信は `auth_expired` で拒否される。再認証は切断して同じフローをやり直す。Agent 自身の
  環境にあるトークン（`CLAUDE_CODE_OAUTH_TOKEN`・`ANTHROPIC_API_KEY`）はファイルより優先
  される。
- **`claude auth status` は遅い**（実測 21〜28 秒）ので、状態の問い合わせはリクエスト経路の
  外で専用の予算で回す。
- **教訓（繰り返さないために残す）:**
  1. setup token を `CLAUDE_CODE_OAUTH_TOKEN` で注入 → **ヘッドレス専用で、対話 TUI は
     読まない**。
  2. refresh token の無い合成資格ファイル → 対話 TUI が**拒否**する。
  3. `ANTHROPIC_AUTH_TOKEN` → 認証は通るが API 利用として課金され、サブスク機能を殺しうる。
  - **判定の教訓**: 状態コマンドもバナーも認証が効くことを証明しない。**実プロンプトと
    実応答だけが証明する。** そして **auth と onboarding は別物**
    （[decisions/0002](../decisions/0002-claude-auth-onboarding.ja.md)）。

## 8.6 その他のエージェント CLI

どの kind が何に対応し、メンバーが Console からどうサインインするかは
[ref/agents](../../guide/ref/agents.ja.md#サインインの仕方)。その下の契約は全 kind 共通:
**Agent が CLI 自身のログインを駆動し、CLI が自分の資格を書き、どの kind もコールバックを
要さない。** ログインの状態は Agent のメモリにしか無いので、CP はこれらのフローを
`restLoginFlow` で中継し、ワークスペースが収束しきる前は、退役するタスクにフローを失わせる
代わりに断る。

| kind | 方式 | エンドポイント（`/api/connections/` 配下）| 資格 |
|---|---|---|---|
| codex | stdin で API キー（`codex login --with-api-key`）、または ChatGPT device flow（PTY で `codex login --device-auth`、検証 URL とワンタイムコードをスクレイプ）| `codex/api-key`・`codex/device/{start,poll}`・`DELETE codex` | `~/.codex/auth.json`（CLI 所有）|
| opencode | 環境変数名つきのプロバイダキー、または `opencode serve` 自身の API を通した opencode アカウントの device flow | `PUT opencode`・`DELETE opencode/{env}`・`opencode/oauth/{start,poll,cancel}`・`opencode/serve/restart` | キーは暗号化ストア。アカウントは opencode 自身の DB |
| cursor | PTY で `cursor-agent login`、URL をスクレイプ。CLI がポーリングし、コードは貼らない | `cursor/{start,poll}`・`DELETE cursor` | `~/.config/cursor/auth.json`（CLI 所有）|
| kiro | PTY で `kiro-cli login --use-device-flow`、URL とコードをスクレイプ | `kiro/{start,poll}`・`DELETE kiro`・`kiro/install` | CLI 自身の DB |
| agy | PTY で対話 TUI（ヘッドレスのログインは無い）: Google OAuth、コードを貼り戻し、続けてオンボーディング | `agy/{start,complete}`・`DELETE agy` | CLI 自身のトークンファイル |
| muse | **PTY でなくパイプ**で `muse login`、device URL とコードをスクレイプ。または API キー（アカウントでサインイン中は `account_login_present` で拒否）| `muse/{start,poll,api-key}`・`DELETE muse`・`muse/install` | `~/.config/muse/auth.json`（CLI 所有）|
| copilot | 自前の方式は無く、`gh` ラッパー（§8.3）経由で GitHub の接続に乗る。Managed の子には `COPILOT_GITHUB_TOKEN` を渡す | —（状態のみ）| GitHub の接続 |
| lcpp | 無し: CP からセッション単位のエンジントークン（§8.9）。メンバー自身の llama.cpp サーバを任意で指定でき、そちらが優先 | `PUT lcpp`・`DELETE lcpp`・`lcpp/check` | メンバーのサーバは暗号化ストア |

kind 固有の点:

- **codex**: 環境変数での資格注入は効かない（`codex login status` がログアウトのまま）ので、
  両経路とも CLI 自身にファイルを書かせる。OpenAI 側のポーリングは CLI が自分で行う。
  ChatGPT アカウントで device code ログインが無効だと、start は `no_url` で失敗する。接続済み
  カードは `auth.json` の `auth_mode` と id token の claim から email とプランを読む。
- **opencode**: 環境変数名は `^[A-Z][A-Z0-9_]{1,63}$` に一致する必要がある。**キーは
  コマンドラインに載せない**: ターミナル（CLI）のセッションは `tmux new-session -e` で、
  Managed のセッションは共有 `opencode serve` デーモンの環境で受け取る。キーを変えたら
  `serve/restart` が要るのはこのため。平文の auth ファイルは作らない
  （[04 §4.3](04-agent.ja.md)）。

## 8.7 MCP — 対外契約

| 面 | 接続する側 | 認証 | スコープ |
|---|---|---|---|
| CP の `/mcp` | 外部の Claude クライアント | Bearer PAT（ログインゲートの除外パス）| member ツール + admin の read/write ツール（**role は呼び出しごとに live 再解決**）|
| Agent の `mcp-stdio`（アシスタント面）| コンテナ内のアシスタントチャット | 同じコンテナの Agent 自身のトークン | 既定は read-only。`--write` でフリート操作を広告 |
| Agent の `mcp-stdio`（セッション面）| セッション内の全エージェント CLI | Agent 自身のトークンを子に転送 | 下記の狭い集合 |
| 利用者・テナントが足す MCP サーバ | 全エージェント CLI | サーバの求めるもの | — |

**CP のエンドポイント**は `AF_MCP_ENABLED=true` のときだけ存在する。POST のみの JSON-RPC を
JSON で返し（SSE なし）、両方のプロトコル世代を話す
（[decisions/0032](../decisions/0032-mcp-2026-07-28.ja.md)）。PAT はハッシュで保存し
（`GET|POST /api/pat`・`DELETE /api/pat/{id}`）、scope は role の上限に切り詰める。テナントは
常にトークンから決まる。member ツールは自分のセッションの駆動・起動・掃除・計量とメモの
管理。admin ツールはワークスペース・使用量・egress・監査ログを読み、書き込みはワーク
スペースやセッションの停止・クォータ設定・許可リスト変更の**提案**だけで、書き込みは
PAT を主体として監査に残る。危険な段（鍵ローテーション・ワークスペース再作成・一括停止）は
予定しない（[decisions/0006](../decisions/0006-mcp-unified.ja.md)）。クライアント設定は:

```json
{"mcpServers":{"agent-fleet":{"type":"http","url":"<PUBLIC_BASE_URL>/mcp","headers":{"Authorization":"Bearer <PAT>"}}}}
```

**セッション面**は組み込みサーバ（`mcpreg` の `BuiltinAF`）で、起動ごとの名前 `af_<8 hex>` で
登録するため、リポジトリ独自の `af` に隠されない。claude・opencode・codex・cursor・kiro・
agy・copilot にはネイティブ設定へ書き込み、muse にはワイヤで送り、lcpp はプロセス内で呼ぶ。
codex は MCP の子を空の環境で起動するので、`AGENT_TOKEN`・`AGENT_ADDR`・`AF_SESSION_NAME`・
`AF_CP_BASE_URL`・`AF_MEMO_TOKEN` を明示的に転送する。広告する集合は `mcpStdioToolList` が
組み立てる:

| 群 | 条件 |
|---|---|
| 引き継ぎ・`af_report`・`af_stop_after_turn`、Chromium Attach のツール、セッションの状態と使用量、メモ（一覧・追加・更新）| 常に |
| `list_peer_sessions`・`send_to_peer_session` | 利用者のピアメッセージ設定 |
| セッションの起動と操縦（`create_session` …）。操縦は呼び出し元自身の子にだけ届く | 利用者のセッション起動設定 |
| `generate_image` | 利用者の画像生成設定、その上で `tools/list` ごとに判定 |
| 画像スタジオのツール | セッションがスタジオに結び付いているとき |

広告していないものは**呼び出しでも拒否**する（最後の `tools/list` と照合）。集合が変われば
監視役が `notifications/tools/list_changed` を送る。メモやセッションの削除はここには出さない。

**利用者・テナントが足すサーバ**（[decisions/0031](../decisions/0031-mcp-registry.ja.md)）は
各 CLI 自身のグローバル設定に書き込み、Agent は自分が書いた名前だけを消す。テナントの
サーバは `GET /internal/mcp-servers`（`AF_MCP_TOKEN`）から取り、CP に届かないときは最後の
写しを使い続ける。組み込みの運用サーバ（PagerDuty・Grafana・CloudWatch・AWS）は
`workspace-agent mcp-run <id>` として動き、起動時に保存済みの資格を注入する。

## 8.8 AWS

AWS と話すものは無関係に 2 つあり、混同してはいけない: 配備自身のアカウントを使う CP と、
自分のアカウントを使うメンバー。

**配備自身のアカウント**（CP の IAM ロール。何も保存しない）。本番は `ecs-ec2` で動いている。
ランタイムはタスク定義の登録・サービスの upsert・EFS アクセスポイントの作成・SSM
SecureString パラメータとしての秘密の注入を行い、`ecs-ec2` はユーザーごとの永続 EBS home・
EC2 スロットプール・スナップショット・SSM `SendCommand` を足す（[09 §9.5](09-deploy.ja.md)）。
CP はエンジン用インスタンスを EC2 Fleet でも買い（`engineFleetAPI`、
[decisions/0077](../decisions/0077-engine-boxes-bought-by-cp.ja.md)）、Cloud Map・Cost
Explorer・Polly・S3・Secrets Manager・CloudWatch Logs も呼ぶ。SDK クライアントはそれぞれ
インターフェース（`ecsAPI`・`ec2API`・`engineFleetAPI` …）の裏にありテストできる。CP は
Bedrock を呼ばない。

**メンバーのアカウント**（パターン (a)。**AWS の秘密は CP に保存も到達もしない**）:

- プロファイルは CP の行（`ssm_profile`、SSM の接続先は `ssm_host`。[06 §6.2](06-data.ja.md)）。
  ワークスペースは `GET /internal/aws-profiles`（`AF_AWS_PROFILES_TOKEN`）で取得し、
  `~/.aws/config` に管理ブロックとして書く。
- **SSM セッション**はワークスペース内で `aws sso login --use-device-code --no-browser`、続けて
  `aws ssm start-session` を実行する。Console は device code を
  `GET /api/sessions/{name}/ssm-login` でポーリングする。
- **`af-aws-exec`** は自分でログインを始めず Console に頼む
  （[decisions/0102](../decisions/0102-aws-login-through-the-console.ja.md)）: device code は
  メンバーがボタンを押したときだけ始まる（`/api/aws-login/…`）。コードと URL は CP を
  通ってブラウザへ届き監査に残るが、トークンは通らない。子プロセスはコンテナと
  インスタンスメタデータの資格を外した環境で動くので、ワークロードロールにフォールバック
  できない。
- SSO キャッシュはワークスペース内の `~/.aws/sso/cache/` にある。

KMS custodian は 📋 — seam のみ（`KeyCustodian`、[07 §7.6](07-security.ja.md)）。

## 8.9 エンジン

エンジンは配備が提供するモデルサーバ——llama.cpp、ComfyUI、OpenAI 互換の画像 API。
**ワークスペースはエンジンと直接話さず**、上流の資格も持たない:

- Agent は `POST /internal/engine/token` からワークスペース単位のトークン（`AF_ENGINE_TOKEN`）を
  得て、ログインゲートの除外パスである CP のゲートウェイ `/engine/{key}/v1/…` を呼ぶ。上流の
  資格は CP が足す: 外部の行なら CP 環境の `AF_ENGINE_API_KEY_<KEY>`、CP が管理する行なら SSM
  SecureString（[decisions/0083](../decisions/0083-openai-compat-image-provider.ja.md)）。
- **別配備のエンジンの借用**は CP から CP への外向きだけ
  （[decisions/0079](../decisions/0079-remote-engine-from-another-deployment.ja.md)）。借りる
  側は `AF_REMOTE_ENGINE_URL`・`AF_REMOTE_ENGINE_TOKEN`（貸す側の super_admin が
  `POST /api/admin/engines/issue-token` で発行）・任意で `AF_REMOTE_ENGINE_KEYS` を設定する。
  発行トークンをエンジントークンに交換してメモリ上だけに持ち、貸す側の
  `GET /internal/engine/catalog` をポーリングする。貸す側は新たに何も保存しない。手順は
  [operate/08](../../guide/operate/08-borrowed-engine.ja.md)。
- **モデル取り込み**は Hugging Face と Civitai から配備全体のトークンで取得する。super_admin が
  設定し、設定の行に封緘する。AWS では取り込みタスクのためにスタックの Secrets Manager の
  シークレットにも写すが、CP はそこに書くだけで読まない。
- codex や agy での画像生成は、その CLI 自身のサインインを使う（§8.6）。キーは関わらない
  （[decisions/0069](../decisions/0069-image-generation-providers.ja.md)）。

## 8.10 チャットブリッジと運用ツール

- **Discord と Slack**（[decisions/0020](../decisions/0020-chat-bridge.ja.md)）: 各利用者が
  自分のボットを登録し、CP でなく **Agent** が外向きの WebSocket を張りっぱなしにする。
  Discord はボットトークンで Gateway、Slack はボットトークンとアプリレベルトークンで Socket
  Mode。webhook も公開 URL も無いので、ブリッジはワークスペースが動いている間だけ生きる。
  CP は `PUT|DELETE /api/connections/{discord,slack}` と点検用エンドポイントを中継する。送信
  専用の Teams 枠は設計だけで実装は無い。
- **PagerDuty と Grafana**: 貼付した API トークンを暗号化ストアに置き
  （`PUT /api/connections/{pagerduty,grafana}`）、§8.7 の運用 MCP サーバが使う。CloudWatch と
  AWS はプロファイル名だけを保存し、メンバーの AWS プロファイル（§8.8）を使う。
