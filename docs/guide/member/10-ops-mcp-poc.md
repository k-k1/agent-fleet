# 10. 運用ツール連携（MCP でインシデント壁打ち）🧪

## PagerDuty は「運用」タブから接続できます（推奨）

**PagerDuty は製品機能として組み込み済み**です（docs/25 Phase 1、要イメージ再ビルド）。手作業の PoC 手順（後述）より、まずこちらを使ってください。

1. **設定 > 運用** タブを開き、PagerDuty カードに API キーを貼って「接続」。**読み取り専用キー**を推奨します（PagerDuty の Integrations > API Access Keys で「Read-only」を選択）。EU アカウントはチェックボックスを入れてください。
2. キーはワークスペース内に暗号化保存され、MCP サーバの起動時にだけ渡されます（設定ファイルや平文には残りません）。
3. チャットで **「SRE アシスタント」** を選んで新しい会話を開始。「今開いている PagerDuty のインシデントを一覧して経緯をまとめて」のように聞くと、実データを確認しながら状況整理・原因の仮説出し・対外報告の草稿を手伝います（読み取り専用。ack/resolve はしません）。
4. 接続の変更は**次のチャット送信から反映**されます（ワークスペースの再起動は不要）。

Grafana / CloudWatch など他ツールは、下の PoC 手順で手動接続できます（順次「運用」タブに取り込み予定）。

---

## （PoC）その他ツールを手動で繋ぐ 🧪

**実験的な手順です**（[docs/25 サービス運用向け拡張](../../25-ops-monitoring.md) の Phase 0）。まだ「運用」タブに無いツール（Grafana / CloudWatch など）を、現行の Workspace のまま手作業で claude の**対話セッション**に繋ぐための手順です。

- 対象: 対話セッション（tmux の claude）。**チャット（アシスタント）には現状 MCP を足せません**（Phase 1 で対応予定）。
- 前提: Workspace から各監視ツールのエンドポイントへ outbound が通ること。PyPI 系は `uvx` の初回取得で必要。
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

# claude に登録（user スコープ = この Workspace の全セッションで有効）
claude mcp add -s user grafana \
  -e GRAFANA_URL=https://grafana.example.com \
  -e GRAFANA_SERVICE_ACCOUNT_TOKEN=<viewer-sa-token> \
  -- ~/.local/bin/mcp-grafana -disable-write -disable-admin
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

AWS 公式（awslabs）。資格は焼き込み済み aws CLI と同じチェーンを読むので、**SSM 接続で使っている SSO プロファイルがあれば追加の秘密は不要**。

```bash
# 事前にコンテナ内で aws sso login 済みであること（SSM セッションと同じ流儀）
claude mcp add -s user cloudwatch \
  -e AWS_PROFILE=<sso-profile> \
  -e FASTMCP_LOG_LEVEL=ERROR \
  -- ~/.local/bin/uvx awslabs.cloudwatch-mcp-server@latest
```

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
