// "Share one sign-in" on selected ungrouped profiles (ADR 0125 decision 6): the selection, the
// proposed name (a member that is signed in now keeps its login), the write order, and a run that
// ends half done staying visible and retryable.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

type Json = Record<string, any>;
const writes: { path: string; method: string; body: Json }[] = [];
let profiles: Json[] = [];
let signIns: Json[] = [];
let states: Json = {};
// The reply to the n-th write (1-based); anything not listed succeeds.
let replies: Record<number, { status: number; code?: string } | "throw"> = {};
const logs: string[] = [];

vi.mock("../../../core/api/client.ts", () => ({
  getTenant: () => "",
  api: vi.fn(async (path: string) => {
    if (path === "api/ssm/profiles") return profiles;
    if (path === "api/ssm/hosts") return [];
    if (path === "api/ssm/sign-ins") return signIns;
    if (path === "api/aws-login/profiles") return states;
    if (path === "api/aws-login") return { requests: [] };
    return {};
  }),
  apiJSON: vi.fn(),
  raw: vi.fn(),
  rawJSON: vi.fn(async (path: string, method: string, body: Json) => {
    writes.push({ path, method, body });
    const r = replies[writes.length];
    if (r === "throw") throw new Error("network");
    if (r) return { ok: false, status: r.status, json: async () => ({ error: { code: r.code } }) };
    // The CP is the source of truth: a landed write moves the profile into the sign-in.
    const row = profiles.find((x) => `api/ssm/profiles/${x.id}` === path);
    if (row && body.signIn) {
      row.signIn = body.signIn;
      let si = signIns.find((x) => x.name === body.signIn);
      if (!si) signIns.push((si = { id: "g-" + body.signIn, name: body.signIn, ...PORTAL, createdAt: "", profiles: [] }));
      si.profiles.push(row.id);
    }
    return { ok: true, status: 200, json: async () => ({}) };
  }),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => () => Promise.resolve(true) }));

const { SsmTab, resetReloginMarks } = await import("./SsmTab.tsx");
const { t } = await import("../../../lib/i18n/index.ts");
const { useAwsLoginStore } = await import("../../awslogin/store.ts");

const PORTAL = { startUrl: "https://example.awsapps.com/start", ssoRegion: "ap-northeast-1" };
const prof = (id: string, name: string, extra: Json = {}): Json => ({
  id,
  name,
  label: name,
  accountId: "123456789012",
  roleName: "Dev",
  region: "us-east-1",
  confirmBeforeUse: id === "pc",
  ...PORTAL,
  ...extra,
});

let root: Root | null = null;
let host: HTMLDivElement;

const mount = async () => {
  await act(async () => root!.render(<SsmTab />));
  await act(async () => {});
  await act(async () => {});
};
const tick = async () => {
  for (let i = 0; i < 4; i++) await act(async () => {});
};
const pick = async (label: string) => {
  const box = host.querySelector<HTMLInputElement>(`input.ssm-share-pick[aria-label="${t("ssm.share_pick", { label })}"]`)!;
  await act(async () => box.click());
};
const open = async () => {
  await act(async () => host.querySelector<HTMLButtonElement>("button.ssm-share-open")!.click());
};
const dlg = () => document.querySelector<HTMLElement>(".ssm-share")!;
const nameInput = () => dlg().querySelector<HTMLInputElement>("input.ssm-share-name")!;
const doBtn = () => dlg().querySelector<HTMLButtonElement>("button.ssm-share-do")!;
async function type(el: HTMLInputElement, v: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, v);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const run = async () => {
  await act(async () => doBtn().click());
  await tick();
};

