// File-type icons. Brand SVGs (VS Code icons set, MIT) bundled as assets, mapped
// extension/filename -> a set-independent "type key" (e.g. "kotlin") -> the matching
// <key>.svg. Ported from CodeLeaf's FileIcons.kt (typeKey + mark split). Files with
// no brand icon fall back to a monochrome codicon (resolved in components/FileIcon).
// See assets/fileicons/ATTRIBUTION.md. The colorful file icons pair with the
// monochrome codicon chrome the way VS Code's file-icon theme pairs with its codicons.

// Eagerly resolve every bundled brand SVG to its hashed URL (Vite). Keyed by basename
// so typeKey "kotlin" -> ICON_URL["kotlin"]. ~31 tiny SVGs, bundled once at load.
const mods = import.meta.glob("../assets/fileicons/*.svg", {
  eager: true,
  query: "?url",
  import: "default",
});
const ICON_URL = {};
for (const p in mods) ICON_URL[p.split("/").pop().replace(".svg", "")] = mods[p];

// extension -> type key (set-independent). Mirrors CodeLeaf FileIcons.byExt.
const BY_EXT = {};
const reg = (key, ...exts) => exts.forEach((e) => (BY_EXT[e] = key));
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
const BY_NAME = {
  dockerfile: "docker",
  ".gitignore": "git",
  ".gitattributes": "git",
  ".gitmodules": "git",
  "build.gradle": "gradle",
  "build.gradle.kts": "gradle",
  "settings.gradle.kts": "gradle",
};

// typeKey resolves a filename to its brand key (or null). Whole-name wins over ext.
export function typeKey(name) {
  const n = (name || "").toLowerCase();
  if (BY_NAME[n]) return BY_NAME[n];
  const ext = n.includes(".") ? n.split(".").pop() : "";
  return BY_EXT[ext] || null;
}

// brandIconURL returns the bundled SVG url for a filename, or null if none.
export function brandIconURL(name) {
  const k = typeKey(name);
  return k ? ICON_URL[k] || null : null;
}

// --- special marks (priority order) — drive emphasis, ported from FileIcons.kt ---
const AI_NAMES = new Set([
  "claude.md", "claude.local.md", "agents.md", "gemini.md",
  "copilot-instructions.md", ".cursorrules", ".windsurfrules",
  ".aider.conf.yml", ".aiderignore", "llms.txt", "llms-full.txt", ".mcp.json",
  ".claude", ".cursor", ".windsurf", ".continue", ".aider", ".codeium",
]);
const SECRET_EXTS = new Set(["pem", "key", "keystore", "jks", "p12", "pfx", "ppk"]);

function isSecret(n) {
  if (n.endsWith(".pub")) return false;
  if (n === ".env" || n.startsWith(".env.")) {
    return !["example", "sample", "template", "dist", "defaults"].includes(n.slice(5));
  }
  if (n === "credentials" || n.startsWith("credentials.")) return true;
  if (/^id_(rsa|dsa|ecdsa|ed25519)/.test(n)) return true;
  return SECRET_EXTS.has(n.includes(".") ? n.split(".").pop() : "");
}

// mark classifies a filename for emphasis. AI / SECRET also override the glyph
// (handled in FileIcon); the rest are styling hints the tree may apply later.
export function mark(name) {
  const n = (name || "").toLowerCase();
  if (AI_NAMES.has(n)) return "ai";
  if (isSecret(n)) return "secret";
  return "none";
}
