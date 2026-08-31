# 82. File ペインで PDF と Office 文書をプレビューする

- 状態: **P0（PDF）・P1（Word / Excel / PowerPoint の簡易プレビュー）実装済み**（2026-08-31）。
  実測はすべて開発 Workspace の headless Chromium で行い、再現コマンドを各節に残した。
  検証ハーネスは `npm --prefix console run pdf:check` と `doc:check`。
  設計判断は [decisions/0063](decisions/0063-document-preview.md)。
- 関連: [44-markdown-code-editor.md](44-markdown-code-editor.md)（File ペインの面と編集・保存機構。
  本機能は**その面を増やす**形で載る） / [65-drawio-viewer.md](65-drawio-viewer.md)（同じ「バイナリを
  ペインの中で読む」系の先例。同梱物の扱いはこれに倣った） / [28-i18n.md](28-i18n.md) /
  [35-packaging.md](35-packaging.md)（同梱物と配布サイズ）

---

## 82.1 いま何が起きているか

`api/fs/file` は PDF や `.docx` を読むと `binary: true` だけを返す（`workspace/agent/fs_file_api.go`
の `readFSFile`：NUL バイトか非 UTF-8 を見つけた時点でバイナリ）。File ペインはそれを受けて

```
(バイナリ, 86.8 KB)
```

とだけ出す。**中身を見るにはダウンロードしてローカルのアプリで開くしかない**。ブラウザで動く
Console の中で、リポジトリに置かれた PDF や配布資料をその場で読めない、というのが穴である。

画像はすでに読める（`imageFormat()` → `ImageView`）。同じ「拡張子で面を選び、生バイトは
`api/fs/download` から取る」形が、そのまま PDF にも Office にも使える。

## 82.2 実測：どのライブラリなら読めるのか（2026-08-31）

日本語・表・埋め込み画像・書式を持つ標本を自作し（`docx`／`exceljs`／`pptxgenjs` で生成、PDF は
chromium の `--print-to-pdf`）、候補を実ブラウザで描かせて目視した。

| 形式 | 候補 | 結果 | 追加サイズ (gzip) | ライセンス |
|------|------|------|------------------|-----------|
| PDF | **pdfjs-dist 6.3.289** | ✅ 2 ページとも忠実。日本語も表も出る | 126 KB ＋ worker 366 KB | Apache-2.0 |
| DOCX | docx-preview 0.4.0 | ✅ ページ体裁・見出し色・表・埋め込み画像まで再現 | 49 KB | Apache-2.0 |
| DOCX | mammoth 1.12.2 | △ 体裁を捨てて意味構造だけ | 122 KB | BSD-2 |
| XLSX | SheetJS 0.20.3 | ✅ 書式済みテキスト（`¥1,200`・日付）まで出る | 120 KB | Apache-2.0 |
| XLSX | exceljs 4.4.0 | △ 値と塗り色は取れるが**表示書式の適用は自前**（`45300` のまま） | 265 KB | MIT |
| PPTX | pptx-preview 1.0.7 | ❌ **黒い矩形だけ。例外も出ず無言で失敗** | 421 KB | ISC |
| PPTX | @jvmr/pptx-to-html 1.1.1 | ✅ 位置・箇条書き・図形・表・画像を再現 | 44 KB | MIT |
| 全部 | **@firecrawl/anydoc-wasm 0.2.4** | ○ Markdown へ変換（**体裁は落ちる**）。init 38ms・変換 1ms 未満 | WASM 2.9 MB | MIT |

読み取り:

- **PDF に代替は無い。** PDF は見た目そのものが情報なので、テキスト化では代わりにならない。
  pdf.js は Mozilla 製で 10 年以上動いている唯一の実用解。
- **PPTX の「見た目の再現」は地雷原。** いちばんそれらしい名前の `pptx-preview` は**無言で
  失敗する**（黒い canvas だけを残す。例外を投げないので、検知にはピクセルを見る必要がある）。
  唯一まともに描けた `@jvmr/pptx-to-html` は公開 1 年・単独メンテナの新しいパッケージ。
- **anydoc は「読むための変換」としては全形式で優秀。** 依存 1 つで docx / xlsx / pptx を賄え、
  出力は GFM なので Console の MarkdownView にそのまま載る。ただし図形の位置・セルの色・
  ページ体裁は落ち、画像は alt text になる（`toDocument()` なら `assets` で実体も取れる）。
  PDF は `toMarkdownBytes()` のみ対応で `toDocument()` は `unsupported` を投げる。
  **スキャン PDF は OCR が無いので `needsOcr` で拒否**される。

再現:

```
mkdir /tmp/doclab && cd /tmp/doclab && npm i docx exceljs pptxgenjs \
  pdfjs-dist docx-preview mammoth @e965/xlsx pptx-preview @jvmr/pptx-to-html @firecrawl/anydoc-wasm esbuild
# 標本を作り、各ライブラリを esbuild で 1 枚にまとめ、CDP で開いてスクリーンショットを撮る
```

**サーバ側変換（LibreOffice → PDF）は選べない。** Workspace コンテナに `soffice` は無く、root が
無いので入れられない（workspace-notes「What is not available」）。したがって描画は全部ブラウザ側。

## 82.3 P0：PDF（実装済み）

`console/src/features/viewer/PdfView.tsx`。codeleaf の `PdfViewer`（Android の `PdfRenderer`・
同時 1 ページ・Mutex で直列化・−/＋ で 1〜3 倍）を Web に写した。違いは 1 点で、**ページ送りでは
なく縦の連続スクロール**にした（ペインは細長く、送りボタンよりスクロールの方が速い）。

- 面の選択は拡張子だけ（`isPdfFile()`）。`api/fs/file` は中身を返さないので、**PDF の分岐は
  バイナリの分岐より前**に置く。逆にすると、いつまでも「(バイナリ, …)」のままになる。
- バイト列は `api/fs/download` を **pdf.js 自身に取りに行かせる**（`getDocument({url})`）。Agent は
  `http.ServeContent`、CP のプロキシはヘッダをそのまま中継するので Range が通り、大きな PDF でも
  全体を JS のメモリに載せずに読み始められる。
- 描画は 1 枚ずつ直列。倍率が変われば走っている描画を `cancel()` して捨てる。
- 画面から遠いページは `canvas.width = 0` で面積を返す（長い文書を端まで送るとページ数ぶんの
  ビットマップが積まれるため）。
- 情報バーは `PDF` タグとページ数。編集面は出さない（テキストではない）。

### 82.3.1 同梱物：cMap と標準 14 フォント

pdf.js の `cmaps/`（1.7 MB）と `standard_fonts/`（820 KB）はパッケージの中にディレクトリごと
入っており、バンドラが辿れない。`vite.config.js` の `afPdfjsAssets` プラグインが
`dist/assets/pdfjs/<version>/` へ複製し、実行時は `src/features/viewer/pdfjs.ts` が URL を組む。

- **同梱しないと日本語 PDF が壊れる。** フォントを埋め込んでいない PDF（`UniJIS-UCS2-H` などの
  符号化）は cMap 無しでは文字が出ない。日本語文書では珍しくない。
- **パスに版を入れる。** `assets/` 配下は CP が `immutable` で配る（`control-plane/routes.go` の
  `registerStatic`）。版を上げれば URL ごと変わるので、古い cMap が居座らない。
- dist は 15 MB → **18 MB**（+2.5 MB）。バンドル（主チャンク）は変わらない：pdf.js 本体は
  `assets/pdf-*.js`（144 KB gzip）として分離し、PDF を開いたときだけ落ちてくる。

### 82.3.2 実測で見つかった罠

いずれも `console/scripts/pdf/check.mjs` が捕まえた。**DOM を見るだけでは 2 件とも「正常」に見える**。

1. **ref コールバックを毎レンダー作ると、描画済みの記録が毎スクロール消える。**
   `ref={(el) => …}` はレンダーのたび別の関数になるので、React は毎回「前のを外して付け直す」。
   外す側で `renderedRef.delete(i)` していたため、描き終えたページが即座に「未描画」に戻り、
   スクロールのたび全ページを描き直していた。画面は正しく見えるので、気づけるのは
   「画面外の canvas が解放されない」という別の症状を検査したときだけだった。
   → `useCallback([])` で同一性を固定し、後始末は ref のクリーンアップ関数に置く。
2. **`align-items: center` は、ペインより広く拡大したページの左端を永久に隠す。**
   flex の中央寄せは、はみ出した側にスクロールで到達できない（実測：125% で見出しの左が切れる）。
   → `align-items: safe center`（収まるときだけ中央、溢れたら先頭寄せ）。

### 82.3.3 検証ハーネス

`npm --prefix console run pdf:check`（`--screenshot <path>` で絵も残る）。CP も Agent も要らない。
製品コードの `PdfView` を React ごと esbuild で 1 枚にまとめ、素の http サーバで配り、CDP で叩く。
標本 PDF はその場で chromium が作るので、リポジトリにバイナリを置かない。

見ているもの（10 項目・すべて OK）:

```
OK   6 ページが並ぶ / onMeta がページ数を返す
OK   1 ページ目に文字が描かれている — ink=3.36%     ← 画素を読む。canvas は「白いまま」でも DOM 上は正常に見える
OK   最初は 1 ページ目 / スクロールでページ番号が進む
OK   ＋ でページが大きくなる — 976 → 1220
OK   拡大しても読んでいるページが変わらない
OK   拡大してもページの左端に届く — left=12px
OK   画面外のページは canvas を解放する — freed=2
OK   壊れた PDF で理由が出る
```

**限界**: headless の SwiftShader は GPU 依存の描画差を再現しない。フォント置換の見え方と
実機での体感は、実ブラウザでの目視が要る。

## 82.4 P1：Word / Excel / PowerPoint（anydoc で簡易プレビュー）

`console/src/features/viewer/DocPreview.tsx`。方針は **「見た目の再現」ではなく「読むための変換」**。
anydoc（Rust → WASM）で GFM へ変換し、既存の MarkdownView に載せる。理由は
[decisions/0063](decisions/0063-document-preview.md)。

依存 1 つで 3 形式（＋ odt / rtf / epub）を賄えること、出力が Markdown なので既存の作法
（リンク解決・表・コピー・読み上げ）がそのまま効くことが理由。対象は `.docx/.docm/.doc/.odt/.rtf`・`.xlsx/.xlsm/.xls/.ods`・`.pptx/.pptm/.ppsx/.ppt/.odp`・`.epub`
（`filemeta.ts` の `documentFormat()`）。**`.csv` は入れない** —— すでにテキストとして読めており、
変換に回すとコードビューも編集面も失う。

- 面の頭に「簡易プレビューです。書式・図形・画像は再現されません」と出し、原本のダウンロード導線
  （情報バー）を隣に残す。**再現しているように見せる方が、書式の落ちた表を鵜呑みにされるぶん危ない。**
- 変換できない理由は黙らずに出す: パスワード付き（`encrypted`）／画像だけのページ（`needsOcr`、
  OCR は持たない）／未対応形式（`unsupported`）／壊れている（`malformed` ほか）。
- **40MB を超えるファイルは変換に回さない**（WASM は全体をメモリに載せる）。ダウンロードへ誘導する。
- WASM は 6.4MB（gzip 2.9MB）の遅延アセット。主チャンクは +1.2KB gzip しか増えない
  （`?url` の文字列と DocPreview のぶん）＝**その形式を開いた人だけが払う**。

### 82.4.1 検証ハーネス

`npm --prefix console run doc:check`。**WASM が実ブラウザで本当に初期化され、実 OOXML を
変換できるか**を見る（jsdom では絶対に分からない層）。標本はその場で組み立てる最小の OOXML で、
zip も自前で書くのでリポジトリにバイナリを置かない。Markdown の描画は MarkdownView の担当なので、
ハーネスでは差し替えて**変換結果の文字列**を検査する。

```
OK   sample.docx / sample.xlsx / sample.pptx が Markdown に変換される
OK   表が GFM の表として出る — "| みかん | 34 |"
OK   簡易プレビューだと明示している
OK   壊れたファイルで理由が出る / 原本を開く導線を出す / 本文の面は出さない
```

**このハーネスが測っていないもの**: 実文書での再現度（それは §82.2 の実測）。最小 OOXML は
配線を確かめるためのもので、「Word が吐いた本物」ではない。

## 82.5 テスト

| 層 | 何を見るか |
|----|-----------|
| `src/features/viewer/pdfPages.test.ts` | 配置・可視範囲・現在ページ・読み位置の保存・倍率の段・canvas 画素上限（純関数） |
| `src/features/viewer/FileViewDocuments.dom.test.tsx` | 面の選択（PDF / Office / バイナリ）・生バイト URL と形式の受け渡し・情報バー・編集面を出さないこと |
| `scripts/pdf/check.mjs` | 実ブラウザでの描画・拡大・解放・失敗表示（82.3.3） |
| `src/lib/filemeta.test.ts` | 拡張子 → 面の割り当て（PDF / Office / テキストのまま） |
| `scripts/doc/check.mjs` | 実ブラウザでの WASM 初期化と OOXML 変換・失敗表示（82.4.1） |

jsdom には canvas も WebAssembly の配信も無いので、**「本当に絵が出るか」「本当に変換できるか」は
dom テストでは決して分からない**。そこはハーネスの担当と割り切って、dom テストでは `PdfView` と
`DocPreview` をモックしている。2 つのハーネスは `scripts/lib/headless.mjs`（一時ディレクトリを配る
http サーバ＋素の WebSocket で叩く CDP）を共有する。
