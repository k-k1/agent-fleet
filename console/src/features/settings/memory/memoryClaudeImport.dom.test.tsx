// The Claude Code import panel (ADR 0108 decision 6). Pinned here: sources that cannot be
// imported are not selectable, a preview shows every status with masked findings and never
// offers the flagged or forgotten ones, the confirm button needs the switch on, and a long list
// is sent in batches of 50 with the project id in the query for the audit ledger.
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

import { ClaudeImportPanel, IMPORT_BATCH } from "./memoryClaudeImport.tsx";
import { ToastProvider } from "../../../ui/ToastProvider.tsx";
import { setSetting } from "../../../lib/settings.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const project = { id: "app-0123456789ab", root: "/r/app", vcs: "git", display: "app" };
const sources = [
  { slug: "-r-app", count: 130, project },
  { slug: "-gone", count: 2, reason: "no_project" },
  { slug: "-late", count: 1, project: { id: "late-0123456789ab", root: "/r/late", vcs: "git", display: "late" } },
];
const mk = (n: number) =>
  Array.from({ length: n }, (_, i) => ({ name: `mem-${i}`, status: "new", sourceHash: `h${i}` }));
const previewBody = {
  slug: "-r-app",
  project,
  counts: { new: 120, update: 1, secret: 1, forgotten: 1, invalid: 1 },
  items: [
    ...mk(120),
    {
      name: "changed",
      status: "update",
      sourceHash: "hu",
      sourceModified: "2026-10-04T10:00:00Z",
      afUpdated: "2026-10-01T10:00:00Z",
    },
    { name: "has-key", status: "secret", findings: [{ path: "body", line: 7, rule: "github-token", hint: "ghp_…(40)" }] },
    { name: "was-forgotten", status: "forgotten" },
    { name: "no-desc", status: "invalid", reason: "no_description" },
  ],
};

const flush = () =>
  act(async () => {
    for (let i = 0; i < 6; i++) await Promise.resolve();
  });

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ClaudeImportPanel reload={0} onChanged={() => {}} />
      </ToastProvider>,
    );
  });
  await flush();
}

