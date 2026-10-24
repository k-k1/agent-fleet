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

const opened: string[] = [];
vi.mock("../sessions/open.ts", async (orig) => ({
  ...(await orig<typeof import("../sessions/open.ts")>()),
  openSessionFromList: (s: { name: string }) => {
    opened.push(s.name);
    return true;
  },
}));

import { CommandPalette } from "./CommandPalette.tsx";
import { useKeysStore } from "./store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useNotificationStore } from "../notifications/store.ts";
import { useReposStore } from "../repos/store.ts";
import { useWorkItemStore } from "../workitems/store.ts";
import { setSetting, getSettings } from "../../lib/settings.ts";
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

  it("starts on the first row every time it reopens, not on the row picked last time (#1886)", () => {
    mount();
    const input = document.querySelector<HTMLInputElement>(".cp-input")!;
    for (let n = 0; n < 3; n++) {
      act(() => {
        input.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }));
      });
    }
    expect(document.querySelector(".cp-item.sel .cp-title")?.textContent).toBe("stopped");
    act(() => useKeysStore.getState().closePalette());
    act(() => useKeysStore.getState().openPalette());
    expect(document.querySelector(".cp-item.sel .cp-title")?.textContent).toBe("askedLast");
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

describe("command palette — ticket references (#1665)", () => {
  const type = (text: string) => {
    const input = document.querySelector<HTMLInputElement>(".cp-input")!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, text);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
  };
  const refOf = (title: string) =>
    [...document.querySelectorAll<HTMLElement>(".cp-item")]
      .find((r) => r.querySelector(".cp-title")?.textContent === title)
      ?.querySelector(".cp-ref")?.textContent;

  beforeEach(() => {
    act(() => {
      useSessionsStore.setState({
        sessions: [
          session("busy", { state: "working", pr: { number: 1662, state: "open", url: "https://github.com/acme/app/pull/1662" } }),
          session("askedFirst", { state: "question" }),
          session("feature1662", { branch: "feature/1662-x" }),
        ],
      });
      useWorkItemStore.setState({
        loaded: true,
        payload: {
          items: [],
          queries: [],
          fetchedAt: "",
          running: false,
          sessions: [{ id: "1", provider: "github", itemKey: "acme/app#45", sessionName: "askedFirst", repo: "", branch: "", createdAt: "" }],
        },
      });
    });
  });
  afterEach(() => {
    act(() => useWorkItemStore.getState().reset());
  });

  it("finds the session by its PR number and shows the reference that matched", () => {
    mount();
    type("#1662");
    expect(titles()).toEqual(["busy"]);
    expect(refOf("busy")).toBe("PR #1662");
  });

  it("finds the session by the issue it was launched from", () => {
    mount();
    type("#45");
    expect(titles()).toEqual(["askedFirst"]);
    expect(refOf("askedFirst")).toBe("acme/app#45");
  });

  it("puts reference hits first and still fuzzy-matches the rest", () => {
    mount();
    type("1662");
    expect(titles()[0]).toBe("busy");
    expect(titles()).toContain("feature1662");
    expect(refOf("feature1662")).toBeUndefined();
  });

  it("keeps the highlighted row when a reference arrives and re-sorts the list under it", () => {
    act(() => {
      useSessionsStore.setState({ sessions: [session("busy"), session("ticketText", { title: "PROJ-123-notes" })] });
    });
    mount();
    type("PROJ-123");
    expect(titles()).toEqual(["PROJ-123-notes"]);
    // The ledger lands after the query was typed: busy becomes a reference hit and goes first.
    act(() => {
      useWorkItemStore.setState({
        loaded: true,
        payload: {
          items: [],
          queries: [],
          fetchedAt: "",
          running: false,
          sessions: [{ id: "2", provider: "jira", itemKey: "PROJ-123", sessionName: "busy", repo: "", branch: "", createdAt: "" }],
        },
      });
    });
    expect(titles()).toEqual(["busy", "PROJ-123-notes"]);
    expect(document.querySelector(".cp-item.sel .cp-title")?.textContent).toBe("PROJ-123-notes");
    // A new query starts again at the top.
    type("PROJ-123 ");
    expect(document.querySelector(".cp-item.sel .cp-title")?.textContent).toBe("busy");
  });

  it("pulls the highlight back into range when its row disappears and the list shrinks below it", () => {
    act(() => {
      useSessionsStore.setState({ sessions: [session("note1"), session("note2"), session("note3")] });
    });
    mount();
    type("note");
    const input = document.querySelector<HTMLInputElement>(".cp-input")!;
    const key = (k: string) => act(() => void input.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true })));
    key("ArrowDown");
    key("ArrowDown");
    expect(document.querySelector(".cp-item.sel .cp-title")?.textContent).toBe("note3");
    act(() => {
      useSessionsStore.setState({ sessions: [session("note1")] });
    });
    expect(titles()).toEqual(["note1"]);
    expect(document.querySelector(".cp-item.sel .cp-title")?.textContent).toBe("note1");
    // An emptied list must still highlight the first row once rows come back.
    act(() => {
      useSessionsStore.setState({ sessions: [] });
    });
    expect(titles()).toEqual([]);
    act(() => {
      useSessionsStore.setState({ sessions: [session("note1")] });
    });
    expect(document.querySelector(".cp-item.sel .cp-title")?.textContent).toBe("note1");
    opened.length = 0;
    key("Enter");
    expect(opened).toEqual(["note1"]);
  });

  it("does not let a prefix of the number match", () => {
    mount();
    type("#166");
    expect(titles()).toEqual([]);
  });
});

