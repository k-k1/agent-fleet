# 65. `.drawio` をペインで表示する

- 状態: **設計確定・未実装**（2026-08-16）。実測は開発 Workspace の headless Chromium で行い、
  再現コマンドを各節に残した。設計判断は [decisions/0046](decisions/0046-drawio-viewer.md)。
- 関連: [44-markdown-code-editor.md](44-markdown-code-editor.md)（File ペインの面と編集・保存機構。
  本機能は**その面を 1 つ増やす**形で載る） / [28-i18n.md](28-i18n.md) /
  [35-packaging.md](35-packaging.md)（同梱物と配布サイズ） /
  [31-container-browser-pane.md](31-container-browser-pane.md)（**別物**。あちらは Workspace 内
  Chromium の画面中継で、本機能は Console 自身の描画）

---

## 65.1 いま何が起きているか

`.drawio` は XML なので `api/fs/file` はテキストとして返し、File ペインは
**生の XML を CodeView に出す**。図としては読めない。

- `console/src/lib/filemeta.ts` の `EXT_LANG` に `drawio` が無い → `langFor()` は `""`
  → ハイライトも無い素のテキスト。
- 一方 **`.drawio.svg` / `.drawio.png` は既に表示できている**。拡張子が `svg` / `png` なので
  `imageFormat()` が効き、`ImageView` が描く（ズーム・パン付き）。
- したがって穴は **素の `.drawio` / `.dio`（および mxfile を格納した `.xml`）だけ**である。

## 65.2 実測（2026-08-16・drawio v31.1.8）

drawio 公式の自己完結ビューア `js/viewer-static.min.js`（Apache-2.0）を対象に測った。

```
curl -sS -o viewer-static.min.js \
  https://raw.githubusercontent.com/jgraph/drawio/v31.1.8/src/main/webapp/js/viewer-static.min.js
chromium --headless --disable-gpu --no-sandbox --virtual-time-budget=8000 \
  --host-resolver-rules="MAP * 127.0.0.1:1" \
  --screenshot=shot.png --window-size=920,620 file:///tmp/dio/test.html
```

| 項目 | 実測値 |
|------|--------|
| ビューア本体 | **4.0 MB**（gzip **870 KB**）の 1 ファイル |
| **完全オフライン描画** | **成立**。DNS を全遮断（`MAP * 127.0.0.1:1`）しても図形・エッジ・破線・矢印・**日本語ラベル**まで描画された |
| 圧縮された `<diagram>`（deflate+base64） | **成立**。pako 同梱でビューアが内部展開する（生 XML と同じ絵になることを確認） |
| 複数ページ | **成立**。ツールバーに `1 / 2` のページ送り・zoom・fit が出る |
| 読み込み〜描画 | **load 76 ms / render ~0 ms**（小さい図・ローカル配信） |
| ダークモード | `graphConfig["dark-mode"]` が正式キー。Console のテーマに追従できる |
| ベンダーアイコン（`shape=mxgraph.aws4.*` 等） | **ステンシル未同梱では中身が空になる**（サイズ・枠・グラデーション・ラベルは出るがアイコンの図案が無い）。`window.STENCIL_PATH` を自ホストへ向けると**正しい EC2 アイコンが出る**ことを確認 |

### 65.2.1 実測で見つかった罠（設計に直結する 5 件）

1. **グローバルを 932 個生やす。** `Object.keys(window)` の差分で計測。`mxClient` 系のほかに
   `lang` / `dash` / `rough` / `Base64` / `Spinner` / `pako` / `MathJax` / `Editor` / `Graph`、
   さらに **`window.DOMPurify` を上書き**する。**アプリの window へ直接読み込んではならない。**
2. **ツールバーの lightbox は図面を外部へ持ち出す。** `GraphViewer.lightboxHost =
   window.DRAWIO_LIGHTBOX_URL`（既定 `https://app.diagrams.net`）を `window.open` し、図の XML を
   渡す。**この操作は Workspace の内容を第三者サービスへ送る経路である。**
3. **`STENCIL_PATH` の既定が外部。** `window.STENCIL_PATH || "https://viewer.diagrams.net/stencils"`。
   未設定のまま置くと、ベンダー図形を含む図を開いた瞬間に外部へ取りに行く。
4. **CP は `/assets/*` だけ immutable、他は `no-store`**（`control-plane/routes.go:776-785`）。
   `console/public/` へ素置きすると 4 MB が毎回再取得になる。
5. **`api/fs/file` は 2 MiB で打ち切る**（`maxEditorFileBytes = 2 << 20`,
   `workspace/agent/fs_fd_linux.go:19`）。画像を埋め込んだ `.drawio` は普通に超え、
   `content` が `(file too large to preview)` に化ける。**図の取得は `api/fs/download`
   （サイズ制限なし・`http.ServeContent`）から行う。**

## 65.3 採る構成

**サンドボックス iframe に閉じ込め、XML は親から postMessage で流し込む。**

