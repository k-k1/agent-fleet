# 0052. git プロバイダ（GitHub / Bitbucket）の OAuth アプリを**テナント管理者**が登録する

- 状態: **採用**（2026-08-22）。検討の記録は [docs/71](../71-tenant-git-oauth.md)。
- 関連: [0043-login-idp.md](0043-login-idp.md) 決定 29/30（テナント定義の IdP＝**承認が要る**側）・
  決定 24/25（テナントの外へ届くものは運用者、中で閉じるものはテナント管理者） /
  [0047-tenant-network-restriction.md](0047-tenant-network-restriction.md) 決定 6（同じ線引き）

## 背景

「OAuth で接続」のアプリはデプロイに 1 つで、GitHub は Workspace の env
（`GITHUB_OAUTH_CLIENT_ID`）、Bitbucket は CP の env（`BITBUCKET_OAUTH_KEY`/`_SECRET`）
だった。アプリが実際に置かれるのは各社の GitHub org / Bitbucket ワークスペースであり、
テナント毎に違って当然のものを運用者が 1 つだけ持っていた。

## 決定 1 — 設定はテナントの行にする。**デプロイ毎の設定は置かない**

`tenant_git_oauth(tenant_id, provider)`。運用者向けの UI も env も新設しない。

単一テナントのデプロイ（native / compose）では default テナントの行が事実上の
デプロイ設定になる。層を 2 つ持つより、1 つの層を全構成で共有する方が説明も導線も 1 本になる。

## 決定 2 — env は**フォールバックにもしない**。移送もしない

「行が無ければ env」を残すと、*どのアプリに送られるか*が**テナントによって変わる**。
ボタンが出ない／別のアプリに飛ぶという問い合わせに対して、見る場所が 2 つになる。

起動時の自動移送（env → default テナント）も入れない。入れると「env は読まない」が
**起動時だけ嘘になり**、`.env` を消し忘れたデプロイで再起動のたびに復活する行ができる。
稼働中デプロイの代償は「テナント管理者が登録し直すまで OAuth ボタンが出ない」だけで、
token 貼付も既存接続も止まらない。

## 決定 3 — **承認を要らない**（`tenant_idp` と揃えない）

`tenant_idp` が super_admin の承認を要るのは、IdP の登録が「**誰であるか**を宣言する
権限」だからである（0043 決定 30）。git の OAuth アプリはそれを持たない:

- identity を増やさない。ログイン画面にボタンは出ないし、user_key も deployment role も動かない。
- `redirect_uri` は **CP 所有で固定**。攻撃者のアプリを登録しても grant を余所へ飛ばせない。
- 得られる token は**押した本人のワークスペース**にしか入らず、登録した管理者には返らない。
- そして `AUTH=dev` のデプロイには承認できる super_admin が居ない（決定 5）。
  承認制にすると、その構成では**永久に pending のまま**という例外規則が要る。

止めるのは常に速い方でよいので、削除もテナント管理者ができる。

## 決定 4 — GitHub の device flow を **Agent から CP へ移す**

per-tenant の client_id をコンテナ env で配る案を捨てた理由は 2 つ。

1. env が固まるのは**コンテナ起動時**で、実装が**ランタイム毎に 4 つ**ある
   （docker / native / ecs / ecs-ec2）。
2. **反映に全メンバーのワークスペース再起動が要る。** 初回登録の直後が一番踏みやすい。

CP で回せば配線ゼロ・即時反映で、Bitbucket と形も揃う。パス
（`/api/connections/git/github/oauth/{start,poll}`）は据え置き、取得した token は
Agent の `PUT /connections/git/github.com`（PAT 貼付と同じ入口）へ渡す。
Agent 側の device flow ハンドラと `githubClientID()` は**削除**する——残せば env を
読む経路が生き、決定 2 が嘘になる。

## 決定 5 — `AUTH=dev` の固定ユーザーを **super_admin** として扱う

`deploy/native/af` は `AUTH=dev` 固定で、その identity は **email を持たない**。
`SUPER_ADMIN_EMAILS` はアドレス照合なので、native / WSL には super_admin が
**原理的に存在しなかった**。設定が全部 env にあった間は問題にならなかったが、
決定 1 の後は「設定できる人が 1 人も居ないデプロイ」になる。

`AUTH=dev` は無認証の単一固定ユーザー＝ホストの持ち主なので、権限を与えているのではなく
**モードの実態を role に写しているだけ**である。email が空なので `DemoteSuperAdmins`
（`email <> ''` のみ対象）にも掛からず、再起動で剥がれない。

## 決定 6 — secret は**書き込み専用**、ただし初回の空は拒否

保存済みの `client_secret` は返さない（`tenant_idp` / `mcp_server` と同じ契約）。空で
保存＝「変えない」。ただし secret が要るプロバイダの**初回**だけは空を拒否する
（`secret_required`）——空のまま保存できると、画面上は登録済みなのに token 交換で落ちる行が
できて、失敗が Bitbucket 側の `invalid_client` としてしか見えなくなる。

GitHub には逆向きの規則を置く: secret を渡されても**保存しない**。device flow は
client_id だけで認証するので、保存すれば「誰も読まない・誰も rotate しない資格情報」を
増やすだけになる。

## 影響

- `BITBUCKET_OAUTH_KEY` / `_SECRET` は読まれなくなる。CFN の `BitbucketOauthKey` と
  `<SsmPrefix>/bitbucket-oauth-secret` の参照を削除。
- `GITHUB_OAUTH_CLIENT_ID` は**残るが意味が変わる**。以降は GitHub **サインイン**専用で、
  ワークスペースへは注入されない（docs/61 §61.7 の「git 連携が先に使っている env」という
  前提はここで終わる）。
- `AUTH=dev` のデプロイで管理モーダルが開くようになる（今まで開かなかった）。
