import type { BrowserCanvas, BrowserSocket } from "./controller.ts";
import type {
  BrowserConsoleEntry,
  BrowserOutbound,
  BrowserPageState,
  BrowserSnapshot,
} from "./protocol.ts";
import { clampZoom } from "./protocol.ts";

export type BrowserAttachmentControlMode = "view-only" | "user-control" | "locked";
export type BrowserAttachmentResult = "pending" | "completed" | "cancelled";
export type BrowserAttachmentViewState =
  | BrowserPageState
  | "expired"
  | "target-closed"
  | "unsupported-target-url"
  | "detached";

export interface BrowserAttachmentHandoff {
  message: string;
  completionLabel: string;
  allowCancel: boolean;
  controlMode: BrowserAttachmentControlMode;
  result: BrowserAttachmentResult;
}

export interface BrowserAttachmentStatus {
  id: string;
  state: string;
  title: string;
  url: string;
  expiresAt: string;
  controlMode: BrowserAttachmentControlMode;
  handoff: BrowserAttachmentHandoff | null;
}

const normalizedControlMode = (value: unknown): BrowserAttachmentControlMode =>
  value === "user-control" || value === "locked" ? value : "view-only";

const normalizedResult = (value: unknown): BrowserAttachmentResult =>
  value === "completed" || value === "cancelled" ? value : "pending";

const browserAttachmentHandoff = (value: unknown): BrowserAttachmentHandoff | null => {
  if (!value || typeof value !== "object") return null;
  const handoff = value as Record<string, unknown>;
  return {
    message: typeof handoff.message === "string" ? handoff.message : "",
    completionLabel: typeof handoff.completionLabel === "string" ? handoff.completionLabel : "",
    allowCancel: handoff.allowCancel === true,
    controlMode: normalizedControlMode(handoff.controlMode),
    result: normalizedResult(handoff.result),
  };
};

/** Decode the camelCase Agent/Control Plane attachment response. */
export function normalizeBrowserAttachmentStatus(raw: unknown): BrowserAttachmentStatus {
  const value = raw as Record<string, unknown> | null;
  if (!value || typeof value.id !== "string" || !value.id) {
    throw { code: "browser_attachment_invalid", message: "Browser attachment response did not contain an id" };
  }
  const mode = normalizedControlMode(value.controlMode);
  const handoff = browserAttachmentHandoff(value.handoff);
  return {
    id: value.id,
    state: typeof value.state === "string" ? value.state : "attached",
    title: typeof value.title === "string" ? value.title : "",
    url: typeof value.url === "string" ? value.url : "",
    expiresAt: typeof value.expiresAt === "string" ? value.expiresAt : "",
    // controlMode is the current enforcement state. The handoff copy records
    // the originally requested mode and intentionally stays unchanged when
    // Lane A's dedicated control-mode endpoint updates the attachment.
    controlMode: mode,
    handoff,
  };
}

export function browserAttachmentSocketURL(
  baseURI: string,
  pageProtocol: string,
  attachmentId: string,
  tenant = "",
): string {
  const url = new URL("ws/browser-attachments", baseURI);
  url.protocol = pageProtocol === "https:" ? "wss:" : "ws:";
  url.searchParams.set("id", attachmentId);
  if (tenant) url.searchParams.set("tenant", tenant);
  return url.toString();
}

export interface BrowserAttachmentSnapshot extends Omit<BrowserSnapshot, "state"> {
  state: BrowserAttachmentViewState;
  attachmentState: string;
  expiresAt: string;
  controlMode: BrowserAttachmentControlMode;
  handoff: BrowserAttachmentHandoff | null;
}

export interface BrowserAttachmentControllerDeps {
  getStatus(id: string): Promise<BrowserAttachmentStatus>;
  detach(id: string): Promise<void>;
  submitResult(id: string, result: Exclude<BrowserAttachmentResult, "pending">): Promise<BrowserAttachmentStatus | null>;
  openSocket(id: string): BrowserSocket;
  drawFrame(canvas: BrowserCanvas, frame: ArrayBuffer | Blob): Promise<void>;
}

type Listener = (snapshot: BrowserAttachmentSnapshot) => void;

const initialSnapshot = (): BrowserAttachmentSnapshot => ({
  state: "loading",
  attachmentState: "attached",
  url: "",
  title: "",
  width: 1200,
  height: 800,
  canBack: false,
  canForward: false,
  errorCode: "",
  errorMessage: "",
  console: [],
  expiresAt: "",
  controlMode: "view-only",
  handoff: null,
});

