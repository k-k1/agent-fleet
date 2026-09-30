# 128. 2 段階の停止の実装 — 契約の凍結とレーンの境界（ADR 0105）

- 依頼: #1289（親）。子: #1291（出どころの配管、PR #1295）、#1292（共通キュー＋7 ドライバ＋`/turn`）、#1293（Console）、#1294（利用者ガイド）。
- 仕様: [ADR 0105](../decisions/0105-stop-continues-into-the-queue.md) の決定 1〜8。ここに書くのは、その実装上の契約と分担だけ。
  ADR と食い違う事実が出たら、統合役が ADR（英日）を先に直す。
- 統合ブランチ: `issue/1292-two-stage-stop`。レーンはここから切り、統合役がマージする。

## 1. 凍結した契約（段 0）

変えたいときは統合役に `send_to_peer_session`（intent=question）で頼む。自分のレーンでは直さない。

### 1.1 出どころ（#1291、`internal/agents/driver.go`）

- `TurnInput.Origin` = `agents.Origin{Kind, From}`。Kind は注入バッジと同じ綴り: `member` / `peer` / `spawn` /
  `operator` / `schedule` / `schedule-manual` / `discord` / `slack` / `auto-resume`。From は peer の送り手と spawn の親。
- `Origin.IsMember()` は `member`・`discord`・`slack` だけ true（決定 2・4 の「利用者の入力」）。空の Kind は false。
- `KeepOnInterrupt` と `KeptOnInterrupt` は、7 ドライバが移り終えた後に統合役が消す。ドライバのレーンは読むのをやめるだけにする。

### 1.2 共通キュー（`internal/agents/turnqueue.go`、本実装済み・単体テスト 18 本）

`TurnQueue` は自分のロックを持たない。**全メソッドをドライバの `h.mu`（accept が取るロック）を握ったまま呼ぶ。**

| 場面 | 呼ぶもの |
|---|---|
| 作る | `NewTurnQueue(name, ledger, LedgerAtAccept)`（codex・opencode・muse）/ `LedgerAtTake`（copilot・cursor・kiro・lcpp）。ledger の記録はキューがする。ドライバは自分で `SeenOrRecord` しない |
| 受け付け | `Accept(in) (id, dup)`。`dup` は再送で、積まれない。対象は、ledger が見たことのある id と、LedgerAtTake でキューにある／取り出し済みの id。利用者の新しい入力はエピソードを終える |
| キューを通らない受け付け | `AcceptOutside(in) (id, dup)`（codex のネイティブ steer）。再送の判定と ledger の記録もここでするので、ドライバは `SeenOrRecord` しない。`dup` なら何も送らない。steer が失敗してキューに落ちるときは `AcceptRecorded(in)` |
| ポンプ | `Take()` → （別のターンの後ろで待つ間は `Hold(t, true)`。戻り値 `redirect` が true なら、1 回目の停止が始めかけの入力に向けて保留していた停止を、待っている先のターンへ届け直す）→ ロックの中の最後に `Commit(t)`（false なら送らない）→ ロックを外して送る → ランタイムが受け取ったら `Received(t)`（true なら停止をポンプが届ける）→ ターンが落ち着いたら `Settle(t)` |
| 受け取る前にランタイムが消えた | `Requeue(t)`（停止待ちなら false、送り直さない） |
| 停止 | `Interrupt(opts, busy) InterruptOutcome`。`busy` は、ランタイムで何かのターン（このドライバのものでも、別のクライアントのものでもよい）が走っているか。`Result` は `/turn` の応答、`Head` は取り出し済みの項目への処置 |
| 取り除く | `Remove(id)` → 項目 / `ErrAlreadyStarted` / `ErrNotQueued` |
| 捨てた入力 | `Discards()`、`DismissDiscard(id)`（直近 5 回分） |
| 後片付け（決定 8） | `DropAll()`（捨てた入力として残さない） |
| 表示 | `Items()`（messages の `queuedItems`）、`Texts()`（従来の `queuedPrompts`） |

`Head` の意味（停止する側がすること）:

- `HeadNone`: 取り出し済みの項目は無い。このキューから始めていない実行中のターン（再起動後に引き継いだもの）があれば、ドライバが止める。
- `HeadKept`: 別のターンの後ろで待っている（`Hold`）。1 回目の停止では続ける。
- `HeadCancelled`: まだ取り消せた。取り出し前でも、`busy=false` のときのキューの先頭は「始めかけの入力」として、1 回目の停止でここに入る（キューから消え、捨てた入力には入らない）。`Commit` が false を返すので、送られない。ドライバはそのターンを `TurnCancelled` にする。
- `HeadStopPending`: 確定済み。`Received` が true を返したときに、ポンプが停止を届ける。
- `HeadStopNow`: ランタイムが受け取り済み。停止する側が、その場で届ける。

「受け取った」点: muse は `turn/started`、codex は `turn/start` の応答（turnId）。ACP（copilot・cursor・kiro）は
`session/prompt` を書いた時点、opencode は `/message` を出した時点（どちらも best effort、決定 3）。lcpp は同じプロセス内なので、
`runTurn` に渡した時点。

まだ ADR に書かれておらず、統合役が決めたこと:

- 他にターンが走っていないときに取り出された入力を 1 回目の停止が止めた場合、その入力は「止めるターン」として扱う。捨てた入力には入れない。
  走っているターンを止めても、そのターンの入力が戻らないのと同じ扱い。
- 出どころが空の入力は、利用者の入力として扱わない。

### 1.3 `ThreadHandle`（`internal/agents/driver.go`）

```go
Interrupt(opts InterruptOpts) (InterruptResult, error)
RemoveQueued(id string) (QueueItem, error)
DismissDiscard(id string) bool
```

`TranscriptData` に `QueuedItems []QueueItem` と `Discards []Discard` を足した。

各ドライバは `agents.LiveHandles`（`LiveHandle(meta) (ThreadHandle, bool)`）も実装する。これは何も起動せずに、生きているハンドルだけを返す。
`/turn` の remove / dismiss_discard はこれを使う。ハンドルが無ければ、404 `not_queued` / `{"dismissed":false}` を返す。止まっているランタイムを、古いタブからの操作で起こさないため。段 0 の時点では、7 ドライバとも古い停止の上にかぶせたシムになっている。

### 1.4 wire（fixture: `workspace/agent/testdata/stop-queue-wire.json`）

- `/turn` の本文に `discard_queue`（interrupt）と `id`（remove / dismiss_discard）を足した。`turnReq` のフィールドは宣言済みで、配線は B-wire が行う。
  - `interrupt` → `{"sent","op","stop":"first"|"second"|"discard","discard":Discard|null}`。Terminal の素の interrupt は、従来どおり `{"sent","op"}` を返す。
  - `remove` → 200 `{"removed":QueueItem}`、409 `already_started`、404 `not_queued`、400 `missing_id`。
  - `dismiss_discard` → 200 `{"dismissed":bool}`（冪等）。
  - Terminal（CLI）では、`discard_queue` / `remove` / `dismiss_discard` を 409 `not_managed` で断る。
- messages:
  - `queuedItems`（`state`: `queued` / `committed` / `sent`。操作できるのは `queued` だけ）は、`queuedPrompts` と同じく working のときだけ返す。
  - `discardedInputs` は状態を問わず、ドライバが持っていれば返す。
- 比べ方: 本文は完全に一致させる。ただし `error.message` は文章なので比べない。Go 側は `internal/agents/turnqueue_wire_test.go` が型の往復を確かめている。
  sessionx の応答全体は B-wire が、Console の解釈は C が、同じファイルで確かめる。

## 2. 担当ファイルの境界

**共有ファイルを持つのは、表の 1 レーンだけ。** 表に無いファイルを触る必要が出たら、先に統合役へ question で聞く。

