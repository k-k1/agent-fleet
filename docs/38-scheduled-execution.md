# 38. 定時実行（スケジュール実行）— オペレーターが仕込む cron 型フリート駆動

- 状態: **設計中（本ドキュメントが正本）**。2026-07-22 起票。実装は未着手。
  採用判断は [decisions/0021](decisions/0021-scheduled-execution.md)。
- ゴール: フリート・オペレーター（`af_write` アシスタント）が「毎朝9時にこのプロンプトで
  claude を回す」「6時間おきに PR をレビューさせる」といった**定時タスクを会話から仕込める**
  ようにする。到来時刻に、必要なら停止中の WS を起こしてセッションを実行し、結果を
  オペレーター会話へ自動報告する。
- 非ゴール（v1）:
  - 分未満の高頻度実行 / リアルタイムトリガ（イベント駆動は docs/37 チャットブリッジの領分）。
  - 複数ステップのワークフロー DAG（1 スケジュール＝1 プロンプト投入まで）。
  - Console からの GUI 作成（v1 はオペレーター MCP が正、UI は後続フェーズ）。

## 背景 / 何が欠けているか（調査結論）

「特定ユーザーの特定セッションを、指定時刻に叩く」土台は**現状ゼロ**である。

- 既存の定期処理は CP の**デプロイ全体で 1 個のシングルトン goroutine** だけ
  （idle-reaper=`control-plane/reaper.go`、usage sampler=`usage.go`、git GC=`git_gc.go`、
  配線は `control-plane/main.go:198-233`）。**時刻起点で「起こす／実行する」機構は無い**
  （reaper は「止める」専用）。スケジュール定義を持つ DB テーブルも無い。
- **WS 停止中はコンテナ内の workspace-agent プロセスごと消える**。セッション実行・通知生成・
  オペレーター報告・自動ターン・チャットブリッジ送受信は**すべて workspace-agent プロセス内**
  （`workspace/agent/…`）。停止中は誰も実行・配送できない。
- 停止中も生きているのは **CP と home ディスク**だけ。そして **WS を起こせるのは CP の
  `ensureWorkspaceStarted`（`control-plane/workspace_handlers.go:112`）だけ**。ただし現状の
  起動トリガは「メンバー認証つき HTTP アクセス（セッション作成など）」に限られ、
  **時刻起点の自動 wake は存在しない**。

→ **第一原理: 停止中でも定時実行するなら、スケジューラは CP 常駐一択。** 停止中に唯一
生きている CP が「時刻を見張り／WS を起こし／注入する」。

## 決定した既定（2026-07-22）

| 論点 | 既定 | 備考 |
|---|---|---|
| WS 停止中の扱い | **wake（起こして実行）** | スケジュール単位で `skip`/`catch-up` に上書き可 |
| 投入先セッション | **毎回新規セッション**（`create_session`） | 長寿命再利用は将来拡張。掃除は cleanup 機構に乗せる |
| v1 の作り方 | **設計 doc 先行 → 合意 → 実装** | docs/27,30,35 と同じ doc-first の流儀 |

## アーキテクチャ

「実行」と「報告」は既存資産でほぼ賄える。**新規なのは 4 点だけ**:
① CP のスケジューラ goroutine ② スケジュール定義 DB ③ 時刻起点 wake の内部認証経路
④ 操作 MCP ツール群。

```
[オペレーター会話(af_write)]
   │ schedule_create/list/update/delete            ← ④ 新規 MCP
   ▼
[workspace-agent mcp_stdio.go] ──(Agent REST)──▶ [CP internal /internal/schedules]  ← ③ 経路(memoブリッジ流用)
                                                        │
                                                        ▼
                                                  [CP DB: schedules 表]  ← ② 新規
                                                        ▲
   ┌────────────────────────────────────────────────────┘
   │ tick(1分)  ← ① 新規スケジューラ goroutine (reaper が手本)
   ▼
 発火: due 判定 → wakeポリシー適用 → ensureWorkspaceStarted → Agent REST で注入
                                                        │ POST /sessions (report_to=オペレーター会話, 冪等キー)
                                                        ▼
                                             [新規セッション実行] ──完了──▶ docs/30 報告 seam
                                                                            (role="report" + 通知 + 自動ターン)
```

