// One memory in a pane of its own (ADR 0141 decision 4): read as Markdown with its metadata, edited
// with a form above the editor and saved through member-save at the revision it was read at, a
// secret refusal shown in place, a new memory becoming the pane of the memory it now is, forget and
// unforget retargeting the pane, and a draft that outlives a tab switch.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));
// The editor is CodeMirror; a textarea stands in so a test can type into the body.
vi.mock("../editor/CodeEditor.tsx", () => ({
  CodeEditor: ({ content, onChange }: { content: string; onChange(v: string): void }) => (
    <textarea data-testid="editor" value={content} onChange={(e) => onChange(e.target.value)} />
  ),
}));

import { MemoryEntryView, resetMemoryDrafts } from "./MemoryEntryView.tsx";
import { memoryEntryContent } from "./openEntry.ts";
import { useMemoryStore } from "./store.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { allViews, freshLayout } from "../../layout/ops.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { ToastProvider } from "../../ui/ToastProvider.tsx";
import { ConfirmProvider } from "../../ui/ConfirmProvider.tsx";

const { t } = await import("../../lib/i18n/index.ts");
const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;

const project = { id: "app-0123456789ab", display: "app" };
const memory = {
  name: "build-cmd",
  scope: "project",
  description: "how to build",
  type: "project",
  updated: "2026-10-02T00:00:00Z",
  pinned: false,
  uses: 2,
  revision: 4,
  authorKind: "member",
  authorSession: "console",
  kinds: ["claude"],
  body: "# Build\n\nRun `make`.",
};
const latest = { commit: "c9", at: "2026-10-03T00:00:00Z", op: "update", scope: "project", project, name: "build-cmd", authorKind: "member", authorSession: "", latest: true, live: true, revertible: true };

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const flush = async () => {
  for (let i = 0; i < 4; i++) await act(async () => {});
};
const button = (label: string) => Array.from(document.body.querySelectorAll<HTMLButtonElement>("button")).find((b) => b.textContent?.trim() === label);
const pane = () => allViews(useLayoutStore.getState().layout).find((v) => v.content.kind === "memoryEntry")!;
async function setValue(el: HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement, v: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), "value")!.set!.call(el, v);
    el.dispatchEvent(new Event(el instanceof HTMLSelectElement ? "change" : "input", { bubbles: true }));
  });
}
// Renders the pane the layout holds, as Pane.tsx does, so a retarget re-renders it.
function Host() {
  const p = useLayoutStore((s) => allViews(s.layout).find((v) => v.content.kind === "memoryEntry"));
  if (!p || p.content.kind !== "memoryEntry") return null;
  return <MemoryEntryView paneId={p.id} content={p.content} />;
}
async function open(content = memoryEntryContent({ scope: "project", project, name: "build-cmd" })) {
  await act(async () => useLayoutStore.getState().openTarget({ content }));
  await act(async () =>
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <Host />
        </ConfirmProvider>
      </ToastProvider>,
    ),
  );
  await flush();
}

beforeEach(() => {
  resetMemoryDrafts();
  useLayoutStore.setState({ layout: freshLayout() });
  // The API first: other stores react to the workspace starting and read through it.
  api.mockImplementation(async (url: string) => {
    if (url.startsWith("api/agents/memory/entries/member-read")) return memory;
    if (url.startsWith("api/agents/memory/entries/member-latest")) return latest;
    if (url.startsWith("api/agents/memory/entries/changes")) return { changes: [] };
    return {};
  });
  apiJSON.mockResolvedValue({});
  useWorkspaceStore.setState({ state: "running" });
  useMemoryStore.setState({ version: 0 });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  host?.remove();
  root = host = null;
  api.mockReset();
  api.mockResolvedValue({});
  apiJSON.mockReset();
});

