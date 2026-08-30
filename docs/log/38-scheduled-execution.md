# 38. 定時実行（スケジュール実行）— オペレーターが仕込む cron 型フリート駆動

- 状態: **実装済（本ドキュメントが正本）**。2026-07-22 起票・設計、以後 v1 コア〜P5 系列まで
  実装（経緯は §フェーズ）。採用判断は [decisions/0021](../decisions/0021-scheduled-execution.md)。
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
| `report` | 完了報告 opt-in。`1` のときだけ発火時に `owner_conv` を `report_to` として渡し docs/30 報告が届く。**既定 `0`＝報告しない**（実行履歴・失敗通知は report と無関係に残る）。assistant 発火では無関係（投入自体が会話に届く） |
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
  ※後日変更: `report=1`（opt-in）のときだけ。既定は `report_to=""`＝報告しない。
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

対策（**P4 実装**）:
- 発火に **jitter**（`scheduleJitter`・`schedule_id` 由来の決定論オフセット [0,max]・
  既定2分・env `AF_SCHEDULE_JITTER`）。cron のみ適用（interval は毎周期ドリフト・once は
  厳密時刻）。**jitter は next_run に焼き込まず「発火時ゲート」として適用する**
  （`jitterForSchedule`＋`tick` の `now < slot+jitter` なら見送り）。next_run は名目
  wall-clock のまま保つので、(a) オペレーターの読み上げ確認・`next_run_local` 表示・
  `{{time}}` が利用者の依頼どおりの時刻（09:00 であって 09:01 でない）になり、(b) 冪等
  スロットが名目時刻で安定し、(c) 再起動で `AF_SCHEDULE_JITTER` を変えてもゲートがズレない。
  `run_now` は名目 next_run を `now - jitter` に置くことでゲートを即通過し即時発火を保つ。
- **wake 同時実行数の上限＝実質1**: `fireOne` は tick 内で due を**逐次処理**するため、
  同時 wake は 1 に自然に律速される。加えて `ensureWorkspaceStarted` が `max_workspaces`
  クォータ（`countRunningInTenant`）を尊重し、超過時は `skipped_quota` で見送り＋通知。
- 到来が詰まった場合も逐次処理で順次 wake（明示キューは不要）。

### ★3. 無人失敗の沈黙

無人実行なので、**ログイン切れ（claude/codex 認証失効）や usage 枯渇・レート制限**時に
再ログインする人が居ない → セッションが沈黙してオペレーターに何も返らない
（auth-expiry の既知パターン）。

対策（**P4 実装**）:
- wake 失敗・注入失敗・quota/membership スキップを検知したら**沈黙せず、CP の通知センターへ
  membership スコープ通知を挿入**（`scheduler.go` `notifyOutcome`）。
  **重要な設計上の発見**: docs/30 の report seam（role="report" 追記＋自動ターン）は
  **停止中 agent 内の会話ストレージに依存**するため、wake 失敗＝WS 停止中では届かない。
  一方、通知は **CP store が源**（agent 通知は読み取り時に `drainAgent` で CP へ吸い上げ）
  なので、**WS 停止中でも surface する唯一の経路**。よって失敗報告は report seam ではなく
  通知センターに乗せる。EventID=(status,id,slot) 決定論で再発火の二重通知を防ぐ。
- 成功時はセッション自身が `report_to`（docs/30）で自己報告するので通知しない。
- **未実装（意図的な限界）**: 発火前のレート制限プリチェック（停止中 WS では usage を
  読めない）と、auth 失効で無応答になったセッションの検知（session 監視が要る）。
  前者は将来、running WS 限定の best-effort として足せる。

### ★4. 二重発火 / CP 再起動

- `next_run`/`last_run` を DB に永続化（in-memory だけだと CP 再起動で消える）。
- 冪等キーを **(schedule_id + 発火時刻スロット)** から決定論生成し、既存 create_session
  冪等機構で二重起動を吸収。
- CP 再起動直後の grace（reaper の `bootTime` と同様の考え方）で、跨いだ発火の
  catch-up 判定を安定させる。

### ★5. overlap / reentrancy

前回実行が走行中に次が来る場合の `overlap_policy`: `skip`（既定・推奨）/ `queue` / `restart`。
reuse セッションでは driver 切替中 `409 busy_switch` にも遭遇しうる。

### ★6. 起動時の CLI 自己更新が wake を殺す（**実障害・2026-07-28 修正**）

**症状**: 毎日 8:00 の定時実行が `error:wake: agent did not become healthy within 15s`
で落ち、プロンプトが配信されなかった。それ以前 11 回の発火はすべて `fired`。

**真因**: workspace の entrypoint は、エージェント CLI の自己更新（opt-in ON 時）を
`exec workspace-agent` **より前に同期実行**する。その所要時間はそのまま CP 側
`waitAgentHealthy` の待ち時間になるが、docker アダプタの予算は **15 秒固定**だった。
実測（本番相当ホスト・約16MB/s の回線）:

