// Wiring for the rendered <a> / <img>: external URLs, in-page anchors, repo-relative links.
import { dirName, baseName, isExternalUrl, resolveMarkdownFileTarget } from "../../../lib/filemeta.ts";
import { api, downloadURL } from "../../../core/api/client.ts";
import { t } from "../../../lib/i18n/index.ts";
import { browserAttachmentIdFromLink } from "../../../layout/browserAttachmentAction.ts";
import { openBrowserAttachment } from "../../browser/attachmentAction.ts";
import type { OpenPathLinkMenu } from "../PathLinkMenu.tsx";
import { wireContextMenu } from "./linkContextMenu.ts";
import { loopbackTarget, wirePreviewLink, type PreviewLinkWiring } from "./mdPreviewLinks.ts";

// resolveRelPath turns a repo-relative Markdown href/src into a home-relative fs path.
// marked percent-encodes non-ASCII (Japanese becomes %E6…), so decode first or the path won't
// resolve; a literal-% name that isn't valid encoding falls back to the raw string.
// A leading "/" resolves from the repo root, everything else from the file's own dir.
function resolveRelPath(ref: string, basePath: string): string {
  return resolveMarkdownFileTarget(ref, basePath)?.path || "";
}

// wireImages rewrites relative <img src> to the file-download endpoint so repo-local
// images (including Japanese filenames) actually load; browser-relative srcs would
// otherwise 404 against the console origin. Scheme / protocol-relative / data: URLs
// are left as-is.
export function wireImages(el: HTMLElement, basePath: string) {
  el.querySelectorAll<HTMLImageElement>("img[src]").forEach((img) => {
    const src = img.getAttribute("src") || "";
    if (!src || src.startsWith("#") || isExternalUrl(src)) return;
    const target = resolveRelPath(src, basePath);
    if (target) img.setAttribute("src", downloadURL(target));
  });
}

// statRepoTarget asks whether a repo-internal path is a file, a directory or gone. One listing
// of the parent tells all three in a single request; a missing or unreachable target is
// reported through onError and answered with null.
async function statRepoTarget(path: string, onError?: (message: string) => void): Promise<"file" | "dir" | null> {
  let d: { entries?: { name: string; type: string }[] } | null = null;
  try {
    d = await api(`api/fs/tree?path=${encodeURIComponent(dirName(path))}`);
  } catch {
    onError?.(t("view.cannot_check_file", { path }));
    return null;
  }
  const entry = (d?.entries || []).find((e) => e.name === baseName(path));
  if (!entry) {
    onError?.(t("view.file_not_found", { path }));
    return null;
  }
  return entry.type === "dir" ? "dir" : "file";
}

// openRepoTarget resolves a repo-internal path to a file (open in viewer) or directory
// (reveal in FILES); missing, denied, and unreachable targets are reported instead of doing
// nothing.
async function openRepoTarget(
  target: { path: string; line?: number; column?: number },
  onOpenFile?: (p: string, line?: number, column?: number, openInNew?: boolean) => void,
  onOpenDir?: (p: string) => void,
  onError?: (message: string) => void,
  openInNew = false,
) {
  const type = await statRepoTarget(target.path, onError);
  if (!type) return;
  if (type === "dir") onOpenDir?.(target.path);
  else if (onOpenFile) onOpenFile(target.path, target.line, target.column, openInNew);
  else onError?.(t("view.cannot_open_from_here", { path: target.path }));
}

