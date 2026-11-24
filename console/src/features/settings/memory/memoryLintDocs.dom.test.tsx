// The document-target part of the lint report (ADR 0130 decision 11): each flagged target is
// listed with its problem, a rename is followed in one click, any target can be re-pointed by
// hand or the memory restored, each repair quotes the revision the report showed and re-runs the
// report, and a refused repair says why without re-running.
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
import { setLocale } from "../../../lib/i18n/index.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const proj = {
  id: "demo-0123456789ab",
  root: "/r/demo",
  vcs: "git",
  display: "demo",
};
const target = {
  repo: "github.com/acme/demo",
  path: "AGENTS.md",
  heading: "Release",
  commit: "0123456789abcdef0123",
};
const report = {
  scope: "project",
  project: proj,
  ref: "origin/main",
  commit: "feedc0ffee12",
  terms: 0,
  report: {
    checked: 3,
    counts: { "restates-document": 1 },
    findings: [
      { memory: "same", check: "restates-document", subject: "docs/guide.md" },
    ],
  },
  documents: {
    checked: 3,
    findings: [
      {
        memory: "moved",
        revision: 4,
        target,
        problem: "renamed",
        path: "docs/agents.md",
        headingFound: true,
        commit: "aaaabbbbccccdddd",
      },
      {
        memory: "gone",
        revision: 2,
        target,
        problem: "reverted",
        commit: "1111222233334444",
      },
      {
        memory: "elsewhere",
        revision: 1,
        target: { ...target, repo: "github.com/other/repo" },
        problem: "other-repository",
      },
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

const lintCalls = () =>
  api.mock.calls.filter((c) =>
    String(c[0]).startsWith("api/agents/memory/entries/lint?"),
  ).length;

const chooseProject = async () => {
  const sel = document.body.querySelector<HTMLSelectElement>("select")!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(
      HTMLSelectElement.prototype,
      "value",
    )!.set!.call(sel, proj.id);
    sel.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await flush();
};

const button = (text: string) =>
  [...document.body.querySelectorAll("button")].find(
    (b) => b.textContent?.trim() === text,
  )!;

const click = async (el: Element) => {
  await act(async () => {
    (el as HTMLElement).click();
  });
  await flush();
};

beforeEach(() => {
  setLocale("en");
  api.mockImplementation(async (path: string) => {
    if (path.startsWith("api/agents/memory/entries/projects"))
      return {
        projects: [{ project: proj, count: 3, orphaned: false, live: true }],
      };
    if (path.startsWith("api/agents/memory/entries/lint-terms"))
      return { terms: [] };
    if (path.includes("scope=user"))
      return {
        scope: "user",
        terms: 0,
        report: { checked: 0, counts: {}, findings: [] },
      };
    return report;
  });
  apiJSON.mockResolvedValue({ name: "moved", revision: 5 });
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
  document.body.innerHTML = "";
  setLocale("ja");
});

describe("LintPanel document targets", () => {
  it("lists each flagged target and the redundancy finding", async () => {
    await mount();
    await chooseProject();
    const text = document.body.textContent ?? "";
    expect(text).toContain("Bodies a document they anchor already says");
    expect(text).toContain("docs/guide.md");
    expect(text).toContain("3 target(s) checked, 3 to repair.");
    expect(text).toContain("the file moved to docs/agents.md in aaaabbbbcccc");
    expect(text).toContain("the merge was reverted by 111122223333");
    expect(text).not.toContain("Nothing to report.");
    // Every row can be re-pointed, another repository's too: the API checks the new target
    // against this project.
    expect(
      [...document.body.querySelectorAll("button")].filter(
        (b) => b.textContent === "Re-point…",
      ),
    ).toHaveLength(3);
  });

  it("follows a rename in one click, quoting the revision, and re-runs the report", async () => {
    await mount();
    await chooseProject();
    const before = lintCalls();
    await click(button("Point at docs/agents.md"));
    expect(apiJSON).toHaveBeenCalledWith(
      "api/agents/memory/entries/document-retarget?project=" + proj.id,
      "POST",
      {
        project: proj.id,
        name: "moved",
        revision: 4,
        path: "docs/agents.md",
        heading: "Release",
        // The revision the report read, not the renaming commit (which may lack the heading).
        commit: "feedc0ffee12",
      },
    );
    expect(lintCalls()).toBe(before + 1);
  });

  it("re-points by hand with the report's commit as the default", async () => {
    await mount();
    await chooseProject();
    const repoint = [...document.body.querySelectorAll("button")].filter(
      (b) => b.textContent === "Re-point…",
    );
    await click(repoint[1]);
    const inputs = document.body.querySelectorAll<HTMLInputElement>(
      ".mem-doc-repoint input",
    );
    expect([...inputs].map((i) => i.value)).toEqual([
      "AGENTS.md",
      "Release",
      "feedc0ffee12",
    ]);
    await click(button("Re-point"));
    expect(apiJSON).toHaveBeenCalledWith(
      "api/agents/memory/entries/document-retarget?project=" + proj.id,
      "POST",
      {
        project: proj.id,
        name: "gone",
        revision: 2,
        path: "AGENTS.md",
        heading: "Release",
        commit: "feedc0ffee12",
      },
    );
  });

  it("restores a memory, and a refused repair is shown without re-running", async () => {
    await mount();
    await chooseProject();
    apiJSON.mockResolvedValueOnce({
      error: {
        code: "memory_conflict",
        status: 409,
        message: "this memory is at revision 3, not the 2 the report showed",
      },
    });
    const before = lintCalls();
    const restore = [...document.body.querySelectorAll("button")].filter(
      (b) => b.textContent === "Bring the memory back",
    );
    await click(restore[1]);
    expect(apiJSON).toHaveBeenCalledWith(
      "api/agents/memory/entries/document-restore?project=" + proj.id,
      "POST",
      { project: proj.id, name: "gone", revision: 2 },
    );
    expect(lintCalls()).toBe(before);
    expect(document.body.textContent).toContain("revision 3");
  });

  it("offers no one-click re-point for a target stored without a heading", async () => {
    api.mockImplementation(async (path: string) => {
      if (path.startsWith("api/agents/memory/entries/projects"))
        return {
          projects: [{ project: proj, count: 3, orphaned: false, live: true }],
        };
      if (path.startsWith("api/agents/memory/entries/lint-terms"))
        return { terms: [] };
      return {
        ...report,
        report: { checked: 1, counts: {}, findings: [] },
        documents: {
          checked: 1,
          findings: [
            {
              memory: "moved",
              revision: 4,
              target: {
                repo: target.repo,
                path: "AGENTS.md",
                commit: target.commit,
              },
              problem: "renamed",
              path: "docs/agents.md",
              headingFound: true,
              commit: "aaaabbbbccccdddd",
            },
          ],
        },
      };
    });
    await mount();
    await chooseProject();
    expect(button("Point at docs/agents.md")).toBeUndefined();
    expect(button("Re-point…")).toBeTruthy();
  });

  it("does not call a report clean when a target was left unjudged", async () => {
    api.mockImplementation(async (path: string) => {
      if (path.startsWith("api/agents/memory/entries/projects"))
        return {
          projects: [{ project: proj, count: 3, orphaned: false, live: true }],
        };
      if (path.startsWith("api/agents/memory/entries/lint-terms"))
        return { terms: [] };
      return {
        ...report,
        report: {
          checked: 1,
          counts: {},
          findings: [],
          skipped: [
            "document target of moved: the document is too large to read its headings",
          ],
        },
        documents: { checked: 0, findings: [] },
      };
    });
    await mount();
    await chooseProject();
    expect(document.body.textContent).toContain("too large");
    expect(document.body.textContent).not.toContain("Nothing to report.");
  });
});
