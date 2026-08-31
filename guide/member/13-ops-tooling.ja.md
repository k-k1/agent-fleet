---
audience: "監視ツールを会話につなぐ人"
updated: "2026-08"
---

# 13. 運用ツール連携（MCP でインシデント壁打ち）🧪

[English](13-ops-tooling.md) | 日本語

## PagerDuty / Grafana / CloudWatch / AWS は「運用・監視」タブから接続できます（推奨）

**PagerDuty・Grafana・CloudWatch・AWS は製品機能として組み込み済み**です（docs/25 Phase 1、要イメージ再ビルド）。手作業の PoC 手順（後述）より、まずこちらを使ってください。

1. **設定 > 運用・監視** タブを開き、各カードに接続情報を入れて「接続」:
   - **PagerDuty**: API キー。**読み取り専用キー**を推奨します（PagerDuty の Integrations > API Access Keys で「Read-only」を選択）。EU アカウントはトグルをオンにしてください。
   - **Grafana**: インスタンス URL と**サービスアカウントトークン**（Viewer 権限を推奨）。セルフホスト / Grafana Cloud / **Amazon Managed Grafana** のいずれも可（AMG は URL に workspace endpoint を指定。トークンの発行方法と 30 日期限は後述の AMG 節を参照）。
   - **CloudWatch**: プルダウンから **SSM 接続のプロファイルを選ぶ**だけ（リージョンは任意で上書き可）。**秘密の入力はありません** — プロファイルの SSO 設定（非秘密）から専用の設定ファイルを生成し、コンテナ内の AWS 資格をそのまま読みます。SSO ログインがまだ／期限切れの場合はツールがエラーになるので、該当の SSM セッションを一度開くか、ターミナルで `AWS_CONFIG_FILE=~/.aws/af-ops/cloudwatch.config aws sso login --profile プロファイル名` を実行してください。自分で `~/.aws` を管理している人は「手動入力」でプロファイル名を直接指定できます。
   - **AWS**（Agent Toolkit for AWS）: AWS が提供する MCP サーバーに接続します。プロファイルの指定は CloudWatch と同じ（SSM 接続のプロファイルを選ぶだけ・秘密の入力なし）。加えて 2 つ設定があります。
     - **MCP エンドポイント**: MCP サーバー自体が動くリージョン（`us-east-1` / `eu-central-1`）。**自分のリソースがあるリージョンとは別物**で、そちらは上の「リージョン」欄に入れます。
     - **書き込みツール**: 既定オフ（読み取り専用）。オンにすると AWS API 呼び出し（`call_aws`）とスクリプト実行（`run_script`）が使えるようになり、**実際に AWS のリソースを作成・変更・削除できます**。必要なときだけオンにしてください。
     - AWS だけは**対話セッションからも使えます**（他の 3 つはチャット専用）。AWS ドキュメント検索・スキル取得・AWS API 参照が、コードを書いているセッションでそのまま使えます。
     - SSO ログインがまだ／期限切れの場合は、該当の SSM セッションを一度開くか、ターミナルで `AWS_CONFIG_FILE=~/.aws/af-ops/aws.config aws sso login --profile プロファイル名` を実行してください。
2. 資格情報はワークスペース内に暗号化保存され、MCP サーバの起動時にだけ渡されます（設定ファイルや平文には残りません）。Grafana は書き込み・管理ツール無効で起動、CloudWatch はサーバ自体が読み取り専用ツールのみ、AWS は既定で `--read-only` 起動です。
3. チャットで **「SRE アシスタント」** を選んで新しい会話を開始。「今開いている PagerDuty のインシデントを一覧して経緯をまとめて」「このサービスの直近 1 時間のエラーレートを Grafana で見て」「CloudWatch でこのロググループの ERROR を分析して」のように聞くと、実データを確認しながら状況整理・原因の仮説出し・対外報告の草稿を手伝います（読み取り専用。ack/resolve はしません）。
4. 接続の変更は**次のチャット送信から反映**されます（AWS をセッションで使う場合は**次のセッション起動から**）。ワークスペースの再起動は不要です。

Zabbix など他ツールは、下の PoC 手順で手動接続できます（順次「運用・監視」タブに取り込み予定）。

---

## （PoC）その他ツールを手動で繋ぐ 🧪

**実験的な手順です。**まだ「運用・監視」タブに無いツール（CloudWatch / Zabbix など）や、チャットではなく**ターミナル（CLI）の claude セッション**に繋ぎたい場合の手作業手順です。

