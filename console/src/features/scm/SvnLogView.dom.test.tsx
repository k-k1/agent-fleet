// Show log / local changes for an SVN working copy (issue #1705).
//
// What is held here is the contract between the panes and the Agent's routes: the log is
// requested on open and on "load more" with the revision to continue from, an auth refusal opens
// the re-authentication dialog instead of printing an error, and the SCM pane kinds pick the
// SVN views (svn-log / svn-changes / svn-show / svn-diff) for an svn working copy and the git
// views for everything else.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;

const api = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (path: string) => api(path),
  apiJSON: async () => ({}),
  rawJSON: async () => ({ ok: true, json: async () => ({}) }),
  errText: (e: unknown) => String((e as { message?: string })?.message ?? e),
  isTransientErr: (d: { error?: { status?: number } } | null) => (d?.error?.status ?? 0) >= 500,
  getTenant: () => "",
}));

const openCommit = vi.fn();
const openFileDiff = vi.fn();
vi.mock("./open.ts", () => ({
  openCommit: (...a: unknown[]) => openCommit(...a),
  openFileDiff: (...a: unknown[]) => openFileDiff(...a),
  openChanges: () => {},
  openRepoLog: () => {},
}));

import { SvnLogView } from "./SvnLogView.tsx";
import { ScmPane } from "./ScmPane.tsx";
import { useReposStore } from "../repos/store.ts";
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const rev = (n: number) => ({ rev: n, author: "alice", date: "2026-10-01T09:00:00Z", message: `change ${n}\n\nmore`, paths: [{ action: "M", path: "/trunk/a.txt" }] });

