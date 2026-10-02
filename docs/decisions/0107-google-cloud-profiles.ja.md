# 0107. Google Cloud プロファイル: Settings で定義し、`af-gcloud-exec` がその 1 つで動かし、gcloud のログインは Console で仕上げる

[English](0107-google-cloud-profiles.md) | 日本語

- 状態: **proposed**（2026-10-02）。まだ何も作っていない。以下の gcloud の挙動は Workspace で Google Cloud SDK 587.0.0 を
  使って測った。測っていないものはその旨を書いている。
- 追跡: #1092（Tier 2「利用者自身のクラウドの機能の gcloud 版」）
- 関連: [0102](0102-aws-login-through-the-console.ja.md)（Console を通す AWS のログイン。その仕組みを流用する）/
  [0106](0106-kubernetes-runtime.ja.md)（GCE と GKE でのメタデータサーバの遮断）/
  [0104](0104-long-lived-member-workspace.ja.md)（`~/.local` と home が停止を越えて残る理由）

## 背景

メンバーは Settings で AWS のプロファイルを定義し、`af-aws-exec --profile <name> --account <id> -- <command>` で任意のコマンドを
その 1 つとして動かせる。プロファイルはメンバーごとのブリッジで Workspace に届き、ラッパーは Workspace 自身のクラウドの ID を
コマンドから遠ざけ、ログインが無ければ Console がそれを仕上げる（[0102](0102-aws-login-through-the-console.ja.md)）。Google Cloud
にはこの種のものが何も無い。イメージに gcloud は無く、プロファイルもラッパーも無く、ポリシーの文章も Google の認証情報について
エージェントに何も教えない。Google Cloud 上で Agent Fleet を動かし（ADR 0106）、セッションから自分のプロジェクトを運用する
メンバーは、gcloud を手で入れ、ターミナルにログインを貼り付けるしかない。

### AWS からそのまま持ち込めるもの

- **ブリッジ。** メンバーシップごとの HMAC トークンを Workspace に注入し、取得のたびに生きたメンバーシップの照会とともに検証し、
  Workspace 専用 listener の許可リストに載せる（`aws_profiles_bridge.go`、`workspace_listener.go`）。
- **Agent の取得ループ。** 起動時に 1 回、その後 5 分ごとに、ロックの下で、CP に届かないときのためにトークンに結び付けた
  キャッシュを持つ（`internal/awsx/profiles.go`）。
- **0102 の Console ログインの仕組み。** ラッパーが出す要求、ペイロードが id だけの送り箱の通知、残り続ける toast、メンバーの押下
  でしか始まらず結果は押したタブにしか見えない開始、監査記録付きの CP の中継、待ってから終了コード 3 で終わる約束。
- **ラッパーの形。** コマンドへの `syscall.Exec`、非公開の状態ディレクトリ、`--list`、終了コード 2（使い方）、3（ログインが要り
  始まっていない）、1（拒否）。

### gcloud が違うところ（実測）

- **ログインは逆向きに進む。** `gcloud auth login --no-launch-browser` は、リダイレクト先が
  `https://sdk.cloud.google.com/authcode.html` の `https://accounts.google.com/o/oauth2/auth?…` の URL を出し、「ブラウザに
  表示された検証コード」を stdin で待つ。メンバーは自分のブラウザでサインインし、**コードを貼り戻す**。AWS のデバイスフローは
  逆に、CLI が出したコードをメンバーが IdP に入力する。
- **コードはそれを求めたプロセスに結び付く。** URL は PKCE（`code_challenge_method=S256`）を持つ。他の URL で得たコードでは
  このログインを完了できない。
- **認証情報の置き場が 2 つある。** gcloud 自身の認証情報（`CLOUDSDK_CONFIG` の下の `credentials.db`）と、Google のクライアント
  ライブラリと Terraform が読む 1 つのファイルである Application Default Credentials。`--update-adc` は 1 回のログインで両方を書く。
- **メタデータサーバから離す SDK 共通のスイッチが無い。** AWS には `AWS_EC2_METADATA_DISABLED` がある。Google のライブラリは
  ほかに何も見つからなければメタデータサーバに落ちる。ADR 0106 は GCE と GKE ではネットワークでそれを塞いでおり、それ以外では
  落ちる先のメタデータサーバが無い。
- **大きさ。** SDK は展開すると 486 MB（圧縮で 88 MB）。

## 決定

### 1. Google Cloud プロファイルは、AWS プロファイルと同じくメンバーの Settings の行である

Settings の AWS の隣に **Google Cloud** の欄を足し、AWS と同じくメンバー単位とする。プロファイルは次を持つ。

| 項目 | 必須 | 意味 |
|---|---|---|
| ラベル | はい | 名前。AWS プロファイルの名前と同じく整える |
| プロジェクト | はい | 既定のプロジェクトで、`--project` を照合する相手 |
| アカウント | いいえ | ログインする Google アカウント。設定すると、ほかの誰かとしてのログインは断る |
| リージョン、ゾーン | いいえ | 構成の `compute/region` と `compute/zone` に書く |
| なりすますサービスアカウント | いいえ | ログインしたアカウントを通してコマンドが振る舞うサービスアカウント（gcloud の `--impersonate-service-account`）。AWS のロールに当たる |

