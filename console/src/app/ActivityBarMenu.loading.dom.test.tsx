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
import { getSettings, hydrateUIPrefs, resyncAccumulatedForIdentitySwitch, setPrefsOwnerSource } from "../lib/settings.ts";

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
  setPrefsOwnerSource(() => "");
  vi.useRealTimers();
});

const Shell = () => {
  const view = useLeftRail((s) => s.view);
  return <ActivityBar view={view} shown foldable={false} schedulerEnabled />;
};
const menu = () => document.querySelector<HTMLElement>(".actbar-menu");
const row = (v: RailView) => menu()!.querySelector<HTMLButtonElement>(`[data-menu-view="${v}"]`)!;

describe("the menu before the saved choice has been read", () => {
  it("changes nothing before the read starts, while it runs, and builds on the server's copy once it is in", async () => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    // The bar is on screen first, as at start-up: the read has not even begun.
    await act(async () => root!.render(<Shell />));
    await act(async () => void host!.querySelector(".actbar")!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 30, clientY: 80 })));
    expect(row("files").getAttribute("aria-disabled")).toBe("true");
    await act(async () => row("files").click());
    expect(getSettings().railViewsOff).toEqual([]);

    let release!: (v: unknown) => void;
    gate.p = new Promise((r) => (release = r));
    const done = hydrateUIPrefs();
    await act(async () => void (await Promise.resolve()));
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

  it("stays read-only after a switch until the new owner's copy has been read, even when a read fails", async () => {
    let owner = "tenant-a|u";
    setPrefsOwnerSource(() => owner);
    const prefs: Record<string, unknown> = { "tenant-a|u": { railViewsOff: ["shared", "future-view"] }, "tenant-b|u": { railViewsOff: ["workitems", "tenant-b-future"] } };
    let failing = false;
    api.mockImplementation((...a: unknown[]): Promise<any> =>
      String(a[0]).includes("api/env/ui-prefs") ? (failing ? Promise.reject(new Error("down")) : Promise.resolve(structuredClone(prefs[owner]))) : Promise.resolve({}),
    );
    // Whatever an earlier test left unsaved is dropped, as a real owner change drops it.
    await act(async () => void (await resyncAccumulatedForIdentitySwitch()));
    expect(getSettings().railViewsOff).toEqual(["shared", "future-view"]);

    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => root!.render(<Shell />));
    await act(async () => void host!.querySelector(".actbar")!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 30, clientY: 80 })));
    expect(row("memory").getAttribute("aria-disabled")).toBeNull();

    // Switch to tenant B; its read fails once. The copy in memory is still A's.
    owner = "tenant-b|u";
    failing = true;
    await act(async () => void (await resyncAccumulatedForIdentitySwitch()));
    expect(row("memory").getAttribute("aria-disabled")).toBe("true");
    await act(async () => row("memory").click());
    expect(getSettings().railViewsOff).toEqual(["shared", "future-view"]);

    // The read that succeeds brings B's list, and an edit then builds on it.
    failing = false;
    await act(async () => void (await resyncAccumulatedForIdentitySwitch()));
    expect(getSettings().railViewsOff).toEqual(["workitems", "tenant-b-future"]);
    expect(row("memory").getAttribute("aria-disabled")).toBeNull();
    await act(async () => row("memory").click());
    expect(getSettings().railViewsOff).toEqual(["workitems", "tenant-b-future", "memory"]);
  });
});
