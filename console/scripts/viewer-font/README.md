# viewer-font — ビュアーの Markdown がフォントサイズ設定に追従するかの検証

```bash
npm --prefix console run viewer:font
node console/scripts/viewer-font/check.mjs --sizes 13,26 --screenshot /tmp/mdfont.png
```

ビルド不要（`console/dist` も CP も Agent も不要）。終了ステータスが検査結果。

## 何を守っているか

**設定＞表示＞ファイルビュアー＞フォントサイズ**（`--viewer-size`）を変えても、ペインが Markdown を
表示しているときだけ文字が動かなかった（2026-09-07）。

- `.markdown` が `font: 14px/1.7` と**固定値**を持っていた。コード表示（`codegrid.css`）・plain
  （`plain.css`）・diff（`diff.css`）はいずれも `var(--viewer-size)` を読むのに、Markdown だけが
  読んでいない。var は要素まで届いているのに文字が動かない、という形の故障。
- フェンスコードブロック（`.markdown pre code`）も同じく 12px 固定。
- 見出しパンくず `.md-sticky` は `.markdown` ではなく**スクローラの子**なので本文サイズを継承せず、
  body の 14px のままだった（本文が 14px 固定だった間はたまたま一致していた）。

キーボードのズーム（Ctrl +/−）も Markdown ペインでは `viewerSize` を動かす（`lib/viewFont.ts`）ので、
設定値だけ動いて見た目が変わらない、という同じ症状になる。

## 4面まとめて測る

同じ `MarkdownView` を3つの面が別々のサイズで描く。**「他の面が動いていないこと」も同じくらい契約**で、
共有ルールを em で相対化して直すと（ビュアーは直るが）ミラーとチャットまで一緒に動いてしまう。

| 面 | サイズの出どころ | 期待 |
|---|---|---|
| ファイルビュアー `.fileview` | `--viewer-size`（インライン style） | 本文・見出し・インラインコード・フェンス・パンくずが全部追従 |
| ミラー `.mirror-turn` | `--chat-size` | `--viewer-size` を動かしても不動 |
| アシスタントチャット `.chatview` | 吹き出しから継承（`font: inherit`） | 同上 |

ミラーが `--chat-size` に載っていることは**陽性対照**でもある。これが動いている＝ハーネスがフォント
サイズの差を実際に見分けられている、ということなので、「ビュアーが動いた」が測定の artefact でない
と言える。修正前の CSS に対して実行すると、ビュアーの5項目だけが NG になり他3面は緑のまま（実測）。

## なぜ unit テストではないのか

原因が **CSS のカスケード**そのもので、jsdom は外部スタイルシートのカスケードを計算しない。一方で
React も CP も要らない——`console/src/**/*.css` は素の CSS なので、`main.tsx` と同じ順で**実ファイルを
そのまま `<link>` すれば**出荷物と同じカスケードで computed style を測れる。

- **CSS は 1 枚も落とさず全部 link する**（`pane-heads` と同じ流儀）。落とすと「そのルールが無い」と
  誤報告する。読み込みに失敗した1枚があれば検査自体を NG にする。
- 唯一の例外が `src/marp-themes/*` で、これは MarpView が marp-core 経由で Shadow DOM に注入する
  デッキテーマ。`@import "default"`（marp-core の中にしか無い）を持つので link すると 200 でも
  onerror になる。このページのカスケードとは無関係なので除外している。
