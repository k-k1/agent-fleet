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

const { useModelOptions } = await import("./agentModels.ts");

function Probe({ kind }: { kind: string }) {
  const opts = useModelOptions(kind);
  return <span data-testid="n">{opts ? opts.length : -1}</span>;
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
});
