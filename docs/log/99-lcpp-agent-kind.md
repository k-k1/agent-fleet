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

⚠️ 範囲外の観察（原因未調査・本タスクの問いではない）: task-2（turn 52-130、log-coder.txt:60-130）で
`todo_write` にほぼ同一の要約文を 71 回連投しており、これは並列 0 とは別の症状（ターン数が 162 まで膨らんだ
主因はこちらで、こちらは繰り返し検出やサンプラ設定の話になるため上流ソースだけでは切り分けられない。**分からなかった**、
と明記して止める）。

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

### 12.3 エンジンに焼かれている llama.cpp の版（結論: 特定不能・下限のみ判明）

**`build: NNNN (sha)` 形式の版バナーは、CloudWatch（`/af/af-ecs-engines/engines`）のどのログにも一度も現れない。**
Logs Insights で `/af/af-ecs-engines/engines` 全体・過去 7 日を `@message like /(?i)build:/ or
/(?i)system_info/ or /(?i)CUDA devices/` で検索して **0 件**。個別に確認した 6 本の `llm/llama/*` ストリーム
（起動ごとに別ストリーム）は全て一言一句同じ先頭行 `warn: LLAMA_ARG_HOST environment variable is set, but will be
overwritten by command line argument --host` で始まり、その手前にあるはずのビルド行は無い。読み取り専用の
CloudWatch 以外の手段（`/props` などエンジンへの実アクセス）はこのタスクの範囲外なので、**正確な版は分からなかった**。

判明した下限（実際にログへ出た文字列から）:

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
  再取得される（`EcrLlamacpp` の周辺コメント、`cfn/20-platform.yaml`）。固定ダイジェストでの pin はしていないため、
  実行中の正確なコミットは配備時点に依存し、ログからは再構築できない。