- 対象: ターミナル（CLI）の claude セッション。**チャット（アシスタント）には現状 MCP を足せません**（Phase 1 で対応予定）。
- 前提: ワークスペースから各監視ツールのエンドポイントへ outbound が通ること。PyPI 系は `uvx` の初回取得で必要。
- ⚠️ **トークンの扱い（PoC 限定の妥協）**: `claude mcp add -e` で渡したトークンは `~/.claude.json` に**平文で保存されます**。home ボリューム内でコンテナ recreate では消えませんが、リポジトリには絶対に書かないこと・**read-only の専用トークンだけ**を使うこと。この平文問題の解消（Connections への統合）が Phase 1 の主目的です。

## 0. 下ごしらえ（1 回だけ・recreate 後も残る）

```bash
mkdir -p ~/.local/bin
# uv/uvx（Python 系 MCP サーバのランチャ。~/.local に入るので永続）
pip install --user uv
```

## 1. Grafana（メトリクス・ログ・アラート・OnCall）

Go 単一バイナリで最軽量・最充実。**検証済み**: v0.17.1 は `-disable-write -disable-admin` で read-only 52 ツール（Prometheus/Loki クエリ、ダッシュボード検索、アラート、Incident/OnCall 参照、Sift 分析。create/update/delete/install 系ゼロ）になる。Grafana のデータソース経由で CloudWatch / Athena / Elasticsearch 等を引くツールも同梱。

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

ターミナル（CLI）の claude セッションは Bash が使えるので、**MCP を繋がなくても焼き込み済みの aws CLI でログ確認の壁打ちができます**。SSM 接続で使っている SSO プロファイルでログインしてあれば追加設定ゼロです。claude に「`<ロググループ>` の直近 1 時間の ERROR を見て」と頼めば、以下のようなコマンドを自分で叩いて調べてくれます。

```bash
aws sso login --profile <sso-profile>          # オンコール開始時に済ませておく
export AWS_PROFILE=<sso-profile> AWS_REGION=ap-northeast-1

aws logs describe-log-groups --log-group-name-prefix /aws/   # ロググループ探し
aws logs tail /aws/ecs/my-service --since 1h                 # 直近ログ（--follow で追尾）
aws logs filter-log-events --log-group-name /aws/ecs/my-service \
  --start-time $(date -d '1 hour ago' +%s)000 --filter-pattern ERROR

# 集計や横断は Logs Insights（start-query → get-query-results の 2 段）
qid=$(aws logs start-query --log-group-name /aws/ecs/my-service \
  --start-time $(date -d '3 hours ago' +%s) --end-time $(date +%s) \
  --query-string 'filter @message like /ERROR/ | stats count(*) by bin(5m)' \
  --query queryId --output text)
aws logs get-query-results --query-id $qid
```

必要な IAM は読み取りのみ（`CloudWatchLogsReadOnlyAccess` 相当: DescribeLogGroups / FilterLogEvents / GetLogEvents / StartQuery / GetQueryResults）。

### 3b. MCP で繋ぐ（アラーム分析・異常検知ツールが欲しいとき）

AWS 公式（awslabs）。資格は焼き込み済み aws CLI と同じチェーンを読むので、**SSO プロファイルがあれば追加の秘密は不要**。全ツール read-only（メトリクス取得・アラーム履歴・ロググループの異常パターン分析・Logs Insights クエリなど）。

```bash
# 事前にコンテナ内で aws sso login 済みであること（SSM セッションと同じ流儀）
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

評価したい観点（docs/25 の UC1/UC2）: ツール横断の状況整理が速いか / 仮説の質 / 対外文案の使い物度 / トークン消費と応答速度。

## 6. 片付け

```bash
claude mcp remove -s user grafana
claude mcp remove -s user pagerduty
claude mcp remove -s user cloudwatch
```

トークンを失効させるのも忘れずに（Grafana SA トークン削除・PagerDuty User API Token 削除）。

## 既知の制約（= Phase 1 以降で解消する予定のもの）

- トークンが `~/.claude.json` 平文（→ Connections + secrets.enc へ）
- チャット/アシスタントでは使えない（→ `chatMCPArgs` のカタログ駆動化）
- アラート本文やログは**攻撃者が影響を与えられる入力**。read-only 構成を崩さないこと。write を試すときは専用アシスタント/セッションで明示的に
- uvx 系は初回起動時に PyPI から取得（egress 必要）。メモリ制約ホストでは同時起動しすぎない
