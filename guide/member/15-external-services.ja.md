---
audience: "セッションをワークスペースの外のサービスとつなぐ人"
---

# 15. 外部サービスとログイン済み CLI — gh / bkt / jira / confluence・Discord / Slack・運用ツール

[English](15-external-services.md) | 日本語

この章は、セッションから使う外部サービスを扱います。`gh`・`bkt`・`jira`・`confluence` の CLI、ほかのログイン済み CLI、Discord / Slack のチャット連携、監視ツールを会話につなぐ方法です。AWS と Google Cloud は [16 クラウドのアカウント](16-cloud-accounts.ja.md) にあります。

Jira と Confluence をセッションで使う方法は [Jira を使う](../user/integrations/jira.ja.md) と [Confluence を使う](../user/integrations/confluence.ja.md) にあります。

## セッションで `gh` と `bkt` を使う

GitHub を接続していれば、セッションの `gh` はサインイン済みです。Bitbucket Cloud
（bitbucket.org）を接続していれば、Bitbucket CLI の `bkt` も同様で、`bkt pr list`・
`bkt pr create`・`bkt pipeline` などに `auth login` は要りません。どちらも保存済みの接続を使い、
git と同じ要領で更新されます。

**GitHub: `gh pr edit` が失敗することがあります。** 組み込みの GitHub OAuth App のトークンの scope は
`repo workflow` だけで、`gh pr edit` はプルリクエストを `read:org` が要り得る方法で読み込むため、自分の個人リポジトリでも
`GraphQL: … requires one of the following scopes: ['read:org']` で失敗することがあります。メッセージのリンク先
（トークン設定ページ）はこのトークンには当てはまりません。代わりに、`repo` だけで足りる REST API で
編集してください:
`gh api -X PATCH repos/<owner>/<repo>/pulls/<N> -F body=@<file>`（タイトルも変えるなら `-f title="…"` を追加）。
これで `read:org` 不足のエラーは避けられます。`read:org` を持つトークンを自分で貼っていればそのエラーは出ませんが、
`gh pr edit` はほかの理由（たとえば対象 PR のチームレビュアーにアクセスできない場合）でも失敗し得ます。

- **スコープ**: `bkt` は接続に与えられた権限の範囲でしか動きません。プルリクエストと
  パイプラインには、Bitbucket の OAuth アプリに `repository`・`pullrequest:write`・`pipeline`（読み取り）・
  `account` が、パイプラインの開始・停止にはさらに `pipeline:write` が必要です。貼り付けた API トークンには
  対応する `read:` / `write:` スコープ（例: `read:pullrequest:bitbucket`、パイプラインの開始・停止には
  `write:pipeline:bitbucket`。write は read を含みません）が必要です。プルリクエストのスコープが無いトークンは
  リポジトリ一覧は取れても `bkt pr list` が `403 Forbidden` になります。OAuth アプリのスコープは
  テナント管理者が設定します。
  同じスコープで、Console の作業項目パネルから Bitbucket のプルリクエストへ報告コメントを投稿できます（OAuth アプリは `pullrequest:write`、API トークンは `write:pullrequest:bitbucket`）。
  Console は、接続がそのスコープを持つと分かっているときだけコメントのボタンを出します。スコープが分からない接続（この対応より前に接続した場合や、Bitbucket がスコープを返さなかった場合）は、作業項目の更新でスコープが分かるまで読み取り専用のままです。Bitbucket の課題（issue）は一覧に出さないため、コメントできるのはプルリクエストだけです。アプリにスコープを足したときは Bitbucket を再接続してください。
- **Bitbucket Data Center**（bitbucket.org 以外のホスト）は設定画面からは接続できません。
  自分で設定してください（`bkt auth login`、または `BKT_HOST` と `BKT_TOKEN`）。設定があると
  その場合 `bkt` は、保存済みの接続を使わず、あなたの設定どおりに動きます。

## サインイン済みの CLI

次のコマンドは、セッションでも端末でも、どのエージェントでも、設定でつないだ接続を使います。端末でログインする
必要はなく、期限が近いトークンは使うときに更新されます。

