// What only a rendered studio can prove (ADR 0100): the per-field lock is a toggle of its own
// (clicking the field's NAME must not flip it), the agent's outline sits on the fields it moved,
// the edit history folds a version's result into its press and offers "back to this point", and
// "attach an agent" offers the kinds decision 8 names — with the blocked ones saying why.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../repos/useRepoRail.ts", () => ({
  useRepoRailContext: () => ({
    launchKinds: ["claude", "codex", "cursor", "copilot", "kiro", "agy", "opencode", "lcpp", "muse", "shell"],
    connsSettling: false,
  }),
}));
vi.mock("../../ui/ModelPicker.tsx", () => ({
  ModelPicker: () => <select data-testid="model" />,
  EffortPicker: () => <select data-testid="effort" />,
}));

import { GenerateForm } from "./parts/GenerateForm.tsx";
import { DraftLog } from "./parts/DraftLog.tsx";
import { AttachAgentModal } from "./parts/AttachAgentModal.tsx";
import { useReposStore } from "../repos/store.ts";
import { emptyDraft, type ImagegenDraft } from "./draft.ts";
import type { DraftLogEntry, ImagegenModel } from "./wire.ts";
import type { StudioKey } from "./studioSync.ts";

let host: HTMLDivElement;
let root: Root;

const SDXL: ImagegenModel = {
  id: "sdxl-base",
  family: "sdxl",
  knobs: ["steps", "cfg", "sampler", "scheduler", "negative", "strength"],
  ops: ["generate", "edit", "inpaint"],
  dialect: "tags",
  quality_prefixes: ["masterpiece, best quality"],
  steps_range: [20, 40],
  cfg_range: [5, 9],
  trial_steps: 10,
};

const mount = async (node: React.ReactNode) => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root.render(node));
};

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});

function Form({
  locks,
  onToggleLock,
  highlight,
}: {
  locks?: string[];
  onToggleLock?: (k: StudioKey) => void;
  highlight?: ReadonlySet<string>;
}) {
  const [draft, setDraft] = useState<ImagegenDraft>({ ...emptyDraft(), model: SDXL.id });
  return (
    <GenerateForm
      draft={draft}
      patch={(p) => setDraft((d) => ({ ...d, ...p }))}
      fleetProviders={[]}
      provider={null}
      models={[SDXL]}
      loras={[]}
      model={SDXL}
      samplers={[]}
      schedulers={[]}
      loraWeightMax={2}
      alwaysNegative=""
      busy={false}
      trialFull={false}
      queueFull={false}
      onTrial={() => {}}
      onEnqueue={() => {}}
      modelInHead
      locks={locks}
      onToggleLock={onToggleLock}
      highlight={highlight}
    />
  );
}

const fieldOf = (name: string): HTMLElement | null => {
  for (const l of host.querySelectorAll<HTMLElement>(".igen-field")) {
    if (l.querySelector(".igen-label")?.textContent?.trim() === name) return l;
  }
  return null;
};

describe("錠（決定 4）", () => {
  it("錠はエージェントの欄にだけ付き、押すと鍵で知らせる", async () => {
    const hits: string[] = [];
    await mount(<Form locks={["prompt"]} onToggleLock={(k) => hits.push(k)} />);
    const prompt = fieldOf("Prompt") ?? fieldOf("プロンプト");
    const lock = prompt!.querySelector<HTMLElement>(".igen-lock")!;
    expect(lock.getAttribute("aria-pressed")).toBe("true");
    await act(async () => lock.click());
    expect(hits).toEqual(["prompt"]);
    // The steps field's lock is the params lock.
    const steps = host.querySelectorAll(".igen-grid .igen-lock")[0] as HTMLElement;
    await act(async () => steps.click());
    expect(hits).toEqual(["prompt", "params"]);
    // No lock on the member's own fields: the seed policy, N.
    const locks = host.querySelectorAll(".igen-lock").length;
    expect(locks).toBeGreaterThan(0);
    expect(host.querySelector("select[class='ds-select'] + .igen-lock")).toBeNull();
  });

  it("欄の名前を押しても錠は動かない（label が span の button に転送しない）", async () => {
    const hits: string[] = [];
    await mount(<Form locks={[]} onToggleLock={(k) => hits.push(k)} />);
    const label = host.querySelector<HTMLElement>(".igen-field .igen-label")!;
    await act(async () => label.click());
    expect(hits).toEqual([]);
  });

  it("スタジオなしのペインには錠が無い", async () => {
    await mount(<Form />);
    expect(host.querySelectorAll(".igen-lock")).toHaveLength(0);
  });
});

describe("エージェントが動かした欄の縁取り（決定 6）", () => {
  it("highlight の鍵の欄だけ縁取る（params は 4 つの摘み）", async () => {
    await mount(<Form locks={[]} onToggleLock={() => {}} highlight={new Set(["prompt", "params"])} />);
    const hl = [...host.querySelectorAll(".igen-hl")];
    expect(hl.some((e) => e.querySelector("textarea.igen-prompt"))).toBe(true);
    expect(hl.some((e) => e.querySelector("textarea.igen-negative"))).toBe(false);
    expect(host.querySelectorAll(".igen-grid .igen-hl").length).toBe(4);
  });
});

describe("族カードは Agent の欄を描く（決定 7）", () => {
  it("方言・推奨範囲・試走 steps・品質接頭辞", async () => {
    await mount(<Form />);
    const card = host.querySelector(".igen-family")!;
    expect(card.textContent).toContain("20");
    expect(card.textContent).toContain("40");
    expect(card.querySelector(".igen-chip")?.textContent).toBe("masterpiece, best quality");
  });
});

