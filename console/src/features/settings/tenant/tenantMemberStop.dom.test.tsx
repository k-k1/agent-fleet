// The administrator's Force-stop in the member detail. A workspace stuck in "starting" has no
// other exit, so the button is offered for it as well as for a running one, and a refusal
// from the CP reaches the administrator instead of closing the dialog.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
const toast = vi.fn();

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
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));

import { MemberView } from "./tenantMemberDetail.tsx";

const MEMBER = { user_key: "a-x-com", email: "a@x.com", role: "member", max_sessions: 2, status: "active", state: "starting" };

let root: Root | null = null;
let host: HTMLDivElement | null = null;
let stats: Record<string, unknown> = {};

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<MemberView slug="acme" member={MEMBER} isSuper={false} onChanged={() => {}} onRemoved={() => {}} />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const buttonWith = (text: string) =>
  Array.from(document.querySelectorAll<HTMLButtonElement>("button")).find((b) => (b.textContent || "").trim() === text);

beforeEach(() => {
  apiJSON.mockReset();
  toast.mockReset();
  api.mockReset();
  api.mockImplementation((p: string) => Promise.resolve(p.endsWith("/stats") ? stats : { sessions: [], count: 0 }));
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("member detail: Force-stop", () => {
  it("is offered for a running workspace and for one that is still starting", async () => {
    for (const s of [{ running: true }, { running: false, starting: true }]) {
      stats = s;
      await mount();
      expect(buttonWith("ワークスペースを強制停止")!.disabled, JSON.stringify(s)).toBe(false);
      act(() => root?.unmount());
      host?.remove();
    }
  });

  it("stays off for a stopped workspace (positive control for the two above)", async () => {
    stats = { running: false };
    await mount();
    expect(buttonWith("ワークスペースを強制停止")!.disabled).toBe(true);
  });

  it("stops a starting workspace and says the start is cancelled", async () => {
    stats = { running: false, starting: true };
    apiJSON.mockResolvedValue({ stopped: true });
    await mount();
    await act(async () => buttonWith("ワークスペースを強制停止")!.click());
    expect(document.body.textContent).toContain("起動を取りやめます");
    await act(async () => buttonWith("停止する")!.click());
    expect(apiJSON).toHaveBeenCalledWith("api/admin/stop-workspace", "POST", { tenant_slug: "acme", user_key: "a-x-com" });
    expect(buttonWith("停止する")).toBeUndefined();
  });

  it("keeps the dialog open and says why when the CP refuses", async () => {
    stats = { running: false, starting: true };
    apiJSON.mockResolvedValue({ error: { code: "operation_in_progress", message: "another operation is in progress" } });
    await mount();
    await act(async () => buttonWith("ワークスペースを強制停止")!.click());
    await act(async () => buttonWith("停止する")!.click());
    expect(toast).toHaveBeenCalledWith("another operation is in progress");
    expect(buttonWith("停止する")).toBeDefined();
  });
});
