// The redesigned MCP server form (#2550): remote first, display name first with a derived ID,
// the Authentication selector, and the collapsed Advanced section.
import { afterEach, describe, expect, it } from "vitest";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { SecretsForm, ServerForm } from "./mcpServerForm.tsx";
import { McpAdminForm } from "./mcpAdmin.tsx";
import { bodyOf, bodyOfTenant, emptyForm, emptyTenantForm, formOf, tenantFormOf } from "./mcpWire.ts";
import type { Form, McpServer, TenantForm } from "./mcpWire.ts";
import { setLocale } from "../../../lib/i18n/index.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  setLocale("ja");
});

function mount(node: React.ReactNode) {
  setLocale("en");
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root!.render(node));
}

// Hosts the form's state the way McpTab does, and records the latest value.
let latest: Form | null = null;
function mountServerForm(initial: Form) {
  latest = initial;
  function Host() {
    const [f, setF] = useState<Form | null>(initial);
    latest = f;
    return f ? (
      <ServerForm
        form={f}
        setForm={setF}
        onSave={async () => true}
        onTest={async () => {}}
        egress={null}
        onProposed={() => {}}
        submitLabel="save"
      />
    ) : null;
  }
  mount(<Host />);
}

let latestTenant: TenantForm | null = null;
function mountTenantForm(initial: TenantForm) {
  function Host() {
    const [f, setF] = useState<TenantForm | null>(initial);
    latestTenant = f;
    return f ? (
      <McpAdminForm form={f} setForm={setF} busy={false} onSave={async () => {}} egress={null} onProposed={() => {}} />
    ) : null;
  }
  mount(<Host />);
}

