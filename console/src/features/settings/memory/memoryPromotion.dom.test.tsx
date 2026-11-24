// The promotion panel (ADR 0130 decision 11): it lists a project's candidates and the held ones
// with what they miss, builds one proposal on request and shows its target, scans and patch, and
// supersedes the memory by the merged document quoting the proposal's revision and text digest.
// A proposal that is not ready still offers the supersede: the scans gate the pull request, and
// the supersede checks the merged document itself.
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

import { PromotionPanel } from "./memoryPromotion.tsx";
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
const candidates = {
  project: proj,
  ref: "origin/main",
  commit: "abc123",
  minSessions: 2,
  quietDays: 7,
  candidates: [
    {
      name: "release-steps",
      revision: 3,
      kind: "procedure",
      sessions: 2,
      anchors: ["scripts/release.sh"],
      updated: "",
    },
  ],
  held: [
    {
      name: "young",
      revision: 1,
      kind: "lesson",
      missing: [{ rule: "quiet", detail: "2 of 7 days" }],
    },
  ],
};
const digest = "a".repeat(64);
const proposal = (ready: boolean) => ({
  project: proj,
  ref: "origin/main",
  commit: "abc123",
  target: {
    repo: "github.com/acme/demo",
    path: "AGENTS.md",
    heading: "Release",
  },
  fileExists: true,
  sectionExists: false,
  sources: [
    {
      name: "release-steps",
      scope: "project",
      revision: 3,
      bodySha256: digest,
    },
  ],
  text: "Run scripts/release.sh from the root.",
  patch: "--- a/AGENTS.md\n+++ b/AGENTS.md\n+## Release\n",
  warnings: ready ? [] : ["memory-links"],
  scans: {
    secret: [],
    preCommit: ready
      ? { ran: true, passed: true }
      : { ran: true, passed: false, output: "gitleaks: leak found" },
  },
  ready,
});

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
        <PromotionPanel reload={0} />
      </ToastProvider>,
    );
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
    if (path.startsWith("api/agents/memory/entries/promotion-candidates"))
      return candidates;
    return {};
  });
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

describe("PromotionPanel", () => {
  it("lists the first project's candidates and the held ones with what they miss", async () => {
    await mount();
    expect(api).toHaveBeenCalledWith(
      "api/agents/memory/entries/promotion-candidates?project=" + proj.id,
    );
    const text = document.body.textContent ?? "";
    expect(text).toContain("release-steps");
    expect(text).toContain("2 sessions confirmed");
    expect(text).toContain("confirmed by 2 or more sessions");
    expect(text).toContain("Not yet (1)");
    expect(text).toContain("quiet: 2 of 7 days");
  });

  it("proposes, shows the change, and supersedes with the quoted revision and digest", async () => {
    apiJSON.mockResolvedValueOnce(proposal(true));
    await mount();
    await click(button("Propose"));
    expect(apiJSON).toHaveBeenCalledWith(
      "api/agents/memory/entries/promotion-proposal?project=" + proj.id,
      "POST",
      { project: proj.id, name: "release-steps" },
    );
    const text = document.body.textContent ?? "";
    expect(text).toContain("Into AGENTS.md, section “Release”");
    expect(text).toContain("new section");
    expect(text).toContain(
      "Ready: the secret scan and the pre-commit hook passed.",
    );
    expect(text).toContain("Run scripts/release.sh from the root.");

    const supersede = button("Supersede by the document");
    expect(supersede.disabled).toBe(true); // no commit yet
    const input = document.body.querySelector<HTMLInputElement>(
      ".mem-promo-merged input",
    )!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(
        HTMLInputElement.prototype,
        "value",
      )!.set!.call(input, "deadbeef12");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await flush();
    apiJSON.mockResolvedValueOnce({ name: "release-steps", revision: 4 });
    const before = api.mock.calls.length;
    await click(button("Supersede by the document"));
    expect(apiJSON).toHaveBeenLastCalledWith(
      "api/agents/memory/entries/supersede-document?project=" + proj.id,
      "POST",
      {
        project: proj.id,
        name: "release-steps",
        revision: 3,
        path: "AGENTS.md",
        heading: "Release",
        commit: "deadbeef12",
        bodySha256: digest,
      },
    );
    // The candidates are read again: a superseded memory is no longer one.
    expect(api.mock.calls.length).toBeGreaterThan(before);
    expect(document.body.textContent).toContain("Superseded by AGENTS.md.");
  });

  it("shows why a proposal is not ready, and still offers the supersede after the merge", async () => {
    apiJSON.mockResolvedValueOnce(proposal(false));
    await mount();
    await click(button("Propose"));
    const text = document.body.textContent ?? "";
    expect(text).toContain("Not ready");
    expect(text).toContain("Pre-commit hook failed.");
    expect(text).toContain("gitleaks: leak found");
    expect(text).toContain("links other memories");
    expect(button("Supersede by the document")).toBeTruthy();
  });

  it("drops a proposal that answers after another project was chosen", async () => {
    const projB = { ...proj, id: "other-0123456789ab", display: "other" };
    api.mockImplementation(async (path: string) => {
      if (path.startsWith("api/agents/memory/entries/projects"))
        return {
          projects: [
            { project: proj, count: 3, orphaned: false, live: true },
            { project: projB, count: 1, orphaned: false, live: true },
          ],
        };
      if (path.startsWith("api/agents/memory/entries/promotion-candidates"))
        return candidates;
      return {};
    });
    let answer: (v: unknown) => void = () => {};
    apiJSON.mockImplementationOnce(() => new Promise((r) => (answer = r)));
    await mount();
    await click(button("Propose"));
    const sel = document.body.querySelector<HTMLSelectElement>("select")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(
        HTMLSelectElement.prototype,
        "value",
      )!.set!.call(sel, projB.id);
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await flush();
    await act(async () => answer(proposal(true)));
    await flush();
    // B lists a memory of the same name; A's proposal must not appear under it.
    expect(document.body.textContent).toContain("release-steps");
    expect(document.body.textContent).not.toContain("Into AGENTS.md");
    expect(button("Supersede by the document")).toBeUndefined();
  });

  it("offers no supersede when the secret scan withheld the target", async () => {
    const p = proposal(false);
    apiJSON.mockResolvedValueOnce({
      ...p,
      target: { repo: p.target.repo, path: "", heading: "" },
      text: undefined,
      patch: undefined,
      scans: {
        secret: [{ path: "proposal", line: 1, rule: "token", hint: "ghp_" }],
        preCommit: { ran: false, passed: false, note: "not run" },
      },
    });
    await mount();
    await click(button("Propose"));
    expect(document.body.textContent).toContain(
      "The secret scan flagged 1 line(s)",
    );
    expect(button("Supersede by the document")).toBeUndefined();
  });
});
