// File metadata helpers for the viewer (CodeLeaf-inspired info display): map a
// filename to a highlight.js language id and a human label, and format sizes.

// extension -> highlight.js language id. Unmapped extensions fall back to no
// highlighting (plain text) to avoid slow / wrong auto-detection on big files.
const EXT_LANG = {
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
const NAME_LANG = {
  dockerfile: "dockerfile",
  makefile: "makefile",
  ".gitignore": "bash",
  ".env": "bash",
};

export function baseName(path) {
  return path.split("/").pop() || path;
}

export function dirName(path) {
  const i = path.lastIndexOf("/");
  return i >= 0 ? path.slice(0, i) : "";
}

// joinPath joins a base directory and a relative path, resolving "." and ".."
// segments. Used to turn a Markdown link's relative href into a home-relative
// path the file browser can open. (Traversal above home is still rejected by the
// Agent's safeBrowsePath, so this needn't be airtight.)
export function joinPath(baseDir, rel) {
  const segs = (baseDir ? baseDir.split("/") : []).concat(rel.split("/"));
  const out = [];
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
export function isExternalUrl(href) {
  return /^[a-z][a-z0-9+.-]*:/i.test(href) || href.startsWith("//");
}

// GitHub-ish heading slug, so in-page #anchors resolve. Keeps letters/numbers
// (incl. CJK) and dashes, turns whitespace into "-".
export function slug(text) {
  return text
    .toLowerCase()
    .trim()
    .replace(/[^\p{L}\p{N} -]/gu, "")
    .replace(/\s+/g, "-");
}

export function langFor(path) {
  const name = baseName(path).toLowerCase();
  if (NAME_LANG[name]) return NAME_LANG[name];
  const ext = name.includes(".") ? name.split(".").pop() : name;
  return EXT_LANG[ext] || "";
}

// A short human label for the info bar (uppercased language, or extension).
export function langLabel(path) {
  const lang = langFor(path);
  if (lang) return lang;
  const name = baseName(path);
  const ext = name.includes(".") ? name.split(".").pop() : "";
  return ext ? ext.toLowerCase() : "text";
}

export function humanSize(bytes) {
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

export function countLines(text) {
  if (!text) return 0;
  let n = 1;
  for (let i = 0; i < text.length; i++) if (text.charCodeAt(i) === 10) n++;
  // a trailing newline shouldn't count as an extra empty line
  if (text.charCodeAt(text.length - 1) === 10) n--;
  return n;
}
