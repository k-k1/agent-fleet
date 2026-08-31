# 65. `.drawio` をペインで表示する

- 状態: **P0 実装済み**（2026-08-16。P1 以降は未着手）。実測は開発 Workspace の headless
  Chromium で行い、再現コマンドを各節に残した。実装で分かったことは §65.11。
  検証ハーネスは `npm --prefix console run drawio:check`。
  設計判断は [decisions/0046](../decisions/0046-drawio-viewer.ja.md)。
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
| ステンシル `.xml`（v31.1.8） | **203 ファイル / 40.8 MB** |
| 最大の 1 セット `aws4.xml` | 6.21 MB |
| 次点 | `rack/hpe_aruba/switches.xml` 3.67 MB、`cisco19.xml` 1.73 MB、`aws3.xml` 1.39 MB、`alibaba_cloud.xml` 1.29 MB、`gcp2.xml` 1.08 MB … |
| 上位 20 件 / 上位 50 件 | 26.0 MB / 34.7 MB（＝**上位 20 件で全体の 64%**） |

**40.8 MB をリポジトリとイメージに常時積むのは、使う人の割合に対して割に合わない。**
（`gh api repos/jgraph/drawio/git/trees/v31.1.8?recursive=1` で計測）

> `stencils/` には `.xml` 以外に `LICENSE` と `clipart/Gear_128x128.png` も居る（合計 205
> ファイル / 42.8 MB）。ビューアが要求するのは `<basename>.xml` だけなので、**台帳に入れる
> のは `.xml` の 203 件**。それ以外を載せても届かないうえ、SSRF の防壁を無駄に緩める。

### 65.5.2b 何が「セット名」で、何が要求されるのか（実測）

台帳の鍵は **ビューアが実際に要求するパス**であって、シェイプ名でもディレクトリ一覧でもない。
`viewer-static.min.js` v31.1.8 を読んで実ブラウザで裏を取った結果:

1. `mxStencilRegistry.getBasenameForStencil("mxgraph.<a>.<b>.<name>")` は先頭の `mxgraph` と
   末尾の `<name>` を落として **`<a>/<b>`** にする。
2. **`mxStencilRegistry.libraries` に 62 セットの表がある**（実行時にも 62 件を確認）。
   ここに載っている basename は、**ファイル名が basename と違うことがある**:
   `ios7icons → ios7/icons.xml`、`rackGeneral → rack/general.xml`、`ibmcloud → ibm_cloud.xml`、
   `pidFlowSensors → pid/flow_sensors.xml`、`veeam2 → veeam/veeam2.xml`。
   **`basename + ".xml"` で組み立てる実装はここで 404 になる。**
3. 表に無い basename はフォールバックで `basename.replace("_-_","_") + ".xml"`。
   203 件のうち **157 件（21.6 MB）がこちら**。

つまり解決規則はビューアの中にしかなく、**フレーム側でその表を引く以外に正しい実装は無い**
（`drawioFrame.ts` の `stencilFilesFor()`）。`drawio:check` の `stencil-remap` ケースが
この読み替えを 1 本で押さえている。

**既知の不整合: `libraries.sap` は `sap.xml` を指すが、upstream v31.1.8 に `sap.xml` は存在
しない**（`stencils/` に `sap` は 1 件も無い。SAP のシェイプは JS 側 `mxSAP.js` に全部ある）。
台帳に無い＝ 404 が正しい挙動なので、「台帳の漏れ」と誤読して手で足さないこと。
`TestDrawioManifestLoads` がこれを明示的に禁じている。

### 65.5.2c `SHAPES_PATH` の `.js` は取りに行かない（当初の懸念は実測で否定された）

`libraries` の各エントリは `.xml` と **`SHAPES_PATH` の `.js` を混ぜて並べている**
（`libraries.aws4 = [SHAPES_PATH + "/mxAWS4.js", STENCIL_PATH + "/aws4.xml"]`）。しかも
`getStencil` の分岐は **リスト内の全ファイルを読む**ので、素直に読むと「aws4 の図を開くだけで
`mxAWS4.js` を取得して `eval` する」ように見える。

