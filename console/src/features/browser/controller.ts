import type { BrowserTarget } from "./target.ts";
import { browserTarget } from "./target.ts";
import type { BrowserOutbound, BrowserPageState, BrowserSnapshot } from "./protocol.ts";
import { clampZoom } from "./protocol.ts";

export interface BrowserPageResult {
  id: string;
  port: number;
  url: string;
  state: string;
}

export interface BrowserSocket {
  binaryType: BinaryType;
  readyState: number;
  onopen: ((event: Event) => void) | null;
  onmessage: ((event: MessageEvent) => void) | null;
  onerror: ((event: Event) => void) | null;
  onclose: ((event: CloseEvent) => void) | null;
  send(data: string): void;
  close(): void;
}

export interface BrowserCanvas {
  width: number;
  height: number;
}

export interface BrowserControllerDeps {
  createPage(target: BrowserTarget, viewport: { width: number; height: number; deviceScaleFactor: 1 }): Promise<BrowserPageResult>;
  deletePage(id: string): Promise<void>;
  openSocket(id: string): BrowserSocket;
  drawFrame(canvas: BrowserCanvas, frame: ArrayBuffer | Blob): Promise<void>;
  hiddenGraceMs?: number;
}

type Listener = (snapshot: BrowserSnapshot) => void;

const initialSnapshot = (): BrowserSnapshot => ({
  state: "loading",
  url: "",
  title: "",
  width: 1200,
  height: 800,
  canBack: false,
  canForward: false,
  errorCode: "",
  errorMessage: "",
  console: [],
});

const clampViewport = (width: number, height: number): { width: number; height: number } => ({
  width: Math.max(1, Math.min(1600, Math.round(width))),
  height: Math.max(1, Math.min(1200, Math.round(height))),
});

/**
 * The LAYOUT viewport the Agent answers with once a pinch zoom is applied. It is
 * smaller than the pane (the page is laid out in fewer CSS pixels and rendered at
 * a matching device pixel ratio), and it is the space pointer coordinates live
 * in — so it is tracked separately from the pane's own size.
 */
const clampLayout = (width: number, height: number): { width: number; height: number } => ({
  width: Math.max(1, Math.min(4000, Math.round(width))),
  height: Math.max(1, Math.min(4000, Math.round(height))),
});

export class BrowserController {
  private targetValue: BrowserTarget;
  private snapshotValue = initialSnapshot();
  private readonly listeners = new Set<Listener>();
  private canvas: BrowserCanvas | null = null;
  private socket: BrowserSocket | null = null;
  private pageId: string | null = null;
  private generation = 0;
  private disposed = false;
  private visible = false;
  private hiddenTimer: ReturnType<typeof setTimeout> | null = null;
  private creating: Promise<void> | null = null;
  private rendering = false;
  private latestFrame: ArrayBuffer | Blob | null = null;
  private pendingPath: string | null = null;

  constructor(readonly paneId: string, target: BrowserTarget, private readonly deps: BrowserControllerDeps) {
    this.targetValue = target;
  }

  get target(): BrowserTarget {
    return this.targetValue;
  }

  get snapshot(): BrowserSnapshot {
    return this.snapshotValue;
  }

  subscribe(listener: Listener): () => void {
    this.listeners.add(listener);
    listener(this.snapshotValue);
    return () => this.listeners.delete(listener);
  }

  mount(canvas: BrowserCanvas): void {
    this.canvas = canvas;
  }

  unmount(canvas: BrowserCanvas): void {
    if (this.canvas === canvas) this.canvas = null;
    this.setVisible(false);
  }

  setVisible(visible: boolean): void {
    if (this.disposed) return;
    this.visible = visible;
    if (visible) {
      if (this.hiddenTimer) clearTimeout(this.hiddenTimer);
      this.hiddenTimer = null;
      if (this.pageId) this.send({ type: "visibility", visible: true });
      else void this.start();
      return;
    }
    if (this.pageId) this.send({ type: "visibility", visible: false });
    if (!this.hiddenTimer) {
      this.hiddenTimer = setTimeout(() => {
        this.hiddenTimer = null;
        if (!this.visible && !this.disposed) void this.resetRuntime(false);
      }, this.deps.hiddenGraceMs ?? 60_000);
    }
  }

