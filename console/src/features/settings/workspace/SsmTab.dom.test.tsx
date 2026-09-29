// The profile row's "Log in" (#1028): it is off for rows the workspace cannot log in to,
// opening it starts nothing, and the press starts an attempt for the row's profile name and
// shows that attempt's code only.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

type Json = Record<string, unknown>;
const calls: { path: string; method: string }[] = [];
let profiles: Json[] = [];
let startReply: Json = { attempt: "att1" };
let attemptReplies: Json[] = [];
let states: Json[] = [];

vi.mock("../../../core/api/client.ts", () => ({
  api: vi.fn(async (path: string) => {
    calls.push({ path, method: "GET" });
    if (path === "api/ssm/profiles") return profiles;
    if (path === "api/ssm/hosts") return [];
    if (path === "api/aws-login") return { requests: [] };
    if (path === "api/aws-login/profiles") return { profiles: states };
    if (path.includes("/attempts/")) return attemptReplies.length > 1 ? attemptReplies.shift() : attemptReplies[0];
    return {};
  }),
  apiJSON: vi.fn(async (path: string, method: string) => {
    calls.push({ path, method });
    return startReply;
  }),
  raw: vi.fn(),
  rawJSON: vi.fn(),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => () => Promise.resolve(true) }));

const { SsmTab } = await import("./SsmTab.tsx");
const { t } = await import("../../../lib/i18n/index.ts");

const prod: Json = {
  id: "p1",
  name: "prod-app",
  label: "prod app",
  accountId: "123456789012",
  roleName: "Dev",
  startUrl: "https://example.awsapps.com/start",
  ssoRegion: "ap-northeast-1",
  region: "",
};

let root: Root | null = null;
let host: HTMLDivElement;

async function tick(ms = 0): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

async function mount(): Promise<void> {
  await act(async () => {
    root!.render(<SsmTab />);
  });
  await tick();
}

function loginButtons(): HTMLButtonElement[] {
  return Array.from(host.querySelectorAll<HTMLButtonElement>("button.ssm-login"));
}

// A button in the open modal: the row's own button is also labelled "Log in".
function button(label: string): HTMLButtonElement {
  const b = Array.from(modal()?.querySelectorAll("button") ?? []).find((x) => x.textContent?.trim() === label);
  if (!b) throw new Error(`no button "${label}" in: ${document.body.textContent}`);
  return b as HTMLButtonElement;
}

function modal(): HTMLElement | null {
  return document.body.querySelector<HTMLElement>(".ui-modal");
}

beforeEach(() => {
  vi.useFakeTimers();
  calls.length = 0;
  profiles = [prod];
  startReply = { attempt: "att1" };
  attemptReplies = [{ phase: "starting" }];
  states = [];
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root?.unmount());
  root = null;
  host.remove();
  vi.useRealTimers();
});

describe("SsmTab profile row Log in", () => {
  it("is off, with the reason, for rows the workspace cannot log in to", async () => {
    profiles = [
      prod,
      { ...prod, id: "p2", name: "dup", label: "dup", nameCollides: true },
      { ...prod, id: "p3", name: "half", label: "half", roleName: "" },
      { ...prod, id: "p4", name: undefined, label: "old-cp" },
    ];
    await mount();
    const [ok, dup, half, old] = loginButtons();
    expect(ok.disabled).toBe(false);
    expect(dup.disabled).toBe(true);
    expect(dup.title).toBe(t("ssm.login_off_collides", { name: "dup" }));
    expect(half.disabled).toBe(true);
    expect(half.title).toBe(t("ssm.login_off_incomplete"));
    expect(old.disabled).toBe(true);
  });

  it("starts nothing on open, then starts the row's profile and shows only its own attempt", async () => {
    await mount();
    await act(async () => loginButtons()[0].click());
    expect(modal()?.textContent).toContain("prod-app");
    expect(modal()?.textContent).toContain(t("awslogin.profile_intro"));
    await tick(5000);
    expect(calls.filter((c) => c.method === "POST")).toEqual([]);

    attemptReplies = [
      { phase: "authorize", url: "https://device.sso.ap-northeast-1.amazonaws.com/?user_code=WXYZ-1234", code: "WXYZ-1234" },
      { phase: "replaced" },
    ];
    await act(async () => button(t("awslogin.start")).click());
    expect(calls.filter((c) => c.method === "POST")).toEqual([
      { path: "api/aws-login/profiles/prod-app/start", method: "POST" },
    ]);
    await tick(300);
    expect(calls.some((c) => c.path === "api/aws-login/profiles/prod-app/attempts/att1")).toBe(true);
    expect(modal()?.textContent).toContain("WXYZ-1234");
    // Replaced by another press: the code goes and the modal offers to start again.
    await tick(1500);
    expect(modal()?.textContent).not.toContain("WXYZ-1234");
    expect(modal()?.textContent).toContain(t("awslogin.replaced"));
    button(t("awslogin.retry"));
    // No cancel: a row's login has no request behind it.
    expect(modal()?.textContent).not.toContain(t("awslogin.cancel_request"));
  });

  it("shows each row's login state, and asks again after the login window closes", async () => {
    profiles = [prod, { ...prod, id: "p2", name: "dev", label: "dev" }, { ...prod, id: "p3", name: "stg", label: "stg" }];
    states = [
      { name: "prod-app", state: "none" },
      { name: "dev", state: "renew" },
    ];
    await mount();
    const badges = () => Array.from(host.querySelectorAll(".ssm-login-state")).map((b) => b.textContent);
    expect(badges()).toEqual([t("ssm.state_none"), t("ssm.state_renew")]);
    states = [
      { name: "prod-app", state: "signed_in" },
      { name: "dev", state: "renew" },
    ];
    await act(async () => loginButtons()[0].click());
    await act(async () => button(t("awslogin.close")).click());
    await tick();
    expect(badges()).toEqual([t("ssm.state_signed_in"), t("ssm.state_renew")]);
  });

  it("says why the Agent refused, in the member's words", async () => {
    startReply = { error: { code: "not_exported", message: "this profile is not in ~/.aws/config" } };
    await mount();
    await act(async () => loginButtons()[0].click());
    await act(async () => button(t("awslogin.start")).click());
    expect(modal()?.textContent).toContain(t("awslogin.err_not_exported"));
  });

  it("refreshes the pending requests once the login is done", async () => {
    attemptReplies = [{ phase: "done" }];
    await mount();
    await act(async () => loginButtons()[0].click());
    await act(async () => button(t("awslogin.start")).click());
    await tick(300);
    expect(modal()?.textContent).toContain(t("awslogin.profile_done"));
    expect(calls.some((c) => c.path === "api/aws-login")).toBe(true);
  });
});