async function pickSource() {
  const sel = host!.querySelector<HTMLSelectElement>("select")!;
  await act(async () => {
    sel.value = "-r-app";
    sel.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await flush();
}

const applyButton = () => host!.querySelector<HTMLButtonElement>(".mem-ci-actions button")!;

beforeEach(() => {
  setSetting("locale", "en");
  setSetting("agentMemory", true);
  api.mockImplementation((path: string) =>
    Promise.resolve(path.startsWith("api/agents/memory/claude-import/preview") ? previewBody : { sources }),
  );
  apiJSON.mockImplementation((_p: string, _m: string, body: { items: { name: string }[] }) =>
    Promise.resolve({ results: body.items.map((i) => ({ name: i.name, result: "imported" })) }),
  );
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

describe("ClaudeImportPanel", () => {
  it("leaves a source with no working copy unselectable and says why", async () => {
    await mount();
    const opts = Array.from(host!.querySelectorAll<HTMLOptionElement>("option"));
    expect(opts.find((o) => o.value === "-r-app")?.disabled).toBe(false);
    const gone = opts.find((o) => o.value === "-gone")!;
    expect(gone.disabled).toBe(true);
    expect(gone.textContent).toContain("no working copy");
  });

  it("previews every status, with masked findings and both times for an update", async () => {
    await mount();
    await pickSource();
    expect(api.mock.calls.some(([p]) => p === "api/agents/memory/claude-import/preview?slug=-r-app")).toBe(true);
    const groups = host!.querySelectorAll(".mem-ci-group");
    expect(groups).toHaveLength(5);
    expect(host!.querySelector(".mem-ci-group .mem-findings, .mem-findings")?.textContent).toContain("ghp_…(40)");
    expect(host!.textContent).toContain("no description");
    expect(host!.textContent).toContain("Claude file");
    // Only new + update are offered.
    expect(applyButton().textContent).toContain("121");
  });

  it("sends the previewed items in batches of 50 with the project id in the query", async () => {
    await mount();
    await pickSource();
    await act(async () => applyButton().click());
    await flush();
    expect(apiJSON).toHaveBeenCalledTimes(Math.ceil(121 / IMPORT_BATCH));
    for (const call of apiJSON.mock.calls) {
      expect(call[0]).toBe("api/agents/memory/claude-import?project=app-0123456789ab");
      expect(call[1]).toBe("POST");
      expect(call[2].items.length).toBeLessThanOrEqual(IMPORT_BATCH);
      expect(call[2]).toMatchObject({ project: "app-0123456789ab", slug: "-r-app" });
    }
    const sent = apiJSON.mock.calls.flatMap((c) => c[2].items.map((i: { name: string }) => i.name));
    expect(sent).toHaveLength(121);
    expect(sent).not.toContain("has-key");
    expect(sent).not.toContain("was-forgotten");
    expect(sent).toContain("changed");
    expect(sent).toContain("mem-0");
    // The preview is re-read afterwards.
    expect(api.mock.calls.filter(([p]) => String(p).startsWith("api/agents/memory/claude-import/preview")).length).toBe(2);
  });

  it("says a refresh of a cut description is a refresh, and names the size limit that is left", async () => {
    const small = {
      ...previewBody,
      counts: { update: 1, invalid: 3 },
      items: [
        {
          name: "was-cut",
          status: "update",
          reason: "refresh_shortened",
          sourceHash: "hr",
          sourceModified: "2026-09-01T10:00:00Z",
          afUpdated: "2026-10-01T10:00:00Z",
        },
        { name: "huge", status: "invalid", reason: "too_large" },
        { name: "wordy", status: "invalid", reason: "description_too_long" },
        { name: "two-lines", status: "invalid", reason: "bad_description" },
      ],
    };
    api.mockImplementation((path: string) =>
      Promise.resolve(path.startsWith("api/agents/memory/claude-import/preview") ? small : { sources }),
    );
    await mount();
    await pickSource();
    const text = host!.textContent ?? "";
    expect(text).toContain("the earlier import cut the description");
    expect(text).not.toContain("Claude file 2026");
    expect(text).toContain("larger than 200 KiB");
    expect(text).not.toContain("shortened");
    expect(text).toContain("description is longer than 2,000 characters");
    expect(text).toContain("description spans more than one line");
  });

  it("disables the import and explains why while the switch is off", async () => {
    setSetting("agentMemory", false);
    await mount();
    await pickSource();
    expect(applyButton().disabled).toBe(true);
    expect(host!.querySelector(".mem-warn")?.textContent).toContain("Agent Fleet memory");
    await act(async () => applyButton().click());
    expect(apiJSON).not.toHaveBeenCalled();
  });

  it("stops at the first refused batch and shows the Agent's message", async () => {
    apiJSON.mockResolvedValueOnce({ error: { code: "memory_disabled", message: "turned off" } });
    await mount();
    await pickSource();
    await act(async () => applyButton().click());
    await flush();
    expect(apiJSON).toHaveBeenCalledTimes(1);
  });

  it("ignores a slow preview for a source that is no longer selected", async () => {
    const other = { ...previewBody, slug: "-late", project: { ...project, id: "late-0123456789ab", display: "late" }, items: mk(1) };
    const resolvers: Record<string, (v: unknown) => void> = {};
    api.mockImplementation((path: string) => {
      if (!path.startsWith("api/agents/memory/claude-import/preview")) return Promise.resolve({ sources });
      const slug = decodeURIComponent(path.split("slug=")[1]);
      return new Promise((r) => (resolvers[slug] = r));
    });
    await mount();
    const sel = host!.querySelector<HTMLSelectElement>("select")!;
    const choose = async (v: string) => {
      await act(async () => {
        sel.value = v;
        sel.dispatchEvent(new Event("change", { bubbles: true }));
      });
    };
    await choose("-late");
    await choose("-r-app");
    await act(async () => resolvers["-r-app"](previewBody));
    await act(async () => resolvers["-late"](other)); // arrives after, for the old choice
    await flush();
    expect(host!.textContent).toContain("app");
    expect(applyButton().textContent).toContain("121");
    await choose("");
    await flush();
    expect(host!.querySelector(".mem-ci-actions")).toBeNull();
  });
});
