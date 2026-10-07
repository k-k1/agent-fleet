# 0052. git プロバイダ（GitHub / Bitbucket）の OAuth アプリを**テナント管理者**が登録する

[English](0052-tenant-git-oauth.md) | 日本語

- 状態: **採用**（2026-08-22）。検討の記録は [docs/71](../log/71-tenant-git-oauth.md)。
  2026-10-04 改訂（issue #1667）: GitHub に組み込みアプリと種類の自動判別を追加——決定 8・9。
  決定 1・2 を一部置き換える。
  2026-10-08 改訂（issue #1676）: Agent が期限付きの GitHub App ユーザートークンを更新するように
  なった——決定 8 の最後の箇条にある日付付きの注記を参照。
- 関連: [0043-login-idp.md](0043-login-idp.ja.md) 決定 29/30（テナント定義の IdP＝**承認が要る**側）・
  決定 24/25（テナントの外へ届くものは運用者、中で閉じるものはテナント管理者） /
  [0047-tenant-network-restriction.md](0047-tenant-network-restriction.ja.md) 決定 6（同じ線引き）

## 背景

「OAuth で接続」のアプリはデプロイに 1 つで、GitHub は Workspace の env
（`GITHUB_OAUTH_CLIENT_ID`）、Bitbucket は CP の env（`BITBUCKET_OAUTH_KEY`/`_SECRET`）
だった。アプリが実際に置かれるのは各社の GitHub org / Bitbucket ワークスペースであり、
テナント毎に違って当然のものを運用者が 1 つだけ持っていた。

## 決定 1 — 設定はテナントの行にする。**デプロイ毎の設定は置かない**

`tenant_git_oauth(tenant_id, provider)`。運用者向けの UI も env も新設しない。

単一テナントのデプロイ（native / compose）では default テナントの行が事実上の
デプロイ設定になる。層を 2 つ持つより、1 つの層を全構成で共有する方が説明も導線も 1 本になる。

🔴 **2026-10-04 改訂（#1667）:** GitHub の行は client_id の代わりにバイナリへ組み込んだ
**組み込みアプリ**を指せるようになった。運用者側には、組み込みアプリを全テナントから外す
スイッチ `AF_GITHUB_BUILTIN_APPS=off` が 1 つだけ増える。決定 8 を参照。

## 決定 2 — env は**フォールバックにもしない**。移送もしない

「行が無ければ env」を残すと、*どのアプリに送られるか*が**テナントによって変わる**。
ボタンが出ない／別のアプリに飛ぶという問い合わせに対して、見る場所が 2 つになる。

起動時の自動移送（env → default テナント）も入れない。入れると「env は読まない」が
**起動時だけ嘘になり**、`.env` を消し忘れたデプロイで再起動のたびに復活する行ができる。
稼働中デプロイの代償は「テナント管理者が登録し直すまで OAuth ボタンが出ない」だけで、
token 貼付も既存接続も止まらない。

🔴 **2026-10-04 改訂（#1667）:** GitHub の行が無い**デプロイの default テナント**は、組み込みの
OAuth App を使うようになった。この決定が退けたフォールバックとは別物である——値は消し忘れる
env ではなくバイナリにあり、テナント設定の画面は実際に効いている選択を「（既定）」付きで常に
表示する。見る場所は 1 つのまま。default 以外のテナントは、行が無ければ今までどおりボタンが
出ない。決定 8 を参照。

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

## 決定 7 — Bitbucket の **refresh grant も CP で回す**（client_secret を配らない）

refresh grant は OAuth アプリの key:secret で Basic 認証する。従来は接続時に CP が
key/secret を Agent へ渡し、Agent が自前で回していた ＝ **テナントの client_secret が
全メンバーの `secrets.enc` に複製**されていた。運用者のアプリだった間は「運用者の秘密が
運用者の作ったコンテナにある」で済むが、決定 1 でアプリの持ち主がテナント管理者になった
以上、他人のディスクに置かれた他人の資格情報になる。