**実測ではそうならない。** `viewer-static.min.js` の末尾（未圧縮のまま残っている部分）に
`mxStencilRegistry.allowEval = false;` があり、実行時にも `allowEval === false` を確認した。
`.js` の分岐はこのフラグの内側にあるので、**取得も eval も起きない**。実ブラウザで
`STENCIL_PATH` / `SHAPES_PATH` の両方を自前サーバへ向けて `dynamicLoading` を戻しても、
出た要求は `aws4.xml` **1 本だけ**だった。

そしてこれらの JS シェイプは **viewer-static に焼き込み済み**である（`registerShape(` が
829 箇所）。`libraries` の 62 件のうち **21 件は `.xml` を一切持たない純 JS セット**
（`archimate3` / `sysml` / `c4` / `er` / `uml25` / `mockup/*` / `infographic` / `emoji` …）で、
これらは**今も完全にオフラインで正しく描けている** —— 6 セットを並べた図を要求 0 本で
描けることを実測した。**台帳にも事前投入にも、これらは一切関係しない。**

> したがって `SHAPES_PATH` に何かを向ける必要は無い。dead value のまま塞いでおく
> （§65.11-1）。`allowEval` をこちらから触る必要も無い —— ビューアが既に false にしている。

### 65.5.3 採る形 — フレームが申告し、親が CP から取って渡す

```
フレーム: 描画後にモデルを走査 → 足りないセットを申告
   │  postMessage {t:"stencils", sets:["aws4.xml"]}
   ▼
親(Console, cookie を持つ) ──GET /api/drawio/stencils/aws4.xml──▶ CP
   │                                    ├ 台帳(go:embed)で名前を照合 … 無ければ 404
   │                                    ├ キャッシュにあればそれを返す
   │                                    └ 無ければ pin した upstream から取得
   │                                       → sha256 照合 → 保存 → 返す
   │  postMessage {t:"stencils", xml:[...]}
   ▼
フレーム: parseStencilSets() → graph.refresh()（**倍率も位置もそのまま**）
```

- **同梱するのは台帳だけ。** `control-plane/assets/drawio-stencils.json`
  （203 件の `名前 → sha256 → サイズ`、26 KB）。バイト列は持たない。
  焼き直しは `node console/scripts/drawio/stencils-manifest.mjs --write`
  （同梱ビューアの版と食い違うと自分で止まる）。
- **台帳は CP 側に置く。照合するのが CP だからである。** Console 側に置いた台帳は飾りで、
  防壁にはならない。
- **台帳を絞ってはいけない。** 載せなかったセットは 404 ＝ その図が黙って劣化する。
  全件で 26 KB しかないので、絞る動機（配布サイズ）はそもそも成り立たない。
  **絞るのは事前投入（プリシード）の方**であって台帳ではない。
- **台帳は完全性の担保と同時に SSRF の防壁である。** セット名は**信用できない `.drawio` の中身**
  から来る（`shape=mxgraph.<set>.<x>`）。台帳に無い名前は取りに行かない、が必須条件。
  これが無いと「図を開かせるだけで CP に任意 URL を叩かせる」道具になる。
  **要求は URL を運ばない** —— CP は台帳の `base` とセット名から自分で URL を組み立てる。
- **外向き通信は CP で 1 回・テナント全体で共有。** 利用者のブラウザからは出さない。
- **閉域では黙って劣化する。** 取得に失敗したら 65.2 の「枠と色だけ」に落ちる＝ P0 と同じ絵。
  **エラー表示にしてはいけない** —— 図は正しく開けているのだから、利用者に見せる異常ではない。
- **この経路は authGate の内側**（除外しない）。取りに来るのは cookie を持つ親であって
  フレームではないので、CORS も認証除外も要らない。理由は次項。

### 65.5.4 なぜフレームに直接取らせないのか（実測 3 点）

当初の設計（`STENCIL_PATH` を CP へ向け、フレームが自分で取りに行く）は、実ブラウザで
3 つとも壊れることを確認したので**捨てた**。

