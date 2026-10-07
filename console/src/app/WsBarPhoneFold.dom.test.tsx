// The phone bar's folded pane buttons (#1650) as WsBar renders them: with the bar too narrow they
// leave the bar for the ⋯ popover, and activating one by keyboard must not leave focus on <body>
// (the popover closes and unmounts the focused button). jsdom has no layout, so the bar's width
// and its children's widths are stubbed: this proves the wiring, not real pixel widths.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn((..._args: unknown[]) => Promise.resolve({}));
vi.mock("../core/api/client.ts", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  api: (...args: unknown[]) => api(...args),
}));
vi.mock("./usageResetNotify.ts", () => ({ useUsageResetNotify: () => {} }));

import { WsBar } from "./WsBar.tsx";
import { ConfirmProvider } from "../ui/ConfirmProvider.tsx";
import { ToastProvider } from "../ui/ToastProvider.tsx";
import { useWorkspaceStore } from "../core/store/workspace.ts";
import { t } from "../lib/i18n/index.ts";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
let root: Root | null = null;
let host: HTMLDivElement | null = null;
const origMatchMedia = window.matchMedia;

beforeEach(() => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  window.matchMedia = ((q: string) => ({
    matches: q.includes("max-width: 760px"),
    media: q,
    addEventListener() {},
    removeEventListener() {},
  })) as unknown as typeof window.matchMedia;
  // Every child is 100px wide on a 100px bar: nothing fits until every step has been taken.
  Object.defineProperty(HTMLElement.prototype, "clientWidth", { configurable: true, get: () => 100 });
  HTMLElement.prototype.getBoundingClientRect = () => ({ width: 100 }) as DOMRect;
  useWorkspaceStore.setState({ state: "running" });
});
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  host?.remove();
  root = host = null;
  window.matchMedia = origMatchMedia;
  delete (HTMLElement.prototype as { clientWidth?: number }).clientWidth;
  delete (HTMLElement.prototype as { getBoundingClientRect?: unknown }).getBoundingClientRect;
});

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(
    <ToastProvider>
      <ConfirmProvider>
        <WsBar />
      </ConfirmProvider>
    </ToastProvider>,
  ));
}

describe("WsBar phone fold", () => {
  it("moves the pane buttons into the ⋯ popover when the bar is too narrow", async () => {
    await mount();
    expect(host!.querySelector(".wsbar")!.getAttribute("data-fold-phone")).toBe("2");
    await act(async () => host!.querySelector<HTMLButtonElement>(".ws-more-btn")!.click());
    const labels = [...host!.querySelectorAll(".ws-more-actions button")].map((b) => b.getAttribute("aria-label"));
    expect(labels).toEqual([t("wsbar.split_down"), t("wsbar.close_all"), t("wsbar.overview")]);
  });

  it("hands focus back to the ⋯ trigger when a folded action closes the popover", async () => {
    await mount();
    const trigger = host!.querySelector<HTMLButtonElement>(".ws-more-btn")!;
    await act(async () => trigger.click());
    const action = host!.querySelector<HTMLButtonElement>(".ws-more-actions .ws-overview")!;
    action.focus();
    expect(document.activeElement).toBe(action);
    await act(async () => action.click());
    expect(host!.querySelector(".ws-more-pop")).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });
});