  /**
   * The pane's own size, tracked apart from snapshot.width/height: with a pinch
   * zoom applied the Agent lays the page out smaller and answers with THAT size,
   * and comparing a resize against it would re-send a viewport forever.
   */
  private pane = { width: 0, height: 0 };
  private zoomValue = 1;

  setViewport(width: number, height: number): void {
    const next = clampViewport(width, height);
    if (next.width === this.pane.width && next.height === this.pane.height) return;
    this.pane = next;
    this.update({ width: next.width, height: next.height });
    this.send({ type: "viewport", ...next, zoom: this.zoomValue });
  }

  get zoom(): number {
    return this.zoomValue;
  }

  /** Pinch zoom: the Agent lays the page out this much smaller (see protocol.ts). */
  zoomBy(factor: number): void {
    const next = clampZoom(this.zoomValue * factor);
    if (next === this.zoomValue || this.pane.width < 1) return;
    this.zoomValue = next;
    this.send({ type: "viewport", ...this.pane, zoom: next });
  }

  /**
   * Double tap: back to the pane's own layout, or in to 2x. This pane has no
   * zoom-to-fit — it previews the user's OWN app at its real viewport — so
   * unzoomed already IS life size and there is no third state to visit.
   */
  toggleZoom(): void {
    if (this.pane.width < 1) return;
    const next = this.zoomValue > 1 ? 1 : 2;
    if (next === this.zoomValue) return;
    this.zoomValue = next;
    this.send({ type: "viewport", ...this.pane, zoom: next });
  }

  /**
   * Ask the page for its selection and put it on the USER's clipboard — the
   * container Chromium's own clipboard is unreachable from the browser tab.
   */
  copySelection(): Promise<boolean> {
    if (!this.socket) return Promise.resolve(false);
    return new Promise<string | null>((resolve) => {
      const timer = window.setTimeout(() => {
        this.clipboardWaiters.delete(resolve);
        resolve(null);
      }, 5000);
      this.clipboardTimers.set(resolve, timer);
      this.clipboardWaiters.add(resolve);
      this.send({ type: "copy" });
    }).then(async (text) => {
      if (!text) return false;
      try {
        await navigator.clipboard.writeText(text);
        return true;
      } catch {
        return false;
      }
    });
  }

  private readonly clipboardWaiters = new Set<(text: string | null) => void>();
  private readonly clipboardTimers = new Map<(text: string | null) => void, number>();

  private resolveClipboard(text: string): void {
    for (const resolve of [...this.clipboardWaiters]) {
      window.clearTimeout(this.clipboardTimers.get(resolve));
      this.clipboardTimers.delete(resolve);
      this.clipboardWaiters.delete(resolve);
      resolve(text);
    }
  }

  /** Adopt a navigation reported by the Page without constructing a new Page. */
  adoptTarget(target: BrowserTarget): void {
    this.targetValue = target;
  }

  changeTarget(target: BrowserTarget): void {
    if (target.port === this.targetValue.port) {
      this.targetValue = target;
      this.navigate(target.path);
      return;
    }
    this.targetValue = target;
    void this.restart();
  }

  navigate(path: string): void {
    const valid = browserTarget(this.targetValue.port, path);
    if (!valid) return;
    this.targetValue = valid;
    if (!this.send({ type: "navigate", path })) this.pendingPath = path;
  }

  reload(ignoreCache = false): void {
    this.send({ type: "reload", ignoreCache });
  }

  history(direction: "back" | "forward"): void {
    this.send({ type: "history", direction });
  }

  sendInput(message: BrowserOutbound): void {
    this.send(message);
  }

  async reconnect(): Promise<void> {
    await this.restart();
  }

  /** Drop an Agent-runtime Page across Stop/Start while retaining pane identity. */
  async resetRuntime(recreate: boolean): Promise<void> {
    if (this.disposed) return;
    this.generation++;
    this.pendingPath = null; // a recreated Page already starts at targetValue.path
    const staleCreate = this.creating;
    await this.releasePage();
    if (staleCreate) await staleCreate.catch(() => {});
    if (recreate && this.visible) await this.start();
  }

