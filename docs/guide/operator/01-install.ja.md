# 01. 初期構築

[English](01-install.md) | 日本語

初めてのデプロイを、判断ポイントを添えて順を追って説明します。**実際のコマンドは
[deploy/compose/README.md](../../../deploy/compose/README.md) の "Quick start" 節が正**です。ここでは
「各ステップで何を決め、何に注意するか」を日本語で補います。作業ディレクトリは `deploy/compose/`
です。全体像とこのガイドの位置づけは [README.md](README.ja.md) を先に読んでください。

## 0. 前提の確認

構築を始める前に、[README.md](README.ja.md) の「前提」で挙げた 4 点がそろっているか確認します。

- Docker Engine + `docker compose` が動く Linux ホスト。
- 公開ドメインと、このホストを指す DNS の A/AAAA レコード（TLS 用）。社内限定なら §4 の判断を参照。
- ログイン用 IdP のクライアント（§3 で作成）。Google OAuth 2.0 Web クライアント、または
  Microsoft Entra ID / Okta / Keycloak / Auth0 / Cognito / GitLab の OIDC アプリ登録。
- Claude シートは各メンバーが後から持ち込むので、構築時点では不要です。

## 1. 設定ファイルを用意する

`deploy/compose/.env.example` を `.env` にコピーして編集します（コマンドは runbook の "Quick start"）。
`.env` は git 管理外で、ここが**設定の単一ソース**です。各変数の意味・生成手順・注釈は
[.env.example](../../../deploy/compose/.env.example) 自体に詳しく書いてあります。索引が欲しいときは
[dev/09 §9.4](../../dev/09-deploy.md) を参照してください。

構築時に必ず埋める主なものは、公開 URL（`PUBLIC_DOMAIN` / `PUBLIC_BASE_URL`）、ログイン IdP の
クライアント ID/シークレット、ログイン許可リスト（`AF_OAUTH_ALLOWED_DOMAINS` など）、初期管理者
（`SUPER_ADMIN_EMAILS`）、データ保管先（`DATA_DIR`）、そして 2 つの秘密（次節）です。

## 2. 秘密を生成する — `AF_MASTER_KEY` はこの時点で金庫へ

`.env` には自分で生成する秘密が 2 つあります。生成コマンド（`/dev/urandom` から 32 バイトを
base64 化）は runbook の "Quick start" に載っています。

- **`AF_MASTER_KEY`** — すべての資格情報暗号の根（封筒暗号の master 鍵）。
- **`AF_COOKIE_SECRET`** — ログインセッション cookie の署名鍵。

> 最重要の判断: **`AF_MASTER_KEY` を生成したら、その場でパスワード金庫／シークレットマネージャに
> 控えを取り、データ領域とは別に独立して保管してください。** この鍵は `DATA_DIR` にもバックアップ
> アーカイブにも入りません（設計上、意図的に）。失うと、保存済みの全資格情報とすべての過去
> バックアップが**永久に復号不能**になります（crypto-shred）。リストアには「同じ鍵」が要ります。
> 詳細は [03-security.md](03-security.ja.md) と [dev/07 §7.6](../../dev/07-security.md)。

あわせて、CP が使う `DOCKER_GID` をホストの docker グループ GID に合わせます（値の求め方は
runbook）。これを間違えると起動後に docker ソケットで permission denied になります（[04](04-troubleshooting.ja.md)）。

## 3. ログイン IdP を設定する

IdP 側の**承認済みリダイレクト URI** に次を登録します。有効にする provider が何個でも、
登録するのは**この 1 本だけ**です。

```
https://<PUBLIC_DOMAIN>/oauth2/callback
```

このパスは `<PUBLIC_BASE_URL>/oauth2/callback` と一致していなければなりません。ここがズレると
ログイン時に "redirect URI mismatch" になります（よくある失敗・[04](04-troubleshooting.ja.md)）。

**Google** — Google Cloud Console で OAuth クライアント ID（Web アプリケーション）を作成し、
発行されたクライアント ID/シークレットを `.env` の `GOOGLE_OAUTH_CLIENT_ID` /
`GOOGLE_OAUTH_CLIENT_SECRET` に入れます。

**Microsoft Entra ID / Okta / Keycloak / Auth0 / Cognito / GitLab** — IdP 側で機密クライアントの
Web アプリを登録し、`.env` に次を書きます。

