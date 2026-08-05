# 47. 中断ターンの検知と自動再開

決定は [ADR0030](decisions/0030-turn-abort-auto-resume.md)。

## 1. 何が壊れていたか

claude の TUI セッションで API エラーがターンを切ると **Stop フックが鳴らない**。
その結果 `working → idle` の遷移が記録されず、ペインだけが待機プロンプトに戻る。

> この前提はエラーの種類による。利用上限の 429 では claude はターンを完了として畳み、
> Stop も鳴らす — そこには別の穴があった。§4-5 を参照。

後始末をしていたペインベースの自己修復（`driveState` / `WireLive`）は、
`state != "idle"` かつペインが待機プロンプトなら `status.Remove(sid)` を呼ぶだけで、
`recordSessionNotification` を通らなかった。よって:

- 応答あり通知が出ない
- docs/30 の完了報告が飛ばない（arm が未消費のまま残る）
- Console 上はただの「入力待ち」— 利用者が気づくまで作業が止まる

### 実測（2026-07-26, セッション ssiw5kb）

| 時刻 (JST) | 出来事 |
|---|---|
| 22:58:39 | セッション起動（worktree `agent-fleet@wip-sz3wiph`、報告先 arm 済み） |
| 23:02:57 | 4分17秒のターン中に `API Error: Connection closed mid-response.`／`turn_duration` が最終レコード |
| 直後のポーリング | 自己修復が発火 → `session-status/<sid>.json` 消失 |
| 〜23:24 | 通知 0 件・報告 0 件。`session-report/ssiw5kb.json` は `armed:true` のまま |

`session-exit/ssiw5kb.json` は起動時ベースライン `{}` のままで、プロセスは生存していた
（＝異常終了ではなく「ターンだけが落ちた」）。

## 2. エラーの実分類（フリート全 transcript の `isApiErrorMessage` 16 件）

| 文言 | 件数 | `apiErrorStatus` | 分類 |
|---|---|---|---|
| `API Error: Connection closed mid-response.` | 1 | なし | **retryable** |
| `API Error: Server is temporarily limiting requests (not your usage limit) · Rate limited` | 7 | 429 | **retryable** |
| `You've reached your <model> limit. Run /usage-credits …` | 5 | 429 | **blocked** |
| `Prompt is too long · the request is ~242785 tokens (limit 200000) …` | 3 | 400 | **blocked** |

**ステータスコードだけでは決められない**（429 に両方が同居する）。よって文言を主・
コードを従として判定し、判定不能は blocked に倒す。

> 罠: retryable 側の文言は "(not your **usage limit**)" を含む。素朴な部分一致だと
> 最多の retryable ケースが blocked に落ちるので、`retryableOverrides` を blocked より
> **先に**評価する。回帰テストあり。

### レコード形状

```jsonc
{"type":"assistant","isApiErrorMessage":true,"apiErrorStatus":429,
 "message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error: …"}]}}
```

直後に `system/turn_duration`、以降は再開されるまで実レコード（user/assistant）が続かない。

## 3. 実装

### 3-1. 検知 — `internal/agents/claude/abort.go`

`AbortedTurn(sid) (msg, retryable, ok)` が transcript を末尾から走査する。

- **許可リストで受ける**: `user` / `assistant` 以外（`system` / `file-history-*` /
  `last-prompt` / `custom-title` / `mode` / `permission-mode` / `agent-name` …）は無視。
  除外リストにすると版ごとに増える記帳レコードで壊れるため。
- `isSidechain` はサブエージェントなので本体ターンの終端材料にしない。
- 直近の実レコードが `isApiErrorMessage:true` のときだけ `ok=true`。
  **再開後は末尾が user/assistant に変わるので自然に false へ戻る**（二重報告しない）。

### 3-2. 通知 — 自己修復を seam に載せる

`claude.HealIdle(sid)` が自己修復の後始末を担う:

