# 70. Rovo Dev エージェント種別（kind=rovo・第9種）— Track 0 プローブ第1段（認証前）

status: **Track 0 第1段（認証不要の範囲）完了（2026-08-20）**。実バイナリ（`acli` 1.3.23-stable・linux/amd64）を
本 Workspace に導入して実測したが、**`acli rovodev` 配下は `auth` 以外すべて認証ゲートで閉じている**ため、
判定を分ける軸（TUI 文字列契約・転写正本の実体・serve モードの実挙動・モデル/使用量）は**未実測**。
継続には Atlassian アカウント＋Rovo Dev スコープ API トークンが要る（§6）。**採否は未決**。
**設計は文献ベースで先に詰めた（§8・2026-08-20 ユーザー判断）**——実測待ちの前提は ⏳ で明示してあり、
そこが覆ると設計も変わる。ADR は採用が決まってから `decisions/0051` で起票する。
関連: docs/43（kiro・直近の雛形）/ docs/40（cursor）/ docs/36（copilot）/ docs/32（agy）/ decisions/0015（managed driver）。

## 0. 対象と背景

- **Rovo Dev** = Atlassian の AI コーディングエージェント。CLI は独立配布ではなく
  **Atlassian CLI（`acli`）のサブコマンド `acli rovodev`** として出荷される。
- 実測対象: **acli 1.3.23-stable**（`https://acli.atlassian.com/linux/latest/acli_linux_amd64/acli`・2026-08-20 取得）。
  **16.6MB の単一 Go 静的バイナリ**（kiro の 855MB と対極。焼き込み判断には効く）。
- ⚠️ **`acli` は Rovo Dev 本体ではない**。`pkg/cmd/rovodev` は `executeBinary` / `tryFallback` /
  「Downloading new Rovo Dev version...」を持つ**ランチャー**で、エージェント実体は**認証後に Atlassian から
  取得される別バイナリ**（公式ドキュメントの healthcheck 例では version 0.11.26）。
  AF から見ると**版を持つ層が二重**になる。
- 課金は Beta 終了後**クレジット制**（GA）。Free = 350 credits/月/ユーザー/サイト＝**CLI タスク 1〜10 回**、
  Standard = $20/開発者/月で 2,000 credits＝5〜50 タスク、超過は $0.01/credit。

## 1. 現時点の判定（◎=実測 / △=文献のみ / ×=未測）

| 分水嶺 | 現時点 | 根拠 |
|---|---|---|
| 認証のコンテナ適性 | **良好（◎）** | `acli rovodev auth login --email <mail> --token`（トークンは stdin）。**対話ピッカーも device flow も無い**＝接続カードは「メール＋トークン」の 2 入力で完結。cursor/kiro の start→poll ハーネスが不要 |
| managed 可否 | **有望（△）** | `acli rovodev serve <port>` が HTTP＋SSE の完全な API（`/v3/stream_chat`・`/v3/sessions/{id}/restore`・`/v3/replay`・`/v3/resume_tool_calls`）。resume も許可往復も**契約として存在する** |
| read 正本 | **有望（△）** | `~/.rovodev/sessions/<uuid>/` に `session_context.json`（会話全体）＋`metadata.json`（title/workspace/fork）。TUI と serve が共用するかは未測 |
| TUI 状態検出 | **未測（×）** | 未認証では TUI を起動できない。idle/working/許可待ちの文字列契約は不明 |
| 版ピン・sha256 | **成立しない（◎）** | 配布は `latest` のみ（版付き URL は **403**）、**チェックサム公開なし**。加えて Rovo Dev 本体は acli が実行時に落とす。**既存の「版＋sha256 を versions.json にピンして焼く」型（agy/cursor/kiro）が使えない唯一の kind** |
| コスト | **要判断（△）** | Free 350 credits/月は実質お試し。⚠️ **開発・検証そのものがクレジットを食う**＝ kiro/cursor を無料枠で作り切った従来の型が通らない |
| 認証ゲートの強さ | ◎ | 未認証では `acli rovodev --help` が `run`/`serve`/`mcp`/`config` を一切出さない（見えるのは `auth` だけ）。偽 profile を置いても突破できない |

