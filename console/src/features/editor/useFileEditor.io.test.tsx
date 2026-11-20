import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, create, type ReactTestRenderer } from "react-test-renderer";
import {
  clearDirtyRegistryForTests,
  confirmDirtyNavigation,
  currentDirtyGuardRequest,
  dirtyPaneIds,
  discardDirtyGuardRequest,
  saveDirtyGuardRequest,
} from "./dirtyRegistry.ts";
import { getEditableFile, putFile, type EditableFile, type PutFileResult } from "./api.ts";
import { revisionOf } from "./buffer.ts";
import { clearEditorSlotsForTests, useFileEditor, type FileEditorIO } from "./useFileEditor.ts";

// The adapter is the hook's only way to the store: with one given, the file API is never called.
vi.mock("./api.ts", () => ({
  getEditableFile: vi.fn(),
  putFile: vi.fn(),
  suggestEdit: vi.fn(),
}));
vi.mock("../../core/api/client.ts", () => ({
  apiJSON: async () => ({}),
  errText: (error: { message: string }) => error.message,
}));

const key = "memory:user/note";
const baseContent = "base\n";
const initial = { path: key, content: baseContent, revision: revisionOf(baseContent) };

const file = (content: string, revision: string): EditableFile => ({
  path: key,
  content,
  size: content.length,
  binary: false,
  truncated: false,
  editable: true,
  editabilityReason: null,
  revision,
});

let editor: ReturnType<typeof useFileEditor> | null = null;
let renderer: ReactTestRenderer | null = null;
let io: FileEditorIO;

function Harness() {
  editor = useFileEditor("p1", initial, io);
  return null;
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.mocked(putFile).mockReset();
  vi.mocked(getEditableFile).mockReset();
});

afterEach(async () => {
  await act(async () => renderer?.unmount());
  renderer = null;
  editor = null;
  clearDirtyRegistryForTests();
  clearEditorSlotsForTests();
});

async function mount() {
  await act(async () => {
    renderer = create(<Harness />);
  });
  return editor!;
}

