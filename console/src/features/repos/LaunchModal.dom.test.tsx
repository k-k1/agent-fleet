// Render tests for the start-work dialog's branch section — the part that decides what git
// actually does. New-branch mode forks a branch off a base; existing-branch mode checks an
// EXISTING branch out into the worktree instead (base=<branch>, no new branch,
// use_existing). Those three fields going out wrong is the difference between
// "start work on develop" and "silently fork a divergent develop".
import "fake-indexeddb/auto";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Mock } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

interface Branch {
  name: string;
  unix?: number;
  current?: boolean;
  worktree_path?: string;
}

let served: Branch[] = [];
// Folder listing for the working-directory picker (api/fs/tree). Keyed by the browsed
// home-relative path so a click into a folder can serve that folder's children.
let tree: Record<string, string[]> = {};
// The lcpp launch model catalog (GET /agents/lcpp/models — agentModels.ts's fetchModels).
// Empty by default so tests that don't care about lcpp's model picker see the ordinary
// "nothing fetched yet" shape; docs/log/109's own tests set this per case.
let lcppModels: { id: string; label: string }[] = [];
// The member's Settings AWS profiles (GET api/ssm/profiles), for the bedrock route's picker.
let ssmProfiles: unknown[] = [];
// When set, GET api/ssm/profiles waits on it (the Bedrock default's profiles arrive late).
let ssmGate: Promise<unknown> | null = null;
// opencode's launch model catalog (GET /agents/opencode/models).
let opencodeModels: { id: string; label: string }[] = [];
// Whether PATCH api/me/ui-state (the member's own default) succeeds.
let patchResult = true;
const apiMock = vi.fn(async (url: string) => {
  if (url.includes("api/me/ui-state")) return patchResult ? {} : { error: { message: "offline" } };
  if (url.includes("api/ssm/profiles")) {
    if (ssmGate) await ssmGate;
    return ssmProfiles;
  }
  if (url.includes("agents/opencode/models")) {
    return { models: opencodeModels };
  }
  if (url.includes("fs/tree")) {
    const path = decodeURIComponent(new URLSearchParams(url.split("?")[1]).get("path") || "");
    return { entries: (tree[path] || []).map((name) => ({ name, type: "dir" })) };
  }
  if (url.includes("agents/lcpp/models")) {
    return { models: lcppModels };
  }
  return { branches: served };
});

// The usage hub behind the real useUsage: each subscription is answered once, the first with
// `usageReading` and every later one with a failed read (a re-read during a network blip).
let usageReading: Record<string, unknown> | null = null;
let usageSubs = 0;
vi.mock("../../app/wsbar/usageAll.ts", async (orig) => ({
  ...((await orig()) as object),
  subscribeUsage: (_t: unknown, _k: unknown, _e: unknown, listener: (d: unknown) => void) => {
    usageSubs++;
    const d = usageSubs === 1 ? usageReading : { ok: false, authed: true, error: "offline" };
    if (d) queueMicrotask(() => listener(d));
    return () => {};
  },
}));
vi.mock("../../core/api/client.ts", () => ({
  apiJSON: async () => ({}),
  getTenant: () => "",
  getUser: () => "",
  api: (...a: unknown[]) => apiMock(...(a as [string])),
  repoPromptTemplates: async () => ({ groups: [] }),
  errText: (e: { message?: string }) => e?.message ?? "",
  errDetail: (e: { message?: string }) => e?.message ?? "",
  isTransientErr: () => false,
}));

const { LaunchModal } = await import("./LaunchModal.tsx");
const { useUIStateStore: uiStore } = await import("../../core/store/uiState.ts");
const { parseKindCatalog, setKindCatalog } = await import("../../agents/kindCatalog.ts");
const { resetAttachDraftDB } = await import("../../lib/attachDraft.ts");
const { t } = await import("../../lib/i18n/index.ts");
import type { LaunchOpts, LaunchResult } from "./LaunchModal.tsx";
import { openAccess } from "../../test/launchAccess.ts";

const ACCESS = openAccess();

type Launch = (o: LaunchOpts) => Promise<LaunchResult>;

let root: Root | null = null;
let host: HTMLDivElement;
let onLaunch: Mock<Launch>;
let onClose: Mock<() => void>;

// A missing element means the UI moved (a control changed section / label), which is the
// thing these tests exist to catch — say so, instead of failing later on `undefined.click`.
function must<T>(el: T | undefined | null, what: string): T {
  if (!el) throw new Error(`not in the DOM: ${what}`);
  return el;
}

const buttons = () => [...document.querySelectorAll<HTMLButtonElement>("button")];
const byText = (t: string) => must(buttons().find((b) => b.textContent?.includes(t)), `button "${t}"`);
// Location / Advanced are collapsed sections (LaunchSection): their controls only exist in the
// DOM once the header is expanded. The header also carries the summary line, so match
// on the label span rather than the whole row.
const secHead = (label: string) =>
  must(
    buttons().find(
      (b) => b.classList.contains("launch-sec-head") && b.querySelector(".launch-sec-label")?.textContent === label,
    ),
    `section "${label}"`,
  );
const summaryOf = (label: string) => secHead(label).querySelector(".launch-sec-sum")!.textContent || "";
const expand = (label: string) => click(secHead(label));
const branchRows = () => [...document.querySelectorAll<HTMLButtonElement>(".branch-item")];
const rowFor = (name: string) => must(branchRows().find((b) => b.textContent?.includes(name)), `branch row "${name}"`);
// A folder row inside the working-directory browser.
const dirRow = (name: string) =>
  must([...document.querySelectorAll(".dirpick-row")].find((b) => b.textContent?.includes(name)), `folder row "${name}"`);

async function render(kinds = ["claude"], extra: { repo?: string; initialPrompt?: string; claudeExpired?: boolean } = {}): Promise<void> {
  await act(async () => {
    root!.render(
      <LaunchModal
        access={ACCESS}
        repo={extra.repo ?? "app"}
        branch="main"
        kinds={kinds}
        claudeExpired={extra.claudeExpired}
        initialPrompt={extra.initialPrompt}
        onClose={onClose}
        onLaunch={onLaunch}
      />,
    );
  });
  await act(async () => {
    await Promise.resolve();
  });
}

// Close and reopen the dialog, the way ✕ / Esc does: the component unmounts, so anything
// that survives has to have been persisted.
async function reopen(extra: { repo?: string; initialPrompt?: string } = {}): Promise<void> {
  act(() => root?.unmount());
  root = createRoot(host);
  await render(["claude"], extra);
  await settle(); // the attachment draft comes back from IndexedDB asynchronously
}

const promptBox = () => must(document.querySelector<HTMLTextAreaElement>("textarea"), "first-prompt textarea");
// Attachment chips (files waiting to be uploaded).
const chips = () => [...document.querySelectorAll(".mirror-attach .ma-chip")];

