// useResolvedModelLabel — 103-impl-review (a): the recommended label used to fall back to the
// RAW recommended id when hidden models excluded it from the visible catalog (`|| recommended`
// in aiModelRow.tsx), so a user who hid "haiku" saw "推奨（現在: haiku）" for a feature that
// actually falls through to the CLI default (chat_providers.go's recommendedUtilityModel /
// visibleModel). This pins the fix: a hidden recommendation resolves to ui.default instead.
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { t } from "../../../lib/i18n/index.ts";
import { useResolvedModelLabel } from "./aiModelRow.tsx";

const { setSetting } = await import("../../../lib/settings.ts");

let root: Root | null = null;
let host: HTMLDivElement;

function Probe({ value }: { value: string | undefined }) {
  const label = useResolvedModelLabel("claude", "short", value);
  return <span data-testid="label">{label}</span>;
}

async function render(value: string | undefined): Promise<void> {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<Probe value={value} />);
  });
}

beforeEach(() => {
  localStorage.clear();
  setSetting("hiddenModels", {});
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
