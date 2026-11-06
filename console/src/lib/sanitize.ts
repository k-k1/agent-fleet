import DOMPurify from "dompurify";
import { isExternalUrl } from "./filemeta.ts";

// The one sanitizer for Markdown-derived HTML. Agent output, repository files and sessions
// shared by other members all reach the DOM through it, so it must not let them draw
// controls or restyle the page: form controls and inline styles are removed outright (a
// form is a one-click same-origin request; a style attribute can paint a look-alike UI).
// Whatever a renderer needs for its own look is done with classes (see `.md-task-box`).
export const FORBID_TAGS = ["form", "input", "button", "select", "textarea", "style"];
export const FORBID_ATTR = ["style"];

// Console paths a rendered document must never make the browser GET by displaying media:
// they have side effects (logout, OAuth callbacks) or return account data.
const BLOCKED_ROOTS = ["api", "oauth2", "login"];

function blockedPath(pathname: string): boolean {
  let first = pathname.replace(/^\/+/, "").split("/")[0] ?? "";
  // The server decodes the path, so `/%61pi/` reaches `/api/`; the URL parser leaves it alone.
  try {
    first = decodeURIComponent(first);
  } catch {}
  first = first.toLowerCase();
  return BLOCKED_ROOTS.includes(first);
}

/**
 * True when `url` would be fetched from the Console origin's `/api/`, `/oauth2/` or `/login`
 * (also when the Console is served under a path prefix: the <base> directory counts as the
 * root). Relative URLs resolve against the document base.
 */
export function isInternalMediaUrl(url: string): boolean {
  let u: URL;
  try {
    u = new URL(url, document.baseURI);
  } catch {
    return false;
  }
  if (u.origin !== window.location.origin) return false;
  if (blockedPath(u.pathname)) return true;
  const base = new URL(document.baseURI);
  const dir = base.pathname.replace(/[^/]*$/, "");
  return u.pathname.startsWith(dir) && blockedPath(u.pathname.slice(dir.length));
}

function srcsetUrls(value: string): string[] {
  return value
    .split(",")
    .map((c) => c.trim().split(/\s+/)[0] ?? "")
    .filter(Boolean);
}

// Attributes that make an element fetch a resource on display.
const FETCH_ATTRS = new Set(["src", "srcset", "poster", "background", "href", "xlink:href"]);
const FETCH_ATTRS_ANCHOR_SAFE = new Set(["href", "xlink:href"]); // only fetches on <image> (SVG)

export interface SanitizeOptions {
  /**
   * The caller rewrites relative / root-relative <img src> to repo files afterwards
   * (wireImages), so those are repo paths here and only absolute ones are checked. Applies to
   * <img src> alone. Off for a document nothing rewrites (streaming): every URL resolves
   * against the Console.
   */
  repoRelative?: boolean;
}

let purifier: ReturnType<typeof DOMPurify> | null = null;
let current: SanitizeOptions = {};

function instance() {
  if (purifier) return purifier;
  const p = DOMPurify(window);
  p.addHook("uponSanitizeAttribute", (node, data) => {
    const name = data.attrName;
    if (!FETCH_ATTRS.has(name)) return;
    if (FETCH_ATTRS_ANCHOR_SAFE.has(name) && node.nodeName.toLowerCase() !== "image") return;
    const value = data.attrValue;
    const urls = name === "srcset" ? srcsetUrls(value) : [value.trim()];
    const bad = urls.some((u) => {
      // Only <img src> is rewritten to a repo file afterwards (wireImages); every other
      // fetching attribute keeps its URL as written and must be judged as such.
      if (current.repoRelative && name === "src" && node.nodeName.toLowerCase() === "img" && !isExternalUrl(u)) return false;
      return isInternalMediaUrl(u);
    });
    if (bad) data.keepAttr = false;
  });
  purifier = p;
  return p;
}

export function sanitizeMarkdownHtml(html: string, opts: SanitizeOptions = {}): string {
  current = opts;
  try {
    return instance().sanitize(html, { FORBID_TAGS, FORBID_ATTR });
  } finally {
    current = {};
  }
}

const hex = /[0-9a-fA-F]/;

// CSS escape at css[i] === "\\": returns the decoded text and the index after it.
function cssEscape(css: string, i: number): [string, number] {
  const c = css[i + 1];
  if (c === undefined) return ["", i + 1];
  if (hex.test(c)) {
    let j = i + 1;
    while (j < css.length && j < i + 7 && hex.test(css[j]!)) j++;
    const cp = parseInt(css.slice(i + 1, j), 16);
    if (/\s/.test(css[j] ?? "")) j++;
    return [cp > 0 && cp <= 0x10ffff ? String.fromCodePoint(cp) : "\ufffd", j];
  }
  if (c === "\n") return ["", i + 2]; // line continuation inside a string
  return [c, i + 2];
}

// A CSS string token starting at the quote css[i]; value is decoded.
function cssString(css: string, i: number): { value: string; end: number } {
  const q = css[i];
  let v = "";
  let j = i + 1;
  while (j < css.length && css[j] !== q) {
    if (css[j] === "\\") {
      const [d, n] = cssEscape(css, j);
      v += d;
      j = n;
    } else v += css[j++];
  }
  return { value: v, end: Math.min(j + 1, css.length) };
}

