// pdf.js の遅延読み込みと同梱アセットの配線（docs/log/82 §82.4）。
//
// pdf.js 本体（gzip 約 126KB）とワーカー（同 366KB）は PDF を開いたときにだけ要る。
// 静的 import にすると起動時の主チャンクに載るので、本体は動的 import で分離し、
// ワーカーは `?url` で「アセットとして出すが、載るのは URL 文字列だけ」にする
// （drawio のビューアと同じ作法 — DrawioView.tsx）。
//
// cMap（CID フォントの符号化表）と標準14フォントは npm パッケージの中にディレクトリ
// ごと入っていて、バンドラは辿れない。vite.config.js の afPdfjsAssets プラグインが
// dist/assets/pdfjs/<version>/ へ丸ごと複製し、ここがその URL を組み立てる。
// **cMap が無いと、フォントを埋め込んでいない日本語 PDF は文字化けするか空白になる**
// （UniJIS-UCS2-H などの符号化を pdf.js が解けなくなる）ため、同梱は必須。
import workerUrl from "pdfjs-dist/build/pdf.worker.min.mjs?url";
import type * as PdfjsModule from "pdfjs-dist";

export type Pdfjs = typeof PdfjsModule;

// vite `define` が焼き込む pdfjs-dist の版（アセットの置き場をこの版で仕切る）。
// テストなど define の無い文脈では空文字になり、同梱アセットは使わない。
const VERSION: string = typeof __AF_PDFJS_VERSION__ !== "undefined" ? __AF_PDFJS_VERSION__ : "";

/** 同梱アセットのディレクトリ URL（末尾スラッシュ付き）。版が分からなければ空文字。
 *
 *  document.baseURI 起点なのは、Console がパスを剥がすプロキシ配下にも載るから
 *  （core/api/client の rel() と同じ理由）。 */
export function pdfjsAssetURL(dir: "cmaps" | "standard_fonts"): string {
  if (!VERSION) return "";
  return new URL(`assets/pdfjs/${VERSION}/${dir}/`, document.baseURI).toString();
}

let loading: Promise<Pdfjs> | null = null;

/** pdf.js を読み込み、ワーカーを配線して返す。2 度目以降は同じ Promise。 */
export function loadPdfjs(): Promise<Pdfjs> {
  if (!loading) {
    loading = import("pdfjs-dist").then((pdfjs) => {
      // ワーカー無し（workerSrc 未設定）でも pdf.js は「偽ワーカー」で動くが、その場合
      // 解析がメインスレッドを占有してペインごと固まる。必ず配線する。
      pdfjs.GlobalWorkerOptions.workerSrc = new URL(workerUrl, document.baseURI).toString();
      return pdfjs;
    });
  }
  return loading;
}

/** 文書を開くときに渡す共通パラメータ（同梱アセットの場所）。 */
export function documentAssetParams(): { cMapUrl?: string; cMapPacked: boolean; standardFontDataUrl?: string } {
  const cMapUrl = pdfjsAssetURL("cmaps");
  const standardFontDataUrl = pdfjsAssetURL("standard_fonts");
  return {
    ...(cMapUrl ? { cMapUrl } : {}),
    cMapPacked: true,
    ...(standardFontDataUrl ? { standardFontDataUrl } : {}),
  };
}
