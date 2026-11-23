// Helpers for the source pins that keep a Console area on its typed API wrapper. A pin that
// matches the raw file text is fooled both ways: a namespace import passes, and a path or import
// that only a comment or an example string contains passes or fails by accident. These helpers
// lex the source (comments dropped, strings and templates kept whole, `${…}` read as code) and
// read import/export declarations from the tokens, never across declarations.
//
// Regex literals are not lexed: a quote inside one would derail the scan. Settings sources do
// not use one next to an import or a path literal.

interface Token {
  k: "id" | "p" | "s" | "t";
  v: string;
}

interface Lexed {
  /** The source with comments blanked; string and template text is untouched, `${…}` included. */
  code: string;
  tokens: Token[];
}

function lex(src: string): Lexed {
  const n = src.length;
  let i = 0;
  let code = "";
  const tokens: Token[] = [];

  const readString = (q: string) => {
    const start = ++i;
    code += q;
    while (i < n && src[i] !== q) {
      if (src[i] === "\\") code += src[i++];
      if (i < n) code += src[i++];
    }
    tokens.push({ k: "s", v: src.slice(start, i) });
    if (i < n) code += src[i++];
  };

  const readTemplate = () => {
    code += src[i++];
    while (i < n && src[i] !== "`") {
      if (src[i] === "\\") code += src[i++];
      else if (src[i] === "$" && src[i + 1] === "{") {
        code += "${";
        i += 2;
        lexCode(true);
        code += "}";
        i++;
        continue;
      }
      if (i < n) code += src[i++];
    }
    tokens.push({ k: "t", v: "" });
    if (i < n) code += src[i++];
  };

  // Lexes code up to the end of the file, or (inTemplate) up to the `}` that closes a `${`.
  const lexCode = (inTemplate: boolean) => {
    let depth = 0;
    while (i < n) {
      const c = src[i];
      if (c === "/" && src[i + 1] === "/") {
        while (i < n && src[i] !== "\n") i++;
      } else if (c === "/" && src[i + 1] === "*") {
        i += 2;
        code += "  ";
        while (i < n && !(src[i] === "*" && src[i + 1] === "/")) {
          code += src[i] === "\n" ? "\n" : " ";
          i++;
        }
        i += 2;
        code += "  ";
      } else if (c === '"' || c === "'") {
        readString(c);
      } else if (c === "`") {
        readTemplate();
      } else if (/[A-Za-z_$]/.test(c)) {
        const s = i;
        while (i < n && /[\w$]/.test(src[i])) code += src[i++];
        tokens.push({ k: "id", v: src.slice(s, i) });
      } else if (c === "}" && inTemplate && depth === 0) {
        return;
      } else {
        if (c === "{") depth++;
        if (c === "}") depth--;
        code += c;
        i++;
        if (!/\s/.test(c)) tokens.push({ k: "p", v: c });
      }
    }
  };

  lexCode(false);
  return { code, tokens };
}

/** Source with its comments replaced by spaces; string and template contents are untouched. */
export function codeOnly(src: string): string {
  return lex(src).code;
}

const CLIENT = /core\/api\/client(?:\.ts)?$/;
const RAW_NAMES = new Set(["api", "apiJSON"]);

/**
 * True when the source reaches the raw client's `api` / `apiJSON` by a static import or re-export
 * (named, aliased or namespace) or a dynamic `import()` / `require()`. Type-only imports do not
 * count; module specifiers inside strings, templates and comments are not code.
 */
export function importsRawClient(src: string): boolean {
  const t = lex(src).tokens;
  const isP = (x: Token | undefined, v: string) => x?.k === "p" && x.v === v;
  const isId = (x: Token | undefined, v: string) => x?.k === "id" && x.v === v;

  for (let i = 0; i < t.length; i++) {
    const head = t[i];
    if (head.k !== "id") continue;

    if (head.v === "require" || head.v === "import") {
      if (isP(t[i + 1], "(") && t[i + 2]?.k === "s" && CLIENT.test(t[i + 2].v)) return true;
    }
    if (head.v !== "import" && head.v !== "export") continue;

    // Read the clause up to `from "<spec>"`. Anything that cannot be part of one (`=`, `;`, a
    // call, the next import/export keyword) means this is not a module-specifier declaration.
    let j = i + 1;
    const clause: Token[] = [];
    for (; j < t.length && !isId(t[j], "from"); j++) {
      const x = t[j];
      const ok = x.k === "id" ? x.v !== "import" && x.v !== "export" : x.k === "p" && "{},*".includes(x.v);
      if (!ok) break;
      clause.push(x);
    }
    if (!isId(t[j], "from") || t[j + 1]?.k !== "s" || !CLIENT.test(t[j + 1].v)) continue;

    if (isId(clause[0], "type") && (isP(clause[1], "{") || isP(clause[1], "*"))) continue;
    if (clause.some((x) => isP(x, "*"))) return true;
    const open = clause.findIndex((x) => isP(x, "{"));
    if (open < 0) continue;
    const entries: Token[][] = [[]];
    for (const x of clause.slice(open + 1)) {
      if (isP(x, "}")) break;
      if (isP(x, ",")) entries.push([]);
      else entries[entries.length - 1].push(x);
    }
    for (const e of entries) {
      if (isId(e[0], "type") && e.length >= 2 && !isId(e[1], "as")) continue;
      if (e[0] && RAW_NAMES.has(e[0].v)) return true;
    }
  }
  return false;
}

/** True when `literal` appears in the code of `src`, not only in a comment. */
export function hasCodeLiteral(src: string, literal: string): boolean {
  return lex(src).code.includes(literal);
}