| 段階 | 実測 |
|---|---|
| 版チェック（`npm ls` + `npm view`×4 + curl×3）— 毎回必ず払う | 約 4 秒 |
| npm 4CLI `@latest`（cold cache / warm cache） | **35.2 秒** / 15.2 秒 |
| agy `install.sh`（52.5MB DL → 189MB バイナリ） | **15.1 秒** |
| cursor（82.5MB DL → 303MB 展開） | **5.7 秒** |
| rtk（4.5MB + checksum） | 約 1〜2 秒 |
| **全部走った場合の合計** | **約 60 秒** |

新リリースが無い日は版比較でスキップされ数秒で起動するため 15 秒に収まっていた。
障害当日は `opencode-ai` に新版があり、実インストールが走って 23 秒かかった。
**「たまにしか壊れない」のはこの版比較スキップのため**で、回線が遅い日・home が
ネットワークボリュームの環境ではさらに容易に超える。

**対策（3 段）**:
1. **無人起動では自己更新を走らせない**（本命）。スケジュール wake は
   `AF_AGENT_SELF_UPDATE_SKIP=1`（`unattendedStartEnv`）付きでコンテナを起動し、
   entrypoint は導入済みの版のまま即起動する。起動時間だけの話ではなく、**未検証の
   `@latest` を無人で引くと、その版の破壊的変更でエージェントが動かないまま無人実行に
   入る**（TUI 文字列契約の破損は本リポジトリで再発済み）。更新は人が居る手動 Start に寄せる。
   - **落とし穴**: これを `AF_AGENT_SELF_UPDATE=0` で表現しては**いけない**。opt-in OFF の
     分岐は「`~/.local` の shadow を撤去して焼き込み版へ戻す」意味論を持つため、焼き込み
     イメージでは無人起動のたびに約 1.3GB の uninstall →次回 reinstall のチャーンになる。
     専用の「この boot だけスキップ」env を分ける必要がある。
2. **docker の health 予算を適応化**。自己更新が走り得る起動（`AF_AGENT_SELF_UPDATE=1`）
   のみ 300 秒へ。native アダプタが rootfs 経路で既に 300 秒を採っていた（同じ理由）のに
   docker だけ 15 秒固定だった不揃いを解消。無人起動は 1. で更新が走らないので 15 秒のまま。
3. **scheduler の二重タイムアウトを解消**。`ensureWorkspaceStarted` の固定期限で
   hard fail していたため、その直後にある寛容な `awaitAgentReady`（`AF_SCHEDULE_WAKE_TIMEOUT`・
   既定 90 秒のポーリング）に**到達できていなかった**。起動エラーは記録するに留め、
   agent が上がってくれば発火を続行する。併せて **keep-alive の確保を wake の前**へ移動
   （従来は起動が期限超過すると keep-alive 未確保のまま return し、★1 の reaper が
   その発火で起こしたばかりの WS を回収し得た）。

**恒久対応（2026-08-24・同じ 15 秒が人の Start でも出ていた）**:

上の 3 段は wake を救ったが、**予算の数字を調整しただけ**なので同じ穴が人手の起動側に
残っていた。実報告: ローカル docker デプロイの一部の利用者だけが、Workspace を起動する
たびに赤いトースト `agent did not become healthy within 15s` を踏み、しかも**数秒後には
普通に使えていた**。300 秒に伸びるのは自己更新 opt-in が ON の起動だけなので、
**OFF の人だけ**が 15 秒固定のまま lean/cold 起動・遅い回線・ネットワーク home で踏む
（＝「一部の人だけ、たまに」に見える）。

真因は数字ではなく契約だった: **到達待ちの超過を「起動の失敗」にしていた**こと。
ECS アダプタだけは最初から違う作りで、`watchReady` に
「A readiness failure must still NEVER fail Start — that would flip a legitimately
starting workspace to failed」と書いてある（docs/62 §62.5）。docker / native の
ローカル 2 つがそこから外れていた。合わせた内容:

1. **Start は到達待ちで失敗しない**（`control-plane/runtime_health.go`）。予算は
   「同期で待ってあげる猶予」に格下げし、超えたら `starting` を名乗って nil を返す。
   native では失敗扱いの副作用がさらに重く、`defer` が **boot-install 中のプロセスを
   kill** していた（遅い初回起動ほど確実に殺す）。
   - これに伴い docker の 15 秒 / 300 秒の分岐は **45 秒ひとつ**へ畳んだ（無人起動だけ
     15 秒 = 誰も待っていないので、寛容な `awaitAgentReady` に任せる）。300 秒は
     「失敗にしない」ための延命だったが、HTTP リクエストの中で 300 秒待つのは
     Runtime port が禁じている当のもの（ingress の idle timeout 超え = 504）。
2. **docker / native の `State()` が `starting` を返せるようにした**。コンテナ/プロセスは
   走っているが `/healthz` がまだ 200 を返さない窓を、`<dataDir>/.agent-starting`
   （中身は期限）で表す。印がある間だけ 1 回 probe し、上がっていれば消す＝Start を
   走らせた CP がもう居なくても収束する。**必ず時限式**（`agentBootBudget` 300 秒）に
   するのが肝で、収束しない `starting` は Console から何もできない箱になる（docs/70 §70.14.6）。
   これで「起動中なのに稼働中と表示され、ターミナルが誰も居ないソケットに繋ぎに行く」も
   同時に消える。
