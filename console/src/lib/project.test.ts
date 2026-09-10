import { describe, it, expect } from "vitest";
import type { Repo } from "../features/repos/store.ts";
import type { Session } from "../types/session.ts";
import {
  sessionFolder,
  orderedRepos,
  groupedRepos,
  sessionsInFolder,
  orphanSessions,
  workingCopyLabel,
  worktreeOwners,
  worktreeParentFolder,
  lineageIndex,
  sessionLineages,
  lineageColor,
  lineageColorOf,
  repoTree,
  filterRepoTree,
  countRepoNodes,
  type RepoTreeNode,
} from "./project.ts";

const repo = (name: string, extra: Partial<Repo> = {}): Repo => ({ name, ...extra });
const wt = (name: string, parent: string, extra: Partial<Repo> = {}): Repo => ({
  name,
  worktree: true,
  parent,
  ...extra,
});
const sess = (name: string, extra: Partial<Session> = {}): Session => ({ name, kind: "claude", ...extra });

describe("sessionFolder", () => {
  it("uses repo when present", () => {
    expect(sessionFolder(sess("s1", { repo: "agent-fleet" }))).toBe("agent-fleet");
  });
  it("falls back to the dir basename", () => {
    expect(sessionFolder(sess("s1", { dir: "/home/dev/repos/foo" }))).toBe("foo");
  });
  it("is empty for a repo-less session", () => {
    expect(sessionFolder(sess("s1"))).toBe("");
    expect(sessionFolder(sess("s1", { dir: "" }))).toBe("");
  });
});

describe("orderedRepos", () => {
  it("groups each base with its worktrees, then the next base", () => {
    const repos = [
      wt("af@wip-b", "agent-fleet"),
      repo("zzz"),
      repo("agent-fleet"),
      wt("af@wip-a", "agent-fleet"),
    ];
    expect(orderedRepos(repos).map((r) => r.name)).toEqual([
      "agent-fleet",
      "af@wip-a",
      "af@wip-b",
      "zzz",
    ]);
  });
  it("keeps a worktree whose base is gone as a trailing node", () => {
    const repos = [repo("keep"), wt("orphan@wt", "deleted-base")];
    expect(orderedRepos(repos).map((r) => r.name)).toEqual(["keep", "orphan@wt"]);
  });
  it("returns [] for no repos", () => {
    expect(orderedRepos([])).toEqual([]);
  });
});

describe("groupedRepos", () => {
  it("clusters each base with its worktrees; orphan worktree is its own group", () => {
    const repos = [
      wt("af@wip-b", "agent-fleet"),
      repo("zzz"),
      repo("agent-fleet"),
      wt("af@wip-a", "agent-fleet"),
      wt("orphan@wt", "deleted-base"),
    ];
    expect(groupedRepos(repos).map((g) => g.map((r) => r.name))).toEqual([
      ["agent-fleet", "af@wip-a", "af@wip-b"],
      ["zzz"],
      ["orphan@wt"],
    ]);
  });

  it("orders a base's worktrees by createdAt (oldest first), ignoring slug order", () => {
    const repos = [
      repo("agent-fleet"),
      // Slug order (name) would be b, c, z; creation order is z (oldest) → b → c.
      wt("af@wip-c", "agent-fleet", { createdAt: "2026-07-15T05:00:00Z" }),
      wt("af@wip-z", "agent-fleet", { createdAt: "2026-07-15T03:00:00Z" }),
      wt("af@wip-b", "agent-fleet", { createdAt: "2026-07-15T04:00:00Z" }),
    ];
    expect(groupedRepos(repos)[0].map((r) => r.name)).toEqual([
      "agent-fleet",
      "af@wip-z",
      "af@wip-b",
      "af@wip-c",
    ]);
  });

  it("falls back to name when a worktree has no createdAt", () => {
    const repos = [
      repo("agent-fleet"),
      wt("af@wip-b", "agent-fleet"),
      wt("af@wip-a", "agent-fleet", { createdAt: "2026-07-15T09:00:00Z" }),
    ];
    // The timestamped one sorts among the untimed by name (deterministic).
    expect(groupedRepos(repos)[0].map((r) => r.name)).toEqual([
      "agent-fleet",
      "af@wip-a",
      "af@wip-b",
    ]);
  });
});

