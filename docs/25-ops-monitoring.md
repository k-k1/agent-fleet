# 25. サービス運用（監視・インシデント対応）向け拡張 — 検討

**Status: 📋 構想検討（実装未着手・意思決定前）** — 2026-07-12 起草

サービス運用担当者（SRE / オンコール / 運用チーム）が agent-fleet を「インシデント対応・監視運用の壁打ち相手」として使えるようにする拡張の検討。PagerDuty / CloudWatch / Zabbix / Grafana / Slack / Teams / Athena などとの連携を含む。

> 本書は検討ドキュメントであり、採否・優先度は未決。実装に進む場合は §8 の論点を ADR で決着させてから、Phase ごとに個別プランへ落とす。

---

## 0. 要旨

- **agent-fleet は既に運用向け拡張の土台をほぼ持っている。** headless チャット基盤（assistant-chat）、アシスタント（ペルソナ＋ツールゲート）機構、暗号化 Connections、コンテナ内 stdio MCP、AWS SSO 資格のコンテナ内完結、memo キュー、監査/egress 設計。欠けているのは「外部 MCP サーバの管理・注入」「webhook 受信」「Slack/Teams 通知」「スケジュール実行」の 4 点だけ。
- **個別連携（PagerDuty API クライアント等）を自前実装する必要はほぼない。** 2026-07 時点で PagerDuty・Grafana・Slack は**公式 MCP サーバ**を提供、AWS は awslabs 公式群（CloudWatch / Athena / 汎用 API）、Zabbix は事実上標準の OSS がある（§3）。
- したがって本拡張の製品価値は「**接続の管理（暗号化保存・注入）・承認カタログ・read-only 既定の統制・イベント駆動の自動初動**」に置く。連携そのものは MCP エコシステムに乗る。
- 最初の一歩は実装ゼロで検証できる（Phase 0: workspace 内で `claude mcp add` + トークン手貼りの PoC）。価値検証を先に行い、製品化（Phase 1 以降）は結果を見て判断する。

---

## 1. ペルソナとユースケース

### ペルソナ

「サービス運用担当者」。既存ロールモデル（member / tenant_admin / super_admin）に**新ロールは足さない**。member のまま、guide/ のペルソナ別分冊（既に member / admin / operator / lite 構造がある）に運用者向け分冊を足す想定。

### ユースケース（価値の高い順に検討）

| # | ユースケース | 型 | 必要になるもの |
|---|---|---|---|
| UC1 | **インシデント壁打ち**: オンコールが「PD #1234 何が起きてる?」→ エージェントが PagerDuty のインシデント詳細・関連アラート・Grafana/CloudWatch のメトリクス・ログを横断して状況整理・仮説出し・対外文案作成 | 対話（read-only） | ops 接続 + MCP 注入 + SRE アシスタント |
| UC2 | **調査・分析**: Athena でログ集計、メトリクスの長期傾向確認、ポストモーテムのタイムライン再構成と草稿作成 | 対話（read-only） | UC1 と同じ（AWS は既存 SSO 資格で大半カバー） |
| UC3 | **イベント駆動の初動**: PagerDuty webhook → 自動で初動調査（直近デプロイ・メトリクス・類似過去インシデント）→ 要約を Slack / Console へ | 自動実行 | webhook ingress + 自動実行の身元（§8-2）+ Slack 通知 |
| UC4 | **定期レポート**: 朝会向けに前日のアラートサマリ・エラーバジェット消費を毎朝生成 | スケジュール実行 | スケジューラ（現状なし） |
| UC5 | **復旧オペレーション実行**: runbook に沿った再起動・スケール変更等の実行 | 対話（write） | 承認ゲート設計。**初期スコープ外候補**（§7） |

UC1/UC2 が本命（既存資産でほぼ届く）。UC3 は面白いがアーキテクチャ追加が大きい。UC5 は慎重（§5）。

---

## 2. 現状資産の棚卸し — 何がそのまま使えるか

