# 99. 自前 llama.cpp ハーネスをセッション種別として載せる（kind=lcpp・第10種）— 調査と見積り

status: **調査のみ（2026-09-19）。実装コードは無い。採否は §10 の推奨（条件付き）で、決定は利用者判断。**
対象は前セッションで P2 と呼んだ「kind として載せる」案だけ。P0（chatx の `ChatProvider` に単発ターン
専用のプロバイダを 1 個足す）／P1（P0 ＋ツールループ＋MCP クライアント＋`SendStream`）は検討済みで、
本書では §9.4 の比較段落にだけ出てくる。
関連: docs/74（rovo・直近の kind 調査の型）/ docs/43（kiro・直近に実装した kind）/ docs/40（cursor）/ docs/36（copilot）/
docs/32（agy）/ docs/27（managed driver の契約）/ decisions/0015（managed driver）/ decisions/0079（別配備のエンジンを借りる）/
decisions/0084（エンジン表示とテナント別可否）。**判断と棄却案は ADR [0093](../decisions/0093-lcpp-agent-kind.ja.md)
（提案・2026-09-19）に起案した**。本書はその棚卸しと見積りの明細（作業記録）。

## 0. 対象と背景（前セッションで確定済み・再調査していない）

- **動機**: 自前 llm エンジン（llama.cpp / llama-server）の文脈量計算が何度直しても着地しない。真因は構造で、
  窓が `store.EngineModel.ContextTokens → active set の c → sidecar の preset → llama-server の --ctx-size` と
  **片道 4 ホップで伝わり、帰り道が無い**（自分で書いた説明が `workspace/agent/internal/agents/opencode/window.go:27-33`）。
  埋まり具合は `usagex.WindowGuess`（`workspace/agent/internal/usagex/usage.go:75-89`）頼みで、自前エンジンの id を
  未知の非 Claude モデルと読んで 200,000 と答える。
- **参考実装**: NousResearch/hermes-agent（Python・MIT）。llama-server を子プロセスとして所有し OpenAI 互換 API を
  直接叩く。効いているのは式ではなく `agent/conversation_loop.py:419-437` の 1 本＝**圧縮の直前に、これから送る
  トークン数を手に持ったままサーバの窓を伸ばしに行く**。1 プロセスが「サーバの窓」と「会話の長さ」を同時に
  知っているから書ける行。⚠️ **成長ラダーはうちには移植不可**（彼らの bounce はローカルの再起動＝秒、うちは
  ECS タスクの入れ替え＝分、しかも東京の GPU は OD でも枯れる）。うちは「1 度だけ余裕を持って決める」側に倒す。
- **llama-server 側で使えると分かっているもの**（README で確認済み）: `POST /v1/chat/completions/input_tokens`（送信前の
  正確なトークン数）／`GET /props`（`default_generation_settings.n_ctx`＝実際に付いた窓）／`POST /v1/messages` ＋
  `/v1/messages/count_tokens`（Anthropic 互換）／`POST /slots/{id}?action=save|restore`／
  `POST /v1/chat/completions/control {reasoning_end}`／`--reasoning-budget N`・`--no-reasoning-preserve`（既定は preserve
  有効＝思考が履歴に積む）／`-fit`（既定 on。**未設定の引数しか動かさない**。`-c` を明示すると窓は触らず重みを CPU に
  溢れさせる）。
- ⚠️ **llama-server 内蔵の MCP クライアント（`--mcp-servers-config`）と `--tools` / `--agent` は使えない**。動くのは GPU
  コンテナの中で、うちのツールは Workspace 側でそのセッションの資格情報で動く必要がある。箱が違うので原理的に
  委譲不可。`/tools` REST は公式に *"Please do NOT use this endpoint in a downstream application"*。
  **MCP クライアントは自前で持つ前提**（うちに Go の MCP クライアント実装は無い。§3.7 で確認）。

## 1. 結論の先出し

| 論点 | 結論 |
|---|---|
| 名前 | **`lcpp`**（§2）。`native` は docs/34・42 のデスクトップ Runtime で使用済み、`llama` は `model_provider.go:68` で Meta の族名、`llm` はエンジンの key、`llamacpp` はエンジンの provider id、`rovo` は docs/74 の予約。全部避ける |
| 何が初か | **CLI を持たない最初の kind**＝(1) Terminal(CLI) 実行方式が存在しない **managed 専用 kind**、(2) 転写の正本を**自分で書く**、(3) `ProcessModel` に 4 つ目の値（in-process）、(4) ツールの実行者が CLI でなく**うち**、(5) 自前エンジンで使用量が **exact** になる最初の kind（§3） |
| 最大の塊 | kind の配線ではなく **CLI が持っていたもの**（ツールループ・組み込みツール・権限・圧縮・system prompt 組立・MCP クライアント）。見積りの過半がここで、**P1 と共通**（§9） |
| 動機の解 | 「帰り道」は kind を作っても自動では生えない。`/props` は CP の gateway が **`/v1/` を 2 回強制**するので届かない（§3.11）。CP に read-only の迂回 1 本（小）を足すのが、P0/P1/P2 のどれを選んでも要る共通部品 |
| 推奨 | **条件付き**（§10）: P1 を先に、その中核（ループ・ツール・MCP クライアント）を kind 非依存パッケージに切って作り、実機で「自前エンジンでコーディング作業が回る」ことを確かめてから P2 の配線に進む。P2 単独で着手はしない |

## 2. 名前 = `lcpp`

| 候補 | 判定 | 理由 |
|---|---|---|
| `native` | ✗ | docs/34（`AF_RUNTIME=native`＝Docker 無しの Runtime アダプタ）／docs/42（native 自動更新）。軸は違う（runtime vs kind）が、ログと文書で必ず混ざる |
| `llama` | ✗ | `workspace/agent/model_provider.go:68` に `"llama": "meta"`＝モデル名の族→ベンダー推定の辞書。kind 名が Meta の族名と同じ綴りになる。加えて Qwen を動かす kind が `llama` では利用者に嘘 |
| `llm` / `llamacpp` | ✗ | `llm` はエンジンの key（`control-plane/engines.go:50`）、`llamacpp` はエンジンの provider id（`engines.go:81`）。opencode のモデル id が `llamacpp/<model>` なので、kind と provider が同綴りになる |
| `engine` | ✗ | 「エンジン」は GPU の箱（engine_waking / EngineModel / 用語集の「インスタンス」）の語。「エンジンセッション」は箱の話に読める |
| `operator` / `af` | ✗ | `operator` は origin 軸（`session.go:45-53`）で使用済み。`af` / `agent-fleet` は MCP サーバ名の予約語（`mcpreg/def.go:55`） |
| `local` / `direct` / `harness` | △ | 空いてはいる。`local` は箱が ECS なので嘘、`direct` と `harness` は「何に対して」が名前に無い |
| **`lcpp`** | **◎** | llama.cpp の慣用略。kind の実体（`/props`・`/slots`・reasoning 制御など **llama-server の API に依存する**ハーネス）を正直に言う。`session.KindLcpp` / `.kind-lcpp` / short `lc` / launchSuffix `-lc`（既存: ""/-cx/-cu/-ag/-cp/-ki/-oc/-sh と非衝突） |

表示名は label=`llama.cpp` / assistantName=`llama.cpp` / displayName は label と同じ。ブランドアイコンは無い
（kind の主体はうち）ので shell/ssm と同じく codicon（`server-process` か `chip` を実描画で決める）。色は 10 色目＝
**着手前に tokens.css の実描画で確定**（[[kind-color-css-checklist]]。rovo の青 3 つで既に ΔE の綱渡りだった＝docs/74 §8.2）。

## 3. 既存 kind の契約チェックリスト（原本: docs/32・36・40・43・74 と `internal/agents/<kind>/`）

「kind を 1 つ載せるとき触る場所」を実コードから総ざらいした（kiro が最新・最完全なので雛形。kiro 参照は
`workspace/agent` 83 / `control-plane` 11 / `console/src` 41 ファイル＝コードだけで約 135）。**各行の右列が
「自前ハーネスの側から読み替えるとどうなるか」**。読み替えの本文は §4。

凡例: ◯=そのまま使える／△=形を変えて使う／✗=契約が破れる（CLI の存在が前提）／＋=新規に要る

### 3.1 Go の read 層（`workspace/agent/internal/agents/agents.go`）

| 契約 | 実体（既存 kind） | lcpp |
|---|---|---|
| `Agent` の 6 メソッド（`agents.go:141-161`） | `Kind` / `Caps` / `BuildLaunch`（tmux ペインのプログラム）/ `WireLive` / `ClearResume` / `Transcript` | `BuildLaunch` が **✗**（ペインに入れるプログラムが無い＝エラーを返す）。他 5 本は ◯ |
| `Caps`（`:82-97`） | `CanFork` / `CanTranscript` / `UsesLabel` / `PermissionChoice` / `CanForkAt` | `CanFork`・`CanForkAt`・`CanTranscript`・`PermissionChoice` を全部 true にできる（自分の転写を自分で切る＝claude 型・§4.3） |
| `LiveInfo`（`:120-136`） | State / Context / Resumable / LastSay / TokenSpends | ◯。`Context` は **自前で recorded**（§4.10） |
| `TranscriptData`（`:191-210`） | Turns / Tasks / Mode / Pending / Queued / Compacting | ◯ 全部自分が発生源（Compacting も自分の圧縮） |
| 任意 IF: `GracefulStopper` / `ContextReporter` / `Forker` / `ForkAtResolver`（`:169-245`） | agy / agy・kiro / 各 kind / opencode・codex・claude | `ContextReporter` は不要（転写にトークンが載る）。`Forker`・`ForkAtResolver` は ＋（自前カット） |
| `NoGenericTranscript`（`:277`） | shell/ssm/claude | 使わない（generic 経路で読ませる） |

### 3.2 managed 層（`workspace/agent/internal/agents/driver.go`・docs/27・ADR 0015）

| 契約 | 実体 | lcpp |
|---|---|---|
| `Driver` = `Agent` ＋ `Capabilities()` ＋ `Resume(m) (ThreadHandle, error)`（`driver.go:161-165`） | opencode/codex（共有デーモン）・copilot/cursor/kiro（per-session ACP 子） | ＋ 自前実装。**子プロセスが無い**（§4.5） |
| `ThreadHandle` 7 本（`:132-140`）: Send / Steer / Interrupt / UpdateSettings / Respond / Events / Snapshot | 各 kind の `driver.go`（kiro 43.7K・cursor 35.9K バイト、ACP クライアント込み） | ＋ 全部自前。ACP/SSE の読み手が無い分だけ薄い |
| `Capabilities.ProcessModel`（`:146`） | `"shared-daemon" \| "per-session-child" \| "tui"` の 3 値 | **✗ 4 つ目の値が要る**（`"in-process"`）。Console 側の参照箇所を確認 |
| `TurnState`（`:95-117`） | queued…aborted の 10 状態 | ◯。**engine_waking（箱の起床・分単位）に対応する状態が無い**（§4.2） |
| `Interaction`（`:48-53`）/ `Decision` / `Scope` | question のみ実装（3 kind とも承認は bypass） | ◯。**承認（approval）を本当に止められる最初の kind**（実行者がうち） |
| `MsgLedger`（`agents/msgledger.go`、`NewMsgLedger("<kind>-msgledger")`） | 冪等キーの台帳 | ◯（名前を `statemig.go:78-88` にも足す） |

### 3.3 sessionx の分岐（`workspace/agent/internal/sessionx/`）

| 場所 | 何を決めるか | lcpp |
|---|---|---|
| `agent.go:28-38` `agentRegistry` / `:49 NormalizeKind` | kind→Agent。**未登録の kind は黙って claude になる**（拒否ではない） | ＋ 登録必須（忘れると claude が起動する） |
| `session_turn.go:31-37` `managedDrivers` | kind→Driver | ＋ |
| `session_handlers.go:36-106` `ManagedAlive` / `managedBusy` / `dropManagedRuntime` / `removeManagedLedger` | managed の生死・busy・切り離し | ＋ 4 箇所 |
| `session_handlers.go:644-668` | create 時の `permission_choice_unsupported` / `driver_unsupported` | △ **driver 既定を managed にする分岐**（既存は tui 既定） |
| `session_driver.go` | tui ⇄ managed の切替 | △ lcpp は `tui` 行きを 400 で拒む |
| `agent.go:74-141` `DriveState` | kind 別の live state 上書き連鎖 | ＋ status ストアを自分が書くので **claude と同じ generic 経路**でよい（§4.2） |
| `session_io.go:117 / 711-718 / 798 / 1024-1035` | PaneMode / boot readiness / bracketed paste / モーダル番人 | **✗ 全部通らない**（ペインが無い）。触らないで済む |
| `session_skills.go:80-93` | kind 別の native スキル列挙 | △ foreign（注入）だけ＝kiro/copilot/agy と同じ側 |
| `turn_end_poll.go:62-70` | ポーリングで turn end を観測する kind | 入れない（自分で `status.RecordTurnEnd`） |
| `chatx/chat_report_reconcile.go:45-54`（ADR 0093 Review で追加） | ポーリング kind の報告は 2 tick 落ち着いてから確定 | 分岐は不要。通常の turn-end マーカーを出すことの**検証点** |
| `session_usage.go:89-92` | kiro だけの live `ManagedContext` | 不要（転写にトークンが載る） |
| `shutdown.go:106/112/141` / `main.go:193 ReconcileManaged` | managed 子の abort・再起動時の引き取り | △ 子が無い＝走行中の ctx cancel と「turn を unknown に落として snapshot で settle」だけ |

### 3.4 package main（`workspace/agent/*.go`）

| 場所 | 何を決めるか | lcpp |
|---|---|---|
| `agent_models.go:42-84` | `GET /agents/{kind}/models` の switch | ＋ エンジン目録（`engines.go:215 engineCatalogRows`）から直接 |
| `agent_instructions.go:83 instrSupportedKinds` / `:128` / `:146` / `:185` / `:301` | 指示ファイル層を各 CLI のファイルに書く | **✗ 書き先のファイルが無い** → 入れない。自前で system prompt に載せる（§4.9） |
| `agent_rtk.go:47-52 / 101-108 / 110-119` | rtk の kind 別配線 | △ **最も素直**（自前 bash ツールの exec 前に `rtk` を通すだけ。フック不要・§4.13） |
| `connections.go:57` | kind→接続状態 | △ 認証は無い。`supported/connected` = 「chat エンジンが目録にある」（`wiremap.golden:26` が変わる） |
| `model_provider.go:126` / `usage_catalog.go:45-55` / `usage_fold.go:204-212` | 既定 provider・価格 provider・exact/partial/none | ＋ `usageMeasuredForKind` の **exact** に入れる（§4.10） |
| `env_tool_versions.go:67-99` | CLI の版プローブ行 | **✗ 不要**（版は workspace-agent 自身） |
| `fs.go:136-137` | 秘密が平文で載る home の denylist | 不要（トークンはメモリ・§4.12） |
| `routes.go:455-457 / 318-323 / 439` | `/connections/{kind}/{start,poll}`・usage 系 | **不要**（接続ルート 0 本） |
| `install_kiro.go` / `kiro_install_http.go` | オンデマンド導入 | **✗ 不要** |

### 3.5 mcpreg / mcpproj / paths / bridge

| 場所 | 何を決めるか | lcpp |
|---|---|---|
| `mcpreg/def.go:57-61 knownKinds` / `materialize.go:47-50 MaterializedKinds` / `:72-92 writerFor` / `materialize_<kind>.go` | AF の登録簿を各 CLI の設定ファイルに書く（af サーバ名は起動毎に回る・`af_server_name.go:33-60`） | **✗ 書き先が無い**。登録簿を**メモリで読む側**に回る。`MaterializedKinds` に入れない（`materialize_test.go:377` の len 一致）が、`knownKinds` には要る（利用者が lcpp 向けにサーバを有効化できるように）→ **「known だが materialize しない kind」は初**（§4.7） |
| `mcpreg/project_spelling.go` / `mcpproj/inspect.go:36-58` / `dialect.go` | プロジェクト MCP の kind 別綴り | △ 自 kind の綴りは無い。他 kind の `.mcp.json` 等を**読む**だけ |
| `mcpreg/probe.go:332-344 / 483-498` | initialize → tools/list の一発プローブ（stdio / HTTP） | ＋ **これを育てて MCP クライアントにする**（`tools/call`・通知・セッション維持が無い） |
| `internal/paths/paths.go:126-139` | kind の home | ＋ `LcppDir()` = `AgentDataDir()/lcpp`（転写と sid） |
| `internal/statemig/statemig.go:78-88` | state-dir 移行名 | ＋ `lcpp-sid` / `lcpp-msgledger` |
| `internal/bridge/format.go:100-117` | kind→製品名（Discord/Slack） | ＋ 1 行 |
| `internal/mcpx/mcp_stdio.go:930 / 2575 / 2760 / 1674` | create_session の kind 説明・許可リスト・managed 既定・使用量説明 | ＋ 4 箇所（managed 既定に入れる） |

### 3.6 control-plane

| 場所 | 何を決めるか | lcpp |
|---|---|---|
| `routes.go:859-876`（[[cp-rest-proxy-allowlist]]・再発常連） | 接続系ルートの明示登録 | **0 本**（認証も導入も無い）。ただし `testdata/routes.golden` は変わらない＝ここでは事故らない |
| `internal/mcpsrv/mcp.go:520 / 524 / 538 / 550` | list_models 説明＋許可・create_session 説明＋managed 既定 | ＋ 4 箇所 |
| `internal/mcpsrv/mcp_server.go:71-74 mcpKnownKinds` | mcpreg.knownKinds の写し | ＋ |
| `scheduler_wake.go:278-285 injectDriver` | 定時実行で managed 既定にする kind | ＋（lcpp は managed しか無い） |
| `enkana_dict.go:200-202` | TTS の読み | ＋ `"lcpp": "ラマシーピーピー"` 等を実音で決める |
| `engine_gateway.go:211 / 1172` | `/engine/{key}/v1/{path...}` ＋ upstream prefix `/v1/` | **✗ `/props`・`/slots` が届かない**（§4.11）。＋ read-only の迂回 1 本 |

### 3.7 Console（TS / CSS / i18n）

