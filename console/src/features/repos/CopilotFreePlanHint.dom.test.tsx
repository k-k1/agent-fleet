// The pre-start hint for a Free copilot plan: shown only for a detected Free plan with a concrete
// model, never for an unknown plan, a paid plan, another kind, or Auto.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let reading: Record<string, unknown> | null = null;
vi.mock("../../app/wsbar/usageCommon.tsx", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return { ...real, useUsage: () => ({ usage: reading, refreshing: false, refresh: () => {} }) };
});

const { CopilotFreePlanHint } = await import("./CopilotFreePlanHint.tsx");
const { t } = await import("../../lib/i18n/index.ts");

let root: Root;
let host: HTMLDivElement;

async function show(kind: string, model: string) {
  await act(async () => root.render(<CopilotFreePlanHint kind={kind} model={model} />));
  return host.textContent || "";
}

beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  reading = null;
});

describe("CopilotFreePlanHint", () => {
  it("says Free allows Auto only when the plan is Free and a concrete model is set", async () => {
    reading = { ok: true, authed: true, plan: "Free" };
    expect(await show("copilot", "claude-sonnet-4.6")).toBe(t("launch.copilot_free_model"));
  });

  it.each(["", "auto"])("stays silent for the default/Auto model %j", async (model) => {
    reading = { ok: true, authed: true, plan: "Free" };
    expect(await show("copilot", model)).toBe("");
  });

  it.each([
    ["a paid plan", { ok: true, authed: true, plan: "Pro" }],
    ["an unreadable plan (failed read)", { ok: false, authed: true, error: "boom" }],
    ["not signed in", { ok: false, authed: false }],
    ["no reading yet", null],
  ])("stays silent for %s", async (_n, r) => {
    reading = r as Record<string, unknown> | null;
    expect(await show("copilot", "claude-sonnet-4.6")).toBe("");
  });

  it("stays silent for another kind even on a Free reading", async () => {
    reading = { ok: true, authed: true, plan: "Free" };
    expect(await show("claude", "opus")).toBe("");
  });
});