```
中断ターンあり → agents.MarkTurnEndErr(sid, TurnAborted|TurnFailed, msg)
                 （status に idle を書き、recordSessionNotification を呼ぶ）
それ以外       → status.Remove(sid)（従来どおり無言）
```

`MarkTurnEndErr` は managed driver 用に作った既存 seam。TUI もこれを通すことで
「どの遷移が完了か」の判定は 1 実装のまま。`Remove` ではなく **idle を Persist** する
ので、以降は自己修復条件（`state != "idle"`）を満たさず二度は鳴らない。

差し替え箇所は 2 つ: `internal/agents/claude/claude.go`（WireLive）と
`workspace/agent/agent.go`（driveState の generic 分岐、`normalizeKind` が claude のときのみ）。

### 3-3. 報告 — `StateAborted` / `turn-aborted`

| state | reason | 報告文の主旨 |
|---|---|---|
| `StateAborted` | `turn-aborted` | 再送で直る中断 → **再開させろ** |
| `StateFailed` | `turn-failed` | 原因が解消するまで再送しても同じ → **原因を伝えて相談しろ** |

どちらも kind は `answer-ready`（終端イベント＝arm を消費する）。

### 3-4. 自動再開（アシスタント主導）

報告文に指示を載せ、オペレーターが `send_to_session` で「続けて」を送る。
`send_to_session` は `report_to` を伴うので**再開後の完了報告が再び arm される**。

**送信文の言語**はセッションの直近の出力に合わせるよう指示する。セッション毎の言語
フィールドは無く、日本語で作業中のセッションに英語を送る（逆も）と**以降の応答言語ごと
転ぶ**ため。文面は「中断したので続けてほしい」旨だけにして新しい指示を混ぜない。

**上限**: `maxAutoResumeAttempts = 2`。`session-resume/<name>.json` に連続回数を持ち、
中断報告の配信で加算、クリーンな完了で 0 にリセット。上限超過後の報告文は再開を促さず、
中断が繰り返されている事実を利用者へエスカレーションする。既存の `chatAutoTurnLimit`
（既定 10）が構造的な最終クランプとして併存する。

### 3-5. 設定

設定 > アシスタント「中断時の自動再開」（`assistantAutoResume`、**既定 ON**）。
自動走行と別トグルにした理由は ADR0030 §4。トグルは**報告文の指示だけ**を切り替える
（自動走行と同じ方式）。

## 4. テスト

| 対象 | 内容 |
|---|---|
| `internal/agents/claude/abort_test.go` | 4 分類 + 判定不能、末尾形（記帳レコード後置・再開後・通常完了・空・sidechain）、実コーパスへのドリフト検知（新しい 150 件・`-short` で skip） |
| `session_status_test.go` | `StateAborted` → answer-ready/turn-aborted、`previous` が working/空の両方で発火、question からは発火しない |
| `chat_report_test.go` | ON/OFF/上限超過の文面（言語指示・破壊的操作ガードを含む）、再開カウンタの加算・リセット・セッション独立 |
| Console | 設定既定値・typecheck・i18n 裸和文 lint |

## 4-2. 追補（2026-07-30 / sp2qemx）— 検知の入口をレベルへ

実測で 2 つ穴が出た。

**(a) 検知の入口がマーカーに依存していた。** §3-2 の自己修復 seam は
`WireLive` / `liveStateOf` のヒール分岐の中にあり、その分岐は「マーカーが非 idle」かつ
「ペインが待機表示」で守られている。マーカーは誤ヒール（`HealIdle` の
`status.Remove`）で消えることがあり、消えると `LiveState` が既定の idle を返すので
ゲートは二度と開かない。sp2qemx は転写の末尾に
`API Error: Server error mid-response.` を持ちながら、通知も報告も自動再開も発火せず、
指示 1 件が pending のまま宙に浮いた（docs/51 のリコンサイラは「無マーカーは不明」で
正しく待ち続けるので、ここは救われない）。

