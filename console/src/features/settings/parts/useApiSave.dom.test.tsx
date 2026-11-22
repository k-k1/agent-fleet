// useApiSave: one write at a time, `saving` back to false on every outcome, a failure turned into
// text, and no state set after unmount.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { useApiSave, type SaveResult } from "./useApiSave.ts";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;
const roots: Root[] = [];
afterEach(() => roots.splice(0).forEach((r) => act(() => r.unmount())));

type W = (n: number) => Promise<unknown>;
let latest: { saving: boolean; save: (n: number) => Promise<SaveResult<unknown>> };
function Probe({ write }: { write: W }) {
  latest = useApiSave(write as never) as typeof latest;
  return <span>{latest.saving ? "saving" : "idle"}</span>;
}
async function mount(write: W) {
  const root = createRoot(document.createElement("div"));
  roots.push(root);
  await act(async () => root.render(<Probe write={write} />));
  return root;
}
const deferred = () => {
  let resolve!: (v: unknown) => void;
  const p = new Promise((r) => (resolve = r));
  return { p, resolve };
};

describe("useApiSave", () => {
  it("reports ok with the value and returns to idle", async () => {
    await mount(async () => ({ id: 1 }));
    let r!: SaveResult<unknown>;
    await act(async () => {
      r = await latest.save(1);
    });
    expect(r).toEqual({ status: "ok", value: { id: 1 } });
    expect(latest.saving).toBe(false);
  });

  it("keeps a JSON null body as an ok with a null value", async () => {
    await mount(async () => null);
    let r!: SaveResult<unknown>;
    await act(async () => {
      r = await latest.save(1);
    });
    expect(r).toEqual({ status: "ok", value: null });
  });

  it("turns an error reply into failed text and a rejection into its message", async () => {
    await mount(async (n) => {
      if (n === 1) return { error: { code: "zz_no_catalogue_entry", message: "nope" } };
      throw new Error("boom");
    });
    let a!: SaveResult<unknown>, b!: SaveResult<unknown>;
    await act(async () => {
      a = await latest.save(1);
      b = await latest.save(2);
    });
    expect(a.status).toBe("failed");
    expect(a.status === "failed" && a.error).toContain("nope");
    expect(b).toEqual({ status: "failed", error: "boom" });
    expect(latest.saving).toBe(false);
  });

  it("drops a second call while one is out", async () => {
    const d = deferred();
    const write = vi.fn(() => d.p);
    await mount(write);
    let first!: Promise<SaveResult<unknown>>;
    let second!: SaveResult<unknown>;
    await act(async () => {
      first = latest.save(1);
      second = await latest.save(1);
    });
    expect(second).toEqual({ status: "busy" });
    expect(write).toHaveBeenCalledTimes(1);
    expect(latest.saving).toBe(true);
    await act(async () => {
      d.resolve({});
      await first;
    });
    expect(latest.saving).toBe(false);
  });

  it("settles a write that finishes after unmount without touching state", async () => {
    const d = deferred();
    const root = await mount(() => d.p);
    const err = vi.spyOn(console, "error").mockImplementation(() => {});
    let first!: Promise<SaveResult<unknown>>;
    await act(async () => {
      first = latest.save(1);
    });
    await act(async () => root.unmount());
    roots.splice(roots.indexOf(root), 1);
    d.resolve({});
    await expect(first).resolves.toEqual({ status: "ok", value: {} });
    expect(err).not.toHaveBeenCalled();
    err.mockRestore();
  });
});
