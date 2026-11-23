// The activity bar's context menu (ADR 0142 decision 11): how it opens, what it lists and locks,
// that the choice reaches the settings and the swipe, and the dot for a hidden view that waits.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const { api } = vi.hoisted(() => ({ api: vi.fn((..._args: unknown[]) => Promise.resolve({})) }));
vi.mock("../core/api/client.ts", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  api: (...args: unknown[]) => api(...args),
  memoList: () => Promise.resolve([]),
}));

import { ActivityBar } from "./ActivityBar.tsx";
import { useLeftRail } from "../core/store/leftRail.ts";
import type { RailView } from "../core/store/leftRail.ts";
import { useSessionsStore } from "../features/sessions/store.ts";
import { useNotificationStore } from "../features/notifications/store.ts";
import { useHandoffStore } from "../features/sharing/handoffStore.ts";
import type { HandoffOffer } from "../features/sharing/handoffStore.ts";
import { getSettings, setSettings } from "../lib/settings.ts";
import { LONG_PRESS_MS } from "../lib/longPress.ts";
import { BAR_STEP_DIST, BAR_SWIPE_UP_IS_NEXT } from "./barSwipe.ts";
import type { Session } from "../types/session.ts";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | null = null;
let host: HTMLDivElement | null = null;

beforeEach(() => {
  localStorage.clear();
  setSettings({ railViewsOff: [] });
  useLeftRail.setState({ open: true, view: "sessions", swipeOverlay: false });
  useSessionsStore.setState({ sessions: [] });
  useNotificationStore.setState({ items: [] });
  useHandoffStore.setState({ owned: [], received: [], receivedAt: 0 });
  api.mockImplementation(() => Promise.resolve({}));
});
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  host?.remove();
  root = host = null;
  document.querySelectorAll(".actbar-menu").forEach((m) => m.remove());
  vi.useRealTimers();
});

type Props = Partial<Parameters<typeof ActivityBar>[0]>;
const Shell = (props: Props) => {
  const view = useLeftRail((s) => s.view);
  return <ActivityBar view={view} shown foldable={false} schedulerEnabled {...props} />;
};
async function mount(props: Props = {}) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<Shell {...props} />));
}
const bar = () => host!.querySelector<HTMLElement>(".actbar")!;
const icons = () => [...host!.querySelectorAll<HTMLElement>(".actbar-btn[data-rail-view]")].map((b) => b.dataset.railView);
const btn = (v: RailView) => host!.querySelector<HTMLButtonElement>(`.actbar-btn[data-rail-view="${v}"]`)!;
const menu = () => document.querySelector<HTMLElement>(".actbar-menu");
const row = (v: RailView) => menu()!.querySelector<HTMLButtonElement>(`[data-menu-view="${v}"]`)!;
const rightClick = (el: Element) => act(async () => void el.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 30, clientY: 80 })));