| 既存資産 | 実装 | 運用向け拡張での役割 |
|---|---|---|
| assistant-chat（headless CLI チャット） | `workspace/agent/chat_*.go`、`docs/history/19-assistant-chat.md` | 壁打ちの器そのもの。tmux セッションを汚さず、SSE ストリーム・会話永続・TTS まで揃っている |
| アシスタント機構（ペルソナ/モデル/tools/knowledge） | `workspace/agent/assistants.go` | 「SRE アシスタント」をビルトイン追加する差し込み口。tools ゲート（none/af_read/af_write = ツール集合でゲート）の思想を ops ツールにそのまま延長できる |
| チャットへの MCP 注入 | `chat_providers.go` の `chatMCPArgs`（`--mcp-config` + `--strict-mcp-config`） | **外部 MCP サーバを会話に足す自然な差し込み口**。現状は自前 stdio MCP（面B）1 個固定 → ここをカタログ駆動にする |
| Connections + 暗号化ストア | `workspace/agent/connections.go`、`internal/secrets/`（AES-256-GCM、鍵は CP から env 注入） | PagerDuty / Grafana / Zabbix / Slack のトークン置き場。「秘密は CP を素通りするだけ」原則ごと流用 |
| AWS SSO（kind=ssm の資格フロー） | `control-plane/ssm.go`、`workspace/agent/session_ssm.go` | **CloudWatch / Athena はほぼ追加コストゼロ**。aws CLI + SSO キャッシュがコンテナ内に既にあり、awslabs MCP も同じ資格チェーンを読む |
| memo キュー | `docs/history/21-memo-queue.md` | webhook で受けたインシデントの受け皿に流用可能（UC3 の最小形） |
| 監査 / egress 統制 | `docs/20-container-audit-egress.md`（enforce 未了） | 監視系ドメインへの外部通信・ops ツール呼び出しの統制面。本拡張が enforce の実需要になる |
| CP `/mcp`（面A、PAT 認証） | `control-plane/mcp.go` | 逆方向（外部の AI クライアントから fleet を操作）。本拡張の主役ではないが、将来「PagerDuty 側の AI から fleet セッションを起動」のような口になり得る |
| 通知 | ブラウザ Notification + TTS のみ | Slack/Teams への outbound は**新規**。TTS（`control-plane/tts.go`）が CP-native 通知系の実装パターンとして先例 |
| スケジュール / webhook | **存在しない**（CP 内 interval ジョブのみ） | UC3/UC4 には新設が必要 |

**結論: 不足は 4 点だけ** — ①外部 MCP の管理・注入、②webhook ingress、③Slack/Teams outbound、④スケジューラ。①が最小で最も価値が高い。

---

## 3. 外部エコシステムの現状（2026-07 Web 裏取り済み）

