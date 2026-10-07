// #1850: "Log out" of a Google Cloud profile, from the WS bar badge and from Settings. The
// Agent signs the workspace out of the profile's account, so the confirmation names every
// other signed-in profile on that account before anything is sent.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";

type Json = Record<string, unknown>;
const calls: string[] = [];
let profiles: Json[] = [];
let logoutReply: Json = {};
// What the Agent lists once the logout went through.
let afterLogout: Json[] | null = null;
let confirmAnswer = true;
let tenant = "t1";
// The bodies of the logout POSTs, in order.
const logoutBodies: string[] = [];
// Set: the profile list cannot be read. holds: each logout, in turn, answers only once its promise settles.
let profilesFail = false;
const holds: Promise<void>[] = [];
// Profile-list answers to give, in turn, each once its promise settles; then `profiles`.
const profileAnswers: { hold: Promise<void>; list: Json[] }[] = [];
// Runs while the confirmation is open, before the member answers.
let duringConfirm: (() => void) | null = null;
const confirms: { title: string; body: ReactNode }[] = [];
const toasts: { msg: string; kind?: string }[] = [];

vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => tenant,
  api: vi.fn(async (path: string, opts?: RequestInit) => {
    calls.push(`${opts?.method || "GET"} ${path}`);
    if (path === "api/gcp-login/profiles") {
      const a = profileAnswers.shift();
      if (a) {
        await a.hold;
        return { profiles: a.list };
      }
      return profilesFail ? { error: { code: "x" } } : { profiles };
    }
    if (path === "api/gcp-login") return { requests: [] };
    if (path.endsWith("/logout")) {
      logoutBodies.push(String(opts?.body ?? ""));
      const h = holds.shift();
      if (h) await h;
      profiles = afterLogout ?? profiles;
      return logoutReply;
    }
    if (path === "api/gcp/profiles")
      return [
        {
          id: "g1",
          label: "Production",
          loginMethod: "google",
          project: "prod-project",
          account: "",
          name: "prod",
        },
        {
          id: "g3",
          label: "",
          loginMethod: "google",
          project: "dev-project",
          account: "",
          name: "dev",
        },
      ];
    return {};
  }),
  apiJSON: vi.fn(async () => ({})),
  raw: vi.fn(),
  rawJSON: vi.fn(),
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({
  useToast: () => (msg: string, o?: { kind?: string }) => toasts.push({ msg, kind: o?.kind }),
}));
vi.mock("../../ui/ConfirmProvider.tsx", () => ({
  useConfirm: () => (o: { title: string; body: ReactNode }) => {
    confirms.push(o);
    duringConfirm?.();
    return Promise.resolve(confirmAnswer);
  },
}));

const { GcpProfilesChip } = await import("./GcpProfilesChip.tsx");
const { GcpTab } = await import("../settings/workspace/GcpTab.tsx");
const { useGcpLoginStore } = await import("./store.ts");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { setLocale } = await import("../../lib/i18n/index.ts");

const prod: Json = {
  name: "prod",
  state: "signed_in",
  label: "Production",
  project: "prod-project",
  account: "dev@example.com",
};
const stg: Json = {
  name: "stg",
  state: "signed_in",
  label: "Staging",
  project: "stg-project",
  account: "dev@example.com",
};
const ops: Json = {
  name: "ops",
  state: "signed_in",
  label: "Ops",
  project: "ops-project",
  account: "ops@example.com",
};
const dev: Json = {
  name: "dev",
  state: "none",
  label: "",
  project: "dev-project",
};

let root: Root | null = null;
let host: HTMLDivElement;

async function flush(): Promise<void> {
  for (let i = 0; i < 4; i++) await act(async () => await Promise.resolve());
}
async function mount(node: ReactNode): Promise<void> {
  await act(async () => root!.render(node));
  await flush();
}
const rows = () => Array.from(host.querySelectorAll(".ws-gcp-row"));
const logoutOf = (i: number) => rows()[i].querySelector<HTMLButtonElement>(".ws-gcp-logout");
async function openPop(): Promise<void> {
  await act(async () => host.querySelector<HTMLButtonElement>(".ws-gcp-btn")!.click());
  await flush();
}
function bodyText(node: ReactNode): string {
  const div = document.createElement("div");
  const r = createRoot(div);
  act(() => r.render(<>{node}</>));
  const text = div.textContent || "";
  act(() => r.unmount());
  return text;
}