```sh
AF_OIDC_PROVIDERS=entra
AF_OIDC_ENTRA_ISSUER=https://login.microsoftonline.com/<tenant-guid>/v2.0
AF_OIDC_ENTRA_CLIENT_ID=<application-client-id>
AF_OIDC_ENTRA_CLIENT_SECRET=<client-secret-value>
AF_OIDC_ENTRA_TRUST=issuer
AF_OIDC_ENTRA_LABEL_JA=Microsoft でサインイン
AF_OIDC_ENTRA_LABEL_EN=Sign in with Microsoft
```

このうち 2 つは、コピーする前に読んでおく価値があります。

- **`_TRUST` に既定値はありません（意図的です）。** 許可リストがメールアドレスで書かれている以上、
  「その IdP のメールアドレスをなぜ信じてよいか」を宣言してもらう必要があるためです。
  `email_verified` は IdP 自身が検証済みと言うアドレスだけを受理します（Google、多くの Okta /
  Keycloak / Auth0）。`issuer` は issuer が単一テナントに固定済みなので、そのディレクトリの
  アドレスを正とする、という意味です。**Entra ID は `email_verified` をそもそも出さない**ので、
  Entra では `issuer` が正解です。`_TRUST` が無い provider は推測されず、起動時に無効化されます。
- **Entra の issuer は自社のテナント GUID に固定してください。** `/common/` や
  `/organizations/` のエンドポイントを使うと、**Microsoft アカウントを持つ全人類**がログイン画面に
  立てます。しかも個人 Microsoft アカウントは自分の email を付け替えられるため、許可リストが
  意味を失います。これらのエンドポイントでは、受け入れるテナントを
  `AF_OIDC_ENTRA_ALLOWED_TIDS` に列挙しない限り CP は起動を拒否します。

**GitHub** — GitHub にはユーザーログイン用の OIDC が無いので設定が別になります。ログインを許可
するのは、指定した **org のメンバーであること**です。

```sh
AF_GITHUB_ALLOWED_ORGS=acme,acme-labs      # 必須。これがボタンを有効にする合図でもある
GITHUB_OAUTH_CLIENT_SECRET=<client-secret>
AF_GITHUB_ALLOWED_DOMAINS=example.com      # 強く推奨（下記）
```

- OAuth App は Console の GitHub **接続**ボタンが使っているもの（`GITHUB_OAUTH_CLIENT_ID`）と
  同じで構いません。上のリダイレクト URI を追加するだけです。ログイン用に別アプリを立てたい
  場合は `AF_GITHUB_LOGIN_CLIENT_ID` / `AF_GITHUB_LOGIN_CLIENT_SECRET` を設定します。
  `GITHUB_OAUTH_CLIENT_ID` だけを設定した状態は、これまでどおり git 連携の device flow のみを
  意味します。
- **org 側でサードパーティ OAuth App を制限している場合、org のオーナーによる承認が必要です。**
  承認前はメンバーシップが見えず、**設定が完全に正しく見えるのに全員が拒否されます**。
- **`AF_GITHUB_ALLOWED_DOMAINS` も設定してください。** GitHub が CP に渡すのはアカウントの
  **primary かつ verified** なアドレスで、多くの人ではそれが個人用アドレスです。ここでは
  アドレスが違えば別人なので、本人のワークスペースではなく**新しい空のワークスペース**に
  着地してしまいます。入口で断る方が、意図しない場所で作業を始めてもらうより親切です。
- メンバーシップは GitHub API で再判定し、1 人ごとに `AF_GITHUB_MEMBERSHIP_TTL`（10 分）
  キャッシュします。GitHub に到達できないときは、最後の肯定結果を
  `AF_GITHUB_MEMBERSHIP_GRACE`（1 時間）だけ延命します。このキャッシュはメモリ上にあるため、
  **CP を再起動すると再サインインを求められます**（拒否ではありません。GitHub 側のセッションが
  生きていれば、たいてい無操作で戻ります）。

`AF_OIDC_PROVIDERS=entra,okta` のように複数列挙すればボタンも複数出ます。ログイン画面には有効な
provider の数だけボタンが出て、1 つだけなら現行とまったく同じ見た目です。設定が不完全な provider
は CP のログに警告を出して無効化されるだけで（1 つの IdP の設定ミスで全社が締め出されないため）、
CP が起動を止めるのは**有効な provider が 1 つも無いとき**だけです。手順の詳細は runbook の
"Login IdP setup" 節。

補足: Console のログイン認証（L1）は CP 自身が OAuth/OIDC を実行します（`AUTH=oauth`・既定）。
既存の認証ゲートウェイ（oauth2-proxy / ALB OIDC など）を前段に置く社は `AUTH=proxy` を選べます
（メール識別を上流ヘッダに委ねる）。**SAML のみの IdP（HENNGE One / TrustLogin / CloudGate など）の
正式な答えもこれ**で、oauth2-proxy や Keycloak でブリッジします。仕組みは
[dev/07 §7.3](../../dev/07-security.md)。GitHub/Bitbucket の連携 OAuth は任意で、無くても
トークン貼り付けで動くため初期構築では省略できます。

