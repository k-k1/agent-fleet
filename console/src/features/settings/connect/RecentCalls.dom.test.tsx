import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setLocale } from "../../../lib/i18n/index.ts";

const api = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({ api: (...args: unknown[]) => api(...args), errText: (e: { message: string }) => e.message }));
import { RecentCalls } from "./RecentCalls.tsx";

let host: HTMLDivElement, root: Root;
const call = {
  at: "2026-10-22T10:00:00Z", http_method: "POST", status: 400, latency_ms: 3, header_version: "2026-07-28",
  mcp_method_header: false, mcp_name_header: false, accept: "application/json", user_agent: "probe-agent/1",
  items: [{ method: "tools/list", era: "stateless", meta_version: "2026-07-28", client_name: "probe", client_version: "0.1", status: 400, error_code: -32020, error_message: "header mismatch: Mcp-Method (absent)" }],
};
const listed = { at: "2026-10-22T10:01:00Z", http_method: "POST", status: 200, latency_ms: 5, mcp_method_header: true, mcp_name_header: false, items: [{ method: "tools/list", era: "legacy", status: 200, tools: 12 }] };
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.clearAllMocks();
  setLocale("en");
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); setLocale("ja"); });
const click = async (b: HTMLElement) => { await act(async () => b.click()); };

describe("recent calls", () => {
  it("loads only when opened and shows what the client sent and got", async () => {
    api.mockResolvedValue({ calls: [call, listed] });
    await act(async () => { root.render(<RecentCalls url="api/pat/p%2F1/calls" />); });
    expect(api).not.toHaveBeenCalled();
    await click(host.querySelector("button")!);
    expect(api).toHaveBeenCalledWith("api/pat/p%2F1/calls");
    for (const t of ["header mismatch", "-32020", "stateless 2026-07-28", "probe/0.1", "Mcp-Method: —", "12 tools", "Control Plane replicas"]) {
      expect(host.textContent).toContain(t);
    }
    expect(host.querySelector("tr.calls-bad")).not.toBeNull();
  });
  it("says so when nothing was recorded, and reports a failed load", async () => {
    api.mockResolvedValueOnce({ calls: [] });
    await act(async () => { root.render(<RecentCalls url="u" />); });
    await click(host.querySelector("button")!);
    expect(host.textContent).toContain("No requests recorded yet.");
    api.mockRejectedValueOnce(new Error("x"));
    await click(host.querySelectorAll("button")[1] as HTMLElement);
    expect(host.textContent).toContain("Could not load recent calls.");
  });
  it("renders in Japanese", async () => {
    setLocale("ja");
    api.mockResolvedValue({ calls: [] });
    await act(async () => { root.render(<RecentCalls url="u" />); });
    expect(host.textContent).toContain("最近の呼び出し");
    await click(host.querySelector("button")!);
    expect(host.textContent).toContain("レプリカ");
  });
});
