---
audience: "配備をはじめて建てる人"
source_of_truth: "コマンドは deploy/compose/README.md の「Quick start」、各変数の意味は deploy/compose/.env.example（食い違ったらこのページのバグ）"
---

# インストールする

[English](install.md) | 日本語

Docker Compose で最初の配備を建て、最初の管理者がサインインしてメンバーを迎えられるところまで進めます。
コマンドは [compose の runbook](../../deploy/compose/README.md) の「Quick start」にあります。このページには、各手順で何を決め、何に気をつけるかを書きます。

## 全体像

| あなたがやること | 起きること |
|---|---|
| `.env` を用意し、秘密を作る | Control Plane がこの値で起動し、保存する資格情報を `AF_MASTER_KEY` で暗号化する |
| IdP にアプリを登録し、許可リストを決める | サインイン画面に、有効な方法のボタンが並ぶ |
| 起動する | Control Plane と Caddy が動き出し、Caddy が TLS の証明書を取る |
| `SUPER_ADMIN_EMAILS` のアドレスでサインインする | そのアカウントが配備管理者（`super_admin`）になる |
| メンバーを迎える | メンバーがサインインすると、そのワークスペースを用意する |

作業するディレクトリは `deploy/compose/` です。ほかの形態は [デプロイ先を選ぶ](choose.ja.md) で選び、その runbook に従ってください。

## 始める前に