### ① 定義の置き場 — CP の DB（新テーブル `schedules`）

停止中に唯一動いているのが CP なので、定義は CP 側に持つ（`~/.config/agent-fleet` 案は
「recreate を生き延びる」利点はあるが、停止中はエージェントが居らず読めないので不採用）。
`control-plane/store_sqlite.go` + migrations に追加。

主なカラム（案）:

| 列 | 意味 |
|---|---|
| `id` | スケジュール ID（`sch_…`） |
| `membership_id` / `tenant_id` | 所有者（`ensureWorkspaceStarted` に要る resolved の素） |
| `owner_conv` | オペレーター会話 id（`report_to` の宛先。docs/30） |
| `spec_kind` | `cron` / `interval` / `once` |
| `spec` | cron 式 or 間隔秒 or 絶対時刻（**評価に使う正本**） |
| `spec_label` | 元の自然言語表現（表示用・任意。評価には使わない） |
| `tz` | ユーザー TZ（cron 評価基準。DST 込み） |
| `wake_policy` | `wake`（既定）/ `skip` / `catch_up` |
| `session_mode` | `new`（既定）/ `reuse`（将来） |
| `reuse_target` | reuse 時の対象セッション名（将来） |
| `agent_kind` / `model` | claude/codex/opencode/copilot・モデル |
| `repo` / `worktree` / `new_branch` | 作業ディレクトリの選択 |
| `prompt` | 投入プロンプト（`{{date}}` 等の**固定メタ変数のみ**置換可・§④''' 参照） |
| `overlap_policy` | `skip`（既定）/ `queue` / `restart` |
| `enabled` | 有効/一時停止 |
| `next_run` / `last_run` / `last_status` | 発火台帳（二重発火防止・catch-up 判定） |

### ② 操作面 — メモキューと同じ「CP に貯めてエージェントから叩く」構造

前例が完全に一致する: **メモキュー**（`add_memo`/`list_memos` …）は CP に保存し、エージェント
から `cpMemoDo`（`workspace/agent/mcp_stdio.go`、`memo_bridge.go` / `AF_MEMO_TOKEN`）で中継
している。定時実行もこれに倣う:

- 新規 MCP ツール（`mcpStdioWriteTools` に追加、`af_write` ゲート）:
  - `create_schedule` / `list_schedules` / `update_schedule` / `delete_schedule`
  - `pause_schedule` / `resume_schedule`（enabled トグル）
  - `run_schedule_now`（手動発火・動作確認用）
  - `get_schedule_runs`（実行履歴＝可観測性）
- ディスパッチは `mcpStdioCall` の case として実装し、`cpScheduleDo`（memoブリッジ同様の
  内部トークン経路）で CP `/internal/schedules*` に中継。
- **REST 二重登録の制約に注意**: 機能させるには (a) CP 側に internal ハンドラ、
  (b) それを叩く Agent 側 MCP、の両方が要る（cp-rest-proxy-allowlist の教訓）。

### ③ 時刻ドライバ + wake

- CP に env-gated シングルトン goroutine を追加（`main.go:198-233` の
  `go newScheduler(deps).run(ctx)` パターン。tick は reaper と同じ 1 分刻みで十分）。
- 各 tick で `next_run <= now` の enabled 行を拾い、**発火**:
  1. `wake_policy` 適用（下記）。
  2. wake が要れば `ensureWorkspaceStarted` を**内部呼び出し**。これが `res *resolved`
     （ws+membership 解決済み）を要求するので、**membership から resolved を内部生成する経路**
     が新規に要る（現状は `withResolved` ミドルウェア経由の HTTP 前提）。
  3. WS ready 後、Agent REST に注入（下記 ④'）。
  4. `last_run`/`next_run`/`last_status` を更新。cron/interval は次回を再計算、once は disable。

### ④'' spec の入力は自然言語も可（**決定 2026-07-22**）

ユーザーは「毎朝9時」「平日の夕方6時」「6時間おき」のように**自然言語で**オペレーターに
頼めるようにする。ただし DB とスケジューラが評価するのは**構造化 spec（cron/interval/once
＋tz）**であって自然言語文字列ではない。実装方針:

- `create_schedule` MCP は **構造化 spec を受ける**（`spec_kind`＋`spec`＋`tz`）。オペレーター
  （LLM）がユーザーの自然言語を cron 等へ**登録時に翻訳**して渡す。生の自然言語を DB に
  貯めて実行時に解釈する方式は取らない（実行時パースの非決定性・沈黙失敗を避ける）。
- 誤解防止に、ツールは登録結果として **解釈した spec と「次回発火＝具体日時」を返す**。
  オペレーターはそれをユーザーに読み上げて確認する（「毎日 09:00 JST に実行、次回は
  7/23 09:00 でよいですか？」）。曖昧語（「朝」等）の既定解釈は persona 側の運用で吸収。
- 元の自然言語表現は `spec_label`（表示用）として任意保存し、一覧・履歴で人に見せる
  （評価には使わない）。

### ④''' プロンプトのテンプレート変数 — 固定メタ変数のみ（**決定 2026-07-22**）