## 2. 実測記録（認証不要でできた範囲・2026-08-20）

### 2.1 配布・導入

- 単一バイナリ 16.6MB、amd64/arm64 とも `linux/latest/acli_linux_{amd64,arm64}/acli` が到達可（200 / 206）。
- **版付き URL は 403**（`…/linux/1.3.23/acli_linux_amd64/acli`）、`.sha256` も公開されていない。
  → **AF の焼き込みピンは「取得物を自前で固定して自前 sha256 を versions.json に書く」しかできない**
  （同じ URL が将来別の版を返すので再現ビルドにならない）。「6か月でサポート終了・随時更新推奨」が公式方針。
- ⚠️ **自動更新が二重**: acli 自身の更新に加え、**acli が実行時に Rovo Dev 本体を落とす**
  （「Downloading new Rovo Dev version...」）。これを止めるノブは strings 上に見当たらない
  （`ROVODEV_BETA` は staging 切替系）。kiro のピン追従問題（docs/43 §11）より構造的に厄介で、
  **起動が無言でネットワークと外部の版に依存する**。
- 関連 env: `ACLI_CONFIG_DIR`（設定ディレクトリ）／`ATLASSIAN_API_URL` 等。

### 2.2 認証

- `acli rovodev auth {login,logout,status}` の 3 つ。login は
  `--email "user@example.com" --token < token.txt`（`--token` は**標準入力から読む**フラグ）。
  → **env 注入ゼロ**でトークンを渡せる＝ cursor ADR0023 決定5（TUI への env 注入は `ps` 露出）の罠を踏まない。
- トークン発行は `https://go.atlassian.com/rovo-dev-api-token`（Rovo Dev スコープ）。
- 設定の置き場: `$ACLI_CONFIG_DIR/acli/`（既定 `~/.acli/`）に製品別 YAML。実測で生成されたのは
  `global_config.yaml`（anonymous_id 付き）/`global_auth_config.yaml`/`rovodev_config.yaml` ほか。
  `rovodev_config.yaml` は `{version, profile:{email, token, accountId}}`（パーミッション 0600）。
  ⚠️ **API トークンが平文 YAML で載る**ので、fs denylist は `~/.acli` と `~/.rovodev` の**2 つ**が要る。
- ゲートは**ローカル profile だけでは満たせない**（キーを合わせた偽 profile を置いても「未認証」のまま）。
  → 接続状態プローブは `acli rovodev auth status` の exit code で素直に取れる見込み（kiro の `whoami` と同型）。

### 2.3 未認証で閉じているもの

`acli rovodev --help` は `auth` しか出さない。したがって `run` / `serve` / `mcp` / `config` の
フラグ・出力・TUI・セッション実体は**この段では一切測れていない**。以下 §3 はすべて公式ドキュメント由来。

## 3. 文献（公式ドキュメント）で分かっている仕様 — **要実測**

### 3.1 serve モード（managed の本命）

`acli rovodev serve <port> ["初期メッセージ"] [--shadow]`。localhost に HTTP サーバを立てる。

- `GET /healthcheck` → `{"status":"healthy","version":"0.11.26","mcp_servers":{…}}`（**本体版が取れる**）
- `POST /v3/set_chat_message` `{message, enable_deep_plan}`
- `GET /v3/stream_chat`（**SSE**。`?pause_on_call_tools_start=true` でツール実行前に停止）
- `POST /v3/resume_tool_calls` `{decisions:[{tool_call_id, deny_message}]}`
  ← **許可待ちの往復が API にある**（ACP の `session/request_permission` 相当）
- セッション: `GET /v3/sessions/list` / `GET /v3/sessions/current_session` / `POST /v3/sessions/create` /
  `POST /v3/sessions/{session_id}/restore` / `POST /v3/replay`（履歴再生＝ミラー再構築の経路）
- ツール: `GET /v3/tools` / `POST /v3/tool` `{tool_name, arguments}`
- 制御: `POST /v3/cancel` / `POST /v3/reset` / `POST /v3/prune`
- ⚠️ **ポートに認証が無い**（localhost 前提の設計）。AF は 1 コンテナに複数セッションが同居するので、
  (1) ポート固定は取れない（workspace-notes「ポートは共有」）、(2) **同居する他セッションから叩けてしまう**。
  → per-session child でポートを払い出し、**実際に listen した番号を読み戻す**設計が要る
  （chromium の `DevToolsActivePort` と同じ罠。固定ポートは他人のサーバを掴む）。

