import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// marp-core statically requires mathjax-full (~43MB) and katex for its math
// feature. We render with { math: false } and verified those modules are never
// touched at runtime in that mode, so we alias them to a tiny stub to keep them
// out of the bundle — otherwise the production build hangs at minification.
const mathStub = fileURLToPath(new URL("./marp-math-stub.js", import.meta.url));

// The Console is served as static files by the Control Plane and may live behind
// a path-stripping proxy (Tailscale Funnel + Caddy strips /agent-fleet). All asset
// URLs must therefore be *relative*, so we set base:'./'. The app additionally
// resolves API/WS URLs via document.baseURI (see src/api.js).
//
// Output goes to dist/, which the CP serves with Cache-Control: no-store. Run
// `npm run dev` (vite build --watch) during development: edits rebuild dist/ and a
// browser reload reflects them — keeping the CP as the single origin so the
// oauth2-proxy / tenant-header chain behaves exactly as in production.
export default defineConfig({
  base: "./",
  plugins: [react()],
  resolve: {
    alias: [
      { find: /^mathjax-full(\/.*)?$/, replacement: mathStub },
      { find: /^katex$/, replacement: mathStub },
      { find: /^katex\/package\.json$/, replacement: mathStub },
    ],
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    // No sourcemaps: mermaid is large and sourcemap generation blew the Node heap
    // on this RAM-constrained host. Re-enable locally if you need to debug.
    sourcemap: false,
    chunkSizeWarningLimit: 1500,
  },
  // vitest — pure-logic tests only (layout ops, parsers, stores); node env, no DOM.
  // Worker cap per the shared-host memory rule (workspace notes).
  test: {
    environment: "node",
    include: ["src/**/*.test.ts"],
    maxWorkers: 2,
    minWorkers: 1,
  },
});
