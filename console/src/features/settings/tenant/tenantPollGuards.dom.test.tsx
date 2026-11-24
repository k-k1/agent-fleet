// The tenant views whose poll period no longer restarts with their subject (#4786): a read
// started for the tenant or member just left must never draw its answer on the one now open.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
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
  apiJSON.mockReset();
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
  const pick = async (slug: string) => {
    const select = host!.querySelector("select")!;
    await act(async () => {
      select.value = slug;
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
  };

  it("drops the answer for the tenant just left, even before the new tenant has answered", async () => {
    const all = hold();
    const b = hold();
    api.mockImplementation((p: string) => (p === "api/admin/sessions" ? all.next() : b.next()));
    await render(<AllSessionsView tenants={tenants} isSuper />);
    await pick("b");
    await all.answer({ sessions: [session("a", "ant")] }); // the all-tenants read, started first
    expect(host!.textContent).not.toContain("ant");
    await b.answer({ sessions: [session("b", "bee")] });
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
  const view = (key: string) => (
    <MemberView slug="acme" member={member(key) as never} isSuper={false} onChanged={() => {}} onRemoved={() => {}} />
  );
  const buttonWith = (text: string) =>
    Array.from(document.querySelectorAll<HTMLButtonElement>("button")).find((b) => (b.textContent || "").trim() === text);
  const stop = () => buttonWith("ワークスペースを強制停止");
  const named = (name: string) => ({ name, label: name, kind: "claude", alive: true, state: "running" });

  it("drops the stats and sessions of the member just left, even before the new member has answered", async () => {
    const stats = { alice: hold(), bob: hold() };
    const sess = { alice: hold(), bob: hold() };
    api.mockImplementation((p: string) => {
      const who = p.includes("/u-alice/") ? "alice" : p.includes("/u-bob/") ? "bob" : "";
      if (who && p.endsWith("/stats")) return stats[who].next();
      if (who && p.endsWith("/sessions")) return sess[who].next();
      return Promise.resolve({ sessions: [], count: 0 });
    });
    await render(view("u-alice"));
    await render(view("u-bob"));
    await stats.alice.answer({ running: true });
    await sess.alice.answer({ sessions: [named("alice-sess")] });
    expect(stop()!.disabled).toBe(true);
    expect(host!.textContent).not.toContain("alice-sess");
    await stats.bob.answer({ running: false });
    await sess.bob.answer({ sessions: [named("bob-sess")] });
    expect(stop()!.disabled).toBe(true);
    expect(host!.textContent).toContain("bob-sess");
  });

  it("drops the re-read of an action begun on the member just left", async () => {
    const stop1 = hold();
    let aliceRunning = true;
    apiJSON.mockImplementation(() => stop1.next());
    api.mockImplementation((p: string) =>
      p.endsWith("/u-alice/stats")
        ? Promise.resolve({ running: aliceRunning })
        : p.endsWith("/stats")
          ? Promise.resolve({ running: false })
          : Promise.resolve({ sessions: [], count: 0 }),
    );
    await render(view("u-alice"));
    await act(async () => stop()!.click());
    await act(async () => buttonWith("停止する")!.click()); // the stop is in flight
    await render(view("u-bob"));
    expect(stop()!.disabled).toBe(true);
    const aliceReads = api.mock.calls.filter((c) => String(c[0]).endsWith("/u-alice/stats")).length;
    aliceRunning = true;
    await stop1.answer({ error: { code: "operation_in_progress", message: "busy" } }); // re-reads alice
    expect(api.mock.calls.filter((c) => String(c[0]).endsWith("/u-alice/stats")).length).toBe(aliceReads + 1);
    await act(async () => {
      await Promise.resolve();
    });
    expect(stop()!.disabled).toBe(true);
  });

  it("a newer read's error answer does not drop an older read's good answer", async () => {
    const stats = hold();
    const sess = hold();
    api.mockImplementation((p: string) =>
      p.endsWith("/stats") ? stats.next() : p.endsWith("/sessions") ? sess.next() : Promise.resolve({}),
    );
    await render(view("u-alice"));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(4000); // a tick's reads join the first ones
    });
    await stats.answer({ error: { code: "unavailable" } }, 1);
    await sess.answer({ error: { code: "unavailable" } }, 1);
    await stats.answer({ running: true }, 0);
    await sess.answer({ sessions: [named("alice-sess")] }, 0);
    expect(stop()!.disabled).toBe(false);
    expect(host!.textContent).toContain("alice-sess");
  });
  it.each([
    ["404", { error: { code: "not_found", status: 404 } }],
    ["403", { error: { code: "forbidden", status: 403 } }],
    ["a non-JSON 404", { error: { code: "http_404" } }],
  ])("a newer %s answer is not overtaken by an older good read", async (_n, refusal) => {
    const stats = hold();
    const sess = hold();
    api.mockImplementation((p: string) =>
      p.endsWith("/stats") ? stats.next() : p.endsWith("/sessions") ? sess.next() : Promise.resolve({}),
    );
    await render(view("u-alice"));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(4000); // a tick's reads join the first ones
    });
    await stats.answer(refusal, 1);
    await sess.answer(refusal, 1);
    await stats.answer({ running: true }, 0); // the older read answers after the refusal
    await sess.answer({ sessions: [named("alice-sess")] }, 0);
    expect(host!.textContent).not.toContain("alice-sess");
    expect(stop()?.disabled ?? true).toBe(true); // the member is not shown as running
  });
});
