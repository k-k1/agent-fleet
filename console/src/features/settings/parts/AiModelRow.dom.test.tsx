// useResolvedModelLabel — what "推奨（現在: X）" names. X is the Agent's own answer (the
// `recommended` member of GET /agents/{kind}/models — Issue #972); the Console used to re-derive
// it (recommendedModelId) and the two drifted. These pin that the label follows the Agent, never
// a rule of its own, plus 103-impl-review (a): a recommendation the user hid resolves to
// ui.default, which is what runs.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { t } from "../../../lib/i18n/index.ts";
import { useResolvedModelLabel } from "./aiModelRow.tsx";
import { clearRecommendedModels } from "../../../lib/agentModels.ts";

const apiMock = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  getTenant: () => "",
  getUser: () => "",
  api: (...a: unknown[]) => apiMock(...a),
  apiJSON: vi.fn(),
  raw: vi.fn(async () => new Response("")),
  isTransientErr: () => false,
}));

const { setSetting } = await import("../../../lib/settings.ts");

let root: Root | null = null;
let host: HTMLDivElement;

function Probe({ value }: { value: string | undefined }) {
  const label = useResolvedModelLabel("claude", "short", value);
  return <span data-testid="label">{label}</span>;
}

function MuseProbe({ value }: { value: string | undefined }) {
  const label = useResolvedModelLabel("muse", "chat", value);
  return <span data-testid="label">{label}</span>;
}

async function render(value: string | undefined, Component = Probe): Promise<void> {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<Component value={value} />);
  });
  // The recommendation (and muse's catalogue) arrive through a fetch, so let the promise chain
  // and the effect it schedules settle before reading the label.
  await settle();
}

// settle lets the fetch, its promise chain and the effect it schedules run — under fake timers
// too (the retry tests), where a real setTimeout(0) would never fire.
async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) {
    await act(async () => {
      if (vi.isFakeTimers()) await vi.advanceTimersByTimeAsync(0);
      else await new Promise((r) => setTimeout(r, 0));
    });
  }
}

// What the Agent answers per kind. claude's list is the Console's own (CLAUDE_MODELS), so its
// response is read for `recommended` only; muse's is a live catalogue.
let agentAnswers: Record<string, unknown> = {};

beforeEach(() => {
  localStorage.clear();
  setSetting("hiddenModels", {});
  agentAnswers = {
    claude: { models: [], recommended: { chat: "sonnet", prose: "sonnet", short: "haiku" } },
    muse: {
      models: [
        { id: "muse-spark-1.3-contributor", label: "muse-spark-1.3-contributor" },
        { id: "muse-spark-1.3", label: "Muse Spark 1.3" },
      ],
      recommended: { chat: "muse-spark-1.3", prose: "", short: "" },
    },
  };
  apiMock.mockReset().mockImplementation(async (p: string) => {
    const kind = /^api\/agents\/([^/]+)\/models$/.exec(p)?.[1];
    return kind ? (agentAnswers[kind] ?? null) : null;
  });
});

afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  root = null;
  clearRecommendedModels();
});

