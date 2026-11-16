// useMemberCatalog fails closed: a failed request leaves no kinds, and a new principal never sees
// the previous one's catalogue, even before its own answer arrives.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const pending: { resolve: (v: unknown) => void; reject: (e: unknown) => void }[] = [];
vi.mock("../../../core/api/client.ts", () => ({
  api: () => new Promise((resolve, reject) => pending.push({ resolve, reject })),
  apiJSON: async () => ({}),
  getTenant: () => "",
  getUser: () => "",
}));

const { useMemberCatalog, parseMemberCatalog } = await import("./memberCatalog.ts");
const { useTenantStore } = await import("../../../core/store/tenant.ts");

let root: Root | null = null;
let host: HTMLDivElement;
let seen: ReturnType<typeof useMemberCatalog> | null = null;

function Probe() {
  seen = useMemberCatalog(true);
  return null;
}

const answer = (kind: string) => ({ kinds: [{ kind }], access: { policyConfirmed: true } });
const flush = async () => {
  for (let i = 0; i < 3; i++) await act(async () => void (await Promise.resolve()));
};

beforeEach(() => {
  pending.length = 0;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("useMemberCatalog", () => {
  it("is pending, then holds the answer, and drops it when a later request fails", async () => {
    await act(async () => root!.render(<Probe />));
    expect(seen!.kinds).toBeNull();
    expect(seen!.pending).toBe(true);
    pending[0].resolve(answer("claude"));
    await flush();
    expect([...seen!.kinds!.keys()]).toEqual(["claude"]);
    await act(async () => void seen!.refresh());
    pending[pending.length - 1].reject(new Error("network"));
    await flush();
    expect(seen!.kinds).toBeNull();
    expect(seen!.pending).toBeFalsy();
  });

  it("never shows the previous principal's catalogue after an identity switch", async () => {
    await act(async () => root!.render(<Probe />));
    pending[0].resolve(answer("claude"));
    await flush();
    expect(seen!.kinds?.has("claude")).toBe(true);
    await act(async () => useTenantStore.setState((s) => ({ identityRev: s.identityRev + 1 })));
    expect(seen!.kinds).toBeNull();
    // The old principal's late answer is not taken either.
    const fresh = pending[pending.length - 1];
    fresh.resolve(answer("codex"));
    await flush();
    expect([...seen!.kinds!.keys()]).toEqual(["codex"]);
  });
});

// The gate every member screen filters by (ADR 0138 D10a, D12): a catalogue is trusted only when
// the Agent vouches that it is filtered by the policy in force.
describe("parseMemberCatalog", () => {
  const kinds = [{ kind: "claude", routes: ["anthropic-api"] }];

  it("refuses an answer that is not confirmed, has no access block, or is not a catalogue", () => {
    expect(parseMemberCatalog({ kinds, access: { policyConfirmed: false } })).toBeNull();
    expect(parseMemberCatalog({ kinds, access: {} })).toBeNull();
    // An older Agent: the same kinds, no access summary.
    expect(parseMemberCatalog({ kinds })).toBeNull();
    expect(parseMemberCatalog({ error: { code: "http_502" } })).toBeNull();
    expect(parseMemberCatalog(null)).toBeNull();
  });

  it("reads a confirmed answer: kinds, the two lines and the addable routes", () => {
    const c = parseMemberCatalog({
      kinds: [...kinds, { nokind: true }],
      access: { policyConfirmed: true, noRouteAvailable: false, allOffByOwnSettings: true, someOffByOwnSettings: true, addableRoutes: ["member-llamacpp", 3] },
    });
    expect(c).not.toBeNull();
    expect([...c!.kinds!.keys()]).toEqual(["claude"]);
    expect(c!.allOff).toBe(true);
    expect(c!.hiddenByOwn).toBe(true);
    expect(c!.noRoute).toBe(false);
    expect([...c!.addable]).toEqual(["member-llamacpp"]);
    expect(parseMemberCatalog({ kinds: [], access: { policyConfirmed: true, noRouteAvailable: true } })!.noRoute).toBe(true);
  });
});
