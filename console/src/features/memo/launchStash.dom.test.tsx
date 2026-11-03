// "Start with this" on a memo (#3289): the launch seed carries the memo's text and files, the
// dialog opens on the memo's repository when this workspace has it, and the memo goes away
// only once a session was launched from it.
import "fake-indexeddb/auto";
import { IDBFactory } from "fake-indexeddb";
import { beforeEach, describe, expect, it, vi } from "vitest";

const deleteMock = vi.fn(async (_id: string) => new Response(null, { status: 204 }));
const createMock = vi.fn(async (_input: unknown): Promise<unknown> => ({ id: "stashed" }));
const pasteMock = vi.fn(async (f: File) => ({ status: 201, path: "/m/paste-1-" + f.name, name: "paste-1-" + f.name }));
vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => "",
  api: async () => ({}),
  isTransientErr: () => false,
  memoDelete: (id: string) => deleteMock(id),
  memoCreate: (input: unknown) => createMock(input),
  memoPasteImage: (f: File) => pasteMock(f),
}));

const { startFromMemo, canStartFromMemo } = await import("./launchStash.ts");
const { isMemoImageName, memoAttachmentLabel } = await import("./attachName.ts");
const { useLaunchSeed, useLaunchTarget, useReposStore } = await import("../repos/store.ts");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useMemoStore } = await import("./store.ts");
const { readLaunchPrompt, launchAttachKey } = await import("../repos/launchDraft.ts");
const { readAttachDraft, writeAttachDraft, resetAttachDraftDB } = await import("../../lib/attachDraft.ts");
const { pendingMemoUploads, stashLaunchDraft } = await import("./launchStash.ts");
const { registerToastSink } = await import("../../ui/toast.ts");
import type { Memo } from "../../types/memo.ts";

const memo = (over: Partial<Memo> = {}): Memo => ({
  id: "m1",
  repo: "app",
  category: "",
  kind: "text",
  body: "本文",
  refPath: "",
  attachments: [{ path: "/x/paste-1.png", name: "paste-1.png" }],
  position: 0,
  createdAt: "",
  sentAt: "",
  ...over,
});

beforeEach(() => {
  localStorage.clear();
  globalThis.indexedDB = new IDBFactory();
  resetAttachDraftDB();
  deleteMock.mockClear();
  createMock.mockClear();
  pasteMock.mockClear();
  useLaunchSeed.getState().clear();
  useLaunchTarget.getState().clear();
  useReposStore.setState({ repos: [{ name: "app", path: "/home/dev/repos/app" }] });
});

describe("startFromMemo", () => {
  it("seeds text and files and opens the memo's repository directly", async () => {
    const tick = useSessionsStore.getState().startTick;
    await startFromMemo(memo());
    const seed = useLaunchSeed.getState();
    expect(seed.prompt).toBe("本文");
    expect(seed.attachments).toEqual([{ path: "/x/paste-1.png", name: "paste-1.png" }]);
    expect(useLaunchTarget.getState().target?.name).toBe("app");
    expect(useSessionsStore.getState().startTick).toBe(tick); // not the hub
    expect(seed.memoId).toBe("m1");
    expect(createMock).not.toHaveBeenCalled(); // no stored draft, nothing to set aside
  });

  // The memo's text outranks the dialog's stored draft, so that draft would be overwritten
  // (and its files mixed into the memo's) without this.
  it("saves the repository's stored draft to a memo before opening over it", async () => {
    localStorage.setItem("af.launch-prompt.app", "書きかけ");
    await writeAttachDraft(launchAttachKey("app"), [
      { name: "a.log", type: "text/plain", image: false, path: "", file: new File(["A"], "a.log", { type: "text/plain" }) },
    ]);
    const r = await startFromMemo(memo());
    expect(r).toEqual({ ok: true, saved: true });
    expect(pasteMock.mock.calls.map(([f]) => f.name)).toEqual(["a.log"]);
    expect(createMock).toHaveBeenCalledWith({ kind: "text", repo: "app", body: "書きかけ", attachments: [{ path: "/m/paste-1-a.log", name: "paste-1-a.log" }] });
    expect(readLaunchPrompt("app")).toBe("");
    expect(await readAttachDraft(launchAttachKey("app"))).toEqual([]);
    expect(useLaunchTarget.getState().target?.name).toBe("app");
  });

  it("opens nothing and keeps the stored draft when it cannot be saved", async () => {
    localStorage.setItem("af.launch-prompt.app", "書きかけ");
    createMock.mockResolvedValueOnce({ error: { code: "x" } });
    const r = await startFromMemo(memo());
    expect(r.ok).toBe(false);
    expect(readLaunchPrompt("app")).toBe("書きかけ");
    expect(useLaunchTarget.getState().target).toBeNull();
    expect(useLaunchSeed.getState().prompt).toBe("");
  });

  it("falls back to the hub when the repository is unknown or unset", async () => {
    for (const repo of ["gone", ""]) {
      useLaunchTarget.getState().clear();
      const tick = useSessionsStore.getState().startTick;
      await startFromMemo(memo({ repo }));
      expect(useLaunchTarget.getState().target).toBeNull();
      expect(useSessionsStore.getState().startTick).toBe(tick + 1);
    }
  });

  it("deletes the memo only after a launch, and a cancelled dialog drops the hook", async () => {
    await startFromMemo(memo());
    expect(deleteMock).not.toHaveBeenCalled();
    const bumps = useMemoStore.getState().tick;
    useLaunchSeed.getState().onLaunched?.("s1");
    await vi.waitFor(() => expect(useMemoStore.getState().tick).toBe(bumps + 1));
    expect(deleteMock).toHaveBeenCalledWith("m1");

    deleteMock.mockClear();
    await startFromMemo(memo({ id: "m2" }));
    useLaunchSeed.getState().clear(); // the dialog closed without launching
    expect(useLaunchSeed.getState().onLaunched).toBeNull();
    expect(useLaunchSeed.getState().attachments).toEqual([]);
    expect(useLaunchSeed.getState().memoId).toBe("");
  });
});

