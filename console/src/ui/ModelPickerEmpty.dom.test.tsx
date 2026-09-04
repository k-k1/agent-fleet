// A dynamic kind's picker must not stay silent about having no selectable model at all.
//
// Damage: when the catalog comes back empty, the picker shows only "default" and gives no
// reason. That is what was reported on a real machine as "I can't pick a model", and the
// investigation mis-diagnosed it as a Console regression (a 200 whose body is
// {"models":[]} paints the same picture).
//
// The note must not state a cause. Empty can mean not signed in, authenticated but the
// provider is unreachable, a plan that only has the default (Copilot Free offers Auto only,
// so empty is normal), or everything excluded in settings — and the Console cannot tell
// which. The test for the wording is that it stays true on a Copilot Free screen.
//
// It must not appear while the fetch is in flight: useModelOptions returns only "default"
// until it resolves, so without checking `settled` the note always flashes on open.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let respond: (body: unknown) => void = () => {};
const api = vi.fn(
  (_path: string) =>
    new Promise((resolve) => {
      respond = resolve;
    }),
);
vi.mock("../core/api/client.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return { ...real, api: (path: string) => api(path) };
});

const { ModelPicker } = await import("./ModelPicker.tsx");
const { t } = await import("../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement;

const hints = () => [...host.querySelectorAll(".ui-field-hint")].map((n) => n.textContent || "");
const optionValues = () => [...host.querySelectorAll("option")].map((o) => (o as HTMLOptionElement).value);

async function mount(kind: string) {
  await act(async () => {
    root!.render(<ModelPicker kind={kind} model="" onChange={() => {}} />);
  });
}

async function settle(body: unknown) {
  await act(async () => {
    respond(body);
    await Promise.resolve();
    await Promise.resolve();
  });
}

beforeEach(() => {
  api.mockClear();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  root = null;
});

describe("dynamic model picker's default-only note", () => {
  it("is not shown while fetching (default-only looks identical to loading)", async () => {
    await mount("cursor");
    expect(optionValues()).toEqual([""]); // still only "default"
    expect(hints().join()).not.toContain(t("ui.model_default_only"));
  });

  it("is shown once the fetch settles on an empty catalog", async () => {
    await mount("kiro");
    await settle({ models: [] });
    expect(optionValues()).toEqual([""]);
    expect(hints().join()).toContain(t("ui.model_default_only"));
  });

  it("is not shown once models arrive", async () => {
    await mount("agy");
    await settle({ models: [{ id: "sonnet-x", label: "Sonnet X" }] });
    expect(optionValues()).toContain("sonnet-x");
    expect(hints().join()).not.toContain(t("ui.model_default_only"));
  });

  // Read both catalogues themselves: t() only ever returns the current display language, so
  // rewriting just one of them into an assertive form would go unnoticed.
  it("states no cause in either ja or en (it also shows on Copilot Free's empty catalog)", async () => {
    const ja = (await import("../lib/i18n/locales/ja/common.ts")).common;
    const en = (await import("../lib/i18n/locales/en/common.ts")).common;
    for (const cat of [ja, en] as Record<string, string>[]) {
      const s = cat["ui.model_default_only"];
      expect(s).toBeTruthy();
      // An assertive rewrite would misreport to accounts whose plan is default-only (where
      // an empty catalog is normal).
      expect(s).not.toMatch(/ログインしていません|未ログイン|接続されていません|not signed in|sign in|not connected|failed/i);
    }
  });
});
