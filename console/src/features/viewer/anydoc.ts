// anydoc（Firecrawl・MIT・Rust → WASM）の遅延読み込み（docs/log/82 §82.4）。
//
// Word / Excel / PowerPoint を GFM へ変換し、Console の MarkdownView に載せるための層。
// **見た目の再現はしない**（ページ体裁・図形の位置・セルの色は落ち、画像は alt text になる）。
// 狙いは「読める・検索できる・引用できる」ところまでで、それは ADR 0063 の決定 3。
//
// WASM は gzip 2.9MB と大きいので、静的に import してはいけない。その形式のファイルを
// 開いた人だけが払うよう、本体もバイナリも動的に取りに行く（pdf.js と同じ作法）。
import wasmUrl from "@firecrawl/anydoc-wasm/anydoc_wasm_bg.wasm?url";
import type * as AnydocModule from "@firecrawl/anydoc-wasm";

/** anydoc が受け取る形式名（拡張子と同じ綴り）。 */
export type AnydocFormat = AnydocModule.Format;

/** 変換が失敗した理由。表示の文言はこの値で選ぶ（docs/log/82 §82.4）。 */
export type AnydocFailure = "unsupported" | "needsOcr" | "malformed" | "encrypted" | "resourceLimit" | "missingPart" | "failed";

let loading: Promise<typeof AnydocModule> | null = null;

/** WASM を読み込んで初期化する。2 度目以降は同じ Promise。 */
export function loadAnydoc(): Promise<typeof AnydocModule> {
  if (!loading) {
    loading = import("@firecrawl/anydoc-wasm").then(async (mod) => {
      await mod.default({ module_or_path: new URL(wasmUrl, document.baseURI).toString() });
      return mod;
    });
  }
  return loading;
}

/** anydoc が投げた Error から失敗の種類を読む。`code` を持たないものは failed 扱い。 */
export function anydocFailure(e: unknown): AnydocFailure {
  const code = (e as { code?: string } | null)?.code;
  switch (code) {
    case "unsupported":
    case "needsOcr":
    case "malformed":
    case "encrypted":
    case "resourceLimit":
    case "missingPart":
      return code;
    default:
      return "failed";
  }
}

/** バイト列を GFM へ変換する。形式は中身から判定させ、拾えないときだけ拡張子で補う。 */
export async function toMarkdown(bytes: Uint8Array, format?: AnydocFormat): Promise<string> {
  const anydoc = await loadAnydoc();
  return anydoc.toMarkdownBytes(bytes, format ?? null);
}
