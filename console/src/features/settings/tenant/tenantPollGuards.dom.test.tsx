// The tenant views whose poll period no longer restarts with their subject (#4786): a read
// started for the tenant or member just left must never draw its answer on the one now open.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: vi.fn(),
  rawJSON: () => Promise.resolve(new Response("")),
  errText: (e: { message?: string; code?: string }) => e?.message || e?.code || "",
  rel: (p: string) => p,
}));
vi.mock("../../../core/store/tenant.ts", () => ({
  useTenantStore: (sel: (s: { whoami: null }) => unknown) => sel({ whoami: null }),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { AllSessionsView } from "./tenantOps.tsx";
import { MemberView } from "./tenantMemberDetail.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function render(node: React.ReactNode) {
  if (!root) {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
  }
  await act(async () => {
    root!.render(node);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

/** One promise per read, each answered by the test, in any order (by call index). */
function hold() {
  const pending: ((v: unknown) => void)[] = [];
  return {
    next: () => new Promise((r) => pending.push(r)),
    answer: async (v: unknown, i = 0) => {
      await act(async () => {
        pending[i](v);
        await Promise.resolve();
      });
    },
  };
}

const session = (tenant: string, name: string) => ({
  tenant,
  user_key: "u-" + name,
  name,
  label: name,
  kind: "claude",
  alive: true,
  state: "running",
});

beforeEach(() => {
  api.mockReset();
  vi.useFakeTimers({ shouldAdvanceTime: true });
});
afterEach(() => {
  vi.useRealTimers();
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("AllSessionsView", () => {
  const tenants = [
    { slug: "a", name: "A" },
    { slug: "b", name: "B" },
  ] as never;

  it("drops the answer for the tenant just left", async () => {
    const all = hold();
    api.mockImplementation((p: string) =>
      p === "api/admin/sessions" ? all.next() : Promise.resolve({ sessions: [session("b", "bee")] }),
    );
    await render(<AllSessionsView tenants={tenants} isSuper />);
    const select = host!.querySelector("select")!;
    await act(async () => {
      select.value = "b";
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(host!.textContent).toContain("bee");
    await all.answer({ sessions: [session("a", "ant")] }); // the all-tenants read, started first
    expect(host!.textContent).toContain("bee");
    expect(host!.textContent).not.toContain("ant");
  });

  it("drops a tick's read that answers after a newer one landed", async () => {
    const reads = hold();
    api.mockImplementation(() => reads.next());
    await render(<AllSessionsView tenants={tenants} isSuper />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000); // a tick's read joins the mount read
    });
    expect(api).toHaveBeenCalledTimes(2);
    await reads.answer({ sessions: [session("a", "ant")] }, 1); // the tick's read lands first
    expect(host!.textContent).toContain("ant");
    await reads.answer({ sessions: [session("a", "old")] }, 0); // the mount read, started earlier
    expect(host!.textContent).toContain("ant");
    expect(host!.textContent).not.toContain("old");
  });
});

describe("MemberView", () => {
  const member = (key: string) => ({ user_key: key, email: key + "@x.com", role: "member", status: "active", state: "running" });

  it("drops the stats of the member just left", async () => {
    const aliceStats = hold();
    api.mockImplementation((p: string) =>
      p.endsWith("/u-alice/stats")
        ? aliceStats.next()
        : p.endsWith("/stats")
          ? Promise.resolve({ running: false })
          : Promise.resolve({ sessions: [], count: 0 }),
    );
    const view = (key: string) => (
      <MemberView slug="acme" member={member(key) as never} isSuper={false} onChanged={() => {}} onRemoved={() => {}} />
    );
    await render(view("u-alice"));
    await render(view("u-bob"));
    const stop = () =>
      Array.from(host!.querySelectorAll<HTMLButtonElement>("button")).find(
        (b) => (b.textContent || "").trim() === "ワークスペースを強制停止",
      );
    expect(stop()!.disabled).toBe(true); // bob is stopped
    await aliceStats.answer({ running: true });
    expect(stop()!.disabled).toBe(true);
  });
});
