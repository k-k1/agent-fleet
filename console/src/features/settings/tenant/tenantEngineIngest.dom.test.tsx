// The operator's grant that lets one tenant take models into the engine catalogue
// (ADR 0072 open question 11, phase P5).
//
// The save handler rewrites the WHOLE limits blob, so a checkbox that is drawn but not sent is
// indistinguishable from one that is sent as false — and the second silently withdraws a grant
// every time somebody edits an idle timeout on this screen. So what is pinned here is the
// request body, both ways, plus the sentence that says the catalogue is still shared: a
// deployment that reads this grant as isolation has been misled by this screen.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: () => Promise.resolve({}),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  rawJSON: () => Promise.resolve(new Response("")),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { TenantLimits } from "./tenantScope.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount(tenant: Record<string, unknown>) {
  apiJSON.mockResolvedValue({ tenant: "acme" });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<TenantLimits slug="acme" tenant={tenant as never} hasPool={false} onChanged={() => {}} />);
  });
}

// The grant's own checkbox, found by the label rather than by position: the group sits next to
// the agent-CLI one, and an index would quietly start asserting about that instead.
function ingestBox(): HTMLInputElement {
  const group = [...(host?.querySelectorAll(".admin-fgroup") || [])].find((g) =>
    (g.textContent || "").includes("取り込"),
  );
  return group?.querySelector("input[type=checkbox]") as HTMLInputElement;
}

async function save() {
  const btn = [...(host?.querySelectorAll("button") || [])].find((b) => b.className.includes("primary"));
  await act(async () => {
    btn?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const body = () => apiJSON.mock.calls.at(-1)?.[2] as Record<string, unknown>;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.clearAllMocks();
});

describe("the model-ingest grant on tenant limits", () => {
  it("reflects a tenant that already has the grant", async () => {
    await mount({ slug: "acme", name: "Acme", allow_engine_ingest: true });
    expect(ingestBox()?.checked).toBe(true);
  });

  it("is off for a tenant that was never granted it", async () => {
    await mount({ slug: "acme", name: "Acme" });
    expect(ingestBox()?.checked).toBe(false);
  });

  it("sends the grant when it is turned on", async () => {
    await mount({ slug: "acme", name: "Acme" });
    await act(async () => {
      ingestBox().click();
    });
    await save();
    expect(body().allow_engine_ingest).toBe(true);
    // The neighbouring gate must not be dragged along: this screen writes the whole blob.
    expect(body().allow_agent_self_update).toBe(false);
  });

  it("carries an existing grant through a save that did not touch it", async () => {
    await mount({ slug: "acme", name: "Acme", allow_engine_ingest: true, allow_agent_self_update: true });
    await save();
    expect(body().allow_engine_ingest).toBe(true);
    expect(body().allow_agent_self_update).toBe(true);
  });

  it("says the catalogue is still shared, so the grant is not read as isolation", async () => {
    await mount({ slug: "acme", name: "Acme" });
    expect(host?.textContent || "").toContain("どのテナントからも見えます");
  });
});
