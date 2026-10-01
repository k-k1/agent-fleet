// The mirror's skill picker over the launch dialog's first prompt. No session exists yet, so the
// list comes from the repository endpoint for the kind about to be started; picking an entry has
// to leave the invocation in the prompt the launch sends, and closing the list with Esc must not
// close the dialog around it.
import "fake-indexeddb/auto";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Mock } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

interface Skill {
  name: string;
  invoke?: string;
  path?: string;
  origin?: string;
  source: "project" | "user" | "cli";
  type: "skill" | "command";
}

let served: Record<string, Skill[]> = {};
const repoSkillsMock = vi.fn(async (_repo: string, kind: string, _subdir: string) => ({ skills: served[kind] ?? [] }));
const sessionSkillsMock = vi.fn(async () => ({ skills: [] }));

vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => "",
  getUser: () => "",
  api: async () => ({}),
  repoPromptTemplates: async () => ({ groups: [] }),
  repoSkills: (...a: unknown[]) => repoSkillsMock(...(a as [string, string, string])),
  sessionSkills: () => sessionSkillsMock(),
  errText: (e: { message?: string }) => e?.message ?? "",
  errDetail: (e: { message?: string }) => e?.message ?? "",
  isTransientErr: () => false,
}));

const { LaunchModal } = await import("./LaunchModal.tsx");
const { resetAttachDraftDB } = await import("../../lib/attachDraft.ts");
const { setSettings } = await import("../../lib/settings.ts");
import type { LaunchOpts, LaunchResult } from "./LaunchModal.tsx";

type Launch = (o: LaunchOpts) => Promise<LaunchResult>;

let root: Root | null = null;
let host: HTMLDivElement;
let onLaunch: Mock<Launch>;
let onClose: Mock<() => void>;

function must<T>(el: T | undefined | null, what: string): T {
  if (!el) throw new Error(`not in the DOM: ${what}`);
  return el;
}

const promptBox = () => must(document.querySelector<HTMLTextAreaElement>("textarea"), "first-prompt textarea");
const list = () => document.querySelector(".launch-prompt-field .mirror-skills");
const rows = () => [...document.querySelectorAll<HTMLButtonElement>(".mirror-skill-item")].map((b) => b.querySelector(".mirror-skill-name")?.textContent);
const kindCard = (label: string) =>
  must([...document.querySelectorAll<HTMLButtonElement>(".ui-seg.big .seg-btn")].find((b) => b.textContent?.includes(label)), `kind "${label}"`);
const launchBtn = () => must([...document.querySelectorAll<HTMLButtonElement>(".ui-modal-foot button")].at(-1), "launch button");

async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

async function render(kinds: string[]): Promise<void> {
  await act(async () => {
    root!.render(<LaunchModal repo="app" branch="main" kinds={kinds} onClose={onClose} onLaunch={onLaunch} />);
  });
  await settle();
}

async function type(text: string): Promise<void> {
  const el = promptBox();
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    setter.call(el, text);
    el.setSelectionRange(text.length, text.length);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await settle();
}

async function key(k: string): Promise<void> {
  await act(async () => {
    promptBox().dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true }));
  });
  await settle();
}

async function click(el: Element): Promise<void> {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
  await settle();
}

beforeEach(() => {
  localStorage.clear();
  globalThis.indexedDB = new IDBFactory();
  resetAttachDraftDB();
  served = {
    claude: [
      { name: "scout", invoke: "/scout ", source: "project", type: "skill" },
      { name: "importer", path: ".agents/skills/importer/SKILL.md", origin: ".agents", source: "project", type: "skill" },
      { name: "simplify", invoke: "/simplify ", source: "cli", type: "skill" },
    ],
    codex: [{ name: "probe", invoke: "$probe ", source: "project", type: "skill" }],
  };
  repoSkillsMock.mockClear();
  sessionSkillsMock.mockClear();
  onLaunch = vi.fn<Launch>(async () => ({ ok: true }));
  onClose = vi.fn<() => void>();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  setSettings({ mirrorSend: "mod-enter" });
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("LaunchModal skill picker", () => {
  it("lists the repo's skills for the kind on the trigger and launches with the picked invocation", async () => {
    await render(["claude"]);
    expect(repoSkillsMock).not.toHaveBeenCalled(); // fetched on open, not on mount

    await type("/");
    expect(repoSkillsMock).toHaveBeenLastCalledWith("app", "claude", "");
    expect(sessionSkillsMock).not.toHaveBeenCalled();
    // Own entries first; the CLI-bundled one is folded behind the "show more" row.
    expect(rows()).toEqual(["/scout", "importer"]);
    expect(document.querySelector(".mirror-skill-more")).not.toBeNull();

    await key("Enter");
    expect(promptBox().value).toBe("/scout ");
    expect(onLaunch).not.toHaveBeenCalled(); // Enter took the pick, not the launch

    await type("/scout chapter 3");
    await click(launchBtn());
    expect(onLaunch.mock.calls[0][0].prompt).toBe("/scout chapter 3");
  });

  it("inserts a foreign skill as a read-and-follow prompt", async () => {
    await render(["claude"]);
    await type("/imp");
    await key("Enter");
    expect(promptBox().value).toBe("Read .agents/skills/importer/SKILL.md and follow that skill's instructions. ");
  });

  it("refetches for the new kind when the agent changes", async () => {
    await render(["claude", "codex"]);
    await click(must(document.querySelector(".launch-prompt-tools .mirror-skill-btn"), "skill button"));
    expect(rows()).toEqual(["/scout", "importer"]);

    await click(kindCard("Codex"));
    expect(repoSkillsMock).toHaveBeenLastCalledWith("app", "codex", "");
    expect(rows()).toEqual(["$probe"]);
  });

  it("closes the list on Esc without closing the dialog", async () => {
    await render(["claude"]);
    await type("/");
    expect(list()).not.toBeNull();
    await key("Escape");
    expect(list()).toBeNull();
    expect(onClose).not.toHaveBeenCalled();
    // Negative control: with the list closed, Esc is the dialog's again.
    await key("Escape");
    expect(onClose).toHaveBeenCalled();
  });

  // Send-on-Enter: an Enter typed while the list is still loading has no row to pick, and must
  // not launch the half-typed "/sco" either. Once the list is closed, Enter is the send key again.
  it("does not launch on a bare Enter while the list is still loading", async () => {
    setSettings({ mirrorSend: "enter" });
    let resolve: (v: { skills: Skill[] }) => void = () => {};
    repoSkillsMock.mockImplementationOnce(() => new Promise((r) => (resolve = r)));
    await render(["claude"]);
    await type("/sco");
    expect(document.querySelector(".mirror-skills-note")).not.toBeNull(); // the loading row
    await key("Enter");
    expect(onLaunch).not.toHaveBeenCalled();
    expect(promptBox().value).toBe("/sco");

    await act(async () => resolve({ skills: served.claude }));
    await settle();
    await key("Enter");
    expect(promptBox().value).toBe("/scout ");
    expect(onLaunch).not.toHaveBeenCalled();

    await key("Escape");
    await key("Enter");
    expect(onLaunch.mock.calls[0][0].prompt).toBe("/scout");
  });
});