| 用意するもの | 補足 |
|---|---|
| Docker Engine と `docker compose` が動く Linux ホスト | 1 台で足ります |
| 公開ドメインと、このホストを指す DNS の A / AAAA レコード | TLS に使います。社内だけで使うなら [`tls internal` を使うか](install-decisions.ja.md#tls-internal-を使うか) を先に決めます |
| サインイン用の IdP のクライアント | 手順 3 で作ります。Google の OAuth 2.0 ウェブクライアント、または Entra ID・Okta・Keycloak・Auth0・Cognito・GitLab の OIDC アプリ |
| エージェントの席 | 各メンバーがあとから持ち込むので、いまは要りません |

## 1. 設定ファイルを用意する

`deploy/compose/.env.example` を `.env` にコピーして編集します。

- `.env` は git の管理外で、設定はすべてここに書きます
- 各変数の意味と作り方は、`.env.example` 自体に書いてあります

最初に必ず埋めるのは次のものです。

| 変数 | 入れるもの |
|---|---|
| `PUBLIC_DOMAIN`・`PUBLIC_BASE_URL` | 公開する URL |
| IdP のクライアント ID とシークレット | 手順 3 |
| `AF_OAUTH_ALLOWED_DOMAINS` など | サインインの許可リスト |
| `SUPER_ADMIN_EMAILS` | 最初の配備管理者のアドレス |
| `DATA_DIR` | データを置くディレクトリ。絶対パスはあとから変えにくいので、ここで決めます |
| `AF_MASTER_KEY`・`AF_COOKIE_SECRET` | 手順 2 |
| `DOCKER_GID` | ホストの docker グループの GID。求め方は runbook にあります |

`DOCKER_GID` を間違えると、起動したあとに docker のソケットで permission denied になります（[06 切り分ける](06-diagnose.ja.md)）。

## 2. 秘密を作る

`.env` には、自分で作る秘密が 2 つあります。

| 秘密 | 役目 |
|---|---|
| `AF_MASTER_KEY` | 資格情報を暗号化する、すべての鍵の根 |
| `AF_COOKIE_SECRET` | サインインのセッションの cookie に署名する鍵 |

どちらも **ランダムな 32 バイト以上** にします。パスフレーズは使わないでください。

```
head -c 32 /dev/urandom | base64
```

- これより短い値や、同じパターンを繰り返しただけの値でも起動はしますが、ログに `WARNING` が出て、管理画面に警告の帯が出ます。今後のリリースでは、その値での起動を拒否します
- 要件と、鍵を変えるときの代償は [04 守る](04-secure.ja.md#配備の鍵-要件と変更の代償) にあります

> [!IMPORTANT]
> **`AF_MASTER_KEY` は、作ったその場でパスワード金庫かシークレットマネージャに控え、データとは別に保管してください。**
> この鍵は `DATA_DIR` にもバックアップにも入りません。なくすと、保存した資格情報とすべてのバックアップが二度と復号できなくなります。
> 復元には同じ鍵が要ります。

## 3. サインインの IdP を設定する

IdP ごとの作り方・入れる値・確かめ方は [05 サインイン方式](05-signin.ja.md) にあります。そちらを終えてから、手順 4 に進んでください。

IdP の **承認済みのリダイレクト URI** には、次の 1 つだけを登録します。有効にする IdP がいくつあっても同じです。

```
https://<PUBLIC_DOMAIN>/oauth2/callback
```

`<PUBLIC_BASE_URL>/oauth2/callback` と完全に一致しないと、サインインのときに「redirect URI mismatch」になります。

IdP ごとに、`.env` で埋めるキーが違います。

| IdP | `.env` のキー |
|---|---|
| Google | `GOOGLE_OAUTH_CLIENT_ID`・`GOOGLE_OAUTH_CLIENT_SECRET`。`AF_OIDC_PROVIDERS` には書きません |
| Entra ID・Okta・Keycloak・Auth0・Cognito・GitLab | `AF_OIDC_PROVIDERS=<id>` と、`AF_OIDC_<ID>_ISSUER`・`_CLIENT_ID`・`_CLIENT_SECRET`・`_TRUST` |
| GitHub | `AF_GITHUB_ALLOWED_ORGS`（必須で、ボタンを出す合図にもなります）、`GITHUB_OAUTH_CLIENT_ID`・`_SECRET`、`AF_GITHUB_ALLOWED_DOMAINS` |

うまくいくかどうかは、次の 3 点で決まります。

- **`AF_OIDC_<ID>_TRUST` には既定値がありません。** その IdP のメールアドレスを信じてよい理由を書く欄で、書いていない IdP は起動のときに無効になります。Entra ID は `email_verified` を返さないので、`issuer` を指定します
- **Entra ID の issuer は、自社テナントの GUID に固定します。** `/common/` や `/organizations/` のエンドポイントでは、受け入れるテナントを `AF_OIDC_<ID>_ALLOWED_TIDS` に挙げない限り、Control Plane は起動を拒否します
- **GitHub の組織がサードパーティのアプリを制限していると、その組織による OAuth App の承認も要ります。** 承認されるまでは、設定が正しく見えても全員が拒否されます

複数の IdP を有効にしたとき:

- `AF_OIDC_PROVIDERS=entra,okta` のように並べると、サインイン画面にボタンが並びます
- 設定が足りない IdP は、ログに警告を出して無効になるだけです。1 つの設定ミスで全員が締め出されないためです
- Control Plane が起動を止めるのは、有効な IdP が 1 つもないときだけです

### 前段の認証ゲートウェイを使うとき

- サインインは、Control Plane 自身が OAuth / OIDC で行うのがおすすめです（`AUTH=oauth`）。`AUTH` には既定値がないので、必ず書きます
- oauth2-proxy や ALB の OIDC など、すでに認証ゲートウェイがあるなら `AUTH=proxy` を選べます。ゲートウェイが付けるヘッダのメールアドレスで、人を見分けます
- SAML しかない IdP（HENNGE One・TrustLogin・CloudGate など）も、oauth2-proxy や Keycloak でつないで `AUTH=proxy` で使います

### ここで設定しないもの

リポジトリをクローンするための、GitHub・Bitbucket の「OAuth で接続」のアプリは、配備ではなくテナントごとに、テナント管理者が Console で登録します。

- `.env` の `GITHUB_OAUTH_CLIENT_ID` は、サインイン用のアプリのことです
- `BITBUCKET_OAUTH_KEY`・`_SECRET` は読まれません
- なくてもトークンを貼り付ければ動くので、インストールのときは省けます。手順は [連携に使う OAuth アプリを選ぶ](../admin/access/oauth-apps.ja.md) にあります

## 4. テナントとプロビジョニングの方式を決める

起動する前に、次の 3 つを決めます。選び方は [テナントとプロビジョニングの方式を決める](install-decisions.ja.md) にあります。

- `tls internal` を使うか
- `AF_PROVISION` を `invite` にするか `auto` にするか
- テナントを 1 つにするか、分けるか

## 5. 起動する

1. `DATA_DIR` を作ります
2. `docker compose up -d` で起動します。ビルド済みのイメージを使わずにローカルでビルドするなら `--build` を付けます
3. Control Plane のログを追い、ループバックのポートの `/healthz` が `ok` を返すことを確かめます

確かめ方は runbook のトラブルシューティングの表にあります。

## 6. 最初の管理者としてサインインする

ブラウザで `https://<PUBLIC_DOMAIN>` を開き、`SUPER_ADMIN_EMAILS` に書いたアカウントでサインインします。

- このアドレスは、初回のサインインで配備管理者（`super_admin`）になります
- 配備管理者には、Console に盾のアイコンの **管理** が出ます。配備全体はここから管理します
- `SUPER_ADMIN_EMAILS` のアカウントは、どのテナントのメンバーでなくても **管理** に入れます。`AF_PROVISION=invite` で始めても困りません

> [!WARNING]
> **許可リストを少なくとも 1 つは埋めてください。**
> `AF_OAUTH_ALLOWED_EMAILS`・`_DOMAINS`・`_EMAILS_FILE` がすべて空で、まだ誰もテナントに招待されていないと、すべてのサインインを拒否します。
> インストールした直後は誰も招待されていないので、最初の管理者も入れません。

## 7. メンバーを迎える

**管理** から、テナントの作成、メンバーの追加、上限やアイドル停止の設定ができます。

- テナントを 1 つで使うなら、テナントを作る必要はありません
- `AF_PROVISION=auto` なら、許可リストに入っているメンバーはサインインするだけで使い始められます
- 各メンバーは、自分のワークスペースを起動したあと、Console で **自分のエージェントの席でログイン** します。運用する人がメンバーの資格情報を代わりに設定することはありません
- メンバーの管理・上限・監査の画面の操作は、[テナントを管理する](../admin/README.ja.md) にあります

### テナントの slug に使えない名前

テナントのディレクトリと、既定テナントのメンバーのホームは、データのディレクトリ（`WS_DATA`）の直下に、Control Plane 自身のファイルと並んで置かれます。そのため、名前がぶつかるテナントやメンバーは作れません。

| 作ろうとしたもの | ぶつかる名前 | エラー |
|---|---|---|
| テナント | 配備が使う名前（`git`・`shared`・`caddy`・`drawio-stencils`・`guide-staged`、データベースと鍵のファイル） | `tenant_slug_reserved` |
| テナント | 既定テナントのメンバーのユーザーキー（メールアドレスを整えたもの。例: `alice-example-com`） | `tenant_slug_conflict` |
| 既定テナントのメンバー | 配備が使う名前 | `user_key_reserved` |
| 既定テナントのメンバー | テナントの slug | `user_key_conflict` |

- 既定テナントに追加できない人は、別のテナントに招待してください
- この確認より前に保存された名前はそのまま残り、Control Plane の起動のときにログに `data root name collision` と出ます

## 不要になったテナントを削除する

**管理** → テナントを開く →「上限・自動停止」の下の **テナントを削除** を押します。配備管理者だけが削除できます。

削除できるのは、**すでに空になったテナントだけ** です。次の順に片付けてください。

1. 内部 git リポジトリを削除する
2. メンバーを外す
3. そのワークスペースを破棄する
4. テナントを削除する

- メンバー・ワークスペース・内部 git リポジトリが 1 つでも残っていると、削除を拒否します。データベースの行が、ホームや EBS ボリューム、リポジトリへの唯一の手がかりのことがあるからです。先に消すと、実体だけが課金され続け、誰もたどれなくなります
- 内部 git リポジトリは、メンバーが名簿に残っているうちに削除してください。削除する画面にはメンバーとしてしか入れないので、最後の 1 人を外すと誰もたどり着けなくなります
- 監査ログ・クラウドの費用・稼働時間は残ります。テナントの欄が空になるだけです
- `golden snapshot (system)` というテナントは一覧に出ず、削除もできません。配備自身のもので、自動で作り直されます

## うまくいかないとき

| 症状 | 見るところ |
|---|---|
| サインインで「redirect URI mismatch」 | IdP に登録した URI が `<PUBLIC_BASE_URL>/oauth2/callback` と完全に一致しているか（手順 3） |
| サインインがいつも拒否される | 許可リストがすべて空ではないか（設定は手順 1、理由は手順 6 の警告） |
| 起動後、docker のソケットで permission denied | `DOCKER_GID`（手順 1） |
| `/healthz` が `ok` を返さない、Control Plane が上がらない | [06 切り分ける](06-diagnose.ja.md#症状別確認表) の「CP が起動しない」 |

## 関連

- [テナントとプロビジョニングの方式を決める](install-decisions.ja.md)
- [05 サインイン方式](05-signin.ja.md)
- [バックアップして復元する](run/backup-restore.ja.md)・[アップグレードする](run/upgrade.ja.md)・[アイドル停止と強制停止を使う](run/idle-stop.ja.md)
- [04 守る](04-secure.ja.md)
