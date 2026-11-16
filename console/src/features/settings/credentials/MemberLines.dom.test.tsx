// The two member lines of ADR 0138 on Settings > Model credentials: D14a's removal line and the
// "hidden by your own settings" line. Both are drawn from counts and booleans only.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));
const toast = vi.fn();
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));
let confirmAnswer = true;
const confirm = vi.fn(() => Promise.resolve(confirmAnswer));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => confirm }));

const { UnusableLine, OwnSettingsLine } = await import("./MemberLines.tsx");
const { getSettings, setSettings, settingsDefaults } = await import("../../../lib/settings.ts");
const { t } = await import("../../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(node);
  });
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}
const text = () => host!.textContent || "";
const button = (label: string) =>
  Array.from(host!.querySelectorAll<HTMLButtonElement>("button")).find((b) => b.textContent?.includes(label));

beforeEach(() => {
  confirmAnswer = true;
  setSettings(settingsDefaults());
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.clearAllMocks();
  setSettings(settingsDefaults());
});

describe("UnusableLine (D14a)", () => {
  const offer = (count: number, token = "tok-1") => api.mockResolvedValue({ count, token });

  it("is absent when nothing stored is forbidden", async () => {
    offer(0, "");
    await mount(<UnusableLine running />);
    expect(text()).toBe("");
  });

  it("is absent while the workspace is stopped, and asks nothing", async () => {
    offer(2);
    await mount(<UnusableLine running={false} />);
    expect(text()).toBe("");
    expect(api).not.toHaveBeenCalled();
  });

  it("says that some cannot be used, without naming any", async () => {
    offer(2);
    await mount(<UnusableLine running />);
    expect(text()).toContain(t("cred.unusable_line"));
    expect(text()).not.toMatch(/claude|codex|anthropic|openai/i);
  });

  it("removes only after the confirmation, sending the token it was shown with", async () => {
    offer(2, "tok-shown");
    apiJSON.mockResolvedValue({ removed: 2, skipped: 0, failed: 0, count: 0 });
    await mount(<UnusableLine running />);
    await act(async () => button(t("cred.unusable_remove"))!.click());
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(apiJSON).toHaveBeenCalledWith("api/connections/unusable/remove", "POST", { token: "tok-shown" });
    expect(toast).toHaveBeenCalledWith(t("cred.unusable_removed", { removed: 2 }));
    expect(text()).toBe("");
  });

  it("removes nothing when the confirmation is declined", async () => {
    confirmAnswer = false;
    offer(1);
    await mount(<UnusableLine running />);
    await act(async () => button(t("cred.unusable_remove"))!.click());
    expect(apiJSON).not.toHaveBeenCalled();
    expect(text()).toContain(t("cred.unusable_line"));
  });

  it("reports what was kept and shows the recomputed line", async () => {
    offer(2, "tok-1");
    apiJSON.mockResolvedValue({ removed: 1, skipped: 1, failed: 0, count: 1, token: "tok-2" });
    await mount(<UnusableLine running />);
    await act(async () => button(t("cred.unusable_remove"))!.click());
    expect(toast).toHaveBeenCalledWith(t("cred.unusable_removed_partly", { removed: 1, left: 1 }));
    expect(text()).toContain(t("cred.unusable_line"));
  });

  it("asks again after a refused removal (an expired confirmation)", async () => {
    offer(1, "tok-old");
    apiJSON.mockResolvedValue({ error: { code: "unusable_offer_expired", message: "expired" } });
    await mount(<UnusableLine running />);
    api.mockClear();
    offer(1, "tok-new");
    await act(async () => button(t("cred.unusable_remove"))!.click());
    expect(toast).toHaveBeenCalledTimes(1);
    expect(api).toHaveBeenCalledWith("api/connections/unusable");
  });
});

describe("OwnSettingsLine", () => {
  const access = (a: Record<string, boolean> | undefined) => api.mockResolvedValue(a ? { kinds: [], access: a } : { kinds: [] });

  it("is absent when the member's own switches hide nothing", async () => {
    access({ noRouteAvailable: false, allOffByOwnSettings: false, someOffByOwnSettings: false });
    await mount(<OwnSettingsLine running />);
    expect(text()).toBe("");
  });

  it("is absent when the Agent sends no access summary", async () => {
    access(undefined);
    await mount(<OwnSettingsLine running />);
    expect(text()).toBe("");
  });

  it("shows the line when some harness is off by the member's own settings", async () => {
    access({ someOffByOwnSettings: true });
    await mount(<OwnSettingsLine running />);
    expect(text()).toContain(t("cred.own_settings_line"));
  });

  it("resets exactly the member layer: every harness switch and llama.cpp's", async () => {
    setSettings({ agentEnabled: { codex: false, kiro: false }, lcppEnabled: false, peerMessaging: true });
    access({ allOffByOwnSettings: true, someOffByOwnSettings: true });
    await mount(<OwnSettingsLine running />);
    await act(async () => button(t("cred.own_settings_reset"))!.click());
    expect(getSettings().agentEnabled).toEqual({});
    expect(getSettings().lcppEnabled).toBe(true);
    expect(getSettings().peerMessaging).toBe(true);
    expect(text()).toBe("");
  });
});