describe("command palette — session families (#1887)", () => {
  const type = (text: string) => {
    const input = document.querySelector<HTMLInputElement>(".cp-input")!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, text);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
  };
  const rowOf = (title: string) =>
    [...document.querySelectorAll<HTMLElement>(".cp-item")].find((r) => r.querySelector(".cp-title")?.textContent === title)!;
  const key = (k: string) =>
    act(() => {
      document.querySelector<HTMLInputElement>(".cp-input")!.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true }));
    });

  beforeEach(() => {
    // "chief" is stopped, its child "kidWaiting" waits, "kidIdle" runs; "loner" is unrelated.
    act(() => {
      useNotificationStore.setState({ items: [askedAt("kidWaiting", "2026-09-01T12:00:00Z")] });
      useSessionsStore.setState({
        sessions: [
          session("loner", { state: "working" }),
          session("kidIdle", { state: "working", originSession: "chief", origin: "session", createdAt: "2026-09-02T00:00:00Z" }),
          session("chief", { alive: false }),
          session("kidWaiting", { state: "question", originSession: "chief", origin: "session", createdAt: "2026-09-01T00:00:00Z" }),
          session("orphan", { state: "working", originSession: "archivedGone" }),
        ],
      });
    });
  });

  it("lays a family out as one block at its most urgent member, children indented under the parent", () => {
    mount();
    // chief is stopped, but its waiting child pulls the whole block to the top; the parent
    // row still looks stopped.
    expect(titles()).toEqual(["chief", "kidWaiting", "kidIdle", "loner", "orphan"]);
    expect(rowOf("chief").className).toContain("cp-stopped");
    expect(rowOf("chief").className).not.toContain("cp-nested");
    expect(rowOf("kidWaiting").className).toContain("cp-nested");
    expect(rowOf("kidIdle").className).toContain("cp-nested");
    expect(rowOf("loner").className).not.toContain("cp-nested");
    // A child whose parent is gone is a plain root: no indent, no marker.
    expect(rowOf("orphan").className).not.toContain("cp-nested");
    expect(rowOf("orphan").querySelector(".cp-sess-parent")).toBeNull();
    // No marker on indented rows (the indent already says it).
    expect(document.querySelector(".cp-sess-parent")).toBeNull();
  });

  it("moves the selection through the rows in visual order", () => {
    mount();
    key("ArrowDown");
    expect(document.querySelector(".cp-item.sel .cp-title")?.textContent).toBe("kidWaiting");
    key("ArrowDown");
    expect(document.querySelector(".cp-item.sel .cp-title")?.textContent).toBe("kidIdle");
  });

  it("keeps a session born while open at the foot instead of slotting it into the block", () => {
    mount();
    act(() => {
      useSessionsStore.getState().applyList([
        ...useSessionsStore.getState().sessions,
        session("newKid", { state: "working", originSession: "chief", createdAt: "2026-09-03T00:00:00Z" }),
      ]);
    });
    expect(titles()).toEqual(["chief", "kidWaiting", "kidIdle", "loner", "orphan", "newKid"]);
    // It is a root at the foot, so it carries the marker instead of an indent.
    expect(rowOf("newKid").className).not.toContain("cp-nested");
    expect(rowOf("newKid").querySelector(".cp-sess-parent")?.textContent).toContain("chief");
  });

  it("flattens under a query and marks a child with its parent", () => {
    mount();
    type("kid");
    expect(titles()).toEqual(["kidWaiting", "kidIdle"]);
    for (const k of ["kidWaiting", "kidIdle"]) {
      expect(rowOf(k).className).not.toContain("cp-nested");
      expect(rowOf(k).querySelector(".cp-sess-parent")?.textContent).toContain("chief");
    }
  });

  it("lists a parent's children when its title is typed", () => {
    mount();
    type("chief");
    expect(titles().sort()).toEqual(["chief", "kidIdle", "kidWaiting"]);
  });

  describe("with a parent whose title differs from its name", () => {
    beforeEach(() => {
      act(() => {
        useNotificationStore.setState({ items: [askedAt("newWaitingChild", "2026-09-01T12:00:00Z")] });
        useSessionsStore.setState({
          sessions: [
            session("stoppedParent", { alive: false, title: "Zanzibar orchestrator" }),
            session("oldWorkingChild", { title: "oldWorkingChild", state: "working", originSession: "stoppedParent", createdAt: "2026-09-01T00:00:00Z" }),
            session("newWaitingChild", { title: "newWaitingChild", state: "question", originSession: "stoppedParent", createdAt: "2026-09-02T00:00:00Z" }),
          ],
        });
      });
    });

    it("keeps the attention order under a query, and Enter opens the row on top", () => {
      mount();
      // Empty query: family order (parent, older child, newer child).
      expect(titles()).toEqual(["Zanzibar orchestrator", "oldWorkingChild", "newWaitingChild"]);
      // A query matching only the children restores the frozen attention order: waiting first.
      type("WorkingChild");
      expect(titles()).toEqual(["oldWorkingChild"]);
      type("Child");
      // (The fuzzy filter also admits the stopped parent, which sorts last.)
      expect(titles().slice(0, 2)).toEqual(["newWaitingChild", "oldWorkingChild"]);
      expect(document.querySelector(".cp-item.sel .cp-title")?.textContent).toBe("newWaitingChild");
      opened.length = 0;
      key("Enter");
      expect(opened).toEqual(["newWaitingChild"]);
    });

    it("opens the filtered row on Enter, not the unfiltered row at the same index", () => {
      mount();
      type("WorkingChild");
      opened.length = 0;
      key("Enter");
      expect(opened).toEqual(["oldWorkingChild"]);
    });

    it("opens the row the arrow keys landed on in the grouped list", () => {
      mount();
      key("ArrowDown");
      key("ArrowDown");
      opened.length = 0;
      key("Enter");
      // Visual order is parent, older child, newer child.
      expect(opened).toEqual(["newWaitingChild"]);
    });

    it("finds the children by the parent's title, which is not its name", () => {
      mount();
      type("zanzibar");
      expect(titles().sort()).toEqual(["Zanzibar orchestrator", "newWaitingChild", "oldWorkingChild"]);
      expect(rowOf("newWaitingChild").querySelector(".cp-sess-parent")?.textContent).toContain("Zanzibar orchestrator");
    });
  });
});