const LOG: DraftLogEntry[] = [
  { seq: 5, kind: "rewind", at: "2026-09-23T10:05:00Z", author: "rewind", rewind_to: 2, draft: { prompt: "a" } },
  { seq: 4, kind: "press_result", at: "2026-09-23T10:04:00Z", version: "v-1", jobs: ["j1"], state: "ok" },
  { seq: 3, kind: "press", at: "2026-09-23T10:03:00Z", author: "human", version: "v-1", mode: "trial", draft: { prompt: "b" } },
  {
    seq: 2,
    kind: "edit",
    at: "2026-09-23T10:02:00Z",
    author: "agent",
    changes: [{ field: "params", before: { cfg: 7 }, after: { cfg: 5 } }],
    draft: { prompt: "b", params: { cfg: 5 } },
  },
  { seq: 6, kind: "press", at: "2026-09-23T10:06:00Z", author: "human", version: "v-2", mode: "enqueue", draft: { prompt: "a" } },
];

describe("編集履歴カード（決定 9）", () => {
  it("press_result は行にせず press の状態として描き、全文を持つ行に「この時点に戻す」", async () => {
    const back: number[] = [];
    await mount(<DraftLog log={LOG} recordPending={new Set(["v-2"])} hasOlder={false} onOlder={() => {}} onRewind={(s) => back.push(s)} />);
    const rows = [...host.querySelectorAll<HTMLElement>(".igen-log-row")];
    expect(rows.map((r) => r.dataset.seq)).toEqual(["5", "3", "2", "6"]);
    const press = rows.find((r) => r.dataset.seq === "3")!;
    expect(press.querySelector(".igen-log-state")?.className).toContain("ok");
    // v-2 has no result yet and its record did not append: "record pending".
    const pending = rows.find((r) => r.dataset.seq === "6")!;
    expect(pending.querySelector(".igen-log-state")?.className).toContain("record_pending");
    const edit = rows.find((r) => r.dataset.seq === "2")!;
    expect(edit.querySelector(".igen-log-changes")?.textContent).toBe("cfg 7→5");
    await act(async () => edit.querySelector<HTMLButtonElement>(".igen-log-rewind")!.click());
    expect(back).toEqual([2]);
  });
});

// The modal portals to document.body, so these look there rather than in the host.
describe("エージェントを付ける: kind の候補（決定 8）", () => {
  const kinds = () => [...document.body.querySelectorAll<HTMLButtonElement>(".igen-attach-kinds button")].map((b) => [b.dataset.kind, b.disabled] as const);

  it("ターミナルは claude と agy を含み、shell・lcpp・muse は出ない", async () => {
    useReposStore.setState({ repos: [{ name: "art", path: "/home/dev/repos/art", branch: "main" }] });
    await mount(<AttachAgentModal onClose={() => {}} onAttach={async () => true} />);
    const k = kinds().map(([n]) => n);
    expect(k).toContain("claude");
    expect(k).toContain("agy");
    expect(k).not.toContain("shell");
    expect(k).not.toContain("lcpp");
    expect(k).not.toContain("muse");
  });

  it("マネージドは claude が無く、opencode と muse は理由付きで押せない", async () => {
    await mount(<AttachAgentModal onClose={() => {}} onAttach={async () => true} />);
    const seg = [...document.body.querySelectorAll<HTMLButtonElement>(".ui-seg:not(.igen-attach-kinds) button")];
    await act(async () => seg[1].click());
    const k = new Map(kinds());
    expect(k.has("claude")).toBe(false);
    expect(k.get("codex")).toBe(false);
    expect(k.get("opencode")).toBe(true);
    expect(k.get("muse")).toBe(true);
    expect(document.body.querySelector(".igen-attach-blocked")?.textContent).toMatch(/opencode|OpenCode/i);
  });

  it("codex マネージドはリポジトリが要り、worktree を外せない", async () => {
    useReposStore.setState({ repos: [{ name: "art", path: "/home/dev/repos/art", branch: "main" }] });
    const sent: unknown[] = [];
    await mount(
      <AttachAgentModal
        onClose={() => {}}
        onAttach={async (o) => {
          sent.push(o);
          return true;
        }}
      />,
    );
    const seg = [...document.body.querySelectorAll<HTMLButtonElement>(".ui-seg:not(.igen-attach-kinds) button")];
    await act(async () => seg[1].click());
    const codex = document.body.querySelector<HTMLButtonElement>('.igen-attach-kinds button[data-kind="codex"]')!;
    await act(async () => codex.click());
    const go = [...document.body.querySelectorAll<HTMLButtonElement>(".ui-modal-foot button")].pop()!;
    expect(go.disabled).toBe(true);
    const repo = document.body.querySelectorAll<HTMLSelectElement>(".ui-modal-body select");
    const repoSel = [...repo].find((s) => [...s.options].some((o) => o.value === "art"))!;
    await act(async () => {
      repoSel.value = "art";
      repoSel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    const wt = document.body.querySelector<HTMLInputElement>(".igen-attach-wt input")!;
    expect(wt.checked).toBe(true);
    expect(wt.disabled).toBe(true);
    await act(async () => go.click());
    expect(sent).toEqual([
      expect.objectContaining({ kind: "codex", driver: "managed", dir: "/home/dev/repos/art", worktree: true, base: "main" }),
    ]);
  });
});
