# 47. 中断ターンの検知と自動再開

決定は [ADR0030](decisions/0030-turn-abort-auto-resume.md)。

## 1. 何が壊れていたか

claude の TUI セッションで API エラーがターンを切ると **Stop フックが鳴らない**。
その結果 `working → idle` の遷移が記録されず、ペインだけが待機プロンプトに戻る。

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

## 5. 積み残し

- 対象は claude TUI のみ（`isApiErrorMessage` は claude 固有）。他 TUI 種別は別シグナルが要る。
- 会話に紐付いていない（Console 起動の）セッションは自動再開の対象外。通知と Console
  表示は出るので可視化の穴は無い。
- 再開プロンプトの言語はオペレーター判断。セッション毎の言語を持てば決定的にできる。