投入プロンプトの `{{...}}` 置換は、**スケジューラが発火時に決定論的に計算する固定メタ
変数のホワイトリストのみ**に限る。ユーザー入力・報告本文・外部内容・前回セッション出力・
git/WS 状態など「データ」は一切運ばない。無人実行では確認する人が居らず、データ搬送を
許すと docs/30 が警戒する「攻撃者影響下データを根拠にした実行」がプロンプト本文経由で
無人フリート駆動へ流れ込む（プロンプトインジェクション面積の拡大）ため。

許可する変数（すべてスケジューラ計算・データ非搬送）:

| 変数 | 展開例 | 由来 |
|---|---|---|
| `{{date}}` | `2026-07-23` | 発火スロットの日付（`tz` 基準） |
| `{{time}}` | `09:00` | 発火スロットの時刻（`tz` 基準） |
| `{{datetime}}` | `2026-07-23 09:00 JST` | 上記の結合 |
| `{{tz}}` | `Asia/Tokyo` | スケジュールの `tz` 列 |
| `{{schedule_id}}` | `sch_ab12` | スケジュール `id` |
| `{{schedule_label}}` | `毎朝9時レビュー` | `spec_label`（登録時の元表現） |
| `{{last_run}}` | `2026-07-22 09:00` | 前回発火時刻（`last_run` 列・時刻のみ。前回**出力**ではない） |

規約:

- **展開は発火時にスケジューラ側で**行い、値は上記台帳列と発火スロットからのみ生成する
  （`create_session` へ渡す `initial_prompt` は展開後の文字列）。
- **未定義の `{{foo}}` はエラーにも展開にもせず、リテラルとして素通し**する（プロンプトに
  たまたま含まれる二重波括弧での誤爆・沈黙失敗を防ぐ）。ホワイトリスト外は決して値を
  埋めない。
- ホワイトリストの拡張（例: `{{repo}}` 等の登録時確定メタ）は将来検討しうるが、**データを
  運ぶ変数（前回出力・git status・報告本文など）は範囲外を維持**する。広げる場合も
  「スケジューラ/登録時に確定する非データ値」の線は越えない。

### ④' 実行注入 — 既存の create_session をそのまま使う

`create_session`（`mcp_stdio.go:537`）は既に **report_to・冪等キー・停止時再開**を持つ。
スケジューラは同じ REST（`POST /sessions`）を、以下を仕込んで叩くだけ:

- `report_to = owner_conv`（オペレーター会話）→ 完了が docs/30 報告 seam に乗る。
- `idempotency_key = f(schedule_id, 発火時刻スロット)` → CP 再起動での二重発火を殺す
  （既存 `session_idempotency.go` の決定論キーに直結）。
- `initial_prompt = テンプレート展開後のプロンプト`、`kind`/`model`/`dir`/`new_branch`。

### ⑤ 完了配送 — docs/30 に全乗り

新規セッションは通常どおり `create_session` で arm され、完了（`answer-ready`）または
異常終了（oom/crashed/killed）で `recordSessionNotification`
（`workspace/agent/session_status.go:103`）→ `POST /chat/report` → role="report" 追記
＋通知センター＋自動ターン（`chat_report.go:247`）へ流れる。**定時実行のための新しい
報告経路は要らない**。オペレーターは「9時のバッチが終わりました」を会話で受け取り、
自動ターン（ユーザー発話なし上限10回）で後続を処理できる。

## 重要な落とし穴（設計で必ず閉じる）

### ★1. reaper との競合（最重要）

起こして注入しても、**スケジュール実行には開いた Console 接続が無い**。reaper
（`reaper.go`）は「long-lived 接続」と last-seen で idle を判定し、コールドな WS を
Tier2 で docker stop する。放置すると **注入直後に WS を止め、セッションを実行途中で殺し、
報告も消える**。

対策: 発火時、スケジューラが reaper に **keep-alive（実行中マーカー）を登録**し、
「対象セッションが idle 到達 ＆ 報告配送完了」まで Tier1/Tier2 のリクレーム対象から外す。
その後スケールダウンを許す。`connRegistry`（`reaper.go:33-122`）に「スケジュール実行中」
という擬似接続を差す実装が素直。

wake ポリシーが `wake` の場合の完了後（**決定 2026-07-22**）: **元が停止中だった WS は、
実行完了（idle 到達＋報告配送完了）後に「少し時間をおいてから」停止に戻す**。settle window
（猶予）を挟むのは、(a) 自動ターン（docs/30・ユーザー発話なし上限10回）や後続報告がまだ
配送されうる、(b) 直後にユーザーが Console を開いて続きを触る可能性がある、ためで、即停止だと
これらを取りこぼす。実装は keep-alive を「idle＋報告完了」で解除するのではなく、そこから
**settle 猶予（例: 数分。定数 or env）を足して**から reaper のスケール停止を許す。元から
running だった WS はスケジュール由来で止めない（reaper の通常タイムアウトに委ねる）。

### ★2. サンダリングハード → ホスト OOM

皆が 09:00 に置くと wake が集中し、**共有ホストが OOM**（既知の実害リスク）。

対策:
- 発火に **jitter**（例: ±数分をスケジュール毎に決定論的に散らす）。
- スケジューラの **wake 同時実行数に上限**、`max_workspaces` クォータ尊重
  （`countRunningInTenant`）。
- 到来が詰まったら**キュー化して順次** wake。

### ★3. 無人失敗の沈黙

無人実行なので、**ログイン切れ（claude/codex 認証失効）や usage 枯渇・レート制限**時に
再ログインする人が居ない → セッションが沈黙してオペレーターに何も返らない
（auth-expiry の既知パターン）。

対策:
- 発火前に `get_agent_usage` 相当でレート制限/解除日時を見て、制限中は skip して
  **「制限中のため見送り」をオペレーターに報告**。
- wake 失敗・注入失敗・認証失効を検知したら**沈黙せず失敗報告**する経路
  （report seam に error 種別を足す）。

### ★4. 二重発火 / CP 再起動

- `next_run`/`last_run` を DB に永続化（in-memory だけだと CP 再起動で消える）。
- 冪等キーを **(schedule_id + 発火時刻スロット)** から決定論生成し、既存 create_session
  冪等機構で二重起動を吸収。
