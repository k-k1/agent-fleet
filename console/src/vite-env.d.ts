/// <reference types="vite/client" />

// Build stamp injected by vite `define` (see vite.config.js / src/lib/version.ts).
declare const __AF_BUILD__: { readonly time: string; readonly sha: string };

// 同梱した pdf.js アセット（cMap / 標準フォント）の置き場を仕切る版
// （vite.config.js の afPdfjsAssets / src/features/viewer/pdfjs.ts）。
declare const __AF_PDFJS_VERSION__: string;
