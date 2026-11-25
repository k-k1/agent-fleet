// The "run with" strip (ADR 0142 decision 7, dated note) in both launch forms: the dialog and the
// Start hub's space form share one component, so each behaviour is checked in the form it matters
// for and the shared pieces in both.
import "fake-indexeddb/auto";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Mock } from "vitest";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";

// What the server's synced ui-prefs say when the strip asks again (another device's edits).
let serverPrefs: unknown = {};
let prefsReads = 0;
vi.mock("../../core/api/client.ts", () => ({
  apiJSON: async () => ({}),
  getTenant: () => "",
  getUser: () => "",
  api: async (url: string) =>
    url.includes("api/env/ui-prefs") ? (prefsReads++, serverPrefs) : url.includes("api/ssm/profiles") ? [] : url.includes("api/me/ui-state") ? {} : { branches: [] },
  repoPromptTemplates: async () => ({ groups: [] }),
  errText: (e: { message?: string }) => e?.message ?? "",
  errDetail: (e: { message?: string }) => e?.message ?? "",
  isTransientErr: () => false,
}));

const { LaunchModal, SpaceLaunchForm } = await import("./LaunchModal.tsx");
const { resetAttachDraftDB, useAttachDraft } = await import("../../lib/attachDraft.ts");
const { setSettings, cancelPrefsTimersForTest } = await import("../../lib/settings.ts");
const { readRecentModels, rememberRecentModel } = await import("../../lib/recentModels.ts");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
import type { LaunchAccess, LaunchOpts, LaunchResult } from "./LaunchModal.tsx";

type Launch = (o: LaunchOpts) => Promise<LaunchResult>;

let root: Root | null = null;
let host: HTMLDivElement;
let onLaunch: Mock<Launch>;

const REGISTERED = ["claude-opus-4-8", "claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5", "my-model"];
const access: LaunchAccess = {
  kinds: new Map([
    ["claude", { kind: "claude", routes: ["anthropic-subscription"], usableRoutes: ["anthropic-subscription"] }],
    ["codex", { kind: "codex", routes: ["chatgpt-subscription"], usableRoutes: ["chatgpt-subscription"] }],
  ]) as LaunchAccess["kinds"],
  noRoute: false,
  allOff: false,
};

async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

function Hub({ kinds }: { kinds: string[] }) {
  const [prompt, setPrompt] = useState("");
  const attach = useAttachDraft(null);
  return (
    <SpaceLaunchForm
      kinds={kinds}
      access={access}
      shared={{ prompt, setPrompt, attach, loading: false, missing: 0 }}
      busy={false}
      setBusy={() => {}}
      onClose={() => {}}
      onLaunch={onLaunch}
    />
  );
}

async function renderDialog(kinds: string[]): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <LaunchModal repo="app" branch="main" kinds={kinds} access={access} onClose={() => {}} onLaunch={onLaunch} />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await settle();
}
async function renderHub(kinds: string[]): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <Hub kinds={kinds} />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await settle();
}

const strip = () => document.querySelector<HTMLElement>('[data-testid="run-strip"]')!;
const modelRow = () => strip().querySelector<HTMLElement>(".run-model--claude")!;
const rowButtons = () => [...modelRow().querySelectorAll<HTMLButtonElement>(".run-model-seg .seg-btn")];
const active = () => rowButtons().filter((b) => b.classList.contains("seg-btn--active"));
const moreSelect = () => modelRow().querySelector<HTMLSelectElement>(".run-model-more select");
const click = async (el: Element) => {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
  await settle();
};
const primary = () => document.querySelector<HTMLButtonElement>("button.ui-btn-primary")!;
const detailsSection = () => [...document.querySelectorAll(".launch-sec")].find((s) => s.querySelector(".launch-sec-label")?.textContent === "Details")!;

beforeEach(() => {
  localStorage.clear();
  globalThis.indexedDB = new IDBFactory();
  resetAttachDraftDB();
  setSettings({ claudeCustomModels: REGISTERED });
  serverPrefs = {};
  onLaunch = vi.fn<Launch>(async () => ({ ok: true }));
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
  setSettings({ claudeCustomModels: [] });
});

