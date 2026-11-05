// The Admin Egress allowlist (control-plane/egress.go allowlistList). The CP's own hosts arrive
// as `builtin`: always admitted by the served policy, so they are listed without a Retire button.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { t } from "../../../lib/i18n/index.ts";
import { EgressView } from "./adminEgress.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function serve(allowlist: Record<string, unknown>) {
  api.mockImplementation(async (path: string) =>
    path.startsWith("api/admin/egress/allowlist")
      ? allowlist
      : { egress: [], days: 7, mode: "enforce", enforce: true },
  );
}

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<EgressView />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const rows = () => [...host!.querySelectorAll<HTMLElement>(".adm-allow-row")];

beforeEach(() => {
  api.mockReset();
  apiJSON.mockReset();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("EgressView allowlist", () => {
  it("lists the CP's own hosts as built-in, with no Retire button", async () => {
    serve({
      allowlist: [{ id: "a1", entry: "extra.example.org", state: "active", reason: "", added_by: "admin" }],
      defaults: ["github.com"],
      builtin: ["console.example.test"],
    });
    await mount();
    const [cp, added] = rows();
    expect(cp.textContent).toContain("console.example.test");
    expect(cp.textContent).toContain(t("admin.egress_builtin"));
    expect(cp.querySelector("button")).toBeNull();
    expect(added.textContent).toContain("extra.example.org");
    expect(added.querySelector("button")?.textContent).toBe(t("admin.retire"));
  });

  it("shows the no-entries note next to the built-in rows, and copes with builtin: null", async () => {
    serve({ allowlist: [], defaults: [], builtin: null });
    await mount();
    expect(rows()).toHaveLength(0);
    expect(host!.textContent).toContain(t("admin.egress_no_entries"));
  });
});

// TCP passthrough entries (ADR 0122, addendum 2026-11-05).
describe("EgressView TCP entries", () => {
  const input = (label: string) => host!.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`)!;
  const type = async (el: HTMLInputElement, v: string) => {
    const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    await act(async () => {
      set.call(el, v);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });
  };
  const tcpTab = async () => {
    const b = [...host!.querySelectorAll<HTMLButtonElement>(".egress-add .seg-btn")].find(
      (x) => x.textContent === t("admin.egress_kind_tcp"),
    )!;
    await act(async () => b.click());
  };
  const submit = () => host!.querySelector<HTMLButtonElement>('.egress-add button[type="submit"]')!;

  it("lists a tcp entry as host:ports with a tag, and a host entry unchanged", async () => {
    serve({
      allowlist: [
        { id: "a1", entry: "tcp:db.example.com:11000-11999", state: "active", reason: "", added_by: "admin" },
        { id: "a2", entry: "extra.example.org", state: "active", reason: "", added_by: "admin" },
      ],
      builtin: [],
    });
    await mount();
    const [tcp, plain] = rows();
    expect(tcp.querySelector(".egress-tcp-tag")?.textContent).toBe(t("admin.egress_tcp_tag"));
    expect(tcp.textContent).toContain("db.example.com:11000-11999");
    expect(tcp.querySelector(".as-name")?.getAttribute("title")).toBe("tcp:db.example.com:11000-11999");
    expect(plain.querySelector(".egress-tcp-tag")).toBeNull();
  });

  it("flags a proposed tcp entry as opening non-HTTP access, and not a proposed host", async () => {
    serve({
      allowlist: [
        { id: "p1", entry: "tcp:git.example.com:22", state: "proposed", reason: "ssh git", added_by: "member" },
        { id: "p2", entry: "api.example.org", state: "proposed", reason: "", added_by: "member" },
      ],
      builtin: [],
    });
    await mount();
    const flags = [...host!.querySelectorAll(".egress-tcp-flag")];
    expect(flags).toHaveLength(1);
    expect(flags[0].textContent).toBe(t("admin.egress_tcp_flag", { host: "git.example.com", ports: "22" }));
  });

  it("posts the canonical tcp entry from the TCP form", async () => {
    serve({ allowlist: [], builtin: [] });
    apiJSON.mockResolvedValue({ id: "n1" });
    await mount();
    await tcpTab();
    expect(submit().disabled).toBe(true);
    await type(input(t("admin.egress_tcp_host_ph")), " Git.Example.com. ");
    await type(input(t("admin.egress_tcp_ports_ph")), "22");
    expect(host!.querySelector(".egress-tcp-err")).toBeNull();
    expect(submit().disabled).toBe(false);
    await act(async () => submit().click());
    expect(apiJSON).toHaveBeenCalledWith("api/admin/egress/allowlist", "POST", { entry: "tcp:git.example.com:22", reason: "" });
  });

  it("explains an invalid destination with the CP's rule and does not send it", async () => {
    serve({ allowlist: [], builtin: [] });
    await mount();
    await tcpTab();
    await type(input(t("admin.egress_tcp_host_ph")), "db.example.com");
    await type(input(t("admin.egress_tcp_ports_ph")), "400-500");
    expect(host!.querySelector(".egress-tcp-err")?.textContent).toBe(t("admin.egress_tcp_err_refused", { port: 443 }));
    expect(submit().disabled).toBe(true);
    await type(input(t("admin.egress_tcp_ports_ph")), "1-5000");
    expect(host!.querySelector(".egress-tcp-err")?.textContent).toBe(t("admin.egress_tcp_err_width", { n: 2048 }));
    await type(input(t("admin.egress_tcp_host_ph")), "*.example.com");
    await type(input(t("admin.egress_tcp_ports_ph")), "22");
    expect(host!.querySelector(".egress-tcp-err")?.textContent).toBe(t("admin.egress_tcp_err_host"));
    expect(apiJSON).not.toHaveBeenCalled();
  });

  it("shows the CP's refusal when the add fails", async () => {
    serve({ allowlist: [], builtin: [] });
    apiJSON.mockResolvedValue({ error: "entry_invalid" });
    await mount();
    await tcpTab();
    await type(input(t("admin.egress_tcp_host_ph")), "git.example.com");
    await type(input(t("admin.egress_tcp_ports_ph")), "22");
    await act(async () => submit().click());
    expect(host!.querySelector(".egress-add-err")?.textContent).toContain(t("admin.egress_add_failed", { msg: "" }).replace(/\s*$/, ""));
  });
});