対処: 中断そのものは**転写の末尾というレベル**に書かれているので、リコンサイラが毎
tick それを直接読む（`claude.AbortInfo` → `reportSignals.Abort`）。マーカーの状態には
一切依存しない。分類はそのまま報告の reason になり、既存の報告文・自動再開カウンタが
そのまま効く。ヒール経路（`HealIdle`）は残る — 先に気付いた方が報告し、配送は台帳の
行が 1 回に畳む。

- 判定規則は `terminalRecord` / `abortFrom` に括り出して純関数版（`abortedTurnFrom`）と
  共有。実装が 2 つに割れると片方だけ版差で腐る。
- 読むのは末尾だけ（`lastLineWhere` = 末尾 512KiB → 見つからなければ全文）。毎 tick 数 MB
  を読み直さないため。
- 中断が末尾にあるときは**転写の鮮度を busy 証拠から下ろす**。その「新しさ」は中断
  レコード自身のもので、進行中の証拠ではない（下ろさないと報告が毎回 90 秒足止めされる）。
  ペイン／サブエージェントの busy は別の事実なので残す＝再開済みなら報告しない。

**(b) `API Error: Server error mid-response.` が blocked に倒れていた。** retryable
コーパスに `internal server error` しか無く、この合成レコードは `apiErrorStatus`
フィールドごと欠けているので 5xx フォールバックにも掛からなかった。`server error` へ
広げた（`blockedMarkers` を先に見る順序は不変なので、利用上限や認証はここで
retryable に化けない）。

## 4-3. 追補（2026-07-31 / s5jjqv4）— 利用上限モーダルが回復経路を塞ぐ

**症状。** 別ユーザーのセッションが約 16 時間「進行中」に貼り付いた。実測は
claude 2.1.220 / model fable、status マーカーは
`{"state":"working","ts":"2026-07-30T18:08:47+09:00"}` のまま一度も書き換わらず、
転写の最終レコードは同 18:08:48 の合成 assistant（`isApiErrorMessage:true` /
`"You've hit your session limit · resets 7:50pm (Asia/Tokyo)"`）で止まっていた。
claude プロセスは `Sl+` / CPU 1.1% で生存（クラッシュでも OOM でもない）。

**真因は §1 と §4-2 の合わせ技ではなく、その回復経路が塞がれること。** 上限でターンが
切れると Stop hook は鳴らない（§1）。それを直すのが「ペインが待機表示なら `HealIdle`」
だが、claude は上限に当たると `/rate-limit-options` のメニューを出して**キー入力待ちで
停止**する。このメニューは `Esc to cancel`（`modalMarkers`）を必ず含み、入力欄のモード
表示フッタごと画面を置き換えるので、`AtIdlePrompt` は**恒久的に** false を返す。
上限リセット時刻（前夜 19:50）が来ても、メニューは人が消すまで消えないので何も進まない。
偶発ではなく、上限に当たれば必ず再現する。

波及は表示だけではない。通知も docs/30 の完了報告も出ず（arm 未消費）、§3-4 の自動再開も
発火せず、`reaper.go` が `working` を busy と数えるため tier1・tier2 とも効かず**コンテナが
起きっぱなしになる**。管理 API（`/api/admin/*`）にはペイン取得も個別 halt も無いので、
運用側から観測も回復もできなかった。

**対処。**

- `tmuxx.AtRateLimitModal` — メニューの**番号付き選択肢行**（`rateLimitOptionRe`）と
  `Enter to confirm` の 2 点一致で判定する。バナー（`You've hit your session limit …`）は
  転写テキストで、メニューを消した後も画面に残るので判定材料にしない（`isCodexUpdateMenu`
  と同じ罠）。行頭アンカーなのは `resumeMenuRe` と同じ理由 — repo 自身の散文に一致させない。
- `WireLive` / `driveState` はこれを `AtIdlePrompt` より**先に**見る。非 idle のときだけ
  `HealIdle` を呼び（メニューは出たままなので、このガードが無いと毎 poll 通知と報告を
  撃ち続ける）、ライブ状態として `agents.StateBlocked` を返す。
