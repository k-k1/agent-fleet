# 05. サインイン方式の設定 — IdP 側の手順まで通しで

[English](05-signin.md) | 日本語

Audience: IdP を配備につなぐ人
Source of truth: `deploy/` 配下のスクリプト（記述がスクリプトと食い違ったら、このページのバグ）
Updated: 2026-08

このページが**サインイン設定の正**です。IdP 側で何を作るか、どの値を控えるか、それを `.env` の
どのキー（または Console のどの欄）に入れるか、うまくいったことをどう確認するか、そしてよくある
失敗は何か、を IdP ごとに並べています。[01-install §3](02-install.ja.md) には要点だけを残して
ここへのリンクを置いてあり、両者が食い違ったときは**このページが正**です。

追加したい IdP の節を上から順に読めば、入口が開くところまで到達できるように書いてあります。
すでに動いているデプロイの切り分けは[06-diagnose.md](06-diagnose.ja.md)の役目
（あちらは症状から引く索引）で、ここでは繰り返しません。

内部の仕組み（信頼規則、2 つの IdP をまたいで同じ人と見なす判定）は
`docs/build/07-security.ja.md` §7.3.1 と ADR 0043。

## 0. はじめる前に

- **OAuth/OIDC のフローは Control Plane 自身が実行します**（`AUTH=oauth`・既定）。有効にする
  IdP ごとに、client secret を持つ**機密クライアント**が必要です。
- **複数を同時に有効にできます。** ログイン画面には有効な方式の数だけボタンが出ます。順番は
  Google が先頭、次に列挙した順の OIDC provider、GitHub が最後です。
- **壊れた IdP は自分だけを無効化します。** 設定が不完全な provider は CP のログに警告を出して
  外されるだけで、CP が起動を止めるのは**使える方式が 1 つも無いとき**（と、§4 の危険な Entra の
  指定のとき）だけです。
- **SAML しか無い IdP（HENNGE One / TrustLogin / CloudGate など）の答えは `AUTH=proxy`** です。
  oauth2-proxy や Keycloak でブリッジし、メールアドレスの識別を前段のゲートウェイに委ねます。
  そのモードではこのページの内容は使いません。
- ここでの `.env` の変更は**次の CP 起動**から効きます。テナントが Console から登録する
  サインイン方式（§7）は再起動不要です。

## 1. どの IdP でも同じ 1 点 — リダイレクト URI

**承認済みリダイレクト URI**（GitHub では *Authorization callback URL*）として、どの IdP にも
次の 1 本だけを登録します。

```
https://<PUBLIC_DOMAIN>/oauth2/callback
```

`<PUBLIC_BASE_URL>/oauth2/callback` と**1 文字も違わない**こと（スキームもホストも同じ、末尾
スラッシュ無し、`PUBLIC_BASE_URL` にポートが無ければポートも無し）。構築全体で最も多い失敗が
ここです。

**この 1 本は増えません。** provider が 10 個でも URI は 1 本です。どの provider のコールバック
かは署名付き Cookie で運んでいて URL には現れないため、provider ごとに登録するものがありません。
**テナント**が自前で定義する方式（§7）で、そのテナントが*自社の* IdP に登録するのも同じこの
URI です — 相手の Console ではなく、**あなたの** Console の URL です。

`PUBLIC_BASE_URL` を変えるときは、同じ作業時間内にすべての IdP 側の URI も直してください。
直さないと、全員が IdP のエラー画面で止まります。

## 2. どの設定がどこにあるか

サインイン方式には 2 種類あり、**同じ設定でも種類によって置き場が違う**のが最大の混乱点です。

- **デプロイ共通の方式** — あなたが `.env` に書くもの。全員に、すべてのログイン画面で出ます。
- **テナント定義の方式** — テナント管理者が Console から登録し、あなたが承認して初めて動くもの
  （§7）。1 つのテナントのものであり、そのテナント自身のログイン画面にしか出ません。