| コマンド | サインインのもと | 詳しく |
|---|---|---|
| `git` | **Git ホスティング**（GitHub、Bitbucket） | [06](06-code.ja.md#git-プロバイダを接続する) |
| `gh` | **Git ホスティング**の GitHub | [15](15-external-services.ja.md#セッションで-gh-と-bkt-を使う) |
| `bkt` | **Git ホスティング**の Bitbucket Cloud（bitbucket.org） | [15](15-external-services.ja.md#セッションで-gh-と-bkt-を使う) |
| `jira` | **課題管理**の Jira | [Jira を使う](../user/integrations/jira.ja.md) |
| `confluence` | **課題管理**の Jira（同じ Atlassian 接続） | [Confluence を使う](../user/integrations/confluence.ja.md) |
| `af-aws-exec --profile <名前> -- <コマンド>` | **AWS プロファイル/SSM**。プロファイルの**ログイン**のあと | [af-aws-exec](../ref/cli/af-aws-exec.ja.md) |
| `af-gcloud-exec --profile <名前> -- <コマンド>` | **Google Cloud**。プロファイルの**ログイン**のあと | [16](16-cloud-accounts.ja.md#プロファイルとしてコマンドを実行する) |

エージェントには、Bitbucket では Bitbucket や Atlassian の MCP サーバーではなく `bkt` を、Jira では既定で `jira` を使うよう
伝えてあります。CLI は呼び出しのたびに保存済みの接続を読み、期限が近ければ更新しますが、MCP コネクタはログインが切れると
セッションの再起動が必要になるためです。Jira で Atlassian MCP ツールを優先させたいときは
[Jira を使う](../user/integrations/jira.ja.md#atlassian-の-mcp-ツールを使わせたいとき) を見てください。

## Discord / Slack と連携する（チャットブリッジ）

⚙設定 → 接続グループの **「チャット連携」** タブから自分の Discord / Slack の Bot を
接続すると、席を離れていてもセッションの進捗がチャットに届き、そのまま返信で操縦
できます。

- **接続**：Discord は **Bot トークン 1 本**（カードのウィザードが検証 → サーバーへの
  招待 → チャンネル選択まで案内し、接続時にテスト通知が届きます）。Slack は Bot
  トークン（`xoxb-…`）と、双方向にする場合の App-level トークン（`xapp-…`）の 2 本。
  両方の同時接続もできます。
- **届くもの**：セッションごとにスレッドが作られ、「応答あり」「質問・計画承認」
  「許可リクエスト」「異常終了」「完了報告」が届きます（種別ごとにトグルあり）。
  opt-in の**全文モード**では応答本文そのものが届きます（トークンなどの秘密は自動で
  伏字化されます）。
- **チャットから操作**：**「返信で操縦」**（opt-in）をオンにすると、スレッドへの返信が
  そのままそのセッションへの入力になります。質問は選択肢ボタン、プラン承認は
  「承認 / 却下」、許可リクエストは「許可 / 拒否」のボタンで答えられます（ボタンの
  対応範囲はエージェント種別によって異なります）。
  返信を受け付けるのは、ほかのセッションからのメッセージを受けられる種別のセッションだけです。
  シェル / SSM セッションへの返信は、スレッドに断りを返して受け付けず、端末には入力されません。
  チャットの通信はブラウザではなくボットから来るため、テナントの接続元の制限は通りません。
  チャットの返信が素の端末に届かないようにしているのが、この断りです。
- **フリート・オペレーター**：常設スレッド「🛰 フリート・オペレーター」に書き込むと、
  [11 たくさんのセッションを動かす](11-many-sessions.ja.md)とチャットから会話できます
  （Console 側のオペレーターと同一会話）。チャット発の破壊的操作（削除など）は、
  実行前に「承認 / 却下」ボタンで一旦止まります。ボタンが有効になるのは、間に合って押され、その押下を
  確定できたときだけです。期限が切れたあとに押すと「期限切れ、または処理済みです」、確定できなかった
  ときは「承認を受け付けましたが確定できませんでした（実行しません）」と返ります。どちらの押下でも
  **新たに実行されることはありません**。「期限切れ、または処理済みです」は、すでに承認した承認への
  2 回目の押下にも返り、最初の押下で操作が実行済みのことがあります。先の結果を確認してから依頼し直して
  ください。もう一度依頼するのは、「確定できませんでした」と返ったとき、または最初の承認が答えのないまま
  明らかに失効したときだけです。
- **通知だけ止めたい**：個人設定 → 「通知」タブの**サービス通知**で、接続を切らずに
  送信だけオフにできます。

## PagerDuty / Grafana / CloudWatch / AWS は「運用・監視」タブから接続できます（推奨）

**PagerDuty・Grafana・CloudWatch・AWS は製品機能として組み込み済み**です。まずこちらを使ってください。

1. **設定 > 運用・監視** タブを開き、各カードに接続情報を入れて「接続」:
   - **PagerDuty**: API キー。**読み取り専用キー**を推奨します（PagerDuty の Integrations > API Access Keys で「Read-only」を選択）。EU アカウントはトグルをオンにしてください。
   - **Grafana**: インスタンス URL と**サービスアカウントトークン**（Viewer 権限を推奨）。セルフホスト / Grafana Cloud / **Amazon Managed Grafana** のいずれも可（AMG は URL に workspace endpoint を指定。トークンの発行方法と 30 日期限は後述の AMG 節を参照）。
   - **CloudWatch**: プルダウンから **SSM 接続のプロファイルを選ぶ**だけ（リージョンは任意で上書き可）。**秘密の入力はありません**。プロファイルの SSO 設定（非秘密）から専用の設定ファイルを生成し、コンテナ内の AWS 資格をそのまま読みます。SSO ログインがまだ／期限切れの場合はツールがエラーになるので、設定 › [AWS プロファイル/SSM](../user/integrations/aws-sso.ja.md#ログインする) でそのプロファイルの **ログイン** を押すか、該当の SSM セッションを一度開いてください。自分で `~/.aws` を管理している人は「手動入力」でプロファイル名を直接指定できます。
   - **AWS**（Agent Toolkit for AWS）: AWS が提供する MCP サーバーに接続します。プロファイルの指定は CloudWatch と同じ（SSM 接続のプロファイルを選ぶだけ・秘密の入力なし）。加えて 2 つ設定があります。
     - **MCP エンドポイント**: MCP サーバー自体が動くリージョン（`us-east-1` / `eu-central-1`）。**自分のリソースがあるリージョンとは別のもの**で、そちらは上の「リージョン」欄に入れます。
     - **書き込みツール**: 既定オフ（読み取り専用）。オンにすると AWS API 呼び出し（`call_aws`）とスクリプト実行（`run_script`）が使えるようになり、**実際に AWS のリソースを作成・変更・削除できます**。必要なときだけオンにしてください。
     - AWS だけは**対話セッションからも使えます**（他の 3 つはチャット専用）。AWS ドキュメント検索・スキル取得・AWS API 参照が、コードを書いているセッションでそのまま使えます。
     - SSO ログインがまだ／期限切れの場合は、設定 › [AWS プロファイル/SSM](../user/integrations/aws-sso.ja.md#ログインする) でそのプロファイルの **ログイン** を押すか、該当の SSM セッションを一度開いてください。
2. 資格情報はワークスペース内に暗号化保存され、MCP サーバの起動時にだけ渡されます（設定ファイルや平文には残りません）。Grafana は書き込み・管理ツール無効で起動、CloudWatch はサーバ自体が読み取り専用ツールのみ、AWS は既定で `--read-only` 起動です。
3. チャットで **「SRE アシスタント」** を選んで新しい会話を開始。「今開いている PagerDuty のインシデントを一覧して経緯をまとめて」「このサービスの直近 1 時間のエラーレートを Grafana で見て」「CloudWatch でこのロググループの ERROR を分析して」のように聞くと、実データを確認しながら状況整理・原因の仮説出し・対外報告の草稿を手伝います（読み取り専用。ack/resolve はしません）。
4. 接続の変更は**次のチャット送信から反映**されます（AWS をセッションで使う場合は**次のセッション起動から**）。ワークスペースの再起動は不要です。

### Amazon Managed Grafana：トークンの発行

カードに入れるのは、セルフホストの Grafana と同じ種類のサービスアカウントトークンです（IAM や SigV4 の設定は不要）。
AMG の Grafana 管理画面（Administration → Service accounts。管理者権限が必要）で発行するか、IAM 権限があれば
AWS CLI の `aws grafana create-workspace-service-account-token` で発行します
（`af-aws-exec` でプロファイルを指定して実行します。[af-aws-exec](../ref/cli/af-aws-exec.ja.md)）。
AMG のトークンの有効期限は**最長 30 日**です。切れたら新しいものをカードに貼り直してください。

### その他のツール（Zabbix など）

カードのないツール（Zabbix や、ターミナル（CLI）セッション専用の Grafana / PagerDuty / CloudWatch サーバーなど）は、
設定 > 接続 > [MCP サーバー（セッションで使う）](../user/integrations/mcp-servers.ja.md#詳細設定) に MCP サーバーとして登録します。**詳細設定**の
**対象**で、ターミナル（CLI）などのエージェントセッションなら**セッション**（次に起動するセッションから使えます）、チャットなら
**アシスタント**を選びます。アシスタントの場合は、さらにアシスタントの編集フォームの「MCP サーバー（任意）」で選びます。
編集できるのは自分で作ったアシスタントだけで、組み込みのもの（SRE アシスタント・フリート・オペレーター・Agent Fleet アシスタント）は
編集できません。どのサーバーを使うかは各ツールの案内に従ってください（Zabbix なら Zabbix ベンダーのサーバー、AWS Athena なら Athena・
データ処理用の MCP サーバー、または単に `aws` CLI）。`claude mcp add` で手作業で足したサーバーはアシスタントの選択肢に出てこず、
トークンもホームフォルダに平文で残ります。暗号化して保存される登録のほうを使ってください。

アラートの本文やログは**攻撃者が影響できる入力**です。これらの接続は読み取り専用のまま使い、書き込みを試すときは
専用のアシスタントかセッションで明示的に行ってください。

接続なしでターミナル（CLI）のセッションから CloudWatch Logs を読むには、エージェントに `af-aws-exec` 経由で
プロファイルを指定して `aws` CLI を使わせます（`aws logs tail`、`aws logs filter-log-events`、Logs Insights）。
プロファイルに必要なのはログの読み取り権限だけです（[af-aws-exec](../ref/cli/af-aws-exec.ja.md)）。