## 4. 判断ポイント

起動前に、自分のデプロイに合わせて 3 つを決めます。

### `tls internal` はいつ使うか

Caddy は既定で公開ドメインの証明書を Let's Encrypt から自動取得します。これには**公開 DNS と
80/443 の到達性**が要ります。社内限定・閉域網で公開 DNS を用意できない場合は、Caddyfile の
代替（`tls internal`・自己署名）に切り替えます。この場合ブラウザに証明書警告が出るので、社内
CA の配布などは別途検討してください。切替方法は runbook の "Quick start" 脚注と Caddyfile を参照。
既存の TLS 終端プロキシを前段に持つ社は Caddy サービス自体を外せます（Caddyfile 代替2）。

### `AF_PROVISION` は auto か invite か

- **`auto`（既定）** — 許可リストを通ったログインを、既定テナントのメンバーとして自動受け入れ。
  少人数・ドメイン単位で許可する運用に向きます。
- **`invite`** — 未知の identity は管理者が Admin パネルで追加するまで拒否。誰を入れるかを
  一件ずつ統制したいときに選びます。

`invite` では、**招待されたこと自体がログインできる資格**になります。Admin パネルで追加した人は
`AF_OAUTH_ALLOWED_*` に載っていなくても入れるので、名簿は 1 箇所（membership）で済み、ずれる
2 つ目のリストを持たずにすみます。`auto` では、そもそも誰が入れるかを決めるのは許可リストで、
それを通った人は全員が既定テナントに入ります。

**最初から `invite` で立ち上げて構いません** — `SUPER_ADMIN_EMAILS` のアカウントは、自分の
membership が 1 つも無くても Admin パネルに到達できます。

### 単一テナントか、分離するか

- **単一テナント（既定）** — 全員が組み込みの `default` テナントに入り、摩擦ゼロ。多くの社は
  これで十分です。
- **テナント分離** — 部署間などで**ハードな分離**が要るときだけ追加します。メンバーシップごとに
  完全に隔離された Workspace が割り当てられます。後からでも追加できるので、迷ったら単一で始めて
  必要になってから分けるのが無難です。

分離したあとは、テナントごとにログイン規則を持たせられます（管理 → 該当テナント → **ログイン規則**）。

| 設定 | 何をするか | どこで効くか |
|---|---|---|
| **使えるサインイン方法** | 有効な IdP のうち、このテナントに入るのに使ってよいもの | ボタンを隠すだけでなく、**毎リクエスト**強制されます |
| **自動参加ドメイン** | このドメインのアドレスは初回サインインでこのテナントに参加 | 1 ドメインにつき 1 テナントだけ |
| **招待できるドメイン** | メンバーとして**追加**してよい相手の上限 | 追加フォームのみ。毎リクエストの制約ではありません |

**使えるサインイン方法**に書くのは `.env` の provider id（`AF_OIDC_PROVIDERS` と Google）で、
書ける id は欄のすぐ下に並びます — このデプロイが持っている id と、そのボタンの文言、
向き先の issuer です。持っていない id は保存時に弾かれますが、そもそも `.env` を見に行かずに
選べます。テナント自身の承認済みの方法は、同じ欄に `t:テナント名:方法名` の形で書けます。

テナントごとに専用のサインインページ `https://<PUBLIC_DOMAIN>/login/<slug>` も付き、そのテナントが
受け付ける方法だけが並びます。新しいメンバーにはこの URL を伝えてください（招待メールはありません
— CP に SMTP を持たせない方針です）。

> **「招待できるドメイン」は「このテナントを使ってよいドメイン」ではありません。** 名簿に載せて
> よい相手の上限を縛るだけです。既にメンバーの人は別ドメインでもそのまま使えます — 業務委託の
> アドレスが成立するのはそのおかげです。アクセスを終わらせる手段はこの欄を狭めることではなく、
> **メンバーを外すこと**です。

### 自前の IdP を持つ子会社の場合

テナントが別法人（グループ子会社・統合途中の会社）の場合、Entra ID（や Okta / Keycloak）の
テナント自体が違い、issuer も client ID も secret も別になります。子会社が増えるたびに `.env` を
編集して CP を再起動する代わりに、そのテナントの管理者が Console から登録できます —
**テナント設定 → サインイン方式**（アカウントメニューの「テナント設定」）。issuer・client ID・
client secret・email の信頼方法・受け入れるメールドメインを入力します。デプロイ管理者は
**管理 → そのテナント → 「このテナントのサインイン方法」**から同じ欄を開きます。

