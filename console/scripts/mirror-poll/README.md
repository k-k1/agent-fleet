# mirror-poll — 開いたままのミラーが静止中に払う費用の計測

`npm --prefix console run mirror:poll`（先に `npm --prefix console run build` が必要）

## 何を守っているか

ミラーは画面に出ているあいだ `GET /sessions/{name}/messages` を叩き続ける。スマホではこれが
そのセッションの固定費で、リクエストのたびに無線がアイドルへ落ちられない。しかも応答には転写
**全体**の集計（`files` / `tasks` / `answers`）が動いていなくても毎回乗る——実在の転写に本番の
`Collect*` を掛けた実測で、`files` 21〜53 件・生 5.3〜13.3 KiB・gzip 後 0.86〜2.6 KiB。受け手側も
同じ内容を state に流していたので、`setTasks` / `setFiles` / `setQueuedPrompts` が毎回新しい配列を
渡し、会話全体が `groupTurns` され再描画されていた。working 中は 1.2 秒に 1 回＝ほぼ毎秒。

対処は 2 つで、この検査はその両方を見る。

- 応答が前回と **verbatim で同一なら state 適用ごと飛ばす**（`MirrorView.tsx` のポーリング）
- 同一が続いた回数で間隔を上げる梯子（`src/features/mirror/pollCadence.ts`）

## 何を測っているか

実バンドルを headless Chromium（素の CDP・Playwright なし・API は `../mirror-scroll/stub.mjs`）で
スマホ幅 390×844 に開き、**静止したセッション**を既定 45 秒眺めて

| 指標 | 取り方 |
|---|---|
| リクエスト数と間隔 | `Network.requestWillBeSent`＝端末が実際に送った分 |
| メインスレッドの仕事 | `Performance.getMetrics` の `ScriptDuration` / `RecalcStyleCount` / `LayoutCount` |

判定は「45 秒の予算内（梯子は 5+2+1＝8 回、固定 3 秒なら 15 回）」かつ「間隔が実際に広がった」。
数だけだと止まっていても緑になるので、広がりの方を必ず併せて見る。

## 効くことの確認方法

`pollCadence.ts` を入れる前の bundle で走らせると赤になる。実測（2026-09-12・CPU スロットル無し）:

| bundle | polls / 45s | 間隔 | ScriptDuration |
|---|---|---|---|
| 修正前 | 15 | 3 秒固定 | 2.35 s |
| 修正後 | 8 | 3/3/3/8/8/8/8 秒 | 1.41 s |

梯子そのものの単体試験は `src/features/mirror/pollCadence.test.ts`。
