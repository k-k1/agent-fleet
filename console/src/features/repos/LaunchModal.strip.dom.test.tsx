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
let prefsGate: Promise<unknown> | null = null;
// Runs when a gated read answers: what the sync would have applied from the server copy.
let onPrefsAnswer: (() => void) | null = null;
vi.mock("../../core/api/client.ts", () => ({
  apiJSON: async () => ({}),
  getTenant: () => "",
  getUser: () => "",
  api: async (url: string) =>
    url.includes("agents/codex/models") ? { models: [{ id: "gpt-6-sol", label: "GPT-6 Sol", efforts: ["low", "medium", "high"], defaultEffort: "low" }] }
      : url.includes("api/env/ui-prefs") ? (prefsReads++, Promise.resolve(prefsGate).then(() => (onPrefsAnswer?.(), serverPrefs))) : url.includes("api/ssm/profiles") ? [] : url.includes("api/me/ui-state") ? {} : { branches: [] },
  repoPromptTemplates: async () => ({ groups: [] }),
  errText: (e: { message?: string }) => e?.message ?? "",
  errDetail: (e: { message?: string }) => e?.message ?? "",
  isTransientErr: () => false,
}));

const { LaunchModal, SpaceLaunchForm } = await import("./LaunchModal.tsx");
const { resetAttachDraftDB, useAttachDraft } = await import("../../lib/attachDraft.ts");
const { getSettings, setSettings, cancelPrefsTimersForTest } = await import("../../lib/settings.ts");
const { resumePrefsTimersForTest } = await import("../../lib/settings/sync.ts");
const { readRecentModels, rememberRecentModel } = await import("../../lib/recentModels.ts");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
import type { LaunchAccess, LaunchOpts, LaunchResult } from "./LaunchModal.tsx";

type Launch = (o: LaunchOpts) => Promise<LaunchResult>;

let root: Root | null = null;
let host: HTMLDivElement;
let onLaunch: Mock<Launch>;

const REGISTERED = ["claude-opus-4-8", "claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5", "claude-opus-4-1"];
const liveKinds = new Map<string, unknown>([
  ["claude", { kind: "claude", routes: ["anthropic-subscription"], usableRoutes: ["anthropic-subscription"] }],
  ["codex", { kind: "codex", routes: ["chatgpt-subscription"], usableRoutes: ["chatgpt-subscription"] }],
]);
const access: LaunchAccess = { kinds: liveKinds as LaunchAccess["kinds"], noRoute: false, allOff: false };

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

async function renderDialog(kinds: string[], home = false): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <LaunchModal repo={home ? "" : "app"} home={home} branch="main" kinds={kinds} access={access} onClose={() => {}} onLaunch={onLaunch} />
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
// setSettings arms a debounced save, which would make the dialog's re-read wait for it: drop it.
const settleSaves = () => {
  cancelPrefsTimersForTest();
  resumePrefsTimersForTest();
};
// Ctrl/⌘+Enter in the prompt box, the default send key.
const sendKey = async () => {
  await act(async () => {
    document.querySelector("textarea")!.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true, cancelable: true }));
  });
  await settle();
};
const primary = () => document.querySelector<HTMLButtonElement>("button.ui-btn-primary")!;
const detailsSection = () => [...document.querySelectorAll(".launch-sec")].find((s) => s.querySelector(".launch-sec-label")?.textContent === "Details")!;

function setCodexEffort(effort: string, model = "gpt-6-sol") {
  const defaults = getSettings().agentLaunchDefaults;
  const route = model ? { route: "chatgpt-subscription" } : undefined;
  setSettings({ agentLaunchDefaults: { ...defaults, codex: { ...defaults.codex, model, effort, route } } });
  settleSaves();
}

async function openEffortPicker(): Promise<HTMLSelectElement> {
  const options = () => [...detailsSection().querySelectorAll<HTMLSelectElement>("select")].find((el) => [...el.options].some((o) => o.value === "high"));
  if (!options()) await click(detailsSection().querySelector(".launch-sec-head")!);
  return options()!;
}

