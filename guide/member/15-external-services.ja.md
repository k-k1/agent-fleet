---
audience: "セッションをワークスペースの外のサービスとつなぐ人"
updated: "2026-10"
---

# 15. 外部サービスとログイン済み CLI — gh / bkt / jira・Discord / Slack・運用ツール

[English](15-external-services.md) | 日本語

この章は、セッションから使う外部サービスを扱います。`gh`・`bkt`・`jira` の CLI、ほかのログイン済み CLI、Discord / Slack のチャット連携、監視ツールを会話につなぐ方法です。AWS と Google Cloud は [16 クラウドのアカウント](16-cloud-accounts.ja.md) にあります。

## セッションで `gh` と `bkt` を使う

GitHub を接続していれば、セッションの `gh` はサインイン済みです。Bitbucket Cloud
（bitbucket.org）を接続していれば、Bitbucket CLI の `bkt` も同様で、`bkt pr list`・
`bkt pr create`・`bkt pipeline` などに `auth login` は要りません。どちらも保存済みの接続を使い、
git と同じ要領で更新されます。

- **スコープ**: `bkt` は接続に与えられた権限の範囲でしか動きません。プルリクエストと
  パイプラインには、Bitbucket の OAuth アプリに `repository`・`pullrequest:write`・`pipeline`（読み取り）・
  `account` が、パイプラインの開始・停止にはさらに `pipeline:write` が必要です。貼り付けた API トークンには
  対応する `read:` / `write:` スコープ（例: `read:pullrequest:bitbucket`、パイプラインの開始・停止には
  `write:pipeline:bitbucket`。write は read を含みません）が必要です。プルリクエストのスコープが無いトークンは
  リポジトリ一覧は取れても `bkt pr list` が `403 Forbidden` になります。OAuth アプリのスコープは
  テナント管理者が設定します。
- **Bitbucket Data Center**（bitbucket.org 以外のホスト）は設定画面からは接続できません。
  自分で設定してください（`bkt auth login`、または `BKT_HOST` と `BKT_TOKEN`）。設定があると
  ラッパーは `bkt` に手を出しません。

## セッションで `jira` を使う

Jira を接続していれば（設定の**課題管理**）、Jira CLI の `jira` はどのセッションでも、どの
エージェントでも使えます。`jira init` でトークンを聞かれることも、ログインもありません。
保存済みの接続を使い、OAuth 接続は呼び出しのたびに更新されるので、トークンの期限切れで
セッションを再起動する必要はありません。課題の表示・一覧・作成・編集・コメント・遷移が
できます: `jira issue view PROJ-1`、`jira issue list -p PROJ`、`jira issue create -p PROJ`、
`jira issue comment add PROJ-1 "…"`、`jira issue move PROJ-1 "In Progress"`。

- **課題の作成**: `jira issue create -p PROJ …` は `jira init` なしで使えます。設定に課題タイプが
  ないとき、ラッパーが Jira からそのプロジェクトの課題タイプを読み（既定のスコープだけで足ります）、
  それを加えた設定のコピーを生成して `jira` に渡します。対象はラッパーが生成した設定と
  `~/.config/agent-fleet/jira/` の中の設定だけで、`-p`（またはその設定の既定プロジェクト）が
  必要です。取得できないときは 1 行のヒントを出し、`jira` は `invalid issue types in config` で
  失敗します。その場合は次の `jira init` を実行してください（正確なボード名は `jira board list -p PROJ` で
  分かります）。`jira init` が書いた設定の課題タイプはそのプロジェクト専用です（チーム管理
  プロジェクトでは ID がプロジェクトごとに違います）。そこで `-p` に別のプロジェクトを指定すると、
  ラッパーはそのプロジェクトの課題タイプを入れ、設定のカスタムフィールド（最初のプロジェクトのもの
  なので）を除いたコピーを `jira` に渡します。課題タイプを読めないときは設定をそのまま使い、
  `jira init -c ~/.config/agent-fleet/jira/<名前>.yml --project PROJ --board "PROJ board"` で
  そのプロジェクト専用の設定を作るよう、ヒントで案内します。別プロジェクトへの `--parent` 付きの作成は、
  設定からそのプロジェクトの種別が分からないため、常にこの案内になります。
