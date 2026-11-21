// The Memory view of the left pane (ADR 0141): memories grouped by scope with pinned first, the
// filters, the pending proposals from the shared store, and where each row leads.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
}));
const openMemoryView = vi.fn();
const openMemoryEntry = vi.fn();
vi.mock("./openEntry.ts", async (importActual) => ({
  ...(await importActual<typeof import("./openEntry.ts")>()),
  openMemoryEntry: (...args: unknown[]) => openMemoryEntry(...args),
}));
vi.mock("./open.ts", async (importActual) => ({
  ...(await importActual<typeof import("./open.ts")>()),
  openMemoryView: (...args: unknown[]) => openMemoryView(...args),
}));

import { MemoryRailSection, groupByScope } from "./MemoryRailSection.tsx";
import { ViewShownContext } from "../../lib/viewShown.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { useMemoryStore } from "./store.ts";
import { ToastProvider } from "../../ui/ToastProvider.tsx";
import { ConfirmProvider } from "../../ui/ConfirmProvider.tsx";
import { useLayoutStore } from "../../layout/store.ts";
import { freshLayout } from "../../layout/ops.ts";
import { memoryEntryContent } from "./openEntry.ts";
import { setSetting } from "../../lib/settings.ts";
import { readFileSync } from "node:fs";
import type { MemoryListed } from "../settings/memory/memoryTypes.ts";

const { t } = await import("../../lib/i18n/index.ts");
const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;

const project = { id: "app-0123456789ab", root: "/r/app", vcs: "git", display: "app" };
const mem = (name: string, over: Partial<MemoryListed> = {}): MemoryListed => ({
  name,
  scope: "user",
  description: name + " desc",
  type: "feedback",
  updated: "2026-10-01T00:00:00Z",
  pinned: false,
  uses: 0,
  revision: 1,
  authorKind: "claude",
  authorSession: "s",
  ...over,
});
const entries = [
  mem("plain"),
  mem("build-cmd", { scope: "project", project, type: "project" }),
  mem("rules", { pinned: true }),
];
const proposal = (id: string, status: string) => ({
  id,
  topicId: "t",
  scope: "user",
  memory: "subject-" + id,
  status,
  created: "2026-11-01T00:00:00Z",
  expires: "",
  items: [{ op: "create", name: "subject-" + id }],
  observationIds: [],
  evidence: [],
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const flush = async () => {
  for (let i = 0; i < 4; i++) await act(async () => {});
};
const rows = () => Array.from(host!.querySelectorAll<HTMLButtonElement>("[data-row-key]"));
const names = () => rows().map((r) => r.querySelector(".memrail-name")!.textContent);
// The filter chips sit under the Memories head, after Proposals' own "All" toggle, so look in them.
const button = (label: string) => Array.from(host!.querySelectorAll<HTMLButtonElement>(".memrail-chips button, button.memrail-link")).find((b) => b.textContent === label)!;
async function setValue(el: HTMLInputElement | HTMLSelectElement, v: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), "value")!.set!.call(el, v);
    el.dispatchEvent(new Event(el instanceof HTMLSelectElement ? "change" : "input", { bubbles: true }));
  });
}

beforeEach(async () => {
  localStorage.removeItem("af.memrail-folded"); // before the first render reads it
  api.mockImplementation(async (url: string) => {
    if (url.startsWith("api/agents/memory/entries/list")) return { entries, withheld: 0 };
    if (url.startsWith("api/agents/memory/entries/member-forgotten"))
      return {
        forgotten: [{ commit: "c1", at: "2026-10-03T00:00:00Z", op: "forget", scope: "project", project, name: "old-note", authorKind: "member", authorSession: "", latest: true, live: false, revertible: true }],
      };
    if (url.startsWith("api/agents/memory/entries/member-search")) return { hits: [] };
    return {};
  });
  useMemoryStore.setState({ version: 0, proposals: [proposal("p1", "pending"), proposal("p2", "approved")], proposalsErr: "" });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await render(true);
});
async function render(shown: boolean) {
  await act(async () =>
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <ViewShownContext.Provider value={shown}>
            <MemoryRailSection />
          </ViewShownContext.Provider>
        </ConfirmProvider>
      </ToastProvider>,
    ),
  );
  await flush();
}
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  host?.remove();
  root = host = null;
  api.mockReset();
  openMemoryView.mockReset();
  openMemoryEntry.mockReset();
  useLayoutStore.setState({ layout: freshLayout() });
});

