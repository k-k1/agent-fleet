// The Scopes field of the tenant distribution form (ADR 0146 decision 2): shown for OAuth only,
// saved as a list, and a list the control plane would refuse holds the save back with a warning.
import { afterEach, describe, expect, it } from "vitest";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { McpAdminForm } from "./mcpAdmin.tsx";
import { bodyOfTenant, emptyTenantForm, tenantFormOf } from "./mcpWire.ts";
import type { TenantForm } from "./mcpWire.ts";
import { setLocale } from "../../../lib/i18n/index.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  setLocale("ja");
});

const saved: TenantForm[] = [];
function Harness({ initial }: { initial: TenantForm }) {
  const [form, setForm] = useState<TenantForm | null>(initial);
  return form ? (
    <McpAdminForm form={form} setForm={setForm} busy={false} onSave={async (f) => void saved.push(f)} egress={null} onProposed={() => {}} />
  ) : null;
}

async function mount(initial: TenantForm) {
  setLocale("en");
  saved.length = 0;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(<Harness initial={initial} />));
}
const text = () => host!.textContent || "";
const button = (label: string) =>
  Array.from(host!.querySelectorAll("button")).find((b) => (b.textContent || "").trim() === label) as HTMLButtonElement | undefined;
const click = async (b: HTMLElement | undefined) => {
  expect(b).toBeTruthy();
  await act(async () => b!.dispatchEvent(new MouseEvent("click", { bubbles: true })));
};
const scopesBox = () => host!.querySelector('textarea[aria-label="Scopes"]') as HTMLTextAreaElement | null;
const type = async (el: HTMLTextAreaElement, value: string) => {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
};

describe("tenant form: Scopes", () => {
  const base = { ...emptyTenantForm(), name: "gmail", url: "https://gmailmcp.googleapis.com/mcp/v1self" };

  it("appears only with OAuth and saves the typed scopes as a list", async () => {
    await mount(base);
    expect(scopesBox()).toBeNull();
    await click(button("OAuth"));
    expect(text()).toContain("Scopes");
    await type(scopesBox()!, "https://www.googleapis.com/auth/gmail.readonly\nopenid");
    await click(button("Distribute"));
    expect(saved).toHaveLength(1);
    expect(bodyOfTenant(saved[0], "sales").scopes).toEqual(["https://www.googleapis.com/auth/gmail.readonly", "openid"]);
  });

  it("warns and holds the save back for a scope with a comma", async () => {
    await mount({ ...base, auth: "oauth" });
    await type(scopesBox()!, "read,archive");
    expect(text()).toContain("without quotes, backslashes or commas");
    expect(scopesBox()!.getAttribute("aria-invalid")).toBe("true");
    const hint = document.getElementById(scopesBox()!.getAttribute("aria-describedby")!);
    expect(hint?.textContent).toContain("without quotes, backslashes or commas");
    expect(button("Distribute")!.disabled).toBe(true);
    await type(scopesBox()!, "read archive");
    expect(button("Distribute")!.disabled).toBe(false);
  });
});

describe("tenant form: Tool confirmations", () => {
  const base = { ...emptyTenantForm(), name: "wiki", url: "https://wiki.example/mcp" };
  const confirmationBox = () =>
    Array.from(host!.querySelectorAll<HTMLInputElement>('input[type="checkbox"]'))
      .find((box) => box.labels?.[0]?.textContent?.trim() === "Show confirmation cards in the Console");

  it("starts off and sends the mode only when an OAuth server opts in", async () => {
    await mount(base);
    expect(confirmationBox()).toBeUndefined();
    await click(button("OAuth"));
    expect(confirmationBox()?.checked).toBe(false);
    await click(confirmationBox());
    await click(button("Distribute"));
    expect(bodyOfTenant(saved[0], "sales").elicitation).toBe("relay");

    await click(button("None"));
    expect(confirmationBox()).toBeUndefined();
    await click(button("Distribute"));
    expect(bodyOfTenant(saved[1], "sales")).not.toHaveProperty("elicitation");
  });

  it("restores the saved setting and lets an admin turn it off", async () => {
    await mount(tenantFormOf({
      id: "tenant-wiki", name: base.name, url: base.url, transport: "http", enabled: true,
      targets: { assistant: false, session: true }, auth: "oauth", elicitation: "relay",
    }));
    expect(confirmationBox()?.checked).toBe(true);
    await click(confirmationBox());
    await click(button("Save"));
    expect(bodyOfTenant(saved[0], "sales")).toMatchObject({ auth: "oauth" });
    expect(bodyOfTenant(saved[0], "sales")).not.toHaveProperty("elicitation");
  });
});
