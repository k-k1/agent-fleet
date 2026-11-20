import { beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../core/api/client.ts")>()),
  api: (...a: unknown[]) => api(...a),
  apiJSON: (...a: unknown[]) => apiJSON(...a),
}));

import { READ_TIMEOUT_MS, SAVE_TIMEOUT_MS, decodeEntry, encodeEntry, entryFile, memoryEntryIO, resetMemoryRevisions, storedForm } from "./memoryEntryIO.ts";
import type { MemoryBody } from "../settings/memory/memoryTypes.ts";
import { revisionOf } from "../editor/buffer.ts";
import { useTenantStore } from "../../core/store/tenant.ts";

const body: MemoryBody = { name: "n", scope: "user" as const, description: "d", type: "user", updated: "", pinned: false, uses: 0, revision: 3, authorKind: "member", authorSession: "", kinds: ["claude"], body: "text" } as MemoryBody;

describe("memory entry buffer", () => {
  it("round-trips the fields, whatever the description holds", () => {
    const f = { description: 'a "quoted"\nmulti-line {x}', type: "project", body: "line 1\n\nline 3\n" };
    expect(decodeEntry(encodeEntry(f))).toEqual(f);
  });

  it("keeps the body readable after one header line", () => {
    expect(encodeEntry({ description: "d", type: "user", body: "text" })).toBe('{"description":"d","type":"user"}\ntext');
  });

  it("takes text that is not an encoded entry as a bare body", () => {
    expect(decodeEntry("just text\nmore")).toEqual({ description: "", type: "feedback", body: "just text\nmore" });
  });
});