### 3.2 セッション（read 正本の候補）

- `~/.rovodev/sessions/` 配下に UUID 単位で `session_context.json`（会話全体と文脈）＋
  `metadata.json`（title / workspace / fork 情報）。置き場は `sessions.persistenceDir` で変更可。
- 復帰は `acli rovodev run --restore [<uuid>]`、`sessions.auto_restore` で自動復帰。
- **fork が親子で入れ子**になる（AF の fork-at-message／docs/55 と相性が良い）。`/prune` は
  ツール結果を捨ててトークンを縮める操作＝**転写の追記単調性が崩れる可能性**があり、要実測
  （docs/68 の「store 系は最後の 1 ターンを畳むな」と同じ型の罠）。

### 3.3 設定・MCP

- `~/.rovodev/config.yml`: `agent.modelId`（既定 `auto`）/ `agent.streaming` / `agent.temperature`（既定 0.3）/
  `agent.enableDeepPlanTool` / `agent.additionalSystemPrompt` / `sessions.persistenceDir` /
  `sessions.enableWorkspaceStateSync`（実験・git 状態同期）/ `console.outputFormat`（markdown|simple|raw）/
  `console.showToolResults` / `toolPermissions.default`（ask|allow|deny）＋ per-tool ＋
  `toolPermissions.bash.commands`（正規表現）/ `mcp.mcpConfigPath` / `mcp.allowedMcpServers` /
  `mcp.disabledMcpServers` / `atlassianBillingSite.{siteUrl,cloudId}`。
- MCP 定義は `~/.rovodev/mcp.json`（`mcpServers`・stdio/http/sse）。
  ⚠️ **AF の MCP 注入は 2 ファイル触る**必要がある（`mcp.json` に書くだけでは無効。`config.yml` の
  `allowedMcpServers` にも載せる）＝ mcpreg の materialize が kind 固有で 2 ファイルを扱う初の形。
- ⚠️ `toolPermissions.default: allow` が **`--yolo` と同義の常時許可**になる想定。AF の TUI 運転は
  「許可待ちで固まらない」ことが前提なので、ここは kiro の `chat.disableTrustAllConfirmation` 相当の
  冪等な設定固定（`ensureSettings`）が要る。

### 3.4 TUI / スラッシュコマンド

- `acli rovodev run`（対話）/ `run "<指示>"`（単発実行）/ `--worktree <name>`（git worktree モード）/
  `--web`（Web UI）/ `--restore [uuid]` / `--yolo` / `--config-file <dir>` / `--shadow`。
- スラッシュ: `/new` `/sessions {new,fork,rename}` `/clear` `/prune` `/prompts` `/memory` `/jira` `/plan`
  `/ask` `/full-context` `/research` `/yolo` `/status` `/config` `/mode` `/models` `/usage` `/mcp`
  `/directories` `/subagents` `/ide` `/hooks` `/efficiency` `/changelog` `/feedback` `/exit`。
- **状態文字列（idle / working / 許可待ち）は未測**＝ここが Track 0 第2段の最重要項目。
  `/usage`（クレジット残）・`/models` は agy 型 PTY スクレイプの候補、`/status` は Session ID の取得口。
- `/hooks` があるので、claude 型の hook マーカー方式が使える可能性がある（kiro は出荷バイナリに Stop が
  無く潰れた・docs/43 §5.1）。**実バイナリの hook トリガ enum を確認するまで前提にしない**。

## 4. AF に落としたときの当たり（要約。設計本体は §8）

