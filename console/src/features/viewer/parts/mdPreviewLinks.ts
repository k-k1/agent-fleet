// Loopback URLs in rendered Markdown (`http://127.0.0.1:8080/x.html`, `http://localhost:…`,
// `http://0.0.0.0:…`) point at a server of the WORKSPACE, so a plain browser link goes to the
// reader's own machine instead. They are wired to the workspace preview of that port, the way the
// session row's `:PORT` chip opens it.
//
// Only the link's href, title, class and handlers change, never the visible text: transcript
// marks count text occurrences (features/mirror/transcript/markPaint.ts).
//
// Only `http:`: the preview carries HTTP only (workspace/agent/preview_errors.go), so an
// `https:` loopback URL stays an ordinary link.
import { previewURL } from "../../../core/api/client.ts";
import { t } from "../../../lib/i18n/index.ts";
import { URL_STOP } from "../../../lib/markdown.ts";
import { targetFromURL } from "../../browser/target.ts";
import { activate, ensurePreviewInfo as ensureInfo } from "../../preview/previewLinkRuntime.ts";
import type { OpenPortLinkMenu } from "../../preview/portLinkContext.ts";
import { rowPorts } from "../../sessions/sessionLinks.ts";
import type { Session } from "../../../types/session.ts";
import { wireContextMenu } from "./linkContextMenu.ts";

export interface PreviewLinkWiring {
  onError: (message: string) => void;
  /** Absent → the browser's own context menu (no PortLinkMenuHost above). */
  openMenu?: OpenPortLinkMenu;
}

const LOOPBACK_HOSTS = new Set(["127.0.0.1", "localhost", "0.0.0.0"]);

/** The port and path a loopback `http:` URL names, or null for anything else (another host,
 *  `https:`, the Agent's own port, an unsafe path). */
export function loopbackTarget(raw: string): { port: number; path: string } | null {
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    return null;
  }
  if (u.protocol !== "http:" || !LOOPBACK_HOSTS.has(u.hostname)) return null;
  // 0.0.0.0 is "any interface" on the server's side; the way to reach it is loopback.
  if (u.hostname === "0.0.0.0") u.hostname = "127.0.0.1";
  return targetFromURL(u.href);
}

/** Whether the rendered document holds a link this module wired. */
export function hasPreviewLinks(el: HTMLElement): boolean {
  return el.querySelector("a[data-pv-port]") !== null;
}

/** Turn `a` into a preview link. Idempotent: an anchor already wired is left alone. */
export function wirePreviewLink(a: HTMLAnchorElement, target: { port: number; path: string }, w: PreviewLinkWiring) {
  if (a.dataset.pvPort) return;
  const { port, path } = target;
  const original = a.getAttribute("href") || "";
  a.dataset.pvPort = String(port);
  a.classList.add("preview-link");
  a.setAttribute("href", previewURL(port, path));
  a.removeAttribute("target");
  a.removeAttribute("rel");
  a.title = t("view.preview_link.title", { port, url: original });
  a.dataset.pvTitle = a.title;
  const menu = w.openMenu
    ? wireContextMenu(a, (x, y) => {
        // A link of a document that has since been replaced must not open a menu.
        if (a.isConnected) w.openMenu?.(port, path, x, y);
      })
    : null;
  a.addEventListener("click", (e) => {
    e.preventDefault();
    if (menu?.clickSwallowed()) return; // the lift of the long press that opened the menu
    const m = e as MouseEvent;
    // Synchronous on purpose: window.open must run inside the click (popup blockers).
    activate(port, path, m.ctrlKey || m.metaKey, w.onError);
  });
  a.addEventListener("auxclick", (e) => {
    if ((e as MouseEvent).button !== 1) return;
    e.preventDefault();
    activate(port, path, true, w.onError);
  });
}

/** Link inline code that is exactly a loopback URL (`http://localhost:5173/`, how agents usually
 *  write one). The `<code>` is wrapped, not rewritten, so its text is untouched. Fenced blocks
 *  and code already inside a link are skipped. */
export function linkifyPreviewCode(el: HTMLElement, w: PreviewLinkWiring) {
  el.querySelectorAll<HTMLElement>("code").forEach((code) => {
    if (code.closest("pre,a") || code.childElementCount) return;
    const text = (code.textContent || "").trim();
    // Bare autolinks end at a non-ASCII punctuation/symbol (URL_STOP); a code span has no end to
    // cut at, so one holding such a character (`http://127.0.0.1:8080/…`) is an elided URL, not a
    // real one, and linking it would open a 404.
    if (URL_STOP.test(text)) return;
    const target = loopbackTarget(text);
    if (!target) return;
    const a = document.createElement("a");
    a.setAttribute("href", text);
    code.replaceWith(a);
    a.append(code);
    wirePreviewLink(a, target, w);
  });
}

/** The ports some live session listens on, as a string a store selector can compare cheaply.
 *  "" while the session list has not loaded: nothing is dimmed on a guess. */
export function livePortsKey(sessions: Session[], loaded: boolean): string {
  if (!loaded) return "";
  const ports = new Set<number>();
  for (const s of sessions) if (s.alive) for (const p of rowPorts(s.ports)) ports.add(p);
  return "p:" + [...ports].sort((a, b) => a - b).join(",");
}

/** Dim the preview links whose port nobody listens on, without touching the rest of the DOM.
 *  The link still opens: the server may have been started outside a session's process tree. */
export function markPortLiveness(el: HTMLElement, key: string) {
  const live = key ? new Set(key.slice(2).split(",").filter(Boolean).map(Number)) : null;
  el.querySelectorAll<HTMLAnchorElement>("a[data-pv-port]").forEach((a) => {
    const idle = !!live && !live.has(Number(a.dataset.pvPort));
    a.classList.toggle("pv-idle", idle);
    const base = a.dataset.pvTitle || "";
    a.title = idle ? `${base}\n${t("view.preview_link.idle")}` : base;
  });
}

/** First preview link in a document and nothing held for this scope → read the preview state so
 *  the first click can already use the subdomain. */
export const ensurePreviewInfo = ensureInfo;
