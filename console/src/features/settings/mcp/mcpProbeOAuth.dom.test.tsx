// A connection test that reaches an OAuth-protected server is shown as "sign-in required" with
// what the server offers, not as a red failure (ADR 0119 decision 8, phase 1: no button yet).
import { afterEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ProbeView } from "./mcpServerRow.tsx";
import { isOAuthRequired } from "./mcpWire.ts";
import type { ProbeResult } from "./mcpWire.ts";
import { setLocale } from "../../../lib/i18n/index.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  setLocale("ja");
});

function render(probe: ProbeResult): HTMLElement {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root!.render(<ProbeView probe={probe} />));
  return host;
}

const oauth: ProbeResult = {
  ok: false,
  toolCount: 0,
  elapsedMs: 5,
  error: "OAuth ...",
  oauth: {
    required: true,
    challengedScope: "mcp:read",
    scopesSupported: ["mcp:read", "mcp:write"],
    selected: "https://as.example",
    servers: [
      { issuer: "https://as.example", valid: true, registration: ["cimd", "dcr"], s256: true, issParam: true },
    ],
  },
};

describe("OAuth connection-test result", () => {
  it("is recognised only when the agent says the server is OAuth-protected", () => {
    expect(isOAuthRequired(oauth)).toBe(true);
    expect(isOAuthRequired({ ...oauth, oauth: undefined })).toBe(false);
    expect(isOAuthRequired({ ...oauth, ok: true })).toBe(false);
  });

  it("renders as sign-in required, not as a failure", () => {
    setLocale("en");
    const el = render(oauth);
    expect(el.querySelector(".mcp-probe.bad")).toBeNull();
    expect(el.querySelector(".mcp-probe.info")).not.toBeNull();
    const text = el.textContent || "";
    expect(text).toContain("OAuth sign-in required");
    expect(text).toContain("https://as.example");
    expect(text).toContain("client ID metadata document (CIMD)");
    expect(text).toContain("dynamic registration (DCR)");
    expect(text).toContain("PKCE S256: supported");
    expect(text).toContain("mcp:read mcp:write");
    expect(text).not.toContain("OAuth ..."); // the agent's raw error text is not shown
    expect(el.querySelector("button")).toBeNull(); // no sign-in button yet
  });

  it("words each problem code, and names the issuer it concerns", () => {
    setLocale("en");
    const el = render({
      ...oauth,
      oauth: {
        required: true,
        selected: "",
        servers: [{ issuer: "https://bad.example", valid: false, registration: [], s256: false, issParam: false }],
        problems: [
          { code: "as_no_s256", issuer: "https://bad.example" },
          { code: "prm_resource_mismatch" },
          { code: "something_new" },
        ],
      },
    });
    const text = el.textContent || "";
    expect(text).toContain("https://bad.example does not offer PKCE S256");
    expect(text).toContain("different resource than the registered URL");
    expect(text).toContain("Unrecognised problem: something_new");
    expect(text).toContain("neither offered");
    expect(text).toContain("PKCE S256: not offered");
  });

  it("renders in Japanese too", () => {
    setLocale("ja");
    expect(render(oauth).textContent).toContain("OAuth のサインインが必要です");
  });

  it("leaves an ordinary failure red", () => {
    const el = render({ ok: false, toolCount: 0, elapsedMs: 1, error: "HTTP 401" });
    expect(el.querySelector(".mcp-probe.bad")).not.toBeNull();
  });
});
