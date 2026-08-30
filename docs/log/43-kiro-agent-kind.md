# 43. Kiro エージェント種別（kind=kiro・第8種）— Track 0 実測記録

status: Track 0（着工前プローブ）完了＋方針4点決定（2026-07-24）。**Track A（workspace agent 本体・read 層＋TUI）実装完了（2026-07-24・temp/snznjpk）**。**Track B（配備・オンデマンド導入＋焼き込みノブ）実装完了（2026-07-24・temp/snznjpk・§7）**。**Track C（CP＋Console 配線・色3種同時変更）実装完了（2026-07-24・temp/snznjpk・§8）**。**Track A2（managed driver・`kiro-cli acp`）実装完了（2026-07-24・temp/kiro-track-a2・§9）**。**Track A/B/C/A2 は全レビュー修正込みで develop へマージ済み（merge de2fb25b）**。**Track D（ライブ使用量の UI 配線）実装完了（2026-07-24・temp/kiro-track-d・§10）＋ADR0026 起票**。**ピン追従の修正（導入済み kiro が新ピンへ上がらない不具合）完了（2026-07-25・temp/syzjob2・§11）**。
関連: docs/40（cursor・章立てのテンプレ）/ docs/36（copilot）/ docs/32（agy）/ decisions/0015（managed driver）/ decisions/0026（本件の ADR）。

## 0. 対象と背景

- **Kiro** = 旧 Amazon Q Developer CLI（2025-11-17 改名）。AWS の Kiro（IDE）のターミナル版。
- 実測対象: **2.14.1**（stable、BUILD_DATE 2026-07-23）。実バイナリを本 Workspace（Debian 12 / x86_64 / glibc 2.36）へ導入し、認証（Builder ID free / device flow）込みで全プローブを実施。
- CLI 本体は非 OSS（旧 aws/amazon-q-developer-cli は MIT/Apache で公開継続・系譜の参照用）。issue は kirodotdev/Kiro。
- `q`/`qchat` は kiro-cli を呼ぶ 65B シムとして同梱（後方互換）。`--v3`（次世代エンジン）のバナーが出るが v1 対象外。

## 1. 結論（プローブ判定）

| 分水嶺 | 結果 | 根拠（実測） |
|---|---|---|
| managed 可否（ACP クロスプロセス resume） | **合格 → v1 から Terminal + Managed 両対応可** | `kiro-cli acp` で initialize→session/new→prompt→プロセス終了→**別プロセスで session/load→文脈保持を確認**（codeword 再答 PASS）。loadSession:true を advertise |
| TUI 状態検出 | **文字列契約が明示テキストで最良クラス** | idle=「ask a question or describe a task ↵」/ working=「**Kiro is working** · Type to steer · Ctrl+S to queue」/ 権限待ち=「shell requires approval」パネル。スピナーグリフ regex 不要 |
| read 正本 | **`~/.kiro/sessions/cli/<sid>.jsonl`（v2 ストア・append-only JSONL）を TUI と ACP が共用** | kind=Prompt/AssistantMessage/ToolResults、message_id・timestamp・toolUse(input)・toolResult(exit_status/stdout) まで記録。SQLite（classic）を読む必要なし |
| ライブ使用量 | **取れる（cursor で不可能だったものが可能）** | ACP `_kiro.dev/metadata` 通知に contextUsagePercentage ＋ meteringUsage(credits) ＋ turnDurationMs。headless でも stderr に「▸ Credits: 0.01 • Time: 1s」 |
| 版ピン・sha256 | **公式 manifest で完結** | `https://prod.download.cli.kiro.dev/stable/latest/manifest.json` に version・版付きパス・sha256・size。`stable/<版>/kirocli-x86_64-linux.zip` で DL、sha256 一致を実測 |
| 認証のコンテナ適性 | **良好（cursor 型 start→poll が組める）** | `login --license free --use-device-flow` は対話ピッカーなしで即 URL+コード表示→CLI 自己ポーリング→「Device authorized」。`whoami` が未認証 exit 1 の状態プローブ |

## 2. 実測記録

### 2.1 配布・導入

- manifest（stable/latest/manifest.json）実測: version=2.14.1、x86_64/aarch64 × zip/tar.gz/tar.xz/tar.zst/deb/appimage、各 sha256・size 付き。`download` フィールドが `2.14.1/kirocli-x86_64-linux.zip` 形式＝**版付き URL でのピンが公式に可能**。
- zip 554MB・**展開後 855MB**: `kiro-cli`(109M) + `kiro-cli-chat`(663M) + `kiro-cli-term`(83M・PTY) + `q`/`qchat` シム。既存 kind と桁違いに巨大（焼込み判断に効く）。
- 同梱 install.sh: ユーザー導入 `KIRO_CLI_SKIP_SETUP=1 ./install.sh`（dotfiles 改変なし・~/.local/bin へ）。root 焼込みは `Q_INSTALL_GLOBAL=1 Q_SKIP_SETUP=1`（/usr/local/bin へ）。
- **glibc 下限（install.sh 明記）: x86_64 は 2.34、aarch64 は 2.39**。Workspace イメージは Debian 12（2.36）→ **arm64 は musl 変種（kirocli-aarch64-linux-musl.zip）必須**。
- auto-update は既定 ON。`kiro-cli settings app.disableAutoupdates true` → `~/.kiro/settings/cli.json` に平文 JSON で永続化（entrypoint 起動毎再固定が容易）。`kiro-cli update` サブコマンドも存在（封殺確認は Track B で）。

### 2.2 認証

- `login --license free --use-device-flow`: プロバイダ対話なしで「Code: XXXX-XXXX / Open this URL: https://view.awsapps.com/start/#/device?user_code=…」を stdout に平文出力→自己ポーリング→「Device authorized / Logged in successfully」。**AF の start→poll 化はこの stdout スクレイプで成立**（agy の OSC-8 汚染のような罠なし）。
- `whoami`: 認証後「Logged in with Builder ID / Email: …」exit 0、未認証「Not logged in」exit 1。接続状態プローブに直用可。
- 資格情報の保存先: **`~/.local/share/kiro-cli/data.sqlite3`（600）の `auth_kv` テーブルのみ**（Builder ID フローでは ~/.aws/sso/cache は作られない）。denylist 追加は `~/.local/share/kiro-cli` と `~/.kiro`（設定・セッション）の 2 箇所。
- API キー（`ksk_`・KIRO_API_KEY）は Pro 以上限定。TUI への env 注入は cursor と同じ ps 露出問題（ADR0023 決定5）→ v1 は login-only が無難。
- ACP は**未認証だと JSON-RPC を話さず stderr「You are not logged in」で即終了**（fail-fast、扱いやすい）。

