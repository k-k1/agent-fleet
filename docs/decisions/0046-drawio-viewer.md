# 0046. `.drawio` は drawio 公式ビューアを同梱し、サンドボックス iframe に閉じ込めて表示する

- 状態: **設計確定・未実装**（2026-08-16。設計と実測は [docs/65](../65-drawio-viewer.md)）。
  成立性は開発 Workspace の headless Chromium で**外部通信を全遮断した状態**まで実測した。
- 関連: [0027-markdown-code-editor.md](0027-markdown-code-editor.md)（File ペインの面と保存機構。
  本 ADR はその面を 1 つ増やす） / [docs/35](../35-packaging.md)（同梱物と配布サイズ） /
  [0031-mcp-registry.md](0031-mcp-registry.md)（信用できない入力を名前で照合してから使う型）

## 背景

Console の File ペインは `.drawio` を**生の XML** としてしか出せない。`.drawio.svg` /
`.drawio.png` は拡張子が画像なので既に表示できており、穴は**素の `.drawio` だけ**である。

## 決定 1 — 描画は drawio 公式の `viewer-static.min.js` を同梱して行う

自前レンダラも外部サービスも採らない。

- **完全オフラインで描画できることを実測した。** DNS を全遮断（`--host-resolver-rules="MAP *
  127.0.0.1:1"`）した headless Chromium で、図形・エッジ・日本語ラベル・複数ページ・
  **圧縮された `<diagram>`**（deflate+base64）まで正しく描画された。読み込み 76 ms・描画 ~0 ms。
- 大きさは **4.0 MB（gzip 870 KB）の 1 ファイル**。既に遅延読み込みしている mermaid / marp-core と
  同じ桁で、遅延読み込みする限り初期表示には乗らない。
- **自前レンダラは却下。** シェイプライブラリを使った図が「それらしく間違って」描かれる。
  無言の低品質は非表示より悪い。
- **サーバ側 SVG 変換は不可。** drawio-desktop（Electron）が要る。
- **npm に代替が無い**（`mxgraph` は下回りのみ・`drawio` は無関係・`react-drawio` は外部 embed の
  ラッパ）。ベンダリングが唯一の道である。

## 決定 2 — アプリの window へは読み込まず、サンドボックス iframe に閉じ込める

`sandbox="allow-scripts"`（`allow-same-origin` も `allow-popups` も与えない）の iframe で
自前の `viewer.html` を開き、**XML は親が `api/fs/download` で取って postMessage で流し込む**。

理由はすべて実測に基づく。

1. **グローバルを 932 個生やす。** `mxClient` 系だけでなく `lang` / `dash` / `Base64` / `Spinner` /
   `pako` / `MathJax` / `Editor` / `Graph`、さらに **`window.DOMPurify` を上書き**する。
   アプリの window に入れたら戻す手段が無い。
2. **ツールバーの lightbox が図面を外部へ持ち出す。** `GraphViewer.lightboxHost =
   window.DRAWIO_LIGHTBOX_URL`（既定 `https://app.diagrams.net`）を `window.open` して図を渡す。
   ツールバーから外すのに加え、**`allow-popups` を与えないので `window.open` 自体が失敗する**——
   設定漏れが事故にならない側に倒す。
3. オリジンを持たない iframe なので、ラベルに仕込まれた HTML がビューア同梱の DOMPurify を
   すり抜けても Console の DOM・Cookie・API に届かない。
4. **P0 はこれで追加設定が要らない。** `<script src>` は CORS の対象外なので、オリジンなしの
   iframe からでも自オリジンのビューアを読める。

## 決定 3 — 新しい `PaneKind` は作らず、File ペインの面を 1 つ増やす

`kind: "file"` のまま `fileMode` に `diagram` を足し、**図 ↔ XML ソースの 2 モード**にする。
タブ・ポップアウト・dirty 管理・キーボード・左ペインからの導線が無改造で効き、ソース側は
既存の CodeMirror 編集面（docs/44 の保存・競合・外部変更追従）をそのまま使える。
判定は拡張子（`.drawio` / `.dio`）に加え、**先頭が `<mxfile` / `<mxGraphModel` の `.xml`** も含める。

