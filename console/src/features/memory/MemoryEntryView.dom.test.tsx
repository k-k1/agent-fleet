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
// Every path it is given is kept: the real editor is rebuilt (undo lost) whenever its path changes.
const editorPaths: string[] = [];
vi.mock("../editor/CodeEditor.tsx", () => ({
  CodeEditor: ({ path, content, onChange }: { path: string; content: string; onChange(v: string): void }) => {
    if (editorPaths.at(-1) !== path) editorPaths.push(path);
    return <textarea data-testid="editor" value={content} onChange={(e) => onChange(e.target.value)} />;
  },
}));

import { MemoryEntryView, resetMemoryDrafts } from "./MemoryEntryView.tsx";
import { resetMemoryRevisions } from "./memoryEntryIO.ts";
import { clearEditorSlotsForTests } from "../editor/useFileEditor.ts";
import { clearDirtyRegistryForTests, dirtyPaneIds } from "../editor/dirtyRegistry.ts";
import { memoryEntryContent } from "./openEntry.ts";
import { useMemoryStore } from "./store.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { allViews, freshLayout } from "../../layout/ops.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
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
  resetMemoryRevisions();
  clearEditorSlotsForTests();
  clearDirtyRegistryForTests();
  editorPaths.length = 0;
  useTenantStore.setState({ tenant: "t1" });
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
    apiJSON.mockResolvedValue({ name: "build-cmd", revision: 5 });
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
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "token AKIA…");
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
    expect(api.mock.calls.some((c) => String(c[0]).startsWith("api/agents/memory"))).toBe(false);
  });

  it("brings the history into view when it is opened, since its toggle is at the foot", async () => {
    const scrolled: Element[] = [];
    const orig = Element.prototype.scrollIntoView;
    Element.prototype.scrollIntoView = function (this: Element) {
      scrolled.push(this);
    };
    try {
      await open();
      await act(async () => button(t("mem.rv_history"))!.click());
      await flush();
      expect(scrolled.some((el) => el.classList.contains("mentry-history"))).toBe(true);
      // Named once, by its toggle: the section does not repeat the title under it.
      const section = host!.querySelector(".mentry-history section")!;
      expect(section.querySelector("h5")).toBeNull();
      expect(document.getElementById(section.getAttribute("aria-labelledby")!)?.textContent?.trim()).toBe(t("mem.rv_history"));
    } finally {
      Element.prototype.scrollIntoView = orig;
    }
  });

  it("reads the memory's history only once it is opened", async () => {
    await open();
    const historyReads = () => api.mock.calls.filter((c) => String(c[0]).startsWith("api/agents/memory/entries/changes")).length;
    expect(historyReads()).toBe(0);
    await act(async () => useMemoryStore.getState().bump());
    await flush();
    expect(historyReads()).toBe(0);
    await act(async () => button(t("mem.rv_history"))!.click());
    await flush();
    expect(historyReads()).toBe(1);
  });

  it("keeps one editor while a new memory's name is typed", async () => {
    await open(memoryEntryContent({ scope: "user", name: "" }));
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "body first");
    await setValue(host!.querySelector<HTMLInputElement>(".mentry-fields input")!, "h");
    await setValue(host!.querySelector<HTMLInputElement>(".mentry-fields input")!, "ho");
    expect(editorPaths).toHaveLength(1);
  });

  it("shows nothing of a pinned memory once it is forgotten", async () => {
    api.mockImplementation(async (url: string) => {
      if (url.startsWith("api/agents/memory/entries/member-read")) return { ...memory, pinned: true };
      if (url.startsWith("api/agents/memory/entries/member-latest")) return pending;
      return {};
    });
    // The forgotten read is held, so what the pane shows meanwhile is what it carried over.
    let pending: unknown = latest;
    await open();
    expect(host!.querySelector(".view-head")?.textContent).toContain(t("mem.br_pinned"));
    await act(async () => button(t("mem.af_forget"))!.click());
    await flush();
    pending = new Promise(() => {});
    await act(async () => document.body.querySelector<HTMLButtonElement>(".ui-confirm-actions button:last-child")!.click());
    await flush();
    expect(pane().content).toMatchObject({ gone: true });
    expect(host!.querySelector(".view-head")?.textContent).not.toContain(t("mem.br_pinned"));
    expect(host!.querySelector(".mentry-md")).toBeNull();
    expect(button(t("mem.br_edit"))).toBeUndefined();
  });

  it("opens a forgotten memory read-only even with a draft left from before the forget", async () => {
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "left behind");
    api.mockImplementation(async (url: string) =>
      url.startsWith("api/agents/memory/entries/member-latest") ? { ...latest, op: "forget", live: false } : {},
    );
    await act(async () => useLayoutStore.getState().setPaneTarget(pane().id, { content: memoryEntryContent({ scope: "project", project, name: "build-cmd" }, true) }));
    await flush();
    expect(host!.querySelector(".mentry-form")).toBeNull();
    expect(button(t("mem.br_unforget"))).toBeTruthy();
  });

  it("asks the Agent once for a forget pressed twice, and allows no edit while it is pending", async () => {
    await open();
    // Both clicks land before React re-renders the button as disabled: only the ref can stop the second.
    const forgetButton = button(t("mem.af_forget"))!;
    await act(async () => {
      forgetButton.click();
      forgetButton.click();
    });
    await flush();
    expect(api.mock.calls.filter((c) => String(c[0]).startsWith("api/agents/memory/entries/member-latest"))).toHaveLength(1);
    expect(button(t("mem.br_edit"))!.disabled).toBe(true);
  });

  it("joins the pane the saved memory already has, rather than laying a second beside it", async () => {
    useLayoutStore.setState({ layout: { ...freshLayout(), mode: "tabs" } as ReturnType<typeof freshLayout> });
    const existing = memoryEntryContent({ scope: "user", name: "house-rules" });
    await act(async () => useLayoutStore.getState().openTarget({ content: existing }));
    await act(async () => useLayoutStore.getState().openTargetInNew({ content: memoryEntryContent({ scope: "user", name: "" }) }));
    const fresh = allViews(useLayoutStore.getState().layout).find((v) => v.content.kind === "memoryEntry" && v.content.name === "")!;
    await act(async () =>
      root!.render(
        <ToastProvider>
          <ConfirmProvider>
            <MemoryEntryView paneId={fresh.id} content={fresh.content as typeof existing} />
          </ConfirmProvider>
        </ToastProvider>,
      ),
    );
    await flush();
    await setValue(host!.querySelector<HTMLInputElement>(".mentry-fields input")!, "house-rules");
    await act(async () => button(t("mem.br_save"))!.click());
    await flush();
    const entries = allViews(useLayoutStore.getState().layout).filter((v) => v.content.kind === "memoryEntry");
    expect(entries.map((v) => v.content)).toEqual([existing]);
  });

  it("closes a new memory's pane on Cancel", async () => {
    await open(memoryEntryContent({ scope: "user", name: "" }));
    await act(async () => button(t("mem.br_cancel"))!.click());
    expect(allViews(useLayoutStore.getState().layout).some((v) => v.content.kind === "memoryEntry")).toBe(false);
  });

  it("never offers one tenant's draft in another, and reads the memory again there", async () => {
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    const reads = () => api.mock.calls.filter((c) => String(c[0]).startsWith("api/agents/memory/entries/member-read")).length;
    const before = reads();
    await act(async () => useTenantStore.setState({ tenant: "t2" }));
    await flush();
    expect(host!.querySelector(".mentry-form")).toBeNull();
    expect(reads()).toBe(before + 1);
    expect(host!.querySelector(".mentry-md")).not.toBeNull();
  });

  it("keeps the purge for a forgotten memory that can no longer be brought back", async () => {
    api.mockImplementation(async (url: string) =>
      url.startsWith("api/agents/memory/entries/member-latest") ? { ...latest, op: "forget", live: false, revertible: false } : {},
    );
    await open(memoryEntryContent({ scope: "project", project, name: "build-cmd" }, true));
    expect(button(t("mem.br_unforget"))).toBeUndefined();
    expect(button(t("mem.pg_button"))).toBeTruthy();
  });

  it("states the pin as pressed or not, and offers Unforget only for a change that can bring it back", async () => {
    await open();
    expect(button(t("mem.pin_pin"))!.getAttribute("aria-pressed")).toBe("false");
    api.mockImplementation(async (url: string) => (url.startsWith("api/agents/memory/entries/member-latest") ? latest : {}));
    await act(async () => useLayoutStore.getState().setPaneTarget(pane().id, { content: memoryEntryContent({ scope: "project", project, name: "build-cmd" }, true) }));
    await flush();
    // `latest` is live: the memory is back already, there is nothing to unforget.
    expect(button(t("mem.br_unforget"))).toBeUndefined();
  });

  it("registers an edited memory with the dirty guards and clears them once saved", async () => {
    apiJSON.mockResolvedValue({ name: "build-cmd", revision: 5 });
    await open();
    expect(dirtyPaneIds()).toEqual([]);
    await act(async () => button(t("mem.br_edit"))!.click());
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "changed");
    expect(dirtyPaneIds()).toEqual([pane().id]);
    await act(async () => button(t("mem.br_save"))!.click());
    await flush();
    expect(dirtyPaneIds()).toEqual([]);
  });

  it("opens the resolution panel when the memory changed first, and adopting the remote text replaces the draft", async () => {
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "mine");
    apiJSON.mockResolvedValue({ error: { code: "memory_conflict", message: "changed" } });
    // The re-read the conflict flow makes finds the newer revision.
    api.mockImplementation(async (url: string) => (url.startsWith("api/agents/memory/entries/member-read") ? { ...memory, revision: 5, body: "theirs" } : {}));
    await act(async () => button(t("mem.br_save"))!.click());
    await flush();
    expect(host!.querySelector(".file-editor-resolution")).not.toBeNull();
    // The save button waits for the resolution.
    expect(button(t("mem.br_save"))!.disabled).toBe(true);
    await act(async () => button(t("editor.conflict.adopt_remote"))!.click());
    await flush();
    expect(host!.querySelector(".file-editor-resolution")).toBeNull();
    expect(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!.value).toBe("theirs");
  });

  it("saves the counter that belongs to the base it was edited from, not the latest read", async () => {
    apiJSON.mockResolvedValue({ name: "build-cmd", revision: 5 });
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "x");
    // Another read lands (a pin elsewhere bumped the list) with a newer counter and other text.
    api.mockImplementation(async (url: string) => (url.startsWith("api/agents/memory/entries/member-read") ? { ...memory, revision: 9, body: "other" } : {}));
    await act(async () => useMemoryStore.getState().bump());
    await flush();
    await act(async () => button(t("mem.br_save"))!.click());
    await flush();
    expect(apiJSON.mock.calls.at(-1)![2]).toMatchObject({ body: "x", revision: 4 });
  });

  it("keeps the counter and kinds the edit started from when the same text is read again", async () => {
    apiJSON.mockResolvedValue({ name: "build-cmd", revision: 6 });
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "x");
    // Someone else changed only the kinds: same text, newer counter.
    api.mockImplementation(async (url: string) => (url.startsWith("api/agents/memory/entries/member-read") ? { ...memory, revision: 5, kinds: ["codex"] } : {}));
    await act(async () => useMemoryStore.getState().bump());
    await flush();
    await act(async () => button(t("mem.br_save"))!.click());
    await flush();
    expect(apiJSON.mock.calls.at(-1)![2]).toMatchObject({ revision: 4, kinds: ["claude"] });
  });

  it("treats a success answer without the new revision as an unknown outcome, not a saved one", async () => {
    apiJSON.mockResolvedValue({});
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "x");
    await act(async () => button(t("mem.br_save"))!.click());
    await flush();
    expect(dirtyPaneIds()).toEqual([pane().id]);
  });
});
