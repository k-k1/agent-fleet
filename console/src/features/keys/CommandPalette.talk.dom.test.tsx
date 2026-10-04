// The command palette's conversations mode (ADR 0110): past-session full-text search. Pinned:
//
// 1. The query goes to the Agent's index as typed (encoded), and the rows are the server's hits
//    in its order — no client-side fuzzy filter dropping hits whose snippet does not spell the
//    query (a CJK bigram match, a width-folded one).
// 2. Enter opens the hit's session with a mark at the hit's turn, so the mirror lands there.
// 3. An archived hit opens the archive shelf rather than doing nothing.
// 4. While the Agent is still building the index, the palette says so.
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
const searched: string[] = [];
const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
  const url = String(input);
  if (url.includes("session-search")) {
    searched.push(url);
    return new Response(JSON.stringify(searchBody), { status: 200, headers: { "Content-Type": "application/json" } });
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

  it("says when the index is still being built", async () => {
    searchBody = { hits: [], indexing: true, indexed: 2, total: 9 };
    await typeQuery("anything");
    expect(document.querySelector(".cp-talk-indexing")?.textContent).toMatch(/2\/9/);
  });
});
