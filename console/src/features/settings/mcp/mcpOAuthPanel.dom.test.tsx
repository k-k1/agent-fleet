// The OAuth row (ADR 0119 P2): the four states, Sign in from the row and from the
// connection-test report, the client-id entry, the DCR retry, Disconnect with a confirm, the
// Authentication selector, and the return from the authorization server.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";

const calls: { path: string; method: string; body?: any }[] = [];
let replies: Record<string, (body: any) => any> = {};
const confirmAnswer = { value: true };
const toasts: string[] = [];

vi.mock("../../../core/api/client.ts", async () => {
  const actual = await vi.importActual<any>("../../../core/api/client.ts");
  const route = async (path: string, method: string, body?: any) => {
    calls.push({ path, method, body });
    const key = method + " " + path;
    const f = replies[key];
    if (!f) throw new Error("unexpected call " + key);
    return f(body);
  };
  return {
    ...actual,
    api: (path: string) => route(path, "GET"),
    apiJSON: (path: string, method: string, body?: unknown) => route(path, method, body),
  };
});
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => async () => confirmAnswer.value }));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => (m: string) => void toasts.push(m) }));

import { OAuthPanel, useOAuthFlow } from "./mcpOAuthPanel.tsx";
import { ServerRow, ProbeView } from "./mcpServerRow.tsx";
import { ServerForm } from "./mcpServerForm.tsx";
import { McpAdminForm } from "./mcpAdmin.tsx";
import { McpOAuthReturn } from "./McpOAuthReturn.tsx";
import { emptyForm, emptyTenantForm, bodyOf } from "./mcpWire.ts";
import type { Form, McpServer, ProbeResult } from "./mcpWire.ts";
import { useSettingsUI } from "../store.ts";
import { setLocale } from "../../../lib/i18n/index.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const assign = vi.fn();

beforeEach(() => {
  calls.length = 0;
  replies = {};
  toasts.length = 0;
  confirmAnswer.value = true;
  assign.mockReset();
  vi.stubGlobal("location", { ...window.location, assign, search: "", pathname: "/", hash: "" });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  vi.unstubAllGlobals();
  setLocale("ja");
});

const server = (over: Partial<McpServer> = {}): McpServer => ({
  id: "mcp-1", name: "hosted", origin: "user", transport: "http", url: "https://m.example.com/mcp",
  auth: "oauth", enabled: true, targets: { assistant: true, session: true }, editable: true, ready: true, ...over,
});

async function mount(node: React.ReactNode, locale: "en" | "ja" = "en") {
  setLocale(locale);
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(node));
  await act(async () => {});
}
const text = () => host!.textContent || "";
const button = (label: string) =>
  Array.from(host!.querySelectorAll("button")).find((b) => (b.textContent || "").trim() === label) as HTMLButtonElement | undefined;