| 場所 | 何を決めるか | lcpp |
|---|---|---|
| `types/session.ts:9 / 12 / 224` | `SessionKind` union・`SESSION_KINDS`・`ProviderConn` | ＋ |
| `agents/registry.ts:160-` descriptor（`AgentCaps` 22 フラグ・`:20-64`）/ `:586 repoLaunchKinds` | 起動 UI は registry 駆動（LaunchModal/StartModal は触らない） | ＋ 1 descriptor。**`managedDriver:true` かつ Terminal 不可**を言うフラグが無い → ＋ `tuiRoute:false` 相当（LaunchModal の driver 選択・`session_driver` の切替ボタン・HandoffModal の経路表示が参照） |
| `lib/agentModels.ts:34-35 isDynamic` | ライブ目録を引く kind。**足し忘れると選択肢が既定だけになる** | ＋ |
| `lib/settings.ts:958-966 DEFAULT_AGENT_LAUNCH` / `:726 ASSISTANT_AGENT_KINDS` | 起動既定・アシスタントの backend | ＋ 前者。後者は P1 を先にやるなら lcpp が入る |
| `lib/brandicons.ts:36` ＋ svg ＋ `styles/brandicons.css` | ブランドアイコン | 不要（codicon） |
| `styles/tokens.css:113 / 263` ＋ `lib/termcolor.ts:14-24` ＋ `features/usage/colors.ts:81 KIND_STACK_ORDER` ＋ CSS twin（`app.css` / `sessions.css` / `ui.css` / `terminal.css` / `providercard.css`） | 10 色目 | ＋ 実描画で確定（§2） |
| `features/settings/agents/<Kind>Card.tsx` ＋ `AgentsTab.tsx:250` ＋ `AgentCardParts.tsx:76` | 接続カード | △ ログイン/導入/更新が無い＝**エンジンの有無と既定モデルだけのカード** |
| `features/settings/mcp/mcpWire.ts:9 MCP_KINDS` / `schedules/ScheduleDetailModal.tsx:22` / `repos/ProjectActionPanels.tsx:37 COPY_TARGET_KINDS` | MCP の kind・定時実行のピッカー・プロジェクト MCP のコピー先 | ＋ 前 2 つ。コピー先は**入れない**（自 kind のファイル綴りが無い） |
| i18n 6 ファイル（`locales/{en,ja}/{sessions,settings,errors}.ts`） | `agent.launch_hint.lcpp`・`agents.lcpp_*`・`err.lcpp_unsupported` | ＋ 10 キー前後（導入・ログイン文言が無い分 kiro の 16 より少ない） |
| `availability.test.ts:15-21` / `modeLabel.test.ts` | `repoLaunchKinds` の各 kind が `launchableFromRepo` を持つこと | ＋ |

### 3.8 配備・CI・golden

| 場所 | 既存 kind | lcpp |
|---|---|---|
| `workspace/Dockerfile:429-459` ＋ `versions.json` | 版＋sha256 のピン焼込み | **✗ 不要** |
| `deploy/local/cli-drift-check.sh:44 / cli-release-edges.sh:35 / cli-drift-stub-test.sh / e2e-smoke.sh:54-62` | CLI の版ドリフト監視 | **✗ 不要**。代わりに ＋ **llama-server の API 契約テスト**（`/v1/messages`・`input_tokens`・`/control`・`/props` の形は llama.cpp の版で動く＝エンジン側のイメージが新しいドリフト軸・§4.14） |
| `<kind>_tui_contract_test.go` / `<kind>/live_test.go` | TUI 文字列契約 | ✗ TUI 無し。＋ 実エンジンに対する live テスト（`AF_TEST_ENGINE_URL` のような opt-in） |
| `testdata/wiremap.golden:26` / `routes.golden` | wire の鍵集合 | `connections` の鍵が 1 つ増える |
| ADR（kiro は `decisions/0026`） | 採用時 | ＋ |

## 4. 各項目を自前ハーネスの側から読み替える（核心）

**自前ハーネスは CLI を持たない最初の kind**。既存の契約は次の 3 つの前提に立っていて、それぞれ破れる:
(a) ペインに入れる**プログラムがある**、(b) 会話の正本を**CLI が書き、うちは読む**、(c) ツールと MCP は
**CLI の設定ファイルに書けば CLI が使う**。破れた所は「うちが書く側／実行する側に回る」に置き換わる。

### 4.1 実行方式 — Terminal(CLI) は成立せず、**managed 専用 kind**（初）

- ペインに入れるものが無い。`BuildLaunch` はエラー（`"lcpp has no terminal route"`）を返し、create は driver 既定を
  managed に倒す（`session_handlers.go:649-668` の `driver_unsupported` は「managed が無い kind」しか見ていない＝逆向きの
  「tui が無い kind」の分岐が要る）。`/driver` の tui 行きは 400。
- ペイン無しは新しくない: managed セッションは既に paneless（registry.ts の `slashSkillsManaged` の注記）で、Console の
  端末ペインはミラーに切り替わる。**新しいのは「tui に戻れない」こと**だけで、Console 側は driver 選択 UI を隠す
  フラグ 1 つ（§3.7）。
- REPL を `workspace-agent lcpp-tui` として書けば Terminal 経路も作れるが、それは 2 つ目の UI を書くことで、
  ミラーが全部を映せる kind でやる理由が無い。**v1 は managed 専用で確定**してよい。

### 4.2 状態判定 — 「読む」から「発生源になる」へ（最も安い項目）

- 既存は hook（claude）／pane 文字列（kiro）／ファイル末尾（copilot・cursor）／DB（opencode・agy）から**推定**する。
  lcpp はターンを自分で回すので `status.Persist(sid, working|idle|question)`（`internal/status/status.go:155`）を
  **直接書く**。`DriveState`（`sessionx/agent.go:74-141`）は claude と同じ generic 経路で読める。
- ⚠️ 既存語彙に無い状態が 1 つ: **engine_waking**（箱の起床。`engine_gateway.go:558` の `pendingGuard`・
  `errEngineWaking`・wake timeout 900 秒）。最初のターンが**分単位で「working のまま何も出ない」**になる。
  v1 は working ＋ `LastSay="エンジン起動待ち（n 秒）"` で逃がし、新状態 `waking` は Console の語彙変更
  （WireLive/チップ/通知）を伴うので後回し。⚠️ [[borrowed-engine-waking-flattened-on-stream]]: 借用エンジン
  では streamed 経路が 200 を先に書き、far の 503 が `engine_unavailable` に化ける。**自前クライアントは
  最初のトークンが来るまで「成功」と読まない**こと。
- rate-limit / auth-expiry の再開機構（`rate_limit_resume.go`・`auth_resume.go`）は不要。代わりに `model_unknown`
  （目録から外れた）／`engine_off`／wake timeout の 3 つを turn の `TurnFailed`（再送で直らない）と `TurnAborted`
  （再送で直る）に正しく振り分ける（docs/47 の区別）。

### 4.3 転写の正本 — 読む側から**書く側**へ

- 既存 kind は CLI の店（claude jsonl／codex rollout／opencode SQLite／kiro v2 JSONL）を `transcript.Turn` に正規化して
  読む。lcpp は**自分の店**を持つ: `AgentDataDir()/lcpp/sessions/<sid>.jsonl`、append-only の 1 行 1 レコード
  （user / assistant（text・reasoning・tool_calls）/ tool_result / system-note（圧縮・モデル変更）/ usage）。
- 1 レコードから **2 つの表現**を導く: 送信用の OpenAI `messages` 配列（ハーネスが次ターンで復元する正史）と、
  表示用の `transcript.Turn`（`Transcript()` が返す）。hermes の `conversation_loop` と同じ形。⚠️ 正規化先は
  `transcript.Turn` 一択（[[transcript-render-layer]]。独自形にすると共有・マーク・変更ファイルの 3 機能が落ちる）。
- **ミラー parity は原理的に 100%**（読み手と書き手が同じコード）。cursor（ツール出力が store.db にしか無い）や
  kiro（`mirror.go` の parity バッファ）で払った代償が無い。`Pending`（質問）・`Queued`（steer の待ち行列）・
  `Compacting` も自分が発生源。
- fork / fork-at: 自分の JSONL を anchor で切って新 sid に複製（claude 型・`agents.go:243 ForkAtResolver`）。
  `ErrForkAtRoute` の「経路によって不可」は無い（経路が 1 つ）。
- ⚠️ 追記単調は自分で守る。圧縮は**新しいレコードを足す**（system-note ＋要約）のであって、過去行を消さない
  （[[session-changed-files]] の畳み込み・[[transcript-marks]] の root が前提にしている）。送信用の `messages` は
  「最後の圧縮ノート以降」から組む。

### 4.4 resume ハンドル — 自分で発番（imposed）、CLI 側の発見は無い

- sid は create 時にうちが発番して `SidStore("lcpp-sid")` に書く。`imposedsid.go` の「CLI が別 id を採った時の
  養子縁組」は不要（採る相手がいない）。
- Agent 再起動後の resume = JSONL を読み直して `messages` を復元するだけ。⚠️ **llama-server 側の prefill（KV）は
  失われる**: `/slots/{id}?action=save|restore` で退避できるが (1) `/v1` の外で今は届かない（§4.11）、(2) 箱が
  入れ替わると消える。opencode 経路でも同じ条件なので**悪化ではない**が、30B 級で 30k トークンの履歴を毎ターン
  再 prefill する時間（数十秒）はこの kind の固定費として利用ガイドに書く。

### 4.5 managed driver — 子プロセス無し、`ProcessModel="in-process"`（初）

- `Resume(m)` はハンドル（goroutine ＋ chan）を返すだけ。opencode Supervisor の Ensure/adopt/generation/drain
  （docs/74 §10.3）は**子プロセスの話なので全部要らない**。要るのは (1) ターン goroutine の生存管理、(2) Agent
  再起動で握っていたターンを `TurnUnknown` に落として `Snapshot` で settle（JSONL の末尾が assistant で閉じていれば
  completed、tool_call で開いていれば aborted）、(3) `shutdown.go` での ctx cancel。
- `ThreadHandle` 7 本の実体: **Send** = ループ 1 周（§4.6）／**Steer** = 次のツール境界で user メッセージを挿入
  （キューは `Queued` に映す）／**Interrupt** = ctx cancel（思考中なら `POST /v1/chat/completions/control
  {reasoning_end}` を先に投げて自然に閉じさせる選択肢がある。`/v1` 配下なので届く）／**UpdateSettings** =
  model / effort（`reasoning_budget`）/ mode（plan＝書き込みツールを外す）を**次ターンから**反映＝
  `DynamicModel/DynamicEffort/DynamicMode` を全部 true にできる（llama-server router は `--models-max` で複数
  モデルを同居させる）／**Respond** = `ask_user` ツールの戻り値／**Events**・**Snapshot** は素直。
- `Capabilities.ProcessModel` の 3 値 enum に `"in-process"` を足す。Console で `ProcessModel` を読んでいる箇所
  （tuiMemoryCost の表示分岐）を確認して `tuiMemoryCost:""`。

### 4.6 ツールループと組み込みツール — **CLI が持っていたものを全部うちが持つ**（最大の塊）

kind の契約表（§3）には出てこないが、既存 kind では**全部 CLI の中にあった**もの。lcpp では全部うち:

| CLI が持っていたもの | lcpp での実体 | 備考 |
|---|---|---|
| ツールループ（LLM → tool_calls → 実行 → 結果を積む → LLM） | ＋ Go で新規（リポジトリに `tools/call` を呼ぶ側は e2e テスト 1 本しか無い） | 並列 tool_calls・途中 cancel・結果の切り詰め |
| 組み込みツール: read / write / edit / bash / glob / grep / ls（＋ web fetch は egress 制限で v1 外） | ＋ 新規 | cwd 拘束（worktree の外に出ない）・出力上限・バイナリ判定・大ファイルの窓読み |
| 権限モデル（bash の危険判定・書き込みの許可・`--dangerously-skip-permissions` 相当の設定） | ＋ 新規。**実行者がうちなので承認を確実に止められる**＝`Permissions:true` / `PermissionChoice:true` の「実測済み」条件を満たしやすい | docs/76 の条件はここで満たす |
| AskUserQuestion / ExitPlanMode 相当 | ＋ `ask_user`・`plan_done` を自前ツールとして定義し `Interaction(kind=question)` に載せる | 既存の質問カードがそのまま使える |
| ToDo（`TranscriptData.Tasks`） | ＋ `todo_write` ツール | 無くても動く |
| system prompt の組立（指示ファイル層・fleet notes・skills・環境情報） | ＋ §4.9 | |
| 文脈圧縮 | ＋ §4.11 | 動機そのもの |
| tool-call の書式 | llama-server の chat template（`--jinja`）に依存。モデル族ごとに tool_calls の出方が違う（Qwen / GLM / Llama で JSON の壊れ方が違う）＝**モデルを絞らないと品質保証できない** | ⚠️ 見積り最大の不確定要素 |

hermes-agent はこの表の全部を Python で持っている（`tools/`・`agent/`）。**移植でなく再実装**（言語が違う・箱が違う）。

### 4.7 MCP クライアント — af ツールは in-process、外部だけ本物のクライアント

- **af ツール（72 本）は MCP を通さなくてよい**: `mcp_stdio.go:243 dispatchMCPStdio(line []byte) []byte` は
  「JSON-RPC 1 行を受けて 1 行返す純粋なスイッチ」に見えるが、ADR 0093 の Review で**そうではない**と分かった
  （所有セッション・会話・Chromium・peer・画像・spawn の状態、stdout への非同期通知、一度きりの watcher に依存＝
  `:79-210`・`:479-544`）。in-process で呼べるのは、それらを持つリクエスト単位の dispatch 文脈を作ってから。⚠️ 加えて
  `parseStdioFlags`（`:131`、`--write` / `--conv` / `--self-report` … の許可集合）が**プロセス全体のグローバル**
  なので、セッションごとの許可を渡せる形（per-call のオプション構造体）に直す小さなリファクタが前置き。
  ツール名（`af_report` など）と説明文は既存のまま＝[[mcp-tool-description-cost]] の固定費もそのまま。
- 外部（テナント配布・プロジェクト・利用者登録の MCP サーバ）には **stdio ＋ Streamable HTTP の JSON-RPC クライアント**
  が要る。既存の `mcpreg/probe.go` が initialize → initialized → tools/list までを両トランスポートで持つ
  （`:332-344` / `:483-498`）ので、育てる土台はある。足すのは `tools/call`・`notifications/tools/list_changed`・
  サーバごとの接続維持と再接続・stdio 子プロセスの寿命（セッション終了で殺す）・タイムアウト
  （[[mcp-tool-call-timeout-per-kind]]）。⚠️ 2026-07-28 の stateless 世代と 2025-06-18 の initialize 世代の両方を
  話す必要がある（うちのサーバが両方受けるのと対称）。
- 登録簿の扱い: `mcpreg` の materialize は「各 CLI のファイルに書く」。lcpp は書き先が無く、`mcpreg.ForKind`
  相当を**メモリで読む**。`knownKinds` に入れて（利用者が lcpp 向けにサーバを有効化できる）`MaterializedKinds` には
  入れない＝**「known だが materialize しない kind」は初**。`materialize_test.go:377` の len 一致はそのままで通る。
- プロジェクト MCP（`.mcp.json` 等）は他 kind の綴りを `mcpproj/inspect.go` が既に読める。lcpp 自身の綴りは
  作らない（`COPY_TARGET_KINDS` にも入れない）。

### 4.8 AUQ・引き継ぎ・報告・peer — 全部汎用（mcpx）で足りる

- `propose_session_handoff` / `af_report` / peer 系は mcpx の汎用実装（`mcp_stdio.go:646 / 666 / 2455 / 2495`）で、
  kind 固有の配線は無い。in-process 呼び出しでそのまま使える。
- af_report の腕（arm）と turn end: `chat_report_reconcile.go:52` が「ポーリング kind（kiro/cursor）は時間的な裏取りが
  要る」としているが、lcpp は turn end を自分で `RecordTurnEnd` するので claude/codex 側（hook 相当）に入る。
- 引き継ぎ先としての lcpp: managed しか無いので `HandoffModal` の経路表示は「managed 固定」。

### 4.9 指示ファイル層・スキル — 「ファイルに書く」から「プロンプトに載せる」へ

- 既存は `agent_instructions.go` が各 CLI の場所（`~/.claude/CLAUDE.md`・kiro の steering …）に AF 所有のファイルを
  書き、CLI が読む。lcpp は書き先が無い。`instrSupportedKinds` に**入れず**、system prompt の組立で (1) fleet notes
  （`/etc/claude-code/CLAUDE.md` 相当の本文）、(2) 利用者指示（[[user-instructions-layer]]）、(3) プロジェクトの
  `AGENTS.md` / `CLAUDE.md`（cwd から上へ）を**自分で読んで載せる**。順序は fleet → user → project → rtk と
  `agent_rtk.go:91-93` の既存順を踏襲。
- スキル: native の探索機構は無いので foreign（SKILL.md を「読んで従え」で注入・docs/50 §8）だけ。
  `slashSkills:true / slashSkillsManaged:true` は安く取れる。

### 4.10 使用量台帳 — 自前エンジンで **exact** になる最初の kind

- llama-server の応答 `usage`（prompt_tokens / completion_tokens）は正確で、streaming でも CP の gateway が
  `stream_options.include_usage` を注入する（`engine_gateway.go:597 askForStreamUsage`）。JSONL の usage レコードから
  `usage_fold.go:204-212` の **exact** 側に入る（今まで exact は claude/codex/opencode だけ。自前エンジンを使う
  opencode も exact だが、それは opencode の店に書かれた数字）。
- ⚠️ 二重計上に注意: エンジン側の使用量は CP → Agent に `POST /engine/usage`（`engine_usage.go:365` →
  `engines.go:788 handleEngineUsage`）で既に流れている。lcpp のターン usage を台帳に書くなら、`usage_catalog.go`
  の provider 行で「llamacpp は GPU 時間で課金・トークン単価 0」と明示し、同じトークンを 2 度数えない。
- 文脈チップ: `LiveInfo.Context` を **`WindowSource="recorded"`** で書く（`usagex/usage.go:41-56`）。この kind は
  `WindowGuess` を**呼ばない**＝動機の「200,000 と答える」穴は kind の設計で塞がる（窓の値そのものは §4.11）。
- `TokenSpends`・`LastSay` も自分の JSONL から素直に出る。

### 4.11 文脈量（動機そのもの）— 「帰り道」は kind を作っても自動では生えない

- **窓を知る**: `GET /props` が正解（実際に付いた `n_ctx`）だが、**CP の gateway に届かない**。`engine_gateway.go:211`
  の mux が `/engine/{key}/v1/{path...}` と `/v1/` を要求し、`:1172 engineUpstreamPrefix` がさらに `/v1/` を前置する
  ので、upstream は常に `<engine>/v1/<path>`。llama-server の `/props`・`/slots`・`/health`・`/tokenize` は root
  にあり、`/v1/props` は 404。届くのは `/v1/chat/completions`・`/v1/models`・`/v1/messages`（`:1086` に明記）・
  `/v1/messages/count_tokens`・`/v1/chat/completions/input_tokens`・`/v1/chat/completions/control`。
  ⚠️ **経路の許可リストは無い**（`serve()` は path を見ない）。塞いでいるのは `/v1/` の 2 重前置だけ。
