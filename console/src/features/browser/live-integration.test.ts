import { describe, expect, it } from "vitest";
import { BrowserController } from "./controller.ts";
import type { BrowserCanvas, BrowserControllerDeps, BrowserPageResult, BrowserSocket } from "./controller.ts";
import type { BrowserTarget } from "./target.ts";

const cpURL = process.env.AF_BROWSER_CP_E2E_URL ?? "";
const targetPort = Number(process.env.AF_BROWSER_TARGET_PORT ?? 0);

describe.runIf(Boolean(cpURL) && Number.isInteger(targetPort) && targetPort > 0)("live W2-W3-W4 browser integration", () => {
  it("round-trips REST, JPEG, state, and Japanese input through the real relay", async () => {
    let frames = 0;
    const deps: BrowserControllerDeps = {
      async createPage(target: BrowserTarget, viewport): Promise<BrowserPageResult> {
        const response = await fetch(`${cpURL}/api/browser/pages?tenant=default`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ ...target, viewport }),
        });
        expect(response.status).toBe(201);
        return await response.json() as BrowserPageResult;
      },
      async deletePage(id: string): Promise<void> {
        const response = await fetch(`${cpURL}/api/browser/pages/${encodeURIComponent(id)}?tenant=default`, { method: "DELETE" });
        expect(response.status).toBe(204);
      },
      openSocket(id: string): BrowserSocket {
        const url = new URL(cpURL);
        url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
        url.pathname = "/ws/browser";
        url.search = new URLSearchParams({ id, tenant: "default" }).toString();
        return new WebSocket(url) as unknown as BrowserSocket;
      },
      async drawFrame(_canvas: BrowserCanvas, frame: ArrayBuffer | Blob): Promise<void> {
        const data = frame instanceof Blob ? await frame.arrayBuffer() : frame;
        const bytes = new Uint8Array(data);
        expect(bytes[0]).toBe(0xff);
        expect(bytes[1]).toBe(0xd8);
        frames++;
      },
      hiddenGraceMs: 1_000,
    };

    const controller = new BrowserController("live-pane", { port: targetPort, path: "/" }, deps);
    controller.mount({ width: 0, height: 0 });
    controller.setVisible(true);
    try {
      await waitFor(() => controller.snapshot.state === "ready" && controller.snapshot.title === "W5 live browser" && frames > 0);
      controller.sendInput({ type: "text", text: "日本語" });
      await waitFor(() => controller.snapshot.console.some((entry) => entry.text.includes("input:日本語")));
    } finally {
      await controller.dispose();
    }
  }, 30_000);
});

async function waitFor(done: () => boolean, timeoutMs = 15_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (done()) return;
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  throw new Error("timed out waiting for the live browser contract");
}