async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.appendChild(host);
  await act(async () => {
    root = createRoot(host!);
    root.render(<ToastProvider>{node}</ToastProvider>);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const rows = () => [...host!.querySelectorAll(".svnlog-row")];
const click = async (el: Element) => {
  await act(async () => {
    (el as HTMLElement).click();
  });
};

beforeEach(() => {
  api.mockReset();
  openCommit.mockReset();
  openFileDiff.mockReset();
  useReposStore.setState({ repos: [] });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

describe("SvnLogView", () => {
  it("lists revisions newest first, marks the ones newer than the working copy, and opens one", async () => {
    api.mockResolvedValue({ revisions: [rev(12), rev(10), rev(9)], hasMore: false, wcRevision: "10" });
    await mount(<SvnLogView repo="docs" />);

    expect(api).toHaveBeenCalledTimes(1);
    expect(api.mock.calls[0][0]).toBe("api/repos/docs/svn-log?limit=50");
    expect(rows().map((r) => r.querySelector(".svnlog-rev")?.textContent)).toEqual(["r12", "r10", "r9"]);
    expect(rows()[0].querySelector(".svnlog-tag.newer")).not.toBeNull();
    expect(rows()[1].querySelector(".svnlog-tag.current")).not.toBeNull();
    expect(rows()[2].querySelector(".svnlog-tag")).toBeNull();
    expect(host!.querySelector(".svnlog-more")).toBeNull();

    await click(rows()[1].querySelector("button")!);
    expect(openCommit).toHaveBeenCalledWith("docs", "10", undefined);
  });

  it("continues from the oldest revision shown when asked for more", async () => {
    api.mockResolvedValueOnce({ revisions: [rev(12), rev(11)], hasMore: true, wcRevision: "12" });
    await mount(<SvnLogView repo="docs" />);
    const more = host!.querySelector(".svnlog-more")!;
    api.mockResolvedValueOnce({ revisions: [rev(10), rev(9)], hasMore: false, wcRevision: "12" });
    await click(more);

    expect(api.mock.calls[1][0]).toBe("api/repos/docs/svn-log?limit=50&from=11");
    expect(rows().map((r) => r.querySelector(".svnlog-rev")?.textContent)).toEqual(["r12", "r11", "r10", "r9"]);
  });

  it("opens the re-authentication dialog on svn_auth_required instead of an error", async () => {
    api.mockImplementation(async (path: string) =>
      path.includes("/svn-log") ? { error: { code: "svn_auth_required", message: "E170001" } } : { url: "svn://h/r/trunk", urlPrefix: "svn://h/r" },
    );
    await mount(<SvnLogView repo="docs" />);

    expect(api.mock.calls.some((c) => String(c[0]).endsWith("/svn-auth"))).toBe(true);
    expect(document.body.querySelector("form input[type=password], input[type=password]")).not.toBeNull();
    expect(host!.querySelector(".svnlog-err")).toBeNull();
  });

  it("shows any other failure in the pane", async () => {
    api.mockResolvedValue({ error: { code: "svn_failed", message: "E170013 unreachable" } });
    await mount(<SvnLogView repo="docs" />);
    expect(host!.querySelector(".svnlog-err")?.textContent).toContain("E170013 unreachable");
    expect(document.body.querySelector("input[type=password]")).toBeNull();
  });
});

describe("ScmPane", () => {
  it("follows scmPath: another Show log path in the same repo reloads with that filter", async () => {
    useReposStore.setState({ repos: [{ name: "docs", vcs: "svn" } as never] });
    api.mockResolvedValue({ revisions: [rev(3)], hasMore: false, wcRevision: "3" });
    await mount(<ScmPane content={{ kind: "scm", scmRepo: "docs", scmPath: "src/a" }} />);
    expect(api.mock.calls[0][0]).toBe("api/repos/docs/svn-log?limit=50&path=src%2Fa");
    await act(async () => {
      root!.render(
        <ToastProvider>
          <ScmPane content={{ kind: "scm", scmRepo: "docs", scmPath: "src/b" }} />
        </ToastProvider>,
      );
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(api.mock.calls.at(-1)![0]).toBe("api/repos/docs/svn-log?limit=50&path=src%2Fb");
    expect((host!.querySelector(".svnlog-filter input") as HTMLInputElement).value).toBe("src/b");
  });

  it("mounts nothing until the repo list answers, then the matching view", async () => {
    useReposStore.setState({ repos: [] });
    let answer!: (v: unknown) => void;
    api.mockImplementation((path: string) =>
      path === "api/repos" ? new Promise((r) => (answer = r)) : Promise.resolve({ changes: [] }),
    );
    await mount(<ScmPane content={{ kind: "changes", scmRepo: "docs" }} />);
    expect(api.mock.calls.map((c) => c[0])).toEqual(["api/repos"]);
    await act(async () => {
      answer({ repos: [{ name: "docs", vcs: "svn" }] });
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(api.mock.calls.map((c) => c[0])).toEqual(["api/repos", "api/repos/docs/svn-changes"]);
  });

  it("treats a listed repo without vcs as git, and an unlisted one as git once the list has answered", async () => {
    useReposStore.setState({ repos: [{ name: "g" } as never] });
    api.mockResolvedValue({ subject: "s", diff: "" });
    await mount(<ScmPane content={{ kind: "commit", scmRepo: "g", commitSha: "abc1234" }} />);
    expect(api.mock.calls[0][0]).toBe("api/repos/g/show?sha=abc1234");
    act(() => root?.unmount());
    host?.remove();
    api.mockReset();
    useReposStore.setState({ repos: [] });
    api.mockImplementation(async (path: string) => (path === "api/repos" ? { repos: [] } : { subject: "s", diff: "" }));
    await mount(<ScmPane content={{ kind: "commit", scmRepo: "gone", commitSha: "abc1234" }} />);
    await act(async () => {
      await Promise.resolve();
    });
    expect(api.mock.calls.map((c) => c[0])).toEqual(["api/repos", "api/repos/gone/show?sha=abc1234"]);
  });

  it("recovers from a transient repo-list failure: svn repo present, then repo absent", async () => {
    for (const [repos, want] of [
      [[{ name: "docs", vcs: "svn" }], "api/repos/docs/svn-show?rev=7"],
      [[], "api/repos/docs/show?sha=7"],
    ] as const) {
      api.mockReset();
      useReposStore.setState({ repos: [] });
      let n = 0;
      api.mockImplementation(async (path: string) =>
        path === "api/repos" ? (n++ === 0 ? { error: { code: "http_502", status: 502 } } : { repos }) : { subject: "s", diff: "" },
      );
      await mount(<ScmPane content={{ kind: "commit", scmRepo: "docs", commitSha: "7" }} />);
      expect(api.mock.calls.map((c) => c[0])).toEqual(["api/repos"]); // nothing mounted on the failure
      await act(async () => {
        await new Promise((r) => setTimeout(r, 900));
      });
      expect(api.mock.calls.map((c) => c[0])).toEqual(["api/repos", "api/repos", want]);
      act(() => root?.unmount());
      host?.remove();
    }
  });

  it("re-authenticates from an svn revision's detail and refetches it after saving", async () => {
    useReposStore.setState({ repos: [{ name: "docs", vcs: "svn" } as never] });
    let authed = false;
    api.mockImplementation(async (path: string) => {
      if (path.includes("/svn-show")) return authed ? { subject: "ok", diff: "" } : { error: { code: "svn_auth_required", message: "E170001" } };
      return { url: "svn://h/r/trunk", urlPrefix: "svn://h/r" };
    });
    await mount(<ScmPane content={{ kind: "commit", scmRepo: "docs", commitSha: "7" }} />);
    const pw = document.body.querySelector("input[type=password]") as HTMLInputElement;
    expect(pw).not.toBeNull();
    authed = true;
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    await act(async () => {
      setter.call(pw, "pw");
      pw.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const submit = [...document.body.querySelectorAll("button")].find((b) => b.getAttribute("type") === "submit")!;
    await act(async () => {
      submit.click();
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(api.mock.calls.filter((c) => String(c[0]).includes("/svn-show")).length).toBe(2);
    expect(document.body.querySelector("input[type=password]")).toBeNull();
  });

  it("renders the svn views and routes to the svn endpoints for an svn working copy", async () => {
    useReposStore.setState({ repos: [{ name: "docs", vcs: "svn" } as never] });
    api.mockResolvedValue({ changes: [{ path: "a.txt", status: "M", untracked: false, conflict: false }] });
    await mount(<ScmPane content={{ kind: "changes", scmRepo: "docs" }} />);

    expect(api.mock.calls.map((c) => c[0])).toEqual(["api/repos/docs/svn-changes"]);
    const name = host!.querySelector(".chg-name") as HTMLElement;
    expect(name.textContent).toBe("a.txt");
    await click(name);
    expect(openFileDiff).toHaveBeenCalledWith("docs", "a.txt", false);
  });

  it("asks svn-show for a revision and svn-diff for a working file", async () => {
    useReposStore.setState({ repos: [{ name: "docs", vcs: "svn" } as never] });
    api.mockResolvedValue({ diff: "diff --git a/x b/x\n", subject: "s" });
    await mount(<ScmPane content={{ kind: "commit", scmRepo: "docs", scmPath: "sub", commitSha: "7" }} />);
    expect(api.mock.calls[0][0]).toBe("api/repos/docs/svn-show?rev=7&path=sub");
    act(() => root?.unmount());
    host?.remove();
    api.mockClear();
    await mount(<ScmPane content={{ kind: "wtdiff", scmRepo: "docs", filePath: "a b.txt", diffStaged: false }} />);
    expect(api.mock.calls[0][0]).toBe("api/repos/docs/svn-diff?path=a%20b.txt");
  });

  it("keeps the git endpoints for a git working copy", async () => {
    useReposStore.setState({ repos: [{ name: "g" } as never] });
    api.mockResolvedValue({ diff: "", subject: "s" });
    await mount(<ScmPane content={{ kind: "commit", scmRepo: "g", commitSha: "abc1234" }} />);
    expect(api.mock.calls[0][0]).toBe("api/repos/g/show?sha=abc1234");
  });
});