- 迂回案 = CP に `GET /engine/{key}/props`（read-only・同じ session token・prefix 無し）を 1 本足す（100 行前後）。
  これで **4 ホップの帰り道が 1 本できる**。⚠️ これは kind 固有ではない: P0/P1 でも要り、opencode 経路でも
  `syncEngineProviders`（`engines.go:287`）が箱の起きている間だけ `n_ctx` で `limit.context` を上書きできる＝
  **動機の最小の手当ては kind を作らなくてもここで済む**（§10）。
- **埋まりを知る**: `POST /v1/chat/completions/input_tokens` に「これから送る messages」をそのまま投げれば
  送信前の正確な値（chat template 込み）。CP の model 検査（`:530`）は body の `model` で通る。⚠️ 1 リクエストが
  demand 信号（`:540`）でもあるので、寝ている箱を起こす。
- **圧縮の判断**: hermes の「窓を伸ばす」はうちでは無い。うちの式は `(input_tokens + 予約出力) > 窓 × 閾値` で
  要約ターンを 1 回打つ、だけ。窓は `/props` が届けば実測、届かなければ目録の `context_tokens`（4 ホップの出口・
  `engine_gateway.go:368 engineStartWindow`）を信じる。**入力側が正確なら、窓側の誤差は「圧縮が少し早い／遅い」に
  収まり、13% 表示で毎ターン圧縮していた事故（window.go の注記）は起きない。**
- reasoning: 既定 `--reasoning-preserve` 有効＝思考が履歴に積む。lcpp は履歴を自分で組むので、**送信用 messages から
  過去ターンの reasoning を落とす**（表示用 JSONL には残す）。`reasoning_budget` は effort ピッカーに対応づける。

### 4.12 認証・接続カード・秘密 — ほぼ消える

- ログインが無い。エンジン token は Agent プロセス内で `engineToken(ctx, "llm", session)`（`engines.go:444`）を呼び
  bearer を自分で付ける＝opencode の `{env:AF_ENGINE_TOKEN}` 間接も、ディスク上の秘密も無い。token は
  1 エンジン専用（`engine_gateway.go:474 claims.Key == key`）。
- ⚠️ `internal/chatx` / 新パッケージから CP を直接知らせない: `engines.go:49-52` の `opencode.EngineEnv` /
  `imagegen.EngineLookup` と同じ **func var の seam** を 1 本足す。
- 接続カードは「chat エンジンが目録にあるか」「起きているか（warm）」「既定モデル」の表示だけ。`available` は
  ssm の `ssmHostCount>0` と同型で `conns.lcpp.connected`（=目録に chat エンジンがある）。テナント別可否は
  ADR 0084 の gate をそのまま踏む。
- `fs.go` の denylist は不要。lcpp のディレクトリに秘密は載らない。

### 4.13 rtk — 最も素直な統合

- 既存は CLI ごとに違う配線（claude: settings.json の PreToolUse / opencode: plugin / codex・agy: AGENTS.md の
  ブロック / copilot: hooks/rtk.json）。lcpp は**自前 bash ツールの exec 前に `rtk` を通す**だけ（`rtk` は
  コマンドを書き換える CLI）。フックもファイルも要らない。`agent_rtk.go:47-52 / 101-108 / 110-119` に 1 行ずつ。

### 4.14 版ピン・CLI 導入・ドリフト CI — 軸が **CLI からエンジンへ**移る

- Dockerfile の ARG・versions.json・cli-drift-check・release watch・`env_tool_versions` は**全部不要**。
- 代わりに **llama-server の API 契約**（`/v1/messages`・`input_tokens`・`/control`・`/props` の形と、tool_calls の
  出力形式）が llama.cpp の版で動く。エンジン側のイメージ（`deploy/aws/ecs/cfn/60-engines.yaml:688` の `llama`
  コンテナ）の版が新しいドリフト軸＝**エンジンのイメージ更新が kind を壊す**。`kiro-contract.yml` 相当を
  「実エンジンに対する契約テスト」として持つ（opt-in・実機のみ）。
- TUI 文字列契約（[[report-arm-pitfalls]]「毎版壊れる」）は無い。これは lcpp が既存 kind より**構造的に安定**な唯一の面。

### 4.15 その他（配線だけ）

- `bridge/format.go`・`enkana_dict.go`・i18n・CSS twin・`KIND_STACK_ORDER`（色覚安全の並び）・`statemig`・
  `paths.LcppDir()`・`wiremap.golden` の `connections` 鍵。全部 §3 の表のとおり 1 行〜数行。
- メモリ: `tuiMemoryCost:""`。会話は in-process（数十 KB〜数 MB）。子プロセスが無いので**同居セッション数に対して
  最も軽い kind**。

## 5. 自前ハーネスが「初」になる 6 つのこと（設計で受ける）

1. **managed 専用 kind**（Terminal(CLI) 経路が無い）— create 既定・切替 UI・引き継ぎ経路の 3 箇所に「tui 不可」を通す。
2. **転写の書き手**（読む側でなく）— parity 100% と引き換えに、追記単調・圧縮の表現・fork の切り方を自分で守る。
3. **`ProcessModel="in-process"`** — 4 つ目の値。Supervisor 骨格は不要、Agent 再起動の settle だけ要る。
4. **ツールの実行者がうち** — 承認を確実に止められる（`Permissions:true` を「実測済み」で宣言できる最初の kind）。
   反面、組み込みツールと権限モデルを**全部持つ**。
5. **known だが materialize しない MCP kind** — 登録簿をメモリで読む。af ツールは MCP を通さない。
6. **自前エンジンで exact な使用量** — WindowGuess を呼ばない kind。ただし窓の実値は CP の迂回 1 本が要る。

## 6. 破れる契約の一覧（CLI の存在が前提だったもの）

| 契約 | どこで CLI を前提にしているか | lcpp での置き換え |
|---|---|---|
| `BuildLaunch` → tmux ペイン | `agents.go:145` / `startSessionTmux` | エラーを返す。create は managed 既定 |
| 状態判定（hook / pane / ファイル末尾 / DB） | `sessionx/agent.go:74-208` / 各 kind の `state.go` | `status.Persist` を自分が書く |
| 転写の正本を読む | 各 kind の `transcript.go` | 自分の JSONL を書き、同じコードで読む |
| resume id の発見／養子縁組 | `imposedsid.go` / kiro `discoverSid` | 自分で発番・発見なし |
| 指示ファイルを CLI の場所に書く | `agent_instructions.go:83-301` | system prompt に自分で載せる |
| MCP を CLI の設定に書く | `mcpreg/materialize*.go` / `chat_mcp.go` | 登録簿をメモリで読み、自前クライアントで繋ぐ |
| rtk を CLI のフック／プラグインで挟む | `agent_rtk.go` ＋各 kind の `rtk.go` | bash ツールの exec 前に通す |
| 版ピン・導入・ドリフト監視 | Dockerfile / cli-drift-* / `env_tool_versions.go` | 無し。エンジン側の契約テストに置換 |
| 接続（ログイン）ルート | `routes.go`（両方）/ `<Kind>Card.tsx` | 無し。エンジン有無の表示だけ |
| TUI 文字列契約テスト | `<kind>_tui_contract_test.go` | 無し。実エンジン契約テストに置換 |
| ツール・権限・圧縮・system prompt | **CLI の中**（契約表に出てこない） | **全部うちが持つ**（§4.6・最大の塊） |

## 7. 見積りの前提

- 単位は**セッション日**（1 レーンの Claude セッションが 1 日で進む量。kiro Track A〜D が 2026-07-24〜25 の 2 日で
  4 トラック、ただし**CLI が全部やってくれる前提**の数字）。行数は既存 kind の実測（非テスト src: claude 5,853 /
  codex 5,948 / opencode 5,171 / kiro 2,896 / cursor 2,753 / copilot 2,892 / agy 2,109 行）を物差しにした。
- 品質の前提: **モデルは 1〜2 族に絞る**（tool_calls の書式が族で違う・§4.6）。絞らないと E の見積りが立たない。
- 含めないもの: Terminal 経路（§4.1）、`waking` 新状態（§4.2）、web fetch ツール、slots の退避（届かない・箱を跨げない）、
  hermes の成長ラダー。

## 8. 項目別の重さ

| # | 項目 | 何を作るか | 行数（src） | セッション日 | P1 と共通か |
|---|---|---|---|---|---|
| A | kind 配線 | enum・registry・sessionx 分岐・CP の 4 箇所・Console descriptor・i18n・CSS・golden・`tuiRoute` フラグ | 500〜700 | 1.5 | ✗ P2 固有 |
| B | managed driver | `Driver`/`ThreadHandle`/status 投影/Snapshot の settle/shutdown/`in-process` | 600〜900 | 2 | ✗ P2 固有 |
| C | 転写の書き手＋読み手 | JSONL レコード → messages / `transcript.Turn` の 2 表現・fork-at のカット・LastSay/TokenSpends | 600〜800 | 1.5 | △（P1 は `c.Messages` に書く。変換器の半分は共通） |
| D | LLM クライアント | streaming chat/completions・usage・input_tokens・reasoning・tool_calls の解析・engine_waking/503 の扱い・engineToken seam | 700〜1,000 | 2 | ◯ |
| E | ツールループ＋組み込みツール＋権限 | §4.6 の表（read/write/edit/bash/glob/grep/ls/ask_user/todo・cwd 拘束・出力上限・危険判定・plan モード） | 2,000〜3,000 | 5〜7 | ◯ |
| F | MCP クライアント | stdio＋HTTP・両世代・tools/call・通知・寿命・af の in-process 化（フラグの per-call 化） | 800〜1,200 | 3 | ◯ |
| G | 文脈管理 | system prompt 組立（fleet/user/project/skills foreign）・圧縮ターン・窓の取得（目録＋`/props`）・reasoning の落とし方 | 600〜900 | 2〜3 | ◯（組立は P1 でも要る） |
| G' | CP の `/props` 迂回 | `GET /engine/{key}/props` read-only | ~100 | 0.5 | ◯（P0 でも要る） |
| H | 使用量・目録・rtk・available | exact 台帳・二重計上の切り分け・`agent_models` の case・`isDynamic`・rtk 前置・接続カード | 300〜400 | 1 | △ |
| I | テスト | 単体（src の 0.5〜1 倍）・実エンジン契約テスト（opt-in）・Console dom | 3,000〜4,500 | 3〜4 | △ |
| J | 文書 | ADR・guide（実行方式・固定費）・release notes（用語集の語で） | — | 1 | ✗ |
| | **合計** | | **約 9,000〜13,000（テスト込み）** | **22〜27 セッション日** | |

- 1 レーンなら約 5 週。E・F・(A+B) は独立なので 3 レーンに切れば **2〜3 週**（[[parallel-lanes-migration-version-collision]]
  の型で移行番号・enum の衝突に注意）。
- 分布: **E＋F＋D＋G で 12〜15 日＝過半**。これは「kind を載せる」費用ではなく「コーディングエージェントを 1 つ書く」
  費用で、P1 と同じ中身。P2 固有（A＋B＋C の差分＋J）は **6〜7 日**。

## 9. 比較（P0 / P1 / P2）と、動機に対する費用対効果

### 9.1 P0 / P1 との比較（1 段落）

P0（`ChatProvider.Send` 1 本・単発ターン・ツール無し）は D の半分＋G' で **1〜2 日・400 行前後**。`c.Messages`
が正史として chatx 側にあるので resume ハンドルもカーソルも要らず、`chat_providers.go:44 / 70 / 110 / 149` の
4 箇所に足すだけ。P1（＋ツールループ＋MCP クライアント＋`SendStream`）は D＋E＋F＋G で **10〜13 日・
4,000〜5,500 行**。P2 は P1 に A＋B＋C の差分＋H＋I＋J を足して **22〜27 日**。つまり **P1 → P2 の増分は 12〜14 日**で、
P2 の価値（ミラー・引き継ぎ・spawn・定時実行・報告・worktree セッションが自前エンジンで opencode 抜きに動く）は
その増分で買う。逆に、**動機（文脈量）だけなら P0 ＋ G' で足りる**（§9.2）。

### 9.2 動機に対する最小の手当て（kind を作らない場合）

動機は「窓の帰り道が無い」と「WindowGuess が 200,000 と答える」の 2 つで、どちらも kind の問題ではない:
- 帰り道 = G'（CP の `/props` 迂回・0.5 日）＋ `syncEngineProviders` が箱の起きている間に `n_ctx` で `limit.context`
  を上書き。これで opencode 経路の窓が実値になる。
- WindowGuess = opencode の `window.go` が既に recorded で塞いでいる（未対応なのは `engineKVCacheMiB` 側＝
  [[gguf-kv-cache-and-ceiling]]）。
kind を作る理由は文脈量ではなく、**「opencode を経由しない自前エンジンのセッション」が欲しいか**
（[[opencode-serve-recycle-silent-abort]] のような opencode 固有の事故から自前エンジンの利用者を切り離すか）に尽きる。

### 9.3 リスク（見積りを外す順）

1. **小さいモデルの tool-call 品質**（E）。llama-server の chat template と族ごとの JSON の壊れ方。1〜2 族に絞っても、
   「コーディング作業が回る」かは実機でしか分からない＝**P1 で先に測る理由**。
2. **東京の GPU 枯渇と起床の分単位**（[[engine-window-change-needs-restart]]）。セッションの最初のターンが「何も出ない
   数分」になり、利用者には固まって見える。`waking` 状態を後回しにすると体験で効く。
3. **毎ターンの再 prefill**（§4.4）。30B 級・30k 履歴で数十秒。opencode 経路と同じだが、kind の固定費として文書に要る。
4. MCP クライアントの世代互換（F）。テナント配布サーバ側は remote 限定（ADR0031）＝ヘッダが飛ぶかは実機で確認。
5. **並列レーンの衝突**（enum・golden・i18n キー・`statemig` の名前）。

## 10. 推奨 = 条件付き（P1 → P2 の 2 段。P2 単独では着手しない）

1. **先に G'**（CP の `/props` 迂回 ＋ `syncEngineProviders` の上書き）。0.5〜1 日で動機の帰り道が 1 本できる。
   P0/P1/P2 のどれを選んでも要る共通部品で、**やらない理由が無い**。
2. **次に P1**（`internal/harness` のような kind 非依存パッケージとして D＋E＋F＋G を作り、chatx の
   `ChatProvider` から使う）。ここで「自前エンジンでコーディング作業が回るか」「どの族なら tool-call が壊れないか」を
   実機で測る。P1 の中核は P2 の driver がそのまま包むので、**P2 に進んでも捨てるコードは無い**。
3. **P2 は P1 の実測が合格したときだけ**（A＋B＋C＋H＋I＋J の 12〜14 日）。合格条件は (a) 絞った族で 20 ターン級の
   実作業が通る、(b) 起床と再 prefill の固定費を利用者が受け入れる、(c) opencode 経路と比べて事故が減る見込みがある。
4. やらない条件: 動機が文脈量だけなら **1 で止める**。自前エンジンの利用が「ときどきの単発質問」に留まるなら **P0 で止める**。

## 11. 着手時に最初に潰すこと（この見積りが壊れる順）

1. 絞る族の決定と、その族で llama-server の tool_calls が JSON として安定するかの実測（E の前提）。
2. `/v1/chat/completions/input_tokens` と `/control` が現行エンジンイメージの llama.cpp 版に**実在するか**
   （README で確認済みだが、配備している版が古いと無い。ここで G の設計が変わる）。
3. `dispatchMCPStdio` のフラグ globals を per-call に切れるか（F の前置き。切れなければ af ツールも HTTP で回す）。
   → ADR 0093 Review: フラグだけでは足りず、通知出力と watcher の寿命まで持つリクエスト単位の文脈が要る。
4. `Capabilities.ProcessModel` を読んでいる Console の箇所（B の `"in-process"` 追加で壊れる先）。
5. `tuiRoute:false` を LaunchModal / session_driver / HandoffModal のどこが読むか（A の新フラグ）。

## 参考

- hermes-agent: https://github.com/NousResearch/hermes-agent（`hermes_cli/local_runtime/`・`agent/conversation_loop.py`）
- llama.cpp server README（`tools/server/README.md`）: `/props`・`/slots`・`/v1/messages`・`input_tokens`・`/control`・`-fit`
- 本リポジトリ: `workspace/agent/internal/agents/{agents.go,driver.go}`・`internal/sessionx/{agent.go,session_turn.go,session_driver.go}`・
  `internal/mcpx/mcp_stdio.go`・`internal/mcpreg/probe.go`・`control-plane/engine_gateway.go`・`console/src/agents/registry.ts`

## 12. 不調モデル 2 件の原因調査（2026-09-20・上流ソースのみ）

`TestManualLiveAgenticSession`（`live_manual_test.go:157`）の 5 モデル実測（別セッションで実施済み）で不調だった
うち 2 件を、**エンジンに触らず**上流ソース（HF・ggml-org/llama.cpp・関連フォーク）と CloudWatch ログの読み取りだけで
切り分けた。生ログはセッションローカル（`log-coder.txt` 63KB・162 ターン全部／`log-bonsai.txt`）を一次資料として読んだ。

### 12.1 `qwen3-coder-30b-a3b` — 並列 0/162 は形式の限界ではない（結論: iii）

| 論点 | 結論 |
|---|---|
| 1. パーサ/文法は複数呼び出しを表現できるか | **できる**。`common/parsers/qwen3-coder.cpp:166-167`（llama.cpp `b23efaa`）: `auto calls = inputs.parallel_tool_calls ? tool_call_first + p.zero_or_more(tool_call) : tool_call_first;` — `parallel_tool_calls` が真なら `<tool_call>` ブロックの `zero_or_more` 繰り返しを文法に含める |
| 2. HF chat_template はループするか | **する**。`Qwen/Qwen3-Coder-30B-A3B-Instruct` の `tokenizer_config.json` の `chat_template`（2026-09-20 取得）78 行目 `{%- for tool_call in message.tool_calls %}` |
| 3. 判定 | **(iii) 純粋にモデルの癖**（下記根拠） |
| 4. 並列 0 は欠格事由か | **欠格事由にしない**。ADR 0093 自身の合格基準（`docs/decisions/0093-lcpp-agent-kind.ja.md:360`・本書 §10-1〜3・§11-1）は「`tool_calls` が全ターン有効な JSON・名前化けなし」であって並列は要求していない。coder は 162 ターン中 `invalid_tool_call_json=0`・`unknown_tool_calls=0`・`bad_arg_shape=0`（`log-coder.txt:171`）で**この基準を完全に満たした** |

根拠（一次資料）:

- **どちらのモデルも同一のパーサ経路を通る。** `common/chat.cpp:1211-1218`（コメントそのまま引用）:
  ```
  // Qwen3-Coder XML tool calls, also used by Nemotron Nano 3, Qwen3.5 and StepFun-3.5-Flash
  if (src.find("<tool_call>") != std::string::npos &&
      src.find("<function=") != std::string::npos &&
      src.find("<parameter=") != std::string::npos) {
      return common_chat_params_init_qwen3_coder(tmpl, params);
  }
  ```
  分岐はモデル名でなくテンプレ文字列の部分一致（`tmpl.source()`＝実行時にサーバへ渡されたテンプレ本体）で行われる。
  `qwen3.8-27b-uncensored-q4_k_m`（ベースは `Qwen/Qwen3.8-27B`、HF `config.json` の `model_type` は
  `qwen3_5`）と `qwen3-coder-30b-a3b` の chat_template は、`<think>` の有無（後者は無し。
  `qwen3-coder.cpp:13,21` の `is_qwen3_coder = !supports_reasoning` はここだけに効く）を除き
  `<tool_call>\n<function=name>\n<parameter=...>` という同じ XML 形式で、**同じ
  `common_chat_params_init_qwen3_coder` を通る**。162 ターン全部が有効な JSON として解けたこと
  （`invalid_tool_call_json=0`）自体が、実際にこの経路（`COMMON_CHAT_FORMAT_PEG_NATIVE`）が選ばれたことの
  実測での裏付け（選ばれなければ汎用フォールバックになり結果はもっと荒れる）。
- **`parallel_tool_calls` の既定値はテンプレ由来で、うちのハーネスは明示していない。**
  `tools/server/server-common.cpp:1295`: `inputs.parallel_tool_calls = json_value(body, "parallel_tool_calls",
  caps["supports_parallel_tool_calls"]);`。うちの `chatRequest`（`workspace/agent/internal/harness/client.go:124-129`）
  に `parallel_tool_calls` フィールドは無い（送っていない）ので、既定は `caps["supports_parallel_tool_calls"]`
  （`common/jinja/caps.h:14` の既定値 `true`）まかせ。`caps.cpp:398-491`「parallel tool support」の実装は、
  2 件の `tool_calls` を持つダミーの assistant 履歴をテンプレへ実際に描画させ、2 件目
  （`tool_calls->at(1)`）が出力で「使われたか」を見て false に倒す（`caps.cpp:479,490`）。coder の
  テンプレは 2 件をそのままループするので、このチェックで false になる理由が無い。
  ⚠️ **確認できていないこと**: この判定は GGUF に焼かれたテンプレ本体に対して行われる。今回 HF から取った
  `tokenizer_config.json` の `chat_template` と、GGUF 変換時に埋め込まれたテンプレが一致する保証はエンジンに
  触らない限り無い。ただし上の分岐一致（`<tool_call>`/`<function=`/`<parameter=` の 3 リテラルが GGUF 側にも
  存在しないと `common_chat_params_init_qwen3_coder` 自体に入れない）と実測の JSON 健全性から、**構造は同じと
  見てよい**。
- **結論**: 文法・テンプレ・ディスパッチのどれも複数呼び出しを禁じていない。同じコードパスを通る
  `qwen3.8-27b-uncensored-q4_k_m` は実際に 9/29・11/30 ターンで並列した（既知の実測）。したがって coder の
  0/162 は **llama.cpp 側にもテンプレ側にも起因しない、生成（サンプリング）側の癖**——(i) でも (ii) でもない。

⚠️ 範囲外の観察（原因未調査・本タスクの問いではない）: task-2（`log-coder.txt`）で `todo_write` にほぼ同一の
要約文を **72 回連続**連投しており、これは並列 0 とは別の症状（ターン数が 162 まで膨らんだ主因はこちらで、
こちらは繰り返し検出やサンプラ設定の話になるため上流ソースだけでは切り分けられない。**分からなかった**、
と明記して止める）。

🔴 **2026-09-21 訂正**: 初出時は「turn 52-130・71 回」と書いていたが誤りだった。生ログの `names=[...]` を
機械的に走査し `todo_write` 単独の連続ランを数え直した結果は **turn 50〜121・72 回連続**（§14 #3 の実測値と
一致）。この数字は本書内で以前にも取り違えられている——以後はここと §14 の値を正とする。

### 12.2 `ternary-bonsai-2-27b-pq2_0` — ggml type 142 の正体と出口（結論: 実質 (iii)）

| 論点 | 結論 |
|---|---|
| 1. 型 142 の正体 | **`GGML_TYPE_PQ2_0 = 142`**。`PrismML-Eng/llama.cpp`（このモデル専用フォーク）の `ggml/include/ggml.h:47` に直接定義がある |
| 上流 ggml-org は読めるか | **どの版でも読めない**。上流 `ggml/include/ggml.h:389-433`（`b23efaa`）の `enum ggml_type` は `GGML_TYPE_COUNT = 43`（0..42 のみ有効）。142 は上流に存在したことがない番号 |
| 2. 標準量子化版の有無 | **無い**（断定）。公式配布元 `prism-ml/Ternary-Bonsai-2-27B-gguf`（HF、2026-09-16 作成）の全ファイル: `Ternary-Bonsai-2-27B-F16.gguf`(53.8GB)・`-PQ2_0.gguf`(7.2GB)・`-PTQ1_0.gguf`(5.9GB)・mmproj 2 種のみ。`Q4_K_M`/`Q5_K_M`/`Q8_0` 等の素の llama.cpp 向け量子化は存在しない |
| 3. 出口 | **(iii) 諦める**（下記理由） |

根拠（一次資料）:

- 142 の系譜: `ikawrakow/ik_llama.cpp`（別の著名フォーク）の `ggml/include/ggml.h` には
  `// depricated: GGML_TYPE_IQ2_TN  = 142,`（コメントアウト済み＝現行では未定義）という同じ番号の痕跡があり、
  同ファイルの `GGML_TYPE_Q1_0_G128 = 41` には `// Bonsai 1-bit quants` という注記もある。番号 142 の再利用が
  意図的な継承かただの空き番号の再利用かは**分からなかった**（両リポジトリの履歴を跨いで確認する手段が無い）が、
  少なくとも「142 は ik_llama.cpp 系フォークの独自量子化の番号帯」であることは実物のヘッダで確認できた。
- **PrismML-Eng/llama.cpp の README（配布元 `prism-ml/Ternary-Bonsai-2-27B-gguf` の `README.md:134-140`）が
  そのものずばりを書いている**（原文）:
  > ### These files need our llama.cpp build
  > The ternary hybrid-attention kernels live in the [PrismML-Eng/llama.cpp] fork. **Stock llama.cpp will not
  > run these files.** It rejects `PQ2_0` and `PTQ1_0` as unknown types, and it loads `Q2_0` without any
  > warning and produces garbage, because it has no Hadamard activation runtime. Use a binary from the fork.

  実測で出たエラー文言（`gguf_init_from_reader: tensor 'output.weight' has invalid ggml type 142. should be
  in [0, 43)`）は配布元が**予告どおりに再現しているだけ**で、うちの配備側の不具合ではない。
- **うちのエンジンは正真正銘の上流イメージ**: `ghcr.io/ggml-org/llama.cpp:server-cuda`（ADR 0071 の表・
  `decisions/0071-self-hosted-inference-engines.md:120`）、フォークではない。`deploy/aws/ecs/cfn/20-platform.yaml:91`
  のコメントと `deploy/aws/ecs/standup.sh:63,373`・`deploy/aws/ecs/cfn/60-engines.yaml:47` が同じタグを裏付ける。
  「上流のビルドを上げれば読める」(ii) は成立しない——**上流に一度もマージされたことがない型**なので、
  `server-cuda` タグをどれだけ新しくしても変わらない。
- **VRAM 見積り**（指示どおり明記）: `PQ2_0.gguf` は 7.2GB、`PTQ1_0.gguf` は 5.9GB——うちの `g6.xlarge`
  （L4 24GB、`decisions/0071-self-hosted-inference-engines.md` の instance class 表）には**フォークさえ動けば
  サイズ自体は無理なく載る**。`F16.gguf`（53.8GB）は明確に載らない。したがってこの模型の「出口」を塞いでいるのは
  VRAM ではなく**エンジンの実行バイナリがフォーク限定**という一点。
- **(i)/(ii) が実質不成立な理由の言い換え**: (i)「標準量子化版を取り込み直せば測れる」も、配布元に標準量子化が
  存在しない以上成立しない。仮に (ii) 相当を追求するなら、それは「上流のバージョンを上げる」ではなく
  **「この 1 モデルのためだけに配備のコンテナイメージを非公式サードパーティフォーク（PrismML-Eng/llama.cpp）へ
  差し替える」という別の意思決定**になる——これは本タスクの範囲外の管理操作なので実行していない。判断は利用者へ。

### 12.3 エンジンに焼かれている llama.cpp の版（結論: 特定できた・**`/props` は箱を起こさない**）

**`build: NNNN (sha)` 形式の版バナーは、CloudWatch（`/af/af-ecs-engines/engines`）のどのログにも一度も現れない。**
Logs Insights で `/af/af-ecs-engines/engines` 全体・過去 7 日を `@message like /(?i)build:/ or
/(?i)system_info/ or /(?i)CUDA devices/` で検索して **0 件**。個別に確認した 6 本の `llm/llama/*` ストリーム
（起動ごとに別ストリーム）は全て一言一句同じ先頭行 `warn: LLAMA_ARG_HOST environment variable is set, but will be
overwritten by command line argument --host` で始まり、その手前にあるはずのビルド行は無い。**この観測（CloudWatch
だけでは版が取れない）はそのまま正しい**。

🔴 ただし版そのものは**別経路で取れた**。`GET /engine/{key}/props`（読み取り専用・`k1.kami@gmail.com` のセッションから
実測・2026-09-20）の応答に `build_info` 欄がある:

```json
{"build_info":"b10830-465e49b9c", ...}
```

（`/home/dev/lcpp-live/props-response.json`）。**`/props` は llama-server が既に応答している時に読む REST 呼び出し
であって、`{engine}/v1/...` のような「箱を眠りから起こす」経路ではない**（ADR 0093 段 0 の設計どおり。GPU 課金を
発生させていない）。したがって「CloudWatch では取れないが、箱を起こさずに `/props` の `build_info` で取れる」が
正しい言い方で、以前の「正確な版は分からなかった」は**解消済み**。以下、旧稿にあった下限の記述は
（`build_info` の実測値より緩い情報として）位置づけを変えてそのまま残す:

- 起動ログに `NOTICE: server default port will be changed to :9931 in a future release / ref:
  https://github.com/ggml-org/llama.cpp/pull/26508` が出る。PR #26508 の `merged_at` は
  **2026-08-03T10:45:24Z**（`gh api repos/ggml-org/llama.cpp/pulls/26508`）。つまり配備バイナリはこれ以降のコミット。
- `qwen3-coder-30b-a3b` の実行と同じログ（`llm/llama/8539013b820242479cafdf5eb3c3f295`、`log-coder.txt` と同一
  セッション）に `common_chat_peg_parse: unparsed peg-native output` という警告文字列そのものが出ており
  （別モデル `llama-3.1-8b-instruct-q4_k_m` の応答で発生。この不調自体は別セッションの担当範囲）、§12.1 で
  使った PEG ネイティブパーサ基盤（`common/chat-peg-parser.cpp`）が実際に配備バイナリへ入っていることを裏付ける。
  同ファイルは上流で 2025-12-03 導入（PR #17136）・2026-09-12 まで手が入り続けている——2026-08-03 の下限より緩いが
  独立に確認できた事実として記録する。
- 配備イメージは `ghcr.io/ggml-org/llama.cpp:server-cuda` という**動くタグ**で、インフラのスタンドアップ毎に
  再取得される（`LLM_ENGINE_FROM="ghcr.io/ggml-org/llama.cpp"`・`deploy/aws/ecs/standup.sh:63`、
  `af_run crane copy "$LLM_ENGINE_FROM:$llm_tag" ...`・`standup.sh:378`、既定タグ `server-cuda`・
  `deploy/aws/ecs/cfn/60-engines.yaml:46`、`20-platform.yaml:91`「pinned by tag and re-copied on every stand-up」の
  コメント）。固定ダイジェストでの pin はしていないため、**次にスタンドアップした瞬間に版が変わりうる**——
  実測した `b10830-465e49b9c`（`gh api repos/ggml-org/llama.cpp/commits/465e49b9c` で 2026-09-06T16:47:05Z のコミット
  と確認済み）は「2026-09-20 時点でこの配備が実際に動かしていた版」であって、以後の恒久的な事実ではない。
  2026-08-03 の PR #26508 下限とは整合する（465e49b9c はそれより新しい）。

### 12.4 `llama-3.1-8b-instruct-q4_k_m` — 先頭の `;` の真因（結論: 分からなかった部分あり。上流に既知の修正は無い）

否定された仮説（前セッションで確定・再確認していない）: 「system prompt と task-0 が並列呼び出しを指示しているせいで、
Llama 3.1 の書式に無い複数同時呼び出しを我流でやって落ちている」——**これは反証されている**。並列指示を外した
2 回目の実行（`log-llama-noparallel.txt` ATTEMPT 2）でも同じ症状が出て、エンジン側ログの実物
（`llm/llama/e78d2c37730945e980af67dddf6c0b3e`・ts=1789893297572）は

```
common_chat_peg_parse: unparsed peg-native output: ; {"name": "todo_write", "parameters": {"todos": "[...]"}}
```

——**単発の `todo_write` 呼び出し 1 件**に、先頭の孤立した `;` が 1 つ付いているだけだった。並列呼び出しの複数連結
ではない。この事実自体は前セッションの記録どおりで、消さずにここへも引き継ぐ。

#### A-1. 「Llama 3.1 の python_tag 形式は `;` で複数呼び出しを区切る」という理解は誤り

Meta 公式の Llama-3.1-8B-Instruct jinja テンプレート（`models/templates/meta-llama-Llama-3.1-8B-Instruct.jinja`、
`ggml-org/llama.cpp` リポジトリ内・`gh api repos/ggml-org/llama.cpp/contents/...?ref=465e49b9c` で本文取得）は、
複数呼び出しをそもそも**許していない**:

```jinja
{%- if 'tool_calls' in message %}
    {%- if not message.tool_calls|length == 1 %}
        {{- raise_exception("This model only supports single tool-calls at once!") }}
    {%- endif %}
```

しかも `;` という文字はこのテンプレート全文のどこにも出てこない。ツール呼び出しの描画は 2 通りだけ:
builtin tool（`code_interpreter` 等）なら `<|python_tag|>funcname.call(arg="val", ...)`、カスタムツールなら
`{"name": "...", "parameters": {...}}` という素の JSON（マーカー無し）。**どちらの経路にも `;` は無い**。したがって
「python_tag 形式は `;` で複数呼び出しを区切る、という理解」は、少なくとも Llama 3.1 の公式テンプレート・公式書式
としては**誤り**。

⚠️ 紛らわしい隣接事実（見つけたので記録するが、本件には直接あたらない）: `;` 区切りは Llama **3.2/4 の
"pythonic" 形式**（`func(a=1), func(b=2)` をリストで並べる書式）については実在の現象として観測・記録されている。
vLLM の `vllm/tool_parsers/llama4_pythonic_tool_parser.py`（`gh api search/code` で発見）の TODO コメント原文:

```python
#   2. Support tools outside of a list (or separated by a semicolon).
#      This depends on item 1 for consistent streaming.
# Neither of these are necessary for e.g. ToolACE, but both would help make
# Llama3.2 models more reliable.
```

——**Llama 3.2/4 の pythonic 形式**でモデルが `[...]` に包まず `;` 区切りで複数呼び出しを出すことがある、という
vLLM 側の経験則。しかし今回動いているのは Llama **3.1** の **JSON** 形式（`--jinja` 指定・GGUF ファイル名も
`Llama-3.1-8B-Instruct-Q4_K_M.gguf`）であり、pythonic 形式ではない。**別モデル・別書式の話を混同していた**、というのが
この論点の実質的な結論。

#### A-2. llama.cpp `b10830`（465e49b9c）側の扱い — 専用パーサが無く、実行時生成の「差分オートパーサ」を通る

`common/chat.cpp`（465e49b9c 時点の実物・`raw.githubusercontent.com/.../465e49b9c/common/chat.cpp`）を検索した限り、
`llama_3_1` / `python_tag` / `ipython` の**いずれの文字列も出てこない**。専用ディスパッチ関数
`common_chat_try_specialized_template`（`chat.cpp:3482-3604`）が手書きパーサへ振り分ける先は次の族だけ:
Ministral/Magistral・GPT-OSS・Muse Glimmer・Functionary v3.2・Kimi K2/K3・Cohere2 MoE・LFM2/LFM2.5・GigaChat V3・
MiniMax-M3・DeepSeek V3.2/V4・MiniCPM5・Qwen3-Coder（`chat.cpp:3486-3600`）。**Llama 系の専用分岐は無く**、末尾は
`return std::nullopt;`（`chat.cpp:3604`）。

呼び出し元 `common_chat_templates_apply_jinja`（`chat.cpp:3713-3720`）はこれが `nullopt` のとき

```cpp
try {
    LOG_DBG("%s: using differential autoparser\n", __func__);
    struct autoparser::autoparser autoparser;
    autoparser.analyze_template(tmpl);
    auto auto_params = autoparser::peg_generator::generate_parser(tmpl, params, autoparser);
```

——**「差分オートパーサ」**（`common/chat-auto-parser-generator.cpp`・`chat-auto-parser-helpers.cpp`・
`chat-diff-analyzer.cpp`）に落ちる。これは特定モデル向けに人間が書いたパーサではなく、**そのモデルの GGUF に
実際に焼かれている jinja テンプレートを実行時に何通りかの合成メッセージで描画させ、出力の差分から呼び出しの
区切りマーカーを逆算して PEG 文法を自動生成する**しくみ（ファイル名 `chat-diff-analyzer.cpp` の由来）。
⚠️ **手書きパーサ（Qwen3-Coder 等）もこのオートパーサも、両方ともログ・エラー文言では同じ
`COMMON_CHAT_FORMAT_PEG_NATIVE`（表示名 "peg-native"）を名乗る**（`chat.cpp:859-860`
`case COMMON_CHAT_FORMAT_PEG_NATIVE: return "peg-native";`、複数の `data.format = COMMON_CHAT_FORMAT_PEG_NATIVE;`
代入箇所）。したがって §12.1 の qwen3-coder のログに出た "peg-native" という語も、今回の llama-3.1 のエラー文言
`does not match the expected peg-native format` に出た同じ語も、**それだけでは「どちらの経路を通ったか」を
区別する情報にならない**——今回、実際に Llama 3.1 がどちらでもなく後者（オートパーサ）を通っていることは、
専用分岐に Llama が無いという `chat.cpp` の中身から確定できる。

複数呼び出し境界の推定ロジック（`chat-diff-analyzer.cpp:888-916 analyze_json_native_parallel_calls`・
`:1047-1080 check_per_call_markers`）は、ツール呼び出し 1 件版と 2 件版の合成メッセージをテンプレートへ実際に
描画させて `compare_variants` で差分を取る、という作りになっている。**もし GGUF に焼かれたテンプレートが公式版と
同じ（2 件で `raise_exception`）なら、この描画は失敗し、両関数とも `if (!comparison) { ...; return; }` で
早期リターンして何の区切りマーカーも設定しない**（`:901-904`・`:1064-1066` 相当）——つまりオートパーサ自身の
このロジックが、公式どおりのテンプレートに対して先頭 `;` を注入する経路には**見えない**。