| 設定したいこと | デプロイ共通（`.env`） | テナント定義（Console） |
|---|---|---|
| アプリ／クライアントの登録 | あなたが IdP 側で作る | テナントが*自社の* IdP に作る |
| リダイレクト（コールバック）URI | `https://<PUBLIC_DOMAIN>/oauth2/callback` | 同じこの 1 本（相手のではなく、あなたの URL） |
| どの方式が存在するか | `AF_OIDC_PROVIDERS`（＋ Google・GitHub は専用キー） | **サインイン方式**の 1 行＝1 方式 |
| client_id / client_secret | `.env` | 入力欄。`AF_MASTER_KEY` で暗号化して保存し、二度と表示しない |
| issuer | `AF_OIDC_<ID>_ISSUER` | **issuer（発行者 URL）** |
| email を信じてよい理由 | `AF_OIDC_<ID>_TRUST`（既定値なし） | **email の信頼方法** |
| 誰が入れるか（メール） | `AF_OAUTH_ALLOWED_EMAILS` / `_DOMAINS` / `_EMAILS_FILE`、または provider 単位の `AF_OIDC_<ID>_ALLOWED_*` | **受け入れるメールドメイン** — 必須。デプロイ共通の許可リストへのフォールバックは無い |
| GitHub の組織 | `AF_GITHUB_ALLOWED_ORGS` | **許可する GitHub 組織** |
| Entra の tenant id | `AF_OIDC_<ID>_ALLOWED_TIDS` | **許可する tenant id** |
| ボタンの文言 | `AF_OIDC_<ID>_LABEL_JA` / `_LABEL_EN` | **ボタンの文言**。既定でも会社名が入る |
| 別アプリ登録をまたいで同じ人と見なす | `AF_OIDC_<ID>_LINK_CLAIM`（任意のクレーム） | **同一アカウントの見分け方**（`oid` のみ） |
| いつ効くか | 次の CP 起動から | デプロイ管理者が**承認**した時点（再起動不要） |
| ボタンが出る場所 | `/login` とすべての `/login/<slug>` | `/login/<slug>` のみ |
| provider id（テナントの**サインイン方法**の面に並ぶ行の id） | 列挙した id（`google` / `github` / `entra` …） | `t:<tenant-slug>:<name>` |
| テナントの面での扱い | バッジ「**デプロイ共通**」・編集不可（受け入れる／出すは選べる） | そのテナントが作った行・編集可 |

探しても**設定できないもの**が 3 つあります。先に知っておくと無駄がありません: Google の
エンドポイントと信頼規則、GitHub の `github.com` / `api.github.com`、各アダプタが要求する
スコープです。

## 3. Google

### 3.1 Google Cloud Console 側で作るもの

1. このクライアントを置くプロジェクトを選ぶ（無ければ作る）。
2. そのプロジェクトの **OAuth 同意画面**を設定する。Google Workspace を使っていて自社ドメイン
   以外にこの画面を見せたくないなら**内部（Internal）**、それ以外は**外部（External）**。これは
   *画面に到達できる人*の範囲であって、許可リスト（§2 のメール設定）ではありません。
3. **認証情報 → 認証情報を作成 → OAuth クライアント ID**、種類は **ウェブ アプリケーション**。
4. *承認済みのリダイレクト URI* に §1 の 1 本を追加する（*承認済みの JavaScript 生成元*は
   不要です。CP はブラウザ側のクライアントではありません）。
5. 作成し、**クライアント ID** と**クライアント シークレット**をその場で控える。

### 3.2 控えた値の入れ先

```sh
GOOGLE_OAUTH_CLIENT_ID=<client-id>.apps.googleusercontent.com
GOOGLE_OAUTH_CLIENT_SECRET=<client-secret>
```

Google はこれだけです。従来の変数名をそのまま使うので `AF_OIDC_GOOGLE_*` は**ありません**し、
`AF_OIDC_PROVIDERS` に書く必要も**ありません** — 上の 2 つを設定することが有効化の合図で、
provider id は `google` です。issuer・エンドポイント・信頼規則は組み込みで、Google に対して
CP は discovery を一切行いません。規則は常に「Google 自身の `email_verified` が付いた
アドレスだけ」で、要求するスコープは `openid email` です。

### 3.3 よくある失敗

- **`redirect_uri_mismatch`** — クライアントの URI が §1 と完全一致していない。Google は
  文字列全体で比較します。
- **2 つのキーの片方だけを設定した** — ボタンが黙って出ません。CP のログに
  `google login disabled — set both GOOGLE_OAUTH_CLIENT_ID and GOOGLE_OAUTH_CLIENT_SECRET`。
- **Google は通すのに Agent Fleet が拒否する** — Google ではなく許可リストの話です。§8 と
  [04「ログインできない」](06-diagnose.ja.md)へ。
- **外部の同意画面がテスト中のまま**だと、テストユーザーに登録したアカウントしか通れません。
  それ以外は CP に届く前に Google が止めます。
