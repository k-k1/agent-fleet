---
audience: "配備に IdP をつなぐ配備管理者（ホストのターミナルと `.env` を扱う人）"
source_of_truth: "`.env` の読み込みは Control Plane のサインイン設定（記述がコードと食い違ったら、このページのバグ）"
---

# GitHub でサインインする

[English](github.md) | 日本語

指定した GitHub の組織のメンバーが、GitHub アカウントでこの配備にサインインできるようにします。
サインインの条件は「その組織のアクティブなメンバーであること」で、GitHub アカウントを持っているだけでは入れません。

## 全体像

| あなたがやること | Control Plane がやること |
|---|---|
| GitHub で OAuth App を作る | |
| 組織のオーナーに、その OAuth App を承認してもらう | |
| 許可する組織と OAuth App の値を `.env` に書き、起動し直す | `AF_GITHUB_ALLOWED_ORGS` があれば、GitHub のボタンをサインイン画面の最後に出す |
| | サインインのたびに組織のメンバーかどうかを GitHub に確かめ、結果をしばらくメモリに保持する |

GitHub にはユーザーのサインインに使う OIDC がないので、専用の設定になります。

## GitHub で OAuth App を作る

1. **OAuth App** を作ります（GitHub App ではありません）。**Settings → Developer settings → OAuth Apps → New OAuth App** です。個人ではなく組織の所有にしたいなら、組織の側で作ります
2. **Authorization callback URL** に `https://<PUBLIC_DOMAIN>/oauth2/callback` を入れます（[リダイレクト URI を登録する](overview.ja.md#リダイレクト-uri-を登録する)）。**Homepage URL** は Console の URL でかまいません
3. **client secret** を作って控えます（一度しか表示されません）
4. 組織がサードパーティの OAuth App を制限しているなら、**組織のオーナーがこの OAuth App を承認します**（組織の設定の、サードパーティ アプリケーションのアクセス ポリシーの欄です）。承認されるまでは、設定がすべて正しくても全員が拒否されます

Console の GitHub の **接続** が使う OAuth App（`GITHUB_OAUTH_CLIENT_ID`）を、そのまま使ってもかまいません。コールバック URL を足すだけです。
スコープは認可のたびに渡すので、1 つのアプリで git の接続（`repo`）とサインイン（`read:org user:email`）の両方に使えます。
組織の承認を 1 回で両方に効かせたくないなら、別のアプリにします。

## .env に書く

```sh
AF_GITHUB_ALLOWED_ORGS=acme,acme-labs      # 必須。これがボタンを有効にする
GITHUB_OAUTH_CLIENT_ID=<client-id>
GITHUB_OAUTH_CLIENT_SECRET=<client-secret>
AF_GITHUB_ALLOWED_DOMAINS=example.com      # 強くすすめます
```

| キー | 決めること |
|---|---|
| `AF_GITHUB_ALLOWED_ORGS` | サインインを認める組織（カンマ区切り）。どれかのアクティブなメンバーなら通ります |
| `GITHUB_OAUTH_CLIENT_ID`・`_SECRET` | OAuth App の値 |
| `AF_GITHUB_LOGIN_CLIENT_ID`・`_SECRET` | サインイン専用の OAuth App を使うときだけ。`GITHUB_OAUTH_*` より優先されます |
| `AF_GITHUB_ALLOWED_DOMAINS` | 受け入れるメールアドレスのドメイン |

- **ボタンを有効にするのは `AF_GITHUB_ALLOWED_ORGS` です。** client id ではありません。git の接続のためだけに `GITHUB_OAUTH_CLIENT_ID` を使っている配備は、そのまま動きます
- **`AF_GITHUB_ALLOWED_DOMAINS` も設定します。** GitHub が渡すのは、アカウントの主アドレスで確認済みのもの 1 件で、個人用のアドレスであることがよくあります。アドレスが違えば別の人として扱うので、その人はいつものワークスペースではなく**新しい空のワークスペース**に入ってしまいます。入口で断るほうが親切です
- メールアドレスの一覧をどこにも設定しないと、組織だけが入口の条件になります。Control Plane は起動時にそのことをログに出します

書いたら Control Plane を起動し直し、[動いたことを確かめる](overview.ja.md#動いたことを確かめる) の手順で確認します。

### ふだんは要らない設定

| キー | 既定 | 決めること |
|---|---|---|
| `AF_GITHUB_ALLOWED_EMAILS` | なし | 受け入れるメールアドレス |
| `AF_GITHUB_LABEL_JA`・`_LABEL_EN` | 組み込みの文言 | ボタンの文言 |
| `AF_GITHUB_MEMBERSHIP_TTL` | `10m` | 組織のメンバーかどうかの結果を保持する時間 |
| `AF_GITHUB_MEMBERSHIP_GRACE` | `1h` | GitHub に届かないとき、最後の結果で通し続ける時間。過ぎたら拒否します |
| `AF_GITHUB_PKCE` | 有効 | `off` で PKCE を送りません |

## 先に知っておくこと

### 組織の承認が要る

サードパーティの OAuth App を制限している組織は、承認していないアプリにはメンバーかどうかを見せません。
Control Plane からは「メンバーでない」と「見る権限がない」の区別がつかないので、全員が拒否されます。ログには次の行が出ます。

```
WARNING: github: org "acme" returned 403 for a membership check — if the org restricts
third-party OAuth apps, an org owner must approve this OAuth app before anyone can sign in
```

### Control Plane を起動し直すと、もう一度サインインを求められる

組織のメンバーかどうかの結果と、それを更新するためのトークンは、メモリにだけ保持しています。起動し直すと確かめ直す材料がなくなるので、
「許可されていません」ではなく「もう一度サインインしてください」になります。GitHub 側のセッションが続いていれば、たいてい何も操作せずに元どおり使えます。
故障ではありません。

### GitHub のユーザー名を変えても、アカウントは変わらない

手放したユーザー名はほかの人が取れるので、Agent Fleet は GitHub の数値のアカウント ID で人を見分けます。

## うまくいかないとき

### 設定は正しく見えるのに、全員が拒否される

組織が OAuth App をまだ承認していません（[組織の承認が要る](#組織の承認が要る)）。`docker compose logs cp | grep "returned 403"` で確かめます。

### 特定の人だけ拒否される

- その人の GitHub の主アドレス（確認済み）が `AF_GITHUB_ALLOWED_DOMAINS` の外にある：会社のアドレスを GitHub で確認して主アドレスにしてもらうか、別のボタンでサインインしてもらいます
- 組織のメンバーシップが招待中（pending）のまま：招待を承諾してもらいます

### the GitHub account has no primary verified email address と出る

そのアカウントに、渡せる確認済みの主アドレスがありません。GitHub でアドレスを確認して、主アドレスにしてもらいます。

### ボタンが出ない

ログに `github login disabled — …` の行が出ます。`AF_GITHUB_ALLOWED_ORGS` が空か、client id と secret の片方しかありません。

## 関連

- [サインインの方法を設定する](overview.ja.md)
- [テナント独自のサインインを承認する](tenant-idp.ja.md)（テナントが GitHub の組織を登録したとき）
- [ログインできない](../diagnose/cannot-login.ja.md)