#### A-3. 上流に既知の不具合・修正はあるか — 見つからなかった

`gh api search/issues`（`repo:ggml-org/llama.cpp`）で `"unparsed peg-native output"`・`peg-native llama` ・
`autoparser Llama-3.1`・`"Llama 3.1" tool_call` 等を検索したが、**この症状（Llama 3.1・先頭の孤立 `;`）に一致する
issue/PR は見つからなかった**。ヒットしたのは Muse Glimmer・Kimi K2.7-Code・Qwen3.6-35B・DeepSeek-V4-Flash・
gpt-oss-20b など**別モデルの peg-native 系不具合**ばかりで、対象の Llama 系は無い。465e49b9c（2026-09-06）以降に
`common/chat.cpp` へ入ったコミットも 3 本（`59657a6`・`acecd56`・`895c045` = Ling 3.0 追加／JSON schema 内部表現／
専用パーサの `common/parsers/` への分割）で、いずれも Llama 3.x や自動パーサの区切り検出には触れていない
（`gh api repos/ggml-org/llama.cpp/commits?path=common/chat.cpp&since=2026-09-06T16:47:05Z`）。**「次の版で直る」と
言える根拠は無い**。

#### A-4. モデルとテンプレートのどちらが `;` を出しているか — 切り分けられなかった

A-2 の分析どおり、GGUF に実際に焼かれたテンプレートが Meta 公式版と一致するかは**このタスクの範囲（エンジン
不接触）では確認できない**（§12.1 で他モデルについて既に明記した同じ限界がここにも当てはまる）。公式版どおり
なら（A-2 の分析により）オートパーサ側が `;` を注入する経路は見えない——ということは、GGUF 側のテンプレートが
公式版と違う（例えば量子化元が改変した、複数呼び出し対応版に差し替えた等）か、モデル自身が学習分布の癖として
`;` を生成しているか、のどちらかになる。**この 2 つを切り分ける手段が、エンジンに触れない範囲には無かった。
分からなかった、とだけ言える。**

#### A-5.（2026-09-20 追記）HF の GGUF メタデータ API で確認——分布元の候補 1 つは公式テンプレートと一致

A-4 の限界（ファイル本体はダウンロード禁止）は保ったまま、**HF がファイルをダウンロードせずに GGUF ヘッダを
返す API**（`GET https://huggingface.co/api/models/{repo}?expand[]=gguf`）で 1 つの候補を検証した。

配布元の候補は `docs/decisions/0071-self-hosted-inference-engines.md:131`（ADR 0071 の推奨表）が名指す
`bartowski/Meta-Llama-3.1-8B-Instruct-GGUF`（`…-Q4_K_M.gguf`）。この API の応答は:

```json
"gguf": { "total": 8030261312, "architecture": "llama", "context_length": 131072,
          "chat_template": "{{- bos_token }}\n...{{- raise_exception(\"This model only supports single tool-calls at once!\") }}...", ... }
```

`chat_template` 本文は**改行・文字列まで含めて A-1 で引用した Meta 公式テンプレートと一致**（同じ
`raise_exception` 行・`;` は本文中に一切無し）。傍証として、この GGUF の `total`（総パラメータ数 8,030,261,312）と
`context_length`（131,072）は、実測の `models-response.json` の `meta.n_params`（8030261312）・
`meta.n_ctx_train`（131072）と**完全一致**する（llama-3.1-8b という base model なら他の量子化元でも同じ値になり
得るので、これだけで配布元を一意には確定できない——ファイルの sha256 は取得していない）。

**このタスクの範囲で言えること**: ADR 0071 が推奨する配布元のテンプレートは公式版と一致していた。もし
実際の ingest がこの配布元をそのまま使ったなら、A-2 の分析（公式どおりのテンプレートに対してはオートパーサの
複数呼び出し検出ロジックが `;` を注入する経路は無い）と合わせて、**先頭の `;` はテンプレート差し替えでは
説明しにくくなる**——モデル自身の生成側に寄る仮説の相対的な確からしさが上がった、という以上のことは言えない。
🔴 sha256 一致という決定的な証拠は無いので、A-4 の「分からなかった」という結論そのものは**変えない**
（別の配布元・別のリビジョンが使われていた可能性は残る）。

#### B. 出口候補 4 つ（実行可能性のみ判定。実行しない）

| # | 候補 | 実行可能性（このリポジトリで読んだ事実） | 誰の作業 | 費用 | 副作用 |
|---|---|---|---|---|---|
| 1 | 上流の版を上げる | **可能だが受動的**。`LlmImageTag` 既定 `server-cuda` は動くタグで、スタンドアップの度に `crane copy $LLM_ENGINE_FROM:$llm_tag`（`standup.sh:63,374-379`）で再取得される——固定 pin ではない。A-3 で修正コミットが見つからなかった以上、「次の配備で直る」保証は無い。**逆に「次の配備でオートパーサや Qwen3-Coder の専用分岐が変わり、今 PASS している qwen3.8/qwen3-coder/qwen3.6 が壊れる」リスクも同じ経路にある**（同じ動くタグが全モデル共通） | 配備役（standup 再実行） | インフラのスタンドアップ 1 回分（GPU 課金は箱を買った時のみ） | 全モデル同時に版が変わる。回帰確認が要る |
| 2 | このモデルだけ引数を変える | **欄はある。ただし `--jinja` を外す狙いは源流で握りつぶされる（確定・§12.4-B 追記参照）**。`store.EngineModel.Args []string`（wire key `"a"`、`engine_admin.go:1402`・`engine_catalog.go:385,433,907-908`）は行単位の追加フラグ欄で実在し、`fetch-models.sh:123,128` の jq が `key = value` 形式で `presets.ini` に書く——実際、既存の `llama-3.1-8b-instruct-q4_k_m` 行の preset にも `jinja = 1`/`mmap = 0`/`n-gpu-layers = 99` が入っている（`models-response.json`）ので、この欄自体は生きている。だが `tools/server/server-models.cpp:548-551`「overlay router's own CLI args on top of every model preset」＋`preset.merge(base_preset)`（`common/preset.cpp:136-139`「overwrite existing options」）により、**role 全体の `LlmExtraArgs`（既定に `--jinja` を含む）が最後に上書きする**——モデル別に `jinja = false`（`--no-jinja` 相当）を書いても、この overlay で `true` に戻される。つまり **jinja を無効化して legacy（非 jinja）の per-family C++ 実装へ逃がす道は、この設計では塞がっている**。`--chat-template-file` でモデル別に別の jinja 本文を差し込むことは（role 側に競合する `--chat-template` が無いので）overlay で潰されず**実行はできる**が、jinja が有効な限り経路は同じ `common_chat_try_specialized_template`→（Llama 分岐無し）→差分オートパーサのままで、**Llama 専用の手書きパーサへは絶対に到達しない**（`arg.cpp:3768-3776` の `--chat-template` の説明文どおり、jinja 有効時は値が「既知の名前」でなく「生の jinja 本文」として扱われるため、`"llama3"` のような legacy 名を渡しても legacy 実装は選ばれない）。**評価を下げる**: 別テンプレートを試す余地はあるが「症状が直るかは差分オートパーサの挙動次第で未確認」、jinja を切ることによる回避は**不可能と確定** | 利用者の管理操作（目録の該当行を編集して再取り込み） | 再取り込み 1 回（軽い） | 他モデルには影響しない（行単位）。ただし効果は限定的（上記） |
| 3 | ハーネス側で吸収する | **不可（吸収先が無い）**。`workspace/agent/internal/harness/client.go:125-129` の `chatRequest` は `model`/`messages`/`stream`/`tools` のみで、`parallel_tool_calls` や `chat_template_kwargs` に相当するフィールドは無い（§12.1 で既に確認済みの事実の再確認）。応答側もハーネスは `deltaToolCall`（`tool_calls` 配列）を**そのまま受け取るだけ**で、テキストから tool_calls へのパース（PEG）は完全にエンジン側で完結し、失敗時はエンジンが 4xx/5xx か整形失敗を返した時点でハーネスに届く。つまり**うちのコードがパースをやり直す余地が無い**——今回のエラーはハーネスへ届く前（エンジン内）で起きている | （該当作業者なし） | ー | ー |
| 4 | 諦める（この族を候補から外す） | 常に可能。ADR 0093 の合格基準（`tool_calls` が全ターン有効な JSON・名前化けなし）を llama-3.1-8b-instruct-q4_k_m は満たしていない（実行 5/5′、後述 §12.5）ので、他候補が無ければこれが既定 | 利用者の管理操作（目録から外す／既定から外す） | ー | この 1 族が使えなくなるだけ |

### 12.5 実機 6 本の実測一覧

🔴 以後 8 本（gpt-oss-20b・gemma-4 系・窓の作り直し・qwen3-coder 3 回目）を含む全 14 本の一覧は §14 参照。
本節の 6 本はそのまま残す。

課題は全部同じ（`TestManualLiveAgenticSession`・バグ持ちの Go プロジェクトを直させる 5 タスク＋圧縮後の想起 1 問・
`window=3500`）。

| 実行 | モデル | 結果 | ターン | 圧縮 | 並列 | JSON不正/未知名/引数欠落 | 所要 |
|---|---|---|---|---|---|---|---|
| 1 | qwen3.8-27b-uncensored-q4_k_m | PASS | 30 | turn 13 | 11 | 0 / 0 / 0 | 431.58s |
| 2 | qwen3.8-27b-uncensored-q4_k_m | PASS | 29 | turn 11 | 9 | 0 / 0 / 0 | 348.90s |
| 3 | qwen3-coder-30b-a3b | PASS（⚠️） | 162 | turn 19 | 0 | 0 / 0 / 0 | 451.28s |
| 4 | ternary-bonsai-2-27b-pq2_0 | FAIL（起動せず） | — | — | — | — | 605.49s（再試行のみ） |
| 5 | llama-3.1-8b-instruct-q4_k_m（並列指示あり） | FAIL | — | — | — | — | 17.98s |
| 5' | llama-3.1-8b-instruct-q4_k_m（**並列指示なし**） | FAIL | — | — | — | — | 15.58s（別に cold start 302.33s で 1 回タイムアウト） |
| 6 | qwen3.6-35b-a3b-ud-iq3_s | PASS | 33 | turn 10 | 14 | 0 / 0 / 0 | 144.14s |

補足:

- 実行 3 は**形式の合格基準は満たしているが** `todo_write` が 72 ターン連続（turn 50〜121）で、162 ターンに膨らんだ。
  繰り返し検知の門は PR #794 で develop に入った。圧縮後の想起は 17 ターン迷走した。
- 実行 6 の圧縮後の想起は**具体的で正確**だった（実際の応答: 「1. `stack/Pop` は panic していたのを zero 値＋false
  返却に修正。2. `mathutil.Average` は `len(nums)-1` で割っていた（本来は `len(nums)`。n-1 はベッセルの補正で標本
  分散用、単純平均には不適）」）。実行 1・2 も同様に的確で、一度は「それは自分の記録に無い」と限界を正直に申告した。
- 実行 5' の 1 回目は**真の cold start が 302.33 秒**かかり `context deadline exceeded` で終わった（箱の購入＋モデル
  同期）。2 回目は warm で 15.58 秒。
- 🔴 `/props` にも `/v1/models` にも **`chat_template` の欄は無い**（実測・`props-response.json`／
  `models-response.json`）。したがって「GGUF に焼かれたテンプレートと HF の `tokenizer_config.json` が一致するか」
  は**エンジンに触れる範囲では確認できない**——§12.1 が唯一裏を取れなかった点はそのまま残る。§12.4 A-4 の
  切り分け不能もこれと同じ限界に由来する。
- 実測の生ログは `$HOME/lcpp-live/`（セッション外からは見えない）にある。

## 13. Qwen3 以外の族の候補（2026-09-20・取り込み前）

門 (a) の実測が Qwen3 系 1 族（`qwen3.8-27b-uncensored`・`qwen3.6-35b-a3b`・`qwen3-coder-30b-a3b`）に
偏っている弱点を埋めるため、**Qwen3 系以外**で測る価値のある候補を選ぶ。**エンジンには 1 回も触っていない**
（GPU 課金なし）。読んだのは (1) GitHub の `ggml-org/llama.cpp`（§12.3 で確定した配備中の版 `465e49b9c`
そのもの）と (2) HF の 2 つの**メタデータ API**——`GET /api/models/{repo}?expand[]=gguf`（GGUF ヘッダから
`architecture`・`context_length`・GGUF に**実際に焼かれた** `chat_template`・`total`（総パラメータ数）・
`totalFileSize` を返す。ファイル本体は返さない）と `GET /api/models/{repo}/tree/main`（ファイル一覧とバイト数。
同じくファイル本体には触れない）——と (3) 配布元モデルの `config.json`（safetensors 版の設定ファイル、数 KB。
GGUF ではない）だけ。**GGUF ファイル本体は 1 バイトも取得していない**（レンジ GET も含め、しなかった）。

### 13.1 母集団 — `common_chat_try_specialized_template` の全分岐（`common/chat.cpp:3482-3605`、465e49b9c）

`gh api repos/ggml-org/llama.cpp/contents/common/chat.cpp?ref=465e49b9c` で取得した実物を読んだ。分岐は
**モデル名ではなく、GGUF に焼かれたテンプレ本体（`src`）の部分一致**で行われる。全 15 分岐（Qwen3 系含む）:

| # | 族 | `chat.cpp` 行 | 一致に使う文字列 | 初期化関数 |
|---|---|---|---|---|
| 1 | Ministral / Magistral Large 3 | 3488-3491 | `[SYSTEM_PROMPT]` ∧ `[TOOL_CALLS]` ∧ `[ARGS]` ∧ ¬`[CALL_ID]` | `common_chat_params_init_ministral_3` |
| 2 | GPT-OSS | 3495-3497 | `<\|channel\|>` | `common_chat_params_init_gpt_oss` |
| 3 | Muse Glimmer | 3501-3503 | `<atem:function_calls>` ∧ `<\|eom\|>` | `common_chat_params_init_muse_glimmer` |
| 4 | Functionary v3.2 | 3508-3510 | `>>>all` ∧ `` >>>${recipient} `` | `common_chat_params_init_functionary_v3_2` |
| 5 | Kimi K2 Thinking | 3515-3518 | `<\|tool_calls_section_begin\|>` ∧ `<\|tool_call_begin\|>` | `common_chat_params_init_kimi_k2` |
| 6 | Kimi K3 | 3522-3525 | `<\|open\|>` ∧ `<\|close\|>` ∧ `<\|end_of_msg\|>` | `common_chat_params_init_kimi_k3` |
| 7 | Cohere2 MoE / North Code | 3531-3534 | `<\|START_TEXT\|>` ∧ `<\|START_ACTION\|>` | `common_chat_params_init_cohere2moe` |
| 8 | LFM2 | 3537-3539（判定は `is_lfm2_template`, 722-725） | `<\|tool_list_start\|>` ∧ `<\|tool_list_end\|>` | `common_chat_params_init_lfm2(…,true)` |
| 9 | LFM2.5 | 3543-3546 | `List of tools: [` ∧ ¬`<\|tool_list_start\|>` | `common_chat_params_init_lfm2(…,false)` |
| 10 | GigaChat V3 | 3550-3554 | `<\|role_sep\|>` ∧ `<\|message_sep\|>` ∧ ¬`<\|function_call\|>` | `common_chat_params_init_gigachat_v3` |
| 11 | MiniMax-M3 | 3559-3563 | `]<]minimax[>[` ∧ `<tool_call>` ∧ `<invoke name=` | `common_chat_params_init_minimax_m3` |
| 12 | DeepSeek V3.2/V4 | 3569-3574 | `dsml_token` ∧ `DSML` ∧ (`function_calls` ∨ `tool_calls`) | `common_chat_params_init_deepseek_v3_2` |
| 13 | Gemma4 | 3578-3585 | `` '<\|tool_call>call:' `` | `common_chat_params_init_gemma4` |
| 14 | MiniCPM5 | 3589-3593 | `Tool usage guidelines:` ∧ `<function name="` ∧ `<param name="` | `common_chat_params_init_minicpm5` |
| 15 | Qwen3-Coder（Nemotron Nano 3・Qwen3.5・StepFun-3.5-Flash も同じ経路、§12.1 既知） | 3597-3601 | `<tool_call>` ∧ `<function=` ∧ `<parameter=` | `common_chat_params_init_qwen3_coder` |

⚠️ **§12.4 A-2 の一覧との差分**: 前回セッションの要約（本書 672-674 行）は「Ministral/Magistral・GPT-OSS・Muse
Glimmer・Functionary v3.2・Kimi K2/K3・Cohere2 MoE・LFM2/LFM2.5・GigaChat V3・MiniMax-M3・DeepSeek
V3.2/V4・MiniCPM5・Qwen3-Coder」と書いており、**Gemma4（#13、3578-3585 行）が抜けている**。実物の再列挙で見つけた
だけで、§12 の記述は書き換えていない（指示どおり）。

いずれの関数も呼び出し直後に `inputs.tools` の有無で `has_tools`/`include_grammar` を分岐させている
（実装を確認した 5 関数のみ記載: `gemma4` 1553,1555 行・`functionary_v3_2` 1683,1684,1706,1713 行・
`lfm2` 1942,1947 行・`ministral_3` 1071 行・`gpt_oss` は 1351 行で `tool_calls` を明示的に扱う）——
「テンプレに `tools` ループがある」だけでなく「パーサ自身が tools を分岐条件にしている」ところまで確認できた。

全 15 族が llama.cpp 本体に実装として存在することも確認済み（`src/llama-arch.cpp:465e49b9c`、
`gh api repos/ggml-org/llama.cpp/contents/src/llama-arch.cpp?ref=465e49b9c`）: `LLM_ARCH_LLAMA`("llama", 10行)・
`LLM_ARCH_GEMMA4`("gemma4", 59行)・`LLM_ARCH_OPENAI_MOE`("gpt-oss", 126行)・`LLM_ARCH_LFM2MOE`("lfm2moe", 128行)・
`LLM_ARCH_MISTRAL3`("mistral3", 142行) ——テンプレ分岐だけでなく実行本体も配備版に入っている。

### 13.2 絞り込み — 除外した族とその理由

- **VRAM で除外**（L4 24GB に載らない・重み ≲18GB の目安を大きく超える巨大 MoE）: Kimi K2/K3（1T 級）・
  DeepSeek V3.2/V4（671B〜）・MiniMax-M3・GigaChat V3・Cohere2 MoE（Command A 系は 100B 超）。ファイルサイズは
  個別に確認していない（サイズを見るまでもなく総パラメータ数の桁が違う——HF の `total` を見ればどれも
  100B〜1T パラメータ級であることはモデルカードの記載から明らか。**分からなかった**、ではなく「見るまでも
  ない」という判断であることを明記する）。