- 個人の `gmail.com` アドレスも Google の検証は問題なく通ります。このボタンを自社に閉じるのは
  `AF_OAUTH_ALLOWED_DOMAINS`（または provider 単位の `AF_OIDC_*_ALLOWED_DOMAINS`）であって、
  「IdP が Google だから」ではありません。

## 4. Microsoft Entra ID

### 4.1 Entra 管理センター側で作るもの

**Microsoft Entra ID → アプリの登録 → 新規登録**で:

1. 名前を付け（同意画面に出ます）、*サポートされているアカウントの種類*で**この組織
   ディレクトリのみに含まれるアカウント**（シングルテナント）を選ぶ。§4.2 で issuer を固定する
   のと対になる設定で、ここでマルチテナントを選ぶことが「CP が起動しない」の原因になります。
2. リダイレクト URI を**Web** プラットフォームで追加し、§1 の URI を入れる。プラットフォームの
   種類は効きます: *シングルページ アプリケーション*として登録すると client secret による
   コード交換ができず、しかも失敗するのは最初のサインインのときです。
3. **証明書とシークレット → 新しいクライアント シークレット。** 表示された**値（Value）**を
   その場でコピーする（一度しか表示されません。隣の *シークレット ID* は secret ではありません）。
   **有効期限を控えてカレンダーに入れてください** — 期限切れは Agent Fleet 側に何の異常も無い
   まま、このボタン経由の全サインインをトークン交換で失敗させます。
4. 概要から **ディレクトリ (テナント) ID** と **アプリケーション (クライアント) ID** の 2 つの
   GUID を控える。
5. 必要な委任アクセス許可は標準の OIDC のもの（`openid` / `email` / `profile`）です。ユーザー
   同意を無効にしているディレクトリでは、管理者が一度同意を与える必要があります。

### 4.2 控えた値の入れ先

```sh
AF_OIDC_PROVIDERS=entra
AF_OIDC_ENTRA_ISSUER=https://login.microsoftonline.com/<directory-tenant-id>/v2.0
AF_OIDC_ENTRA_CLIENT_ID=<application-client-id>
AF_OIDC_ENTRA_CLIENT_SECRET=<手順 3 の「値」>
AF_OIDC_ENTRA_TRUST=issuer
AF_OIDC_ENTRA_LABEL_JA=Microsoft でサインイン
AF_OIDC_ENTRA_LABEL_EN=Sign in with Microsoft
```

- **列挙した id が、他の変数名の作り方を決めます。** `AF_OIDC_PROVIDERS=entra` なら
  `AF_OIDC_ENTRA_*` です。id に使えるのは `a-z 0-9 - _`（32 文字以内・先頭は英数字）で、`-` は
  変数名では `_` になります — id が `entra-id` なら `AF_OIDC_ENTRA_ID_*` を読みます。この id は
  テナントの**サインイン方式**規則に書く名前でもあるので、短く安定した値にしてください。
- **`AF_OIDC_<ID>_TRUST` に既定値はありません（意図的です）。** 許可リストがメールアドレスで
  書かれている以上、「その IdP のメールアドレスをなぜ信じてよいか」を宣言してもらう必要が
  あるためです。`email_verified` は IdP 自身が検証済みと言うアドレスだけを受理し、`issuer` は
  issuer が単一ディレクトリに固定済みなのでそのディレクトリのアドレスを正とする、という意味です。
  **Entra は `email_verified` をそもそも出さない**ので、Entra では `issuer` が正解です
  （`email_verified` にすると、Entra からのサインインは全部「未検証」で拒否されます）。
  `_TRUST` が無い provider は推測されず、起動時に無効化されます。
- **issuer は自社のテナント GUID に固定してください。** `/common/` `/organizations/`
  `/consumers/` の issuer を使うと、**Microsoft アカウントを持つ全人類**がログイン画面に立てます。
  しかも個人 Microsoft アカウントは自分の email を付け替えられるため、許可リストが意味を失います。
  これらのエンドポイントでは、受け入れるテナント GUID を `AF_OIDC_ENTRA_ALLOWED_TIDS` に列挙
  しない限り **CP は起動を拒否します**。IdP 設定の誤りのうち、ボタンが消えるだけでなく致命的に
  なるのはこれだけです。
