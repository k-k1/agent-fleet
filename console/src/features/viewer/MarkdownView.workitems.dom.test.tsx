// Ticket references in rendered Markdown → the work item detail modal (#1659). Pins what a reader
// sees: which tokens become links, which stay text, and what a click opens.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { WorkItem, WorkItemPayload } from "../workitems/read.ts";

// One toast function for the whole file, as the real provider gives: a new identity per render
// would re-run MarkdownView's parse effect on every store change and hide whether the relink
// effect (the one under test for late repositories and cache pushes) does its job.
const toast = () => {};
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));
vi.mock("../scm/open.ts", () => ({ openCommit: () => {} }));

// The file resolver behind linkifyPathRefs, held open so a test can let the ticket linkifier run
// while a path answer is still on its way.
let resolvePaths: (m: Map<string, { path: string; type: string }>) => void = () => {};
vi.mock("./pathResolve.ts", () => ({
  resolvePathRefs: () =>
    new Promise((r) => {
      resolvePaths = r;
    }),
}));

// The inbox read behind ensureWorkItems (the "cache not loaded yet" path).
const workItemList = vi.fn();
vi.mock("../workitems/api.ts", () => ({
  workItemList: (...a: unknown[]) => workItemList(...a),
  workItemRefresh: vi.fn(),
}));

const { MarkdownView } = await import("./MarkdownView.tsx");
const { useReposStore } = await import("../repos/store.ts");
const { useWorkItemStore } = await import("../workitems/store.ts");
const { useWorkItemModal } = await import("../workitems/modal.ts");
const { useChatStore } = await import("../chat/store.ts");
const { isPathCandidateCode } = await import("./parts/mdRefLinks.ts");

const row = (provider: string, key: string, extra: Partial<WorkItem> = {}): WorkItem => ({
  id: `${provider}:${key}`,
  queryId: "q1",
  provider,
  kind: "issue",
  key,
  title: `title of ${key}`,
  state: "open",
  url: `https://tracker.example.test/${key}`,
  assignee: "",
  labels: [],
  labelColors: {},
  repo: "",
  updatedAt: "2026-10-01T00:00:00Z",
  checks: { state: "", total: 0, failed: 0, pending: 0 },
  mergeable: "",
  ...extra,
});

const payload = (items: WorkItem[]): WorkItemPayload => ({ items, queries: [], sessions: [], fetchedAt: "", running: true });

let host: HTMLDivElement;
let root: Root;

const render = async (source: string, repo: string | null = "fleet", workItemRefs = true) => {
  await act(async () => {
    root.render(<MarkdownView source={source} repo={repo} workItemRefs={workItemRefs} />);
  });
};
const links = () => [...host.querySelectorAll<HTMLAnchorElement>("a.md-workitem-link")];
const click = async (a: Element, init: MouseEventInit = {}) =>
  act(async () => {
    a.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, ...init }));
  });

beforeEach(() => {
  workItemList.mockReset();
  workItemList.mockResolvedValue(payload([]));
  useChatStore.setState({ convs: [], titles: {} });
  useReposStore.setState({
    repos: [
      { name: "fleet", provider: "github", remote: "github.com", remotePath: "octo/fleet" },
      { name: "app", provider: "bitbucket", remote: "bitbucket.org", remotePath: "team/app" },
    ],
  });
  useWorkItemStore.setState({ payload: payload([row("github", "octo/fleet#956"), row("github", "octo/fleet#1649")]), loaded: true, loadErr: "" });
  useWorkItemModal.getState().close();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});