// Paste an image from the clipboard. jsdom has no DataTransfer, so only the clipboardData the
// handler reads is put on the raw event (React reads it from the native event).
async function pasteImage(name: string, type = "image/png"): Promise<void> {
  const file = new File(["BYTES"], name, { type });
  const ev = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(ev, "clipboardData", {
    value: { items: [{ kind: "file", type, getAsFile: () => file }] },
  });
  await act(async () => {
    promptBox().dispatchEvent(ev);
  });
  await settle();
}

// Fire a drag event on the prompt field. jsdom has no DataTransfer either; browsers expose
// types during dragover but an empty files list until the drop itself.
async function drag(type: "dragover" | "drop", types: string[], files: File[] = []): Promise<Event> {
  const ev = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(ev, "dataTransfer", { value: { types, files } });
  await act(async () => {
    promptBox().dispatchEvent(ev);
  });
  await settle();
  return ev;
}

// Both IndexedDB and React finish their reads and writes a few microtasks later.
async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

async function type(text: string): Promise<void> {
  const el = promptBox();
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    setter.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function click(el: Element): Promise<void> {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const launchedWith = (): LaunchOpts => onLaunch.mock.calls[0][0] as LaunchOpts;

beforeEach(() => {
  localStorage.clear();
  usageReading = null;
  usageSubs = 0;
  // These suites drive the agent, model and advanced controls, which sit in the Details fold
  // (ADR 0142 decision 7); start with it open, as a device that left it open does. The fold
  // itself is tested in LaunchModal.dom.test.tsx ("what -> where -> details").
  localStorage.setItem("af.launch-open.details", "1");
  // The attachment draft lives in IndexedDB (lib/attachDraft). Start every test with a fresh
  // DB and a connection re-opened against it.
  globalThis.indexedDB = new IDBFactory();
  resetAttachDraftDB();
  tree = { "repos/app": ["console", "workspace"], "repos/app/console": ["src"] };
  served = [
    { name: "main", unix: 3, current: true },
    { name: "develop", unix: 2 },
    { name: "busy", unix: 1, worktree_path: "/home/dev/repos/app@busy" },
  ];
  lcppModels = [];
  patchResult = true;
  apiMock.mockClear();
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

// ADR 0142 decision 7: what -> where -> details. The prompt comes first with the run-with strip
// (agent, route, model) under it, then the place, then one Details fold holding the rest; nothing
// that blocks the launch, and no expired login the launch would run on, is ever left behind a fold.
describe("LaunchModal what -> where -> details", () => {
  // These tests are about the fold, so they start from a device that never opened it.
  beforeEach(() => localStorage.removeItem("af.launch-open.details"));
  const strip = () => must(document.querySelector<HTMLElement>('[data-testid="run-strip"]'), "run-with strip");
  const follows = (a: Node, b: Node) => !!(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING);

  it("puts the prompt first, the run-with strip under it, then Location, then a folded Details without agent or model", async () => {
    await render(["claude", "codex"]);
    expect([...document.querySelectorAll(".launch-sec-label")].map((el) => el.textContent)).toEqual(["Location", "Details"]);
    expect(follows(promptBox(), strip())).toBe(true);
    expect(follows(strip(), secHead("Location"))).toBe(true);
    expect(follows(secHead("Location"), secHead("Details"))).toBe(true);
    expect(secHead("Details").getAttribute("aria-expanded")).toBe("false");
    // The strip names the agent; Details no longer lists agent, route or model.
    expect(strip().textContent).toContain("Claude Code");
    expect(summaryOf("Details")).not.toMatch(/Claude Code/);
    expect(strip().contains(document.querySelector(".ui-seg.big"))).toBe(true);
    expect(secHead("Details").closest("section")!.querySelector(".ui-seg.big")).toBeNull();
  });

  it("launches the agent the strip names, without opening anything", async () => {
    await render(["claude", "codex"]);
    await click(byText("Start in a worktree with Claude Code"));
    expect(launchedWith().kind).toBe("claude");
  });

  it("remembers an opened Details for the next dialog", async () => {
    await render(["claude", "codex"]);
    await expand("Details");
    expect(localStorage.getItem("af.launch-open.details")).toBe("1");
    await reopen();
    expect(secHead("Details").getAttribute("aria-expanded")).toBe("true");
    await expand("Details");
    expect(localStorage.getItem("af.launch-open.details")).toBe(null);
  });

  it("says an expired Claude login under the strip, with Details still folded", async () => {
    await render(["claude"], { claudeExpired: true });
    expect(secHead("Details").getAttribute("aria-expanded")).toBe("false");
    expect(strip().textContent).toContain("Claude login has expired");
  });

  it("leaves the expired-login note out when another agent is picked", async () => {
    await render(["codex", "claude"], { claudeExpired: true });
    expect(document.body.textContent).not.toContain("Claude login has expired");
  });

  it("explains in the strip, not in Details, when no agent can be launched", async () => {
    await render([]);
    expect(secHead("Details").getAttribute("aria-expanded")).toBe("false");
    expect(strip().querySelector('[data-testid="launch-noagents"]')).not.toBeNull();
  });

  it("keeps the launch disabled while a model must be picked and none can be (empty lcpp catalog)", async () => {
    lcppModels = [];
    await render(["lcpp"]);
    await settle();
    expect(byText("Start in a worktree").disabled).toBe(true);
    expect(secHead("Details").getAttribute("aria-expanded")).toBe("false");
    // lcpp has no default model: the button must not read as launching on one, and the strip says a pick is needed.
    expect(byText("Start in a worktree").textContent).not.toContain("default model");
    // One explanation, not two: the picker says the catalog is empty; the strip adds nothing contradictory.
    expect(strip().textContent).toContain("catalog is empty");
    expect(strip().textContent).not.toContain("Only the default model");
    expect(strip().textContent).not.toContain("pick one to launch");
  });

  it("names the model on the button once the catalog delivers one", async () => {
    lcppModels = [{ id: "local-7b", label: "local-7b" }];
    await render(["lcpp"]);
    await settle();
    expect(byText("Start in a worktree").disabled).toBe(false);
    expect(byText("Start in a worktree").textContent).toContain("local-7b");
  });

  it("keeps Details open over an invalid budget, and folds it again once fixed", async () => {
    await render(["claude"]);
    await expand("Details");
    const input = must(document.querySelector<HTMLInputElement>('input[inputmode="decimal"]'), "budget field");
    const type = (v: string) =>
      act(async () => {
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, v);
        input.dispatchEvent(new Event("input", { bubbles: true }));
      });
    await type("abc");
    await expand("Details"); // asks to fold it
    expect(secHead("Details").getAttribute("aria-expanded")).toBe("true");
    expect(byText("Start in a worktree").disabled).toBe(true);
    await type("5");
    expect(secHead("Details").getAttribute("aria-expanded")).toBe("false");
  });

  it("keeps the Copilot Free-plan note in view, folded or open, without re-reading the plan", async () => {
    usageReading = { ok: true, authed: true, plan: "Free" };
    localStorage.setItem("af.repo-model.copilot.app", "claude-sonnet-4.6");
    await render(["copilot"]);
    await settle();
    const note = t("launch.copilot_free_model");
    expect(secHead("Details").getAttribute("aria-expanded")).toBe("false");
    expect(document.body.textContent).toContain(note);
    // Opening and folding again keeps the one reading: a re-read here would fail (see the mock).
    await expand("Details");
    expect(document.body.textContent!.split(note)).toHaveLength(2);
    await expand("Details");
    await settle();
    expect(document.body.textContent).toContain(note);
    expect(usageSubs).toBe(1);
  });

  it("shows no Free-plan note for a paid Copilot plan, folded or open", async () => {
    usageReading = { ok: true, authed: true, plan: "Pro" };
    localStorage.setItem("af.repo-model.copilot.app", "claude-sonnet-4.6");
    await render(["copilot"]);
    await settle();
    const note = t("launch.copilot_free_model");
    expect(document.body.textContent).not.toContain(note);
    await expand("Details");
    expect(document.body.textContent).not.toContain(note);
  });
});

describe("LaunchModal branch mode", () => {
  // The picker is a wrapping grid, not a scroller: every connected kind must be in the
  // DOM (the old horizontal scroller clipped the 4th card and hid the rest).
  it("renders every available agent as a card", async () => {
    const kinds = ["claude", "codex", "cursor", "copilot", "kiro", "opencode"];
    await render(kinds);
    expect(document.querySelectorAll(".ui-seg.big .seg-btn")).toHaveLength(kinds.length);
  });

  // An expired workspace login is a notice here, as in the start hub (StartModal): claude stays
  // launchable (/login in the pane recovers). It shows for claude only, and only when expired.
  it("notes an expired Claude login while claude is the picked agent", async () => {
    await render(["claude"], { claudeExpired: true });
    expect(document.body.textContent).toContain("Claude login has expired");
    await render(["claude"], { claudeExpired: false });
    expect(document.body.textContent).not.toContain("Claude login has expired");
  });

  it("does not show the expired-login note for another agent kind", async () => {
    await render(["codex", "claude"], { claudeExpired: true });
    expect(document.body.textContent).not.toContain("Claude login has expired");
    await click(must([...document.querySelectorAll<HTMLButtonElement>(".ui-seg.big .seg-btn")].find((b) => b.textContent?.includes("Claude")), "claude card"));
    expect(document.body.textContent).toContain("Claude login has expired");
  });

  // Location is collapsed by default, so the summary line is the ONLY thing telling the user
  // what git is about to do. It has to describe the pending launch, not a stale default.
  it("summarises the pending location while the section is collapsed", async () => {
    await render();
    expect(summaryOf("Location")).toContain("New worktree");
    expect(summaryOf("Location")).toContain("main"); // the base branch it will fork from

    await expand("Location");
    await click(byText("Directly in this copy"));
    await expand("Location"); // collapse again
    expect(summaryOf("Location")).toContain("In this copy"); // launch.sum.direct
  });

  it("defaults to forking a new branch and never sets use_existing", async () => {
    await render();
    await click(byText("Start in a worktree"));
    const o = launchedWith();
    expect(o.worktree).toBe(true);
    expect(o.base).toBe("main"); // the base branch field
    expect(o.newBranch).toBe(""); // empty => the server mints temp/<slug>
    expect(o.useExisting).toBe(false);
  });

  it("checks an existing branch out instead of forking one", async () => {
    await render();
    await expand("Location");
    await click(byText("Existing branch"));
    await click(rowFor("develop"));
    await click(byText("Start in a worktree"));
    const o = launchedWith();
    expect(o.base).toBe("develop"); // the branch IS the start point
    expect(o.newBranch).toBe(""); // nothing is created
    expect(o.useExisting).toBe(true);
  });

  it("blocks the launch until a branch is picked", async () => {
    await render();
    await expand("Location");
    await click(byText("Existing branch"));
    expect(byText("Start in a worktree").disabled).toBe(true);
    await click(rowFor("develop"));
    expect(byText("Start in a worktree").disabled).toBe(false);
  });

  it("refuses to target a branch another working copy holds", async () => {
    await render();
    await expand("Location");
    await click(byText("Existing branch"));
    expect(rowFor("busy").disabled).toBe(true);
    expect(rowFor("busy").textContent).toContain("app@busy");
    await click(rowFor("busy"));
    expect(byText("Start in a worktree").disabled).toBe(true); // nothing got picked
  });

  // The working directory (Meta.Subdir): which folder INSIDE the working copy the agent
  // starts in. Getting it wrong means the agent runs in the wrong package of a monorepo,
  // which looks like a working launch until it edits the wrong files. It lives in Location —
  // where the launch happens — not in Advanced.
  it("launches in the folder picked from the tree", async () => {
    await render();
    await expand("Location");
    await click(byText("Browse"));
    await click(dirRow("console"));
    await click(dirRow("src"));
    await click(byText("Start in a worktree"));
    expect(launchedWith().subdir).toBe("console/src");
  });

  it("defaults to the working copy root and remembers the last folder per repo", async () => {
    await render();
    await click(byText("Start in a worktree"));
    expect(launchedWith().subdir).toBe(""); // untouched => the repo root

    act(() => root?.unmount());
    localStorage.setItem("af.repo-subdir.app", "console");
    root = createRoot(host);
    await render();
    await expand("Location");
    const input = must(document.querySelector<HTMLInputElement>(".subdirpick-input"), "working-directory input");
    expect(input.value).toBe("console");
  });

  // The first-prompt draft (launchDraft) survives closing, is kept per repository, and is
  // dropped only on a successful launch. Get it backwards and either the text is gone after a
  // trip to check the location, or an already-launched text squats on the next launch.
  it("keeps the typed first prompt per repo when the dialog is closed", async () => {
    await render();
    await type("直して");
    await reopen();
    expect(promptBox().value).toBe("直して");

    await reopen({ repo: "other" }); // never leaks into another repository
    expect(promptBox().value).toBe("");
  });

  it("drops the draft once the session has started", async () => {
    await render();
    await type("直して");
    expect(localStorage.getItem("af.launch-prompt.app")).toBe("直して"); // present before it is dropped
    await click(byText("Start in a worktree"));
    expect(launchedWith().prompt).toBe("直して");
    expect(localStorage.getItem("af.launch-prompt.app")).toBe(null);
    await reopen();
    expect(promptBox().value).toBe("");
  });

  // Coming back from a failed launch, the typed text is still needed (fix the collision and
  // press again).
  it("keeps the draft when the launch did not happen", async () => {
    onLaunch = vi.fn<Launch>(async () => ({ ok: false, conflict: "local" }));
    await render();
    await type("直して");
    await click(byText("Start in a worktree"));
    expect(promptBox().value).toBe("直して");
    await reopen();
    expect(promptBox().value).toBe("直して");
  });

  // A seed from a handoff proposal, a memo send or a work item outranks an earlier draft: the
  // caller opened the box having decided to start from that text.
  it("prefers a seeded first prompt over the stored draft", async () => {
    await render();
    await type("書きかけ");
    await reopen({ initialPrompt: "引き継ぎの本文" });
    expect(promptBox().value).toBe("引き継ぎの本文");
  });

  // An attachment (a pasted image) has the same lifetime as the text (lib/attachDraft): it
  // survives closing and is dropped only on a successful launch. Without it, a trip to check
  // the location lost the pasted screenshot alone — and because the text survived, the loss was
  // easy to miss.
  it("keeps a pasted image per repo when the dialog is closed", async () => {
    await render();
    await pasteImage("shot.png");
    expect(chips()).toHaveLength(1);
    await reopen();
    expect(chips()).toHaveLength(1);
    expect(chips()[0].querySelector("img.ma-thumb")?.getAttribute("src")).toMatch(/^blob:/);

    await reopen({ repo: "other" }); // never leaks into another repository
    expect(chips()).toHaveLength(0);
  });

  // A pasted screenshot is only a 56px chip; clicking it is how the user checks it is the right
  // one. The lightbox sits above the dialog, and Esc / its backdrop peel it alone — closing the
  // dialog with it would throw away the half-written launch.
  it("enlarges a pasted image in a lightbox that closes without closing the dialog", async () => {
    await render();
    await pasteImage("shot.png");
    await click(must(chips()[0].querySelector(".ma-thumb-btn"), "thumbnail button"));
    const box = () => document.querySelector(".mirror-lightbox.over-modal");
    expect(box()?.querySelector("img")?.getAttribute("src")).toMatch(/^blob:/);

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(box()).toBeNull();
    expect(onClose).not.toHaveBeenCalled();

    await click(must(chips()[0].querySelector(".ma-thumb-btn"), "thumbnail button"));
    await click(must(box(), "lightbox"));
    expect(box()).toBeNull();
    expect(onClose).not.toHaveBeenCalled();
  });

  it("hands the restored image to the launch, then forgets it", async () => {
    await render();
    await pasteImage("shot.png");
    await reopen(); // after a close and reopen, the launch still gets the same file
    await click(byText("Start in a worktree"));
    expect(launchedWith().images.map((f) => f.name)).toEqual(["shot.png"]);
    await reopen();
    expect(chips()).toHaveLength(0);
  });

  // The launch modal attaches what the mirror composer attaches: any file type, through paste,
  // drop and the + picker. Images only was a gap users hit with PDFs and logs (#1447).
  it("attaches non-image files by paste, drop and the + picker and hands them to the launch", async () => {
    await render();
    await pasteImage("spec.pdf", "application/pdf");
    expect(chips()).toHaveLength(1);
    expect(chips()[0].querySelector("img.ma-thumb")).toBeNull(); // icon + name, no thumbnail

    // dragover must be cancelled for the drop to land here at all; during it browsers report
    // the types but no files yet.
    expect((await drag("dragover", ["Files"])).defaultPrevented).toBe(true);
    const dropped = new File(["log"], "build.log", { type: "text/plain" });
    expect((await drag("drop", ["Files"], [dropped])).defaultPrevented).toBe(true);
    expect(chips()).toHaveLength(2);

    // Dragged text keeps the textarea's own drop (insert at the caret) and stages nothing.
    expect((await drag("dragover", ["text/plain"])).defaultPrevented).toBe(false);
    expect((await drag("drop", ["text/plain"])).defaultPrevented).toBe(false);
    expect(chips()).toHaveLength(2);

    const input = must(document.querySelector<HTMLInputElement>('input[type="file"]'), "file input");
    expect(input.hasAttribute("accept")).toBe(false);
    const picked = new File(["a,b"], "data.csv", { type: "text/csv" });
    Object.defineProperty(input, "files", { value: [picked], configurable: true });
    await act(async () => {
      input.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await settle();
    expect(chips()).toHaveLength(3);

    await reopen(); // a not-yet-uploaded non-image keeps its bytes across a close
    await click(byText("Start in a worktree"));
    // The restored Files must carry their bytes, not just their names: a 0-byte File
    // uploads "successfully" and hands the agent an empty file.
    const launched = launchedWith().images;
    expect(launched.map((f) => f.name)).toEqual(["spec.pdf", "build.log", "data.csv"]);
    expect(launched.map((f) => f.type)).toEqual(["application/pdf", "text/plain", "text/csv"]);
    expect(launched.map((f) => f.size)).toEqual([5, 3, 3]);
    expect(await Promise.all(launched.map((f) => f.text()))).toEqual(["BYTES", "log", "a,b"]);
  });

  // An agent without attachment support (shell) has nowhere to put a file: the drop is left
  // to the browser and nothing is staged.
  it("does not take a file drop for an agent without attachments", async () => {
    await render(["shell"]);
    expect(document.querySelector('input[type="file"]')).toBeNull(); // no + picker either
    expect((await drag("dragover", ["Files"])).defaultPrevented).toBe(false);
    const f = new File(["x"], "a.txt", { type: "text/plain" });
    expect((await drag("drop", ["Files"], [f])).defaultPrevented).toBe(false);
    expect(chips()).toHaveLength(0);
  });

  // Coming back from a failed launch, the attachment is needed as much as the text.
  it("keeps the pasted image when the launch did not happen", async () => {
    onLaunch = vi.fn<Launch>(async () => ({ ok: false, conflict: "local" }));
    await render();
    await pasteImage("shot.png");
    await click(byText("Start in a worktree"));
    expect(chips()).toHaveLength(1);
    await reopen();
    expect(chips()).toHaveLength(1);
  });

  it("offers to use the colliding branch when a LOCAL name is taken", async () => {
    onLaunch = vi
      .fn<Launch>()
      .mockResolvedValueOnce({ ok: false, conflict: "local" })
      .mockResolvedValueOnce({ ok: true });
    await render();
    await expand("Location");
    const input = document.querySelector<HTMLInputElement>('input[placeholder*="temporary name"]')!;
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      setter.call(input, "develop");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(byText("Start in a worktree"));
    // The fix button used to appear for remote collisions only, dead-ending local ones.
    const fix = byText("Work on that existing branch");
    expect(fix).toBeTruthy();
    await click(fix);
    const o = onLaunch.mock.calls[1][0] as LaunchOpts;
    expect(o.base).toBe("develop");
    expect(o.newBranch).toBe("");
    expect(o.useExisting).toBe(true);
  });
});

// terminalDriver === false (lcpp, ADR 0093 決定 2): no Terminal (CLI) route exists, ever, so the
// managed/terminal choice in Advanced must not be offered — the kind never gets a fake choice
// that can only fail. lcpp is now in repoLaunchKinds (stage 2's driver landed), so this modal
// really does render for it in practice, not just through the registry-driven `kinds` prop
// passed here directly.
describe("LaunchModal driver choice — terminalDriver gate", () => {
  it("offers Managed / Terminal for a kind with no terminalDriver override — negative control", async () => {
    await render(["codex"]);
    expect(byText("Managed (recommended)")).toBeTruthy();
    expect(byText("Terminal (CLI)")).toBeTruthy();
  });

  it("offers no driver choice at all for lcpp (terminalDriver: false)", async () => {
    await render(["lcpp"]);
    expect(buttons().find((b) => b.textContent?.includes("Terminal (CLI)"))).toBeUndefined();
    expect(buttons().find((b) => b.textContent?.includes("Managed (recommended)"))).toBeUndefined();
  });
});

// The kind catalog can land after the dialog is open. Facts it changes must show up without a
// reopen, and an untouched driver follows the new default instead of the one at mount.
describe("LaunchModal driver choice — catalog arriving late", () => {
  const claudeManaged = () =>
    parseKindCatalog({ kinds: [{ kind: "claude", managedOnly: false, managedDefault: true, mcpClient: "yes" }] });
  afterEach(() => act(() => setKindCatalog(null)));

  it("offers the choice and defaults an untouched driver to Managed", async () => {
    await render(["claude"]);
    expect(buttons().find((b) => b.textContent?.includes("Managed (recommended)"))).toBeUndefined();
    await act(async () => setKindCatalog(claudeManaged()));
    expect(byText("Managed (recommended)").classList.contains("seg-btn--active")).toBe(true);
    expect(byText("Terminal (CLI)").classList.contains("seg-btn--active")).toBe(false);
  });

  it("leaves a driver the user picked alone", async () => {
    await act(async () => setKindCatalog(claudeManaged()));
    await render(["claude"]);
    await click(byText("Terminal (CLI)"));
    await act(async () => setKindCatalog(parseKindCatalog({ kinds: [{ kind: "claude", managedOnly: false, managedDefault: true, mcpClient: "yes" }, { kind: "x", managedOnly: false, managedDefault: false, mcpClient: "no" }] })));
    expect(byText("Terminal (CLI)").classList.contains("seg-btn--active")).toBe(true);
  });
});

// lcpp has no CLI-picked own default (docs/log/109): unlike codex/opencode, launching it with
// no model selected used to POST model="" and fail the very first turn silently. These pin the
// Console-side half of the fix: launch stays disabled until the live catalog resolves, the
// picker auto-selects the sole entry rather than offering an empty Default, and the launch
// actually sends the auto-picked id.
describe("LaunchModal lcpp model requirement (docs/log/109)", () => {
  it("auto-picks the sole catalog model once it resolves and sends it on launch", async () => {
    lcppModels = [{ id: "m1", label: "m1" }];
    await render(["lcpp"]);
    await settle();
    expect(byText("Start in a worktree").disabled).toBe(false);
    await click(byText("Start in a worktree"));
    expect(launchedWith().model).toBe("m1");
  });

  it("offers no Default entry in the model combo for lcpp", async () => {
    lcppModels = [{ id: "m1", label: "m1" }];
    await render(["lcpp"]);
    await settle();
    const input = must(
      document.querySelector<HTMLInputElement>('input[aria-label="lcpp model"]'),
      "lcpp model combo input",
    );
    await act(async () => {
      input.focus();
    });
    const rows = [...document.querySelectorAll(".model-combo-item")].map((r) => r.textContent);
    expect(rows).toEqual(["m1"]);
  });

  it("stays disabled when the lcpp catalog comes back empty", async () => {
    lcppModels = [];
    await render(["lcpp"]);
    await settle();
    expect(byText("Start in a worktree").disabled).toBe(true);
  });

  it("does not require a model pick at all for a kind with a real CLI default — negative control", async () => {
    await render(["claude"]);
    await settle();
    expect(byText("Start in a worktree").disabled).toBe(false);
  });
});

// ADR 0128: the model-route picker appears only for a kind the live catalog says declares
// routes, defaults to "as before" (no route sent), and goes out as the chosen route id. A
// kind without routes, and a catalog that never arrived (an older Agent), show nothing.
describe("LaunchModal model route picker (ADR 0128)", () => {
  const catalog = (routes?: string[]) =>
    parseKindCatalog({
      kinds: [
        { kind: "claude", managedOnly: false, managedDefault: false, mcpClient: "yes", ...(routes ? { routes } : {}) },
        { kind: "codex", managedOnly: false, managedDefault: false, mcpClient: "yes" },
      ],
    });
  const routeSelect = () =>
    [...document.querySelectorAll<HTMLSelectElement>("select")].find((s) => [...s.options].some((o) => o.value === "anthropic-api"));
  afterEach(() => act(() => setKindCatalog(null)));

  it("shows no picker without a catalog, or for a kind that declares none", async () => {
    await render(["claude", "codex"]);
    expect(routeSelect()).toBeUndefined();
    await act(async () => setKindCatalog(catalog()));
    expect(routeSelect()).toBeUndefined();
  });

  it("offers the declared routes, sends nothing by default and the id once chosen", async () => {
    await act(async () => setKindCatalog(catalog(["anthropic-api", "anthropic-subscription"])));
    await render(["claude", "codex"]);
    const sel = must(routeSelect(), "the route picker");
    expect(sel.value).toBe("");
    expect([...sel.options].map((o) => o.value)).toEqual(["", "anthropic-api", "anthropic-subscription"]);
    await click(byText("Start in a worktree"));
    expect(launchedWith().route).toBeUndefined();
  });

  it("sends the chosen route id", async () => {
    await act(async () => setKindCatalog(catalog(["anthropic-api", "anthropic-subscription"])));
    await render(["claude", "codex"]);
    const sel = must(routeSelect(), "the route picker");
    await act(async () => {
      sel.value = "anthropic-api";
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await click(byText("Start in a worktree"));
    expect(launchedWith().route).toBe("anthropic-api");
  });

  it("drops the choice when the kind changes", async () => {
    await act(async () => setKindCatalog(catalog(["anthropic-api"])));
    await render(["claude", "codex"]);
    const sel = must(routeSelect(), "the route picker");
    await act(async () => {
      sel.value = "anthropic-api";
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await click(byText("Codex"));
    expect(routeSelect()).toBeUndefined();
    await click(byText("Claude"));
    expect(must(routeSelect(), "the route picker").value).toBe("");
  });
});

// The bedrock route (ADR 0128 P2b): offered only when the member has a Settings AWS profile marked
// as a model route, and it goes out with the profile and the region as `<id>@<region>`.
describe("LaunchModal bedrock route (ADR 0128 P2b)", () => {
  const catalog = parseKindCatalog({
    kinds: [{ kind: "claude", managedOnly: false, managedDefault: false, mcpClient: "yes", routes: ["anthropic-api", "anthropic-subscription", "bedrock"] }],
  });
  const routeSelect = () =>
    [...document.querySelectorAll<HTMLSelectElement>("select")].find((s) => [...s.options].some((o) => o.value === "anthropic-api"));
  const profileSelect = () =>
    [...document.querySelectorAll<HTMLSelectElement>("select")].find((s) => [...s.options].some((o) => o.value === "p-marked"));
  const regionInput = () => document.querySelector<HTMLInputElement>('input[aria-label="Region"]');
  const modelInput = () => document.querySelector<HTMLInputElement>('input[aria-label="Bedrock model ID"]');
  const type = async (el: HTMLInputElement, value: string) =>
    act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });
  const choose = async (sel: HTMLSelectElement, value: string) =>
    act(async () => {
      sel.value = value;
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
  afterEach(() => {
    ssmProfiles = [];
    act(() => setKindCatalog(null));
  });

  it("keeps the remembered effort and Bedrock id while the saved Bedrock default's profiles are still loading", async () => {
    ssmProfiles = [{ id: "p-marked", label: "Bedrock invoke", region: "eu-west-1", modelRoute: true }];
    await act(async () => setKindCatalog(catalog));
    const { setSettings, getSettings } = await import("../../lib/settings.ts");
    const { rememberBedrockModel } = await import("./bedrockProfiles.ts");
    setSettings({ agentLaunchDefaults: { ...getSettings().agentLaunchDefaults, claude: { ...getSettings().agentLaunchDefaults.claude, route: { route: "bedrock", connection: "p-marked@" } } } });
    rememberBedrockModel("p-marked", "eu.anthropic.claude-opus-5-5");
    // What the previous Bedrock launch wrote for this repository.
    localStorage.setItem("af.repo-model.app", "eu.anthropic.claude-opus-5-5");
    localStorage.setItem("af.repo-effort.claude.app", "high");
    let release!: (v: unknown) => void;
    ssmGate = new Promise((r) => (release = r));
    try {
      await render(["claude"]);
      await settle();
      // The profiles have not answered: the Bedrock default is not yet the route, so nothing launches.
      expect(byText("Start in a worktree").disabled).toBe(true);
      await click(byText("Start in a worktree"));
      expect(onLaunch).not.toHaveBeenCalled();
      await act(async () => release(undefined));
      ssmGate = null;
      await settle();
      expect(modelInput()?.value).toBe("eu.anthropic.claude-opus-5-5");
      await click(byText("Start in a worktree"));
      expect(launchedWith().route).toBe("bedrock");
      expect(launchedWith().model).toBe("eu.anthropic.claude-opus-5-5");
      expect(launchedWith().effort).toBe("high");
    } finally {
      ssmGate = null;
      setSettings({ agentLaunchDefaults: { ...getSettings().agentLaunchDefaults, claude: { ...getSettings().agentLaunchDefaults.claude, route: undefined } } });
    }
  });

  it("is not offered without a marked profile", async () => {
    ssmProfiles = [{ id: "p-plain", label: "Plain", region: "us-east-1" }];
    await act(async () => setKindCatalog(catalog));
    await render(["claude"]);
    await settle();
    const sel = must(routeSelect(), "the route picker");
    expect([...sel.options].map((o) => o.value)).toEqual(["", "anthropic-api", "anthropic-subscription"]);
  });

  it("offers marked profiles only and sends <id>@<region>", async () => {
    ssmProfiles = [
      { id: "p-plain", label: "Plain", region: "us-east-1" },
      { id: "p-marked", label: "Bedrock invoke", region: "eu-west-1", modelRoute: true },
    ];
    await act(async () => setKindCatalog(catalog));
    await render(["claude"]);
    await settle();
    const sel = must(routeSelect(), "the route picker");
    expect([...sel.options].map((o) => o.value)).toContain("bedrock");
    await choose(sel, "bedrock");
    const prof = must(profileSelect(), "the profile picker");
    expect([...prof.options].map((o) => o.value)).toEqual(["p-marked"]);
    await type(must(modelInput(), "the model id field"), "eu.anthropic.claude-opus-5-5");
    await click(byText("Start in a worktree"));
    expect(launchedWith().route).toBe("bedrock");
    expect(launchedWith().routeConnection).toBe("p-marked@");
  });

  it("sends a region override, and refuses to start on one that is not a region", async () => {
    ssmProfiles = [{ id: "p-marked", label: "Bedrock invoke", region: "eu-west-1", modelRoute: true }];
    await act(async () => setKindCatalog(catalog));
    await render(["claude"]);
    await settle();
    await choose(must(routeSelect(), "the route picker"), "bedrock");
    await type(must(modelInput(), "the model id field"), "eu.anthropic.claude-opus-5-5");
    const region = must(regionInput(), "the region field");
    await act(async () => {
      const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      set.call(region, "not a region");
      region.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(byText("Start in a worktree").disabled).toBe(true);
    await act(async () => {
      const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      set.call(region, "us-west-2");
      region.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(byText("Start in a worktree"));
    expect(launchedWith().routeConnection).toBe("p-marked@us-west-2");
  });

  it("keeps the launch refused, with the fields in the strip, while the Bedrock model id or region would refuse it", async () => {
    localStorage.removeItem("af.launch-open.details");
    ssmProfiles = [{ id: "p-marked", label: "Bedrock invoke", region: "eu-west-1", modelRoute: true }];
    await act(async () => setKindCatalog(catalog));
    await render(["claude"]);
    await settle();
    await choose(must(routeSelect(), "the route picker"), "bedrock");
    await type(must(modelInput(), "the model id field"), "");
    expect(byText("Start in a worktree").disabled).toBe(true);
    // The route chip carries the reason: warned, and described by the line that says what to fix.
    const chip = must(document.querySelector<HTMLElement>(".run-chip--route"), "route chip");
    expect(chip.className).toContain("run-chip--warn");
    expect(document.getElementById(chip.getAttribute("aria-describedby")!)?.textContent).toMatch(/Bedrock|model/i);
    expect(secHead("Details").getAttribute("aria-expanded")).toBe("false");
    await type(must(modelInput(), "the model id field"), "eu.anthropic.claude-opus-5-5");
    expect(byText("Start in a worktree").disabled).toBe(false);
    await type(must(regionInput(), "the region field"), "not a region");
    expect(byText("Start in a worktree").disabled).toBe(true);
    await type(must(regionInput(), "the region field"), "us-west-2");
    expect(byText("Start in a worktree").disabled).toBe(false);
    expect(chip.className).not.toContain("run-chip--warn");
    expect(chip.getAttribute("aria-describedby")).toBeNull();
    expect(secHead("Details").getAttribute("aria-expanded")).toBe("false");
  });

  it("sends no connection for a route that takes none", async () => {
    ssmProfiles = [{ id: "p-marked", label: "Bedrock invoke", region: "eu-west-1", modelRoute: true }];
    await act(async () => setKindCatalog(catalog));
    await render(["claude"]);
    await settle();
    await choose(must(routeSelect(), "the route picker"), "anthropic-api");
    await click(byText("Start in a worktree"));
    expect(launchedWith().route).toBe("anthropic-api");
    expect(launchedWith().routeConnection).toBeUndefined();
  });

  // #3587: Claude Code maps an alias to an id of its own and walks a fallback ladder on a denial, so
  // the route takes only a Bedrock id, typed here, and sends it as the session's model.
  it("requires a Bedrock model id, sends it as the model and remembers it per profile", async () => {
    ssmProfiles = [{ id: "p-marked", label: "Bedrock invoke", region: "eu-west-1", modelRoute: true }];
    await act(async () => setKindCatalog(catalog));
    await render(["claude"]);
    await settle();
    expect(modelInput()).toBeNull();
    await choose(must(routeSelect(), "the route picker"), "bedrock");
    const field = must(modelInput(), "the model id field");
    expect(field.value).toBe("");
    expect(byText("Start in a worktree").disabled).toBe(true);
    for (const bad of ["opus", "claude-opus-4-6", "arn:aws:bedrock:eu-west-1:000000000000:inference-profile/eu.anthropic.claude-opus-5-5", "eu.anthropic.claude-opus-5-5[1m]", "eu.amazon.nova-pro-v1:0"]) {
      await type(field, bad);
      expect(byText("Start in a worktree").disabled).toBe(true);
      expect(field.getAttribute("aria-invalid")).toBe("true");
    }
    await type(field, " eu.anthropic.claude-opus-5-5 ");
    expect(byText("Start in a worktree").disabled).toBe(false);
    await click(byText("Start in a worktree"));
    expect(launchedWith().route).toBe("bedrock");
    expect(launchedWith().model).toBe("eu.anthropic.claude-opus-5-5");
    expect(localStorage.getItem("af.bedrock-model.p-marked")).toBe("eu.anthropic.claude-opus-5-5");
  });

  it("starts from the id last launched with the profile", async () => {
    ssmProfiles = [{ id: "p-marked", label: "Bedrock invoke", region: "eu-west-1", modelRoute: true }];
    localStorage.setItem("af.bedrock-model.p-marked", "anthropic.claude-sonnet-4-5-20250929-v1:0");
    await act(async () => setKindCatalog(catalog));
    await render(["claude"]);
    await settle();
    await choose(must(routeSelect(), "the route picker"), "bedrock");
    expect(must(modelInput(), "the model id field").value).toBe("anthropic.claude-sonnet-4-5-20250929-v1:0");
    await click(byText("Start in a worktree"));
    expect(launchedWith().model).toBe("anthropic.claude-sonnet-4-5-20250929-v1:0");
  });
});

// opencode on the bedrock route (ADR 0128 P2c): the model picker offers the route's own models
// only (opencode's default is not on the route), the dialog does not start until one is picked,
// and without the route those models are not offered at all.
describe("LaunchModal opencode on the bedrock route (ADR 0128 P2c)", () => {
  const catalog = parseKindCatalog({
    kinds: [{ kind: "opencode", managedOnly: false, managedDefault: false, mcpClient: "yes", routes: ["bedrock"] }],
  });
  const routeSelect = () =>
    [...document.querySelectorAll<HTMLSelectElement>("select")].find((s) => [...s.options].some((o) => o.value === "bedrock"));
  const choose = async (sel: HTMLSelectElement, value: string) =>
    act(async () => {
      sel.value = value;
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
  const modelInput = () => must(document.querySelector<HTMLInputElement>('input[role="combobox"]'), "the model picker");
  const listed = async (): Promise<string[]> => {
    const input = modelInput();
    await act(async () => {
      input.focus();
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }));
    });
    const rows = [...document.querySelectorAll<HTMLElement>('[role="option"]')].map((o) => o.textContent ?? "");
    await act(async () => {
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
      input.blur();
    });
    return rows;
  };
  beforeEach(() => {
    opencodeModels = [
      { id: "opencode/big-pickle", label: "opencode/big-pickle" },
      { id: "amazon-bedrock/anthropic.claude-sonnet-4-6", label: "amazon-bedrock/anthropic.claude-sonnet-4-6" },
    ];
    ssmProfiles = [{ id: "p-marked", label: "Bedrock invoke", region: "eu-west-1", modelRoute: true }];
  });
  afterEach(() => {
    ssmProfiles = [];
    opencodeModels = [];
    act(() => setKindCatalog(null));
  });

  it("never offers the route's models without a marked profile", async () => {
    ssmProfiles = [];
    await act(async () => setKindCatalog(catalog));
    await render(["opencode"]);
    await settle();
    expect(routeSelect()).toBeUndefined();
    const plain = await listed();
    expect(plain.some((t) => t.includes("big-pickle"))).toBe(true);
    expect(plain.some((t) => t.includes("amazon-bedrock/"))).toBe(false);
  });

  it("offers the route on a Managed launch too, and starts it there", async () => {
    // A kind that can run Managed: the default launch is Managed, where a routed session runs on
    // a server of its own (#4004).
    await act(async () =>
      setKindCatalog(parseKindCatalog({ kinds: [{ kind: "opencode", managedOnly: false, managedDefault: true, mcpClient: "yes", routes: ["bedrock"] }] })),
    );
    await render(["opencode"]);
    await settle();
    // No route chosen: the route's models stay out of the menu.
    const plain = await listed();
    expect(plain.some((t) => t.includes("amazon-bedrock/"))).toBe(false);
    expect(plain.some((t) => t.includes("big-pickle"))).toBe(true);
    await choose(must(routeSelect(), "the route picker on Managed"), "bedrock");
    await settle();
    expect((await listed()).some((t) => t.includes("amazon-bedrock/"))).toBe(true);
    const input = modelInput();
    await act(async () => {
      input.focus();
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }));
    });
    await click(must([...document.querySelectorAll<HTMLElement>('[role="option"]')].find((o) => o.textContent?.includes("amazon-bedrock/")), "the bedrock model"));
    await click(byText("Start in a worktree"));
    expect(launchedWith().route).toBe("bedrock");
    expect(launchedWith().driver).toBe("managed");
  });

  it("refuses the launch, naming why in the strip, until a model the route can run is picked", async () => {
    localStorage.removeItem("af.launch-open.details");
    await act(async () => setKindCatalog(catalog));
    localStorage.setItem("af.repo-model.opencode.app", "opencode/big-pickle");
    await render(["opencode"]);
    await settle();
    await choose(must(routeSelect(), "the route picker"), "bedrock");
    await settle();
    expect(byText("Start in a worktree").disabled).toBe(true);
    expect(secHead("Details").getAttribute("aria-expanded")).toBe("false");
    expect(must(document.querySelector('[data-testid="run-strip"]'), "strip").textContent).toContain("Pick an Amazon Bedrock model");
    // The model the route cannot run is dropped with a short notice naming it.
    expect(document.querySelector(".run-strip-notice")?.textContent).toContain("opencode/big-pickle");
  });

  it("offers the route's models only on the route, and needs one to start", async () => {
    await act(async () => setKindCatalog(catalog));
    await render(["opencode"]);
    await settle();
    const before = await listed();
    expect(before.some((t) => t.includes("big-pickle"))).toBe(true);
    expect(before.some((t) => t.includes("amazon-bedrock/"))).toBe(false);

    await choose(must(routeSelect(), "the route picker"), "bedrock");
    await settle();
    expect(byText("Start in a worktree").disabled).toBe(true);
    const on = await listed();
    expect(on.some((t) => t.includes("big-pickle"))).toBe(false);
    expect(on.some((t) => t.includes("amazon-bedrock/anthropic.claude-sonnet-4-6"))).toBe(true);

    const input = modelInput();
    await act(async () => {
      input.focus();
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }));
    });
    await click(must([...document.querySelectorAll<HTMLElement>('[role="option"]')].find((o) => o.textContent?.includes("amazon-bedrock/")), "the bedrock model"));
    await click(byText("Start in a worktree"));
    expect(launchedWith().route).toBe("bedrock");
    expect(launchedWith().routeConnection).toBe("p-marked@");
    expect(launchedWith().model).toBe("amazon-bedrock/anthropic.claude-sonnet-4-6");
  });
});


// ADR 0114 P3: the tenant's effective launch defaults prefill the dialog, from the cached CP answer.
describe("tenant launch defaults", () => {
  const useUIStateStore = uiStore;
  afterEach(() => useUIStateStore.setState({ state: {} }));

  const ops = { grafana_url: "", aws_sso_start_url: "", aws_account_id: "", aws_role_name: "" };
  const answer = (agent: string, extra: Record<string, unknown> = {}) => ({
    effective: { launch: { agent, model: "gpt-x", driver: "", permission_mode: "", budget_usd: 5 }, repos: [], ops },
    defaults: { agent, launch: { agent: "", model: "gpt-x", driver: "", permission_mode: "", budget_usd: 5 } },
    ...extra,
  });

  it("starts on the tenant's agent, model and budget, and launches with them", async () => {
    useUIStateStore.setState({ state: answer("codex") as never });
    await render(["claude", "codex"]);
    await click(byText("Start in a worktree"));
    const o = launchedWith();
    expect(o.kind).toBe("codex");
    expect(o.model).toBe("gpt-x");
    expect(o.spendCapUsd).toBe(5);
  });

  it("fills in a late CP answer for untouched fields only (C2)", async () => {
    await render(["claude", "codex"]);
    expect(buttons().find((b) => b.classList.contains("seg-btn--active") && /claude/i.test(b.textContent || ""))).toBeTruthy();
    await act(async () => useUIStateStore.setState({ state: answer("codex") as never }));
    await click(byText("Start in a worktree"));
    expect(launchedWith().kind).toBe("codex");
    expect(launchedWith().model).toBe("gpt-x");
    expect(launchedWith().spendCapUsd).toBe(5);
  });

  it("leaves a budget the member typed when the late answer arrives (C2)", async () => {
    await render(["claude", "codex"]);
    const input = must(document.querySelector<HTMLInputElement>('input[inputmode="decimal"]'), "budget field");
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "9");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => useUIStateStore.setState({ state: answer("claude") as never }));
    await click(byText("Start in a worktree"));
    expect(launchedWith().spendCapUsd).toBe(9);
  });

  it("re-resolves the tenant's agent when the kinds list lands after a cached answer (C2)", async () => {
    useUIStateStore.setState({ state: answer("codex") as never });
    await render([]);
    await act(async () => root!.render(<LaunchModal access={ACCESS} repo="app" branch="main" kinds={["claude", "codex"]} onClose={onClose} onLaunch={onLaunch} />));
    await click(byText("Start in a worktree"));
    expect(launchedWith().kind).toBe("codex");
  });

  it("does not save a default the CP would refuse, and reports a failed save (C6)", async () => {
    await render(["claude"]);
    const save = byText("Use as my default");
    expect(save.disabled).toBe(false);
    patchResult = false;
    await click(save);
    expect(document.querySelector('[role="alert"]')?.textContent).toContain("Could not save");
  });

  it("keeps a permission the member set by hand when a late answer changes the agent (C8)", async () => {
    await render(["claude", "cursor"]);
    // The permission selector is only offered for kinds with that choice; set it through the UI.
    const sel = must([...document.querySelectorAll<HTMLSelectElement>("select")].find((e) => [...e.options].some((o) => o.value === "skip")), "permission select");
    await act(async () => {
      sel.value = "ask";
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => useUIStateStore.setState({ state: answer("cursor", { effective: { launch: { agent: "cursor", model: "", driver: "", permission_mode: "skip", budget_usd: null }, repos: [], ops } }) as never }));
    await click(byText("Start in a worktree"));
    expect(launchedWith().kind).toBe("cursor");
    expect(launchedWith().skipPermissions).toBe(false);
  });

  it("a budget saved by the member follows them to another agent card (C9)", async () => {
    useUIStateStore.setState({
      state: answer("claude", { launch: { agent: "codex", model: "", driver: "", permission_mode: "", budget_usd: 0 }, effective: { launch: { agent: "claude", model: "", driver: "", permission_mode: "", budget_usd: 0 }, repos: [], ops } }) as never,
    });
    await render(["claude", "codex"]);
    await click(byText("Codex"));
    await click(byText("Start in a worktree"));
    expect(launchedWith().kind).toBe("codex");
    expect(launchedWith().spendCapUsd).toBe(0);
  });

  it("will not save an unparsable budget as unset (C10)", async () => {
    await render(["claude"]);
    const input = must(document.querySelector<HTMLInputElement>('input[inputmode="decimal"]'), "budget field");
    for (const bad of ["abc", "1.234", "100001"]) {
      await act(async () => {
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, bad);
        input.dispatchEvent(new Event("input", { bubbles: true }));
      });
      expect(byText("Use as my default").disabled).toBe(true);
    }
  });

  it("keeps a budget typed while the kinds list was still resolving (C11)", async () => {
    useUIStateStore.setState({ state: answer("codex") as never });
    await render([]);
    const input = must(document.querySelector<HTMLInputElement>('input[inputmode="decimal"]'), "budget field");
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "9");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => root!.render(<LaunchModal access={ACCESS} repo="app" branch="main" kinds={["codex"]} onClose={onClose} onLaunch={onLaunch} />));
    await click(byText("Start in a worktree"));
    expect(launchedWith().kind).toBe("codex");
    expect(launchedWith().spendCapUsd).toBe(9);
  });

  it("does not move to another agent under a model picked by hand (C12)", async () => {
    await render(["claude", "codex"]);
    const picked = byText("Sonnet");
    await click(picked);
    await act(async () => useUIStateStore.setState({ state: answer("codex") as never }));
    await click(byText("Start in a worktree"));
    expect(launchedWith().kind).toBe("claude");
    expect(launchedWith().model).toMatch(/sonnet/i);
  });

  it("changes nothing without a cached answer", async () => {
    await render(["claude", "codex"]);
    await click(byText("Start in a worktree"));
    expect(launchedWith().kind).toBe("claude");
    expect(launchedWith().spendCapUsd).toBeUndefined();
  });
});