describe("MemoryRailSection", () => {
  it("groups memories by scope, the user scope first and pinned first within it", () => {
    const heads = Array.from(host!.querySelectorAll(".memrail-group-name")).map((h) => h.textContent);
    expect(heads).toEqual([t("mem.af_scope_user"), "app"]);
    expect(names()).toEqual(["rules", "plain", "build-cmd"]);
  });

  it("narrows by the Pinned chip, by type and by scope", async () => {
    await act(async () => button(t("mem.md_f_pinned")).click());
    expect(names()).toEqual(["rules"]);
    await act(async () => button(t("mem.md_f_all")).click());
    const [scopeSel, typeSel] = Array.from(host!.querySelectorAll<HTMLSelectElement>(".memrail-selects select"));
    await setValue(typeSel, "project");
    expect(names()).toEqual(["build-cmd"]);
    await setValue(typeSel, "");
    await setValue(scopeSel, "user");
    expect(names()).toEqual(["rules", "plain"]);
  });

  it("matches the listed fields at once", async () => {
    await setValue(host!.querySelector<HTMLInputElement>('input[type="search"]')!, "build");
    expect(names()).toEqual(["build-cmd"]);
  });

  it("opens a memory in a pane of its own (ADR 0141 decision 4)", async () => {
    await act(async () => rows()[2].click());
    expect(openMemoryEntry).toHaveBeenCalledTimes(1);
    expect(openMemoryEntry.mock.calls[0][0]).toMatchObject({ scope: "project", project, name: "build-cmd" });
  });

  it("marks the row of the memory in the pane in front", async () => {
    await act(async () =>
      useLayoutStore.getState().openTarget({ content: memoryEntryContent({ scope: "project", project, name: "build-cmd" }) }),
    );
    const current = host!.querySelector('[aria-current="true"][data-row-key]');
    expect(current?.querySelector(".memrail-name")?.textContent).toBe("build-cmd");
  });

  it("starts a new memory in the project on screen, or in the user scope", async () => {
    const plus = () => host!.querySelector<HTMLButtonElement>(`button[aria-label="${t("mem.rv_new")}"]`)!;
    await act(async () => plus().click());
    expect(openMemoryEntry.mock.calls.at(-1)![0]).toEqual({ scope: "user", name: "" });
    const [scopeSel] = Array.from(host!.querySelectorAll<HTMLSelectElement>(".memrail-selects select"));
    await setValue(scopeSel, "project:" + project.id);
    await act(async () => plus().click());
    expect(openMemoryEntry.mock.calls.at(-1)![0]).toEqual({ scope: "project", project: { id: project.id, display: "app" }, name: "" });
  });

  it("offers the tidy-up's run only where it is switched on", async () => {
    const run = () => host!.querySelector(`button[aria-label="${t("mem.rv_run")}"]`);
    setSetting("memoryConsolidation", false);
    await render(true);
    expect(run()).toBeNull();
    setSetting("memoryConsolidation", true);
    await render(true);
    expect(run()).not.toBeNull();
    setSetting("memoryConsolidation", false);
  });

  it("lists pending proposals only by default, and opens one alone in the pane", async () => {
    const props = () => Array.from(host!.querySelectorAll<HTMLButtonElement>("[data-proposal-id]"));
    expect(props().map((b) => b.dataset.proposalId)).toEqual(["p1"]);
    await act(async () => props()[0].click());
    expect(openMemoryView).toHaveBeenCalledWith("proposals", { proposalId: "p1" });
    const toggle = host!.querySelector<HTMLButtonElement>(".memrail-toggle")!;
    expect(toggle.getAttribute("aria-pressed")).toBe("false");
    await act(async () => toggle.click());
    expect(toggle.getAttribute("aria-pressed")).toBe("true");
    expect(toggle.textContent).toBe(t("mem.md_f_all"));
    expect(props().map((b) => b.dataset.proposalId)).toEqual(["p1", "p2"]);
  });

  it("reads the forgotten list only once its chip is on, and opens one as forgotten", async () => {
    const forgottenCalls = () => api.mock.calls.filter((c) => String(c[0]).startsWith("api/agents/memory/entries/member-forgotten"));
    expect(forgottenCalls()).toHaveLength(0);
    await act(async () => button(t("mem.md_f_forgotten")).click());
    await flush();
    expect(forgottenCalls()).toHaveLength(1);
    expect(names()).toEqual(["old-note"]);
    await act(async () => rows()[0].click());
    expect(openMemoryEntry.mock.calls[0]).toEqual([expect.objectContaining({ scope: "project", name: "old-note" }), { gone: true }]);
  });

  it("reads the list again after a write anywhere bumps the shared version", async () => {
    const listCalls = () => api.mock.calls.filter((c) => String(c[0]).startsWith("api/agents/memory/entries/list")).length;
    const before = listCalls();
    await act(async () => useMemoryStore.getState().bump());
    await flush();
    expect(listCalls()).toBe(before + 1);
  });
});

