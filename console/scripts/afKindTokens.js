// Vite plugin: expands the `/* @af-kind-tokens … */` markers in styles/tokens.css from
// agents/kindTable.ts, so the per-kind colour tokens and map are generated, not typed.
//
// The table is loaded from disk on every transform and registered as a watched file. Importing it
// statically from the config would freeze it at startup: under `vite build --watch` (npm run dev)
// a changed hue or a new kind then reached the JS but not the stylesheet (measured).
import fs from "node:fs";
import { fileURLToPath } from "node:url";
import { transformWithEsbuild } from "vite";

const TABLE = fileURLToPath(new URL("../src/agents/kindTable.ts", import.meta.url));

async function loadTable() {
  // kindTable.ts imports nothing, so its transpiled text runs as a data: module; the unique
  // content is what keeps the module cache from returning an older table.
  const { code } = await transformWithEsbuild(fs.readFileSync(TABLE, "utf8"), TABLE, { loader: "ts" });
  return import("data:text/javascript;base64," + Buffer.from(code).toString("base64"));
}

export function afKindTokens() {
  return {
    name: "af-kind-tokens",
    enforce: "pre",
    async transform(code, id) {
      if (!id.split("?")[0].endsWith("/styles/tokens.css")) return null;
      this.addWatchFile(TABLE);
      const out = (await loadTable()).expandKindTokens(code);
      if (out.includes("@af-kind-tokens")) this.error("tokens.css has a kind-token marker the table does not know");
      return { code: out, map: null };
    },
  };
}
