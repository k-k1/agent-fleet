# 70. Rovo Dev エージェント種別（kind=rovo・第9種）— Track 0 プローブ第1段（認証前）

status: **Track 0 第1段（認証不要の範囲）完了（2026-08-20）**。実バイナリ（`acli` 1.3.23-stable・linux/amd64）を
本 Workspace に導入して実測したが、**`acli rovodev` 配下は `auth` 以外すべて認証ゲートで閉じている**ため、
判定を分ける軸（TUI 文字列契約・転写正本の実体・serve モードの実挙動・モデル/使用量）は**未実測**。
継続には Atlassian アカウント＋Rovo Dev スコープ API トークンが要る（§6）。**採否は未決**。
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

## 4. AF に落としたときの当たり（提案・すべて未確定）

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

## 参考

- Rovo Dev CLI コマンド一覧: https://support.atlassian.com/rovo/docs/rovo-dev-cli-commands/
- server モード: https://support.atlassian.com/rovo/docs/use-server-mode-in-rovo-dev-cli/
- セッション管理: https://support.atlassian.com/rovo/docs/manage-sessions-in-rovo-dev-cli/
- 設定: https://support.atlassian.com/rovo/docs/manage-rovo-dev-cli-settings/
- MCP 接続: https://support.atlassian.com/rovo/docs/connect-to-an-mcp-server-in-rovo-dev-cli/
- acli 導入（Linux）: https://developer.atlassian.com/cloud/acli/guides/install-linux/
- 料金: https://www.atlassian.com/software/rovo-dev/pricing
