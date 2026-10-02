// #1029 on the Console side: a warning before a cached SSO login ends shows what the Agent
// lists as expiring, once per profile and end, opens the profile's login modal without
// starting anything, and goes once a re-login moves the end.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

type Json = Record<string, unknown>;
const calls: { path: string; method: string }[] = [];
let profiles: Json[] = [];
let attemptReply: Json = { phase: "starting" };
let profilesFail: "" | "throw" | "error" = "";

vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(async (path: string) => {
    calls.push({ path, method: "GET" });
    if (path === "api/aws-login") return { requests: [] };
    if (path === "api/aws-login/profiles") {
      if (profilesFail === "throw") throw new Error("offline");
      if (profilesFail === "error") return { error: { code: "workspace_stopped" } };
      return { profiles };
    }
    if (path.includes("/attempts/")) return attemptReply;
    return {};
  }),
  apiJSON: vi.fn(async (path: string, method: string) => {
    calls.push({ path, method });
    return { attempt: "att1" };
  }),
}));

const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { AwsLoginHost } = await import("./AwsLoginHost.tsx");
const { useAwsLoginStore } = await import("./store.ts");
const { openNotificationTarget } = await import("../notifications/store.ts");

const expiring: Json = {
  name: "prod",
  state: "signed_in",
  label: "Production",
  accountId: "123456789012",
  roleName: "Dev",
  expiresAt: "2026-10-02T03:10:00Z",
  expiring: true,
};

let root: Root | null = null;
let host: HTMLDivElement;

async function mount(): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <AwsLoginHost />
      </ToastProvider>,
    );
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
}

async function tick(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

const profileCalls = () => calls.filter((c) => c.path === "api/aws-login/profiles").length;
function footButton(label: string): HTMLButtonElement {
  const b = Array.from(document.querySelectorAll(".ui-modal-foot button")).find((x) => x.textContent?.trim() === label);
  if (!b) throw new Error(`no "${label}" in the modal footer`);
  return b as HTMLButtonElement;
}
const toasts = () => Array.from(document.querySelectorAll(".ui-toast"));

beforeEach(() => {
  vi.useFakeTimers();
  calls.length = 0;
  profiles = [expiring];
  attemptReply = { phase: "starting" };
  profilesFail = "";
  useAwsLoginStore.setState({ requests: [], hidden: {}, modal: null, expiring: [], hiddenExpiry: {}, profileModal: null });
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

describe("SSO expiry warning", () => {
  it("shows Settings' account and role in one toast, and opens the login modal without starting it", async () => {
    await mount();
    expect(toasts()).toHaveLength(1);
    const t = toasts()[0];
    expect(t.textContent).toContain("Your AWS login ends at");
    expect(t.textContent).toContain("Production");
    expect(t.textContent).toContain("123456789012");
    await act(async () => (t.querySelector(".update-toast-btn") as HTMLButtonElement).click());
    expect(document.body.textContent).toContain("AWS login (Production)");
    await tick(5000);
    expect(calls.some((c) => c.path.includes("/start"))).toBe(false);
  });

  it("shows nothing and polls nothing when no profile is expiring", async () => {
    profiles = [{ ...expiring, expiring: false }, { name: "stg", state: "none" }];
    await mount();
    const before = profileCalls();
    await tick(10 * 60_000);
    expect(toasts()).toHaveLength(0);
    expect(profileCalls()).toBe(before);
  });

  it("withdraws the toast once a re-login moves the end", async () => {
    await mount();
    expect(toasts()).toHaveLength(1);
    profiles = [{ ...expiring, expiresAt: "2026-10-02T11:00:00Z", expiring: false }];
    await tick(60_100);
    expect(toasts()).toHaveLength(0);
  });

  it("keeps a closed warning closed for that end, and warns again for a new one", async () => {
    await mount();
    await act(async () => (document.querySelector(".ui-toast-x") as HTMLButtonElement).click());
    const before = profileCalls();
    await tick(5 * 60_000);
    expect(toasts()).toHaveLength(0);
    expect(profileCalls()).toBe(before);
    profiles = [{ ...expiring, expiresAt: "2026-10-02T11:00:00Z" }];
    await act(async () => void useAwsLoginStore.getState().refreshExpiry());
    await tick(0);
    expect(toasts()).toHaveLength(1);
  });

  it("keeps the login in progress when the old end passes while the code waits", async () => {
    await mount();
    await act(async () => (toasts()[0].querySelector(".update-toast-btn") as HTMLButtonElement).click());
    attemptReply = { phase: "authorize", url: "https://device.sso.ap-northeast-1.amazonaws.com/?user_code=ABCD-EFGH", code: "ABCD-EFGH" };
    await act(async () => footButton("Log in").click());
    await tick(400);
    expect(document.body.textContent).toContain("ABCD-EFGH");
    profiles = [{ ...expiring, state: "none", expiring: false }];
    await tick(60_100);
    expect(toasts()).toHaveLength(0);
    expect(document.body.textContent).toContain("ABCD-EFGH");
  });

  it("does not reopen the modal by itself when the next end is near", async () => {
    await mount();
    await act(async () => (toasts()[0].querySelector(".update-toast-btn") as HTMLButtonElement).click());
    expect(document.body.textContent).toContain("AWS login (Production)");
    // Logged in: the warning goes, the open modal stays until the member closes it.
    profiles = [];
    await act(async () => void useAwsLoginStore.getState().refreshExpiry());
    expect(document.body.textContent).toContain("AWS login (Production)");
    await act(async () => footButton("Close").click());
    expect(document.body.textContent).not.toContain("AWS login (Production)");
    profiles = [{ ...expiring, expiresAt: "2026-10-02T11:00:00Z" }];
    await act(async () => void useAwsLoginStore.getState().refreshExpiry());
    await tick(0);
    expect(toasts()).toHaveLength(1);
    expect(document.body.textContent).not.toContain("AWS login (Production)");
  });

  it("opens the modal from the notification only for a profile the Agent lists as expiring", async () => {
    await mount();
    const n = (profile: string) =>
      ({ kind: "aws-sso-expiring", payload: { profile }, target: { type: "workspace", id: "" } }) as never;
    await act(async () => void openNotificationTarget(n("forged"), false));
    await tick(0);
    expect(document.body.textContent).not.toContain("AWS login (");
    await act(async () => void openNotificationTarget(n("prod"), false));
    await tick(0);
    expect(document.body.textContent).toContain("AWS login (Production)");
  });

  for (const fail of ["throw", "error"] as const) {
    it(`opens nothing from a notification when the check fails (${fail}), even with an old list`, async () => {
      await mount();
      await act(async () => (document.querySelector(".ui-toast-x") as HTMLButtonElement).click());
      expect(useAwsLoginStore.getState().expiring).toHaveLength(1);
      profilesFail = fail;
      const n = { kind: "aws-sso-expiring", payload: { profile: "prod" }, target: { type: "workspace", id: "" } } as never;
      await act(async () => void openNotificationTarget(n, false));
      await tick(0);
      expect(document.body.textContent).not.toContain("AWS login (");
    });
  }
});
