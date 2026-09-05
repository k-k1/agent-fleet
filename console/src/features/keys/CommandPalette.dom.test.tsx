// The command palette's sessions mode, the surface Ctrl/⌘+P opens on. Three things are
// pinned here, and each still renders a screen when broken, which makes it hard to notice:
//
// 1. It opens on the sessions mode, ordered by most recently waiting for input, then
//    running, then stopped. Break that and the top of Ctrl+P is no longer the session that
//    needs an answer right now.
// 2. The order does not move while it is open. The list refreshes every 4s, and a row
//    swapping under the selection (an index) makes Enter open a different session. Only
//    the badges follow the live list.
// 3. A row carries the repo name, the worktree name and the state chip. Missing any of
//    them, a reader cannot tell which session a row is.
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});
// The list is pushed straight into the store, so a network that returns nothing is enough;
// the only request made is the repos refresh right after opening.
const fetchMock = vi.fn(async () => new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } }));
vi.stubGlobal("fetch", fetchMock);
window.fetch = fetchMock as unknown as typeof window.fetch;

import { CommandPalette } from "./CommandPalette.tsx";
import { useKeysStore } from "./store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useNotificationStore } from "../notifications/store.ts";
import { useReposStore } from "../repos/store.ts";
import { resetWaitingLedgerForTest } from "../sessions/waiting.ts";
import type { Session } from "../../types/session.ts";

const session = (name: string, extra: Partial<Session> = {}): Session => ({
  name,
  kind: "claude",
  alive: true,
  title: name,
  repo: "webshop",
  createdAt: "2026-09-01T00:00:00Z",
  ...extra,
});

const askedAt = (id: string, createdAt: string) => ({
  seq: 1,
  id,
  kind: "question",
  target: { type: "session", id },
  displayName: id,
  payload: {},
  createdAt,
  seen: false,
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const titles = () => [...document.querySelectorAll(".cp-item .cp-title")].map((el) => el.textContent);

function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<CommandPalette />));
}

beforeEach(() => {
  values.clear();
  resetWaitingLedgerForTest();
  useReposStore.setState({
    repos: [
      { name: "webshop", branch: "main" },
      { name: "webshop@checkout", branch: "feat/checkout", worktree: true, parent: "webshop" },
    ],
  });
  useNotificationStore.setState({
    items: [askedAt("askedFirst", "2026-09-01T10:00:00Z"), askedAt("askedLast", "2026-09-01T11:00:00Z")],
  });
  useSessionsStore.setState({
    sessions: [
      session("stopped", { alive: false }),
      session("askedFirst", { state: "question" }),
      session("busy", { state: "working", repo: "webshop@checkout", worktree: true }),
      session("askedLast", { state: "permission" }),
    ],
  });
  act(() => useKeysStore.getState().openPalette());
});

afterEach(() => {
  act(() => {
    root?.unmount();
  });
  host?.remove();
  root = null;
  host = null;
  act(() => useKeysStore.getState().closePalette());
});

describe("command palette — sessions mode", () => {
  it("opens on the sessions tab, newest waiting-for-input first and stopped at the foot", () => {
    mount();
    // The first mode tab is sessions, and it is the selected one.
    const tabs = [...document.querySelectorAll(".cp-mode")];
    expect(tabs[0].getAttribute("aria-selected")).toBe("true");
    expect(titles()).toEqual(["askedLast", "askedFirst", "busy", "stopped"]);
  });

  it("shows the working copy, the worktree and the state chip on a row", () => {
    mount();
    const rows = [...document.querySelectorAll<HTMLElement>(".cp-item.cp-sess")];
    const busy = rows.find((r) => r.querySelector(".cp-title")?.textContent === "busy")!;
    expect(busy.querySelector(".cp-sess-repo")?.textContent).toBe("webshop");
    expect(busy.querySelector(".cp-sess-wt")?.textContent).toBe("checkout");
    expect(busy.querySelector(".session-state")?.textContent?.trim()).toBeTruthy();
    expect(busy.querySelector(".sess-kic")?.className).toContain("kind-claude");
    // A session running in a plain clone shows no worktree field; showing one would lie.
    const stopped = rows.find((r) => r.querySelector(".cp-title")?.textContent === "stopped")!;
    expect(stopped.querySelector(".cp-sess-wt")).toBeNull();
    expect(stopped.className).toContain("cp-stopped");
  });

  it("still orders by attention when the list only arrives after it opened", () => {
    // The palette opened right after startup, before the first poll. The list is empty so
    // it opens in command mode and the sessions arrive later. Settling for name order just
    // because there was nothing to sort by would silently break the attention-order promise
    // that one time.
    const list = useSessionsStore.getState().sessions;
    act(() => useSessionsStore.setState({ sessions: [] }));
    mount();
    act(() => {
      useSessionsStore.getState().applyList(list);
    });
    const sessionsTab = document.querySelector<HTMLElement>(".cp-mode")!;
    act(() => {
      sessionsTab.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true }));
    });
    expect(titles()).toEqual(["askedLast", "askedFirst", "busy", "stopped"]);
  });

  it("keeps the order frozen while open, but lets the badges follow the live list", () => {
    mount();
    act(() => {
      useSessionsStore.getState().applyList([
        session("stopped", { alive: false }),
        session("askedFirst", { state: "question" }),
        session("busy", { state: "question", repo: "webshop@checkout", worktree: true }), // asked while open
        session("askedLast", { state: "permission" }),
      ]);
    });
    // The order does not move, so no row is swapped under the cursor...
    expect(titles()).toEqual(["askedLast", "askedFirst", "busy", "stopped"]);
    // ...but the badge reflects the new state.
    const busy = [...document.querySelectorAll<HTMLElement>(".cp-item.cp-sess")].find(
      (r) => r.querySelector(".cp-title")?.textContent === "busy",
    )!;
    expect(busy.querySelector(".session-state")?.className).toContain("question");
  });
});