- **同じパーサ経路を通るので後回し**: Nemotron Nano 3・Qwen3.5・StepFun-3.5-Flash は #15 の
  `common_chat_params_init_qwen3_coder` を Qwen3-Coder と共有する（§12.1 既知）。パーサは同じでも
  **モデルの癖は別**（§12.1 の結論どおり）なので理論上は候補になり得るが、既に qwen3-coder で
  「形式は合格・運用は 162 ターンに膨張」という実測が 1 件あるので、**同じ経路の別モデルを測る優先度は
  低いと判断し、今回の 5 本には含めない**（測ることを禁じる理由ではない）。
- **Muse Glimmer**: 分岐名こそ紛らわしいが `agent-fleet` 独自の `muse` kind（[[muse-agent-kind-adr0095]]）とは
  無関係の、llama.cpp 上流が命名した別のモデル族。配布元・GGUF の所在を確認する時間を割かず見送った
  （**分からなかった**、のうち「時間の都合で調べていない」に該当。断定はしない）。
- **MiniCPM5**: OpenBMB の小型モデル（HF 検索で 1B/2B 級が見つかる）。個体としては魅力的だが、既存候補が
  性格の異なる 5 本（後述）で揃ったため、今回は候補表に含めなかった（除外の理由は「サイズが合わない」
  ではなく単に手が回らなかったこと）。

残った中から、**tool call 対応・L4 24GB 適合（重み ≲18GB）・GGUF 公開・性格の分散**を満たす 5 本を選んだ。

### 13.3 候補表（測る価値の順）

| # | 族・モデル名 | `chat.cpp` の根拠 | HF リポジトリ | ファイル | サイズ・量子化 | VRAM 見積り | tool call の根拠 | 懸念 |
|---|---|---|---|---|---|---|---|---|
| 1 | **GPT-OSS-20B**（MoE, OpenAI, 2025 公開） | #2・`<\|channel\|>` | `ggml-org/gpt-oss-20b-GGUF`（llama.cpp 公式変換） | `gpt-oss-20b-MXFP4.gguf`（単一ファイル） | 12,109,566,624 B（**11.28 GiB**）・ネイティブ MXFP4（OpenAI 自身の量子化、下位量子化版なし） | 重み 11.28 + KV(ctx=3500) 0.08 ≈ **11.36 GiB**（§13.4） | GGUF 埋め込みテンプレに `tool_calls` ループ実在（`render_tool_namespace`）。`gpt_oss` パーサは `tool_calls` を明示処理（1351 行） | ライセンス Apache-2.0・非 gated（実測）。24 層中 12 層だけが `full_attention`（残りは `sliding_attention`、窓 128）——本リポジトリの VRAM 見積り機構（後述）がこの型を認識するかは未確認 |
| 2 | **gemma-4-12b-it**（Google, dense） | #13・`'<\|tool_call>call:'` | `unsloth/gemma-4-12b-it-GGUF`（公式 `google/gemma-4-12B-it` 系列） | `gemma-4-12b-it-Q4_K_M.gguf`（単一ファイル） | 7,121,861,440 B（**6.63 GiB**） | 重み 6.63 + KV(3500) 0.37 ≈ **7.00 GiB** | テンプレが `message.get('tool_calls')` をループし `<\|tool_call>call:name{...}` を生成（実物引用済み・§13.4） | ライセンス: 公式 `google/gemma-4-12B-it` の HF タグは `apache-2.0`・非 gated（実測、2026-09-20）——旧世代 Gemma の Google 独自利用規約とは違う値なので、モデルカード本文で再確認を勧める（タグだけで断定しない）。48 層中 8 層のみ `full_attention`（残り `sliding_attention`、窓 1024）で、しかも global/local で **KV ヘッド数・head_dim が違う**（後述） |
| 3 | **LFM2.5-8B-A1B**（Liquid AI, MoE, 総 8.47B/アクティブ ~1B） | #9・`List of tools: [` ∧ ¬`<\|tool_list_start\|>` | `LiquidAI/LFM2.5-8B-A1B-GGUF`（公式） | `LFM2.5-8B-A1B-Q4_K_M.gguf`（単一ファイル） | 5,155,564,768 B（**4.80 GiB**） | 重み 4.80 + KV(3500) 0.04 ≈ **4.84 GiB** | テンプレに `List of tools:` ブロックとツールループが実在（マーカー実測。関数本体までは未確認） | ライセンス **LFM Open License v1.0**（`license:other`、実測でライセンス全文取得）——商用利用は年商 **$10,000,000 未満**の法人/個人のみ無償（§5(a)(b)）。24 層中 6 層のみ `full_attention`（残り 18 層は `conv`、コンテキスト長に依存しない固定状態） |
| 4 | **Ministral-3-8B-Instruct-2512**（Mistral, dense） | #1・`[SYSTEM_PROMPT]`∧`[TOOL_CALLS]`∧`[ARGS]`∧¬`[CALL_ID]` | `unsloth/Ministral-3-8B-Instruct-2512-GGUF`（`ggml-org` の公式変換で分岐一致を確認、量子化違いは unsloth） | `Ministral-3-8B-Instruct-2512-Q4_K_M.gguf`（単一ファイル） | 5,198,386,720 B（**4.84 GiB**） | 重み 4.84 + KV(3500) 0.45 ≈ **5.29 GiB** | `common_chat_params_init_ministral_3` は `has_tools` で文法を分岐（1071 行） | ライセンス Apache-2.0・非 gated（実測）。34 層**全層が `full_attention`**（`sliding_window: null`）——分散候補の中で唯一「均一・素の式で正しい」族。ビジョン塔（`mmproj`）同梱だが今回はテキストのみで無視してよい |
| 5 | ⚠️ **functionary-small-v3.2**（meetkai, Llama-3.1-8B ファインチューン） | #4・`>>>all`∧`` >>>${recipient} `` | `bartowski/functionary-small-v3.2-GGUF`（`meetkai` 公式変換も同一症状） | `functionary-small-v3.2-Q4_K_M.gguf`（単一ファイル） | 4,920,735,360 B（**4.58 GiB**） | 重み 4.58 + KV(3500) 0.43 ≈ **5.01 GiB** | 🔴 **配布中の GGUF は分岐条件を満たさない（実測・下記 13.5）** | 🔴 **最大の懸念そのものが「専用パーサへ届かない」こと**。ライセンス表記は MIT だが土台は `meta-llama/Meta-Llama-3.1-8B-Instruct`（`config.json` の `_name_or_path`）——Meta の Llama 3.1 Community License が重みに及ぶかは**分からなかった**（MIT タグは配布者側の主張） |

🔴 **表全体の限界**: どの行も「専用分岐に載っている」ことは確認したが、ADR 0093 の合格基準
（20 ターン級の実作業で `tool_calls` が全ターン有効な JSON・未知ツール名 0）を満たすかは**実機でしか分からない**
——qwen3-coder が専用パーサを持ちながら運用上の癖（72 ターン連投）を出したのと同じで、この表は
「測る価値の順に並べた候補」であって合格の予言ではない。

### 13.4 VRAM 見積りの式と根拠

🔴 **素の式（`n_layer × n_head_kv × (k_len+v_len) × ctx × 2byte` を全層に一様適用）は使わない。**
[[gguf-kv-cache-and-ceiling]] が Qwen3.8-27B で実測した通り、この式は full-attention でない層まで
ctx 分カウントするために **最大 4 倍過大**になる（実測: 素の式 16,640 MiB に対し実際 4,096 MiB、
window 65,536）。今回の 5 候補は Qwen3.5 の「4 層に 1 層だけ full attention」とはまた違う 2 パターンの
ハイブリッド構造を持つので、層ごとに以下の式を適用する:

```
KV(ctx) = Σ[full_attention 層]  n_head_kv(layer) × head_dim(layer) × 2(K,V) × 2byte(f16) × ctx
        + Σ[sliding_attention 層] n_head_kv(layer) × head_dim(layer) × 2(K,V) × 2byte(f16) × min(ctx, window)
        + Σ[conv/recurrent 層] ≈ 0（固定長の状態。ctx に依存しない）
```

🔴 **この式は GGUF ヘッダではなく、配布元（safetensors 版）の `config.json` から作った**——GGUF ファイル本体
（ヘッダも含め）を一切取得していない今回の制約の直接の帰結。`config.json` は数 KB の設定ファイルで GGUF
バイナリではないため「メタデータ API だけ」の制約には触れない（GGUF の `?expand[]=gguf` API 自体は層別の
attention 種別までは返さない——`architecture`/`context_length`/`chat_template`/`total`/`totalFileSize` のみ）。
**layer_types などのアーキテクチャがコンバート後の GGUF でも 1:1 保たれる前提**を置いている——ここは
検証していない（GGUF ヘッダを読めば確認できるが、今回はしていない）。

層別の内訳（`config.json` 実測値。architecture は llama.cpp の内部名、HF の `model_type` とは呼び名が違う
ことがある——13.1 末尾で実装の存在は別途確認済み）:

| # | HF `model_type` | 総層数 | full attention 層 | sliding/conv 層 | full 層の `n_kv_head`×`head_dim` | sliding/conv 層の `n_kv_head`×`head_dim`（窓） |
|---|---|---|---|---|---|---|
| 1 gpt-oss-20b | `gpt_oss` | 24 | 12（`layer_types` 偶数番） | 12 sliding（窓 128） | 8×64 | 8×64（窓 128） |
| 2 gemma-4-12b | `gemma4_unified_text` | 48 | 8（6 層に 1 層） | 40 sliding（窓 1024） | `num_global_key_value_heads`=**1**×`global_head_dim`=**512** | `num_key_value_heads`=8×`head_dim`=256（窓 1024） |
| 3 LFM2.5-8B-A1B | `lfm2_moe` | 24 | 6 | 18 conv（`conv_L_cache`=3、ctx 非依存） | 8×64（`hidden_size`÷`num_attention_heads`=2048÷32） | — |
| 4 Ministral-3-8B | `ministral3`（`text_config`） | 34 | 34（全層） | 0（`sliding_window: null`） | 8×128（`head_dim` 明記） | — |
| 5 functionary-small-v3.2 | `llama`（Llama-3.1-8B 土台） | 32 | 32（全層。Llama 3.1 の `sliding_window` フィールドは実装上未使用） | 0 | 8×128 | — |

🔴 **gemma-4 は global 層と local 層で KV ヘッド構成そのものが違う**（`num_global_key_value_heads=1`・
`global_head_dim=512` vs `num_key_value_heads=8`・`head_dim=256`）——同じモデル内で 2 通りの式を使い分けて
いて、見落とすと計算が丸ごと間違う。

`window=3500`（§12.5 の実機テストと同じ、ハーネスの圧縮窓）での計算結果（KiB→GiB は 1024^3 で換算）:

| # | full 層 KV/token | sliding/conv 層 KV | KV(3500) | 重み | 重み+KV(3500) | 参考: KV(32768) | 重み+KV(32768) |
|---|---|---|---|---|---|---|---|
| 1 gpt-oss-20b | 12層×2KiB=24KiB | 12層×2KiB×128(窓上限)=3,072KiB（固定） | 87,072KiB≈**0.083GiB** | 11.28GiB | **11.36GiB** | 0.75GiB(可変分のみ増加) | **12.03GiB** |
| 2 gemma-4-12b | 8層×2KiB=16KiB | 40層×8KiB×1024(窓上限)=327,680KiB（固定） | 383,680KiB≈**0.366GiB** | 6.63GiB | **7.00GiB** | 0.81GiB | **7.45GiB** |
| 3 LFM2.5-8B-A1B | 6層×2KiB=12KiB | ≈0（conv） | 42,000KiB≈**0.040GiB** | 4.80GiB | **4.84GiB** | 0.375GiB | **5.18GiB** |
| 4 Ministral-3-8B | 34層×8×128×2×2byte=136KiB | — | 476,000KiB≈**0.454GiB** | 4.84GiB | **5.29GiB** | 4.25GiB | **9.09GiB** |
| 5 functionary-v3.2 | 32層×8×128×2×2byte=128KiB | — | 448,000KiB≈**0.427GiB** | 4.58GiB | **5.01GiB** | 4.00GiB | **8.58GiB** |

**全 5 本が「重み ≲18GB」の目安を大きく下回り、L4 24GB に KV・計算バッファ込みでも余裕で載る**——既存で
実測済みの `qwen3.8-27b-uncensored`（重み 17,092 MiB=16.7 GiB、[[gguf-kv-cache-and-ceiling]]）よりはるかに軽い。
候補を「小さすぎて能力が心配」側に振っているのはこの余裕を承知の選択で、能力面のリスクは実機でしか測れない
（13.3 の限界の節と同じ）。

🔴 **この見積りと、本リポジトリ自身の VRAM 見積り機構（`control-plane/engine_gguf.go`）の関係**: 現在の
`engineKVGeometry.cacheLayers()`（`engine_gguf.go:166-175` 付近）は `<arch>.full_attention_interval`
（均一な間隔のハイブリッド、Qwen3.5 型）と `<arch>.nextn_predict_layers`（MTP ヘッド）だけを読んで補正する
実装になっている（コメントに根拠あり、[[gguf-kv-cache-and-ceiling]] が指摘した「4 倍過大」バグはここで
**修正済み**——メモリは 2026-09-18 時点で「未修正」と書いていたが、今回読んだ現在のコードでは直っていた。
**メモリの該当行はもう古い**）。しかし `grep -rln "sliding" control-plane/` は **0 件**——今回の候補のうち
#1 GPT-OSS・#2 gemma-4・#3 LFM2.5 が使う「層ごとに `sliding_attention`/`conv` を個別指定する」パターン
（HF の `layer_types` 配列に相当する GGUF 側のキー）を読む経路が見当たらない。**確認できなかったこと**:
llama.cpp のコンバータがこの 3 族の GGUF にどんなキー名でこの情報を書くか（`<arch>.attention.sliding_window`
系と推測しているが実物のヘッダは読んでいない）、そして本リポジトリの取り込みパスがそれを読むか。読まない
場合、`cacheLayers()` は `block_count` をそのまま使う（Qwen3.5 と同じ「全層 full-attention 扱い」）方向に
**倒れるはずで、それは過大方向の間違い**（実際より高い VRAM クラスを要求する）——安全側だが、無駄に高い
クラスを選ぶ・窓を過度に狭めて警告する可能性がある。Ministral-3・functionary-v3.2 は全層 full-attention
なので、この不確実性の影響を受けない。

### 13.5 🔴 functionary-small-v3.2 は「専用パーサに届かない」ことが実測できた

候補選定の途中で、**族に専用パーサがあることは、配布されている GGUF がそのパーサに実際に届くことを
保証しない**という具体例が出た（指示の警告どおり）。

- `bartowski/functionary-small-v3.2-GGUF` と `meetkai/functionary-small-v3.2-GGUF`（両方とも 2024-08 変換、
  他に公開されている変換は見つからなかった）の GGUF 埋め込み `chat_template`（`?expand[]=gguf` で実測、
  2026-09-20）は**どちらも同一の 873 文字**で、`>>>all` は含むが `` >>>${recipient} `` は**含まない**。
- 一方 llama.cpp 自身のテストが束ねている参照テンプレート
  `models/templates/meetkai-functionary-medium-v3.2.jinja`（`gh api
  repos/ggml-org/llama.cpp/contents/models/templates/meetkai-functionary-medium-v3.2.jinja?ref=465e49b9c`）
  の 265 行目には `` Respond in this format:\n>>>${recipient}\n${content}\n `` という、ツールのスキーマを
  システムプロンプトへレンダリングするブロックの中に、まさにこの分岐条件の文字列が実在する。
- つまり公開されている `functionary-small-v3.2` の GGUF は、**ツールのスキーマをシステムプロンプトへ
  レンダリングする一段（`generate_schema_from_functions(tools)` 相当）を欠いた簡略版テンプレート**で、
  `common_chat_params_init_functionary_v3_2` の分岐条件に当たらない——**取り込んでも Llama 3.1（§12.4）と
  同じ「差分オートパーサ」経路に落ちる**可能性が高い（実機で確認していないので「可能性が高い」までしか
  言えない）。
- **分からなかったこと**: この GGUF がなぜ簡略版テンプレートを埋め込んでいるのか（変換ミスか、meetkai が
  2024-08 以降にベースリポジトリの `tokenizer_config.json` だけ更新して GGUF を再変換していないのか）。
  再変換すれば直る見込みはあるが、それは利用者の管理操作（GGUF の作り直し・別配布元探し）の範囲。
- この 1 件のために **表 13.3 の #5 は最下位**に置いた。優先して測るなら #1〜#4 から。

### 13.6 分割 GGUF の有無

**5 本とも単一ファイル**（分割 GGUF ではない）。`tree/main` の実測一覧（13.3 の各行のファイル欄）に
`-of-000` を含むものは無い。[[engine-split-family-parts]] の注意（分割族は 1 回で取り込む必要がある）は
今回**適用対象がない**。

### 13.7 取り込みの実務 — `store.EngineModel` への写像（提案。実測ではない）

🔴 **2026-09-20 再訂正**: 前回の訂正（「既存行に合わせて `Args` に `jinja`/`mmap`/`n-gpu-layers` を書け」）は
**誤りだった**——駆動役が実機の管理 API（`GET /api/admin/engines` の `model_rows`）で確認した事実により
再度直す。以下は再訂正後。

`store.EngineModel`（`control-plane/internal/store/store.go:224` 以降）の関連欄: `Role`（`"llm"`）・
`ID`（目録の鍵、ロール内で一意——`engine_admin.go:1376` の POST ボディで管理者が自由入力、文法を定めた
ADR は無い）・`Kind`（既存 5 行は `"checkpoint"` または `"gguf"`。既存行の実例から `"checkpoint"` を踏襲）・
`BaseModel`（llm プロバイダには語彙検証が無い——`engineBaseModelsFor`（`engine_catalog.go:323-328`）は
`provider=="comfy"` のときしか語彙を返さないので、lcpp の `BaseModel` は自由記入。妥当な値の**提案**であって
強制される文法ではない）・`Source`（store.go のコメントが定める形式 `hf:<repo>/<file>`）。

🔴 **`Args` は空でよい（というより、ingest 経由では設定できない）。**

- 実機（`GET /api/admin/engines` の `model_rows`、2026-09-20・駆動役の確認）: **既存 5 行すべて
  `args: null`**（`llama-3.1-8b-instruct-q4_k_m`・`qwen3.6-35b-a3b-ud-iq3_s`・
  `qwen3.8-27b-uncensored-q4_k_m`・`qwen3-coder-30b-a3b`・`qwen2.5-1.5b-abliterated-lora` 全部）。
  §13.7 初稿・第一次訂正はどちらも `models-response.json`（`GET /engine/{key}/v1/models` の preset 表示）の
  `jinja = 1`/`mmap = 0`/`n-gpu-layers = 99` を**行の `Args` 由来と読み違えていた**。
