// Click-to-load for images the sanitizer deferred (deferExternalMedia in lib/sanitize.ts).
// Displaying a document must not make the browser contact the document author's host: that
// reveals the reader's IP address and, without a referrer policy, the page they are on.
import { EXT_ATTR_PREFIX, isExternalMediaUrl, isInternalMediaUrl, srcsetUrls } from "../../../lib/sanitize.ts";
import { t } from "../../../lib/i18n/index.ts";

const DEFERRED = `[${EXT_ATTR_PREFIX}src], [${EXT_ATTR_PREFIX}srcset]`;

// The node itself counts: a lone <img> is its own deferred element.
function deferredIn(node: Element): Element[] {
  return [node, ...node.querySelectorAll(DEFERRED)].filter((e) => e.matches(DEFERRED));
}

function hostsOf(node: Element): string {
  const hosts = new Set<string>();
  deferredIn(node).forEach((el) => {
    const urls = [el.getAttribute(EXT_ATTR_PREFIX + "src") ?? ""];
    const set = el.getAttribute(EXT_ATTR_PREFIX + "srcset");
    if (set) urls.push(...set.split(",").map((c) => c.trim().split(/\s+/)[0] ?? ""));
    for (const u of urls) {
      try {
        if (u) hosts.add(new URL(u, document.baseURI).host);
      } catch {}
    }
  });
  return [...hosts].join(", ");
}

// Gives the URLs back. <source>s first: an <img> starts selecting its candidate the moment
// its src is set. Every <img> in the node gets the referrer policy before any URL is set,
// including a non-external fallback <img> of a <picture> whose <source> was the external one.
//
// What is restored is only what the reader asked for: external URLs as written, and the
// node's non-external <img src> through `resolveSrc` (the repo-path rule of a normal image).
// A srcset naming a Console path is dropped whole (see restoreSrcset).
function load(node: Element, resolveSrc: SrcResolver) {
  node.querySelectorAll("img").forEach((i) => i.setAttribute("referrerpolicy", "no-referrer"));
  if (node.localName === "img") node.setAttribute("referrerpolicy", "no-referrer");
  // Parts of the node that were never deferred (a fallback <img src="docs/a.png">) were
  // detached from the document with it, so wireImages never saw them either.
  for (const el of [node, ...node.querySelectorAll("img, source")]) {
    const src = el.getAttribute("src");
    if (src != null && !isExternalMediaUrl(src)) {
      const next = resolveSrc(src);
      if (next) el.setAttribute("src", next);
      else el.removeAttribute("src");
    }
    const set = el.getAttribute("srcset");
    if (set != null) el.setAttribute("srcset", restoreSrcset(set));
  }
  const els = deferredIn(node);
  els.sort((a, b) => Number(a.localName === "img") - Number(b.localName === "img"));
  for (const el of els) {
    for (const name of ["srcset", "src"]) {
      const v = el.getAttribute(EXT_ATTR_PREFIX + name);
      if (v == null) continue;
      el.removeAttribute(EXT_ATTR_PREFIX + name);
      const next = name === "src" ? restoreSrc(v, resolveSrc) : restoreSrcset(v);
      if (next) el.setAttribute(name, next);
    }
  }
}

function restoreSrc(v: string, resolveSrc: SrcResolver): string | null {
  if (isExternalMediaUrl(v)) return v;
  return resolveSrc(v);
}

// Kept as written unless a candidate names the Console's own paths, then the whole attribute
// goes (re-joining candidates would break the commas inside data: URLs). The sanitizer already
// removed those, so this is a second line; same-origin and data: candidates are safe and must
// stay, or the browser would pick a different candidate than the author meant.
function restoreSrcset(v: string): string {
  return srcsetUrls(v).some(isInternalMediaUrl) ? "" : v;
}

/** Maps a non-external src to the URL to load (null: drop it). */
export type SrcResolver = (src: string) => string | null;

// Surfaces without repo files (streaming, Marp): keep what is not the Console's own API.
const keepUnlessInternal: SrcResolver = (src) => (isInternalMediaUrl(src) ? null : src);

/** Replaces each deferred <img> (or its enclosing <picture>) under `root` with a click-to-load button. */
export function wireExternalImages(root: ParentNode, resolveSrc: SrcResolver = keepUnlessInternal): void {
  const seen = new Set<Element>();
  root.querySelectorAll(DEFERRED).forEach((el) => {
    const parent = el.parentElement;
    const target = parent?.localName === "picture" ? parent : el.localName === "img" ? el : null;
    if (!target || seen.has(target) || !target.parentNode) return;
    seen.add(target);
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "md-ext-img";
    const alt = target.localName === "img" ? target.getAttribute("alt") : target.querySelector("img")?.getAttribute("alt");
    btn.textContent = t("view.ext_image_load", { host: hostsOf(target) });
    if (alt) btn.title = alt;
    btn.addEventListener("click", (e) => {
      e.preventDefault();
      load(target, resolveSrc);
      btn.replaceWith(target);
    });
    target.replaceWith(btn);
  });
}
