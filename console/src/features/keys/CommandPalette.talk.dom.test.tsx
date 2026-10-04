// The command palette's conversations mode (ADR 0110): past-session full-text search. Pinned:
//
// 1. The query goes to the Agent's index as typed (encoded), and the rows are the server's hits
//    in its order — no client-side fuzzy filter dropping hits whose snippet does not spell the
//    query (a CJK bigram match, a width-folded one).
// 2. Enter opens the hit's session with a mark at the hit's turn, so the mirror lands there.
// 3. An archived hit opens the archive shelf rather than doing nothing.
// 4. While the Agent is still building the index, the palette says so and asks again until done.
// 5. A search that could not run says why; it never reads as "no matching conversation".
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});

let searchBody: unknown = { hits: [], indexing: false, indexed: 0, total: 0 };
let searchStatus = 200;
let searchReject = false;
const searched: string[] = [];
const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
  const url = String(input);
  if (url.includes("session-search")) {
    searched.push(url);
    if (searchReject) throw new TypeError("Failed to fetch");
    const body = typeof searchBody === "string" ? searchBody : JSON.stringify(searchBody);
    return new Response(body, { status: searchStatus, headers: { "Content-Type": "application/json" } });
  }
  return new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } });
});
vi.stubGlobal("fetch", fetchMock);
window.fetch = fetchMock as unknown as typeof window.fetch;

const opened: { name: string; split: boolean }[] = [];
vi.mock("../sessions/open.ts", async (orig) => ({
  ...(await orig<typeof import("../sessions/open.ts")>()),
  openSessionFromList: (s: { name: string }, split: boolean) => {
    opened.push({ name: s.name, split });
    return true;
  },
}));

import { CommandPalette } from "./CommandPalette.tsx";
import { useKeysStore } from "./store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useSessionUI } from "../sessions/ui.ts";
import { useReposStore } from "../repos/store.ts";
import { clearMarks, loadMark } from "../mirror/scrollMark.ts";
import type { SessionSearchHit } from "./talkSearch.ts";

const hit = (session: string, idx: number, snippet: string, extra: Partial<SessionSearchHit> = {}): SessionSearchHit => ({
  session,
  display: session,
  kind: "codex",
  idx,
  role: "assistant",
  snippet,
  score: 1,
  ...extra,
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const input = () => document.querySelector<HTMLInputElement>(".cp-input")!;
const snippets = () => [...document.querySelectorAll(".cp-talk-snippet")].map((el) => el.textContent);

async function typeQuery(text: string) {
  const el = input();
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    setter.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
  // past the 250ms debounce, then let the fetch resolve
  await act(async () => {
    await new Promise((r) => setTimeout(r, 320));
  });
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });
}

function pressEnter() {
  act(() => {
    input().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
  });
}

beforeEach(() => {
  values.clear();
  clearMarks();
  opened.length = 0;
  searched.length = 0;
  searchStatus = 200;
  searchReject = false;
  useReposStore.setState({ repos: [] });
  useSessionsStore.setState({ sessions: [{ name: "fixer", kind: "codex", alive: false, title: "fixer" }] });
  useSessionUI.setState({ archivedOpen: false });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<CommandPalette />));
  act(() => useKeysStore.getState().openPalette());
  const tab = [...document.querySelectorAll<HTMLButtonElement>(".cp-mode")].find((b) =>
    /会話|Conversations/.test(b.textContent || ""),
  )!;
  act(() => {
    tab.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
  });
});

afterEach(() => {
  act(() => useKeysStore.getState().closePalette());
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("CommandPalette mode tabs", () => {
  // On a narrow screen the tab row scrolls sideways and Tab switching keeps focus in the input,
  // so the palette itself has to bring the selected tab into view.
  it("scrolls the selected tab into view when the mode changes", () => {
    const seen: string[] = [];
    const orig = Element.prototype.scrollIntoView;
    Element.prototype.scrollIntoView = function (this: Element) {
      if (this.classList.contains("cp-mode")) seen.push(this.textContent || "");
    };
    try {
      act(() => {
        input().dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true }));
      });
      act(() => {
        input().dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", shiftKey: true, bubbles: true }));
      });
    } finally {
      Element.prototype.scrollIntoView = orig;
    }
    const tabs = [...document.querySelectorAll(".cp-mode")].map((b) => b.textContent || "");
    const talk = tabs.findIndex((x) => /会話|Conversations/.test(x));
    // beforeEach left the palette on the conversations tab: Tab moves one right, Shift+Tab back.
    expect(seen).toEqual([tabs[(talk + 1) % tabs.length], tabs[talk]]);
  });
});

