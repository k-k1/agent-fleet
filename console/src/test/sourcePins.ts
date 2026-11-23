// Helpers for the source pins that keep a Console area on its typed API wrapper. A pin that
// matches the raw text is fooled both ways: a namespace import passes, and a path or import
// that only survives in a comment fails or passes by accident. These helpers look at code only
// (comments blanked, string contents kept) and read every import form syntactically.

/** Source with its comments replaced by spaces; string and template contents are untouched. */
export function codeOnly(src: string): string {
  let out = "";
  let i = 0;
  const n = src.length;
  while (i < n) {
    const c = src[i];
    const d = src[i + 1];
    if (c === "/" && d === "/") {
      while (i < n && src[i] !== "\n") i++;
    } else if (c === "/" && d === "*") {
      i += 2;
      while (i < n && !(src[i] === "*" && src[i + 1] === "/")) {
        out += src[i] === "\n" ? "\n" : " ";
        i++;
      }
      i += 2;
      out += "  ";
    } else if (c === '"' || c === "'" || c === "`") {
      // A template's `${…}` is kept verbatim: a quote inside it only ends the scan early, which
      // at worst leaves a comment-looking run in the output, never hides code.
      out += c;
      i++;
      while (i < n && src[i] !== c) {
        if (src[i] === "\\") {
          out += src[i++];
        }
        if (i < n) out += src[i++];
      }
      if (i < n) out += src[i++];
    } else {
      out += c;
      i++;
    }
  }
  return out;
}

const RAW_NAMES = /\b(api|apiJSON)\b/;
const CLIENT = String.raw`["'][^"']*core\/api\/client(?:\.ts)?["']`;

/**
 * True when the source reaches the raw client's `api` / `apiJSON` by a static import or re-export
 * (named, aliased or namespace) or a dynamic `import()` / `require()`. Type-only imports do not count.
 */
export function importsRawClient(src: string): boolean {
  const code = codeOnly(src);
  const statics = new RegExp(String.raw`\b(?:import|export)\b(?!\s*\()([^;"'\`]*?)\bfrom\s*${CLIENT}`, "g");
  for (const m of code.matchAll(statics)) {
    const clause = m[1].trim();
    if (/^(?:import\s+)?type\b/.test(clause)) continue;
    if (/\*\s*as\b/.test(clause) || /^\*/.test(clause)) return true;
    const braces = clause.match(/\{([^}]*)\}/);
    if (braces && braces[1].split(",").some((s) => !/^\s*type\b/.test(s) && RAW_NAMES.test(s.split(/\bas\b/)[0]))) return true;
  }
  return new RegExp(String.raw`\b(?:import|require)\s*\(\s*${CLIENT}`).test(code);
}

/** True when `literal` appears in the code of `src`, not only in a comment. */
export function hasCodeLiteral(src: string, literal: string): boolean {
  return codeOnly(src).includes(literal);
}
