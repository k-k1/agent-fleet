import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { setLocale } from "../../../lib/i18n/index.ts";
import { listTenantMcpServers, createWorkspaceMcpServers, testMcpServer } from "./mcpApi.ts";
import { McpAdminView } from "./mcpAdmin.tsx";
import type { WorkspaceMcpPreset } from "./mcpApi.ts";

vi.mock("./mcpApi.ts", async (importOriginal) => ({
  ...await importOriginal<typeof import("./mcpApi.ts")>(),
  listTenantMcpServers: vi.fn(),
  createWorkspaceMcpServers: vi.fn(),
  testMcpServer: vi.fn(),
  checkEgress: vi.fn().mockResolvedValue(null),
}));

const url = "https://gmailmcp.googleapis.com/mcp/v1self";
const readonly = "https://www.googleapis.com/auth/gmail.readonly";
const compose = "https://www.googleapis.com/auth/gmail.compose";
const gmailService = { id: "gmail", name: "google-gmail", label: "Gmail", host: "gmailmcp.googleapis.com", url, read_only: [readonly], standard: [readonly, compose] };
let root: Root | undefined;
let host: HTMLDivElement;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  setLocale("ja");
  vi.clearAllMocks();
});

const button = (label: string) =>
  [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === label)!;
const click = (el: HTMLElement) => act(async () => el.click());
const type = (el: HTMLInputElement, value: string) => act(async () => {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
});

it("tests the selected public endpoint and distributes it using the chosen bundle and tenant client", async () => {
  setLocale("en");
  vi.mocked(listTenantMcpServers).mockResolvedValue({
    servers: [],
    google_workspace: {
      redirect_uri: "https://af.example.com/oauth2/mcp/callback",
      services: [
        gmailService,
        { id: "people", name: "google-people", label: "People API", host: "people.googleapis.com",
          url: "https://people.googleapis.com/mcp/v1", preview: true, read_only: ["contacts"], standard: ["contacts"] },
      ],
    },
  });
  vi.mocked(testMcpServer).mockResolvedValue({
    ok: true, toolCount: 4, elapsedMs: 20, serverName: "Gmail",
    oauth: { required: true, selected: "https://accounts.google.com" },
  });
  vi.mocked(createWorkspaceMcpServers).mockResolvedValue({ created: 1 });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(<McpAdminView tenants={[{ slug: "sales", name: "Sales" }]} />));
  await click(button("Google Workspace preset"));

  expect(host.textContent).toContain("https://af.example.com/oauth2/mcp/callback");
  expect(host.textContent).toContain("people.googleapis.com");
  expect(host.textContent).toContain("Google Developer Preview");
  expect(button("Distribute").disabled).toBe(true);

  const [gmail] = host.querySelectorAll<HTMLInputElement>('input[type="checkbox"]');
  await click(gmail);
  const bundle = host.querySelector<HTMLSelectElement>('select[aria-label="Scope bundle"]')!;
  expect(bundle.value).toBe("read_only");
  expect(host.textContent).toContain(readonly);
  expect(host.textContent).not.toContain(compose);
  await act(async () => {
    bundle.value = "standard";
    bundle.dispatchEvent(new Event("change", { bubbles: true }));
  });
  expect(host.textContent).toContain(compose);

  const [id, secret] = host.querySelectorAll<HTMLInputElement>("input.cinput");
  expect(secret.type).toBe("password");
  await type(id, "tenant-agent");
  await type(secret, "dummy-secret");
  await click(button("Test connection"));
  expect(testMcpServer).toHaveBeenCalledWith({ name: "google-gmail", transport: "http", url, auth: "oauth" });
  expect(host.textContent).toContain("Connected");
  expect(host.textContent).toContain("OAuth metadata found");

  await click(button("Distribute"));
  expect(createWorkspaceMcpServers).toHaveBeenCalledWith({
    tenant_slug: "sales", client_id: "tenant-agent", client_secret: "dummy-secret",
    services: ["gmail"], bundle: "standard",
  });
  expect(listTenantMcpServers).toHaveBeenCalledTimes(2);
  expect(host.querySelector('input[type="password"]')).toBeNull();
});

it("refreshes overlapping definitions and keeps the form ready to distribute the remaining service", async () => {
  setLocale("en");
  const googleWorkspace: WorkspaceMcpPreset = { services: [gmailService, {
    id: "drive", name: "google-drive", label: "Google Drive", host: "drivemcp.googleapis.com",
    url: "https://drivemcp.googleapis.com/mcp/v1self", read_only: ["drive.readonly"], standard: ["drive.readonly", "drive.file"],
  }] };
  vi.mocked(listTenantMcpServers).mockResolvedValue({ servers: [], google_workspace: googleWorkspace })
    .mockResolvedValueOnce({ servers: [], google_workspace: googleWorkspace })
    .mockResolvedValueOnce({
      google_workspace: googleWorkspace,
      servers: [{ id: "other", name: "Google-GMAIL", url, transport: "http", enabled: true, targets: { session: true, assistant: true } }],
    });
  vi.mocked(createWorkspaceMcpServers).mockResolvedValue({ created: 1 })
    .mockResolvedValueOnce({ error: { status: 409, code: "mcp_name_taken" } });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(<McpAdminView tenants={[{ slug: "sales", name: "Sales" }]} />));
  await click(button("Google Workspace preset"));
  const [gmail, drive] = host.querySelectorAll<HTMLInputElement>('input[type="checkbox"]');
  await click(gmail);
  await click(drive);
  const [id, secret] = host.querySelectorAll<HTMLInputElement>("input.cinput");
  await type(id, "tenant-agent");
  await type(secret, "dummy-secret");
  await click(button("Distribute"));

  expect(listTenantMcpServers).toHaveBeenCalledTimes(2);
  expect(host.querySelector('[role="alert"]')?.textContent).toContain("already registered");
  expect(host.textContent).toContain("Server name already in use");
  expect(gmail.checked).toBe(false);
  expect(gmail.disabled).toBe(true);
  expect(drive.checked).toBe(true);
  expect(id.value).toBe("tenant-agent");
  expect(secret.value).toBe("dummy-secret");
  await click(button("Distribute"));
  expect(createWorkspaceMcpServers).toHaveBeenNthCalledWith(2, {
    tenant_slug: "sales", client_id: "tenant-agent", client_secret: "dummy-secret",
    services: ["drive"], bundle: "read_only",
  });
});
