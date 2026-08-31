# Mirror scroll-landing harness

Asserts what the mirror's scroll must do, against the **real** Console bundle in headless
Chromium:

1. opening a session that already has history lands at the **true bottom**;
2. expanding a 作業過程 disclosure while parked there **keeps the reader's place**;
3. a wheel-up **stops** auto-follow, shows 最新へ, and does not yank them back;
4. `restore` — 途中まで読んで別のセッションへ移り、戻ってくると**同じ位置**に戻る
   （`scrollMark`）。同時に「別のセッションは自分の末尾に着地する」＝位置の記憶がセッションを
   跨いで漏れないことも見る;
5. `swipe` — スマホ幅＋タッチで**横スワイプによるセッションの持ち替え**をやり、末尾に着地して
   そこに居座ることを見る。
6. `typing` — 末尾に貼りついたまま**コンポーサーに長い下書きを書く**。入力欄が縦に伸びていても、
   1 打鍵ごとに転写が末尾から浮いてはいけない（実測: 修正前は 1 打鍵目で 154px 浮き、「最新へ」
   まで出た）。**このシナリオだけ `.mirror-body` の `overflow-anchor` を切ってから見る** —
   Chromium はスクロールアンカリングで浮きを打ち消してしまい、素の headless では壊れたビルドでも
   3/3 緑になる。アンカリングは仕様上の保証ではなく、持たない/抑止されたエンジンでは素通しで
   出る（利用者の報告もそちら側）。切った状態が「アンカリングに助けられていないか」の踏み絵。

`swipe` は 390×844 / `Emulation.setTouchEmulationEnabled` で走る。指の送りが大きく速いのは
意図で、Chromium は連続した `touchMove` を合成して間の座標を返すため、細かく送ると 1 イベント
ぶんの `dx` が縮み、`ROTATE_DIST=70px` を `LONG_PRESS_MS=500ms` 以内に越えられずスワイプが
成立しない（実測: 120ms 刻みでは 600ms 目にようやく 70px を越え、候補が取り消されていた）。

```bash
npm --prefix console run build        # console/dist must exist (the real bundle)
npm --prefix console run mirror:scroll
node console/scripts/mirror-scroll/check.mjs --runs 5 --scenario mermaid
```

Exit status is the check.

## Why this is not a unit test

The failure is a layout-timing one, so jsdom cannot see it. The transcript's turn bodies
are filled by `MarkdownView` into `innerHTML` from a **passive** effect, so at the moment
`MirrorView` pins the bottom from its layout effect the turns are still empty —
essentially all of a transcript's height arrives afterwards, in several steps (parse →
highlight → mermaid → image decode → fonts). Anything that decides "is the user at the
bottom" from geometry sampled when the **scroll event** is dispatched can mistake that
growth for the user scrolling up, and every re-pin path is disarmed from then on.

`--cpu 4` (the default) throttles the main thread, because that window between the
programmatic scroll and its event is exactly what a modest or busy machine widens. Without
it a broken build often lands correctly by luck on an idle headless run — which is why the
symptom always read as intermittent. Measured: with throttling the pre-fix bundle failed
3/3 with the view stranded 1246 px above the end; the fixed one passes.

## How it works

- `stub.mjs` serves `console/dist` and answers the Control Plane's API surface from the
  screenshot harness's fixtures (`../shots/fixtures.mjs`) — no CP, no workspace agent, no
  Docker. Only the transcript endpoint is its own: a synthetic idle transcript whose shape
  is the parameter (`--turns`, `--images`, `--imgdelay`, `--mermaid`).
- `check.mjs` drives headless Chromium over raw CDP (Node's global `WebSocket`, no
  Playwright/Puppeteer — same technique as `../shots/capture.mjs`), seeds the localStorage a
  returning user would have, and **clicks the session's row in the left pane** rather than
  deep-linking, because opening from the rail is the reported path.
- Scenarios live at the top of `check.mjs`. `switch` seeds the pane with a *different*
  session first, so a reused pane (the D&D / open-another-session path) is covered too;
  its transcript uses different line indices, or the incoming session would inherit the
  previous one's anchored reply and hide a bug.
- 浮くピル（「最新へ」「返信を頭から」）は同じ `.mirror-jump` なので、着地の判定は
  `.mirror-jump:not(.mirror-jump-top)` で数える。ピルは**本文の先頭**に置いてある — 末尾に
  置くと、はみ出したボタンぶん（実測 12px）がスクロール可能領域を伸ばし、末尾に貼り付いて
  いるのに `gap=12px` になる（「返信を頭から」は末尾でも出るので、これが表に出た）。

## 再現しなかったこと（残しておく）

スワイプの指の `pointerdown` が `noteInteraction` の 600ms を武装したまま次のセッションへ
持ち越される、という筋は塞いだが、**その 1 行を戻したビルドでも `swipe` は 3/3 で末尾に着地
した**（`--cpu 4` でも `--cpu 1` でも、画像なしの小さい transcript でも同じ）。fetch とレンダが
毎回 600ms より長くかかり、窓が閉じたあとの成長で再ピンが効いてしまうため。ここで赤くならない
＝実機の「位置が不定」の主因は別、という可能性を残す。

Fixture shapes follow the same wire contracts as the screenshot harness
(`workspace/agent/internal/transcript/transcript.go`). If one changes, the transcript
renders empty rather than failing — check the reported `turns=` count, not just the exit
code.