1. **認証を通れない。** `STENCIL_PATH` を自前サーバへ向けて出た `GET /stencils/aws4.xml` には
   **セッション cookie が付いていなかった**。オリジンを持たないフレームからの要求は cross-site
   扱いになり SameSite=Lax の cookie が落ちる —— §65.11-7 で `<script src>` が 401 になったのと
   まったく同じ穴である。CP 側を authGate 除外＋`Access-Control-Allow-Origin: *` にすれば通せるが、
   それは「認証の外側に穴を開ける」という対価を払う話になる。
2. **CSP を開けることになる。** `connect-src 'none'` のままだとフレームの取得は止まる
   （実測: 要求 0 本・アイコンは空のまま）。通すには `connect-src` に自オリジンを足すしかなく、
   フレームが自分で外を叩ける経路がそこで復活する。
3. **一度失敗すると二度と再取得しない。** `loadStencilSet` の失敗は握り潰され、そのあと
   `mxStencilRegistry.packages[basename] = 1` が立つ。実測では、CSP に阻まれて失敗したあとに
   もう一度描画しても**要求は 1 本も出ず**、アイコンは空のままだった。一時的な失敗が
   フレームの寿命いっぱい固定される。

親が取って渡す形にすると 3 つとも消える。**そして絵は同じ**である —— フレーム直取り版と
親から `parseStencilSets()` で流し込んだ版のスクリーンショットは**バイト単位で一致**した。

差し込みの描き直しは **`graph.refresh()` で足りる。** `render` をやり直すと見ていた場所が
飛ぶが、`refresh()` なら図案だけが差し替わる（実測: 利用者が 1.8221 倍に拡大した状態で
差し込んで、倍率も位置もそのまま・`path` が 1 → 3 に増えた）。

**必要なセットの割り出しは「展開済みのモデル」を見る。** 生の XML を走査する実装は、
圧縮された `<diagram>`（deflate+base64）で何も見つけられない（実測: `rawSeen=0`）。
`viewer.graph.model.cells` の `style` を見れば圧縮の有無を問わない（実測: 圧縮図で
`need=["aws4"]` を正しく取り出せた）。**親は deflate を解く必要が無い**、というのも
この形を採る理由のひとつ。

### 65.5.4b 瞬断は再試行する（実機で踏んだ）

**実機の初回取得で `raw.githubusercontent` の connection reset を実際に踏んだ**
（台帳を焼くときも 8 並列で同じものが出た）。1 回の瞬断をそのまま 502 にすると、
Console 側はそのセットを「頼んだ済み」にしたまま二度と要求しないので、**そのペインの
寿命いっぱいアイコンだけが欠ける** —— フレーム直取りを否決した理由（§65.5.4-3 の
`packages[basename] = 1`）と、そっくり同じ詰まり方を自分で作ってしまう。両側で塞ぐ:

- **CP は再試行する**（3 回・線形バックオフ）。ただし **404 と「完全に取れたうえでの
  sha256 不一致」は何度やっても同じ**なので即座に諦める。長さが足りない不一致は
  途中で切れた応答なので再試行の価値がある。
- **親は取れなかったセット名を `missing` で返し、フレームはそれを「頼んだ済み」から
  外す。** 次の描画でもう一度頼めるようになる。

`drawio:check` の `stencil-retry` が「失敗 → 通じるようにする → 再描画 → 同じセットを
もう一度要求して図案が載る」までを実ブラウザで見る。

### 65.5.5 事前投入（閉域向け）

CP のサブコマンド。**キャッシュのある場所で動き、同梱台帳をそのまま使う**ので、
サーバが後で照合するものとずれようがない。

```sh
control-plane drawio-preseed              # 既定束をダウンロードして投入（49 件 / 17.0 MB）
control-plane drawio-preseed --all        # 台帳の全件（203 件 / 40.8 MB）
control-plane drawio-preseed --from <dir> # ネットワークを使わず手元の stencils/ から
control-plane drawio-preseed --list       # 対象を並べるだけ
```

