// The administrator's half of the home operations in the member detail: Clean home (the
// offboarding step) and deleting the backup copies Clean home leaves.
//   - Clean home is offered only where the runtime can reach the home (whoami.home_erase).
//   - A refusal must reach the administrator. Closing the dialog as if it had worked is how
//     an offboarding gets recorded as done while the home is still there.
//   - The backups are shown, and deletable, only where the runtime keeps them
//     (whoami.home_backups) and only when there are some.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
const toast = vi.fn();
let whoami: { home_erase?: boolean; home_backups?: boolean } | null = null;

vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  rawJSON: () => Promise.resolve(new Response("")),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));
vi.mock("../../../core/store/tenant.ts", () => ({
  useTenantStore: (sel: (s: { whoami: typeof whoami }) => unknown) => sel({ whoami }),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));

import { MemberView } from "./tenantMemberDetail.tsx";

const MEMBER = { user_key: "a-x-com", email: "a@x.com", role: "member", max_sessions: 2, status: "removed", state: "stopped" };
const BACKUPS = "api/admin/tenants/acme/members/a-x-com/home-backups";

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
const inDangerZone = (text: string) =>
  Array.from(document.querySelectorAll(".danger-zone button")).some((b) => (b.textContent || "").trim() === text);

let backupCount = 0;
beforeEach(() => {
  apiJSON.mockReset();
  toast.mockReset();
  api.mockReset();
  backupCount = 2;
  api.mockImplementation((p: string) =>
    p === BACKUPS
      ? Promise.resolve({ count: backupCount, newest: "2026-09-29T04:00:00Z" })
      : Promise.resolve({ running: false, sessions: [] }),
  );
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("member detail: Clean home", () => {
  it("is not offered where the runtime cannot reach the home, and says why", async () => {
    whoami = { home_erase: false };
    await mount();
    expect(inDangerZone("home を掃除")).toBe(false);
    expect(document.querySelector(".danger-zone")!.textContent).toContain("この配備では home を掃除できません");
  });

  it("keeps the dialog open and says so when the CP refuses", async () => {
    whoami = { home_erase: true };
    apiJSON.mockResolvedValue({ error: { code: "home_wipe_unsupported", message: "clean home is not available" } });
    await mount();
    await act(async () => buttonWith("home を掃除")!.click());
    await act(async () => buttonWith("掃除する")!.click());
    expect(apiJSON).toHaveBeenCalledWith("api/admin/clean-home", "POST", { tenant_slug: "acme", user_key: "a-x-com" });
    expect(toast).toHaveBeenCalledWith("clean home is not available");
    expect(buttonWith("掃除する")).toBeDefined();
  });

  it("closes the dialog once the home was cleaned", async () => {
    whoami = { home_erase: true };
    apiJSON.mockResolvedValue({ cleaned: "a-x-com", tenant: "acme" });
    await mount();
    await act(async () => buttonWith("home を掃除")!.click());
    await act(async () => buttonWith("掃除する")!.click());
    expect(toast).not.toHaveBeenCalled();
    expect(buttonWith("掃除する")).toBeUndefined();
  });
});

describe("member detail: backups", () => {
  it("never asks for backups on a runtime that keeps none", async () => {
    whoami = { home_erase: true, home_backups: false };
    await mount();
    expect(api.mock.calls.some((c) => c[0] === BACKUPS)).toBe(false);
    expect(document.body.textContent).not.toContain("バックアップを削除");
  });

  it("shows how many copies Clean home leaves, and deletes them on request", async () => {
    whoami = { home_erase: true, home_backups: true };
    await mount();
    await act(async () => buttonWith("home を掃除")!.click());
    expect(document.body.textContent).toContain("この home のバックアップ 2 件は残ります");
    await act(async () => buttonWith("キャンセル")!.click());

    expect(inDangerZone("バックアップを削除（2 件）")).toBe(true);
    apiJSON.mockResolvedValue({ deleted: 2, tenant: "acme" });
    await act(async () => buttonWith("バックアップを削除（2 件）")!.click());
    backupCount = 0;
    await act(async () => buttonWith("削除する")!.click());
    expect(apiJSON).toHaveBeenCalledWith(BACKUPS, "DELETE", {});
    expect(toast).toHaveBeenCalledWith("バックアップを 2 件削除しました");
    await act(async () => {
      await Promise.resolve();
    });
    expect(document.body.textContent).not.toContain("バックアップを削除");
  });

  it("offers no deletion when there is nothing to delete", async () => {
    whoami = { home_erase: true, home_backups: true };
    backupCount = 0;
    await mount();
    expect(document.body.textContent).not.toContain("バックアップを削除");
  });
});
