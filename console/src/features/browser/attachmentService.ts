import { api, apiJSON, getTenant } from "../../core/api/client.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { allViews } from "../../layout/ops.ts";
import type { BrowserCanvas, BrowserSocket } from "./controller.ts";
import {
  BrowserAttachmentRegistry,
  browserAttachmentSocketURL,
  normalizeBrowserAttachmentSiblingTargets,
  normalizeBrowserAttachmentStatus,
  type BrowserAttachmentResult,
  type BrowserAttachmentSiblingTarget,
  type BrowserAttachmentStatus,
} from "./attachmentController.ts";

function throwAPIError(data: unknown): void {
  const error = data && typeof data === "object" ? (data as { error?: unknown }).error : null;
  if (error) throw error;
}

export async function getBrowserAttachment(id: string): Promise<BrowserAttachmentStatus> {
  const data = await api(`api/browser/attachments/${encodeURIComponent(id)}`);
  throwAPIError(data);
  const status = normalizeBrowserAttachmentStatus(data);
  const expires = Date.parse(status.expiresAt);
  if (Number.isFinite(expires) && expires <= Date.now()) {
    throw { code: "browser_attachment_not_found", message: "Browser attachment expired" };
  }
  return status;
}

/**
 * The live attachments, newest first. The action link is the primary way in
 * (docs/log/53 §53.7); this is the way BACK in once that link has scrolled out of
 * the mirror or its pane was closed while the hand-off is still pending.
 * Expired-but-not-yet-reaped entries are dropped here, as getBrowserAttachment
 * does, so the list never offers a pane that cannot open.
 */
export async function listBrowserAttachments(): Promise<BrowserAttachmentStatus[]> {
  const data = await api("api/browser/attachments");
  throwAPIError(data);
  const raw = (data as { attachments?: unknown })?.attachments;
  if (!Array.isArray(raw)) return [];
  const now = Date.now();
  const live: BrowserAttachmentStatus[] = [];
  for (const entry of raw) {
    try {
      const status = normalizeBrowserAttachmentStatus(entry);
      const expires = Date.parse(status.expiresAt);
      if (Number.isFinite(expires) && expires <= now) continue;
      live.push(status);
    } catch {
      // One malformed entry must not hide the rest of the list.
    }
  }
  return live;
}

async function detachBrowserAttachment(id: string): Promise<void> {
  const data = await api(`api/browser/attachments/${encodeURIComponent(id)}`, { method: "DELETE" });
  throwAPIError(data);
}

async function submitBrowserAttachmentResult(
  id: string,
  handoffResult: Exclude<BrowserAttachmentResult, "pending">,
): Promise<BrowserAttachmentStatus | null> {
  const data = await apiJSON(`api/browser/attachments/${encodeURIComponent(id)}/handoff-result`, "POST", {
    result: handoffResult,
  });
  throwAPIError(data);
  return data && typeof data.id === "string" ? normalizeBrowserAttachmentStatus(data) : null;
}

/** Other targets on the same Chromium instance an attachment could switch to. */
export async function listBrowserAttachmentSiblingTargets(id: string): Promise<BrowserAttachmentSiblingTarget[]> {
  const data = await api(`api/browser/attachments/${encodeURIComponent(id)}/targets`);
  throwAPIError(data);
  return normalizeBrowserAttachmentSiblingTargets(data);
}

async function retargetBrowserAttachment(id: string, targetId: string): Promise<BrowserAttachmentStatus> {
  const data = await apiJSON(`api/browser/attachments/${encodeURIComponent(id)}/retarget`, "POST", { targetId });
  throwAPIError(data);
  return normalizeBrowserAttachmentStatus(data);
}

function attachmentSocket(id: string): BrowserSocket {
  return new WebSocket(browserAttachmentSocketURL(document.baseURI, location.protocol, id, getTenant()));
}

async function drawFrame(canvas: BrowserCanvas, frame: ArrayBuffer | Blob): Promise<void> {
  const element = canvas as HTMLCanvasElement;
  const blob = frame instanceof Blob ? frame : new Blob([frame], { type: "image/jpeg" });
  const bitmap = await createImageBitmap(blob);
  try {
    element.width = bitmap.width;
    element.height = bitmap.height;
    element.getContext("2d", { alpha: false })?.drawImage(bitmap, 0, 0);
  } finally {
    bitmap.close();
  }
}

const registry = new BrowserAttachmentRegistry({
  getStatus: getBrowserAttachment,
  detach: detachBrowserAttachment,
  submitResult: submitBrowserAttachmentResult,
  openSocket: attachmentSocket,
  drawFrame,
  listSiblingTargets: listBrowserAttachmentSiblingTargets,
  retarget: retargetBrowserAttachment,
});

export const ensureBrowserAttachment = (paneId: string, attachmentId: string) =>
  registry.ensure(paneId, attachmentId);

export function wireBrowserAttachmentReconcile(): () => void {
  return useLayoutStore.subscribe((state, previous) => {
    if (state.layout === previous.layout) return;
    registry.keepOnly(allViews(state.layout).map((pane) => pane.id));
  });
}

export function disposeAllBrowserAttachments(): void {
  registry.disposeAll();
}
