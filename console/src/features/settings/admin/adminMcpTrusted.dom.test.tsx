// The trusted MCP client hosts editor: what it sends, and that it reports which source is in
// effect (saved / environment / default) as the server says it, with reset only when something
// is saved.
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

import { McpTrustedHostsView } from "./adminMcpTrusted.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const view = (source: string, hosts: string[], extra = {}) => ({
  hosts,
  source,
  fallback: ["claude.ai", "claude.com", "chatgpt.com"],
  fallback_source: "default",
  served: true,
  ...extra,
});

async function flush() {
  await act(async () => {
    await Promise.resolve();
  });
}
async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<McpTrustedHostsView />);
  });
  await flush();
}
const button = (label: string) =>
  Array.from(host!.querySelectorAll("button")).find((b) => b.textContent === label) as
    | HTMLButtonElement
    | undefined;
const click = async (el: HTMLElement | undefined) => {
  expect(el).toBeTruthy();
  await act(async () => {
    el!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
  await flush();
};
const area = () => host!.querySelector("textarea") as HTMLTextAreaElement;
async function type(value: string) {
  await act(async () => {
    const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    set.call(area(), value);
    area().dispatchEvent(new Event("input", { bubbles: true }));
  });
}

beforeEach(() => {
  api.mockReset();
  apiJSON.mockReset();
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("McpTrustedHostsView", () => {
  it("shows the list in effect and its source; reset is off while nothing is saved", async () => {
    api.mockResolvedValue(view("default", ["chatgpt.com", "claude.ai", "claude.com"]));
    await mount();
    expect(api).toHaveBeenCalledWith("api/admin/mcp-oauth/trusted-hosts");
    expect(area().value).toBe("chatgpt.com\nclaude.ai\nclaude.com");
    expect(host!.textContent).toContain("組み込みの既定値");
    expect(button("既定に戻す")!.disabled).toBe(true);
  });

  it("saves the edited list, a removed default vendor included, and sends commas and blanks cleanly", async () => {
    api.mockResolvedValue(view("default", ["chatgpt.com", "claude.ai", "claude.com"]));
    apiJSON.mockResolvedValue(view("admin", ["claude.ai", "af.peer.example"]));
    await mount();
    await type("claude.ai, af.peer.example\n\n");
    await click(button("保存"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/mcp-oauth/trusted-hosts", "PUT", {
      hosts: ["claude.ai", "af.peer.example"],
    });
    expect(host!.textContent).toContain("ここで保存した一覧");
    expect(button("既定に戻す")!.disabled).toBe(false);
  });

  it("saves an empty list as none and says only loopback clients remain", async () => {
    api.mockResolvedValue(view("default", ["claude.ai"]));
    apiJSON.mockResolvedValue(view("admin", []));
    await mount();
    await type("");
    await click(button("保存"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/mcp-oauth/trusted-hosts", "PUT", { hosts: [] });
    expect(host!.textContent).toContain("ループバックのクライアントだけ");
  });

  it("resets with DELETE and shows what the server says applies afterwards", async () => {
    api.mockResolvedValue(view("admin", ["claude.ai"], { fallback: ["mcp.corp.example"], fallback_source: "env" }));
    apiJSON.mockResolvedValue(view("env", ["mcp.corp.example"], { fallback: ["mcp.corp.example"], fallback_source: "env" }));
    await mount();
    expect(host!.textContent).toContain("mcp.corp.example");
    expect(host!.textContent).toContain("環境変数");
    await click(button("既定に戻す"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/mcp-oauth/trusted-hosts", "DELETE", undefined);
    expect(host!.textContent).toContain("AF_MCP_OAUTH_TRUSTED_HOSTS");
    expect(area().value).toBe("mcp.corp.example");
  });

  it("shows the server's refusal and keeps what was typed", async () => {
    api.mockResolvedValue(view("default", ["claude.ai"]));
    apiJSON.mockResolvedValue({ error: { message: "wildcards are not supported" } });
    await mount();
    await type("*.example.com");
    await click(button("保存"));
    expect(host!.textContent).toContain("wildcards are not supported");
    expect(area().value).toBe("*.example.com");
  });

  it("warns when the deployment does not serve MCP OAuth", async () => {
    api.mockResolvedValue(view("default", ["claude.ai"], { served: false }));
    await mount();
    expect(host!.textContent).toContain("まだ MCP OAuth を提供していません");
  });
});