- **閉域では `--from`。** 外に出られる場所で drawio を 1 回取り、
  `src/main/webapp/stencils` を持ち込んでそのディレクトリを指す。**1 件ずつ台帳の
  sha256 で照合してから置く**ので、持ち込みの経路が信用できなくても中身は保証される
  （版がずれていれば `size …, want …（--from のディレクトリは v31.1.8 のものか）` で落ちる）。
- **キャッシュは内容アドレス**（`<sha256>.xml`）で索引ファイルを持たない。したがって
  **投入済みディレクトリを tar で運んで展開しても同じ**ことができる。
- **既定束を全件にしない理由。** 203 件 40.8 MB のうち `aws4.xml` が 6.21 MB、
  `rack/hpe_aruba/switches.xml` が 3.67 MB を占める。実際に要るクラウド／インフラ図と
  汎用作図は 49 件 17.0 MB に収まる（`mscae/*`・`office/*`・`rack/` の機種別＋
  aws4/aws3/gcp2/azure/ibm/kubernetes/networks/basic/flowchart/bpmn/… ）。
  **足りなければ `--all`**。
- **絞ったことは必ず出す。** 実行のたびに「対象 49 件 17.0 MB（対象外 154 件 23.8 MB
  —— 全件なら --all）」を表示する。黙って打ち切ると「全部入った」と読まれる。
- **これは台帳を絞るのとは別の話**である。台帳は全件（絞ると図が黙って劣化する。
  §65.5.3）で、絞ってよいのは「先に置いておく分」だけ。既定束に無いセットも、外に
  出られる環境なら要求された時点で CP が取りに行く。
- 失敗しても図は開く（枠と色だけになる）。ただし終了ステータスは正直に 1 を返す。

### 65.5.6 サンプル

`docs/assets/architecture.drawio` —— **Agent Fleet 自身の構成図**（ページ 1: AWS 上の
ECS/EC2 デプロイ、ページ 2: Docker Compose の 1 ホスト）。ペインを開いて実物を見るための
ものだが、この機能が扱うものをひととおり通る:

- ベンダーアイコン（`mxgraph.aws4.*` の 18 種）＝ P1 の経路。取れないと図案だけが空になる
- グループ図形（`group_aws_cloud_alt` / `group_vpc` / `group_subnet`）
- 複数ページ・ページ送り

**このファイル自体が正**である（drawio で開いて普通に編集してよい）。**圧縮せず素の XML で
コミットしてある**ので、ソース面でも git の差分でもそのまま読める。追加時は実ブラウザで描いて
**未解決のシェイプが 0 件**（＝シェイプ名の綴り間違いが無い）ことと、明暗どちらでも読めることを
確認した。**シェイプ名を間違えても図は開いてしまい、その図形だけが黙って矩形になる**ので、
`aws4.xml` に対して名前を突き合わせるのが確実（`grep -o 'name="[^"]*"' aws4.xml`）。

手で XML を書き足すときの実測メモ:

- **ラベルの改行は `&#10;` で書く。** XML は属性値のリテラル改行を空白へ正規化するので、
  素の改行を入れても 1 行に潰れる（`&#10;` なら mxText が `<br>` にする）。
- **アイコンの下にラベルが出るので、上下に素直に線を引くと必ず文字を貫く。**
  `exitX/exitY` と `entryX/entryY` で側面から出し入れして逃がす。
- 出入口を指定しない線は箱を突き抜けて隣の要素へ入ることがある（ブラウザ → Caddy が
  control-plane の箱を貫いた）。

## 65.6 セキュリティ

