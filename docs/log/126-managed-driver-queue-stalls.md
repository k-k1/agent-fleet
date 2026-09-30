# 126. Managed ドライバのキューが止まる・追い越される・消える — muse・ACP 3 種、送り出し途中の入力と停止

- 依頼: #1258。
- 関連: [125](125-peer-message-held-behind-a-turn.md) §4（残件としてここへ回したもの）、ADR 0041（Addendum 2026-09-30）、
  docs/log/27 §12.2-4（interrupt はキューも捨てる）。

## 1. 実測（muse 1.4.0-R4161.1、`muse serve` を隔離 HOME で）

資格情報の無い HOME で `serve` を立て、`turn/start` を 3 回送って通知の順を記録した。ターンは `authRequired` で
すぐ終わるので課金は無い。プローブは一時的なテストファイルで、コミットしていない。

| 経過 | 出来事 |
|---|---|
| 1050 ms | `turn/start`（1 件目）送信 |
| 1263 ms | 1 件目の応答 `disposition: started`、`turnId` = `commandId` |
| 1264 ms | `turn/start`（2 件目）送信 ← 1 件目の `turn/started` より前 |
| 1292 ms | `session/statusChanged running` → `turn/started`（1 件目） |
| 1454 ms | `session/statusChanged idle` → `turn/completed`（1 件目、`authRequired`）。**idle が先**、同じミリ秒 |
| 1531 ms | 2 件目の応答 `disposition: queued`（ホスト側のキューに入った） |
| 1589 ms | `turn/started`（2 件目）… 以下同じ順 |

分かったこと:

1. **`turn/start` の応答は `turn/started` より先に来る**（約 30 ms）。この隙間の送信はホストのキューに入る。
   125 §3 では仕様から読んでいたが、実際に起きる。
2. **idle は `turn/completed` より先に来る。** issue にあった「idle で `pump` を呼べば止まらない」は、この順では
   `turn/completed` を処理する前に次のターンを始めてしまう。idle でターンを閉じてはいけない。
3. 新しいターンの `turnId` は `commandId` と同じ（スキーマの記述どおり）。

## 2. 真因（コードで確認）

**muse**（`internal/agents/muse/handle.go`）

- `finishTurn` は `turn/completed` を処理する**クライアントの読み取りゴルーチンの上で** `pump()` を同期的に呼んで
  いた。`pump` → `startTurn` は `turn/start` の応答を待つが、応答を届けるのはその読み取りゴルーチン自身なので、
  `callTimeout`（30 秒）まで全通知が止まり、呼び出しはタイムアウトで失敗して「queued turn failed to start」の
  ログ 1 行で終わっていた（ホスト側ではターンが始まっている）。issue に無かった穴で、既存の
  `TestSecondSendQueuesAndDrains` は `turn/start` が**届いた**ことしか見ていなかったので気付けなかった。
- idle で `running=false` にしていたので、idle と `turn/completed` の間の送信は古いキューを追い越して即座に始まる。
- ホストが死ぬと `watch` は `alive/running` を落とすだけで、キューは残るが誰も流さない。次の `Send` は
  `running=false` を見て古い項目より先に始まる。
- `pump` での開始失敗はログ 1 行で入力が消える。
- `running` は `turn/started` で初めて立つので、§1-1 の隙間の送信はホストのキューに入り、ドライバは見せも止めもしない。
- `handle_test.go` の送信系テストは HOME を持たず、実 HOME の `~/.local/state/agent-fleet/muse-msgledger/` に
  `test-*.json` を書いていた（5 件あった。消した）。

**copilot / cursor / kiro**（3 本とも同じ `pump`）

- 子が死ぬと `pump` は `!alive` で抜けてキューを残し、`spawn` は再開しない。次の `accept` まで流れない。
- **issue に無かった取りこぼし**: 子が死ぬと ACP クライアントは読み取りループの終わりで閉じ（`markClosed`）、
  実行中の `session/prompt` がエラーで戻る。`alive` を落とす `runtimeLost` は `watch` が子の回収後に呼ぶので、その間
  `alive` はまだ true。ポンプは次の項目を取り出し、ledger に記録し、閉じたパイプへの書き込みで失敗させていた。
  ledger に載っているので再送も「既に見た」で捨てられる。

**送り出し途中の入力に停止が届かない**（125 §4 でここへ回したもの）

- opencode: ポンプは入力をキューから出して `running` を立ててから、別のクライアントのターンを `waitIdle` で待つ。
  そこで停止すると、そのターンが中断され、入力はそのあと送られる。
- codex: 取り出してから `turn/start` の応答（または `turn/started`）までは `turnID` が空で、`Interrupt` は何も送らない。
- muse: §1-1 の隙間は同じ形。

## 3. 直したこと

**muse**

- `starting`（応答待ちの `turn/start` の `commandId`）を持ち、`turn/started` が来るまで忙しいとみなす。隙間の送信は
  ドライバのキューに入り、Console に見え、停止が届く。`turn/started` の `commandId` が一致したら解除する（空の
  `commandId` も解除する）。応答が `steered` のとき、`turn/completed` がその id を名指ししたとき、ホストが死んだ
  とき、送信が失敗したときも解除する。`queued` の応答（この handle が始めていないターンの後ろ）は、ホストが後で
  その id で `turn/started` を出すので保持する。
