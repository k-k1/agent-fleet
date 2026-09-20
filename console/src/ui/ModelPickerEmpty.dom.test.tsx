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
const { MODELS_RETRY_MS } = await import("../lib/agentModels.ts");

let root: Root | null = null;
let host: HTMLDivElement;

const hints = () => [...host.querySelectorAll(".ui-field-hint")].map((n) => n.textContent || "");

// The choices are a popup listbox now, not <option>s, so they only exist while the combobox
// is open — focusing the field is what opens it.
const rowLabels = () => [...host.querySelectorAll('[role="option"]')].map((n) => n.textContent || "");
async function openList() {
  await act(async () => {
    host.querySelector<HTMLInputElement>('input[role="combobox"]')!.focus();
  });
}

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
    await openList();
    expect(rowLabels()).toEqual([t("ui.default")]); // still only "default"
    expect(hints().join()).not.toContain(t("ui.model_default_only"));
  });

  it("is shown once the fetch settles on an empty catalog", async () => {
    await mount("kiro");
    await settle({ models: [] });
    await openList();
    expect(rowLabels()).toEqual([t("ui.default")]);
    expect(hints().join()).toContain(t("ui.model_default_only"));
  });

  it("is not shown once models arrive", async () => {
    await mount("agy");
    await settle({ models: [{ id: "sonnet-x", label: "Sonnet X" }] });
    await openList();
    expect(rowLabels()).toContain("Sonnet X");
    expect(hints().join()).not.toContain(t("ui.model_default_only"));
  });

  // The catalog arrives a moment after the modal opens (the Agent asks the CLI or its
  // daemon). Until now that moment was silent and looked exactly like "this account has no
  // model", so the picker read as broken to anyone who opened and glanced.
  // opencode deliberately, and not one of the kinds above: fetchModels holds a per-kind
  // promise for the life of the module, so a kind an earlier test left mid-flight would be
  // handed that pending promise instead of making the call this test resolves. opencode is
  // the one kind it never caches (its connections change while the Console is open).
  it("says it is loading while the fetch is in flight, and stops once it lands", async () => {
    await mount("opencode");
    expect(hints().join()).toContain(t("ui.model_loading"));
    await settle({ models: [{ id: "opencode-go/glm-5.2", label: "opencode-go/glm-5.2" }] });
    await openList(); // one more flush: `settled` lands a turn after the options do
    expect(hints().join()).not.toContain(t("ui.model_loading"));
  });

  // "Nothing to pick" has three causes that used to print the same sentence. Two of them are
  // this workspace's own settings, and telling someone to check the connection and the plan
  // sent them to look at something that was never wrong.
  it("names the cause the Agent reported: everything excluded in settings", async () => {
    await mount("copilot");
    await settle({ models: [], reason: "hidden" });
    expect(hints().join()).toContain(t("ui.model_none_hidden"));
    expect(hints().join()).not.toContain(t("ui.model_default_only"));
  });

  it("names the cause the Agent reported: the billing choice left nothing", async () => {
    await mount("opencode");
    await settle({ models: [], reason: "route" });
    expect(hints().join()).toContain(t("ui.model_none_route"));
    expect(hints().join()).not.toContain(t("ui.model_default_only"));
  });

  // An Agent older than this Console sends no reason, and an enumeration that came back
  // empty for an unknowable reason sends "catalog_empty". Both keep the cause-free wording.
  // A kind of its own: fetchModels caches a non-empty answer per kind for the life of the
  // module, so reusing one that an earlier test filled would assert against that cache.
  it("falls back to the cause-free note when the Agent does not say", async () => {
    await mount("codex");
    await settle({ models: [], reason: "catalog_empty" });
    expect(hints().join()).toContain(t("ui.model_default_only"));
  });

  // The first seconds after a workspace starts: the Agent is not listening yet and the CP
  // answers 502 (the same window connsRetry exists for). That is not an empty catalog, and
  // saying "check this agent's connection and plan" sends the user to look at two things that
  // are both fine — they only had to wait.
  it("retries a 502 instead of calling it an empty catalog, and says so when it gives up", async () => {
    vi.useFakeTimers();
    try {
      await mount("kiro");
      // Every attempt fails the way a booting workspace does.
      for (let i = 0; i <= MODELS_RETRY_MS.length; i++) {
        await act(async () => {
          respond({ error: { code: "http_502", status: 502 } });
          await Promise.resolve();
        });
        // Still loading, never "only the default model is available".
        expect(hints().join()).not.toContain(t("ui.model_default_only"));
        await act(async () => {
          await vi.advanceTimersByTimeAsync(MODELS_RETRY_MS[i] ?? 0);
        });
      }
      await act(async () => {
        await Promise.resolve();
      });
      expect(hints().join()).toContain(t("ui.model_unreachable"));
      expect(hints().join()).not.toContain(t("ui.model_default_only"));
    } finally {
      vi.useRealTimers();
    }
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
