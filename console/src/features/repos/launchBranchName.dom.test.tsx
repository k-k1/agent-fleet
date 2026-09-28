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
const apiMock = vi.fn(async (url: string): Promise<Json> => {
  if (url.includes("branch-rule")) return { kinds: [] };
  return { branches: [] };
});
const apiJSONMock = vi.fn(async (url: string, _method: string, body: Json): Promise<Json> => {
  if (url.endsWith("/branch-name")) return branchName(body);
  if (url.endsWith("/branch-name/check")) return check(body);
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
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
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

async function render(withItem = true): Promise<void> {
  await act(async () => {
    root!.render(
      <LaunchModal
        repo="web"
        branch="main"
        kinds={["claude"]}
        initialNewBranch="feature/issue-45"
        workItem={withItem ? item : undefined}
        onClose={() => {}}
        onLaunch={onLaunch}
      />,
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
  localStorage.clear();
  globalThis.indexedDB = new IDBFactory();
  resetAttachDraftDB();
  branchName = async () => resolved();
  check = () => ({ warnings: [] });
  apiMock.mockClear();
  apiJSONMock.mockClear();
  onLaunch = vi.fn<Launch>(async () => ({ ok: true }));
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
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

  it("a launch that did not come from a work item never asks for a name", async () => {
    await render(false);
    expect(apiJSONMock.mock.calls.some((c) => String(c[0]).endsWith("/branch-name"))).toBe(false);
  });
});
