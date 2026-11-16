// useModelRouteProfiles hands a principal only its own answer, on every render: on the render where
// the tenant or member has just changed, before any effect runs, the previous one's profiles are
// not returned.
import { describe, it, expect, vi } from "vitest";
import { act } from "react";
import { createRoot } from "react-dom/client";

let pending: ((v: unknown) => void) | null = null;
const api = vi.fn((path: string) =>
  path === "api/ssm/profiles" && pending !== null
    ? new Promise((r) => (pending = r))
    : Promise.resolve([{ id: "alice", label: "Alice profile", region: "us-east-1", modelRoute: true }]),
);
vi.mock("../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../core/api/client.ts")>()),
  api: (path: string) => api(path),
}));

const { useModelRouteProfiles } = await import("./bedrockProfiles.ts");
const { useTenantStore } = await import("../../core/store/tenant.ts");

describe("useModelRouteProfiles across a member switch", () => {
  it("returns nothing for the new member on the very render of the switch", async () => {
    const seen: (string[] | null)[] = [];
    function Probe() {
      const p = useModelRouteProfiles(true);
      seen.push(p ? p.map((x) => x.label) : null);
      return null;
    }
    const host = document.createElement("div");
    const root = createRoot(host);
    await act(async () => root.render(<Probe />));
    await act(async () => {
      await Promise.resolve();
    });
    expect(seen.at(-1)).toEqual(["Alice profile"]);
    // Bob's request is held; record what each render returns from the switch on.
    pending = () => {};
    seen.length = 0;
    await act(async () => useTenantStore.setState((s) => ({ identityRev: s.identityRev + 1 })));
    expect(seen.length).toBeGreaterThan(0);
    expect(seen.every((p) => p === null)).toBe(true);
    // Bob's answer lands.
    await act(async () => pending!([{ id: "bob", label: "Bob profile", region: "eu-west-1", modelRoute: true }]));
    expect(seen.at(-1)).toEqual(["Bob profile"]);
    act(() => root.unmount());
  });
});