## 決定 4 — 図の取得は `api/fs/file` ではなく `api/fs/download` から行う

`api/fs/file` は **2 MiB で打ち切る**（`maxEditorFileBytes = 2 << 20`,
`workspace/agent/fs_fd_linux.go:19`）。画像を埋め込んだ `.drawio` は普通に超え、`content` が
`(file too large to preview)` に化ける。`api/fs/download` はサイズ制限を持たない
（`http.ServeContent`）。**この 1 点でファイルの半分が開かないか開くかが変わる。**

## 決定 5 — ステンシルは同梱せず、CP がプロキシしてディスクにキャッシュする

「必要になったときに取りに行く」を採る。**実行時のオンデマンド性は最初からある**——
`mxStencilRegistry.loadStencilSet` は図に現れたセットだけを 1 回取りに行くので、1 枚の図で
全部を読むことは起きない。問題は配布サイズだけである。

- **ステンシル全体は 42.8 MB / 205 ファイル**（`aws4.xml` だけで 6.5 MB）。使う図の割合に対して
  リポジトリとイメージに常時積むのは割に合わない。
- **同梱するのは台帳だけ**（205 件の `名前 → sha256 → サイズ`、約 20 KB）。CP は
  `GET /drawio/stencils/<set>.xml` を受け、台帳で照合 → キャッシュ → 無ければ pin した upstream
  から取得 → sha256 照合 → 保存、の順で応える。
- **台帳は完全性の担保であると同時に SSRF の防壁である。** セット名は**信用できない `.drawio` の
  中身**（`shape=mxgraph.<set>.<x>`）から来る。台帳に無い名前を取りに行く実装は、
  「図を開かせるだけで CP に任意の URL を叩かせる」道具になる。
- **外向き通信は CP で 1 回・テナント全体で共有**する。利用者のブラウザからは出さない
  （ブラウザ直取得は実装が小さいが、利用者ごとの外部通信・閉域での破綻・完全性の担保を
  ブラウザに置くことの 3 点で劣る）。
- **閉域では黙って劣化する**——取得に失敗したら「枠と色だけ」（＝ P0 と同じ絵）に落ちるだけで、
  ペインは壊れない。事前投入用のスクリプトを併せて用意する。
- iframe はオリジンを持たない（決定 2）ため、**この経路にだけ `Access-Control-Allow-Origin: *`**
  が要る。ステンシルは drawio の公開アセットで秘密を含まない。

**ステンシルの有無で何が変わるかは実測済み**である。未同梱だと `shape=mxgraph.aws4.*` は
サイズ・枠・グラデーション・ラベルは出るが**アイコンの図案が空になる**。自ホストして
`window.STENCIL_PATH` を向けると正しい EC2 アイコンが出る。

## 決定 6 — ビューア本体はリポジトリにコミットし、Vite のハッシュ資産として配る

- **ビルドが外部ネットワークに依存しない**ことを優先し、4.0 MB を `console/vendor/drawio/` に
  バージョン ＋ sha256 固定でコミットする（`NOTICE` に Apache-2.0 の帰属を追記）。
  ステンシル 42.8 MB は決定 5 の通りコミットしない——**同梱するかどうかの線はサイズで引く。**
- **`console/public/` に素置きしてはならない。** CP は `/assets/*` だけ immutable で、他は
  `no-store`（`control-plane/routes.go:776-785`）。素置きすると 4 MB がペインを開くたびに
  再取得される。`?url` インポートで `dist/assets/` に吐かせ、バージョン更新はハッシュ変更に任せる。

## 決定 7 — 編集は本 ADR の範囲外

drawio webapp の自ホスト（`draw.war` 53 MB）が要り、配布サイズが別次元になる。着手時に別 ADR を
起こす。保存機構そのものは docs/44 の revision/競合機構がそのまま使えるため、**難所は編集器の
配布であって保存ではない**ことだけ記録しておく。