- 任意（普段は不要）: `AF_OIDC_ENTRA_SCOPES`（既定 `openid email profile`）と
  `AF_OIDC_ENTRA_PROMPT`（既定 `select_account`。`none` にするとパラメータ自体を送らないので、
  受け付けない IdP に使えます）。`AF_OIDC_ENTRA_ALLOWED_EMAILS` / `_ALLOWED_DOMAINS` はこの
  provider だけの許可リストで、設定するとデプロイ共通の許可リストに**足すのではなく置き換えます**。

CP は `<issuer>/.well-known/openid-configuration` を、このボタンで最初にサインインされた
ときに読み（24 時間キャッシュします）。したがって issuer の誤りは起動時には出ず、最初に
ボタンが押された瞬間にエラーとして出ます。

### 4.3 同じディレクトリを 2 つのアプリ登録で使う場合

同じ Entra ディレクトリを 2 つのアプリ登録で使うと（このデプロイのボタンと、自前の方式を登録した
テナント（§7）のボタン、など）、**Entra の `sub` はアプリ登録ごとに違う値**になるため、同じ人が
どちらのボタンを押したかで別人＝別アカウント・別ワークスペースになります。安定したクレームを
指定すると同じ人になります。

```sh
AF_OIDC_ENTRA_LINK_CLAIM=oid
```

`oid` はそのディレクトリでのその人のオブジェクト ID で、どのアプリ登録でも同じ値であり、
本人にも選べません。この「選べない」が肝心なところで、他の値を指定する前に §7.1 の警告を
読んでください。なぜこれが `sub` の**差し替えではなく追加**なのかは
ADR 0043（決定 38）にあります。

### 4.4 よくある失敗

- **起動時に CP がマルチテナントのエンドポイントだと言って終了する** — §4.2 の issuer です。
- **Entra 自身のエラー画面に `AADSTS…` のコードが出る。** 実際に当たるのは 2 つで、client
  secret が無効（**値**ではなく*シークレット ID* を貼った、または期限切れ）と、リダイレクト
  URI の不一致（プラットフォームの種類が違う、または 1 文字違う）です。どちらも Entra 側の
  設定で、`.env` をいじっても直りません。
- **CP のログに `no email claim from id_token or userinfo`** — そのアカウントにディレクトリ上の
  メールアドレスが無く、`preferred_username` / `upn` もアドレスの形をしていませんでした。アプリ
  登録で ID トークンに `email` のオプション クレームを追加するか、アカウントにメール属性を
  与えてください。
- **`discovery …: issuer mismatch (got "…")`** — 設定した issuer が IdP の名乗る issuer と違い
  ます。手で打たず、discovery ドキュメントからコピーしてください。
- **数か月動いていたのに全員が拒否される** — client secret の期限切れです。

## 5. GitHub

GitHub にはユーザーログイン用の OIDC が無いので、専用のアダプタと専用の設定になります。
サインインを認可するのは、**指定した組織のアクティブなメンバーであること**であって、
GitHub アカウントを持っていることではありません。

### 5.1 GitHub 側で作るもの

1. **OAuth App** を作る（GitHub App ではありません）: *Settings → Developer settings → OAuth
   Apps → New OAuth App*。個人ではなく組織に持たせたいなら、**組織**の側で作ります。
2. **Authorization callback URL** に §1 の URI を設定する。Homepage URL は Console の URL で
   構いません。
3. **client secret** を生成してコピーする（一度しか表示されません）。
4. **組織側でサードパーティ アプリケーションのアクセスを制限している場合、その OAuth App を
   組織のオーナーが承認する必要があります** — 組織の設定の、サードパーティ アプリケーションの
   アクセス ポリシーの欄です。承認前はメンバーシップが見えず、**設定が完全に正しく見えるのに
   全員が拒否されます**。§5.3 の罠です。

OAuth App は、Console の GitHub **接続**ボタンが使っているもの（`GITHUB_OAUTH_CLIENT_ID`）と
同じで構いません。コールバック URL を足すだけです。スコープは認可のたびに与えられるので、
1 つのアプリが git の device flow（`repo`）とログイン（`read:org user:email`）の両方を、互いに
干渉せずに担えます。1 回の org 承認で両方を通したくない場合は別アプリにしてください。

### 5.2 控えた値の入れ先

```sh
AF_GITHUB_ALLOWED_ORGS=acme,acme-labs      # 必須。これがボタンを有効にする合図でもある
GITHUB_OAUTH_CLIENT_ID=<client-id>
GITHUB_OAUTH_CLIENT_SECRET=<client-secret>
AF_GITHUB_ALLOWED_DOMAINS=example.com      # 強く推奨（下記）
```

