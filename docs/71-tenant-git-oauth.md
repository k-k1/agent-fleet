# 71. git プロバイダ OAuth をテナント管理者が設定する

- 決定は [ADR 0052](decisions/0052-tenant-git-oauth.md)。
- 関連: [08-integrations.md](dev/08-integrations.md) §8.4（git 連携の現状）/
  [61-login-idp.md](61-login-idp.md) §61.11（テナント定義のサインイン方法 — **承認が要る側**の
  前例）/ [66](66-tenant-network-restriction.md)（テナント管理者が書ける面の前例）

## 71.1 背景 — 何が「デプロイの設定」になっていたか

Console の「接続」タブにある **OAuth で接続** は 2 つの別実装だった。

| | どこで走っていたか | 何を読んでいたか |
|---|---|---|
| GitHub | **Workspace Agent**（device flow） | コンテナ env `GITHUB_OAUTH_CLIENT_ID`（CP が注入） |
| Bitbucket | **CP**（authorization code grant） | CP の env `BITBUCKET_OAUTH_KEY` / `_SECRET` |

どちらもデプロイに 1 つで、テナント毎に変えられない。しかし OAuth アプリが置かれるのは
**各社の GitHub org / Bitbucket ワークスペース**であり、持ち主はテナントである。子会社が
自社の org のアプリを使いたければ運用者に頼むしかなく、頼まれた運用者は他社の資格情報を
`.env` に持つことになる。

## 71.2 決めたこと（要約）

1. **テナント単位の行だけを正とする。** `tenant_git_oauth`（migration 0048 / pg 0032）。
2. **env は一切読まない。** フォールバックも移送もしない（→ §71.7）。
3. **承認は要らない。** テナント管理者の保存で即有効（→ ADR 決定 3）。
4. **GitHub の device flow を CP へ移す。** コンテナ env を経由しなくなる（→ §71.5）。
5. **`AUTH=dev` の固定ユーザーを super_admin にする。** そうしないと native / WSL で
   設定画面に入れる人が 1 人も居ない（→ §71.6）。

## 71.3 データ

```sql
CREATE TABLE tenant_git_oauth(
  id, tenant_id, provider,          -- provider = github | bitbucket
  client_id,
  secret_enc, key_ref,              -- テナント鍵で封印（tenant_idp.secret_enc と同じ封筒）
  updated_by, created_at, updated_at
);
UNIQUE(tenant_id, provider)
```

- **status 列は無い。** これが `tenant_idp` との唯一で最大の構造差である（理由は ADR 決定 3）。
- **GitHub 行は secret を持たない**のが正常。device flow は client_id だけで認証する。
  API は GitHub に secret を渡されても捨てる——読まれない資格情報を DB に残すと、
  誰も rotate しないものが増えるだけになる。
- 封印は `sealTenantSecret` / `openTenantSecret`（`tenant_idp.go`）をそのまま使う。
  `AF_MASTER_KEY` 未設定のデプロイでは平文＋空 `key_ref` に退行する——CP の他所と同じ。
  以前この値は `.env` に平文で置かれていたので、露出の種類は増えていない。

## 71.4 API と画面

| | |
|---|---|
| `GET /api/admin/tenants/{slug}/git-oauth` | 既知プロバイダを**必ず全部**返す（未登録は空のカード） |
| `PUT /api/admin/tenants/{slug}/git-oauth/{provider}` | `client_id` 必須・`client_secret` は書き込み専用 |
| `DELETE /api/admin/tenants/{slug}/git-oauth/{provider}` | 新規接続の導線を止める（既存接続は生きたまま） |
| `GET /api/git-oauth` | **メンバー向け**。自分のテナントでどのボタンを出せるか |

- 門は `tenantAdminFor`（ハンドラ内で取る）。super_admin はどのテナントでも触れる。
- `client_secret` は**返さない**。空のまま保存＝「変えない」。ただし **secret が要る
  プロバイダの初回保存**だけは空を拒否する（`secret_required`）——空で保存できると、
  「登録済みに見えるのに token 交換で落ちる」行ができる。
- 画面は **テナント設定 › 連携 › git プロバイダ OAuth**（`tenantGitOAuth.tsx`）。
  レールに「連携」という節を新設した。ログインでも運用でもないため。
- ★ **`GET /api/git-oauth` を別に立てた理由**: `GET /api/connections` は Agent への
  プロキシで、ワークスペース停止中は 502 を返す。「OAuth ボタンを出すか」を決めたいのは
  まさにその瞬間である。答えは CP の DB にあるので Agent を経由する理由が無い。
- ★ **押してから `not_configured` を返す形にはしない。** 直せるのはテナント管理者であって
  押した本人ではないので、押す前に「テナント管理者に登録を頼め」と言えないと詰む。
  取得できていない間（null）は**出す側に倒す**——取得失敗でボタンを消すと、登録済みなのに
  導線が無いという直しようのない画面になる。

## 71.5 GitHub の device flow を CP へ移した理由（native / docker が決め手）

「テナント毎の client_id をコンテナ env で配る」案は成立しない。

