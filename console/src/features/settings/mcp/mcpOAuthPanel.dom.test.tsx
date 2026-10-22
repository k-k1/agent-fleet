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
// Chronological log of window.open and API calls, to assert what happens inside the click.
const order: string[] = [];

vi.mock("../../../core/api/client.ts", async () => {
  const actual = await vi.importActual<any>("../../../core/api/client.ts");
  const route = async (path: string, method: string, body?: any) => {
    calls.push({ path, method, body });
    order.push(method + " " + path);
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
// The blank tab window.open hands back; the authorization URL is set on it after the start call.
let tab: { closed: boolean; close: ReturnType<typeof vi.fn>; location: { href: string }; opener: unknown };
let openTab: ReturnType<typeof vi.fn>;

beforeEach(() => {
  calls.length = 0;
  replies = {};
  toasts.length = 0;
  confirmAnswer.value = true;
  assign.mockReset();
  tab = { closed: false, close: vi.fn(), location: { href: "" }, opener: {} };
  order.length = 0;
  openTab = vi.fn(() => {
    order.push("open");
    return tab;
  });
  vi.stubGlobal("open", openTab);
  sessionStorage.clear();
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

  it("says what sessions get in each state, and no longer that the relay is missing", async () => {
    const want: Record<string, string> = {
      not_signed_in: "once you sign in",
      connected: "through a relay in your workspace",
      sign_in_again: "do not get this server until you sign in again",
      needs_more_permission: "through a relay in your workspace",
    };
    for (const [state, phrase] of Object.entries(want)) {
      setStatus({ state });
      await mount(row());
      const note = host!.querySelector('[data-testid="mcp-oauth-relay-note"]')!.textContent!;
      expect(note).toContain(phrase);
      expect(note).not.toContain("ships");
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

  it("needs more permission: re-authorize asks for the challenged scope on top of the granted ones", async () => {
    setStatus({ state: "needs_more_permission", stepUpScope: "mcp:write", scopes: ["mcp:read", "offline_access"] });
    replies["POST api/mcp-servers/mcp-1/oauth/start"] = () => ({
      authorize_url: "https://as.example.com/authorize?x=1", issuer: "https://as.example.com", registration: "dcr",
    });
    await mount(row());
    await click(button("Approve the extra permission"));
    const start = calls.find((c) => c.path.endsWith("/oauth/start"))!;
    expect(start.body.scope).toBe("mcp:read offline_access mcp:write");
    expect(tab.location.href).toContain("https://as.example.com/authorize");
  });

  it("plain Sign in again does not carry a scope", async () => {
    setStatus({ state: "needs_more_permission", stepUpScope: "mcp:write", scopes: ["mcp:read"] });
    replies["POST api/mcp-servers/mcp-1/oauth/start"] = () => ({ authorize_url: "https://as.example.com/a", issuer: "https://as.example.com" });
    await mount(row());
    await click(button("Sign in again"));
    expect(calls.find((c) => c.path.endsWith("/oauth/start"))!.body.scope).toBeUndefined();
  });

  it("renders in Japanese", async () => {
    setStatus({ state: "sign_in_again" });
    await mount(row(), "ja");
    expect(text()).toContain("再サインインが必要");
    expect(button("もう一度サインイン")).toBeTruthy();
  });

  it("renders the step-up button in Japanese", async () => {
    setStatus({ state: "needs_more_permission", stepUpScope: "mcp:write" });
    await mount(row(), "ja");
    expect(button("追加の権限を許可する")).toBeTruthy();
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
    expect(tab.location.href).toBe("https://as.example.com/authorize?state=s");
    expect(assign).not.toHaveBeenCalled();
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
    expect(tab.location.href).toBe("https://as.example.com/authorize?x=1");
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
    expect(tab.location.href).toBe("https://as.example.com/a");
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

  it("does not wait for the status read", async () => {
    replies["GET api/mcp-servers/mcp-1/oauth"] = () => new Promise(() => {}); // never answers
    await mount(row({ failedReturn: { ok: false, server: "mcp-1", reason: "failed", registration: "cimd" } }));
    expect(button("Retry with dynamic registration")!.disabled).toBe(false);
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
    expect(tab.location.href).toBe("https://as.example.com/a");
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
  // The old in-place behaviour is the same-tab fallback's: the marker says this tab started it.
  const setLocation = (search: string, sameTab = true) => {
    if (sameTab) sessionStorage.setItem("af.mcp_oauth.same_tab", "1");
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

// --- #2568: new tab, no wait for the status, unfinished sign-in ---------------------------------

// An in-memory BroadcastChannel bus: every instance of the same name hears the others.
class FakeChannel {
  static all: FakeChannel[] = [];
  onmessage: ((e: { data: unknown }) => void) | null = null;
  constructor(public name: string) {
    FakeChannel.all.push(this);
  }
  postMessage(data: unknown) {
    for (const c of FakeChannel.all) if (c !== this && c.name === this.name) c.onmessage?.({ data });
  }
  close() {
    FakeChannel.all = FakeChannel.all.filter((c) => c !== this);
  }
}
const startReply = (url = "https://as.example.com/authorize?state=s") =>
  (replies["POST api/mcp-servers/mcp-1/oauth/start"] = () => ({ authorize_url: url, issuer: "https://as.example.com" }));

describe("new-tab sign-in", () => {
  it("opens the blank tab inside the click, cuts its opener, and points it at the URL afterwards", async () => {
    setStatus({ state: "not_signed_in" });
    startReply();
    await mount(row());
    await click(button("Sign in"));
    expect(openTab).toHaveBeenCalledTimes(1);
    expect(openTab.mock.calls[0][0]).toBe("about:blank"); // not the AS URL: that is not known yet
    expect(tab.opener).toBeNull();
    expect(tab.location.href).toBe("https://as.example.com/authorize?state=s");
    expect(assign).not.toHaveBeenCalled();
    expect(sessionStorage.getItem("af.mcp_oauth.same_tab")).toBeNull();
  });

  it("closes the blank tab when the start fails or the URL is unusable", async () => {
    setStatus({ state: "not_signed_in" });
    replies["POST api/mcp-servers/mcp-1/oauth/start"] = () => ({ error: { code: "oauth_discovery_failed" } });
    await mount(row());
    await click(button("Sign in"));
    expect(tab.close).toHaveBeenCalled();
    expect(tab.location.href).toBe("");
  });

  it("closes the blank tab when a client id is needed", async () => {
    setStatus({ state: "not_signed_in" });
    replies["POST api/mcp-servers/mcp-1/oauth/start"] = () => ({
      error: { code: "client_id_needed", issuer: "https://as.example.com" },
    });
    await mount(row());
    await click(button("Sign in"));
    expect(tab.close).toHaveBeenCalled();
    expect(host!.querySelector('[data-testid="mcp-oauth-client"]')).toBeTruthy();
  });

  it("popup blocked: says so and falls back to this tab, leaving the same-tab marker", async () => {
    openTab.mockReturnValue(null);
    setStatus({ state: "not_signed_in" });
    startReply();
    await mount(row());
    await click(button("Sign in"));
    expect(toasts.join(" ")).toContain("blocked the new tab");
    expect(assign).toHaveBeenCalledWith("https://as.example.com/authorize?state=s");
    expect(sessionStorage.getItem("af.mcp_oauth.same_tab")).toBe("1");
  });

  it("a second click while the start is in flight starts nothing more", async () => {
    setStatus({ state: "not_signed_in" });
    let release!: () => void;
    replies["POST api/mcp-servers/mcp-1/oauth/start"] = () =>
      new Promise((res) => (release = () => res({ authorize_url: "https://as.example.com/a", issuer: "x" })));
    await mount(row());
    const b = button("Sign in")!;
    await act(async () => {
      b.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      b.dispatchEvent(new MouseEvent("click", { bubbles: true })); // same tick: React has not re-rendered
    });
    await act(async () => release());
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(1);
    expect(openTab).toHaveBeenCalledTimes(1);
  });

  it("does not navigate a tab the member already closed", async () => {
    setStatus({ state: "not_signed_in" });
    startReply();
    tab.closed = true;
    await mount(row());
    await click(button("Sign in"));
    expect(tab.location.href).toBe("");
    expect(assign).not.toHaveBeenCalled();
  });

  it("opens the tab in the click itself, while the start call is still unanswered", async () => {
    setStatus({ state: "not_signed_in" });
    let release!: () => void;
    replies["POST api/mcp-servers/mcp-1/oauth/start"] = () =>
      new Promise((res) => (release = () => res({ authorize_url: "https://as.example.com/a", issuer: "x" })));
    await mount(row());
    order.length = 0;
    await act(async () => void button("Sign in")!.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect(order).toEqual(["open", "POST api/mcp-servers/mcp-1/oauth/start"]); // open first, nothing awaited yet
    expect(tab.location.href).toBe("");
    await act(async () => release());
    expect(tab.location.href).toBe("https://as.example.com/a");
    expect(openTab).toHaveBeenCalledTimes(1);
  });

  it("Save and sign in opens its tab in the click, before the save, and never a second one", async () => {
    setStatus({ state: "not_signed_in" });
    let n = 0;
    replies["POST api/mcp-servers/mcp-1/oauth/start"] = () =>
      n++ === 0 ? { error: { code: "client_id_needed", issuer: "https://as.example.com" } } : { authorize_url: "https://as.example.com/b", issuer: "x" };
    let releaseSave!: () => void;
    replies["PUT api/mcp-servers/mcp-1/oauth/client"] = () => new Promise((res) => (releaseSave = () => res({})));
    await mount(row());
    await click(button("Sign in"));
    const input = host!.querySelector('input[aria-label="Client ID"]') as HTMLInputElement;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "cid");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    order.length = 0;
    await act(async () => void button("Save and sign in")!.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect(order).toEqual(["open", "PUT api/mcp-servers/mcp-1/oauth/client"]); // the save is still pending
    await act(async () => releaseSave());
    await act(async () => {});
    expect(order).toEqual(["open", "PUT api/mcp-servers/mcp-1/oauth/client", "POST api/mcp-servers/mcp-1/oauth/start"]);
    expect(tab.location.href).toBe("https://as.example.com/b");
  });
});

describe("cross-tab completion", () => {
  beforeEach(() => {
    FakeChannel.all = [];
    vi.stubGlobal("BroadcastChannel", FakeChannel);
  });

  const announce = (msg: unknown) => new FakeChannel("af-mcp-oauth").postMessage(msg);

  // Presses Sign in so this tab is the one awaiting a completion.
  const waiting = async (registration = "cimd") => {
    setStatus({ state: "not_signed_in" });
    replies["POST api/mcp-servers/mcp-1/oauth/start"] = () => ({
      authorize_url: "https://as.example.com/a", issuer: "https://as.example.com", registration,
    });
    await mount(row());
    await click(button("Sign in"));
    toasts.length = 0;
  };
  const reads = () => calls.filter((c) => c.method === "GET").length;

  it("says 'Signed in' once the status read agrees, for the sign-in started here", async () => {
    await waiting();
    setStatus({ state: "connected", registration: "cimd" });
    await act(async () => announce({ server: "mcp-1", outcome: "ok" }));
    await act(async () => {});
    expect(host!.querySelector('[data-testid="mcp-oauth-panel"]')!.getAttribute("data-state")).toBe("connected");
    expect(toasts).toEqual(["Signed in."]);
  });

  it("ignores a notice when no sign-in was started from this tab", async () => {
    setStatus({ state: "not_signed_in" });
    await mount(row());
    const before = reads();
    await act(async () => announce({ server: "mcp-1", outcome: "ok" }));
    await act(async () => {});
    expect(reads()).toBe(before);
    expect(toasts).toHaveLength(0);
  });

  it("a made-up ok does not claim success, nor stop the wait", async () => {
    await waiting();
    await act(async () => announce({ server: "mcp-1", outcome: "ok" })); // status stays not_signed_in
    await act(async () => {});
    expect(toasts).toHaveLength(0);
    expect(host!.querySelector('[data-testid="mcp-oauth-panel"]')!.getAttribute("data-state")).toBe("not_signed_in");
    const n = reads();
    await act(async () => void window.dispatchEvent(new Event("focus")));
    expect(reads()).toBeGreaterThan(n); // still waiting: the real completion is still found on focus
  });

  it("repeated notices say it once", async () => {
    await waiting();
    setStatus({ state: "connected" });
    for (let i = 0; i < 3; i++) await act(async () => announce({ server: "mcp-1", outcome: "ok" }));
    await act(async () => {});
    expect(toasts).toEqual(["Signed in."]);
  });

  it("ignores another server's completion, and malformed payloads", async () => {
    await waiting();
    const before = reads();
    await act(async () => announce({ server: "mcp-other", outcome: "ok" }));
    await act(async () => announce({ server: "mcp-1", outcome: "<script>" }));
    await act(async () => announce("mcp-1"));
    expect(reads()).toBe(before);
    expect(toasts).toHaveLength(0);
  });

  it("a failed outcome shows our own text for the code", async () => {
    await waiting();
    await act(async () => announce({ server: "mcp-1", outcome: "iss_mismatch" }));
    await act(async () => {});
    expect(toasts[0]).toContain("different authorization server");
  });

  it("a made-up failure while the sign-in is still pending neither says so nor stops the wait", async () => {
    await waiting("cimd");
    setStatus({ state: "not_signed_in", pendingRegistration: "cimd" }); // the real attempt is still running
    await act(async () => announce({ server: "mcp-1", outcome: "iss_mismatch" }));
    await act(async () => {});
    expect(toasts).toHaveLength(0);
    setStatus({ state: "connected", registration: "cimd" });
    await act(async () => void window.dispatchEvent(new Event("focus"))); // the focus re-read still runs
    await act(async () => {});
    expect(host!.querySelector('[data-testid="mcp-oauth-panel"]')!.getAttribute("data-state")).toBe("connected");
  });

  it("the real success notice still lands after a made-up failure", async () => {
    await waiting("cimd");
    setStatus({ state: "not_signed_in", pendingRegistration: "cimd" });
    await act(async () => announce({ server: "mcp-1", outcome: "state_expired" }));
    await act(async () => {});
    setStatus({ state: "connected" });
    await act(async () => announce({ server: "mcp-1", outcome: "ok" }));
    await act(async () => {});
    expect(toasts).toEqual(["Signed in."]);
  });

  it("a failure notice is not confirmed while the status cannot be read", async () => {
    await waiting("cimd");
    replies["GET api/mcp-servers/mcp-1/oauth"] = () => ({ error: { code: "internal" } });
    await act(async () => announce({ server: "mcp-1", outcome: "iss_mismatch" }));
    await act(async () => {});
    expect(toasts).toHaveLength(0);
  });

  it("a CIMD sign-in that failed in the other tab keeps the DCR retry, though nothing is pending any more", async () => {
    await waiting("cimd");
    setStatus({ state: "not_signed_in" }); // the callback consumed the pending record
    await act(async () => announce({ server: "mcp-1", outcome: "token_exchange_failed" }));
    await act(async () => {});
    expect(button("Retry with dynamic registration")).toBeTruthy();
  });

  it("a failure of a DCR sign-in offers no DCR retry", async () => {
    await waiting("dcr");
    await act(async () => announce({ server: "mcp-1", outcome: "token_exchange_failed" }));
    await act(async () => {});
    expect(button("Retry with dynamic registration")).toBeUndefined();
  });

  it("re-reads on focus only while a sign-in started here is waiting", async () => {
    setStatus({ state: "not_signed_in" });
    startReply();
    await mount(row());
    const reads = () => calls.filter((c) => c.method === "GET").length;
    let n = reads();
    await act(async () => void window.dispatchEvent(new Event("focus")));
    expect(reads()).toBe(n); // nothing started here
    await click(button("Sign in"));
    n = reads();
    await act(async () => void window.dispatchEvent(new Event("focus")));
    await act(async () => void document.dispatchEvent(new Event("visibilitychange")));
    expect(reads()).toBeGreaterThan(n);
  });

  it("works through the storage event when there is no BroadcastChannel", async () => {
    vi.stubGlobal("BroadcastChannel", undefined);
    await waiting();
    setStatus({ state: "connected" });
    await act(async () =>
      void window.dispatchEvent(
        new StorageEvent("storage", { key: "af.mcp_oauth.done", newValue: JSON.stringify({ server: "mcp-1", outcome: "ok" }) }),
      ),
    );
    await act(async () => {});
    expect(host!.querySelector('[data-testid="mcp-oauth-panel"]')!.getAttribute("data-state")).toBe("connected");
  });
});

describe("return tab", () => {
  beforeEach(() => {
    FakeChannel.all = [];
    vi.stubGlobal("BroadcastChannel", FakeChannel);
  });
  const at = (search: string) =>
    vi.stubGlobal("location", { ...window.location, assign, search, pathname: "/", hash: "" });

  it("shows the outcome, says the tab can be closed, announces only server id and outcome code", async () => {
    const heard: unknown[] = [];
    const listener = new FakeChannel("af-mcp-oauth");
    listener.onmessage = (e) => heard.push(e.data);
    const open = vi.spyOn(useSettingsUI.getState(), "openSettings").mockImplementation(() => {});
    at("?mcp_oauth=ok&server=mcp-1&registration=cimd&code=SECRET&state=SECRET");
    await mount(<McpOAuthReturn booted />);
    expect(host!.querySelector('[data-testid="mcp-oauth-return"]')!.textContent).toContain("Signed in.");
    expect(text()).toContain("You can close this tab");
    expect(heard).toEqual([{ server: "mcp-1", outcome: "ok" }]);
    expect(JSON.stringify(heard)).not.toContain("SECRET");
    expect(open).not.toHaveBeenCalled(); // no navigation, no Settings takeover
    expect(assign).not.toHaveBeenCalled();
    open.mockRestore();
  });

  it("a failure shows our own words, announces the code, and never the received reason", async () => {
    const heard: unknown[] = [];
    const listener = new FakeChannel("af-mcp-oauth");
    listener.onmessage = (e) => heard.push(e.data);
    at("?mcp_oauth=failed&server=mcp-1&reason=" + encodeURIComponent("https://evil.example.com"));
    await mount(<McpOAuthReturn booted />);
    expect(text()).toContain("Sign-in did not finish");
    expect(text()).not.toContain("evil");
    expect(heard).toEqual([{ server: "mcp-1", outcome: "failed" }]);
  });

  it("announces nothing for a return that names no valid server", async () => {
    const heard: unknown[] = [];
    const listener = new FakeChannel("af-mcp-oauth");
    listener.onmessage = (e) => heard.push(e.data);
    at("?mcp_oauth=failed&server=" + encodeURIComponent("../x"));
    await mount(<McpOAuthReturn booted />);
    expect(heard).toEqual([]);
  });

  it("'Open the Console in this tab' dismisses it and opens Settings on MCP", async () => {
    const open = vi.spyOn(useSettingsUI.getState(), "openSettings").mockImplementation(() => {});
    at("?mcp_oauth=ok&server=mcp-1");
    await mount(<McpOAuthReturn booted />);
    await click(button("Open the Console in this tab"));
    expect(host!.querySelector('[data-testid="mcp-oauth-return"]')).toBeNull();
    expect(open).toHaveBeenCalledWith("mcp");
    open.mockRestore();
  });
});

describe("Sign in with the status read pending or failed", () => {
  it("is enabled while the status read has not answered", async () => {
    replies["GET api/mcp-servers/mcp-1/oauth"] = () => new Promise(() => {});
    await mount(row());
    expect(button("Sign in")!.disabled).toBe(false);
  });

  it("stays usable after a failed read, which offers a Reload that re-reads", async () => {
    replies["GET api/mcp-servers/mcp-1/oauth"] = () => ({ error: { code: "mcp_not_found" } });
    startReply();
    await mount(row());
    expect(text()).toContain("Could not read the sign-in status");
    expect(button("Sign in")!.disabled).toBe(false);
    setStatus({ state: "connected" });
    await click(button("Reload"));
    expect(host!.querySelector('[data-testid="mcp-oauth-status-error"]')).toBeNull();
    expect(host!.querySelector('[data-testid="mcp-oauth-panel"]')!.getAttribute("data-state")).toBe("connected");
  });

  it("a Sign in pressed before the status arrives still starts a sign-in", async () => {
    replies["GET api/mcp-servers/mcp-1/oauth"] = () => new Promise(() => {});
    startReply();
    await mount(row());
    await click(button("Sign in"));
    expect(tab.location.href).toBe("https://as.example.com/authorize?state=s");
  });
});

describe("unfinished sign-in note", () => {
  it("shows for a pending registration: try again, DCR, and the operator's trusted host", async () => {
    setStatus({ state: "not_signed_in", pendingRegistration: "cimd" });
    await mount(row());
    const note = host!.querySelector('[data-testid="mcp-oauth-unfinished"]')!;
    expect(note.textContent).toContain("never finished");
    expect(note.textContent).toContain("Press Sign in to try again");
    expect(note.textContent).toContain("trust this deployment's host");
    expect(note.textContent).toContain("trusted client hosts");
    expect(button("Retry with dynamic registration")).toBeTruthy();
  });

  it("offers no DCR retry for a pending registration that was not CIMD", async () => {
    setStatus({ state: "not_signed_in", pendingRegistration: "dcr" });
    await mount(row());
    expect(host!.querySelector('[data-testid="mcp-oauth-unfinished"]')).toBeTruthy();
    expect(button("Retry with dynamic registration")).toBeUndefined();
  });

  it("is absent with nothing pending, and when connected", async () => {
    setStatus({ state: "not_signed_in" });
    await mount(row());
    expect(host!.querySelector('[data-testid="mcp-oauth-unfinished"]')).toBeNull();
    act(() => root!.unmount());
    host!.remove();
    setStatus({ state: "connected", pendingRegistration: "cimd" });
    await mount(row());
    expect(host!.querySelector('[data-testid="mcp-oauth-unfinished"]')).toBeNull();
  });

  it("reads naturally in Japanese", async () => {
    setStatus({ state: "not_signed_in", pendingRegistration: "cimd" });
    await mount(row(), "ja");
    expect(text()).toContain("最後まで終わっていません");
    expect(text()).toContain("信頼するクライアントのホスト");
  });
});

// Keep the imports used even if a test is trimmed.
void OAuthPanel;
void useOAuthFlow;
