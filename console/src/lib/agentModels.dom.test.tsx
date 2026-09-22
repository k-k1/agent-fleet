// A kind whose connection is edited in Settings must not serve a CACHED model list for the
// rest of the Console load.
//
// Damage (reported on a real machine, 2026-09-21): a member saved their own llama.cpp
// connection (docs/log/107) and the launch picker kept offering the DEPLOYMENT engine's
// models — the list the Agent had already stopped answering. The Agent was right the whole
// time (GET /agents/lcpp/models answered the member connection's own list, and an empty one
// once that box went down); the stale copy was this module's per-load cache, which opencode
// was already exempt from for exactly the same reason and lcpp was not.
//
// So the assertion is about the REQUEST, not the rendered list: mounting the picker twice has
// to reach the Agent twice for a volatile kind, and exactly once for a cacheable one.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn(async (_path: string) => ({ models: [{ id: "m1", label: "m1" }] }));
vi.mock("../core/api/client.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return { ...real, api: (path: string) => api(path), isTransientErr: () => false };
});

const { requiresConcreteModel, resolveQuickLaunchModel, useAutoConcreteModel, useModelOptions } =
  await import("./agentModels.ts");

function Probe({ kind }: { kind: string }) {
  const opts = useModelOptions(kind);
  return <span data-testid="n">{opts ? opts.length : -1}</span>;
}

function AutoProbe({ kind, model, onChange }: { kind: string; model: string; onChange: (m: string) => void }) {
  useAutoConcreteModel(kind, model, onChange);
  return null;
}

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  api.mockClear();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

async function mountOnce(kind: string) {
  await act(async () => {
    root.render(<Probe kind={kind} />);
  });
  await act(async () => {
    root.render(<></>);
  });
}

describe("model catalog caching", () => {
  it("re-asks the Agent on every mount for lcpp — the member connection can change from Settings mid-load", async () => {
    await mountOnce("lcpp");
    const first = api.mock.calls.filter((c) => String(c[0]).includes("agents/lcpp/models")).length;
    expect(first).toBe(1);

    await mountOnce("lcpp");
    const second = api.mock.calls.filter((c) => String(c[0]).includes("agents/lcpp/models")).length;
    expect(second).toBe(2);
  });

  it("still caches a kind whose catalog cannot change from Settings", async () => {
    await mountOnce("codex");
    await mountOnce("codex");
    const calls = api.mock.calls.filter((c) => String(c[0]).includes("agents/codex/models")).length;
    expect(calls).toBe(1);
  });

  // lcpp offers no "let the CLI decide" entry (docs/log/109): the mocked catalog above never
  // includes one, so a returned list of length 1 for lcpp means exactly the real model and
  // nothing else — the assertion a caller relying on defaultOnly's old unconditional Default
  // entry would have missed.
  it("offers no Default entry for lcpp", async () => {
    function Probe2() {
      const opts = useModelOptions("lcpp");
      return <span data-testid="ids">{(opts || []).map(([id]) => id).join(",")}</span>;
    }
    await act(async () => {
      root.render(<Probe2 />);
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(host.querySelector('[data-testid="ids"]')?.textContent).toBe("m1");
  });

  it("still offers a Default entry for a kind with a real CLI-picked default (negative control)", async () => {
    function Probe2() {
      const opts = useModelOptions("codex");
      return <span data-testid="ids">{(opts || []).map(([id]) => id).join(",")}</span>;
    }
    await act(async () => {
      root.render(<Probe2 />);
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(host.querySelector('[data-testid="ids"]')?.textContent).toBe(",m1");
  });
});

describe("requiresConcreteModel", () => {
  it("is true for lcpp and false for every other launchable kind", () => {
    expect(requiresConcreteModel("lcpp")).toBe(true);
    for (const kind of ["claude", "codex", "opencode", "copilot", "cursor", "kiro", "agy", "muse", "shell"]) {
      expect(requiresConcreteModel(kind)).toBe(false);
    }
  });
});

describe("useAutoConcreteModel", () => {
  it("auto-picks the catalog's sole entry for lcpp once it settles", async () => {
    const onChange = vi.fn();
    await act(async () => {
      root.render(<AutoProbe kind="lcpp" model="" onChange={onChange} />);
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(onChange).toHaveBeenCalledWith("m1");
  });

  it("never overrides an already-picked model", async () => {
    const onChange = vi.fn();
    await act(async () => {
      root.render(<AutoProbe kind="lcpp" model="already-picked" onChange={onChange} />);
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(onChange).not.toHaveBeenCalled();
  });

  it("is a no-op for a kind with a real CLI-picked default (negative control)", async () => {
    const onChange = vi.fn();
    await act(async () => {
      root.render(<AutoProbe kind="codex" model="" onChange={onChange} />);
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(onChange).not.toHaveBeenCalled();
  });
});

describe("resolveQuickLaunchModel", () => {
  it("awaits the live catalog and returns its first entry when lcpp resolved to nothing", async () => {
    await expect(resolveQuickLaunchModel("lcpp", "")).resolves.toBe("m1");
  });

  it("returns an already-resolved model unchanged, without a fetch", async () => {
    api.mockClear();
    await expect(resolveQuickLaunchModel("lcpp", "already-picked")).resolves.toBe("already-picked");
    expect(api).not.toHaveBeenCalled();
  });

  it("is a no-op for a kind with a real CLI-picked default (negative control)", async () => {
    api.mockClear();
    await expect(resolveQuickLaunchModel("codex", "")).resolves.toBe("");
    expect(api).not.toHaveBeenCalled();
  });
});