- **ボタンを有効にするのは `AF_GITHUB_ALLOWED_ORGS` であって client id ではありません。**
  `GITHUB_OAUTH_CLIENT_ID` を git 連携の device flow のためだけに使ってきたデプロイは今までどおり
  動き、頼んでもいないログイン機能について毎回警告されることもありません。この一覧が空だと
  ログに `github login disabled — AF_GITHUB_ALLOWED_ORGS is required` が出ます。
- ログイン**専用**の OAuth App を使いたい場合は `AF_GITHUB_LOGIN_CLIENT_ID` /
  `AF_GITHUB_LOGIN_CLIENT_SECRET` を設定します。こちらが `GITHUB_OAUTH_*` より優先されます。
- **`AF_GITHUB_ALLOWED_DOMAINS` も設定してください。** GitHub が CP に渡すのはアカウントの
  **primary かつ verified** なアドレスで、多くの人ではそれが個人用アドレスです。ここでは
  アドレスが違えば別人なので、本人のワークスペースではなく**新しい空のワークスペース**に
  着地してしまいます。入口で断る方が、意図しない場所で作業を始めてもらうより親切です。メールの
  一覧をどこにも設定しない場合は組織だけが入口の条件になり、CP は起動時にその旨を出します。
- 任意: `AF_GITHUB_ALLOWED_EMAILS`、`AF_GITHUB_LABEL_JA` / `_LABEL_EN`、
  `AF_GITHUB_MEMBERSHIP_TTL`（既定 `10m`）、`AF_GITHUB_MEMBERSHIP_GRACE`（既定 `1h`）。

### 5.3 先に知っておく挙動 2 つ

**org 承認の罠。** サードパーティ OAuth App を制限している組織は、未承認のアプリからは
メンバーシップを隠します。CP からは「メンバーでない」と「見る権限が無い」の区別がつきません。
結果として、設定した値がすべて正しいのに全員が拒否されます。ログには明示的に出ます。

```
WARNING: github: org "acme" returned 403 for a membership check — if the org restricts
third-party OAuth apps, an org owner must approve this OAuth app before anyone can sign in
```

**CP を再起動すると再サインインを求められます。** メンバーシップはリクエストのたびに GitHub API
で再判定するため、肯定結果は本人ごとに TTL の間キャッシュし、更新用のトークンを添えて保持して
います（更新時に GitHub へ到達できない場合、最後の肯定結果を grace の間だけ延命し、その後は
拒否します）。このキャッシュは**メモリ上**なので、再起動すると誰も再検証できません。そのため
「許可されていません」ではなく「サインインし直してください」になります。GitHub 側のセッションが
生きていれば、たいてい無操作で戻ります。仕様どおりで、故障ではありません。

### 5.4 よくある失敗

- **全員拒否・設定は正しく見える** — §5.3 の org 承認です。
- **特定の 1 人だけ拒否される** — その人の GitHub の primary verified アドレスが
  `AF_GITHUB_ALLOWED_DOMAINS` の外にあるか、組織のメンバーシップが *pending* のままです。
  会社のアドレスを GitHub で verify して primary にしてもらうか、別のボタンを使ってもらいます。
- **「the GitHub account has no primary verified email address」** — 文字どおり、渡せる
  検証済みの主アドレスがアカウントに無い状態です。
- **GitHub のユーザー名を変えた人が何も失わなかった** — 正しい挙動です。手放されたユーザー名は
  他人が取得できてしまうため、身元は数値のアカウント ID で紐づけています。

## 6. Okta / Keycloak / Auth0 / Cognito / GitLab などの OIDC IdP

いずれも同じ汎用 OIDC クライアントに載るので、手順は §4 の issuer 違いです。§1 のリダイレクト
URI で**機密クライアントの Web アプリ**を登録し、client ID と secret を控えて、`.env` に 5 行
足します。

```sh
AF_OIDC_PROVIDERS=okta
AF_OIDC_OKTA_ISSUER=https://<your-org>.okta.com
AF_OIDC_OKTA_CLIENT_ID=<client-id>
AF_OIDC_OKTA_CLIENT_SECRET=<client-secret>
AF_OIDC_OKTA_TRUST=email_verified
```

IdP の管理画面で何を探せばよいかの目安として、issuer の形:

| IdP | issuer の形 |
|---|---|
| Okta | `https://<org>.okta.com`、カスタム認可サーバーを使う場合は `https://<org>.okta.com/oauth2/<authorization-server-id>` |
| Keycloak | `https://kc.example.com/realms/<realm>` |
| Auth0 | `https://<tenant>.<region>.auth0.com/` |
| Cognito | `https://cognito-idp.<region>.amazonaws.com/<user-pool-id>` |
| GitLab | `https://gitlab.com`、または自社運用のベース URL |

- **issuer はブラウザのアドレス欄からではなく、その IdP の discovery ドキュメントから取って
  ください。** CP はこの値に `/.well-known/openid-configuration` を足して読み、その文書の中の
  `issuer` が同じ値であることを要求します。食い違うと
  `discovery …: issuer mismatch (got "…")` でサインインが失敗します。末尾スラッシュの有無は
  不一致になりません（両側とも外して比較します）。
- **`_TRUST`**: その IdP が実際に `email_verified` を出すなら `email_verified`（多くの Okta /
  Keycloak / Auth0 は出します）。出さないが issuer が単一ディレクトリに固定されているなら
  `issuer`。判断がつかないときは `email_verified` から始めてください。外した場合の結果は
  「ログに理由が残るサインイン拒否」であって、「通ってはいけないものが通る」ではありません。
- `https` が必要です。`http` はローカルの Keycloak や Dex を開発中に使えるよう
  `localhost` / `127.0.0.1` / `::1` にだけ許され、その場合 CP は警告を出します。
- 複数の id を列挙すればボタンも増えます: `AF_OIDC_PROVIDERS=entra,okta`。

## 7. テナントが自分で定義するサインイン方式

テナントによっては、自前の身元の源を持っています。Entra ID（や Okta / Keycloak）のテナント自体が
違い、issuer も client ID も secret も別、あるいは自社の IdP の代わりに GitHub の組織、という形です。
分かりやすいのはグループ子会社ですが、統合途中の会社、業務委託先、単に自前のディレクトリを
運用している部門でも同じことが起きます。そのたびに `.env` を編集して CP を再起動する代わりに、
そのテナントの管理者が Console から登録し、**あなたが承認します**。ここでは一切再起動が要りません。

場所は、テナント側の管理者なら**テナント設定 → ログイン → サインイン方式**（アカウント
メニューの「テナント設定」）。デプロイ管理者は**管理 → そのテナント → サインイン方式**から
同じ欄を開けるほか、レールの**サインイン方法の登録簿**（「テナント定義のサインイン方法」）から
全テナント分の承認・停止を直接打てます。

> そもそもテナントを分けるかどうか、兼務者がいる場合にテナントのログイン規則がどう絡むかは
> 「判断」の話なので、[01-install §4](02-install.ja.md) に置いてあります。

### 7.1 テナント管理者が埋める欄

相手が自社の IdP で行う作業は §3〜§6 と同じです。ただし**登録するコールバック URL は
あなたのもの**（§1 の `https://<PUBLIC_DOMAIN>/oauth2/callback`、同じ 1 本）です。ここが
いちばん間違えられます — 相手の Console ではなく、あなたの Console の URL です。

| 欄 | 何を入れるか |
|---|---|
| **名前** | このテナント内での識別子（`a-z 0-9 - _`）。例: `entra`。デプロイ全体での id は `t:<tenant-slug>:<name>` になる |
| **サインインの種類** | *自社の IdP*（OIDC）か *GitHub の組織*（§7.3） |
| **issuer（発行者 URL）** | OIDC のときのみ。§4.2 と同じ規則（tenant id を伴わない `common` / `organizations` の拒否も同じ） |
| **client_id / client_secret** | 自社のアプリ登録のもの。secret は保存時に暗号化され、二度と表示されません。後から編集するとき空のままにすれば保存済みの値が保たれます |
| **email の信頼方法** | *issuer が自社テナントに固定されている*（Entra）か *IdP が email_verified を返す* |
| **受け入れるメールドメイン** | **必須。** テナント定義の方式はデプロイ共通の許可リストにフォールバックしないので、空は「全員」ではなく「誰も入れない」です。この issuer が名乗ってよいアドレスの範囲でもあります |
| **許可する tenant id** | Entra の `tid`。issuer がマルチテナントのエンドポイントのときは必須 |
| **同一アカウントの見分け方** | 同じ issuer をこのデプロイ側が別のアプリ登録で既に使っている場合の `oid`（§4.3）。テナントが指定できるのはこの値だけです。★ **その状況では保存時に必須**です — 未指定だと「すでに別のサインイン方法で使われています」で既存の利用者が全員弾かれるため、CP が保存を拒否します |
| **ボタンの文言** | 任意。既定の文言にも会社名が入るので、あなたのボタンと同じ文字のボタンにはなりません |