3. **Agent を要する API は待ち先を移した**。session 作成 / fork / 再開 / 持ち越し回答 /
   SSM ノード探索は `ensureWorkspaceReady` を通り、`agentReadyWait`（既定 55 秒・
   `AF_AGENT_READY_WAIT_SEC`）だけ待って、届かなければ **409 `workspace_starting`**
   （Console 訳「起動中です。準備ができてからもう一度」）を返す。55 秒は ALB の idle
   timeout 60 秒の内側（docs/62 §62.5 — 超えると応答自体が 504 で消える）。
4. **home を消す判断は `workspaceAlive()` へ**。recreate / clean-home の「Stop 失敗かつ
   まだ running なら中断」は `starting` を取りこぼすと**生きた bind-mount を消しに行く**。

副産物として、失敗扱いが作っていた**状態の嘘**も消えた: 以前は Start がエラーだと
`SetWorkspaceState("running")` も `touchWorkspace` も通らず、コンテナは走っているのに
DB は stopped・reaper の in-memory lastSeen も古いまま、だった。

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
  `ScheduleStore`・env ゲート `AF_SCHEDULER_INTERVAL`。**当初 P1 は既定 OFF（opt-in）だったが
  P5.1 後に既定 ON（1m tick・opt-out）へ反転＝`=0` で hard-off。下記 P5.1 ⑧参照**）。実 wake+注入は
  `scheduleFirer` シーム越しの P2 に委ね、P1 既定は no-op の `logFirer`（有効化しても
  台帳が進むだけ）。cron は外部依存ゼロの自前評価器（5 フィールド・dom OR dow の
  Vixie ルール・`time/tzdata` 埋込で TZ/DST）。テスト 18 件（DST 両方向含む）緑。
