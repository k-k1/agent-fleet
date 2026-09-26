// AiAssistTab's per-feature cards (docs/log/103 §103.7). What is pinned here:
//   1. All 8 features render, one card each, under the SAME label the usage view uses
//      (decision 6 — one feature, one name, in both places).
//   2. A feature turned off shows no agent/model row (§103.7's "fold the card").
//   3. Left on "auto", the model row offers no catalog pick (decision 4's invariant) — only
//      once a concrete agent is pinned does a real model select appear.
//   4. The "currently uses" line reflects the Agent's answer (GET /ai-assist/resolution),
//      not a Console guess.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { t } from "../../../lib/i18n/index.ts";
import { AI_ASSIST_FEATURES } from "../../../lib/aiAssistFeatures.ts";

const apiMock = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => apiMock(...a),
  apiJSON: vi.fn(),
  raw: vi.fn(async () => new Response("")),
  isTransientErr: () => false,
}));

const { AiAssistTab } = await import("./AiAssistTab.tsx");
const { setSetting } = await import("../../../lib/settings.ts");
const { clearRecommendedModels } = await import("../../../lib/agentModels.ts");

let root: Root | null = null;
let host: HTMLDivElement;

async function render(): Promise<void> {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<AiAssistTab />);
  });
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

const cardFor = (id: string): HTMLElement => {
  const label = t(AI_ASSIST_FEATURES.find((f) => f.id === id)!.labelKey);
  const heading = [...host.querySelectorAll(".ds-label")].find((el) => el.textContent === label);
  return heading!.closest(".ai-feature-card") as HTMLElement;
};

beforeEach(() => {
  localStorage.clear();
  apiMock.mockReset().mockResolvedValue({ features: [] });
  setSetting("aiFeatureAgents", {});
  setSetting("aiFeatureModels", {});
});

afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  root = null;
  clearRecommendedModels();
});

