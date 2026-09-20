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
}));

const { AiAssistTab } = await import("./AiAssistTab.tsx");
const { setSetting } = await import("../../../lib/settings.ts");

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
});

describe("AiAssistTab per-feature cards", () => {
  it("renders one card per feature, under the usage view's own label", async () => {
    await render();
    for (const f of AI_ASSIST_FEATURES) {
      expect(cardFor(f.id)).toBeTruthy();
    }
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
    expect(card.textContent).toContain(t("aiassist.currently_using", { agent: "Codex" }));
  });

  it("shows 'not yet known' rather than a guess when the resolution has not answered yet", async () => {
    setSetting("autoTitleSuggest", true);
    await render();
    const card = cardFor("title.session");
    expect(card.textContent).toContain(t("aiassist.currently_using_unknown"));
  });
});