| 項目 | 提案 | 備考 |
|---|---|---|
| kind スラグ | `rovo` | `rovodev` は長い。`session.KindRovo` |
| 表示名 | label=`Rovo Dev` / assistantName=`Rovo` / short=`rd` / launchSuffix=`-rd` | |
| 色 | Atlassian ブルー系（#2684FF 帯）が素直だが**既存 9 色との非衝突は tokens.css の実描画で確定**（[[kind-color-css-checklist]]・copilot 紫衝突の教訓） | 着手前に twin 全ファイルを確定 |
| 実行方式 | v1 は Terminal。serve が実測合格なら Managed を A2 で追加（cursor/kiro と同じ二段） | serve は per-session child＋動的ポート |
| 認証 | メール＋API トークンの 2 入力（start→poll 不要）。トークンは stdin 経由で env 注入ゼロ | 状態は `auth status` |
| read 正本 | `~/.rovodev/sessions/<uuid>/session_context.json`（スキーマ実測が前提）。serve 稼働中は `/v3/replay` | `/prune` の破壊性を要確認 |
| 配備 | **16.6MB なので焼き込みが素直**（kiro のオンデマンド導入は不要）。ただし版ピンは自前 sha256 のみ、本体は実行時 DL | §5-1/5-2 |
| MCP | `mcp.json` ＋ `config.yml` の `allowedMcpServers` の**2 面**へ materialize | |
| headlessChat | v1 スコープ外を想定（`run "<指示>"` の出力形式が未測・クレジットも食う） | kiro §4-3 と同じ判断 |

## 5. 他 kind と構造的に違う点（効きそうな差分）

1. **版ピンが成立しない唯一の kind**（版付き URL も sha256 も無い）。
2. **エージェント本体が実行時ダウンロード**＝初回起動が遅く、オフライン不可、版が外部都合で動く。
   [[backend-drift-restart-badge]] の「版・digest」の話とは別種の、**制御できない版**が入る。
3. **serve が HTTP＋SSE でポート認証なし**＝共有コンテナ（1 WS に複数セッション）との噛み合わせに設計が要る。
4. **クレジット課金**。Free 350/月では実装検証を回し切れない見込み＝**原資の確保が着工条件**。
5. **MCP 有効化が 2 ファイル**（定義と許可リストが別）。
6. **Jira / Confluence 連携がネイティブ**（`/jira`・`atlassianBillingSite`）＝ AF の他 8 種に無い付加価値。
   採用理由が「9 個目のコーディングエージェント」なのか「Atlassian 連携」なのかで、優先する面が変わる。

## 6. 継続に要るもの（ブロッカー）

**Atlassian アカウント（Rovo Dev が有効なサイトのメンバー）＋ Rovo Dev スコープ API トークン**。
これが無いと以下が測れない＝採否判定に必要な材料が揃わない。

- TUI の文字列契約（idle / working / 許可待ち）と `/hooks` の実トリガ enum（状態検出の源）
- `session_context.json` / `metadata.json` の実スキーマ（転写・ツール出力・ファイル編集が描けるか）
- serve モードの実挙動（SSE のイベント形・resume の文脈保持・ポート挙動・並行起動）
- `/models`・`/usage` の実出力（モデル選択とクレジット残チップの可否）
- Rovo Dev 本体バイナリの落ち先・サイズ・更新挙動（焼き込み設計の前提）
- `run "<指示>"` の出力形式（headlessChat の可否）

## 7. ユーザー決定待ち

1. **続行するか**（= Rovo Dev スコープの API トークンを用意するか）。用意できないなら本件はここで保留。
2. **クレジットの原資**。Free（350/月）で第2段プローブだけ回すか、Standard（$20/月・2,000）を用意して
   実装まで通すか。⚠️ 検証で消費するので「使い切ったら次月まで止まる」。
3. **採用の狙い**。Atlassian 連携（Jira/Confluence を触れるエージェント）が主目的か、
   単純に選択肢を増やすのか。前者なら Terminal 先行で `/jira` を活かす、後者なら serve（managed）先行。
4. **配備方針**。版ピンできない CLI を焼き込むことを許容するか（自前 sha256 で固定＋更新は手動で追う）、
   それとも kiro 型のオンデマンド導入（利用者の home に入れる）にするか。

## 8. 設計（文献ベース・2026-08-20／実測待ちは ⏳ で明示）