### 2.3 ACP（managed 経路）

`kiro-cli acp [--model <id>] [--effort low..max] [--agent-engine v1|v2|v3] [--trust-all-tools|--trust-tools=…]`、stdio JSON-RPC 2.0（NDJSON）。

- initialize 応答: protocolVersion 1、**loadSession:true**、promptCapabilities.image:true（audio/embeddedContext false）、mcpCapabilities.http:true、agentInfo(name/version)。
- session/new 応答: sessionId（UUID・**Kiro 採番**）＋ modes（kiro_default / kiro_planner / kiro_guide）。→ set_mode で DynamicMode 候補。
- session/prompt: blocking、応答 `{"stopReason":"end_turn"}`。進行中は session/update 通知（agent_message_chunk でストリーミング）。
- **`_kiro.dev/metadata` 通知（毎ターン）: contextUsagePercentage / meteringUsage [{value, unit:"credit"}] / turnDurationMs** → コンテキストバー＋セッション使用量がライブ経路で実現可能（cursor 不可・codex/claude 同等以上）。
- session/load: 過去履歴を agent_message_chunk として**再生**（EventReplay 相当）→ 新プロセスで文脈保持を実測確認。
- 権限: session/request_permission（今回のプローブでは --trust なし TUI 側でのみ発生を確認。ACP 側は Track A2 で allow-all 運転＋防御実装を検証）。

### 2.4 セッションストア（read 正本）

- **v2 ストア: `~/.kiro/sessions/cli/<sid>.jsonl` + `<sid>.json` + `.history` + `.lock`**。**新 TUI と ACP の両方がここへ書く**（読み手を一本化できる。copilot events.jsonl と同じ理想形）。
  - `.jsonl`: `{"version":"v1","kind":"Prompt|AssistantMessage|ToolResults","data":{message_id, content[{kind:text|toolUse|toolResult, data}], meta.timestamp}}`。toolUse は name/input、toolResult は exit_status/stdout/stderr/status まで＝ミラーのツールカードまで描ける。
  - `.json`: session_id / cwd / created_at / updated_at / title / session_state（メタ）。
  - `.lock`: `{"pid","started_at"}` ＝ 所有プロセスの生存マーカー（状態検出の補助に使える）。
- classic（v1）ストア: `~/.local/share/kiro-cli/data.sqlite3` の conversations_v2（key=cwd、value=履歴入り単一 JSON blob）。**headless `--no-interactive` はこちらに書く**（source:"classic"）。非保証内部として v1 実装では読まない（opencode 教訓）。
- `chat --list-sessions -f json`: cwd ごとに sessionId / source("v2"|"classic") / title / updatedAt / messageCount を返す**機械可読の一覧**。`--delete-session <id> [--session-source v1|v2]` あり。

### 2.5 TUI

- 新 TUI（既定）フッタ: `kiro_default · claude-haiku-4.5 · ◔ 3%   <cwd>` ＋ 入力行。**状態は明示テキスト**:
  - idle: 「ask a question or describe a task ↵」
  - working: 「**Kiro is working · Type to steer · Ctrl+S to queue**」（steering・キュー投入がネイティブ）
  - 権限待ち: 「shell requires approval / ❯ Yes, single permission / Trust, always allow in this session / No (Tab to edit)」（↑↓+↵ キー駆動・esc で閉じる）
