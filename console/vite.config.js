import { createRequire } from "node:module";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { execSync } from "node:child_process";
import { defineConfig } from "vite";
import { configDefaults } from "vitest/config";
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

// pdf.js の cMap（CID 符号化表）と標準14フォントは、パッケージの中にディレクトリごと
// 入っていてバンドラが辿れない。dist/assets/pdfjs/<version>/ へ丸ごと複製し、実行時は
// src/features/viewer/pdfjs.ts がその URL を組み立てる（docs/82 §82.4）。
//
// - **同梱しないと日本語 PDF が壊れる**: フォントを埋め込んでいない PDF（UniJIS-UCS2-H
//   などの符号化）は、cMap 無しでは文字が出ない。
// - パスに版を入れるのは、assets/ 配下が CP から immutable で配られるため。版を上げると
//   URL ごと変わるので、古い cMap が居座らない。
const pdfjsVersion = createRequire(import.meta.url)("pdfjs-dist/package.json").version;

function afPdfjsAssets(version) {
  const from = fileURLToPath(new URL("./node_modules/pdfjs-dist", import.meta.url));
  return {
    name: "af-pdfjs-assets",
    apply: "build",
    closeBundle() {
      const to = path.join(fileURLToPath(new URL("./dist", import.meta.url)), "assets", "pdfjs", version);
      for (const dir of ["cmaps", "standard_fonts"]) {
        const src = path.join(from, dir);
        if (!fs.existsSync(src)) {
          this.warn(`pdfjs-dist/${dir} is missing — PDFs without embedded fonts will render wrong`);
          continue;
        }
        fs.cpSync(src, path.join(to, dir), { recursive: true });
      }
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
  define: { __AF_BUILD__: JSON.stringify(AF_BUILD), __AF_PDFJS_VERSION__: JSON.stringify(pdfjsVersion) },
  plugins: [react(), afVersionManifest(AF_BUILD), afPdfjsAssets(pdfjsVersion)],
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
  // vitest — two projects, because a jsdom environment costs ~1.3s to build per
  // test FILE. Running everything under jsdom measured 10.8s -> 51.3s here, and
  // the worker cap below (the shared-host memory rule, workspace notes) means
  // that cannot be parallelised away. So the default stays node, and only tests
  // that genuinely mount components opt in via the *.dom.test.tsx suffix.
  test: {
    maxWorkers: 2,
    minWorkers: 1,
    projects: [
      {
        extends: true,
        test: {
          name: "node",
          // Pure logic (layout ops, parsers, stores). .tsx is included so
          // component tests that assert on renderToStaticMarkup output run too:
          // CommitGraph.test.tsx sat here silently unexecuted under a .ts-only
          // glob while the graph it covers shipped with broken merge edges.
          environment: "node",
          include: ["src/**/*.test.{ts,tsx}"],
          exclude: [...configDefaults.exclude, "src/**/*.dom.test.tsx"],
        },
      },
      {
        extends: true,
        test: {
          // Render tests: a real component tree over the real DOM. Vitest 4
          // dropped the per-file `@vitest-environment` docblock, so the split
          // has to be a project.
          name: "dom",
          environment: "jsdom",
          include: ["src/**/*.dom.test.tsx"],
          setupFiles: ["./src/test/domSetup.ts"],
        },
      },
    ],
  },
});