> **なぜテナントは `oid` しか指定できず、`AF_OIDC_<ID>_LINK_CLAIM` は任意のクレームを取るのか。**
> `oid` はディレクトリが割り当てる値で、誰にも選べません。`email` / `upn` /
> `preferred_username` のような**名乗れる**値だと、同じ issuer を共有する方式がその値を名乗る
> だけで**既存のアカウントに着地**できてしまいます。env の方を制限していないのは、それが
> あなた自身のデプロイについてのあなた自身の宣言だからで、`email` を指定してよいという意味では
> ありません。

### 7.2 承認の前に確認すること

新規登録も編集も、まず**承認待ち**で作られます。デプロイ管理者が有効化するまで、そのテナントの
ログイン画面にボタンは出ず、サインイン用のリンクを手で組み立てた相手にも**セッションは発行され
ません** — ボタンを隠すのは見た目の話で、ここでの見た目は強制力ではないからです。

この 1 段は形式的な手続きではありません。IdP の登録は「**その人が誰であるか**を宣言する」権限で、
このデプロイでは人はメールアドレスで識別されます — デプロイ全体で、しかも「誰がデプロイ管理者か」
も含めて。自分の IdP を単独で有効化できる管理者は、**あなたのアドレスを名乗るトークン**を自分で
発行できてしまいます。

承認前に行を見て確認するのは 2 つです。

- **issuer がその会社自身のテナントであること** — `common` / `organizations` ではないこと。
  （GitHub の行では issuer ではなく組織を見ます。issuer は全テナント `github.com` で、判断材料に
  なりません。）
- **受け入れるメールドメインがその会社のものであること。** 承認は**この範囲に対して**与えるもの
  です。この一覧が issuer の名乗ってよいアドレスを縛り、**1 つのドメインを持てるのは 1 テナント
  だけ**です（他テナントが押さえているドメインを含む行は
  `domain … is already claimed by the sign-in method of tenant …` で保存自体が拒否されます）。
  デプロイの他の人たちが使っているドメインを主張しているものは承認してはいけません。

その行が実際に動く方式として組み立てられない場合（issuer が不正、ドメインが空、client secret が
もう復号できない）、承認は理由つきで**拒否されます**。ボタンの出ない「承認済み」は、承認されて
いないものと見分けがつかないためです。

**承認済みの方式が承認待ちに戻る条件**: issuer・client ID・信頼方法・種類・同一アカウントの
見分け方の変更、およびドメイン／tenant id／GitHub 組織の**追加**。どれも「狭める」方向では戻り
ません。client secret の入れ替えでも戻りません（日常的な鍵の入れ替えのたびに再承認を要求すると、
入れ替えない運用を教えることになるためです）。**停止はいつでも**、テナント側の管理者からも
打てます — 止めることをあなた待ちにはしません。

承認済みのものも、承認者と承認日時つきで登録簿に残ります。空になるキューではなく、ときどき
読み直す**登録簿**だと考えてください。IdP は相手の会社の管理下にあり続け、セルフサインアップの
ような設定は承認の後からでも変えられます。

### 7.3 テナントが GitHub の組織を使う場合

種類に *GitHub の組織* を選ぶと、issuer の欄が**許可する GitHub 組織**に置き換わります。
`github.com` は全世界で 1 つの発行元なので、その組織のアクティブなメンバーであることが
「その会社の人である」根拠になります。

- **OAuth App はそのテナントが自分で用意します。** 自社 org に作り、コールバック URL は §1 の
  あなたの URI を登録します。あなたのアプリを共有してしまうと、テナントごとの org オーナーが「git の
  device flow でも使っているアプリ」を承認することになるためです。**あなたの `.env` に GitHub の
  設定は不要**で、env 側の GitHub ログインは無効のまま、テナントの分だけ動かせます。
- **§5.3 の org 承認の罠はここでも同じ**で、動く必要があるのはそのテナントの org オーナーです。
- **受け入れるメールドメインは GitHub でも必須**です。1 ドメイン 1 テナントの理由と、primary の
  GitHub アドレスが会社ドメイン外の人が既存ではなく新しいワークスペースに着地してしまう理由の
  両方からです。
