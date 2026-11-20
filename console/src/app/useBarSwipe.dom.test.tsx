// The activity bar's vertical swipe, end to end through the component: steps views, persists
// via showView, never also fires a tap, and stays out of the way of an overflowing column.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const { api } = vi.hoisted(() => ({ api: vi.fn((..._args: unknown[]) => Promise.resolve({})) }));
vi.mock("../core/api/client.ts", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  api: (...args: unknown[]) => api(...args),
}));

import { ActivityBar } from "./ActivityBar.tsx";
import { useLeftRail } from "../core/store/leftRail.ts";
import type { RailView } from "../core/store/leftRail.ts";
import { BAR_STEP_DIST, BAR_SWIPE_UP_IS_NEXT } from "./barSwipe.ts";
import { LONG_PRESS_MS } from "./swipeGestures.ts";

// Past the 400ms click-swallow window.
const CLICK_WAIT = 500;

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | null = null;
let host: HTMLDivElement | null = null;

function touch(type: string, target: Element, x: number, y: number, extra: [number, number][] = []) {
  const e = new Event(type, { bubbles: true, cancelable: true });
  const all = type === "touchend" || type === "touchcancel" ? [] : [{ clientX: x, clientY: y }, ...extra.map(([a, b]) => ({ clientX: a, clientY: b }))];
  Object.defineProperty(e, "touches", { value: all });
  target.dispatchEvent(e);
}
const bar = () => host!.querySelector<HTMLElement>(".actbar")!;
const btn = (v: RailView) => host!.querySelector<HTMLButtonElement>(`.actbar-btn[data-rail-view="${v}"]`)!;
// "toward next": up when BAR_SWIPE_UP_IS_NEXT, otherwise down.
const toward = (next: boolean) => (next === BAR_SWIPE_UP_IS_NEXT ? -1 : 1) * (BAR_STEP_DIST + 10);

async function swipe(from: Element, next: boolean, dx = 0) {
  await act(async () => {
    touch("touchstart", from, 20, 200);
    touch("touchmove", from, 20 + dx, 200 + toward(next));
    touch("touchend", from, 20 + dx, 200 + toward(next));
  });
}

type BarProps = Partial<Parameters<typeof ActivityBar>[0]>;
const Shell = (props: BarProps) => {
  const view = useLeftRail((s) => s.view);
  return <ActivityBar view={view} shown foldable={false} schedulerEnabled {...props} />;
};
// Re-render the same Shell (same element type), so the bar and its listeners stay mounted.
const rerender = (props: BarProps) => act(async () => root!.render(<Shell {...props} />));
async function mount(props: BarProps = {}) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await rerender(props);
}

beforeEach(() => {
  localStorage.clear();
  useLeftRail.setState({ open: true, view: "assistant", swipeOverlay: false });
});
afterEach(async () => {
  vi.useRealTimers();
  if (root) await act(async () => root!.unmount());
  host?.remove();
  root = host = null;
});