const URL_STRING_FUNCS = new Set(["image-set", "-webkit-image-set", "src", "image"]);

/**
 * Replaces every url() / image-set() string / @import string in a CSS text whose decoded
 * URL points at a blocked Console path with `url()`. Tokenizes the way a browser does
 * (escapes, quoted parentheses, comments) so spelling tricks cannot hide a URL; a url() whose
 * token never terminates is dropped too.
 */
export function scrubCssUrls(css: string): string {
  let out = "";
  let i = 0;
  let last = 0; // start of the text not yet copied to `out`
  const stack: string[] = []; // function names of the open parentheses
  let atImport = false;
  const cut = (from: number, to: number) => {
    out += css.slice(last, from) + "url()";
    last = to;
    i = to;
  };
  while (i < css.length) {
    const ch = css[i]!;
    if (ch === "/" && css[i + 1] === "*") {
      const e = css.indexOf("*/", i + 2);
      i = e < 0 ? css.length : e + 2;
      continue;
    }
    if (ch === '"' || ch === "'") {
      const { value, end } = cssString(css, i);
      const top = stack[stack.length - 1] ?? "";
      if ((atImport && stack.length === 0) || URL_STRING_FUNCS.has(top)) {
        if (isInternalMediaUrl(value.trim())) {
          cut(i, end);
          continue;
        }
      }
      i = end;
      continue;
    }
    if (ch === "@") {
      let j = i + 1;
      let name = "";
      while (j < css.length && /[\w-\\]/.test(css[j]!)) {
        if (css[j] === "\\") {
          const [d, n] = cssEscape(css, j);
          name += d;
          j = n;
        } else name += css[j++];
      }
      atImport = name.toLowerCase() === "import";
      i = j;
      continue;
    }
    if (/[\w-\\]/.test(ch) || ch.charCodeAt(0) > 127) {
      const start = i;
      let name = "";
      while (i < css.length && (/[\w-\\]/.test(css[i]!) || css.charCodeAt(i) > 127)) {
        if (css[i] === "\\") {
          const [d, n] = cssEscape(css, i);
          name += d;
          i = n;
        } else name += css[i++];
      }
      if (css[i] !== "(") continue;
      const fn = name.toLowerCase();
      if (fn === "url") {
        let j = i + 1;
        while (j < css.length && /\s/.test(css[j]!)) j++;
        let value: string;
        let end: number;
        if (css[j] === '"' || css[j] === "'") {
          ({ value, end } = cssString(css, j));
          while (end < css.length && /\s/.test(css[end]!)) end++;
          if (css[end] === ")") end++;
        } else {
          value = "";
          end = j;
          while (end < css.length && css[end] !== ")") {
            if (css[end] === "\\") {
              const [d, n] = cssEscape(css, end);
              value += d;
              end = n;
            } else value += css[end++];
          }
          if (end < css.length) end++;
        }
        if (isInternalMediaUrl(value.trim())) cut(start, end);
        else i = end;
        continue;
      }
      stack.push(fn);
      i++;
      continue;
    }
    if (ch === "(") stack.push("");
    else if (ch === ")") stack.pop();
    else if (ch === ";" || ch === "{") atImport = false;
    i++;
  }
  return out + css.slice(last);
}

/**
 * Post-pass for a renderer whose output is not run through sanitizeMarkdownHtml (Marp's
 * own HTML): drops media URLs and inline CSS url()s that resolve to blocked Console paths.
 */
export function stripInternalMedia(root: ParentNode): void {
  root.querySelectorAll<Element>("[src], [srcset], [poster], [style], image, style").forEach((el) => {
    const attrs = ["src", "poster"];
    if (el.localName === "image") attrs.push("href", "xlink:href");
    for (const name of attrs) {
      const v = el.getAttribute(name);
      if (v && isInternalMediaUrl(v.trim())) el.removeAttribute(name);
    }
    const set = el.getAttribute("srcset");
    if (set && srcsetUrls(set).some(isInternalMediaUrl)) el.removeAttribute("srcset");
    const style = el.getAttribute("style");
    if (style && /url|image|\\|@/i.test(style)) el.setAttribute("style", scrubCssUrls(style));
    if (el.localName === "style" && el.textContent) el.textContent = scrubCssUrls(el.textContent);
  });
}

// Mermaid fetches whatever resource a diagram names (image nodes, <img> in labels, CSS in
// init directives) while it renders, before any post-pass sees the SVG. Diagram source that
// can name a resource is therefore not rendered at all; plain diagrams never need these.
const MERMAID_RESOURCE = /<\s*\/?\s*(?:img|image|video|audio|source|svg|link|object|embed|iframe|style)\b|\bimg\s*:|\bicon\s*:|url\s*\(|@import|\bsrc\s*=|\bhref\b|xlink|\\|%%\s*\{|\bclick\b/i;

export function mermaidNamesResource(source: string): boolean {
  return MERMAID_RESOURCE.test(source);
}