// Findings from the review of #3293.
describe("races and failures", () => {
  // Reading while the closed dialog's write is still reading bytes would miss the file, and
  // the clear after it would cancel that write too: the file would be in neither draft.
  it("waits for an in-flight attachment write before taking the draft over", async () => {
    let give!: () => void;
    const slow = new File(["SLOW"], "slow.log", { type: "text/plain" });
    const gate = new Promise<void>((r) => (give = r));
    slow.arrayBuffer = async () => {
      await gate;
      return new TextEncoder().encode("SLOW").buffer as ArrayBuffer;
    };
    void writeAttachDraft(launchAttachKey("app"), [{ name: "slow.log", type: "text/plain", image: false, path: "", file: slow }]);
    const p = startFromMemo(memo());
    await new Promise((r) => setTimeout(r, 10));
    give();
    expect(await p).toEqual({ ok: true, saved: true });
    expect(pasteMock.mock.calls.map(([f]) => f.name)).toEqual(["slow.log"]);
  });

  it("keeps a stash's uploads out of the image GC until its memo can be listed", async () => {
    expect(await stashLaunchDraft("app", "x", [new File(["A"], "b.log")])).toEqual({ ok: true });
    expect(pendingMemoUploads()).toContain("paste-1-b.log");
  });

  // Backing out to the hub and picking the same repo again, or cancelling and restoring the
  // same memo, must resume the draft instead of saving the memo's own text as another memo.
  it("resumes a draft that is already this memo's instead of saving a copy", async () => {
    localStorage.setItem("af.launch-prompt.app", "メモの本文（編集済み）");
    localStorage.setItem("af.launch-memo.app", "m1");
    expect(await startFromMemo(memo())).toEqual({ ok: true });
    expect(createMock).not.toHaveBeenCalled();
    const seed = useLaunchSeed.getState();
    expect(seed.prompt).toBe(""); // the dialog opens on the stored draft
    expect(seed.attachments).toEqual([]);
    expect(seed.memoId).toBe("m1");

    // Another memo still sets the draft aside.
    await startFromMemo(memo({ id: "m2" }));
    expect(createMock).toHaveBeenCalledTimes(1);
  });

  it("says so when the restored memo cannot be deleted after the launch", async () => {
    const shown: unknown[] = [];
    registerToastSink((m) => void shown.push(m));
    try {
      deleteMock.mockResolvedValueOnce(new Response(null, { status: 500 }));
      await startFromMemo(memo());
      useLaunchSeed.getState().onLaunched?.("s1");
      await vi.waitFor(() => expect(shown).toHaveLength(1));
      expect(String(shown[0])).toMatch(/could not be deleted|削除できませんでした/);
    } finally {
      registerToastSink(null);
    }
  });
});

describe("canStartFromMemo", () => {
  it("offers only the member's own text memos with something in them", () => {
    expect(canStartFromMemo(memo())).toBe(true);
    expect(canStartFromMemo(memo({ body: "", attachments: [] }))).toBe(false);
    expect(canStartFromMemo(memo({ body: "", attachments: [{ path: "/x/a", name: "paste-1.png" }] }))).toBe(true);
    expect(canStartFromMemo(memo({ kind: "file" }))).toBe(false);
    expect(canStartFromMemo(memo({ origin: { itemId: "i", sender: "a" } }))).toBe(false);
  });
});

describe("attachment names", () => {
  it("tells images from other files and shows a file's original name", () => {
    expect(isMemoImageName("paste-12.png")).toBe(true);
    expect(isMemoImageName("paste-12-run.log")).toBe(false);
    expect(memoAttachmentLabel("paste-12-run.log")).toBe("run.log");
    expect(memoAttachmentLabel("paste-12.jpg")).toBe("paste-12.jpg");
  });
});
