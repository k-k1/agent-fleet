// The plan card's "review in another session" button. What is pinned here is when it may
// be pressed at all: it REJECTS the plan on the way to the launch dialog, so it appearing
// on a plan that is no longer awaiting approval, or on a session nothing can reach, would
// throw away a decision the user did not make.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

import { PlanBlock } from "./blocks.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function mount(props: Parameters<typeof PlanBlock>[0]) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root!.render(<PlanBlock {...props} />));
  return host;
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

const reviewBtn = (el: HTMLElement) => el.querySelector<HTMLButtonElement>(".mt-plan-review");

const base = {
  plan: "# 移行計画\n\n本文",
  session: "planner-x",
  onOpen: () => {},
  onApprove: () => {},
  onReject: () => {},
};

describe("PlanBlock review button", () => {
  it("is offered while the plan awaits approval", () => {
    const onReview = vi.fn();
    const el = mount({ ...base, pending: true, onReview });
    const btn = reviewBtn(el);
    expect(btn).not.toBeNull();
    act(() => btn!.click());
    expect(onReview).toHaveBeenCalledTimes(1);
  });

  it("is absent once the plan has been decided", () => {
    // A historical card renders the same component with pending unset. Rejecting there
    // would interrupt whatever the session is doing now.
    const el = mount({ ...base, answered: true, outcome: "User has approved your plan.", onReview: vi.fn() });
    expect(reviewBtn(el)).toBeNull();
  });

  it("refuses to fire on a session that cannot be reached", () => {
    const onReview = vi.fn();
    const el = mount({ ...base, pending: true, sendDisabled: "session stopped", onReview });
    const btn = reviewBtn(el)!;
    expect(btn.disabled).toBe(true);
    act(() => btn.click());
    expect(onReview).not.toHaveBeenCalled();
  });
});
