// The command palette's memory mode (#2407): Agent Fleet's own agent memory. Pinned:
//
// 1. The mode is offered only while the memory switch is on, and is left if the switch goes off.
// 2. A name or description match shows at once from the list; a body match comes from the Agent's
//    body search, with the matching line, after the pause. Neither is filtered a second time.
// 3. Enter opens Settings > Agent memory > Memories asking for that memory; Ctrl/⌘+Enter does the same.
// 4. A stopped workspace, a failed read and an empty answer each say their own thing.
// 5. Memories withheld by the secret scan are a count, never a row.
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});

const project = { id: "app-0123456789ab", root: "/r/app", vcs: "git", display: "app" };
const mem = (name: string, extra: Record<string, unknown> = {}) => ({
  name,
  scope: "user",
  description: "",
  updated: "2026-10-01T00:00:00Z",
  pinned: false,
  uses: 0,
  revision: 1,
  authorKind: "claude",
  authorSession: "main",
  ...extra,
});
const entries = [
  mem("house-rules", { description: "how we write commits", pinned: true, type: "feedback" }),
  mem("build-cmd", { scope: "project", project, description: "how to build" }),
  mem("zebra-note", { description: "unrelated" }),
];

let listBody: unknown = { entries, withheld: 0 };
let searchBody: unknown = { hits: [] };
let failList = false;
let failSearch = false;
const asked: string[] = [];
const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
  const url = String(input);
  const json = (b: unknown) =>
    new Response(JSON.stringify(b), { status: 200, headers: { "Content-Type": "application/json" } });
  if (url.includes("memory/entries/member-search")) {
    asked.push(url);
    if (failSearch) return new Response("<html>bad gateway</html>", { status: 502 });
    return json(searchBody);
  }
  if (url.includes("memory/entries/list")) {
    asked.push(url);
    if (failList) return new Response(JSON.stringify({ error: { code: "memory_failed", message: "store unreadable" } }), { status: 500, headers: { "Content-Type": "application/json" } });
    return json(listBody);
  }
  return json({});
});
vi.stubGlobal("fetch", fetchMock);
window.fetch = fetchMock as unknown as typeof window.fetch;

import { CommandPalette } from "./CommandPalette.tsx";
import { useKeysStore } from "./store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useReposStore } from "../repos/store.ts";
import { useSettingsUI } from "../settings/store.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { setSetting } from "../../lib/settings.ts";
import { parseMemoryTarget } from "../settings/memory/memorySearch.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const input = () => document.querySelector<HTMLInputElement>(".cp-input")!;
const tabs = () => [...document.querySelectorAll<HTMLButtonElement>(".cp-mode")];
const memoryTab = () => tabs().find((b) => /^(メモリ|Memory)$/.test(b.textContent || ""));
const rows = () => [...document.querySelectorAll(".cp-item")];
const names = () => rows().map((r) => r.querySelector(".cp-title")?.textContent);
const listText = () => document.querySelector(".cp-list")?.textContent ?? "";

const settle = async (ms: number) => {
  await act(async () => {
    await new Promise((r) => setTimeout(r, ms));
  });
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });
};
async function typeQuery(text: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    setter.call(input(), text);
    input().dispatchEvent(new Event("input", { bubbles: true }));
  });
  await settle(320); // past the body-search pause
}
const pressEnter = (extra: KeyboardEventInit = {}) =>
  act(() => {
    input().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, ...extra }));
  });
async function openMemoryMode() {
  act(() => useKeysStore.getState().openPalette());
  act(() => {
    memoryTab()!.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
  });
  await settle(0);
}
async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<CommandPalette />));
}

beforeEach(() => {
  values.clear();
  asked.length = 0;
  listBody = { entries, withheld: 0 };
  searchBody = { hits: [] };
  failList = false;
  failSearch = false;
  setSetting("agentMemory", true);
  useWorkspaceStore.setState({ state: "running" });
  useSettingsUI.setState({ settingsOpen: false, settingsSection: "display", settingsView: null, settingsTarget: null });
  useReposStore.setState({ repos: [] });
  useSessionsStore.setState({ sessions: [{ name: "fixer", kind: "codex", alive: false, title: "fixer" }] });
});

afterEach(() => {
  act(() => useKeysStore.getState().closePalette());
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  setSetting("agentMemory", false);
});