describe("workingCopyLabel", () => {
  it("titles a worktree with its BASE project and its branch, not the folder", () => {
    const r = wt("agent-fleet@wip-ssvdkv3", "agent-fleet", { branch: "temp/ssvdkv3" });
    expect(workingCopyLabel(r.name, r)).toEqual({ project: "agent-fleet", branch: "temp/ssvdkv3" });
  });
  it("titles a base clone with its own folder and branch", () => {
    const r = repo("agent-fleet", { branch: "develop" });
    expect(workingCopyLabel(r.name, r)).toEqual({ project: "agent-fleet", branch: "develop" });
  });
  it("falls back to the folder's <base>@ prefix when the parent is gone", () => {
    const r = wt("agent-fleet@wip-x", "", { branch: "temp/x" });
    expect(workingCopyLabel(r.name, r).project).toBe("agent-fleet");
  });
  it("reports no branch for an SVN copy or an unknown folder — the caller shows the folder", () => {
    expect(workingCopyLabel("svn-wc", repo("svn-wc", { vcs: "svn", revision: "42" })).branch).toBe("");
    expect(workingCopyLabel("gone", undefined)).toEqual({ project: "gone", branch: "" });
  });
});

describe("sessionsInFolder", () => {
  it("filters to the folder, newest first", () => {
    const sessions = [
      sess("a", { repo: "agent-fleet", createdAt: "2026-01-01" }),
      sess("b", { repo: "other" }),
      sess("c", { repo: "agent-fleet", createdAt: "2026-02-01" }),
    ];
    expect(sessionsInFolder(sessions, "agent-fleet").map((s) => s.name)).toEqual(["c", "a"]);
  });
});

// ─── spawn lineage (ADR 0073) ───────────────────────────────────────────────
// Shapes only: what the rail nests under what, and which copies share a family.
// The five ways the parent fails to resolve each get a case, because every one of
// them is an ordinary state of a live rail, not an error path.

/** The tree as [name, [children…]] pairs, so a failure prints the whole shape. */
const shape = (n: RepoTreeNode): unknown => [n.repo.name, n.children.map(shape)];
const shapes = (ns: RepoTreeNode[]) => ns.map(shape);

describe("worktreeOwners", () => {
  it("names the session that ran in the folder FIRST — later ones are colleagues", () => {
    const sessions = [
      sess("late", { repo: "af@wt", createdAt: "2026-09-02T00:00:00Z" }),
      sess("first", { repo: "af@wt", createdAt: "2026-09-01T00:00:00Z" }),
    ];
    expect(worktreeOwners(sessions).get("af@wt")?.name).toBe("first");
  });

  it("breaks a createdAt tie by name, whichever order the list arrived in", () => {
    const a = sess("s-aaa", { repo: "af@wt", createdAt: "2026-09-01T00:00:00Z" });
    const b = sess("s-bbb", { repo: "af@wt", createdAt: "2026-09-01T00:00:00Z" });
    expect(worktreeOwners([a, b]).get("af@wt")?.name).toBe("s-aaa");
    expect(worktreeOwners([b, a]).get("af@wt")?.name).toBe("s-aaa");
  });

  it("prefers a timestamped session over one with no createdAt at all", () => {
    const sessions = [sess("untimed", { repo: "af@wt" }), sess("timed", { repo: "af@wt", createdAt: "2026-09-01T00:00:00Z" })];
    expect(worktreeOwners(sessions).get("af@wt")?.name).toBe("timed");
  });
});

describe("sessionLineages", () => {
  it("collapses a chain onto its topmost ancestor and counts the family", () => {
    const sessions = [
      sess("root"),
      sess("kid", { originSession: "root" }),
      sess("grandkid", { originSession: "kid" }),
      sess("stranger"),
    ];
    const lin = sessionLineages(sessions);
    expect(lin.get("grandkid")).toEqual({ root: "root", size: 3 });
    expect(lin.get("root")).toEqual({ root: "root", size: 3 });
    expect(lin.get("stranger")).toEqual({ root: "stranger", size: 1 });
  });

  it("ends the walk at a parent that is gone — deleted and archived parents are normal", () => {
    const lin = sessionLineages([sess("orphaned", { originSession: "deleted-long-ago" })]);
    expect(lin.get("orphaned")).toEqual({ root: "orphaned", size: 1 });
  });

  it("terminates on a cycle in the metadata instead of hanging the renderer", () => {
    const lin = sessionLineages([sess("a", { originSession: "b" }), sess("b", { originSession: "a" })]);
    // Which of the two is called the root doesn't matter; that they agree does.
    expect(lin.get("a")!.root).toBe(lin.get("b")!.root);
    expect(lin.get("a")).toEqual({ root: lin.get("a")!.root, size: 2 });
  });
});

