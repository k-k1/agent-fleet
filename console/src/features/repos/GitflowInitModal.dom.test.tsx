// Initialize Git Flow (ADR 0103 decision 9). The config it writes is shared by every worktree
// of the repository, so what is held here is about not overwriting anything unseen:
//   - existing keys the save would change are listed, and the button says "Overwrite";
//   - the save carries the keys the dialog opened with, and a 409 reloads instead of retrying;
//   - a branch only on origin is flagged: saving creates a local branch tracking it, because the
//     git flow CLI needs one;
//   - a write that stopped part-way names the branches created and the keys already written.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn(async (..._args: unknown[]) => ({}) as Record<string, unknown>);
vi.mock("../../core/api/client.ts", () => ({
  api: (path: string) => api(path),
  apiJSON: (path: string, method: string, body: unknown) => apiJSON(path, method, body),
  errText: (e: unknown) => String((e as { message?: string })?.message ?? e),
  getTenant: () => "",
}));

import { GitflowInitModal } from "./GitflowInitModal.tsx";
import { t } from "../../lib/i18n/index.ts";
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const events: string[] = [];

const PREFILL = {
  production: "main",
  development: "develop",
  feature: "feature/",
  bugfix: "",
  release: "release/",
  hotfix: "hotfix/",
  versiontag: "",
};

const FRESH = { current: {}, prefill: PREFILL, local: ["main"], origin: ["develop", "main"], native: false, committed: [] };

async function render(states: unknown[]) {
  api.mockReset();
  let i = 0;
  api.mockImplementation(async () => states[Math.min(i++, states.length - 1)]);
  host = document.createElement("div");
  document.body.appendChild(host);
  await act(async () => {
    root = createRoot(host!);
    root.render(
      <ToastProvider>
        <GitflowInitModal repo="web" onClose={() => events.push("closed")} onSaved={() => events.push("saved")} />
      </ToastProvider>,
    );
  });
  await act(async () => {
    await Promise.resolve();
  });
}

// The modal renders through a portal under document.body.
const input = (name: string) => document.querySelector(`input[name="${name}"]`) as HTMLInputElement;
const submit = () =>
  [...document.querySelectorAll("button")].find((b) => b.getAttribute("type") === "submit") as HTMLButtonElement;
const text = () => document.body.textContent || "";

async function type(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    setter.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function press() {
  await act(async () => {
    submit().click();
  });
  await act(async () => {
    await Promise.resolve();
  });
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  events.length = 0;
  apiJSON.mockReset();
  apiJSON.mockImplementation(async () => ({}));
});

