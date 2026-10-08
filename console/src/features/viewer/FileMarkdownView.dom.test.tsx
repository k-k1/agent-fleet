// `#N` in a Markdown file's preview reads against the file's own working copy (#1900).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const toast = () => {};
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));
vi.mock("../scm/open.ts", () => ({ openCommit: () => {} }));
vi.mock("./pathResolve.ts", () => ({ resolvePathRefs: async () => new Map() }));
vi.mock("../workitems/api.ts", () => ({
  workItemList: async () => ({ items: [], queries: [], sessions: [], fetchedAt: "", running: true }),
  workItemRefresh: vi.fn(),
}));

const { FileMarkdownView } = await import("./FileMarkdownView.tsx");
const { useReposStore } = await import("../repos/store.ts");
const { useWorkItemStore } = await import("../workitems/store.ts");
const { useChatStore } = await import("../chat/store.ts");

let host: HTMLDivElement;
let root: Root;

const SRC = "## Changes\n\n- fixed in #1652 (see octo/other#9)\n- colour #112233 and `#1652`\n\n```\nin a fence #1652\n```\n";
const render = async (filePath: string) => {
  await act(async () => {
    root.render(<FileMarkdownView source={SRC} filePath={filePath} />);
  });
};
const linked = () => [...host.querySelectorAll("a.md-workitem-link")].map((a) => a.textContent);

beforeEach(() => {
  useChatStore.setState({ convs: [], titles: {} });
  useWorkItemStore.setState({ payload: null, loaded: true, loadErr: "" });
  useReposStore.setState({
    repos: [
      { name: "fleet", provider: "github", remote: "github.com", remotePath: "octo/fleet" },
      { name: "local" },
    ],
  });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});

const { useWorkItemModal } = await import("../workitems/modal.ts");

const click = async (a: Element) =>
  act(async () => {
    a.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
const opened = () => useWorkItemModal.getState().detail;
const first = () => host.querySelector("a.md-workitem-link")!;

describe("FileMarkdownView", () => {
  it("links #N against the repository the file lives in, and only in prose", async () => {
    await render("repos/fleet/CHANGELOG.md");
    expect(linked()).toEqual(["#1652", "octo/other#9"]);
  });

  it("opens the ticket of the file's own repository, not of a sibling working copy", async () => {
    useReposStore.setState({
      repos: [
        { name: "fleet", provider: "github", remote: "github.com", remotePath: "octo/fleet" },
        { name: "fleet@wip", provider: "github", remote: "github.com", remotePath: "octo/fleet-wip" },
        { name: "app", provider: "bitbucket", remote: "bitbucket.org", remotePath: "team/app" },
      ],
    });
    await render("repos/fleet@wip/CHANGELOG.md");
    await click(first());
    expect(opened()?.item).toMatchObject({ provider: "github", key: "octo/fleet-wip#1652" });
    expect(opened()?.repoHint).toBe("fleet@wip");

    // A Bitbucket copy links the number only when its pull request is cached, on that host.
    await render("repos/app/CHANGELOG.md");
    expect(linked()).toEqual(["octo/other#9"]);
    await act(async () => {
      useWorkItemStore.setState({
        payload: {
          items: [{ id: "bitbucket:team/app#1652", queryId: "q", provider: "bitbucket", kind: "pr", key: "team/app#1652", title: "cached", state: "open", url: "https://bitbucket.example.test/1", assignee: "", labels: [], labelColors: {}, repo: "", updatedAt: "2026-10-01T00:00:00Z", checks: { state: "", total: 0, failed: 0, pending: 0 }, mergeable: "" }],
          queries: [], sessions: [], fetchedAt: "", running: true,
        },
      });
    });
    expect(linked()).toContain("#1652");
    await click(first());
    expect(opened()?.item).toMatchObject({ provider: "bitbucket", key: "team/app#1652" });
    expect(opened()?.repoHint).toBe("app");
  });

  it("links once the repository list arrives after the file rendered", async () => {
    useReposStore.setState({ repos: [] });
    await render("repos/fleet/CHANGELOG.md");
    expect(linked()).toEqual([]);
    await act(async () => {
      useReposStore.setState({ repos: [{ name: "fleet", provider: "github", remote: "github.com", remotePath: "octo/late" }] });
    });
    await click(first());
    expect(opened()?.item.key).toBe("octo/late#1652");
  });

  it("links nothing inside a Git root nested below the working copy", async () => {
    await act(async () => {
      root.render(<FileMarkdownView source={SRC} filePath="repos/fleet/vendor/inner/CHANGELOG.md" nestedRepo />);
    });
    expect(linked()).toEqual([]);
  });

  it("links nothing for a working copy with no recognised remote", async () => {
    await render("repos/local/CHANGELOG.md");
    expect(linked()).toEqual([]);
  });

  it("links nothing for a file outside ~/repos or in an unknown working copy", async () => {
    await render("notes/CHANGELOG.md");
    expect(linked()).toEqual([]);
    await render("repos/gone/CHANGELOG.md");
    expect(linked()).toEqual([]);
  });
});
