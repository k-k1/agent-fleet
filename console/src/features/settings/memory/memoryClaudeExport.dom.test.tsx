// The Claude Code write-back panel (#1914). Pinned here: a project with no recorded main working
// copy is not selectable, a conflict has a checkbox that is the only way it gets named in the
// request (a locked one has none that works, a secret has none at all), the preview's token goes
// back with the apply, the project id rides in the query for the audit ledger, and the apply
// works with the AF memory switch off.
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

import { ClaudeExportPanel } from "./memoryClaudeExport.tsx";
import { ToastProvider } from "../../../ui/ToastProvider.tsx";
import { setSetting } from "../../../lib/settings.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const project = { id: "app-0123456789ab", root: "/r/app", vcs: "git", display: "app" };
const sources = {
  projects: [
    { project, count: 4 },
    { project: { id: "x-0123456789ab", root: "", vcs: "", display: "x" }, count: 1, reason: "no_root" },
  ],
};
const previewBody = {
  project,
  slug: "-r-app",
  token: "tok-1",
  nativeExists: true,
  userScope: 2,
  index: "rewrite",
  indexListed: 5,
  indexMore: 0,
  switchOn: false,
  counts: { new: 1, conflict: 3, secret: 1, native_only: 1 },
  items: [
    { name: "fresh", status: "new" },
    { name: "clash", status: "conflict", reason: "not_written_by_af" },
    { name: "linked", status: "conflict", reason: "symlink" },
    { name: "clash-two", status: "conflict", reason: "changed_since_write" },
    { name: "has-key", status: "secret", findings: [{ path: "body", line: 7, rule: "github-token", hint: "ghp_…(40)" }] },
    { name: "native_note", status: "native_only" },
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
        <ClaudeExportPanel reload={0} />
      </ToastProvider>,
    );
  });
  await flush();
}

async function pick() {
  const sel = host!.querySelector<HTMLSelectElement>("select")!;
  await act(async () => {
    sel.value = project.id;
    sel.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await flush();
}

const applyButton = () => host!.querySelector<HTMLButtonElement>(".mem-ci-actions button")!;
const box = (name: string) =>
  host!.querySelector<HTMLInputElement>(`input[type=checkbox][aria-label="Replace ${name} with the Agent Fleet version"]`);

beforeEach(() => {
  setSetting("locale", "en");
  setSetting("agentMemory", false);
  api.mockImplementation((path: string) =>
    Promise.resolve(path.startsWith("api/agents/memory/claude-export/preview") ? previewBody : sources),
  );
  apiJSON.mockImplementation(() => Promise.resolve({ results: [{ name: "fresh", result: "written" }] }));
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

describe("ClaudeExportPanel", () => {
  it("leaves a project with no recorded working copy unselectable", async () => {
    await mount();
    const opts = Array.from(host!.querySelectorAll<HTMLOptionElement>("option"));
    expect(opts.find((o) => o.value === project.id)?.disabled).toBe(false);
    expect(opts.find((o) => o.value === "x-0123456789ab")?.disabled).toBe(true);
  });

  it("offers a checkbox for a conflict only, disabled for a locked one", async () => {
    await mount();
    await pick();
    expect(box("clash")?.disabled).toBe(false);
    expect(box("clash-two")?.disabled).toBe(false);
    expect(box("linked")?.disabled).toBe(true);
    expect(box("fresh")).toBeNull();
    expect(box("has-key")).toBeNull();
    expect(host!.querySelector(".mem-findings")?.textContent).toContain("ghp_…(40)");
    expect(host!.textContent).toContain("2 user-scope");
  });

  it("sends the token, the ticked names only and the project id in the query, with the switch off", async () => {
    await mount();
    await pick();
    expect(applyButton().disabled).toBe(false);
    await act(async () => box("clash")!.click());
    await act(async () => applyButton().click());
    await flush();
    expect(apiJSON).toHaveBeenCalledTimes(1);
    const [path, method, body] = apiJSON.mock.calls[0];
    expect(path).toBe("api/agents/memory/claude-export?project=app-0123456789ab");
    expect(method).toBe("POST");
    expect(body).toEqual({ project: project.id, token: "tok-1", overwrite: ["clash"] });
    // The preview is re-read afterwards.
    expect(api.mock.calls.filter(([p]) => String(p).startsWith("api/agents/memory/claude-export/preview")).length).toBe(2);
  });

  it("warns while the switch is on", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(path.includes("/preview") ? { ...previewBody, switchOn: true } : sources),
    );
    await mount();
    await pick();
    expect(host!.querySelector(".mem-warn")?.textContent).toContain("started before");
  });

  it("shows the Agent's message when the apply is refused", async () => {
    apiJSON.mockResolvedValueOnce({ error: { code: "memory_conflict", message: "changed since the preview" } });
    await mount();
    await pick();
    await act(async () => applyButton().click());
    await flush();
    expect(apiJSON).toHaveBeenCalledTimes(1);
  });
});