```
FileView (.drawio)
  ├ fetch api/fs/download?path=…        ← 資格情報つき・親だけが行う
  └ <iframe sandbox="allow-scripts" src="assets/drawio-<ver>/viewer.html">
        └ viewer-static.min.js + GraphViewer     ← 資格情報なし・connect-src 'none'
             ↑ postMessage({xml, dark, page})
             ↓ postMessage({ready, pages, error})
```

この形を採る理由は 3 つとも実測に基づく。

- **65.2.1-1（グローバル汚染）**を構造的に無効化できる。iframe の window は使い捨てで、
  ペインを閉じれば消える。CSS も混ざらない。
- **65.2.1-2（lightbox 持ち出し）**に二重の蓋ができる。ツールバーから外す（`lightbox: 0`）のに加え、
  `sandbox` に `allow-popups` を与えないので `window.open` 自体が失敗する。
- iframe は**オリジンを持たない**（`allow-same-origin` を与えない）ので、図のラベルに仕込まれた
  HTML がビューアの DOMPurify をすり抜けても、Console の DOM・Cookie・API に届かない。
  `<script src>` は CORS の対象外なので、**P0（ステンシル無し）ではこの構成に追加設定は要らない。**

`viewer.html` は自前の 30 行程度の受け皿で、`window.STENCIL_PATH` /
`window.DRAWIO_LIGHTBOX_URL` を自オリジンへ固定してから `viewer-static.min.js` を読む。

## 65.4 ペインへの載せ方

**新しい `PaneKind` は作らない。** `kind: "file"` の面を 1 つ増やす。

- タブ・ポップアウト・`dirtyRegistry`・キーボード・左ペインからの `openTarget` が
  そのまま効く（`console/src/layout/types.ts:24` の `file` を触らない）。
- `console/src/features/viewer/fileMode.ts` の `FileModeCaps` に `diagram` を足し、
  `FileModeState` に `{ kind: "diagram"; mode: "figure" | "source" }` を加える。
  **図 ↔ XML ソースの 2 モード**で、ソース側は既存の CodeMirror 編集面をそのまま使う
  （`.drawio` は編集可能なテキストなので docs/44 の保存・競合・外部変更追従が無改造で効く）。
- `filemeta.ts` に `drawio` 判定を足す。対象は `.drawio` / `.dio`、および
  **先頭が `<mxfile` または `<mxGraphModel` の `.xml`**（拡張子だけでは判定できないため
  中身も見る。判定は先頭 1 KB で足りる）。
- ヘッダの表示は既存の `ui-seg` ボタン群に合わせる。文言は i18n（`view.diagram` / `view.source`）。

## 65.5 ステンシルは「必要になったときに取りに行く」

### 65.5.1 実行時は既にオンデマンドである

`mxStencilRegistry.loadStencilSet(name, …)` は、図に `shape=mxgraph.<set>.*` が現れた
**そのときに `<set>.xml` を 1 回だけ** 取りに行く（`mxStencilRegistry.packages` にキャッシュ）。
`parseStencilSet(documentElement, …)` が公開されているので、**親が取得済みの DOM を渡す形にもできる**。
つまり「1 枚の図を開くのに 43 MB を読む」ということは最初から起きない。

### 65.5.2 したがって問題は配布サイズだけ

| 単位 | 実測 |
|------|------|
| ステンシル全体（`stencils/**`, v31.1.8） | **42.8 MB / 205 ファイル** |
| 最大の 1 セット `aws4.xml` | 6.5 MB（gzip **1.1 MB**） |
| 次点 | `rack/hpe_aruba/switches.xml` 3.8 MB、`cisco19.xml` 1.8 MB、`aws3.xml` 1.5 MB … |

**42.8 MB をリポジトリとイメージに常時積むのは、使う人の割合に対して割に合わない。**
（`gh api repos/jgraph/drawio/git/trees/v31.1.8?recursive=1` で計測）

### 65.5.3 採る形 — CP がプロキシしてディスクにキャッシュする

```
iframe ──GET /drawio/stencils/aws4.xml──▶ CP
                                          ├ 台帳（同梱）で名前を照合  … 無ければ 404
                                          ├ キャッシュにあればそれを返す
                                          └ 無ければ pin した upstream から取得
                                             → sha256 照合 → 保存 → 返す
```

- **同梱するのは台帳だけ。** `stencils.json`（205 件の `名前 → sha256 → サイズ`、約 20 KB）を
  リポジトリに置く。バイト列は持たない。
- **台帳は完全性の担保と同時に SSRF の防壁である。** セット名は**信用できない `.drawio` の中身**
  から来る（`shape=mxgraph.<set>.<x>`）。台帳に無い名前は取りに行かない、が必須条件。
  これが無いと「図を開かせるだけで CP に任意 URL を叩かせる」道具になる。
