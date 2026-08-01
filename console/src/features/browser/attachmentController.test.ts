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

  it("records completion/cancellation as locked without detaching the owner", async () => {
    const fake = fakeDeps("user-control");
    const controller = new BrowserAttachmentController("p1", "ba_test", fake.deps);
    controller.setVisible(true);
    await settle();
    await controller.finish("completed");
    expect(fake.submitted).toEqual(["completed"]);
    expect(controller.snapshot.controlMode).toBe("locked");
    expect(controller.snapshot.handoff?.result).toBe("completed");
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