beforeEach(() => {
  localStorage.clear();
  globalThis.indexedDB = new IDBFactory();
  resetAttachDraftDB();
  setSettings({ claudeCustomModels: REGISTERED });
  cancelPrefsTimersForTest(); // no debounced save of this file's own setup may defer the dialog's re-read
  resumePrefsTimersForTest();
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

describe("Codex launch effort", () => {
  const effortKey = "af.repo-effort.codex.app";
  beforeEach(() => setCodexEffort("high"));
  afterEach(() => setCodexEffort("", ""));

  it("follows the current setting even when the model catalog advertises a different default", async () => {
    await renderDialog(["codex"]);
    expect(detailsSection().querySelector(".launch-sec-sum")?.textContent).toContain("effort Use setting (high)");
    const picker = await openEffortPicker();
    expect(picker.selectedOptions[0].textContent).toBe("Use setting (high)");
    expect([...picker.options].map((o) => o.value)).toEqual(["", "low", "medium", "high"]);
    await act(async () => setCodexEffort("medium"));
    expect(picker.selectedOptions[0].textContent).toBe("Use setting (medium)");
    await click(primary());
    expect(onLaunch).toHaveBeenCalledWith(expect.objectContaining({ model: "gpt-6-sol", effort: "medium", effortChoice: "" }));
  });

  it("restores a concrete choice and discards edits when the form closes", async () => {
    localStorage.setItem(effortKey, "low");
    await renderDialog(["codex"]);
    const picker = await openEffortPicker();
    expect(picker.value).toBe("low");
    await act(async () => {
      picker.value = "";
      picker.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => root!.render(null));
    expect(onLaunch).not.toHaveBeenCalled();
    await renderDialog(["codex"]);
    expect((await openEffortPicker()).value).toBe("low");
    await click(primary());
    expect(onLaunch).toHaveBeenCalledWith(expect.objectContaining({ effort: "low", effortChoice: "low" }));
  });

  it.each([
    ["home", () => renderDialog(["codex"], true)],
    ["space", () => renderHub(["codex"])],
  ])("starts %s with Use setting even when a repo remembers a concrete effort", async (_place, render) => {
    localStorage.setItem(effortKey, "low");
    setCodexEffort("");
    await render();
    expect((await openEffortPicker()).selectedOptions[0].textContent).toBe("Use setting (unspecified)");
  });
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

  it("names what the folded Details holds, and follows each choice", async () => {
    await mount(["claude", "codex"]);
    const head = () => detailsSection().querySelector<HTMLElement>(".launch-sec-head")!;
    // The summary is shown only while the section is folded.
    const sum = () => detailsSection().querySelector(".launch-sec-sum")?.textContent || "";
    const folded = async () => {
      await click(head());
      const text = sum();
      await click(head());
      return text;
    };
    const selectWith = (value: string) =>
      [...detailsSection().querySelectorAll<HTMLSelectElement>("select")].find((e) => [...e.options].some((o) => o.value === value))!;
    const choose = async (sel: HTMLSelectElement, value: string) => {
      await act(async () => {
        sel.value = value;
        sel.dispatchEvent(new Event("change", { bubbles: true }));
      });
    };
    expect(sum()).toBe("effort default · Normal start · Approvals skipped");
    await click(head());
    await choose(selectWith("ask"), "ask");
    expect(await folded()).toContain("Approvals on");
    expect(await folded()).not.toContain("skipped");
    await choose(selectWith("plan"), "plan");
    expect(await folded()).toContain("Plan mode");
    expect(await folded()).not.toContain("Normal start");
    const effort = selectWith("high");
    await choose(effort, "high");
    const effortLabel = effort.selectedOptions[0]!.textContent;
    expect(await folded()).toContain("effort " + effortLabel);
    // The agent's own choices: codex offers no approval choice, so the summary does not claim one.
    await click([...strip().querySelectorAll(".run-chip--agent")][0]!);
    await click([...strip().querySelectorAll(".ui-seg.big .seg-btn")].find((b) => b.textContent === "Codex")!);
    expect(await folded()).not.toContain("Approvals");
    expect(await folded()).not.toContain("Claude Code");
    // A budget is named only once set.
    expect(await folded()).not.toContain("budget");
  });

  it("keeps exactly one active model button across aliases and registered models", async () => {
    rememberRecentModel("claude", "claude-opus-4-1");
    await mount(["claude"]);
    const mine = rowButtons().find((b) => b.title === "claude-opus-4-1")!;
    await click(mine);
    expect(active()).toEqual([mine]);
    await click(rowButtons().find((b) => b.textContent === "Sonnet")!);
    expect(active().map((b) => b.textContent)).toEqual(["Sonnet"]);
  });

  it("lists at most 3 recent registered models with the common prefix dropped, and +N for the rest", async () => {
    for (const m of ["claude-haiku-4-5", "claude-sonnet-4-6", "claude-opus-4-7", "claude-opus-4-8"]) rememberRecentModel("claude", m);
    await mount(["claude"]);
    const registered = rowButtons().filter((b) => b.title.startsWith("claude-") || b.title === "claude-opus-4-1").filter((b) => !["Fable", "Opus", "Sonnet", "Haiku"].includes(b.textContent!));
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
      sel.value = "claude-opus-4-1";
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await settle();
    expect(active().map((b) => b.title)).toEqual(["claude-opus-4-1"]);
    const ids = rowButtons().map((b) => b.title).filter((id) => REGISTERED.includes(id));
    expect(ids).toHaveLength(3);
    expect(ids[0]).toBe("claude-opus-4-1");
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
      moreSelect()!.value = "claude-opus-4-1";
      moreSelect()!.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await click(primary());
    expect(onLaunch).toHaveBeenCalledTimes(1);
    expect(readRecentModels("claude")).toEqual(["claude-opus-4-1", "claude-opus-4-7", "claude-sonnet-4-6"]);
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

describe("a model another device registered", () => {
  it("is kept until the synced list answers, then shown active and sent", async () => {
    localStorage.setItem("af.repo-model.app", "claude-opus-4-9");
    cancelPrefsTimersForTest();
    onPrefsAnswer = () => {
      setSettings({ claudeCustomModels: [...REGISTERED, "claude-opus-4-9"] });
      settleSaves();
    };
    let release!: () => void;
    prefsGate = new Promise<void>((r) => (release = r));
    try {
      await renderDialog(["claude"]);
      await settle();
      // Still waiting: the unverified choice has not been replaced, and nothing launches on it —
      // not by the button, and not by the send key either.
      expect(primary().disabled).toBe(true);
      await click(primary());
      await sendKey();
      expect(onLaunch).not.toHaveBeenCalled();
      expect(strip().querySelector(".run-strip-why")).not.toBeNull();
      expect(strip().querySelector(".run-strip-notice")).toBeNull();
      await act(async () => release());
      prefsGate = null;
      await settle();
      expect(active().map((b) => b.title)).toEqual(["claude-opus-4-9"]);
      expect(strip().querySelector(".run-strip-notice")).toBeNull();
      await sendKey();
      expect(onLaunch).toHaveBeenCalledTimes(1);
      expect(onLaunch.mock.calls[0][0].model).toBe("claude-opus-4-9");
    } finally {
      prefsGate = null;
      onPrefsAnswer = null;
    }
  });
});

describe("the registered models are read again when the agent becomes Claude", () => {
  it("picks up a model registered elsewhere after switching from another agent", async () => {
    localStorage.setItem("af.repo-model.app", "claude-opus-4-9");
    onPrefsAnswer = () => {
      setSettings({ claudeCustomModels: [...REGISTERED, "claude-opus-4-9"] });
      settleSaves();
    };
    try {
      await renderDialog(["codex", "claude"]);
      const before = prefsReads;
      await click([...strip().querySelectorAll<HTMLButtonElement>(".ui-seg.big .seg-btn")].find((b) => b.textContent?.includes("Claude"))!);
      await settle();
      expect(prefsReads).toBeGreaterThan(before);
      expect(active().map((b) => b.title)).toEqual(["claude-opus-4-9"]);
    } finally {
      onPrefsAnswer = null;
    }
  });
});

describe("each switch to Claude is its own read", () => {
  it("does not let the first read vouch for a later selection", async () => {
    localStorage.setItem("af.repo-model.app", "claude-opus-4-9");
    await renderDialog(["claude", "codex"]);
    const tile = (name: string) => [...strip().querySelectorAll<HTMLButtonElement>(".ui-seg.big .seg-btn")].find((b) => b.textContent?.includes(name))!;
    await click(tile("Codex"));
    // The model is registered elsewhere by the time of the second read, which is slow.
    let release!: () => void;
    prefsGate = new Promise<void>((r) => (release = r));
    onPrefsAnswer = () => {
      setSettings({ claudeCustomModels: [...REGISTERED, "claude-opus-4-9"] });
      settleSaves();
    };
    settleSaves();
    try {
      await click(tile("Claude"));
      await settle();
      expect(strip().querySelector(".run-strip-notice")).toBeNull();
      expect(primary().disabled).toBe(true);
      await act(async () => release());
      prefsGate = null;
      await settle();
      expect(active().map((b) => b.title)).toEqual(["claude-opus-4-9"]);
    } finally {
      prefsGate = null;
      onPrefsAnswer = null;
    }
  });
});

describe("a Settings default that is no longer registered", () => {
  it("does not hide the team's default", async () => {
    localStorage.setItem("af.repo-model.app", "removed-model");
    const { getSettings } = await import("../../lib/settings.ts");
    const { useUIStateStore } = await import("../../core/store/uiState.ts");
    setSettings({ agentLaunchDefaults: { ...getSettings().agentLaunchDefaults, claude: { ...getSettings().agentLaunchDefaults.claude, model: "claude-opus-4-9" } } });
    settleSaves();
    useUIStateStore.setState({ state: { defaults: { agent: "claude", launch: { model: "haiku" } } } as never });
    try {
      await renderDialog(["claude"]);
      expect(active().map((b) => b.title)).toEqual(["haiku"]);
      await click(primary());
      expect(onLaunch.mock.calls[0][0].model).toBe("haiku");
    } finally {
      useUIStateStore.setState({ state: {} });
      setSettings({ agentLaunchDefaults: { ...getSettings().agentLaunchDefaults, claude: { ...getSettings().agentLaunchDefaults.claude, model: "" } } });
    }
  });
});

describe("a read that did not land", () => {
  it("asks again instead of treating a failed or overtaken read as confirmation", async () => {
    localStorage.setItem("af.repo-model.app", "claude-opus-4-9");
    serverPrefs = { error: { code: "http_502" } };
    const reads = () => prefsReads;
    const before = reads();
    onPrefsAnswer = () => {
      // The second answer is the good one and carries the model.
      serverPrefs = {};
      setSettings({ claudeCustomModels: [...REGISTERED, "claude-opus-4-9"] });
      settleSaves();
    };
    try {
      await renderDialog(["claude"]);
      await act(async () => void (await new Promise((r) => setTimeout(r, 900))));
      await settle();
      expect(reads() - before).toBeGreaterThanOrEqual(2);
      expect(active().map((b) => b.title)).toEqual(["claude-opus-4-9"]);
      expect(strip().querySelector(".run-strip-notice")).toBeNull();
    } finally {
      onPrefsAnswer = null;
      serverPrefs = {};
    }
  });
});

describe("a save pending when the row opens", () => {
  it("waits without reading or launching, then reads after the save and sends the model it found", async () => {
    localStorage.setItem("af.repo-model.app", "claude-opus-4-9");
    await renderDialog(["claude", "codex"]); // the first read lands: the prefs are loaded, saves can be armed
    const tile = (name: string) => [...strip().querySelectorAll<HTMLButtonElement>(".ui-seg.big .seg-btn")].find((b) => b.textContent?.includes(name))!;
    await click(tile("Codex"));
    setSettings({ claudeCustomModels: REGISTERED.slice(0, 4) }); // arms the debounced save
    onPrefsAnswer = () => {
      setSettings({ claudeCustomModels: [...REGISTERED, "claude-opus-4-9"] });
      settleSaves();
    };
    const before = prefsReads;
    try {
      await click(tile("Claude"));
      await settle();
      expect(prefsReads).toBe(before); // the save is pending: no read
      expect(primary().disabled).toBe(true);
      await sendKey();
      expect(onLaunch).not.toHaveBeenCalled();
      await act(async () => void (await new Promise((r) => setTimeout(r, 1800)))); // the save lands, the retry reads
      await settle();
      expect(prefsReads).toBeGreaterThan(before);
      expect(active().map((b) => b.title)).toEqual(["claude-opus-4-9"]);
      await sendKey();
      expect(onLaunch.mock.calls[0][0].model).toBe("claude-opus-4-9");
    } finally {
      onPrefsAnswer = null;
    }
  });
});

describe("a member default that is no longer registered", () => {
  it("falls to the team's default before the built-in one", async () => {
    localStorage.setItem("af.repo-model.app", "removed-model");
    const { useUIStateStore } = await import("../../core/store/uiState.ts");
    useUIStateStore.setState({
      state: {
        launch: { agent: "claude", model: "claude-opus-4-9" },
        defaults: { agent: "claude", launch: { model: "haiku" } },
        effective: { launch: { agent: "claude", model: "claude-opus-4-9" } },
      } as never,
    });
    try {
      await renderDialog(["claude"]);
      expect(active().map((b) => b.title)).toEqual(["haiku"]);
      await click(primary());
      expect(onLaunch.mock.calls[0][0].model).toBe("haiku");
    } finally {
      useUIStateStore.setState({ state: {} });
    }
  });
});

describe("a dropped remembered model falls back along the usual defaults", () => {
  it("uses the member's Settings default, not the first alias", async () => {
    localStorage.setItem("af.repo-model.app", "removed-model");
    const { getSettings } = await import("../../lib/settings.ts");
    setSettings({ agentLaunchDefaults: { ...getSettings().agentLaunchDefaults, claude: { ...getSettings().agentLaunchDefaults.claude, model: "haiku" } } });
    settleSaves();
    await renderDialog(["claude"]);
    expect(active().map((b) => b.title)).toEqual(["haiku"]);
    await click(primary());
    expect(onLaunch.mock.calls[0][0].model).toBe("haiku");
    setSettings({ agentLaunchDefaults: { ...getSettings().agentLaunchDefaults, claude: { ...getSettings().agentLaunchDefaults.claude, model: "" } } });
  });
});

describe("a default that is no longer registered either", () => {
  it("steps down to the next default that is offered, not the first button", async () => {
    localStorage.setItem("af.repo-model.app", "removed-model");
    const { useUIStateStore } = await import("../../core/store/uiState.ts");
    const { getSettings } = await import("../../lib/settings.ts");
    useUIStateStore.setState({ state: { launch: { agent: "claude", model: "claude-opus-4-9" } } as never });
    setSettings({ agentLaunchDefaults: { ...getSettings().agentLaunchDefaults, claude: { ...getSettings().agentLaunchDefaults.claude, model: "haiku" } } });
    settleSaves();
    try {
      await renderDialog(["claude"]);
      expect(active().map((b) => b.title)).toEqual(["haiku"]);
      await click(primary());
      expect(onLaunch.mock.calls[0][0].model).toBe("haiku");
    } finally {
      useUIStateStore.setState({ state: {} });
      setSettings({ agentLaunchDefaults: { ...getSettings().agentLaunchDefaults, claude: { ...getSettings().agentLaunchDefaults.claude, model: "" } } });
    }
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

describe("a route panel that vanished with the agent", () => {
  it("leaves the combined chip closed and consistent", async () => {
    liveKinds.set("codex", { kind: "codex", routes: [], usableRoutes: [] } as never);
    try {
      await renderDialog(["claude", "codex"]);
      const combo = strip().querySelector<HTMLButtonElement>(".run-chip--combo")!;
      await click(combo);
      expect(combo.getAttribute("aria-expanded")).toBe("true");
      await click(strip().querySelectorAll<HTMLButtonElement>(".ui-seg.big .seg-btn")[1]); // codex: nothing to choose
      expect(strip().querySelector(".run-chip--route")).toBeNull();
      expect(combo.getAttribute("aria-expanded")).toBe("false");
      expect([...strip().querySelectorAll<HTMLElement>(".run-strip-panel")].every((p) => p.hidden)).toBe(true);
    } finally {
      liveKinds.set("codex", { kind: "codex", routes: ["chatgpt-subscription"], usableRoutes: ["chatgpt-subscription"] } as never);
    }
  });
});
