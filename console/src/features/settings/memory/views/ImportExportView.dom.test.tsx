// The Import & export hub. Pinned here: one card per source under Import / Export / Maintenance,
// Start opens that source's wizard in place of the hub, Cancel and Back return to it, one
// wizard's state does not survive into the next, and an import started from the hub still
// refreshes the rest of Agent memory through onChanged.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { ImportExportView } from "./ImportExportView.tsx";
import { ToastProvider } from "../../../../ui/ToastProvider.tsx";
import { setSetting } from "../../../../lib/settings.ts";
import { t } from "../../../../lib/i18n/index.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const flush = () =>
  act(async () => {
    for (let i = 0; i < 6; i++) await Promise.resolve();
  });
const proj = { id: "demo-1", display: "demo", root: "/r/demo", vcs: "git" };

beforeEach(() => {
  setSetting("agentMemory", true);
  api.mockImplementation(async (url: string) => {
    if (url === "api/agents/memory/claude-import") return { sources: [{ slug: "-r-demo", count: 1, project: proj }] };
    if (url.startsWith("api/agents/memory/claude-import/preview"))
      return { slug: "-r-demo", project: proj, counts: { new: 1 }, items: [{ name: "n1", status: "new", sourceHash: "h1" }] };
    if (url === "api/agents/memory/muse-import") return { sources: [] };
    if (url === "api/agents/memory/claude-export") return { projects: [] };
    if (url === "api/agents/memory/entries/projects") return { projects: [] };
    return {};
  });
  apiJSON.mockResolvedValue({ results: [{ name: "n1", result: "imported" }] });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  api.mockReset();
  apiJSON.mockReset();
});
async function mount(onChanged = () => {}) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(<ToastProvider><ImportExportView reload={0} onChanged={onChanged} /></ToastProvider>));
  await flush();
}
const btn = (label: string) => Array.from(host!.querySelectorAll("button")).find((b) => b.textContent === label || b.getAttribute("aria-label") === label);
const click = async (label: string) => {
  await act(async () => btn(label)!.click());
  await flush();
};
const start = (title: Parameters<typeof t>[0]) => click(`${t("mem.ie_start")}: ${t(title)}`);
const h4s = () => Array.from(host!.querySelectorAll("h4")).map((h) => h.textContent);

describe("ImportExportView hub", () => {
  it("shows a card per source under Import, Export and Maintenance", async () => {
    await mount();
    expect(Array.from(host!.querySelectorAll(".mem-ie-group")).map((g) => [g.querySelector("h3")?.textContent, Array.from(g.querySelectorAll("h4")).map((h) => h.textContent)])).toEqual([
      [t("mem.ie_group_import"), [t("mem.ci_title"), t("mem.mi_title"), t("mem.bi_title")]],
      [t("mem.ie_group_export"), [t("mem.ce_title")]],
      [t("mem.ie_group_maintenance"), [t("mem.pm_title")]],
    ]);
    // Every card has a description and a Start button; no wizard has been loaded yet.
    expect(host!.querySelectorAll(".mem-ie-card")).toHaveLength(5);
    expect(host!.querySelectorAll(".mem-ie-card p")).toHaveLength(5);
    expect(host!.querySelectorAll(".mem-ie-card button")).toHaveLength(5);
    expect(api).not.toHaveBeenCalled();
  });

  it("replaces the hub with the wizard on Start and returns on Cancel", async () => {
    await mount();
    await start("mem.ci_title");
    expect(host!.querySelector(".mem-ie-card")).toBeNull();
    expect(h4s()).toEqual([t("mem.ci_title")]);
    await click(t("common.cancel"));
    expect(host!.querySelectorAll(".mem-ie-card")).toHaveLength(5);
  });

  it("opens each source's own wizard", async () => {
    const cases: [Parameters<typeof t>[0], string][] = [
      ["mem.ci_title", "claude-import"],
      ["mem.mi_title", "muse-import"],
      ["mem.ce_title", "claude-export"],
      ["mem.pm_title", "entries/projects"],
    ];
    for (const [title, endpoint] of cases) {
      api.mockClear();
      await mount();
      await start(title);
      expect(h4s()).toEqual([t(title)]);
      expect(api.mock.calls.map((c) => c[0])).toContain("api/agents/memory/" + endpoint);
      act(() => root!.unmount());
      host!.remove();
    }
    await mount();
    await start("mem.bi_title");
    expect(h4s()).toEqual([t("mem.bi_title")]);
    expect(host!.querySelector("input[type=file]")).toBeTruthy();
  });

  it("does not carry one wizard's picked source into the next", async () => {
    await mount();
    await start("mem.ci_title");
    const sel = host!.querySelector("select")!;
    await act(async () => {
      sel.value = "-r-demo";
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await flush();
    expect(host!.textContent).toContain("n1");
    await click(t("common.cancel"));
    await start("mem.mi_title");
    expect(host!.textContent).not.toContain("n1");
    await click(t("common.cancel"));
    await start("mem.ci_title");
    // A fresh wizard starts at the source step, with nothing previewed.
    expect(host!.textContent).not.toContain("n1");
    expect(host!.querySelector("select")!.value).toBe("");
  });

  it("refreshes Agent memory (onChanged) after an import and offers Back to the hub", async () => {
    const changed = vi.fn();
    await mount(changed);
    await start("mem.ci_title");
    const sel = host!.querySelector("select")!;
    await act(async () => {
      sel.value = "-r-demo";
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await flush();
    await click(t("mem.ci_apply", { n: 1 }));
    expect(apiJSON).toHaveBeenCalledTimes(1);
    expect(changed).toHaveBeenCalled();
    expect(btn(t("mem.wz_back"))).toBeTruthy();
    await click(t("mem.wz_back"));
    expect(host!.querySelectorAll(".mem-ie-card")).toHaveLength(5);
  });
});
