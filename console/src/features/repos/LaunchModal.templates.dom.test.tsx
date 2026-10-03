// The launch dialog's first-prompt template picker (#1469): long history entries are shown by a
// one-line summary with the full text in a preview, history can be deleted or kept as a
// template, personal templates are created, edited and deleted here, and picking one never
// throws away text already typed.
import "fake-indexeddb/auto";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Mock } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

type Group = { source: string; label: string; items: { id: string; label: string; body: string }[] };
let served: Group[] = [];

vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => "",
  getUser: () => "",
  api: async () => ({}),
  apiJSON: async () => ({}),
  repoPromptTemplates: async () => ({ groups: served }),
  repoSkills: async () => ({ skills: [] }),
  sessionSkills: async () => ({ skills: [] }),
  errText: (e: { message?: string }) => e?.message ?? "",
  errDetail: (e: { message?: string }) => e?.message ?? "",
  isTransientErr: () => false,
}));

// jsdom reports touch support; the cases run as a desktop (fine pointer) unless they say otherwise.
let coarse = false;
vi.mock("../../lib/device.ts", async (orig) => ({ ...(await orig<typeof import("../../lib/device.ts")>()), coarsePointer: () => coarse }));

const { LaunchModal } = await import("./LaunchModal.tsx");
const { resetAttachDraftDB } = await import("../../lib/attachDraft.ts");
const { getSettings, hydrateUIPrefs, setSettings } = await import("../../lib/settings.ts");
const { normalizeHistory, pushPromptHistory } = await import("../../lib/promptHistory.ts");
const { readTemplates } = await import("../../lib/launchTemplates.ts");
const hist = () => normalizeHistory(getSettings().launchHistory);
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

const promptBox = () => must(document.querySelector<HTMLTextAreaElement>(".launch-prompt-field > textarea"), "first-prompt textarea");
const pop = () => document.querySelector(".launch-tmpl-pop");
const openBtn = () => must(document.querySelector<HTMLButtonElement>(".launch-tmpl-btn"), "template button");
const search = () => must(document.querySelector<HTMLInputElement>(".launch-tmpl-search input"), "search box");
const items = () => [...document.querySelectorAll<HTMLElement>(".launch-tmpl-item")];
const titles = () => items().map((el) => el.querySelector(".launch-tmpl-item-title")?.textContent);
const groups = () => [...document.querySelectorAll(".launch-tmpl-group")].map((g) => g.textContent);
const preview = () => document.querySelector(".launch-tmpl-preview-text")?.textContent;
const button = (label: string, scope: ParentNode = document) =>
  must([...scope.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label), `button "${label}"`);
const launchBtn = () => must([...document.querySelectorAll<HTMLButtonElement>(".ui-modal-foot button")].at(-1), "launch button");

async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

async function render(repo = "app"): Promise<void> {
  await act(async () => {
    root!.render(<LaunchModal repo={repo} branch="main" kinds={["claude", "codex"]} onClose={onClose} onLaunch={onLaunch} />);
  });
  await settle();
}

function setValue(el: HTMLInputElement | HTMLTextAreaElement, text: string): void {
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, text);
  el.dispatchEvent(new Event("input", { bubbles: true }));
}

async function type(el: HTMLInputElement | HTMLTextAreaElement, text: string): Promise<void> {
  await act(async () => setValue(el, text));
  await settle();
}

async function key(el: Element, k: string, init: KeyboardEventInit = {}): Promise<void> {
  await act(async () => {
    el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true, ...init }));
  });
  await settle();
}

async function click(el: Element): Promise<void> {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
  await settle();
}

/** Lets a requestAnimationFrame callback run. */
async function frame(): Promise<void> {
  await act(async () => void (await new Promise((r) => setTimeout(r, 50))));
}

async function hover(el: Element): Promise<void> {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("mousemove", { bubbles: true }));
  });
  await settle();
}

const LONG = "Fix the flaky upload test\n\nThe upload test fails one run in five on CI. Find the race, add a test that fails without the fix, and explain the root cause in the commit body.";

