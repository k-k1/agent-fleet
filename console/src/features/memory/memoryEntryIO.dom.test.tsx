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
});