- **担当者の指定**: 担当者の検索（`jira issue assign`、および `issue edit`・`create`・`move` の
  `-a` / `--assignee`）は、設定の既定プロジェクトか `-p` のプロジェクトを対象にするため、
  プロジェクトが必要です。既定プロジェクトがない（まだ `jira init` していない）ときは、
  `jira issue assign PROJ-1 <ユーザー>` が課題キーの接頭辞からプロジェクトを補います。
  ほかのコマンドは 1 行のヒントを出します: `-p PROJ` を付けるか、
  `jira init --project PROJ --board "PROJ board"` を実行してください。明示した `-p` や、
  プロジェクトを持つ設定は上書きしません。
- **プロジェクト・ボード・スプリント・エピック**: 一度 `jira init --project PROJ --board "PROJ board"`
  を実行してください（前の設定を置き換えるときは `--force` を付けます）。既定のプロジェクト・
  ボード・課題タイプ・エピック用フィールドが書き込まれ、`jira sprint list`・エピック系のコマンドが
  使えるようになります。質問はされません。実行しない場合は、コマンドごとに `-p PROJ` を
  付けてください。2 つ目のボードは
  `jira init -c ~/.config/agent-fleet/jira/other-board.yml --project PROJ --board "Other board"`
  で作り、`jira -c ~/.config/agent-fleet/jira/other-board.yml …` で使います。接続が使われるのは
  `~/.config/agent-fleet/jira/` の中の設定だけです。
- **コメントや説明文の Markdown**: CLI は Markdown を Jira の記法に変換しますが、最初の記法（見出し、
  リスト、太字、リンク）が出てくるまで、記法のない段落の直後に来るリスト・見出し・コードブロック・引用がその段落に
  貼り付きます（段落が続くだけなら問題ありません）。段落、空行、リストの順に書くと、こう送られます:

  ```
  Intro.          →  Intro.* a
  （空行）
  - a
  ```

  本文は見出しかリストで始めるか、最初の段落に太字・リンク・`code` などのインライン記法を入れ、リストの前には
  必ず空行を置いてください。エージェントにも同じことを伝えてあります。
- **Atlassian への通信が不安定なとき**: 読み取り系のコマンド（`issue view` / `issue list`、`board`・`sprint`・
  `epic`・`project` の `list`、`me`）は、接続のリセットやタイムアウトの後に最大 2 回やり直され、最後の
  実行の出力だけが表示されます。変更を伴うコマンドは決してやり直されないので、`issue create` や
  `comment add` が失敗した、または時間切れになったら、再実行する前に Jira を確認してください
  （例: JQL `project = PROJ AND creator = currentUser() AND created >= -15m`）。`comment edit` と `comment delete` は
  自分でやり直し、「probably already deleted」と出たときは削除は通っています。
  `jira` は応答しないサーバを永遠に待つことがあるため、端末のない実行は `AF_JIRA_TIMEOUT` 秒
  （既定は変更を伴うコマンドが 120 秒、読み取り系は 1 回あたり 45 秒。`0` で無効）で止められます。読み取り系は
  接続リセットのときと同じようにやり直され、変更を伴うコマンドは終了コード 124 で、課題やコメントが
  作られている可能性があると表示します（`issue create` では、直近 15 分以内に自分が同じ件名で作成した課題を
  「possible matches」として併記し、見つからなければ「no matching issue found」と出します。終了コードは 124 のままです）。また `jira` は `-b` を付けても標準入力を最後まで読むため、本文を
  コマンドラインで渡したときは、ラッパーが空の入力に差し替えます。それでも止まったままのときは、`jira`
  プロセスの `/proc/<pid>/wchan` で、入力待ち（`pipe_read`、`unix_stream_data_wait`）とネットワーク待ちを
  見分けられます。認可の期限切れや取り消しのときは、
  API トークンを求める代わりに「the Jira connection is unusable」と表示するので、設定で Jira を接続し直して
  ください。端末ではない出力では、課題へのリンクがお使いの Jira サイトを指します。
