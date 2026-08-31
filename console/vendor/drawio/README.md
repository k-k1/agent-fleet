# drawio ビューア（同梱物）

`.drawio` を File ペインで図として表示するために、drawio 公式の自己完結ビューアを
**そのまま** 置いている（docs/log/65 / [ADR 0046](../../../docs/decisions/0046-drawio-viewer.md)）。

| | |
|---|---|
| ファイル | `viewer-static.min.js`（4,137,189 bytes / gzip 約 870 KB） |
| バージョン | **v31.1.8** |
| SHA-256 | `a19f7399f6417509bd7610138991883d6167af48a5eacf485b70b1334cf8e451` |
| 取得元 | `https://raw.githubusercontent.com/jgraph/drawio/v31.1.8/src/main/webapp/js/viewer-static.min.js` |
| ライセンス | Apache-2.0（JGraph Ltd）。帰属は本リポジトリの `NOTICE` に記載 |

## なぜ npm ではなくベンダリングなのか

drawio のビューアを含む npm パッケージが存在しない（`mxgraph` は下回りだけで drawio の
図形定義を含まない・`drawio` は同名の無関係パッケージ・`react-drawio` は外部 embed の
ラッパ）。ビルドを外部ネットワークに依存させないため、リポジトリに置く方を採った。

## 更新の手順

```sh
V=v31.2.0   # 上げたいタグ
curl -sSL -o console/vendor/drawio/viewer-static.min.js \
  "https://raw.githubusercontent.com/jgraph/drawio/$V/src/main/webapp/js/viewer-static.min.js"
sha256sum console/vendor/drawio/viewer-static.min.js   # この表の値を書き換える
# ↑ を書き換えてから台帳（ステンシルの sha256）を焼き直す。版が食い違うと自分で止まる。
node console/scripts/drawio/stencils-manifest.mjs --tag "$V" --write
npm --prefix console run drawio:check                  # 実ブラウザで描画と外部通信 0 件を確認
(cd control-plane && go test -run TestDrawio ./...)    # 台帳と SSRF の防壁
```

**ビューアを上げたら台帳も必ず焼き直すこと。** ステンシルのバイト列は同梱せず
`control-plane/assets/drawio-stencils.json`（名前 → sha256 → サイズ）だけを持ち、CP は
その sha256 と照合してから配る（docs/log/65 §65.5.3）。版がずれると、名前の変わったセットが
黙って 404 になったり、正しいバイト列が「改竄」として弾かれたりする。

**上げたら必ず `drawio:check` を通すこと。** このファイルは外部 URL を既定値として
持っており（`window.X = window.X || "https://viewer.diagrams.net/…"` の形）、版が変われば
その一覧も変わる。`src/features/viewer/drawioFrame.ts` はその既定値を **空文字ではなく
dead value で** 潰しているが、新しい名前が増えていれば取りこぼす —— それを捕まえるのが
あのハーネスの「外部への要求 0 件」の判定である。

配布物の中でどう扱われるかは Vite が決める（`?url` インポート ＝ ハッシュ付きで
`dist/assets/` へ。CP が immutable キャッシュを付けるのはこのディレクトリだけ）。
`console/public/` へ移すと `no-store` になり、ペインを開くたびに 4 MB を取り直す。

**このファイルを iframe に `<script src>` で読ませてはならない。** サンドボックス
iframe はオリジンを持たないため要求が cross-site 扱いになり、`SameSite=Lax` の
セッション cookie が付かず CP の `authGate` に 401 で弾かれる（docs/log/65 §65.11-7）。
親が `fetch` して本文を postMessage で渡す —— それが `DrawioView.tsx` の作法。
