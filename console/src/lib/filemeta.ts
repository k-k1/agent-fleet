// File metadata helpers for the viewer (CodeLeaf-inspired info display): map a
// filename to a highlight.js language id and a human label, and format sizes.

// extension -> highlight.js language id. Unmapped extensions fall back to no
// highlighting (plain text) to avoid slow / wrong auto-detection on big files.
const EXT_LANG: Record<string, string> = {
  js: "javascript", mjs: "javascript", cjs: "javascript", jsx: "javascript",
  ts: "typescript", tsx: "typescript",
  json: "json", jsonc: "json",
  py: "python", rb: "ruby", go: "go", rs: "rust",
  java: "java", kt: "kotlin", kts: "kotlin", scala: "scala", swift: "swift",
  c: "c", h: "c", cpp: "cpp", cc: "cpp", cxx: "cpp", hpp: "cpp", cs: "csharp",
  php: "php", lua: "lua", pl: "perl", r: "r", dart: "dart",
  sh: "bash", bash: "bash", zsh: "bash", fish: "bash",
  yml: "yaml", yaml: "yaml", toml: "ini", ini: "ini", cfg: "ini", conf: "ini",
  xml: "xml", html: "xml", htm: "xml", svg: "xml", vue: "xml",
  css: "css", scss: "scss", sass: "scss", less: "less",
  md: "markdown", markdown: "markdown",
  sql: "sql", graphql: "graphql", gql: "graphql",
  dockerfile: "dockerfile",
  makefile: "makefile", mk: "makefile",
  diff: "diff", patch: "diff",
};

// Some files are identified by name, not extension.
const NAME_LANG: Record<string, string> = {
  dockerfile: "dockerfile",
  makefile: "makefile",
  ".gitignore": "bash",
  ".env": "bash",
};

export function baseName(path: string): string {
  return path.split("/").pop() || path;
}

export function dirName(path: string): string {
  const i = path.lastIndexOf("/");
  return i >= 0 ? path.slice(0, i) : "";
}

// joinPath joins a base directory and a relative path, resolving "." and ".."
// segments. Used to turn a Markdown link's relative href into a home-relative
// path the file browser can open. (Traversal above home is still rejected by the
// Agent's safeBrowsePath, so this needn't be airtight.)
export function joinPath(baseDir: string, rel: string): string {
  const segs = (baseDir ? baseDir.split("/") : []).concat(rel.split("/"));
  const out: string[] = [];
  for (const s of segs) {
    if (s === "" || s === ".") continue;
    if (s === "..") {
      out.pop();
      continue;
    }
    out.push(s);
  }
  return out.join("/");
}

// A URL is "external" when it carries a scheme (http:, mailto:, …) or is protocol-
// relative (//host). Everything else is treated as a path inside the workspace.
export function isExternalUrl(href: string): boolean {
  return /^[a-z][a-z0-9+.-]*:/i.test(href) || href.startsWith("//");
}

// GitHub-ish heading slug, so in-page #anchors resolve. Keeps letters/numbers
// (incl. CJK) and dashes, turns whitespace into "-".
export function slug(text: string): string {
  return text
    .toLowerCase()
    .trim()
    .replace(/[^\p{L}\p{N} -]/gu, "")
    .replace(/\s+/g, "-");
}

// isMarpDoc reports whether a Markdown source opts into Marp slides via YAML
// frontmatter (`marp: true`) at the very top of the file. We only sniff the
// leading `---` … `---` block, not the whole document, and don't fully parse YAML.
export function isMarpDoc(source: string | null | undefined): boolean {
  if (!source) return false;
  const m = source.match(/^---\r?\n([\s\S]*?)\r?\n---\s*(\r?\n|$)/);
  if (!m) return false;
  return /^\s*marp\s*:\s*true\s*$/m.test(m[1]);
}

// Image extensions the browser can render in an <img>. Maps to a lowercase
// display format (mirrors CodeLeaf's FileKind.Image(format)). svg is text on the
// wire but still previewed as an image (with a source toggle in the viewer).
const IMAGE_EXT: Record<string, string> = {
  png: "png", apng: "png",
  jpg: "jpeg", jpeg: "jpeg", jfif: "jpeg",
  gif: "gif",
  webp: "webp",
  avif: "avif",
  bmp: "bmp",
  ico: "ico",
  svg: "svg",
};

// imageFormat returns the display format (png/jpeg/svg…) for an image path, or
// "" when the path isn't a previewable image. Detection is by extension only —
// the viewer sources the bytes from the download endpoint, which the browser
// sniffs and decodes regardless of the Content-Type header.
export function imageFormat(path: string): string {
  const name = baseName(path).toLowerCase();
  const ext = name.includes(".") ? name.split(".").pop() ?? "" : "";
  return IMAGE_EXT[ext] || "";
}

export function langFor(path: string): string {
  const name = baseName(path).toLowerCase();
  if (NAME_LANG[name]) return NAME_LANG[name];
  const ext = name.includes(".") ? name.split(".").pop() ?? name : name;
  return EXT_LANG[ext] || "";
}

// A short human label for the info bar (uppercased language, or extension).
export function langLabel(path: string): string {
  const lang = langFor(path);
  if (lang) return lang;
  const name = baseName(path);
  const ext = name.includes(".") ? name.split(".").pop() ?? "" : "";
  return ext ? ext.toLowerCase() : "text";
}

export function humanSize(bytes: number | null | undefined): string {
  if (bytes == null) return "";
  if (bytes < 1024) return `${bytes} B`;
  const u = ["KB", "MB", "GB"];
  let n = bytes / 1024;
  let i = 0;
  while (n >= 1024 && i < u.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n.toFixed(n < 10 ? 1 : 0)} ${u[i]}`;
}

// Extensions that are almost certainly non-text, used to hide actions that only make
// sense on readable files (e.g. handing a file to an assistant — docs/19 Phase C). This
// is a denylist: unknown extensions (LICENSE, Dockerfile, .env, source files…) are
// treated as text-eligible on purpose, so the check is generous rather than strict.
const BINARY_EXT = new Set([
  "pdf", "zip", "gz", "tgz", "bz2", "xz", "7z", "rar", "tar", "jar", "war",
  "exe", "dll", "so", "dylib", "bin", "o", "a", "class", "wasm", "node",
  "png", "jpg", "jpeg", "gif", "bmp", "ico", "webp", "tif", "tiff", "svg", "avif", "heic",
  "mp3", "wav", "flac", "ogg", "m4a", "aac", "mp4", "mov", "avi", "mkv", "webm",
  "woff", "woff2", "ttf", "otf", "eot",
  "sqlite", "db", "dat", "pyc", "pdb", "lock",
]);

// isProbablyBinary reports whether a filename looks like a non-text asset (by extension
// only). Errs toward false (text) for unknown extensions.
export function isProbablyBinary(path: string): boolean {
  const name = path.toLowerCase();
  const base = name.includes("/") ? name.slice(name.lastIndexOf("/") + 1) : name;
  const ext = base.includes(".") ? base.split(".").pop() ?? "" : "";
  return !!ext && BINARY_EXT.has(ext);
}

export function countLines(text: string | null | undefined): number {
  if (!text) return 0;
  let n = 1;
  for (let i = 0; i < text.length; i++) if (text.charCodeAt(i) === 10) n++;
  // a trailing newline shouldn't count as an extra empty line
  if (text.charCodeAt(text.length - 1) === 10) n--;
  return n;
}