- `StateBlocked`（`"blocked"`）は**ライブのワイヤ状態**で、status ストアには書かない。
  `idle` に寄せるとミラー／オペレーター／定時実行が送ったプロンプトが**メニューの選択操作に
  化ける**（`AgentsViewActive` と同じ誤配達クラス）ので、`session_io.go` は
  `rate_limit_modal` で 409 を返して弾く。どちらの選択肢を選ぶかは課金と待ち時間の判断
  なので自動復帰は試みない。
- `reaper.go` は `blocked` を busy に数えない。`question` と違い人が気づくまで何日でも
  続きうる停止で、ターンは既に終わっているため tier2 に停止させても失う作業は無い。
- `blockedMarkers` に `session limit` を追加。`hit your` なので `reached your` に当たらず、
  `session limit` なので `usage limit` にも当たらず、既定の「判定不能は blocked」に落ちて
  **偶然だけ正解していた**。結論が同じでも意図した分類にしておく。

**テスト。** `testdata/footers/modal_rate_limit.txt`（実キャプチャの会話本文をスクラブ
したもの。他セッションの実作業をコーパスに入れない規約は維持）をゴールデンコーパスへ追加し、
`verdict` に `rateLimit` を足して**全フレーム**で固定した — 誤検知すると走っているターンを
「上限で停止」と読むので、false 側の固定が本体。`TestRateLimitModalDismissed` は
「メニューを消したらバナーが残っていても検出は外れ、idle に戻る」を押さえる。

## 4-4. 追補（2026-07-31）— 上限モーダルの自動解除とリセット時刻での自動再開

§4-3 は貼り付きを**読める**ようにしただけで、止まったままなのは変わらなかった（§5 の
積み残し「第 3 クラス」）。ここがそれを実装する。分類は retryable / blocked の 2 値のまま
（`session limit` は blocked のまま — 時刻が来るまで再送しても同じ結果である事実は変わらない）で、
**「いつ解けるか」を別の軸として足す**。

### 4-4-1. ① メニューの自動解除（設定に関わらず行う）

`tmuxx.DismissRateLimitModal(name)`。既定選択が 1（`❯ 1. Stop and wait for limit to reset`）
のときだけ Enter を送り、**ペインをもう一度読んで**メニューが消えたことを確かめる。

- **なぜ自動で押してよいか。** 課金判断を伴うのは 2（管理者へ増枠を依頼）だけで、1 は
  「待つ＝何も買わない」側。一方でメニューが出ている間セッションは注入も通知も報告も
  受け付けられない（§4-3）。よって 1 の確定は**判断の代行ではなく回復**である。
  2 を選びたい利用者はメニューが出ている間に自分で選べる。
- **既定選択が動いていたら触らない**（`rateLimitDefaultRe`）。Enter はカーソル位置を確定
  するので、人が 2 へ動かしかけている画面で押すと**利用者がしていない依頼を送る**。
  ここが自動化の唯一の危険側なので、ガードをテストで固定した。
- 送っただけで成功と見なさない（`send-keys` が 0 を返しても TUI が受けたとは限らない）。
  失敗は 1 エピソードあたり `maxRateLimitDismissTries=3` 回でやめる — 直らない原因
  （人が触っている・TUI の形が変わった）は叩き続けても直らないので、人待ちの blocked に戻す。

### 4-4-2. ② リセット時刻の確定 — `claude.ResetAt`

材料は独立に 2 つあり、**どちらも単独では足りない**:

| 材料 | 得られるもの | 弱点 |
|---|---|---|
| バナー（転写）`resets 7:50pm (Asia/Tokyo)` | そのセッションが実際に当たった窓の**壁時計** | 日付が無い |
| statusline 捕捉 `af-usage.json` の `resets_at` | 曖昧さの無い **epoch** | アカウント単位・5時間窓と週次窓の**どちらに当たったか分からない**・古いことがある |

