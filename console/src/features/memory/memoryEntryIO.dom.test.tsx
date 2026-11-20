import { beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../core/api/client.ts")>()),
  api: (...a: unknown[]) => api(...a),
  apiJSON: (...a: unknown[]) => apiJSON(...a),
}));

import { decodeEntry, encodeEntry, entryFile, memoryEntryIO, resetMemoryRevisions } from "./memoryEntryIO.ts";
import type { MemoryBody } from "../settings/memory/memoryTypes.ts";
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

  it("reads a memory the Agent sent without type, description or body", async () => {
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
    api.mockResolvedValue({ ...body, revision: 5 });
    await rebuilt.load("k");
    apiJSON.mockResolvedValue({ revision: 6 });
    await rebuilt.save("k", file.content, file.revision);
    expect(apiJSON.mock.calls[1][2]).toMatchObject({ revision: 5 });
  });
});