describe("lineageColor", () => {
  it("gives a family of one no colour — a line on every row would say nothing", () => {
    expect(lineageColor({ root: "solo", size: 1 })).toBe("");
    expect(lineageColor(undefined)).toBe("");
  });

  it("gives every member of a family the same slot, chosen from the root's name", () => {
    const sessions = [sess("root"), sess("kid", { originSession: "root" })];
    const c = lineageColorOf(sessions, "root");
    expect(c).toMatch(/^var\(--lineage-[0-5]\)$/);
    expect(lineageColorOf(sessions, "kid")).toBe(c);
    // Same root, different list object → same slot: the colour is not positional, so
    // an unrelated session appearing or being deleted never repaints a family.
    expect(lineageColor({ root: "root", size: 9 })).toBe(c);
  });
});

// One case per early return. They need their own tests: at repoTree level the last
// two are invisible, because breakLineageCycles' cycle and dangling-edge branches
// land a badly-parented worktree on the root anyway — the same answer for a
// different reason, which is exactly the shape of a test that proves nothing.
describe("worktreeParentFolder", () => {
  const GROUP = new Set(["af", "af@x", "af@sib"]);
  const ask = (sessions: Session[], folder = "af@x") =>
    worktreeParentFolder(lineageIndex(sessions), folder, "af", GROUP);
  const owner = (extra: Partial<Session>) => sess("owner", { repo: "af@x", createdAt: "2026-09-01T00:00:00Z", ...extra });

  it("returns the copy the owner's parent session runs in", () => {
    expect(ask([owner({ originSession: "p" }), sess("p", { repo: "af@sib" })])).toBe("af@sib");
  });
  it("falls back to the root when nobody owns the copy", () => {
    expect(ask([])).toBe("af");
  });
  it("falls back to the root when the owner was raised by nobody", () => {
    expect(ask([owner({})])).toBe("af");
  });
  it("falls back to the root when the parent session is gone", () => {
    expect(ask([owner({ originSession: "archived" })])).toBe("af");
  });
  it("falls back to the root when the parent runs in no working copy at all", () => {
    expect(ask([owner({ originSession: "p" }), sess("p")])).toBe("af");
  });
  it("falls back to the root rather than nesting the copy under ITSELF", () => {
    expect(ask([owner({ originSession: "p" }), sess("p", { repo: "af@x" })])).toBe("af");
  });
  it("falls back to the root when the parent runs in ANOTHER repository", () => {
    expect(ask([owner({ originSession: "p" }), sess("p", { repo: "other@w" })])).toBe("af");
  });
  it("returns the base itself when the parent runs there — the base IS the root", () => {
    expect(ask([owner({ originSession: "p" }), sess("p", { repo: "af" })])).toBe("af");
  });
});

