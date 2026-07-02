// File-type icons. Brand SVGs (VS Code icons set, MIT) bundled as assets, mapped
// extension/filename -> a set-independent "type key" (e.g. "kotlin") -> the matching
// <key>.svg. Ported from CodeLeaf's FileIcons.kt (typeKey + mark split). Files with
// no brand icon fall back to a monochrome codicon (resolved in components/FileIcon).
// See assets/fileicons/ATTRIBUTION.md. The colorful file icons pair with the
// monochrome codicon chrome the way VS Code's file-icon theme pairs with its codicons.

// Eagerly resolve every bundled brand SVG to its hashed URL (Vite), across all sets
// (assets/fileicons/<set>/<key>.svg). Keyed "set/key" so a chosen set + type key
// (e.g. "seti/kotlin") resolves to its url. Tiny SVGs, bundled once at load.
const mods = import.meta.glob<string>("../assets/fileicons/*/*.svg", {
  eager: true,
  query: "?url",
  import: "default",
});
const ICON_URL: Record<string, string> = {}; // "set/key" -> url
for (const p in mods) {
  const parts = p.split("/");
  const key = (parts.pop() ?? "").replace(".svg", "");
  const set = parts.pop() ?? "";
  ICON_URL[set + "/" + key] = mods[p];
}

// extension -> type key (set-independent). Mirrors CodeLeaf FileIcons.byExt.
const BY_EXT: Record<string, string> = {};
const reg = (key: string, ...exts: string[]) => exts.forEach((e) => (BY_EXT[e] = key));
reg("kotlin", "kt", "kts");
reg("java", "java", "jar", "class");
reg("groovy", "groovy");
reg("gradle", "gradle");
reg("scala", "scala", "sc");
reg("c", "c", "h");
reg("cplusplus", "cpp", "cc", "cxx", "hpp", "hh");
reg("csharp", "cs");
reg("javascript", "js", "mjs", "cjs");
reg("typescript", "ts", "mts", "cts");
reg("react", "jsx", "tsx");
reg("css3", "css");
reg("sass", "scss", "sass");
reg("less", "less");
reg("html5", "html", "htm", "xhtml");
reg("xml", "xml", "svg", "plist");
reg("python", "py", "pyw", "pyi");
reg("go", "go");
reg("swift", "swift");
reg("dart", "dart");
reg("markdown", "md", "markdown", "mdx");
reg("ruby", "rb", "gemspec");
reg("php", "php");
reg("rust", "rs");
reg("bash", "sh", "bash", "zsh");
reg("vuejs", "vue");
reg("nodejs", "node");
reg("json", "json");
reg("yaml", "yml", "yaml");
reg("docker", "dockerfile");

// whole-filename (lowercased) -> type key, for files with no useful extension.
const BY_NAME: Record<string, string> = {
  dockerfile: "docker",
  ".gitignore": "git",
  ".gitattributes": "git",
  ".gitmodules": "git",
  "build.gradle": "gradle",
  "build.gradle.kts": "gradle",
  "settings.gradle.kts": "gradle",
};

// typeKey resolves a filename to its brand key (or null). Whole-name wins over ext.
export function typeKey(name: string | null | undefined): string | null {
  const n = (name || "").toLowerCase();
  if (BY_NAME[n]) return BY_NAME[n];
  const ext = n.includes(".") ? n.split(".").pop() ?? "" : "";
  return BY_EXT[ext] || null;
}

// --- per-set resolution + tint (ported from FileIcons.kt spec/coverage) ---

// Seti glyphs are monochrome; tint them per type (seti-ui mapping.less palette).
const SETI_DEFAULT = "#d4d7d6";
const SETI_COLOR: Record<string, string> = {
  kotlin: "#e37933", java: "#cc3e44", gradle: "#519aba", scala: "#cc3e44",
  c: "#519aba", cplusplus: "#519aba", csharp: "#519aba", javascript: "#cbcb41",
  typescript: "#519aba", react: "#519aba", css3: "#519aba", sass: "#f55385",
  less: "#519aba", html5: "#e37933", xml: "#e37933", python: "#519aba",
  go: "#519aba", swift: "#e37933", dart: "#519aba", markdown: "#519aba",
  ruby: "#cc3e44", php: "#a074c4", rust: "#d4d7d6", bash: "#8dc149",
  vuejs: "#8dc149", json: "#cbcb41", docker: "#519aba", git: "#687d8a",
};
// Devicon ships these as black logos (no fill colors) → tint to theme text color.
const DEVICON_MONO = new Set(["markdown", "rust", "json", "yaml"]);
// Seti lacks these keys → fall back to the generic icon for them.
const SETI_MISSING = new Set(["groovy", "nodejs"]);

// How to render a filename's icon in the chosen set.
export type IconRender =
  | { url: string; tint: "none" }
  | { url: string; tint: "mask"; color: string };

// resolveIcon returns how to render a filename's icon in the chosen set, or null
// when there's no brand icon (caller uses a codicon).
export function resolveIcon(set: string, name: string | null | undefined): IconRender | null {
  const key = typeKey(name);
  if (!key) return null;
  if (set === "seti" && SETI_MISSING.has(key)) return null;
  const url = ICON_URL[set + "/" + key];
  if (!url) return null;
  if (set === "seti") return { url, tint: "mask", color: SETI_COLOR[key] || SETI_DEFAULT };
  if (set === "devicon" && DEVICON_MONO.has(key)) return { url, tint: "mask", color: "currentColor" };
  return { url, tint: "none" };
}

// --- special marks (priority order) — drive emphasis, ported from FileIcons.kt ---
const AI_NAMES = new Set([
  "claude.md", "claude.local.md", "agents.md", "gemini.md",
  "copilot-instructions.md", ".cursorrules", ".windsurfrules",
  ".aider.conf.yml", ".aiderignore", "llms.txt", "llms-full.txt", ".mcp.json",
  ".claude", ".cursor", ".windsurf", ".continue", ".aider", ".codeium",
]);
const SECRET_EXTS = new Set(["pem", "key", "keystore", "jks", "p12", "pfx", "ppk"]);

function isSecret(n: string): boolean {
  if (n.endsWith(".pub")) return false;
  if (n === ".env" || n.startsWith(".env.")) {
    return !["example", "sample", "template", "dist", "defaults"].includes(n.slice(5));
  }
  if (n === "credentials" || n.startsWith("credentials.")) return true;
  if (/^id_(rsa|dsa|ecdsa|ed25519)/.test(n)) return true;
  return SECRET_EXTS.has(n.includes(".") ? n.split(".").pop() ?? "" : "");
}

// mark classifies a filename for emphasis. AI / SECRET also override the glyph
// (handled in FileIcon); the rest are styling hints the tree may apply later.
export function mark(name: string | null | undefined): "ai" | "secret" | "none" {
  const n = (name || "").toLowerCase();
  if (AI_NAMES.has(n)) return "ai";
  if (isSecret(n)) return "secret";
  return "none";
}
