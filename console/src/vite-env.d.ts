/// <reference types="vite/client" />

// Build stamp injected by vite `define` (see vite.config.js / src/lib/version.ts).
declare const __AF_BUILD__: { readonly time: string; readonly sha: string };

// Version that namespaces where the bundled pdf.js assets (cMaps / standard fonts) live
// (afPdfjsAssets in vite.config.js / src/features/viewer/pdfjs.ts).
declare const __AF_PDFJS_VERSION__: string;
