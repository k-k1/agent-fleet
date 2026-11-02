// `npm run dev` is `vite build --watch`: a change to the kind table must reach the stylesheet as
// well as the JS. Runs the real plugin over a copy of the table in a temp project, changes a hue and
// adds a kind, and reads both rebuilds' CSS.
import { cpSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { expect, it } from "vitest";
import { build } from "vite";

it("rebuilds tokens.css when kindTable.ts changes under watch", async () => {
  const root = mkdtempSync(path.join(tmpdir(), "af-kindwatch-"));
  const here = path.resolve(__dirname, "..", "..");
  try {
    mkdirSync(path.join(root, "src", "agents"), { recursive: true });
    mkdirSync(path.join(root, "src", "styles"));
    mkdirSync(path.join(root, "scripts"));
    cpSync(path.join(here, "src/agents/kindTable.ts"), path.join(root, "src/agents/kindTable.ts"));
    cpSync(path.join(here, "scripts/afKindTokens.js"), path.join(root, "scripts/afKindTokens.js"));
    writeFileSync(
      path.join(root, "src/styles/tokens.css"),
      ":root {\n  /* @af-kind-tokens dark */\n}\n[data-theme=\"light\"] {\n  /* @af-kind-tokens light */\n}\n/* @af-kind-tokens map */\n",
    );
    writeFileSync(path.join(root, "index.html"), '<script type="module" src="/main.js"></script>');
    writeFileSync(path.join(root, "main.js"), 'import "./src/styles/tokens.css";\n');
    // vite resolves the plugin's own imports from the project, so link the real node_modules.
    const { symlinkSync } = await import("node:fs");
    symlinkSync(path.join(here, "node_modules"), path.join(root, "node_modules"));
    const { afKindTokens } = await import(pathToFileURL(path.join(root, "scripts/afKindTokens.js")).href);

    const css = () =>
      readdirSync(path.join(root, "dist/assets"))
        .filter((f) => f.endsWith(".css"))
        .map((f) => readFileSync(path.join(root, "dist/assets", f), "utf8"))
        .join("");
    const watcher: any = await build({
      root,
      logLevel: "silent",
      configFile: false,
      plugins: [afKindTokens()],
      build: { watch: {}, minify: false },
    });
    const next = (): Promise<void> =>
      new Promise((resolve, reject) => {
        const on = (e: any) => {
          if (e.code === "END") (watcher.off("event", on), resolve());
          if (e.code === "ERROR") (watcher.off("event", on), reject(e.error));
        };
        watcher.on("event", on);
      });
    await next();
    expect(css()).toContain("--kind-claude: #e0a45e");

    const table = path.join(root, "src/agents/kindTable.ts");
    const src = readFileSync(table, "utf8");
    const rebuilt = next();
    writeFileSync(
      table,
      src
        .replace('dark: "#e0a45e"', 'dark: "#123456"')
        .replace('  { id: "shell"', '  { id: "zz", managedOnly: false, managedDefault: false, mcpClient: "no", hue: { dark: "#654321", light: "#111111" } },\n  { id: "shell"'),
    );
    await rebuilt;
    await watcher.close();
    const out = css();
    expect(out).toContain("--kind-claude: #123456");
    expect(out).not.toContain("--kind-claude: #e0a45e");
    expect(out).toContain("--kind-zz: #654321");
    expect(out).toContain(".kind-zz { --kind: var(--kind-zz); }");
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
}, 60000);
