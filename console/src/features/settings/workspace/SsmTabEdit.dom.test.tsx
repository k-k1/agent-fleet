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

vi.mock("../../../core/api/client.ts", () => ({
  api: vi.fn(async (path: string) => {
    gets.push(path);
    if (path === "api/ssm/profiles") return profiles;
    if (path === "api/ssm/hosts") return hosts;
    if (path === "api/aws-login/profiles") return { profiles: [] };
    return {};
  }),
  apiJSON: vi.fn(),
  raw: vi.fn(async () => ({ ok: true, status: 204 })),
  rawJSON: vi.fn(async (path: string, method: string, body: Json) => {
    writes.push({ path, method, body });
    return { ok: writeReply.ok, status: writeReply.status, json: async () => writeReply.body };
  }),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => (m: string) => toasts.push(m) }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({
  useConfirm: () => (o: Json) => {
    confirms.push(o);
    return Promise.resolve(false);
  },
}));

const { SsmTab, awsProfileName } = await import("./SsmTab.tsx");
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
    expect(gets).toContain("api/aws-login/profiles");
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