describe("command palette — reverse mode chord and paging (#2412)", () => {
  // Modes offered here: sessions, command, changed, file, talk (sfiles needs an active session).
  const activeTab = () => document.querySelector(".cp-mode.on")?.textContent;
  const tabs = () => [...document.querySelectorAll(".cp-mode")].map((el) => el.textContent);
  const press = (init: KeyboardEventInit & { isComposing?: boolean }) =>
    act(() => {
      document.querySelector<HTMLInputElement>(".cp-input")!.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init }));
    });
  const pressCancelable = (init: KeyboardEventInit) => {
    const ev = new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
    act(() => {
      document.querySelector<HTMLInputElement>(".cp-input")!.dispatchEvent(ev);
    });
    return ev;
  };
  const chordP = { code: "KeyP", key: "p", ctrlKey: true };
  const selTitle = () => document.querySelector(".cp-item.sel .cp-title")?.textContent;

  afterEach(() => setSetting("keybindings", {}));

  it("Ctrl+Shift+P goes to the previous mode and wraps from the first to the last", () => {
    mount();
    const all = tabs();
    expect(activeTab()).toBe(all[0]);
    const ev = pressCancelable({ ...chordP, shiftKey: true });
    expect(ev.defaultPrevented).toBe(true);
    expect(activeTab()).toBe(all[all.length - 1]);
    pressCancelable({ ...chordP, shiftKey: true });
    expect(activeTab()).toBe(all[all.length - 2]);
  });

  it("Ctrl+P still goes forward", () => {
    mount();
    const all = tabs();
    press(chordP);
    expect(activeTab()).toBe(all[1]);
  });

  it("does not offer the session-files mode in either direction without an active session", () => {
    mount();
    expect(tabs()).toHaveLength(5);
    for (let n = 0; n < 5; n++) press({ ...chordP, shiftKey: true });
    expect(activeTab()).toBe(tabs()[0]);
  });

  it("derives the reverse chord from a rebound palette chord", () => {
    setSetting("keybindings", { ...getSettings().keybindings, "app.palette": "alt+o" });
    mount();
    const all = tabs();
    // The old chord no longer cycles; the rebound one does, and so does its Shift twin.
    press({ ...chordP, shiftKey: true });
    expect(activeTab()).toBe(all[0]);
    press({ code: "KeyO", key: "o", altKey: true, shiftKey: true });
    expect(activeTab()).toBe(all[all.length - 1]);
    press({ code: "KeyO", key: "o", altKey: true });
    expect(activeTab()).toBe(all[0]);
  });

  it("has no reverse chord when the bound chord already contains Shift, and forward keeps working", () => {
    setSetting("keybindings", { ...getSettings().keybindings, "app.palette": "mod+shift+k" });
    mount();
    const all = tabs();
    const rev = pressCancelable({ code: "KeyK", key: "k", ctrlKey: true, shiftKey: true, altKey: true });
    expect(rev.defaultPrevented).toBe(false);
    expect(activeTab()).toBe(all[0]);
    press({ code: "KeyK", key: "k", ctrlKey: true, shiftKey: true });
    expect(activeTab()).toBe(all[1]);
    press({ key: "Tab", shiftKey: true });
    expect(activeTab()).toBe(all[0]);
  });

  describe("mode hint", () => {
    const hint = () => document.querySelector(".cp-mode-hint")?.textContent;

    it("names the bound chord and offers Shift: back", () => {
      mount();
      expect(hint()).toContain("Tab / Ctrl+P to switch (Shift: back)");
    });

    it("follows a rebound chord", () => {
      setSetting("keybindings", { ...getSettings().keybindings, "app.palette": "alt+o" });
      mount();
      expect(hint()).toContain("Tab / Alt+O to switch (Shift: back)");
      expect(hint()).not.toContain("Ctrl+P");
    });

    it("drops Shift: back when the bound chord already has Shift", () => {
      setSetting("keybindings", { ...getSettings().keybindings, "app.palette": "mod+shift+k" });
      mount();
      expect(hint()).toContain("Tab / Ctrl+Shift+K to switch");
      expect(hint()).not.toContain("Shift: back");
    });

    it("names only Tab when the palette chord is unbound", () => {
      setSetting("keybindings", { ...getSettings().keybindings, "app.palette": "" });
      mount();
      expect(hint()).toContain("Tab to switch");
      expect(hint()).not.toContain("Ctrl");
    });
  });

  describe("PageUp / PageDown", () => {
    // jsdom has no layout: 5 rows fit (100px list / 20px row).
    const proto = HTMLElement.prototype;
    const savedClient = Object.getOwnPropertyDescriptor(proto, "clientHeight");
    const savedOffset = Object.getOwnPropertyDescriptor(proto, "offsetHeight");
    beforeEach(() => {
      Object.defineProperty(proto, "clientHeight", { configurable: true, get: () => 100 });
      Object.defineProperty(proto, "offsetHeight", { configurable: true, get: () => 20 });
      useSessionsStore.setState({
        sessions: Array.from({ length: 12 }, (_, i) => session("s" + String(i).padStart(2, "0"), { state: "working" })),
      });
      useNotificationStore.setState({ items: [] });
    });
    afterEach(() => {
      if (savedClient) Object.defineProperty(proto, "clientHeight", savedClient);
      else delete (proto as unknown as Record<string, unknown>).clientHeight;
      if (savedOffset) Object.defineProperty(proto, "offsetHeight", savedOffset);
      else delete (proto as unknown as Record<string, unknown>).offsetHeight;
    });
    const selIndex = () => titles().indexOf(selTitle() ?? "");

    it("moves the highlight by one visible page and clamps at both ends", () => {
      mount();
      expect(selIndex()).toBe(0);
      const ev = pressCancelable({ key: "PageDown" });
      expect(ev.defaultPrevented).toBe(true);
      expect(selIndex()).toBe(5);
      press({ key: "PageDown" });
      expect(selIndex()).toBe(10);
      press({ key: "PageDown" });
      expect(selIndex()).toBe(11);
      press({ key: "PageUp" });
      expect(selIndex()).toBe(6);
      press({ key: "PageUp" });
      press({ key: "PageUp" });
      expect(selIndex()).toBe(0);
    });

    it("opens the row PageDown landed on with Enter", () => {
      mount();
      const fifth = titles()[5];
      press({ key: "PageDown" });
      opened.length = 0;
      press({ key: "Enter" });
      expect(opened).toEqual([fifth]);
    });

    it("moves by at least one row when the list has no measurable height", () => {
      Object.defineProperty(proto, "clientHeight", { configurable: true, get: () => 0 });
      mount();
      press({ key: "PageDown" });
      expect(selIndex()).toBe(1);
    });

    it("leaves modified Page keys to the browser / OS", () => {
      mount();
      for (const mod of [{ ctrlKey: true }, { metaKey: true }, { altKey: true }]) {
        const ev = pressCancelable({ key: "PageDown", ...mod });
        expect(ev.defaultPrevented).toBe(false);
      }
      expect(selIndex()).toBe(0);
    });

    it("does nothing while an IME composition is in progress", () => {
      mount();
      const ev = pressCancelable({ key: "PageDown", isComposing: true });
      expect(ev.defaultPrevented).toBe(false);
      expect(selIndex()).toBe(0);
      pressCancelable({ ...chordP, shiftKey: true, isComposing: true });
      expect(activeTab()).toBe(tabs()[0]);
    });
  });
});