CP には秘密を置かない。AWS と同じく、認証情報は Workspace の中で CLI が得る。サービスアカウントの JSON 鍵はどこでも受け付けない。
長寿命の秘密であり、禁じている組織が多く、なりすましが同じ必要を満たす。

プロファイルは `GET /internal/gcp-profiles` を通して、専用のトークン（`AF_GCP_PROFILES_TOKEN`）で Workspace に届き、その経路は
Workspace 専用 listener の許可リストに足す。Agent は各プロファイルを、メンバー自身の gcloud ディレクトリに gcloud の名前付き構成
`af-<name>` として書く。これでターミナルから手で `gcloud --configuration af-<name>` としても動く。メンバーが自分で作った同名の
構成には触れず、AWS プロファイルと同じくそのプロファイルを隠されたものとして報告する。

### 2. `af-gcloud-exec --profile <name> --project <id> -- <command>` はコマンドを 1 つのプロファイルとして動かす

- **`--project` はプロファイルのプロジェクトと一致しなければならない。** AWS プロファイルで `--account` がアカウントと一致
  しなければならないのと同じで、コマンドがメンバーの意図した場所に着くことの確認である。
- **コマンドが受け取るのは短期のアクセストークンで、メンバーの更新トークンではない。** ラッパーは gcloud にプロファイルとしての
  アクセストークンを求め（`gcloud auth print-access-token --configuration af-<name>`。プロファイルが指定していればなりすましで）、
  非公開ディレクトリのファイルに書き、次のものとともにコマンドを始める。
  - `CLOUDSDK_AUTH_ACCESS_TOKEN_FILE`。gcloud はそのトークンだけを使う。
  - `GOOGLE_OAUTH_ACCESS_TOKEN`。Terraform の Google プロバイダが使う。
  - `CLOUDSDK_CORE_PROJECT`、`GOOGLE_CLOUD_PROJECT`、リージョンとゾーン。
  - 空の非公開ディレクトリを指す `CLOUDSDK_CONFIG`。コマンドの gcloud が、メンバーがログインしているほかのアカウントに届かない
    ようにする。
  - 呼び出し側の `GOOGLE_APPLICATION_CREDENTIALS` とほかの `CLOUDSDK_*` の上書きはすべて取り除く。
- **1 回のログインが gcloud と Terraform の両方に効く。** ADC のファイルは書かず、読みもしない。両方が受け取るのはアクセス
  トークンである。`--update-adc` は使わない。ADC はすべてのプロファイルで 1 つのファイルなので、「これは誰の ID か」が最後に
  走ったログインで決まってしまう。
- **トークンの寿命は 1 時間。** それより長く走るコマンドではトークンが切れる。ラッパーは使い方の文章でそう述べ、手立ては再実行
  である。自分で更新するのがどのツールかを測るのは未決事項 1。
- **終了コードと `--list` は `af-aws-exec` に従う。**

それでもコマンドにできること: 2 つの変数をどちらも無視する Google のクライアントライブラリは、ADC に、続いてメタデータサーバに
落ちる。非公開の構成に ADC のファイルは無く、GCE と GKE ではメタデータサーバが塞がれている（ADR 0106）ので、その落ち先は
Workspace 自身の ID として振る舞うのではなく失敗する。使い方の文章と notes はどのツールが対象かを書く。

### 3. ログインは Console が仕上げ、メンバーはコードをそこに貼り付ける

ターミナルが無いとき、`af-gcloud-exec` は `af-aws-exec` とまったく同じくログイン要求を出す（0102 の決定 1、2、5、6 をそのまま
当てはめ、要求はプロファイルごとに 1 つ）。違うのは開始である。

1. メンバーが toast か Settings で **ログイン** を押す。Agent は
   `gcloud auth login [<account>] --no-launch-browser --configuration af-<name>` を自分のプロセスグループで始める。試行は
   プロファイルごとに 1 つ。
2. Agent は出力から URL を読み、見せる前に確かめる。スキームは `https`、ホストは厳密に `accounts.google.com`、`redirect_uri` は
   厳密に `https://sdk.cloud.google.com/authcode.html`。それ以外なら試行を終える。
3. URL は押したタブにだけ見え、メンバーの押下でだけ開く。モーダルの入力欄は 1 つ、**検証コード**である。
4. メンバーがコードを貼り付ける。Console はそれを Agent の試行に送り（CP が中継し `gcp.login.submit` として監査する）、Agent は
   その gcloud の stdin に書く。コードを受け付けるのは、存在し、まだ待っていて、同じタブが始めた試行に対してだけである。
5. プロファイルがアカウントを指定していて、ログインが別のアカウントになったら、Agent は新しい認証情報を取り消し、不一致を報告する。

