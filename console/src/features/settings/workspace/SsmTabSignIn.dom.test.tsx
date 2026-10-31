// Shared sign-ins in Settings > AWS profiles/SSM (ADR 0125 decision 6): members listed under one
// header that logs in and out once, the profile form's sign-in choice (own, join, new), the
// header's rename and portal change with their warnings, and the CP's sign-in refusals in words.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import type { ReactNode } from "react";

type Json = Record<string, unknown>;
const writes: { path: string; method: string; body: Json }[] = [];
const toasts: string[] = [];
const confirms: Json[] = [];
let profiles: Json[] = [];
let signIns: Json[] = [];
let states: Json = {};
let writeReply: { ok: boolean; status: number; body: unknown } = { ok: true, status: 200, body: {} };
let confirmAnswer = true;
// Login starts and logouts the Agent refuses as not exported, by profile name.
const notExported = new Set<string>();
const starts: string[] = [];
const logouts: string[] = [];

vi.mock("../../../core/api/client.ts", () => ({
  getTenant: () => "",
  api: vi.fn(async (path: string) => {
    if (path === "api/ssm/profiles") return profiles;
    if (path === "api/ssm/hosts") return [];
    if (path === "api/ssm/sign-ins") return signIns;
    if (path === "api/aws-login/profiles") return states;
    if (path === "api/aws-login") return { requests: [] };
    if (path.endsWith("/logout")) {
      const name = decodeURIComponent(path.split("/").slice(-2)[0]);
      logouts.push(name);
      if (notExported.has(name)) return { error: { code: "not_exported", message: "not in ~/.aws/config" } };
      return { revoked: true, profiles: ["a", "b"] };
    }
    return {};
  }),
  apiJSON: vi.fn(async (path: string) => {
    const name = decodeURIComponent(path.split("/").slice(-2)[0]);
    starts.push(name);
    if (notExported.has(name)) return { error: { code: "not_exported", message: "not in ~/.aws/config" } };
    return { attempt: "a1" };
  }),
  raw: vi.fn(),
  rawJSON: vi.fn(async (path: string, method: string, body: Json) => {
    writes.push({ path, method, body });
    return { ok: writeReply.ok, status: writeReply.status, json: async () => writeReply.body };
  }),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => (m: string) => toasts.push(m) }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({
  useConfirm: () => (o: Json) => {
    confirms.push(o);
    return Promise.resolve(confirmAnswer);
  },
}));

const { SsmTab, resetReloginMarks } = await import("./SsmTab.tsx");
const { t } = await import("../../../lib/i18n/index.ts");
const { useAwsLoginStore } = await import("../../awslogin/store.ts");

const PORTAL = { startUrl: "https://example.awsapps.com/start", ssoRegion: "ap-northeast-1" };
const member = (id: string, name: string): Json => ({
  id,
  name,
  label: name,
  accountId: "123456789012",
  roleName: "Dev",
  region: "",
  ...PORTAL,
  signIn: "team",
});
const solo: Json = { id: "p3", name: "solo", label: "solo", accountId: "210987654321", roleName: "Ops", region: "", ...PORTAL };
const team: Json = { id: "g1", name: "team", ...PORTAL, createdAt: "2026-10-31T00:00:00Z", profiles: ["p1", "p2"] };

let root: Root | null = null;
let host: HTMLDivElement;

async function mount(): Promise<void> {
  await act(async () => root!.render(<SsmTab />));
  await act(async () => {});
  await act(async () => {});
}

const header = () => host.querySelector<HTMLElement>("li.ssm-signin");

function btn(scope: HTMLElement, label: string): HTMLButtonElement {
  const b = Array.from(scope.querySelectorAll("button")).find((x) => x.textContent?.trim() === label);
  if (!b) throw new Error(`no button "${label}" in: ${scope.textContent}`);
  return b as HTMLButtonElement;
}

function field(sel: string): HTMLInputElement | HTMLSelectElement {
  const el = host.querySelector<HTMLInputElement | HTMLSelectElement>(sel);
  if (!el) throw new Error(`no ${sel} in: ${host.textContent}`);
  return el;
}

async function type(el: HTMLInputElement | HTMLSelectElement, value: string): Promise<void> {
  const proto = el instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
  await act(async () => {
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event(el instanceof HTMLSelectElement ? "change" : "input", { bubbles: true }));
  });
}

async function click(b: HTMLButtonElement): Promise<void> {
  await act(async () => b.click());
  await act(async () => {});
  await act(async () => {});
}