| 経路 | 扱い |
|------|------|
| 図面の外部持ち出し（lightbox） | ツールバーから外す ＋ `sandbox` に `allow-popups` を与えない（二重） |
| `STENCIL_PATH` / `SHAPES_PATH` 既定の外部取得 | **dead value で潰したまま**。フレームはステンシルも自分で取りに行かない（§65.5.4） |
| ラベル内の HTML / `javascript:` リンク | ビューア同梱の DOMPurify ＋ オリジンなし iframe（`allow-same-origin` なし）で二重 |
| 任意 URL 取得（SSRF） | ステンシル名を CP 同梱の台帳で照合（65.5.3）。要求は URL を運ばず、CP が台帳の `base` から組み立てる |
| ステンシル経路の認証 | **authGate の内側**。取りに来るのは cookie を持つ親で、CORS も認証除外も要らない（§65.5.4） |
| ステンシルの改竄 | 取得バイト列を台帳の sha256 とサイズで照合してから保存・配布（`drawio_stencils.go`） |
| iframe からの通信 | `Content-Security-Policy: default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'`。**外部オリジンを script-src に載せない** —— 載せるとフレームが自分で取りに行く経路が復活し、§65.11-7 の欠陥に戻る。ステンシルも同じ理由で**親が取って渡す**ので、`connect-src` は **`none` のまま**（§65.5.4） |
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
| **P1** ✅ | ステンシルの CP プロキシ＋キャッシュ＋台帳（65.5.3）、フレーム申告 → 親取得の往復（65.5.4） | `aws4` を含む図でアイコンが出る／`rackGeneral` が `rack/general.xml` に読み替わる／圧縮図でも同じセットに行き着く／台帳に無い名前が 404／改竄バイト列を拒否／2 回目はキャッシュから／取れなくても図は開いたまま —— **`drawio:check` の 5 ケース＋ `drawio_stencils_test.go` の 10 本**（実機の Console での目視は未） |
| **P1b** ✅ | 閉域向けの事前投入（`control-plane drawio-preseed`・65.5.5） | 既定束 49 件 17.0 MB が入る／2 回目は「既にあった」で冪等／`--from` は台帳の sha256 で照合してから置き、改竄・版ずれを何も書かずに拒否する／**投入済みなら upstream 到達不能でも配れる** —— `drawio_stencils_test.go` の 8 本 |
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

## 65.12 操作（ズーム / パン）

**`GraphViewer` はジェスチャを一切配線していない。** `init` は `pinchEnabled = false`・
`setPanning(false)` で終わり、`addMouseWheelListener` の購読も無い（ツールバーのボタンだけ）。
そこでフレーム側に実装した:

| 操作 | 効果 |
|------|------|
| **Ctrl / ⌘ ＋ ホイール** | 指した点を軸に拡大縮小。**トラックパッドのピンチも同じ経路**（ブラウザが `ctrlKey` を立てる） |
| 素のホイール | 上下左右へパン（倍率は変えない） |
| **2 本指ピンチ**（スマホ） | 2 点の中点を軸に拡大縮小 |
| 1 本指ドラッグ / 左ドラッグ | パン |
| **ダブルタップ / ダブルクリック** | 収まり ↔ 等倍（収まりが既に等倍なら 2 倍） |

- 倍率は **0.05〜16** に収める。軸を固定した拡大は
  `translate' = translate + p × (1/s' − 1/s)`（mxGraph は `screen = (graph + translate) × scale`）。
- **`touch-action: none` が要る。** これが無いとスマホのピンチはブラウザのページ拡大に
  取られ、図には届かない。あわせて `overscroll-behavior: contain` で、端まで動かしたときに
  親ペインが引っ張られるのを止める。
- **利用者が動かしたらペインの寸法が変わっても収め直さない**（拡大して見ている最中に
  元へ戻されるのが一番困る）。寸法だけ追従させる。
- ツールバーの上で始まった操作は素通しする（ボタンを潰さないため）。
- 倍率が変わるたびに親へ `rendered` を返すので、ヘッダの「%」表示はそのまま追従する。

