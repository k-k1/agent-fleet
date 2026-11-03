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
  memoList: async () => listed,
}));
// GET api/memos: what the queue holds on the server.
let listed: unknown[] = [];

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
  listed = [];
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
  // same memo, leaves an untouched copy of the memo as the draft: clear it, do not save it as
  // another memo. Edited, it is the person's work and is kept (review of #3293).
  it("clears an untouched copy of the memo instead of saving a duplicate", async () => {
    localStorage.setItem("af.launch-prompt.app", "本文");
    await writeAttachDraft(launchAttachKey("app"), [
      { name: "paste-1.png", type: "image/png", image: true, path: "", file: new File(["I"], "paste-1.png", { type: "image/png" }) },
    ]);
    expect(await startFromMemo(memo())).toEqual({ ok: true });
    expect(createMock).not.toHaveBeenCalled();
    expect(readLaunchPrompt("app")).toBe("");
    expect(await readAttachDraft(launchAttachKey("app"))).toEqual([]);
    // The seed carries the memo in full: the dialog refills from it, not from a draft.
    expect(useLaunchSeed.getState().prompt).toBe("本文");
    expect(useLaunchSeed.getState().attachments).toHaveLength(1);
  });

  // Restoring memo C over an untouched copy of memo A (restored earlier, then cancelled).
  it("clears an untouched copy of another memo still in the queue", async () => {
    listed = [memo({ id: "mA", body: "Aの本文", attachments: [] })];
    localStorage.setItem("af.launch-prompt.app", "Aの本文");
    expect(await startFromMemo(memo())).toEqual({ ok: true });
    expect(createMock).not.toHaveBeenCalled();
    expect(readLaunchPrompt("app")).toBe("");
  });

  it("keeps an edited copy as a memo", async () => {
    localStorage.setItem("af.launch-prompt.app", "本文（追記）");
    expect(await startFromMemo(memo())).toEqual({ ok: true, saved: true });
    expect(createMock).toHaveBeenCalledWith({ kind: "text", repo: "app", body: "本文（追記）" });
  });

  it("keeps a copy whose files changed as a memo", async () => {
    localStorage.setItem("af.launch-prompt.app", "本文");
    expect(await startFromMemo(memo())).toEqual({ ok: true, saved: true }); // the image was removed
  });

  it("keeps a copy whose file was swapped for another as a memo", async () => {
    localStorage.setItem("af.launch-prompt.app", "本文");
    await writeAttachDraft(launchAttachKey("app"), [
      { name: "other.png", type: "image/png", image: true, path: "", file: new File(["O"], "other.png", { type: "image/png" }) },
    ]);
    expect(await startFromMemo(memo())).toEqual({ ok: true, saved: true });
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