describe("MemoryEntryView", () => {
  it("reads one memory and renders its body as Markdown, with its metadata", async () => {
    await open();
    expect(api.mock.calls.map((c) => c[0])).toContain(`api/agents/memory/entries/member-read?scope=project&project=${project.id}&name=build-cmd`);
    expect(host!.querySelector(".mentry-md h1")?.textContent).toBe("Build");
    expect(host!.querySelector(".mentry-where")?.textContent).toBe("app");
    expect(host!.querySelector(".mentry-meta")?.textContent).toContain("project");
    expect(host!.textContent).toContain("how to build");
  });

  it("saves an edit through member-save at the revision it was read at, with the kinds kept", async () => {
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    await setValue(host!.querySelector<HTMLInputElement>(".mentry-desc input")!, "how to build it");
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "Run `make all`.");
    const before = useMemoryStore.getState().version;
    await act(async () => button(t("mem.br_save"))!.click());
    await flush();
    expect(apiJSON).toHaveBeenCalledWith(`api/agents/memory/entries/member-save?project=${project.id}`, "POST", {
      scope: "project",
      project: project.id,
      name: "build-cmd",
      description: "how to build it",
      type: "project",
      kinds: ["claude"],
      body: "Run `make all`.",
      revision: 4,
    });
    expect(host!.querySelector(".mentry-form")).toBeNull();
    expect(useMemoryStore.getState().version).toBeGreaterThan(before);
  });

  it("shows a secret refusal in place and keeps the draft", async () => {
    apiJSON.mockResolvedValue({ error: { code: "memory_secret_detected", message: "secret" }, findings: [{ rule: "aws-key", line: 3, hint: "AKIA…" }] });
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    await act(async () => button(t("mem.br_save"))!.click());
    await flush();
    expect(host!.querySelector('[role="alert"]')?.textContent).toBe(t("mem.br_secret"));
    expect(host!.querySelector(".mem-findings")?.textContent).toContain("aws-key");
    expect(host!.querySelector(".mentry-form")).not.toBeNull();
  });

  it("keeps a draft across a tab switch (the view unmounts)", async () => {
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "half-written");
    await act(async () => root!.render(<></>));
    await act(async () =>
      root!.render(
        <ToastProvider>
          <ConfirmProvider>
            <Host />
          </ConfirmProvider>
        </ToastProvider>,
      ),
    );
    await flush();
    expect(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!.value).toBe("half-written");
  });

  it("opens a new memory in edit mode and becomes that memory's pane once saved", async () => {
    await open(memoryEntryContent({ scope: "user", name: "" }));
    expect(api).not.toHaveBeenCalledWith(expect.stringContaining("member-read"));
    await setValue(host!.querySelector<HTMLInputElement>(".mentry-fields input")!, "house-rules");
    await act(async () => button(t("mem.br_save"))!.click());
    await flush();
    expect(apiJSON.mock.calls[0][2]).toMatchObject({ scope: "user", project: "", name: "house-rules", revision: 0 });
    expect(pane().content).toEqual({ kind: "memoryEntry", scope: "user", project: "", name: "house-rules" });
  });

  it("forgets through the newest change's revert, pinned to the revision on screen, and turns the pane read-only", async () => {
    await open();
    await act(async () => button(t("mem.af_forget"))!.click());
    await flush();
    await act(async () => document.body.querySelector<HTMLButtonElement>(".ui-confirm-actions button:last-child")!.click());
    await flush();
    expect(apiJSON).toHaveBeenCalledWith("api/agents/memory/entries/revert?commit=c9", "POST", { commit: "c9", forget: true, ack: false, expectRevision: 4 });
    expect(pane().content).toMatchObject({ name: "build-cmd", gone: true });
  });

  it("reads a forgotten memory from its last change and brings it back", async () => {
    api.mockImplementation(async (url: string) =>
      url.startsWith("api/agents/memory/entries/member-latest") ? { ...latest, op: "forget", live: false } : url.includes("changes") ? { changes: [] } : {},
    );
    await open(memoryEntryContent({ scope: "project", project, name: "build-cmd" }, true));
    expect(api.mock.calls.some((c) => String(c[0]).includes("member-read"))).toBe(false);
    expect(button(t("mem.br_edit"))).toBeUndefined();
    await act(async () => button(t("mem.br_unforget"))!.click());
    await flush();
    expect(apiJSON).toHaveBeenCalledWith("api/agents/memory/entries/revert?commit=c9", "POST", { commit: "c9", forget: false, ack: false });
    expect(pane().content).toEqual({ kind: "memoryEntry", scope: "project", project: project.id, projectName: "app", name: "build-cmd" });
  });

  it("toggles the pin for its own scope and project", async () => {
    await open();
    await act(async () => button(t("mem.pin_pin"))!.click());
    await flush();
    expect(apiJSON).toHaveBeenCalledWith(`api/agents/memory/entries/pin?project=${project.id}`, "POST", {
      scope: "project",
      project: project.id,
      name: "build-cmd",
      pinned: true,
    });
  });

  it("offers Start while the workspace is stopped, and asks the Agent nothing", async () => {
    useWorkspaceStore.setState({ state: "stopped" });
    await open();
    expect(host!.textContent).toContain(t("mem.ws_required_title"));
    expect(api.mock.calls.some((c) => String(c[0]).includes("member-read"))).toBe(false);
  });
});