describe("ticket references", () => {
  it("links every number in an agent's status table and opens the cached row", async () => {
    await render("| PR | Issue |\n|---|---|\n| #1649 | #956 |\n| #1652 | #1054 |");
    // #1652 and #1054 are not in the inbox (merged, closed, or never the reader's): cited in prose,
    // they still link.
    expect(links().map((a) => a.textContent)).toEqual(["#1649", "#956", "#1652", "#1054"]);
    expect(links()[1].title.split("\n")[0]).toBe("title of octo/fleet#956");

    await click(links()[1]);
    const d = useWorkItemModal.getState().detail;
    expect(d?.item.key).toBe("octo/fleet#956");
    expect(d?.reference).toBe(false);
    // The ticket belongs to the working copy the mirror is about, so the launch defaults there.
    expect(d?.repoHint).toBe("fleet");
  });

  it("leaves a number in inline code as text unless the inbox holds it", async () => {
    // A search the user typed, quoted back by the agent: #166 exists on GitHub, but is not cited.
    await render("typed `#166` and `octo/fleet#166`; `#956` is ours; merged in #167");
    expect(links().map((a) => a.textContent)).toEqual(["#956", "#167"]);
  });

  it("opens a merged or closed ticket the inbox no longer holds as a reference with a tracker link", async () => {
    await render("merged in #1652");
    await click(links()[0]);
    const d = useWorkItemModal.getState().detail;
    expect(d?.reference).toBe(true);
    expect(d?.item.url).toBe("https://github.com/octo/fleet/issues/1652");
  });

  it("re-guesses a guessed link once the cache or the clones know better", async () => {
    useReposStore.setState({ repos: [] });
    await render("see team/svc#7 and team/svc#8", null);
    expect(links()).toHaveLength(2);
    await act(async () => {
      useReposStore.setState({ repos: [{ name: "svc", provider: "bitbucket", remote: "bitbucket.org", remotePath: "team/svc" }] });
      useWorkItemStore.setState({ payload: payload([row("bitbucket", "team/svc#7", { kind: "pr" })]) });
    });
    // #8 is an uncached number on a Bitbucket repository: text again, never the GitHub it was
    // guessed for. #7 is now the cached Bitbucket pull request.
    expect(links().map((a) => a.textContent)).toEqual(["team/svc#7"]);
    await click(links()[0]);
    expect(useWorkItemModal.getState().detail?.item).toMatchObject({ provider: "bitbucket", key: "team/svc#7", title: "title of team/svc#7" });
  });

  it("goes straight to the tracker on a Ctrl-click", async () => {
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    await render("see #956");
    await click(links()[0], { ctrlKey: true });
    expect(open).toHaveBeenCalledWith("https://tracker.example.test/octo/fleet#956", "_blank", "noopener,noreferrer");
    expect(useWorkItemModal.getState().detail).toBeNull();
    open.mockRestore();
  });

  it("opens a reference-only panel when the row left the cache after the link was drawn", async () => {
    await render("PR #1649 is ready");
    useWorkItemStore.setState({ payload: payload([row("github", "octo/fleet#956")]) });
    await click(links()[0]);
    const d = useWorkItemModal.getState().detail;
    expect(d?.reference).toBe(true);
    expect(d?.item.url).toBe("https://github.com/octo/fleet/issues/1649");
  });

  it("leaves a bare number as text without a context repository", async () => {
    await render("see #956", null);
    expect(links()).toHaveLength(0);
    // The qualified form needs no context.
    await render("see octo/fleet#956", null);
    expect(links().map((a) => a.textContent)).toEqual(["octo/fleet#956"]);
  });

  it("is off unless the surface asks for it", async () => {
    await render("see #956 and octo/fleet#956", "fleet", false);
    expect(links()).toHaveLength(0);
  });

  it("does not link things that only look like tickets", async () => {
    useWorkItemStore.setState({ payload: payload([row("jira", "G3M-5"), row("github", "octo/fleet#112233")]), loaded: true });
    await render("C# and &amp;#123; and page#12 and `#112233` and UTF-8, SHA-256, P2-1");
    expect(links()).toHaveLength(0);
  });

  it("takes a number whole, so no digits are left for the commit shape", async () => {
    useWorkItemStore.setState({ payload: payload([row("github", "octo/fleet#1234567"), row("github", "octo/fleet#11223344")]) });
    await render("fixed in #1234567; colour #11223344, #00112233, #00000000 and octo/fleet#11223344; `#7654321` unknown");
    expect(links().map((a) => a.textContent)).toEqual(["#1234567", "octo/fleet#11223344"]);
    expect(host.querySelector("a.md-commit-link")).toBeNull();
  });

  describe("the same owner/name on both hosts", () => {
    const bothHosts = [
      { name: "bb-copy", provider: "bitbucket", remote: "bitbucket.org", remotePath: "team/both" },
      { name: "gh-copy", provider: "github", remote: "github.com", remotePath: "team/both" },
    ];

    it("keeps a bare number on the mirror's host after its row leaves the cache", async () => {
      useReposStore.setState({ repos: bothHosts });
      useWorkItemStore.setState({ payload: payload([row("bitbucket", "team/both#7", { kind: "pr" }), row("github", "team/both#8")]) });
      await render("PR #7, and #8 is the other host's", "bb-copy");
      expect(links().map((a) => a.textContent)).toEqual(["#7"]);
      useWorkItemStore.setState({ payload: payload([]) });
      await click(links()[0]);
      expect(useWorkItemModal.getState().detail?.item.provider).toBe("bitbucket");
    });

    it("does not guess an uncached qualified number onto the other host's clone, in either list order", async () => {
      for (const repos of [bothHosts, [...bothHosts].reverse()]) {
        useReposStore.setState({ repos });
        useWorkItemStore.setState({ payload: payload([]) });
        await render("see #7 and team/both#7", "bb-copy");
        expect(links()).toHaveLength(0);
        await render("see #7 and team/both#7", "gh-copy");
        expect(links().map((a) => a.textContent)).toEqual(["#7", "team/both#7"]);
      }
    });

    it("keeps a qualified link on the host it was drawn for when that row leaves the cache", async () => {
      useReposStore.setState({ repos: bothHosts });
      useWorkItemStore.setState({ payload: payload([row("bitbucket", "team/both#7", { kind: "pr" }), row("github", "team/both#7")]) });
      await render("see team/both#7", "bb-copy");
      // Only the Bitbucket row goes; the GitHub one with the same key is a different ticket.
      useWorkItemStore.setState({ payload: payload([row("github", "team/both#7")]) });
      await click(links()[0]);
      const d = useWorkItemModal.getState().detail;
      expect(d?.item.provider).toBe("bitbucket");
      expect(d?.reference).toBe(true);
      expect(d?.repoHint).toBe("bb-copy");
    });

    it("does not hint the mirror's copy for the other host's ticket", async () => {
      useReposStore.setState({ repos: bothHosts });
      useWorkItemStore.setState({ payload: payload([row("github", "team/both#7")]) });
      await render("see team/both#7", "bb-copy");
      await click(links()[0]);
      const d = useWorkItemModal.getState().detail;
      expect(d?.item.provider).toBe("github");
      expect(d?.repoHint).toBe("");
    });
  });

  it("reads the launch hint when clicked, so a late repository list still names the session's copy", async () => {
    // The working copy's folder name differs from the remote's, so only the hint can find it.
    useReposStore.setState({ repos: [] });
    useWorkItemStore.setState({ payload: payload([row("github", "octo/app#7")]) });
    await render("see octo/app#7", "renamed");
    useReposStore.setState({
      repos: [
        { name: "other", provider: "github", remote: "github.com", remotePath: "octo/other" },
        { name: "renamed", provider: "github", remote: "github.com", remotePath: "octo/app" },
      ],
    });
    await click(links()[0]);
    expect(useWorkItemModal.getState().detail?.repoHint).toBe("renamed");
  });

  it("does not wrap a ticket link that appeared while a path answer was on its way", async () => {
    // #7 is not cached yet, so the path pass takes the code.
    await act(async () => {
      root.render(<MarkdownView source="see `team/app#7`" repo="fleet" workItemRefs onOpenFile={() => {}} />);
    });
    expect(links()).toHaveLength(0);
    await act(async () => {
      useWorkItemStore.setState({ payload: payload([row("bitbucket", "team/app#7", { kind: "pr" })]) });
    });
    expect(links()).toHaveLength(1);
    await act(async () => {
      resolvePaths(new Map([["team/app#7", { path: "team/app#7", type: "file" }]]));
    });
    expect(host.querySelector("a.md-path-link")).toBeNull();
    expect(host.querySelector("code a.md-workitem-link")?.textContent).toBe("team/app#7");
  });

  it("turns a guessed link back into text once the clones make it ambiguous", async () => {
    useReposStore.setState({ repos: [] });
    await render("see team/both#7 and octo/fleet#956", null);
    expect(links()).toHaveLength(2);
    await act(async () => {
      useReposStore.setState({
        repos: [
          { name: "bb-copy", provider: "bitbucket", remote: "bitbucket.org", remotePath: "team/both" },
          { name: "gh-copy", provider: "github", remote: "github.com", remotePath: "team/both" },
        ],
      });
    });
    // As if rendered now: team/both is on both hosts, so its uncached number is not guessed. The
    // link drawn from a cached row stays.
    expect(links().map((a) => a.textContent)).toEqual(["octo/fleet#956"]);
    expect(host.textContent).toContain("see team/both#7 and");
  });

  it("re-links when only a clone's host changes", async () => {
    // The chat: no context repository, so team/svc's host comes from the clones alone.
    useReposStore.setState({ repos: [{ name: "svc", provider: "bitbucket", remote: "bitbucket.org", remotePath: "team/svc" }] });
    await render("see team/svc#7", null);
    expect(links()).toHaveLength(0);
    await act(async () => {
      useReposStore.setState({ repos: [{ name: "svc", provider: "github", remote: "github.com", remotePath: "team/svc" }] });
    });
    expect(links().map((a) => a.textContent)).toEqual(["team/svc#7"]);
  });

  it("links once the repository list arrives, without the text changing", async () => {
    const repos = useReposStore.getState().repos;
    useReposStore.setState({ repos: [] });
    await render("see #956");
    expect(links()).toHaveLength(0);
    await act(async () => {
      useReposStore.setState({ repos });
    });
    expect(links().map((a) => a.textContent)).toEqual(["#956"]);
  });

  it("links a ticket when a push brings it into a loaded cache", async () => {
    await render("`G3M-12` and `#2000` are blocked");
    expect(links()).toHaveLength(0);
    await act(async () => {
      useWorkItemStore.setState({ payload: payload([row("jira", "G3M-12"), row("github", "octo/fleet#2000")]) });
    });
    expect(links().map((a) => a.textContent)).toEqual(["G3M-12", "#2000"]);
  });

  it("leaves a path-shaped token a path where ticket links are off or do not take it", async () => {
    await render("see `octo/fleet#956`", "fleet", false);
    expect(isPathCandidateCode(host.querySelector("code")!)).toBe(true);
    // Off, the code stays the path pass's: its digits are not offered to the commit shape either.
    await render("see `octo/fleet#1234567`", "fleet", false);
    expect(host.querySelector("a.md-commit-link")).toBeNull();
    // Not in the inbox, so not a ticket here: the path pass still gets it.
    await render("see `team/app#7`", "fleet");
    expect(links()).toHaveLength(0);
    expect(isPathCandidateCode(host.querySelector("code")!)).toBe(true);
  });

  it("links a cached qualified reference in inline code instead of treating it as a path", async () => {
    await render("see `octo/fleet#956`");
    expect(links().map((a) => a.textContent)).toEqual(["octo/fleet#956"]);
  });

  it("never touches a fenced block", async () => {
    await render("```\n#956 octo/fleet#956\n```");
    expect(links()).toHaveLength(0);
  });

  it("links a cached ticket once the cache has loaded", async () => {
    useWorkItemStore.getState().reset();
    workItemList.mockResolvedValue(payload([row("jira", "G3M-12", { url: "https://jira.example.test/browse/G3M-12" })]));
    await render("G3M-12 is blocked", null);
    await act(async () => {
      await Promise.resolve();
    });
    expect(workItemList).toHaveBeenCalledTimes(1);
    expect(links().map((a) => a.textContent)).toEqual(["G3M-12"]);
    await click(links()[0]);
    expect(useWorkItemModal.getState().detail?.item.url).toBe("https://jira.example.test/browse/G3M-12");
  });
});
