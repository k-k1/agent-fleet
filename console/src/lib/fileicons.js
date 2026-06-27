// File-type icons for the tree / viewer / repo & session rows. Emoji-based (no asset
// bundling, renders everywhere), mapped from extension or special filename. Inspired
// by CodeLeaf's FileIcons typeKey/mark split, but pictographic categories instead of
// brand SVGs (which would need an asset pipeline). A handful of languages with a
// widely-recognized emoji get a distinct glyph; the rest fall back to a category icon.

// extension -> icon. Source files share a generic "page" glyph (they're syntax-
// highlighted on open); a few languages get their well-known mascot emoji.
const EXT_ICON = {
  // languages with a widely-recognized emoji
  py: "🐍", pyw: "🐍", pyi: "🐍",
  rs: "🦀",
  rb: "💎", gemspec: "💎",
  // scripts / shells
  sh: "🐚", bash: "🐚", zsh: "🐚", fish: "🐚",
  // docs / markup
  md: "📝", markdown: "📝", mdx: "📝", rst: "📝",
  txt: "📄", rtf: "📄", pdf: "📕",
  // web
  html: "🌐", htm: "🌐", xhtml: "🌐",
  css: "🎨", scss: "🎨", sass: "🎨", less: "🎨",
  // data / config
  json: "🧾", jsonc: "🧾",
  yml: "⚙️", yaml: "⚙️", toml: "⚙️", ini: "⚙️", cfg: "⚙️", conf: "⚙️", env: "⚙️",
  xml: "📐", plist: "📐",
  // images
  svg: "🖼️", png: "🖼️", jpg: "🖼️", jpeg: "🖼️", gif: "🖼️",
  webp: "🖼️", ico: "🖼️", bmp: "🖼️", avif: "🖼️",
  // media
  mp3: "🎵", wav: "🎵", flac: "🎵", ogg: "🎵",
  mp4: "🎬", mov: "🎬", webm: "🎬", mkv: "🎬",
  // archives / packages
  zip: "🗜️", tar: "🗜️", gz: "🗜️", tgz: "🗜️", bz2: "🗜️", xz: "🗜️", "7z": "🗜️", rar: "🗜️",
  jar: "📦", war: "📦",
  // database
  db: "🗄️", sqlite: "🗄️", sqlite3: "🗄️", sql: "🗄️",
  // fonts
  ttf: "🔤", otf: "🔤", woff: "🔤", woff2: "🔤",
  // source code (generic page glyph; highlighted on open)
  js: "📜", mjs: "📜", cjs: "📜", jsx: "📜",
  ts: "📜", tsx: "📜", mts: "📜", cts: "📜",
  go: "📜", java: "📜", kt: "📜", kts: "📜", scala: "📜",
  c: "📜", h: "📜", cpp: "📜", cc: "📜", cxx: "📜", hpp: "📜", hh: "📜",
  cs: "📜", php: "📜", swift: "📜", dart: "📜", lua: "📜", pl: "📜", r: "📜",
  vue: "📜", svelte: "📜", graphql: "📜", gql: "📜",
};

// whole-name files (no useful extension).
const NAME_ICON = {
  dockerfile: "🐳",
  ".dockerignore": "🐳",
  makefile: "🛠️",
  ".gitignore": "🔀", ".gitattributes": "🔀", ".gitmodules": "🔀",
  license: "⚖️", "license.md": "⚖️", licence: "⚖️", copying: "⚖️",
  readme: "📘", "readme.md": "📘",
};

// AI assistant instruction / config files — surfaced with a distinct glyph.
const AI_NAMES = new Set([
  "claude.md", "claude.local.md", "agents.md", "gemini.md",
  "copilot-instructions.md", ".cursorrules", ".windsurfrules", ".mcp.json",
]);

const SECRET_EXTS = new Set(["pem", "key", "keystore", "jks", "p12", "pfx", "ppk"]);

// isSecret mirrors CodeLeaf: keys / real .env files, but not .env.example or *.pub.
function isSecret(n) {
  if (n.endsWith(".pub")) return false;
  if (n === ".env" || n.startsWith(".env.")) {
    return !["example", "sample", "template", "dist", "defaults"].includes(n.slice(5));
  }
  if (/^id_(rsa|dsa|ecdsa|ed25519)/.test(n)) return true;
  return SECRET_EXTS.has(n.includes(".") ? n.split(".").pop() : "");
}

// fileIcon returns an emoji for a filename (priority: AI > secret > whole-name > ext).
export function fileIcon(name) {
  const n = (name || "").toLowerCase();
  if (AI_NAMES.has(n)) return "✨";
  if (isSecret(n)) return "🔑";
  if (NAME_ICON[n]) return NAME_ICON[n];
  const ext = n.includes(".") ? n.split(".").pop() : n;
  return EXT_ICON[ext] || "📄";
}

// dirIcon: open vs closed folder.
export function dirIcon(open) {
  return open ? "📂" : "📁";
}
