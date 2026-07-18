import { describe, expect, it, vi } from "vitest";
import type { BrowserCanvas, BrowserControllerDeps, BrowserPageResult, BrowserSocket } from "./controller.ts";
import { BrowserController, BrowserRegistry } from "./controller.ts";
import type { BrowserTarget } from "./target.ts";

class FakeSocket implements BrowserSocket {
  binaryType: BinaryType = "blob";
  readyState = 0;
  onopen: ((event: Event) => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  onclose: ((event: CloseEvent) => void) | null = null;
  readonly sent: string[] = [];

  open(): void {
    this.readyState = 1;
    this.onopen?.(new Event("open"));
  }
  message(data: string | ArrayBuffer): void {
    this.onmessage?.({ data } as MessageEvent);
  }
  send(data: string): void { this.sent.push(data); }
  close(): void { this.readyState = 3; }
  json(): unknown[] { return this.sent.map((message) => JSON.parse(message)); }
}

function fakeDeps(drawFrame?: BrowserControllerDeps["drawFrame"]) {
  const created: Array<{ target: BrowserTarget; viewport: { width: number; height: number; deviceScaleFactor: 1 } }> = [];
  const deleted: string[] = [];
  const sockets: FakeSocket[] = [];
  let nextId = 0;
  const deps: BrowserControllerDeps = {
    hiddenGraceMs: 100,
    async createPage(target, viewport): Promise<BrowserPageResult> {
      created.push({ target, viewport });
      return { id: `browser-${++nextId}`, port: target.port, url: `http://127.0.0.1:${target.port}${target.path}`, state: "starting" };
    },
    async deletePage(id) { deleted.push(id); },
    openSocket() { const socket = new FakeSocket(); sockets.push(socket); return socket; },
    drawFrame: drawFrame ?? (async () => {}),
  };
  return { deps, created, deleted, sockets };
}

const settle = async () => {
  await Promise.resolve();
  await Promise.resolve();
};

describe("BrowserController with fake REST/WS/CDP-facing transport", () => {
  it("creates from port/path, draws a fixed JPEG, and forwards restricted controls", async () => {
    const drawn: Uint8Array[] = [];
    const fake = fakeDeps(async (_canvas, frame) => { drawn.push(new Uint8Array(frame as ArrayBuffer)); });
    const controller = new BrowserController("p7", { port: 5173, path: "/app" }, fake.deps);
    const canvas: BrowserCanvas = { width: 0, height: 0 };
    controller.mount(canvas);
    controller.setViewport(900, 600);
    controller.setVisible(true);
    await settle();

    expect(fake.created).toEqual([{ target: { port: 5173, path: "/app" }, viewport: { width: 900, height: 600, deviceScaleFactor: 1 } }]);
    const socket = fake.sockets[0];
    socket.open();
    socket.message(JSON.stringify({ type: "ready", version: 1, url: "http://127.0.0.1:5173/app", title: "App", width: 900, height: 600 }));
    const fixedJPEG = new Uint8Array([0xff, 0xd8, 0xff, 0xd9]).buffer;
    socket.message(fixedJPEG);
    await settle();
    controller.sendInput({ type: "text", text: "日本語" });
    controller.sendInput({ type: "mouse", event: "down", x: 10, y: 20, button: "left", buttons: 1, modifiers: 0, clickCount: 1 });
    controller.navigate("/next");

    expect(controller.snapshot).toMatchObject({ state: "ready", title: "App", width: 900, height: 600 });
    expect([...drawn[0]]).toEqual([0xff, 0xd8, 0xff, 0xd9]);
    expect(socket.json()).toContainEqual({ type: "text", text: "日本語" });
    expect(socket.json()).toContainEqual({ type: "navigate", path: "/next" });
    expect(socket.json()).toContainEqual({ type: "visibility", visible: true });
  });

  it("keeps only the newest queued frame while drawing is backpressured", async () => {
    let releaseFirst!: () => void;
    const firstDraw = new Promise<void>((resolve) => { releaseFirst = resolve; });
    const seen: number[] = [];
    const fake = fakeDeps(async (_canvas, frame) => {
      seen.push(new Uint8Array(frame as ArrayBuffer)[0]);
      if (seen.length === 1) await firstDraw;
    });
    const controller = new BrowserController("p0", { port: 3000, path: "/" }, fake.deps);
    controller.mount({ width: 0, height: 0 });
    controller.setVisible(true);
    await settle();
    const socket = fake.sockets[0];
    socket.open();
    socket.message(new Uint8Array([1]).buffer);
    socket.message(new Uint8Array([2]).buffer);
    socket.message(new Uint8Array([3]).buffer);
    await settle();
    expect(seen).toEqual([1]);
    releaseFirst();
    await settle();
    expect(seen).toEqual([1, 3]);
  });

  it("suspends while hidden, deletes after grace, and recreates from the saved target", async () => {
    vi.useFakeTimers();
    try {
      const fake = fakeDeps();
      const controller = new BrowserController("p0", { port: 8080, path: "/health" }, fake.deps);
      controller.setVisible(true);
      await settle();
      fake.sockets[0].open();
      controller.setVisible(false);
      expect(fake.sockets[0].json()).toContainEqual({ type: "visibility", visible: false });
      await vi.advanceTimersByTimeAsync(100);
      expect(fake.deleted).toEqual(["browser-1"]);
      controller.setVisible(true);
      await settle();
      expect(fake.created.map((entry) => entry.target)).toEqual([
        { port: 8080, path: "/health" },
        { port: 8080, path: "/health" },
      ]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("uses paneId as registry identity and immediately disposes a removed pane", async () => {
    const fake = fakeDeps();
    const registry = new BrowserRegistry(fake.deps);
    const first = registry.ensure("p4", { port: 3000, path: "/" });
    expect(registry.ensure("p4", { port: 3000, path: "/" })).toBe(first);
    first.setVisible(true);
    await settle();
    registry.keepOnly([]);
    await settle();
    expect(fake.deleted).toEqual(["browser-1"]);
  });
});