- **P2**: wake 経路（membership→resolved 内部生成・`ensureWorkspaceStarted` 内部呼び）と
  reaper keep-alive（★1）。**実装済み**（`control-plane/scheduler_wake.go`＝`wakeFirer`）。
  membership→resolved は既存の `IdentityIDForMembership`＋`resolveByMembership`（memo bridge
  flush と同経路）を再利用＝新規解決コード不要。wake_policy 適用（wake/catch_up=起こす・
  skip=停止中なら見送り・running は続行）→`ensureWorkspaceStarted` 内部 wake→connRegistry に
  tier2 擬似接続で keep-alive（settle 猶予後 release）→agent 到達待ち→create_session REST 注入
  （`report_to`＝完了は docs/30 seam へ・`idempotency_key`＝(schedule_id＋slot) 決定論）。
  テンプレート展開は §④''' の固定メタ変数のみ。env: `AF_SCHEDULE_SETTLE`（既定5分）/
  `AF_SCHEDULE_WAKE_TIMEOUT`（既定90秒）。テスト8件。**後回し**（P4）: jitter/並列上限/
  レート制限事前チェック/無人失敗報告。dir/worktree/new_branch の完全配線は P3。
- **P3**: 操作 MCP（create/list/update/delete/pause/run_now/get_runs）＋ CP internal 経路。
  **実装済み**。CP: `schedule_bridge.go`（専用 `AF_SCHEDULE_TOKEN`＝memo/git と別クレデンシャル・
  `withScheduleToken`）＋`schedule.go`（`scheduleAPI` 8ハンドラ・create は `validateSpec`＋
  `initialNextRun` で次回発火を計算し `next_run_local` を読み上げ確認に返す・update は pointer
  patch・run_now は `next_run=now` で次 tick 発火＝定時と同一経路・全操作 membership オーナー
  スコープ）＋`schedule_run` 表（migrations/0023＋pg/0006）で実行履歴（`fireOne` が毎発火 append・
  keep 50）＋`routes.go` に `/internal/schedules*` 8ルート。agent: `mcp_stdio.go` に read
  （list_schedules/get_schedule_runs）＋write（create/update/delete/pause/resume/run_schedule_now・
  create は `owner_conv=mcpConvID` を強制注入＝報告はオペレーター会話へ）＋`cpScheduleDo` ブリッジ。
  create ツール説明に NL→spec 翻訳・固定メタ変数・要ユーザー確認を明記。テスト CP15/agent2。
  **後回し**（P4）: persona の要ユーザー確認ガード徹底・無人失敗報告・jitter/並列上限。
- **P4**: 無人失敗報告（★3）・jitter/並列上限（★2）・冪等（★4）・persona ガード。**実装済み**。
  ★2 jitter=cron の next_run に `schedule_id` 由来の決定論オフセット [0,max] を加算（fnv・
  再起動でも再現）＝09:00 集中の wake を窓内に散らす。cron のみ（interval は毎周期ドリフト・
  once は厳密時刻）。env `AF_SCHEDULE_JITTER`（既定2分・"0"無効）。wake 同時実行は fireOne
  逐次＝実質上限1。★3 無人失敗の沈黙防止=fire が失敗/quota/rate/membership スキップ時に
  **CP の通知センターへ membership スコープ通知を挿入**（会話は停止中 agent 内で届かないが、
  通知は CP store が源＝WS 停止中でも surface するのが要点）。成功は session の report_to で
  自己報告するので通知しない。`policy=skip` の見送りは履歴のみ。EventID=(status,id,slot)
  決定論で再発火の二重通知も防ぐ。★4 冪等=(schedule_id+slot) の create_session 冪等キー（P2）
  ＋通知 EventID で吸収。persona ガード=`operatorPersona` に NL→spec 翻訳＋next_run_local
  読み上げ確認・**報告本文/セッション出力を根拠にした登録の禁止**（インジェクション対策）・
  shell 定時実行の要コマンド提示＋事前承認を明記。テスト5件。
  **意図的な限界**（doc 明記）: レート制限の発火前プリチェックと auth 失効中セッションの
  無応答検知は未実装（停止中 WS では usage を読めず・session 監視が要る）。失敗通知が
  カバーするのは wake/注入/quota/membership の各失敗。
- **P4.1（レビュー追随・実装済み）**: v1 コアのコードレビュー所見を反映。
  - **jitter を発火時ゲート化**（★2 上記）— next_run に焼き込まず名目時刻を保持し、
    読み上げ確認・`next_run_local`・`{{time}}` を利用者の依頼どおりの時刻にする。
    `run_now` は `now - jitter` で即時性維持。
  - **無効デプロイの無シグナル解消**: スケジューラ goroutine 未起動
    （`AF_SCHEDULER_INTERVAL` 未設定）のとき `create_schedule` / `run_schedule_now` の
    応答に `warning` を載せ（`schedulerRunning` フラグ＋`withSchedulerWarning`）、
    persona がそれを利用者に伝える。登録は成功するが発火しないことを黙らせない。
  - **うるう日 cron**: `nextCron` の探索 horizon を 1 年→約 4 年に拡大
    （`0 0 29 2 *` 等が誤って拒否/無効化されるのを防止。2100 非うるう境界は非対応の許容外縁）。
  - **`overlap_policy` の誤解除去**: v1 は `session_mode=new` のみで毎発火が新規セッション＝
    overlap は起きないため、操作 MCP の create/update ツール表面から `overlap_policy` を撤去
    （DB 列と既定 `skip` は温存。reuse モード導入時に再公開）。
  - **使用済み `once` の resume 拒否**: 過去時刻の once を resume すると即再発火するため、
    `resume` で過去 once を 400（`once_in_past`）に。将来 once は従来どおり resume 可。
  - **`owner_conv` を更新不可に**: create は operator 自身の会話に固定注入するが、update の
    patch から `owner_conv` を除去（membership 内での報告先すり替え面を閉じる。生涯固定）。
- **P5（Console UI）実装済み**: 左レール専用セクション「スケジュール」（`console/src/features/
  schedules/`）。**閲覧＋管理**（一覧／実行履歴／有効・無効トグル／run-now／削除）で、登録・
  編集はオペレーター会話（NL→spec 翻訳が要る）に残す。CP は既存の membership スコープ済み
  `scheduleAPI` ハンドラを `/api/schedules*` にも生やす（`registerScheduleRoutes` に
  `withMembership` アダプタ `scheduleMember` で list/runs/pause/resume/run-now/delete の6経路。
  create/update は /internal のみ）。フロントは memo キュー同型の自己完結セクション（mount時
  fetch＋15s poll＋tenant キーで再取得）。ステータスは `--ok/--warn/--del/--muted` の4トーンで
  ドット表示、run-now の warning（scheduler 無効デプロイ）はトースト。pure ロジック
  （statusTone/statusIcon/specSummary/formatInterval/sortSchedules）は `read.ts` に分離し
  vitest 9件。ライト/ダーク両テーマを headless Chromium で描画確認。console test 374／CP 199／
  typecheck／i18n-lint／build 緑。**残（後続）**: 長寿命セッション再利用モード（`session_mode=
  reuse`・`reuse_target`）＝**P6**（下記「長寿命セッション再利用モード」節で設計中）。
- **P6（長寿命セッション再利用モード）**: **実装済み**（下記専用節が設計正本・2026-07-23）。
  同一の長寿命セッションへ毎発火プロンプトを送り（`send_to_session` 経由）、文脈を継続させる。
  CP: `scheduler_reuse.go`（`fireReuse`＝pinned/managed 2モード・rotation 評価・CP側
  send/resume/input・managed create から name 取得→台帳更新・overlap 適用）＋
  `scheduler_wake.go` の `fire()` が `SessionMode=="reuse"` を分岐。DB: migrations/0024＋
  pg/0007（`reuse_session`/`reuse_started_at`/`reuse_run_count`/`rotation`/`missing_target_policy`）
  ＋`SetScheduleReuse`。API: `schedule.go` DTO/validate に rotation・missing_target_policy、
  reuse 用に overlap_policy を再公開。MCP: create/update_schedule に
  session_mode/reuse_target/rotation/missing_target_policy/overlap_policy 引数、persona に
  reuse 確認ガード。テスト CP14 件（rotation 純関数＋fireReuse pinned/managed/rotate/missing/
  overlap の fake-agent 統合）。**意図的な限界**（決定 3）: 使用率トリガ `context_pct` は未実装
  （停止中 WS で usage を読めないため後続 best-effort）。**残**: 実フリート再ビルド後の実機目視、
  Console のスケジュール一覧に reuse/rotation の表示（現状 read-only 台帳は DTO に出るが UI 未装飾）。
- **P5.1（Console/CP 改良・2026-07-23）**＝利用者フィードバック7件:
  ① **無効デプロイでは左レールのスケジュールを隠す**＝`whoami` に `scheduler_enabled`
  （＝`schedulerRunning`）を追加し、`App.tsx` が偽なら `<SchedulesSection>` を描画しない
  （発火し得ないデプロイで UI ノイズを出さない）。② **env サンプル追記**＝`deploy/compose/.env.example`
  と `deploy/local/oauth.env.example` に `AF_SCHEDULER_INTERVAL`（＋`AF_SCHEDULE_JITTER`/`SETTLE`/
  `WAKE_TIMEOUT`）を既定 OFF の説明付きで記載。④ **行 UI 再構成**＝行本体クリックで実行履歴を開閉、
  一時停止/再開・今すぐ発火・削除は「⋯」メニュー（右クリックでも開く。`SessionRow` と同じ
  `useDismiss`＋`useMenuRoving`＋`placeFixed` イディオム）へ集約。⑤ **履歴→セッション起動＋成否表示**＝
  `schedule_run` に `session` 列（migrations/0025＋pg/0008）を追加し `fire()` の戻りを
  `(status, session, err)` に拡張（new＝create_session 応答の name を parse、reuse＝reuse 対象名）、
  履歴行に成否ラベル（成功/失敗/スキップ/未実行・4トーン）を出し、`session` があればクリックで
  該当セッションを開く（schedule の agent_kind で chat/terminal を選択）。⑦ **手動 run-now と定時の
  区別**＝`schedule.manual_fire_pending` 列（migrations/0026＋pg/0009）を run-now が立て（`MarkManualFirePending`）、
  fireOne が読んで run 履歴を `trigger_kind`（migrations/0025）に `manual`/`scheduled` で記録し発火時に
  クリア（`RecordScheduleFire`）。履歴行にトリガーバッジ（手動/定時）を表示。⑥ **dist README への
  追記**＝`deploy/release/dist-repo/README.md`／`README.ja.md` の主要機能に「定時実行」節を追加。③ の
  `sch_98968564…` は状態確認依頼（実フリート DB は当環境から触れないためオペレーター会話で
  `list_schedules` する）。テスト＝CP +2件（manual-fire フラグ set/clear、run session/trigger 往復）
  ＋console read.test +2件（`runStatusLabelKey`/`isManualRun`）。CP 224／console typecheck・vitest 394・
  i18n-lint・build 緑。**⑧ スケジューラを既定 ON（opt-out）へ反転（2026-07-23・ユーザー判断）**＝`main.go` の
  `AF_SCHEDULER_INTERVAL` 既定を `0`→`1m`（`=0` で hard-off は温存）。根拠＝tick は `enabled,next_run`
  インデックス済み due-query 1本でスケジュール0件なら完全 no-op・「作った定時実行が実際に発火する」方が
  驚きが少ない。リスク本体は tick でなく発火＝停止中 WS の wake（意図的にスケジュールを作った時だけ発生・
  jitter/クォータ/keep-alive で緩和済み）。ECS 課金や no-wake ポリシーは `=0` で殺せる。副作用＝① の左ペイン
  非表示は既定 ON では `=0` にした時だけ隠れる affordance に格下げ（矛盾なし）。**残**: 実フリート再ビルド後の実機目視。

## 長寿命セッション再利用モード（`session_mode=reuse`・実装済み 2026-07-23）

v1 コアは `session_mode=new`（毎発火で新規セッション）のみ。reuse は**同一の長寿命
セッションへ毎回プロンプトを送り、会話文脈を継続させる**モード。「昨日の続きをやらせる」
「同じ作業スレッドに積み上げる」用途と、掃除対象を 1 本に抑える運用に効く。DB 列
（`session_mode`／`reuse_target`／`overlap_policy`）は v1 で既に用意済み（値は未使用）。

### 動機と非動機

- 動機: new は毎回ゼロ文脈で、継続タスク（レビューの積み上げ・日次の引き継ぎ）に不向き。
  reuse なら前回までの文脈が生きる。生成セッションが増えない＝掃除も楽。
- 非動機（v1 reuse）: 複数スケジュールで 1 セッションを共有する多重化、reuse 先での
  agent_kind/model の動的切替は範囲外（後述のとおり kind は既存セッション側が正）。

### 注入プリミティブ — create ではなく send（既存資産で賄える）

reuse の注入は `send_to_session`（`workspace/agent/mcp_stdio.go` `agentSendToSession`・
Agent REST は状態確認→停止中なら `POST /sessions/{name}/start`→`POST /sessions/{name}/input`）
を使う。停止中 WS でも wake 後に resume される。`resumed` フラグが返る。

**重要な帰結**: reuse では **kind/model/repo/worktree/new_branch は既存セッション側が正**で、
スケジュールのそれらは（初回作成のシード用途を除き）注入時に無視する。よって
**driver 切替＝★5 の `busy_switch` は reuse では起きない**（作り直さないから）。★5 の
overlap 懸念のうち残るのは「前発火が走行中に次が来る」reentrancy だけ（下記 overlap）。

### reuse_target の解決 — 2 モード

- **(A) ピン留め reuse**: `reuse_target` = 既存セッション名。オペレーターが「この常設
  セッションへ毎朝送って」と指定。セッションのライフサイクルはユーザー/オペレーターが所有。
  ローテーションは既定 off。
- **(B) 管理 reuse（ローテーション対象）**: `reuse_target` 空 → スケジュールが専用の
  長寿命セッションを**初回発火で作成**（create_session、`report_to`＝owner_conv）し、以後は
  そこへ send。名前は派生（例 `sched-<id>` タイトル）。この管理下セッションに
  ローテーションを適用する。
- **対象消失時**（アーカイブ/削除された）: 沈黙失敗にせず、選択制ポリシー `missing_target_policy`
  で裁く（**既定 `recreate`＝作り直して台帳を貼り替え・run 履歴に「recreated」記録** / `fail`＝
  失敗通知で止める）。(B) 管理 reuse は自セッションなので実質 `recreate` 前提。(A) ピン留めは
  ユーザー所有セッションのため `fail` を選べる（既定は `recreate`・下記決定 4）。

### 利用者以外の発注元 — Agent 自身（2026-07-31 追補）

reuse の想定利用者はオペレーター（MCP）だったが、**Agent 自身が予約を入れる経路**が 1 つ
増えた: claude が利用上限でターンを切られたとき、上限が解ける時刻に「続きを進めて」を
そのセッションへ送る一回限りの予約（docs/47 §4-4、`workspace/agent/rate_limit_resume.go`）。

```
spec_kind=once / session_mode=reuse / reuse_target=<止まったセッション>
wake_policy=wake / overlap_policy=skip / missing_target_policy=fail / report=false / owner_conv 空
```

ここで定時実行を使うのは**待ち合わせの器としてこれしか無い**から。上限のリセットまで
数時間あり、その間 WS は停止してよい（ターンは終わっている）ので、プロセス内タイマーでは
待てない — 停止中も生きているのは CP だけ、という docs/38 の第一原理そのものである。

含意:
- `/internal/schedules` の呼び出し元が **MCP（`mcpConvID` あり）だけではなくなった**。
  `owner_conv` は空でよい（会話に紐付かない）ので、その前提のコードを増やさないこと。
- 使い切った `once` は Agent 側が発火後に削除する（無効な行を一覧に溜めない）。
- 一覧には他の予約と同じ行として出る（`spec_label` に「利用上限リセット後の自動再開」）。
  Agent の裏送信を利用者から見えるようにする唯一の窓なので、隠さないこと。

### ローテーション — いつ新品に戻すか（ユーザー提案・2026-07-23）

reuse は文脈が積み上がり、放置するとコンテキスト上限に達する（docs/33 の 90% 自動圧縮で
延命はするが限界がある）。「一定量／期間／暦でローテーション」を入れる。トリガは **OR 合成**
（どれか満たせば**次回発火で現セッションを退役し新規作成**）:

| トリガ | 列（案） | 意味 | 判定材料 |
|---|---|---|---|
| 量（単純） | `rotation.every_runs` | N 発火ごとに作り直し | 台帳 `reuse_run_count`（決定論） |
| 期間 | `rotation.after` | 現セッション開始から一定経過で作り直し（例 7d） | 台帳 `reuse_started_at`（決定論） |
| 暦 | `rotation.calendar` | 暦境界を跨いだら作り直し（daily/weekly/monthly） | 発火スロットと `reuse_started_at` の境界比較。「**月曜は新セッション**」＝ weekly（週境界＝月曜始まり）（決定論） |
| 量（使用率・**後続**） | `rotation.context_pct` | コンテキスト使用率が閾値超で作り直し | 発火前に running セッションの usage を読む（docs/33）。**停止中は読めず best-effort＝v1 reuse では未実装、後続で追加** |

- ローテーション実行時: 現セッションを退役（停止のまま cleanup 機構へ、または archive）→
  新規作成 → 台帳を新セッションへ貼り替え → run 履歴に「rotated」記録。
- 使用率トリガは running 限定の best-effort と割り切る（停止中 WS では usage を読めない、の
  既知制約＝★3 と同根）。決定論が要るなら `rotate_every_runs`／`rotate_after`／`rotate_calendar`
  で足りる。

### 台帳（新規列）

reuse の現行セッション同一性とローテーション判定材料を持つ（`schedule` 表に追加）:

- `reuse_session` — 現に使っている実セッション名（ローテーションで変わる。(A) では reuse_target と同じ）
- `reuse_started_at` — 現 reuse セッションの開始時刻（期間/暦判定）
- `reuse_run_count` — 前回ローテーションからの発火数（量ベース単純版）
- `rotation` — ローテーション設定を畳んだ**単一 JSON 列**（決定 2。例:
  `{"every_runs":20,"after":"7d","calendar":"weekly"}`。`context_pct` は後続）
- `missing_target_policy` — 対象消失時の挙動（`recreate` 既定 / `fail`。決定 4）

### overlap の再公開（★5）

reuse は同一セッションゆえ、前発火が走行中に次が来る overlap が現実に起きる。v1 P4.1 で
操作 MCP 表面から撤去した `overlap_policy` を **reuse でのみ再公開**する（new では引き続き隠す）:

- `skip`（既定）: send 前に状態を見て busy なら今回を見送り（run 履歴に `skipped_overlap`）。
- `queue`: そのまま `/input` へ送る＝現ターン後に処理されるステアリング的投入。
- `restart`: 現ターンを halt してから送る。
- `send_to_session` の 409（`question_pending`＝入力待ち中）も busy とみなし overlap_policy で裁く。

### keep-alive / settle（★1）は new と同じ

reuse でも wake→keep-alive→settle 後 release。停止中だった WS は settle 後に停止へ戻す。
ただし (A) ピン留めでユーザーが日常使う常設セッションなら、settle 後の停止判断は reaper の
通常タイムアウトに委ね、スケジュール由来で強制停止しない方が親切（要決定に含めうる）。

### セキュリティ / 掃除

- reuse は「無人で同じセッションに積む」＝過去の会話（データ）が文脈に残り続ける。§④''' の
  「プロンプト本文にデータを運ばない」線は維持されるが、**reuse セッション自体が過去データを
  保持する**点が new と異なる。要ユーザー確認の persona ガード（★セキュリティ）は reuse 登録
  にも適用する。