- CP 再起動直後の grace（reaper の `bootTime` と同様の考え方）で、跨いだ発火の
  catch-up 判定を安定させる。

### ★5. overlap / reentrancy

前回実行が走行中に次が来る場合の `overlap_policy`: `skip`（既定・推奨）/ `queue` / `restart`。
reuse セッションでは driver 切替中 `409 busy_switch` にも遭遇しうる。

### その他

- **TZ / DST**: cron はユーザー TZ 基準で評価。夏時間跨ぎの二重/欠落時刻の扱いを規定。
- **頻度下限・本数上限**: 毎秒 cron を禁止（最小間隔を定数化）、テナント毎のスケジュール
  本数上限（乱造・資源枯渇防止）。
- **セキュリティ**: 「停止 WS を起こして無人で agent を回す」は強力プリミティブ。
  **報告本文（攻撃者影響下データ）を根拠にしたスケジュール登録は要ユーザー確認**を
  `operatorPersona`（`assistants.go:150`）に明記（docs/30 の既存ガードの延長）。
  shell セッションを定時で叩く登録も同様に要確認。
- **可観測性**: `get_schedule_runs`（実行履歴）と、後続フェーズで Console にスケジュール
  一覧/次回・前回/有効トグル/履歴ビュー。

## フェーズ

- **P0（本 doc）**: 設計合意・ADR0021。**完了**。
- **P1**: CP スケジューラ骨格（DB 表・tick・`next_run` 台帳・cron/interval/once 評価）。
  **実装済み**（`control-plane/scheduler.go`・`schedules` 表 = migrations/0022＋pg/0005・
  `ScheduleStore`・env ゲート `AF_SCHEDULER_INTERVAL` 既定 OFF）。実 wake+注入は
  `scheduleFirer` シーム越しの P2 に委ね、P1 既定は no-op の `logFirer`（有効化しても
  台帳が進むだけ）。cron は外部依存ゼロの自前評価器（5 フィールド・dom OR dow の
  Vixie ルール・`time/tzdata` 埋込で TZ/DST）。テスト 18 件（DST 両方向含む）緑。
- **P2**: wake 経路（membership→resolved 内部生成・`ensureWorkspaceStarted` 内部呼び）と
  reaper keep-alive（★1）。
- **P3**: 操作 MCP（create/list/update/delete/pause/run_now/get_runs）＋ CP internal 経路。
- **P4**: 無人失敗報告（★3）・jitter/並列上限（★2）・冪等（★4）・persona ガード。
- **P5（後続）**: Console UI（一覧・履歴・トグル）、長寿命セッション再利用モード。

## 決定済み（2026-07-22・当初の未決から確定）

- **wake 完了後の停止方針**: 元が停止中だった WS は **settle 猶予を挟んでから停止に戻す**
  （★1 後段）。元 running はスケジュール由来で止めない。
- **spec 入力 UX**: **自然言語も可**。オペレーターが登録時に構造化 spec へ翻訳し、解釈結果と
  次回発火を返してユーザー確認（④''）。DB 正本は構造化 spec。
- **`run_now`（手動発火）**: **v1 に入れる**（動作確認・即時実行に有用）。wake ポリシー・
  冪等・keep-alive は定時発火と同じ経路を通す（手動でも★の対策を素通りさせない）。
- **テンプレート変数の範囲（注入面積）**: **固定メタ変数のみ**（`{{date}}`/`{{time}}`/
  `{{datetime}}`/`{{tz}}`/`{{schedule_id}}`/`{{schedule_label}}`/`{{last_run}}`）。すべて
  スケジューラが発火時に決定論的に計算する非データ値で、報告本文・前回出力・git/WS 状態など
  「データ」は運ばない。未定義 `{{foo}}` は素通し（§④'''）。無人実行にインジェクション面を
  開かないための線引き。

## 未決事項

（なし。P0 で詰めるべき論点はすべて確定。実装（P1〜）で具体化する事項は各フェーズ参照。）