| ツール | MCP サーバ | 提供元 | 形態 | 備考 |
|---|---|---|---|---|
| PagerDuty | [pagerduty-mcp-server](https://github.com/PagerDuty/pagerduty-mcp-server) | **公式** | hosted（`mcp.pagerduty.com/mcp`）と self-host OSS の両方 | インシデント/サービス/スケジュール/オンコール/エスカレーション。read+write。hosted は既定で write も出す点に注意 |
| Grafana | [grafana/mcp-grafana](https://github.com/grafana/mcp-grafana) | **公式** | **Go 単一バイナリ** / Docker | ダッシュボード・データソース（Prometheus/Loki）クエリ・アラートルール・**Grafana Incident / OnCall / Sift**（エラーパターン検出）まで。運用系で最も充実 |
| CloudWatch | [awslabs cloudwatch-mcp-server](https://awslabs.github.io/mcp/servers/cloudwatch-mcp-server) | **AWS 公式（awslabs）** | Python / uvx | アラーム起点トラブルシュート・ログ異常分析・アラーム推奨。既存 SSO 資格チェーンをそのまま読む |
| Athena | [awslabs aws-dataprocessing-mcp-server](https://awslabs.github.io/mcp/servers/aws-dataprocessing-mcp-server)（or 汎用 [aws-api-mcp-server](https://awslabs.github.io/mcp/servers/aws-api-mcp-server)） | **AWS 公式（awslabs）** | Python / uvx | Athena クエリ実行・Glue カタログ。※セッション（tmux）側は焼き込み済み aws CLI で今日でも可能 |
| Zabbix | [initMAX/zabbix-mcp-server](https://github.com/initMAX/zabbix-mcp-server) | 事実上標準の OSS（Zabbix 社公式ではない） | self-host、OAuth2.1/bearer | 全 58 API 群 237 ツール、**read-only モード既定**、Zabbix 5.0〜8.0。代替: Zabbix API は素の JSON-RPC で薄く、最悪自前ツール化も容易 |
| Slack | [公式 Slack MCP server](https://slack.com/help/articles/48855576908307-Guide-to-the-Slack-MCP-server) | **公式（GA）** | Slack hosted | Real-Time Search API 連動の検索 + メッセージ送信。Claude / Claude Code 対応を明記 |
| Teams | 公式 Teams MCP あり | Microsoft | hosted | 検索不可・プレーンテキストのみ等**制約が多い**。優先度低（§7） |

**含意**: 連携レイヤは「作る」ではなく「選んで・つないで・統制する」。fleet 側の仕事は接続管理・注入・read-only 統制・自動化のオーケストレーションに絞れる。

---

## 4. アーキテクチャ方針（提案）

### 4.1 基本方針 — MCP ファースト、コンテナ内実行

- 外部 MCP サーバは **Workspace コンテナ内で stdio 実行**する。理由:
  - 「秘密は CP を素通りするだけで保持しない」原則に整合（トークンはコンテナ内 `secrets.enc` → MCP プロセスへ env 渡し。コマンド前置 env 注入の既存流儀と同じ）。
  - egress 統制（docs/20）の管轄内に収まる。外部への通信主体が常にコンテナ。
  - 身元 = 自コンテナで、PAT も追加認証も不要（面B と同じ理屈）。
- hosted 型（`mcp.pagerduty.com/mcp`、Slack 公式）は remote HTTP になる。claude CLI は http type の MCP をサポートするので技術的には繋がるが、egress 先とデータ持ち出しの統制論点がある（§8-4）。**初期は self-host / stdio を既定**にし、hosted はテナント管理者の明示許可制が無難。

### 4.2 注入先 — まずチャット、セッションは後追い

| 注入先 | 方法 | 判断 |
|---|---|---|
| assistant-chat | `chatMCPArgs` を拡張し、アシスタント定義の `tools` に応じて `--mcp-config` の mcpServers に承認済みサーバを追加（`--strict-mcp-config` は維持 = 会話ごとに集合が確定） | **本命**。既存の書き込み opt-in と同じ「ツール集合そのものがゲート」思想で統制できる |
| tmux セッション（対話 claude 等） | `BuildLaunch` での `--mcp-config` 付与、または利用者が自分で `claude mcp add` | 後者は**今日でも可能**（Phase 0）。製品としての自動注入は、対話セッションは人間が見ているぶんリスクが低い一方、kind ごとの差（codex/opencode の MCP 設定方式）を吸収するコストがあるので後回し |

### 4.3 新概念: ops 接続 + テナント MCP カタログ

2 層に分ける:

1. **ops 接続（per-user）** — Connections に kind を追加: `pagerduty`（API キー）、`grafana`（URL + サービスアカウントトークン）、`zabbix`（URL + API トークン）、`slack`（bot/user トークン）。保存は既存 `secrets.enc`、UI は既存 Connections タブの延長。AWS は既存 ssm/SSO 接続をそのまま使う（追加不要）。
2. **MCP カタログ（tenant 管理）** — tenant_admin が「承認済み MCP サーバ定義」を管理: 表示名 / 起動コマンド（イメージ焼き込みのバイナリ or uvx/npx）/ 必要な接続 kind / 渡す env のマッピング / read-only フラグ（例: initMAX zabbix の read-only モード、PagerDuty self-host の read only 構成）。member はカタログから選び、自分の接続を貼るだけ。

member が任意コマンドを MCP として登録できてしまう形は避ける（監査・egress・リソースの統制が崩れる）。カタログはその防波堤。

**論点（§8-1）**: 監視系は「チーム共有のサービスアカウント」運用が現実には多い。per-user 貼付で開始し、tenant 共有接続（CP 側 DB に封筒暗号で保存 → 起動時にコンテナへ注入）は原則との衝突を ADR で決着させてから。

### 4.4 ビルトイン「SRE アシスタント」

`assistants.go` のビルトインに追加(例):

- persona: インシデント対応の壁打ち相手。事実（メトリクス・ログ・アラート）と推測を峻別、時系列整理、影響範囲→原因仮説→次アクションの順で構造化、対外報告文の草稿支援。
- tools: `af_read` + ops MCP（read-only 集合のみ）。
- knowledge: テナントの runbook / 構成ドキュメントの場所（リポジトリ内パスや URL）を示す knowledge テキスト。
- voice: 既存 TTS でそのまま読み上げ可能（オンコールがハンズフリーで聞ける、は地味に運用向きの差別化）。

会話テンプレ（「インシデント URL を貼って開始」等）は Console 側の起動導線として Phase 2 以降で検討。

### 4.5 チャットの権限モデルの明文化（先決事項）

headless チャットは `--dangerously-skip-permissions` で走る。ops MCP（外部と通信し、攻撃者影響下のデータを取り込むツール）を足す前に、**チャット実行時に MCP 以外のツール（Bash / Write 等の CLI 組み込みツール）がどう制限されているかを確認・明文化する**必要がある（`--allowedTools` / `--tools` 相当の指定有無。§8-3）。read-only 統制は「MCP 集合」と「組み込みツール」の両方が閉じて初めて成立する。

### 4.6 イベント駆動（UC3）— webhook ingress

- CP に `/hooks/<provider>` を新設（PagerDuty Webhook v3 の署名検証、テナント紐付け、冪等化）。CP は現状 unsolicited inbound を一切持たないので、**新しい攻撃面**として docs/dev/07-security.md の脅威モデルに追記が必要。
- 受けた後の段階案:
  - **最小**: memo キューに積む + 既存ブラウザ通知/TTS（+Phase 2 の Slack 通知）。オンコールが Console を開くと初動材料が揃っている。
  - **自動初動**: assistant-chat の会話を自動作成し headless で初動調査を走らせ、要約を Slack へ投稿。ここで「誰の身元・誰のサブスクで走るか」問題が出る（§8-2）。BYO サブスク前提と ToS 判断（サブスク OAuth の生 API 流用禁止 → CLI 経由）は 19-assistant-chat で決着済みだが、**不在時の自動実行を本人サブスクで走らせるのは消費面でも心理面でも筋が悪い**。テナントの「ops ボット」identity + 専用 workspace + API キー認証の claude、が有力。

### 4.7 Slack / Teams outbound 通知

- TTS と同型の **CP-native** 実装（`tts.go` がプロバイダ抽象の先例）: `notify.go` + Slack Bot Token（tenant 設定、CP 側保存 = 通知先はユーザー秘密ではなくテナント設定なので原則と衝突しない）。
- 用途: ①セッション状態通知（回答が返った/質問が来た → DM。既存 `useSessionNotifications` のサーバ版）②UC3 の初動要約投稿。①は運用拡張と独立に汎用価値がある。
- Teams は Graph/Incoming Webhook で可能だが公式 MCP 含め制約が多く、需要が出てから。

### 4.8 スケジュール実行（UC4）

CP には interval ジョブの基盤（reaper / usage サンプラー等）が既にあるので、「cron 式 + アシスタント + プロンプト」のユーザー定義スケジューラは小さく足せる。ただし自動実行の身元問題（§8-2）が先。優先度は UC1-3 より下。

---

## 5. セキュリティ / 統制の論点

1. **Prompt injection が構造的に発生する領域**。アラート本文・ログ・チケット記述は攻撃者が影響を与えられる入力。対策は「read-only 既定 + write は別ゲート」を崩さないこと:
   - ops MCP は read-only 構成を既定（カタログの read-only フラグ。initMAX zabbix / PagerDuty self-host は構成で対応、Grafana はビューア権限のサービスアカウントで対応）。
   - write（PagerDuty の ack/resolve、Slack 投稿など）は af_write と同様「明示 opt-in したアシスタントにのみツールを広告」。既定の SRE アシスタントには入れない。
   - UC5（復旧オペレーション）は自動化しない。人間がセッション（対話）側で実行し、チャットは手順の提示まで。
2. **資格の最小化ガイドを guide/ に用意**: Grafana は Viewer サービスアカウント、PagerDuty は read-only API キー、AWS は ReadOnlyAccess 相当の SSO ロール、Zabbix は read-only ユーザー + サーバ側 read-only モード。
3. **egress**: 監視系エンドポイント（PagerDuty API、社内 Grafana/Zabbix の URL 等)をテナント allowlist に追加できる必要。docs/20 の enforce 未了に本拡張が実需要を与える。
4. **監査**: ops MCP のツール呼び出し（誰がどのインシデント/ログを見たか、write を撃ったか）を docs/20 の監査ログへ。カタログ経由の起動なら計装点を fleet 側（起動ラッパ or 面B 経由）に持てる。
5. **ホストのメモリ**（実績のある事故要因）: uvx/npx 系 MCP はプロセスが重い。(a) Go 単一バイナリ（grafana）優先、(b) MCP プロセスは会話単位で起動・終了、(c) 同時起動数の上限、(d) awslabs 系はイメージへの焼き込み（uv キャッシュ）で起動コストを抑える、を設計に織り込む。

---

## 6. 段階的ロードマップ（提案）

| Phase | 内容 | 新規実装 | 規模感 |
|---|---|---|---|
| **0. PoC（実装ゼロ）** | workspace の対話セッションで `claude mcp add` + トークン手貼りし、PagerDuty/Grafana/CloudWatch MCP で UC1/UC2 を実地検証。guide/ に手順メモ | なし（docs のみ） | 数時間〜。**まずこれで価値を確かめる** |

Phase 0 は 2026-07-12 に着手済み。手順は [guide/member/10-ops-mcp-poc.md](guide/member/10-ops-mcp-poc.md)。dev コンテナでの事前検証結果:
- mcp-grafana v0.17.1（Go 単一バイナリ 49MB）は Grafana 未接続・ダミートークンでも起動し tools/list 応答（遅延接続）。既定 65 ツール、`-disable-write -disable-admin` で 52 ツール・create/update/delete/install 系ゼロを実測。Grafana データソース経由の CloudWatch/Athena クエリツールも同梱（カテゴリ別 disable 可）。
- PagerDuty 公式 self-host は PyPI `pagerduty-mcp`（`uvx pagerduty-mcp`、env `PAGERDUTY_USER_API_KEY`）で**既定 read-only**、write は `--enable-write-tools` 明示。hosted（mcp.pagerduty.com）は既定で write も出るため PoC では非推奨。
- awslabs CloudWatch は `uvx awslabs.cloudwatch-mcp-server@latest` + `AWS_PROFILE`（SSO チェーン = 既存 ssm 接続と同じ資格で追加秘密なし）。
- initMAX zabbix は systemd 常駐のチーム共有型（remote HTTP + config.toml の `read_only = true`）で個人 PoC には重い。stdio の軽量版で雰囲気確認 → 本採用評価時に initMAX。
- PagerDuty は**実アカウントで疎通済み**（2026-07-12、dev コンテナ）: `uvx pagerduty-mcp` 1.28.1 を `claude mcp add -s user` で登録、read 系 63 ツール、`list_incidents` で実データ（Zabbix 連携サービスのインシデント）取得を確認。認証は env `PAGERDUTY_USER_API_KEY` のみで成立。
- 残タスク = Grafana / CloudWatch の実環境接続と、実インシデントでの UC1/UC2 壁打ち評価（新規 claude セッションで pagerduty ツールが使える状態になっている）。※PyPI 遮断は一時的だった（uvx はこの dev コンテナでも動作）。
| **1. 最小の製品化** | Connections に ops kind 追加 + テナント MCP カタログ + `chatMCPArgs` のカタログ駆動化 + ビルトイン SRE アシスタント + §4.5 の権限明文化 | Agent: connections/chat_providers/assistants、CP: カタログ CRUD、Console: 設定 UI | 中。既存 seam に沿う |
| **2. 通知と導線** | Slack outbound（CP-native、セッション状態通知含む）+ インシデント起点の会話テンプレ | CP: notify、Console: 導線 | 小〜中 |
| **3. イベント駆動** | webhook ingress（PagerDuty v3 署名）→ memo キュー + 通知。自動初動は ops ボット identity の ADR 決着後に | CP: /hooks、脅威モデル追記 | 中〜大（身元問題込み） |
| **4. 発展** | スケジュールレポート / write 系 runbook（承認ゲート付き）/ Teams / hosted MCP 許可制 | — | 需要を見て |

Phase 1 の判断材料は Phase 0 の実地検証。**Phase 0 は現行 main のまま今日から実施できる**（唯一の前提は監視系エンドポイントへの outbound が通ること）。

---

## 7. やらない / 捨てる候補

- **監視データの自前収集・保存**: fleet は監視基盤（Zabbix/Grafana の代替）にはならない。既存監視の「頭脳・壁打ち相手」に徹する。
- **個別 API クライアントの自前実装**: MCP エコシステムで足りる。例外は Zabbix で外部 OSS の品質が問題になった場合のみ（JSON-RPC が薄いので面B に数ツール足す逃げ道はある）。
- **runbook の自動実行を既定にすること**: write は永続的に opt-in。
- **Teams の早期対応**: 公式 MCP の制約が大きく、Slack を先例にして需要が出てから。
- **新ロールの追加**: member + guide 分冊 + アシスタント/カタログで表現できる。

---

## 8. 未決の設計論点（ADR 候補）

1. **tenant 共有接続 vs per-user 接続**: 監視系はチーム共有サービスアカウントが実態。共有するなら秘密が CP 側 DB に載る（封筒暗号の既存機構で技術的には可能）が、「CP は秘密を保持しない」原則（ADR 0003/0005 の系譜）と衝突する。per-user で始めて痛みを観測してから決める。
2. **自動実行の身元と課金**: webhook 起点・スケジュール起点の headless 実行を誰として走らせるか。ops ボット identity（専用 workspace + API キー認証 claude）が有力だが、identity/tenant モデルと課金（BYO サブスクの外に API 課金が生まれる）への影響が大きい。
3. **チャットの組み込みツール権限の実態**: `--dangerously-skip-permissions` 下で Bash 等がどう閉じているか確認し、docs/dev（04 か 07）に明文化。ops MCP 追加の前提条件。
4. **hosted/remote MCP の可否**: `mcp.pagerduty.com` や Slack 公式 hosted は運用が楽だが、egress とデータ持ち出しの統制外になる。既定拒否 + tenant_admin の明示許可制か。

---

## 参照

- 土台: `docs/history/19-assistant-chat.md`（チャット/アシスタント/面B）、`docs/dev/08-integrations.md`（Connections/MCP/AWS）、`docs/20-container-audit-egress.md`（監査/egress）、`docs/history/21-memo-queue.md`
- 外部（2026-07 確認）: [PagerDuty MCP](https://github.com/PagerDuty/pagerduty-mcp-server) / [grafana/mcp-grafana](https://github.com/grafana/mcp-grafana) / [awslabs MCP（CloudWatch ほか）](https://github.com/awslabs/mcp) / [initMAX zabbix-mcp-server](https://github.com/initMAX/zabbix-mcp-server) / [Slack 公式 MCP](https://slack.com/help/articles/48855576908307-Guide-to-the-Slack-MCP-server)