// wireLinks classifies and rewires every <a> in the rendered markdown.
export function wireLinks(
  el: HTMLElement,
  basePath: string,
  baseDir: string,
  onOpenFile?: (path: string, line?: number, column?: number, openInNew?: boolean) => void,
  onOpenDir?: (path: string) => void,
  onError?: (message: string) => void,
  openMenu?: OpenPathLinkMenu,
  preview?: PreviewLinkWiring,
) {
  el.querySelectorAll<HTMLAnchorElement>("a[href]").forEach((a) => {
    const href = a.getAttribute("href") || "";

    if (href.startsWith("#")) {
      a.classList.add("anchor-link");
      a.addEventListener("click", (e) => {
        e.preventDefault();
        const id = decodeURIComponent(href.slice(1));
        let t: Element | null = null;
        try {
          t = el.querySelector("#" + CSS.escape(id));
        } catch {}
        t?.scrollIntoView({ behavior: "smooth", block: "start" });
      });
      return;
    }

    // The Chromium attachment action link (docs/log/53 §53.7). It has to be claimed
    // BEFORE the repo-file branch below: `/open/browser-attachment/<id>` carries
    // no scheme, so the file resolver would happily read it as a repo-root path
    // and answer the click with "file not found" — which is exactly how this
    // link died in the mirror. Opening the pane here also spares the user a full
    // page navigation; the action ROUTE stays for new tabs and reloads.
    const attachmentId = browserAttachmentIdFromLink(href);
    if (attachmentId) {
      a.classList.add("action-link");
      a.title = t("browser.attach.open_link");
      a.addEventListener("click", (e) => {
        e.preventDefault();
        void openBrowserAttachment(attachmentId);
      });
      return;
    }

    // A server of the workspace, on a surface that opts in: the preview of that port, not the
    // reader's own machine. After the two branches above, which own their hrefs.
    const loopback = preview ? loopbackTarget(href) : null;
    if (preview && loopback) {
      wirePreviewLink(a, loopback, preview);
      return;
    }

    if (isExternalUrl(href)) {
      a.target = "_blank";
      a.rel = "noopener noreferrer";
      a.classList.add("ext-link");
      return;
    }

    // Repo-internal relative link → open a file in the viewer or reveal a directory in
    // FILES (path decoded + resolved by the shared helper, so Japanese names work).
    a.classList.add("repo-link");
    const target = resolveMarkdownFileTarget(href, basePath, baseDir);
    if (target) {
      a.title = target.line
        ? t("view.open_in_pane_at_line", { path: target.path, line: target.line })
        : t("view.open_in_pane", { path: target.path });
      a.setAttribute("aria-label", t("view.open_in_pane_aria", { label: a.textContent || target.path }));
    }
    const openTarget = (openInNew: boolean) => {
      const resolved = resolveMarkdownFileTarget(href, basePath, baseDir);
      if (resolved) openRepoTarget(resolved, onOpenFile, onOpenDir, onError, openInNew);
      else onError?.(t("view.cannot_resolve_link", { href }));
    };
    // A context menu only where files can be opened from here (the shared view has no
    // onOpenFile and keeps the browser's own menu).
    const menu =
      openMenu && onOpenFile
        ? wireContextMenu(a, (x, y) => {
            // A link of a document that has since been replaced must not open a menu.
            if (!a.isConnected) return;
            // Re-resolved on open: the link was built when the document rendered.
            const resolved = resolveMarkdownFileTarget(href, basePath, baseDir);
            if (!resolved) return onError?.(t("view.cannot_resolve_link", { href }));
            void statRepoTarget(resolved.path, onError).then((type) => {
              if (!type || !a.isConnected) return;
              openMenu(
                {
                  path: resolved.path,
                  type,
                  openFile: (inNew) => onOpenFile(resolved.path, resolved.line, resolved.column, inNew),
                },
                x,
                y,
              );
            });
          })
        : null;
    a.addEventListener("click", (e) => {
      e.preventDefault();
      if (menu?.clickSwallowed()) return; // the lift of the long press that opened the menu
      const mouse = e as MouseEvent;
      openTarget(mouse.ctrlKey || mouse.metaKey);
    });
    a.addEventListener("auxclick", (e) => {
      if (e.button !== 1) return;
      e.preventDefault();
      openTarget(true);
    });
  });
}