describe("repoTree", () => {
  const base = repo("af");
  const at = (n: number) => `2026-09-0${n}T00:00:00Z`;

  it("nests a worktree under the copy its owner's parent session runs in", () => {
    const repos = [base, wt("af@p", "af", { createdAt: at(1) }), wt("af@c", "af", { createdAt: at(2) })];
    const sessions = [
      sess("parent", { repo: "af@p", createdAt: at(1) }),
      sess("child", { repo: "af@c", createdAt: at(2), originSession: "parent" }),
    ];
    expect(shapes(repoTree(repos, sessions))).toEqual([["af", [["af@p", [["af@c", []]]]]]]);
  });

  it("keeps every level in createdAt order, so a new worktree lands at the end of its level", () => {
    const repos = [
      base,
      wt("af@p", "af", { createdAt: at(1) }),
      wt("af@z", "af", { createdAt: at(3) }), // name-last, created second
      wt("af@a", "af", { createdAt: at(4) }),
    ];
    const sessions = [
      sess("parent", { repo: "af@p", createdAt: at(1) }),
      sess("k1", { repo: "af@z", createdAt: at(3), originSession: "parent" }),
      sess("k2", { repo: "af@a", createdAt: at(4), originSession: "parent" }),
    ];
    expect(shapes(repoTree(repos, sessions))).toEqual([["af", [["af@p", [["af@z", []], ["af@a", []]]]]]]);
  });

  it("puts a worktree nobody owns under the base — its owner was deleted or archived", () => {
    const repos = [base, wt("af@x", "af", { createdAt: at(1) })];
    expect(shapes(repoTree(repos, []))).toEqual([["af", [["af@x", []]]]]);
  });

  it("puts it under the base when the PARENT session is gone", () => {
    const repos = [base, wt("af@x", "af", { createdAt: at(1) })];
    const sessions = [sess("owner", { repo: "af@x", createdAt: at(1), originSession: "archived-parent" })];
    expect(shapes(repoTree(repos, sessions))).toEqual([["af", [["af@x", []]]]]);
  });

  it("puts it under the base when the parent runs in ANOTHER repository — nesting cannot say that", () => {
    const repos = [base, repo("other"), wt("other@w", "other", { createdAt: at(1) }), wt("af@x", "af", { createdAt: at(2) })];
    const sessions = [
      sess("parent", { repo: "other@w", createdAt: at(1) }),
      sess("child", { repo: "af@x", createdAt: at(2), originSession: "parent" }),
    ];
    // af@x stays under af; what ties it to other@w is the spine colour, checked below.
    expect(shapes(repoTree(repos, sessions))).toEqual([
      ["af", [["af@x", []]]],
      ["other", [["other@w", []]]],
    ]);
  });

  it("refuses to nest a worktree under ITSELF when its parent was resumed inside it", () => {
    const repos = [base, wt("af@x", "af", { createdAt: at(1) })];
    const sessions = [
      sess("owner", { repo: "af@x", createdAt: at(1), originSession: "parent" }),
      // The parent ended up in the very copy its child created (a resume, a second launch).
      sess("parent", { repo: "af@x", createdAt: at(5) }),
    ];
    expect(shapes(repoTree(repos, sessions))).toEqual([["af", [["af@x", []]]]]);
  });

  it("breaks a cycle instead of recursing forever", () => {
    const repos = [base, wt("af@a", "af", { createdAt: at(1) }), wt("af@b", "af", { createdAt: at(2) })];
    // Corrupt metadata: each copy's owner claims to come from a session in the other.
    const sessions = [
      sess("A", { repo: "af@a", createdAt: at(1), originSession: "B" }),
      sess("B", { repo: "af@b", createdAt: at(2), originSession: "A" }),
    ];
    const tree = repoTree(repos, sessions);
    expect(countRepoNodes(tree)).toBe(3); // every copy is rendered exactly once
    expect(shapes(tree)).toEqual([["af", [["af@a", [["af@b", []]]]]]]);
  });

  it("gives one family one spine colour, across base clones as well", () => {
    const repos = [base, repo("other"), wt("other@w", "other", { createdAt: at(1) }), wt("af@x", "af", { createdAt: at(2) })];
    const sessions = [
      sess("parent", { repo: "other@w", createdAt: at(1) }),
      sess("child", { repo: "af@x", createdAt: at(2), originSession: "parent" }),
    ];
    const [afTree, otherTree] = repoTree(repos, sessions);
    const near = otherTree.children[0].spine;
    expect(near).toMatch(/^var\(--lineage-[0-5]\)$/);
    expect(afTree.children[0].spine).toBe(near);
    expect(afTree.spine).toBe(""); // the base clone hosts nobody — no family, no line
  });

  it("leaves a lone worktree without a spine colour", () => {
    const repos = [base, wt("af@x", "af", { createdAt: at(1) })];
    const sessions = [sess("solo", { repo: "af@x", createdAt: at(1) })];
    expect(repoTree(repos, sessions)[0].children[0].spine).toBe("");
  });

  it("keeps an orphaned worktree as its own root, as the flat layout did", () => {
    expect(shapes(repoTree([repo("keep"), wt("orphan@wt", "gone")], []))).toEqual([
      ["keep", []],
      ["orphan@wt", []],
    ]);
  });
});

describe("filterRepoTree", () => {
  const tree = repoTree(
    [repo("af"), wt("af@p", "af", { createdAt: "2026-09-01T00:00:00Z" }), wt("af@c", "af", { createdAt: "2026-09-02T00:00:00Z" })],
    [
      sess("parent", { repo: "af@p", createdAt: "2026-09-01T00:00:00Z" }),
      sess("child", { repo: "af@c", createdAt: "2026-09-02T00:00:00Z", originSession: "parent" }),
    ],
  );

  it("keeps every ancestor of a match as its anchor", () => {
    expect(shapes(filterRepoTree(tree, (r) => r.name === "af@c"))).toEqual([["af", [["af@p", [["af@c", []]]]]]]);
  });

  it("drops a branch with no match anywhere in it", () => {
    expect(filterRepoTree(tree, (r) => r.name === "nothing")).toEqual([]);
  });

  it("counts the whole forest, roots included", () => {
    expect(countRepoNodes(tree)).toBe(3);
  });
});

describe("orphanSessions", () => {
  it("returns sessions whose folder is not a known repo", () => {
    const repos = [repo("agent-fleet")];
    const sessions = [
      sess("in", { repo: "agent-fleet" }),
      sess("shell"), // no repo/dir
      sess("gone", { repo: "removed-repo" }),
    ];
    expect(orphanSessions(sessions, repos).map((s) => s.name).sort()).toEqual(["gone", "shell"]);
  });
});
