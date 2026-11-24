import { describe, it, expect } from "vitest";
import type { Repo } from "../features/repos/store.ts";
import type { Session } from "../types/session.ts";
import { railOrder } from "../features/sessions/rotate.ts";
import { planStopTree, liveSessionCount } from "../features/repos/stopTree.ts";
import { planTree } from "../features/repos/deleteTree.ts";
import {
  sessionFolder,
  orderedRepos,
  groupedRepos,
  sessionsInFolder,
  orphanSessions,
  spaceGroups,
  terminalGroups,
  shellPlaceOf,
  placeRepos,
  withoutTerminals,
  isTerminalSession,
  shellPlaceKey,
  ssmMachineKey,
  isUnattendedOnlyFolder,
  withoutUnattended,
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
  childNodes,
  lineageNodes,
  railNodeSessions,
  sessionAutoKind,
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

// ADR 0142 decision 9: a space is told from a repository by the Agent's `space` (the directory's
// root), never by the folder name, so a space named like a repository stays out of its node.
describe("spaces", () => {
  const repos = [repo("notes")];
  const sessions = [
    sess("in-repo", { repo: "notes", dir: "/home/dev/repos/notes", createdAt: "2026-11-01T00:00:00Z" }),
    sess("in-space", { repo: "notes", dir: "/home/dev/spaces/notes", space: "notes", createdAt: "2026-11-02T00:00:00Z" }),
    sess("other-space", { repo: "q3-abcdef", dir: "/home/dev/spaces/q3-abcdef", space: "q3-abcdef", createdAt: "2026-11-03T00:00:00Z" }),
    sess("older-in-space", { repo: "notes", dir: "/home/dev/spaces/notes", space: "notes", createdAt: "2026-10-01T00:00:00Z" }),
    sess("home-shell"),
  ];

  it("keeps space sessions out of the repository of the same name", () => {
    expect(sessionFolder(sessions[1])).toBe("");
    expect(sessionsInFolder(sessions, "notes").map((s) => s.name)).toEqual(["in-repo"]);
  });

  it("keeps space sessions out of Other", () => {
    expect(orphanSessions(sessions, repos).map((s) => s.name)).toEqual(["home-shell"]);
  });

  it("groups space sessions by space, the most recently used space first", () => {
    expect(spaceGroups(sessions).map((g) => [g.folder, g.sessions.map((s) => s.name)])).toEqual([
      ["q3-abcdef", ["other-space"]],
      ["notes", ["in-space", "older-in-space"]],
    ]);
  });

  it("is empty without space sessions", () => {
    expect(spaceGroups([sessions[0], sessions[4]])).toEqual([]);
  });

  it("puts the Spaces section between the repositories and Other in rail order", () => {
    expect(railOrder(sessions, repos, null).map((s) => s.name)).toEqual([
      "in-repo",
      "other-space",
      "in-space",
      "older-in-space",
      "home-shell",
    ]);
  });
});

describe("repoTree auto-run / schedule groups", () => {
  const t = (n: number) => `2026-09-0${n}T00:00:00Z`;
  const repos = [
    repo("af"),
    wt("af@hand", "af", { createdAt: t(1) }),
    wt("af@run", "af", { createdAt: t(2) }),
    wt("af@rev", "af", { createdAt: t(3) }),
    wt("af@cron", "af", { createdAt: t(4) }),
    wt("af@hf", "af", { createdAt: t(5) }),
    wt("af@kid", "af", { createdAt: t(6) }),
  ];
  const sessions = [
    sess("hand", { repo: "af@hand", createdAt: t(1), origin: "user" }),
    sess("run", { repo: "af@run", createdAt: t(2), origin: "queue" }),
    // The run's reviewer: a child spawned from the run's session, in its own worktree.
    sess("rev", { repo: "af@rev", createdAt: t(3), origin: "session", originSession: "run" }),
    sess("cron", { repo: "af@cron", createdAt: t(4), origin: "schedule" }),
    // A handoff keeps origin=user while carrying originSession: it is the member's.
    sess("hf", { repo: "af@hf", createdAt: t(5), origin: "user", originSession: "run" }),
    // A child of a hand-started session stays with it.
    sess("kid", { repo: "af@kid", createdAt: t(6), origin: "session", originSession: "hand" }),
  ];
  const tree = repoTree(repos, sessions)[0];
  const names = (ns: RepoTreeNode[]) => ns.map((n) => n.repo.name);

  it("takes auto-run and schedule worktrees out of the tree's display", () => {
    // Neither is displayed (the Auto-run and Schedules views list them) but both stay in the scope.
    expect(shapes(tree.detached ?? [])).toEqual([["af@run", [["af@rev", []]]], ["af@cron", []]]);
  });

  it("leaves hand-started work, handoffs and a hand-started parent's child where they are", () => {
    expect(shapes(tree.children)).toEqual([["af@hand", [["af@kid", []]]], ["af@hf", []]]);
  });

  it("keeps a member's own copy in the tree even when its parent copy is unattended", () => {
    expect(names(tree.detached ?? [])).not.toContain("af@hf");
  });

  it("childNodes sees what the tree shows and lineageNodes the unattended copies too", () => {
    expect(names(childNodes(tree))).toEqual(["af@hand", "af@hf"]);
    expect(names(lineageNodes(tree))).toEqual(["af@hand", "af@hf", "af@run", "af@cron"]);
    expect(countRepoNodes([tree])).toBe(4); // the root, hand, its kid, hf
  });

  it("filterRepoTree shows nothing for a schedule match and keeps its scope", () => {
    expect(filterRepoTree([tree], (r) => r.name === "af@cron")).toEqual([]);
    expect(filterRepoTree([tree], (r) => r.name === "af@hand")[0].children.map((c) => c.repo.name)).toEqual(["af@hand"]);
  });

  it("has nothing detached when nothing is unattended", () => {
    expect(repoTree([repo("x"), wt("x@a", "x")], [sess("a", { repo: "x@a", origin: "user" })])[0].detached).toBeUndefined();
  });
});

describe("repoTree: a handoff the member made from a run", () => {
  const t = (n: number) => `2026-09-0${n}T00:00:00Z`;
  const repos = [repo("af"), wt("af@a", "af", { createdAt: t(1) }), wt("af@h", "af", { createdAt: t(2) }), wt("af@r", "af", { createdAt: t(3) })];
  const sessions = [
    sess("A", { repo: "af@a", createdAt: t(1), origin: "queue" }),
    // The member opened a handoff A proposed: origin stays user, originSession names A.
    sess("H", { repo: "af@h", createdAt: t(2), origin: "user", originSession: "A" }),
    // H spawned a reviewer.
    sess("R", { repo: "af@r", createdAt: t(3), origin: "session", originSession: "H" }),
  ];
  const tree = repoTree(repos, sessions)[0];
  const names = (ns: RepoTreeNode[]) => ns.map((n) => n.repo.name);

  it("keeps H and its reviewer R in the ordinary tree, and A out of it", () => {
    expect(shapes(tree.children)).toEqual([["af@h", [["af@r", []]]]]);
    expect(names(tree.detached ?? [])).toEqual(["af@a"]);
  });

  it("still counts H as part of what a stop / delete on A covers, once, flagged as listed elsewhere", () => {
    const a = tree.detached![0];
    expect(a.children).toEqual([]); // not displayed under A…
    expect(names(lineageNodes(a))).toEqual(["af@h"]); // …but part of its scope
    const live = sessions.map((x) => ({ ...x, alive: true }));
    const stop = planStopTree(a, live);
    expect(stop.map((g) => [g.repo.name, g.detached])).toEqual([["af@a", false], ["af@h", true], ["af@r", true]]);
    expect(liveSessionCount(a, live)).toBe(3);
    const del = planTree(a, sessions);
    expect(del.map((p) => p.repo.name)).toEqual(["af@a", "af@h", "af@r"]);
    // From the root every copy shows up once, none flagged.
    expect(planStopTree(tree, live).map((g) => [g.repo.name, g.detached])).toEqual([["af@h", false], ["af@r", false], ["af@a", true]]);
  });

  it("falls back to ordinary display for a cycle or a vanished parent", () => {
    const byName = new Map([
      ["x", sess("x", { origin: "session", originSession: "y" })],
      ["y", sess("y", { origin: "session", originSession: "x" })],
    ]);
    expect(sessionAutoKind(byName.get("x"), byName)).toBe("");
    expect(sessionAutoKind(sess("z", { origin: "session", originSession: "gone" }), byName)).toBe("");
  });
});

describe("repoTree: filtering and mixed copies", () => {
  const t = (n: number) => `2026-09-0${n}T00:00:00Z`;
  const repos = [repo("af"), wt("af@a", "af", { createdAt: t(1) }), wt("af@h", "af", { createdAt: t(2) }), wt("af@r", "af", { createdAt: t(3) })];
  const live = (x: Session): Session => ({ ...x, alive: true });
  const sessions = [
    live(sess("A", { repo: "af@a", createdAt: t(1), origin: "queue" })),
    live(sess("H", { repo: "af@h", createdAt: t(2), origin: "user", originSession: "A" })),
    live(sess("R", { repo: "af@r", createdAt: t(3), origin: "session", originSession: "H" })),
  ];
  const tree = repoTree(repos, sessions);
  const names = (ns: RepoTreeNode[]) => ns.map((n) => n.repo.name);

  it("a search that matches only A shows nothing: the run is the Auto-run view's, not the tree's", () => {
    expect(filterRepoTree(tree, (r) => r.name === "af@a")).toEqual([]);
  });

  it("a search that keeps only R shows it under the root and does not surface A's scope", () => {
    const f = filterRepoTree(tree, (r) => r.name === "af@r");
    expect(shapes(f)).toEqual([["af", [["af@h", [["af@r", []]]]]]]);
    // The display is H/R only, but the base's stop and delete still reach A once, as on main.
    expect(planStopTree(f[0], sessions).map((g) => g.repo.name).sort()).toEqual(["af@a", "af@h", "af@r"]);
    expect(planTree(f[0], sessions).map((p) => p.repo.name).sort()).toEqual(["af", "af@a", "af@h", "af@r"]);
    expect(planTree(f[0], sessions).filter((p) => p.repo.name === "af@a").map((p) => p.detached)).toEqual([true]);
  });

  it("a run's copy where a schedule also runs stays ordinary, and neither's session is listed", () => {
    const ss = [...sessions, live(sess("cron", { repo: "af@a", createdAt: t(5), origin: "schedule" }))];
    const r = repoTree(repos, ss)[0];
    const a = r.children.find((c) => c.repo.name === "af@a")!;
    const byName = new Map(ss.map((x) => [x.name, x]));
    expect(railNodeSessions(a, ss, byName).map((x) => x.name)).toEqual(["H", "R"]);
  });

  it("a copy where a run and a schedule both ran stays in the tree, so it is not 'unattended only' either", () => {
    const ss = [live(sess("A", { repo: "af@a", createdAt: t(1), origin: "queue" })), live(sess("cron", { repo: "af@a", createdAt: t(2), origin: "schedule" }))];
    const rs = [repo("af"), wt("af@a", "af", { createdAt: t(1) })];
    expect(names(repoTree(rs, ss)[0].children)).toEqual(["af@a"]);
    expect(isUnattendedOnlyFolder(ss, rs[1], rs)).toBe(false);
    expect(isUnattendedOnlyFolder([ss[0]], rs[1], rs)).toBe(true);
  });

  it("a schedule's copy is out of the display but inside the base's stop and delete scope, before and after filtering", () => {
    const rs = [repo("af"), wt("af@cron", "af", { createdAt: t(1) })];
    const ss = [live(sess("cron", { repo: "af@cron", createdAt: t(1), origin: "schedule" }))];
    const tree = repoTree(rs, ss);
    for (const f of [tree, filterRepoTree(tree, () => true)]) {
      expect(f[0].children).toEqual([]);
      expect(planStopTree(f[0], ss).map((g) => [g.repo.name, g.detached])).toEqual([["af@cron", true]]);
      expect(planTree(f[0], ss).map((p) => [p.repo.name, p.detached])).toEqual([["af", false], ["af@cron", true]]);
    }
    // A filter that keeps only the base leaves the schedule's copy out of the scope, as it did when
    // the copy was grouped: the scope narrows with the filter.
    const only = filterRepoTree(tree, (r) => r.name === "af");
    expect(planStopTree(only[0], ss)).toEqual([]);
    expect(planTree(only[0], ss).map((p) => p.repo.name)).toEqual(["af"]);
    // Displayed: the base only; operational (what stop and delete reach): the schedule's copy matches too.
    const op = filterRepoTree(tree, (r) => r.name === "af", () => true);
    expect(op[0].children).toEqual([]);
    expect(planStopTree(op[0], ss).map((g) => g.repo.name)).toEqual(["af@cron"]);
    expect(planTree(op[0], ss).map((p) => [p.repo.name, p.detached])).toEqual([["af", false], ["af@cron", true]]);
    expect(filterRepoTree(tree, () => false)).toEqual([]);
  });

  it("a copy a run made but where the member later started a session stays in the ordinary tree", () => {
    const mixed = [...sessions, live(sess("hand", { repo: "af@a", createdAt: t(4), origin: "user" }))];
    const r = repoTree(repos, mixed)[0];
    expect(names(r.children)).toContain("af@a");
    expect(planStopTree(r, mixed).map((g) => g.repo.name).sort()).toEqual(["af@a", "af@h", "af@r"]); // no copy twice
  });

  it("railOrder follows the drawn order: own sessions, then ordinary copies, never an unattended one", () => {
    const rs = [repo("af"), wt("af@w", "af", { createdAt: t(1) })];
    const ss = [
      sess("H", { repo: "af", createdAt: t(1), origin: "user" }),
      sess("S", { repo: "af", createdAt: t(3), origin: "schedule" }),
      sess("W", { repo: "af@w", createdAt: t(2), origin: "user" }),
    ];
    const byName = new Map(ss.map((x) => [x.name, x]));
    expect(railNodeSessions(repoTree(rs, ss)[0], ss, byName).map((x) => x.name)).toEqual(["H", "W"]);
  });
});

describe("auto-run sessions outside the member's lists", () => {
  const repos = [repo("af")];
  const sessions = [
    sess("shellrun", { origin: "queue", createdAt: "2026-09-01T00:00:00Z" }), // a common category: no repository
    sess("shellrev", { origin: "session", originSession: "shellrun" }),
    sess("hand", { origin: "user" }),
    sess("inrepo", { repo: "af", origin: "queue" }),
  ];
  it("withoutUnattended drops a run and what it started, and keeps the member's own", () => {
    expect(withoutUnattended(sessions).map((s) => s.name)).toEqual(["hand"]);
  });
  it("leaves them out of the repo-less sessions and the rotation order", () => {
    expect(orphanSessions(sessions, repos).map((s) => s.name)).toEqual(["hand"]);
    expect(railOrder(sessions, repos, null).map((s) => s.name)).toEqual(["hand"]);
  });
  it("tells a copy only auto-run works in", () => {
    const wtree = { name: "af", worktree: true, parent: "base" };
    const all = [{ name: "base" }, wtree];
    expect(isUnattendedOnlyFolder(sessions, wtree, all)).toBe(true);
    expect(isUnattendedOnlyFolder([...sessions, sess("m", { repo: "af", origin: "user" })], wtree, all)).toBe(false);
    expect(isUnattendedOnlyFolder(sessions, { name: "empty", worktree: true, parent: "base" }, all)).toBe(false);
    // A base clone an auto-run reused, and a worktree whose base is not listed, are rows of the tree.
    expect(isUnattendedOnlyFolder(sessions, { name: "af" }, all)).toBe(false);
    expect(isUnattendedOnlyFolder(sessions, wtree, [wtree])).toBe(false);
    expect(isUnattendedOnlyFolder(sessions, undefined, all)).toBe(false);
  });
});

describe("a filter narrows stop and delete by every session, hidden auto-run included", () => {
  const t = (n: number) => `2026-09-0${n}T00:00:00Z`;
  const repos = [repo("af"), wt("af@run", "af", { createdAt: t(1) }), wt("af@h", "af", { createdAt: t(2) })];
  const live = (x: Session): Session => ({ ...x, alive: true });
  const sessions = [
    live(sess("run", { repo: "af@run", createdAt: t(1), origin: "queue" })),
    // The member's handoff from the run: a copy of their own, listed elsewhere.
    live(sess("h", { repo: "af@h", createdAt: t(2), origin: "user", originSession: "run" })),
  ];
  const tree = repoTree(repos, sessions);
  const display = (r: Repo) => r.name === "af";

  it("reaches the run when its own title matches, as it did on main", () => {
    const f = filterRepoTree(tree, display, (r) => r.name === "af" || r.name === "af@run");
    expect(planStopTree(f[0], sessions).map((g) => g.repo.name).sort()).toEqual(["af@run"]);
    expect(planTree(f[0], sessions).map((p) => p.repo.name).sort()).toEqual(["af", "af@run"]);
  });

  it("reaches neither the run nor the member's handoff from it when only the base matches", () => {
    const f = filterRepoTree(tree, display, display);
    expect(planStopTree(f[0], sessions).map((g) => g.repo.name)).toEqual([]);
    expect(planTree(f[0], sessions).map((p) => p.repo.name)).toEqual(["af"]);
  });
});

describe("a filter and a mixed copy an auto-run's title matches", () => {
  const t = (n: number) => `2026-09-0${n}T00:00:00Z`;
  const repos = [repo("af"), wt("af@mix", "af", { createdAt: t(1) })];
  const live = (x: Session): Session => ({ ...x, alive: true });
  const sessions = [
    live(sess("run", { repo: "af@mix", createdAt: t(1), origin: "queue" })),
    live(sess("hand", { repo: "af@mix", createdAt: t(2), origin: "user" })),
  ];
  it("reaches the copy from the base when the run's title is what matched", () => {
    const f = filterRepoTree(repoTree(repos, sessions), (r) => r.name === "af", (r) => r.name === "af" || r.name === "af@mix");
    expect(planStopTree(f[0], sessions).map((g) => g.repo.name)).toEqual(["af@mix"]);
    expect(planTree(f[0], sessions).map((p) => p.repo.name).sort()).toEqual(["af", "af@mix"]);
  });
});

// ADR 0142 decision 10: shells and SSM logins are the Terminal view's. The Sessions view's helpers
// leave them out, and terminalGroups files them by place and by machine.
describe("terminals", () => {
  const repos = [repo("app"), wt("app@wip-x", "app"), repo("notes")];
  const shell = (name: string, extra: Partial<Session> = {}): Session => sess(name, { kind: "shell", ...extra });
  const ssm = (name: string, target: string, extra: Partial<Session> = {}, over: Partial<NonNullable<Session["ssm"]>> = {}): Session =>
    sess(name, { kind: "ssm", ssm: { target, region: "ap-northeast-1", accountId: "111122223333", profile: "ops", ...over }, ...extra });
  const sessions: Session[] = [
    shell("home1", { repo: "dev", dir: "/home/dev", createdAt: "2026-11-01T00:00:00Z" }),
    shell("home-db", { createdAt: "2026-11-02T00:00:00Z" }), // af-db connect: no dir at all
    shell("in-app", { repo: "app", dir: "/home/dev/repos/app", createdAt: "2026-11-03T00:00:00Z" }),
    shell("in-wt", { repo: "app@wip-x", dir: "/home/dev/repos/app@wip-x", createdAt: "2026-11-04T00:00:00Z" }),
    // A space that shares a repository's folder name is still a space.
    shell("in-space", { repo: "notes", dir: "/home/dev/spaces/notes", space: "notes", createdAt: "2026-11-05T00:00:00Z" }),
    shell("gone", { repo: "removed", dir: "/home/dev/repos/removed", createdAt: "2026-11-06T00:00:00Z" }),
    ssm("m1-old", "i-aaa", { createdAt: "2026-11-01T00:00:00Z" }),
    ssm("m1-new", "i-aaa", { createdAt: "2026-11-07T00:00:00Z" }, { profile: "other-profile", document: "AF-Shell" }),
    ssm("m2", "i-bbb", { createdAt: "2026-11-03T00:00:00Z" }),
    ssm("m2-other-account", "i-bbb", { createdAt: "2026-11-04T00:00:00Z" }, { accountId: "999900001111" }),
    sess("agent", { repo: "app", dir: "/home/dev/repos/app" }),
  ];

  it("files shells by the place they were opened in: home, a repository, a space", () => {
    const { shells } = terminalGroups(sessions, repos);
    expect(shells.map((g) => [g.key, g.sessions.map((s) => s.name)])).toEqual([
      // a removed repository's shell and the database tab's shell have no place of their own
      ["shell:home", ["gone", "home-db", "home1"]],
      ["shell:repo:app", ["in-app"]],
      ["shell:repo:app@wip-x", ["in-wt"]],
      ["shell:space:notes", ["in-space"]],
    ]);
  });

  it("does not file a space's shell under the repository of the same name", () => {
    const { shells } = terminalGroups(sessions, repos);
    expect(shells.some((g) => g.key === "shell:repo:notes")).toBe(false);
  });

  it("groups SSM logins by account, region and instance, and keeps a different profile or document in its group", () => {
    const { ssm: groups } = terminalGroups(sessions, repos);
    expect(groups.map((g) => [g.key, g.sessions.map((s) => s.name)])).toEqual([
      ["ssm:111122223333|ap-northeast-1|i-aaa", ["m1-new", "m1-old"]],
      ["ssm:999900001111|ap-northeast-1|i-bbb", ["m2-other-account"]],
      ["ssm:111122223333|ap-northeast-1|i-bbb", ["m2"]],
    ]);
  });

  it("puts an SSM login whose row names no target in a group of its own, last", () => {
    const { ssm: groups } = terminalGroups([sess("old", { kind: "ssm", createdAt: "2027-01-01T00:00:00Z" }), ssm("m", "i-aaa", { createdAt: "2026-01-01T00:00:00Z" })], repos);
    expect(groups.map((g) => [g.key, g.target])).toEqual([
      ["ssm:111122223333|ap-northeast-1|i-aaa", "i-aaa"],
      ["ssm:?", ""],
    ]);
  });

  it("leaves agents out", () => {
    expect(terminalGroups([sess("a", { repo: "app" })], repos)).toEqual({ shells: [], ssm: [] });
  });

  // Session.repo is the base name of the launch directory: a shell in the home directory of a
  // member called "dev" has repo "dev", which is also the name of a clone.
  it("does not mistake the home directory for a working copy of the same name", () => {
    const clone = [repo("dev"), repo("app")];
    const home = shell("in-home", { repo: "dev", dir: "/home/dev" });
    const copy = shell("in-copy", { repo: "dev", dir: "/home/dev/repos/dev" });
    const names = placeRepos(clone);
    expect(shellPlaceOf(home, names)).toEqual({ kind: "home" });
    expect(shellPlaceOf(copy, names)).toEqual({ kind: "repo", folder: "dev" });
    // A row without a dir (an older list) is taken at its word.
    expect(shellPlaceOf(shell("old", { repo: "app" }), names)).toEqual({ kind: "repo", folder: "app" });
    // With the copy's real path in the list, another `repos/app` under home is not that copy.
    const real = placeRepos([repo("app", { path: "/home/dev/repos/app" })]);
    expect(shellPlaceOf(shell("a", { repo: "app", dir: "/home/dev/repos/app" }), real)).toEqual({ kind: "repo", folder: "app" });
    expect(shellPlaceOf(shell("b", { repo: "app", dir: "/home/dev/notes/repos/app" }), real)).toEqual({ kind: "home" });
    // Another spelling of the same path is the same copy: the Agent keeps a launch dir as written.
    expect(shellPlaceOf(shell("c", { repo: "app", dir: "/home/dev//repos/app/" }), real)).toEqual({ kind: "repo", folder: "app" });
    // The working-copy id both sides carry settles it whatever the path looks like (a symlink).
    const withId = placeRepos([repo("app", { path: "/home/dev/repos/app", workingCopyId: "wc_1" })]);
    expect(shellPlaceOf(shell("d", { repo: "app", dir: "/srv/link/app", workingCopyId: "wc_1" }), withId)).toEqual({ kind: "repo", folder: "app" });
    expect(shellPlaceOf(shell("e", { repo: "app", dir: "/home/dev/repos/app", workingCopyId: "wc_other" }), withId)).toEqual({ kind: "home" });
    // ...and it leads to the copy even when the launch directory has another name: /home/dev/short
    // is a symlink to /home/dev/repos/app, and the Agent recorded repo "short".
    expect(shellPlaceOf(shell("f", { repo: "short", dir: "/home/dev/short", workingCopyId: "wc_1" }), withId)).toEqual({ kind: "repo", folder: "app" });
    // A file copy of a clone carries the same id: two listed copies with one id are told apart by
    // the directory, never by their order in the list.
    const twins = placeRepos([repo("app", { path: "/home/dev/repos/app", workingCopyId: "wc_1" }), repo("app-copy", { path: "/home/dev/repos/app-copy", workingCopyId: "wc_1" })]);
    expect(shellPlaceOf(shell("g", { repo: "app-copy", dir: "/home/dev/repos/app-copy", workingCopyId: "wc_1" }), twins)).toEqual({ kind: "repo", folder: "app-copy" });
    expect(shellPlaceOf(shell("h", { repo: "app", dir: "/home/dev/repos/app", workingCopyId: "wc_1" }), twins)).toEqual({ kind: "repo", folder: "app" });
    expect(terminalGroups([shell("g", { repo: "app-copy", dir: "/home/dev/repos/app-copy", workingCopyId: "wc_1" }), shell("h", { repo: "app", dir: "/home/dev/repos/app", workingCopyId: "wc_1" })], [repo("app", { path: "/home/dev/repos/app", workingCopyId: "wc_1" }), repo("app-copy", { path: "/home/dev/repos/app-copy", workingCopyId: "wc_1" })]).shells.map((g) => [g.key, g.sessions.map((x) => x.name)])).toEqual([["shell:repo:app", ["h"]], ["shell:repo:app-copy", ["g"]]]);
    expect(terminalGroups([shell("f", { repo: "short", dir: "/home/dev/short", workingCopyId: "wc_1" })], [repo("app", { path: "/home/dev/repos/app", workingCopyId: "wc_1" })]).shells.map((g) => g.key)).toEqual(["shell:repo:app"]);
    expect(terminalGroups([home, copy], clone).shells.map((g) => [g.key, g.sessions.map((s) => s.name)])).toEqual([
      ["shell:home", ["in-home"]],
      ["shell:repo:dev", ["in-copy"]],
    ]);
  });

  it("names the group a chip reveals the way the view keys it", () => {
    expect(shellPlaceKey({ kind: "home" })).toBe("shell:home");
    expect(shellPlaceKey({ kind: "repo", folder: "app" })).toBe("shell:repo:app");
    expect(shellPlaceKey({ kind: "space", folder: "notes" })).toBe("shell:space:notes");
    expect(ssmMachineKey({ target: "i-aaa" })).toBe("ssm:||i-aaa");
  });

  it("keeps terminals out of the Sessions view's lists", () => {
    expect(isTerminalSession(sessions[0])).toBe(true);
    expect(withoutTerminals(sessions).map((s) => s.name)).toEqual(["agent"]);
    // Other: neither the home shells nor the removed repository's shell.
    expect(orphanSessions(sessions, repos).map((s) => s.name)).toEqual([]);
    // Spaces: a space with only a shell has no row.
    expect(spaceGroups(sessions)).toEqual([]);
    // The tree: the node's rows carry no shell. The swipe rotation keeps them in its order, as a
    // position to step on from (a shell pane has to keep its place).
    const node = repoTree(repos, sessions).find((n) => n.repo.name === "app")!;
    const byName = new Map(sessions.map((s) => [s.name, s]));
    expect(railNodeSessions(node, sessions, byName).map((s) => s.name)).toEqual(["agent"]);
    expect(railNodeSessions(node, sessions, byName, true).map((s) => s.name)).toEqual(["in-app", "agent", "in-wt"]);
    expect(railOrder(sessions, repos, null).map((s) => s.name)).toContain("in-space");
  });
});

describe("a schedule that also shows in the Sessions view", () => {
  const t = (n: number) => `2026-09-0${n}T00:00:00Z`;
  const repos = [repo("af"), wt("af@cron", "af", { createdAt: t(1) }), wt("af@kid", "af", { createdAt: t(2) }), wt("af@plain", "af", { createdAt: t(3) })];
  const sessions = [
    sess("cron", { repo: "af@cron", createdAt: t(1), origin: "schedule", scheduleId: "s1", worktree: true }),
    sess("kid", { repo: "af@kid", createdAt: t(2), origin: "session", originSession: "cron", worktree: true }),
    // A child that shares the root's folder, with no worktree of its own.
    sess("plain", { repo: "af@plain", createdAt: t(3), origin: "session", originSession: "cron" }),
  ];
  const on = new Set(["s1"]);
  const names = (ns: RepoTreeNode[]) => ns.map((n) => n.repo.name);

  it("off: nothing changes", () => {
    expect(withoutUnattended(sessions).map((s) => s.name)).toEqual([]);
    expect(names(repoTree(repos, sessions)[0].children)).toEqual([]);
    expect(withoutUnattended(sessions, new Set()).map((s) => s.name)).toEqual([]);
  });
  it("on: a child with a worktree is listed, the root and a child without one are not", () => {
    expect(withoutUnattended(sessions, on).map((s) => s.name)).toEqual(["kid"]);
  });
  it("on: its worktree is an ordinary tree row, the others stay detached", () => {
    const tree = repoTree(repos, sessions, on)[0];
    expect(names(tree.children)).toEqual(["af@kid"]);
    // The root's copy stays detached, with the child that has no worktree nested under it.
    expect(shapes(tree.detached ?? [])).toEqual([["af@cron", [["af@plain", []]]]]);
  });
  it("on: stop / delete still reach every copy once, displayed or detached", () => {
    const tree = repoTree(repos, sessions, on)[0];
    const reach = (n: RepoTreeNode): string[] => lineageNodes(n).flatMap((c) => [c.repo.name, ...reach(c)]);
    // Walkers dedupe by folder (see lineageNodes); what matters is nothing is lost.
    expect([...new Set(reach(tree))].sort()).toEqual(["af@cron", "af@kid", "af@plain"]);
  });
  it("on: the filter's match rule finds the child and not the root", () => {
    const hit = (folder: string) => sessionsInFolder(withoutUnattended(sessions, on), folder).length > 0;
    expect(hit("af@kid")).toBe(true);
    expect(hit("af@cron")).toBe(false);
    expect(hit("af@plain")).toBe(false);
  });
  it("on: the rotation order and the unattended-only test follow", () => {
    expect(railOrder(sessions, repos, null, on).map((s) => s.name)).toEqual(["kid"]);
    expect(isUnattendedOnlyFolder(sessions, repos[2], repos, on)).toBe(false);
    expect(isUnattendedOnlyFolder(sessions, repos[2], repos)).toBe(true);
  });
  it("a root without an id, or whose schedule is not flagged, keeps today's behaviour", () => {
    const noId = sessions.map((s) => (s.name === "cron" ? { ...s, scheduleId: undefined } : s));
    expect(withoutUnattended(noId, on)).toEqual([]);
    expect(withoutUnattended(sessions, new Set(["other"]))).toEqual([]);
  });
});
