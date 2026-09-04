# File viewer scroll-memory harness

ファイルビュアーが **読んでいた位置を覚えているか** を、**本物の Console バンドル**と
headless Chromium で見る（隣の `../mirror-scroll/` と同じ作り: 素の CDP、Playwright /
Puppeteer なし、CP も Agent も Docker も要らない）。

```bash
npm --prefix console run build          # console/dist（本物のバンドル）が要る
npm --prefix console run viewer:scroll
node console/scripts/viewer-scroll/check.mjs --runs 3 --scenario markdown
```

終了ステータスが検査結果。

## 見ている 4 本

| 筋 | やること | 落ちるとどう見えるか |
|---|---|---|
| `code` | コードを途中まで送り、**2 枚目のタブへ移って戻る** | 先頭に戻っている |
| `markdown` | 同じことを Markdown プレビューで | 同上（こちらは高さが遅れて確定する） |
| `pdf` | 同じことを PDF で | 同上（面が自前でスクロール位置を触る経路） |
| `modes` | **表示 ⇄ 編集**を往復する | 読む面が先頭に戻っている |

Office 文書（`.docx` 等）の簡易プレビューも同じ仕組みに繋いであるが、筋は無い
（Markdown プレビューと同じ `.md-scroll` に載る面なので、`markdown` が代表している）。

`code` / `markdown` / `pdf` はタブ表示で 1 セルに 2 枚のファイルを積んだ配置を localStorage に
seed して始める（`af.layout2.<user>.<tenant>.tabs` ＋ 表示設定の `paneLayout:"tabs"`）。
タブを踏み外して「同じ位置のまま別のファイル」を見ていないことまで確かめるため、
判定には情報バーのパス（`.fi-path`）も入っている。

送りは **本物のホイール**（`Input.dispatchMouseEvent`）。`scrollTop` への代入では
足りない —— 復元の打ち切りは「読み手が触ったか」で決めているので、入力の経路ごと
通さないと契約を見たことにならない。

## なぜ jsdom では足りないか

1. **高さが遅れて確定する。** Markdown プレビューは `MarkdownView` が `innerHTML` を
   passive effect で書き、そのあとハイライトで伸びる。付いた瞬間の
   `scrollHeight === clientHeight`（＝戻す先が無い）から始まるので、「1 回書き戻して
   終わり」の実装はここで落ちる。jsdom にはレイアウトが無く、器で与えた寸法は最初から
   最終値なので、この段差そのものが存在しない。
2. **タブ切替と `hidden` は別の経路。** タブ表示は選ばれた 1 枚しか描かない（`PaneHost`
   の `selectedView`）＝面ごと unmount されるが、表示⇄編集は `.file-viewer-shell` を
   `hidden` にするだけ。

`--cpu 4`（既定）で主スレッドを絞るのも `mirror-scroll` と同じ理由で、復元と遅延
レイアウトの競争を広げるため。編集できるファイルでは CodeMirror も上がるので、面が
出るのを固定 sleep ではなく `waitFor` で待っている（絞った状態では 5 秒に間に合わず、
`modes` だけ「面が出ていない」で落ちた）。

## 正の対照（この検査が何を捕まえるか）

位置の記憶を殺したバンドル（`scrollMemoryKey` が常に `null` を返す）を焼いて流すと:

```
[code 1/1] NG  読んだ位置 2400 → 戻り 0 → 2 秒後 0
[markdown 1/1] NG  読んだ位置 2400 → 戻り 0 → 2 秒後 0
[pdf 1/1] NG  読んだ位置 1600 → 戻り 0 → 2 秒後 0
[modes 1/1] OK  読んだ位置 2400 → 編集 → 表示 2400
```

★ **`modes` は壊れたビルドでも緑になる**（2026-09-04 実測）。Chromium は
`display:none` を跨いで scrollTop を保つので、この経路は今のところ記憶を必要としない。
残してあるのは契約を固定するため（保たないエンジン、あるいは面が作り直しになる実装に
変えた日に赤くなる）で、**退行の再現ではない**。`code` / `markdown` / `pdf` の 3 本が、
このハーネスが実際に捕まえているものである。

## CI

回していない。`dist` を焼いてから 4 本ぶんの Chromium を上げるので、`pdf:check` /
`doc:check` のような秒単位の検査とは値段が違う（`mirror:scroll` も同じ理由で手動）。
ビュアーのスクロール周りを触ったら手で流す。
