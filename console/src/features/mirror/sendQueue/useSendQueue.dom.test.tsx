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

function Harness({ session, busy, canSend = true, send, list = false }: { session: string; busy: boolean; canSend?: boolean; send: (i: QueuedSend) => Promise<boolean>; list?: boolean }) {
  const q = useSendQueue({ session, busy, canSend, sendItem: send });
  return (
    <div>
      <div data-testid="n">{q.items.map((i) => i.text).join("|")}</div>
      {list && (
        <SendQueueList items={q.items} paused={q.paused} injects={false} onEdit={q.edit} onRemove={q.remove} onMove={q.move} onSendNow={q.sendNow} onResume={q.resume} onEditing={q.setEditing} />
      )}
    </div>
  );
}
const add = (s: string, t: string) => act(() => useSendQueueStore.getState().add(s, t, []));
const flush = () => act(async () => { await Promise.resolve(); await Promise.resolve(); });
const polledBusy = (s: string) => act(() => useSendQueueStore.getState().observeBusy(s));
const never = () => new Promise<boolean>(() => {});

beforeEach(() => {
  useSendQueueStore.setState({ bySession: {}, paused: {}, gate: {}, editing: {} });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("useSendQueue drain", () => {
  it("sends the head once on idle, then waits for the POLL to show busy before the next", async () => {
    const send = vi.fn(async (_i: QueuedSend) => true);
    add("s", "one"); add("s", "two");
    const { rerender } = render(<Harness session="s" busy={true} send={send} />);
    expect(send).not.toHaveBeenCalled();
    rerender(<Harness session="s" busy={false} send={send} />);
    await flush();
    expect(send).toHaveBeenCalledTimes(1);
    expect(send.mock.calls[0][0].text).toBe("one");
    // sendPrompt's optimistic "working", then a stale idle poll: the real turn was never observed.
    rerender(<Harness session="s" busy={true} canSend={false} send={send} />);
    rerender(<Harness session="s" busy={false} send={send} />);
    await flush();
    expect(send).toHaveBeenCalledTimes(1);
    polledBusy("s");
    rerender(<Harness session="s" busy={true} send={send} />);
    rerender(<Harness session="s" busy={false} send={send} />);
    await flush();
    expect(send).toHaveBeenCalledTimes(2);
    expect(send.mock.calls[1][0].text).toBe("two");
    expect(text("n")).toBe("");
  });

  it("sends one item in total when the same session is mounted twice", async () => {
    const send = vi.fn((_i: QueuedSend) => never());
    add("s", "one"); add("s", "two");
    const a = document.createElement("div");
    document.body.appendChild(a);
    const second = createRoot(a);
    act(() => second.render(<Harness session="s" busy={false} send={send} />));
    render(<Harness session="s" busy={false} send={send} />);
    await flush();
    expect(send).toHaveBeenCalledTimes(1);
    act(() => second.unmount());
    a.remove();
  });

  it("does not send the next item after a remount or a switch away and back while one is in flight", async () => {
    const send = vi.fn((_i: QueuedSend) => never());
    add("s", "one"); add("s", "two");
    const { rerender } = render(<Harness key="1" session="s" busy={false} send={send} />);
    await flush();
    expect(send).toHaveBeenCalledTimes(1);
    rerender(<Harness key="2" session="s" busy={false} send={send} />);
    rerender(<Harness key="2" session="other" busy={false} send={send} />);
    rerender(<Harness key="3" session="s" busy={false} send={send} />);
    await flush();
    expect(send).toHaveBeenCalledTimes(1);
  });

  it("does not release the second item within the settle window after a remount", async () => {
    const send = vi.fn(async (_i: QueuedSend) => true);
    add("s", "one"); add("s", "two");
    const { rerender } = render(<Harness key="1" session="s" busy={false} send={send} />);
    await flush();
    rerender(<Harness key="2" session="s" busy={false} send={send} />);
    await flush();
    expect(send).toHaveBeenCalledTimes(1);
  });

  it("puts a refused item back at the head and pauses instead of retrying", async () => {
    const send = vi.fn(async (_i: QueuedSend) => false);
    add("s", "one"); add("s", "two");
    render(<Harness session="s" busy={false} send={send} />);
    await flush();
    await flush();
    expect(send).toHaveBeenCalledTimes(1);
    expect(text("n")).toBe("one|two");
    expect(useSendQueueStore.getState().paused.s).toBe(true);
  });

  it("pauses after a refused send now too, so the turn's end does not resend it", async () => {
    const send = vi.fn(async (_i: QueuedSend) => false);
    add("s", "one");
    const { rerender } = render(<Harness session="s" busy={true} send={send} list />);
    click(buttons(/Send now|今すぐ送る/)[0]);
    await flush();
    expect(send).toHaveBeenCalledTimes(1);
    expect(text("n")).toBe("one");
    expect(useSendQueueStore.getState().paused.s).toBe(true);
    rerender(<Harness session="s" busy={false} send={send} list />);
    await flush();
    expect(send).toHaveBeenCalledTimes(1);
  });

  it("does not drain while paused by a stop", async () => {
    const send = vi.fn(async (_i: QueuedSend) => true);
    add("s", "one");
    act(() => useSendQueueStore.getState().pauseIfHolding("s"));
    render(<Harness session="s" busy={false} send={send} />);
    await flush();
    expect(send).not.toHaveBeenCalled();
  });

  it("holds the drain while a row is being edited, and sends the edited text after saving", async () => {
    const send = vi.fn(async (_i: QueuedSend) => true);
    add("s", "old");
    const { rerender } = render(<Harness session="s" busy={true} send={send} list />);
    click(buttons(/Edit|編集/)[0]);
    typeInto(host!.querySelector("textarea")!, "new instruction");
    rerender(<Harness session="s" busy={false} send={send} list />);
    await flush();
    expect(send).not.toHaveBeenCalled();
    expect(host!.querySelector("textarea")!.value).toBe("new instruction");
    key(host!.querySelector("textarea")!, { key: "Enter" });
    await flush();
    expect(send).toHaveBeenCalledTimes(1);
    expect(send.mock.calls[0][0].text).toBe("new instruction");
  });

  it("drops the edit lock when the list unmounts mid-edit", async () => {
    add("s", "old");
    const { rerender } = render(<Harness session="s" busy={true} send={vi.fn(async (_i: QueuedSend) => true)} list />);
    click(buttons(/Edit|編集/)[0]);
    expect(useSendQueueStore.getState().editing.s).toBe(useSendQueueStore.getState().bySession.s![0].id);
    rerender(<Harness session="s" busy={true} send={vi.fn(async (_i: QueuedSend) => true)} />);
    expect(useSendQueueStore.getState().editing.s).toBeUndefined();
  });

  it("keeps each session's items across a switch and drains only the visible one", async () => {
    const send = vi.fn(async (_i: QueuedSend) => true);
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
    const first = vi.fn(async (_i: QueuedSend) => true);
    const second = vi.fn(async (_i: QueuedSend) => true);
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
    items, paused: false, injects: false, onEdit: vi.fn(), onRemove: vi.fn(), onMove: vi.fn(), onSendNow: vi.fn(), onResume: vi.fn(), onEditing: vi.fn(),
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