- **外向き通信は CP で 1 回・テナント全体で共有。** 利用者のブラウザからは出さない。
- **閉域では黙って劣化する。** 取得に失敗したら 65.2 の「枠と色だけ」に落ちる＝ P0 と同じ絵。
  壊れない。事前投入したい環境向けに、キャッシュディレクトリへ全件を流し込む
  スクリプトを用意する（オフライン環境の管理者はこれを 1 回流す）。
- iframe はオリジンを持たない（65.3）ので、**この経路にだけ `Access-Control-Allow-Origin: *`
  が要る**。ステンシルは drawio の公開アセットで秘密を含まないため問題ない。

## 65.6 セキュリティ

| 経路 | 扱い |
|------|------|
| 図面の外部持ち出し（lightbox） | ツールバーから外す ＋ `sandbox` に `allow-popups` を与えない（二重） |
| `STENCIL_PATH` 既定の外部取得 | `viewer.html` で自オリジンへ固定。CP 経由以外へは出ない |
| ラベル内の HTML / `javascript:` リンク | ビューア同梱の DOMPurify ＋ オリジンなし iframe（`allow-same-origin` なし）で二重 |
| 任意 URL 取得（SSRF） | ステンシル名を同梱台帳で照合（65.5.3） |
| iframe からの通信 | `Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; img-src data: blob:; connect-src 'self'`（P0 は `connect-src 'none'`） |

## 65.7 配布

- **ビューア本体はリポジトリにコミットする**（`console/vendor/drawio/`）。バージョンと sha256 を
  固定し、`NOTICE` に Apache-2.0 の帰属を追記する。**ビルドが外部ネットワークに依存しない**ことを
  優先した（`npm ci` 以外の取得経路を増やさない）。
- **Vite のハッシュ資産として出す。** `console/public/` に素置きすると 65.2.1-4 で `no-store` に
  なるため、`?url` インポートで `dist/assets/` へ吐かせ、`immutable` キャッシュに載せる。
  バージョン更新はハッシュ変更で自然に反映される。
- `viewer.html` も同じ扱い（`assets/` 配下に置き、iframe の `src` はビルド時に確定した相対 URL）。

## 65.8 段階

| Phase | 内容 | 受け入れ基準 |
|-------|------|--------------|
| **P0** | ビューア同梱・iframe・図 ↔ XML ソース切替・テーマ追従・ページ送り・`.drawio`/`.dio`/`mxfile` 判定・i18n・dom テスト | 素の `.drawio` が図として開く／ラベルが日本語で出る／圧縮 `<diagram>` が開く／2 MiB 超が開く（`fs/download` 経由）／外向き通信が 0 件（CDP で確認） |
| **P1** | ステンシルの CP プロキシ＋キャッシュ＋台帳（65.5.3）、事前投入スクリプト | `aws4` を含む図でアイコンが出る／台帳に無い名前が 404／2 回目はキャッシュから |
| **P2** | `.drawio.svg` / `.drawio.png` の埋め込み XML から図モードを出す（今は画像として開く） | 同じファイルが画像／図の両モードで開ける |
| **P3** | **編集**。drawio webapp を自ホストし embed（`?embed=1&proto=json`）、保存は docs/44 の revision/競合機構へ載せる | 別起票（配布サイズが桁で変わるため） |

## 65.9 採らなかった案

- **`embed.diagrams.net` を iframe で開く**（`react-drawio` が既定でこれ）。実装は最小だが、
  **図面が第三者サービスへ出る**。egress を絞った環境では動かない。却下。
- **`viewer-static.min.js` をアプリへ直接 `import()`。** グローバル 932 個（65.2.1-1）。
  戻す手段が無く、`window.DOMPurify` の上書きのような他機能への影響も読み切れない。却下。
- **mxGraphModel → SVG の自前レンダラを書く。** 依存ゼロだが、シェイプライブラリを使った図が
  「それらしく間違って」描かれる。**無言の低品質は非表示より悪い。** 却下。
- **サーバ側で SVG に変換して配る。** drawio-desktop（Electron）が要る。Workspace には Docker も
  ブラウザ自動化基盤としての Electron も無い。不可。
- **npm の既存パッケージに乗る。** ビューアを含むものが無い（`mxgraph` 4.2.2 は下回りのみで
  drawio の図形定義とステンシルを含まない／`drawio` は同名の無関係パッケージ／`react-drawio` は
  外部 embed のラッパ）。ベンダリングが唯一の道。
- **ステンシルを 42.8 MB 同梱。** 使う図の割合に対して配布コストが見合わない（65.5.2）。
- **ブラウザから upstream のステンシルを直接取る。** 実装は最小だが、利用者ごとに外部通信が出る・
  閉域で壊れる・完全性の担保をブラウザ側に置くことになる。CP プロキシを採る。

## 65.10 未解決

- P1 のキャッシュ置き場（CP の既存データディレクトリのどこに置くか。テナント横断で 1 つでよい）。
- upstream の pin 先（GitHub raw のタグ URL か、リリース `draw.war` の展開か）。
- P3 の webapp 自ホストは配布サイズが別次元（`draw.war` 53 MB）なので、着手時に別 ADR を起こす。
