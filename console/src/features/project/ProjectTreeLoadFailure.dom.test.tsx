// The rail must never read "could not load the repositories" as "there are none": a failed
// load keeps the last list across workspace-state blips, and the empty area says which of
// loading / unreachable / stopped / genuinely empty it is — only the last offers Add.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Repo } from "../repos/store.ts";

let answer: () => unknown = () => ({ error: { code: "http_502", status: 502 } });

vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(async (url: string) => (url.startsWith("api/repos") ? answer() : {})),
  isTransientErr: (d: { error?: { status?: number } }) => (d?.error?.status ?? 0) >= 500,
  getTenant: () => "",
}));

const { ProjectTree } = await import("./ProjectTree.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { useReposStore } = await import("../repos/store.ts");
const { useProjectFilter } = await import("./filter.ts");

let root: Root | null = null;
let host: HTMLDivElement;

const REPOS: Repo[] = [{ name: "af", branch: "develop" }];
const flush = () => act(async () => { await Promise.resolve(); });
const rows = () => host.querySelectorAll("[data-rail-repo]").length;
const empty = () => host.querySelector(".ui-empty")?.textContent ?? "";
const hasAdd = () => !!host.querySelector(".ui-empty-actions button");

async function render(): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <ProjectTree />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await flush();
}
const setWs = (state: string) => act(async () => { useWorkspaceStore.setState({ state }); await Promise.resolve(); });

beforeEach(() => {
  localStorage.clear();
  answer = () => ({ error: { code: "http_502", status: 502 } });
  useReposStore.setState({ repos: [], loadState: "loading" });
  useProjectFilter.setState({ q: "" });
  useWorkspaceStore.setState({ state: "running" });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("repos rail on a failed load", () => {
  it("keeps the list when the workspace state flips to unknown / starting with a failing load", async () => {
    useReposStore.setState({ repos: REPOS, loadState: "ok" });
    await render();
    expect(rows()).toBe(1);
    for (const state of ["unknown", "starting", "none"]) {
      await setWs(state);
      expect(useReposStore.getState().repos).toEqual(REPOS);
      expect(rows()).toBe(1);
      expect(empty()).not.toContain("No repositories");
    }
    await setWs("running");
    expect(rows()).toBe(1);
  });

  it("keeps the list when the load answers 4xx or {}", async () => {
    useReposStore.setState({ repos: REPOS, loadState: "ok" });
    answer = () => ({ error: { code: "unauthorized", status: 401 } });
    await render();
    expect(rows()).toBe(1);
    answer = () => ({});
    await setWs("starting");
    await setWs("running");
    expect(rows()).toBe(1);
  });

  it("says unreachable, without Add, when nothing was ever loaded", async () => {
    answer = () => ({ error: { code: "http_502", status: 502 } });
    await render();
    expect(empty()).toContain("reach");
    expect(hasAdd()).toBe(false);
  });

  it("promises a retry only while the workspace runs", async () => {
    await render();
    expect(empty()).toContain("Retrying");
    await setWs("starting");
    expect(empty()).toContain("reach");
    expect(empty()).not.toContain("Retrying");
  });

  it("shows a terminal error, not the retry hint, when a running workspace answers 4xx", async () => {
    answer = () => ({ error: { code: "unauthorized", status: 401 } });
    await render();
    expect(empty()).toContain("Couldn't load the repository list");
    expect(empty()).not.toContain("Retrying");
    expect(empty()).not.toContain("reach");
    expect(hasAdd()).toBe(false);
  });

  it("shows the terminal error for a non-JSON answer too ({})", async () => {
    answer = () => ({});
    await render();
    expect(empty()).toContain("Couldn't load the repository list");
    expect(empty()).not.toContain("Retrying");
  });

  it("does not blame the connection for a kept list after a 4xx", async () => {
    useReposStore.setState({ repos: REPOS, loadState: "ok" });
    answer = () => ({ error: { code: "unauthorized", status: 401 } });
    await render();
    const note = host.querySelector(".proj-sub-empty")?.textContent ?? "";
    expect(note).toContain("last known");
    expect(note).not.toContain("reach");
  });

  it("says the workspace is stopped, without Add", async () => {
    useWorkspaceStore.setState({ state: "stopped" });
    await render();
    expect(empty()).toContain("stopped");
    expect(hasAdd()).toBe(false);
  });

  it("offers Add only for a genuinely empty list", async () => {
    answer = () => ({ repos: [] });
    await render();
    expect(empty()).toContain("No repositories");
    expect(hasAdd()).toBe(true);
  });
});