const clampViewport = (width: number, height: number): { width: number; height: number } => ({
  width: Math.max(1, Math.min(1600, Math.round(width))),
  height: Math.max(1, Math.min(1200, Math.round(height))),
});

/**
 * A zoomed-out LAYOUT viewport, which is deliberately allowed past the
 * screencast's 1600x1200 ceiling: the frames stay pane-sized (the Agent scales
 * them), only the coordinate space grows. Mirrors browserMaxLayout* in the Agent.
 */
const clampLayout = (width: number, height: number): { width: number; height: number } => ({
  width: Math.max(1, Math.min(4000, Math.round(width))),
  height: Math.max(1, Math.min(4000, Math.round(height))),
});

const terminalViewState = (state: string): BrowserAttachmentViewState | null => {
  if (state === "target-closed" || state === "unsupported-target-url" || state === "disconnected") return state;
  if (state === "expired" || state === "detached") return state;
  return null;
};

const attachmentStopsSocket = (state: string): boolean =>
  state === "target-closed" || state === "disconnected" || state === "expired" || state === "detached";

export class BrowserAttachmentController {
  private snapshotValue = initialSnapshot();
  private readonly listeners = new Set<Listener>();
  private canvas: BrowserCanvas | null = null;
  private socket: BrowserSocket | null = null;
  private generation = 0;
  private disposed = false;
  private visible = false;
  private loading: Promise<void> | null = null;
  private rendering = false;
  private latestFrame: ArrayBuffer | Blob | null = null;

  constructor(readonly paneId: string, readonly attachmentId: string, private readonly deps: BrowserAttachmentControllerDeps) {}

  get snapshot(): BrowserAttachmentSnapshot {
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
    if (!visible) {
      this.send({ type: "visibility", visible: false });
      return;
    }
    if (this.socket) this.send({ type: "visibility", visible: true });
    else void this.start();
  }

  /**
   * The pane's own size. It is tracked separately from snapshot.width/height
   * because with zoom-to-fit the Agent answers with a WIDER layout viewport —
   * the space pointer coordinates live in — and comparing against that would
   * re-send a viewport on every frame of a resize.
   */
  private pane = { width: 0, height: 0 };
  private fitValue = true;
  private zoomValue = 1;

  setViewport(width: number, height: number): void {
    const next = clampViewport(width, height);
    if (next.width === this.pane.width && next.height === this.pane.height) return;
    this.pane = next;
    this.update(next);
    this.sendViewport();
  }

  get fit(): boolean {
    return this.fitValue;
  }

  /**
   * Zoom the page out until its content fits the pane (or back to 1:1). It also
   * drops the pinch zoom: the toolbar's fit button is the one control that
   * restores a known view, and leaving a 4x pinch applied on top of it would
   * make "fit" not fit.
   */
  setFit(fit: boolean): void {
    if (this.fitValue === fit && this.zoomValue === 1) return;
    this.fitValue = fit;
    this.zoomValue = 1;
    if (this.pane.width < 1) return;
    this.update(this.pane);
    this.sendViewport();
  }

  get zoom(): number {
    return this.zoomValue;
  }

  /** Pinch zoom, applied on top of the pane (and of fit) by the Agent. */
  zoomBy(factor: number): void {
    const next = clampZoom(this.zoomValue * factor);
    if (next === this.zoomValue || this.pane.width < 1) return;
    this.zoomValue = next;
    this.sendViewport();
  }

  private sendViewport(): void {
    this.send({ type: "viewport", ...this.pane, fit: this.fitValue, zoom: this.zoomValue });
  }

