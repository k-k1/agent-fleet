// The Jira connection card (docs/log/80 §80.17). The point is two things:
//   1. The OAuth button is shown or withheld on whether the tenant registered an app — never
//      pressed only to return not_configured, since the person pressing cannot fix that setting.
//   2. The API-token path stays, because a tenant with no registered app has no other way in.
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

const { TrackerTab } = await import("./TrackerTab.tsx");
const { useWorkspaceStore } = await import("../../../core/store/workspace.ts");
const { ToastProvider } = await import("../../../ui/ToastProvider.tsx");
// A connected card renders the disconnect button, which requires useConfirm.
const { ConfirmProvider } = await import("../../../ui/ConfirmProvider.tsx");

let root: Root | null = null;
let host: HTMLDivElement;

const conns = (jira: unknown) => ({ jira });

async function render(): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <TrackerTab />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

const text = () => host.textContent || "";
const buttons = () => [...host.querySelectorAll("button")].map((b) => b.textContent || "");

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
});

describe("TrackerTab", () => {
  it("disables the OAuth button and states why when no app is registered", async () => {
    apiMock.mockImplementation((p: string) => {
      if (p === "api/git-oauth") return Promise.resolve({ jira: { configured: false } });
      return Promise.resolve(conns({ connected: false }));
    });
    await render();
    const btn = [...host.querySelectorAll("button")].find((b) => b.textContent === t("tracker.jira_connect_oauth"));
    expect(btn).toBeTruthy();
    expect(btn!.disabled).toBe(true);
    expect(text()).toContain(t("tracker.jira_oauth_unconfigured"));
    // The token path remains — the only way in for a tenant with no registered app.
    expect(buttons()).toContain(t("tracker.jira_use_token"));
  });

  it("enables the OAuth button when an app is registered", async () => {
    apiMock.mockImplementation((p: string) => {
      if (p === "api/git-oauth") return Promise.resolve({ jira: { configured: true } });
      return Promise.resolve(conns({ connected: false }));
    });
    await render();
    const btn = [...host.querySelectorAll("button")].find((b) => b.textContent === t("tracker.jira_connect_oauth"));
    expect(btn!.disabled).toBe(false);
    expect(text()).not.toContain(t("tracker.jira_oauth_unconfigured"));
  });

  it("shows three fields when the API-token path is chosen (the email is a credential too)", async () => {
    apiMock.mockImplementation((p: string) =>
      Promise.resolve(p === "api/git-oauth" ? { jira: { configured: true } } : conns({ connected: false })),
    );
    await render();
    await act(async () => {
      [...host.querySelectorAll("button")].find((b) => b.textContent === t("tracker.jira_use_token"))!.click();
    });
    expect(host.querySelectorAll("input.cinput").length).toBe(3);
  });

  it("offers a site picker when connected with several sites", async () => {
    const jira = {
      connected: true,
      authKind: "oauth",
      account: "山田 太郎",
      site: "https://one.atlassian.net",
      cloudId: "cid-1",
      sites: [
        { cloudId: "cid-1", url: "https://one.atlassian.net", name: "One" },
        { cloudId: "cid-2", url: "https://two.atlassian.net", name: "Two" },
      ],
    };
    apiMock.mockImplementation((p: string) =>
      Promise.resolve(p === "api/git-oauth" ? { jira: { configured: true } } : conns(jira)),
    );
    await render();
    expect(text()).toContain("山田 太郎");
    expect(text()).toContain(t("tracker.jira_via_oauth"));
    const sel = host.querySelector("select");
    expect(sel).toBeTruthy();
    expect(sel!.options.length).toBe(2);

    apiJSONMock.mockResolvedValue({ connected: true });
    await act(async () => {
      sel!.value = "cid-2";
      sel!.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(apiJSONMock).toHaveBeenCalledWith("api/connections/jira/site", "PUT", { cloudId: "cid-2" });
  });

  it("shows no picker when there is only one site", async () => {
    apiMock.mockImplementation((p: string) =>
      Promise.resolve(
        p === "api/git-oauth"
          ? { jira: { configured: true } }
          : conns({ connected: true, authKind: "oauth", account: "x", site: "https://one.atlassian.net", cloudId: "cid-1", sites: [{ cloudId: "cid-1", url: "https://one.atlassian.net" }] }),
      ),
    );
    await render();
    expect(host.querySelector("select")).toBeNull();
  });

  it("asks to start the workspace first when it is stopped (credentials live in the container)", async () => {
    useWorkspaceStore.setState({ state: "stopped" });
    apiMock.mockResolvedValue({ jira: { configured: true } });
    await render();
    expect(text()).toContain(t("ops.ws_required_title"));
  });
});
