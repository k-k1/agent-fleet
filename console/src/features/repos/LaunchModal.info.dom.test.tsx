// Static field explanations in both launch forms sit behind an ⓘ (docs/CONVENTIONS.md §13); only
// state notices stay inline. The dialog and the Start hub's space form are checked side by side.
import "fake-indexeddb/auto";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../../core/api/client.ts", () => ({
  apiJSON: async () => ({}),
  getTenant: () => "",
  getUser: () => "",
  api: async (url: string) => (url.includes("api/ssm/profiles") ? [] : url.includes("api/me/ui-state") || url.includes("api/env/ui-prefs") ? {} : { branches: [] }),
  repoPromptTemplates: async () => ({ groups: [] }),
  errText: (e: { message?: string }) => e?.message ?? "",
  errDetail: (e: { message?: string }) => e?.message ?? "",
  isTransientErr: () => false,
}));

const { LaunchModal, SpaceLaunchForm } = await import("./LaunchModal.tsx");
const { resetAttachDraftDB, useAttachDraft } = await import("../../lib/attachDraft.ts");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const en = (await import("../../lib/i18n/locales/en/sessions.ts")).sessions as Record<string, string>;
import type { LaunchAccess } from "./LaunchModal.tsx";

let root: Root | null = null;
let host: HTMLDivElement;
const access: LaunchAccess = {
  kinds: new Map([["claude", { kind: "claude", routes: ["anthropic-subscription"], usableRoutes: ["anthropic-subscription"] }]]) as LaunchAccess["kinds"],
  noRoute: false,
  allOff: false,
};
const settle = async () => {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
function Hub() {
  const [prompt, setPrompt] = useState("");
  const attach = useAttachDraft(null);
  return (
    <SpaceLaunchForm kinds={["claude"]} access={access} shared={{ prompt, setPrompt, attach, loading: false, missing: 0 }} busy={false} setBusy={() => {}} onClose={() => {}} onLaunch={async () => ({ ok: true })} />
  );
}
async function mount(node: React.ReactNode) {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>{node}</ConfirmProvider>
      </ToastProvider>,
    );
  });
  await settle();
}

beforeEach(() => {
  localStorage.clear();
  globalThis.indexedDB = new IDBFactory();
  resetAttachDraftDB();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

// Texts that explain a field whatever its value: none may be an inline (visible) hint.
const STATIC_KEYS = ["launch.first_prompt_note", "launch.first_prompt_empty", "launch.subdir_wt_hint", "launch.base_origin_note", "launch.own_default_hint", "launch.perm_hint", "launch.plan_hint", "launch.field.effort_hint", "launch.budget_hint"];

// The ⓘ texts each form must carry (the others depend on the agent, the working copy or the branch mode).
const EXPECTED: Record<string, string[]> = {
  dialog: ["launch.first_prompt_note", "launch.first_prompt_empty", "launch.title_empty", "launch.subdir_wt_hint", "launch.base_origin_note", "launch.perm_hint", "launch.plan_hint", "launch.budget_hint", "launch.own_default_hint"],
  "space form": ["launch.title_empty", "launch.perm_hint", "launch.plan_hint", "launch.own_default_hint", "start.space_note"],
};

const panels = () => [...document.querySelectorAll<HTMLElement>(".ui-field-info-panel")];
const inline = (text: string) =>
  [...document.querySelectorAll<HTMLElement>(".ui-field-hint")].filter((e) => !e.closest("[hidden]") && e.textContent?.includes(text));

describe("launch forms: static explanations behind ⓘ", () => {
  for (const [name, node] of [
    ["dialog", <LaunchModal repo="app" branch="main" kinds={["claude"]} access={access} onClose={() => {}} onLaunch={async () => ({ ok: true })} />],
    ["space form", <Hub />],
  ] as const) {
    it(`${name}: no static hint inline, each ⓘ reveals its text and is keyboard-reachable`, async () => {
      await mount(node);
      // Open every folded section so the fields inside are rendered.
      for (const h of document.querySelectorAll<HTMLButtonElement>('.launch-sec-head[aria-expanded="false"]')) await act(async () => h.click());
      await settle();
      const have = panels().map((p) => p.textContent);
      // A prompt's panel joins several texts and Trans strings carry markup: compare the leading words.
      for (const k of EXPECTED[name]) expect(have.some((t) => t?.includes(en[k].slice(0, 16))), k).toBe(true);
      const inLaunchForm = panels().length > 0;
      expect(inLaunchForm).toBe(true);
      for (const k of STATIC_KEYS) {
        const text = en[k];
        expect(text, k).toBeTruthy();
        expect(inline(text), `${k} must not be rendered inline`).toHaveLength(0);
      }
      for (const p of panels()) {
        expect(p.hidden).toBe(true);
        const btn = document.querySelector<HTMLButtonElement>(`button.ui-field-info[aria-controls="${p.id}"]`)!;
        expect(btn, "an ⓘ button per panel").toBeTruthy();
        expect(btn.tabIndex).toBeGreaterThanOrEqual(0);
        expect(btn.getAttribute("aria-describedby")).toBe(p.id);
      }
      const first = panels()[0];
      const btn = document.querySelector<HTMLButtonElement>(`button.ui-field-info[aria-controls="${first.id}"]`)!;
      await act(async () => btn.click());
      expect(first.hidden).toBe(false);
      expect(btn.getAttribute("aria-expanded")).toBe("true");
    });
  }

  it("dialog: an invalid budget and an unsavable default stay inline, the base label names its input", async () => {
    await mount(<LaunchModal repo="app" branch="main" kinds={["claude"]} access={access} onClose={() => {}} onLaunch={async () => ({ ok: true })} />);
    for (const h of document.querySelectorAll<HTMLButtonElement>('.launch-sec-head[aria-expanded="false"]')) await act(async () => h.click());
    await settle();
    // The ⓘ must not capture the label: clicking "Base branch" has to reach the input, which carries its name.
    const base = document.getElementById("launch-base-branch") as HTMLInputElement | null;
    expect(base, "base branch input").toBeTruthy();
    const label = document.querySelector<HTMLLabelElement>('label[for="launch-base-branch"]')!;
    expect(label.control).toBe(base);
    const budget = [...document.querySelectorAll<HTMLInputElement>("input")].find((i) => i.getAttribute("inputmode") === "decimal");
    expect(budget, "budget input").toBeTruthy();
    expect(inline(en["sess.budget_invalid"])).toHaveLength(0);
    await act(async () => {
      const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      set.call(budget, "abc");
      budget!.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(inline(en["sess.budget_invalid"]).length).toBeGreaterThan(0);
    expect(inline(en["launch.own_default_no_budget"]).length).toBeGreaterThan(0);
  });
});
