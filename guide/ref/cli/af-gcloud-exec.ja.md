---
audience: "af-gcloud-exec のオプション・出力・終了コード・細かい動きを調べたい人"
source_of_truth: "af-gcloud-exec --help（ワークスペースの workspace-agent に入っているもの）"
---

# af-gcloud-exec

[English](af-gcloud-exec.md) | 日本語

Google Cloud のプロファイル 1 つの一時的なアクセストークンで、コマンドを 1 つ実行します。トークンはそのコマンドにだけ渡されます。
使いかたの流れは [Google Cloud を使う](../../user/integrations/gcloud.ja.md) にあります。

## 書式

```sh
af-gcloud-exec --profile <名前> --project <プロジェクト ID> [--login | --no-login] [-q] -- <コマンド> [引数...]
af-gcloud-exec --list
af-gcloud-exec --help
af-gcloud-exec --version
```

`--` より後ろが、実行するコマンドです。`--profile` と `--project` は `--profile=<名前>` の形でも書けます。
`--login` と `--no-login` は片方だけ指定してください。両方を指定すると、後ろのものが有効になります。

## オプション

| オプション | 意味 |
|---|---|
| `--profile <名前>` | 使うプロファイル。必須です。名前は `--list` で確かめられます |
| `--project <ID>` | プロファイルのプロジェクト。必須です。違うと何も実行せずに止めます（[`--project`](#--project)） |
| `--login` | プロファイルに使えるログインがないとき、いつもこのターミナルで gcloud のログインを始めます |
| `--no-login` | ログインを求めず、ターミナルで実行するコマンドを表示して終了コード 3 で終わります |
| `-q`, `--quiet` | 実行するIDとトークンの残り時間の行を表示しません |
| `--list` | 設定からプロファイルを読み直して、使える名前を一覧にします |
| `-h`, `--help` | ヘルプを表示します |
| `--version` | バージョン（属する workspace-agent のビルド）を表示します |

## 終了コード

| コード | 意味 |
|---|---|
| コマンドのもの | 実行できたときは、コマンド自身の終了コードをそのまま返します |
| 1 | そのほかの拒否・失敗（プロジェクトが `--project` と違う、トークンを作れない、など） |
| 2 | 使いかたの誤り |
| 3 | ログインが必要だったが、済まなかった（`--no-login`、Console で時間内にログインされなかった、待っている間に Ctrl-C、実行中にトークンが切れてログインし直しが必要になった） |

終了コード 3 は、いつもログインのことです。権限や API、ネットワークの問題は終了コード 1 です。

## 出力

コマンドを実行する前に、標準エラーに次の 1 行を出します（`-q` で消せます）。トークンそのものは表示しません。

```
af-gcloud-exec: profile prod runs as deployer@acme-shop-prod.iam.gserviceaccount.com (impersonated by you@example.com) in project acme-shop-prod; the token is valid for 59 more minutes
```

実際にどのアカウントで動くかは、この行で確かめられます。サービスアカウントの権限を借用しないプロファイルでは、`runs as you@example.com` のように
ログインしたアカウントだけが出ます。

## `--list` の見方

```
$ af-gcloud-exec --list
analytics   acme-analytics      (chosen at the first login)                                                ("analytics")
prod        acme-shop-prod      you@example.com  impersonates deployer@acme-shop-prod.iam.gserviceaccount.com  ("prod")
staging     acme-shop-staging   you@example.com                                                            ("staging")
prod-app    (not exported: Settings labels Prod App / prod-app all map to this name; rename all but one)
```

実際の出力はタブ区切りです。ここでは読みやすいように空白でそろえています。

- 1 行に 1 つのプロファイルが、名前・プロジェクト・アカウント・（あれば）借用するサービスアカウント・ラベルの順に並びます
- アカウントを指定していないプロファイルは、ログインで選んだアカウントが出ます。まだログインしていなければ `(chosen at the first login)` です
- 使えないプロファイルは、一覧の後ろに `名前  (not exported: 理由)` の形で出ます。理由は、同じ名前になるラベルが 2 つある、
  gcloud が受け付けない値がある、などです

名前は、ラベルを英小文字・数字・`-` だけにしたものです。数字で始まる名前には先頭に `p` が付き、日本語だけのラベルのように
使える文字がないものは `p-` に短いコードが続く名前になります。名前は設定の行にも表示されます。

名前だけでなく、プロジェクトとアカウントを見て選んでください。

`--list` は、そのときに設定からプロファイルを読み直します。設定を読めないときは、前回読んだ分を使い、その旨を表示します。

## `--project`

`--project` はプロファイルのプロジェクトと同じでなければならず、違えば何も実行せずに終了コード 1 で止まります。

- Google のトークンはプロジェクトに結びつきません。これは、コマンドが既定でどのプロジェクトを向くかについて、あなたの認識と
  プロファイルが一致しているかを確かめるだけです。トークンが届く範囲を狭めるものではありません
- コマンド自身の `--project` や、Terraform の設定に書いたプロジェクトは、そちらが優先されます

## コマンドが受け取るもの

コマンドが受け取るのは、プロファイルの一時的な**アクセストークン**だけです。自分の gcloud のログインも、アプリケーションの
デフォルト認証情報も、マシン自身の Google のIDも渡りません。自分の `~/.config/gcloud` は読みも変えもしません。

| 環境変数 | 中身 |
|---|---|
| `CLOUDSDK_AUTH_ACCESS_TOKEN_FILE` | トークンのファイル。gcloud と GKE 認証プラグインはここから読みます |
| `GOOGLE_OAUTH_ACCESS_TOKEN` | トークンそのもの。Terraform の Google プロバイダーはここから読みます |
| `CLOUDSDK_CONFIG` | 空の、この実行専用の gcloud の設定ディレクトリ |
| `CLOUDSDK_CORE_PROJECT`・`GOOGLE_CLOUD_PROJECT`・`GOOGLE_PROJECT` | プロファイルのプロジェクト |
| `CLOUDSDK_BILLING_QUOTA_PROJECT`・`GOOGLE_BILLING_PROJECT`・`GOOGLE_CLOUD_QUOTA_PROJECT` | 割り当てプロジェクト（空欄ならプロジェクト）。`USER_PROJECT_OVERRIDE=true` も付きます |
| `CLOUDSDK_COMPUTE_REGION`・`GOOGLE_REGION`、`CLOUDSDK_COMPUTE_ZONE`・`GOOGLE_ZONE` | プロファイルのリージョンとゾーン（設定したときだけ） |
| `GOOGLE_APPLICATION_CREDENTIALS` | 認証情報の入っていないパス |

シェルにある `CLOUDSDK_*`・`GOOGLE_*`・`GCLOUD_*`・`GCE_METADATA_*` の変数は、先にすべて取り除きます。

## トークンが届くツール

| ツール | トークンを使うか |
|---|---|
| `gcloud`（`gcloud storage` を含む） | 使う |
| GKE クラスターへの `kubectl` | 使う（GKE 認証プラグイン経由。[kubectl を使う](#kubectl-を使う)） |
| Terraform の Google プロバイダー | 使う（割り当てプロジェクト付き。実行中のトークンの更新は届きません） |
| `bq` | 使う。SDK に入っていますがパスは通っていないので、`~/.local/share/agent-fleet/google-cloud-sdk/bin/bq` で実行します |
| `gsutil` | **使わない**。トークンを無視します。代わりに `gcloud storage` を使ってください |
| Google のクライアントライブラリ（Go・Python・Node など） | プログラムがトークンを渡したときだけ（[クライアントライブラリから使う](#クライアントライブラリから使う)） |

`gsutil` は、自分の boto の設定がなければ**認証なしで**リクエストを送ります。公開バケットは応答するので、読めたように見えます。
設定があれば、その設定にある別のIDで動くことがあります。

### kubectl を使う

クラスターの接続情報を一度プロファイル経由で取り、`kubectl` も `af-gcloud-exec` 経由で実行します。
GKE 認証プラグインはトークンを gcloud に求め、gcloud がトークンを持つのは `af-gcloud-exec` の中だけだからです。

```sh
af-gcloud-exec --profile prod --project acme-shop-prod -- gcloud container clusters get-credentials <クラスタ> --location <ロケーション>
af-gcloud-exec --profile prod --project acme-shop-prod -- kubectl get pods
```

### クライアントライブラリから使う

クライアントライブラリで作ったプログラムは、そのままでは「File … was not found」や「no such file or directory」のようなエラーで止まります。
これは意図したものです。止めないと、自分の gcloud のログインやマシンのIDなど、別のIDを見つけてしまいます。

プログラムがトークンを自分で受け取るように書きます。

| 言語 | 書き方 |
|---|---|
| Python | `google.oauth2.credentials.Credentials(os.environ["GOOGLE_OAUTH_ACCESS_TOKEN"], quota_project_id=os.environ["GOOGLE_BILLING_PROJECT"])` |
| Go | `option.WithTokenSource(oauth2.StaticTokenSource(&oauth2.Token{AccessToken: os.Getenv("GOOGLE_OAUTH_ACCESS_TOKEN")}))`。割り当てプロジェクトは `GOOGLE_CLOUD_QUOTA_PROJECT` から読まれます。`option.WithQuotaProject` とは一緒に使えません |
| Node | `OAuth2Client` に `setCredentials({access_token: process.env.GOOGLE_OAUTH_ACCESS_TOKEN})` して `authClient` に渡します |

- Node では、クライアントライブラリが使っているのと**同じメジャーバージョン**の `google-auth-library` から `OAuth2Client` を作ってください。
  違うバージョンのものはエラーにならずに受け取られ、認証情報を何も送りません
- エージェントは、あなたのコードをこのように変える前に、あなたに確認します

## トークンの期限

- トークンの有効期間は、発行した時点での残り時間です。最短 10 分、最長でおよそ 1 時間です。残りが 10 分を切るトークンしか作れないときは、
  何も実行せずに止まります（1 分ほどしてから、もう一度試してください）
- コマンドの実行中は `af-gcloud-exec` が横で動き続け、トークンが切れる約 8 分前に**トークンのファイルを更新**します。
  その後に起動する `gcloud`・`bq`・GKE 認証プラグインは新しいトークンを読むので、それらを呼び直すスクリプトは動き続けます
- 起動したときのトークンを持ち続けるプロセスは、そのトークンが切れると失敗します。1 つの長く動く `gcloud` と、
  `GOOGLE_OAUTH_ACCESS_TOKEN` を一度だけ読むプログラム（**長い `terraform apply`**）がこれにあたります。短い実行に分けてください
- `kubectl` の watch や接続中の通信が続くかは、プラグインの再実行と再接続しだいです（確かめていません）

### 更新できないとき

ログインが取り消された、ログインし直しが必要、権限、ネットワークなどの理由でトークンを更新できないときは、次のように動きます。

1. その旨を標準エラーに表示し、トークンが切れるまで再試行を続けます
2. 切れたらコマンドを止めます（SIGTERM。30 秒で終わらなければ SIGKILL）
3. ログインし直しが必要なら終了コード 3、それ以外は終了コード 1 で終わります

開始時に Console に依頼できる実行だった場合は、ログインの依頼が Console にもう一度出ます。

### コマンドを止める

`af-gcloud-exec` を止めると（強制終了されたときも）、起動したコマンドに SIGTERM を送ります。コマンドはこれを無視できます。
コマンド自身が起動したプログラムには届きません。強制終了されたときは、トークンのファイルが期限まで残り、後の実行がそれを削除します。

## ログインしていないとき

プロファイルでまだログインしていない、または Google が保存済みのログインを断ったときの動きは、どこから実行したかで変わります。

| どこから | 動き |
|---|---|
| エージェント | Console にログインを頼み、1 分半ほど待ちます。間に合わなければ終了コード 3 で終わります |
| ワークスペースのほかのセッション（シェルのスクリプトなど） | Console に頼み、数秒待ちます |
| ワークスペースのターミナル | Console に頼み、最大 10 分待ちます。Ctrl-C で待つのをやめると終了コード 3 で終わり、依頼は Console に残ります |
| `--login` を付けたとき | そのターミナルで gcloud のログインを始めます |
| `--no-login` を付けたとき、ワークスペースの外 | 終了コード 3 と、ターミナルで実行するコマンドを表示して終わります |

Console に頼んだときは、標準エラーに次のように出ます。依頼はプロファイルごとに 1 つで、2 つ目の実行は同じ依頼を待ちます。

```
af-gcloud-exec: Google Cloud login for profile "prod" requested in the Agent Fleet Console; waiting up to 1m30s for the member to finish it there
```

Console で依頼が取り消されたときは、そのプロファイルについて 1 分ほど再び頼みません。

### ターミナルでログインする

```sh
af-gcloud-exec --profile <名前> --project <プロジェクト ID> --login -- true
```

表示される URL をブラウザで開いてログインし、ページに出る確認コードを**その**ターミナルに貼り付けます。
貼り付けてよいのは、いま自分で始めたログインのコードだけです。Claude Code からなら、プロンプトで `!` に続けて入力すると自分のターミナルとして実行できます。

Google Cloud の VM の上で直接動くワークスペースでは、gcloud が VM で個人アカウントを使うかを尋ねます。Console のログインはこれに答えないので、
この方法でログインしてください。

## 実行はセッションの会話に出ます

実行はそれぞれ、セッションの会話に 1 行を残します。

- プロファイル、プロジェクト、アカウント（と借用するサービスアカウント）
- プログラム名と、コマンドの終了コード。実行しなかったときは、その理由

コマンドの引数は記録しません。`--list` と `--help` は記録しません。全セッションの最近の実行は、設定の「Google Cloud」タブの「**最近の実行**」に並びます。

## Google Cloud SDK

- はじめて使うときに、固定されたバージョンの Google Cloud SDK と GKE 認証プラグインをホームディレクトリにインストールします。ダウンロードは約 85 MB で、ディスクを約 510 MB 使います
- 停止しても作り直しても残ります。インストール後は、設定の「**ツールチェーン**」タブのツールのバージョンの表に gcloud が出ます
- その後は、gcloud の各コマンドの初回だけ数秒遅くなります
- ターミナルで素の `gcloud` を実行すると同じプログラムが動きますが、使うのは（あれば）自分のログインで、プロファイルのものではありません

## 止まったときのメッセージ

| メッセージ | 意味 | すること |
|---|---|---|
| 終了コード 3、`the login was requested in the Agent Fleet Console and is waiting …` | ログインがまだないか、Google が保存済みのログインを断り、待つのをやめました | トーストか設定からログインし、もう一度実行します |
| 終了コード 3、`the token of profile … has ended and could not be renewed, so the command was stopped: …` | 実行中にトークンが切れ、ログインし直しが必要です | メッセージにターミナルのコマンドがあればそれを実行し、なければ Console でログインします |
| 終了コード 3、`the login request was cancelled in the Agent Fleet Console` | 依頼を取り消しました | ログインするなら設定から行います |
| 終了コード 3、`Google Cloud login required … log in from a terminal with:` | Console に頼めませんでした（`--no-login`、ワークスペースの外） | 表示されたコマンドを自分のターミナルで実行します |
| `is for project X, not Y; --project must be the profile's project` | このプロジェクトには違うプロファイルです | `--list` で選び直します |
| `no Google Cloud profile …`、`not exported: …` | その名前は使えるプロファイルにありません | 設定で追加するか直します |
| `gcloud could not mint a token: …` | ログインの問題ではありません。権限がない（借用するサービスアカウントへの権限を含む）、API が有効でない、ネットワーク | メッセージの理由を直します |
| `the token gcloud minted is valid for only …` | 残り 10 分のトークンを作れませんでした | 1 分ほどしてから、もう一度試します |
| `the login finished but still gave no usable credential: …` | ログインしたのに、使える認証情報になりませんでした | メッセージを確かめます |
| `this deployment does not export Google Cloud profiles` | この配備は Google Cloud のプロファイルを提供していません | ほかの方法はありません |
| `could not renew the token of profile … trying again until it ends in N minutes` | 使っているトークンをまだ更新できていません | 待ちます。先にトークンが切れたらコマンドは止まります |
| `the profile's login is now X, not Y, which the command started as; run it again` | 実行中に、そのプロファイルが別のアカウントでログインし直されました | もう一度実行します |
| `the profile changed in Settings while this run started; run it again` | 実行の開始中に、設定でプロファイルが変わりました | もう一度実行します。アカウントやプロジェクトが変わっていれば `--list` で確かめます |
| `waiting for another af-gcloud-exec or a profile sync …` | ターミナルでのログインか別の実行が、ワークスペースの gcloud の保存場所を使っています | 何もしません。終われば続きます |

## 保証しないこと

- コマンドはあなたとして動くので、自分の gcloud のディレクトリを含めてホームディレクトリを読めます。`af-gcloud-exec` は、Google の認証情報を探す
  標準の方法（gcloud とクライアントライブラリのもの）が、渡したトークン以外を見つけないようにするだけです
- 自分の設定を持つ `gsutil` のように、別の場所を探すツールは対象外です
- Google Cloud の VM や Kubernetes の Pod の上で動くワークスペースでは、標準の方法を無視するプログラムが、VM やノードのID
  （GKE では Workload Identity）に届く可能性があります。管理者がネットワークで経路を塞がないかぎり、ワークスペースからは防げません