  /**
   * Ask the page for its selection and put it on the user's clipboard. The
   * remote Chromium's own clipboard lives in the container, so a forwarded
   * Ctrl+C would copy into a void.
   */
  copySelection(): Promise<boolean> {
    if (!this.socket || this.snapshotValue.controlMode === "locked") return Promise.resolve(false);
    const pending = this.clipboardWaiters;
    return new Promise<string | null>((resolve) => {
      const timer = window.setTimeout(() => {
        pending.delete(resolve);
        resolve(null);
      }, 5000);
      this.clipboardTimers.set(resolve, timer);
      pending.add(resolve);
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

  sendInput(message: BrowserOutbound): void {
    // Dedicated attachment wire has no arbitrary navigate message. The Agent is
    // also authoritative, but rejecting here keeps view-only/locked inert even
    // against an accidental UI regression.
    if (this.snapshotValue.controlMode !== "user-control" || message.type === "navigate") return;
    this.send(message);
  }

  reload(ignoreCache = false): void {
    if (this.snapshotValue.controlMode === "user-control") this.send({ type: "reload", ignoreCache });
  }

  history(direction: "back" | "forward"): void {
    if (this.snapshotValue.controlMode === "user-control") this.send({ type: "history", direction });
  }

  async reconnect(): Promise<void> {
    this.closeSocket();
    if (this.visible && !this.disposed) await this.start();
  }

  async finish(result: Exclude<BrowserAttachmentResult, "pending">): Promise<void> {
    if (!this.snapshotValue.handoff || this.snapshotValue.handoff.result !== "pending") return;
    const status = await this.deps.submitResult(this.attachmentId, result);
    if (status) this.applyStatus(status);
    this.update({
      controlMode: "locked",
      handoff: { ...(this.snapshotValue.handoff ?? status?.handoff ?? { message: "", completionLabel: "", allowCancel: false }), result, controlMode: "locked" },
    });
  }

  async detach(): Promise<void> {
    await this.deps.detach(this.attachmentId);
    this.closeSocket();
    this.update({ state: "detached", attachmentState: "detached", controlMode: "locked" });
  }

  async dispose(): Promise<void> {
    if (this.disposed) return;
    this.disposed = true;
    this.generation++;
    this.closeSocket();
    this.listeners.clear();
    this.canvas = null;
  }

  private async start(): Promise<void> {
    if (this.disposed || !this.visible || this.socket || this.loading) return this.loading ?? undefined;
    const generation = ++this.generation;
    this.update({ state: "loading", errorCode: "", errorMessage: "" });
    const task = (async () => {
      try {
        const status = await this.deps.getStatus(this.attachmentId);
        if (this.disposed || generation !== this.generation) return;
        this.applyStatus(status);
        // locked and unsupported-target-url stop frames/input, not the wire.
        // The wire must remain live so a later handoff/control-mode/navigation
        // event can make this pane operable without a manual reconnect.
        if (attachmentStopsSocket(status.state) || !this.visible) return;
        this.connect(generation);
      } catch (error) {
        if (this.disposed || generation !== this.generation) return;
        const e = error as { code?: string; message?: string };
        const expired = e.code === "browser_attachment_not_found";
        this.update({
          state: expired ? "expired" : "disconnected",
          attachmentState: expired ? "expired" : "disconnected",
          errorCode: e.code || "browser_attachment_unreachable",
          errorMessage: e.message || String(error),
          controlMode: expired ? "locked" : this.snapshotValue.controlMode,
        });
      }
    })();
    this.loading = task;
    await task.finally(() => {
      if (this.loading === task) this.loading = null;
    });
  }

  private connect(generation: number): void {
    const socket = this.deps.openSocket(this.attachmentId);
    this.socket = socket;
    socket.binaryType = "arraybuffer";
    socket.onopen = () => {
      if (socket !== this.socket || generation !== this.generation) return;
      const pane = this.pane.width > 0 ? this.pane : { width: this.snapshotValue.width, height: this.snapshotValue.height };
      this.send({ type: "viewport", ...pane, fit: this.fitValue, zoom: this.zoomValue });
      this.send({ type: "visibility", visible: this.visible });
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
      if (!this.disposed) this.update({ state: "disconnected" });
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
          this.closeSocket();
          return;
        }
        const attachmentState = typeof message.state === "string" ? message.state : this.snapshotValue.attachmentState;
        const terminal = terminalViewState(attachmentState);
        const mode = message.controlMode;
        const controlMode = mode === "view-only" || mode === "user-control" || mode === "locked"
          ? mode
          : this.snapshotValue.controlMode;
        this.update({
          state: "ready",
          attachmentState,
          errorCode: "",
          errorMessage: "",
          url: typeof message.url === "string" ? message.url : this.snapshotValue.url,
          title: typeof message.title === "string" ? message.title : this.snapshotValue.title,
          controlMode: attachmentStopsSocket(attachmentState) ? "locked" : controlMode,
          handoff: message.handoff === undefined ? this.snapshotValue.handoff : browserAttachmentHandoff(message.handoff),
          ...clampViewport(
            typeof message.width === "number" ? message.width : this.snapshotValue.width,
            typeof message.height === "number" ? message.height : this.snapshotValue.height,
          ),
        });
        if (terminal) this.update({ state: terminal });
        return;
      }
      case "navigation":
        this.update({
          url: typeof message.url === "string" ? message.url : this.snapshotValue.url,
          title: typeof message.title === "string" ? message.title : this.snapshotValue.title,
          canBack: message.canBack === true,
          canForward: message.canForward === true,
        });
        return;
      case "state": {
        const state = String(message.state ?? "");
        const terminal = terminalViewState(state);
        if (terminal) this.update({
          state: terminal,
          attachmentState: state,
          ...(attachmentStopsSocket(state) ? { controlMode: "locked" as const } : {}),
        });
        else if (state === "loading" || state === "ready" || state === "attached" || state === "viewer-open") {
          this.update({
            state: state === "loading" ? "loading" : "ready",
            attachmentState: state,
            errorCode: "",
            errorMessage: "",
          });
        }
        return;
      }
      case "viewport":
        // Zoom-to-fit: the Agent laid the page out wider than the pane, so the
        // canvas must map pointer coordinates into THAT space.
        this.update(clampLayout(
          typeof message.width === "number" ? message.width : this.snapshotValue.width,
          typeof message.height === "number" ? message.height : this.snapshotValue.height,
        ));
        return;
      case "clipboard":
        this.resolveClipboard(typeof message.text === "string" ? message.text : "");
        return;
      case "control-mode": {
        const mode = message.controlMode;
        if (mode === "view-only" || mode === "user-control" || mode === "locked") {
          this.update({ controlMode: mode });
        }
        return;
      }
      case "handoff": {
        const handoff = browserAttachmentHandoff(message.handoff);
        const mode = message.controlMode;
        this.update({
          handoff,
          ...(mode === "view-only" || mode === "user-control" || mode === "locked" ? { controlMode: mode } : {}),
        });
        return;
      }
      case "console":
        this.appendConsole({
          level: typeof message.level === "string" ? message.level.slice(0, 20) : "log",
          text: typeof message.text === "string" ? message.text.slice(0, 16_384) : "",
          ts: typeof message.ts === "string" ? message.ts : "",
        });
        return;
      // The agent refuses input while the attachment is not in user-control and
      // says so — but nothing used to read it, so a pane whose input was being
      // dropped looked identical to a working one. Surfacing it in the console
      // drawer makes a stale Console bundle or a control-mode race diagnosable
      // instead of silent.
      case "protocol-error":
        this.appendConsole({
          level: "error",
          text: `[agent] ${String(message.code ?? "protocol_error")}: ${String(message.message ?? "")}`.slice(0, 16_384),
          ts: typeof message.ts === "string" ? message.ts : "",
        });
        return;
      case "page-error":
        this.appendConsole({
          level: "error",
          text: (typeof message.text === "string" ? message.text : String(message.message ?? "Page error")).slice(0, 16_384),
          ts: typeof message.ts === "string" ? message.ts : "",
        });
        return;
    }
  }

  private appendConsole(entry: BrowserConsoleEntry): void {
    this.update({ console: [...this.snapshotValue.console, entry].slice(-200) });
  }

  private applyStatus(status: BrowserAttachmentStatus): void {
    const terminal = terminalViewState(status.state);
    this.update({
      attachmentState: status.state,
      state: terminal ?? this.snapshotValue.state,
      title: status.title,
      url: status.url,
      expiresAt: status.expiresAt,
      controlMode: terminal ? "locked" : status.controlMode,
      handoff: status.handoff,
    });
  }

  private queueFrame(frame: ArrayBuffer | Blob): void {
    this.latestFrame = frame;
    if (this.rendering) return;
    this.rendering = true;
    void (async () => {
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

  private closeSocket(): void {
    const socket = this.socket;
    this.socket = null;
    if (!socket) return;
    socket.onopen = socket.onmessage = socket.onerror = socket.onclose = null;
    try { socket.close(); } catch {}
    this.latestFrame = null;
  }

  private update(patch: Partial<BrowserAttachmentSnapshot>): void {
    this.snapshotValue = { ...this.snapshotValue, ...patch };
    for (const listener of this.listeners) listener(this.snapshotValue);
  }
}

export class BrowserAttachmentRegistry {
  private readonly controllers = new Map<string, BrowserAttachmentController>();

  constructor(private readonly deps: BrowserAttachmentControllerDeps) {}

  ensure(paneId: string, attachmentId: string): BrowserAttachmentController {
    const existing = this.controllers.get(paneId);
    if (existing?.attachmentId === attachmentId) return existing;
    if (existing) void existing.dispose();
    const controller = new BrowserAttachmentController(paneId, attachmentId, this.deps);
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
}
