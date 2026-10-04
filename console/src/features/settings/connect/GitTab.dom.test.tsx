// The GitHub connection card when the tenant's app is a GitHub App (issue #1667). A GitHub
// App token reaches only the repositories the app is installed on, so the card has to say so
// before connecting, and the connection has to say so again when GitHub reports no
// installation — otherwise the member is "connected" and every clone fails.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { t } from "../../../lib/i18n/index.ts";

const apiMock = vi.fn();
const apiJSONMock = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => apiMock(...a),
  apiJSON: (...a: unknown[]) => apiJSONMock(...a),
  raw: vi.fn(async () => ({ ok: true })),
  errText: (e: unknown) => String(e),
}));

const { GitTab } = await import("./GitTab.tsx");
const { useWorkspaceStore } = await import("../../../core/store/workspace.ts");
const { ToastProvider } = await import("../../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../../ui/ConfirmProvider.tsx");

let root: Root | null = null;
let host: HTMLDivElement;

async function render(): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <GitTab />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

let connected = false;

function serve(github: Record<string, unknown>) {
  connected = false;
  apiMock.mockImplementation((p: string) => {
    if (p === "api/git-oauth") return Promise.resolve({ github, bitbucket: { configured: false } });
    if (p === "api/connections/git/github/oauth/start")
      return Promise.resolve({ flow_id: "f", user_code: "ABCD-1234", verification_uri: "https://github.com/login/device", interval: 1, expires_in: 60 });
    return Promise.resolve({ github: connected ? { connected: true, username: "octo" } : { connected: false }, bitbucket: { connected: false } });
  });
}

beforeEach(() => {
  apiMock.mockReset();
  apiJSONMock.mockReset();
  useWorkspaceStore.setState({ state: "running" });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
  vi.useRealTimers();
});

describe("GitTab GitHub App", () => {
  it("points at the install page before connecting when the tenant's app is a GitHub App", async () => {
    serve({ configured: true, app_type: "github_app", install_url: "https://github.com/apps/acme/installations/new" });
    await render();
    expect(host.textContent).toContain(t("git.github_app_install_hint"));
    const link = [...host.querySelectorAll<HTMLAnchorElement>("a")].find(
      (a) => a.textContent === t("git.github_app_install_link"),
    );
    expect(link?.href).toBe("https://github.com/apps/acme/installations/new");
  });

  it("says nothing about installing for an OAuth App", async () => {
    serve({ configured: true });
    await render();
    expect(host.textContent).not.toContain(t("git.github_app_install_hint"));
  });

  it("keeps the warnings and the install link on the connected card after the toasts are gone", async () => {
    vi.useFakeTimers();
    serve({ configured: true, app_type: "github_app", install_url: "https://github.com/apps/acme/installations/new" });
    apiJSONMock.mockImplementation(async () => {
      connected = true; // the token is stored before the poll answers
      return {
        connected: true,
        not_installed: true,
        token_expires: true,
        install_url: "https://github.com/apps/acme/installations/new",
      };
    });
    await render();
    const oauth = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) =>
      (b.textContent || "").includes(t("git.connect_oauth")),
    )!;
    await act(async () => oauth.click());
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1100);
    });
    expect(apiJSONMock).toHaveBeenCalledWith("api/connections/git/github/oauth/poll", "POST", { flow_id: "f" });
    // Long after any toast has faded, the connected card still says what to do and links there.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(host.textContent).toContain("octo");
    expect(host.textContent).toContain(t("git.github_app_not_installed"));
    expect(host.textContent).toContain(t("git.github_token_expires"));
    const link = [...host.querySelectorAll<HTMLAnchorElement>("a")].find(
      (a) => a.textContent === t("git.github_app_install_link"),
    );
    expect(link?.href).toBe("https://github.com/apps/acme/installations/new");
  });
});