  async dispose(): Promise<void> {
    if (this.disposed) return;
    this.disposed = true;
    this.generation++;
    if (this.hiddenTimer) clearTimeout(this.hiddenTimer);
    this.hiddenTimer = null;
    await this.releasePage();
    this.listeners.clear();
    this.canvas = null;
  }

  private async restart(): Promise<void> {
    this.generation++;
    this.pendingPath = null; // a replacement Page is created directly at the latest target
    const staleCreate = this.creating;
    await this.releasePage();
    if (staleCreate) await staleCreate.catch(() => {});
    if (this.visible && !this.disposed) await this.start();
  }

  private async start(): Promise<void> {
    if (this.disposed || !this.visible || this.pageId || this.creating) return this.creating ?? undefined;
    const generation = ++this.generation;
    this.update({ state: "loading", errorCode: "", errorMessage: "" });
    const task = (async () => {
      try {
        const page = await this.deps.createPage(this.targetValue, {
          width: this.snapshotValue.width,
          height: this.snapshotValue.height,
          deviceScaleFactor: 1,
        });
        if (this.disposed || generation !== this.generation) {
          await this.deps.deletePage(page.id).catch(() => {});
          return;
        }
        this.pageId = page.id;
        this.update({ url: page.url, state: page.state === "target-unreachable" ? "target-unreachable" : "loading" });
        this.connect(page.id, generation);
      } catch (error) {
        if (generation !== this.generation || this.disposed) return;
        const e = error as { code?: string; message?: string };
        if (e.code === "browser_installing") {
          // First-use pinned Chromium install is running agent-side (lean rootfs
          // — docs/35 §35.7.2-4). Show "preparing" and poll until it lands.
          this.update({ state: "loading", errorCode: e.code, errorMessage: e.message || "" });
          setTimeout(() => {
            if (!this.disposed && this.visible && !this.pageId) void this.start();
          }, 5000);
          return;
        }
        this.update({ state: "disconnected", errorCode: e.code || "browser_start_failed", errorMessage: e.message || String(error) });
      }
    })();
    this.creating = task;
    await task.finally(() => {
      if (this.creating === task) this.creating = null;
    });
  }

  private connect(id: string, generation: number): void {
    const socket = this.deps.openSocket(id);
    this.socket = socket;
    socket.binaryType = "arraybuffer";
    socket.onopen = () => {
      if (socket !== this.socket || generation !== this.generation) return;
      const pane = this.pane.width > 0 ? this.pane : { width: this.snapshotValue.width, height: this.snapshotValue.height };
      this.send({ type: "viewport", ...pane, zoom: this.zoomValue });
      this.send({ type: "visibility", visible: this.visible });
      if (this.pendingPath) {
        const path = this.pendingPath;
        this.pendingPath = null;
        this.send({ type: "navigate", path });
      }
    };
    socket.onmessage = (event) => {
      if (socket !== this.socket || generation !== this.generation) return;
      if (typeof event.data === "string") this.handleText(event.data);
      else if (event.data instanceof ArrayBuffer || event.data instanceof Blob) this.queueFrame(event.data);
    };
    socket.onerror = () => {
      if (socket === this.socket) this.update({ state: "disconnected" });
    };
    socket.onclose = () => {
      if (socket !== this.socket) return;
      this.socket = null;
      if (!this.disposed && this.pageId) this.update({ state: "disconnected" });
    };
  }