- **代わりに Atlassian MCP ツールを使う**: `jira` が既定なのは、どのエージェントでも使え、トークンも
  自動で更新されるからです。MCP コネクタは 1 つのエージェントにしか使えず、ログインが切れるとセッションの
  再起動が要ります。MCP を優先したいときは、**⚙設定 →「エージェントへの指示」** に書いてください。例:
  「Jira は `jira` CLI ではなく Atlassian MCP ツールを使うこと。」コネクタを持つエージェントはそれに従い、
  持たないエージェントは `jira` を使い続けます。この一文がなければ、MCP ツールを使うのは CLI にできない
  操作だけです。
- **コメントの編集と削除**: `jira issue comment edit PROJ-1 10001 -b "…"` と
  `jira issue comment delete PROJ-1 10001`。`jira` 本体にあるのは `comment add` だけで、この 2 つは
  Agent Fleet が足したものです。`edit` は新しい本文を `-b`、`-T FILE`（`-` は標準入力）またはパイプで
  受け取り、Markdown を Jira 用に変換して送るので、崩れたコメント（上記）の直しにも使えます。
  コメント ID は `jira issue view PROJ-1 --raw` の `fields.comment.comments[].id` で分かります。
  Jira で変更できるのは通常、自分のコメントだけです（他人のものは、コメント管理の権限がなければ 403）。エージェントには、そのセッションで
  自分が投稿したコメントか、あなたが指名したコメントだけを編集・削除するよう伝えてあります。
  使えるのは保存済みの接続だけで、`--debug`、`JIRA_API_TOKEN`、自分で用意した設定では
  `jira` 本体が「そのコマンドはない」と答えます。
- **`--debug` では接続を使いません**: jira のデバッグ出力は認可ヘッダーまで表示するので、
  ラッパーはデバッグ実行にトークンを渡しません。
- **変更できる範囲**は接続の権限どおりです。CLI はあなたとして Jira に書き込むので、課題の
  作成・編集・遷移にはそれが許されたアカウントが必要です。
- **OAuth でボード・スプリント・エピックを使う**には、テナントの Jira アプリに Jira Software の
  スコープが要ります。
  テナント管理者が有効にしたあと、あなたが一度 **Jira を接続し直して**
  ください。それまでは、これらのコマンドと `jira init` は認可エラーになります。設定 > 課題管理の
  Jira カードに「ボード・スプリント・エピック: 利用できません」と出て、`jira` もそれらの
  コマンドの前に同じ説明を表示します。また、CLI が
  表示するリンクはサイトではなく `api.atlassian.com` を指します。
- **Jira Data Center**（またはほかの Jira）は設定画面からは接続できません。自分で設定して
  ください（自分の `--server` を付けた `jira init`、`~/.config/agent-fleet/jira/` の外の設定、
  または `JIRA_API_TOKEN`）。設定があるとラッパーは `jira` に手を出しません。

## サインイン済みの CLI

次のコマンドは、セッションでも端末でも、どのエージェントでも、設定でつないだ接続を使います。端末でログインする
必要はなく、期限が近いトークンは使うときに更新されます。

