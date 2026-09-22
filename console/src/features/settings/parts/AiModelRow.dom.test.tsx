// useResolvedModelLabel — 103-impl-review (a): the recommended label used to fall back to the
// RAW recommended id when hidden models excluded it from the visible catalog (`|| recommended`
// in aiModelRow.tsx), so a user who hid "haiku" saw "推奨（現在: haiku）" for a feature that
// actually falls through to the CLI default (chat_providers.go's recommendedUtilityModel /
// visibleModel). This pins the fix: a hidden recommendation resolves to ui.default instead.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { t } from "../../../lib/i18n/index.ts";
import { useResolvedModelLabel } from "./aiModelRow.tsx";

// muse's catalogue is fetched (isDynamic), unlike claude's built-in list.
const apiMock = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
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
  // The muse catalogue arrives through a fetch, so let the promise chain and the effect it
  // schedules settle before reading the label.
  for (let i = 0; i < 5; i++) {
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });
  }
}

beforeEach(() => {
  localStorage.clear();
  setSetting("hiddenModels", {});
  // Ordered contributor-FIRST on purpose: the rule is the suffix, not the position, and a
  // recommendation that just took the catalogue's first row would pass with the real order.
  apiMock.mockReset().mockResolvedValue({
    models: [
      { id: "muse-spark-1.3-contributor", label: "muse-spark-1.3-contributor" },
      { id: "muse-spark-1.3", label: "muse-spark-1.3" },
      { id: "muse-spark-1.2", label: "muse-spark-1.2" },
    ],
  });
});

afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  root = null;
});

describe("useResolvedModelLabel", () => {
  it("resolves the recommended label from the visible catalog when nothing is hidden", async () => {
    await render(undefined); // undefined = nothing set anywhere in the chain, follow "推奨"
    expect(host.textContent).toBe(t("assistant.recommended_now", { model: "Haiku" }));
  });

  it("falls back to the CLI default when the recommended model is hidden, not the raw id", async () => {
    setSetting("hiddenModels", { claude: ["haiku"] });
    await render(undefined);
    // Must NOT read "推奨（現在: haiku）" — that is the exact bug: showing an excluded id as if
    // it would run, when the Agent's own visibleModel() falls through to the CLI default here.
    expect(host.textContent).not.toContain("haiku");
    expect(host.textContent).toBe(t("assistant.recommended_now", { model: t("ui.default") }));
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
// every model — same model, same price, except that the vendor may use those conversations to
// improve the product — and it is the host's own default. Measured with a real turn: an exec
// with no --model was recorded against muse-spark-1.3-contributor. The Agent resolves the safe
// row for itself (muse.SafeDefaultExecModel); this is the label that has to agree with it.
describe("useResolvedModelLabel for muse", () => {
  it("recommends the newest model WITHOUT the product-improvement clause", async () => {
    await render(undefined, MuseProbe);
    expect(host.textContent).toBe(t("assistant.recommended_now", { model: "muse-spark-1.3" }));
    expect(host.textContent).not.toContain("contributor");
  });

  it("still resolves a contributor twin the member picked on purpose", async () => {
    await render("muse-spark-1.3-contributor", MuseProbe);
    expect(host.textContent).toBe("muse-spark-1.3-contributor");
  });
});
