import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, create, type ReactTestRenderer } from "react-test-renderer";
import { clearDirtyRegistryForTests, dirtyPaneIds } from "./dirtyRegistry.ts";
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
