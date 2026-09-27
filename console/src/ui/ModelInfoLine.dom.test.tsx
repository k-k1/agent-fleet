// The picker's catalog facts (Issue #1021): what the Agent attaches to a model under ?info=1
// must reach both the combo's rows and the line under the field, and a model the Agent says
// nothing about must get nothing — not an empty "$0 / $0".
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };

const asked: string[] = [];
let answer: unknown = { models: [] };
vi.mock("../core/api/client.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return {
    ...real,
    api: (path: string) => {
      asked.push(path);
      return Promise.resolve(answer);
    },
  };
});

const { ModelPicker } = await import("./ModelPicker.tsx");
const { AiModelRow } = await import("../features/settings/parts/aiModelRow.tsx");
const { t } = await import("../lib/i18n/index.ts");
const { setSetting } = await import("../lib/settings.ts");
const { clearRecommendedModels } = await import("../lib/agentModels.ts");

let root: Root | null = null;
let host: HTMLDivElement;

function Harness({ kind, initial }: { kind: string; initial: string }) {
  const [model, setModel] = useState(initial);
  return <ModelPicker kind={kind} model={model} onChange={setModel} />;
}

async function render(el: React.ReactElement) {
  await act(async () => {
    root!.render(el);
  });
  for (let i = 0; i < 6; i++) await act(async () => Promise.resolve());
}

async function mount(kind: string, initial: string) {
  await render(<Harness kind={kind} initial={initial} />);
  await act(async () => host.querySelector<HTMLInputElement>('input[role="combobox"]')!.focus());
}

const row = (id: string) =>
  [...host.querySelectorAll<HTMLElement>('[role="option"]')].find((r) => r.textContent?.includes(id))!;

beforeEach(() => {
  asked.length = 0;
  clearRecommendedModels();
  g.IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  root = null;
  delete g.IS_REACT_ACT_ENVIRONMENT;
});

describe("ModelInfoLine", () => {
  it("shows price, window and retirement on the rows and under the field", async () => {
    answer = {
      models: [
        {
          id: "gpt-6-luna",
          label: "gpt-6-luna",
          info: { price: { in: 0.1, out: 0.5, cacheRead: 0.01 }, priceFrom: "openai", context: 1050000, released: "2026-09-22" },
        },
        {
          id: "gpt-5.5",
          label: "gpt-5.5",
          info: {
            price: { in: 5, out: 30 },
            retiring: { at: "2026-10-14T19:00:00Z", note: "GPT-5.5 retires on October 14, 2026.", successor: "gpt-5.6-sol" },
          },
        },
        { id: "gpt-reserve", label: "gpt-reserve" },
      ],
    };
    await mount("codex", "gpt-5.5");
    expect(asked.some((p) => p.startsWith("api/agents/codex/models?") && p.includes("info=1"))).toBe(true);

    expect(row("gpt-6-luna").querySelector(".model-combo-meta")?.textContent).toBe("$0.10 / $0.50 · 1.05M");
    expect(row("gpt-6-luna").querySelector(".model-combo-badge")).toBeNull();
    expect(row("gpt-5.5").querySelector(".model-combo-badge")?.textContent).toBe(t("ui.mi_retiring"));
    // Nothing known: no meta at all, rather than an empty or zero price.
    expect(row("gpt-reserve").querySelector(".model-combo-meta")).toBeNull();
    // The rows' bare numbers are explained in the popup itself.
    expect(host.querySelector(".model-combo-legend")?.textContent).toBe(
      t("ui.mi_legend", { label: t("ui.mi_list_price") }),
    );

    const line = host.querySelector(".model-info-line")!;
    expect(line.querySelector(".model-info-price")?.textContent).toContain("$5");
    expect(line.querySelector(".model-info-price")?.getAttribute("title")).toBe(t("ui.mi_list_price_note"));
    const warn = line.querySelector(".model-info-warn")!;
    expect(warn.textContent).toContain("gpt-5.6-sol");
    expect(warn.getAttribute("title")).toBe("GPT-5.5 retires on October 14, 2026.");
  });

  it("keeps a stated free cache read, and omits an unstated one", async () => {
    answer = {
      models: [
        { id: "opencode/hy3-free", label: "hy3", info: { price: { in: 0, out: 0, cacheRead: 0 }, priceFrom: "opencode" } },
        { id: "opencode/glm-5", label: "glm", info: { price: { in: 1, out: 3.2 }, priceFrom: "opencode" } },
      ],
    };
    await mount("opencode", "opencode/hy3-free");
    const price = () => host.querySelector(".model-info-price")?.textContent || "";
    expect(price()).toContain(t("ui.mi_price_cache", { v: "$0" }));
    expect(price()).toContain("opencode");
  });

  it("marks a retiring registered claude id before it is picked, and prices no alias", async () => {
    setSetting("claudeCustomModels", ["claude-opus-4-7"]);
    answer = {
      models: [
        { id: "opus", label: "Opus" },
        { id: "claude-opus-4-7", label: "claude-opus-4-7", info: { price: { in: 5, out: 25 }, priceFrom: "anthropic", deprecated: true } },
      ],
      recommended: { chat: "sonnet", prose: "sonnet", short: "haiku" },
    };
    await render(<Harness kind="claude" initial="opus" />);
    const opts = [...host.querySelectorAll("select option")].map((o) => o.textContent);
    expect(opts).toContain(t("ui.mi_retiring_suffix", { label: "claude-opus-4-7" }));
    expect(host.querySelector(".model-info-line")).toBeNull(); // the alias is selected
    setSetting("claudeCustomModels", []);
  });

  it("describes what a feature's 'follow default' actually runs", async () => {
    answer = {
      models: [{ id: "gpt-6-luna", label: "GPT-6 Luna", info: { price: { in: 0.1, out: 0.5 }, context: 1050000 } }],
      recommended: { chat: "gpt-6-luna", prose: "gpt-6-luna", short: "gpt-6-luna" },
    };
    await render(
      <AiModelRow kind="codex" tier="short" value="__follow__" extraOption={["__follow__", "follow"]} inherited={undefined} onChange={() => {}} />,
    );
    expect(host.querySelector(".model-info-price")?.textContent).toContain("$0.10");
  });

  it("draws no line for a model the Agent says nothing about", async () => {
    answer = { models: [{ id: "auto", label: "Auto" }, { id: "claude-sonnet-4.5", label: "Sonnet" }] };
    await mount("kiro", "claude-sonnet-4.5");
    expect(host.querySelector(".model-info-line")).toBeNull();
    expect(host.querySelector(".model-combo-meta")).toBeNull();
  });
});
