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
6. `working` — **走行中のセッション**。他の 5 本はすべてアイドルの転写なので、返信が進行中の面は
   一度も踏まれていなかった。巨大な作業過程を持つ返信が畳まれたあと、読者を**最終回答の先頭**
   （返信完了時にミラー自身が連れて行く位置＝畳んだ作業過程の真下）に停め、ポーリング 8 回ぶん
   見張る。**作業過程の畳み込みが消えないこと**と**読者が動かないこと**が契約。作業過程は
   `workSplit` が境界を見つけている間しか disclosure の中に入っておらず、境界が消えた回は
   **inline＝全高で描かれる**ので、これは「ちらつき」ではなく数万 px の伸縮になる。
7. `paging` — **「以前の会話を読み込む」**。これも従来は踏めなかった（スタブが常に
   `firstLine: 0` / `hasMore: false` を返していた）。前置された頁が**遅れてレイアウトされる間**、
   読者が同じ内容の上に留まることを見る。前置の位置補正だけが「コミット直後に測った
   scrollHeight の差」という**一発測定**で、その時点で前置ターンの本文はまだ空だった。
8. `readup` — **末尾から上へスクロールし続ける**。`paging` はボタンで（＝止まった視点から）1 頁だけ
   読み込むので、**センチネルが「動いている視点」で発火する経路**と、**頁の境目がアシスタントの
   ブロックの内側に落ちる**形は踏めていなかった。スタブの新しい摘み 3 つがその形を作る:
   `--split`（返信を 1 パーツ 1 行で書く＝実物の jsonl）、`--asks`（利用者のプロンプトを N 返信に
   1 回だけ＝自律的に長く走った区間。窓の中にプロンプトが 1 つも無くなる）、`--longans`（畳んだ
   あとも背の高いブロック）。契約は 3 つ: **読んでいたブロックが別の idx で描き直されないこと**
   （＝再マウントされないこと）、**誰も押していないのに開いた作業過程が増えないこと**、
   **上へ動かしているのに頭出しのターンが後ろへ進まないこと**。
9. `typing` — 末尾に貼りついたまま**コンポーサーに長い下書きを書く**。入力欄が縦に伸びていても、
   1 打鍵ごとに転写が末尾から浮いてはいけない（実測: 修正前は 1 打鍵目で 154px 浮き、「最新へ」
   まで出た）。**`working` / `paging` / `readup` / `typing` の 4 本は `.mirror-body` の `overflow-anchor` を
   切ってから見る** —
   Chromium はスクロールアンカリングで浮きを打ち消してしまい、素の headless では壊れたビルドでも
   3/3 緑になる。アンカリングは仕様上の保証ではなく、持たない/抑止されたエンジンでは素通しで
   出る（利用者の報告もそちら側）。切った状態が「アンカリングに助けられていないか」の踏み絵。

> ⚠️ **`readup` は今わざと赤い**（未修正の不具合の再現）。実測 3/3 で、頁が 1 つ入った瞬間に
> 読んでいたブロック（turn 182）が **turn 122 として描き直され**、閉じていた作業過程が**開いた
> 状態で復活**し、転写の高さが **4,427px → 91,296px**、読者は `182@-3752px` から `122@+39px`
> ＝転写の先頭へ飛ばされる。原因はブロックの**同一性**にある: `groupTurns` はブロックに
> **最初の行の idx** を付けるので、頁の境目がブロックの内側に落ちると同じブロックが別の idx に
> なり、React の key が変わって subtree ごと作り直される。畳み込みの片道ラッチも、確定した
> `WorkSplit` も、読者が押した開閉も、そこで捨てられる（`TranscriptTurn` の `work` ref）。
> 前置アンカーの宛先も同じ idx なので（`scrollTopForTurn`）、掴む相手ごと消える。

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
