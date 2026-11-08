import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, create, type ReactTestRenderer } from "react-test-renderer";
import {
  cancelDirtyGuardRequest,
  clearDirtyRegistryForTests,
  confirmDirtyNavigation,
  currentDirtyGuardRequest,
  dirtyPaneIds,
  discardDirtyGuardRequest,
  saveDirtyGuardRequest,
} from "./dirtyRegistry.ts";
import {
  getEditableFile,
  putFile,
  suggestEdit,
  type EditableFile,
  type PutFileResult,
  type SuggestEditResult,
} from "./api.ts";
import { revisionOf } from "./buffer.ts";
import { clearEditorSlotsForTests, useFileEditor } from "./useFileEditor.ts";

vi.mock("./api.ts", () => ({
  getEditableFile: vi.fn(),
  putFile: vi.fn(),
  suggestEdit: vi.fn(),
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

// What the view hands the hook; a test may swap it before a remount (another file, or a newer
// copy of the same one).
let given: typeof initial = initial;

function Harness() {
  editor = useFileEditor("p1", given);
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
  given = initial;
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.mocked(putFile).mockReset();
  vi.mocked(getEditableFile).mockReset();
  vi.mocked(suggestEdit).mockReset();
});

afterEach(async () => {
  await act(async () => {
    renderer?.unmount();
  });
  renderer = null;
  editor = null;
  clearDirtyRegistryForTests();
  clearEditorSlotsForTests();
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

describe("useFileEditor save classification", () => {
  it("treats a rejected PUT (unreadable 200 body) as SaveStateUnknown, not a failed save", async () => {
    vi.mocked(putFile).mockRejectedValue(new Error("invalid save response"));
    vi.mocked(getEditableFile).mockRejectedValue(new Error("offline"));
    const current = await renderEditor();
    await act(async () => current.edit("mine\n"));
    await act(async () => {
      await current.save();
    });

    expect(editor!.model?.phase).toBe("save_state_unknown");
    expect(editor!.model?.dirty).toBe(true);
    expect(editor!.model?.saveSnapshot).not.toBeNull();
  });
});

describe("useFileEditor discard abort", () => {
  it("keeps the dirty buffer when the guard aborts while awaiting the PUT", async () => {
    const mine = "mine\n";
    const pendingPut = deferred<PutFileResult>();
    vi.mocked(putFile).mockReturnValue(pendingPut.promise);
    const current = await renderEditor();
    await act(async () => current.edit(mine));

    const controller = new AbortController();
    let save!: Promise<boolean>;
    let discard!: Promise<boolean>;
    await act(async () => {
      save = current.save();
      await Promise.resolve();
      discard = current.discard(controller.signal);
      await Promise.resolve();
    });
    controller.abort();
    pendingPut.resolve({
      ok: false,
      status: 400,
      error: { code: "invalid_request", message: "rejected" },
    });
    let discarded = true;
    await act(async () => {
      await save;
      discarded = await discard;
    });

    expect(discarded).toBe(false);
    expect(editor!.model?.content).toBe(mine);
    expect(editor!.model?.dirty).toBe(true);
    expect(editor!.model?.phase).toBe("dirty");
  });

  it("keeps SaveStateUnknown when the guard aborts while recovery is pending", async () => {
    const mine = "mine\n";
    const pendingPut = deferred<PutFileResult>();
    const pendingGet = deferred<EditableFile>();
    vi.mocked(putFile).mockReturnValue(pendingPut.promise);
    vi.mocked(getEditableFile).mockReturnValue(pendingGet.promise);
    const current = await renderEditor();
    await act(async () => current.edit(mine));

    const controller = new AbortController();
    let save!: Promise<boolean>;
    let discard!: Promise<boolean>;
    await act(async () => {
      save = current.save();
      await Promise.resolve();
      discard = current.discard(controller.signal);
      await Promise.resolve();
    });
    await act(async () => {
      pendingPut.resolve({
        ok: false,
        status: 500,
        error: { code: "write_state_unknown", message: "unknown" },
      });
      await Promise.resolve();
    });
    controller.abort();
    pendingGet.resolve(editableFile(mine));
    let discarded = true;
    await act(async () => {
      await save;
      discarded = await discard;
    });

    expect(discarded).toBe(false);
    expect(editor!.model?.content).toBe(mine);
    expect(editor!.model?.dirty).toBe(true);
    expect(editor!.model?.phase).toBe("save_state_unknown");
  });

  it("popstate-style guard cancel during a delayed discard refuses navigation without wiping the buffer", async () => {
    // Full chain: the hook registers its entry, the guard starts a discard that
    // waits on an in-flight PUT, and Back (DirtyGuardHost's popstate handler)
    // cancels the request. The cancellation must propagate into the pending
    // discard so the buffer survives even though the modal already closed.
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

    const decision = confirmDirtyNavigation("history");
    const request = currentDirtyGuardRequest()!;
    let discardRun!: Promise<boolean>;
    await act(async () => {
      discardRun = discardDirtyGuardRequest(request.id);
      await Promise.resolve();
    });

    cancelDirtyGuardRequest(request.id);
    await expect(decision).resolves.toBe(false);
    expect(currentDirtyGuardRequest()).toBeNull();

    pendingPut.resolve({
      ok: false,
      status: 400,
      error: { code: "invalid_request", message: "rejected" },
    });
    await act(async () => {
      await save;
      await expect(discardRun).resolves.toBe(false);
    });

    expect(editor!.model?.content).toBe(mine);
    expect(editor!.model?.dirty).toBe(true);
    expect(editor!.model?.phase).toBe("dirty");
  });
});

describe("useFileEditor external change handling (docs/log/44 §7)", () => {
  const external = "external\n";

  it("auto-follows a clean buffer and reports the fetched file", async () => {
    vi.mocked(getEditableFile).mockResolvedValue(editableFile(external));
    const current = await renderEditor();

    let followed: EditableFile | null = null;
    await act(async () => {
      followed = await current.applyProbeResult({ kind: "revision", revision: revisionOf(external) });
    });

    expect(followed).toEqual(editableFile(external));
    expect(editor!.model?.content).toBe(external);
    expect(editor!.model?.baseDiskRevision).toBe(revisionOf(external));
    expect(editor!.model?.phase).toBe("clean");
    expect(editor!.model?.dirty).toBe(false);
    expect(editor!.model?.followEpoch).toBe(1);
  });

  it("does not mistake its own save for an external change", async () => {
    const mine = "mine\n";
    vi.mocked(putFile).mockResolvedValue({
      ok: true,
      path: initial.path,
      size: new TextEncoder().encode(mine).byteLength,
      revision: revisionOf(mine),
    });
    const current = await renderEditor();
    await act(async () => current.edit(mine));
    await act(async () => {
      await current.save();
    });
    expect(editor!.model?.phase).toBe("saved");

    await act(async () => {
      await current.applyProbeResult({ kind: "revision", revision: revisionOf(mine) });
    });

    expect(getEditableFile).not.toHaveBeenCalled();
    expect(editor!.model?.content).toBe(mine);
    expect(editor!.model?.phase).toBe("saved");
    expect(editor!.model?.followEpoch).toBe(0);
    expect(editor!.model?.externalObservation).toBeNull();
  });

  it("keeps a dirty buffer and phase, recording only an advisory", async () => {
    const current = await renderEditor();
    await act(async () => current.edit("mine\n"));

    await act(async () => {
      await current.applyProbeResult({ kind: "revision", revision: revisionOf(external) });
    });

    expect(getEditableFile).not.toHaveBeenCalled();
    expect(editor!.model?.phase).toBe("dirty");
    expect(editor!.model?.content).toBe("mine\n");
    expect(editor!.model?.externalObservation).toEqual({
      kind: "changed",
      revision: revisionOf(external),
    });

    await act(async () => {
      await current.applyProbeResult({ kind: "revision", revision: revisionOf("mine\n") });
    });
    expect(editor!.model?.externalObservation).toEqual({
      kind: "same_as_buffer",
      revision: revisionOf("mine\n"),
    });
    expect(editor!.model?.phase).toBe("dirty");
  });

  it("drops an auto-follow that raced a fresh edit", async () => {
    const pendingGet = deferred<EditableFile>();
    vi.mocked(getEditableFile).mockReturnValue(pendingGet.promise);
    const current = await renderEditor();

    let follow!: Promise<EditableFile | null>;
    await act(async () => {
      follow = current.applyProbeResult({ kind: "revision", revision: revisionOf(external) });
      await Promise.resolve();
    });
    await act(async () => current.edit("typed while fetching\n"));
    pendingGet.resolve(editableFile(external));

    let followed: EditableFile | null = null;
    await act(async () => {
      followed = await follow;
    });

    expect(followed).toBeNull();
    expect(editor!.model?.content).toBe("typed while fetching\n");
    expect(editor!.model?.phase).toBe("dirty");
    expect(editor!.model?.followEpoch).toBe(0);
  });

  it("records missing/uneditable observations without touching the buffer", async () => {
    const current = await renderEditor();
    await act(async () => {
      await current.applyProbeResult({ kind: "missing" });
    });
    expect(editor!.model?.phase).toBe("clean");
    expect(editor!.model?.externalObservation).toEqual({ kind: "missing" });

    await act(async () => {
      await current.applyProbeResult({ kind: "uneditable", reason: "unsupported_newline" });
    });
    expect(editor!.model?.externalObservation).toEqual({
      kind: "uneditable",
      reason: "unsupported_newline",
    });
    expect(editor!.model?.content).toBe(baseContent);
  });

  it("opens the ordinary conflict UI from the explicit diff check", async () => {
    vi.mocked(getEditableFile).mockResolvedValue(editableFile(external));
    const current = await renderEditor();
    await act(async () => current.edit("mine\n"));

    await act(async () => {
      await current.confirmExternalChange();
    });

    expect(editor!.model?.phase).toBe("conflict");
    expect(editor!.model?.conflict?.content).toBe(external);
    expect(editor!.model?.content).toBe("mine\n");
  });

  it("classifies an identical external write as same-as-buffer, not a conflict", async () => {
    vi.mocked(getEditableFile).mockResolvedValue(editableFile("mine\n"));
    const current = await renderEditor();
    await act(async () => current.edit("mine\n"));

    await act(async () => {
      await current.confirmExternalChange();
    });

    expect(editor!.model?.phase).toBe("dirty");
    expect(editor!.model?.externalObservation).toEqual({
      kind: "same_as_buffer",
      revision: revisionOf("mine\n"),
    });
  });

  it("falls to conflict-remote-unavailable when the diff check cannot fetch", async () => {
    vi.mocked(getEditableFile).mockRejectedValue(new Error("offline"));
    const current = await renderEditor();
    await act(async () => current.edit("mine\n"));

    await act(async () => {
      await current.confirmExternalChange();
    });

    expect(editor!.model?.phase).toBe("conflict_remote_unavailable");
    expect(editor!.model?.content).toBe("mine\n");
  });
});

describe("useFileEditor AI suggestion (docs/log/44 §4)", () => {
  it("records a suggestion whose envelope matches the request-time identity", async () => {
    vi.mocked(suggestEdit).mockResolvedValue({ ok: true, summary: "改善", replacement: "new" });
    const current = await renderEditor();
    let code: string | null = "unset";
    await act(async () => {
      code = await current.requestSuggestion("直して", { from: 0, to: 4 });
    });
    expect(code).toBeNull();
    const envelope = editor!.model!.suggestion!;
    expect(envelope.paneId).toBe("p1");
    expect(envelope.filePath).toBe(initial.path);
    expect(envelope.sourceRevision).toBe(initial.revision);
    expect(envelope.suggestion).toMatchObject({
      replacement: "new",
      range: { from: 0, to: 4 },
      baseRevision: initial.revision,
    });
    expect(vi.mocked(suggestEdit).mock.calls[0][0]).toMatchObject({
      path: initial.path,
      instruction: "直して",
      selection: "base",
    });
  });

  it("rejects a response computed on an older buffer as suggestion_stale", async () => {
    const pending = deferred<SuggestEditResult>();
    vi.mocked(suggestEdit).mockReturnValue(pending.promise);
    const current = await renderEditor();
    let request!: Promise<string | null>;
    await act(async () => {
      request = current.requestSuggestion("直して", { from: 0, to: 4 });
      await Promise.resolve();
      current.edit("typed while generating\n");
    });
    pending.resolve({ ok: true, summary: "改善", replacement: "new" });
    let code: string | null = null;
    await act(async () => {
      code = await request;
    });
    expect(code).toBe("suggestion_stale");
    expect(editor!.model!.suggestion).toBeNull();
  });

  it("accept mutates only the in-memory buffer and never calls PUT", async () => {
    vi.mocked(suggestEdit).mockResolvedValue({ ok: true, summary: "改善", replacement: "new" });
    const current = await renderEditor();
    await act(async () => {
      await current.requestSuggestion("直して", { from: 0, to: 4 });
    });
    let code: string | null = "unset";
    await act(async () => {
      code = current.acceptSuggestion();
    });
    expect(code).toBeNull();
    expect(editor!.model!.content).toBe("new\n");
    expect(editor!.model!.dirty).toBe(true);
    expect(editor!.model!.phase).toBe("dirty");
    expect(editor!.model!.suggestion).toBeNull();
    expect(vi.mocked(putFile)).not.toHaveBeenCalled();
  });

  it("accept through the view seam only retires the suggestion; the view's onChange owns the buffer", async () => {
    vi.mocked(suggestEdit).mockResolvedValue({ ok: true, summary: "改善", replacement: "new" });
    const current = await renderEditor();
    await act(async () => {
      await current.requestSuggestion("直して", { from: 0, to: 4 });
    });
    const applied: Array<{ from: number; to: number; insert: string }> = [];
    await act(async () => {
      expect(
        current.acceptSuggestion((edit) => {
          applied.push(edit);
          return true;
        }),
      ).toBeNull();
    });
    expect(applied).toEqual([{ from: 0, to: 4, insert: "new" }]);
    expect(editor!.model!.suggestion).toBeNull();
    expect(vi.mocked(putFile)).not.toHaveBeenCalled();
  });

  it("reject leaves the buffer untouched", async () => {
    vi.mocked(suggestEdit).mockResolvedValue({ ok: true, summary: "改善", replacement: "new" });
    const current = await renderEditor();
    await act(async () => {
      await current.requestSuggestion("直して", { from: 0, to: 4 });
    });
    await act(async () => {
      current.rejectSuggestion();
    });
    expect(editor!.model!.suggestion).toBeNull();
    expect(editor!.model!.content).toBe(baseContent);
    expect(editor!.model!.dirty).toBe(false);
  });

  it("a cancelled request drops its late response", async () => {
    const pending = deferred<SuggestEditResult>();
    vi.mocked(suggestEdit).mockReturnValue(pending.promise);
    const current = await renderEditor();
    let request!: Promise<string | null>;
    await act(async () => {
      request = current.requestSuggestion("直して", { from: 0, to: 4 });
      await Promise.resolve();
      current.cancelSuggestion();
    });
    pending.resolve({ ok: true, summary: "改善", replacement: "new" });
    let code: string | null = "unset" as string | null;
    await act(async () => {
      code = await request;
    });
    expect(code).toBeNull();
    expect(editor!.model!.suggestion).toBeNull();
    expect(editor!.suggesting).toBe(false);
  });
});

// A tabbed cell renders only its selected view, so a tab switch unmounts the editor and
// coming back mounts a new one for the same view id and file.
describe("useFileEditor across unmount (tab switch)", () => {
  const unmount = async () => {
    await act(async () => {
      renderer?.unmount();
    });
    renderer = null;
  };

  it("keeps an unsaved edit and its guard, and hands it back on remount", async () => {
    const current = await renderEditor();
    await act(async () => current.edit("mine\n"));
    await unmount();
    expect(dirtyPaneIds()).toEqual(["p1"]);

    const back = await renderEditor();
    expect(back.model?.content).toBe("mine\n");
    expect(back.model?.dirty).toBe(true);
    expect(dirtyPaneIds()).toEqual(["p1"]);
  });

  it("parks nothing for a clean editor", async () => {
    await renderEditor();
    await unmount();
    expect(dirtyPaneIds()).toEqual([]);
    const back = await renderEditor();
    expect(back.model?.content).toBe(baseContent);
  });

  it("saves a parked edit from the guard and then lets it go", async () => {
    vi.mocked(putFile).mockResolvedValue({ ok: true, path: initial.path, size: 5, revision: revisionOf("mine\n") });
    const current = await renderEditor();
    await act(async () => current.edit("mine\n"));
    await unmount();

    const decision = confirmDirtyNavigation("reload");
    const request = currentDirtyGuardRequest()!;
    await act(async () => {
      await saveDirtyGuardRequest(request.id);
    });
    await expect(decision).resolves.toBe(true);
    expect(vi.mocked(putFile)).toHaveBeenCalledWith(initial.path, "mine\n", initial.revision);
    expect(dirtyPaneIds()).toEqual([]);
    // Let go, not kept: the next mount starts from the file it was given, not the saved buffer
    // (the disk may have moved on since).
    const back = await renderEditor();
    expect(back.model?.content).toBe(baseContent);
  });

  it("discards a parked edit from the guard and then lets it go", async () => {
    const current = await renderEditor();
    await act(async () => current.edit("mine\n"));
    await unmount();

    const decision = confirmDirtyNavigation("layout");
    const request = currentDirtyGuardRequest()!;
    await act(async () => {
      await discardDirtyGuardRequest(request.id);
    });
    await expect(decision).resolves.toBe(true);
    expect(dirtyPaneIds()).toEqual([]);
    const back = await renderEditor();
    expect(back.model?.content).toBe(baseContent);
    expect(back.model?.dirty).toBe(false);
  });

  it("adopts the outcome of a save that was still in flight when it unmounted", async () => {
    const pendingPut = deferred<PutFileResult>();
    vi.mocked(putFile).mockReturnValue(pendingPut.promise);
    const current = await renderEditor();
    await act(async () => current.edit("mine\n"));
    await act(async () => {
      void current.save();
      await Promise.resolve();
    });
    await unmount();

    await renderEditor();
    await act(async () => {
      pendingPut.resolve({ ok: true, path: initial.path, size: 5, revision: revisionOf("mine\n") });
      await pendingPut.promise;
    });
    expect(editor!.model?.content).toBe("mine\n");
    expect(editor!.model?.dirty).toBe(false);
    expect(editor!.model?.baseDiskRevision).toBe(revisionOf("mine\n"));
  });

  it("keeps the guard through a remount while a save is in flight, and after a failed save", async () => {
    const pendingPut = deferred<PutFileResult>();
    vi.mocked(putFile).mockReturnValue(pendingPut.promise);
    const current = await renderEditor();
    await act(async () => current.edit("mine\n"));
    await act(async () => {
      void current.save();
      await Promise.resolve();
    });
    await unmount();
    await renderEditor();
    expect(dirtyPaneIds()).toEqual(["p1"]);
    await unmount(); // away again before the save settles
    expect(dirtyPaneIds()).toEqual(["p1"]);

    await act(async () => {
      pendingPut.resolve({ ok: false, status: 400, error: { code: "bad_request", message: "no" } });
      await pendingPut.promise;
    });
    expect(dirtyPaneIds()).toEqual(["p1"]);
    const back = await renderEditor();
    expect(back.model?.content).toBe("mine\n");
    expect(back.model?.dirty).toBe(true);
  });

  it("lets go of a buffer whose save succeeds while no view shows it", async () => {
    const pendingPut = deferred<PutFileResult>();
    vi.mocked(putFile).mockReturnValue(pendingPut.promise);
    const current = await renderEditor();
    await act(async () => current.edit("mine\n"));
    await act(async () => {
      void current.save();
      await Promise.resolve();
    });
    await unmount();
    await act(async () => {
      pendingPut.resolve({ ok: true, path: initial.path, size: 5, revision: revisionOf("mine\n") });
      await pendingPut.promise;
    });
    expect(dirtyPaneIds()).toEqual([]);
    const back = await renderEditor();
    expect(back.model?.content).toBe(baseContent); // a fresh start from the file given
  });

  it("keeps guarding the left-behind buffer when the view comes back on another file", async () => {
    const current = await renderEditor();
    await act(async () => current.edit("mine\n"));
    await unmount();

    const otherContent = "other\n";
    given = { path: "repos/b.txt", content: otherContent, revision: revisionOf(otherContent) };
    const other = await renderEditor();
    expect(other.model?.path).toBe("repos/b.txt");
    expect(other.model?.dirty).toBe(false);
    expect(dirtyPaneIds()).toEqual(["p1"]);
    const decision = confirmDirtyNavigation("layout", ["p1"]);
    expect(currentDirtyGuardRequest()!.entries.map((e) => e.label)).toEqual([initial.path]);
    cancelDirtyGuardRequest(currentDirtyGuardRequest()!.id);
    await expect(decision).resolves.toBe(false);
  });

  it("keeps guarding a buffer whose guard save fails", async () => {
    vi.mocked(putFile).mockResolvedValue({ ok: false, status: 400, error: { code: "bad_request", message: "no" } });
    const current = await renderEditor();
    await act(async () => current.edit("mine\n"));
    await unmount();
    const decision = confirmDirtyNavigation("reload");
    const request = currentDirtyGuardRequest()!;
    let saved = true;
    await act(async () => {
      saved = await saveDirtyGuardRequest(request.id);
    });
    expect(saved).toBe(false);
    cancelDirtyGuardRequest(request.id);
    await expect(decision).resolves.toBe(false);
    expect(dirtyPaneIds()).toEqual(["p1"]);
  });

  it("flags a file that changed on disk while the view was away", async () => {
    const current = await renderEditor();
    await act(async () => current.edit("mine\n"));
    await unmount();
    const disk = "someone else\n";
    given = { path: initial.path, content: disk, revision: revisionOf(disk) };
    const back = await renderEditor();
    expect(back.model?.content).toBe("mine\n");
    expect(back.model?.externalObservation).toMatchObject({ kind: "changed", revision: revisionOf(disk) });
  });

  it("discards and saves a buffer left behind on another file through the guard", async () => {
    vi.mocked(putFile).mockResolvedValue({ ok: true, path: initial.path, size: 5, revision: revisionOf("mine\n") });
    const current = await renderEditor();
    await act(async () => current.edit("mine\n"));
    await unmount();
    const otherContent = "other\n";
    given = { path: "repos/b.txt", content: otherContent, revision: revisionOf(otherContent) };
    await renderEditor();
    await act(async () => editor!.edit("other edit\n"));
    expect(dirtyPaneIds()).toEqual(["p1"]);

    // Save the one left behind only: the shown file keeps its own edit.
    const decision = confirmDirtyNavigation("layout", ["p1"]);
    const request = currentDirtyGuardRequest()!;
    expect(request.entries.map((e) => e.label)).toEqual([initial.path, "repos/b.txt"]);
    await act(async () => {
      await request.entries[0]!.save();
    });
    expect(vi.mocked(putFile)).toHaveBeenCalledWith(initial.path, "mine\n", initial.revision);
    expect(editor!.model?.content).toBe("other edit\n");
    expect(editor!.model?.dirty).toBe(true);
    cancelDirtyGuardRequest(request.id);
    await expect(decision).resolves.toBe(false);
    expect(currentDirtyGuardRequest()).toBeNull();
    // Only the shown file is still unsaved; the saved one was let go.
    const again = confirmDirtyNavigation("layout", ["p1"]);
    const second = currentDirtyGuardRequest()!;
    expect(second.entries.map((e) => e.label)).toEqual(["repos/b.txt"]);
    await act(async () => {
      await discardDirtyGuardRequest(second.id);
    });
    await expect(again).resolves.toBe(true);
    expect(dirtyPaneIds()).toEqual([]);
  });

  it("calls it the same as the buffer when the disk now holds exactly the unsaved text", async () => {
    const current = await renderEditor();
    await act(async () => current.edit("mine\n"));
    await unmount();
    given = { path: initial.path, content: "mine\n", revision: revisionOf("mine\n") };
    const back = await renderEditor();
    expect(back.model?.externalObservation).toMatchObject({ kind: "same_as_buffer", revision: revisionOf("mine\n") });
  });

  it("does not let a hook that moved to another file run the old file's guard on the new buffer", async () => {
    const pendingPut = deferred<PutFileResult>();
    vi.mocked(putFile).mockReturnValue(pendingPut.promise);
    const current = await renderEditor();
    await act(async () => current.edit("mine\n"));
    await act(async () => {
      void current.save();
      await Promise.resolve();
    });
    // Same hook instance, another file (no remount).
    const otherContent = "other\n";
    given = { path: "repos/b.txt", content: otherContent, revision: revisionOf(otherContent) };
    await act(async () => {
      renderer!.update(<Harness />);
    });
    expect(editor!.model?.path).toBe("repos/b.txt");
    await act(async () => {
      pendingPut.resolve({ ok: true, path: initial.path, size: 5, revision: revisionOf("mine\n") });
      await pendingPut.promise;
    });
    // The new file's buffer did not take the old save's outcome.
    expect(editor!.model?.content).toBe(otherContent);
    expect(editor!.model?.baseDiskRevision).toBe(revisionOf(otherContent));
    expect(editor!.model?.dirty).toBe(false);
    // The old buffer is still guarded, and its guard does not act on the new one: the new
    // file's own unsaved edit must survive a discard aimed at the old file.
    await act(async () => editor!.edit("other edit\n"));
    const decision = confirmDirtyNavigation("layout", ["p1"]);
    const request = currentDirtyGuardRequest()!;
    expect(request.entries.map((e) => e.label)).toEqual([initial.path, "repos/b.txt"]);
    let discarded = true;
    await act(async () => {
      discarded = await request.entries[0]!.discard() !== false;
    });
    expect(discarded).toBe(false); // fail closed
    expect(editor!.model?.content).toBe("other edit\n");
    expect(editor!.model?.dirty).toBe(true);
    cancelDirtyGuardRequest(request.id);
    await expect(decision).resolves.toBe(false);
  });
});
