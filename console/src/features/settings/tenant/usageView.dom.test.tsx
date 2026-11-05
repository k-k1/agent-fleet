// UsageView's member list shares its columns across rows (#3382): the list's modifier and each
// row's cells must agree, or every cell shifts one column and the bars stop lining up.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => "http://localhost/" + p,
}));

import { UsageView } from "./tenantOps.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount(el: React.ReactElement) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(el);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
});

const tenants = [{ slug: "acme", name: "Acme" }] as any;
const totals = [
  { tenant: "acme", user_key: "alice", email: "a@x.com", running_secs: 7200 },
  { tenant: "", user_key: "bob", email: "b@x.com", running_secs: 3600 },
];

function gridMatchesRows() {
  const list = host!.querySelector(".usage-rows.ur-table")!;
  const withTenant = list.classList.contains("ur-with-tenant");
  const rows = Array.from(list.querySelectorAll(":scope > .usage-row"));
  expect(rows.length).toBe(2);
  for (const r of rows) expect(r.children.length).toBe(withTenant ? 4 : 3);
  return withTenant;
}

describe("UsageView member list", () => {
  it("super admin, all tenants: tenant column on, empty tenant keeps its cell", async () => {
    api.mockResolvedValue({ from: "a", to: "b", totals });
    await mount(<UsageView tenants={tenants} isSuper={true} />);
    expect(gridMatchesRows()).toBe(true);
  });

  it("super admin with one tenant selected: tenant column off", async () => {
    api.mockResolvedValue({ from: "a", to: "b", totals });
    await mount(<UsageView tenants={tenants} isSuper={true} />);
    const sel = host!.querySelector("select")!;
    await act(async () => {
      sel.value = "acme";
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(gridMatchesRows()).toBe(false);
  });

  it("tenant admin: tenant column off", async () => {
    api.mockResolvedValue({ from: "a", to: "b", totals });
    await mount(<UsageView tenants={tenants} isSuper={false} />);
    expect(gridMatchesRows()).toBe(false);
  });
});
