import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type React from "react";
import { useSendQueueStore } from "./store.ts";
import { useSendQueue } from "./useSendQueue.ts";
import { SendQueueList } from "./SendQueueList.tsx";
import type { QueuedSend } from "./queue.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;
function render(el: React.ReactElement) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root!.render(el));
  return { rerender: (e: React.ReactElement) => act(() => root!.render(e)) };
}
const text = (id: string) => host!.querySelector(`[data-testid="${id}"]`)!.textContent;
const buttons = (re: RegExp) =>
  Array.from(host!.querySelectorAll("button")).filter((b) => re.test(b.getAttribute("aria-label") || b.textContent || ""));
const click = (el: Element) => act(() => { (el as HTMLElement).click(); });
function typeInto(el: HTMLTextAreaElement, value: string) {
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const key = (el: Element, init: KeyboardEventInit) =>
  act(() => { el.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init })); });

function Harness({ session, busy, canSend = true, send }: { session: string; busy: boolean; canSend?: boolean; send: (i: QueuedSend) => Promise<boolean> }) {
  const q = useSendQueue({ session, busy, canSend, sendItem: send });
  return <div data-testid="n">{q.items.map((i) => i.text).join("|")}</div>;
}
const add = (s: string, t: string) => act(() => useSendQueueStore.getState().add(s, t, []));
const flush = () => act(async () => { await Promise.resolve(); await Promise.resolve(); });

beforeEach(() => {
  useSendQueueStore.setState({ bySession: {}, paused: {} });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("useSendQueue drain", () => {
  it("sends the head once on idle and holds the rest while the session is busy", async () => {
    const send = vi.fn(async () => true);
    add("s", "one"); add("s", "two");
    const { rerender } = render(<Harness session="s" busy={true} send={send} />);
    expect(send).not.toHaveBeenCalled();
    rerender(<Harness session="s" busy={false} send={send} />);
    await flush();
    expect(send).toHaveBeenCalledTimes(1);
    expect(send.mock.calls[0][0].text).toBe("one");
    // Status polls idle again before the turn registered: the second item must NOT follow yet.
    rerender(<Harness session="s" busy={false} canSend={false} send={send} />);
    rerender(<Harness session="s" busy={false} send={send} />);
    await flush();
    expect(send).toHaveBeenCalledTimes(1);
    // Busy seen, then idle again: now the second goes, exactly once.
    rerender(<Harness session="s" busy={true} send={send} />);
    rerender(<Harness session="s" busy={false} send={send} />);
    await flush();
    expect(send).toHaveBeenCalledTimes(2);
    expect(send.mock.calls[1][0].text).toBe("two");
    expect(text("n")).toBe("");
  });

  it("puts a refused item back at the head and pauses instead of retrying", async () => {
    const send = vi.fn(async () => false);
    add("s", "one"); add("s", "two");
    render(<Harness session="s" busy={false} send={send} />);
    await flush();
    await flush();
    expect(send).toHaveBeenCalledTimes(1);
    expect(text("n")).toBe("one|two");
    expect(useSendQueueStore.getState().paused.s).toBe(true);
  });

  it("does not drain while paused by a stop", async () => {
    const send = vi.fn(async () => true);
    add("s", "one");
    act(() => useSendQueueStore.getState().pauseIfHolding("s"));
    render(<Harness session="s" busy={false} send={send} />);
    await flush();
    expect(send).not.toHaveBeenCalled();
  });

  it("keeps each session's items across a switch and drains only the visible one", async () => {
    const send = vi.fn(async () => true);
    add("a", "for-a"); add("b", "for-b");
    const { rerender } = render(<Harness session="a" busy={true} send={send} />);
    rerender(<Harness session="b" busy={true} send={send} />);
    expect(text("n")).toBe("for-b");
    rerender(<Harness session="a" busy={true} send={send} />);
    expect(text("n")).toBe("for-a");
    rerender(<Harness session="a" busy={false} send={send} />);
    await flush();
    expect(send).toHaveBeenCalledTimes(1);
    expect(send.mock.calls[0][0].text).toBe("for-a");
    expect(useSendQueueStore.getState().bySession.b?.[0].text).toBe("for-b");
  });

  it("uses the latest sender, not the one from the render that scheduled the effect", async () => {
    const first = vi.fn(async () => true);
    const second = vi.fn(async () => true);
    add("s", "one");
    const { rerender } = render(<Harness session="s" busy={true} send={first} />);
    rerender(<Harness session="s" busy={true} send={second} />);
    rerender(<Harness session="s" busy={false} send={second} />);
    await flush();
    expect(first).not.toHaveBeenCalled();
    expect(second).toHaveBeenCalledTimes(1);
  });
});

describe("SendQueueList", () => {
  const items: QueuedSend[] = [{ id: "a", text: "first", paths: [] }, { id: "b", text: "second", paths: ["/x"] }];
  const props = () => ({
    items, paused: false, injects: false, onEdit: vi.fn(), onRemove: vi.fn(), onMove: vi.fn(), onSendNow: vi.fn(), onResume: vi.fn(),
  });

  it("wires reorder, send-now and delete to the right item", () => {
    const p = props();
    render(<SendQueueList {...p} />);
    click(buttons(/Move down|下へ/)[0]);
    expect(p.onMove).toHaveBeenCalledWith("a", 1);
    click(buttons(/Send now|今すぐ送る/)[1]);
    expect(p.onSendNow).toHaveBeenCalledWith("b");
    click(buttons(/Delete|削除/)[0]);
    expect(p.onRemove).toHaveBeenCalledWith("a");
  });

  it("does not save an edit on Enter while an IME composition is open", () => {
    const p = props();
    render(<SendQueueList {...p} />);
    click(buttons(/Edit|編集/)[0]);
    const box = host!.querySelector("textarea")!;
    expect(box.value).toBe("first");
    typeInto(box, "にほん");
    key(box, { key: "Enter", isComposing: true, keyCode: 229 });
    expect(p.onEdit).not.toHaveBeenCalled();
    key(box, { key: "Enter" });
    expect(p.onEdit).toHaveBeenCalledWith("a", "にほん");
  });

  it("offers resume only when paused", () => {
    const p = { ...props(), paused: true };
    render(<SendQueueList {...p} />);
    click(buttons(/Resume|再開/)[0]);
    expect(p.onResume).toHaveBeenCalled();
  });
});