→ **バナーで窓を選び、epoch で日付を確定する**（一致判定は ±2 分）。epoch が無ければ
バナーの壁時計から、バナーが読めなければ未来の epoch から決める。どちらも無ければ
**仕込まない** — 当てずっぽうの時刻に起こしても、また上限に当たるだけ。

基準時刻は `now` ではなく**中断レコードの時刻**（バナーが書かれた瞬間）。実測のように
メニューが 16 時間放置されたまま発見されると、`now` 基準では過ぎたリセットが「翌日の
同時刻」に化けてまる一日待つ。過去になった時刻は呼び出し側が `now + 2 分` に丸める。

### 4-4-3. ③ 待ち合わせは CP の定時実行に預ける

①でメニューを消すとセッションは普通の入力待ちになるので、リセットまでの数時間で
idle-reaper が WS ごと停止させる（**させてよい** — ターンは終わっている）。プロセス内
タイマーはそこで死ぬので、待ち合わせは停止中も生きている CP に持たせる（docs/38 の
第一原理そのもの）。既存の定時実行をそのまま使う:

```
spec_kind=once / spec=<リセット時刻> / session_mode=reuse / reuse_target=<セッション名>
wake_policy=wake / overlap_policy=skip / missing_target_policy=fail / report=false
```

- `session_mode=reuse`（docs/38 P6）＝**新規セッションではなく止まったそのセッションへ**
  投入する。文脈を引き継げない別セッションを生やしても再開にならないので
  `missing_target_policy=fail`（消えていたら作り直さない）。
- `wake_policy=wake` が WS を起こす。これが「プロセス内タイマーではなく CP」の理由。
- `overlap_policy=skip` — その時刻に人が既に動かしていたら黙って見送る。
- `report=false` — この投入をオペレーター会話へ報告しない（`owner_conv` が空で報告先が
  無い）。再開したターンが終われば「応答あり」通知は通常どおり出る。上限で止まった事実と
  再開予定時刻は、その前段の 失敗報告（turn-failed）に足す（`rateLimitResumeNote`）—
  そうしないとオペレーターは「対処を相談」で止まり、利用者にはあとから勝手に再開したように
  見える。
- **Agent が MCP 以外から `/internal/schedules` を叩く初めての経路**（`cpScheduleDo` を
  そのまま使う）。owner_conv は空 = 会話に紐付かないので、Console 起動のセッションでも効く
  （§3-4 の自動再開が会話持ちに限られるのとはここが違う）。

### 4-4-4. 状態とループ

`session-rate-limit/<name>.json`（`fstore`、`session-resume` と同じ理由で Meta に相乗り
しない）が 1 エピソード＝「メニューを見てから予約した再開が過ぎるまで」を持つ。
専用ループ `startRateLimitWatch`（1 分刻み）が回す:

- **一覧ポーリング（`wireSession`）に相乗りしない。** 解除も再開も**誰も見ていないとき**に
  効かなければ意味が無く、読み取り経路に副作用を置くと「画面を開いていれば直る」機能になる。
- 順序は **②→①**。①が成功するとメニューは消え、この検知経路は二度と開かない。仕込み
  損ねた場合だけ、状態ファイルを見て後続の tick がリトライする（上限 5 回）。
- 予約時刻＋30 分を過ぎたら、使い切った once スケジュールを削除して状態を畳む
  （残すと Console の一覧に無効な行が溜まり、次の上限で予約されなくなる）。
- 初回検知時に `rate-limit-reached`、再開プロンプトの配達確認が成功した時点に
  `rate-limit-resumed` を Agent の永続 outbox へ各1回だけ書き、通知センターへ届ける。
  予約時刻だけでは「再開」としない（重複発火・対象消失・overlap・配達失敗を誤通知しない）。
  どちらも対象セッションへのリンクを持ち、外部チャット連携の通知グループには含めない。

### 4-4-5. 設定