- env が固まるのは**コンテナ起動時**で、実装は**ランタイム毎に 4 つ**ある
  （docker の `-e` / native のプロセス env / ecs の task definition / ecs-ec2）。
  per-tenant env の口自体は既にある（`workspaceExtraEnv`・`AF_AGENT_SELF_UPDATE_ALLOWED`）が、
  4 つ全部に通す必要がある。
- 何より **反映にワークスペース再起動が要る**。テナント管理者がアプリを登録した直後、
  メンバーには依然「未設定」と見え、全員に「一度停止して起動し直してください」と
  言うことになる。初回設定でそれを踏むのが一番痛い。

CP で回せば env の配線がゼロになり、docker / native / ecs / ecs-ec2 の差も消え、
保存した瞬間に効く。Bitbucket が既にそうなっているので、形も揃う。

移設で変わったこと:

- `POST /api/connections/git/github/oauth/{start,poll}` は**パスそのまま**で
  proxy(`restLogin`) → CP ハンドラ（`oauth_github_device.go`）になった。Console は無変更。
- 取得したトークンは CP から Agent の **`PUT /connections/git/github.com`** に渡す。
  PAT 貼付と同じ入口なので、保存・cred helper・アカウント照会が二重化しない。
- flow の状態は CP のプロセスメモリ（`bbFlows` と同じ制約。CP 多重化時は sticky か DB 退避）。
- ★ **poll は開始した本人にしか答えない。** `flow_id` は他人の保留中の grant を指すので、
  誰でも poll できると「トークンが最初の人のワークスペースに入り、poll した人には
  connected と返る」。
- ★ Agent 側の `handleGithubOAuthStart/Poll` と `githubClientID()` は**削除**した。
  残すと env を読む経路が生き続け、「env は読まない」が嘘になる。

Bitbucket 側の変更は 1 点だけ:`bbState` に **`tenantID` を持たせた**。コールバックは
bitbucket.org からの素のリダイレクトで `X-AF-Tenant` を持たないので、そこで解決し直すと
**別テナントのアプリで code を交換しうる**。

## 71.6 `AUTH=dev` を super_admin にした理由（native / WSL の穴）

`deploy/native/af` は `AUTH=dev` を固定し、`run-dev.sh` の wsl プリセットも同じ。
このモードの identity は **email を持たない**（`resolver.go`）。ところが
`SUPER_ADMIN_EMAILS` はアドレスで照合するので、**native / WSL には super_admin が
原理的に存在しない**。自動プロビジョンの役割も `member` 固定なので tenant_admin も居ない。
＝ `GET /api/admin/tenants` は空を返し、テナント設定画面には誰も入れなかった。

デプロイの設定が全部 env にあった間はこれで済んでいた。OAuth アプリがテナントの行に
なった以上、**設定できる人が 1 人も居ないデプロイ**を作ることになるので、
`roleHintFor` が dev モードで `super_admin` を返すようにした。

- 与えているものは無い。`AUTH=dev` は**そもそも無認証の単一固定ユーザー**で、その人は
  ホストの持ち主である。「全員が運用者」はこのモードの説明であって特権ではない。
- membership は `AF_PROVISION=auto`（既定）が初回リクエストで default テナントに作る。
  役割 `super_admin` は membership が無くても管理面が開くので、どちらでも起動直後に使える。
- 再起動で剥がれない。`DemoteSuperAdmins` は `email <> ''` の行しか見ない。

## 71.7 移行 — env は読まず、移送もしない

`GITHUB_OAUTH_CLIENT_ID`（ワークスペースへの注入）・`BITBUCKET_OAUTH_KEY` / `_SECRET` は
**コードから消した**。起動時に default テナントへ書き写す処理も入れていない。

- 稼働中デプロイでは、テナント管理者が登録し直すまで OAuth ボタンが出ない。
  **接続そのものは止まらない**——token 貼付は常に使えるし、既存の接続は
  ワークスペース側の資格情報なのでそのまま動く。
- CFN の `BitbucketOauthKey` パラメータと `<SsmPrefix>/bitbucket-oauth-secret` の
  Secrets 参照は `30-ingress.yaml` から削除した。`GithubClientId` は**残る**——
  GitHub **サインイン**が同じ env を読んでいるため（別機能）。
- ★ 用語の後始末: `GITHUB_OAUTH_CLIENT_ID` はこれ以降「GitHub ログイン用アプリ」だけを
  意味する。docs/61 §61.7 の「git 連携の device flow が先に使っている env」という前提は
  もう成り立たない。

## 71.8 やっていないこと

- **GitLab**。器（`provider` 列と `gitOAuthProviders`）は増やせる形にしてあるが、
  git 接続そのものが GitLab に未対応（Agent の `gitHosts` に無い）ので入れていない。
- **Bitbucket の client_secret をワークスペースへ配るのをやめる**。refresh は
  `workspace-agent cred` がオフラインで回すので key/secret がコンテナの暗号化ストアに
  複製される（`bitbucketStoreReq`）。構造は運用者のアプリだった頃と同じで、露出の種類は
  増えていない。CP 経由の refresh へ寄せるのは別作業。
