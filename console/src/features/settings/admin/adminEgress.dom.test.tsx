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