- 退役セッションは operator の掃除ツール群（cleanup 機構）に乗せる。archive か stop-only かは
  掃除ポリシーに合わせる。

### 配達検証（confirm・2026-07-24 追補 — sbk7oej 再発の恒久対策）

**事象（2026-07-24 朝 08:01 JST）**: reuse 発火が `fired`（成功）を記録したのに、対象
セッション（sbk7oej）の会話 jsonl には user ターンが一切追記されず、タスクは実行され
なかった。WS は稼働中・セッションは停止中（Tier1 idle halt 済み・設計どおり）で、
resume→readiness ゲート→`/input` 200 まで全部通っている。つまり **readiness ゲート
（silent-drop 第1弾修正）だけでは足りない**: `tmux send-keys` の成功は「キーがペインに
届いた」ことしか保証せず、CLI 側の一瞬の受け付け不能（resume 直後のスラッシュコマンド
登録前・ペースト折り畳みで Enter が食われる・モーダルの文字無視、いずれも版依存で再現
条件が揮発する）でプロンプトは無音で消える。同じ穴はオペレーターの `send_to_session`
（停止中セッションへの指示）でも踏んだ（conv bc5d685e）。

**対策 — 成功の定義を「打鍵」から「ターン開始の証拠」へ引き上げる**:

- Agent `/input {prompt}` に **`confirm:true`（オプトイン）** を追加
  （`session_delivery.go`）。証拠 = **claude の会話 jsonl に user ターンが追記された**
  （`claude.TranscriptSnapshot`/`UserTurnAppendedSince`・提出の一次記録）∨ ペインが
  実行中スピナー（`tmuxx.IsBusy`・jsonl フラッシュ遅延の保険）。
- 証拠が窓（12s）内に出なければ**自己修復を 1 巡**: コンポーザに下書きが残っていれば
  Enter 再送（提出済みなら no-op で安全）、下書きごと消えていれば全文再タイプ（証拠なし
  ＝未提出なので二重実行しない）。再窓（12s）でも証拠が出なければ
  **502 `delivery_unconfirmed`** を返す。
- 設定側: CP スケジューラの reuse 送信（`reuseSendBody`）と operator MCP の
  `send_to_session` が `confirm:true` を付ける。スケジューラは失敗を `error:` として
  台帳に記録し★3 通知を出す — **偽 `fired` は作らない**。create 経路の
  `deliverInitialPrompt` も同じ検証＋自己修復を行う（goroutine のため最終失敗はログ）。
- オプトインの理由: `/model` などターンを生まない UI スラッシュコマンドや、人が見て
  いる Console 送信の意味論を変えないため。検証手段の無い kind（非 claude TUI）では
  no-op（「検証できない」を「配達失敗」と混同しない）。
