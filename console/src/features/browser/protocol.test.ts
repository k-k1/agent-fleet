import { describe, expect, it } from "vitest";
import type { BrowserOutbound, RemoteKeyLike } from "./protocol.ts";
import { BrowserInputBridge, clipboardShortcut, heldButton, modifiersOf, remotePoint } from "./protocol.ts";

const key = (overrides: Partial<RemoteKeyLike> = {}): RemoteKeyLike => ({
  key: "a",
  code: "KeyA",
  repeat: false,
  altKey: false,
  ctrlKey: false,
  metaKey: false,
  shiftKey: false,
  ...overrides,
});

describe("browser input protocol", () => {
  it("uses the CDP modifier mask and scales CSS coordinates to the remote viewport", () => {
    expect(modifiersOf({ altKey: true, ctrlKey: true, metaKey: false, shiftKey: true })).toBe(11);
    expect(remotePoint(
      { clientX: 60, clientY: 45, altKey: false, ctrlKey: false, metaKey: false, shiftKey: false },
      { left: 10, top: 20, width: 100, height: 50 },
      1200,
      800,
    )).toEqual({ x: 600, y: 400 });
  });

  it("suppresses composition keys and sends only the committed Japanese text", () => {
    const sent: BrowserOutbound[] = [];
    const bridge = new BrowserInputBridge((message) => sent.push(message));
    bridge.compositionStart();
    bridge.keyDown(key({ key: "Process", code: "Enter", isComposing: true }));
    bridge.compositionEnd("日本語");
    bridge.input("日本語"); // compositionend is commonly followed by an input event
    bridge.keyUp(key({ key: "Enter", code: "Enter" }));
    expect(sent).toEqual([{ type: "text", text: "日本語" }]);

    bridge.keyDown(key({ ctrlKey: true }));
    bridge.keyUp(key({ ctrlKey: true }));
    expect(sent.slice(1)).toEqual([
      { type: "key", event: "down", key: "a", code: "KeyA", modifiers: 2, repeat: false },
      { type: "key", event: "up", key: "a", code: "KeyA", modifiers: 2, repeat: false },
    ]);
  });
});

describe("drag and clipboard classification", () => {
  it("reads the held button out of the buttons bitmask", () => {
    expect(heldButton(0)).toBe("none");
    expect(heldButton(1)).toBe("left");
    expect(heldButton(2)).toBe("right");
    expect(heldButton(4)).toBe("middle");
    expect(heldButton(3)).toBe("left"); // left wins when several are down
  });

  it("recognises only the clipboard shortcuts, and not Alt-modified ones", () => {
    expect(clipboardShortcut(key({ key: "c", ctrlKey: true }))).toBe("copy");
    expect(clipboardShortcut(key({ key: "V", metaKey: true }))).toBe("paste");
    expect(clipboardShortcut(key({ key: "x", ctrlKey: true }))).toBe("cut");
    expect(clipboardShortcut(key({ key: "c" }))).toBeNull();
    expect(clipboardShortcut(key({ key: "c", ctrlKey: true, altKey: true }))).toBeNull();
    expect(clipboardShortcut(key({ key: "a", ctrlKey: true }))).toBeNull();
  });
});
