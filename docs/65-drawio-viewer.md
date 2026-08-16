# 65. `.drawio` をペインで表示する

- 状態: **P0 実装済み**（2026-08-16。P1 以降は未着手）。実測は開発 Workspace の headless
  Chromium で行い、再現コマンドを各節に残した。実装で分かったことは §65.11。
  検証ハーネスは `npm --prefix console run drawio:check`。
  設計判断は [decisions/0046](decisions/0046-drawio-viewer.md)。
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
FileView (.drawio)  … src/features/viewer/DrawioView.tsx
  ├ fetch api/fs/download?path=…                  ← 図の XML   ┐ 取得は
  ├ fetch assets/viewer-static.min-<hash>.js      ← ビューア本文 ┘ 親だけが行う（資格情報つき）
  └ <iframe sandbox="allow-scripts" srcDoc={drawioFrameSrcdoc(…)}>
             ↓ {t:"ready"}                        ← 文書ができた（まだ何も無い）
             ↑ {t:"boot", src}                    ← ビューア本文をインライン評価
             ↓ {t:"booted"}
             ↑ {t:"render", xml, dark}
             ↓ {t:"rendered", pages, page, scale} / {t:"error", code}
```

**フレームは 1 本も要求を出さない。** 図の XML もビューア本文も親が取って渡す
（CSP は `default-src 'none'` / `connect-src 'none'` で、script-src に外部オリジンすら
載せない）。ビューアを `<script src>` で読ませる形は**実機だけで壊れる** — §65.11-7。

この形を採る理由は 3 つとも実測に基づく。

- **65.2.1-1（グローバル汚染）**を構造的に無効化できる。iframe の window は使い捨てで、
  ペインを閉じれば消える。CSS も混ざらない。
- **65.2.1-2（lightbox 持ち出し）**に二重の蓋ができる。ツールバーから外す（`lightbox: 0`）のに加え、
  `sandbox` に `allow-popups` を与えないので `window.open` 自体が失敗する。
- iframe は**オリジンを持たない**（`allow-same-origin` を与えない）ので、図のラベルに仕込まれた
  HTML がビューアの DOMPurify をすり抜けても、Console の DOM・Cookie・API に届かない。
  `<script src>` は CORS の対象外なので、**P0（ステンシル無し）ではこの構成に追加設定は要らない。**

フレームの中身は静的ファイルではなく **`srcdoc`**（`src/features/viewer/drawioFrame.ts` が
組み立てる文字列）にした。CSP・外部 URL の潰し・メッセージ契約が 1 か所に集まり、
«lightbox を出していないか» «外部 URL を潰しているか» を**文字列としてユニットテストで
検査できる**（`drawioFrame.test.ts`）。ハッシュ付き資産になるのは 4 MB の本体だけで、
srcdoc はそれを `<script src>` で読む —— `<script src>` は CORS の対象外なので、
オリジンを持たないフレームからでも読める。

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
| iframe からの通信 | `Content-Security-Policy: default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'`。**外部オリジンを script-src に載せない** —— 載せるとフレームが自分で取りに行く経路が復活し、§65.11-7 の欠陥に戻る。ステンシル（P1）も同じ理由で**親が取って渡す**（フレームの CSP は開けない） |
| 外部 URL の既定値 | `PROXY_URL` / `STYLE_PATH` / `SHAPES_PATH` / `STENCIL_PATH` / `DRAW_MATH_URL` / `GRAPH_IMAGE_PATH` / `CSS_PATH` / `DRAWIO_BASE_URL` / `DRAWIO_SERVER_URL` / `DRAWIO_LIGHTBOX_URL` / `DRAWIO_LOG_URL` を **dead value で** 潰す（空文字では潰せない — §65.11-1） |

## 65.7 配布

- **ビューア本体はリポジトリにコミットする**（`console/vendor/drawio/`）。バージョンと sha256 を
  固定し、`NOTICE` に Apache-2.0 の帰属を追記する。**ビルドが外部ネットワークに依存しない**ことを
  優先した（`npm ci` 以外の取得経路を増やさない）。
- **Vite のハッシュ資産として出す。** `console/public/` に素置きすると 65.2.1-4 で `no-store` に
  なるため、`?url` インポートで `dist/assets/` へ吐かせ、`immutable` キャッシュに載せる。
  バージョン更新はハッシュ変更で自然に反映される。
- 資産 URL は **`document.baseURI` で絶対化してから** フレームへ渡す。srcdoc の中の相対 URL は
  親の base に対して解決されるため、パスを剥がすプロキシの下や `/open/...` のような深い URL では
  解決先がずれる。
- 版とチェックサム、更新手順は `console/vendor/drawio/README.md`。gitleaks の許可（upstream の
  Dropbox アプリ ID）は `.gitleaks.toml` に値だけを登録している。

## 65.8 段階

