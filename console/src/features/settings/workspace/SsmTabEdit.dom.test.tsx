// Editing a profile or a host in place (#1409): the row's Edit opens the form pre-filled and
// saves through PUT on the same id, so the hosts that use a profile keep pointing at it.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

type Json = Record<string, unknown>;
const gets: string[] = [];
const writes: { path: string; method: string; body: Json }[] = [];
const toasts: string[] = [];
const confirms: Json[] = [];
let profiles: Json[] = [];
let hosts: Json[] = [];
let writeReply: { ok: boolean; status: number; body: unknown } = { ok: true, status: 200, body: {} };
let confirmAnswer = false;
let states: Json[] = [];
// When set, the next write waits for release() before it answers.
let hold: { release: () => void } | null = null;
let profilesGate: Promise<void> | null = null;
let delHold: { release: () => void } | null = null;
let delReply: { ok: boolean; status: number; body: unknown } = { ok: true, status: 204, body: null };

vi.mock("../../../core/api/client.ts", () => ({
  api: vi.fn(async (path: string) => {
    gets.push(path);
    if (path === "api/ssm/profiles") {
      if (profilesGate) await profilesGate;
      return profiles;
    }
    if (path === "api/ssm/hosts") return hosts;
    if (path === "api/aws-login/profiles") return { profiles: states };
    if (path === "api/aws-login") return { requests: [] };
    if (path.includes("/attempts/")) return { phase: "done" };
    return {};
  }),
  apiJSON: vi.fn(async () => ({ attempt: "a1" })),
  raw: vi.fn(async (path: string) => {
    if (delHold) await new Promise<void>((r) => (delHold!.release = r));
    if (!delReply.ok) return { ok: false, status: delReply.status, json: async () => delReply.body };
    const id = decodeURIComponent(path.split("/").pop() || "");
    if (path.startsWith("api/ssm/profiles/")) profiles = profiles.filter((p) => p.id !== id);
    if (path.startsWith("api/ssm/hosts/")) hosts = hosts.filter((h) => h.id !== id);
    return { ok: true, status: 204 };
  }),
  rawJSON: vi.fn(async (path: string, method: string, body: Json) => {
    writes.push({ path, method, body });
    if (hold) await new Promise<void>((r) => (hold!.release = r));
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

const { SsmTab, awsProfileName, resetReloginMarks } = await import("./SsmTab.tsx");
const { t } = await import("../../../lib/i18n/index.ts");

const prod: Json = {
  id: "p1",
  name: "prod",
  label: "prod",
  accountId: "123456789012",
  roleName: "Dev",
  startUrl: "https://example.awsapps.com/start",
  ssoRegion: "ap-northeast-1",
  region: "",
};
const stg: Json = { ...prod, id: "p2", name: "stg", label: "stg" };
const web: Json = { id: "h1", alias: "web-01", profileId: "p1", instanceId: "i-0123456789abcdef0", documentName: "", region: "" };

let root: Root | null = null;
let host: HTMLDivElement;

async function mount(): Promise<void> {
  await act(async () => root!.render(<SsmTab />));
  await act(async () => {});
}

const sections = () => Array.from(host.querySelectorAll<HTMLElement>("section.ssm-section"));
const rows = (sec: number) => Array.from(sections()[sec].querySelectorAll<HTMLElement>("li.ssm-item"));

function btn(scope: HTMLElement, label: string): HTMLButtonElement {
  const b = Array.from(scope.querySelectorAll("button")).find((x) => x.textContent?.trim() === label);
  if (!b) throw new Error(`no button "${label}" in: ${scope.textContent}`);
  return b as HTMLButtonElement;
}

// The input in the open form whose placeholder is ph.
function input(ph: string): HTMLInputElement {
  const el = host.querySelector<HTMLInputElement>(`.ssm-frm input[placeholder="${ph}"]`);
  if (!el) throw new Error(`no input "${ph}"`);
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
}

beforeEach(() => {
  gets.length = 0;
  writes.length = 0;
  toasts.length = 0;
  confirms.length = 0;
  profiles = [prod, stg];
  hosts = [web];
  writeReply = { ok: true, status: 200, body: {} };
  confirmAnswer = false;
  states = [];
  hold = null;
  profilesGate = null;
  delHold = null;
  delReply = { ok: true, status: 204, body: null };
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

describe("SsmTab profile Edit", () => {
  it("opens the form pre-filled and saves through PUT on the same id", async () => {
    await mount();
    await click(btn(rows(0)[0], t("ssm.edit")));
    expect(input("my-profile").value).toBe("prod");
    expect(input("https://my-company.awsapps.com/start").value).toBe("https://example.awsapps.com/start");
    expect(input("AdministratorAccess").value).toBe("Dev");
    // The add toggle is gone while a row is being edited: the two forms share one state.
    expect(sections()[0].textContent).not.toContain(t("ssm.add_profile"));

    await type(input("AdministratorAccess"), " Admin ");
    await type(input("https://my-company.awsapps.com/start"), "https://example.awsapps.com/start/#/console");
    gets.length = 0;
    await click(btn(sections()[0], t("common.save")));
    expect(writes).toEqual([
      {
        path: "api/ssm/profiles/p1",
        method: "PUT",
        body: {
          label: "prod",
          startUrl: "https://example.awsapps.com/start/#/console",
          ssoRegion: "ap-northeast-1",
          accountId: "123456789012",
          roleName: "Admin",
          region: "",
        },
      },
    ]);
    expect(host.querySelector(".ssm-frm")).toBeNull();
    expect(gets).toContain("api/ssm/profiles");
  });

  it("keeps Save off while a required field is invalid, and keeps the form when the CP refuses", async () => {
    await mount();
    await click(btn(rows(0)[0], t("ssm.edit")));
    const save = () => btn(sections()[0], t("common.save"));
    await type(input("https://my-company.awsapps.com/start"), "http://example.awsapps.com/start");
    expect(save().disabled).toBe(true);
    await type(input("https://my-company.awsapps.com/start"), "https://example.awsapps.com/start");
    await type(input("my-profile"), "   ");
    expect(save().disabled).toBe(true);
    await type(input("my-profile"), "prod");
    expect(save().disabled).toBe(false);

    writeReply = { ok: false, status: 400, body: { error: { code: "bad_region", message: "ssoRegion is required" } } };
    await click(save());
    expect(writes.map((w) => w.method)).toEqual(["PUT"]);
    expect(toasts).toEqual([t("ssm.save_failed_http", { status: 400, detail: " — ssoRegion is required" })]);
    expect(input("my-profile").value).toBe("prod");
  });

  it("cancel writes nothing and the next Edit starts from the saved row again", async () => {
    await mount();
    await click(btn(rows(0)[0], t("ssm.edit")));
    await type(input("AdministratorAccess"), "Changed");
    await click(btn(sections()[0], t("common.cancel")));
    expect(writes).toEqual([]);
    expect(host.querySelector(".ssm-frm")).toBeNull();
    expect(rows(0)[0].textContent).toContain("Dev");
    await click(btn(rows(0)[0], t("ssm.edit")));
    expect(input("AdministratorAccess").value).toBe("Dev");
  });

  it("warns when the edit renames the workspace profile or moves it to another portal", async () => {
    await mount();
    await click(btn(rows(0)[0], t("ssm.edit")));
    const warn = () => host.querySelector(".ssm-edit-warn")?.textContent ?? null;
    expect(warn()).toBeNull();
    await type(input("my-profile"), "prod app");
    expect(warn()).toBe(t("ssm.edit_rename_warn", { from: "prod", to: "prod-app" }));
    await type(input("my-profile"), "prod");
    await type(input("https://my-company.awsapps.com/start"), "https://other.awsapps.com/start");
    expect(warn()).toBe(t("ssm.edit_portal_warn"));
  });

  it("names the hosts a delete would strand", async () => {
    await mount();
    await click(btn(rows(0)[0], t("common.delete")));
    await click(btn(rows(0)[1], t("common.delete")));
    expect(confirms.map((c) => c.body)).toEqual([
      t("ssm.profile_del_body_hosts", { n: 1 }),
      t("ssm.profile_del_body"),
    ]);
  });
});

describe("SsmTab host Edit", () => {
  it("saves the edited host through PUT, including a new profile", async () => {
    await mount();
    await click(btn(rows(1)[0], t("ssm.edit")));
    const sel = host.querySelector<HTMLSelectElement>(".ssm-frm select")!;
    expect(sel.value).toBe("p1");
    expect(input("admin@web-01").value).toBe("web-01");
    await type(sel, "p2");
    await type(input("i-0123456789abcdef0"), "i-0fedcba9876543210");
    await click(btn(sections()[1], t("common.save")));
    expect(writes).toEqual([
      {
        path: "api/ssm/hosts/h1",
        method: "PUT",
        body: { alias: "web-01", profileId: "p2", instanceId: "i-0fedcba9876543210", documentName: "", region: "" },
      },
    ]);
    expect(host.querySelector(".ssm-frm")).toBeNull();
  });

  it("marks a host whose profile is gone and makes its edit pick one before saving", async () => {
    hosts = [{ ...web, profileId: "deleted" }];
    await mount();
    expect(rows(1)[0].querySelector(".ssm-missing")?.textContent).toBe(t("ssm.profile_missing"));
    await click(btn(rows(1)[0], t("ssm.edit")));
    const save = () => btn(sections()[1], t("common.save"));
    expect(host.querySelector<HTMLSelectElement>(".ssm-frm select")!.value).toBe("");
    expect(save().disabled).toBe(true);
    await type(host.querySelector<HTMLSelectElement>(".ssm-frm select")!, "p1");
    await click(save());
    expect(writes[0].body.profileId).toBe("p1");
  });

  it("cancel writes nothing", async () => {
    await mount();
    await click(btn(rows(1)[0], t("ssm.edit")));
    await type(input("admin@web-01"), "renamed");
    await click(btn(sections()[1], t("common.cancel")));
    expect(writes).toEqual([]);
    expect(rows(1)[0].textContent).toContain("web-01");
  });
});

// Lets pending promises (a held write, a reload) settle inside act.
async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) await act(async () => {});
}

const disabled = (el: Element | null) => !!el && el.matches(":disabled");

describe("SsmTab edit state stays consistent", () => {
  it("deleting the profile being edited closes its form and brings Add back", async () => {
    profiles = [prod];
    hosts = [];
    confirmAnswer = true;
    await mount();
    await click(btn(rows(0)[0], t("ssm.edit")));
    await click(btn(rows(0)[0], t("common.delete")));
    await settle();
    expect(rows(0)).toEqual([]);
    expect(host.querySelector(".ssm-frm")).toBeNull();
    btn(sections()[0], t("ssm.add_profile"));
  });

  it("deleting the host being edited closes its form and brings Add back", async () => {
    confirmAnswer = true;
    await mount();
    await click(btn(rows(1)[0], t("ssm.edit")));
    await click(btn(rows(1)[0], t("common.delete")));
    await settle();
    expect(rows(1)).toEqual([]);
    expect(host.querySelector(".ssm-frm")).toBeNull();
    btn(sections()[1], t("ssm.add_host"));
  });

  it("locks the profile form and the other rows while a PUT is in flight", async () => {
    await mount();
    await click(btn(rows(0)[0], t("ssm.edit")));
    await type(input("AdministratorAccess"), "Admin");
    hold = { release: () => {} };
    await click(btn(sections()[0], t("common.save")));
    expect(writes.length).toBe(1);
    expect(disabled(input("AdministratorAccess"))).toBe(true);
    expect(btn(sections()[0], t("common.cancel")).disabled).toBe(true);
    expect(btn(rows(0)[1], t("ssm.edit")).disabled).toBe(true);
    expect(btn(rows(0)[1], t("common.delete")).disabled).toBe(true);
    expect(btn(rows(0)[0], t("common.delete")).disabled).toBe(true);
    // A login begun now would be for the portal the CP still holds.
    expect(btn(rows(0)[0], t("ssm.login")).disabled).toBe(true);
    expect(btn(rows(0)[1], t("ssm.login")).disabled).toBe(true);
    // A click on a disabled control does nothing: no second form, no delete prompt.
    await click(btn(rows(0)[1], t("ssm.edit")));
    await click(btn(rows(0)[1], t("common.delete")));
    expect(confirms).toEqual([]);
    expect(input("AdministratorAccess").value).toBe("Admin");
    hold.release();
    await settle();
    expect(host.querySelector(".ssm-frm")).toBeNull();
    expect(btn(rows(0)[1], t("ssm.edit")).disabled).toBe(false);
  });

  it("locks the host form and the other rows while a PUT is in flight", async () => {
    hosts = [web, { ...web, id: "h2", alias: "web-02" }];
    await mount();
    await click(btn(rows(1)[0], t("ssm.edit")));
    hold = { release: () => {} };
    await click(btn(sections()[1], t("common.save")));
    expect(disabled(input("admin@web-01"))).toBe(true);
    expect(disabled(host.querySelector(".ssm-frm select"))).toBe(true);
    expect(btn(sections()[1], t("common.cancel")).disabled).toBe(true);
    expect(btn(rows(1)[1], t("ssm.edit")).disabled).toBe(true);
    expect(btn(rows(1)[1], t("common.delete")).disabled).toBe(true);
    hold.release();
    await settle();
    expect(host.querySelector(".ssm-frm")).toBeNull();
  });

  it("drops a host's picked profile when that profile is deleted while the form is open", async () => {
    confirmAnswer = true;
    await mount();
    await click(btn(rows(1)[0], t("ssm.edit")));
    expect(host.querySelector<HTMLSelectElement>(".ssm-frm select")!.value).toBe("p1");
    await click(btn(rows(0)[0], t("common.delete")));
    await settle();
    expect(host.querySelector<HTMLSelectElement>(".ssm-frm select")!.value).toBe("");
    expect(btn(sections()[1], t("common.save")).disabled).toBe(true);
  });

  it("keeps a host's Save off until the profile list has loaded", async () => {
    let open = () => {};
    profilesGate = new Promise<void>((r) => (open = r));
    await mount();
    await click(btn(rows(1)[0], t("ssm.edit")));
    expect(btn(sections()[1], t("common.save")).disabled).toBe(true);
    open();
    await settle();
    expect(host.querySelector<HTMLSelectElement>(".ssm-frm select")!.value).toBe("p1");
    expect(btn(sections()[1], t("common.save")).disabled).toBe(false);
  });
});

describe("SsmTab delete while editing", () => {
  it("holds the profile section still while a DELETE is out, then closes only the deleted row's form", async () => {
    confirmAnswer = true;
    await mount();
    await click(btn(rows(0)[0], t("ssm.edit")));
    delHold = { release: () => {} };
    await click(btn(rows(0)[0], t("common.delete")));
    expect(btn(rows(0)[1], t("ssm.edit")).disabled).toBe(true);
    expect(btn(rows(0)[1], t("common.delete")).disabled).toBe(true);
    expect(btn(rows(0)[1], t("ssm.login")).disabled).toBe(true);
    expect(disabled(input("my-profile"))).toBe(true);
    await click(btn(rows(0)[1], t("ssm.edit")));
    expect(input("my-profile").value).toBe("prod");
    delHold.release();
    await settle();
    expect(rows(0).map((r) => r.querySelector(".ssm-alias")?.textContent)).toEqual(["stg"]);
    expect(host.querySelector(".ssm-frm")).toBeNull();
    // Now B can be edited, and nothing from A's delete closes it.
    await click(btn(rows(0)[0], t("ssm.edit")));
    await type(input("AdministratorAccess"), "Draft");
    await settle();
    expect(input("AdministratorAccess").value).toBe("Draft");
  });

  it("holds the host section still while a DELETE is out", async () => {
    confirmAnswer = true;
    hosts = [web, { ...web, id: "h2", alias: "web-02" }];
    await mount();
    await click(btn(rows(1)[0], t("ssm.edit")));
    delHold = { release: () => {} };
    await click(btn(rows(1)[0], t("common.delete")));
    expect(btn(rows(1)[1], t("ssm.edit")).disabled).toBe(true);
    expect(btn(rows(1)[1], t("common.delete")).disabled).toBe(true);
    expect(disabled(input("admin@web-01"))).toBe(true);
    delHold.release();
    await settle();
    expect(rows(1).map((r) => r.querySelector(".ssm-alias")?.textContent)).toEqual(["web-02"]);
    expect(host.querySelector(".ssm-frm")).toBeNull();
    await click(btn(rows(1)[0], t("ssm.edit")));
    await type(input("admin@web-01"), "draft");
    await settle();
    expect(input("admin@web-01").value).toBe("draft");
  });

  it("a refused profile DELETE keeps the row, its draft and its relogin mark, and says why", async () => {
    confirmAnswer = true;
    await mount();
    await click(btn(rows(0)[0], t("ssm.edit")));
    await type(input("https://my-company.awsapps.com/start"), "https://other.awsapps.com/start");
    await click(btn(sections()[0], t("common.save")));
    await settle();
    expect(rows(0)[0].querySelector(".ssm-login-state")?.textContent).toBe(t("ssm.state_relogin"));
    await click(btn(rows(0)[0], t("ssm.edit")));
    await type(input("AdministratorAccess"), "Draft");
    delReply = { ok: false, status: 500, body: { error: { code: "internal", message: "db down" } } };
    await click(btn(rows(0)[0], t("common.delete")));
    await settle();
    expect(toasts).toEqual([t("ssm.delete_failed_http", { status: 500, detail: " — db down" })]);
    expect(input("AdministratorAccess").value).toBe("Draft");
    expect(disabled(input("AdministratorAccess"))).toBe(false);
    expect(rows(0)[0].querySelector(".ssm-login-state")?.textContent).toBe(t("ssm.state_relogin"));
  });

  it("a refused host DELETE keeps the row and its draft, and says why", async () => {
    confirmAnswer = true;
    await mount();
    await click(btn(rows(1)[0], t("ssm.edit")));
    await type(input("admin@web-01"), "draft");
    delReply = { ok: false, status: 403, body: null };
    await click(btn(rows(1)[0], t("common.delete")));
    await settle();
    expect(toasts).toEqual([t("ssm.delete_failed_http", { status: 403, detail: "" })]);
    expect(input("admin@web-01").value).toBe("draft");
    expect(btn(sections()[1], t("common.cancel")).disabled).toBe(false);
  });
});

describe("SsmTab login badge after an edit", () => {
  const badge = (i: number) => rows(0)[i].querySelector(".ssm-login-state");

  it("says to log in again after a rename, even while the Agent still reports the old name", async () => {
    states = [{ name: "prod", state: "signed_in" }];
    await mount();
    await click(btn(rows(0)[0], t("ssm.edit")));
    await type(input("my-profile"), "prod app");
    profiles = [{ ...prod, label: "prod app", name: "prod-app" }, stg];
    await click(btn(sections()[0], t("common.save")));
    await settle();
    expect(badge(0)?.textContent).toBe(t("ssm.state_relogin"));
    // Closing and reopening Settings before the next poll keeps it.
    await act(async () => root!.unmount());
    root = createRoot(host);
    await mount();
    expect(badge(0)?.textContent).toBe(t("ssm.state_relogin"));
  });

  it("does not show the old portal's Signed in after a portal edit, until the row logs in", async () => {
    states = [{ name: "prod", state: "signed_in" }];
    await mount();
    expect(badge(0)?.textContent).toBe(t("ssm.state_signed_in"));
    await click(btn(rows(0)[0], t("ssm.edit")));
    await type(input("https://my-company.awsapps.com/start"), "https://other.awsapps.com/start");
    await click(btn(sections()[0], t("common.save")));
    await settle();
    expect(badge(0)?.textContent).toBe(t("ssm.state_relogin"));

    await click(btn(rows(0)[0], t("ssm.login")));
    const modal = document.body.querySelector<HTMLElement>(".ui-modal")!;
    await click(btn(modal, t("awslogin.start")));
    // The attempt's first poll is 300 ms after the start.
    for (let i = 0; i < 20 && !modal.textContent?.includes(t("awslogin.profile_done")); i++) {
      await act(async () => new Promise((r) => setTimeout(r, 100)));
    }
    expect(modal.textContent).toContain(t("awslogin.profile_done"));
    await click(btn(modal, t("awslogin.close")));
    await settle();
    expect(badge(0)?.textContent).toBe(t("ssm.state_signed_in"));
  });

  it("an edit that keeps the name and portal leaves the badge alone", async () => {
    states = [{ name: "prod", state: "signed_in" }];
    await mount();
    await click(btn(rows(0)[0], t("ssm.edit")));
    await type(input("AdministratorAccess"), "Admin");
    await click(btn(sections()[0], t("common.save")));
    await settle();
    expect(badge(0)?.textContent).toBe(t("ssm.state_signed_in"));
  });
});

describe("awsProfileName", () => {
  // Must match the CP's ssmProfileName (control-plane/ssm.go), or the rename warning lies.
  it("maps labels the way the CP does", () => {
    expect(awsProfileName(" prod app ")).toBe("prod-app");
    expect(awsProfileName("a/b:c")).toBe("a-b-c");
    expect(awsProfileName("x.y_z@w-1")).toBe("x.y_z@w-1");
    expect(awsProfileName("日本")).toBe("-");
    expect(awsProfileName("   ")).toBe("ssm");
  });
});
