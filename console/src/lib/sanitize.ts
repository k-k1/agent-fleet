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
   * The caller rewrites relative / root-relative media URLs to repo files afterwards
   * (wireImages), so those are repo paths here and only absolute URLs are checked. Off for
   * a document nothing rewrites (streaming): every URL then resolves against the Console.
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
      if (current.repoRelative && !isExternalUrl(u)) return false;
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

const CSS_URL = /url\(\s*(['"]?)([^)'"]*)\1\s*\)/gi;

/** Replaces each `url(...)` in a CSS text that points at a blocked Console path with `url()`. */
export function scrubCssUrls(css: string): string {
  return css.replace(CSS_URL, (m, _q, u: string) => (isInternalMediaUrl(u.trim()) ? "url()" : m));
}

/**
 * Post-pass for a renderer whose output is not run through sanitizeMarkdownHtml (Marp's
 * own HTML): drops media URLs and inline CSS url()s that resolve to blocked Console paths.
 */
export function stripInternalMedia(root: ParentNode): void {
  root.querySelectorAll<HTMLElement>("[src], [srcset], [poster], [style]").forEach((el) => {
    for (const name of ["src", "poster"]) {
      const v = el.getAttribute(name);
      if (v && isInternalMediaUrl(v.trim())) el.removeAttribute(name);
    }
    const set = el.getAttribute("srcset");
    if (set && srcsetUrls(set).some(isInternalMediaUrl)) el.removeAttribute("srcset");
    const style = el.getAttribute("style");
    if (style && /url\(/i.test(style)) el.setAttribute("style", scrubCssUrls(style));
  });
}
