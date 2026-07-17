import { fileURLToPath } from "node:url";
import { execSync } from "node:child_process";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// marp-core statically requires mathjax-full (~43MB) and katex for its math
// feature. We render with { math: false } and verified those modules are never
// touched at runtime in that mode, so we alias them to a tiny stub to keep them
// out of the bundle — otherwise the production build hangs at minification.
const mathStub = fileURLToPath(new URL("./marp-math-stub.js", import.meta.url));

// Build stamp injected into the client (see src/lib/version.ts). The ISO timestamp is
// always available and is the canonical build id the running app compares against
// dist/version.json to detect a newer deploy and offer a reload; the short git SHA is
// best-effort for display (empty if the image build context has no .git — harmless).
function afBuildStamp() {
  const time = new Date().toISOString();
  let sha = process.env.AF_BUILD_SHA || "";
  if (!sha) {
    try {
      sha = execSync("git rev-parse --short HEAD", { stdio: ["ignore", "pipe", "ignore"] })
        .toString()
        .trim();
    } catch {
      /* no git in the build context — the timestamp alone identifies the build */
    }
  }
  return { time, sha };
}
const AF_BUILD = afBuildStamp();

// Emit dist/version.json (stable, unhashed path) so the running client can poll the
// server's current build id with cache:'no-store' and prompt a reload when it moves —
// the fix for a phone PWA that kept running old code after a deploy.
function afVersionManifest(build) {
  return {
    name: "af-version-manifest",
    generateBundle() {
      this.emitFile({ type: "asset", fileName: "version.json", source: JSON.stringify(build) });
    },
  };
}

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
  // Build id available to the client as a compile-time constant (src/lib/version.ts).
  define: { __AF_BUILD__: JSON.stringify(AF_BUILD) },
  plugins: [react(), afVersionManifest(AF_BUILD)],
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
  // .tsx is included so component tests that assert on renderToStaticMarkup output run
  // too: CommitGraph.test.tsx sat here silently unexecuted under a .ts-only glob while
  // the graph it covers shipped with broken merge edges.
  // Worker cap per the shared-host memory rule (workspace notes).
  test: {
    environment: "node",
    include: ["src/**/*.test.{ts,tsx}"],
    maxWorkers: 2,
    minWorkers: 1,
  },
});