ユーザー判断（2026-08-20）= **トークンは今は用意せず、文献ベースで設計だけ先に詰める**。
以下は「第2段の実測が通ればこの形で作れる」という設計であって、⏳ の付いた前提は**まだ裏が取れていない**。
ADR は採用が決まってから `decisions/0051` で起票する（現時点で ADR は作らない＝状態「検討中」の ADR は前例が無い）。

### 8.1 kind 契約（確定案）

| 項目 | 案 | 根拠・注意 |
|---|---|---|
| slug | `rovo` | `rovodev` は長い。`session.KindRovo` / `.kind-rovo` |
| icon（codicon） | `organization` か `symbol-event` を候補に実描画で決める | 既存 9 種（sparkle/rocket/inspect/magnet/copilot/compass/hubot/terminal/cloud）と非衝突であること |
| label / assistantName / short / launchSuffix | `Rovo Dev` / `Rovo` / `rd` / `-rd` | displayName は label と同じ（`Rovo Dev` が正式名） |
| 実行方式 | v1 = Terminal。Managed（serve）は A2 で追加 | §8.6 |
| 認証 | メール＋API トークンの 2 入力 | §8.4 |
| 表示順 | kiro の後（＝追加順の末尾、shell/ssm の前） | 既存の並びを動かさない |

### 8.2 色 = **Atlassian ブルー**（ユーザー決定 2026-08-21）。ただし**ブランド青そのものは使えない**

現行 9 色（tokens.css dark）: claude `#e0a45e`（橙）/ codex `#4ec97a`（緑）/ cursor `#d96ba1`（薔薇）/
**agy `#4285f4`（青）** / kiro `#a371f7`（紫）/ copilot `#7d8590`（チャコール）/ opencode `#aab4be`（淡灰）/
shell `#46c9d0`（シアン）/ **ssm `#6d8bf5`（藍）**。

ユーザー決定は「**色は Atlassian ブルーでよい（利用者は多くないだろうから）**」。青の一族が 3 つになることは
受け入れる。その上で、**どの青を採るかは数値と実描画で決めた**——ここを外すと「同じ色が 2 つある」になる。

- ⚠️ **ブランド青そのもの（`#2684FF`・ADS B400）は採れない**: agy `#4285f4` との **ΔE2000 = 1.6**
  ＝人の目には同じ色。既存で最も近い組（agy↔ssm = 5.7）の 1/3 以下しか離れていない。
  ライトの `#0052CC`（legacy B500）も agy `#0f5ec4` と **ΔE 4.5** で、ライトの既存最小（agy↔ssm = 13.3）より近い。
- **決定値**（Atlassian Design System の青トークンから選択・ΔE2000 と WCAG コントラストで実測）:
  - **dark = `#579DFF`**（ADS dark-mode Blue）— agy とのΔE **7.5**・ssm とのΔE **8.0**（＝**既存の agy↔ssm 5.7 より離れている**）、
    背景 4 種への最小コントラスト **4.96**（既存 9 色中でも上位。agy 3.81 / copilot 3.64 より良い）。
  - **light = `#0747A6`**（ADS B600）— ssm とのΔE **6.3**・agy とのΔE **8.2**、最小コントラスト **6.75**。
  - 候補比較（dark）: `#1D7AFC` はΔE 3.7（近すぎ）、`#0C66E4` は離れる（11.1）がコントラスト 2.61 で暗すぎ。
    （light）: `#1D7AFC` は分離最良（11.9）だがコントラスト 3.17 で弱い、`#0C66E4` は 4.7/4.11 で両方中途半端。
- **実描画で確認済み**（headless chromium・両テーマ＋`--active-bg` 上、チップ＋ベタ塗り帯の両方）:
  dark は ROVO が agy/ssm より明らかに明るい青として分離、light は agy（明るい青）・ssm（藍紫）・rovo（濃紺）で見分けられる。
  `#0052CC` を並べた列も作ったが、**agy と紛らわしい**ことが目視でも確認できたので不採用。
- ⚠️ 残る注意: 青が 3 つある事実は変わらないので、**セッション一覧のような小面積で agy/ssm/rovo が隣り合う場面**が
  最終確認ポイント（実フリート再ビルド後の実機目視に回す）。色クラス twin は
  tokens/app/terminal/sessions/settings/ui.css の総ざらい（[[kind-color-css-checklist]]）。