判定は `scripts/drawio/check.mjs` が実ブラウザで行う（CDP の `Input.dispatchMouseEvent` /
`dispatchTouchEvent` で実際に押し、倍率の変化を見る）。実測: 収まり 1 →
Ctrl+ホイール 1.82 → ピンチ 4.37 → ダブルタップ 1。

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
10. **`"dark-mode"` は真偽値では効かない。** `GraphViewer.isDarkMode()` は
   `"dark" == this.darkMode || "auto" == this.darkMode && matchMedia(…)` という
   **文字列比較**なので、`true` を渡すと `false` に落ちて黙ってライト描画になる。
   こちらは背景だけ暗くしていたため、**既定色（黒）のラベルが暗い背景に載って消えた**
   —— 実測で文字の輝度 0・背景 30（コントラスト比 1.3:1）。`"dark"` / `"light"` を渡す。
   **判定を画素で書いてはいけない**: 暗色にならない絵の方が図形の明るい塗りのぶん
   「明るい画素」が多くなり（40778 対 2387）、素朴なしきい値は真逆の答えを出した。
   ハーネスは **ビューアの `isDarkMode()` を返させて要求と突き合わせる**（この判定は
   真偽値に戻すと実際に赤くなることを確認済み）。
11. **`fitGraph()` はコンテナ幅が前回と同じなら何もしない**（`N == t` で早期 return）。
   寸法が変わらないダブルタップの「収め直す」がこれで無反応になっていた（4.37 倍のまま）。
   ビューアが控えている `graph.initialViewState` を戻す方が速く、ズームボタンの基準とも
   食い違わない。
12. **同じフレームの中でテーマは切り替わらない。** drawio の `darkModeChanged()` は
   CSS クラス（`geDarkMode`）と `color-scheme` を触るだけで、色の決定は読み込み・初回
   描画の時点で固まる。1 文書内でのテーマ往復は想定されていない —— 実測では、同じ
   フレームにライト → ダークの描き直しを頼むと、背景と図形の塗りは暗くなるのに
   **コンテナ見出しが消え、エッジのラベルはライト時の白いピル＋黒文字のまま残った**
   （ビューア自身は `isDarkMode() === true` と答えている）。
   → **テーマが変わったらフレームごと作り直す。** 代償は 4 MB の再評価（ブラウザ
   キャッシュから ~76 ms）だけで、テーマ切替は頻繁な操作ではない。
   **見ていた場所は引き継ぐ**: ページ（**番号ではなく diagram の id** ＝
   `graphConfig.pageId`。番号はページの増減でずれる）、倍率、位置。
   引き継ぎは**利用者が自分で動かしていたときだけ**行い、そうでなければ収め直す
   （何も操作していない人にとっては、収まっているのが正しい状態）。
   復元の前に一度 `fit()` を通すのは、`fitScale` と `initialViewState`
   （ダブルタップの戻り先）を確定させるため。
13. **背景色は「組み立て時のテーマ」で塗られ、描画要求は効いていなかった。** srcdoc の
   スタイルシートが `html, body` の**両方**に色を付けているので、描画時に `html` だけを
   inline で上書きしても **`body` の指定が上に塗る**。組み立てと要求をわざと食い違わせて
   測ると、**組み立て dark ＋ 要求 light → 背景 #1e1e1e のまま**、**組み立て light ＋
   要求 dark → 白のまま**。利用者から出た「ダークにしても白いまま」「ライトにしても
   背景が暗いまま（要素だけライト）」は**どちらもこれ 1 つ**だった。
   → 描画のたびに **`html` と `body` の両方へ inline で**置く。
   **背景色は画素で判定してよい**（単色なので指標が素直に対応する。§65.11-10 の
   コントラストと違い、しきい値が真逆を向くことはない）—— ハーネスは組み立てと要求を
   食い違わせたケースで隅の画素を読む。
14. **図には朗読を出さない。** 読み上げる本文が無く（あるのは mxfile の XML）、押しても
   意味のある結果にならない。ヘッダのボタンもキーボードコマンドも対象外にする。
9. **「読み込めなかった」と「図として読めない」を混ぜない。** 7 の不具合はビューアの
   読み込み失敗だったのに、`try/catch` が一律 `parse` を返していたため画面には
   「drawio の図として解釈できません」と出て、**原因がファイル側にあるように見えた**。
   `code: "boot"` を分けて別の文言にした。