- 実際の出どころは**役全体の `LlmExtraArgs`**（`deploy/aws/ecs/cfn/60-engines.yaml:49-52`、既定値
  `-ngl,99,--jinja,--no-mmap`）——ルーター起動引数として**全モデルの preset に後掛けされる**
  overlay（`tools/server/server-models.cpp:548-551`、§12.4 で確定済み）。行ごとに `--jinja`/`--mmap`/
  `-ngl` を書く必要は無い（書いても role 側で上書きされる、と §12.4-B 既述）。
- 🔴 **そもそも ingest API では `Args` を渡す欄が無い。** `POST …/ingest`（このセクションの取り込み経路）が
  受ける body `engineIngestBody`（`engine_admin.go:1711-1737`）は `id`/`kind`/`plan_token`/`source`/
  `description`/`base_model`/`context_tokens`/`max_output_tokens`/`sizes`/`params`/`license_accepted`
  のみで、**`args` フィールドが存在しない**（実物のフィールド一覧を確認済み）。`args []string json:"args"`
  が出てくるのは `POST …/models`（`postModel`、`engine_admin.go:1402`——**箱に既にあるファイルを手で
  登録する別経路**）だけ。**ingest でこの 5 本の行を作る限り、`Args` は設定しようがない**（欄そのものが無い）。

