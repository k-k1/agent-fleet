// The lint panel (ADR 0130 decision 5, step 1): it asks only for reads, shows each check's
// findings and what was skipped, re-runs for the chosen scope, and saves the member's
// retired-term list through the one write it has.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { LintPanel } from "./memoryLint.tsx";
import { ToastProvider } from "../../../ui/ToastProvider.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const proj = {
  id: "demo-0123456789ab",
  root: "/r/demo",
  vcs: "git",
  display: "demo",
};
const projectReport = {
  scope: "project",
  project: proj,
  ref: "origin/main",
  commit: "abc123",
  terms: 1,
  report: {
    checked: 3,
    counts: { "broken-link": 1, oversize: 1 },
    skipped: [],
    findings: [
      { memory: "links", check: "broken-link", subject: "ghost", line: 3 },
      { memory: "big", check: "oversize", subject: "5000" },
    ],
  },
};
const userReport = {
  scope: "user",
  terms: 1,
  report: {
    checked: 1,
    counts: {},
    findings: [],
    skipped: [
      "repository checks (paths, symbols, ADRs): user scope: no repository",
    ],
  },
};

const flush = () =>
  act(async () => {
    for (let i = 0; i < 6; i++) await Promise.resolve();
  });

const mount = async () => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <LintPanel reload={0} />
      </ToastProvider>,
    );
  });
  await flush();
};

beforeEach(() => {
  api.mockImplementation(async (path: string) => {
    if (path.startsWith("api/agents/memory/entries/projects"))
      return {
        projects: [{ project: proj, count: 3, orphaned: false, live: true }],
      };
    if (path.startsWith("api/agents/memory/entries/lint-terms"))
      return { terms: ["muse", "old-thing"] };
    if (path.includes("scope=user")) return userReport;
    return projectReport;
  });
  apiJSON.mockResolvedValue({ terms: ["alpha"] });
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
  document.body.innerHTML = "";
});

const select = () => document.body.querySelector<HTMLSelectElement>("select")!;
const setValue = async (
  el: HTMLSelectElement | HTMLTextAreaElement,
  value: string,
) => {
  const proto =
    el instanceof HTMLSelectElement
      ? HTMLSelectElement.prototype
      : HTMLTextAreaElement.prototype;
  await act(async () => {
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, value);
    el.dispatchEvent(
      new Event(el instanceof HTMLSelectElement ? "change" : "input", {
        bubbles: true,
      }),
    );
  });
  await flush();
};

describe("LintPanel", () => {
  it("shows the user scope's report, with what was skipped, and writes nothing", async () => {
    await mount();
    expect(api).toHaveBeenCalledWith(
      "api/agents/memory/entries/lint?scope=user",
    );
    expect(document.body.textContent).toContain("repository checks");
    expect(document.body.textContent).toMatch(
      /Nothing to report|指摘はありません/,
    );
    expect(apiJSON).not.toHaveBeenCalled();
  });

  it("lists each finding of a project with its line and the ref it read", async () => {
    await mount();
    await setValue(select(), proj.id);
    expect(api).toHaveBeenCalledWith(
      `api/agents/memory/entries/lint?scope=project&project=${proj.id}`,
    );
    const text = document.body.textContent ?? "";
    expect(text).toContain("ghost");
    expect(text).toContain(":3");
    expect(text).toContain("5000");
    expect(text).toContain("origin/main");
    expect(text).toContain("abc123");
    expect(apiJSON).not.toHaveBeenCalled();
  });

  it("saves the retired-term list as typed, one term per line", async () => {
    await mount();
    const ta = document.body.querySelector<HTMLTextAreaElement>("textarea")!;
    expect(ta.value).toBe("muse\nold-thing");
    await setValue(ta, "alpha\n\nbeta");
    const save = Array.from(document.body.querySelectorAll("button")).find(
      (b) => /^(Save|保存)$/.test(b.textContent ?? ""),
    )!;
    await act(async () => save.click());
    await flush();
    expect(apiJSON).toHaveBeenCalledWith(
      "api/agents/memory/entries/lint-terms",
      "POST",
      { terms: ["alpha", "", "beta"] },
    );
  });
});
