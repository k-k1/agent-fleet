import { apiJSON, getTenant, raw, rel } from "../../core/api/client.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { allViews } from "../../layout/ops.ts";
import { BrowserRegistry } from "./controller.ts";
import type { BrowserCanvas, BrowserControllerDeps, BrowserPageResult } from "./controller.ts";
import type { BrowserTarget } from "./target.ts";

async function createPage(
  target: BrowserTarget,
  viewport: { width: number; height: number; deviceScaleFactor: 1 },
): Promise<BrowserPageResult> {
  const result = await apiJSON("api/browser/pages", "POST", { ...target, viewport });
  if (result?.error) throw result.error;
  if (!result || typeof result.id !== "string" || !result.id) {
    throw { code: "browser_start_failed", message: "Browser page response did not contain an id" };
  }
  // Not an unchecked cast: every field, not just id, is normalized to its declared type, so
  // a malformed response cannot silently hand a wrong type to targetFromURL or the state
  // checks downstream.
  return {
    id: result.id,
    port: typeof result.port === "number" ? result.port : 0,
    url: typeof result.url === "string" ? result.url : "",
    state: typeof result.state === "string" ? result.state : "",
  };
}

async function deletePage(id: string): Promise<void> {
  await raw(`api/browser/pages/${encodeURIComponent(id)}`, { method: "DELETE" });
}

function browserSocket(id: string): WebSocket {
  const url = new URL(rel("ws/browser"));
  url.protocol = location.protocol === "https:" ? "wss:" : "ws:";
  url.searchParams.set("id", id);
  const tenant = getTenant();
  if (tenant) url.searchParams.set("tenant", tenant);
  return new WebSocket(url);
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

const deps: BrowserControllerDeps = {
  createPage,
  deletePage,
  openSocket: browserSocket,
  drawFrame,
};

const registry = new BrowserRegistry(deps);

export const ensureBrowser = (paneId: string, target: BrowserTarget) => registry.ensure(paneId, target);

/** Dispose Browser Pages only when their pane identity leaves the layout. */
export function wireBrowserReconcile(): () => void {
  return useLayoutStore.subscribe((state, previous) => {
    if (state.layout === previous.layout) return;
    registry.keepOnly(allViews(state.layout).map((pane) => pane.id));
  });
}

export function disposeAllBrowsers(): void {
  registry.disposeAll();
}

export function resetBrowserRuntime(recreate: boolean): void {
  registry.resetRuntime(recreate);
}
