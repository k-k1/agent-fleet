// Per-member engine access (#1215). Pinned here:
//   1. The mode chips PUT {role, members_only} and the ticks PUT {membership_id, role, granted}.
//   2. A role the deployment admin denied the tenant is shown but cannot be edited — a tick
//      there could never take effect.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));
const toastSpy = vi.fn(() => () => {});
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => toastSpy() }));

import { MemberEngineAccessPanel, TenantEngineAccessView } from "./tenantEngineAccess.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount(node = <TenantEngineAccessView slug="acme" />) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(node);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const VIEW = {
  tenant: "acme",
  roles: [
    { role: "llm", tenant_allowed: true, members_only: true },
    { role: "image", tenant_allowed: false, members_only: false },
  ],
  members: [
    { membership_id: "M-a", user_key: "alice", email: "a@example.com", role: "tenant_admin", grants: ["llm"] },
    { membership_id: "M-b", user_key: "bob", email: "b@example.com", role: "member", grants: [] },
  ],
};

const group = (role: string) => document.querySelector<HTMLElement>(`.engine-access [data-role="${role}"]`)!;
const seg = (role: string, text: string) =>
  Array.from(group(role).querySelectorAll<HTMLButtonElement>(".seg .seg-btn")).find(
    (b) => (b.textContent || "").trim() === text,
  )!;
const tick = (label: string) => document.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`)!;

beforeEach(() => {
  toastSpy.mockReturnValue(() => {});
  api.mockResolvedValue(VIEW);
  apiJSON.mockResolvedValue({});
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.clearAllMocks();
});

describe("tenant engine access", () => {
  it("shows the mode per role and each member's ticks", async () => {
    await mount();
    expect(seg("llm", "許可したメンバーだけ").className).toContain("active");
    expect(seg("llm", "メンバー全員").className).not.toContain("active");
    // Positive control on the other role: an open role has the other button active.
    expect(seg("image", "メンバー全員").className).toContain("active");
    expect(tick("alice チャット（llm）").checked).toBe(true);
    expect(tick("bob チャット（llm）").checked).toBe(false);
  });

  it("opens a restricted role to everyone with one PUT", async () => {
    await mount();
    await act(async () => seg("llm", "メンバー全員").click());
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/acme/engine-access", "PUT", {
      role: "llm",
      members_only: false,
    });
  });

  it("grants a member through the members endpoint", async () => {
    await mount();
    await act(async () => tick("bob チャット（llm）").click());
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/acme/engine-access/members", "PUT", {
      membership_id: "M-b",
      role: "llm",
      granted: true,
    });
  });

  it("does not let a tenant-denied role be edited", async () => {
    await mount();
    // Positive control: the allowed role's controls are live.
    expect(seg("llm", "メンバー全員").disabled).toBe(false);
    expect(seg("image", "許可したメンバーだけ").disabled).toBe(true);
    expect(tick("bob 画像生成（image）").disabled).toBe(true);
    expect(group("image").textContent).toContain("デプロイ管理者");
  });
});

describe("tenant engine access failures", () => {
  it("says why the screen is empty instead of rendering nothing", async () => {
    api.mockResolvedValue({ error: { code: "forbidden", message: "not a tenant admin" } });
    await mount();
    expect(document.querySelector(".engine-access")!.textContent).toContain("not a tenant admin");
  });

  it("re-reads after a save so the screen shows what the server stored", async () => {
    await mount();
    const before = api.mock.calls.length;
    await act(async () => tick("bob チャット（llm）").click());
    expect(api.mock.calls.length).toBe(before + 1);
  });

  it("tells the admin when the save request itself fails", async () => {
    const toast = vi.fn();
    toastSpy.mockReturnValue(toast);
    apiJSON.mockRejectedValue(new Error("offline"));
    await mount();
    await act(async () => tick("bob チャット（llm）").click());
    expect(toast).toHaveBeenCalledWith("保存できませんでした。接続を確かめてもう一度お試しください。");
  });
});

describe("member detail engine access", () => {
  const panel = () => document.querySelector<HTMLElement>(".member-engine-access");
  const box = (role: string) => panel()!.querySelector<HTMLInputElement>(`[data-role="${role}"] input`)!;

  it("shows this member's ticks and what they mean right now", async () => {
    await mount(<MemberEngineAccessPanel slug="acme" userKey="bob" />);
    expect(box("llm").checked).toBe(false);
    expect(panel()!.querySelector('[data-role="llm"]')!.textContent).toContain("使えません");
    expect(box("image").disabled).toBe(true);
  });

  it("writes through the same members endpoint as the table", async () => {
    await mount(<MemberEngineAccessPanel slug="acme" userKey="bob" />);
    await act(async () => box("llm").click());
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/acme/engine-access/members", "PUT", {
      membership_id: "M-b",
      role: "llm",
      granted: true,
    });
  });

  it("renders nothing for someone not on the active roster", async () => {
    await mount(<MemberEngineAccessPanel slug="acme" userKey="gone" />);
    // Positive control is the first case above: the same mount does render for bob.
    expect(panel()).toBeNull();
  });
});

// #1491: the per-role headings were a second <h4> styled exactly like the panel title, so the
// panel and its role groups read as the same level. The panel keeps the one h4; each role is
// a sub-heading.
describe("tenant engine access — heading levels", () => {
  it("has one panel title and a sub-heading per role", async () => {
    await mount();
    const panel = document.querySelector<HTMLElement>(".engine-access")!;
    expect(panel.querySelectorAll("h4").length).toBe(1);
    expect(group("llm").querySelector("h5.admin-subhead")).not.toBeNull();
    expect(group("image").querySelector("h5.admin-subhead")).not.toBeNull();
  });

  it("styles the matrix as a shared admin table", async () => {
    await mount();
    expect(document.querySelector(".engine-access table")?.classList.contains("admin-table")).toBe(true);
  });
});