describe("useFileEditor with an I/O adapter", () => {
  it("saves through the adapter and guards the dirty buffer", async () => {
    const save = vi.fn(async (): Promise<PutFileResult> => ({ ok: true, path: key, size: 6, revision: revisionOf("mine\n") }));
    io = { load: vi.fn(), save };
    const current = await mount();
    await act(async () => current.edit("mine\n"));
    expect(dirtyPaneIds()).toContain("p1");
    await act(async () => {
      await editor!.save();
    });
    expect(save).toHaveBeenCalledWith(key, "mine\n", revisionOf(baseContent));
    expect(editor!.model?.baseDiskRevision).toBe(revisionOf("mine\n"));
    expect(editor!.model?.dirty).toBe(false);
    expect(putFile).not.toHaveBeenCalled();
  });

  it("runs the conflict flow on the adapter's own load", async () => {
    const save = vi.fn(async (): Promise<PutFileResult> => ({
      ok: false,
      status: 409,
      error: { code: "revision_conflict", message: "stale" },
    }));
    const load = vi.fn(async () => file("theirs\n", revisionOf("theirs\n")));
    io = { load, save };
    const current = await mount();
    await act(async () => current.edit("mine\n"));
    await act(async () => {
      await editor!.save();
    });
    expect(load).toHaveBeenCalledWith(key);
    expect(editor!.model?.phase).toBe("conflict");
    expect(editor!.model?.conflict?.revision).toBe(revisionOf("theirs\n"));
    expect(getEditableFile).not.toHaveBeenCalled();
  });

  it("treats a thrown save as unknown and re-reads through the adapter", async () => {
    const mine = "mine\n";
    const save = vi.fn(async (): Promise<PutFileResult> => {
      throw new Error("lost");
    });
    const load = vi.fn(async () => file(mine, revisionOf(mine)));
    io = { load, save };
    const current = await mount();
    await act(async () => current.edit(mine));
    await act(async () => {
      await editor!.save();
    });
    expect(load).toHaveBeenCalledWith(key);
    expect(editor!.model?.phase).toBe("save_state_unknown");
    expect(editor!.model?.unknownObservation?.kind).not.toBe("unavailable");
  });

  it("keeps a confirmed failure dirty without re-reading", async () => {
    const save = vi.fn(async (): Promise<PutFileResult> => ({
      ok: false,
      status: 400,
      error: { code: "bad", message: "no" },
    }));
    const load = vi.fn();
    io = { load, save };
    const current = await mount();
    await act(async () => current.edit("mine\n"));
    await act(async () => {
      await editor!.save();
    });
    expect(load).not.toHaveBeenCalled();
    expect(editor!.model?.dirty).toBe(true);
  });

  it("guard save after unmount uses the newest adapter the view rendered with", async () => {
    const oldIo: FileEditorIO = { load: vi.fn(), save: vi.fn() };
    const newSave = vi.fn(async (): Promise<PutFileResult> => ({ ok: true, path: key, size: 6, revision: revisionOf("mine\n") }));
    const newIo: FileEditorIO = { load: vi.fn(), save: newSave };
    io = oldIo;
    const current = await mount();
    await act(async () => current.edit("mine\n"));
    io = newIo;
    await act(async () => renderer!.update(<Harness />));
    await act(async () => renderer!.unmount());
    renderer = null;
    expect(dirtyPaneIds()).toContain("p1");
    void confirmDirtyNavigation("history");
    const request = currentDirtyGuardRequest()!;
    let saved = false;
    await act(async () => {
      saved = await saveDirtyGuardRequest(request.id);
    });
    expect(saved).toBe(true);
    expect(newSave).toHaveBeenCalledTimes(1);
    expect(oldIo.save).not.toHaveBeenCalled();
  });

  it("guard save after a remount uses the last hook's adapter", async () => {
    const firstSave = vi.fn();
    const lastSave = vi.fn(async (): Promise<PutFileResult> => ({ ok: true, path: key, size: 6, revision: revisionOf("mine\n") }));
    io = { load: vi.fn(), save: firstSave };
    const current = await mount();
    await act(async () => current.edit("mine\n"));
    await act(async () => renderer!.unmount());
    io = { load: vi.fn(), save: lastSave };
    await act(async () => {
      renderer = create(<Harness />);
    });
    await act(async () => renderer!.unmount());
    renderer = null;
    void confirmDirtyNavigation("history");
    await act(async () => {
      await saveDirtyGuardRequest(currentDirtyGuardRequest()!.id);
    });
    expect(lastSave).toHaveBeenCalledTimes(1);
    expect(firstSave).not.toHaveBeenCalled();
  });

  it("guard discard of an unknown save never touches the file API", async () => {
    const save = vi.fn(async (): Promise<PutFileResult> => {
      throw new Error("lost");
    });
    const load = vi.fn(async () => file("theirs\n", revisionOf("theirs\n")));
    io = { load, save };
    const current = await mount();
    await act(async () => current.edit("mine\n"));
    await act(async () => {
      await editor!.save();
    });
    load.mockClear();
    await act(async () => renderer!.unmount());
    renderer = null;
    void confirmDirtyNavigation("history");
    await act(async () => {
      await discardDirtyGuardRequest(currentDirtyGuardRequest()!.id);
    });
    expect(getEditableFile).not.toHaveBeenCalled();
    expect(save).toHaveBeenCalledTimes(1);
  });

  it("defaults to the file API", async () => {
    vi.mocked(putFile).mockResolvedValue({ ok: true, path: "repos/a.txt", size: 5, revision: revisionOf("x\n") });
    function Default() {
      editor = useFileEditor("p2", { path: "repos/a.txt", content: "a\n", revision: revisionOf("a\n") });
      return null;
    }
    await act(async () => {
      renderer = create(<Default />);
    });
    await act(async () => editor!.edit("x\n"));
    await act(async () => {
      await editor!.save();
    });
    expect(putFile).toHaveBeenCalledWith("repos/a.txt", "x\n", revisionOf("a\n"));
  });
});
