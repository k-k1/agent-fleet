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

describe("FileMarkdownView", () => {
  it("links #N against the repository the file lives in, and only in prose", async () => {
    await render("repos/fleet/CHANGELOG.md");
    expect(linked()).toEqual(["#1652", "octo/other#9"]);
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