// Drives a controlled React input the way a keystroke does.
function type(el: Element, value: string) {
  const input = el as HTMLInputElement;
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  act(() => {
    setter.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const click = (el: Element) => act(() => (el as HTMLElement).click());

const fieldInput = (label: string) =>
  [...host!.querySelectorAll(".ssm-fld")].find((f) => f.querySelector("label")?.textContent?.startsWith(label))!.querySelector("input")!;
const seg = (label: string) => [...host!.querySelectorAll(".seg-btn")].find((b) => b.textContent === label)!;
const details = () => host!.querySelector("details.mcp-adv") as HTMLDetailsElement;

const row = (over: Partial<McpServer> = {}): McpServer => ({
  id: "u9",
  name: "wiki",
  label: "Wiki",
  origin: "user",
  transport: "http",
  url: "https://mcp.example.com/mcp",
  enabled: true,
  targets: { assistant: true, session: true },
  editable: true,
  ready: true,
  ...over,
});

describe("new server form", () => {
  it("is remote first, display name first, https placeholder, Advanced closed", () => {
    mountServerForm(emptyForm());
    const labels = [...host!.querySelectorAll(".ssm-fld > label")].map((l) => l.textContent);
    expect(labels[0]).toBe("Display name");
    expect(labels[1]).toBe("ID*");
    expect([...host!.querySelectorAll(".seg-btn")].slice(0, 2).map((b) => b.textContent)).toEqual([
      "Remote (HTTP)",
      "stdio",
    ]);
    expect(fieldInput("URL").placeholder).toBe("https://");
    expect(details().open).toBe(false);
    expect(host!.querySelector(".mcp-adv-sum")!.textContent).toBe(
      "Assistant, Sessions · Every agent · default timeout",
    );
  });

  it("derives the ID from the display name, then from the URL host, until the ID is edited", () => {
    mountServerForm(emptyForm());
    type(fieldInput("Display name"), "Internal Wiki");
    expect(fieldInput("ID").value).toBe("internal-wiki");
    type(fieldInput("Display name"), "社内ウィキ");
    type(fieldInput("URL"), "https://mcp.example.com/mcp");
    expect(fieldInput("ID").value).toBe("mcp-example-com");
    type(fieldInput("ID"), "mine");
    type(fieldInput("Display name"), "Another");
    type(fieldInput("URL"), "https://other.example.com/mcp");
    expect(fieldInput("ID").value).toBe("mine");
  });

  it("warns on plain http:// to a non-loopback host, not on loopback", () => {
    mountServerForm(emptyForm());
    type(fieldInput("URL"), "http://mcp.example.com/mcp");
    expect(host!.querySelector(".ps-note-warn")).not.toBeNull();
    type(fieldInput("URL"), "http://127.0.0.1:8080/mcp");
    expect(host!.querySelector(".ps-note-warn")).toBeNull();
  });

  it("Bearer mode takes one token field and sends Authorization: Bearer <token>", () => {
    mountServerForm({ ...emptyForm(), url: "https://mcp.example.com/mcp", name: "x", nameEdited: true });
    expect([...host!.querySelectorAll(".seg-btn")].map((b) => b.textContent)).toEqual([
      "Remote (HTTP)",
      "stdio",
      "None",
      "Bearer token",
      "Custom headers",
    ]);
    click(seg("Bearer token"));
    type(fieldInput("Token"), "Bearer abc");
    expect(bodyOf(latest!).headers).toEqual({ Authorization: "Bearer abc" });
    click(seg("None"));
    expect(bodyOf(latest!).headers).toEqual({});
  });

  it("Custom headers shows the key/value editor", () => {
    mountServerForm(emptyForm());
    click(seg("Custom headers"));
    expect(host!.querySelector(".mcp-kv")).not.toBeNull();
  });
});

const addHeader = (k: string, v: string) => {
  const add = [...host!.querySelectorAll("button")].find((b) => b.textContent?.includes("Add header"))!;
  click(add);
  const rows = host!.querySelectorAll(".mcp-kv-row");
  const inputs = rows[rows.length - 1].querySelectorAll("input");
  type(inputs[0], k);
  type(inputs[1], v);
};

describe("switching the authentication mode keeps what was typed", () => {
  it("new: Bearer token -> Custom headers -> extra header keeps Authorization", () => {
    mountServerForm({ ...emptyForm(), url: "https://mcp.example.com/mcp", name: "x", nameEdited: true });
    click(seg("Bearer token"));
    type(fieldInput("Token"), "fresh");
    click(seg("Custom headers"));
    addHeader("X-Team", "ops");
    expect(bodyOf(latest!).headers).toEqual({ Authorization: "Bearer fresh", "X-Team": "ops" });
  });

  it("edit: replacing a stored token, then switching, does not resurrect the old one", () => {
    mountServerForm(formOf(row({ headers: { Authorization: "Bearer ***" } })));
    type(fieldInput("Token"), "fresh");
    click(seg("Custom headers"));
    expect(bodyOf(latest!).headers).toEqual({ Authorization: "Bearer fresh" });
    click(seg("None"));
    expect(bodyOf(latest!).headers).toEqual({});
  });

  it("tenant: Bearer token -> Custom headers keeps the token", () => {
    mountTenantForm(emptyTenantForm());
    click(seg("Bearer token"));
    type(fieldInput("Token"), "fresh");
    click(seg("Custom headers"));
    expect(bodyOfTenant(latestTenant!, "acme").headers).toEqual({ Authorization: "Bearer fresh" });
  });
});

describe("edit form", () => {
  it("opens a stored Bearer token in Bearer mode and keeps it untouched", () => {
    mountServerForm(formOf(row({ headers: { Authorization: "Bearer ***" } })));
    expect(seg("Bearer token").className).toContain("active");
    expect(fieldInput("Token").value).toBe("");
    expect(fieldInput("Token").placeholder).toMatch(/Saved/);
    expect(bodyOf(latest!).headers).toEqual({ Authorization: "Bearer ***" });
    type(fieldInput("Token"), "fresh");
    expect(bodyOf(latest!).headers).toEqual({ Authorization: "Bearer fresh" });
  });

  it("opens a non-Bearer Authorization as Custom headers", () => {
    mountServerForm(formOf(row({ headers: { Authorization: "***" } })));
    expect(seg("Custom headers").className).toContain("active");
    expect(bodyOf(latest!).headers).toEqual({ Authorization: "***" });
  });

  it("keeps the ID when the display name changes", () => {
    mountServerForm(formOf(row()));
    type(fieldInput("Display name"), "Renamed");
    expect(fieldInput("ID").value).toBe("wiki");
  });

  it("opens Advanced when a value is not the default, and shows it in the summary", () => {
    mountServerForm(formOf(row({ kinds: ["claude"], timeoutMs: 5000, targets: { assistant: false, session: true } })));
    expect(details().open).toBe(true);
    expect(host!.querySelector(".mcp-adv-sum")!.textContent).toBe("Sessions · Claude · 5000 ms timeout");
  });

  it("keeps Advanced closed when every value is the default", () => {
    mountServerForm(formOf(row()));
    expect(details().open).toBe(false);
  });

  it("a disabled server opens Advanced and says so", () => {
    mountServerForm(formOf(row({ enabled: false })));
    expect(details().open).toBe(true);
    expect(host!.querySelector(".mcp-adv-sum")!.textContent).toMatch(/disabled$/);
  });
});

describe("SecretsForm (member values for a distributed server)", () => {
  const dist = (headers: Record<string, string>): McpServer =>
    row({ origin: "tenant", userSecret: true, editable: false, headers });
  async function save(s: McpServer, typed: string[]): Promise<Record<string, string>> {
    let sent: Record<string, string> = {};
    mount(
      <SecretsForm
        s={s}
        onSave={async (_s, h) => {
          sent = h;
          return true;
        }}
        onCancel={() => {}}
      />,
    );
    const inputs = [...host!.querySelectorAll<HTMLInputElement>('input[type="password"]')];
    typed.forEach((v, i) => v !== "" && type(inputs[i], v));
    await act(async () => (host!.querySelector("button.primary") as HTMLElement).click());
    return sent;
  }

  it("an unset Authorization takes a bare token and sends it as Bearer", async () => {
    expect(await save(dist({ Authorization: "" }), ["abc"])).toEqual({ Authorization: "Bearer abc" });
  });
  it("a stored Bearer token is kept when untouched and replaced when typed", async () => {
    expect(await save(dist({ Authorization: "Bearer ***" }), [""])).toEqual({ Authorization: "Bearer ***" });
    expect(await save(dist({ Authorization: "Bearer ***" }), ["new"])).toEqual({ Authorization: "Bearer new" });
  });
  it("the saved value can be deleted explicitly, which sends a blank", async () => {
    let sent: Record<string, string> = {};
    mount(
      <SecretsForm
        s={dist({ Authorization: "Bearer ***" })}
        onSave={async (_s, h) => {
          sent = h;
          return true;
        }}
        onCancel={() => {}}
      />,
    );
    const clear = [...host!.querySelectorAll("button")].find((b) => b.textContent === "Delete saved value")!;
    click(clear);
    expect(host!.querySelector<HTMLInputElement>('input[type="password"]')!.disabled).toBe(true);
    await act(async () => (host!.querySelector("button.primary") as HTMLElement).click());
    expect(sent).toEqual({ Authorization: "" });
    // And undoing it keeps the value again.
    click([...host!.querySelectorAll("button")].find((b) => b.textContent === "Keep it")!);
    await act(async () => (host!.querySelector("button.primary") as HTMLElement).click());
    expect(sent).toEqual({ Authorization: "Bearer ***" });
  });
  it("any other header takes the raw value", async () => {
    expect(await save(dist({ "X-Api-Key": "" }), ["k1"])).toEqual({ "X-Api-Key": "k1" });
    expect(await save(dist({ Authorization: "***" }), [""])).toEqual({ Authorization: "***" });
  });
});

describe("tenant distribution form", () => {
  it("has the same shape: display name first, auth selector, Advanced closed", () => {
    mountTenantForm(emptyTenantForm());
    expect(host!.querySelector(".ssm-fld > label")!.textContent).toBe("Display name");
    expect(details().open).toBe(false);
    click(seg("Bearer token"));
    type(fieldInput("Token"), "abc");
    expect(bodyOfTenant(latestTenant!, "acme").headers).toEqual({ Authorization: "Bearer abc" });
  });

  it("with user_secret the Bearer mode distributes the name only", () => {
    mountTenantForm({ ...emptyTenantForm(), userSecret: true });
    click(seg("Bearer token"));
    expect(host!.querySelector('input[type="password"]')).toBeNull();
    expect(bodyOfTenant(latestTenant!, "acme").headers).toEqual({ Authorization: "" });
  });

  it("opens Advanced for a distribution with a non-default value", () => {
    mountTenantForm(tenantFormOf({ id: "t", name: "w", transport: "http", url: "https://e.com", targets: { assistant: true, session: false }, enabled: true }));
    expect(details().open).toBe(true);
  });
});