設定 > エージェント > Claude > 動作設定「利用上限リセット後の自動再開」
（`rateLimitAutoResume`、**既定 ON**）。アシスタント会話に属する設定ではなく、Console
から直接起動した独立セッションを含む全 Claude TUI セッションに適用するため、この配置とする。
このトグルが左右するのは**②③の一回限りの再開予約だけ**で、①の解除は OFF でも行う
（上記の理由）。予約は `spec_kind=once` で繰り返さず、使用後に削除する。

### 4-4-6. テスト

| 対象 | 内容 |
|---|---|
| `internal/tmuxx/footer_corpus_test.go` | 既定選択ガード: 実キャプチャで真、`❯` を 2 行目へ動かしたフレームで偽（＝増枠依頼を選ばない） |
| `internal/agents/claude/ratelimit_test.go` | バナー解析（12am/12pm 境界・分なし・日付つき・TZ 無し・am/pm 無しは読まない）、窓の選択、放置メニューが翌日に化けないこと、材料無しは決めないこと |
| `rate_limit_resume_test.go` | 予約→解除の順序と冪等、解除リトライの上限、設定 OFF でも解除はすること、独立起動／アシスタント起点のどちらにも同じ処理が走ること、検知／配達確認後の通知が各1回だけ出ること、過去のリセットは最短で回すこと、登録失敗のリトライと打ち切り、エピソードの畳み方 |

## 4-5. 追補（2026-08-05 / s6no6jv）— 上限には**メニューを出さない形**がある

§4-3 / §4-4 が相手にしていたのはアカウントの窓（`/rate-limit-options` メニュー）だけだった。
**モデル別の上限は形が違う**: claude はメニューを出さず、1 行の合成レコードを書いて
ターンを**完了として畳み**、普通の入力欄へ戻る。

```
⎿  You've reached your Fable 5 limit. Run /usage-credits to continue or switch models with /model.
✻ Brewed for 18m 56s
```

転写のレコード（実測）— `error` が構造化されているのが重要で、英文言に依存しない材料はここにしかない:

```json
{"type":"assistant","isSidechain":false,"isApiErrorMessage":true,"apiErrorStatus":429,
 "error":"rate_limit","errorDetails":"429 {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\", …}}",
 "message":{"content":[{"text":"You've reached your Fable 5 limit. Run /usage-credits …"}]}}
```

### 4-5-1. なぜ全部すり抜けたか

3 つのゲートが順に閉じていた。

| # | 場所 | 条件 | 実際 |
|---|---|---|---|
| 1 | `rateLimitTick` | `ReadPane().RateLimitMenu` | メニューが無い → エピソードが開かない |
| 2 | `WireLive` / `driveState` の `HealIdle` | `state != "idle"` | Stop が先に idle を書く → 素通り |
| 3 | Stop フック経路（`runSessionStatusHook`） | `AbortInfo` を見ない | 素の `idle` ＝ 応答が完了 として通知 |

②の前提「API エラーでターンが落ちると Stop は鳴らない」（§1）は**この 429 では成立しない**。
claude は `turn_duration` を書き、`Brewed for …` を出し、Stop も鳴らす。実測 `s6no6jv`:
中断レコード 14:57:54.99 → ステータス `{"state":"idle","turnEnd":true,"ts":"14:57:56"}`。
Stop が鳴った証拠は pending-text が消えていること（削除するのは `applyPendingPayloads` ＝
フック経路の 1 箇所だけ）。

docs/51 のリコンサイラ（`collectAbortSignal`）だけは転写末尾をレベルで見るので拾えるが、
**arm 済みセッション限定**。Console から利用者が直接起動したセッションには何も出ない。

### 4-5-2. 直し方

**A. Stop の idle を「どう終わったか」に精緻化する** — `turnEndLabel`（`session_status.go`）。
`state=="idle"` のとき `claude.AbortInfo` を見て、中断なら `StateFailed` / `StateAborted` を
`recordSessionNotification` に渡す。理由は excerpt に載せる（managed の `MarkTurnEndErr` と
同じ契約）。他 kind では `AbortInfo` が claude の `ConfigDir` を見るので自然に素通しになる。