- 同じ組織を 2 つのテナントが登録するのは構いません。誰がどちらに入るかを決めるのはメール
  ドメインの方で、そちらは 1 テナントに 1 つだからです。
- 同じ GitHub アカウントであれば、デプロイ共通の GitHub ボタンとテナントの GitHub ボタンの
  どちらから入っても**同じ人**として扱われます。

## 8. 動いたことを確認する

上から順に。各段は次の段が前提にしていることを教えてくれます。

1. **そもそもどの方式が組み立てられたか。**

   ```sh
   docker compose logs cp | grep "login providers:"
   ```

   有効なデプロイ共通の provider id が、ボタンの順に 1 行で出ます。ここに無い id はそもそも
   組み立てられていないので、ログイン画面をいくら眺めても理由は分かりません。

2. **なぜ 1 つ足りないのか。**

   ```sh
   docker compose logs cp | grep -i "login provider"
   ```

   `WARNING: login provider "…" disabled — …` の各行が、不足または不正だった設定名を名指しします
   （最も多いのは、意図的に既定値を持たない `AF_OIDC_<ID>_TRUST`）。Google と GitHub のアダプタは
   専用の行を出します（§3.3 と §5.2 に引用）。

3. **CP がそもそも起動しない。** 致命的にしてあるのは 2 つだけです。マルチテナントの Entra
   issuer（§4.2）と、`AUTH=oauth requires AF_COOKIE_SECRET, PUBLIC_BASE_URL and at least one
   login provider …` — 使える方式が 1 つも設定されていない、という意味です。

4. **`https://<PUBLIC_DOMAIN>/login` を開く。** ボタンの数を数えます。手順 1 の id の数だけ、
   Google が先頭、GitHub が最後。「サインイン方法が設定されていません」と出るなら、手順 1 が
   何も見つけていない状態です。
   ★ **既定テナント（`default`）で「ボタンに出す」を外した方式は、ここにも出ません。**
   このページは既定テナントのページとして描かれるためです。数が合わないときは、まず
   既定テナントのサインイン方法の面を見てください。
   ★ 一方、既定テナントの「**受け入れる**」の絞り込みはここには効きません（どのテナントにも
   属していない人の唯一の入口なので、ボタンが 0 になる経路を作らないため）。

5. **`https://<PUBLIC_DOMAIN>/login/<slug>` を開く。** そのテナントが「受け入れる」にした
   デプロイ共通のボタンから「**ボタンに出す**」を外したものを引き、最後にその
   テナント自身の**承認済み**の方式が並びます。承認済みなのにボタンが無い場合、理由は CP の
   ログに出ます。

   ```sh
   docker compose logs cp | grep -i "tenant login provider"
   ```

   （全部隠す指定は無視されるので、この画面が空になることはありません。存在しない slug は
   黙って汎用のページを出します。404 にしないのは意図的なので、追いかけないでください。）

6. **GitHub のときだけ**: 拒否について結論を出す前に
   `docker compose logs cp | grep "returned 403"` を見てください（§5.3）。

7. **通しで 1 回サインインする。** 入れるはずのアカウントで試します。**新しいワークスペースを
   作成しました**というページに着いたら、そのアドレスはこのデプロイが見たことのないアドレス
   です — 人が間違った場所で作業を始める前に出したい警告が、まさにこれです。アドレスの違う
   アカウント同士を後から 1 つにまとめることはできません。両方のアカウントを持っている人なら、
   **設定 → 個人設定 → アカウント → サインイン方法を追加**で本人が追加できます（同じアドレスを
   名乗る方式だけで、その方式自身の org / ドメイン条件も満たす必要があります）。

## 9. それでも動かないとき

症状から引く切り分けは[06-diagnose.md](06-diagnose.ja.md)の**「ログインできない」**
にあります（拒否され続ける、リダイレクト URI 不一致、ボタンが出ない、マルチテナント issuer、
GitHub 各種、平文 HTTP で Cookie が保存されない）。IdP ごとの失敗は上の §3.3・§4.4・§5.4 です。

**故障ではない**のに問い合わせが多い 2 つだけ、ここでも繰り返します: CP 再起動後に GitHub の
利用者が再サインインを求められること（§5.3）と、テナントの承認済み方式が素の `/login` には
ボタンを出さないこと（仕様です。そのボタンは `/login/<slug>` にだけ出ます）。
