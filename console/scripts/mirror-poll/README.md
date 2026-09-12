# mirror-poll — 開いたままのミラーが静止中に払う費用の計測

`npm --prefix console run mirror:poll`（先に `npm --prefix console run build` が必要）

## 何を守っているか

ミラーは画面に出ているあいだ `GET /sessions/{name}/messages` を叩き続ける。スマホではこれが
そのセッションの固定費で、リクエストのたびに無線がアイドルへ落ちられない。しかも応答には転写
**全体**の集計（`files` / `tasks` / `answers`）が動いていなくても毎回乗る——実在の転写に本番の
`Collect*` を掛けた実測で、`files` 21〜53 件・生 5.3〜13.3 KiB・gzip 後 0.86〜2.6 KiB。受け手側も
同じ内容を state に流していたので、`setTasks` / `setFiles` / `setQueuedPrompts` が毎回新しい配列を
渡し、会話全体が `groupTurns` され再描画されていた。working 中は 1.2 秒に 1 回＝ほぼ毎秒。

対処は 4 つで、この検査はその全部を見る。

- 応答が前回と **verbatim で同一なら state 適用ごと飛ばす**（`MirrorView.tsx` のポーリング）
- 同一が続いた回数で間隔を上げる梯子（`src/features/mirror/pollCadence.ts`）
- 会話のグルーピングと capability オブジェクトのメモ化＋`TranscriptTurn`・`FileChangeStrip` の
  `memo()`。作文中は 1 打鍵ごとに MirrorView が再描画されるので、会話全体を作り直していると
  **1 文字の値段が転写 1 本の値段**になる（`--mode typing` が測るのはこれ）。
- 集計の digest（`?agg=` と `aggSame`・`session_transcript_agg.go`）。**ターン実行中**は本文が
  毎回変わるので CP の 304 が効かず、そこだけ集計が乗り続ける（`--mode working`）。

## 何を測っているか

実バンドルを headless Chromium（素の CDP・Playwright なし・API は `../mirror-scroll/stub.mjs`）で
スマホ幅 390×844 に開き、200 ターンのセッションで

| 指標 | 取り方 |
|---|---|
| リクエスト数と間隔 | `Network.requestWillBeSent`＝端末が実際に送った分 |
| メインスレッドの仕事 | `Performance.getMetrics` の `ScriptDuration` / `RecalcStyleCount` / `LayoutCount` |

- 既定（`--mode idle`）: **静止したセッション**を 45 秒眺める。判定は「予算内（梯子は 5+2+1＝8 回、
  固定 3 秒なら 15 回）」かつ「間隔が実際に広がった」。数だけだと**止まっていても緑**になるので、
  広がりの方を必ず併せて見る。
- `--mode typing`: 作文欄に 30 文字打ち、**1 打鍵あたりの ScriptDuration** を見る（予算 12ms）。
  打鍵数で割るので `--keys` を変えても基準は動かない。
- `--mode working`: セッションを**ターン実行中**にして、`Network.getResponseBody` で応答の中身を
  読む。判定は「クライアントが `agg=` を送っている」「`aggSame` が返っている」「集計が本文に
  乗っていない」の 3 つ。バイト数は参考値として出す（stub は gzip しないので実配備より大きい）。

## 効くことの確認方法

修正前の bundle で走らせると赤になる。実測（2026-09-12・CPU スロットル無し・390×844）:

| bundle | idle: polls / 45s | idle: 間隔 | typing | working: 応答 |
|---|---|---|---|---|
| 何も入れる前 | 15 | 3 秒固定 | 91.1 ms/打鍵 | 30,631 B/poll・集計が毎回 |
| ポーリングだけ直した版 | 8 | 3/3/3/8/8/8/8 秒 | 91.1 ms/打鍵 | 同上 |
| 描画のメモ化まで | 8 | 3/3/3/8/8/8/8 秒 | **9.5 ms/打鍵** | 同上 |
| digest まで | 8 | 同上 | 9.5 ms/打鍵 | **22,279 B/poll・集計 0 回** |

メモ化は**渡す側が props を安定させている前提**で効く。`TranscriptTurn` の `memo()` は既定の浅い
比較なので、`caps` か `turn` を毎レンダリング作り直した瞬間に黙って無効になる——この表の 3 行目が
2 行目に戻ったら、まずそこを疑う。梯子そのものの単体試験は `src/features/mirror/pollCadence.test.ts`。