ガイドが教える規則は gcloud の形で持ち込む。**自分で始めたログインにだけコードを貼り付ける**。PKCE はコードを、それを求めた
gcloud 以外では役に立たなくし、押下の規則はその gcloud をメンバーが始めたことを確かにする。

### 4. gcloud は必要なときに、版を固定して `~/.local` に入れる

`workspace-agent install-gcloud` は、固定した 1 つの版を sha256 で確かめ、コア部分だけを、停止と Recreate を越えて残る `~/.local`
に入れる。`af-aws-exec` が `install-awscli` を動かすのと同じく、`af-gcloud-exec` は初回にそれを動かす。版は `AWSCLI_VERSION` と
同じ方法で固定する。`versions.json` に記録される Dockerfile の `ARG` で、`readBuildPins` が読み戻す。両アーキテクチャについて、版付きの
アーカイブ（`google-cloud-cli-<version>-linux-<arch>.tar.gz`）とその sha256 を持つ。版の更新は AWS CLI と同じく手で固定を変える
（CLI の版更新ワークフローが扱うのはエージェントの CLI だけ）。Toolchain タブの実効ツール版の表にそれを載せる。

イメージには焼き込まない。486 MB が、冷えたイメージの取得のたびに乗る。Fargate は起動のたびに冷えた取得をし、GKE はノードごとに
取得する。それを、一部のメンバーしか使わない道具のために負うことになる。

### 5. AWS の残りの面にも対応するものを用意する

- プロファイルがあるとき、Workspace のバーは AWS の隣に Google Cloud のバッジを出す。
- `workspace/notes/gcp.md`（`af-gcp` スキル）は、ラッパー、貼り付けの規則、終了コードを教える。`workspace/workspace-notes.md` の
  ポリシーの文章に Google Cloud の項を足す。既定の認証情報の連鎖はメンバーではなく、メンバーのプロジェクトに関わることはすべて
  `af-gcloud-exec` を通す。
- ガイドの連携と Settings のページを英語と日本語で。

### 対象外

- **Workforce Identity Federation**（`gcloud auth login --login-config`）。Google 以外の IdP で Google Cloud にサインインする組織の
  ためのもの。ログインは別の流れであり、後続の issue とする。
- SSM のような**セッションの種類**。たとえば `gcloud compute ssh --tunnel-through-iap`。後続とする。
- AWS MCP の組み込みに当たる **Google Cloud の MCP サーバ**。
- **サービスアカウントの鍵**（決定 1）。

## 却下した案

- **更新トークンを CP に保存し、そこでアクセストークンを発行する案。** CP をメンバーの Google の認証情報の置き場にしてしまう。
  AWS の設計が意図して避けたことである。
- **共有の置き場としての ADC**（`--update-adc`）。決定 2 のとおり。
- **gcloud をイメージに焼き込む案。** 決定 4 のとおり。
- **メンバーの gcloud ディレクトリをまるごとコマンドに渡す案。** メンバーがログインしているすべてのアカウントに、どのプロファイル
  として動かしたどのコマンドからも届いてしまう。

## 結果

- 2 つ目のクラウドの分の Settings・ブリッジ・ラッパー・ログイン・notes を、1 つ目と足並みをそろえて保つことになる。汎用の部分
  （ログイン要求と試行の仕組み、ブリッジのトークン、ラッパーの骨組み）は、写すのではなく `awsx` から一度切り出す価値がある。
- Console のログインのモーダルに入力欄が増える。AWS ではコードを表示するだけだった。ここではメンバーの貼り付けが CP の中継を
  通る移動中の秘密なので、中継は本文をログに残してはならない。
- アクセストークンより長生きするコマンドは失敗し、再実行することになる。

## 未決事項（測ってから決める）

1. `CLOUDSDK_AUTH_ACCESS_TOKEN_FILE` / `GOOGLE_OAUTH_ACCESS_TOKEN` に従うのはどのツールか。gcloud と Terraform は従う見込み。
   `gke-gcloud-auth-plugin`（GKE に対する kubectl）、`gsutil` / `gcloud storage`、`bq`、クライアントライブラリは測る。
2. 組織の再認証ポリシー（Google Cloud のセッションの長さ）がどう現れるか、そして AWS SSO と同じく Agent がその前に警告できるか。
3. `install-gcloud` が `bq` や同梱の追加物を落として 486 MB より小さくできるか。

## 段階

| 段階 | 内容 | 完了の条件 |
|---|---|---|
| 1 | 決定 1、2、4: Settings、ブリッジ、`af-gcloud-exec`、`install-gcloud`。ログインはターミナルで | メンバーがラッパーを通して自分のプロジェクトに対して `gcloud` と `terraform plan` を動かし、プロジェクトの不一致が断られる |
| 2 | 決定 3: コードの貼り付けを伴う Console のログイン | エージェントの `af-gcloud-exec` が、ターミナル無しで Console から仕上がる |
| 3 | 決定 5 と未決事項 1 | notes、ガイド、バッジが出る。測ったツールの一覧が notes にある |