### 8.3 配備 — 版ピンが無い CLI をどう焼くか

現状の型（agy/cursor/kiro）は「版付き URL ＋ 公開 sha256 を Dockerfile の ARG に固定」だが、
**Rovo Dev はどちらも無い**（§2.1 実測）。取り得るのは次で、**推奨は (a)**。

- **(a) 焼き込み＋自前 sha256〔推奨〕**: Dockerfile で `linux/latest` を取得し、`acli --version` で
  実際に焼けた版を読んで **versions.json へ「事後申告」として書く**（`rovo: "1.3.23-stable"`）。
  sha256 も**その取得物のもの**を記録する（改竄検知としては機能する／再現ビルドにはならない）。
  16.6MB なので kiro 型オンデマンド導入（`workspace-agent install-kiro`）は**不要**＝ lean でも焼いてよい。
  ⚠️ **同じ Dockerfile が再ビルドの度に別の版を焼く**（ピンではなく latest 追従）ことを ADR で明示的に受け入れる。
- (b) 取得物をリリース資産としてこちらでミラーし、そこに版と sha256 を打つ — 再現性は得られるが
  **再配布の可否がライセンス判断**になり、更新の面倒も増える。v1 では採らない。
- ⚠️ **どちらを選んでも Rovo Dev 本体（実行時 DL）の版は固定できない**。よって
  **`env_tool_versions.go` の rovo は 2 行**にする: `acli`（イメージが持つ版）と
  **Rovo Dev 本体**（`/healthcheck` の `version`、または `--version` 相当の実効版）。
  3 版表示（実効／イメージ／`~/.local`）の枠に**外部が決める版**という第 4 の軸が入るのはこの kind だけ。
- ⏳ 未測: 本体の落ち先ディレクトリ・サイズ・更新頻度・オフライン時の挙動（`tryFallback` が
  何にフォールバックするか）。**初回起動が無言で数十秒〜止まる**なら、kiro の起動ガードと同じく
  ペインに進捗を出す前置きが要る。

### 8.4 認証 — 接続カードは 2 入力、start→poll は不要

- Console の AgentsTab に `RovoCard`: **メール**＋**API トークン**（`type=password`）＋トークン発行導線
  （`https://go.atlassian.com/rovo-dev-api-token` へのリンク）。保存は
  `POST /connections/rovo`（Agent と CP の**両方**に登録 — [[cp-rest-proxy-allowlist]]）、切断は `DELETE`。
- Agent 側は受け取ったトークンを **stdin 経由**で `acli rovodev auth login --email <mail> --token` に渡す
  （環境変数にもコマンドラインにも載せない＝`ps` 露出なし）。状態は `acli rovodev auth status` の exit code。
- ⚠️ トークンは `~/.acli/rovodev_config.yaml` に平文で残るので、`fs.go` の denylist に
  **`~/.acli` と `~/.rovodev` の 2 つ**を足す（ファイルブラウザ・grep から隠す）。
- device flow が無いので、cursor/kiro の `start`/`poll` 2 エンドポイントは作らない（1 回の POST で完結）。

### 8.5 Track A（Terminal）— 状態検出は三段構えで設計する

- 起動: `acli rovodev run [--restore <uuid>]`（+ 必要なら `--config-file`）。`--worktree` は AF が
  worktree を作るので**使わない**（二重管理になる）。`--web` も使わない（AF のミラーが担当）。
- **状態検出（⏳ 全部未測なので、実装は差し替え可能な形にする）**:
  1. 一次 = **TUI の明示文字列契約**（`internal/agents/rovo/state.go`）。kiro と同じく
     スピナーグリフ regex は使わない（[[spinner-re-slash-command-miss]] / false-idle の教訓）。
  2. 保険 = **`/hooks`**。⚠️ **公式ドキュメントに載っていても出荷バイナリに無いことがある**
     （kiro の Stop hook は docs にあってバイナリに無かった・docs/43 §5.1）ので、
     **実バイナリの hook トリガ enum を見るまで前提にしない**。
  3. 縮退 = `~/.rovodev/sessions/<uuid>/` の mtime と `driveState` の楽観 working。
