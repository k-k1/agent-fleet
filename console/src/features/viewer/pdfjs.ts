// Lazy loading of pdf.js and the wiring of its bundled assets (docs/log/82 §82.4).
//
// pdf.js itself (about 126KB gzipped) and its worker (366KB) are only needed once a PDF is
// opened. A static import would put them in the startup main chunk, so the library is split
// out through a dynamic import and the worker goes through `?url` — emitted as an asset with
// only the URL string in the bundle (the same discipline as the drawio viewer,
// DrawioView.tsx).
//
// The cMaps (CID font encoding tables) and the 14 standard fonts sit as whole directories
// inside the npm package, which the bundler cannot follow. The afPdfjsAssets plugin in
// vite.config.js copies them wholesale into dist/assets/pdfjs/<version>/, and this module
// builds those URLs. Shipping them is mandatory: without the cMaps a Japanese PDF with no
// embedded fonts renders as mojibake or as blanks, because pdf.js cannot resolve encodings
// such as UniJIS-UCS2-H.
import workerUrl from "pdfjs-dist/build/pdf.worker.min.mjs?url";
import type * as PdfjsModule from "pdfjs-dist";

export type Pdfjs = typeof PdfjsModule;

// The pdfjs-dist version baked in by vite `define`; asset directories are partitioned by it.
// Where there is no define (tests) it is the empty string and no bundled assets are used.
const VERSION: string = typeof __AF_PDFJS_VERSION__ !== "undefined" ? __AF_PDFJS_VERSION__ : "";

/** Directory URL of a bundled asset set, with a trailing slash; empty when the version is
 *  unknown.
 *
 *  It resolves against document.baseURI because the Console can also be served behind a proxy
 *  that strips the path (the same reason as rel() in core/api/client). */
export function pdfjsAssetURL(dir: "cmaps" | "standard_fonts"): string {
  if (!VERSION) return "";
  return new URL(`assets/pdfjs/${VERSION}/${dir}/`, document.baseURI).toString();
}

let loading: Promise<Pdfjs> | null = null;

/** Loads pdf.js, wires up the worker and returns it. Later calls get the same Promise. */
export function loadPdfjs(): Promise<Pdfjs> {
  if (!loading) {
    loading = import("pdfjs-dist").then((pdfjs) => {
      // With no workerSrc pdf.js still runs, on a fake worker, but parsing then occupies the
      // main thread and freezes the whole pane. Always wire it up.
      pdfjs.GlobalWorkerOptions.workerSrc = new URL(workerUrl, document.baseURI).toString();
      return pdfjs;
    });
  }
  return loading;
}

/** Common parameters passed when opening a document: where the bundled assets live. */
export function documentAssetParams(): { cMapUrl?: string; cMapPacked: boolean; standardFontDataUrl?: string } {
  const cMapUrl = pdfjsAssetURL("cmaps");
  const standardFontDataUrl = pdfjsAssetURL("standard_fonts");
  return {
    ...(cMapUrl ? { cMapUrl } : {}),
    cMapPacked: true,
    ...(standardFontDataUrl ? { standardFontDataUrl } : {}),
  };
}