describe("MemoryRailSection reads", () => {
  const calls = (prefix: string) => api.mock.calls.filter((c) => String(c[0]).startsWith(prefix)).map((c) => String(c[0]));

  it("makes no Agent read while hidden, and reads once on the next show (ADR 0140 decision 3)", async () => {
    await render(false);
    const before = calls("api/agents/memory/entries/list").length;
    await act(async () => useMemoryStore.getState().bump());
    await act(async () => useMemoryStore.getState().bump());
    await flush();
    expect(calls("api/agents/memory/entries/list")).toHaveLength(before);
    await render(true);
    expect(calls("api/agents/memory/entries/list")).toHaveLength(before + 1);
  });

  it("does not show one tenant's memories while the next tenant's list is on its way", async () => {
    expect(names().length).toBeGreaterThan(0);
    api.mockImplementation(() => new Promise(() => {}));
    await act(async () => useTenantStore.setState({ tenant: "other-tenant" }));
    await flush();
    expect(names()).toEqual([]);
    expect(host!.textContent).toContain(t("common.loading"));
  });

  it("asks for the scope on screen once a chosen project scope has no memory left", async () => {
    const [scopeSel] = Array.from(host!.querySelectorAll<HTMLSelectElement>(".memrail-selects select"));
    await setValue(scopeSel, "project:" + project.id);
    api.mockImplementation(async (url: string) => {
      if (url.startsWith("api/agents/memory/entries/list")) return { entries: entries.filter((m) => m.scope === "user") };
      if (url.startsWith("api/agents/memory/entries/member-forgotten")) return { forgotten: [] };
      if (url.startsWith("api/agents/memory/entries/member-search")) return { hits: [] };
      return {};
    });
    await act(async () => useMemoryStore.getState().bump());
    await flush();
    expect(scopeSel.value).toBe("all");
    await act(async () => button(t("mem.md_f_forgotten")).click());
    await flush();
    expect(calls("api/agents/memory/entries/member-forgotten").at(-1)).toBe("api/agents/memory/entries/member-forgotten");
  });

  it("says it is searching, not 'no match', while the body search is on its way", async () => {
    vi.useFakeTimers();
    try {
      await setValue(host!.querySelector<HTMLInputElement>('input[type="search"]')!, "zebra");
      expect(host!.textContent).toContain(t("mem.br_searching"));
      expect(host!.textContent).not.toContain(t("mem.br_no_match"));
    } finally {
      vi.useRealTimers();
    }
  });

  it("drops the paging cursor of the old scope at once when the scope changes", async () => {
    let release: (v: unknown) => void = () => {};
    api.mockImplementation(async (url: string) => {
      if (url.startsWith("api/agents/memory/entries/list")) return { entries };
      if (url === "api/agents/memory/entries/member-forgotten") return { forgotten: [], next: "curA" };
      if (url.startsWith("api/agents/memory/entries/member-forgotten?scope=user")) return new Promise((r) => (release = r));
      return {};
    });
    await act(async () => button(t("mem.md_f_forgotten")).click());
    await flush();
    expect(button(t("mem.br_forgotten_more"))).toBeTruthy();
    const [scopeSel] = Array.from(host!.querySelectorAll<HTMLSelectElement>(".memrail-selects select"));
    await setValue(scopeSel, "user");
    // The user scope's first page has not answered: no cursor of "all" may be offered meanwhile.
    expect(button(t("mem.br_forgotten_more"))).toBeUndefined();
    await act(async () => release({ forgotten: [] }));
    await flush();
    expect(calls("api/agents/memory/entries/member-forgotten").some((u) => u.includes("before=curA"))).toBe(false);
  });
});