beforeEach(() => {
  setLocale("en");
  calls.length = 0;
  confirms.length = 0;
  toasts.length = 0;
  confirmAnswer = true;
  profiles = [prod, stg, ops, dev];
  logoutReply = { account: "dev@example.com", profiles: ["prod", "stg"] };
  afterLogout = null;
  tenant = "t1";
  logoutBodies.length = 0;
  profilesFail = false;
  holds.length = 0;
  profileAnswers.length = 0;
  duringConfirm = null;
  useGcpLoginStore.setState({
    profiles: null,
    requests: [],
    hidden: {},
    modal: null,
    profileModal: null,
    loggingOut: {},
  });
  useWorkspaceStore.setState({ state: "running" });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  document.body.innerHTML = "";
  root = null;
});

describe("Google Cloud profile logout", () => {
  it("offers Log out on signed-in rows only, names the profiles sharing the account, and logs out", async () => {
    await mount(<GcpProfilesChip />);
    await openPop();
    const names = rows().map((r) => r.querySelector(".ws-aws-name")!.textContent);
    expect(names).toEqual(["Ops", "Production", "Staging", "dev"]);
    expect(logoutOf(3)).toBeNull();
    afterLogout = [{ ...prod, state: "none" }, { ...stg, state: "none" }, ops, dev];
    await act(async () => logoutOf(1)!.click());
    await flush();
    expect(confirms).toHaveLength(1);
    expect(confirms[0].title).toBe("Log out of Production?");
    const body = bodyText(confirms[0].body);
    expect(body).toContain("dev@example.com");
    expect(body).toContain("Also signed out: Staging");
    expect(body).not.toContain("Ops");
    expect(calls.filter((c) => c.endsWith("/logout"))).toEqual(["POST api/gcp-login/profiles/prod/logout"]);
    expect(toasts).toEqual([
      {
        msg: "Logged out of Production, and of Staging with the same account.",
        kind: "success",
      },
    ]);
    expect(host.querySelector(".ws-gcp-btn")!.textContent).toContain("Ops");
  });

  it("names no other profile when the account is the profile's alone", async () => {
    logoutReply = { account: "ops@example.com", profiles: ["ops"] };
    await mount(<GcpProfilesChip />);
    await openPop();
    await act(async () => logoutOf(0)!.click());
    await flush();
    expect(bodyText(confirms[0].body)).not.toContain("Also signed out");
    expect(toasts).toEqual([{ msg: "Logged out of Ops.", kind: "success" }]);
  });

  it("sends nothing when the member declines, and says why the Agent refused", async () => {
    confirmAnswer = false;
    await mount(<GcpProfilesChip />);
    await openPop();
    await act(async () => logoutOf(0)!.click());
    await flush();
    expect(calls.some((c) => c.endsWith("/logout"))).toBe(false);
    expect(toasts).toEqual([]);

    confirmAnswer = true;
    logoutReply = {
      error: { code: "busy", message: "another login holds the store" },
    };
    await openPop();
    await act(async () => logoutOf(0)!.click());
    await flush();
    expect(toasts).toEqual([
      {
        msg: "Another Google Cloud login is in progress in the workspace. Try again in a moment.",
        kind: undefined,
      },
    ]);
  });

  it("sends one logout while one is running", async () => {
    useGcpLoginStore.setState({ loggingOut: { prod: 99 } });
    const r = await useGcpLoginStore.getState().logoutProfile("prod", "dev@example.com", "t1");
    expect(r).toEqual({ ok: false, code: "in_flight", message: "" });
    expect(calls.some((c) => c.endsWith("/logout"))).toBe(false);
  });

  it("sends the confirmed account, and says so when the Agent finds another one selected", async () => {
    logoutReply = { error: { code: "account_changed", message: "x" } };
    await mount(<GcpProfilesChip />);
    await openPop();
    await act(async () => logoutOf(1)!.click());
    await flush();
    expect(logoutBodies).toEqual([JSON.stringify({ account: "dev@example.com" })]);
    expect(toasts).toEqual([
      {
        msg: "The profile's account changed since you confirmed, so nothing was logged out. Look at the list again and retry.",
        kind: undefined,
      },
    ]);
  });

  it("asks nothing and sends nothing when the profiles cannot be read now", async () => {
    await mount(<GcpProfilesChip />);
    await openPop();
    profilesFail = true;
    await act(async () => logoutOf(1)!.click());
    await flush();
    expect(confirms).toEqual([]);
    expect(logoutBodies).toEqual([]);
    expect(toasts[0].msg).toContain("nothing was logged out");
  });

  it("sends nothing once another tenant is selected during the confirmation", async () => {
    await mount(<GcpProfilesChip />);
    await openPop();
    duringConfirm = () => {
      tenant = "t2";
    };
    await act(async () => logoutOf(1)!.click());
    await flush();
    expect(confirms).toHaveLength(1);
    expect(logoutBodies).toEqual([]);
    expect(toasts).toEqual([]);
  });

  it("confirms with its own ask's list, not an older ask's that answered after it", async () => {
    await mount(<GcpProfilesChip />);
    await openPop();
    let releaseOld!: () => void;
    let releaseNew!: () => void;
    profileAnswers.push(
      { hold: new Promise<void>((r) => (releaseOld = r)), list: [prod, { ...stg, state: "none", account: "" }, ops, dev] },
      { hold: new Promise<void>((r) => (releaseNew = r)), list: [prod, stg, ops, dev] },
    );
    // Declined, so no logout re-reads the list afterwards and the store shows which answer won.
    confirmAnswer = false;
    // An earlier ask (the tab coming back, say) still waits when the logout asks.
    const older = useGcpLoginStore.getState().refreshProfiles();
    await act(async () => logoutOf(1)!.click());
    await act(async () => {
      releaseNew();
      releaseOld();
      await older;
    });
    await flush();
    expect(bodyText(confirms[0].body)).toContain("Also signed out: Staging");
    expect(useGcpLoginStore.getState().profiles!.find((p) => p.name === "stg")!.state).toBe("signed_in");
  });

  it("does not let the previous tenant's answer free the next tenant's running logout", async () => {
    let release1!: () => void;
    let release2!: () => void;
    holds.push(new Promise<void>((r) => (release1 = r)), new Promise<void>((r) => (release2 = r)));
    const first = useGcpLoginStore.getState().logoutProfile("prod", "dev@example.com", "t1");
    await flush();
    // The tenant switch resets the store; the new tenant's logout of a same-named profile runs.
    tenant = "t2";
    useGcpLoginStore.getState().reset();
    const second = useGcpLoginStore.getState().logoutProfile("prod", "other@example.com", "t2");
    await flush();
    const mark = useGcpLoginStore.getState().loggingOut.prod;
    expect(typeof mark).toBe("number");
    release1();
    expect(await first).toEqual({ ok: false, code: "tenant_changed", message: "" });
    expect(useGcpLoginStore.getState().loggingOut.prod).toBe(mark);
    expect(await useGcpLoginStore.getState().logoutProfile("prod", "other@example.com", "t2")).toEqual({
      ok: false,
      code: "in_flight",
      message: "",
    });
    release2();
    expect((await second).ok).toBe(true);
    expect(useGcpLoginStore.getState().loggingOut.prod).toBeUndefined();
    expect(logoutBodies).toHaveLength(2);
  });

  it("offers Log out on a signed-in Settings row and re-reads the states after it", async () => {
    await mount(<GcpTab />);
    const item = (id: string) => host.querySelector(`[data-profile="${id}"]`)!;
    expect(item("g3").querySelector(".gcp-logout")).toBeNull();
    const before = calls.filter((c) => c === "GET api/gcp-login/profiles").length;
    await act(async () => item("g1").querySelector<HTMLButtonElement>(".gcp-logout")!.click());
    await flush();
    expect(calls).toContain("POST api/gcp-login/profiles/prod/logout");
    expect(bodyText(confirms[0].body)).toContain("Also signed out: Staging");
    expect(calls.filter((c) => c === "GET api/gcp-login/profiles").length).toBeGreaterThan(before + 1);
  });
});
