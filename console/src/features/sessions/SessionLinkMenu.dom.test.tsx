// The session context menu on the session slugs a Markdown body auto-links (issue #1553).
//
// Pinned here: the menu is opt-in by surface (outside a SessionLinkMenuHost the browser's own
// menu stays), every input reaches it (right click, long press for iOS, which never fires
// contextmenu, and the Menu key), the lift of a long press does not also open the session, and a
// session deleted after the document rendered toasts instead of opening a menu for nothing.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Session } from "../../types/session.ts";

const toasts: unknown[] = [];
// Stable, like the real context value: a new identity per render would re-run MarkdownView's
// render effect and rebuild the very link a test is pressing.
const toast = (m: unknown) => toasts.push(m);
vi.mock("../../ui/ToastProvider.tsx", () => ({
  useToast: () => toast,
}));
const opened: { ref: string; openInNew?: boolean }[] = [];
vi.mock("./open.ts", () => ({
  openSessionChat: (name: string) => opened.push({ ref: name }),
  openSessionChatSplit: (name: string) => opened.push({ ref: name, openInNew: true }),
  openSessionTerminal: () => {},
}));
// Nothing here presses an action item; the menu only needs the object to exist.
vi.mock("./useSessionActions.tsx", () => ({
  useSessionActions: () => new Proxy({}, { get: () => async () => {} }),
}));

const { MarkdownView } = await import("../viewer/MarkdownView.tsx");
const { SessionLinkMenuHost } = await import("./SessionLinkMenu.tsx");
const { useSessionsStore } = await import("./store.ts");
const { t } = await import("../../lib/i18n/index.ts");

const session = (name: string): Session => ({ name, kind: "claude", alive: true, state: "idle" }) as Session;

let host: HTMLDivElement;
let root: Root;

const render = async (withHost: boolean) => {
  const body = <MarkdownView source="子は sukbq4s で動いている。" />;
  await act(async () => {
    root.render(withHost ? <SessionLinkMenuHost>{body}</SessionLinkMenuHost> : body);
  });
};
const link = () => host.querySelector<HTMLAnchorElement>("a.md-session-link")!;
const menu = () => document.querySelector(".sess-menu");

const fire = (el: HTMLElement, e: Event) => {
  act(() => {
    el.dispatchEvent(e);
  });
  return e;
};
const rightClick = (el: HTMLElement) =>
  fire(el, new MouseEvent("contextmenu", { bubbles: true, cancelable: true, button: 2, clientX: 40, clientY: 50 }));
const click = (el: HTMLElement) => fire(el, new MouseEvent("click", { bubbles: true, cancelable: true }));
// jsdom has no TouchEvent constructor, so build the bare minimum: the touch coordinates.
const touch = (el: HTMLElement, type: string, x = 10, y = 10) => {
  const e = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(e, "touches", { value: type === "touchend" ? [] : [{ clientX: x, clientY: y }] });
  return fire(el, e);
};

beforeEach(() => {
  toasts.length = 0;
  opened.length = 0;
  useSessionsStore.setState({ sessions: [session("sukbq4s")] });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  vi.useRealTimers();
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

describe("session link context menu", () => {
  it("opens the session menu on a right click inside a host, and does not open the session", async () => {
    await render(true);
    const e = rightClick(link());
    expect(e.defaultPrevented).toBe(true);
    expect(menu()).not.toBeNull();
    expect(opened).toHaveLength(0);
  });

  it("leaves the browser's own menu alone outside a host", async () => {
    await render(false);
    const e = rightClick(link());
    expect(e.defaultPrevented).toBe(false);
    expect(menu()).toBeNull();
    expect(link().classList.contains("md-has-menu")).toBe(false);
    // The click path is unchanged.
    click(link());
    expect(opened).toEqual([{ ref: "sukbq4s" }]);
  });

  it("opens on a long press and swallows the click of that lift only", async () => {
    vi.useFakeTimers();
    await render(true);
    touch(link(), "touchstart");
    act(() => {
      vi.advanceTimersByTime(500);
    });
    expect(menu()).not.toBeNull();
    const end = touch(link(), "touchend");
    expect(end.defaultPrevented).toBe(true); // no compatibility mousedown to dismiss the menu
    click(link());
    expect(opened).toHaveLength(0);
    // The next tap is an ordinary open again.
    touch(link(), "touchstart");
    touch(link(), "touchend");
    click(link());
    expect(opened).toEqual([{ ref: "sukbq4s" }]);
  });

  it("does not open when the finger moves (a scroll that started on the link)", async () => {
    vi.useFakeTimers();
    await render(true);
    touch(link(), "touchstart", 10, 10);
    touch(link(), "touchmove", 12, 60);
    act(() => {
      vi.advanceTimersByTime(500);
    });
    expect(menu()).toBeNull();
  });

  it("does not eat the next left click after a mouse right click", async () => {
    await render(true);
    rightClick(link());
    fire(link(), new MouseEvent("mousedown", { bubbles: true }));
    click(link());
    expect(opened).toEqual([{ ref: "sukbq4s" }]);
  });

  it("opens from the Menu key on a focused link", async () => {
    await render(true);
    const e = fire(link(), new KeyboardEvent("keydown", { key: "ContextMenu", bubbles: true, cancelable: true }));
    expect(e.defaultPrevented).toBe(true);
    expect(menu()).not.toBeNull();
  });

  it("toasts instead of opening a menu when the session was deleted after the render", async () => {
    await render(true);
    useSessionsStore.setState({ sessions: [] });
    rightClick(link());
    expect(menu()).toBeNull();
    expect(toasts).toEqual([t("view.session_not_found", { name: "sukbq4s" })]);
  });
});