`POST /internal/git-oauth/bitbucket/refresh` を追加し、メンバーシップ毎の
`AF_GIT_OAUTH_TOKEN`（他のブリッジと同形・**署名鍵は別**）で認証する。テナントは
**トークンから引く**（リクエストで選ばせない）。

★ **refresh token は動かさない。** ワークスペースに残り、CP は保存しない。「CP は秘密を
素通しさせるだけで保持しない」を保ったまま、**テナントの秘密は CP・本人のトークンは
ワークスペース**という分け方にした。全部を CP に集めるより、壊れたときに失うものが小さい。

移行は**動くことを確かめてから消す**順にする: 既存ストアの key/secret は**ブリッジが一度
成功した時点で**破棄し、ブリッジ失敗時**かつ**旧値が残っている間だけ旧経路へ落ちる。
新規接続には旧値が無いので、フォールバックは一世代で構造的に消える。

代償は 2 つ、どちらも受け入れる。①refresh に CP 到達性が要る（access token は ~2h 有効
なので CP の再起動は見えない）。②docs/71 より前に起動したコンテナの Agent は保存 API で
key/secret を必須にしているため、**アップグレードの窓で 1 回だけ**「ワークスペースを
停止→起動」が要る（CP がその旨に文言を差し替える）。

## 決定 8 — GitHub の**組み込みアプリを 2 つ**同梱し、テナントはどちらか・自前・使わないを選ぶ（2026-10-04・#1667）

個人の native / WSL では、持ち主が GitHub で OAuth App を作り、Device flow を有効にして client_id を
貼るまで OAuth ボタンが出なかった。プロジェクトが所有するアプリを 2 つリリースに同梱する。

| source | client_id | 向き |
|---|---|---|
| `builtin_oauth` — 組み込み OAuth App | バイナリ | 個人。認可 1 回、scope は `repo workflow` |
| `builtin_app` — 組み込み GitHub App | バイナリ | 小さなチーム。インストール→リポジトリ選択→認可。権限が細かい |
| `custom` — テナント自前 | 行 | 自社の OAuth App **または** GitHub App を運用する組織 |
| `none` | — | ボタンなし。トークン貼付で接続 |

- **バイナリに入れてよい理由。** device flow は client_id だけで認証するので漏れる secret が無い
  （gh も同じ）。トークンはメンバーのワークスペースに入り、アプリの持ち主には届かない。
- **callback が使える構成でも device flow に揃える理由。** 同梱アプリには配備ごとのドメインを
  callback として登録しきれない（GitHub は 1 アプリ 10 個まで）。native なら loopback の callback が
  成立するが、code の交換に client_secret が要り、それがバイナリに載ることになる。全構成で
  共通に使えるのは device flow だけ。
- **両方を置く理由。** GitHub App の方が付与は狭い（リポジトリ単位のインストール・細かい権限・
  組織が管理できる）が、インストールの 1 手間は個人には実際の敷居になる。OAuth App は `repo` が
  広い代わりに 1 クリック。どちらも万人向けではないので、テナントが選ぶ。
- **既定**は `builtin_oauth`。デプロイの default テナントだけ（決定 2 の改訂）。
- **client_id の置き場所。** `github_builtin_apps.go`。`-ldflags -X` で差し替えられる。値の無い
  ビルド（fork）は組み込みの選択肢を出さない。それを指す行は「ボタンなし」に解決し、画面が
  理由を示す。
- **運用者のスイッチ** `AF_GITHUB_BUILTIN_APPS=off` は両方の組み込みアプリを全テナントから外す。
  第三者のアプリにメンバーを送ってはならない組織のため。テナントの外へ届くものなのでデプロイ
  設定とする（0043 決定 24/25）。
- **GitHub App はインストール先にしか届かない。** 認可の後で CP が `GET /user/installations` を
  確かめ、1 つも無ければその場でインストールのリンク付きで知らせる（後で clone が落ちるのを
  待たない）。GitHub App のインストールページは client_id からは分からないので、自前の GitHub App
  ではテナント管理者が `https://github.com/apps/<名前>` を入力する。
