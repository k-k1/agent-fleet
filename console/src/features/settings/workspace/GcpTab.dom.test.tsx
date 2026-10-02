// Settings → Google Cloud (ADR 0107 decision 1). The page shows the name the CP computed next
// to each label and says why a row is not exported; it never derives either itself, so the
// test serves names a client-side rule would not produce.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

type Json = Record<string, unknown>;
let rows: Json[] = [];
const rawJSON = vi.fn();
const raw = vi.fn();

vi.mock("../../../core/api/client.ts", () => ({
  api: vi.fn(async (path: string) => (path === "api/gcp/profiles" ? rows : {})),
  apiJSON: vi.fn(),
  raw: (...a: unknown[]) => raw(...a),
  rawJSON: (...a: unknown[]) => rawJSON(...a),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => () => Promise.resolve(true) }));

const { GcpTab } = await import("./GcpTab.tsx");
const { t } = await import("../../../lib/i18n/index.ts");

const prod: Json = {
  id: "g1",
  label: "Prod",
  loginMethod: "google",
  project: "my-prod-1",
  quotaProject: "",
  account: "me@example.com",
  region: "asia-northeast1",
  zone: "asia-northeast1-a",
  impersonateServiceAccount: "deployer@my-prod-1.iam.gserviceaccount.com",
  name: "from-the-cp",
  conflict: { reason: "collision", labels: ["Prod", "prod"] },
};
const ja: Json = { id: "g2", label: "本番", loginMethod: "google", project: "my-prod-2", name: "p-0f719b1f" };

let root: Root | null = null;
let host: HTMLDivElement;

async function flush(): Promise<void> {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

async function mount(): Promise<void> {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<GcpTab />);
  });
  await flush();
}

function input(placeholder: string): HTMLInputElement {
  return host.querySelector<HTMLInputElement>(`input[placeholder="${placeholder}"]`)!;
}

