// A JSON `null` body reads as an empty success, as the `res?.error` call sites did before the
// typed wrappers: no wrapper hands null to a caller that reads `res.error` / `res.results`.
import { describe, it, expect, vi } from "vitest";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...a: unknown[]) => api(...a),
  apiJSON: (...a: unknown[]) => apiJSON(...a),
}));
const { getMemoryTree, getProjectMovePreview, restoreMemory, getMemorySnapshots, getConsolProposals, moveMemoryProject, revertMemoryChange, putCodexMemories } =
  await import("./memoryApi.ts");

describe("memoryApi null bodies", () => {
  it("turns a null read into {}", async () => {
    api.mockResolvedValue(null);
    expect(await getMemorySnapshots(100)).toEqual({});
    expect(await getConsolProposals()).toEqual({});
  });
  it("turns a null write into {}", async () => {
    apiJSON.mockResolvedValue(null);
    expect(await moveMemoryProject("p", { from: "p", to: "q", collision: "skip", items: [] })).toEqual({});
    expect(await revertMemoryChange("c", false, false)).toEqual({});
    expect(await putCodexMemories(true)).toEqual({});
  });
  it("keeps null where the caller needs the whole object (previews, commit results)", async () => {
    api.mockResolvedValue(null);
    apiJSON.mockResolvedValue(null);
    expect(await getProjectMovePreview("from=a&to=b")).toBeNull();
    expect(await getMemoryTree("r")).toBeNull();
    expect(await restoreMemory("r", { rev: "r", scope: { all: true } })).toBeNull();
  });
  it("keeps the extra fields of a refusal", async () => {
    const r = { error: { code: "x", message: "m" }, results: [{ name: "n" }], stopped: "n" };
    apiJSON.mockResolvedValue(r);
    expect(await moveMemoryProject("p", { from: "p", to: "q", collision: "skip", items: [] })).toEqual(r);
  });
});