> **登録しただけでは動きません。** 新しいサインイン方法は「承認待ち」で作られ、デプロイ管理者が
> 有効化するまで、そのテナントのログイン画面にボタンは出ず、
> ログイン URL を手で組み立てた相手にもセッションは発行されません。
>
> **この 1 段は形式的な手続きではありません。** IdP の登録は「**その人が誰であるか**を宣言する」
> 権限で、このデプロイでは人はメールアドレスで識別されます — デプロイ全体で、しかも
> 「誰がデプロイ管理者か」も含めて。自分の IdP を単独で有効化できる管理者は、**あなたの
> アドレスを名乗るトークン**を自分で発行できてしまいます。承認は子会社あたり 1 回きりなので、
> 「部署のことは部署で完結する」という日常の運用像は変わりません。

承認の前に確認することは、**管理 → テナント**のデプロイ全体の一覧で 2 つだけです:

- **issuer** がその会社自身のテナントであること — `common` / `organizations` ではないこと
  （全世界の Microsoft アカウントを受け入れる指定で、tenant id を固定しない限り拒否されます）。
- **受け入れるメールドメイン**がその会社のものであること。承認は**この範囲に対して**与えるもので、
  この一覧がその issuer の名乗ってよいアドレスを縛ります。1 つのドメインを持てるのは 1 テナント
  だけです。親会社のドメインを主張しているものを承認してはいけません。

issuer・client ID・email の信頼方法の変更、および受け入れドメインの**追加**は、承認のやり直しに
なります（承認は「この issuer をこの範囲で信じてよい」に対して与えたものだからです）。停止は
いつでも、テナントの管理者からも打てます — 止めることをあなた待ちにはしません。

**承認して有効化**と**停止する**は、この一覧の下にある**登録簿**（「テナント定義のサインイン方法」）
の行から直接打てます。テナントの詳細画面を開いても同じ操作ができます。

承認済みのものも、承認者と承認日時つきで一覧に残ります。空になるキューではなく、ときどき
読み直す**登録簿**だと考えてください。IdP は相手の会社の管理下にあり続け、セルフサインアップの
ような設定は承認の後からでも変えられます。

## 5. 起動する

`.env` がそろったら `DATA_DIR` を作成し、`docker compose up -d`（プレビルド image を使うなら
そのまま、ローカルビルドなら `--build`）で起動します。正確なコマンドは runbook の "Quick start"。
起動後、CP のログを追い、ヘルスチェックが通ることを確認します。

```
curl -s http://127.0.0.1:8099/healthz    # -> ok
```

`ok` が返らない・そもそも CP が上がらないときは [04-troubleshooting.md](04-troubleshooting.ja.md) の
「CP が起動しない」を参照してください。

## 6. 初回ログインと最初の管理者

ブラウザで `https://<PUBLIC_DOMAIN>` を開き、`SUPER_ADMIN_EMAILS` に列挙したアカウントで
サインインします。**このメールアドレスが初回ログインで `super_admin`** になります。super_admin は
Console に盾アイコンの **Admin パネル**が見え、デプロイ全体を管理できます。

> ログインが常に拒否される場合、許可リストが空の可能性が高いです。3 系統（`AF_OAUTH_ALLOWED_EMAILS`
> / `_DOMAINS` / `_EMAILS_FILE`）が**すべて空で、かつまだ誰もテナントに招待されていないと全ログインを
> 拒否**します（fail-closed = 安全側に倒す設計）。新規設置の時点では当然まだ誰も招待されていないので、
> **最初の管理者を入れるために少なくとも 1 つは設定**してください。
> 詳細は [04](04-troubleshooting.ja.md)。

## 7. 最初のテナントとメンバー

super_admin として Admin パネルから、テナントの作成、メンバーの追加、資源上限やアイドル停止の
設定ができます。既定の単一テナント運用ならテナント作成は不要で、`AF_PROVISION=auto` なら許可
リスト内のメンバーはログインするだけで使い始められます。メンバー管理・上限・監査のブラウザ操作
そのものは、管理者向けの admin 分冊が扱います。

各メンバーは自分の Workspace を起動したあと、Console から**自分の Claude シートでログイン**します
（BYO）。運用者がメンバーの Claude 資格情報を代理設定することはありません。

構築後の日常運用（バックアップ・アップグレード・停止）は [02-operations.md](02-operations.ja.md) へ、
セキュリティ運用は [03-security.md](03-security.ja.md) へ進んでください。
