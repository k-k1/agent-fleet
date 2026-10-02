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
    await type(input("asia-northeast1"), "asia-northeast1");
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
});