beforeEach(async () => {
  coarse = false;
  localStorage.clear();
  setSettings({ launchTemplates: {}, launchHistory: {} });
  await hydrateUIPrefs(); // the server copy has been read, so history is the synced list
  globalThis.indexedDB = new IDBFactory();
  resetAttachDraftDB();
  served = [];
  onLaunch = vi.fn<Launch>(async () => ({ ok: true }));
  onClose = vi.fn<() => void>();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("LaunchModal template picker", () => {
  it("shows a long history entry by its first line, with the full text in the preview", async () => {
    pushPromptHistory("app", LONG);
    pushPromptHistory("app", "short one");
    await render();
    await click(openBtn());
    expect(pop()).not.toBeNull();
    expect(groups()).toEqual(["History"]);
    expect(titles()).toEqual(["short one", "Fix the flaky upload test"]);
    expect(items()[1].querySelector(".launch-tmpl-item-excerpt")?.textContent).toMatch(/^The upload test fails one run in five/);
    expect(preview()).toBe("short one");

    await key(search(), "ArrowDown");
    expect(preview()).toBe(LONG);
    expect(items()[1].getAttribute("aria-selected")).toBe("true");
    expect(search().getAttribute("aria-activedescendant")).toBe(items()[1].id);

    await hover(items()[0]);
    expect(preview()).toBe("short one");
  });

  it("inserts the highlighted entry into an empty prompt with Enter and expands variables", async () => {
    pushPromptHistory("app", "review {{repo}} on {{branch}}");
    await render();
    await click(openBtn());
    await key(search(), "Enter");
    expect(pop()).toBeNull();
    expect(promptBox().value).toBe("review app on main");
  });

  it("asks before touching typed text, and inserts at the cursor without losing it", async () => {
    pushPromptHistory("app", "TEMPLATE");
    await render();
    await type(promptBox(), "before after");
    promptBox().setSelectionRange(6, 6);
    await click(openBtn());
    await click(items()[0]);
    expect(promptBox().value).toBe("before after"); // nothing changed yet
    expect(document.querySelector(".launch-tmpl-ask")).not.toBeNull();

    await click(button("Insert at cursor"));
    expect(promptBox().value).toBe("before\nTEMPLATE\n after"); // on a line of its own, every typed character kept
    expect(pop()).toBeNull();
    await frame();
    expect([promptBox().selectionStart, promptBox().selectionEnd]).toEqual([15, 15]); // right after the template
    expect(document.activeElement).toBe(promptBox());
  });

  it("puts the caret after the template on touch too, without focusing the box", async () => {
    coarse = true;
    pushPromptHistory("app", "TEMPLATE");
    await render();
    await type(promptBox(), "before after");
    promptBox().setSelectionRange(6, 6);
    promptBox().blur();
    await click(openBtn());
    await click(button("Insert"));
    await click(button("Insert at cursor"));
    await frame();
    expect(promptBox().value).toBe("before\nTEMPLATE\n after");
    expect([promptBox().selectionStart, promptBox().selectionEnd]).toEqual([15, 15]);
    expect(document.activeElement).not.toBe(promptBox());
  });

  // jsdom has no editing commands, so this stands in for a browser's: the template has to go in as
  // a native edit over exactly the range it replaces, which is what lets Ctrl/⌘+Z take it back
  // (checked in headless Chromium for the PR).
  it("inserts and replaces as an undoable native edit where the browser offers one", async () => {
    const calls: [number, number, string][] = [];
    const exec = vi.fn((cmd: string, _ui?: boolean, value?: string) => {
      const el = document.activeElement as HTMLTextAreaElement;
      if (cmd !== "insertText" || !(el instanceof HTMLTextAreaElement)) return false;
      const [a, b] = [el.selectionStart, el.selectionEnd];
      calls.push([a, b, value!]);
      setValue(el, el.value.slice(0, a) + value + el.value.slice(b));
      el.setSelectionRange(a + value!.length, a + value!.length);
      return true;
    });
    const had = Object.getOwnPropertyDescriptor(document, "execCommand");
    Object.defineProperty(document, "execCommand", { configurable: true, value: exec });
    try {
      pushPromptHistory("app", "TEMPLATE");
      await render();
      await type(promptBox(), "before after");
      promptBox().setSelectionRange(6, 6);
      await click(openBtn());
      await click(items()[0]);
      await click(button("Insert at cursor"));
      expect(calls).toEqual([[6, 6, "\nTEMPLATE\n"]]);
      expect(promptBox().value).toBe("before\nTEMPLATE\n after");
      expect([promptBox().selectionStart, promptBox().selectionEnd]).toEqual([15, 15]);

      await click(openBtn());
      await click(items()[0]);
      await click(button("Replace all"));
      expect(calls[1]).toEqual([0, "before\nTEMPLATE\n after".length, "TEMPLATE"]);
      expect(promptBox().value).toBe("TEMPLATE");
      await click(launchBtn());
      expect(onLaunch.mock.calls[0][0].prompt).toBe("TEMPLATE"); // the state followed the native edit
    } finally {
      if (had) Object.defineProperty(document, "execCommand", had);
      else delete (document as { execCommand?: unknown }).execCommand;
    }
  });

  it("replaces the prompt only when asked to, and Cancel keeps it", async () => {
    pushPromptHistory("app", "TEMPLATE");
    await render();
    await type(promptBox(), "typed text");
    await click(openBtn());
    await click(items()[0]);
    await click(button("Cancel"));
    expect(promptBox().value).toBe("typed text");
    expect(items().length).toBe(1); // back on the list

    await click(items()[0]);
    await click(button("Replace all"));
    expect(promptBox().value).toBe("TEMPLATE");
  });

  it("Esc backs out of the question first, then closes the picker, then the dialog", async () => {
    pushPromptHistory("app", "TEMPLATE");
    await render();
    await type(promptBox(), "typed");
    await click(openBtn());
    await click(items()[0]);
    await key(document.body, "Escape");
    expect(document.querySelector(".launch-tmpl-ask")).toBeNull();
    expect(pop()).not.toBeNull();
    await key(document.body, "Escape");
    expect(pop()).toBeNull();
    expect(onClose).not.toHaveBeenCalled();
    expect(promptBox().value).toBe("typed");
  });

  it("deletes a history entry", async () => {
    pushPromptHistory("app", "keep me");
    pushPromptHistory("app", "drop me");
    await render();
    await click(openBtn());
    expect(titles()).toEqual(["drop me", "keep me"]);
    await click(button("Delete", must(document.querySelector(".launch-tmpl-actions"), "actions")));
    expect(titles()).toEqual(["keep me"]);
    expect(hist().map((e) => e.text)).toEqual(["keep me"]);
  });

  it("deletes a history entry too large to sync, which lives on this device only", async () => {
    const big = "huge prompt\n" + "x".repeat(4000);
    pushPromptHistory("app", big);
    expect(hist()).toEqual([]);
    await render();
    await click(openBtn());
    expect(titles()).toEqual(["huge prompt"]);
    await click(button("Delete", must(document.querySelector(".launch-tmpl-actions"), "actions")));
    expect(titles()).toEqual([]);
    expect(localStorage.getItem("af.repo-prompts.app")).toBeNull();
  });

  it("previews on the first tap with a touch pointer, and inserts from the preview", async () => {
    coarse = true;
    pushPromptHistory("app", "first");
    pushPromptHistory("app", "second");
    await render();
    await click(openBtn());
    expect(preview()).toBe("second");
    // The first row starts highlighted; tapping it still only previews.
    await hover(items()[0]);
    await click(items()[0]);
    expect(pop()).not.toBeNull();
    expect(promptBox().value).toBe("");
    // A synthesized mousemove does not move the highlight either; the tap does.
    await hover(items()[1]);
    expect(preview()).toBe("second");
    await click(items()[1]);
    expect(pop()).not.toBeNull(); // previewed, not inserted
    expect(preview()).toBe("first");
    expect(promptBox().value).toBe("");
    await click(button("Insert"));
    expect(promptBox().value).toBe("first");
  });

  it("ignores the Enter that confirms an IME conversion", async () => {
    pushPromptHistory("app", "TEMPLATE");
    await render();
    await click(openBtn());
    await key(search(), "Enter", { keyCode: 229 } as KeyboardEventInit);
    expect(pop()).not.toBeNull();
    expect(promptBox().value).toBe("");
    await key(search(), "Enter");
    expect(promptBox().value).toBe("TEMPLATE");

    await click(openBtn());
    await click(button("New template"));
    await type(must(document.querySelector<HTMLInputElement>(".launch-tmpl-edit input[type=text]"), "name"), "n");
    const body = must(document.querySelector<HTMLTextAreaElement>(".launch-tmpl-edit textarea"), "body");
    await type(body, "b");
    await key(body, "Enter", { ctrlKey: true, keyCode: 229 } as KeyboardEventInit);
    expect(readTemplates()).toEqual([]);
    await key(body, "Enter", { ctrlKey: true });
    expect(readTemplates().map((t) => t.name)).toEqual(["n"]);
  });

  it("hands focus back to the button when Esc closes the picker", async () => {
    await render();
    await click(openBtn());
    expect(document.activeElement).toBe(search());
    await key(document.body, "Escape");
    expect(pop()).toBeNull();
    expect(document.activeElement).toBe(openBtn());
  });

  it("creates a template from scratch, shows it in every repository, edits, renames and deletes it", async () => {
    await render();
    await click(openBtn());
    expect(document.querySelector(".launch-tmpl-note")?.textContent).toMatch(/No templates/);
    await click(button("New template"));
    const name = must(document.querySelector<HTMLInputElement>(".launch-tmpl-edit input[type=text]"), "name");
    const body = must(document.querySelector<HTMLTextAreaElement>(".launch-tmpl-edit textarea"), "body");
    await click(button("Save"));
    expect(document.querySelector(".launch-tmpl-err")?.textContent).toMatch(/name/); // validated, not saved
    expect(readTemplates()).toEqual([]);

    await type(name, "Triage");
    await type(body, "Triage the open issues of {{repo}}");
    await click(button("Save"));
    expect(groups()).toEqual(["My templates"]);
    expect(titles()).toEqual(["Triage"]);
    expect(preview()).toBe("Triage the open issues of app");
    expect(readTemplates()).toMatchObject([{ name: "Triage", body: "Triage the open issues of {{repo}}", repo: "" }]);

    // Another repository's launch dialog shows it too.
    await act(async () => root!.unmount());
    root = createRoot(host);
    await render("other");
    await click(openBtn());
    expect(titles()).toEqual(["Triage"]);
    expect(preview()).toBe("Triage the open issues of other");

    await click(button("Edit"));
    await type(must(document.querySelector<HTMLInputElement>(".launch-tmpl-edit input[type=text]"), "name"), "Issue triage");
    await click(button("Save"));
    expect(titles()).toEqual(["Issue triage"]);
    expect(readTemplates()).toHaveLength(1);

    await click(button("Delete", must(document.querySelector(".launch-tmpl-actions"), "actions")));
    expect(document.querySelector(".launch-tmpl-actions")?.textContent).toMatch(/Delete this template\?/);
    expect(readTemplates()).toHaveLength(1); // asked first
    await click(button("Delete", must(document.querySelector(".launch-tmpl-actions"), "actions")));
    expect(readTemplates()).toEqual([]);
    expect(titles()).toEqual([]);
  });

  it("saves a history entry as a template, limited to this repository", async () => {
    pushPromptHistory("app", LONG);
    await render("app@feature-x"); // a worktree: scope and history are its base clone's
    await click(openBtn());
    expect(titles()).toEqual(["Fix the flaky upload test"]);
    await click(button("Save as template"));
    expect(must(document.querySelector<HTMLInputElement>(".launch-tmpl-edit input[type=text]"), "name").value).toBe("Fix the flaky upload test");
    const only = must(
      [...document.querySelectorAll<HTMLLabelElement>(".launch-tmpl-scope-set label")].find((l) => l.textContent === "Only app"),
      "repo scope",
    );
    await click(must(only.querySelector("input"), "radio"));
    await click(button("Save"));
    expect(groups()).toEqual(["My templates", "History"]);
    expect(readTemplates()).toMatchObject([{ name: "Fix the flaky upload test", body: LONG, repo: "app" }]);

    await act(async () => root!.unmount());
    root = createRoot(host);
    await render("other");
    await click(openBtn());
    expect(titles()).toEqual([]); // neither the app-only template nor app's history
  });

  it("offers the repository's template file but not its commands and skills", async () => {
    served = [
      { source: "command", label: "コマンド", items: [{ id: "c", label: "deploy", body: "/deploy " }] },
      { source: "skill", label: "スキル", items: [{ id: "s", label: "scout", body: "/scout " }] },
      { source: "file", label: "テンプレート", items: [{ id: "Bug report", label: "Bug report", body: "Reproduce and fix" }] },
    ];
    await render();
    await click(openBtn());
    expect(groups()).toEqual(["This repository (.agent-fleet/launch-prompts.md)"]);
    expect(titles()).toEqual(["Bug report"]);
  });

  it("filters by title and body", async () => {
    pushPromptHistory("app", "alpha task");
    pushPromptHistory("app", "beta task\nmentions gamma");
    await render();
    await click(openBtn());
    await type(search(), "gamma");
    expect(titles()).toEqual(["beta task"]);
    await type(search(), "zzz");
    expect(document.querySelector(".launch-tmpl-note")?.textContent).toBe("Nothing matches.");
  });

  it("launches with the inserted template text", async () => {
    pushPromptHistory("app", "do the thing");
    await render();
    await click(openBtn());
    await key(search(), "Enter");
    await click(launchBtn());
    expect(onLaunch.mock.calls[0][0].prompt).toBe("do the thing");
  });
});
