import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setLocale } from "../../../lib/i18n/index.ts";

const api = vi.fn(), raw = vi.fn(), apiJSON = vi.fn(), confirm = vi.fn(), toast = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({ api: (...args: unknown[]) => api(...args), raw: (...args: unknown[]) => raw(...args), apiJSON: (...args: unknown[]) => apiJSON(...args), errText: (e: { message: string }) => e.message }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => confirm }));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));
import { ConnectedApps } from "./ConnectedApps.tsx";
import { TenantMCPOAuthView } from "../tenant/tenantMCPOAuth.tsx";

let host: HTMLDivElement, root: Root;
// PAT dates use local time, so the fixture must stay on the same local calendar day.
const grant = { id: "grant/1", client_name: "Example App", redirect_host: "chatgpt.com", tenant: "team", scope: "read write", created_at: new Date(2023, 10, 14, 12).getTime() / 1000, last_used_at: 0 };
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.clearAllMocks();
  setLocale("en");
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  confirm.mockResolvedValue(true); raw.mockResolvedValue({ ok: true });
});
afterEach(() => { act(() => root.unmount()); host.remove(); setLocale("ja"); });
const mount = async (node: React.ReactNode) => { await act(async () => { root.render(node); }); };
const click = async (button: HTMLElement) => { await act(async () => button.click()); };

describe("connected apps", () => {
  it("hides on the explicit deployment flag", async () => {
    api.mockResolvedValue({ enabled: false, grants: [] });
    await mount(<ConnectedApps />);
    expect(host.textContent).toBe(""); expect(api).toHaveBeenCalledWith("api/mcp-oauth/grants");
  });
  it("shows display fields and confirms before revoking an encoded grant id", async () => {
    api.mockResolvedValue({ enabled: true, grants: [grant] });
    await mount(<ConnectedApps />);
    for (const text of ["Connected apps", "Example App", "Self-declared name (unverified)", "chatgpt.com", "team", "read write", "2023-11-14", "Never used"]) expect(host.textContent).toContain(text);
    confirm.mockResolvedValueOnce(false);
    await click(host.querySelector("button")!); expect(raw).not.toHaveBeenCalled();
    await click(host.querySelector("button")!);
    expect(confirm.mock.calls[1][0].body).toContain("Example App");
    expect(raw).toHaveBeenCalledWith("api/mcp-oauth/grants/grant%2F1", { method: "DELETE" });
    expect(api).toHaveBeenCalledTimes(2);
  });
  it("keeps the grant visible and reports a failed revoke", async () => {
    api.mockResolvedValue({ enabled: true, grants: [grant] }); raw.mockResolvedValue({ ok: false });
    await mount(<ConnectedApps />); await click(host.querySelector("button")!);
    expect(toast).toHaveBeenCalledWith("Could not revoke the connection. Try again.");
    expect(host.textContent).toContain("Example App");
  });
  it("reloads an already-ended connection after a revoke returns 404", async () => {
    api.mockResolvedValueOnce({ enabled: true, grants: [grant] }).mockResolvedValueOnce({ enabled: true, grants: [] });
    raw.mockResolvedValue({ ok: false, status: 404 });
    await mount(<ConnectedApps />);
    await click(host.querySelector("button")!);
    expect(api).toHaveBeenCalledTimes(2);
    expect(host.textContent).not.toContain("Example App");
    expect(host.textContent).toContain("No connected apps.");
    expect(toast).not.toHaveBeenCalled();
  });
  it("keeps dates consistent with PATs in Japanese even with an en-US browser", async () => {
    setLocale("ja");
    const browserDate = vi.spyOn(Date.prototype, "toLocaleDateString").mockReturnValue("11/14/2023");
    try {
      api.mockResolvedValue({ enabled: true, grants: [{ ...grant, last_used_at: grant.created_at }] });
      await mount(<ConnectedApps />);
      const cells = Array.from(host.querySelectorAll("tbody td")).map((cell) => cell.textContent);
      expect(cells.slice(4, 6)).toEqual(["2023-11-14", "2023-11-14"]);
      expect(browserDate).not.toHaveBeenCalled();
    } finally { browserDate.mockRestore(); }
  });
  it("renders Japanese labels and empty state", async () => {
    setLocale("ja"); api.mockResolvedValue({ enabled: true, grants: [] });
    await mount(<ConnectedApps />); expect(host.textContent).toContain("接続済みアプリはありません。");
  });
});

describe("tenant MCP OAuth", () => {
  it("hides when the deployment does not serve OAuth", async () => {
    api.mockResolvedValue({ enabled: true, served: false });
    await mount(<TenantMCPOAuthView slug="team" />); expect(host.textContent).toBe("");
  });
  it("confirms disabling and saves only to the selected tenant", async () => {
    api.mockResolvedValue({ enabled: true, served: true }); apiJSON.mockResolvedValue({ enabled: false, served: true });
    await mount(<TenantMCPOAuthView slug="team/a" />);
    await click(host.querySelector("input")!);
    confirm.mockResolvedValueOnce(false);
    await click(host.querySelector("button")!); expect(apiJSON).not.toHaveBeenCalled();
    await click(host.querySelector("button")!);
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/team%2Fa/mcp-oauth", "PUT", { enabled: false });
    expect(host.textContent).toContain("Personal Access Tokens are unaffected");
  });
});
