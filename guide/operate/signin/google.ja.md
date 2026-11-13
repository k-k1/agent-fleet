---
audience: "配備に IdP をつなぐ配備管理者（ホストの端末と `.env` を扱う人）"
source_of_truth: "`.env` の読み込みは Control Plane のサインイン設定（記述がコードと食い違ったら、このページのバグ）"
---

# Google でサインインする

[English](google.md) | 日本語

Google アカウントでこの配備にサインインできるようにします。Google Cloud Console で OAuth クライアントを作り、
2 つの値を `.env` に書くだけです。

## 全体像

| あなたがやること | Control Plane がやること |
|---|---|
| Google Cloud Console で OAuth クライアントを作る | |
| クライアント ID とシークレットを `.env` に書き、起動し直す | 2 つがそろっていれば、Google のボタンをサインイン画面の先頭に出す |
| 誰が入れるかを許可リストで決める | Google が確認済みとしたメールアドレスだけを受け取り、許可リストと照らし合わせる |

## Google Cloud Console でクライアントを作る

1. クライアントを置くプロジェクトを選びます（なければ作ります）
2. そのプロジェクトの **OAuth 同意画面** を設定します
   - Google Workspace を使っていて、自社のドメイン以外にこの画面を見せたくないなら **内部**、それ以外は **外部**
   - これは「同意画面までたどり着ける人」の範囲です。誰がサインインできるかは、Agent Fleet の許可リストで決めます
3. **認証情報 → 認証情報を作成 → OAuth クライアント ID** を選び、種類を **ウェブ アプリケーション** にします
4. **承認済みのリダイレクト URI** に `https://<PUBLIC_DOMAIN>/oauth2/callback` を追加します（[リダイレクト URI を登録する](overview.ja.md#リダイレクト-uri-を登録する)）。**承認済みの JavaScript 生成元** は要りません
5. 作成し、**クライアント ID** と **クライアント シークレット** をその場で控えます

## .env に書く

```sh
GOOGLE_OAUTH_CLIENT_ID=<client-id>.apps.googleusercontent.com
GOOGLE_OAUTH_CLIENT_SECRET=<client-secret>
```

- 書くのはこの 2 つだけです。両方を設定すると Google のボタンが有効になり、IdP の id は `google` になります
- `AF_OIDC_PROVIDERS` に `google` を足す必要はありません。`AF_OIDC_GOOGLE_*` というキーもありません
- issuer・エンドポイント・信頼の規則は組み込みで、変えられません。受け取るのは Google が `email_verified` を付けたアドレスだけで、要求するスコープは `openid email` です
- 個人の `gmail.com` のアドレスも、Google の確認は通ります。自社の人だけに絞るのは `AF_OAUTH_ALLOWED_DOMAINS` の役目です

書いたら Control Plane を起動し直し、[動いたことを確かめる](overview.ja.md#動いたことを確かめる) の手順で確認します。

## うまくいかないとき

### Google の画面に redirect_uri_mismatch と出る

クライアントに登録した URI が `<PUBLIC_BASE_URL>/oauth2/callback` と完全に一致していません。Google は文字列全体で比べます。

### ボタンが出ない

2 つのキーのどちらかしか設定していません。Control Plane のログに次の行が出ます。

```
WARNING: google login disabled — set both GOOGLE_OAUTH_CLIENT_ID and GOOGLE_OAUTH_CLIENT_SECRET
```

### テストユーザーしか通れない

同意画面が **外部** で、まだテスト中です。テストユーザーに登録したアカウント以外は、Control Plane に届く前に Google が止めます。

### Google は通るのに Agent Fleet が拒否する

Google ではなく、許可リストの問題です。[ログインできない](../diagnose/cannot-login.ja.md) を見てください。

## 関連

- [サインインの方法を設定する](overview.ja.md)
- [ログインできない](../diagnose/cannot-login.ja.md)