| Phase | 内容 | 受け入れ基準 |
|-------|------|--------------|
| **P0** ✅ | ビューア同梱・iframe・図 ↔ XML ソース切替・テーマ追従・ページ送り・`.drawio`/`.dio`/`mxfile` 判定・i18n・dom テスト | 素の `.drawio` が図として開く／ラベルが日本語で出る／圧縮 `<diagram>` が開く／2 MiB 超が開く（`fs/download` 経由）／外向き通信が 0 件（CDP で確認）—— **すべて `drawio:check` が実ブラウザで判定**（実機の Console での目視は未） |
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

---

## 65.11 実装で分かったこと（2026-08-16・P0）

設計時の実測（§65.2）に加え、**書いてみて初めて出た**ものを残す。どれも「静かに壊れる」型で、
次に触る人が同じ時間を溶かさないためのもの。

1. **外部 URL の既定値は空文字では潰せない。** ビューアは
   `window.X = window.X || "https://viewer.diagrams.net/…"` の形で既定値を入れるので、
   先回りして `""` を代入しても falsy ＝ 外部の既定値が生き残る。実際、`DRAW_MATH_URL` を
   `""` にしたまま `viewer.diagrams.net/math4/es5/startup.js` を取りに行き、CSP が止めていた
   （CSP が無ければ黙って外部に出ていた）。**ネットワークに出ない dead value**（`about:blank`）を
   入れる。`drawio:check` の「外部への要求 0 件」はこの取りこぼしを捕まえるためにある。
2. **`window.onerror` は使えない。** ビューア本体が自分のロガーで `window.onerror` を
   上書きするため、こちらが代入したハンドラは静かに外れる。上書きできない
   `addEventListener("error", …)` で受ける。**失敗を親へ返せないと、ペインは理由も出さず
   空になる。**
3. **コンテナの寸法はビューアが奪う。** `GraphViewer` は既定で
   `graph.resizeContainer = true` にし、コンテナを図の大きさへ縮める。860×520 のフレームで
   コンテナが **181×341** まで縮み、図が左上に貼り付いた。`addSizeHandler` の分岐が
   **インライン `style.height` の有無**を見ているので、CSS クラスで大きさを与えても効かない。
   → ホスト要素にピクセルで幅・高さを与え、`resize: 0` にし、収め直しはビューア自身の
   `fitGraph()` に任せる（`allowZoomIn` が既定 false なので上限が等倍 ＝ 大きい図は縮小、
   小さい図は原寸で中央）。
4. **`--virtual-time-budget` はサンドボックス iframe を動かさない。** 別プロセスになる
   フレームは仮想時間の対象外で、`ready` の後で時間が止まり「描画されない」ように見える。
   **ハーネスの罠であって製品の不具合ではない** —— 実時間（CDP で待つ）に切り替えたら
   同じコードがそのまま通った。iframe を含む UI の検証はスクリーンショット CLI ではなく
   CDP ＋ 実時間で行うこと。
5. **gitleaks は同梱ビューアに反応する。** upstream の配布物に Dropbox の**アプリ ID**
   （公開識別子）が入っており `dropbox-api-token` ルールに当たる。`.gitleaks.toml` の原則
   どおりパスごと除外はせず、その値だけを許可した。
6. 図の面は **一度出したら畳んでも外さない**（`hidden` にするだけ）。作り直すと 4 MB の
   ビューアを読み直し、ズーム位置と開いていたページも失う。逆に**一度も図を見ていない
   うちは作らない** —— 行を指した引用でソース面に着地した人に、図の取得をさせない。
7. **サンドボックス iframe からの `<script src>` は認証を通れない**（実機で最初に壊れた点。
   ローカルの素の静的配信では絶対に出ない）。オリジンを持たないフレームからの要求は
   **cross-site 扱いになり、`SameSite=Lax` のセッション cookie が付かない**
   （`setCookie` は Lax・`registerStatic` は auth 除外を宣言していない）。CP の `authGate` が
   `/assets/*` を 401 で弾き、`GraphViewer` が未定義のまま `render` に入っていた。
   → **フレームは何ひとつ自分で取りに行かない**形に変えた。ビューア本文は親が `fetch`
   して `postMessage` で渡し、フレームはインライン script として評価する。
   CP 側の認証やキャッシュの規則を触らずに済み、P1 のステンシルも同じ経路に乗る。
   検証ハーネスは **cookie の無い要求を 401 にする配信**を持つようになった
   （この形にしないと、この欠陥はローカルでは一生再現しない）。
8. **iframe を作った直後に `postMessage` してはいけない。** まだ srcdoc の文書が無く、
   メッセージは初期の `about:blank` に配達されて**消える**（実測: 遅延 0 / 10 / 50 ms は
   届かず、200 ms で届いた）。フレーム内に置いた保持キューも、その文書がまだ無い以上
   役に立たない。→ フレームが `ready` を出してから送る。
9. **「読み込めなかった」と「図として読めない」を混ぜない。** 7 の不具合はビューアの
   読み込み失敗だったのに、`try/catch` が一律 `parse` を返していたため画面には
   「drawio の図として解釈できません」と出て、**原因がファイル側にあるように見えた**。
   `code: "boot"` を分けて別の文言にした。