describe("opening the menu", () => {
  it("opens on a right-click on the bar and lists every view with a check for the shown ones", async () => {
    await mount();
    await rightClick(btn("memo"));
    expect(menu()).not.toBeNull();
    const rows = [...menu()!.querySelectorAll<HTMLElement>("[data-menu-view]")].map((r) => r.dataset.menuView);
    expect(rows).toEqual(["sessions", "shared", "assistant", "workitems", "memo", "autorun", "memory", "schedules", "files"]);
    expect(row("memo").getAttribute("aria-checked")).toBe("true");
    expect(row("memo").getAttribute("role")).toBe("menuitemcheckbox");
  });

  it("opens on the context-menu key, anchored on the focused icon, and puts focus back on close", async () => {
    await mount();
    const b = btn("files");
    b.focus();
    await act(async () => void b.dispatchEvent(new KeyboardEvent("keydown", { key: "ContextMenu", bubbles: true, cancelable: true })));
    expect(menu()).not.toBeNull();
    await act(async () => void document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    expect(menu()).toBeNull();
    expect(document.activeElement).toBe(b);
  });

  it("opens on a long press, and the click that follows the lift does not pick an icon", async () => {
    vi.useFakeTimers();
    await mount();
    const target = btn("files");
    const touch = (type: string, x: number, y: number) => {
      const e = new Event(type, { bubbles: true, cancelable: true });
      Object.defineProperty(e, "touches", { value: type === "touchend" ? [] : [{ clientX: x, clientY: y }] });
      target.dispatchEvent(e);
    };
    await act(async () => {
      target.dispatchEvent(new Event("pointerdown", { bubbles: true }));
      Object.defineProperty(Event.prototype, "pointerType", { value: "touch", configurable: true });
      touch("touchstart", 20, 200);
      vi.advanceTimersByTime(LONG_PRESS_MS + 20);
    });
    delete (Event.prototype as unknown as Record<string, unknown>).pointerType;
    expect(menu()).not.toBeNull();
    await act(async () => {
      touch("touchend", 20, 200);
      target.click();
    });
    expect(useLeftRail.getState().view).toBe("sessions");
  });
});

describe("turning icons off", () => {
  it("hides the icon, stores the choice with the settings, and reset brings it back", async () => {
    await mount();
    await rightClick(bar());
    await act(async () => row("memory").click());
    expect(icons()).not.toContain("memory");
    expect(getSettings().railViewsOff).toEqual(["memory"]);
    expect(row("memory").getAttribute("aria-checked")).toBe("false");
    await act(async () => menu()!.querySelector<HTMLButtonElement>("[data-menu-reset]")!.click());
    expect(icons()).toContain("memory");
    expect(getSettings().railViewsOff).toEqual([]);
  });

  it("turns an icon back on from the menu", async () => {
    setSettings({ railViewsOff: ["files"] });
    await mount();
    expect(icons()).not.toContain("files");
    await rightClick(bar());
    await act(async () => row("files").click());
    expect(icons()).toContain("files");
  });

  it("cannot turn off the view on screen, and says why", async () => {
    await mount();
    await rightClick(bar());
    expect(row("sessions").getAttribute("aria-disabled")).toBe("true");
    await act(async () => row("sessions").click());
    expect(getSettings().railViewsOff).toEqual([]);
    expect(row("sessions").textContent).toContain("on screen");
  });

  it("keeps the icon of the view on screen even when it is stored as off", async () => {
    setSettings({ railViewsOff: ["sessions", "files"] });
    await mount();
    expect(icons()).toContain("sessions");
    expect(icons()).not.toContain("files");
  });

  it("cannot turn off the last icon", async () => {
    // Every icon but one is off, and the screen is on a view with no icon left to keep.
    const others = ["shared", "assistant", "workitems", "memo", "autorun", "memory", "schedules", "files"];
    setSettings({ railViewsOff: others });
    useLeftRail.setState({ view: "sessions" });
    await mount();
    expect(icons()).toEqual(["sessions"]);
    await rightClick(bar());
    expect(row("sessions").getAttribute("aria-disabled")).toBe("true");
    await act(async () => row("files").click());
    expect(icons()).toEqual(["sessions", "files"]);
  });

  it("lists Schedules greyed out with the reason where there is no scheduler", async () => {
    await mount({ schedulerEnabled: false });
    expect(icons()).not.toContain("schedules");
    await rightClick(bar());
    expect(row("schedules").getAttribute("aria-disabled")).toBe("true");
    expect(row("schedules").getAttribute("aria-checked")).toBe("false");
    expect(row("schedules").textContent).toContain("no scheduler");
    await act(async () => row("schedules").click());
    expect(getSettings().railViewsOff).toEqual([]);
  });

  it("offers no reset while nothing is off", async () => {
    await mount();
    await rightClick(bar());
    expect(menu()!.querySelector("[data-menu-reset]")!.getAttribute("aria-disabled")).toBe("true");
  });
});

describe("the vertical swipe follows the shown icons only", () => {
  it("steps over an icon that is off", async () => {
    setSettings({ railViewsOff: ["shared"] });
    useLeftRail.setState({ view: "sessions" });
    await mount();
    const next = BAR_SWIPE_UP_IS_NEXT ? -1 : 1;
    const target = btn("sessions");
    const touch = (type: string, y: number) => {
      const e = new Event(type, { bubbles: true, cancelable: true });
      Object.defineProperty(e, "touches", { value: type === "touchend" ? [] : [{ clientX: 20, clientY: y }] });
      target.dispatchEvent(e);
    };
    await act(async () => {
      touch("touchstart", 200);
      touch("touchmove", 200 + next * (BAR_STEP_DIST + 10));
      touch("touchend", 200 + next * (BAR_STEP_DIST + 10));
    });
    expect(useLeftRail.getState().view).toBe("assistant");
  });
});

describe("a hidden view that waits on the member", () => {
  const offer = (id: string) => ({ id, status: "pending" }) as unknown as HandoffOffer;
  const waitingSession = { name: "w", state: "question", alive: true, kind: "claude" } as unknown as Session;

  it("shows one dot on the bar and names the view in the menu", async () => {
    useHandoffStore.setState({ received: [offer("a"), offer("b")] });
    setSettings({ railViewsOff: ["shared"] });
    await mount();
    expect(icons()).not.toContain("shared");
    const dot = host!.querySelector<HTMLElement>("[data-hidden-waiting]")!;
    expect(dot.dataset.hiddenWaiting).toBe("shared");
    expect(dot.getAttribute("aria-label")).toContain("Shared sessions");
    await act(async () => dot.click());
    expect(menu()).not.toBeNull();
    expect(row("shared").textContent).toContain("2 handoffs waiting for your decision");
  });

  it("shows no dot for news or unread of a hidden view", async () => {
    useNotificationStore.setState({
      items: [{ seq: 1, id: "n", kind: "answer-ready", target: { type: "session", id: "s1" }, displayName: "", payload: {}, createdAt: "", seen: false }],
    });
    useSessionsStore.setState({ sessions: [{ name: "s1", state: "working", alive: true, kind: "claude" } as unknown as Session] });
    useLeftRail.setState({ view: "files" });
    setSettings({ railViewsOff: ["sessions", "assistant"] });
    await mount();
    expect(icons()).not.toContain("sessions");
    expect(host!.querySelector("[data-hidden-waiting]")).toBeNull();
  });

  it("does not repeat the badge on a view whose icon is shown", async () => {
    useSessionsStore.setState({ sessions: [waitingSession] });
    useLeftRail.setState({ view: "files" });
    await mount();
    expect(host!.querySelector("[data-hidden-waiting]")).toBeNull();
    expect(btn("sessions").querySelector(".actbar-badge")?.textContent).toBe("1");
  });

  it("has the dot for a session stopped on an answer when the Sessions icon is off", async () => {
    useSessionsStore.setState({ sessions: [waitingSession] });
    useLeftRail.setState({ view: "files" });
    setSettings({ railViewsOff: ["sessions"] });
    await mount();
    expect(host!.querySelector("[data-hidden-waiting]")?.getAttribute("data-hidden-waiting")).toBe("sessions");
  });
});
