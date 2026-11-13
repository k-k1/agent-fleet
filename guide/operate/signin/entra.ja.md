---
audience: "配備に IdP をつなぐ配備管理者（ホストの端末と `.env` を扱う人）"
source_of_truth: "`.env` の読み込みは Control Plane のサインイン設定（記述がコードと食い違ったら、このページのバグ）"
---

# Microsoft Entra ID でサインインする

[English](entra.md) | 日本語

Microsoft Entra ID のアカウントでこの配備にサインインできるようにします。Entra でアプリを登録し、
issuer を自社のディレクトリに固定して `.env` に書きます。

## 全体像

| あなたがやること | Control Plane がやること |
|---|---|
| Entra 管理センターでアプリを登録し、シークレットを作る | |
| issuer・クライアント ID・シークレットを `.env` に書き、起動し直す | issuer が複数テナント向けのエンドポイントで、受け入れるテナントの指定がなければ起動を止める |
| | 最初にボタンが押されたとき、issuer の discovery ドキュメントを読む（24 時間キャッシュする） |
| シークレットの期限を管理する | |

## Entra 管理センターでアプリを登録する

**Microsoft Entra ID → アプリの登録 → 新規登録** で、次の手順を行います。

1. 名前を付けます（同意画面に出ます）。**サポートされているアカウントの種類** は **この組織ディレクトリのみに含まれるアカウント**（シングルテナント）を選びます。ここで複数テナントを選ぶと、Control Plane が起動しなくなる原因になります
2. リダイレクト URI を **Web** のプラットフォームで追加し、`https://<PUBLIC_DOMAIN>/oauth2/callback` を入れます（[リダイレクト URI を登録する](overview.ja.md#リダイレクト-uri-を登録する)）
   - **シングルページ アプリケーション** で登録すると、client secret でのコード交換ができません。しかも、失敗がわかるのは最初にサインインしたときです
3. **証明書とシークレット → 新しいクライアント シークレット** で作り、表示された **値** をその場で控えます
   - 値は一度しか表示されません。隣の **シークレット ID** はシークレットそのものではありません
   - **有効期限をカレンダーに入れておきます。** 期限が切れると、Agent Fleet 側には何も起きていないまま、このボタンからのサインインは全員、トークン交換で失敗します
4. 概要から **ディレクトリ (テナント) ID** と **アプリケーション (クライアント) ID** を控えます
5. アクセス許可は、標準の OIDC のもの（`openid`・`email`・`profile`）で足ります。ユーザーの同意を止めているディレクトリでは、管理者が一度同意します

## .env に書く

```sh
AF_OIDC_PROVIDERS=entra
AF_OIDC_ENTRA_ISSUER=https://login.microsoftonline.com/<directory-tenant-id>/v2.0
AF_OIDC_ENTRA_CLIENT_ID=<application-client-id>
AF_OIDC_ENTRA_CLIENT_SECRET=<手順 3 の「値」>
AF_OIDC_ENTRA_TRUST=issuer
AF_OIDC_ENTRA_LABEL_JA=Microsoft でサインイン
AF_OIDC_ENTRA_LABEL_EN=Sign in with Microsoft
```

| キー | 決めること |
|---|---|
| `AF_OIDC_PROVIDERS` | IdP の id の一覧。id がほかのキーの名前を決めます（`entra` なら `AF_OIDC_ENTRA_*`） |
| `AF_OIDC_<ID>_ISSUER` | 自社のディレクトリの GUID を含む issuer |
| `AF_OIDC_<ID>_TRUST` | メールアドレスを信じる理由。Entra では `issuer` |
| `AF_OIDC_<ID>_LABEL_JA`・`_LABEL_EN` | ボタンの文言 |

- **id** に使えるのは `a-z 0-9 - _` で、32 文字以内、先頭は英数字です。キーの名前では `-` が `_` になります（`entra-id` なら `AF_OIDC_ENTRA_ID_*`）。テナントのサインイン方法の一覧にもこの id が出るので、短く変わらない値にします
- **`_TRUST` に既定値はありません。** 許可リストはメールアドレスで書くので、「その IdP のアドレスをなぜ信じてよいか」を宣言してもらうためです。未設定の IdP は推測せず、起動時に外します
  - `email_verified`：IdP 自身が確認済みとしたアドレスだけを受け取ります
  - `issuer`：issuer が 1 つのディレクトリに固定されているので、そのディレクトリのアドレスを正しいものとします
  - **Entra は `email_verified` を出しません。** `email_verified` にすると、Entra からのサインインはすべて「未確認」で拒否されます
- **issuer は自社のディレクトリの GUID に固定します。** `/common/`・`/organizations/`・`/consumers/` の issuer では、Microsoft アカウントを持つ誰もがサインイン画面まで進めます。個人の Microsoft アカウントは自分でメールアドレスを付け替えられるので、許可リストも意味がなくなります
  - これらの issuer を使うときは、受け入れるディレクトリの GUID を `AF_OIDC_<ID>_ALLOWED_TIDS` に並べます。並べていないと、Control Plane は起動を拒否します

書いたら Control Plane を起動し直し、[動いたことを確かめる](overview.ja.md#動いたことを確かめる) の手順で確認します。
issuer の誤りは起動時には出ず、最初にボタンが押されたときにエラーとして出ます。

### ふだんは要らない設定

| キー | 既定 | 使うとき |
|---|---|---|
| `AF_OIDC_<ID>_SCOPES` | `openid email profile` | 要求するスコープを変えるとき |
| `AF_OIDC_<ID>_PROMPT` | `select_account` | `none` にするとこのパラメーターを送りません。受け付けない IdP に使います |
| `AF_OIDC_<ID>_ALLOWED_EMAILS`・`_ALLOWED_DOMAINS` | なし | この IdP だけの許可リスト。設定すると、配備共通の許可リストに足すのではなく置き換えます |
| `AF_OIDC_<ID>_PKCE` | 有効 | `off` で PKCE を送りません。更新のあと、認可の段階でサインインが失敗するときだけ使います |

## 同じディレクトリを 2 つのアプリ登録で使う

同じ Entra のディレクトリを 2 つのアプリ登録で使うことがあります（配備共通のボタンと、テナントが登録したボタンなど）。
Entra の `sub` はアプリ登録ごとに違う値なので、何もしないと、同じ人でもどちらのボタンを押したかで別のアカウント・別のワークスペースになります。

```sh
AF_OIDC_ENTRA_LINK_CLAIM=oid
```

- `oid` は、そのディレクトリでのその人のオブジェクト ID です。どのアプリ登録でも同じ値で、本人には変えられません
- 本人が名乗れる値（`email`・`upn`・`preferred_username` など）を指定すると、同じ issuer を使う別の方法がその値を名乗るだけで、既存のアカウントに入れてしまいます。`oid` 以外を指定する前に、この点を確かめてください
- テナントの側で同じディレクトリを登録するときに何が要るかは [テナント独自のサインインを承認する](tenant-idp.ja.md#同じ-idp-を配備でも使っているとき) にあります

## うまくいかないとき

### 起動時に、複数テナント向けのエンドポイントを理由に Control Plane が終了する

issuer が `/common/`・`/organizations/`・`/consumers/` のどれかで、`AF_OIDC_<ID>_ALLOWED_TIDS` が空です。issuer を
`https://login.microsoftonline.com/<directory-tenant-id>/v2.0` に固定するか、受け入れるディレクトリを並べます。

### Entra の画面に AADSTS で始まるコードが出る

原因はたいてい次の 2 つで、どちらも Entra 側の設定です。`.env` を変えても直りません。

- client secret が無効：**値** ではなく **シークレット ID** を貼った、または期限が切れた
- リダイレクト URI が一致しない：プラットフォームの種類が違う、または 1 文字違う

### ログに no email claim from id_token or userinfo と出る

そのアカウントにディレクトリ上のメールアドレスがなく、`preferred_username`・`upn` もメールアドレスの形をしていません。
アプリ登録で ID トークンに `email` のオプション クレームを足すか、アカウントにメールアドレスを設定します。

### ログに discovery …: issuer mismatch と出る

設定した issuer が、IdP が名乗る issuer と違います。手で打たずに、discovery ドキュメントからコピーします。

### 何か月も動いていたのに全員が拒否される

client secret の期限切れです。新しいシークレットを作り、`AF_OIDC_<ID>_CLIENT_SECRET` を書き換えて起動し直します。

## 関連

- [サインインの方法を設定する](overview.ja.md)
- [そのほかの OIDC でサインインする](oidc.ja.md)
- [ログインできない](../diagnose/cannot-login.ja.md)