- idle はターンを「完了」と表示するだけで、キューを流すのは `turn/completed`。`turn/completed` が来ないときの保険と
  して `settleIdle` が 3 秒後にターンを閉じる（`runGen` が動いていれば何もしない）。`settleIdle` が閉じたあとに
  遅れて届いた `turn/completed` は、今のターンでも応答待ちでもない id なら無視する。
- `pump` は読み取りゴルーチンの外で回す（`go h.pump()`）。ループにして、キューが空になるか忙しくなるまで進む。
  - ホストが閉じて失敗した入力（`msp.ErrClosed`）は先頭に戻し、再起動を待つ。
  - 生きているホストが拒否した入力は、同じ入力を送り直しても拒否されるので捨てる。ただしログだけでなく
    `TurnFailed` として表に出し、次へ進む。
- `accept` は、実行中・応答待ち・**古いキューがある**のどれかなら積む。`spawn` は成功したら `go h.pump()` を呼ぶ。
- 停止: 応答待ちの入力は「まだキューにある」ものとして扱う。印の無い入力には `stopStarting` を立て、
  `turn/started` が来た瞬間に `turn/interrupt` を送る。`KeepOnInterrupt`（peer）は ADR 0041 どおり残す。
- `ManagedBusy`・`AbortManaged`・`DropHandle` は `starting` も「実行中」と数える。
- `main_test.go` の `TestMain` でパッケージ全体の HOME を一時ディレクトリにした。`newTestHandle` で `t.Setenv` すると、
  自分で HOME を用意してから呼ぶテスト（fork・mcp・usage）の HOME を上書きして壊すため。

**copilot / cursor / kiro**

- `pump` は `cl == nil || cl.dead()` なら取り出さない。閉じたクライアントは、`watch` がハンドルを落とす前でも死んだものとして扱う。
- `resumePump` を足し、`spawn` の成功時に呼ぶ。死んだ子が残したキューは再起動と同時に、順番どおりに流れる。

**送り出し途中の入力への停止**（方針は変えず、キューにある入力と同じ規則を当てる）

- opencode: `held` / `heldKeep` / `dropHeld`。`waitIdle` で待っている入力には、停止時に（peer 以外なら）
  `dropHeld` を立てる。ポンプは待ち終わったら送らずに `TurnCancelled` にする。別のクライアントのターンを中断
  する動き（`running` の間は `serveAbort`）は変えていない。`abortAsked` のリセットを `runTurn` の先頭から
  `releaseHeld` へ移した。こうすると、待ち終わってから `/message` を送るまでの間に来た停止も `runTurn` が拾い、
  送らずに終わる。
- codex: `startKeep` / `stopStart`。`turnID` が空の間の停止は `stopStart` を立て、`runTurn` が応答で id を得た
  ところで `turn/interrupt` を送る。peer は残す。

## 4. 採らなかった案と残件

- **idle で `pump` する**（issue の案）: §1-2 の順で壊れる。
- **ホスト側に積まれたターンを `turn/unqueue` で外す**（#1268 レビューの案）: 隙間を塞いだので、ホストのキューに入る
  のは、この handle が始めていないターンの後ろに積まれた場合だけになる。`turn/unqueue` の実機の挙動は測っていない。
- **ACP で、閉じたパイプへの書き込みに失敗した入力を先頭に戻す**: ledger から消す口が要る。§3 の `dead()` 確認で
  窓はほぼ閉じた。残るのは、確認と書き込みの間に子が死んだ場合だけで、その入力は `TurnUnknown` で終わる（黙って
  消えはしない）。
- opencode: 別のクライアントのターンを停止が中断するかどうかは変えていない。キューが空のときは中断せず、入力を
  待たせている間は中断する、という不揃いは残る。`serveAbort` が `/message` より先に serve に着く HTTP 上の窓も残る。
- **停止の意味そのもの**（1 回目でターンだけ止めてキューを続けるか、利用者の入力を入力欄に戻すか、2 回目で全部
  止めるか）: ADR で決める #1282。

## 5. 検証

- 新しいテスト: muse 11 本（`queue_test.go` 9、`handle_test.go` の隙間 2）、copilot / cursor / kiro 各 1 本、
  opencode 2 本、codex 2 本。
- 陽性対照（修正を 1 つずつ外し、対応するテストが落ちることを確かめてから戻した）:
  - muse 8 種: 読み取りゴルーチン上の pump、idle での解放、古いキューの確認、隙間、到着時の停止、先頭への戻し、古い完了のガード、peer の保護。
    - 古い完了のガードは、最初のテストでは外しても通った。次のターンが `turn/started` まで進んだあとに古い完了が
      届く形に直し、落ちることを確かめた。
  - ACP 3 種 × 2（`dead()` の確認、`resumePump`）、opencode 2、codex 2。
- ledger の漏れ: 修正前は隔離 HOME で走らせると `muse-msgledger/test-*.json` が 5 件でき、修正後は実 HOME に 0 件。
- `go vet ./...`・`gofmt -l`（空）・`go test -p 2 ./...`（workspace/agent）。