describe("CommandPalette memory mode", () => {
  it("is offered only while the memory switch is on", async () => {
    setSetting("agentMemory", false);
    await mount();
    act(() => useKeysStore.getState().openPalette());
    expect(memoryTab()).toBeUndefined();
    // Tab goes round the offered modes only: the memory mode is never landed on.
    const seen = new Set<string>();
    for (let i = 0; i < tabs().length + 1; i++) {
      act(() => {
        input().dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true }));
      });
      seen.add(document.querySelector(".cp-mode.on")?.textContent ?? "");
    }
    expect(seen.has("Memory") || seen.has("メモリ")).toBe(false);
    act(() => setSetting("agentMemory", true));
    expect(memoryTab()).toBeTruthy();
  });

  it("leaves the mode when the switch goes off under it", async () => {
    await mount();
    await openMemoryMode();
    expect(document.querySelector(".cp-mode.on")?.textContent).toMatch(/^(Memory|メモリ)$/);
    await act(async () => setSetting("agentMemory", false));
    expect(document.querySelector(".cp-mode.on")?.textContent).not.toMatch(/^(Memory|メモリ)$/);
  });

  it("shows the hint for an empty query and asks the Agent for nothing but the list", async () => {
    await mount();
    await openMemoryMode();
    expect(rows()).toHaveLength(0);
    expect(listText()).toMatch(/Type to search the memories|メモリ（名前・説明・本文）/);
    expect(asked.filter((u) => u.includes("member-search"))).toEqual([]);
  });

  it("finds a name or description at once, and a body-only hit with its matching line", async () => {
    searchBody = { hits: [{ ...entries[2], snippets: ["commits go through the zebra script"] }] };
    await mount();
    await openMemoryMode();
    await typeQuery("commits");
    // house-rules matches by description; zebra-note only by its text, after the search answered.
    expect(names()).toEqual(["house-rules", "zebra-note"]);
    expect(rows()[1].querySelector(".cp-talk-snippet")?.textContent).toBe("commits go through the zebra script");
    expect(rows()[0].querySelector(".cp-talk-snippet")?.textContent).toBe("how we write commits");
    expect(asked.at(-1)).toContain("q=commits");
    expect(asked.at(-1)).not.toContain("scope=");
    // The pinned row is marked, the project row names its project.
    expect(rows()[0].querySelector(".codicon-pinned")).toBeTruthy();
    await typeQuery("build");
    expect(rows()[0].textContent).toContain("app");
  });

  it("opens Settings > Agent memory > Memories asking for the memory, with Enter and with Ctrl+Enter", async () => {
    await mount();
    await openMemoryMode();
    await typeQuery("build-cmd");
    pressEnter();
    let ui = useSettingsUI.getState();
    expect([ui.settingsOpen, ui.settingsSection, ui.settingsView]).toEqual([true, "memory", "memories"]);
    expect(parseMemoryTarget(ui.settingsTarget)).toEqual({ scope: "project", project: project.id, name: "build-cmd" });
    expect(useKeysStore.getState().paletteOpen).toBe(false);

    useSettingsUI.setState({ settingsOpen: false, settingsTarget: null });
    await openMemoryMode();
    await typeQuery("house-rules");
    pressEnter({ ctrlKey: true });
    ui = useSettingsUI.getState();
    expect(parseMemoryTarget(ui.settingsTarget)).toEqual({ scope: "user", project: "", name: "house-rules" });
  });

  it("says when the workspace is stopped, without asking the Agent", async () => {
    useWorkspaceStore.setState({ state: "stopped" });
    await mount();
    await openMemoryMode();
    await typeQuery("rules");
    expect(listText()).toMatch(/only be searched while it is running|起動中のみ検索できます/);
    expect(asked).toEqual([]);
  });

  it("reports an unreadable list and a failed body search with their reason, never as no match", async () => {
    failList = true;
    await mount();
    await openMemoryMode();
    await typeQuery("rules");
    expect(document.querySelector(".cp-talk-err")?.textContent).toContain("store unreadable");
    expect(listText()).not.toMatch(/No matching memory|一致するメモリはありません/);
    // Enter retries both reads; it does not try to open a row.
    failList = false;
    failSearch = true;
    const before = asked.length;
    pressEnter();
    await settle(320);
    expect(asked.length).toBeGreaterThan(before);
    expect(useSettingsUI.getState().settingsOpen).toBe(false);
    expect(document.querySelector(".cp-talk-err")?.textContent).toMatch(/502|gateway|bad/i);
    // The next keystroke asks again, and a good answer clears the message.
    failSearch = false;
    searchBody = { hits: [{ ...entries[2], snippets: ["rules of zebra"] }] };
    await typeQuery("rules ");
    expect(document.querySelector(".cp-talk-err")).toBeNull();
  });

  it("says when nothing matches, and counts the memories the secret scan withheld", async () => {
    listBody = { entries, withheld: 2 };
    await mount();
    await openMemoryMode();
    await typeQuery("nothing-like-this");
    expect(listText()).toMatch(/No matching memory|一致するメモリはありません/);
    expect(document.querySelector(".cp-talk-indexing")?.textContent).toMatch(/2/);
  });
});