**B. エピソードの入口を 2 経路にする** — `rateLimitTick` はペインでメニューを探し、無ければ
転写末尾を `claude.UsageLimitAbort` で見る。`limitMarkers` は `blockedMarkers` の部分集合
（`reached your` / `usage limit` / `session limit`）＝**待てば解ける上限だけ**。プロンプト超過や
認証エラーで「上限に達した」と通知すると、利用者は来ないリセットを待つことになる。
メニューが無い形では `DismissRateLimitModal` は呼ばない（メニューが無いのに Enter を撃つと、
それはそのまま利用者のプロンプト送信になる）。

**C. モデル別上限では捕捉フォールバックを使わない** — `scheduleRateLimitResume` は
`st.Menu == false` のとき `source` がバナー由来でなければ「時刻を決められなかった」と扱う。
§4-4-2 のフォールバック（statusline 捕捉）が答えるのは**アカウントの 5時間 / 週次の窓**で、
モデル別上限は別の窓だから。実測 s6no6jv では上限に当たった時点で 5時間窓 23% / 週次 75%、
フォールバックはその日の 19:30（5時間窓のリセット）を返す — そこで再開しても同じ上限に当たる。

### 4-5-3. statusline からの事前検知はできない

claude 本体が statusLine へ詰める `rate_limits` は `five_hour` と `seven_day` **だけ**（バイナリ内の
構築コードで確認）。スキーマ自体には `seven_day_opus` / `seven_day_sonnet` と
`model_scoped: [{display_name, utilization, resets_at}]`（"Per-model weekly windows"）が存在するが、
statusline 面には出てこない。よって「上限に近づいている」の予告は現状の捕捉からは作れない。

### 4-5-4. テスト

| 対象 | 内容 |
|---|---|
| `internal/agents/claude/abort_test.go` | `UsageLimitAbort` が上限だけを拾う（一時的なレート制限・プロンプト超過・認証・接続断・通常完了は偽） |
| `session_status_test.go` | `turnEndLabel` の 4 ケース、および実物の転写を植えた hook 経路で失敗の理由がブリッジ本文に乗ること（修正を外すと落ちることを確認済み） |
| `rate_limit_resume_test.go` | メニュー無しでもエピソードが開き通知が 1 回だけ出ること・キーを送らないこと、時刻の材料（banner / capture）× 形（メニュー有無）の 4 組 |

## 5. 積み残し

- 対象は claude TUI のみ（`isApiErrorMessage` は claude 固有）。他 TUI 種別は別シグナルが要る。
- 会話に紐付いていない（Console 起動の）セッションは §3-4 の自動再開の対象外。通知と Console
  表示は出るので可視化の穴は無い（§4-4 の上限再開は会話の有無に依らず効く）。
- 再開プロンプトの言語はオペレーター判断。セッション毎の言語を持てば決定的にできる
  （§4-4 の再開文は表示言語 `uiLocale` を使う — 同じ穴の別の埋め方）。
- 上限で停止したセッションを Console から**手で**復帰させる導線は無い（自動解除に任せるか、
  ペインで選ぶ）。増枠依頼（選択肢 2）は課金判断を含むので、出すならワンクリック実行では
  なく明示的な確認付きで。
- 予約した再開時刻を Console のセッション行に出していない（定時実行の一覧には出る）。
- モデル別上限（§4-5）は**自動再開しない** — リセット時刻を決める材料が無い（バナーが無く、
  statusline 捕捉は別の窓）。復旧はモデル切替か `/usage-credits` で、どちらも課金・選択の
  判断を含むので自動化しない。通知（`rate-limit-reached`）と失敗理由つきの完了報告までが範囲。
- `error:"rate_limit"` は文言非依存の材料だが、まだ分類には使っていない（`abortRecord` は
  `apiErrorStatus` までしか読まない）。英文言のドリフトに備えるならここが次の一手。