const click = async (b: HTMLElement | undefined | null) => {
  expect(b).toBeTruthy();
  await act(async () => b!.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  await act(async () => {});
};
const setStatus = (status: any) => (replies["GET api/mcp-servers/mcp-1/oauth"] = () => ({ serverId: "mcp-1", ...status }));
const row = (extra: Partial<React.ComponentProps<typeof ServerRow>> = {}) => (
  <ServerRow
    s={server()} egress={null} onProposed={() => {}} onEdit={() => {}} onTest={() => {}}
    onToggle={() => {}} onDelete={() => {}} onEnterSecrets={() => {}} {...extra}
  />
);

describe("row states", () => {
  it("not signed in: offers Sign in", async () => {
    setStatus({ state: "not_signed_in" });
    await mount(row());
    expect(host!.querySelector('[data-testid="mcp-oauth-panel"]')!.getAttribute("data-state")).toBe("not_signed_in");
    expect(text()).toContain("Not signed in");
    expect(button("Sign in")).toBeTruthy();
    expect(button("Disconnect")).toBeUndefined();
  });

  it("connected: expiry, scopes, issuer, registration path, Disconnect", async () => {
    setStatus({
      state: "connected", issuer: "https://as.example.com", registration: "cimd",
      scopes: ["mcp:read", "offline_access"], expiresAt: 1893456000,
    });
    await mount(row());
    expect(text()).toContain("Connected");
    expect(text()).toContain("Access token valid until");
    expect(text()).toContain("mcp:read offline_access");
    expect(text()).toContain("https://as.example.com");
    expect(host!.querySelector('[data-testid="mcp-oauth-registration"]')!.textContent).toContain("client ID metadata document (CIMD)");
    expect(button("Disconnect")).toBeTruthy();
    expect(button("Sign in")).toBeUndefined(); // "Connected" next to "Sign in" reads as not signed in
  });

  it("names a pre-registered and a dynamically registered client", async () => {
    setStatus({ state: "connected", registration: "preregistered" });
    await mount(row());
    expect(text()).toContain("a client ID you entered");
    act(() => root!.unmount());
    host!.remove();
    setStatus({ state: "connected", registration: "dcr" });
    await mount(row());
    expect(text()).toContain("dynamic registration (DCR)");
  });

  it("says sessions do not use the sign-in yet, whatever the state", async () => {
    for (const state of ["not_signed_in", "connected"]) {
      setStatus({ state });
      await mount(row());
      expect(host!.querySelector('[data-testid="mcp-oauth-relay-note"]')!.textContent).toContain("not handed this server yet");
      act(() => root!.unmount());
      host!.remove();
    }
  });

  it("sign in again", async () => {
    setStatus({ state: "sign_in_again" });
    await mount(row());
    expect(text()).toContain("can no longer be renewed");
    expect(button("Sign in again")).toBeTruthy();
    expect(button("Disconnect")).toBeTruthy();
  });

  it("needs more permission, naming the scope", async () => {
    setStatus({ state: "needs_more_permission", stepUpScope: "mcp:write", scopes: ["mcp:read"] });
    await mount(row());
    expect(text()).toContain("More permission needed");
    expect(text()).toContain("mcp:write");
    expect(button("Sign in again")).toBeTruthy();
  });

  it("renders in Japanese", async () => {
    setStatus({ state: "sign_in_again" });
    await mount(row(), "ja");
    expect(text()).toContain("再サインインが必要");
    expect(button("もう一度サインイン")).toBeTruthy();
  });

  it("shows nothing for a server that is not OAuth, and asks the Agent nothing", async () => {
    await mount(row({ s: server({ auth: undefined }) }));
    expect(host!.querySelector('[data-testid="mcp-oauth-panel"]')).toBeNull();
    expect(calls).toHaveLength(0);
  });

  it("a failed status read is said, not hidden as 'not signed in' silently", async () => {
    replies["GET api/mcp-servers/mcp-1/oauth"] = () => ({ error: { code: "mcp_not_found" } });
    await mount(row());
    expect(text()).toContain("Could not read the sign-in status");
  });
});

describe("Sign in", () => {
  it("starts through the control plane and navigates to the authorization URL", async () => {
    setStatus({ state: "not_signed_in" });
    replies["POST api/mcp-servers/mcp-1/oauth/start"] = () => ({
      authorize_url: "https://as.example.com/authorize?state=s", issuer: "https://as.example.com", registration: "cimd",
    });
    await mount(row());
    await click(button("Sign in"));
    expect(calls.find((c) => c.method === "POST")!.body).toEqual({});
    expect(assign).toHaveBeenCalledWith("https://as.example.com/authorize?state=s");
  });

  it("never navigates to a non-http(s) address", async () => {
    setStatus({ state: "not_signed_in" });
    replies["POST api/mcp-servers/mcp-1/oauth/start"] = () => ({ authorize_url: "javascript:alert(1)", issuer: "x" });
    await mount(row());
    await click(button("Sign in"));
    expect(assign).not.toHaveBeenCalled();
    expect(toasts.join(" ")).toContain("Could not start the sign-in");
  });

  it("says why a start was refused", async () => {
    setStatus({ state: "not_signed_in" });
    replies["POST api/mcp-servers/mcp-1/oauth/start"] = () => ({ error: { code: "oauth_discovery_failed", message: "x" } });
    await mount(row());
    await click(button("Sign in"));
    expect(toasts.join(" ")).toContain("OAuth metadata could not be used");
    expect(assign).not.toHaveBeenCalled();
  });

  it("asks for a client id on 409, saves it, then retries", async () => {
    setStatus({ state: "not_signed_in" });
    let starts = 0;
    replies["POST api/mcp-servers/mcp-1/oauth/start"] = () =>
      ++starts === 1
        ? { error: { code: "client_id_needed", message: "m", issuer: "https://as.example.com" } }
        : { authorize_url: "https://as.example.com/authorize?x=1", issuer: "https://as.example.com", registration: "preregistered" };
    replies["PUT api/mcp-servers/mcp-1/oauth/client"] = () => ({});
    await mount(row());
    await click(button("Sign in"));
    expect(host!.querySelector('[data-testid="mcp-oauth-client"]')).not.toBeNull();
    expect(text()).toContain("https://as.example.com");
    const [idIn, secretIn] = Array.from(host!.querySelectorAll('[data-testid="mcp-oauth-client"] input')) as HTMLInputElement[];
    expect(button("Save and sign in")!.disabled).toBe(true); // no id yet
    const set = (el: HTMLInputElement, v: string) => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      setter.call(el, v);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    };
    await act(async () => set(idIn, "my-client"));
    await act(async () => set(secretIn, "s3cret"));
    await click(button("Save and sign in"));
    const put = calls.find((c) => c.method === "PUT")!;
    expect(put.body).toEqual({ issuer: "https://as.example.com", clientId: "my-client", clientSecret: "s3cret" });
    expect(starts).toBe(2);
    expect(assign).toHaveBeenCalledWith("https://as.example.com/authorize?x=1");
  });

  it("omits an empty secret", async () => {
    setStatus({ state: "not_signed_in" });
    replies["POST api/mcp-servers/mcp-1/oauth/start"] = () => ({ error: { code: "client_id_needed", issuer: "https://as.example.com" } });
    replies["PUT api/mcp-servers/mcp-1/oauth/client"] = () => ({});
    await mount(row());
    await click(button("Sign in"));
    const idIn = host!.querySelector('[data-testid="mcp-oauth-client"] input') as HTMLInputElement;
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    await act(async () => {
      setter.call(idIn, "c1");
      idIn.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(button("Save and sign in"));
    expect(calls.find((c) => c.method === "PUT")!.body).toEqual({ issuer: "https://as.example.com", clientId: "c1" });
  });
});

describe("DCR retry", () => {
  it("is offered while a CIMD sign-in is unfinished, and retries with dynamic registration", async () => {
    setStatus({ state: "not_signed_in", pendingRegistration: "cimd" });
    replies["POST api/mcp-servers/mcp-1/oauth/start"] = () => ({
      authorize_url: "https://as.example.com/a", issuer: "https://as.example.com", registration: "dcr",
    });
    await mount(row());
    await click(button("Retry with dynamic registration"));
    expect(calls.find((c) => c.method === "POST")!.body).toEqual({ registration: "dcr" });
    expect(assign).toHaveBeenCalled();
  });

  it("is offered after a failed CIMD return for this server only", async () => {
    setStatus({ state: "not_signed_in" });
    await mount(row({ failedReturn: { ok: false, server: "mcp-1", reason: "failed", registration: "cimd" } }));
    expect(button("Retry with dynamic registration")).toBeTruthy();
    act(() => root!.unmount());
    host!.remove();
    await mount(row({ failedReturn: { ok: false, server: "mcp-other", reason: "failed", registration: "cimd" } }));
    expect(button("Retry with dynamic registration")).toBeUndefined();
  });

  it("is disabled until the status has been read", async () => {
    replies["GET api/mcp-servers/mcp-1/oauth"] = () => new Promise(() => {}); // never answers
    await mount(row({ failedReturn: { ok: false, server: "mcp-1", reason: "failed", registration: "cimd" } }));
    expect(button("Retry with dynamic registration")!.disabled).toBe(true);
  });

  it("is not offered once connected, nor after a DCR failure", async () => {
    setStatus({ state: "connected", pendingRegistration: "cimd" });
    await mount(row());
    expect(button("Retry with dynamic registration")).toBeUndefined();
    act(() => root!.unmount());
    host!.remove();
    setStatus({ state: "not_signed_in" });
    await mount(row({ failedReturn: { ok: false, server: "mcp-1", reason: "failed", registration: "dcr" } }));
    expect(button("Retry with dynamic registration")).toBeUndefined();
  });
});

describe("Disconnect", () => {
  it("confirms, then disconnects and re-reads the status", async () => {
    setStatus({ state: "connected" });
    replies["DELETE api/mcp-servers/mcp-1/oauth"] = () => ({ serverId: "mcp-1", hadGrant: true, revoked: true });
    await mount(row());
    await click(button("Disconnect"));
    expect(calls.filter((c) => c.method === "DELETE")).toHaveLength(1);
    expect(toasts.join(" ")).toContain("Disconnected from hosted");
  });

  it("does nothing when the confirm is declined", async () => {
    setStatus({ state: "connected" });
    confirmAnswer.value = false;
    await mount(row());
    await click(button("Disconnect"));
    expect(calls.filter((c) => c.method === "DELETE")).toHaveLength(0);
  });
});

describe("connection-test report", () => {
  const oauthProbe: ProbeResult = {
    ok: false, toolCount: 0, elapsedMs: 1,
    oauth: { required: true, selected: "https://as.example.com", servers: [{ issuer: "https://as.example.com", valid: true, registration: ["cimd"], s256: true, issParam: false }] },
  };

  it("has a Sign in button on an OAuth row, which starts the same flow", async () => {
    setStatus({ state: "not_signed_in" });
    replies["POST api/mcp-servers/mcp-1/oauth/start"] = () => ({ authorize_url: "https://as.example.com/a", issuer: "i" });
    await mount(row({ probe: oauthProbe }));
    const inReport = host!.querySelector(".mcp-probe.info")!;
    const b = Array.from(inReport.querySelectorAll("button")).find((x) => x.textContent === "Sign in")!;
    await click(b);
    expect(assign).toHaveBeenCalledWith("https://as.example.com/a");
  });

  it("explains how to get there when the server is not saved as OAuth", async () => {
    await mount(<ProbeView probe={oauthProbe} />);
    expect(host!.querySelector(".mcp-probe.info button")).toBeNull();
    expect(text()).toContain("Choose OAuth under Authentication");
  });
});

describe("Authentication selector", () => {
  const mountForm = async (f: Form) => {
    function Host() {
      const [form, setForm] = useState<Form | null>(f);
      (globalThis as any).__form = form;
      return form ? (
        <ServerForm form={form} setForm={setForm} onSave={async () => true} onTest={async () => {}} egress={null} onProposed={() => {}} submitLabel="save" />
      ) : null;
    }
    await mount(<Host />);
  };

  it("offers OAuth to a member, with no token or header fields", async () => {
    await mountForm({ ...emptyForm(), name: "hosted", url: "https://m.example.com/mcp" });
    await click(button("OAuth"));
    expect(host!.querySelector('[data-testid="mcp-auth-oauth-note"]')).not.toBeNull();
    expect(host!.querySelector('input[type="password"]')).toBeNull();
    const form = (globalThis as any).__form as Form;
    expect(form.auth).toBe("oauth");
    expect(bodyOf(form).auth).toBe("oauth");
    expect(bodyOf(form).headers).toEqual({});
  });

  it("does not offer OAuth in the tenant distribution form", async () => {
    await mount(
      <McpAdminForm form={{ ...emptyTenantForm(), name: "t", url: "https://m.example.com/mcp" }} setForm={() => {}} busy={false} onSave={async () => {}} egress={null} onProposed={() => {}} />,
    );
    expect(button("Bearer token")).toBeTruthy();
    expect(button("OAuth")).toBeUndefined();
  });
});

describe("return from the authorization server", () => {
  const setLocation = (search: string) => {
    vi.stubGlobal("location", { ...window.location, assign, search, pathname: "/", hash: "" });
  };

  it("on success: says so, opens Settings on MCP, strips the query", async () => {
    setLocation("?mcp_oauth=ok&server=mcp-1&registration=cimd&keep=1");
    const replace = vi.spyOn(window.history, "replaceState");
    const open = vi.spyOn(useSettingsUI.getState(), "openSettings").mockImplementation(() => {});
    await mount(<McpOAuthReturn booted />);
    expect(toasts).toEqual(["Signed in."]);
    expect(replace.mock.calls[0][2]).toBe("/?keep=1");
    expect(open).toHaveBeenCalledWith("mcp");
    replace.mockRestore();
    open.mockRestore();
  });

  it("on failure: shows our own text for the reason, never the received one", async () => {
    setLocation("?mcp_oauth=failed&server=mcp-1&reason=" + encodeURIComponent("<b>EVIL</b> https://evil.example.com"));
    const open = vi.spyOn(useSettingsUI.getState(), "openSettings").mockImplementation(() => {});
    await mount(<McpOAuthReturn booted />);
    expect(toasts).toHaveLength(1);
    expect(toasts[0]).toContain("Sign-in did not finish");
    expect(toasts[0]).not.toContain("EVIL");
    open.mockRestore();
  });

  it("maps a known reason to its words", async () => {
    setLocation("?mcp_oauth=failed&reason=iss_mismatch");
    const open = vi.spyOn(useSettingsUI.getState(), "openSettings").mockImplementation(() => {});
    await mount(<McpOAuthReturn booted />);
    expect(toasts[0]).toContain("different authorization server");
    open.mockRestore();
  });

  it("does not announce a success that names no server", async () => {
    setLocation("?mcp_oauth=ok");
    const open = vi.spyOn(useSettingsUI.getState(), "openSettings").mockImplementation(() => {});
    await mount(<McpOAuthReturn booted />);
    expect(toasts).toHaveLength(0);
    expect(open).not.toHaveBeenCalled();
    open.mockRestore();
  });

  it("does nothing before boot, or on an ordinary URL", async () => {
    setLocation("?mcp_oauth=ok&server=mcp-1");
    await mount(<McpOAuthReturn booted={false} />);
    expect(toasts).toHaveLength(0);
    act(() => root!.unmount());
    host!.remove();
    setLocation("?x=1");
    await mount(<McpOAuthReturn booted />);
    expect(toasts).toHaveLength(0);
  });
});

// Keep the imports used even if a test is trimmed.
void OAuthPanel;
void useOAuthFlow;
