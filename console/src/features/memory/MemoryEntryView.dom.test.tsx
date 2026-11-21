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

import { MemoryEntryView } from "./MemoryEntryView.tsx";
import { resetMemoryRevisions } from "./memoryEntryIO.ts";
import { clearEditorSlotsForTests } from "../editor/useFileEditor.ts";
import { clearDirtyRegistryForTests, confirmDirtyNavigation, currentDirtyGuardRequest, dirtyPaneIds, discardDirtyGuardRequest, saveDirtyGuardRequest } from "../editor/dirtyRegistry.ts";
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
  // A new memory lands on its saved pane from a timer, once its save has settled.
  await act(async () => new Promise((r) => setTimeout(r, 0)));
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
    }, undefined, { signal: expect.any(AbortSignal) });
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

  async function switchBack() {
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
  }

  it("stays in edit mode across a tab switch, with nothing typed", async () => {
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    expect(host!.querySelector(".mentry-form")).not.toBeNull();
    await switchBack();
    expect(host!.querySelector(".mentry-form")).not.toBeNull();
    await act(async () => button(t("mem.br_cancel"))!.click());
    await flush();
    await switchBack();
    expect(host!.querySelector(".mentry-form")).toBeNull();
  });

  it("opens the editor on the text shown, not on the older buffer the pane kept through a re-read", async () => {
    apiJSON.mockResolvedValue({ name: "build-cmd", revision: 6 });
    await open();
    const newer = { ...memory, revision: 5, body: "Run `make v2`." };
    api.mockImplementation(async (url: string) => (url.startsWith("api/agents/memory/entries/member-read") ? newer : url.includes("changes") ? { changes: [] } : {}));
    // Re-read while mounted: the body shown moves on, the clean buffer stays on the old text.
    await act(async () => useMemoryStore.getState().bump());
    await flush();
    let release!: (v: unknown) => void;
    api.mockImplementationOnce(() => new Promise((r) => (release = r)));
    await act(async () => button(t("mem.br_edit"))!.click());
    // While the buffer catches up the form is not open: typing now would start from the old text.
    expect(host!.querySelector(".mentry-form")).toBeNull();
    await act(async () => release(newer));
    await flush();
    expect(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!.value).toBe("Run `make v2`.");
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "Run `make v3`.");
    await act(async () => button(t("mem.br_save"))!.click());
    await flush();
    expect(apiJSON.mock.calls[0][2]).toMatchObject({ revision: 5, body: "Run `make v3`." });
  });

  it("opens the form when the follow reads newer text than the body shown", async () => {
    await open();
    const shown = { ...memory, revision: 5, body: "Run `make v2`." };
    const newest = { ...memory, revision: 6, body: "Run `make v3`." };
    let reads = 0;
    api.mockImplementation(async (url: string) => {
      if (!url.startsWith("api/agents/memory/entries/member-read")) return url.includes("changes") ? { changes: [] } : {};
      // The body re-read answers v2; the follow that comes with Edit finds v3 already.
      return ++reads === 1 ? shown : newest;
    });
    await act(async () => useMemoryStore.getState().bump());
    await flush();
    await act(async () => button(t("mem.br_edit"))!.click());
    await flush();
    expect(host!.querySelector(".mentry-form")).not.toBeNull();
    expect(host!.textContent).not.toContain(t("mem.br_edit_stale"));
  });

  it("ends edit mode in the view that is on screen when a save outlives the view that asked for it", async () => {
    let finish!: (v: unknown) => void;
    apiJSON.mockImplementation(() => new Promise((r) => (finish = r)));
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "Run `make all`.");
    await act(async () => button(t("mem.br_save"))!.click());
    await switchBack();
    await act(async () => finish({ name: "build-cmd", revision: 5 }));
    await flush();
    expect(host!.querySelector(".mentry-form")).toBeNull();
  });

  it("does not bring edit mode back to a pane that was closed and opened again", async () => {
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    const id = pane().id;
    await act(async () => useLayoutStore.getState().closePane(id));
    await act(async () => root!.render(<></>));
    useLayoutStore.setState({ layout: freshLayout() });
    await open();
    expect(host!.querySelector(".mentry-form")).toBeNull();
  });

  it("follows again when the body shown goes back to a text it was followed to earlier, during the same edit", async () => {
    await open();
    const b = { ...memory, revision: 5, body: "Run `make v2`." };
    const c = { ...memory, revision: 6, body: "Run `make v3`." };
    let live = b;
    let reads: unknown[] = [];
    api.mockImplementation(async (url: string) => {
      if (!url.startsWith("api/agents/memory/entries/member-read")) return url.includes("changes") ? { changes: [] } : {};
      return reads.shift() ?? live;
    });
    await act(async () => useMemoryStore.getState().bump());
    await flush();
    // The follow that comes with Edit finds c; the body shown then catches up to c.
    reads = [c];
    await act(async () => button(t("mem.br_edit"))!.click());
    await flush();
    live = c;
    await act(async () => useMemoryStore.getState().bump());
    await flush();
    expect(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!.value).toBe("Run `make v3`.");
    // Another writer puts b back before anything is typed.
    live = b;
    await act(async () => useMemoryStore.getState().bump());
    await flush();
    expect(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!.value).toBe("Run `make v2`.");
  });

  it("follows again when the shown text is reverted to at a later counter after the follow adopted a text never shown", async () => {
    await open();
    const b = { ...memory, revision: 5, body: "Run `make v2`." };
    const c = { ...memory, revision: 6, body: "Run `make v3`." };
    let live = b;
    let reads: unknown[] = [];
    api.mockImplementation(async (url: string) => {
      if (!url.startsWith("api/agents/memory/entries/member-read")) return url.includes("changes") ? { changes: [] } : {};
      return reads.shift() ?? live;
    });
    await act(async () => useMemoryStore.getState().bump());
    await flush();
    // The follow that comes with Edit adopts c, which the body shown never was.
    reads = [c];
    await act(async () => button(t("mem.br_edit"))!.click());
    await flush();
    expect(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!.value).toBe("Run `make v3`.");
    // Another writer reverts to b at a later counter before anything is typed.
    live = { ...b, revision: 7 };
    await act(async () => useMemoryStore.getState().bump());
    await flush();
    expect(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!.value).toBe("Run `make v2`.");
    apiJSON.mockResolvedValue({ name: "build-cmd", revision: 8 });
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "Run `make v4`.");
    await act(async () => button(t("mem.br_save"))!.click());
    await flush();
    expect(apiJSON.mock.calls.at(-1)![2]).toMatchObject({ revision: 7 });
  });

  it("does not bring edit mode back after the memory was forgotten and restored, or moved to another and back", async () => {
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    const id = pane().id;
    const base = pane().content as Extract<typeof pane extends () => infer P ? (P extends { content: infer C } ? C : never) : never, { kind: "memoryEntry" }>;
    await act(async () => useLayoutStore.getState().setPaneTarget(id, { content: { ...base, gone: true } }));
    await flush();
    await act(async () => useLayoutStore.getState().setPaneTarget(id, { content: { ...base, gone: false } }));
    await flush();
    expect(host!.querySelector(".mentry-form")).toBeNull();
    await act(async () => button(t("mem.br_edit"))!.click());
    await act(async () => useLayoutStore.getState().setPaneTarget(id, { content: { ...base, name: "other" } }));
    await flush();
    await act(async () => useLayoutStore.getState().setPaneTarget(id, { content: base }));
    await flush();
    expect(host!.querySelector(".mentry-form")).toBeNull();
  });

  it("does not open the editor on an older base when the follow read fails", async () => {
    await open();
    const newer = { ...memory, revision: 5, body: "Run `make v2`." };
    let failing = false;
    api.mockImplementation(async (url: string) => {
      if (!url.startsWith("api/agents/memory/entries/member-read")) return url.includes("changes") ? { changes: [] } : {};
      return failing ? { error: { code: "agent_unavailable", message: "x" } } : newer;
    });
    await act(async () => useMemoryStore.getState().bump());
    await flush();
    failing = true;
    await act(async () => button(t("mem.br_edit"))!.click());
    await flush();
    expect(host!.querySelector(".mentry-form")).toBeNull();
    expect(host!.textContent).toContain(t("mem.br_edit_stale"));
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

  describe("a new memory's draft rides the editor hook", () => {
    const nameInput = () => host!.querySelector<HTMLInputElement>(".mentry-fields input")!;
    const bodyEditor = () => host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!;
    const remount = async () => {
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
    };

    it("is not dirty until something is typed, and cannot be saved empty", async () => {
      await open(memoryEntryContent({ scope: "user", name: "" }));
      expect(dirtyPaneIds()).toEqual([]);
      expect(button(t("mem.br_save"))!.disabled).toBe(true);
      await setValue(nameInput(), "house-rules");
      expect(dirtyPaneIds()).toEqual([pane().id]);
      expect(button(t("mem.br_save"))!.disabled).toBe(false);
    });

    it("is guarded while typed, kept with its name across a tab switch, and cleared by Cancel", async () => {
      await open(memoryEntryContent({ scope: "user", name: "" }));
      await setValue(nameInput(), "house-rules");
      await setValue(bodyEditor(), "half-written");
      await remount();
      expect(dirtyPaneIds()).toEqual([pane().id]);
      expect(nameInput().value).toBe("house-rules");
      expect(bodyEditor().value).toBe("half-written");
      await act(async () => button(t("mem.br_cancel"))!.click());
      expect(dirtyPaneIds()).toEqual([]);
      expect(allViews(useLayoutStore.getState().layout).some((v) => v.content.kind === "memoryEntry")).toBe(false);
    });

    it("is saved by the close / reload guard even after its view is gone, and the pane lands on the memory", async () => {
      await open(memoryEntryContent({ scope: "user", name: "" }));
      await setValue(nameInput(), " house-rules ");
      await setValue(bodyEditor(), "text");
      await act(async () => root!.render(<></>));
      expect(await saveThroughGuard()).toBe(true);
      await act(async () => new Promise((r) => setTimeout(r, 0)));
      expect(apiJSON.mock.calls[0][2]).toMatchObject({ scope: "user", name: "house-rules", body: "text", revision: 0, kinds: [] });
      expect(dirtyPaneIds()).toEqual([]);
      expect(pane().content).toEqual({ kind: "memoryEntry", scope: "user", project: "", name: "house-rules" });
    });

    it("is discarded by the guard without a request", async () => {
      await open(memoryEntryContent({ scope: "user", name: "" }));
      await setValue(bodyEditor(), "text");
      await act(async () => root!.render(<></>));
      void confirmDirtyNavigation("layout").catch(() => {});
      await act(async () => {
        const req = currentDirtyGuardRequest();
        if (req) await discardDirtyGuardRequest(req.id);
      });
      expect(dirtyPaneIds()).toEqual([]);
      expect(apiJSON).not.toHaveBeenCalled();
    });

    it("does not land on the saved memory when the pane was retargeted while the save was out", async () => {
      let release: (v: unknown) => void = () => {};
      apiJSON.mockImplementation(() => new Promise((r) => (release = r)));
      await open(memoryEntryContent({ scope: "user", name: "" }));
      await setValue(nameInput(), "house-rules");
      await act(async () => button(t("mem.br_save"))!.click());
      const other = memoryEntryContent({ scope: "user", name: "elsewhere" });
      await act(async () => useLayoutStore.getState().setPaneTarget(pane().id, { content: other }));
      await act(async () => release({ name: "house-rules", revision: 1 }));
      await flush();
      expect(pane().content).toEqual(other);
    });

    it("ignores typing while the save is out, so the landing cannot hide it", async () => {
      let release: (v: unknown) => void = () => {};
      apiJSON.mockImplementation(() => new Promise((r) => (release = r)));
      await open(memoryEntryContent({ scope: "user", name: "" }));
      await setValue(nameInput(), "house-rules");
      await act(async () => button(t("mem.br_save"))!.click());
      await setValue(bodyEditor(), "late addition");
      await act(async () => release({ name: "house-rules", revision: 1 }));
      await flush();
      expect(dirtyPaneIds()).toEqual([]);
      expect(pane().content).toMatchObject({ name: "house-rules" });
    });

    it("locks the form and the code editor while the save is out", async () => {
      let release: (v: unknown) => void = () => {};
      apiJSON.mockImplementation(() => new Promise((r) => (release = r)));
      await open(memoryEntryContent({ scope: "user", name: "" }));
      await setValue(nameInput(), "house-rules");
      await act(async () => button(t("mem.br_save"))!.click());
      expect(nameInput().disabled).toBe(true);
      expect(host!.querySelector(".mentry-editor")!.hasAttribute("inert")).toBe(true);
      await act(async () => release({ name: "house-rules", revision: 1 }));
      await flush();
    });

    describe("when the answer to the save is lost", () => {
      const stored = { ...memory, name: "house-rules", scope: "user", description: "", type: "feedback", body: "text", revision: 1, kinds: [] };
      const lost = async () => {
        apiJSON.mockRejectedValue(new Error("network"));
        await open(memoryEntryContent({ scope: "user", name: "" }));
        await setValue(nameInput(), "house-rules");
        await setValue(bodyEditor(), "text");
        await act(async () => button(t("mem.br_save"))!.click());
        await flush();
      };

      it("lands on the memory when the recovery read finds it stored as sent", async () => {
        api.mockImplementation(async (url: string) => (url.includes("name=house-rules") ? stored : {}));
        await lost();
        expect(api.mock.calls.some((c) => String(c[0]).includes("member-read") && String(c[0]).includes("name=house-rules"))).toBe(true);
        expect(dirtyPaneIds()).toEqual([pane().id]);
        expect(button(t("editor.unknown.resave"))).toBeUndefined();
        expect(nameInput().disabled).toBe(true);
        expect(host!.querySelector(".mentry-editor")!.hasAttribute("inert")).toBe(true);
        await act(async () => button(t("editor.unknown.accept_risk"))!.click());
        await flush();
        expect(dirtyPaneIds()).toEqual([]);
        expect(pane().content).toMatchObject({ name: "house-rules" });
      });

      it("offers a resave, not acceptance, when the memory is not there", async () => {
        api.mockImplementation(async (url: string) => (url.includes("name=house-rules") ? { error: { code: "memory_not_found", message: "no", status: 404 } } : {}));
        await lost();
        expect(dirtyPaneIds()).toEqual([pane().id]);
        expect(pane().content).toMatchObject({ name: "" });
        expect(button(t("editor.unknown.accept_risk"))).toBeUndefined();
        expect(button(t("editor.unknown.resave_old"))).toBeDefined();
      });

      it.each([
        ["other text", { body: "someone else" }],
        ["an earlier revision", { revision: 2 }],
        ["kinds it was not sent with", { kinds: ["claude"] }],
      ])("offers a resave but not acceptance when a memory of that name holds %s, and Cancel leaves it be", async (_why, patch) => {
        api.mockImplementation(async (url: string) => (url.includes("name=house-rules") ? { ...stored, ...patch } : {}));
        await lost();
        expect(dirtyPaneIds()).toEqual([pane().id]);
        expect(pane().content).toMatchObject({ name: "" });
        expect(button(t("editor.unknown.accept_risk"))).toBeUndefined();
        expect(button(t("editor.unknown.resave_old"))).toBeDefined();
        apiJSON.mockClear();
        await act(async () => button(t("mem.br_cancel"))!.click());
        await flush();
        expect(dirtyPaneIds()).toEqual([]);
        expect(apiJSON).not.toHaveBeenCalled();
        expect(allViews(useLayoutStore.getState().layout).some((v) => v.content.kind === "memoryEntry")).toBe(false);
      });

      it("keeps the outcome unconfirmed while the memory cannot be read, and lands once the read answers", async () => {
        let online = false;
        api.mockImplementation(async (url: string) => {
          if (!url.includes("name=house-rules")) return {};
          if (!online) throw new Error("offline");
          return stored;
        });
        await lost();
        expect(button(t("editor.unknown.accept_risk"))).toBeUndefined();
        expect(button(t("editor.unknown.resave_old"))).toBeUndefined();
        online = true;
        await act(async () => button(t("editor.unknown.retry"))!.click());
        await flush();
        expect(button(t("editor.unknown.accept_risk"))).toBeDefined();
      });

      it("asks before Cancel drops a buffer whose save cannot be confirmed", async () => {
        api.mockImplementation(async (url: string) => {
          if (url.includes("name=house-rules")) throw new Error("offline");
          return {};
        });
        await lost();
        await act(async () => button(t("mem.br_cancel"))!.click());
        await flush();
        expect(document.body.textContent).toContain(t("mem.br_discard_unconfirmed_title"));
        expect(dirtyPaneIds()).toEqual([pane().id]);
        await act(async () => button(t("mem.br_discard_unconfirmed"))!.click());
        await flush();
        expect(dirtyPaneIds()).toEqual([]);
      });

      it("can be discarded by the guard with its view gone when the read of the memory fails", async () => {
        api.mockImplementation(async (url: string) => {
          if (url.includes("name=house-rules")) throw new Error("offline");
          return {};
        });
        await lost();
        await act(async () => root!.render(<></>));
        void confirmDirtyNavigation("layout").catch(() => {});
        await act(async () => {
          const req = currentDirtyGuardRequest();
          if (req) await discardDirtyGuardRequest(req.id);
        });
        expect(dirtyPaneIds()).toEqual([]);
      });
    });

    it("keeps the buffer and shows the reason when the Agent refuses it", async () => {
      apiJSON.mockResolvedValue({ error: { code: "memory_secret_detected", message: "secret" }, findings: [{ rule: "aws-key", line: 1, hint: "AKIA…" }] });
      await open(memoryEntryContent({ scope: "user", name: "" }));
      await setValue(nameInput(), "keys");
      await act(async () => button(t("mem.br_save"))!.click());
      await flush();
      expect(host!.querySelector(".mem-findings")?.textContent).toContain("aws-key");
      expect(dirtyPaneIds()).toEqual([pane().id]);
      expect(pane().content).toMatchObject({ name: "" });
    });
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

  it("says why a body with a CR cannot be edited, instead of opening an editor on it", async () => {
    api.mockImplementation(async (url: string) => (url.startsWith("api/agents/memory/entries/member-read") ? { ...memory, body: "a\r\nb" } : {}));
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    expect(host!.querySelector(".mentry-form")).toBeNull();
    expect(host!.querySelector('[role="alert"]')?.textContent).toBe(t("editor.validation.unsupported_newline"));
  });

  // The guard path a dirty buffer is saved through when the pane is closed or the page reloaded.
  async function saveThroughGuard() {
    void confirmDirtyNavigation("layout");
    return act(async () => saveDirtyGuardRequest(currentDirtyGuardRequest()!.id));
  }

  it("keeps a dirty buffer saveable through the guard when the re-read of the memory fails", async () => {
    apiJSON.mockResolvedValue({ name: "build-cmd", revision: 5 });
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "mine");
    api.mockImplementation(async (url: string) => (url.startsWith("api/agents/memory/entries/member-read") ? { error: { code: "memory_unreadable", message: "no" } } : {}));
    await act(async () => useMemoryStore.getState().bump());
    await flush();
    expect(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!.value).toBe("mine");
    expect(await saveThroughGuard()).toBe(true);
    expect(apiJSON.mock.calls.at(-1)![2]).toMatchObject({ body: "mine", revision: 4 });
  });

  it("keeps a dirty buffer saveable through the guard when the pane switches to the forgotten memory", async () => {
    apiJSON.mockResolvedValue({ name: "build-cmd", revision: 5 });
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "mine");
    await act(async () => useLayoutStore.getState().setPaneTarget(pane().id, { content: memoryEntryContent({ scope: "project", project, name: "build-cmd" }, true) }));
    await flush();
    // Read-only as a forgotten memory is, but the unsaved text is still the guards' to save.
    expect(host!.querySelector(".mentry-form")).toBeNull();
    expect(dirtyPaneIds()).toEqual([pane().id]);
    expect(await saveThroughGuard()).toBe(true);
    expect(apiJSON.mock.calls.at(-1)![2]).toMatchObject({ body: "mine" });
  });

  it("shows the reason a save was refused when the tab was switched while it was in flight", async () => {
    let answer!: (v: unknown) => void;
    apiJSON.mockReturnValue(new Promise((r) => (answer = r)));
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "token AKIA…");
    await act(async () => button(t("mem.br_save"))!.click());
    await act(async () => root!.render(<></>));
    await act(async () => answer({ error: { code: "memory_secret_detected", message: "secret" }, findings: [{ rule: "aws-key", line: 3, hint: "AKIA…" }] }));
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
    expect(host!.querySelector('[role="alert"]')?.textContent).toBe(t("mem.br_secret"));
    expect(host!.querySelector(".mem-findings")?.textContent).toContain("aws-key");
    expect(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!.value).toBe("token AKIA…");
  });

  it("reads a lost answer for a text with trailing whitespace as the text that was sent, not a third version", async () => {
    apiJSON.mockResolvedValue({ error: { code: "memory_snapshot_failed", message: "x" } });
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "kept  \n\n");
    // The commit landed: the store holds what the Agent trims the text to.
    api.mockImplementation(async (url: string) => (url.startsWith("api/agents/memory/entries/member-read") ? { ...memory, revision: 5, body: "kept" } : {}));
    await act(async () => button(t("mem.br_save"))!.click());
    await flush();
    expect(host!.textContent).toContain(t("mem.br_unknown_sent_live"));
  });

  it("lets an existing memory be closed without saving when its recovery read answers 404", async () => {
    apiJSON.mockResolvedValue({ error: { code: "memory_snapshot_failed", message: "x" } });
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "kept\n");
    api.mockImplementation(async (url: string) =>
      url.startsWith("api/agents/memory/entries/member-read") ? { error: { code: "memory_not_found", message: "gone", status: 404 } } : {},
    );
    await act(async () => button(t("mem.br_save"))!.click());
    await flush();
    expect(dirtyPaneIds()).toEqual([pane().id]);
    void confirmDirtyNavigation("layout").catch(() => {});
    await act(async () => {
      const req = currentDirtyGuardRequest();
      if (req) await discardDirtyGuardRequest(req.id);
    });
    expect(dirtyPaneIds()).toEqual([]);
  });

  const remount = async () => {
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
  };

  it("shows a refusal that arrives after the view came back to the tab", async () => {
    let answer!: (v: unknown) => void;
    apiJSON.mockReturnValue(new Promise((r) => (answer = r)));
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "token AKIA…");
    await act(async () => button(t("mem.br_save"))!.click());
    await remount();
    await act(async () => answer({ error: { code: "memory_secret_detected", message: "secret" }, findings: [{ rule: "aws-key", line: 3, hint: "AKIA…" }] }));
    await flush();
    expect(host!.querySelector('[role="alert"]')?.textContent).toBe(t("mem.br_secret"));
  });

  it("does not bring back a refusal about text that was edited afterwards", async () => {
    apiJSON.mockResolvedValue({ error: { code: "memory_secret_detected", message: "secret" }, findings: [] });
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "token AKIA…");
    await act(async () => button(t("mem.br_save"))!.click());
    await flush();
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "fixed");
    await remount();
    expect(host!.querySelector('[role="alert"]')).toBeNull();
    expect(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!.value).toBe("fixed");
  });

  it("lets the guards discard, and refuses to save, the buffer of a memory forgotten in the meantime", async () => {
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "mine");
    await act(async () => useLayoutStore.getState().setPaneTarget(pane().id, { content: memoryEntryContent({ scope: "project", project, name: "build-cmd" }, true) }));
    await flush();
    apiJSON.mockResolvedValue({ error: { code: "memory_not_found", message: "gone", status: 404 } });
    expect(await saveThroughGuard()).toBe(false);
    expect(dirtyPaneIds()).toEqual([pane().id]);
    void confirmDirtyNavigation("layout").catch(() => {});
    await act(async () => {
      const req = currentDirtyGuardRequest();
      if (req) await discardDirtyGuardRequest(req.id);
    });
    expect(dirtyPaneIds()).toEqual([]);
  });

  it("drops a refusal about text the buffer no longer holds", async () => {
    apiJSON.mockResolvedValue({ error: { code: "memory_secret_detected", message: "secret" }, findings: [{ rule: "aws-key", line: 3, hint: "AKIA…" }] });
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "token AKIA…");
    await act(async () => button(t("mem.br_save"))!.click());
    await flush();
    expect(host!.querySelector(".mem-findings")).not.toBeNull();
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "fixed");
    expect(host!.querySelector(".mem-findings")).toBeNull();
    expect(host!.querySelector('[role="alert"]')).toBeNull();
  });

  it("does not show a refusal that comes back for text the member has rewritten since it was sent", async () => {
    let answer!: (v: unknown) => void;
    apiJSON.mockReturnValue(new Promise((r) => (answer = r)));
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "token AKIA…");
    await act(async () => button(t("mem.br_save"))!.click());
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "rewritten");
    await act(async () => answer({ error: { code: "memory_secret_detected", message: "secret" }, findings: [{ rule: "aws-key", line: 3, hint: "AKIA…" }] }));
    await flush();
    expect(host!.querySelector('[role="alert"]')).toBeNull();
    await remount();
    expect(host!.querySelector('[role="alert"]')).toBeNull();
  });

  const copied: string[] = [];
  const forgetUnder = () => useLayoutStore.getState().setPaneTarget(pane().id, { content: memoryEntryContent({ scope: "project", project, name: "build-cmd" }, true) });
  const copyButtons = () => Array.from(document.body.querySelectorAll<HTMLButtonElement>("button")).filter((b) => b.textContent?.trim() === t("editor.copy_mine"));
  beforeEach(() => {
    copied.length = 0;
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: async (v: string) => void copied.push(v) } });
  });

  it("offers the unsaved text for copying when the memory is forgotten under it, even if its last change cannot be read", async () => {
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "mine");
    api.mockImplementation(async (url: string) => (url.startsWith("api/agents/memory/entries/member-latest") ? { error: { code: "memory_unreadable", message: "no" } } : {}));
    await act(async () => forgetUnder());
    await flush();
    await act(async () => copyButtons()[0].click());
    expect(copied[0]).toContain("mine");
  });

  it("copies the member's own text, not the adopted remote, when a manual merge was started before the memory was forgotten", async () => {
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "mine");
    apiJSON.mockResolvedValue({ error: { code: "memory_conflict", message: "changed" } });
    api.mockImplementation(async (url: string) => (url.startsWith("api/agents/memory/entries/member-read") ? { ...memory, revision: 5, body: "theirs" } : {}));
    await act(async () => button(t("mem.br_save"))!.click());
    await flush();
    await act(async () => button(t("editor.conflict.manual_merge"))!.click());
    // The member goes on editing the merge text.
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "merged by hand");
    api.mockImplementation(async (url: string) => (url.startsWith("api/agents/memory/entries/member-latest") ? latest : {}));
    await act(async () => forgetUnder());
    await flush();
    await act(async () => copyButtons()[0].click());
    expect(copied[0]).toContain("mine");
    expect(copied[0]).not.toContain("theirs");
    expect(copied[0]).not.toContain("merged by hand");
    await act(async () => button(t("mem.br_copy_current"))!.click());
    expect(copied[1]).toContain("merged by hand");
  });

  it("does not show memory A's refusal on memory B when the view was reused while A's save was in flight", async () => {
    let answer!: (v: unknown) => void;
    apiJSON.mockReturnValue(new Promise((r) => (answer = r)));
    await open();
    await act(async () => button(t("mem.br_edit"))!.click());
    await setValue(host!.querySelector<HTMLTextAreaElement>('[data-testid="editor"]')!, "token AKIA…");
    await act(async () => button(t("mem.br_save"))!.click());
    // The same pane now shows memory B, whose read fails.
    api.mockImplementation(async (url: string) => (url.startsWith("api/agents/memory/entries/member-read") ? { error: { code: "memory_unreadable", message: "no" } } : {}));
    await act(async () => useLayoutStore.getState().setPaneTarget(pane().id, { content: memoryEntryContent({ scope: "project", project, name: "other" }) }));
    await flush();
    await act(async () => answer({ error: { code: "memory_secret_detected", message: "secret" }, findings: [] }));
    await flush();
    expect(host!.textContent).not.toContain(t("mem.br_secret"));
  });

  it("says upfront why a body with a CR is read-only", async () => {
    api.mockImplementation(async (url: string) => (url.startsWith("api/agents/memory/entries/member-read") ? { ...memory, body: "a\r\nb" } : {}));
    await open();
    expect(host!.querySelector(".mentry-readonly-why")?.textContent).toBe(t("editor.validation.unsupported_newline"));
  });
});
