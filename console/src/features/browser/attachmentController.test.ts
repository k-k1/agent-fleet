import { describe, expect, it, vi } from "vitest";
import type { BrowserCanvas, BrowserSocket } from "./controller.ts";
import {
  BrowserAttachmentController,
  browserAttachmentSocketURL,
  normalizeBrowserAttachmentStatus,
  type BrowserAttachmentControllerDeps,
  type BrowserAttachmentStatus,
} from "./attachmentController.ts";

class FakeSocket implements BrowserSocket {
  binaryType: BinaryType = "blob";
  readyState = 0;
  onopen: ((event: Event) => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  onclose: ((event: CloseEvent) => void) | null = null;
  readonly sent: string[] = [];
  open(): void { this.readyState = 1; this.onopen?.(new Event("open")); }
  message(data: string | ArrayBuffer): void { this.onmessage?.({ data } as MessageEvent); }
  send(data: string): void { this.sent.push(data); }
  close(): void { this.readyState = 3; }
  json(): unknown[] { return this.sent.map((message) => JSON.parse(message)); }
}

const status = (mode: BrowserAttachmentStatus["controlMode"]): BrowserAttachmentStatus => ({
  id: "ba_test",
  state: "attached",
  title: "Editor",
  url: "https://example.invalid/edit?token=secret",
  expiresAt: "2099-01-01T00:00:00Z",
  controlMode: mode,
  handoff: {
    message: "Confirm",
    completionLabel: "Done",
    allowCancel: true,
    controlMode: mode,
    result: "pending",
  },
});

function fakeDeps(mode: BrowserAttachmentStatus["controlMode"]) {
  const socket = new FakeSocket();
  const submitted: string[] = [];
  const detached: string[] = [];
  const deps: BrowserAttachmentControllerDeps = {
    async getStatus() { return status(mode); },
    async detach(id) { detached.push(id); },
    async submitResult(_id, result) { submitted.push(result); return null; },
    openSocket() { return socket; },
    async drawFrame(_canvas: BrowserCanvas) {},
  };
  return { deps, socket, submitted, detached };
}

const settle = async () => { await Promise.resolve(); await Promise.resolve(); };

describe("BrowserAttachmentController", () => {
  it("connects only to the attachment wire and blocks all page input in view-only", async () => {
    const fake = fakeDeps("view-only");
    const controller = new BrowserAttachmentController("p1", "ba_test", fake.deps);
    controller.mount({ width: 0, height: 0 });
    controller.setVisible(true);
    await settle();
    fake.socket.open();
    controller.sendInput({ type: "text", text: "must-not-send" });
    controller.reload();
    expect(fake.socket.json()).toContainEqual({ type: "visibility", visible: true });
    expect(fake.socket.json()).not.toContainEqual({ type: "text", text: "must-not-send" });
    expect(fake.socket.json().some((message) => (message as { type?: string }).type === "reload")).toBe(false);
  });

  it("forwards restricted input and IME text in user-control, but never navigate", async () => {
    const fake = fakeDeps("user-control");
    const controller = new BrowserAttachmentController("p1", "ba_test", fake.deps);
    controller.setVisible(true);
    await settle();
    fake.socket.open();
    controller.sendInput({ type: "text", text: "日本語" });
    controller.sendInput({ type: "navigate", path: "/forbidden" });
    controller.history("back");
    expect(fake.socket.json()).toContainEqual({ type: "text", text: "日本語" });
    expect(fake.socket.json()).toContainEqual({ type: "history", direction: "back" });
    expect(fake.socket.json().some((message) => (message as { type?: string }).type === "navigate")).toBe(false);
  });

  // Double tap has to reach LIFE SIZE, and only the Agent knows how far that is:
  // it measured the page for zoom-to-fit. The controller recovers the base from
  // the layout the Agent reported (layout x zoom) rather than tracking its own.
  it("toggles between the fitted view and life size on a double tap", async () => {
    const fake = fakeDeps("view-only");
    const controller = new BrowserAttachmentController("p1", "ba_test", fake.deps);
    controller.setVisible(true);
    await settle();
    fake.socket.open();
    controller.setViewport(390, 800);
    // Zoom-to-fit: the Agent laid a 1240 px site out wide and said so.
    fake.socket.message(JSON.stringify({ type: "viewport", width: 1240, height: 2544 }));

    controller.toggleZoom();
    // 1240 / 390 = one layout pixel per pane pixel.
    expect(controller.zoom).toBeCloseTo(3.18, 2);
    expect(fake.socket.json().at(-1)).toMatchObject({ type: "viewport", width: 390, height: 800, zoom: controller.zoom });

    // The Agent answers with the zoomed layout; tapping again returns to the fit.
    fake.socket.message(JSON.stringify({ type: "viewport", width: 390, height: 800 }));
    controller.toggleZoom();
    expect(controller.zoom).toBe(1);
    expect(fake.socket.json().at(-1)).toMatchObject({ type: "viewport", zoom: 1 });
  });

  // Without fit the base IS the pane, so there is no 1:1 to jump to.
  it("double taps to 2x when the pane is already life size", async () => {
    const fake = fakeDeps("view-only");
    const controller = new BrowserAttachmentController("p1", "ba_test", fake.deps);
    controller.setVisible(true);
    await settle();
    fake.socket.open();
    controller.setViewport(390, 800);

    controller.toggleZoom();
    expect(controller.zoom).toBe(2);
  });

  it("keeps the wire connected while locked and applies ready and handoff transitions", async () => {
    const fake = fakeDeps("locked");
    const controller = new BrowserAttachmentController("p1", "ba_test", fake.deps);
    controller.setVisible(true);
    await settle();
    fake.socket.open();
    fake.socket.message(JSON.stringify({
      type: "ready",
      version: 1,
      state: "viewer-open",
      url: "https://example.invalid/review",
      title: "Review",
      width: 1280,
      height: 900,
      controlMode: "locked",
      handoff: null,
    }));
    expect(controller.snapshot).toMatchObject({
      state: "ready",
      attachmentState: "viewer-open",
      controlMode: "locked",
      handoff: null,
    });

    fake.socket.message(JSON.stringify({
      type: "handoff",
      controlMode: "user-control",
      handoff: {
        message: "Confirm the change",
        completionLabel: "Approve",
        allowCancel: true,
        controlMode: "user-control",
        result: "pending",
      },
    }));
    controller.sendInput({ type: "text", text: "approved" });
    expect(controller.snapshot).toMatchObject({
      controlMode: "user-control",
      handoff: { message: "Confirm the change", completionLabel: "Approve", result: "pending" },
    });
    expect(fake.socket.json()).toContainEqual({ type: "text", text: "approved" });
  });

  it("clears unsupported state when the attachment returns to attached or viewer-open", async () => {
    const fake = fakeDeps("view-only");
    const controller = new BrowserAttachmentController("p1", "ba_test", fake.deps);
    controller.setVisible(true);
    await settle();
    fake.socket.open();

    for (const recoveredState of ["attached", "viewer-open"]) {
      fake.socket.message(JSON.stringify({ type: "state", state: "unsupported-target-url" }));
      expect(controller.snapshot).toMatchObject({
        state: "unsupported-target-url",
        attachmentState: "unsupported-target-url",
      });
      fake.socket.message(JSON.stringify({ type: "state", state: recoveredState }));
      expect(controller.snapshot).toMatchObject({
        state: "ready",
        attachmentState: recoveredState,
        errorCode: "",
        errorMessage: "",
      });
    }
  });

  it("records completion/cancellation as locked without detaching the owner", async () => {
    const fake = fakeDeps("user-control");
    const controller = new BrowserAttachmentController("p1", "ba_test", fake.deps);
    controller.setVisible(true);
    await settle();
    fake.socket.open();
    const visibilityBeforeFinish = fake.socket.json().filter((message) =>
      (message as { type?: string }).type === "visibility");
    await controller.finish("completed");
    expect(fake.submitted).toEqual(["completed"]);
    expect(controller.snapshot.controlMode).toBe("locked");
    expect(controller.snapshot.handoff?.result).toBe("completed");
    expect(fake.socket.json().filter((message) =>
      (message as { type?: string }).type === "visibility")).toEqual(visibilityBeforeFinish);
    expect(fake.detached).toEqual([]);
    await controller.detach();
    expect(fake.detached).toEqual(["ba_test"]);
    expect(controller.snapshot.state).toBe("detached");
  });

  it("shows an expired overlay when the short-lived id no longer resolves", async () => {
    const fake = fakeDeps("view-only");
    fake.deps.getStatus = vi.fn().mockRejectedValue({ code: "browser_attachment_not_found" });
    const controller = new BrowserAttachmentController("p1", "ba_expired", fake.deps);
    controller.setVisible(true);
    await settle();
    expect(controller.snapshot).toMatchObject({ state: "expired", controlMode: "locked" });
  });
});

describe("attachment API normalization", () => {
  it("uses Lane A's camelCase shape and keeps current control mode independent from handoff", () => {
    expect(normalizeBrowserAttachmentStatus({
      id: "ba_test",
      state: "attached",
      title: "Editor",
      url: "https://example.invalid/edit",
      expiresAt: "2099-01-01T00:00:00Z",
      controlMode: "locked",
      handoff: {
        message: "Check",
        completionLabel: "Finish",
        allowCancel: true,
        controlMode: "user-control",
        result: "pending",
      },
    })).toMatchObject({
      expiresAt: "2099-01-01T00:00:00Z",
      controlMode: "locked",
      handoff: { completionLabel: "Finish", allowCancel: true, controlMode: "user-control" },
    });
  });

  it("uses the dedicated attachment WebSocket namespace with tenant membership", () => {
    const url = browserAttachmentSocketURL("https://fleet.invalid/console/", "https:", "ba_test", "acme");
    expect(url).toBe("wss://fleet.invalid/console/ws/browser-attachments?id=ba_test&tenant=acme");
    expect(url).not.toContain("/ws/browser?");
  });
});