beforeEach(() => {
  writes.length = 0;
  replies = {};
  useAwsLoginStore.getState().reset();
  profiles = [prof("pa", "a"), prof("pb", "b"), prof("pc", "c"), prof("pd", "d", { startUrl: "https://other.awsapps.com/start" })];
  signIns = [];
  states = {
    profiles: [
      { name: "a", state: "none" },
      { name: "b", state: "signed_in" },
      { name: "c", state: "none" },
      { name: "d", state: "signed_in" },
    ],
    signIns: [],
  };
  resetReloginMarks();
  logs.length = 0;
  for (const m of ["log", "info", "warn", "error", "debug"] as const) {
    vi.spyOn(console, m).mockImplementation((...a: unknown[]) => void logs.push(a.map(String).join(" ")));
  }
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root?.unmount());
  root = null;
  host.remove();
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

describe("Share one sign-in", () => {
  it("needs two profiles of one start URL and SSO region", async () => {
    await mount();
    expect(host.querySelector("button.ssm-share-open")).toBeNull();
    await pick("a");
    expect(host.querySelector<HTMLButtonElement>("button.ssm-share-open")!.disabled).toBe(true);
    await pick("d");
    const b = host.querySelector<HTMLButtonElement>("button.ssm-share-open")!;
    expect(b.disabled).toBe(true);
    expect(host.textContent).toContain(t("ssm.share_mixed"));
    await pick("d");
    await pick("b");
    expect(host.querySelector<HTMLButtonElement>("button.ssm-share-open")!.disabled).toBe(false);
  });

  it("proposes the name of a member that is signed in now, writes it first, and keeps its login", async () => {
    await mount();
    for (const l of ["a", "b", "c"]) await pick(l);
    await open();
    expect(nameInput().value).toBe("b");
    expect(dlg().textContent).toContain(t("ssm.share_keeps", { name: "b" }));
    await run();
    // b first: until it has moved in, it is a profile of its own named like the sign-in.
    expect(writes.map((w) => w.path)).toEqual(["api/ssm/profiles/pb", "api/ssm/profiles/pa", "api/ssm/profiles/pc"]);
    expect(writes.every((w) => w.method === "PUT")).toBe(true);
    expect(writes[0].body).toEqual({
      label: "b",
      ...PORTAL,
      accountId: "123456789012",
      roleName: "Dev",
      region: "us-east-1",
      confirmBeforeUse: false,
      signIn: "b",
    });
    // confirmBeforeUse is carried, not reset, for a profile that has it on.
    expect(writes[2].body.confirmBeforeUse).toBe(true);
    expect(dlg().textContent).toContain(t("ssm.share_done", { total: 3, name: "b" }));
    // The group kept b's login: no member is marked as needing a new one.
    expect(document.body.textContent).not.toContain(t("ssm.state_relogin"));
  });

  it("says the group starts signed out when no member is signed in, and marks the members", async () => {
    states = { profiles: [], signIns: [] };
    await mount();
    await pick("a");
    await pick("c");
    await open();
    expect(nameInput().value).toBe("a");
    expect(dlg().textContent).toContain(t("ssm.share_fresh"));
    await type(nameInput(), "team");
    await run();
    expect(writes.map((w) => (w.body as Json).signIn)).toEqual(["team", "team"]);
    await act(async () => document.querySelector<HTMLButtonElement>(".ssm-share button.ghost")!.click());
    expect(host.querySelectorAll(".ssm-signin .ssm-signin-head .ssm-login-state.relogin")).toHaveLength(1);
  });

  it("refuses a name that is another profile's own sign-in, or another portal's sign-in, before any write", async () => {
    signIns = [{ id: "g", name: "team", ...PORTAL, startUrl: "https://other.awsapps.com/start", createdAt: "", profiles: ["pz"] }];
    await mount();
    await pick("a");
    await pick("b");
    await open();
    await type(nameInput(), "c");
    expect(dlg().textContent).toContain(t("ssm.share_p_taken"));
    expect(doBtn().disabled).toBe(true);
    await type(nameInput(), "team");
    expect(dlg().textContent).toContain(t("ssm.share_p_mismatch"));
    expect(doBtn().disabled).toBe(true);
    await type(nameInput(), "bad name");
    expect(dlg().textContent).toContain(t("ssm.share_p_invalid"));
    expect(doBtn().disabled).toBe(true);
    expect(writes).toHaveLength(0);
  });

  it("stops with nothing changed when the write that creates the sign-in is refused", async () => {
    replies = { 1: { status: 409, code: "sign_in_name_taken" } };
    await mount();
    await pick("a");
    await pick("b");
    await open();
    await run();
    expect(writes).toHaveLength(1);
    const text = dlg().textContent!;
    expect(text).toContain("sign_in_name_taken");
    expect(text).toContain(t("ssm.err_sign_in_name_taken"));
    expect(text).toContain(t("ssm.share_r_not_run", { label: "a" }));
    // Still retryable, and the name may still be changed.
    expect(doBtn().textContent).toContain("2");
    expect(nameInput().disabled).toBe(false);
  });

  it("shows a half group, keeps the failed profile selected for a retry, and the retry moves only it", async () => {
    replies = { 3: { status: 409, code: "sign_in_mismatch" } };
    await mount();
    for (const l of ["a", "b", "c"]) await pick(l);
    await open();
    await run();
    expect(writes).toHaveLength(3);
    const text = dlg().textContent!;
    expect(text).toContain(t("ssm.share_partial", { done: 2, total: 3, name: "b" }));
    expect(text).toContain("sign_in_mismatch");
    expect(dlg().querySelector('[data-profile="c"] .ssm-share-outcome')?.className).toContain("failed");
    expect(dlg().querySelector('[data-profile="a"] .ssm-share-outcome')?.className).toContain("joined");
    // The list behind the dialog shows the group the first two made.
    expect(Array.from(host.querySelectorAll("li.ssm-signin li.ssm-member .ssm-alias")).map((e) => e.textContent)).toEqual(["a", "b"]);
    expect(Array.from(host.querySelectorAll("ul.ssm-list > li.ssm-item:not(.ssm-signin) .ssm-alias")).map((e) => e.textContent)).toContain("c");
    expect(nameInput().disabled).toBe(true);
    replies = {};
    await run();
    expect(writes.slice(3).map((w) => w.path)).toEqual(["api/ssm/profiles/pc"]);
    expect(dlg().textContent).toContain(t("ssm.share_done", { total: 3, name: "b" }));
  });

  it("marks a write that never answered as unknown and sends nothing after it", async () => {
    replies = { 2: "throw" };
    await mount();
    for (const l of ["a", "b", "c"]) await pick(l);
    await open();
    await run();
    expect(writes).toHaveLength(2);
    const text = dlg().textContent!;
    expect(text).toContain(t("ssm.share_r_unknown", { label: "a" }));
    expect(text).toContain(t("ssm.share_r_not_run", { label: "c" }));
  });

  it("sends nothing beyond the profile's own fields and the name, and shows no token", async () => {
    states = {
      profiles: [{ name: "b", state: "signed_in", accessToken: "TOKENSENTINEL" }],
      signIns: [],
    };
    await mount();
    await pick("a");
    await pick("b");
    await open();
    await run();
    const seen = JSON.stringify(writes) + document.body.innerHTML + logs.join("\n");
    expect(seen).not.toContain("TOKENSENTINEL");
    for (const w of writes) expect(Object.keys(w.body).sort()).toEqual(["accountId", "confirmBeforeUse", "label", "region", "roleName", "signIn", "ssoRegion", "startUrl"]);
  });

  it("refuses a name an unticked profile renders too, even when a member renders the same name", async () => {
    profiles.push(prof("pe", "a b", { name: "a-b" }), prof("pf", "a-b"));
    await mount();
    await pick("a b");
    await pick("c");
    await open();
    await type(nameInput(), "a-b");
    expect(dlg().textContent).toContain(t("ssm.share_p_taken"));
    expect(doBtn().disabled).toBe(true);
  });

  it("refuses a group holding two profiles that render the sign-in's name, before any write", async () => {
    profiles.push(prof("pe", "a b", { name: "a-b" }), prof("pf", "a-b"));
    await mount();
    await pick("a b");
    await pick("a-b");
    await open();
    await type(nameInput(), "a-b");
    expect(dlg().textContent).toContain(t("ssm.share_p_taken"));
    expect(doBtn().disabled).toBe(true);
    expect(writes).toHaveLength(0);
  });

  it("re-reads before writing: a portal changed in another tab, or a sign-in created there, stops the run", async () => {
    await mount();
    await pick("a");
    await pick("c");
    await open();
    profiles.find((x) => x.id === "pa")!.startUrl = "https://moved.awsapps.com/start";
    await type(nameInput(), "team");
    await run();
    expect(writes).toHaveLength(0);
    expect(dlg().textContent).toContain(t("ssm.share_stale"));
    profiles.find((x) => x.id === "pa")!.startUrl = PORTAL.startUrl;
    signIns.push({ id: "g", name: "team", ...PORTAL, startUrl: "https://other.awsapps.com/start", createdAt: "", profiles: ["pz"] });
    await run();
    expect(writes).toHaveLength(0);
    expect(dlg().textContent).toContain(t("ssm.share_p_mismatch"));
  });
});