- MCP 側クライアントタイムアウトは 45s（検証ブロックの最悪 ~26s を上回る値）。

### 注入元バッジ（source・2026-07-25 追補）

スケジュール発のプロンプトをミラー上で識別可能にする。既存の注入元記録
（`session_injections.go`・operator/discord/slack）を拡張:

- CP スケジューラは create（initial_prompt）と reuse 送信（/input）の両方に
  **`source: "schedule"`（定時発火）/ `"schedule-manual"`（run-now 手動発火・
  `manual_fire_pending` 由来）** を付与。Agent はホワイトリスト
  （`injectionSource`）で受け、未知値は従来どおり "operator" に落とす（任意
  バッジ文字列の鋳造防止）。
- **スラッシュコマンド注入の照合修正**: 記録は送信生テキスト（"/scout"）だが
  transcript の user turn は `<command-*>` タグブロック（スキルは message 先頭・
  ビルトインは name 先頭）で完全一致しない → `commandSlashForm` が "/name args"
  を復元して照合。これは operator バッジがスラッシュコマンドで効いていなかった
  既存欠陥の修正でもある。
- Console: user turn に緑系ピル「定時実行」（clock）/「手動発火」（play）を表示
  （operator=橙 broadcast・チャット=青と並ぶ第3の注入元色）。