| コマンド | サインインのもと | 詳しく |
|---|---|---|
| `git` | **Git ホスティング**（GitHub、Bitbucket） | [06](06-code.ja.md#git-プロバイダを接続する) |
| `gh` | **Git ホスティング**の GitHub | [15](15-external-services.ja.md#セッションで-gh-と-bkt-を使う) |
| `bkt` | **Git ホスティング**の Bitbucket Cloud（bitbucket.org） | [15](15-external-services.ja.md#セッションで-gh-と-bkt-を使う) |
| `jira` | **課題管理**の Jira | [15](15-external-services.ja.md#セッションで-jira-を使う) |
| `af-aws-exec --profile <名前> -- <コマンド>` | **AWS プロファイル/SSM**。プロファイルの**ログイン**のあと | [16](16-cloud-accounts.ja.md#プロファイルを端末sdkビルドツールから使う) |
| `af-gcloud-exec --profile <名前> -- <コマンド>` | **Google Cloud**。プロファイルの**ログイン**のあと | [16](16-cloud-accounts.ja.md#プロファイルとしてコマンドを実行する) |

エージェントには、Bitbucket では Bitbucket や Atlassian の MCP サーバーではなく `bkt` を、Jira では既定で `jira` を使うよう
伝えてあります。CLI は呼び出しのたびに保存済みの接続を読み、期限が近ければ更新しますが、MCP コネクタはログインが切れると
セッションの再起動が必要になるためです。Jira で Atlassian MCP ツールを優先させたいときは
[15](15-external-services.ja.md#セッションで-jira-を使う)（「代わりに Atlassian MCP ツールを使う」）を見てください。

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
- **フリート・オペレーター**：常設スレッド「🛰 フリート・オペレーター」に書き込むと、
  [11 たくさんのセッションを動かす](11-many-sessions.ja.md)とチャットから会話できます
  （Console 側のオペレーターと同一会話）。チャット発の破壊的操作（削除など）は、
  実行前に「承認 / 却下」ボタンで一旦止まります。
- **通知だけ止めたい**：個人設定 → 「通知」タブの**サービス通知**で、接続を切らずに
  送信だけオフにできます。

## PagerDuty / Grafana / CloudWatch / AWS は「運用・監視」タブから接続できます（推奨）

**PagerDuty・Grafana・CloudWatch・AWS は製品機能として組み込み済み**です。手作業の PoC 手順（後述）より、まずこちらを使ってください。

1. **設定 > 運用・監視** タブを開き、各カードに接続情報を入れて「接続」:
   - **PagerDuty**: API キー。**読み取り専用キー**を推奨します（PagerDuty の Integrations > API Access Keys で「Read-only」を選択）。EU アカウントはトグルをオンにしてください。
   - **Grafana**: インスタンス URL と**サービスアカウントトークン**（Viewer 権限を推奨）。セルフホスト / Grafana Cloud / **Amazon Managed Grafana** のいずれも可（AMG は URL に workspace endpoint を指定。トークンの発行方法と 30 日期限は後述の AMG 節を参照）。
   - **CloudWatch**: プルダウンから **SSM 接続のプロファイルを選ぶ**だけ（リージョンは任意で上書き可）。**秘密の入力はありません**。プロファイルの SSO 設定（非秘密）から専用の設定ファイルを生成し、コンテナ内の AWS 資格をそのまま読みます。SSO ログインがまだ／期限切れの場合はツールがエラーになるので、設定 › [AWS プロファイル/SSM](16-cloud-accounts.ja.md#社内の別ホストに入るssm) でそのプロファイルの **ログイン** を押すか、該当の SSM セッションを一度開いてください。自分で `~/.aws` を管理している人は「手動入力」でプロファイル名を直接指定できます。
   - **AWS**（Agent Toolkit for AWS）: AWS が提供する MCP サーバーに接続します。プロファイルの指定は CloudWatch と同じ（SSM 接続のプロファイルを選ぶだけ・秘密の入力なし）。加えて 2 つ設定があります。
     - **MCP エンドポイント**: MCP サーバー自体が動くリージョン（`us-east-1` / `eu-central-1`）。**自分のリソースがあるリージョンとは別のもの**で、そちらは上の「リージョン」欄に入れます。
     - **書き込みツール**: 既定オフ（読み取り専用）。オンにすると AWS API 呼び出し（`call_aws`）とスクリプト実行（`run_script`）が使えるようになり、**実際に AWS のリソースを作成・変更・削除できます**。必要なときだけオンにしてください。
     - AWS だけは**対話セッションからも使えます**（他の 3 つはチャット専用）。AWS ドキュメント検索・スキル取得・AWS API 参照が、コードを書いているセッションでそのまま使えます。
     - SSO ログインがまだ／期限切れの場合は、設定 › [AWS プロファイル/SSM](16-cloud-accounts.ja.md#社内の別ホストに入るssm) でそのプロファイルの **ログイン** を押すか、該当の SSM セッションを一度開いてください。
2. 資格情報はワークスペース内に暗号化保存され、MCP サーバの起動時にだけ渡されます（設定ファイルや平文には残りません）。Grafana は書き込み・管理ツール無効で起動、CloudWatch はサーバ自体が読み取り専用ツールのみ、AWS は既定で `--read-only` 起動です。
3. チャットで **「SRE アシスタント」** を選んで新しい会話を開始。「今開いている PagerDuty のインシデントを一覧して経緯をまとめて」「このサービスの直近 1 時間のエラーレートを Grafana で見て」「CloudWatch でこのロググループの ERROR を分析して」のように聞くと、実データを確認しながら状況整理・原因の仮説出し・対外報告の草稿を手伝います（読み取り専用。ack/resolve はしません）。
4. 接続の変更は**次のチャット送信から反映**されます（AWS をセッションで使う場合は**次のセッション起動から**）。ワークスペースの再起動は不要です。

Zabbix など他ツールは、設定 > 接続 > [MCP サーバー](20-settings.ja.md#mcp-サーバー) にその MCP サーバーを登録してください。下の手作業の PoC 手順は、ターミナル（CLI）の claude セッションから試す用途に残しています。

---

## （PoC）その他ツールを手動で繋ぐ 🧪

**実験的な手順です。**「運用・監視」タブに無いツール（Zabbix など）や、チャットではなく**ターミナル（CLI）の claude セッション**に繋ぎたい場合の手作業手順です。

- 対象: ターミナル（CLI）の claude セッション。**チャット（アシスタント）にツールを持たせるには、設定 > 接続 > [MCP サーバー](20-settings.ja.md#mcp-サーバー) で登録します**。対象にアシスタントを含めて登録し、アシスタントの編集フォームの「MCP サーバー（任意）」で選んでください。編集できるのは自分で作ったアシスタントだけで、組み込みのもの（SRE アシスタント・フリート・オペレーター・Agent Fleet アシスタント）は編集できません。`claude mcp add` で手作業で足したサーバーは付け方になりません。その選択肢には出てこず、MCP サーバーや Agent Fleet のツールを付けた claude ベースのアシスタントは、それを含まない厳格な MCP 構成で動きます。
- 前提: ワークスペースから各監視ツールのエンドポイントへ outbound が通ること。PyPI 系は `uvx` の初回取得で必要。
- ⚠️ **トークンの扱い（PoC 限定の妥協）**: `claude mcp add -e` で渡したトークンは `~/.claude.json` に**平文で保存されます**。home ボリューム内でコンテナ recreate では消えませんが、リポジトリには絶対に書かないこと・**read-only の専用トークンだけ**を使うこと。登録元のトークンを暗号化して保管したいときは、代わりに [MCP サーバー](20-settings.ja.md#mcp-サーバー) に登録してください。登録情報は暗号化して保存されますが、CLI に渡す構成にはトークンが含まれることがあります。

## 0. 下ごしらえ（1 回だけ・recreate 後も残る）

```bash
mkdir -p ~/.local/bin
# uv/uvx（Python 系 MCP サーバのランチャ。~/.local に入るので永続）
pip install --user uv
```

## 1. Grafana（メトリクス・ログ・アラート・OnCall）

Go の単一バイナリで、最も軽量で、機能も最も充実している。**検証済み**: v0.17.1 は `-disable-write -disable-admin` で read-only 52 ツール（Prometheus/Loki クエリ、ダッシュボード検索、アラート、Incident/OnCall 参照、Sift 分析。create/update/delete/install 系ゼロ）になる。Grafana のデータソース経由で CloudWatch / Athena / Elasticsearch 等を引くツールも同梱。

```bash
# バイナリ取得（~/.local/bin に置くと永続）
curl -sL https://github.com/grafana/mcp-grafana/releases/download/v0.17.1/mcp-grafana_Linux_x86_64.tar.gz \
  | tar xz -C ~/.local/bin mcp-grafana

# Grafana 側: 管理画面で Viewer 権限のサービスアカウントを作りトークン発行

# claude に登録（user スコープ = このワークスペースの全セッションで有効）
claude mcp add -s user grafana \
  -e GRAFANA_URL=https://grafana.example.com \
  -e GRAFANA_SERVICE_ACCOUNT_TOKEN=<viewer-sa-token> \
  -- ~/.local/bin/mcp-grafana -disable-write -disable-admin
```

**Amazon Managed Grafana（AMG）の場合**も同じ手順で繋がる（認証はセルフホストと同じサービスアカウントトークン。IAM/SigV4 は不要）。違いは 2 点だけ:

- `GRAFANA_URL` は workspace endpoint（`https://g-xxxxxxxxxx.grafana-workspace.<region>.amazonaws.com`）。
- トークンは AMG の Grafana 管理画面（Administration → Service accounts、要 admin）か、IAM 権限があれば AWS CLI でも発行できる（**最長 30 日**で失効するので期限管理に注意）:

```bash
aws grafana create-workspace-service-account-token \
  --workspace-id g-xxxxxxxxxx --service-account-id <sa-id> \
  --name poc-$(date +%Y%m%d) --seconds-to-live 604800   # 7日。応答の key がトークン（再表示不可）
```

## 2. PagerDuty（インシデント・オンコール）

公式 self-host 版（Python / PyPI `pagerduty-mcp`）。**既定で read-only**、write 系は `--enable-write-tools` を付けない限り出ない。トークンは PagerDuty の User API Token（My Profile → User Settings）。

```bash
claude mcp add -s user pagerduty \
  -e PAGERDUTY_USER_API_KEY=<user-api-token> \
  -- ~/.local/bin/uvx pagerduty-mcp
# EU アカウントは -e PAGERDUTY_API_HOST=https://api.eu.pagerduty.com を追加
```

> hosted 版（`https://mcp.pagerduty.com/mcp`、remote HTTP）もあるが、**既定で write ツールまで出る**ため PoC では self-host + read-only 既定を推奨。

## 3. CloudWatch（アラーム起点調査・ログ分析）

### 3a. まずは MCP なしで: aws CLI で直接ログを見る（最速）

ターミナル（CLI）の claude セッションは Bash が使えるので、**MCP を繋がなくても焼き込み済みの aws CLI でログ確認の壁打ちができます**。SSM 接続で使っているプロファイルでログインしてあれば（設定 › AWS プロファイル/SSM の **ログイン**）追加設定ゼロです。`AWS_PROFILE` を export するのではなく、`af-aws-exec` で毎回プロファイルを名指ししてください（理由は [16](16-cloud-accounts.ja.md#プロファイルを端末sdkビルドツールから使う)）。claude に「`<ロググループ>` の直近 1 時間の ERROR を見て」と頼めば、以下のようなコマンドを自分で叩いて調べてくれます。

```bash
awsx() { af-aws-exec --profile <name> --region ap-northeast-1 -- aws "$@"; }

awsx logs describe-log-groups --log-group-name-prefix /aws/   # ロググループ探し
awsx logs tail /aws/ecs/my-service --since 1h                 # 直近ログ（--follow で追尾）
awsx logs filter-log-events --log-group-name /aws/ecs/my-service \
  --start-time $(date -d '1 hour ago' +%s)000 --filter-pattern ERROR

# 集計や横断は Logs Insights（start-query → get-query-results の 2 段）
qid=$(awsx logs start-query --log-group-name /aws/ecs/my-service \
  --start-time $(date -d '3 hours ago' +%s) --end-time $(date +%s) \
  --query-string 'filter @message like /ERROR/ | stats count(*) by bin(5m)' \
  --query queryId --output text)
awsx logs get-query-results --query-id $qid
```

必要な IAM は読み取りのみ（`CloudWatchLogsReadOnlyAccess` 相当: DescribeLogGroups / FilterLogEvents / GetLogEvents / StartQuery / GetQueryResults）。

### 3b. MCP で繋ぐ（アラーム分析・異常検知ツールが欲しいとき）

AWS 公式（awslabs）の MCP サーバ。資格は焼き込み済み aws CLI と同じチェーンを読むので、**SSO プロファイルがあれば追加の秘密は不要**。全ツール read-only（メトリクス取得・アラーム履歴・ロググループの異常パターン分析・Logs Insights クエリなど）。

```bash
# 事前にプロファイルへログインしておく（設定 › AWS プロファイル/SSM の「ログイン」）
claude mcp add -s user cloudwatch \
  -e AWS_PROFILE=<sso-profile> \
  -e FASTMCP_LOG_LEVEL=ERROR \
  -- ~/.local/bin/uvx awslabs.cloudwatch-mcp-server@latest
```

※ チャットの SRE アシスタントで使うだけなら、この手順は不要です（冒頭の「運用・監視」タブから接続してください）。この手順はターミナル（CLI）の claude セッションに繋ぎたい場合用です。

Athena は (a) まず素の aws CLI（追加設定ゼロ、claude が Bash で叩ける）、(b) 本格的には `uvx awslabs.aws-dataprocessing-mcp-server@latest`。PoC では (a) で十分なことが多い。

## 4. Zabbix

initMAX 製（事実上の標準、read_only 設定・全 API 対応）は **systemd 常駐のチーム共有サービス**として立てて remote HTTP で繋ぐ設計なので、個人 PoC には重い。軽く試すなら PyPI の stdio 版（例: `uvx zabbix-mcp`、`ZABBIX_URL`/`ZABBIX_TOKEN`）で雰囲気を掴み、本採用の評価時に initMAX を検討する。

## 5. 動作確認と壁打ちの試し方

```bash
claude mcp list        # 登録一覧と接続ヘルス
```

セッション（claude）を開いて、実インシデントで試す例:

- 「PagerDuty で今開いているインシデントを一覧して、最新のものの経緯をまとめて」
- 「そのサービスの直近 1 時間のエラーレートを Grafana の Prometheus で引いて、アラート発火時刻と突き合わせて」
- 「CloudWatch で該当 Lambda のログから ERROR パターンを分析して、時系列で何が起きたか仮説を 3 つ」
- 「ここまでの調査を、経緯 → 影響範囲 → 原因仮説 → 次アクションの形式で対外報告向けに整理して」

評価したい観点: ツール横断の状況整理が速いか / 仮説の質 / 対外文案の使い物度 / トークン消費と応答速度。

## 6. 片付け

```bash
claude mcp remove -s user grafana
claude mcp remove -s user pagerduty
claude mcp remove -s user cloudwatch
```

トークンを失効させるのも忘れずに（Grafana SA トークン削除・PagerDuty User API Token 削除）。

## 手作業の経路の制約

- トークンが `~/.claude.json` 平文（→ 登録元の情報を暗号化して保管するなら [MCP サーバー](20-settings.ja.md#mcp-サーバー) に登録する）
- `claude mcp add` で足したサーバーはチャット/アシスタントに付けられない（→ 代わりに [MCP サーバー](20-settings.ja.md#mcp-サーバー) に登録し、自分で作ったアシスタントに付ける）
- アラート本文やログは**攻撃者が影響を与えられる入力**。read-only 構成を崩さないこと。write を試すときは専用アシスタント/セッションで明示的に
- uvx 系は初回起動時に PyPI から取得（egress 必要）。メモリ制約ホストでは同時起動しすぎない
