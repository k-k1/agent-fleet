import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, create, type ReactTestRenderer } from "react-test-renderer";
import { clearDirtyRegistryForTests } from "./dirtyRegistry.ts";
import { getEditableFile, putFile, type EditableFile, type PutFileResult } from "./api.ts";
import { revisionOf } from "./buffer.ts";
import { useFileEditor } from "./useFileEditor.ts";

vi.mock("./api.ts", () => ({
  getEditableFile: vi.fn(),
  putFile: vi.fn(),
}));
vi.mock("../../core/api/client.ts", () => ({
  errText: (error: { message: string }) => error.message,
}));

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

const baseContent = "base\n";
const initial = {
  path: "repos/a.txt",
  content: baseContent,
  revision: revisionOf(baseContent),
};

type Editor = ReturnType<typeof useFileEditor>;
let editor: Editor | null = null;
let renderer: ReactTestRenderer | null = null;

function Harness() {
  editor = useFileEditor("p1", initial);
  return null;
}

async function renderEditor(): Promise<Editor> {
  await act(async () => {
    renderer = create(<Harness />);
  });
  return editor!;
}

function editableFile(content: string): EditableFile {
  return {
    path: initial.path,
    content,
    size: new TextEncoder().encode(content).byteLength,
    binary: false,
    truncated: false,
    editable: true,
    editabilityReason: null,
    revision: revisionOf(content),
  };
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.mocked(putFile).mockReset();
  vi.mocked(getEditableFile).mockReset();
});

afterEach(async () => {
  await act(async () => {
    renderer?.unmount();
  });
  renderer = null;
  editor = null;
  clearDirtyRegistryForTests();
  delete (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT;
});

describe("useFileEditor discard during save", () => {
  it("waits for a delayed 200 and adopts the saved disk base", async () => {
    const mine = "mine\n";
    const pendingPut = deferred<PutFileResult>();
    vi.mocked(putFile).mockReturnValue(pendingPut.promise);
    const current = await renderEditor();
    await act(async () => current.edit(mine));

    let save!: Promise<boolean>;
    let discard!: Promise<boolean>;
    await act(async () => {
      save = current.save();
      await Promise.resolve();
      discard = current.discard();
      await Promise.resolve();
    });
    expect(editor!.model?.phase).toBe("saving");

    pendingPut.resolve({
      ok: true,
      path: initial.path,
      size: new TextEncoder().encode(mine).byteLength,
      revision: revisionOf(mine),
    });
    await act(async () => {
      await Promise.all([save, discard]);
    });

    expect(editor!.model?.content).toBe(mine);
    expect(editor!.model?.baseDiskContent).toBe(mine);
    expect(editor!.model?.baseDiskRevision).toBe(revisionOf(mine));
    expect(editor!.model?.dirty).toBe(false);
    expect(editor!.model?.phase).toBe("clean");
  });

  it("returns to the old base after a confirmed ordinary PUT failure", async () => {
    const mine = "mine\n";
    const pendingPut = deferred<PutFileResult>();
    vi.mocked(putFile).mockReturnValue(pendingPut.promise);
    const current = await renderEditor();
    await act(async () => current.edit(mine));

    let save!: Promise<boolean>;
    await act(async () => {
      save = current.save();
      await Promise.resolve();
    });
    let discard!: Promise<boolean>;
    await act(async () => {
      discard = current.discard();
      await Promise.resolve();
    });
    pendingPut.resolve({
      ok: false,
      status: 400,
      error: { code: "invalid_request", message: "rejected" },
    });
    await act(async () => {
      await Promise.all([save, discard]);
    });

    expect(editor!.model?.content).toBe(baseContent);
    expect(editor!.model?.baseDiskRevision).toBe(initial.revision);
    expect(editor!.model?.dirty).toBe(false);
    expect(editor!.model?.phase).toBe("clean");
  });

  it("waits for SaveStateUnknown recovery before choosing the live base", async () => {
    const mine = "mine\n";
    const pendingPut = deferred<PutFileResult>();
    const pendingGet = deferred<EditableFile>();
    vi.mocked(putFile).mockReturnValue(pendingPut.promise);
    vi.mocked(getEditableFile).mockReturnValue(pendingGet.promise);
    const current = await renderEditor();
    await act(async () => current.edit(mine));

    let save!: Promise<boolean>;
    await act(async () => {
      save = current.save();
      await Promise.resolve();
    });
    let discard!: Promise<boolean>;
    await act(async () => {
      discard = current.discard();
      await Promise.resolve();
    });
    pendingPut.resolve({
      ok: false,
      status: 500,
      error: { code: "write_state_unknown", message: "unknown" },
    });
    pendingGet.resolve(editableFile(mine));
    await act(async () => {
      await Promise.all([save, discard]);
    });

    expect(editor!.model?.content).toBe(mine);
    expect(editor!.model?.baseDiskRevision).toBe(revisionOf(mine));
    expect(editor!.model?.dirty).toBe(false);
    expect(editor!.model?.phase).toBe("clean");
  });

  it("refuses discard when ConflictRemoteUnavailable cannot be recovered", async () => {
    vi.mocked(putFile).mockResolvedValue({
      ok: false,
      status: 409,
      error: { code: "revision_conflict", message: "conflict" },
    });
    vi.mocked(getEditableFile).mockRejectedValue(new Error("offline"));
    const current = await renderEditor();
    await act(async () => current.edit("mine\n"));
    await act(async () => {
      await current.save();
    });
    expect(editor!.model?.phase).toBe("conflict_remote_unavailable");

    let discarded = true;
    await act(async () => {
      discarded = await current.discard();
    });
    expect(discarded).toBe(false);
    expect(editor!.model?.phase).toBe("conflict_remote_unavailable");
    expect(editor!.model?.dirty).toBe(true);
  });

  it("refuses discard when SaveStateUnknown cannot be recovered", async () => {
    vi.mocked(putFile).mockResolvedValue({
      ok: false,
      status: 500,
      error: { code: "write_state_unknown", message: "unknown" },
    });
    vi.mocked(getEditableFile).mockRejectedValue(new Error("offline"));
    const current = await renderEditor();
    await act(async () => current.edit("mine\n"));
    await act(async () => {
      await current.save();
    });
    expect(editor!.model?.phase).toBe("save_state_unknown");

    let discarded = true;
    await act(async () => {
      discarded = await current.discard();
    });
    expect(discarded).toBe(false);
    expect(editor!.model?.phase).toBe("save_state_unknown");
    expect(editor!.model?.dirty).toBe(true);
  });
});