describe("activity bar swipe", () => {
  it("steps to the next and previous view and persists the choice", async () => {
    await mount();
    await swipe(bar(), true);
    expect(useLeftRail.getState().view).toBe("workitems");
    expect(localStorage.getItem("af-left-view")).toBe("workitems");
    expect(btn("workitems").getAttribute("aria-pressed")).toBe("true");
    await swipe(bar(), false);
    expect(useLeftRail.getState().view).toBe("assistant");
  });

  it("takes one step per gesture, however far the finger goes", async () => {
    await mount();
    await act(async () => {
      touch("touchstart", bar(), 20, 300);
      touch("touchmove", bar(), 20, 300 + toward(true));
      touch("touchmove", bar(), 20, 300 + toward(true) * 4);
    });
    expect(useLeftRail.getState().view).toBe("workitems");
  });

  it("clamps at the ends", async () => {
    useLeftRail.setState({ view: "files" });
    await mount();
    await swipe(bar(), true);
    expect(useLeftRail.getState().view).toBe("files");
  });

  it("skips Schedules when the scheduler is off", async () => {
    useLeftRail.setState({ view: "memory" });
    await mount({ schedulerEnabled: false });
    await swipe(bar(), true);
    expect(useLeftRail.getState().view).toBe("files");
  });

  it("starts from a launcher button too, but never targets it", async () => {
    await mount();
    const home = host!.querySelector<HTMLElement>('[data-launch="home"]')!;
    await swipe(home, true);
    expect(useLeftRail.getState().view).toBe("workitems");
  });

  it("does not fire the tap of the button the swipe started on", async () => {
    await mount();
    const onClick = vi.fn();
    host!.addEventListener("click", onClick);
    const files = btn("files");
    await swipe(files, true);
    await act(async () => files.click()); // the click a browser would add after touchend
    expect(onClick).not.toHaveBeenCalled();
    expect(useLeftRail.getState().view).toBe("workitems");
  });

  it("lets the next, unrelated tap through (suppression does not leak)", async () => {
    await mount();
    await swipe(btn("files"), true);
    await act(async () => touch("touchstart", btn("memo"), 20, 100));
    await act(async () => btn("memo").click());
    expect(useLeftRail.getState().view).toBe("memo");
  });

  it("lets a late click through once the swallow window has passed", async () => {
    vi.useFakeTimers();
    await mount();
    await swipe(btn("files"), true);
    vi.advanceTimersByTime(1000);
    await act(async () => btn("memo").click());
    expect(useLeftRail.getState().view).toBe("memo");
  });

  it("still taps normally", async () => {
    await mount();
    await act(async () => {
      touch("touchstart", btn("files"), 20, 200);
      touch("touchmove", btn("files"), 21, 203);
      touch("touchend", btn("files"), 21, 203);
      btn("files").click();
    });
    expect(useLeftRail.getState().view).toBe("files");
  });

  it("leaves an overflowing column to native scrolling", async () => {
    await mount();
    Object.defineProperty(bar(), "scrollHeight", { value: 500, configurable: true });
    Object.defineProperty(bar(), "clientHeight", { value: 300, configurable: true });
    await swipe(bar(), true);
    expect(useLeftRail.getState().view).toBe("assistant");
  });

  it("ignores a mostly horizontal drag", async () => {
    await mount();
    await swipe(bar(), true, 80);
    expect(useLeftRail.getState().view).toBe("assistant");
  });

  it("is off when the bar is not on a phone drawer (foldable) or the drawer is closed", async () => {
    await mount({ foldable: true });
    await swipe(bar(), true);
    expect(useLeftRail.getState().view).toBe("assistant");
    await act(async () => root!.unmount());
    host!.remove();
    root = host = null;
    await mount({ shown: false });
    await swipe(bar(), true);
    expect(useLeftRail.getState().view).toBe("assistant");
  });

  it("drops the candidate on a second finger or after the long-press window", async () => {
    vi.useFakeTimers();
    await mount();
    await act(async () => {
      touch("touchstart", bar(), 20, 300);
      touch("touchmove", bar(), 20, 300 + toward(true), [[200, 200]]);
    });
    expect(useLeftRail.getState().view).toBe("assistant");
    await act(async () => {
      touch("touchstart", bar(), 20, 300);
      vi.advanceTimersByTime(LONG_PRESS_MS + 10);
      touch("touchmove", bar(), 20, 300 + toward(true));
    });
    expect(useLeftRail.getState().view).toBe("assistant");
  });

  it("marks a fitting column for touch-action and announces the new view", async () => {
    await mount();
    expect(bar().hasAttribute("data-fits")).toBe(true);
    await swipe(bar(), true);
    expect(host!.querySelector(".actbar-live")!.textContent).not.toBe("");
  });

  it("does not swallow a mouse click that lands after a committed swipe", async () => {
    await mount();
    await swipe(btn("files"), true);
    const ev = new MouseEvent("click", { bubbles: true, cancelable: true });
    Object.defineProperty(ev, "pointerType", { value: "mouse" });
    await act(async () => btn("memo").dispatchEvent(ev));
    expect(useLeftRail.getState().view).toBe("memo");
  });

  it("does not swallow a click once the gesture is off (width grew past a phone)", async () => {
    await mount();
    await swipe(btn("files"), true);
    await rerender({ foldable: true });
    await act(async () => btn("memo").click());
    expect(useLeftRail.getState().view).toBe("memo");
  });

  it("releases the suppression when the swipe's start button is removed mid-gesture", async () => {
    vi.useFakeTimers();
    await mount({ schedulerEnabled: true });
    const sched = btn("schedules");
    await act(async () => {
      touch("touchstart", sched, 20, 300);
      touch("touchmove", sched, 20, 300 + toward(true));
    });
    await rerender({ schedulerEnabled: false });
    expect(sched.isConnected).toBe(false);
    // The detached button's touchend does not bubble to the bar.
    await act(async () => touch("touchend", sched, 0, 0));
    vi.advanceTimersByTime(CLICK_WAIT);
    await act(async () => btn("memo").click());
    expect(useLeftRail.getState().view).toBe("memo");
  });

  it("releases the suppression on touchcancel", async () => {
    await mount();
    await act(async () => {
      touch("touchstart", bar(), 20, 300);
      touch("touchmove", bar(), 20, 300 + toward(true));
      touch("touchcancel", bar(), 0, 0);
    });
    await act(async () => btn("memo").click());
    expect(useLeftRail.getState().view).toBe("memo");
  });

  it("does nothing while the page is pinch-zoomed", async () => {
    await mount();
    const vv = { width: window.innerWidth / 2 } as VisualViewport;
    Object.defineProperty(window, "visualViewport", { value: vv, configurable: true });
    await swipe(bar(), true);
    delete (window as { visualViewport?: unknown }).visualViewport;
    expect(useLeftRail.getState().view).toBe("assistant");
  });

  it("only marks the column for touch-action while the gesture is live and the column fits", async () => {
    await mount({ foldable: true });
    expect(bar().hasAttribute("data-fits")).toBe(false);
    await act(async () => root!.unmount());
    host!.remove();
    root = host = null;
    await mount({ shown: false });
    expect(bar().hasAttribute("data-fits")).toBe(false);
    await act(async () => root!.unmount());
    host!.remove();
    root = host = null;
    await mount();
    expect(bar().hasAttribute("data-fits")).toBe(true);
    Object.defineProperty(bar(), "scrollHeight", { value: 500, configurable: true });
    Object.defineProperty(bar(), "clientHeight", { value: 300, configurable: true });
    await act(async () => window.dispatchEvent(new Event("resize")));
    expect(bar().hasAttribute("data-fits")).toBe(false);
  });

  it("re-announces a repeat swipe to the same view and clears the text afterwards", async () => {
    vi.useFakeTimers();
    await mount();
    const live = () => host!.querySelector(".actbar-live")!.textContent;
    await swipe(bar(), true);
    expect(live()).not.toBe("");
    await act(async () => vi.advanceTimersByTime(1100));
    expect(live()).toBe("");
    await act(async () => btn("sessions").click());
    await swipe(bar(), true);
    expect(live()).not.toBe("");
    await act(async () => vi.advanceTimersByTime(CLICK_WAIT));
    await act(async () => btn("files").click());
    expect(live()).toBe("");
  });
});