describe("AiAssistTab per-feature cards", () => {
  it("renders one card per feature, under the usage view's own label", async () => {
    await render();
    for (const f of AI_ASSIST_FEATURES) {
      expect(cardFor(f.id)).toBeTruthy();
    }
  });

  // 103-impl-review 重大2: useAiAssistResolution used to be called INSIDE AiFeatureCard, so 8
  // cards fired 8 identical GET /ai-assist/resolution requests per mount (measured: 48 req/min
  // while the tab stayed open at the 10s poll interval) even though the Agent already answers
  // all 8 features in one response. It must be called once, in AiAssistTab, and passed down.
  it("fetches the resolution exactly once per mount, not once per card", async () => {
    await render();
    const calls = apiMock.mock.calls.filter(([p]) => p === "api/ai-assist/resolution");
    expect(calls.length).toBe(1);
  });

  it("hides the agent/model rows when the feature is off", async () => {
    setSetting("autoTitleSuggest", false);
    await render();
    const card = cardFor("title.session");
    expect(card.textContent).not.toContain(t("aiassist.feature_agent"));
  });

  it("offers no catalog model pick while the agent is left on auto", async () => {
    setSetting("autoTitleSuggest", true);
    await render();
    const card = cardFor("title.session");
    expect(card.textContent).toContain(t("aiassist.feature_agent"));
    expect(card.textContent).toContain(t("aiassist.feature_model_auto_note"));
    // No second <select> (the agent picker is the only one) — decision 4's invariant.
    expect(card.querySelectorAll("select").length).toBe(1);
  });

  it("shows a real model select once the feature is pinned to one agent", async () => {
    setSetting("autoTitleSuggest", true);
    setSetting("aiFeatureAgents", { "title.session": "claude" });
    await render();
    const card = cardFor("title.session");
    expect(card.querySelectorAll("select").length).toBe(2);
    expect(card.textContent).not.toContain(t("aiassist.feature_model_auto_note"));
  });

  it("shows the Agent's resolved backend, not a guess, on the currently-using line", async () => {
    apiMock.mockImplementation((p: string) => {
      if (p === "api/ai-assist/resolution") {
        return Promise.resolve({
          features: [{ feature: "title.session", enabled: true, kind: "codex", source: "default" }],
        });
      }
      return Promise.resolve({});
    });
    setSetting("autoTitleSuggest", true);
    await render();
    await act(async () => {
      await Promise.resolve();
    });
    const card = cardFor("title.session");
    // The agent (X) comes from the Agent's own answer; the model (Y) is drawn separately by
    // the Console from its own catalog (decision 5) and checked by the next test instead, so
    // this does not overfit to the exact catalog-derived label text.
    const prefix = t("aiassist.currently_using", { agent: "Codex", model: "" }).replace(/\s*\/\s*$/, "");
    expect(card.textContent).toContain(prefix);
  });

  it("shows 'not yet known' rather than a guess when the resolution has not answered yet", async () => {
    setSetting("autoTitleSuggest", true);
    await render();
    const card = cardFor("title.session");
    expect(card.textContent).toContain(t("aiassist.currently_using_unknown"));
  });

  // 103-impl-review 中9: Y (the model name) is drawn by the Console from its own catalog, not
  // answered by the Agent (decision 5) — pinned to claude with a concrete model chosen, the
  // model's own catalog label must appear on the currently-using line.
  it("draws the pinned model's own label on the currently-using line", async () => {
    apiMock.mockImplementation((p: string) => {
      if (p === "api/ai-assist/resolution") {
        return Promise.resolve({
          features: [{ feature: "title.session", enabled: true, kind: "claude", source: "pin" }],
        });
      }
      return Promise.resolve({});
    });
    setSetting("autoTitleSuggest", true);
    setSetting("aiFeatureAgents", { "title.session": "claude" });
    setSetting("aiFeatureModels", { "title.session": { claude: "haiku" } });
    await render();
    await act(async () => {
      await Promise.resolve();
    });
    const card = cardFor("title.session");
    expect(card.textContent).toContain(t("aiassist.currently_using", { agent: "Claude", model: "Haiku" }));
  });

  // 103-final-review 中3: pinned but with no per-feature override, the currently-using line
  // must show what the Agent actually runs — the §1 tier default for the pinned kind — not
  // "推奨". Before this fix the missing override collapsed to "", which useResolvedModelLabel
  // read as "nothing chosen" and rendered as 推奨 even though a concrete tier default existed.
  it("shows the tier default's own label when pinned with no per-feature override", async () => {
    apiMock.mockImplementation((p: string) => {
      if (p === "api/ai-assist/resolution") {
        return Promise.resolve({
          features: [{ feature: "title.session", enabled: true, kind: "claude", source: "pin" }],
        });
      }
      return Promise.resolve({});
    });
    setSetting("autoTitleSuggest", true);
    setSetting("aiFeatureAgents", { "title.session": "claude" });
    setSetting("aiShortModels", { claude: "opus" });
    await render();
    await act(async () => {
      await Promise.resolve();
    });
    const card = cardFor("title.session");
    expect(card.textContent).toContain(t("aiassist.currently_using", { agent: "Claude", model: "Opus" }));
  });

  // 103-impl-review 中6: the model picker must be able to go back to "follow the setting
  // above" — before this fix there was no such option, and an unset value showed as
  // "Recommended" (a real, different, sticky choice) instead of "nothing chosen here".
  it("offers a way back to the tier default, mapped to clearing the override", async () => {
    setSetting("autoTitleSuggest", true);
    setSetting("aiFeatureAgents", { "title.session": "claude" });
    setSetting("aiFeatureModels", { "title.session": { claude: "haiku" } });
    await render();
    const card = cardFor("title.session");
    const selects = card.querySelectorAll("select");
    const modelSelect = selects[1] as HTMLSelectElement;
    const followOption = [...modelSelect.options].find((o) => o.textContent === t("aiassist.feature_model_follow_default"));
    expect(followOption).toBeTruthy();

    modelSelect.value = followOption!.value;
    await act(async () => {
      modelSelect.dispatchEvent(new Event("change", { bubbles: true }));
    });
    const { getSettings } = await import("../../../lib/settings.ts");
    expect(getSettings().aiFeatureModels?.["title.session"]?.claude).toBeUndefined();
  });
});