const body = (o: Json) => renderToStaticMarkup(<>{o.body as ReactNode}</>);

beforeEach(() => {
  writes.length = 0;
  toasts.length = 0;
  confirms.length = 0;
  useAwsLoginStore.getState().reset();
  profiles = [member("p1", "a"), member("p2", "b"), solo];
  signIns = [team];
  states = {
    profiles: [
      { name: "a", state: "signed_in", signIn: "team" },
      { name: "b", state: "signed_in", signIn: "team" },
      { name: "solo", state: "none" },
    ],
    signIns: [{ name: "team", state: "signed_in", profiles: ["a", "b"] }],
  };
  writeReply = { ok: true, status: 200, body: {} };
  confirmAnswer = true;
  notExported.clear();
  starts.length = 0;
  logouts.length = 0;
  resetReloginMarks();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root?.unmount());
  root = null;
  host.remove();
});

describe("SsmTab shared sign-ins", () => {
  it("lists the members under one header that carries the state, Log in and Log out once", async () => {
    await mount();
    const h = header()!;
    expect(h.querySelector(".ssm-signin-head .ssm-alias")?.textContent).toBe("team");
    expect(h.querySelector(".ssm-signin-head .ssm-login-state")?.textContent).toBe(t("ssm.state_signed_in"));
    expect(h.querySelector(".ssm-meta")?.textContent).toContain(String(PORTAL.startUrl));
    const members = Array.from(h.querySelectorAll<HTMLElement>("li.ssm-member"));
    expect(members.map((m) => m.querySelector(".ssm-alias")?.textContent)).toEqual(["a", "b"]);
    // No login, logout or state of a member's own: the header's is theirs.
    for (const m of members) {
      expect(m.querySelector("button.ssm-login, button.ssm-logout, .ssm-login-state")).toBeNull();
      expect(m.textContent).not.toContain(String(PORTAL.startUrl));
    }
    expect(h.querySelectorAll("button.ssm-login")).toHaveLength(1);
    expect(h.querySelectorAll("button.ssm-logout")).toHaveLength(1);
    // A profile of its own keeps today's row.
    const own = Array.from(host.querySelectorAll<HTMLElement>("li.ssm-item:not(.ssm-member):not(.ssm-signin)"));
    expect(own.map((r) => r.querySelector(".ssm-alias")?.textContent)).toEqual(["solo"]);
    expect(own[0].querySelector("button.ssm-login")).not.toBeNull();
  });

  it("logs out of the sign-in after a confirmation that names every member", async () => {
    await mount();
    await click(header()!.querySelector<HTMLButtonElement>("button.ssm-signin-logout")!);
    expect(confirms).toHaveLength(1);
    expect(confirms[0].title).toBe(t("awslogin.logout_confirm_title_signin", { signIn: "team" }));
    expect(body(confirms[0])).toContain(t("awslogin.logout_confirm_signin", { signIn: "team", profiles: "b" }));
  });

  it("joins an existing sign-in: the portal is the sign-in's and is not sent", async () => {
    await mount();
    await click(btn(host, t("ssm.add_profile")));
    await type(field(".ssm-frm input[placeholder='my-profile']"), "c");
    // A portal typed before choosing the sign-in is not sent: the sign-in's is the one.
    await type(field(".ssm-frm input[placeholder='https://my-company.awsapps.com/start']"), "https://other.awsapps.com/start");
    await type(field("select.ssm-sign-in-select"), "join:team");
    expect(host.querySelector(".ssm-frm input[placeholder='https://my-company.awsapps.com/start']")).toBeNull();
    expect(host.querySelector(".ssm-sign-in-portal")?.textContent).toContain(String(PORTAL.startUrl));
    await click(btn(host.querySelector<HTMLElement>(".ssm-frm")!, t("ssm.add_profile")));
    expect(writes).toHaveLength(1);
    expect(writes[0]).toMatchObject({ path: "api/ssm/profiles", method: "POST" });
    expect(writes[0].body).toMatchObject({ label: "c", signIn: "team", startUrl: "", ssoRegion: "" });
  });

  it("creates a new sign-in only with a valid name and a portal", async () => {
    await mount();
    await click(btn(host, t("ssm.add_profile")));
    await type(field(".ssm-frm input[placeholder='my-profile']"), "c");
    await type(field("select.ssm-sign-in-select"), "new");
    await type(field(".ssm-frm input[placeholder='https://my-company.awsapps.com/start']"), String(PORTAL.startUrl));
    await type(field(".ssm-frm .region-select select"), String(PORTAL.ssoRegion));
    const save = btn(host.querySelector<HTMLElement>(".ssm-frm")!, t("ssm.add_profile"));
    await type(field("input.ssm-sign-in-name"), "bad name");
    expect(save.disabled).toBe(true);
    await type(field("input.ssm-sign-in-name"), "laptop");
    expect(save.disabled).toBe(false);
    await click(save);
    expect(writes[0].body).toMatchObject({ signIn: "laptop", startUrl: PORTAL.startUrl, ssoRegion: PORTAL.ssoRegion });
  });

  it("leaving a sign-in sends signIn \"\" with the profile's own portal, and warns", async () => {
    await mount();
    const a = Array.from(host.querySelectorAll<HTMLElement>("li.ssm-member"))[0];
    await click(btn(a, t("ssm.edit")));
    await type(field("select.ssm-sign-in-select"), "own");
    expect(host.querySelector(".ssm-edit-signin-warn")?.textContent).toBe(t("ssm.edit_signin_leave_warn", { from: "team" }));
    await click(btn(host.querySelector<HTMLElement>(".ssm-frm")!, t("common.save")));
    expect(writes[0]).toMatchObject({ path: "api/ssm/profiles/p1", method: "PUT" });
    expect(writes[0].body).toMatchObject({ signIn: "", startUrl: PORTAL.startUrl, ssoRegion: PORTAL.ssoRegion });
  });

  it("an edit that leaves the sign-in alone does not send it", async () => {
    await mount();
    const own = Array.from(host.querySelectorAll<HTMLElement>("li.ssm-item:not(.ssm-member):not(.ssm-signin)"))[0];
    await click(btn(own, t("ssm.edit")));
    await click(btn(host.querySelector<HTMLElement>(".ssm-frm")!, t("common.save")));
    expect(writes[0].body).not.toHaveProperty("signIn");
  });

  it("a member's edit that does not touch the sign-in keeps the CP's (another tab may have moved it) and needs no login", async () => {
    await mount();
    const a = Array.from(host.querySelectorAll<HTMLElement>("li.ssm-member"))[0];
    await click(btn(a, t("ssm.edit")));
    await type(field(".ssm-frm input[placeholder='123456789012']"), "111111111111");
    await click(btn(host.querySelector<HTMLElement>(".ssm-frm")!, t("common.save")));
    expect(writes[0]).toMatchObject({ path: "api/ssm/profiles/p1", method: "PUT" });
    expect(writes[0].body).not.toHaveProperty("signIn");
    expect(header()!.querySelector(".ssm-login-state")?.textContent).toBe(t("ssm.state_signed_in"));
  });

  it("joining a signed-in sign-in, or naming a new one after a signed-in profile on its portal, needs no new login", async () => {
    states = {
      profiles: [
        { name: "a", state: "signed_in", signIn: "team" },
        { name: "b", state: "signed_in", signIn: "team" },
        { name: "solo", state: "signed_in" },
      ],
      signIns: [{ name: "team", state: "signed_in", profiles: ["a", "b"] }],
    };
    await mount();
    const soloRow = () => Array.from(host.querySelectorAll<HTMLElement>("li.ssm-item:not(.ssm-member):not(.ssm-signin)"))[0];
    await click(btn(soloRow(), t("ssm.edit")));
    await type(field("select.ssm-sign-in-select"), "join:team");
    await click(btn(host.querySelector<HTMLElement>(".ssm-frm")!, t("common.save")));
    expect(writes[0].body).toMatchObject({ signIn: "team" });
    expect(soloRow().querySelector(".ssm-login-state")?.textContent).toBe(t("ssm.state_signed_in"));

    // Decision D: a sign-in named after solo, which is signed in, on solo's portal.
    await click(btn(soloRow(), t("ssm.edit")));
    await type(field("select.ssm-sign-in-select"), "new");
    await type(field("input.ssm-sign-in-name"), "solo");
    await click(btn(host.querySelector<HTMLElement>(".ssm-frm")!, t("common.save")));
    expect(writes[1].body).toMatchObject({ signIn: "solo", startUrl: PORTAL.startUrl });
    expect(soloRow().querySelector(".ssm-login-state")?.textContent).toBe(t("ssm.state_signed_in"));

    // Control: a new sign-in of another name starts signed out.
    await click(btn(soloRow(), t("ssm.edit")));
    await type(field("select.ssm-sign-in-select"), "new");
    await type(field("input.ssm-sign-in-name"), "fresh");
    await click(btn(host.querySelector<HTMLElement>(".ssm-frm")!, t("common.save")));
    expect(soloRow().querySelector(".ssm-login-state")?.textContent).toBe(t("ssm.state_relogin"));
  });

  it("logs in and out through the next member when the Agent refuses the first as not exported", async () => {
    notExported.add("a");
    await mount();
    await click(header()!.querySelector<HTMLButtonElement>("button.ssm-signin-login")!);
    const modal = () => document.querySelector<HTMLElement>(".ui-modal")!;
    await click(btn(modal(), t("awslogin.start")));
    await act(async () => {});
    expect(starts).toEqual(["a", "b"]);
    expect(modal().textContent).toContain("b");
    await click(btn(modal(), t("awslogin.close")));
    await click(header()!.querySelector<HTMLButtonElement>("button.ssm-signin-logout")!);
    expect(logouts).toEqual(["a", "b"]);
    expect(confirms).toHaveLength(1);
  });

  it.each([
    ["sign_in_mismatch", "ssm.err_sign_in_mismatch"],
    ["sign_in_name_taken", "ssm.err_sign_in_name_taken"],
    ["sign_in_exists", "ssm.err_sign_in_exists"],
  ] as const)("says what to do about %s", async (code, key) => {
    writeReply = { ok: false, status: 409, body: { error: { code, message: "server text" } } };
    await mount();
    await click(btn(host, t("ssm.add_profile")));
    await type(field(".ssm-frm input[placeholder='my-profile']"), "c");
    await type(field("select.ssm-sign-in-select"), "join:team");
    await click(btn(host.querySelector<HTMLElement>(".ssm-frm")!, t("ssm.add_profile")));
    expect(toasts).toEqual([t(key)]);
    // The form stays open with what was typed.
    expect((field(".ssm-frm input[placeholder='my-profile']") as HTMLInputElement).value).toBe("c");
  });

  it("renames the sign-in on its header after a warning, and marks every member for a new login", async () => {
    await mount();
    await click(header()!.querySelector<HTMLButtonElement>("button.ssm-signin-edit")!);
    await type(field("input.ssm-signin-name"), "team2");
    confirmAnswer = false;
    await click(header()!.querySelector<HTMLButtonElement>("button.ssm-signin-save")!);
    expect(writes).toHaveLength(0);
    expect(body(confirms[0])).toContain(t("ssm.sign_in_rename_warn", { from: "team", to: "team2" }));
    expect(body(confirms[0])).toContain(t("ssm.sign_in_members_out", { profiles: "a, b" }));
    confirmAnswer = true;
    await click(header()!.querySelector<HTMLButtonElement>("button.ssm-signin-save")!);
    expect(writes).toEqual([{ path: "api/ssm/sign-ins/g1", method: "PUT", body: { name: "team2", ...PORTAL } }]);
    expect(header()!.querySelector(".ssm-login-state")?.textContent).toBe(t("ssm.state_relogin"));
  });

  it("warns that a portal change signs every member out", async () => {
    await mount();
    await click(header()!.querySelector<HTMLButtonElement>("button.ssm-signin-edit")!);
    await type(field("input.ssm-signin-url"), "https://other.awsapps.com/start");
    await click(header()!.querySelector<HTMLButtonElement>("button.ssm-signin-save")!);
    expect(body(confirms[0])).toContain(t("ssm.sign_in_portal_warn"));
    expect(writes[0].body).toEqual({ name: "team", startUrl: "https://other.awsapps.com/start", ssoRegion: PORTAL.ssoRegion });
  });

  it("never shows or sends what the state answer carries beyond its fields", async () => {
    const sentinel = "zq-sentinel-token";
    states = {
      profiles: [{ name: "a", state: "signed_in", signIn: "team", accessToken: sentinel }],
      signIns: [{ name: "team", state: "signed_in", profiles: ["a", "b"], accessToken: sentinel, refreshToken: sentinel }],
    };
    await mount();
    await click(header()!.querySelector<HTMLButtonElement>("button.ssm-signin-logout")!);
    expect(host.innerHTML).not.toContain(sentinel);
    expect(confirms.map(body).join("")).not.toContain(sentinel);
    expect(JSON.stringify(writes)).not.toContain(sentinel);
    expect(useAwsLoginStore.getState().signIns.map((s) => s.name)).toEqual(["team"]);
    expect(JSON.stringify(useAwsLoginStore.getState().signIns)).not.toContain(sentinel);
  });
});