describe("CommandPalette conversations mode", () => {
  it("shows the server's hits in its order, unfiltered, and opens the first at its turn", async () => {
    searchBody = {
      hits: [hit("fixer", 42, "トークンの更新が抜けていた"), hit("fixer", 7, "unrelated words")],
      indexing: false,
      indexed: 3,
      total: 3,
    };
    await typeQuery("認証 エラー");
    expect(searched.at(-1)).toContain("q=" + encodeURIComponent("認証 エラー"));
    expect(snippets()).toEqual(["トークンの更新が抜けていた", "unrelated words"]);
    expect(document.querySelector(".cp-talk-indexing")).toBeNull();
    pressEnter();
    expect(opened).toEqual([{ name: "fixer", split: false }]);
    expect(loadMark("fixer")).toEqual({ atBottom: false, idx: 42, offset: 0, near: true });
  });

  it("opens the archive shelf for an archived hit", async () => {
    searchBody = { hits: [hit("shelved", 3, "old fix", { archived: true })], indexing: false, indexed: 1, total: 1 };
    await typeQuery("fix");
    pressEnter();
    expect(opened).toEqual([]);
    expect(useSessionUI.getState().archivedOpen).toBe(true);
  });

  it("says when the index is still being built, and asks again until it is", async () => {
    searchBody = { hits: [], indexing: true, indexed: 2, total: 9 };
    await typeQuery("anything");
    expect(document.querySelector(".cp-talk-indexing")?.textContent).toMatch(/2\/9/);
    const asked = searched.length;
    searchBody = { hits: [hit("fixer", 1, "found after indexing")], indexing: false, indexed: 9, total: 9 };
    await act(async () => {
      await new Promise((r) => setTimeout(r, 3300));
    });
    expect(searched.length).toBe(asked + 1);
    expect(snippets()).toEqual(["found after indexing"]);
    expect(document.querySelector(".cp-talk-indexing")).toBeNull();
    await act(async () => {
      await new Promise((r) => setTimeout(r, 3300));
    });
    expect(searched.length).toBe(asked + 1); // done indexing: no more polling
  }, 10_000);

  it("stops asking again once the palette closes", async () => {
    searchBody = { hits: [], indexing: true, indexed: 2, total: 9 };
    await typeQuery("anything");
    const asked = searched.length;
    act(() => useKeysStore.getState().closePalette());
    await act(async () => {
      await new Promise((r) => setTimeout(r, 3300));
    });
    expect(searched.length).toBe(asked);
  }, 10_000);

  it("reports a failed search with its reason, not as no match", async () => {
    const failures: [string, () => void][] = [
      ["index cannot be read", () => {
        searchStatus = 500;
        searchBody = { error: { code: "search_failed", message: "index cannot be read" } };
      }],
      ["bad gateway", () => {
        searchStatus = 502;
        searchBody = "<html>bad gateway</html>";
      }],
      ["Failed to fetch", () => {
        searchReject = true;
      }],
    ];
    for (const [reason, arrange] of failures) {
      arrange();
      await typeQuery("q " + reason);
      const err = document.querySelector(".cp-talk-err");
      expect(err?.textContent, reason).toContain(reason);
      expect(document.querySelector(".cp-list")?.textContent).not.toMatch(/No matching conversation|一致する会話はありません/);
      searchStatus = 200;
      searchReject = false;
    }
    // Retry runs the same query again — from the keyboard first (Enter in the input), then
    // through the button's click, which is what a keyboard press on it or a screen reader sends.
    const settle = async () => {
      await act(async () => {
        await new Promise((r) => setTimeout(r, 320));
      });
      await act(async () => {
        await new Promise((r) => setTimeout(r, 0));
      });
    };
    let asked = searched.length;
    pressEnter();
    await settle();
    expect(searched.length).toBe(asked + 1);
    expect(opened).toEqual([]); // Enter retried; it did not try to open a row
    searchBody = { hits: [hit("fixer", 1, "back")], indexing: false, indexed: 1, total: 1 };
    searchReject = true;
    await typeQuery("again");
    searchReject = false;
    asked = searched.length;
    act(() => document.querySelector<HTMLButtonElement>(".cp-talk-retry")!.click());
    await settle();
    expect(searched.length).toBe(asked + 1);
    expect(snippets()).toEqual(["back"]);
  });
});
