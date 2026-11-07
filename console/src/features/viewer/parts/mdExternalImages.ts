// Click-to-load for images the sanitizer deferred (deferExternalMedia in lib/sanitize.ts).
// Displaying a document must not make the browser contact the document author's host: that
// reveals the reader's IP address and, without a referrer policy, the page they are on.
import { EXT_ATTR_PREFIX } from "../../../lib/sanitize.ts";
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
// its src is set. The referrer policy goes on before the URL for the same reason.
function load(node: Element) {
  const els = deferredIn(node);
  els.sort((a, b) => Number(a.localName === "img") - Number(b.localName === "img"));
  for (const el of els) {
    if (el.localName === "img") el.setAttribute("referrerpolicy", "no-referrer");
    for (const name of ["srcset", "src"]) {
      const v = el.getAttribute(EXT_ATTR_PREFIX + name);
      if (v == null) continue;
      el.removeAttribute(EXT_ATTR_PREFIX + name);
      el.setAttribute(name, v);
    }
  }
}

/** Replaces each deferred <img> (or its enclosing <picture>) under `root` with a click-to-load button. */
export function wireExternalImages(root: ParentNode): void {
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
      load(target);
      btn.replaceWith(target);
    });
    target.replaceWith(btn);
  });
}
