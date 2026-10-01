// A work-item launch names its branch through the Agent's resolver (ADR 0103 decision 8): the
// modal is prefilled with the resolver's name and base, says where the base came from, shows
// the advisory warnings, and offers to read a pending Bitbucket model again. An Agent without
// the resolver leaves the Console's own branchForItem suggestion in place, and a field the
// person already edited is never overwritten by a late answer.
import "fake-indexeddb/auto";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Mock } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

type Json = Record<string, unknown>;
let branchName: (body: Json) => Promise<Json> = async () => ({ error: { code: "http_404" } });
let check: (body: Json) => Json = () => ({ warnings: [] });
let refresh: () => Promise<Json> = async () => ({ kinds: [] });
const gitflowState: Json = {
  current: {},
  prefill: { production: "main", development: "develop", feature: "feature/", bugfix: "", release: "release/", hotfix: "hotfix/", versiontag: "" },
  local: ["main", "develop"],
  origin: ["main", "develop"],
  native: false,
  committed: [],
};
const apiMock = vi.fn(async (url: string): Promise<Json> => {
  if (url.includes("branch-rule?refresh=1")) return refresh();
  if (url.includes("branch-rule")) return { kinds: [] };
  if (url.endsWith("/gitflow")) return gitflowState;
  return { branches: [] };
});
const apiJSONMock = vi.fn(async (url: string, _method: string, body: Json): Promise<Json> => {
  if (url.endsWith("/branch-name")) return branchName(body);
  if (url.endsWith("/branch-name/check")) return check(body);
  if (url.endsWith("/gitflow/init")) return { written: ["gitflow.branch.develop"], state: gitflowState };
  return {};
});

vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => "",
  getUser: () => "",
  api: (...a: unknown[]) => apiMock(...(a as [string])),
  apiJSON: (...a: unknown[]) => apiJSONMock(...(a as [string, string, Json])),
  repoPromptTemplates: async () => ({ groups: [] }),
  errText: (e: { message?: string }) => e?.message ?? "",
  errDetail: (e: { message?: string }) => e?.message ?? "",
  isTransientErr: () => false,
}));