🔴 **`--ctx-size` は（そもそも `Args` に入れる欄自体が無いが、念のため）`context_tokens` という別欄から来る。**
`store.EngineModel.ContextTokens`（`context_tokens` として ingest body に渡す——`engine_admin.go:1723`）が
`engineActiveModel.Ctx`（wire key `"c,omitempty"`・`engine_catalog.go:388`、ドキュメントコメント
「the window this model is started with (llama-server's -c). Per MODEL」）へ写る欄で、
`engine_catalog.go:433` の `Ctx: m.ContextTokens` がその変換点。`fetch-models.sh:123` の jq では
`(if ($m.c//0)>0 then ["c = "+($m.c|tostring)] else [] end)` という**独立した分岐**から `presets.ini` の
`ctx-size = …` 行になる。

既存行がどう入っているかは、目録行を作る静的な JSON/シードファイルとしてこのリポジトリには存在しない
（テストの中のリテラル `store.EngineModel{Role: "llm", ID: "qwen3-coder-30b-a3b", Kind: "gguf", Enabled:
true, Default: true}`（`engine_gateway_test.go:1035`）程度で、実物は管理者の ingest 操作でしか作られない）。
したがって以下は「この形で POST すればこの表と同じ意味の行になる」という**提案**であり、実際に POST・
ingest したものではない（指示どおり、取り込み・有効化はしていない）:

| # | 提案する `ID`（鍵） | `Source` | 提案する `BaseModel` | `Args` | 推奨 `c`（ctx-size） |
|---|---|---|---|---|---|
| 1 | `gpt-oss-20b-mxfp4` | `hf:ggml-org/gpt-oss-20b-GGUF/gpt-oss-20b-MXFP4.gguf` | `gpt-oss` | 設定不要（ingest に欄が無い。既存行同様 null になる） | **131072**（学習上限そのもの。§13.8） |
| 2 | `gemma-4-12b-it-q4_k_m` | `hf:unsloth/gemma-4-12b-it-GGUF/gemma-4-12b-it-Q4_K_M.gguf` | `gemma-4` | 設定不要 | **262144**（学習上限そのもの。§13.8） |
| 3 | `lfm2.5-8b-a1b-q4_k_m` | `hf:LiquidAI/LFM2.5-8B-A1B-GGUF/LFM2.5-8B-A1B-Q4_K_M.gguf` | `lfm2-moe` | 設定不要 | **128000**（学習上限そのもの。§13.8） |
| 4 | `ministral-3-8b-instruct-2512-q4_k_m` | `hf:unsloth/Ministral-3-8B-Instruct-2512-GGUF/Ministral-3-8B-Instruct-2512-Q4_K_M.gguf` | `ministral-3` | 設定不要 | **65536**（学習上限 262144 は L4 に載らない。§13.8） |
| 5 | `functionary-small-v3.2-q4_k_m` | `hf:bartowski/functionary-small-v3.2-GGUF/functionary-small-v3.2-Q4_K_M.gguf` | `llama-3.1` | 設定不要 | **65536**（既存 `llama-3.1-8b-instruct-q4_k_m` 行と同じ値・同じ理由。§13.8） |

`mmproj-*.gguf`（gemma-4・LFM2.5-VL 系ではなく gemma-4 のみ同梱）はテキストのみで使うなら `Files` に
含めない。ライセンス欄（`License`/`LicenseName`/`LicenseURL`）は #3 に LFM Open License v1.0（13.3 参照）を
明記すべき。#5 は 🔴 13.5 の懸念どおり、この GGUF のままでは専用パーサに届かない見込み。`--chat-template-file`
で llama.cpp 同梱の正しいテンプレートへ差し替える手が理論上あるが、**`Args` が ingest では設定できない以上
（上記）、行単位でこの手を打つ経路はそもそも無い**——役全体の `LlmExtraArgs` を変えるしかなく、それは
lcpp 役全体に影響する管理操作で今回の範囲外。#4 の `ID` は既存キーの慣例（`llama-3.1-8b-instruct-q4_k_m`
のように長いファイル名をそのまま）に倣うと長い。短縮するかは利用者判断——本書はどちらかを断定しない。

### 13.8 推奨 `c`（ctx-size）の根拠 — 族ごとに上限を決めているものが違う

🔴 `context_length`（GGUF 側の学習上限。既存行の `meta.n_ctx_train` に相当）は**上限であって、
そのまま設定してよい値ではない**（[[gguf-kv-cache-and-ceiling]] の「窓に公開上限を入れる罠」）。
13.4 の層別の式で、候補ごとに「学習上限まで使ったら重み+KV がいくつになるか」を計算し、**L4 24GB に
実際に載るか**で上限採用の可否を判定した。**計算バッファ・CUDA コンテキスト分の余白**は本書に実測が
1 件しかない（[[gguf-kv-cache-and-ceiling]]: `qwen3.8-27b-uncensored`、重み 16.69GiB+KV(窓 65536 訂正後)
4.00GiB=20.69GiB で起動**成功**——24GiB との差 3.31GiB が実際に足りた実測値）。ここでは**その 1 件だけを
根拠に、保守的に 3GiB を余白として引いた 21GiB を「安全に載る」判定の予算**にした——他の 4 族（特に
MoE・ハイブリッド構造）で計算バッファの実際の必要量が同じとは**確認できていない**。

| # | 学習上限（GGUF `context_length`） | 学習上限での 重み+KV | 判定 | 推奨 `c` | 推奨 `c` での 重み+KV |
|---|---|---|---|---|---|
| 1 gpt-oss-20b | 131072 | 11.28+3.00=**14.28GiB** | 21GiB 予算に対し 6.72GiB 余白——**載る** | **131072**（学習上限を推奨） | 14.28GiB |
| 2 gemma-4-12b | 262144 | 6.63+4.31=**10.95GiB** | 10.05GiB 余白——**載る** | **262144**（学習上限を推奨） | 10.95GiB |
| 3 LFM2.5-8B-A1B | 128000 | 4.80+1.46=**6.27GiB** | 14.73GiB 余白——**載る（余裕が最大）** | **128000**（学習上限を推奨） | 6.27GiB |
| 4 Ministral-3-8B | 262144 | 4.84+34.00=**38.84GiB** | 🔴 **L4 の物理容量 24GiB を大きく超える（載らない）** | **65536**（重み+KV 13.34GiB・7.66GiB 余白） | 13.34GiB（参考: 131072 なら 21.84GiB で 21GiB 予算を超え非推奨） |
| 5 functionary-v3.2 | 131072 | 4.58+16.00=**20.58GiB** | 21GiB 予算に対し 0.42GiB しか余らない——**きつすぎるので非推奨** | **65536**（重み+KV 12.58GiB・8.42GiB 余白。既存 `llama-3.1-8b-instruct-q4_k_m` 行と同じ値） | 12.58GiB |

**族ごとに「何が上限を決めているか」が違う**: #1〜#3（GPT-OSS・gemma-4・LFM2.5）はハイブリッド構造
（sliding/conv 層が大半）のおかげで KV が軽く、**学習上限そのものが L4 に楽に載る**——推奨値は学習上限に
一致させた。#4・#5（Ministral-3・functionary-v3.2）は**全層が full attention** で KV が ctx に比例して
素直に伸びるため、**VRAM が学習上限よりずっと手前で先に効く**——Ministral-3 は学習上限（262144）の
7 分の 1 以下（65536）でしか安全に動かせず、functionary-v3.2 は既存の `llama-3.1-8b-instruct-q4_k_m` 行
（同じアーキテクチャ・同じ 65536）と同じ値に落ち着いた。これは推測ではなく、**同じアーキテクチャの
既存行が既にその値で稼働している**という直接の先例がある（`models-response.json` 実測）。

**追記（2026-09-20・駆動役より）**: 実際の取り込みでは GPT-OSS-20B・gemma-4-12b-it とも
`context_tokens=32768` で登録した——本書の推奨（学習上限、#1=131072・#2=262144）より低い。理由は
費用: 取り込み時の VRAM 自動見積りが非均一な `layer_types`（§13.4 末尾で本書が指摘した、この
リポジトリの `engine_gguf.go` がまだ扱えないパターン）を扱えず過大に出るため、窓を大きく取ると
インスタンスクラスが上の段（`g6e-od`・$2.70/h）へ動きうる。実測に必要な窓は harness の圧縮窓と同じ
3,500 で足り、後から上げられる。**本書の見積り（学習上限まで載る、という計算そのもの）を否定する
事実ではない**——実際に選んだ運用値が別の制約（自動見積りの精度・費用）で低く決まった、という
別軸の話として両方を記録する。

## 受け入れ条件チェック（このセクションのみ）

- 全項目に一次資料（URL/ファイル/行番号）を付けた。推測は「提案」「分からなかった」と明記した。
- 「この族なら通る」という断定はしていない——13.3 冒頭と各行の懸念欄で明記。
- Go のコードは読んだだけで 1 行も変えていない。
- ブランチは切っていない（`temp/sj477m4` のまま）。`git stash` は使っていない。GGUF ファイル本体は
  一切取得していない（レンジ GET も含め、しなかった——`config.json` と HF のメタデータ API のみ）。

## 14. 実機 14 本の実測一覧（段 2 着手前・2026-09-20 追加・2026-09-20 生ログ突合により訂正）

課題は §12.5 と同じ（`TestManualLiveAgenticSession`・`workspace/agent/internal/harness/live_manual_test.go`・
バグ持ちの Go プロジェクトを直させる 5 タスク＋圧縮後の想起 1 問）。番号は #1〜#14 で、#5 は「並列指示あり／なし」
の 2 走行を数える——§12.5 が実機 6 本（#1〜#6）と数えたのと同じ数え方をそのまま延長し、番号行は**「実機 14 本」**・
#5 の 2 走行を数えると**走行数は計 15**。§12.5 の表はそのまま残し、以下がその延長（#7〜#14）を含む全体表。

🔴 **本節は駆動役セッションが `$HOME/lcpp-live/` の生ログを突き合わせて 1 回訂正している**（初出時は #14 が
抜けており、#9〜#12 の数字にもずれがあった）。以下は訂正後の確定値。生ログ本体（実ホスト名・実パスを含む）は
ここに引用しない——載せるのは数値と `=== SUMMARY … ===` の項目名（`total_assistant_turns` /
`compacted_at_turn` / `parallel_tool_call_turns` / `invalid_tool_call_json` / `unknown_tool_calls` /
`bad_arg_shape`）まで。

🔴 **番号順（#1〜#14）は走行順ではない。** #14 は番号こそ末尾だが、実際には #10〜#13 より前に走っている
（下記「走行順とハーネス版」参照）。番号は §12.5 からの延長のために振った識別子であって、時系列ではない。

| # | モデル | 窓 | 結果 | ターン | 圧縮 | 並列 | JSON不正/未知名/引数欠落 | 所要 | ハーネス版 |
|---|---|---|---|---|---|---|---|---|---|
| 1 | qwen3.8-27b-uncensored-q4_k_m | 3500 | PASS | 30 | turn 13 | 11 | 0/0/0 | 431.58s | 段 1 |
| 2 | qwen3.8-27b-uncensored-q4_k_m | 3500 | PASS | 29 | turn 11 | 9 | 0/0/0 | 348.90s | 段 1 |
| 3 | qwen3-coder-30b-a3b（1 回目） | 3500 | 完走だが 162 ターン（`todo_write` が **72 ターン連続**・turn 50〜121）・圧縮後の想起が 17 ターン迷走 | 162 | turn 19 | 0 | 0/0/0 | 451.28s | 段 1 |
| 4 | ternary-bonsai-2-27b-pq2_0 | — | FAIL（起動せず・ggml 型 142＝フォーク限定・§12.2）**目録から削除済み** | — | — | — | — | 605.48s | 段 1 |
| 5 | llama-3.1-8b-instruct-q4_k_m（並列指示あり） | 3500 | FAIL（tool call の構文解析で失敗） | — | — | — | — | 17.98s | 段 1 |
| 5' | llama-3.1-8b-instruct-q4_k_m（**並列指示なし**） | 3500 | FAIL（同じ症状。**並列指示が原因という仮説は反証された**） | — | — | — | — | 15.58s | 段 1 |
| 6 | qwen3.6-35b-a3b-ud-iq3_s | 3500 | PASS | 33 | turn 10 | 14 | 0/0/0 | 144.14s | 段 1 |
| 7 | **gpt-oss-20b-mxfp4**（新規・MoE） | 3500 | **PASS** | 95 | turn 53 | 0 | 0/0/0 | 480.11s | 段 1 |
| 8 | **gemma-4-12b-it-q4_k_m**（新規・dense） | 3500 | **PASS** | 59 | turn 36 | 0 | 0/0/0 | 251.65s | 段 1 |
| 9 | qwen3-coder-30b-a3b（2 回目・再測） | 3500 | FAIL＝**うちの欠陥**。task-1 を 37 ターンで完走した直後、task-2 の最初の送信が実窓超過（32772 トークン＞窓 32768 トークン）で中断 | 37（task-1 まで） | — | — | — | 305.64s | 段 1（#811 の直前） |
| 10 | gemma-4-12b-it-q4_k_m（PR #811 の**初版**） | 3500 | FAIL＝**こちらの変更が原因**。353 ターン超で未完（task-2 進入時点で 350・task-3 進入時点で 353。FAIL のため SUMMARY 無し＝総数不明） | 353+ | 計 95（task0:9／task1:86／task2:0／task3:0） | 0 | — | 2488.38s | #811 初版（ログ強化後） |
| 11 | gemma-4-12b-it-q4_k_m（#811 最終） | 3500 | task-4 で `ErrCompactionThrashing` により**明示停止**（設計どおり） | 139 | 計 35（task0:7／task1:3／task2:24／task3:1） | 0 | 0/0/0 | 1131.20s | #811 最終 |
| 12 | gemma-4-12b-it-q4_k_m（#811 最終） | 8000 | **PASS**。圧縮が正しく発火し想起も正確＝**直近往復の生保持が効いた証拠**（`compacted_at_turn=0`） | 63 | 計 2（task0:1／task4:1） | 0 | 0/0/0 | 304.04s | #811 最終 |
| 13 | gemma-4-12b-it-q4_k_m（#811 最終） | 24000 | PASS（圧縮 0 回＝現実的な窓では圧縮自体が要らない。`compacted_at_turn=-1`） | 43 | 0 | 0 | 0/0/0 | 262.40s | #811 最終 |
| 14 | qwen3-coder-30b-a3b（3 回目） | 3500 | 🔴 FAIL＝非収束（**ただし #811 初版のハーネスで走っており、#10 と同じ圧縮スラッシング欠陥に晒されている——単独ではモデルの性質の証拠にならない**）。task-0 だけで 434 ターン（同じ検証を延々繰り返す）。その後 task-1 の最初の送信が接続タイムアウトで打ち切り | 434（task-0 のみ） | task-0 で 1 回 | 0 | — | 3300.0s（テストの上限に張り付き） | 🔴 #811 初版（ログ強化前） |

補足:

- 生ログは `$HOME/lcpp-live/`（`log-*.txt`・`props-response.json`・`models-response.json`）にあり、**セッションを
  消しても残る**が、**リポジトリの外なのでセッション外からは見えない**。本文は実ホスト名・実パスを含むため、
  この文書へは引用しない。
- 種プロジェクトは `workspace/agent/internal/harness/testdata/liveproject/`（PR #795）。実行ごとに `cp -r` で
  未使用の複写を作る。
- 🔴 **走行順とハーネス版（2026-09-20 追加訂正）。** ログの mtime とコミット時刻を突き合わせ、`git log -S` で
  ログ文字列の初出コミットを特定して確定した。`compaction fired during task-N` は **52bbda2d**（ツールループ内
  でも窓を見て圧縮するようにした＝PR #811 の初版）でしか導入されておらず、`compactions during task-N: n` は
  **6f9dbae0**（ログ強化）が初出、PR #811 の**最終**版は **570d5042**（直近往復を生で残し圧縮スラッシングを
  打ち切る）。走行順は次の通り: #3 → #1 → #2 → #4 → #5 → #6 → #5' → #7 → #8 → #9（ここまで段 1 のハーネス、
  タスク境界でしか圧縮しない）→ **#14**（52bbda2d 以降・6f9dbae0 未満＝#811 初版・ログ強化前）→ #10（6f9dbae0
  以降＝#811 初版・ログ強化後）→ #11 → #13 → #12（この 3 本が #811 最終版・570d5042）。**圧縮の形が変わる
  境界は走行順で #14 以降**であり、それ以前の 10 走行（#1〜#9 と #5'）はタスク境界でしか圧縮していない。
  さらに #14 と #10 は #811 の**初版**、#11〜#13 が**最終**版——番号順の #9→#10→#11→#12→#13→#14 という
  並びと実際の版の切り替わりは一致しない。合否の基準（`tool_calls` の JSON 健全性）は版によらず同じなので
  比較はできるが、**`compacted_at_turn` の意味は #811 初版以降で変わっている**ことに注意（版をまたいで
  この値を素朴に突き合わせない）。⚠️ 走行順内でも #13（w24k）は #12（w8k）より先に走っている——番号は識別子
  としてそのままでよいが、走行順の記述で取り違えないこと。
- 🔴 **`qwen3-coder-30b-a3b` は #811 最終版のハーネスでは 1 度も測っていない。** 3 回の測定はそれぞれ別の
  ハーネス版に当たっている: **#3＝段 1 版で 72 ターン連続ループ**／**#9＝段 1 版で実窓超過**（この原因である
  「`Run` に窓が渡らない」欠陥は 52bbda2d と 570d5042 で直っている）／**#14＝#811 初版で非収束**（この版自体が
  #10 と同じ圧縮スラッシング欠陥を持つため、coder の性質の証拠として使えない＝交絡）。したがって「3 回とも
  別々の理由でモデルがダメ」ではなく、**「3 回とも別々のハーネス版で測っており、直った版（#811 最終）での
  評価がまだ無い」**が正しい要約。合格チェックポイントには入っていない。段 2 の残作業として、#811 最終版で
  qwen3-coder をもう 1 本測ることを推奨する。
- 🔴 **§14 #13 の直前に HTTP 502 が 1 回出ている**（gateway の一時障害）。原因は `retryOnWake` の再試行対象
  文字列一覧に `HTTP 502` が無く、リトライせず即 Fatal していたこと。PR #811 で再試行対象に追加した。前
  セッションが実際に生ログで確認した cold start／起床まわりの失敗は、#5' の 302.33 秒 `context deadline
  exceeded`（§12.5・§14 表）と、この HTTP 502 の 2 件のみ。

## 15. 転写の保存方式の再検討（2026-09-21・親セッション from=so2ydq6 の依頼・利用者の明示的な問題意識）

利用者の問題意識（原文の趣旨）: 「転写の jsonl 形式がベストな方法か再検討したい。ecs-ec2 構成では
`/var/lib/af` は EFS でマウントされており、負荷に弱い。claude の方式に寄せず、他のエージェントでベストな
方法があればそちらの方法でも良い」。ADR 0087 が名指しした「未測定の項」（決定 1: 「10 セッション化で本当に
伸びるのは走行中の転写書き込み」）を、`lcpp` の store（`workspace/agent/internal/agents/lcpp/store.go`、
PR #818 でレビュー済み・develop マージ済み）に対して先取りで検証した。

結論を先に書く: **(1) 置き場所は `AgentDataDir` → `AgentStateDir` に直した（実装済み）。durability の差は
無い——両方とも EBS の同じボリュームで、正しさ／一貫性の修正であって耐障害性の変更ではない。(2) 書き込みの
形は「1 レコードごとに開閉」から「ハンドルを使い回す」に直した（実装済み）——レコードあたり約 4.09
→ 約 1.10（定常状態では書き込み 1 回のみ）に実測で減った。(3) claude の方式（EFS 上の jsonl）に寄せる
のは誤りで、他 kind の実例（SQLite・codex の rollout jsonl）を見ても、いま `lcpp` が使っている「追記専用
jsonl・自前で書いて自前で読む」という形自体は変える理由がない。** 詳細は以下。

### 15.1 問い1: 置き場所は `AgentStateDir()` にすべきか

**結論: すべき。直した。** `paths.go` の 2 つの doc コメントを読み比べると判定基準は明確だった:

- `AgentStateDir()`（`internal/paths/paths.go:33-58`）の doc コメントは 3 段の線引きを書いており、その 3
  番目が「Anything keyed by session name or sid belongs on this side」。ADR 0087 決定 4 の実装節
  （`docs/decisions/0087-efs-metadata-io.ja.md:786-787`）も「残りは AgentStateDir。セッション名や sid で
  引くものは全部こちら」とほぼ同じ言葉で繰り返している。`lcpp` の store は `<sid>.jsonl` そのもの——
  sid で引く典型例で、この規則に照らして最初から `AgentStateDir` 側だった。
- 対して `AgentDataDir()`（`paths.go:135-141`）の doc コメントが挙げる用途は「削除したセッションの
  cleanup archive」であり、実際の呼び出し元（`cleanup_archive.go:34` の `cleanup/`、`internal/afdb/afdb.go:214,223`
  の postgres/mysql データディレクトリ、`internal/usagex/ledger.go:139` の使用量台帳、
  `internal/agents/opencode/workspaceid.go:70` の単一ファイル）はどれも sid 単位ではない——`lcpp` の store
  だけがこの中で唯一 sid キーだった。ズレの原因は悪意でも設計判断でもなく、単に「両方の doc コメントを
  突き合わせて確認する」というチェックが無かったこと（`lcpp` はまだ配線されていない段階のコードで、
  レビューの俎上にも上っていなかった——PR #818 のレビューでもこの点は指摘されていない）。

**副産物として denylist の穴が閉じた。** Console のファイルブラウザの denylist（`workspace/agent/fs.go`
の `fsDeny`）は `.local/state/agent-fleet`（=`AgentStateDir`）を持つが `.local/share/agent-fleet`
（=`AgentDataDir`）は持たない。つまり**移す前は、生の会話内容（ツール結果・reasoning を含む）がファイル
ブラウザ経由で見えていた**——ADR 0087 決定 4 の「移行先にも今と同等の保護が要る」という警告が、ここでは
逆向き（今の場所に保護が無かった）に効いていた。`AgentStateDir` へ移すことでこの保護を新規実装無しで
獲得した。

**トレードオフは「無い」——ただし理由が本質的**: 依頼文は `AgentStateDir` を EBS 単一 AZ・
`AgentDataDir` を暗に比較対象として挙げていたが、実際には**両方とも同じ home ボリューム（EBS・単一 AZ）
に載っている**（`0087-efs-metadata-io.ja.md` の背景節の表: `home → /home/dev` は「インスタンスの EBS」の
1 行のみで、`~/.local/share` と `~/.local/state` はどちらもその配下）。EFS が載っているのは `claude`
（`CLAUDE_CONFIG_DIR`）と `keep`（`AF_WS_KEEP_DIRS`）の 2 マウントだけで、どちらも `AgentDataDir` /
`AgentStateDir` とは無関係。**したがって『どちらの状態ディレクトリに置くか』は耐久性の論点ではなく、
一貫性と denylist の論点でしかない。** 耐久性の本当の論点は次の「claude に寄せるべきか」（EBS vs EFS）
であり、そちらは §15.1.1 で扱う。

#### 15.1.1 claude に寄せて EFS に置くべきか（利用者の問題意識への直接の回答）

**結論: 寄せるべきではない。** claude の転写が `CLAUDE_CONFIG_DIR`（EFS）に載っているのは偶然ではなく、
ADR 0045 決定 3-6 と ADR 0087 の背景節が明言する設計判断そのものである: 「単一 AZ・単一ボリュームの EBS
が失われたとき、ログイン情報まで一緒に失わないため」（`0045-ec2-persistent-workspace.ja.md:85-86`）。
ADR 0087 決定 4 のトレードオフ節も同じ非対称性を認めている——**home（EBS）を失うとセッション台帳は失うが
「転写そのものは `CLAUDE_CONFIG_DIR` 側なので残る」**（`0087-efs-metadata-io.ja.md:764-768`）。つまり
claude にとって EFS は「会話内容という取り返しのつかないものを、取り返しのつく台帳より長生きさせる」ため
の意図的な置き場所であり、**EFS のメタデータ I/O コストはその耐久性と引き換えに払っている代償**である。

`lcpp` にこの非対称性は無い。`lcpp` の store は claude のような二重構造（消えても構わない台帳 + 消えては
困る転写）を持たない——**store そのものが会話の正本**（ADR 0093 決定 3）であり、失えばセッション台帳と
同じ運命どころか、会話内容そのものが消える。だからといって claude と同じ場所（EFS）に置くべきだ、とは
ならない。理由は 2 つ:

1. **EFS に置くことは、ADR 0087 が全力で塞ぎにいった問題をこの store 1 つのために再導入すること**になる。
   ADR 0087 決定 1 が名指しした「未測定の項」（走行中の転写書き込み）は、まさにこの store が EFS 上に
   あったときにだけ意味を持つ懸念であり、§15.2 の実測はその懸念が**現状は的外れである**ことを示す
   （store は EBS 上にあり、EFS のメタデータ課金にもバーストクレジットにも一切乗らない）。ここを EFS へ
   動かせば、その実測がそのまま「本当に効いてくる数字」に変わる——利用者が最初に持っていた懸念そのものを
   自分で作り出すことになる。
2. **EBS 喪失時に失うものの重さが claude と違う。** claude はセッション一覧が空になるだけ（転写は残る）。
   `lcpp` は EBS を失うと会話そのものを失う——これは重い。しかし「EFS に置けば防げる」も過大な期待で、
   ecs-ec2 では GPU エンジンを載せた ECS タスクと Workspace の home が同じ AZ 障害に巻き込まれる場面では、
   会話を再開する先（動いている llama-server、Workspace 自体）もろとも失われる可能性が高く、
   **転写だけを生き残らせても対話は再開できない**。EBS 喪失というまれな事象に備えて、日常的な
   メタデータ I/O コストを毎回払うのは割に合わない。

**したがって `lcpp` の store は EBS（`AgentStateDir`）に留め、claude の方式には寄せない。** これは
「たぶん速いから」ではなく、ADR 0045/0087 が明文化した EFS 選択の理由（資格情報・claude 転写の耐久性）が
`lcpp` には当てはまらないという、一次資料に基づく判断である。将来 EBS 喪失時の転写保全が重要な要件になる
場合は、EFS への丸ごと移設ではなく、非同期バックアップ／レプリケーションのような別の手段を検討すべき
（本調査のスコープ外）。

### 15.2 問い2: 書き込みの形は妥当か（実測）

**実測方法**: `internal/session/meta_probe_test.go` と同じ手法——実コードをテストバイナリに固めて子
プロセスとして起こし、`strace -f -c`（`-e trace=file` は使わない。`fstat`/`close`/`write` はファイル名を
取らないので `trace=file` だと取りこぼす——`meta_probe_test.go` 自身が既に踏んだ罠と同じ）。プローブは
`internal/agents/lcpp/store_probe_test.go`（`TestProbeAppend`、`AF_PROBE=1` でのみ動く）として追加した:

```
go test -c -o /tmp/lcpp-probe ./internal/agents/lcpp/
AF_PROBE=1 strace -f -c /tmp/lcpp-probe -test.run '^TestProbeAppend$'
```

500 回の `AppendUser` を、修正前と修正後のコードでそれぞれ計測した。

| 実装 | openat | newfstatat | write | close | mkdirat | 合計（append 起因） | 1 レコードあたり |
|---|---|---|---|---|---|---|---|
| 修正前（毎回 MkdirAll→OpenFile→Write→Close） | 516 | 505 | 504 | 515 | 7 | 2,047 | **約 4.09** |
| 修正後（ハンドルを使い回す） | 17 | 6 | 504 | 15 | 7 | 549 | **約 1.10**（定常状態は write 1 回のみ） |

内訳の意味: 修正前は `os.MkdirAll` が実装上まず `Stat`（`newfstatat`）を打ってから既存なら何もしない、と
いう形なので、ディレクトリが既にあってもレコードごとに 1 回の `stat` が乗る。それに `OpenFile`（`openat`）
と `Close` を毎回挟むので、**メタデータ操作だけで 1 レコードあたり 3 回**（stat + openat + close）＋
データ操作 1 回（write）＝ **4 回**——利用者への報告文が挙げていた「3〜4 回」という見立てが、この store
では実測でも正確だった。修正後は最初の 1 回だけが MkdirAll/OpenFile を払い、以降は保持したハンドルへの
`Write` だけが増える。

**なぜこれを直す価値があったか（EFS に無いのに）**: 現状 `lcpp` の store は EBS 上にあり、この差は
本番の請求にもレイテンシにも今日時点では効かない（EBS のローカルシステムコールは EFS の NFS ラウンドトリップ
と桁が違う）。しかし直す理由は 3 つある: (a) タダで直せる——決定 3 の不変条件（追記単調・最終行だけが
破損しうる）を一切変えずに済む改善であることを下で確認した、(b) `harness` の `runToolCalls` は 1 ターンの
複数ツール結果を並行に append するため、この mutex の臨界区間が短くなること自体が同時実行の観点で意味を
持つ、(c) §15.1.1 で「EFS には寄せない」と決めたが、将来 lcpp の要件が変わって別のネットワークファイル
システムに載る可能性がゼロとは言えない——そのときに効いてくる直しを今のうちに入れておくのは ADR 0087 の
教訓そのものである。

**クラッシュ耐性は変わらない。** `os.File.Close` は `fsync` を呼ばない——閉じても閉じなくても、書き込み
済みの bytes の耐久性（ページキャッシュに乗っているだけで、まだディスクに無いかもしれない）は同じである。
「最終行だけが壊れうる」という Records の契約を支えているのは「それより前の Write は既に成功して返って
いた」という事実であって、その後ハンドルを閉じたかどうかとは無関係——ハンドルを使い回しても、プロセスが
死んで良いのは *まさにいま書き込み中だった 1 行* だけ、という性質は変わらない（`store.go` の `append` の
doc コメントに明記した）。

**実装**: `Store` に `f *os.File` を追加し、`append()` が最初の呼び出しでのみ `MkdirAll`+`OpenFile` を行い、
以降は保持したハンドルへ `Write` するだけにした。まだ誰もこの kind を配線していない（`agent.go` 自身の
doc コメント）ため呼び出す側は無いが、将来のセッション終了時に呼べるよう `Close()` を追加した——呼ばなくて
も正しい（プロセス終了時に OS が fd を閉じるだけで、`Close` 自体が足すものは無い）が、fd を無限に持ち続け
ないための片付け先を用意した。

**陽性対照（実施済み）**:
- `statemig.Entries` から `"lcpp"` を一時的に外し、`TestEveryStateStoreIsInEntries` が赤くなることを確認
（`"lcpp" (internal/agents/lcpp/store.go) resolves under the state dir but is not in statemig.Entries`）→
Edit で戻し緑を確認。
- `Close()` の `s.f = nil` を一時的に外し、新設した `TestCloseThenAppendReopens`（2 回目の `Close` で
`file already closed`）が赤くなることを確認 → Edit で戻し緑を確認。

**陰性対照**: 既存の `store_test.go` 全 15 本（`TestAppendIsMonotonic`・`TestAppendConcurrentNoTornLines`・
`TestRecordsTruncatedFinalLineIsTolerated`・`TestRecordsMidStreamCorruptionStillErrors` を含む）が
`-count=1` で緑のまま——追記単調・最終行のみ破損許容・合成ターン除外のどの不変条件も壊していない。

### 15.3 問い3: 他 kind の実例に学ぶものはあるか

4 kind の実際の格納形式を一次コードから確認した（読み取り専用調査。詳細は各 `internal/agents/<kind>/` 配下）:

| kind | 実際の形式 | 追記専用か | 備考 |
|---|---|---|---|
| opencode | SQLite（`~/.local/share/opencode/opencode.db`、WAL、`message`/`part` テーブル） | **否**——可変なリレーショナルストアで、約 38 回の適用済みマイグレーションを持つ未バージョン管理のスキーマ | `internal/agents/opencode/transcript.go:20-38,120-123` |
| codex | JSONL rollout（`~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl`） | **是**——`transcript.go:27-31` に明記 | `rolloutcache.go` が末尾だけを増分パースし、途中で壊れた・縮んだファイルは破棄して読み直す |
| copilot | JSONL（`events.jsonl`、ライブ追記） | **是**——`stop.go:3` に明記 | SQLite の索引（`session-store.db`）も別途持つが、うちのコードは**あえて読まない**（`forkat.go:4-21`）——jsonl の方が正本という判断 |
| kiro | JSONL v2 session store（`~/.kiro/sessions/cli/<sid>.jsonl`） | **是**——`kiro.go:16` に明記 | 旧 SQLite（`~/.local/share/kiro-cli/data.sqlite3`）は**意図的に見ない**——`kiro.go:18-20` のコメントは「opencode のストア契約変更が引き起こした false-idle の教訓」と明記しており、他社スキーマへの依存が実際に事故を起こした前例そのもの |

🔴 **決定的な非対称性**: 上の 4 kind はどれも「CLI 自身が書き手で、うちは読むだけ」。フォーマットを選べる
のは `lcpp` だけであり、「他がこうしているから」は理由にならない——**うちの要件（追記単調・ミラーが末尾を
読む・fork が anchor で切る・クラッシュ耐性・EBS 上）に対して良いかどうか**で評価する。

**SQLite（opencode の実例）を採らない理由**: (a) 依存が増える（pure-Go ドライバでも新規の外部依存）、
(b) WAL モードは副ファイル（`-wal`/`-shm`）を伴い、`lcpp` が今 mutex 1 本で済ませている「1 プロセス内の
書き手が 1 つ」という単純な排他モデルに対して過剰、(c) 決定 3 の「追記専用・最終行だけが壊れうる」という
契約は、リレーショナルストアの一般的なクラッシュ耐性モデル（トランザクションのロールバック）とは前提が
違い、素直に対応しない、(d) kiro のコメントが第一者の実例として警告している通り、可変スキーマへの依存は
静かな事故（false-idle）を起こした実績がある——**今回は自分でスキーマを書く側になるので他社の破壊的変更
という同じ事故は起きないが、「読み手（ミラー・fork）が持つ前提とスキーマが少しずつずれていく」という
構造的なリスクは残り、append-only な行指向フォーマットにはそもそも無い種類の問題を増やすだけで、得るもの
が無い**。

**codex の rollout（jsonl）は、実質すでに `lcpp` と同じ設計**——追記専用・1 行 1 イベント・最終行の破損
だけを許容、という組み合わせは `lcpp/store.go` が独自に選んだのではなく、うちが既に信頼して読んでいる
別 kind の実例と一致する。**変える理由は無い、むしろこの一致は今の設計を裏付ける一次資料である。**

**§15.2 の外への波及として 1 点だけ記録する（今回は実装しない）**: codex の `rolloutcache.go` は
ミラー側の再読み込みを「新しく増えたバイト分だけ」に抑える増分パースを持つ（`rolloutcache.go:34-51`）。
`lcpp` の `Records()`/`Transcript()`/`Full()` は今のところ毎回全文を読み直す——書き込み側（本節で直した
所）とは別の軸だが、ミラーの poll 頻度が上がった場合に効いてくる可能性がある改善候補として、ADR 0093
決定 3 の P2 相当の残作業に記録しておく（本調査のスコープ外・実装なし）。

### 15.4 ADR 0093 決定 3 に対する提案（決定文そのものの書き換えは範囲外）

ADR 0093 決定 3 の記述のうち、以下の 2 点は本節の結論を踏まえて直すことを提案する（**この文書の役割は
提案までで、決定文自体の書き換えは駆動役・利用者の判断**）:

1. 保存先を `AgentDataDir()` ではなく `AgentStateDir()` と明記する（§15.1）。
2. 書き込み方式について「1 レコード 1 write（ハンドルは使い回す）」という実装上の性質を明記し、
   §15.2 の実測値（4.09 → 1.10 syscalls/レコード）を根拠として添える。EFS には置かない、という
   §15.1.1 の判断も、決定 3 の「保存先」節に一次資料（ADR 0045 決定 3-6・ADR 0087 決定 4）への参照とともに
   残すことを推奨する——次にこの store を触る人が同じ疑問（claude に寄せるべきでは）を再び一から
   調べ直さずに済むように。