  private handleText(raw: string): void {
    let message: Record<string, unknown>;
    try {
      message = JSON.parse(raw) as Record<string, unknown>;
    } catch {
      return;
    }
    switch (message.type) {
      case "ready": {
        if (message.version !== 1) {
          this.update({ state: "disconnected", errorCode: "browser_protocol_mismatch", errorMessage: String(message.version ?? "") });
          this.socket?.close();
          return;
        }
        const width = typeof message.width === "number" ? message.width : this.snapshotValue.width;
        const height = typeof message.height === "number" ? message.height : this.snapshotValue.height;
        this.update({
          state: "ready",
          url: typeof message.url === "string" ? message.url : this.snapshotValue.url,
          title: typeof message.title === "string" ? message.title : "",
          ...clampViewport(width, height),
        });
        return;
      }
      case "viewport":
        // A pinch zoom laid the page out smaller than the pane, so the canvas
        // must map pointer coordinates into THAT space.
        this.update(clampLayout(
          typeof message.width === "number" ? message.width : this.snapshotValue.width,
          typeof message.height === "number" ? message.height : this.snapshotValue.height,
        ));
        return;
      case "clipboard":
        this.resolveClipboard(typeof message.text === "string" ? message.text : "");
        return;
      case "navigation":
        this.update({
          url: typeof message.url === "string" ? message.url : this.snapshotValue.url,
          title: typeof message.title === "string" ? message.title : this.snapshotValue.title,
          canBack: message.canBack === true,
          canForward: message.canForward === true,
        });
        return;
      case "state": {
        const state = message.state;
        if (state === "loading" || state === "ready" || state === "disconnected" || state === "crashed" || state === "target-unreachable") {
          this.update({ state: state as BrowserPageState });
        }
        return;
      }
      case "console": {
        const entry = {
          level: typeof message.level === "string" ? message.level.slice(0, 20) : "log",
          text: typeof message.text === "string" ? message.text.slice(0, 16_384) : "",
          ts: typeof message.ts === "string" ? message.ts : "",
        };
        const console = [...this.snapshotValue.console, entry].slice(-200);
        this.update({ console });
        return;
      }
      case "page-error": {
        const detail = typeof message.text === "string"
          ? message.text
          : typeof message.message === "string"
            ? message.message + (typeof message.stack === "string" ? `\n${message.stack}` : "")
            : "Page error";
        const entry = {
          level: "error",
          text: detail.slice(0, 16_384),
          ts: typeof message.ts === "string" ? message.ts : "",
        };
        this.update({ console: [...this.snapshotValue.console, entry].slice(-200) });
        return;
      }
    }
  }

  private queueFrame(frame: ArrayBuffer | Blob): void {
    this.latestFrame = frame;
    if (this.rendering) return;
    this.rendering = true;
    void (async () => {
      // finally で必ず rendering を戻す — drawFrame が同期 throw した場合でも
      // フラグが立ちっぱなしになって以後のフレーム描画が止まることのないように。
      try {
        while (this.latestFrame) {
          const next = this.latestFrame;
          this.latestFrame = null;
          const canvas = this.canvas;
          if (canvas) await this.deps.drawFrame(canvas, next).catch(() => {});
        }
      } finally {
        this.rendering = false;
      }
    })();
  }

  private send(message: BrowserOutbound): boolean {
    const socket = this.socket;
    if (!socket || socket.readyState !== 1) return false;
    try {
      socket.send(JSON.stringify(message));
      return true;
    } catch {
      return false;
    }
  }

  private async releasePage(): Promise<void> {
    const socket = this.socket;
    this.socket = null;
    if (socket) {
      socket.onopen = socket.onmessage = socket.onerror = socket.onclose = null;
      try { socket.close(); } catch {}
    }
    this.latestFrame = null;
    const id = this.pageId;
    this.pageId = null;
    if (id) await this.deps.deletePage(id).catch(() => {});
  }

  private update(patch: Partial<BrowserSnapshot>): void {
    this.snapshotValue = { ...this.snapshotValue, ...patch };
    for (const listener of this.listeners) listener(this.snapshotValue);
  }
}

export class BrowserRegistry {
  private readonly controllers = new Map<string, BrowserController>();

  constructor(private readonly deps: BrowserControllerDeps) {}

  ensure(paneId: string, target: BrowserTarget): BrowserController {
    const existing = this.controllers.get(paneId);
    if (existing) {
      if (existing.target.port !== target.port || existing.target.path !== target.path) existing.changeTarget(target);
      return existing;
    }
    const controller = new BrowserController(paneId, target, this.deps);
    this.controllers.set(paneId, controller);
    return controller;
  }

  keepOnly(paneIds: readonly string[]): void {
    const keep = new Set(paneIds);
    for (const [paneId, controller] of this.controllers) {
      if (!keep.has(paneId)) {
        this.controllers.delete(paneId);
        void controller.dispose();
      }
    }
  }

  disposeAll(): void {
    for (const controller of this.controllers.values()) void controller.dispose();
    this.controllers.clear();
  }

  resetRuntime(recreate: boolean): void {
    for (const controller of this.controllers.values()) void controller.resetRuntime(recreate);
  }
}