- **期限付きのユーザートークンはまだ更新しない。** 「Expire user authorization tokens」が有効な
  GitHub App は 8 時間の token と refresh token を返す。device flow で得た token は client_secret
  なしで更新できるが、af は今は access token しか保存しないので、接続はしたうえでメンバーに
  警告し、管理画面では期限をオフにするよう案内する。更新への対応は #1676。
  🔴 **2026-10-08 改訂（#1676）:** 上の箇条は 2026-10-04 時点の記述で、今は当てはまらない。CP は
  refresh token・両方の有効期間・アプリの client_id を、access token と一緒にメンバーの Agent へ渡す。
  Agent は期限の直前と 401 のときに `POST /login/oauth/access_token`（`client_id`・
  `grant_type=refresh_token`・`refresh_token`）で自分で更新する。client_secret はどこにも保存しない。
  refresh token は 1 回しか使えないので、更新はプロセスをまたぐロックの下で行い、新しい組を使う前に
  書き込む。GitHub が refresh token を拒否したとき（取り消し、または 6 か月が過ぎた）は、
  接続画面に「再接続が必要」と出す。refresh token の無いトークンは従来どおり。管理画面は期限を
  オフにするよう案内しなくなった。接続後の警告は、更新に対応する前の Agent のワークスペースに
  限って残る。

## 決定 9 — 自前アプリの種類は**尋ねずに判別する**（2026-10-04・#1667）

CP は client_id が OAuth App か GitHub App かを知る必要がある（scope の扱い・インストールの確認）。
管理者に尋ねると、入力した場所から遠いところで落ちる誤答を招く。強い順に 3 つの手掛かりを使う。

1. **保存時:** 存在しない scope を付けた `POST /login/device/code`。github.com で実測: OAuth App は
   `invalid_scope`（device code は作られない）、GitHub App は scope を無視して device code を返す
   （使われずに失効する）、未知の client_id は `Not Found`、Device flow が無効なアプリは
   `device_flow_disabled`。後の 2 つは保存時に断る——保存すると設定済みに見え、メンバーが
   ボタンを押したときに初めて落ちるため。
2. **毎回の認可の後:** GitHub が文書化しているトークンの接頭辞——`gho_` は OAuth App、`ghu_` は
   GitHub App。行を訂正する（行がまだその client_id を指している間だけ）。
3. **保存時に GitHub へ届かなかったときの代替:** client_id の形（`Ov23…` / 16 進 20 桁 → OAuth
   App、`Iv1.…` / `Iv23…` → GitHub App）。GitHub は文書化していないので、画面では推定として示す。

どの手掛かりで決まったかを行に記録する（`app_type_by`: `probe` / `token` / `prefix`）。

## 影響

- `BITBUCKET_OAUTH_KEY` / `_SECRET` は読まれなくなる。CFN の `BitbucketOauthKey` と
  `<SsmPrefix>/bitbucket-oauth-secret` の参照を削除。
- `GITHUB_OAUTH_CLIENT_ID` は**残るが意味が変わる**。以降は GitHub **サインイン**専用で、
  ワークスペースへは注入されない（docs/61 §61.7 の「git 連携が先に使っている env」という
  前提はここで終わる）。
- `AUTH=dev` のデプロイで管理モーダルが開くようになる（今まで開かなかった）。
- ワークスペースに `AF_GIT_OAUTH_TOKEN` が 1 つ増え、`secrets.enc` から Bitbucket の
  `key`/`secret` が（次回 refresh 時に）消える。逆に refresh は CP 到達性に依存する。
- （#1667）`tenant_git_oauth` に `source`・`app_type`・`app_type_by`・`install_url` を追加
  （migration 0088 / pg 0073）。既存行は `custom`。env `AF_GITHUB_BUILTIN_APPS` を新設。
  GitHub の OAuth 接続ごとに、source と client_id を記した監査行 `git_oauth.github_connect` を書く。