- **`ensureSettings`（冪等・プロセス内 1 回）**で `~/.rovodev/config.yml` に固定する候補:
  `toolPermissions.default: allow`（⚠️ 素の home だと許可待ちでペインが固着する。kiro の
  `chat.disableTrustAllConfirmation` と同じ位置づけ）／`sessions.auto_restore: false`（AF が
  `--restore` を明示するので、勝手な復帰は枠とセッションの対応を壊す）／`console.outputFormat`（⏳
  転写をファイルから読むなら影響しないはずだが要確認）。
  ⚠️ **config.yml はユーザーの設定でもある**ので、丸ごと書き換えず該当キーだけ触る（§8.8 と同じ問題）。

### 8.6 Track A2（Managed）— `serve` は「動的ポート＋自分の子だと確認」が要

- `acli rovodev serve <port>` を **per-session child** で起動（cursor/kiro の driver 骨格を流用）。
- ⚠️ **ポート番号は固定できない**（1 コンテナに複数セッション／他人のサーバを掴む）。設計:
  1. AF が空きポートを選ぶ（bind→close で番号を取る／衝突したら再試行）。⏳ **`serve 0` が使えるかは未測**
     — 使えるなら chromium の `DevToolsActivePort` と同じく「実際に listen した番号を読み戻す」方が安全。
  2. 起動後 `GET /healthcheck` で生存確認し、**自分が作った session_id と `current_session` を照合**してから使う。
     ⚠️ **これをやらないと、同居する別セッションの serve を掴む**（[[chromium-attach-link-and-port]] と同型の罠）。
  3. ⏳ **bind アドレスが 127.0.0.1 か 0.0.0.0 かは未測**。0.0.0.0 ならコンテナ外へ出る可能性があるので、
     その場合は採用条件（ループバック限定にできるか）を先に確かめる。
- 転写・ターン state machine は `GET /v3/stream_chat`（SSE）を張りっぱなしにして構築。
  停止は `POST /v3/cancel` → stdin/プロセスグループの順で正規終了（kiro の `.lock` 解放と同じ作法）。
- **許可待ち**: `?pause_on_call_tools_start=true` で止め、`POST /v3/resume_tool_calls` に
  `{tool_call_id, deny_message}` を返す＝既存の Interaction（質問カード）へそのまま載る。
- **resume**: `POST /v3/sessions/{id}/restore` ＋ `POST /v3/replay` で履歴を再生してミラーを再構築
  （cursor/kiro の `session/load` リプレイと同じ経路）。
- Capabilities は cursor/kiro に倣い ProcessModel=per-session-child / Steer / Questions。
  Dynamic*（稼働中のモデル変更等）は ⏳ 未測なので v1 は全 false。

### 8.7 転写（read 正本）

- 候補は `~/.rovodev/sessions/<uuid>/session_context.json`。⏳ スキーマ未測。
- ⚠️ **append-only JSONL ではなく「単一 JSON を丸ごと読み直す」形の可能性が高い**（kiro の v2 JSONL とは違い、
  opencode 型）。その場合、tail 窓・差分更新・畳み込みの前提が変わる。
- ⚠️ **`/prune` はツール結果を捨てる＝過去の part が消える**。AF の転写は追記単調を前提にしている箇所がある
  （[[session-changed-files]] の逐次畳み込み・[[transcript-marks]] の root）。
  設計としては「レコード数の減少を検出したらキャッシュを捨てて作り直す」＋「マーカーは root が消えたら描かない」。
- `metadata.json` の fork 情報は [[fork-at-message]] と噛み合う可能性がある（⏳ 第2段で確認）。

### 8.8 MCP — mcpreg 初の「2 ファイル・YAML マージ」

- 定義は `~/.rovodev/mcp.json` の `mcpServers`（stdio=`command`/http=`url`/sse）＝既存の
  `materialize_json.go` の系列でほぼ書ける。
- ⚠️ **それだけでは有効にならない**。`~/.rovodev/config.yml` の `mcp.allowedMcpServers` にも名前を載せる必要がある。
  つまり **materialize が 2 ファイルに跨る初の kind**で、しかも 2 つ目は **YAML の部分更新**
  （ユーザーの他キー・コメントを保つ）＝ mcpreg は今まで JSON しか触っていないので新規実装が要る。