describe("useResolvedModelLabel", () => {
  it("resolves the recommended label from the visible catalog when nothing is hidden", async () => {
    await render(undefined); // undefined = nothing set anywhere in the chain, follow "推奨"
    expect(host.textContent).toBe(t("assistant.recommended_now", { model: "Haiku" }));
  });

  it("falls back to the CLI default when the recommended model is hidden, not the raw id", async () => {
    setSetting("hiddenModels", { claude: ["haiku"] });
    // An answer the Agent gave before it saw the new hidden list: the label must not trust it.
    await render(undefined);
    // Must NOT read "推奨（現在: haiku）" — that is the exact bug: showing an excluded id as if
    // it would run, when the Agent's own visibleModel() falls through to the CLI default here.
    expect(host.textContent).not.toContain("haiku");
    expect(host.textContent).toBe(t("assistant.recommended_now", { model: t("ui.default") }));
  });

  it("names whatever the Agent recommends — the Console has no rule of its own", async () => {
    // A model no rule in the Console could have produced: only the Agent's answer can put it here.
    agentAnswers.claude = { models: [], recommended: { chat: "opus", prose: "opus", short: "claude-haiku-5-5" } };
    await render(undefined);
    expect(host.textContent).toBe(t("assistant.recommended_now", { model: "claude-haiku-5-5" }));
  });

  it("reads an empty recommendation as the CLI default", async () => {
    agentAnswers.claude = { models: [], recommended: { chat: "", prose: "", short: "" } };
    await render(undefined);
    expect(host.textContent).toBe(t("assistant.recommended_now", { model: t("ui.default") }));
  });

  it("names no model while the Agent cannot be asked, rather than guess one", async () => {
    vi.useFakeTimers();
    try {
      agentAnswers = {};
      await render(undefined);
      expect(host.textContent).toBe(t("assistant.recommended"));
      // Exhaust the retries inside this test, so none of them lands in the next one.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(60_000);
      });
      expect(host.textContent).toBe(t("assistant.recommended"));
    } finally {
      vi.useRealTimers();
    }
  });

  // #972 review round 2: the answer expires, and a tab left open must pick up the new one
  // without being reopened — the Agent's answer moves with prices and releases.
  it("re-asks the Agent while left open, once the answer has expired", async () => {
    vi.useFakeTimers();
    try {
      await render(undefined);
      expect(host.textContent).toBe(t("assistant.recommended_now", { model: "Haiku" }));
      agentAnswers.claude = { models: [], recommended: { chat: "sonnet", prose: "sonnet", short: "sonnet" } };
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5 * 60 * 1000);
      });
      expect(host.textContent).toBe(t("assistant.recommended_now", { model: "Haiku" }));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(6 * 60 * 1000);
      });
      await settle();
      expect(host.textContent).toBe(t("assistant.recommended_now", { model: "Sonnet" }));
    } finally {
      vi.useRealTimers();
    }
  });

  // A workspace that is still booting answers 502 first; the label must not stay at a bare
  // "推奨" until the settings are reopened (#972 review).
  it("retries while the Agent is not reachable yet, then names its answer", async () => {
    vi.useFakeTimers();
    try {
      const answer = agentAnswers.claude;
      agentAnswers = {};
      await render(undefined);
      expect(host.textContent).toBe(t("assistant.recommended"));
      agentAnswers = { claude: answer };
      await act(async () => {
        await vi.advanceTimersByTimeAsync(2_000);
      });
      expect(host.textContent).toBe(t("assistant.recommended_now", { model: "Haiku" }));
    } finally {
      vi.useRealTimers();
    }
  });

  it("asks again when the hidden list changes, and shows the Agent's new answer", async () => {
    await render(undefined);
    expect(host.textContent).toBe(t("assistant.recommended_now", { model: "Haiku" }));
    agentAnswers.claude = { models: [], recommended: { chat: "sonnet", prose: "sonnet", short: "sonnet" } };
    await act(async () => {
      setSetting("hiddenModels", { claude: ["haiku"] });
    });
    await settle();
    expect(host.textContent).toBe(t("assistant.recommended_now", { model: "Sonnet" }));
  });

  it("resolves an explicitly configured model to its own catalog label", async () => {
    await render("sonnet");
    expect(host.textContent).toBe("Sonnet");
  });

  // 103-final-review 中3: "" is not "nothing set" — it is something in the chain explicitly
  // picking the CLI's own default (assistantModelPref's `ok` marks this the same way on the
  // Agent side). Collapsing it into the 推奨 branch made a pinned-but-unconfigured feature card
  // claim "推奨" for a value the Agent never actually falls back to.
  it("resolves an explicit empty value to the CLI default, not to 推奨", async () => {
    await render("");
    expect(host.textContent).toBe(t("ui.default"));
    expect(host.textContent).not.toBe(t("assistant.recommended_now", { model: "Haiku" }));
  });
});

// ADR 0095 decision 6 clamp 8 / P2-21. Muse Code's catalogue carries a "-contributor" twin of
// every model and it is the host's own default. The Agent resolves the safe row
// (muse.SafeDefaultExecModel, reading descriptions as well as ids); the label draws that answer
// with the catalogue's own label.
describe("useResolvedModelLabel for muse", () => {
  it("names the Agent's safe default with its catalogue label", async () => {
    await render(undefined, MuseProbe);
    expect(host.textContent).toBe(t("assistant.recommended_now", { model: "Muse Spark 1.3" }));
    expect(host.textContent).not.toContain("contributor");
  });

  it("still resolves a contributor twin the member picked on purpose", async () => {
    await render("muse-spark-1.3-contributor", MuseProbe);
    expect(host.textContent).toBe("muse-spark-1.3-contributor");
  });
});