const { LaunchModal } = await import("./LaunchModal.tsx");
const { launchBranchTiming } = await import("./useLaunchBranchName.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { resetAttachDraftDB } = await import("../../lib/attachDraft.ts");
import type { LaunchOpts, LaunchResult } from "./LaunchModal.tsx";

type Launch = (o: LaunchOpts) => Promise<LaunchResult>;

let root: Root | null = null;
let host: HTMLDivElement;
let onLaunch: Mock<Launch>;

const item = { provider: "github", key: "acme/web#45", title: "Empty list after login", type: "Bug", labels: ["ui"] };

function must<T>(el: T | undefined | null, what: string): T {
  if (!el) throw new Error(`not in the DOM: ${what}`);
  return el;
}
const buttons = () => [...document.querySelectorAll<HTMLButtonElement>("button")];
const byText = (t: string) => must(buttons().find((b) => b.textContent?.includes(t)), `button "${t}"`);
const secHead = () =>
  must(
    buttons().find((b) => b.classList.contains("launch-sec-head") && b.querySelector(".launch-sec-label")?.textContent === "Location"),
    "Location section",
  );
const field = (label: string) =>
  must(
    [...document.querySelectorAll("label.ui-field")].find((l) => l.querySelector(".ui-field-label")?.textContent === label)?.querySelector("input"),
    `field "${label}"`,
  );
const nameField = () => field("Branch name (optional)");
const baseField = () => field("Base branch");
const warnings = () => [...document.querySelectorAll(".launch-branch-warns li")].map((l) => l.textContent?.trim());

async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) {
    if (vi.isFakeTimers()) await act(async () => void (await vi.advanceTimersByTimeAsync(0)));
    else await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
  }
}
// Fake time only: moves the clock, running the re-ask timers and the answers they wait for.
async function advance(ms: number): Promise<void> {
  await act(async () => void (await vi.advanceTimersByTimeAsync(ms)));
  await settle();
}
// The check waits for typing to stop.
async function afterCheck(): Promise<void> {
  await act(async () => void (await new Promise((r) => setTimeout(r, 450))));
  await settle();
}
async function click(el: Element): Promise<void> {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
  await settle();
}
async function typeInto(el: HTMLInputElement, text: string): Promise<void> {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function render(withItem = true, repo = "web", it: typeof item = item, prompt?: string): Promise<void> {
  await act(async () => {
    root!.render(
      // The app wraps every dialog in it; the Git Flow dialog toasts on save.
      <ToastProvider>
        <LaunchModal
          repo={repo}
          branch="main"
          kinds={["claude"]}
          initialNewBranch="feature/issue-45"
          initialPrompt={prompt}
          workItem={withItem ? it : undefined}
          onClose={() => {}}
          onLaunch={onLaunch}
        />
      </ToastProvider>,
    );
  });
  await settle();
}

const resolved = (over: Json = {}): Json => ({
  name: "fix/45-empty-list-after-login",
  name_empty: false,
  base: "develop",
  base_branch: "develop",
  kind: "bugfix",
  provisional: false,
  warnings: [],
  sources: { base: "repository: gitflow gitflow.branch.develop" },
  ...over,
});

beforeEach(() => {
  launchBranchTiming.reaskAtMs = [20, 40, 60];
  localStorage.clear();
  globalThis.indexedDB = new IDBFactory();
  resetAttachDraftDB();
  branchName = async () => resolved();
  check = () => ({ warnings: [] });
  refresh = async () => ({ kinds: [] });
  apiMock.mockClear();
  apiJSONMock.mockClear();
  onLaunch = vi.fn<Launch>(async () => ({ ok: true }));
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  vi.useRealTimers();
  host.remove();
  root = null;
});

describe("work-item launch through the branch-name resolver", () => {
  it("prefills the resolver's name and base, says where the base came from, and launches with them", async () => {
    await render();
    expect(apiJSONMock.mock.calls.find((c) => c[0] === "api/repos/web/branch-name")?.[2]).toEqual({ item });
    await click(secHead());
    expect(nameField().value).toBe("fix/45-empty-list-after-login");
    expect(baseField().value).toBe("develop");
    expect(document.querySelector(".launch-base-source")?.textContent).toContain("gitflow.branch.develop");
    await click(byText("Start"));
    expect(onLaunch.mock.calls[0][0]).toMatchObject({ newBranch: "fix/45-empty-list-after-login", base: "develop" });
  });

  it("offers Initialize Git Flow when origin has develop and nothing is declared, then re-resolves", async () => {
    let declared = false;
    branchName = async () =>
      declared
        ? resolved()
        : resolved({ base: "head", base_branch: "main", sources: { base: "builtin: builtin" }, gitflow: "suggest" });
    await render();
    await click(secHead());
    expect(baseField().value).toBe("main");
    expect(document.querySelector(".launch-gitflow-suggest")).toBeTruthy();
    await click(byText("Initialize Git Flow…"));
    expect(apiMock.mock.calls.some((c) => c[0] === "api/repos/web/gitflow")).toBe(true);
    declared = true;
    const save = must(
      buttons().find((b) => b.getAttribute("type") === "submit" && b.textContent === "Initialize"),
      "the dialog's Initialize",
    );
    await click(save);
    expect(apiJSONMock.mock.calls.some((c) => c[0] === "api/repos/web/gitflow/init")).toBe(true);
    // Nothing initialises by itself: the write came from the press, and the base follows it.
    expect(baseField().value).toBe("develop");
    expect(document.querySelector(".launch-gitflow-suggest")).toBeNull();
  });

  it("asks again after a provisional answer and takes the English slug", async () => {
    let asks = 0;
    branchName = async () =>
      ++asks === 1 ? resolved({ name: "fix/45", provisional: true }) : resolved({ sources: { slug: "ai" } });
    launchBranchTiming.reaskAtMs = [300];
    await render();
    await click(secHead());
    expect(nameField().value).toBe("fix/45");
    expect(asks).toBe(1);
    await act(async () => void (await new Promise((r) => setTimeout(r, 350))));
    await settle();
    expect(nameField().value).toBe("fix/45-empty-list-after-login");
    expect(asks).toBe(2);
  });

  describe("on the real back-off schedule (fake time)", () => {
    let asks: number;
    let start: number;
    const hint = () => document.querySelector(".launch-branch-provisional");
    beforeEach(() => {
      launchBranchTiming.reaskAtMs = [8000, 20000, 45000];
      vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
      start = Date.now();
      asks = 0;
      // The English slug arrives about 30 s after the first ask, as measured on a real Agent.
      branchName = async () => {
        asks++;
        return Date.now() - start < 30_000 ? resolved({ name: "fix/45", provisional: true }) : resolved({ sources: { slug: "ai" } });
      };
    });

    it("keeps asking with back-off, takes the English name once it is ready, then stops", async () => {
      await render();
      await click(secHead());
      expect(nameField().value).toBe("fix/45");
      expect(hint()?.textContent).toContain("provisional name");
      await advance(8000);
      await advance(12_000);
      expect(asks).toBe(3);
      expect(nameField().value).toBe("fix/45");
      await advance(25_000);
      expect(asks).toBe(4);
      expect(nameField().value).toBe("fix/45-empty-list-after-login");
      expect(hint()).toBeNull();
      await advance(300_000);
      expect(asks).toBe(4);
    });

    it("stops asking once the person edits the name, and the hint goes", async () => {
      await render();
      await click(secHead());
      await advance(8000);
      expect(asks).toBe(2);
      await typeInto(nameField(), "fix/45-mine");
      await settle();
      expect(hint()).toBeNull();
      await advance(300_000);
      expect(asks).toBe(2);
      expect(nameField().value).toBe("fix/45-mine");
    });

    it("stops asking when the modal closes, leaving no timer of its own behind", async () => {
      // What the rest of the modal leaves behind on close, with no re-ask scheduled.
      const provisional = branchName;
      branchName = async () => resolved();
      await render();
      act(() => root!.unmount());
      const baseline = vi.getTimerCount();
      vi.clearAllTimers();
      root = createRoot(host);
      branchName = provisional;
      asks = 0;
      await render();
      expect(asks).toBe(1);
      act(() => root!.unmount());
      root = null;
      expect(vi.getTimerCount()).toBe(baseline);
      await act(async () => void (await vi.advanceTimersByTimeAsync(300_000)));
      expect(asks).toBe(1);
    });

    it("an answer for the last item never lands on the next one", async () => {
      await render();
      await advance(8000);
      expect(asks).toBe(2);
      // The next item's resolver answers a final name at once; the last item's schedule must
      // not ask (or apply) anything for it afterwards.
      const other = { ...item, key: "acme/web#46", title: "Another one" };
      branchName = async (b) => {
        asks++;
        return (b.item as Json).key === "acme/web#46" ? resolved({ name: "fix/46-another-one" }) : resolved({ name: "fix/45-late" });
      };
      await render(true, "web", other);
      await click(secHead());
      expect(nameField().value).toBe("fix/46-another-one");
      await advance(300_000);
      expect(asks).toBe(3);
      expect(nameField().value).toBe("fix/46-another-one");
    });

    it("after the last re-ask a provisional name stays, and no longer says it may change", async () => {
      branchName = async () => {
        asks++;
        return resolved({ name: "fix/45", provisional: true });
      };
      await render();
      await click(secHead());
      await advance(45_000);
      expect(asks).toBe(4);
      expect(nameField().value).toBe("fix/45");
      expect(hint()).toBeNull();
      await advance(300_000);
      expect(asks).toBe(4);
    });
  });

  it("does not ask again after a final answer", async () => {
    await render();
    await act(async () => void (await new Promise((r) => setTimeout(r, 40))));
    await settle();
    expect(apiJSONMock.mock.calls.filter((c) => c[0] === "api/repos/web/branch-name")).toHaveLength(1);
  });

  it("keeps the Console's own suggestion when the Agent has no resolver", async () => {
    branchName = async () => ({ error: { code: "http_404", message: "404 page not found" } });
    await render();
    await click(secHead());
    expect(nameField().value).toBe("feature/issue-45");
    expect(baseField().value).toBe("main");
    expect(document.querySelector(".launch-base-source")).toBeNull();
  });

  it("never overwrites a field the person edited before the answer arrived", async () => {
    let answer: (v: Json) => void = () => {};
    branchName = () => new Promise((r) => (answer = r));
    await render();
    await click(secHead());
    await typeInto(nameField(), "feature/my-own-name");
    await act(async () => answer(resolved()));
    await settle();
    expect(nameField().value).toBe("feature/my-own-name");
    // The base was not touched, so the rules still pick it.
    expect(baseField().value).toBe("develop");
  });

  it("an empty name leaves the field empty, so the server mints temp/<slug>", async () => {
    branchName = async () => resolved({ name: "", name_empty: true });
    await render();
    await click(secHead());
    expect(nameField().value).toBe("");
  });

  it("shows the advisory warnings and names them on the folded summary, without blocking the launch", async () => {
    check = (b) =>
      b.name === "feat/x" ? { warnings: [{ code: "prefix_mismatch", message: "the name starts with none of the resolved prefixes (feature/ fix/)" }] } : { warnings: [] };
    await render();
    await click(secHead());
    await typeInto(nameField(), "feat/x");
    await afterCheck();
    expect(warnings()).toHaveLength(1);
    expect(warnings()[0]).toContain("does not start with one of this repository's prefixes");
    await click(secHead()); // fold it again
    expect(secHead().querySelector(".launch-sec-sum")?.textContent).toContain("branch name has a note");
    await click(byText("Start"));
    expect(onLaunch).toHaveBeenCalledTimes(1);
  });

  it("offers to read a pending Bitbucket model again, then takes the new answer", async () => {
    branchName = async () =>
      resolved({
        name: "feature/45-empty-list-after-login",
        base_branch: "main",
        sources: { bitbucket: "pending" },
        warnings: [{ code: "bitbucket_pending", message: "Bitbucket's branching model has not arrived yet" }],
      });
    await render();
    await click(secHead());
    expect(warnings()[0]).toContain("Bitbucket's branch settings have not been read yet");
    branchName = async () => resolved({ sources: { bitbucket: "ok", base: "repository: bitbucket development" } });
    await click(byText("Read Bitbucket's branch settings again"));
    expect(apiMock.mock.calls.some((c) => c[0] === "api/repos/web/branch-rule?refresh=1")).toBe(true);
    expect(nameField().value).toBe("fix/45-empty-list-after-login");
    expect(baseField().value).toBe("develop");
    expect(warnings()).toHaveLength(0);
  });

  it("a typed base drops the rules' source note and their base_missing warning", async () => {
    branchName = async () =>
      resolved({ warnings: [{ code: "base_missing", message: "the base \"develop\" exists neither locally nor on origin" }] });
    await render();
    await click(secHead());
    expect(document.querySelector(".launch-base-source")).not.toBeNull();
    expect(warnings()).toHaveLength(1);
    await typeInto(baseField(), "release/1.2");
    await settle();
    expect(document.querySelector(".launch-base-source")).toBeNull();
    expect(warnings()).toHaveLength(0);
  });

  it("a Bitbucket re-read that returns after the target changed does not resolve the old one", async () => {
    branchName = async () => resolved({ sources: { bitbucket: "pending" } });
    let refreshed: (v: Json) => void = () => {};
    refresh = () => new Promise((r) => (refreshed = r));
    await render();
    await click(secHead());
    await click(byText("Read Bitbucket's branch settings again"));
    await render(true, "api"); // the same dialog, now for another working copy
    const before = apiJSONMock.mock.calls.filter((c) => c[0] === "api/repos/web/branch-name").length;
    await act(async () => refreshed({ kinds: [] }));
    await settle();
    expect(apiJSONMock.mock.calls.filter((c) => c[0] === "api/repos/web/branch-name")).toHaveLength(before);
  });

  it("the same dialog handed another working copy drops the edits and the answer of the last one", async () => {
    await render();
    await click(secHead());
    await typeInto(nameField(), "feature/typed-for-web");
    await typeInto(baseField(), "release/1.2");
    branchName = async () => resolved({ name: "feature/45-for-api", base_branch: "main", sources: { base: "repository: .agent-fleet/branches naming.base" } });
    await render(true, "api");
    expect(nameField().value).toBe("feature/45-for-api");
    expect(baseField().value).toBe("main");
    expect(document.querySelector(".launch-base-source")?.textContent).toContain("naming.base");

    // A third copy whose Agent has no resolver starts from the Console's own suggestion, with
    // nothing left of the previous answer.
    branchName = async () => ({ error: { code: "http_404" } });
    await render(true, "docs");
    expect(nameField().value).toBe("feature/issue-45");
    expect(baseField().value).toBe("main");
    expect(document.querySelector(".launch-base-source")).toBeNull();
  });

  it("another work item gets a fresh form inside the same Modal, whose back-button guard stays", async () => {
    await render(true, "web", item, "look at #45");
    const box = () => document.querySelector("textarea")!;
    expect(box().value).toBe("look at #45");
    const push = vi.spyOn(history, "pushState");
    const back = vi.spyOn(history, "back");
    const other = { ...item, key: "acme/web#46", title: "Another one" };
    await render(true, "web", other, "look at #46");
    // The form follows the new item: its first prompt, not the last item's.
    expect(box().value).toBe("look at #46");
    expect(apiJSONMock.mock.calls.at(-1)?.[2]).toEqual({ item: other });
    // The Modal was not remounted: no guard consumed, none pushed again.
    expect(back).not.toHaveBeenCalled();
    expect(push).not.toHaveBeenCalled();
    push.mockRestore();
    back.mockRestore();
  });

  it("another pull request's head branch replaces the last one's preselected branch", async () => {
    const renderPR = (existing: string) =>
      act(async () => {
        root!.render(
          <LaunchModal repo="web" branch="main" kinds={["claude"]} initialExistingBranch={existing} onClose={() => {}} onLaunch={onLaunch} />,
        );
      });
    await renderPR("pr-a");
    await settle();
    await renderPR("pr-b");
    await settle();
    await click(byText("Start"));
    expect(onLaunch.mock.calls[0][0]).toMatchObject({ base: "pr-b", useExisting: true });
  });

  it("a launch that did not come from a work item never asks for a name", async () => {
    await render(false);
    expect(apiJSONMock.mock.calls.some((c) => String(c[0]).endsWith("/branch-name"))).toBe(false);
  });
});
