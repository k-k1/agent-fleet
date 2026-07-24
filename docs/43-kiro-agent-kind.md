# 43. Kiro CLI エージェント種別（kind=kiro・第8種）— Track 0 実測記録

status: Track 0（着工前プローブ）完了＋方針4点決定（2026-07-24）。Track A 以降は未着手・ADR 未起票（→ 0026 予定）。
関連: docs/40（cursor・章立てのテンプレ）/ docs/36（copilot）/ docs/32（agy）/ decisions/0015（managed driver）。

## 0. 対象と背景

- **Kiro CLI** = 旧 Amazon Q Developer CLI（2025-11-17 改名）。AWS の Kiro（IDE）のターミナル版。
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
| 3段命名 | label=`Kiro` / displayName=`Kiro CLI` / assistantName=`Kiro` / short=`ki` | 表示順はユーザー決定待ち |
| 色 | AWS系オレンジ〜アンバー帯（--kind-kiro、dark/light、既存8色と非衝突を tokens.css で確定） | copilot 紫衝突の教訓 → 着手前に twin 全ファイル確定 |
| 実行方式 | v1 から Terminal + Managed（per-session child `kiro-cli acp`、cursor/copilot 同型） | resume は session/load 実測合格済み |
| 認証 | v1 login-only（device flow start→poll、stdout スクレイプ）。API キーは Track D | whoami を状態プローブに |
| read 正本 | v2 JSONL（~/.kiro/sessions/cli）一本。SQLite は読まない | TUI/ACP 共用を実測済み |
| モデル | --list-models ライブ取得＋ chat/acp の --model 固定。ACP set_model があるため DynamicModel は A2 で判定 | effort も両経路にあり |
| 使用量 | セッション使用量= _kiro.dev/metadata（managed）／プラン残量チップ= /usage PTY スクレイプ（Track D） | cursor と違い v1 から現実的 |
| 配備 | manifest sha256 ピン焼込み（agy 型）＋ app.disableAutoupdates を entrypoint 起動毎再固定。arm64 は musl 変種 | **855MB → lean/boot-install 側へ寄せる判断が必要（未決）** |

## 4. 決定事項（2026-07-24 ユーザー決定・ADR0026 に転記予定）

1. **色 = 紫**（Kiro ブランド）。現行パレットは copilot が紫（dark #a371f7 / light #8250df）を占有しているため **copilot の色を移動する**。空き色相は実質 紅(crimson) 帯のみ（橙=claude/緑=codex/ローズ=cursor/青=agy・ssm/テール=shell/グレー=opencode）。copilot の新色の最終値と Kiro の表示順・アイコン（codicon）は Track C 着手前にユーザー確認。色変更は kind-color-css-checklist の全 twin（tokens/app/terminal/sessions/settings/ui.css × dark/light）を copilot・kiro 両方について総ざらいすること。
2. **配備 = オンデマンド導入・利用ユーザー限定**。イメージへは焼かない（BAKE_AGENT_CLIS=1 でも kiro は対象外）。全ユーザー一律の boot-install もしない。**kiro を使うユーザーの初回利用時に、その ~/.local へ manifest sha256 ピン付きで導入**する新パターン（導線は接続カードの「インストール」or 起動時導入。versions.json にピンは載せる。導入時に app.disableAutoupdates を固定）。855MB がユーザー home 볼륨に載る旨は UI で明示。
3. **headlessChat = 不要（v1 スコープ外で確定）**。ASSISTANT_AGENT_KINDS / defaultHeadlessOrder に kiro を加えない。**タイトル AI 提案は現行機構のままで動く**: session_title.go は oneShotHeadless（既存の利用可能バックエンド）で生成し、対象セッションの転写は generic read 層から読む＝Track A の転写実装のみが前提。
4. **ToS = 注意事項として記載**（Builder ID free の業務利用可否・組織ポリシー整合は採用組織側の確認事項として docs/ADR に明記）。**開発・検証は Free（Builder ID）で進める**。

## 5. 残る実装時判断（次セッション向け）

1. hooks（Stop）のライブ検証と、フッタ文字列契約との両建て構成（状態検出の正の置き方）。
2. `--agent-engine v3` / `kiro-cli --v3` の扱い（v1 は v2 エンジン固定を提案）。
3. オンデマンド導入の具体設計（Track B 相当）: 導入コマンドの置き場（workspace-agent サブコマンド案）・進捗表示・失敗時の再試行・lean/フル両イメージでの挙動。
