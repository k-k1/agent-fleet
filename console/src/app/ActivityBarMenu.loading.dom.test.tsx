// The activity bar's context menu before the saved choice has been read from the server: a change
// made then would be built on the empty local copy and a save would drop what another browser stored.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const { api, gate } = vi.hoisted(() => ({
  api: vi.fn((..._args: unknown[]) => Promise.resolve({})),
  // The ui-prefs read waits on it.
  gate: { p: null as Promise<unknown> | null },
}));
vi.mock("../core/api/client.ts", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  api: (...args: unknown[]) => (gate.p && String(args[0]).includes("api/env/ui-prefs") ? gate.p : api(...args)),
  memoList: () => Promise.resolve([]),
}));

import { ActivityBar } from "./ActivityBar.tsx";
import { useLeftRail } from "../core/store/leftRail.ts";
import type { RailView } from "../core/store/leftRail.ts";
import { useSessionsStore } from "../features/sessions/store.ts";
import { useNotificationStore } from "../features/notifications/store.ts";
import { useHandoffStore } from "../features/sharing/handoffStore.ts";
import { getSettings, hydrateUIPrefs } from "../lib/settings.ts";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | null = null;
let host: HTMLDivElement | null = null;

beforeEach(() => {
  localStorage.clear();
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

const Shell = () => {
  const view = useLeftRail((s) => s.view);
  return <ActivityBar view={view} shown foldable={false} schedulerEnabled />;
};
const menu = () => document.querySelector<HTMLElement>(".actbar-menu");
const row = (v: RailView) => menu()!.querySelector<HTMLButtonElement>(`[data-menu-view="${v}"]`)!;

describe("the menu before the saved choice has been read", () => {
  it("changes nothing until the server's copy has arrived, then builds on it", async () => {
    let release!: (v: unknown) => void;
    gate.p = new Promise((r) => (release = r));
    const done = hydrateUIPrefs();
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => root!.render(<Shell />));
    await act(async () => void host!.querySelector(".actbar")!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 30, clientY: 80 })));
    expect(row("files").getAttribute("aria-disabled")).toBe("true");
    await act(async () => row("files").click());
    expect(getSettings().railViewsOff).toEqual([]);
    release({ railViewsOff: ["shared", "future-view"] });
    await act(async () => void (await done));
    gate.p = null;
    expect(getSettings().railViewsOff).toEqual(["shared", "future-view"]);
    expect(row("files").getAttribute("aria-disabled")).toBeNull();
    await act(async () => row("files").click());
    expect(getSettings().railViewsOff).toEqual(["shared", "future-view", "files"]);
  });
});