describe("memoryEntryIO", () => {
  beforeEach(() => {
    api.mockReset();
    apiJSON.mockReset();
    resetMemoryRevisions();
    useTenantStore.setState({ tenant: "t1" });
  });

  it("refuses to save or read under another workspace than the one it was made in", async () => {
    const io = memoryEntryIO({ key: "k", scope: "user", project: "", name: "n" }, () => {});
    const file = entryFile("k", body);
    useTenantStore.setState({ tenant: "t2" });
    const res = await io.save("k", file.content, file.revision);
    expect(res).toMatchObject({ ok: false, error: { code: "tenant_changed" } });
    await expect(io.load("k")).rejects.toThrow("tenant_changed");
    expect(apiJSON).not.toHaveBeenCalled();
    expect(api).not.toHaveBeenCalled();
  });

  it("answers a snapshot failure after the commit as an unknown outcome", async () => {
    const io = memoryEntryIO({ key: "k", scope: "user", project: "", name: "n" }, () => {});
    const file = entryFile("k", body);
    apiJSON.mockResolvedValue({ error: { code: "memory_snapshot_failed", message: "x" } });
    expect(await io.save("k", file.content, file.revision)).toMatchObject({ ok: false, error: { code: "write_state_unknown" } });
  });

  it("does not take an empty answer for a memory", async () => {
    const io = memoryEntryIO({ key: "k", scope: "user", project: "", name: "n" }, () => {});
    api.mockResolvedValue({});
    await expect(io.load("k")).rejects.toThrow();
  });

  it("answers a custom-coded 5xx as an unknown outcome", async () => {
    const io = memoryEntryIO({ key: "k", scope: "user", project: "", name: "n" }, () => {});
    const file = entryFile("k", body);
    apiJSON.mockResolvedValue({ error: { code: "gateway_stuff", message: "x", status: 504 } });
    expect(await io.save("k", file.content, file.revision)).toMatchObject({ ok: false, error: { code: "write_state_unknown" } });
  });

  it("does not take an answer for another memory, or a save answer with a bad revision, as good", async () => {
    const io = memoryEntryIO({ key: "k", scope: "user", project: "", name: "n" }, () => {});
    api.mockResolvedValue({ ...body, name: "other" });
    await expect(io.load("k")).rejects.toThrow();
    const file = entryFile("k", body);
    apiJSON.mockResolvedValue({ revision: 0 });
    await expect(io.save("k", file.content, file.revision)).rejects.toThrow();
  });

  it("keeps the counter of text it already knows when the same text is read again", async () => {
    const io = memoryEntryIO({ key: "k", scope: "user", project: "", name: "n" }, () => {});
    const file = entryFile("k", body);
    api.mockResolvedValue({ ...body, revision: 6, kinds: ["codex"] });
    await io.load("k");
    apiJSON.mockResolvedValue({ revision: 7 });
    await io.save("k", file.content, file.revision);
    expect(apiJSON.mock.calls[0][2]).toMatchObject({ revision: 3, kinds: ["claude"] });
  });

  it("takes the counter and kinds of the remote read that follows a lost save", async () => {
    const io = memoryEntryIO({ key: "k", scope: "user", project: "", name: "n" }, () => {});
    const file = entryFile("k", body);
    apiJSON.mockResolvedValueOnce({ error: { code: "memory_conflict", message: "x" } });
    expect(await io.save("k", file.content, file.revision)).toMatchObject({ ok: false, status: 409 });
    api.mockResolvedValue({ ...body, revision: 5, kinds: ["codex"] });
    await io.load("k");
    apiJSON.mockResolvedValue({ revision: 6 });
    await io.save("k", file.content, file.revision);
    expect(apiJSON.mock.calls[1][2]).toMatchObject({ revision: 5, kinds: ["codex"] });
  });

  it("reads a memory the Agent sent without type or body", async () => {
    const io = memoryEntryIO({ key: "k", scope: "user", project: "", name: "n" }, () => {});
    api.mockResolvedValue({ name: "n", scope: "user", description: "", revision: 2 });
    const file = await io.load("k");
    expect(decodeEntry(file.content)).toEqual({ description: "", type: "feedback", body: "" });
  });

  it("still adopts the remote's counter after the adapter was rebuilt (a tab switch) between the conflict and the read", async () => {
    const target = { key: "k", scope: "user", project: "", name: "n" };
    const file = entryFile("k", body);
    apiJSON.mockResolvedValueOnce({ error: { code: "memory_conflict", message: "x" } });
    await memoryEntryIO(target, () => {}).save("k", file.content, file.revision);
    const rebuilt = memoryEntryIO(target, () => {});
    api.mockResolvedValue({ ...body, revision: 5, kinds: ["codex"] });
    await rebuilt.load("k");
    apiJSON.mockResolvedValue({ revision: 6 });
    await rebuilt.save("k", file.content, file.revision);
    expect(apiJSON.mock.calls[1][2]).toMatchObject({ revision: 5, kinds: ["codex"] });
  });

  it("keeps the adoption flag when the first read after a conflict cannot be edited", async () => {
    const io = memoryEntryIO({ key: "k", scope: "user", project: "", name: "n" }, () => {});
    const file = entryFile("k", body);
    apiJSON.mockResolvedValueOnce({ error: { code: "memory_conflict", message: "x" } });
    await io.save("k", file.content, file.revision);
    api.mockResolvedValueOnce({ ...body, revision: 5, body: "a\rb" });
    await expect(io.load("k")).rejects.toThrow();
    api.mockResolvedValue({ ...body, revision: 6, kinds: ["codex"] });
    await io.load("k");
    apiJSON.mockResolvedValue({ revision: 7 });
    await io.save("k", file.content, file.revision);
    expect(apiJSON.mock.calls[1][2]).toMatchObject({ revision: 6, kinds: ["codex"] });
  });

  const spaced = (b: string) => encodeEntry({ description: "d", type: "user", body: b });
  const target = { key: "k", scope: "user", project: "", name: "n" };

  async function lostAnswer(read: Partial<MemoryBody>) {
    const io = memoryEntryIO(target, () => {});
    const file = entryFile("k", { ...body, body: "x" });
    const sentText = spaced("x  \n");
    apiJSON.mockResolvedValueOnce({ error: { code: "memory_snapshot_failed", message: "x" } });
    await io.save("k", sentText, file.revision);
    api.mockResolvedValue({ ...body, body: "x", ...read });
    return { io, file, sentText, read: await io.load("k") };
  }

  it("matches the text sent when the Agent's trimmed copy is read at the counter the save would have made", async () => {
    const { io, sentText, read } = await lostAnswer({ revision: 4 });
    expect(read.content).toBe(sentText);
    expect(read.revision).toBe(revisionOf(sentText));
    apiJSON.mockResolvedValue({ revision: 5 });
    await io.save("k", sentText, read.revision);
    expect(apiJSON.mock.calls[1][2]).toMatchObject({ revision: 4, kinds: ["claude"] });
  });

  it("matches a hand-written memory whose kinds repeat, as the Agent saves them deduplicated", async () => {
    const io = memoryEntryIO(target, () => {});
    const file = entryFile("k", { ...body, body: "x", kinds: ["claude", "claude"] });
    const sentText = spaced("x \n");
    apiJSON.mockResolvedValueOnce({ error: { code: "memory_snapshot_failed", message: "x" } });
    await io.save("k", sentText, file.revision);
    api.mockResolvedValue({ ...body, body: "x", revision: 4, kinds: ["claude"] });
    expect((await io.load("k")).content).toBe(sentText);
  });

  it("matches kinds the Agent trimmed on save", async () => {
    const io = memoryEntryIO(target, () => {});
    const file = entryFile("k", { ...body, body: "x", kinds: [" claude "] });
    const sentText = spaced("x \n");
    apiJSON.mockResolvedValueOnce({ error: { code: "memory_snapshot_failed", message: "x" } });
    await io.save("k", sentText, file.revision);
    api.mockResolvedValue({ ...body, body: "x", revision: 4, kinds: ["claude"] });
    expect((await io.load("k")).content).toBe(sentText);
  });

  it("does not take the unchanged memory, or another writer's update, for the save that was sent", async () => {
    // The save never landed: the memory is still at the counter it was sent from.
    const old = await lostAnswer({ revision: 3 });
    expect(old.read.content).toBe(spaced("x"));
    // Someone else wrote the same text at the next counter, with other kinds.
    const other = await lostAnswer({ revision: 4, kinds: ["codex"] });
    expect(other.read.content).toBe(spaced("x"));
  });

  it("carries the live counter into a resave when another writer stored the text the save started from", async () => {
    const { io, file, sentText, read } = await lostAnswer({ revision: 9, kinds: ["codex"] });
    expect(read.content).toBe(spaced("x"));
    expect(read.revision).toBe(file.revision);
    apiJSON.mockResolvedValue({ revision: 10 });
    await io.save("k", sentText, read.revision);
    expect(apiJSON.mock.calls[1][2]).toMatchObject({ revision: 9, kinds: ["codex"] });
  });

  it("does not let a read that began before the conflict use up the adoption flag", async () => {
    const io = memoryEntryIO(target, () => {});
    const file = entryFile("k", body);
    // The follow read starts first and answers last.
    let release!: (v: unknown) => void;
    api.mockImplementationOnce(() => new Promise((r) => (release = r)));
    const early = io.load("k");
    apiJSON.mockResolvedValueOnce({ error: { code: "memory_conflict", message: "x" } });
    await io.save("k", file.content, file.revision);
    api.mockResolvedValueOnce({ ...body, revision: 5, kinds: ["codex"] });
    await io.load("k");
    release({ ...body, revision: 4 });
    await early;
    apiJSON.mockResolvedValue({ revision: 6 });
    await io.save("k", file.content, file.revision);
    expect(apiJSON.mock.calls[1][2]).toMatchObject({ revision: 5, kinds: ["codex"] });
  });

  it("does not take a read for the text sent once the save is known to have been refused as a conflict", async () => {
    const io = memoryEntryIO(target, () => {});
    const file = entryFile("k", { ...body, body: "x" });
    apiJSON.mockResolvedValueOnce({ error: { code: "memory_conflict", message: "x" } });
    await io.save("k", spaced("x \n"), file.revision);
    api.mockResolvedValue({ ...body, body: "x", revision: 6, kinds: ["codex"] });
    const read = await io.load("k");
    expect(read.content).toBe(spaced("x"));
  });

  it("trims the way the Agent does: U+0085 goes, U+FEFF stays", () => {
    expect(storedForm(spaced("a\u0085 "))).toBe(spaced("a"));
    expect(storedForm(spaced("\ufeffa"))).toBe(spaced("\ufeffa"));
  });

  it("aborts a save and a read that never answer, so the pane is not held forever", async () => {
    vi.useFakeTimers();
    try {
      const io = memoryEntryIO({ key: "k", scope: "user", project: "", name: "n" }, () => {});
      const file = entryFile("k", body);
      const hang = (...a: unknown[]) => {
        const signal = (a.at(-1) as { signal?: AbortSignal }).signal!;
        return new Promise((_, reject) => signal.addEventListener("abort", () => reject(new Error("aborted"))));
      };
      apiJSON.mockImplementation(hang);
      api.mockImplementation((_p: string, init: unknown) => hang(init));
      const saved = io.save("k", file.content, file.revision);
      const savedErr = expect(saved).rejects.toThrow("aborted");
      await vi.advanceTimersByTimeAsync(SAVE_TIMEOUT_MS);
      await savedErr;
      const read = io.load("k");
      const readErr = expect(read).rejects.toThrow("aborted");
      await vi.advanceTimersByTimeAsync(READ_TIMEOUT_MS);
      await readErr;
    } finally {
      vi.useRealTimers();
    }
  });

  it("lets a memory whose recovery cannot be read be discarded", () => {
    expect(memoryEntryIO({ key: "k", scope: "user", project: "", name: "n" }, () => {}).discardUnconfirmed).toBe(true);
  });
});