async function type(el: HTMLInputElement, value: string): Promise<void> {
  await act(async () => {
    const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    set.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function choose(el: HTMLSelectElement, value: string): Promise<void> {
  await act(async () => {
    el.value = value;
    el.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

// [region, zone] pickers of the open form.
function pickers(): [HTMLSelectElement, HTMLSelectElement] {
  const [region, zone] = host.querySelectorAll<HTMLSelectElement>(".ssm-frm .region-select select");
  return [region, zone];
}

function otherInputs(): HTMLInputElement[] {
  return Array.from(host.querySelectorAll<HTMLInputElement>(".ssm-frm .region-select input"));
}

const optionValues = (el: HTMLSelectElement) => Array.from(el.options, (o) => o.value);

function lastBody(): Json {
  return rawJSON.mock.calls.at(-1)![2] as Json;
}

async function save(): Promise<void> {
  await click(host.querySelector<HTMLButtonElement>(".ssm-frm-foot button.primary")!);
}

async function edit(index: number): Promise<void> {
  await click(button(t("ssm.edit"), host.querySelectorAll(".ssm-item")[index]));
}

async function click(el: Element): Promise<void> {
  await act(async () => {
    (el as HTMLElement).click();
  });
  await flush();
}

function button(text: string, scope: ParentNode = host): HTMLButtonElement {
  return Array.from(scope.querySelectorAll<HTMLButtonElement>("button")).find((b) => b.textContent?.trim().endsWith(text))!;
}

beforeEach(() => {
  rows = [prod, ja];
  rawJSON.mockReset();
  raw.mockReset();
  rawJSON.mockResolvedValue(new Response("{}", { status: 201 }));
  raw.mockResolvedValue(new Response(null, { status: 204 }));
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
});

describe("GcpTab", () => {
  it("shows the CP's name beside each label and why a colliding row is not exported", async () => {
    await mount();
    const items = host.querySelectorAll(".ssm-item");
    expect(items).toHaveLength(2);
    expect(items[0].querySelector(".ssm-alias")?.textContent).toBe("Prod");
    expect(items[0].querySelector(".gcp-name")?.textContent).toBe("from-the-cp");
    expect(items[0].querySelector(".gcp-conflict")?.textContent).toBe(
      t("gcp.conflict", { name: "from-the-cp", labels: "Prod, prod" }),
    );
    expect(items[1].querySelector(".gcp-name")?.textContent).toBe("p-0f719b1f");
    expect(items[1].querySelector(".gcp-conflict")).toBeNull();
    // An empty quota project reads as "same as the project", not as a missing value.
    expect(items[1].textContent).toContain(t("gcp.same_as_project"));
  });

  it("adds a profile with the Google login method and no name of its own", async () => {
    rows = [];
    await mount();
    expect(host.textContent).toContain(t("gcp.empty"));
    await click(button(t("gcp.add_profile")));
    const save = () => Array.from(host.querySelectorAll<HTMLButtonElement>(".ssm-frm-foot button.primary"))[0];
    expect(save().disabled).toBe(true);
    await type(input("prod"), "  Dev ");
    await type(input("my-project-123"), "my-dev-1");
    await choose(pickers()[0], "asia-northeast1");
    expect(save().disabled).toBe(false);
    await click(save());
    expect(rawJSON).toHaveBeenCalledTimes(1);
    const [path, method, body] = rawJSON.mock.calls[0];
    expect([path, method]).toEqual(["api/gcp/profiles", "POST"]);
    expect(body).toEqual({
      loginMethod: "google",
      label: "Dev",
      project: "my-dev-1",
      quotaProject: "",
      account: "",
      impersonateServiceAccount: "",
      region: "asia-northeast1",
      zone: "",
    });
  });

  it("edits a row in place with PUT and deletes with DELETE", async () => {
    await mount();
    const first = host.querySelectorAll(".ssm-item")[0];
    await click(button(t("ssm.edit"), first));
    const label = input("prod");
    expect(label.value).toBe("Prod");
    await type(label, "Prod EU");
    await click(Array.from(host.querySelectorAll<HTMLButtonElement>(".ssm-frm-foot button.primary"))[0]);
    const [path, method, body] = rawJSON.mock.calls[0];
    expect([path, method]).toEqual(["api/gcp/profiles/g1", "PUT"]);
    expect(body).toMatchObject({ label: "Prod EU", project: "my-prod-1", account: "me@example.com" });
    expect(body).not.toHaveProperty("name");

    await click(button(t("common.delete"), host.querySelectorAll(".ssm-item")[1]));
    expect(raw).toHaveBeenCalledWith("api/gcp/profiles/g2", { method: "DELETE" });
  });

  it("preselects a listed region and zone and offers only the region's zones", async () => {
    await mount();
    await edit(0);
    const [region, zone] = pickers();
    expect(region.value).toBe("asia-northeast1");
    expect(zone.value).toBe("asia-northeast1-a");
    expect(otherInputs()).toHaveLength(0);
    expect(region.selectedOptions[0].textContent).toBe(`asia-northeast1 — ${t("gcp.region.asia-northeast1")}`);
    expect(optionValues(zone).slice(1, -1)).toEqual(["asia-northeast1-a", "asia-northeast1-b", "asia-northeast1-c"]);

    // Suffixes are per region, not a fixed a b c.
    await choose(region, "us-central1");
    expect(optionValues(pickers()[1]).slice(1, -1)).toEqual(["us-central1-a", "us-central1-b", "us-central1-c", "us-central1-f"]);
    await choose(pickers()[0], "europe-west1");
    expect(optionValues(pickers()[1]).slice(1, -1)).toEqual(["europe-west1-b", "europe-west1-c", "europe-west1-d"]);

    // No region: nothing but "(not set)" and Other.
    await choose(pickers()[0], "");
    expect(pickers()[1].options).toHaveLength(2);
    expect(pickers()[1].options[0].value).toBe("");
  });

  it("keeps an unlisted region and zone in Other and saves them unchanged", async () => {
    rows = [{ ...prod, region: "us-east7", zone: "us-east7-x" }];
    await mount();
    await edit(0);
    const [region, zone] = pickers();
    expect(region.value).toBe("*other*");
    expect(zone.value).toBe("*other*");
    expect(otherInputs().map((i) => i.value)).toEqual(["us-east7", "us-east7-x"]);
    await save();
    expect(lastBody()).toMatchObject({ region: "us-east7", zone: "us-east7-x" });
  });

  it("keeps a stored zone outside the stored region as stored", async () => {
    rows = [{ ...prod, region: "asia-northeast1", zone: "us-central1-f" }];
    await mount();
    await edit(0);
    expect(pickers()[0].value).toBe("asia-northeast1");
    expect(pickers()[1].value).toBe("*other*");
    await save();
    expect(lastBody()).toMatchObject({ region: "asia-northeast1", zone: "us-central1-f" });
  });

  it("saves (not set) as an empty string", async () => {
    await mount();
    await edit(0);
    await choose(pickers()[1], "");
    await choose(pickers()[0], "");
    await save();
    expect(lastBody()).toMatchObject({ region: "", zone: "" });
  });

  it("clears a zone picked in this edit when the region no longer has it, never the stored one", async () => {
    await mount();
    await edit(0);
    // The stored zone survives a region change: it moves to Other with its value.
    await choose(pickers()[0], "us-central1");
    expect(pickers()[1].value).toBe("*other*");
    expect(otherInputs().map((i) => i.value)).toEqual(["asia-northeast1-a"]);

    // A zone picked from the list goes once its region no longer offers it.
    await choose(pickers()[1], "us-central1-f");
    await choose(pickers()[0], "europe-west1");
    expect(pickers()[1].value).toBe("");
    expect(otherInputs()).toHaveLength(0);

    // A pick in the current region is what gets saved.
    await choose(pickers()[1], "europe-west1-d");
    await save();
    expect(lastBody()).toMatchObject({ region: "europe-west1", zone: "europe-west1-d" });
  });
});
