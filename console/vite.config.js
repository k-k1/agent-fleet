import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

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
  build: {
    outDir: "dist",
    emptyOutDir: true,
    // No sourcemaps: mermaid is large and sourcemap generation blew the Node heap
    // on this RAM-constrained host. Re-enable locally if you need to debug.
    sourcemap: false,
    chunkSizeWarningLimit: 1500,
  },
});