| ファイル / ディレクトリ | 持ち主 |
|---|---|
| `workspace/agent/internal/agents/*.go`（driver.go、agents.go、turnqueue*.go、msgledger.go） | 統合役 |
| `workspace/agent/testdata/stop-queue-wire.json` | 統合役 |
| `internal/agents/codex/**`、`opencode/**`、`muse/**` | B-drv1 |
| `internal/agents/copilot/**`、`cursor/**`、`kiro/**`、`lcpp/**` | B-drv2 |
| `internal/sessionx/session_turn.go`（`/turn` の op）、`session_transcript.go`（messages）、sessionx の新しいテスト | B-wire（完了・マージ済み。以後は統合役） |
| `internal/sessionx/session_io.go`、`session_peer_test.go`（KeepOnInterrupt の除去） | 統合役（最後に） |
| `internal/sessionx/bridge_inbound.go`、`session_handlers.go`、`session_carried.go`、`abort_resume.go`、`auth_resume.go` | 凍結（#1291 で済み） |
| `workspace/agent/testdata/wiremap.golden` | B-wire。ドライバのレーンが map リテラルを変えた場合は、各自のブランチで再生成し、統合役がマージ後にもう一度再生成する |
| `console/**`（client.ts、MirrorView.tsx、parts/、i18n の mirror.ts 英日、テスト） | C |
| `docs/decisions/0105*`、`docs/log/128`、`guide/member/02-sessions*.md`（D） | 統合役 |

## 3. レーン

| レーン | ブランチ | 種別・モデル |
|---|---|---|
| B-wire | `issue/1292-stop-wire` | claude / Sonnet 5.5 |
| B-drv1 | `issue/1292-stop-drv-codex-opencode-muse` | claude / Opus 5.5 |
| B-drv2 | `issue/1292-stop-drv-acp-lcpp` | claude / Opus 5.5 |
| C | `issue/1293-console-two-stage-stop` | claude / Opus 5.5 |
| レビュー | — | codex / gpt-6-sol（途中）、Opus 5.5（完了判定の直前）。編集はさせない |

## 4. 経過

- 段 0: #1291 を PR #1295 でマージ。契約を凍結し、`TurnQueue` を本実装した。
- B-wire: マージ済み（`/turn` の 3 op、messages の 2 項目、fixture の全件を再生、陽性対照 8 種）。
- codex レビュー（1 巡目、共通キューと wire）で 4 件の指摘があり、すべて直した。
  - 🔴 受け付けた後、ポンプが取り出す前に 1 回目の停止が来ると、始めかけの入力が停止を擦り抜けて始まっていた。`Interrupt` に `busy` を足して直した。
  - 🟡 LedgerAtTake で `Requeue` した項目を、次の `Take` が再送とみなして捨てていた。`Take` の時点で記録済みの項目には印を付けて、判定を免除した。
  - 🟡 ネイティブ steer の `Accepted` は再送でもエピソードを終えていた。`AcceptOutside` に置き換え、再送の判定と ledger の記録もここでするようにした。
  - 🟡 remove / dismiss_discard が `Resume` で止まっているランタイムを起こしていた。`LiveHandles` を足して直した。
- B-drv2（copilot・cursor・kiro・lcpp）をマージした。陽性対照は 35 件。
  - ACP は、`session/prompt` を書いた時点で受け取ったとみなす（`callWritten`）。
  - 報告された指摘への対応: LedgerAtTake でキューにある id の再送が 2 回積まれ、捨てた入力にも 2 回出ていた。`Accept` が `dup` を返すようにした。
- B-drv1（codex・opencode・muse）をマージし、`KeepOnInterrupt` / `KeptOnInterrupt` を消した。
- codex レビュー（2 巡目、7 ドライバ）の指摘:
  - 🔴 muse で、`turn/start` の応答（queued）が返る前に 1 回目の停止が来ると、停止待ちが保留された入力に残り、後で始まったときにそれを止めていた。
    `Hold` が `redirect` を返して、その停止を先のターンへ振り替えるようにした。
  - 🟡 ACP と lcpp の accept が `dup` を無視していた（アイドル時の再送で状態が queued のまま残る、送り手へ held と誤報する）。レーンに差し戻した。
