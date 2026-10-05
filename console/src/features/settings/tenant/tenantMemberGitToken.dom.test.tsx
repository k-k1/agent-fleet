// Rotating a member's internal git token from the member detail (issue #1199).
//   - The confirmation says what breaks before anything is sent.
//   - The toast reports how the running workspace took the new token, because "failed" is
//     the one outcome that leaves the administrator something to do (restart it).
//   - A refusal keeps the dialog open: nothing was rotated.
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
  useTenantStore: (sel: (s: { whoami: object }) => unknown) => sel({ whoami: {} }),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));

import { MemberView } from "./tenantMemberDetail.tsx";

const MEMBER = { user_key: "a-x-com", email: "a@x.com", role: "member", max_sessions: 2, status: "active", state: "running" };

let root: Root | null = null;
let host: HTMLDivElement | null = null;

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
  api.mockResolvedValue({ running: true, sessions: [] });
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("member detail: rotate git token", () => {
  it("says what breaks before rotating, then reports the workspace took the new token", async () => {
    apiJSON.mockResolvedValue({ rotated: "a-x-com", tenant: "acme", workspace: "updated" });
    await mount();
    await act(async () => buttonWith("git トークンを再発行")!.click());
    expect(apiJSON).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("fetch も push もできません");
    await act(async () => buttonWith("再発行する")!.click());
    expect(apiJSON).toHaveBeenCalledWith("api/admin/rotate-git-token", "POST", { tenant_slug: "acme", user_key: "a-x-com" });
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("起動中のワークスペースに渡しました"));
    expect(buttonWith("再発行する")).toBeUndefined();
  });

  it("tells the administrator to restart when the running workspace did not take it", async () => {
    apiJSON.mockResolvedValue({ rotated: "a-x-com", tenant: "acme", workspace: "failed" });
    await mount();
    await act(async () => buttonWith("git トークンを再発行")!.click());
    await act(async () => buttonWith("再発行する")!.click());
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("再起動"));
  });

  it("keeps the dialog open when the CP refuses", async () => {
    apiJSON.mockResolvedValue({ error: { code: "audit_unavailable", message: "nothing was done" } });
    await mount();
    await act(async () => buttonWith("git トークンを再発行")!.click());
    await act(async () => buttonWith("再発行する")!.click());
    expect(toast).toHaveBeenCalledWith("nothing was done");
    expect(buttonWith("再発行する")).toBeDefined();
  });
});
