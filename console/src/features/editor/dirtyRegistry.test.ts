import { afterEach, describe, expect, it } from "vitest";
import type { Layout } from "../../layout/types.ts";
import * as ops from "../../layout/ops.ts";
import {
  cancelDirtyGuardRequest,
  clearDirtyRegistryForTests,
  confirmDirtyNavigation,
  currentDirtyGuardRequest,
  dirtyPanesDestroyedByLayout,
  discardDirtyGuardRequest,
  registerDirtyEditor,
  saveDirtyGuardRequest,
} from "./dirtyRegistry.ts";

const layout = (content: import("../../layout/types.ts").PaneContent): Layout => ({
  version: 3,
  mode: "split",
  cols: [{ id: "c1", rowRatio: 0.5, cells: [{ id: "g1", selectedViewId: "p1", views: [{ id: "p1", session: null, wrap: null, content }] }] }],
  colRatios: [1],
  activeCellId: "g1",
});

afterEach(clearDirtyRegistryForTests);

describe("dirty navigation registry", () => {
  it("guards pane replacement but not moves that preserve pane identity and file", () => {
    let dirty = true;
    registerDirtyEditor({
      paneId: "p1",
      label: "a.txt",
      isDirty: () => dirty,
      save: async () => false,
      discard: () => { dirty = false; },
    });
    const current = layout({ kind: "file", filePath: "a.txt" });
    expect(dirtyPanesDestroyedByLayout(current, structuredClone(current))).toEqual([]);
    expect(dirtyPanesDestroyedByLayout(current, layout({ kind: "file", filePath: "b.txt" }))).toEqual(["p1"]);
    expect(dirtyPanesDestroyedByLayout(current, layout({ kind: "read", filePath: "a.txt" }))).toEqual(["p1"]);
  });

  it("covers close, active replacement, reader, reset, and 8-pane reuse", () => {
    let dirty = true;
    registerDirtyEditor({
      paneId: "p1",
      label: "a.txt",
      isDirty: () => dirty,
      save: async () => false,
      discard: () => { dirty = false; },
    });
    const current = layout({ kind: "file", filePath: "a.txt" });
    expect(dirtyPanesDestroyedByLayout(current, ops.openActive(current, {
      content: { kind: "terminal", chat: false },
    }))).toEqual(["p1"]);
    expect(dirtyPanesDestroyedByLayout(current, ops.setPaneTarget(current, "p1", {
      content: { kind: "read", filePath: "a.txt" },
    }))).toEqual(["p1"]);
    expect(dirtyPanesDestroyedByLayout(current, ops.closePane(current, "p1"))).toEqual(["p1"]);
    expect(dirtyPanesDestroyedByLayout(current, ops.freshLayout())).toEqual(["p1"]);

    clearDirtyRegistryForTests();
    const panes = Array.from({ length: 8 }, (_, i) => ({
      id: `p${i + 1}`,
      session: null,
      wrap: null,
      content: { kind: "file" as const, filePath: `${i + 1}.txt` },
    }));
    const full: Layout = {
      version: 3,
      mode: "split",
      cols: [0, 1, 2, 3].map((i) => ({
        id: `c${i + 1}`,
        rowRatio: 0.5,
        cells: panes.slice(i * 2, i * 2 + 2).map((view, row) => ({ id: `g${i * 2 + row + 1}`, selectedViewId: view.id, views: [view] })),
      })),
      colRatios: [0.25, 0.25, 0.25, 0.25],
      activeCellId: "g1",
    };
    registerDirtyEditor({
      paneId: "p8",
      label: "8.txt",
      isDirty: () => true,
      save: async () => false,
      discard: () => {},
    });
    const reused = ops.openInNew(full, { content: { kind: "file", filePath: "new.txt" } }, {
      mobile: false,
      force: true,
    });
    expect(dirtyPanesDestroyedByLayout(full, reused)).toContain("p8");
  });

  it("supports save, discard, and cancel decisions", async () => {
    let dirty = true;
    let saves = 0;
    registerDirtyEditor({
      paneId: "p1",
      label: "a.txt",
      isDirty: () => dirty,
      save: async () => { saves++; dirty = false; return true; },
      discard: () => { dirty = false; },
    });
    const saveDecision = confirmDirtyNavigation("reload");
    await saveDirtyGuardRequest(currentDirtyGuardRequest()!.id);
    await expect(saveDecision).resolves.toBe(true);
    expect(saves).toBe(1);

    dirty = true;
    const discardDecision = confirmDirtyNavigation("layout");
    await discardDirtyGuardRequest(currentDirtyGuardRequest()!.id);
    await expect(discardDecision).resolves.toBe(true);
    expect(dirty).toBe(false);

    dirty = true;
    const cancelDecision = confirmDirtyNavigation("tenant");
    cancelDirtyGuardRequest(currentDirtyGuardRequest()!.id);
    await expect(cancelDecision).resolves.toBe(false);
    expect(dirty).toBe(true);
  });

  it("waits for asynchronous discard before resolving the guard", async () => {
    let dirty = true;
    let finishDiscard!: () => void;
    const pending = new Promise<void>((resolve) => {
      finishDiscard = resolve;
    });
    registerDirtyEditor({
      paneId: "p1",
      label: "a.txt",
      isDirty: () => dirty,
      save: async () => false,
      discard: async () => {
        await pending;
        dirty = false;
        return true;
      },
    });

    const decision = confirmDirtyNavigation("workspace_lifecycle");
    const discard = discardDirtyGuardRequest(currentDirtyGuardRequest()!.id);
    let decided = false;
    void decision.then(() => { decided = true; });
    await Promise.resolve();
    expect(decided).toBe(false);

    finishDiscard();
    await expect(discard).resolves.toBe(true);
    await expect(decision).resolves.toBe(true);
  });

  it("aborts a pending discard's signal when the request is cancelled", async () => {
    let dirty = true;
    let seenSignal: AbortSignal | undefined;
    let finishDiscard!: () => void;
    const pending = new Promise<void>((resolve) => {
      finishDiscard = resolve;
    });
    registerDirtyEditor({
      paneId: "p1",
      label: "a.txt",
      isDirty: () => dirty,
      save: async () => false,
      discard: async (signal) => {
        seenSignal = signal;
        await pending;
        if (signal?.aborted) return false;
        dirty = false;
        return true;
      },
    });

    const decision = confirmDirtyNavigation("history");
    const request = currentDirtyGuardRequest()!;
    const discard = discardDirtyGuardRequest(request.id);
    await Promise.resolve();
    expect(seenSignal?.aborted).toBe(false);

    cancelDirtyGuardRequest(request.id);
    expect(seenSignal?.aborted).toBe(true);
    await expect(decision).resolves.toBe(false);

    finishDiscard();
    await expect(discard).resolves.toBe(false);
    expect(dirty).toBe(true);
  });

  it("does not let a stale completion clobber a newer request", async () => {
    let finishSave!: (ok: boolean) => void;
    const pending = new Promise<boolean>((resolve) => {
      finishSave = resolve;
    });
    let dirty = true;
    registerDirtyEditor({
      paneId: "p1",
      label: "a.txt",
      isDirty: () => dirty,
      save: () => pending.then((ok) => { dirty = !ok; return ok; }),
      discard: () => {},
    });

    const first = confirmDirtyNavigation("history");
    const firstId = currentDirtyGuardRequest()!.id;
    const stale = saveDirtyGuardRequest(firstId);
    cancelDirtyGuardRequest(firstId);
    await expect(first).resolves.toBe(false);

    const second = confirmDirtyNavigation("layout");
    const secondId = currentDirtyGuardRequest()!.id;
    expect(secondId).not.toBe(firstId);

    finishSave(true);
    await expect(stale).resolves.toBe(false);
    expect(currentDirtyGuardRequest()?.id).toBe(secondId);

    cancelDirtyGuardRequest(secondId);
    await expect(second).resolves.toBe(false);
  });

  it("keeps the guard open when disk-safe discard cannot complete", async () => {
    registerDirtyEditor({
      paneId: "p1",
      label: "a.txt",
      isDirty: () => true,
      save: async () => false,
      discard: async () => false,
    });

    const decision = confirmDirtyNavigation("workspace_lifecycle");
    const request = currentDirtyGuardRequest()!;
    await expect(discardDirtyGuardRequest(request.id)).resolves.toBe(false);
    expect(currentDirtyGuardRequest()?.id).toBe(request.id);

    cancelDirtyGuardRequest(request.id);
    await expect(decision).resolves.toBe(false);
  });
});