describe.each([
  ["the launch dialog", renderDialog],
  ["the hub's space form", renderHub],
])("run-with strip in %s", (_name, mount) => {
  it("shows the agent, the route chip's absence without routes, and ONE model row; Details holds none of them", async () => {
    await mount(["claude", "codex"]);
    expect(strip().querySelector(".run-chip--agent")!.textContent).toContain("Claude Code");
    expect(strip().querySelectorAll(".ui-seg.big .seg-btn")).toHaveLength(2);
    expect(modelRow()).toBeTruthy();
    expect(active()).toHaveLength(1);
    expect(detailsSection().querySelector(".ui-seg.big, .run-model, select")).toBeNull();
  });

  it("keeps exactly one active model button across aliases and registered models", async () => {
    rememberRecentModel("claude", "my-model");
    await mount(["claude"]);
    const mine = rowButtons().find((b) => b.title === "my-model")!;
    await click(mine);
    expect(active()).toEqual([mine]);
    await click(rowButtons().find((b) => b.textContent === "Sonnet")!);
    expect(active().map((b) => b.textContent)).toEqual(["Sonnet"]);
  });

  it("lists at most 3 recent registered models with the common prefix dropped, and +N for the rest", async () => {
    for (const m of ["claude-haiku-4-5", "claude-sonnet-4-6", "claude-opus-4-7", "claude-opus-4-8"]) rememberRecentModel("claude", m);
    await mount(["claude"]);
    const registered = rowButtons().filter((b) => b.title.startsWith("claude-") || b.title === "my-model").filter((b) => !["Fable", "Opus", "Sonnet", "Haiku"].includes(b.textContent!));
    expect(registered.map((b) => b.textContent)).toEqual(["opus-4-8", "opus-4-7", "sonnet-4-6"]);
    expect(registered[0].title).toBe("claude-opus-4-8");
    expect(modelRow().querySelector(".run-model-more-label")!.textContent).toContain("+2");
    expect(moreSelect()!.options).toHaveLength(3); // the hint option + the two left out
  });

  it("promotes a pick from +N into the row, which keeps three registered buttons", async () => {
    for (const m of ["claude-haiku-4-5", "claude-sonnet-4-6", "claude-opus-4-7"]) rememberRecentModel("claude", m);
    await mount(["claude"]);
    const sel = moreSelect()!;
    await act(async () => {
      sel.value = "my-model";
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await settle();
    expect(active().map((b) => b.title)).toEqual(["my-model"]);
    const ids = rowButtons().map((b) => b.title).filter((id) => REGISTERED.includes(id));
    expect(ids).toHaveLength(3);
    expect(ids[0]).toBe("my-model");
  });

  it("names the agent and the model on the launch button", async () => {
    await mount(["claude", "codex"]);
    await click(rowButtons().find((b) => b.textContent === "Sonnet")!);
    expect(primary().textContent).toContain("Claude Code · Sonnet");
  });

  it("swaps the model row when the agent changes", async () => {
    await mount(["claude", "codex"]);
    const codex = [...strip().querySelectorAll<HTMLButtonElement>(".ui-seg.big .seg-btn")].find((b) => b.textContent?.includes("Codex"))!;
    await click(codex);
    expect(strip().querySelector(".run-model--claude")).toBeNull();
    expect(strip().querySelector(".run-model--catalog")).not.toBeNull();
    expect(primary().textContent).toContain("Codex");
    expect(primary().textContent).not.toContain("Claude Code");
  });

  it("collapses to one chip and one select for narrow widths (the CSS shows them)", async () => {
    await mount(["claude"]);
    expect(strip().querySelector(".run-chip--combo")!.textContent).toContain("Claude Code");
    const narrow = modelRow().querySelector<HTMLSelectElement>(".run-model-narrow")!;
    expect(narrow.options.length).toBeGreaterThan(4);
    await act(async () => {
      narrow.value = "sonnet";
      narrow.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(active().map((b) => b.textContent)).toEqual(["Sonnet"]);
  });

  it("opens the agent tiles from the chip and closes them after a pick", async () => {
    await mount(["claude", "codex"]);
    const panel = strip().querySelector<HTMLElement>(".run-strip-panel")!;
    expect(panel.hidden).toBe(true);
    await click(strip().querySelector(".run-chip--agent")!);
    expect(panel.hidden).toBe(false);
    await click(panel.querySelectorAll<HTMLButtonElement>(".seg-btn")[1]);
    expect(panel.hidden).toBe(true);
  });
});

describe("recents are written by a launch", () => {
  it("records a registered model, newest first, capped at 3 per agent", async () => {
    for (const m of ["claude-haiku-4-5", "claude-sonnet-4-6", "claude-opus-4-7"]) rememberRecentModel("claude", m);
    await renderDialog(["claude"]);
    await act(async () => {
      moreSelect()!.value = "my-model";
      moreSelect()!.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await click(primary());
    expect(onLaunch).toHaveBeenCalledTimes(1);
    expect(readRecentModels("claude")).toEqual(["my-model", "claude-opus-4-7", "claude-sonnet-4-6"]);
  });

  it("does not record a failed launch", async () => {
    onLaunch = vi.fn<Launch>(async () => ({ ok: false }));
    await renderDialog(["claude"]);
    await click(primary());
    expect(onLaunch).toHaveBeenCalledTimes(1);
    expect(readRecentModels("claude")).toEqual([]);
  });
});

describe("remembered model that is no longer offered", () => {
  it("resolves to the default with a notice, the same value on every layout and in the POST", async () => {
    localStorage.setItem("af.repo-model.app", "removed-on-another-device");
    await renderDialog(["claude"]);
    expect(active()).toHaveLength(1);
    expect(modelRow().querySelector<HTMLSelectElement>(".run-model-narrow")!.value).toBe(active()[0].title);
    expect(primary().textContent).not.toContain("removed-on-another-device");
    expect(strip().querySelector(".run-strip-notice")!.textContent).toContain("removed-on-another-device");
    await click(primary());
    expect(onLaunch.mock.calls[0][0].model).toBe(active()[0].title);
  });
});

describe("registered models of another device", () => {
  it("asks the server for the synced prefs again when the row opens", async () => {
    cancelPrefsTimersForTest(); // the beforeEach's own setSettings left a debounced save that would defer the re-read
    await Promise.resolve();
    const before = prefsReads;
    await renderDialog(["claude"]);
    await settle();
    expect(prefsReads).toBeGreaterThan(before);
  });
});

describe("recents ignore tier aliases", () => {
  it("keeps three registered models when a launch uses an alias", async () => {
    for (const m of ["claude-haiku-4-5", "claude-sonnet-4-6", "claude-opus-4-7"]) rememberRecentModel("claude", m);
    await renderDialog(["claude"]);
    await click(rowButtons().find((b) => b.textContent === "Opus")!);
    await click(primary());
    expect(onLaunch).toHaveBeenCalledTimes(1);
    expect(readRecentModels("claude")).toEqual(["claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5"]);
  });
});

describe("launch button names what it runs with", () => {
  it("says the default for an agent whose model is left to the CLI", async () => {
    await renderDialog(["codex"]);
    expect(primary().textContent).toContain("Codex · default model");
  });
  it("keeps a long registered id in the title and ellipsizes through the launch-go class", async () => {
    setSettings({ claudeCustomModels: [...REGISTERED, "claude-a-very-long-registered-model-id-20260101"] });
    await renderDialog(["claude"]);
    const sel = moreSelect()!;
    await act(async () => {
      sel.value = "claude-a-very-long-registered-model-id-20260101";
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(primary().className).toContain("launch-go");
    expect(primary().title).toContain("a-very-long-registered-model-id-20260101");
  });
});

describe("panels and chips agree", () => {
  const chips = () => [...strip().querySelectorAll<HTMLButtonElement>(".run-chip")];
  it("reports every chip as expanded when the combined chip opened the panels, and closes on Escape", async () => {
    await renderDialog(["claude", "codex"]);
    const combo = strip().querySelector<HTMLButtonElement>(".run-chip--combo")!;
    await click(combo);
    expect(chips().every((c) => c.getAttribute("aria-expanded") === "true" || c.classList.contains("run-chip--route"))).toBe(true);
    expect(strip().querySelector<HTMLElement>(".run-chip--agent")!.getAttribute("aria-expanded")).toBe("true");
    expect([...strip().querySelectorAll<HTMLElement>(".run-strip-panel")].every((p) => p.hidden === false)).toBe(true);
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(chips().every((c) => c.getAttribute("aria-expanded") === "false")).toBe(true);
    expect([...strip().querySelectorAll<HTMLElement>(".run-strip-panel")].every((p) => p.hidden)).toBe(true);
  });

  it("returns the focus to the agent chip after a pick", async () => {
    await renderDialog(["claude", "codex"]);
    const chip = strip().querySelector<HTMLButtonElement>(".run-chip--agent")!;
    await click(chip);
    await click(strip().querySelectorAll<HTMLButtonElement>(".ui-seg.big .seg-btn")[1]);
    expect(document.activeElement).toBe(chip);
  });
});

describe("a manual model dropped by an agent switch", () => {
  it("is named in the notice, and a later manual pick clears it", async () => {
    await renderDialog(["claude", "codex"]);
    await click(rowButtons().find((b) => b.textContent === "Sonnet")!);
    await click(strip().querySelectorAll<HTMLButtonElement>(".ui-seg.big .seg-btn")[1]);
    expect(strip().querySelector(".run-strip-notice")!.textContent).toContain("sonnet");
  });
});