describe("groupByScope", () => {
  it("orders projects by name after the user scope", () => {
    const other = { ...project, id: "zzz-0123456789ab", display: "aaa" };
    const out = groupByScope([mem("x", { scope: "project", project }), mem("y", { scope: "project", project: other }), mem("z")], "U");
    expect(out.map(([, label]) => label)).toEqual(["U", "aaa", "app"]);
    // Keyed by scope: two projects may share a display name.
    const twin = { ...project, id: "twin-0123456789ab" };
    const keys = groupByScope([mem("a", { scope: "project", project }), mem("b", { scope: "project", project: twin })], "U").map(([k]) => k);
    expect(new Set(keys).size).toBe(2);
  });
});

describe("MemoryRailSection layout (#4675)", () => {
  const headTitles = () => Array.from(host!.querySelectorAll(".ui-section-title")).map((e) => e.textContent);
  const groupHead = (k: string) => host!.querySelector<HTMLButtonElement>(`.memrail-group-head[data-group="${k}"]`)!;

  it("titles the top section Agent memory, apart from the Memories list", () => {
    expect(headTitles()).toEqual([t("mem.rail_head"), t("mem.view_proposals"), t("mem.view_memories")]);
    expect(t("mem.rail_head")).not.toBe(t("rail.view.memory"));
  });

  it("keeps the search and filters inside the Memories section, not the top one", () => {
    const sections = Array.from(host!.querySelectorAll("section.ui-section"));
    const owner = sections.filter((s) => s.querySelector(".memrail-filter"));
    expect(owner).toHaveLength(1);
    expect(owner[0].querySelector(".ui-section-title")?.textContent).toBe(t("mem.view_memories"));
    expect(sections[0].querySelector('input[type="search"]')).toBeNull();
  });

  it("renders the type as a badge, one class per known type and a neutral one otherwise", async () => {
    const types = Array.from(host!.querySelectorAll(".memrail-type")).map((e) => e.className);
    expect(types).toContain("memrail-type t-feedback");
    expect(types).toContain("memrail-type t-project");
    entries.push(mem("odd", { type: "mystery" }));
    try {
      await act(async () => useMemoryStore.setState({ version: 1 }));
      await flush();
      expect(host!.querySelector(".memrail-type.t-other")?.textContent).toBe("mystery");
    } finally {
      entries.pop();
    }
  });

  it("folds a scope group, shows its count, and keeps the fold per device", async () => {
    expect(groupHead("user").querySelector(".memrail-group-count")?.textContent).toBe("2");
    expect(groupHead("user").getAttribute("aria-expanded")).toBe("true");
    await act(async () => groupHead("user").click());
    expect(groupHead("user").getAttribute("aria-expanded")).toBe("false");
    expect(names()).toEqual(["build-cmd"]);
    expect(JSON.parse(localStorage.getItem("af.memrail-folded")!)).toEqual({ user: true });
    await act(async () => root!.unmount());
    root = createRoot(host!);
    await render(true);
    expect(names()).toEqual(["build-cmd"]);
    await act(async () => groupHead("user").click());
    expect(names()).toEqual(["rules", "plain", "build-cmd"]);
    expect(JSON.parse(localStorage.getItem("af.memrail-folded")!)).toEqual({});
  });

  it("starts open when the stored fold state is unreadable", async () => {
    localStorage.setItem("af.memrail-folded", "{not json");
    await act(async () => root!.unmount());
    root = createRoot(host!);
    await render(true);
    expect(names()).toEqual(["rules", "plain", "build-cmd"]);
  });

  it("does not hide a folded group that holds a search hit, and counts the hits", async () => {
    await act(async () => groupHead("user").click());
    expect(names()).toEqual(["build-cmd"]);
    await setValue(host!.querySelector<HTMLInputElement>('input[type="search"]')!, "plain");
    expect(names()).toEqual(["plain"]);
    expect(groupHead("user").getAttribute("aria-expanded")).toBe("true");
    expect(groupHead("user").querySelector(".memrail-group-count")?.textContent).toBe("1");
  });

  it("does not hide a folded group that holds the open memory", async () => {
    await act(async () => groupHead("project:" + project.id).click());
    expect(names()).toEqual(["rules", "plain"]);
    await act(async () =>
      useLayoutStore.getState().openTarget({ content: memoryEntryContent({ scope: "project", project, name: "build-cmd" }) }),
    );
    expect(names()).toContain("build-cmd");
  });

  it.each([["null"], ["[1]"], ['"x"'], ['{"user":"false"}'], ['{"user":1}']])("ignores a stored fold state of the wrong shape: %s", async (raw) => {
    localStorage.setItem("af.memrail-folded", raw);
    await act(async () => root!.unmount());
    root = createRoot(host!);
    await render(true);
    expect(names()).toEqual(["rules", "plain", "build-cmd"]);
  });

  it("drops a stale key from the stored state's effect and keeps a real fold", async () => {
    localStorage.setItem("af.memrail-folded", JSON.stringify({ gone: true, user: true }));
    await act(async () => root!.unmount());
    root = createRoot(host!);
    await render(true);
    expect(names()).toEqual(["build-cmd"]);
  });

  it("publishes the filter block's height, again after the section is closed and reopened", async () => {
    const body = () => host!.querySelector(".memrail-filter")!.parentElement!;
    expect(body().style.getPropertyValue("--memrail-filter-h")).toBe("0px"); // jsdom lays nothing out
    const toggle = () =>
      Array.from(host!.querySelectorAll<HTMLButtonElement>("button.ui-section-toggle")).find((b) => b.textContent?.includes(t("mem.view_memories")))!;
    await act(async () => toggle().click());
    expect(host!.querySelector(".memrail-filter")).toBeNull();
    await act(async () => toggle().click());
    expect(body().style.getPropertyValue("--memrail-filter-h")).toBe("0px");
    localStorage.removeItem("af-section-memory-list");
  });

  // jsdom has no layout, so the pinning is pinned in the stylesheet: section head, then the
  // filter block under it, then the group heads under both, z-index descending.
  const css = readFileSync("src/features/memory/memory-rail.css", "utf8");
  it("pins the filter under the section head and the group heads under the filter", () => {
    const rule = (sel: string) => css.match(new RegExp("(?:^|\\n)" + sel.replace(/[.]/g, "\\.") + " \\{([^}]*)\\}"))![1];
    const filter = rule(".memrail-filter");
    const head = rule(".memrail-group-head");
    expect(filter).toMatch(/position:\s*sticky/);
    expect(filter).toMatch(/top:\s*var\(--sec-head-h/);
    expect(head).toMatch(/position:\s*sticky/);
    expect(head).toMatch(/top:\s*calc\(var\(--sec-head-h[^)]*\)\s*\+\s*var\(--memrail-filter-h/);
    const z = (r: string) => Number(r.match(/z-index:\s*(\d+)/)![1]);
    expect(z(filter)).toBeGreaterThan(z(head));
    expect(z(filter)).toBeLessThan(3); // .ui-section-head is 3
  });
});