- ⚠️ テナント配布サーバは remote 限定（ADR0031 決定 2）なので、**ヘッダが実際に飛ぶか**を
  kiro でやったのと同じくヘッダ記録用リスナーで**エンドツーエンドで確認する**（⏳ 第2段）。

### 8.9 モデル・使用量

- モデルは `agent.modelId`（既定 `auto`）が config.yml のキーなので、**起動前に書けば固定できる**見込み。
  ⏳ 一覧の機械可読な取得口が `/models`（TUI）しか無い可能性があり、その場合 `caps.model` は
  「固定リスト or 非表示」に倒す（cursor の Free で named 指定不可だった件と同型の判断）。
- 使用量は ⏳ **SSE にトークン／クレジットが乗るかどうか**次第。乗るなら kiro Track D と同じ
  `ContextReporter` seam に載せられる。乗らないなら `/usage` の PTY スクレイプになるが、
  [[usage-chip-statusline]] の教訓どおり**常駐チップにはしない**。
- ⚠️ クレジットは**サイト／組織単位の残量**なので、AF の「セッション使用量」とは意味が違う。
  出すなら「このセッションが使ったクレジット」と「残量」を混ぜない。

### 8.10 Track 分割と着工順（狙いが未定でも決められる部分）

狙い（Atlassian 連携が主目的か、選択肢を増やすか）は未定だが、**どちらでも先に要るもの**は同じなので着工順は決まる。

1. **Track 0 第2段（実測）** — トークン取得後。§6 のチェックリストを埋める。**ここを飛ばして実装に入らない**。
2. **Track A（Terminal）** — read 層＋状態検出＋`ensureSettings`。serve の結果に依存しない。
3. **Track B（配備）** — 焼き込み＋versions.json の 2 軸（§8.3）。
4. **Track C（CP＋Console）** — 色・接続カード・i18n・kind enum の総ざらい（両 routes.go／mcp_stdio／CP mcp.go／
   bridge format／enkana 辞書／registry.ts／types/session.ts）。
5. **Track A2（Managed / serve）** — 第2段で serve が合格したときだけ。
- 狙いが「Atlassian 連携」なら Track A の後に `/jira` を活かす面（プロンプト導線）を足す。
  狙いが「選択肢を増やす」なら A2 を前倒しして managed 既定に寄せる。**この分岐は Track C までは効かない**。

### 8.11 第2段で最初に潰すこと（この設計が壊れる順）

1. `serve` の bind アドレスとポート指定（0 が使えるか）— ⚠️ ダメなら **managed は成立しない**。
2. TUI の状態文字列 — ⚠️ 取れないと Terminal が「動いているのか分からない」kind になる。
3. `session_context.json` のスキーマと `/prune` の破壊性 — ⚠️ 転写・マーカー・変更ファイルの前提。
4. hook トリガの実 enum（docs を信じない）。
5. Rovo Dev 本体の DL 挙動（初回起動の所要・オフライン・落ち先）。
6. `allowedMcpServers` を書かないと MCP が無効という前提の確認。
7. `run "<指示>"` の出力形式（headlessChat の可否／既定はスコープ外）。
8. 1 ターンあたりの実消費クレジット（Free 350/月で第2段が何回回るか）。

## 参考

- Rovo Dev CLI コマンド一覧: https://support.atlassian.com/rovo/docs/rovo-dev-cli-commands/
- server モード: https://support.atlassian.com/rovo/docs/use-server-mode-in-rovo-dev-cli/
- セッション管理: https://support.atlassian.com/rovo/docs/manage-sessions-in-rovo-dev-cli/
- 設定: https://support.atlassian.com/rovo/docs/manage-rovo-dev-cli-settings/
- MCP 接続: https://support.atlassian.com/rovo/docs/connect-to-an-mcp-server-in-rovo-dev-cli/
- acli 導入（Linux）: https://developer.atlassian.com/cloud/acli/guides/install-linux/
- 料金: https://www.atlassian.com/software/rovo-dev/pricing
