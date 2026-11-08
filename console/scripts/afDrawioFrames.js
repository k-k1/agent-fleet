// Vite plugin: emits drawio-frame-light.html / drawio-frame-dark.html next to index.html, built
// from drawioFrameSrcdoc() in src/features/viewer/drawioFrame.ts.
//
// Why files and not a srcdoc: an about:srcdoc document inherits its embedder's CSP, and the
// Console's is `script-src 'self'` (writegate.FrameHeaders). The frame runs an inline boot script
// and evaluates the viewer source it receives by postMessage as an inline script, so it needs
// `'unsafe-inline'`. A document loaded from a URL carries the CSP of its own response, which the
// Control Plane sets for these two paths only (writegate.DrawioFrame).
//
// The generator is loaded from disk on every build and registered as a watched file, for the same
// reason as afKindTokens: under `vite build --watch` a static import would freeze it.
import fs from "node:fs";
import { fileURLToPath } from "node:url";
import { transformWithEsbuild } from "vite";

const SRC = fileURLToPath(new URL("../src/features/viewer/drawioFrame.ts", import.meta.url));

async function loadBuilder() {
  // drawioFrame.ts imports nothing, so its transpiled text runs as a data: module.
  const { code } = await transformWithEsbuild(fs.readFileSync(SRC, "utf8"), SRC, { loader: "ts" });
  return import("data:text/javascript;base64," + Buffer.from(code).toString("base64"));
}

export function afDrawioFrames() {
  return {
    name: "af-drawio-frames",
    async generateBundle() {
      this.addWatchFile(SRC);
      const mod = await loadBuilder();
      for (const dark of [false, true]) {
        this.emitFile({ type: "asset", fileName: mod.drawioFrameFile(dark), source: mod.drawioFrameSrcdoc({ dark }) });
      }
    },
  };
}
