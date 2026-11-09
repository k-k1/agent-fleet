// The launch repository picker of the work item panel (#3770): nothing resolved means nothing
// selected and Start disabled, and a Jira project's choice is remembered on launch.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const workItemDetail = vi.fn();
vi.mock("./api.ts", async (orig) => ({
  ...(await orig<typeof import("./api.ts")>()),
  workItemDetail: (...a: unknown[]) => workItemDetail(...a),
}));

const { WorkItemDetailModal } = await import("./WorkItemDetailModal.tsx");
const { t } = await import("../../lib/i18n/index.ts");
type WorkItem = import("./read.ts").WorkItem;
type LaunchRepoSource = import("./read.ts").LaunchRepoSource;
type Repo = import("../repos/store.ts").Repo;

let root: Root | null = null;
let host: HTMLDivElement;

const item: WorkItem = {
  id: "1",
  queryId: "q1",
  provider: "jira",
  kind: "issue",
  key: "CPAAS-8",
  title: "CI",
  state: "open",
  url: "",
  assignee: "",
  labels: [],
  labelColors: {},
  repo: "",
  updatedAt: "2026-11-01T00:00:00Z",
  checks: { state: "", total: 0, failed: 0, pending: 0 },
  mergeable: "",
};

const repos: Repo[] = [{ name: "client-sdk" }, { name: "web" }];

const onPick = vi.fn();
const onRemember = vi.fn();

const render = async (defaultRepo: string, defaultSource: LaunchRepoSource = "", row: WorkItem = item, remember = true) => {
  await act(async () => {
    root!.render(
      <WorkItemDetailModal
        item={row}
        repos={repos}
        defaultRepo={defaultRepo}
        defaultSource={defaultSource}
        onRemember={remember ? onRemember : undefined}
        started={[]}
        onClose={() => {}}
        onPick={onPick}
        onStartHub={() => {}}
        sessionRef={() => ({ state: "live", title: "" })}
        onOpenSession={() => {}}
        onReport={() => {}}
      />,
    );
  });
};

const repoSelect = () => document.querySelector<HTMLSelectElement>(".wi-sfield select")!;
const startButton = () =>
  [...document.querySelectorAll<HTMLButtonElement>(".ui-modal-foot button")].find((b) => b.textContent === t("wi.start"))!;
const rememberBox = () => document.querySelector<HTMLInputElement>(".wi-sremember input");
const choose = async (name: string) => {
  await act(async () => {
    const sel = repoSelect();
    sel.value = name;
    sel.dispatchEvent(new Event("change", { bubbles: true }));
  });
};

beforeEach(() => {
  localStorage.clear();
  workItemDetail.mockReset();
  workItemDetail.mockReturnValue(new Promise(() => {}));
  onPick.mockReset();
  onRemember.mockReset();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  root = null;
});

describe("WorkItemDetailModal launch repository", () => {
  it("selects nothing and disables Start when no repository resolved, instead of the first one", async () => {
    await render("");
    expect(repoSelect().value).toBe("");
    expect(repoSelect().selectedOptions[0].textContent).toBe(t("wi.start_repo_choose"));
    expect(startButton().disabled).toBe(true);
    await act(async () => startButton().click());
    expect(onPick).not.toHaveBeenCalled();
  });

  it("enables Start once the user chooses, and remembers the choice for the Jira project", async () => {
    await render("");
    await choose("web");
    expect(startButton().disabled).toBe(false);
    expect(rememberBox()!.checked).toBe(true);
    await act(async () => startButton().click());
    expect(onRemember).toHaveBeenCalledWith("jira:CPAAS", "web");
    expect(onPick.mock.calls[0][0].name).toBe("web");
  });

  it("does not remember when the box is cleared", async () => {
    await render("");
    await choose("web");
    await act(async () => rememberBox()!.click());
    await act(async () => startButton().click());
    expect(onRemember).not.toHaveBeenCalled();
    expect(onPick).toHaveBeenCalled();
  });

  it("says a remembered repository was remembered, and does not rewrite the same entry", async () => {
    await render("web", "project");
    expect(repoSelect().value).toBe("web");
    expect(document.body.textContent).toContain(t("wi.start_repo_remembered", { project: "CPAAS" }));
    await act(async () => startButton().click());
    expect(onRemember).not.toHaveBeenCalled();
  });

  it("overwrites the remembered repository when the user picks another", async () => {
    await render("web", "project");
    await choose("client-sdk");
    expect(document.body.textContent).not.toContain(t("wi.start_repo_remembered", { project: "CPAAS" }));
    await act(async () => startButton().click());
    expect(onRemember).toHaveBeenCalledWith("jira:CPAAS", "client-sdk");
  });

  it("offers no remember box for a GitHub item, which names its own repository", async () => {
    await render("web", "item", { ...item, provider: "github", key: "acme/web#3", repo: "acme/web" });
    expect(rememberBox()).toBeNull();
    await act(async () => startButton().click());
    expect(onRemember).not.toHaveBeenCalled();
    expect(onPick.mock.calls[0][0].name).toBe("web");
  });
});
