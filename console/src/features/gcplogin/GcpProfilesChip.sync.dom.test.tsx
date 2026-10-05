// The badge reads the Agent's profile list, which follows Settings only on the Agent's own
// pull. While the list is empty the badge is hidden, so nothing on it can ask again: these
// drive the real Settings tab and real focus events and check the badge recovers.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

type Json = Record<string, unknown>;
let settingsRows: Json[] = [];
let agentProfiles: Json[] = [];
let agentRequests: Json[] = [];
const asked: string[] = [];

vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => "",
  api: vi.fn(async (path: string) => {
    asked.push(path);
    if (path === "api/gcp/profiles") return settingsRows;
    if (path === "api/gcp-login/profiles") return { profiles: agentProfiles };
    if (path === "api/gcp-login") return { requests: agentRequests };
    return {};
  }),
  apiJSON: vi.fn(async () => ({})),
  raw: vi.fn(async () => new Response(null, { status: 204 })),
  rawJSON: vi.fn(async () => new Response("{}", { status: 201 })),
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => () => Promise.resolve(true) }));

const { GcpProfilesChip } = await import("./GcpProfilesChip.tsx");
const { GcpTab } = await import("../settings/workspace/GcpTab.tsx");
const { useGcpLoginStore, SETTINGS_SYNC_MS } = await import("./store.ts");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { setLocale, t } = await import("../../lib/i18n/index.ts");

const row: Json = { id: "g1", label: "Dev", loginMethod: "google", project: "my-dev-1", name: "dev" };
const agentDev: Json = { name: "dev", state: "none", label: "Dev", project: "my-dev-1" };

let root: Root | null = null;
let host: HTMLDivElement;

async function tick(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}
async function mount(): Promise<void> {
  await act(async () =>
    root!.render(
      <>
        <GcpProfilesChip />
        <GcpTab />
      </>,
    ),
  );
  await tick(0);
}
const chip = () => host.querySelector(".ws-gcp-btn") as HTMLButtonElement | null;
const button = (text: string, scope: ParentNode = host) =>
  Array.from(scope.querySelectorAll<HTMLButtonElement>("button")).find((b) => b.textContent?.trim().endsWith(text))!;
async function click(el: HTMLElement): Promise<void> {
  await act(async () => el.click());
  await tick(0);
}
async function type(el: HTMLInputElement, value: string): Promise<void> {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

beforeEach(() => {
  setLocale("en");
  vi.useFakeTimers();
  asked.length = 0;
  settingsRows = [];
  agentProfiles = [];
  agentRequests = [];
  useGcpLoginStore.setState({ profiles: null, requests: [], hidden: {}, modal: null, profileModal: null });
  useWorkspaceStore.setState({ state: "running" });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  root = null;
  vi.useRealTimers();
});

describe("Google Cloud badge and Settings", () => {
  it("appears after the first profile is added, once the Agent's pull has it", async () => {
    await mount();
    expect(chip()).toBeNull();
    await click(button(t("gcp.add_profile")));
    await type(host.querySelector<HTMLInputElement>('input[placeholder="prod"]')!, "Dev");
    await type(host.querySelector<HTMLInputElement>('input[placeholder="my-project-123"]')!, "my-dev-1");
    settingsRows = [row];
    await click(host.querySelector<HTMLButtonElement>(".ssm-frm-foot button.primary")!);
    // The Agent has not pulled yet: still nothing to show.
    expect(chip()).toBeNull();
    agentProfiles = [agentDev];
    await tick(SETTINGS_SYNC_MS);
    expect(chip()).not.toBeNull();
    expect(chip()!.textContent).toContain("0/1");
  });

  it("goes away after the last profile is deleted", async () => {
    settingsRows = [row];
    agentProfiles = [agentDev];
    await mount();
    expect(chip()).not.toBeNull();
    settingsRows = [];
    await click(button(t("common.delete"), host.querySelector(".ssm-item")!));
    // The Agent has not pulled yet, so the immediate ask still lists it; its pull drops it.
    expect(chip()).not.toBeNull();
    agentProfiles = [];
    await tick(SETTINGS_SYNC_MS);
    expect(chip()).toBeNull();
  });

  it("asks again when the tab comes back, once per return", async () => {
    agentProfiles = [agentDev];
    await mount();
    expect(chip()!.textContent).toContain("0/1");
    // Logged in from another tab: nothing here was told.
    agentProfiles = [{ ...agentDev, state: "signed_in", account: "dev@example.com" }];
    const before = asked.filter((p) => p === "api/gcp-login/profiles").length;
    await act(async () => {
      window.dispatchEvent(new Event("focus"));
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await tick(0);
    expect(asked.filter((p) => p === "api/gcp-login/profiles").length).toBe(before + 1);
    expect(asked).toContain("api/gcp-login");
    expect(chip()!.textContent).toContain("Dev");
    expect(chip()!.className).toContain("ok");
  });
});
