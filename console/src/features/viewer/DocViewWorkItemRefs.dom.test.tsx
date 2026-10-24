// DocView renders its Markdown with work-item links, resolving a bare "#N" against the working
// copy of the session the document (plan) belongs to. Uses the real MarkdownView.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("./pathResolve.ts", () => ({ resolvePathRefs: () => new Promise(() => {}) }));
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<typeof import("../../core/api/client.ts")>()),
  api: async () => ({ connected: true, keys: [], truncated: false }),
}));
vi.mock("../workitems/api.ts", () => ({
  workItemList: async () => ({ items: [], queries: [], sessions: [], fetchedAt: "", running: true }),
  workItemRefresh: vi.fn(),
}));

const { DocView } = await import("./DocView.tsx");
const { useReposStore } = await import("../repos/store.ts");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useWorkItemStore } = await import("../workitems/store.ts");

let root: Root;
let host: HTMLDivElement;

const render = async (session?: string) => {
  await act(async () => {
    root.render(<DocView title="Plan" content="see #956" session={session} />);
  });
};
const links = () => [...host.querySelectorAll<HTMLAnchorElement>("a.md-workitem-link")].map((a) => a.textContent);

beforeEach(() => {
  useReposStore.setState({
    repos: [
      { name: "fleet", provider: "github", remote: "github.com", remotePath: "octo/fleet" },
      { name: "app", provider: "bitbucket", remote: "bitbucket.org", remotePath: "team/app" },
    ],
  });
  useSessionsStore.setState({
    sessions: [
      { name: "s-fleet", kind: "claude", alive: true, repo: "fleet" },
      { name: "s-app", kind: "claude", alive: true, repo: "app" },
    ],
  });
  useWorkItemStore.setState({ payload: null, loaded: true, loadErr: "" });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});

describe("DocView ticket links", () => {
  it("links #N against the plan session's working copy", async () => {
    await render("s-fleet");
    expect(links()).toEqual(["#956"]);
  });

  it("follows the session's repo, not another one", async () => {
    // Bitbucket numbers are never guessed, so the same text stays plain for that session.
    await render("s-app");
    expect(links()).toEqual([]);
  });

  it("leaves #N as text when the document has no repository", async () => {
    await render();
    expect(links()).toEqual([]);
  });
});
