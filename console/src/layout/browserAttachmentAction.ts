import { mobileMatches } from "../lib/device.ts";
import type { Layout, OpenTarget } from "./types.ts";
import { activePane, allPanes, allViews, isBlankPane, openActive, openInNew, sameTarget } from "./ops.ts";

export const DESKTOP_PANE_LIMIT = 8;
export const MOBILE_PANE_LIMIT = 2;

export type BrowserAttachmentOpenPlan =
  | { kind: "commit"; layout: Layout }
  | { kind: "confirm-replace" };

/** Extract an opaque attachment id from the Console action URL, if this is one. */
export function browserAttachmentIdFromPath(pathname: string): string | null {
  const match = pathname.match(/(?:^|\/)open\/browser-attachment\/([^/]+)\/?$/);
  if (!match) return null;
  try {
    const id = decodeURIComponent(match[1]);
    return /^[A-Za-z0-9_-]{1,256}$/.test(id) ? id : null;
  } catch {
    return null;
  }
}

/**
 * Same question as browserAttachmentIdFromPath, asked of a Markdown link href.
 * The agent is told to paste `open_url` verbatim (a path), but a full same-origin
 * URL is the obvious variation, and both must open the pane rather than being
 * mistaken for a repository file path. A foreign origin is never ours to open.
 */
export function browserAttachmentIdFromLink(href: string, baseURI = document.baseURI): string | null {
  if (!href) return null;
  let url: URL;
  try {
    url = new URL(href, baseURI);
    if (url.origin !== new URL(baseURI).origin) return null;
  } catch {
    return null;
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") return null;
  return browserAttachmentIdFromPath(url.pathname);
}

export const browserAttachmentTarget = (attachmentId: string): OpenTarget => ({
  content: { kind: "browserAttach", attachmentId },
});

/**
 * Reuse openInNew's focus → blank → right column → down split ordering, but
 * stop before its cap-only implicit overwrite branch. Action links must ask the
 * user before replacing anything that is already visible.
 */
export function planBrowserAttachmentOpen(
  layout: Layout,
  attachmentId: string,
  mobile = mobileMatches(),
): BrowserAttachmentOpenPlan {
  const target = browserAttachmentTarget(attachmentId);
  const panes = layout.mode === "tabs" ? allViews(layout) : allPanes(layout);
  if (panes.some((pane) => sameTarget(pane, target)) || panes.some(isBlankPane)) {
    return { kind: "commit", layout: openInNew(layout, target, { mobile }) };
  }
  const limit = mobile ? MOBILE_PANE_LIMIT : DESKTOP_PANE_LIMIT;
  if (panes.length >= limit) return { kind: "confirm-replace" };
  return { kind: "commit", layout: openInNew(layout, target, { mobile }) };
}

/** Apply the user's explicit cap dialog choice to whichever pane is active now. */
export function replaceActiveWithBrowserAttachment(layout: Layout, attachmentId: string): Layout {
  if (!activePane(layout)) return layout;
  return openActive(layout, browserAttachmentTarget(attachmentId));
}

/** The dynamic <base> in index.html points at the canonical Console root. */
export function canonicalWorkspaceURL(baseURI = document.baseURI): string {
  const url = new URL(".", baseURI);
  return url.pathname + url.search + url.hash;
}

interface BrowserAttachmentActionStatus {
  id: string;
  expiresAt: string;
}

export interface BrowserAttachmentActionDeps {
  attachmentId: string;
  mobile: boolean;
  getStatus(id: string): Promise<BrowserAttachmentActionStatus>;
  getLayout(): Layout;
  commit(layout: Layout): Promise<boolean>;
  confirmReplace(): Promise<boolean>;
  replaceURL(): void;
  now?: number;
}

/** Execute the verified one-click action; returns false for either user cancel. */
export async function runBrowserAttachmentAction(deps: BrowserAttachmentActionDeps): Promise<boolean> {
  const status = await deps.getStatus(deps.attachmentId);
  if (status.id !== deps.attachmentId) throw { code: "browser_attachment_invalid" };
  const expires = Date.parse(status.expiresAt);
  if (Number.isFinite(expires) && expires <= (deps.now ?? Date.now())) {
    throw { code: "browser_attachment_not_found" };
  }

  let plan = planBrowserAttachmentOpen(deps.getLayout(), deps.attachmentId, deps.mobile);
  if (plan.kind === "confirm-replace") {
    if (!(await deps.confirmReplace())) return false;
    plan = planBrowserAttachmentOpen(deps.getLayout(), deps.attachmentId, deps.mobile);
    if (plan.kind === "confirm-replace") {
      plan = {
        kind: "commit",
        layout: replaceActiveWithBrowserAttachment(deps.getLayout(), deps.attachmentId),
      };
    }
  }
  if (!(await deps.commit(plan.layout))) return false;
  deps.replaceURL();
  return true;
}