describe("GitflowInitModal", () => {
  it("prefills, flags an origin-only branch, and sends the keys it opened with", async () => {
    apiJSON.mockImplementation(async () => ({ written: ["gitflow.branch.develop"], created: ["develop"], state: FRESH }));
    await render([FRESH]);
    expect(api).toHaveBeenCalledWith("api/repos/web/gitflow");
    expect(input("production").value).toBe("main");
    expect(input("development").value).toBe("develop");
    expect(input("feature").value).toBe("feature/");
    // develop is only on origin: say that saving makes the local branch the git flow CLI needs.
    expect(document.querySelector(".gitflow-place")?.textContent).toBe(t("gitflow.origin_only", { branch: "develop" }));
    expect(text()).toContain(t("gitflow.shared"));
    expect(submit().textContent).toBe(t("gitflow.submit"));
    expect(input("production").getAttribute("type")).toBe("text"); // the shared .ui-field input styles

    await type(input("bugfix"), "bugfix/");
    await press();
    expect(apiJSON).toHaveBeenCalledWith("api/repos/web/gitflow/init", "POST", {
      expected: {},
      values: { ...PREFILL, bugfix: "bugfix/" },
    });
    expect(events).toEqual(["saved", "closed"]);
    expect(text()).toContain(t("gitflow.saved_created", { name: "web", branches: "develop" }));
  });

  it("names the branches created when creating the next one failed", async () => {
    apiJSON.mockImplementationOnce(async () => ({
      error: { code: "branch_failed", message: "creating the local branch \"develop\" failed" },
      written: [],
      created: ["main"],
    }));
    await render([{ ...FRESH, local: [], origin: ["develop", "main"] }, FRESH]);
    await press();
    const err = document.querySelector(".gitflow-err")?.textContent || "";
    expect(err).toContain(t("gitflow.err_branch_failed", { err: 'creating the local branch "develop" failed' }));
    expect(err).toContain(t("gitflow.err_created", { branches: "main" }));
    expect(events).toEqual([]);
    // The state is reloaded, so main is no longer shown as origin-only.
    expect(api).toHaveBeenCalledTimes(2);
    expect(document.querySelectorAll(".gitflow-place")).toHaveLength(1);
  });

  it("lists the existing keys a save would overwrite", async () => {
    const current = { "gitflow.branch.master": "main", "gitflow.branch.develop": "develop", "gitflow.prefix.feature": "feat/" };
    await render([{ ...FRESH, current, prefill: { ...PREFILL, feature: "feat/" }, local: ["main", "develop"] }]);
    expect(document.querySelector(".gitflow-changes")).toBeNull();
    expect(submit().textContent).toBe(t("gitflow.submit_save"));
    expect(document.querySelector(".gitflow-place")).toBeNull();

    await type(input("feature"), "feature/");
    const changes = document.querySelector(".gitflow-changes")?.textContent || "";
    expect(changes).toContain("gitflow.prefix.feature");
    expect(changes).toContain("feat/");
    expect(submit().textContent).toBe(t("gitflow.submit_overwrite"));
  });

  it("reloads on a 409 instead of overwriting what it never showed", async () => {
    const changed = { ...FRESH, current: { "gitflow.prefix.feature": "f/" }, prefill: { ...PREFILL, feature: "f/" } };
    apiJSON.mockImplementation(async () => ({ error: { code: "gitflow_changed", message: "changed" } }));
    await render([FRESH, changed]);
    await press();
    expect(api).toHaveBeenCalledTimes(2);
    expect(input("feature").value).toBe("f/");
    expect(document.querySelector(".gitflow-err")?.textContent).toBe(t("gitflow.err_changed"));
    expect(events).toEqual([]);
  });

  it("refuses a development branch that exists nowhere", async () => {
    apiJSON.mockImplementation(async () => ({ error: { code: "branch_missing", message: "x", field: "development" } }));
    await render([{ ...FRESH, origin: ["main"] }]);
    expect(document.querySelector(".gitflow-place")?.textContent).toBe(t("gitflow.missing", { branch: "develop" }));
    await press();
    expect(document.querySelector(".gitflow-err")?.textContent).toBe(t("gitflow.err_branch_missing", { branch: "develop" }));
    expect(input("development").getAttribute("aria-invalid")).toBe("true");
    expect(events).toEqual([]);
  });

  it("names the keys already written when a write stops part-way, and saving again sends the new keys", async () => {
    const partial = { ...FRESH, current: { "gitflow.prefix.feature": "feature/", "gitflow.prefix.release": "release/" } };
    apiJSON.mockImplementationOnce(async () => ({
      error: { code: "write_failed", message: "writing gitflow.prefix.hotfix failed" },
      written: ["gitflow.prefix.feature", "gitflow.prefix.release"],
    }));
    await render([FRESH, partial]);
    await type(input("development"), "dev");
    await press();
    const err = document.querySelector(".gitflow-err")?.textContent || "";
    expect(err).toContain("gitflow.prefix.feature, gitflow.prefix.release");
    expect(err).toContain("gitflow.prefix.hotfix");
    expect(events).toEqual([]);
    // The typed value survives, and the retry carries the keys the failed write left behind.
    expect(input("development").value).toBe("dev");
    apiJSON.mockImplementation(async () => ({ written: [], state: partial }));
    await press();
    expect(apiJSON).toHaveBeenLastCalledWith("api/repos/web/gitflow/init", "POST", {
      expected: partial.current,
      values: { ...PREFILL, development: "dev" },
    });
    expect(events).toEqual(["saved", "closed"]);
  });

  it("says so when the Agent has nothing to show", async () => {
    await render([{ error: { code: "not_git", message: "not a git working copy" } }]);
    expect(text()).toContain(t("gitflow.unavailable"));
    expect(submit().disabled).toBe(true);
  });
});