- ターン毎に「▸ Credits: 0.01 • Time: 1s」を本文に描画。フッタの ◔ n% はコンテキスト使用率（ターン毎更新）。
- `--classic`: プロンプトが `n% > `、スピナー「⠋ Thinking...」、起動時に `Model: … | Plan: KIRO FREE` を表示。フォールバック用に温存可。
- `/usage`: 「Estimated Usage | resets on 2026-08-01 | KIRO FREE / Credits (0.01 of 50 covered in plan)」＝ **agy 型 PTY スクレイプでプラン残量チップが実現可能**（リセット日・プラン名付き）。
- `chat --resume-id <sid>` で TUI resume 実測 PASS（履歴全再描画）。`--resume`（同 cwd 最新）/`--resume-picker` あり。
- hooks（AgentSpawn/UserPromptSubmit/PreToolUse/PostToolUse/**Stop**、グローバルは ~/.kiro/hooks・2.13+）は**ライブ未検証**（Track A で Stop hook のマーカー書込みを検証する。フッタ文字列契約の版ドリフト保険）。

### 2.6 headless / モデル / MCP

- `chat --no-interactive "…"`: exit 0、stdout は ANSI 装飾付きテキスト（`> ` 前置）。**JSON 出力なし**（`-f json` は --list-models/--list-sessions 専用。issue #5423/#9066）。stderr 末尾に「▸ Credits: … • Time: …」。書込みは classic ストア。アシスタントチャット（headlessChat）に使うなら ANSI strip ＋ read-only agent 構成（cursor --mode ask 教訓）が必要 → **ACP 経由の方が筋が良い**（構造化・usage 付き）。
- `chat --list-models -f json`: model_id / context_window_tokens / rate_multiplier / default_model の完全 JSON。**Free プランでも named モデル指定可**（実測ラインナップ: auto(1M ctx) / claude-sonnet-4.5 / claude-sonnet-4 / claude-haiku-4.5(0.4x) / deepseek-3.2 / minimax-m2.5 / minimax-m2.1 / glm-5 / qwen3-coder-next(0.05x)）。`chat --model` / `--effort low..max` フラグ実在（cursor Free の named 不可より好条件）。
- MCP: `kiro-cli mcp add/remove/list/import/status`、設定は `~/.kiro/settings/mcp.json`（グローバル）/ `.kiro/settings/mcp.json`（ワークスペース）。af MCP 注入はここへ。

## 3. 先に固定する契約（提案・未確定）

| 項目 | 提案 | 備考 |
|---|---|---|
| kind スラグ | `kiro` | session.KindKiro |
| 表示名 | label=`Kiro` / displayName=`Kiro` / assistantName=`Kiro` / short=`ki` | 表示順はユーザー決定待ち |
| 色 | AWS系オレンジ〜アンバー帯（--kind-kiro、dark/light、既存8色と非衝突を tokens.css で確定） | copilot 紫衝突の教訓 → 着手前に twin 全ファイル確定 |
| 実行方式 | v1 から Terminal + Managed（per-session child `kiro-cli acp`、cursor/copilot 同型） | resume は session/load 実測合格済み |
| 認証 | v1 login-only（device flow start→poll、stdout スクレイプ）。API キーは Track D | whoami を状態プローブに |
| read 正本 | v2 JSONL（~/.kiro/sessions/cli）一本。SQLite は読まない | TUI/ACP 共用を実測済み |
| モデル | --list-models ライブ取得＋ chat/acp の --model 固定。ACP set_model があるため DynamicModel は A2 で判定 | effort も両経路にあり |
| 使用量 | セッション使用量= _kiro.dev/metadata（managed）／プラン残量チップ= /usage PTY スクレイプ（Track D） | cursor と違い v1 から現実的 |
| 配備 | manifest sha256 ピン焼込み（agy 型）＋ app.disableAutoupdates を entrypoint 起動毎再固定。arm64 は musl 変種 | **855MB → lean/boot-install 側へ寄せる判断が必要（未決）** |

## 4. 決定事項（2026-07-24 ユーザー決定・ADR0026 に転記予定）

1. **色 = 紫**（Kiro が copilot から紫を引き継ぐ）。**copilot は黒/グレー系へ、opencode は現行スレートグレーより薄いグレーへ移動**（ユーザー決定 2026-07-24）。3種同時変更になるため候補値を先置きする:
   - kiro: dark #a371f7 / light #8250df（現 copilot 値を継承。Kiro ブランド紫と整合）
   - copilot: 黒寄りグレー — dark #6b7075（チャコール・dark 背景で視認可） / light #24292f（GitHub 黒）
   - opencode: 薄いグレー — dark #aab4be / light #9aa4ae（copilot のチャコールと明度差で分離）
   最終値は Track C の実描画（両テーマ）で確定。色変更は kind-color-css-checklist の全 twin（tokens/app/terminal/sessions/settings/ui.css × dark/light）を **kiro・copilot・opencode の3種**について総ざらいすること。表示順・アイコン（codicon）は Track C 着手前にユーザー確認。
2. **配備 = 既定はオンデマンド導入・利用ユーザー限定、BAKE_AGENT_CLIS=1 では焼いてよい**（ユーザー決定 2026-07-24: BAKE は利用者が覚悟の上で立てるノブのため kiro も対象に含める）。既定（lean / BAKE=0）ではイメージへ焼かず全ユーザー一律 boot-install もしない。**kiro を使うユーザーの初回利用時に、その ~/.local へ manifest sha256 ピン付きで導入**する新パターン（導線は接続カードの「インストール」or 起動時導入。versions.json にピンは載せる。導入時に app.disableAutoupdates を固定）。855MB がユーザーの home ボリュームに載る旨は UI で明示。
3. **headlessChat = 不要（v1 スコープ外で確定）**。ASSISTANT_AGENT_KINDS / defaultHeadlessOrder に kiro を加えない。**タイトル AI 提案は現行機構のままで動く**: session_title.go は oneShotHeadless（既存の利用可能バックエンド）で生成し、対象セッションの転写は generic read 層から読む＝Track A の転写実装のみが前提。
4. **ToS = 注意事項として記載**（Builder ID free の業務利用可否・組織ポリシー整合は採用組織側の確認事項として docs/ADR に明記）。**開発・検証は Free（Builder ID）で進める**。

## 5. 残る実装時判断（Track A 着手前の3点・実測で決着 2026-07-24）

### 5.1 Stop hook のライブ検証 → **Stop hook は 2.14.1 に存在しない・状態源は TUI 文字列契約に確定**

- 実バイナリ（`kiro-cli-chat`）の hook トリガ enum を strings で確認: **AgentSpawn / PrePrompt / PreToolUse / PostToolUse の4種のみ**。Track 0 で「hooks 公式5種（Stop あり）」としたのは web ドキュメント由来で、**出荷バイナリと不一致**（Stop トリガは無い）。hook config を書いて headless ターンを走らせても Stop 相当のマーカーは発火せず。
- 結論: **claude 型の hook マーカー方式は取り得ない**。状態源は**明示テキスト契約**（`internal/agents/kiro/state.go`）に一本化する:
  - working = 「Kiro is working · Type to steer · Ctrl+S to queue」
  - question = 「shell requires approval」（plan モード等で trust-all を外したとき・**cursor では取れなかった許可待ちが取れる**）
  - idle = 「ask a question or describe a task ↵」
- これは固定の明示句で版ドリフトに強く、false-idle 教訓（スピナーグリフ regex 回避）に整合。文字列が消えたら空を返し、`driveState` の generic 経路（`/input` が積む楽観 working）へフォールバックする。turn 完了（working→idle）はこの poll が唯一の観測点なので `agents.MarkTurnEnd` を発火（docs/30 ②・cursor/copilot と同型）。

### 5.2 `--agent-engine` v2/v3 の扱い → **v2 を明示ピン**

- `--agent-engine` の**既定は現状すでに v2**（`chat --help` 実測。v3 は「Launch the next generation Kiro agent」の opt-in）。read 正本の v2 JSONL ストアは v2 エンジンが書く。
- 決定: 起動コマンドに **`--agent-engine v2` を明示付与**（`program.go` defaultFlags）。既定が将来 v3 へ振れても本実装の read/状態契約（v2 JSONL・TUI 文字列）が崩れないようにするドリフト保険。v3 対応（別ストア/別 UI）は将来 Track。

### 5.3 オンデマンド導入の具体設計（Track B 相当・設計のみ確定／実装は Track B）

Track A では**設定固定の冪等ヘルパ（`ensureSettings`）だけ先行実装**した（素の home でも launch pane が危険モード確認ダイアログで固着しないよう、`app.disableAutoupdates=true` と `chat.disableTrustAllConfirmation=true` をプロセス内 1 回だけ best-effort で書く。`chat.disableTrustAllConfirmation` は「Yes, and don't ask again」が書く設定キーと同一・実測）。導入本体（Track B）の設計:

- **配置**: `workspace-agent install-kiro`（`install-jdk` と同じ workspace-agent サブコマンド流儀）。manifest（`prod.download.cli.kiro.dev/stable/latest/manifest.json`）から版・**sha256**・arch 別 URL を引き、sha256 一致を検証して `~/.local`（home 永続ボリューム・855MB）へ展開。arm64/Debian12 は **musl 変種**（glibc 2.39 要求回避）。
- **導線**: 接続カードの「インストール」ボタン（未導入時に Status().supported=false で出す）または初回起動時の自動導入。`versions.json` にピンを載せる。
- **進捗/失敗**: DL 進捗表示＋sha256 失敗/回線失敗時のリトライ。導入完了時に上記2設定を固定。
- **lean/フル両対応**: `BAKE_AGENT_CLIS=1` では焼き込み（覚悟のノブ・§4-2）。既定 lean では焼かず、kiro 利用ユーザーの初回のみ導入。

## 6. Track A 実装メモ（2026-07-24・temp/snznjpk）

- **パッケージ** `workspace/agent/internal/agents/kiro/`: `kiro.go`（agentImpl・sid 解決）/ `program.go`（起動コマンド・パス・sid 発見・ensureSettings）/ `transcript.go`（v2 JSONL パーサ）/ `state.go`（TUI 文字列分類）/ `auth.go`（Status/LoggedIn＝whoami -f json）/ `models.go`（--list-models -f json）/ `kiro_test.go` ＋ `live_test.go`（KIRO_LIVE=1 ゲート）。
- **セッション同一性（cursor との最大差）**: kiro は**セッション ID を CLI が採番**し、自己採番 `--resume-id` を渡しても採用されない（実測: 独自 ID を切る）。よって**起動後に `~/.kiro/sessions/cli/<sid>.json`（cwd 記録付き）を cwd＋mtime で発見**し sidstore にキャッシュ（codex rollout 発見と同型）。BuildLaunch はキャッシュ済み sid のみ resume に使う（fresh 枠が同一 cwd の無関係セッションを掴まない）。同一 cwd 複数枠は既知の縁（worktree は別 dir なので実運用で問題化しない）。
- **read 正本**: v2 JSONL（Prompt/AssistantMessage/ToolResults）。`ToolResults` を `toolUseId` で対応 tool パートに突合し**ツール出力（stdout）まで描ける**（cursor では不可だった）。ハードキル後の `--resume-id` 復帰も実測 PASS（`.lock` は終了で消え、resume は履歴再生）→ GracefulStop 不要。
- **root 配線**: `session.go`（KindKiro）/ `agent.go`（registry＋driveState 分岐）/ `connections.go`（Status）/ `agent_models.go`（Models）/ `fs.go` denylist（`.kiro` ＋ `.local/share/kiro-cli`）/ `session_io.go`（paneMode readiness・bracketed paste・readiness 待ち）。
- **検証**: `go build ./...`／全 test 緑（kiro 8 件＋main/session 既存）。**ライブ E2E（KIRO_LIVE=1）**= 実 TUI 起動→idle 描画→プロンプト→**cwd 発見→転写パース（user＋tool 出力 attach 確認）**→working→idle 状態遷移まで実測 PASS（sid=40a4893f・turns=2・sawUser/sawToolOut=true）。models 8 件・whoami connected も実測。

## 7. Track B 実装メモ（2026-07-24・temp/snznjpk）— 配備（オンデマンド導入＋焼き込みノブ）

§4-2 決定（既定オンデマンド・利用ユーザー限定／BAKE_AGENT_CLIS=1 でのみ焼く）を、他 CLI と非対称な「巨大・per-user」配備として実装。

- **manifest 実測**（`prod.download.cli.kiro.dev/stable/latest/manifest.json`・2.14.1）: linux zip の sha256 を確定。**x86_64=gnu**（`kirocli-x86_64-linux.zip`・`2e354160…`）、**aarch64=musl**（`kirocli-aarch64-linux-musl.zip`・`4a1acf14…`）。zip 内レイアウト= `kirocli/{BUILD-INFO,bin/{kiro-cli,kiro-cli-chat,kiro-cli-term,q,qchat},install.sh,README}`。install.sh は 3 バイナリのみ設置（q/qchat シムは触らない）＋ gnu ビルドのみ glibc 下限ガード（x86_64=2.34・aarch64=2.39／musl は無ガード）＝Debian 12(2.36) では x64 gnu が通り arm64 は musl 必須が裏取れる。
- **Dockerfile**（`workspace/Dockerfile`・BAKE_AGENT_CLIS=1 ゲート）: `ARG KIRO_VERSION` ＋両 arch sha256。版付き URL DL→sha256 検証→unzip→`Q_INSTALL_GLOBAL=1 Q_SKIP_SETUP=1 sh install.sh`（/usr/local/bin へ 3 バイナリ・setup/integrations 無し）→`kiro-cli --version`。versions.json に `kiro` ＋ arch 依存 `kiro_sha256`（agy/cursor と同型・BAKE ノブに関わらず常に書く）を追加。自己更新封殺は build ENV ノブが無い（copilot と違う）ため焼かず runtime に寄せる。
- **オンデマンド導入（新パターン）**: `workspace-agent install-kiro`（`workspace/agent/install_kiro.go`）。他 CLI と違い**全ユーザー一律 boot-install しない**（~855MB）。versions.json の `kiro`＋`kiro_sha256` ピンで版付き zip を DL→sha256 検証→unzip→**展開バイナリを直接 `~/.local/bin` へ atomic rename 設置**→導入直後に `app.disableAutoupdates`＋`chat.disableTrustAllConfirmation` を固定（`pinKiroSettings`）。冪等（PATH/`~/.local/bin` に居れば設定固定のみで即 return）。
  - **クラッシュ安全（レビュー B-1 修正）**: install の最も自然な中断は「遅い初回起動に待ちきれずセッション停止＝tmux ペイン kill」。install.sh のその場コピーは非原子的で、cp 途中死→実行ビット付き半端 kiro-cli が home 永続ボリュームに残り、以後 `command -v`／冪等チェック双方が「導入済み」誤認→恒久ブリック（自己修復経路なし）。対策=(1)**home ボリューム上の決定的 staging** に展開（`~/.local/share/agent-fleet/kiro-install`・同一 fs なので rename が atomic）、(2)展開 kiro-cli を**設置前に `--version` サニティチェック**（Dockerfile bake の健全性ゲートと対称・truncated/arch 不一致を昇格させない）、(3)`~/.local/bin` へは chat/term を先に、**presence marker の kiro-cli を最後に** rename（最終 rename 前の kill は kiro-cli 不在→次回起動で再導入＝自己修復）、(4)staging は毎回冒頭で wipe＋defer 削除（/tmp に ~1.4GB 残す旧実装を廃し、残骸を in-flight 1 件分に限定）。move 設置で 855MB の二重コピーも回避。
  - **クロスプロセス排他（レビュー B-2 修正）**: `installKiro` 冒頭で **flock**（`~/.local/share/agent-fleet/.kiro-install.lock`・LOCK_EX、NB 試行→占有中は「waiting」表示して blocking）。CLI 起動ガード（別プロセス `workspace-agent install-kiro`）と HTTP 経路（`kiro_install_http.go`）の**両方が `installKiro()` を通る**ため flock が自動共有。2 ペイン同時起動でも 554MB×2 DL／同一 `~/.local/bin` 同時書き込み／実行中バイナリへの ETXTBSY を防ぐ（待機側は起床時に kiro-cli を検出して skip）。
  - **初回起動時の自動導入（導線）**: kiro の launch program（`kiro/program.go buildProgram`）先頭に `command -v kiro-cli || workspace-agent install-kiro;` ガードを前置（tmux は /bin/sh でペイン実行）。**未導入ユーザーが kiro セッションを起動した初回だけ**、ペインに DL 進捗を出して導入→そのまま `kiro-cli chat …` へ。焼き込み/導入済みなら `command -v` の no-op。`AGENT_KIRO_BIN` override（テスト/別パス）時はガードを付けない。**ensureSettings（BuildLaunch）は初回起動時点でバイナリ不在→no-op になるため、設定固定は install-kiro 側が担うのが必須**（sync.Once で agent プロセス内は再発火しないため）。接続カードの「インストール」ボタン（Track C）も同 `install-kiro` を叩く。
- **entrypoint.sh**: kiro は lean 一律 boot-install ループに**入れない**。代わりに「kiro-cli が居れば（焼き込み/home 導入済みの双方）毎起動 `app.disableAutoupdates`＋`chat.disableTrustAllConfirmation` を再固定」する小ブロックのみ（未導入なら無音スキップ）。
- **env_tool_versions.go**: `{Name:"kiro", Cmd:"kiro-cli", Baked:"/usr/local/bin/kiro-cli", Pin:"kiro"}` を追加（未導入なら effective/baked とも null＝「未導入」がそのまま3版表示に出る）。
- **e2e-smoke.sh**: baked（EXPECT_AGENT_CLIS=1）で `check_ver kiro`／lean で不在ループに `kiro-cli` 追加／versions.json の `kiro` ピン＋arch 依存 `kiro_sha256`（cursor/agy と同じ arch 分岐）を検証。
- **検証**: `go build ./...`／`go vet`／`gofmt` 緑。全モジュール test 622 件緑（kiro パッケージ＋新規 `install_kiro_test.go`＝kiroAsset arch マッピング／present 時の冪等 skip／flock 排他 acquire→NB 失敗→release→再取得）。`bash -n e2e-smoke.sh`／`sh -n entrypoint.sh` 緑。manifest 到達・sha256・zip レイアウト・install.sh 挙動は実測で裏取り済み（855MB 実 DL は未実施＝焼き込み/実導入の通し目視は実フリート再ビルド後）。残=Track A2（managed）/C（CP+Console・色3種同時変更）/D＋ADR0026 起票／実導入の実機目視。

## 8. Track C 実装メモ（2026-07-24・temp/snznjpk）— CP＋Console 配線＋色3種同時変更

§4-1（色）・§3 の契約を CP／Console へ配線。cursor Track C（docs/40）を雛形に、kiro は
**Terminal 専用**（managed/ACP driver は A2 送り）である点だけ非対称に扱う。

- **両 routes.go**（cp-rest-proxy-allowlist 教訓＝CP は明示許可リスト）: workspace/agent と
  control-plane の**両方**に device-flow ログイン `POST /connections/kiro/{start,poll}`＋
  `DELETE /connections/kiro`、およびオンデマンド導入 `POST|GET /connections/kiro/install` を登録。
- **auth.go（login フロー）**: `kiro-cli login --license free --use-device-flow` を PTY 起動して
  検証 URL（user_code 埋め込み済み）＋確認コードをスクレイプ、`whoami -f json` を uncached で
  ポーリング（codex/cursor と同じ start→poll・コード貼付なし）。切断は `kiro-cli logout`。
- **オンデマンド導入の HTTP 面（kiro_install_http.go・新）**: lean は ~855MB を焼かないため、
  接続カードの「インストール」ボタンが `POST /connections/kiro/install` を叩く。背景 goroutine で
  Track B の `installKiro()` を回し、`{state: idle|installing|done|error}` を GET でポーリング公開。
  完了で次の /connections poll が supported=true を返しログインへ遷移（起動ガードは available が
  connected を要求するため lean では install ボタンが唯一のブートストラップ導線）。`kiro.Installed()` 追加。
- **MCP kind enum 両総ざらい**（mcp_stdio.go＋CP mcp.go）: list_models whitelist／create_session
  kind enum／list_my_sessions／get_session_usage（agy・cursor と同じく「転写にトークン無し＝
  context 空・cumulative 0」）／get_agent_usage（「使用量ソース無し」側）に kiro を追加。
  **create_session の driver=managed 分岐には kiro を入れない**（Terminal 専用）。bridge/format.go
  kindLabel に "Kiro"。enkana_dict.go（TTS 読み）に `kiro→キロ`。
- **types/session.ts**: SessionKind union に "kiro"、SESSION_KINDS を copilot の後に挿入、
  ProviderConn `kiro?`。
- **registry.ts descriptor**（caps 全項目を根拠明示）: icon=`compass`（ユーザー確認済み・spec/guide 志向）、
  label=Kiro/displayName=Kiro/short=ki/launchSuffix=-ki。**managedDriver:false**（A2 未）、
  caps= chat/transcript/model/tuiStartMode/runsInDir/launchableFromRepo。effort/tuiEffort=false
  （--effort はあるがカタログに effort メタ無し・per-model 未検証＝picker 出さない）、contextBar=false
  （転写にトークン無し・ライブ使用量 _kiro.dev/metadata は A2）、planMode=false（3 モード循環で
  クリーン二値でない・cursor 同型）、imagePaste=false（未配線）、headlessChat=false（§4-3）。
  available= supported!==false && connected。repoLaunchKinds に kiro。表示順=copilot の後（ユーザー確認済み）。
- **色3種同時変更**（tokens.css dark/light＋色クラス twin 6 ファイル: app/terminal/sessions/settings/ui.css）:
  **kiro=紫**（dark #a371f7 / light #8250df＝旧 copilot 値を継承）、**copilot=中立チャコール**
  （dark #7d8590 / light #30363d）、**opencode=薄いスレートグレー**（dark #aab4be / light #6e7781）。
  **最終値は両テーマの実描画（headless chromium スウォッチ）で確定**——方針の候補（copilot dark
  #6b7075／light #24292f、opencode light #9aa4ae）は暗背景のチャコール低コントラスト／白背景の
  淡グレー低コントラストで視認性が落ちたため、階層（copilot=濃いめ・opencode=薄め）を保ったまま
  可読値へ寄せた。両テーマで 9 色すべて非衝突・kiro 紫が明瞭。settings チップ（pb-kiro）含め twin 総ざらい。
- **AgentsTab KiroCard**: 未導入＝install ボタン＋進捗（855MB 注記）、導入済み未認証＝device-flow
  ログイン（URL＋コード表示・DeviceSteps）、認証済み＝email＋切断。LaunchDefaults kind union に kiro。
  ScheduleDetailModal の AGENT_KINDS に kiro（定時実行の kind ピッカー）。i18n ja/en（kiro_* 12 キー＋
  launch_hint.kiro）。
- **検証**: workspace/agent `go build`／`go vet`／kiro+bridge test 73 緑。control-plane
  `go build`／`go vet`／test 233 緑。Console typecheck／i18n:lint（裸和文ゼロ）／vitest 413／
  vite build 緑。色は headless chromium スウォッチで両テーマ実描画確認済み。残=Track A2（managed
  driver）／D＋ADR0026 起票／実フリート再ビルド後の実機目視（実 device-flow ログイン・855MB 実導入）。

## 9. Track A2 実装メモ（2026-07-24・temp/kiro-track-a2）— managed driver（`kiro-cli acp`）

§1 の「managed 合格」を per-session child の ACP driver として実装。cursor Track A2（docs/40）を
雛形にしつつ、着工前に**実 CLI（2.14.1）で ACP 契約を再プローブ**して kiro 固有の 2 差分を確定した。

- **着工前プローブで確定した契約**（実 `kiro-cli acp` に生 JSON-RPC を流して実測）:
  - initialize→`agentCapabilities.loadSession:true`／session/new→`sessionId`（CLI 採番）＋
    `modes{currentModeId:"kiro_default", availableModes:[kiro_default/kiro_planner/kiro_guide]}`＋
    `models{currentModelId:"auto", availableModels:[…]}`。
  - session/update は **ACP 標準の判別子**（`agent_message_chunk` 等）で cursor と同型。加えて
    `_kiro.dev/*` 名前空間の独自**通知**（`metadata`／`subagent/list_update`／`commands/available`／
    `session/update=retry_warning`）を流す＝すべて id 無しなので onNotify で受けて未使用は捨てる。
  - **`.lock` によるクロスプロセス排他**（cursor に無い）: `~/.kiro/sessions/cli/<sid>.lock`（pid 入り）を
    握った所有プロセスが生きている間、別プロセスの session/load は「Session is active in another
    process (PID …)」で拒否（-32603）。**停止は stdin を閉じて EOF で正規終了**させると kiro-cli acp が
    exit 0 ＋ .lock 除去（実測）→後続 resume の lock 競合が消える。ハードキル時は pid 死亡で解放。
  - **session/load は履歴を `user_message_chunk`＋`agent_message_chunk` として再生**（実測）＝cursor と
    同じリプレイ経路でミラーを再構築できる。**クロスプロセス文脈保持も実測 PASS**（codeword 再答）。
  - session/prompt は blocking＝`{stopReason:"end_turn"}`（cancelled/refusal も観測）。
- **driver.go / acp.go / mirror.go**: cursor 骨格をそのまま（turn 状態機械・permission→Interaction・
  reconciliation・ledger 冪等化・per-session child ＋ Setpgid）流用。kiro 固有:
  1. **spawn** = `kiro-cli acp --agent-engine v2 --trust-all-tools [--model …] [--effort …]`（plan は
     `--trust-all-tools` を外し session/set_mode `kiro_planner`。承認は onServerRequest が拾う）。
     モード語彙は `kiro_default`/`kiro_planner`（`kiro_guide` は normal 扱い）。
  2. **停止**（stopChild）= stdin を閉じて .lock を綺麗に手放させ、EOF 無視の安全網として
     プロセスグループへ SIGTERM→SIGKILL。**resume（loadWithLockRetry）は「active in another
     process」を検出して旧所有者の消滅を数回リトライで待つ**（非 lock エラーは即 session/new へ）。
  3. **転写**（mirror.go transcriptBuf）= 生きた handle は session/update のメモリ構築でライブ配信。
     **kiro は ACP 転写を v2 JSONL にも persist する**（cursor は書かない）ので、停止して handle が
     無いときは transcript.go の `fileTranscript` がその JSONL を読む（cursor は停止中は空ミラーだった
     ——kiro は停止中でも履歴を出せる）。managedTranscript が両者を切替える。
  4. **Capabilities** = ProcessModel:per-session-child／Steer（driver 内キュー）／Questions のみ。
     Dynamic* は全 false（モデル/effort/モードは起動フラグ固定・registry も UI を出さない）＝
     UpdateSettings は稼働中変更を明示エラーで拒否。
- **除外指定の managed 対応化**（Track C は「managed 系に kiro を入れない」としていた箇所を A2 で反転。
  cursor 参照サイトを grep 総ざらいして採否判定）: `session_turn.go` managedDrivers／`session_handlers.go`
  の 4 switch（ManagedAlive/Busy/DropHandle/RemoveLedger）／`main.go` ReconcileManaged／`shutdown.go`
  AbortManaged＋Shutdown／`mcp_stdio.go` create_session の driver=managed 既定／CP `scheduler_wake.go`
  injectDriver＋CP `mcp.go` create_session 既定＋desc に kiro を追加。**Console `registry.ts` は
  `managedDriver:true` へ反転**（起動 UI に Terminal/Managed の選択が出る）。**headlessChat 系
  （chat_providers.go／chat_provider_context.go）には引き続き kiro を入れない**（§4-3 で確定）。
- **ライブ使用量（`_kiro.dev/metadata` の contextUsagePercentage/credits）は v1 A2 では UI 未配線**。
  取得は onNotify で拾える seam にあるが、コンテキストバー表示は percentage↔token モデルの整合＋
  registry contextBar／get_session_usage の配線（A2 のファイルスコープ外）が要るため将来 Track 送り。
- **検証**: workspace/agent `go build`／`go vet`／gofmt／kiro test 16（内訳: fake-ACP turn 状態機械・
  permission 往復・session/update→転写構築・モード/lock 純関数）緑。control-plane `go build`／`go vet`／
  test 緑（既存の events SSE テスト 1 件は timing flaky＝単独再実行で緑・本変更と無関係）。Console
  typecheck／i18n:lint（裸和文ゼロ）／vitest 413／vite build 緑。**実バイナリ契約テスト
  （`KIRO_LIVE=1 TestLiveManagedSpawnPromptResume`）= 実 `kiro-cli acp` で spawn→prompt(completed)→
  in-memory 転写＋v2 JSONL persist→stdin EOF 正規終了（.lock 解放）→別プロセス session/load 再生→
  文脈保持まで実測 PASS（16s）**。残=Track D／ADR0026 起票／実フリート再ビルド後の実機目視。

## 10. Track D 実装メモ（2026-07-24・temp/kiro-track-d）— ライブ使用量の UI 配線

Track A2 で「取得 seam はあるが %↔token 変換モデル＋registry contextBar 配線が要るため A2 ファイル
スコープ外」として送りにした、**`_kiro.dev/metadata` のライブ使用量（contextUsagePercentage・
meteringUsage credits）を UI へ配線**する。着工前に実 `kiro-cli acp` 2.14.1 に生 JSON-RPC を流して
metadata の正確な shape を再プローブし確定した。

- **metadata の実測 shape（プローブ再確認）**: `_kiro.dev/metadata` 通知 =
  `{sessionId, contextUsagePercentage: float(0–100), meteringUsage:[{value, unit:"credit", unitPlural}], turnDurationMs}`。
  重要な観測: ①**contextUsagePercentage は 0–100 スケール**（例 3.39=3.39%・TUI の「◔ 3%」と一致）。
  ②**ターン内で複数回流れ、値は変動する**（3.39→1.23→1.23＝縮小もあり得る）ので**最新値を保持**（max ではない）。
  ③**meteringUsage はターン終了時のみ付く**（当該ターンの credit 消費・pct 単独の通知もある）。
- **%→token 変換モデル（この Track の肝）**: 既存の ContextBar / get_session_usage は**転写の per-turn
  トークン数ベース**（read/create/fresh を window に対して描く）だが、kiro の v2 JSONL 転写にはトークンが
  無く、metadata は % を直接くれる。そこで **% をモデルの実 context window（`--list-models` の
  `context_window_tokens`・auto=1M）に対するトークン数へ変換**して単一セグメントとして載せる。**window を
  明示で渡す**ため、フロントが tokens/window から % を再計算しても**元の % に厳密一致**する（丸め誤差のみ・
  window の推定精度に依存しない）。この経路は agy の ContextReporter（/context PTY スクレイプ）と同じ
  セッションレベル fallback だが、kiro は値が生きた ACP handle にインメモリで載っているので**サブプロセス
  不要・非ブロッキング**。
- **実装**:
  - `kiro/driver.go`: threadHandle に `usageMu`/`ctxWindow`/`ctxPct`/`credits`/`hasUsage` を追加。`onNotify` の
    冒頭で `_kiro.dev/metadata` を分岐し `onMetadata` へ（contextUsagePercentage=最新値・meteringUsage credit=
    累積・pointer フィールドで片方欠落に耐える）。spawn で currentModelId → `ModelWindow` を確定（分母）。
    package-level `ManagedContext(name)→(pct,window,credits,model,ok)` を追加（生きた handle かつ metadata 受信済み
    のみ ok・停止中/TUI/未受信は ok=false＝正直に非表示）。
  - `kiro/models.go`: `--list-models` パースを `context_window_tokens` 込みに拡張し、**auto を含む**全モデルの
    id→window マップと `ModelWindow(id)` を追加（picker リストは従来どおり auto を除外）。
  - `kiro/context.go`（新）: `agents.ContextReporter.ContextFill` を実装（ManagedContext → pct×window の tokens を
    `transcript.Context{Tokens,Window}` で返す）。**フロントは無改修**——ミラーの既存 agy fallback 経路
    （`/messages` の `d.context` → `agentCtx` → ContextBar・window 明示）がそのまま描く。**managed(paneless)
    セッションはミラーが唯一のビュー**なのでここが主表示。
  - `session_usage.go`: `get_session_usage` に `overlayKiroLiveUsage` を追加（kiro のみ、稼働中 managed の
    ManagedContext から context{pct,tokens,window,model}＋`cumulative.credits` を上書き）。`cumulativeUsage` に
    `Credits float64 json:"credits,omitempty"` を追加（トークンではないので Spend に畳まず併置）。
  - MCP ツール説明（`mcp_stdio.go`／CP `mcp.go` の get_session_usage）: 「kiro は context 空・cumulative 0」を
    「稼働中 managed は metadata のライブ context（pct＋概算 tokens）＋credits を返す・停止中/TUI は空」に更新。
  - Console `registry.ts`: kiro caps に `contextBar: true`（ContextFill 経由でミラーの ContextBar が点灯）。
- **不採用/送り（Track D の判断）**:
  - **アシスタントチャット（headlessChat）は §4-3 決定どおり不採用のまま**（ASSISTANT_AGENT_KINDS に kiro を
    加えない）。タイトル AI 提案は generic read 層で既に動く。Track D で再検討したが変更なし。
  - **API キー（`ksk_`/KIRO_API_KEY）認証は v1 不採用・login-only 継続**。cursor ADR0023 決定5 と同じ理由＝
    TUI(tmux ペイン)への env 注入は `ps` 露出（平文資格禁止に抵触）で、device-flow login（ambient 資格）が
    TUI/managed/status/models 全経路を env 注入ゼロで賄うため。
  - **プラン残量チップ（/usage PTY スクレイプ → get_agent_usage）は本 Track では見送り**。機械可読手段が無く
    （issue #7752）agy 型 PTY スクレイプ harness が要る一方、本 Track のセッション使用量（context%＋credits）で
    「どれだけ使ったか」は賄える。cursor ADR0023 決定7 と同じく WS バー常駐チップは非公式 API/スクレイプの
    脆さ（[[usage-chip-statusline]] の 429 事件）を避け、必要になれば別途起票する。
- **検証**: workspace/agent `go build`／`go vet`／gofmt／全 test 緑（kiro に `TestManagedContextFromMetadata`＝
  pct 最新値保持・credit 累積・pct→token 厳密往復・停止 handle で ok=false を追加）。control-plane
  `go build`／`go vet`／test233 緑。Console typecheck／i18n:lint（裸和文ゼロ）／vitest417／vite build 緑。
  **実バイナリ E2E（`KIRO_LIVE=1 TestLiveManagedSpawnPromptResume` に Track D assertion 追加）= 実
  `kiro-cli acp` で 1 ターン完了後 `ManagedContext` が `pct=1.23% window=1000000(auto=1M・カタログ由来)
  credits=0.0295 model=auto` を返し ContextFill が非 nil を実測 PASS（19s）**。残=実フリート再ビルド後の実機
  目視（ミラーの ContextBar 描画・複数ターンでの pct 推移）。

## 11. ピン追従の修正（2026-07-25・temp/syzjob2）— 導入済み kiro が新ピンへ更新されない

**症状**（設定 > ツールのバージョン表）: `kiro` が 実効 2.14.1 / イメージ (2.14.2) / ~/.local 2.14.1。
イメージのピンを 2.14.2 に上げて再ビルドしても、既に導入済みユーザーの kiro が永久に 2.14.1 のまま。

**真因**: kiro だけ「ピンを前進させる経路が存在しなかった」。他 CLI と違い、
(1) `~/.local` の実体は home ボリュームなのでイメージ再ビルドでは replace されず、
(2) 855MB のため lean の boot-install ループにも入れておらず（§7）、
(3) 自己更新（`app.disableAutoupdates`）は entrypoint／`pinKiroSettings`／`ensureSettings` の三重で封殺しており、
(4) 唯一の導入経路である起動ガードが `command -v kiro-cli >/dev/null 2>&1 || workspace-agent install-kiro`
＝**「不在」しか見ない**（`installKiro` も冒頭で presence 判定だけして即 return）。
結果、最初に入った版が終着点になる。self-update opt-in（ON）も kiro を対象にしていない（npm4種/agy/rtk/cursor のみ）ので救われない。

**修正**（`install_kiro.go` / `kiro/program.go` / `kiro_install_http.go`）:
- **判定を presence から「ピンとの一致」へ**（`kiroCheck` → `kiroMissing`/`kiroCurrent`/`kiroStale`/`kiroUnknownVer`）。
  導入版 ≠ versions.json ピンなら再導入（**上げも下げも**＝ピンが「検証済みの版」という契約側が正）。
  - `kiroUnknownVer`（`--version` が版を返さない）は**据え置き＋WARN**。554MB の再 DL を毎起動空回りさせない。
  - ピンが読めない（versions.json 無しの手組みイメージ）時は比較対象が無いので presence 時代の挙動へ縮退（触らない）。
- **起動ガードを無条件化**: `workspace-agent install-kiro --if-needed; kiro-cli chat …`。
  `--if-needed` は「一致なら**完全に無言**で exit 0」（ペインに 1 行も出さない＝TUI 文字列契約に触らない）、
  ずれていればこれまで通りペインに DL 進捗を出して再導入。`;` 連結なので失敗しても既存版で起動は続く。
- **毎起動コストをゼロ近くに**: 導入時に `~/.local/bin/.kiro.version` マーカーを書き（agy の `.agy.version` と同型）、
  一致判定は **stat だけ**で済ませる。マーカーが無い／ずれている時のみ実バイナリを `--version` で probe するので、
  マーカーの陳腐化（本修正以前の導入・焼き込み `/usr/local` 版）で誤 DL することはない。
  `pinKiroSettings` も `~/.kiro/settings/cli.json` に両キー true が既にあれば skip（855MB バイナリの exec×2 を毎起動やらない）。
- **上書き設置の安全性**: 更新時の rename は既存ファイルへ着地するが、rename はディレクトリエントリの差し替えなので
  **旧版で走行中の kiro セッションは旧 inode を掴んだまま無傷**（in-place 書き込みと違い ETXTBSY も無い）。
  中断耐性は §7 の設計そのまま（kiro-cli を最後に rename／マーカーは設置前に削除するので半端な「新版です」表示は残らない）。
  ピーク disk は「旧設置分＋新展開分」になる。
- **HTTP 経路**（接続カードの「インストール」）も presence 判定 → ピン一致判定へ（`kiroInstallCurrent`）。stale は "done" と答えず再導入に回る。

**検証**: `go build ./...`／`go vet` 緑、workspace/agent test **668 件緑**（新規=ピンドリフト検出／マーカー fast path ／
unknown 版据え置き／ピン欠落時の縮退／起動ガード文字列）。実物での再 DL（zip 554MB・展開後 855MB）通しは未実施（実フリート再ビルド後の実機目視が残）。

**未修正の同型リスク（別件・要判断）**: lean 配布の boot-install（entrypoint）も `cli_present` の presence 判定なので、
self-update OFF のまま npm4種/rtk/agy/cursor のピンを上げても `~/.local` の boot-install 品は前進しない。
kiro と違い ON にすれば latest へ追従するため露見しにくいが、構造は同じ。

### 11.1 更新 UI（接続カード・2026-07-25）

自動追従（起動ガード）だけだと「気づかないうちに起動が数分止まる」ので、**任意のタイミングで押せるボタン**を接続カードに足した。

- **導入と認証の関係（前提の再確認）**: 認証時に導入は走らない。`kiro.Status()` はバイナリ不在で `supported=false`＝
  Console 上は「未導入」表示で、ログイン（`kiro-cli login`）自体がバイナリを要求する。したがって初回導線は
  **設定 > エージェント > Kiro >「Kiro を導入」→ サインイン（device flow）→ 起動** の順。`available` が connected ゲートのため
  未導入のうちは起動モーダルに kiro が出ず、起動ガードの自動導入は「home を掃除した」等で消えた時の保険という位置づけ。
- **GET `/connections/kiro/install` を拡張**（新ルート追加ではない＝CP 許可リスト変更不要）: `{state, error}` に
  `installed / version / pin / updateAvailable` を追加。`updateAvailable` は `kiroStale` の時だけ true
  （版が読めない・ピンが無い時は false ＝ 憶測で 554MB を促さない）。install 実行中は版の読み取り自体を省く（tree が入れ替え中）。
- **POST は据え置き**（stale なら再導入に回る＝§11）。カードは install と同じ state machine をポーリングする。
- **Console（AgentsTab の KiroCard）**: 接続の有無に関わらず（＝バイナリの話であって auth の話ではない）
  「新しい版が利用できます（導入済み {cur} → ピン {pin}）」＋「Kiro を更新」ボタンを表示。更新中は
  「実行中の Kiro セッションはそのまま動き続けます（次回起動から新しい版）」と明示（rename 差し替えの実挙動どおり）。
  i18n は ja/en 4 キー（`agents.kiro_update_avail` / `_update` / `_update_note` / `_updating`）。
- **検証**: workspace/agent test 669 緑（GET ペイロードのドリフト報告テスト追加）。Console typecheck／i18n:lint／vitest 429 緑。
  実描画（カードに更新ボタンが出る／押して 855MB 更新が通る）は実フリート再ビルド後の目視が残る。