#### 完了報告 OFF の発火にバッジが付いていなかった（2026-08-18 修正）

`source` は最初から届いていたのに、Agent 側の記録（`recordInjection`）が
`report_to != ""` の枝の中にあった。`report_to` は `report=true` のときだけ付く
（CP `scheduleReportTo`）ので、**完了報告 OFF のスケジュール投入は由来が丸ごと
捨てられ、利用者が自分で打った入力と見分けが付かなかった**。Console の完了報告
チェックは既定 OFF なので、素直に作ったスケジュールはほぼ全部これに当たる。利用
上限リセット後の自動再開（docs/47 §4-4）は `report:false` 固定なので必ず該当し、
「利用上限がリセットされました…」が無印の user ターンとして並ぶ。

修正は由来の記録を報告の有無から切り離すこと。`scheduleInjectionSource()`
（schedule / schedule-manual のみ通し、それ以外は ""）を門にして、`report_to` が
空でもスケジュール由来なら記録する。`injectionSource()` をそのまま使えないのは、
あれが未知/空を "operator" へ倒す設計で、素の Console 入力まで operator バッジに
なってしまうため。適用箇所は4つ — /input（TUI・managed）と create_session
（TUI・managed の initial_prompt）。

- **指示台帳は従来どおり**: `addInstruction` は `report_to` があるときだけ。報告先の
  無い発火に台帳行を立てない（docs/51 の早期 settle と同型の事故を作らない）。
- **チャットブリッジへの転記も止まる**: 従来これらは `default` 枝に落ちていたため
  `bridge.MirrorUserInput` が走り、定時実行のプロンプトが Discord スレッドへ「利用者が
  Console で打った入力」として流れていた（docs/37 Fix ② の意図外）。
- 自動再開のターンは「定時実行」バッジになる（実体が once スケジュールの発火なので）。
  専用の「自動再開」バッジ（docs/47 §4-6 の `auto-resume`）は中断再開のみが使う。

### アシスタント発火（session_mode=assistant・2026-07-25 追補）

スケジュールから**セッションではなくアシスタント会話**へ発火する第3のモード。
「毎朝オペレーターにフリート状況をまとめさせる」等、会話（persona＋af ツール）に
定時タスクを直接やらせる。

- **会話 slug**: 会話に短い不変 ID「a＋base32 6字」を導入（セッション slug "s…" の
  対称）。作成時採番＋既存会話は agent 起動時に backfill（`chat_slug.go`）。UUID と
  slug の両方で参照可（`resolveConvRef`）。一覧 API（chatMeta.slug）と Console の
  会話行 tooltip に露出。
- **対象解決**: `reuse_target` に会話 slug/UUID。**空なら owner_conv（スケジュールを
  作ったオペレーター会話）** — 追加設定なしで「自分のオペレーターを蹴る」が既定。
  repo/agent_kind/model は無視（会話側の設定が正）。
- **実行**: CP `fireAssistant`（`scheduler_assistant.go`）→ wake/keep-alive/agent 到達
  待ちは new/reuse と同一 → Agent `POST /assistant-turns {conv, prompt}`
  （`assistant_turn.go`）→ **runOperatorTurn 委譲**（Discord @メンション経路と同一＝
  ロック・自動圧縮・overflow 自己修復・AutoTurns リセット・無人承認ゲート[破壊的
  ツールはブリッジ承認、未接続なら fail-closed]まで同挙動）。
- **同期意味論**: 200＝ターン完走・エラー＝未実行なので配達検証（confirm）は不要。
  実行中の会話への発火は 409 `turn_in_progress` → `skipped_overlap`（busy reuse と
  同じ無人非配送サーフェス・通知対象）。会話消失は `skipped_target_missing`。
  タイムアウトは CP 側 8m ＞ agent 側 operatorTurnTimeout 6m（先に諦めるのは agent）。
- **履歴リンク**: run.session に会話 slug を記録。Console の実行履歴クリックは
  a-slug を検出してチャットペインを開く（セッションは従来どおり）。
- rotation はアシスタント発火には不適用（会話の肥大は docs/33 の自動圧縮が受け持つ
  ＝ローテーション不要という綺麗な分業）。

### 決定（2026-07-23）

1. **主モード**: **(A) ピン留め / (B) 管理 の両対応。ツール表面の既定は (B) 管理**
   （ローテーションの主役で自己完結。ピン留めは `reuse_target` 明示で選択）。
2. **ローテーション設定の持ち方**: **単一 JSON `rotation` 列**（`{"every_runs":N,"after":"7d",
   "calendar":"weekly","context_pct":80}` 等。トリガ追加でマイグレーション不要）。
3. **量ベースの既定**: **まず決定論の 3 種のみ**（`every_runs`／`after`／`calendar`）。使用率
   トリガ（`context_pct`・running 限定・best-effort）は**後続**で足す（停止中 WS では usage を
   読めない＝★3 と同根の制約のため）。
4. **対象消失時（`missing_target_policy`）**: **選択制。既定 `recreate`（作り直す）** / `fail`
   （失敗通知で止める）。(A) ピン留めでユーザー所有セッションを保護したい場合に `fail` を選ぶ。

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
