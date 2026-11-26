import { describe, expect, it } from "vitest";
import { activeGroups, activeScope, unattendedScope, cycleCuts, needsMember, OTHER_GROUP } from "./active.ts";
import { waitingSessions } from "../../app/activity/badges.ts";
import type { Repo } from "../repos/store.ts";
import type { Space } from "../spaces/store.ts";
import type { WorkingSet } from "../../lib/workingSets.ts";
import type { Session } from "../../types/session.ts";

const s = (name: string, over: Partial<Session> = {}): Session => ({ name, kind: "claude", alive: true, state: "idle", ...over });
const repo = (name: string, over: Partial<Repo> = {}): Repo => ({ name, ...over });
const set = (over: Partial<WorkingSet>): WorkingSet => ({ id: "w1", name: "A", repos: [], convs: [], sessions: [], schedules: [], ...over });
const names = (list: Session[]): string[] => list.map((x) => x.name);

describe("activeScope", () => {
  it("lists running sessions and leaves an ordinary stopped one out", () => {
    expect(names(activeScope([s("a"), s("b", { alive: false })], null))).toEqual(["a"]);
  });

  it("keeps a stopped session that still needs the member", () => {
    const list = [s("q", { alive: false, carried: "question" }), s("h", { alive: false, handoffPending: true }), s("x", { alive: false })];
    expect(names(activeScope(list, null))).toEqual(["q", "h"]);
    expect(needsMember(s("live", { carried: "question" }))).toBe(false);
  });

  it("keeps every descendant of a listed session, stopped ones included, at any depth", () => {
    const list = [
      s("p"),
      s("c1", { alive: false, originSession: "p" }),
      s("g1", { alive: false, originSession: "c1" }),
      s("orphan", { alive: false, originSession: "gone" }),
    ];
    expect(names(activeScope(list, null))).toEqual(["p", "c1", "g1"]);
  });

  it("leaves shells and SSM logins to the Terminal view", () => {
    expect(names(activeScope([s("a"), s("sh", { kind: "shell" }), s("m", { kind: "ssm" })], null))).toEqual(["a"]);
  });

  it("leaves unattended runs, and what they started, to their own views", () => {
    const list = [s("a"), s("run", { origin: "queue" }), s("kid", { origin: "session", originSession: "run" })];
    expect(names(activeScope(list, null))).toEqual(["a"]);
  });

  it("is narrowed by the working set", () => {
    const list = [s("a", { repo: "proj" }), s("b", { repo: "other" })];
    expect(names(activeScope(list, set({ repos: ["proj"] })))).toEqual(["a"]);
  });

  it("keeps a descendant the set holds when the generation between is outside the set", () => {
    const list = [
      s("a", { repo: "app" }),
      s("b", { repo: "other", alive: false, originSession: "a" }),
      s("c", { repo: "app", alive: false, originSession: "b" }),
    ];
    expect(names(activeScope(list, set({ repos: ["app"] })))).toEqual(["a", "c"]);
  });

  it("does not list the stopped child of a running session the set leaves out", () => {
    const list = [s("a", { repo: "other" }), s("c", { repo: "app", alive: false, originSession: "a" })];
    expect(names(activeScope(list, set({ repos: ["app"] })))).toEqual([]);
  });

  it("places a worktree in the set by its parent, whatever its folder is called", () => {
    const repos = [repo("app"), repo("nightly", { worktree: true, parent: "app" })];
    const list = [s("n", { repo: "nightly" })];
    expect(names(activeScope(list, set({ repos: ["app"] }), undefined, repos))).toEqual(["n"]);
    // Without the working-copy list only the folder name is left to go by.
    expect(names(activeScope(list, set({ repos: ["app"] })))).toEqual([]);
  });
});

describe("activeGroups", () => {
  const repos = [
    repo("zeta", { remote: "github.com", remotePath: "acme/zeta" }),
    repo("app", { remote: "github.com", remotePath: "acme/app" }),
    repo("app@wip-x", { remote: "github.com", remotePath: "acme/app", worktree: true, parent: "app" }),
  ];
  const spaces: Space[] = [{ folder: "s1", name: "Research", dir: "/home/dev/spaces/s1" }];

  it("orders repositories, then spaces, then other, and draws no empty group", () => {
    const list = [
      s("home"),
      s("sp", { space: "s1" }),
      s("z", { repo: "zeta" }),
      s("a", { repo: "app" }),
      s("w", { repo: "app@wip-x" }),
      s("gone", { repo: "removed" }),
    ];
    const groups = activeGroups(list, repos, spaces, null);
    expect(groups.map((g) => [g.kind, g.label])).toEqual([
      ["repo", "app"],
      ["repo", "zeta"],
      ["space", "Research"],
      ["other", ""],
    ]);
    expect(names(groups[0].sessions).sort()).toEqual(["a", "w"]);
    expect(groups[3].key).toBe(OTHER_GROUP);
    expect(names(groups[3].sessions).sort()).toEqual(["gone", "home"]);
  });

  it("files a worktree whose own row is missing under its base repository", () => {
    const list = [s("late", { repo: "app@wip-late" })];
    const groups = activeGroups(list, repos, spaces, null);
    expect(groups.map((g) => [g.kind, g.label])).toEqual([["repo", "app"]]);
    // Same heading key as the base's own sessions, so the two share one group.
    const both = activeGroups([...list, s("a", { repo: "app" })], repos, spaces, null);
    expect(both).toHaveLength(1);
    expect(names(both[0].sessions).sort()).toEqual(["a", "late"]);
  });

  it("resolves the longest known parent when names contain @", () => {
    const nested = [repo("app", { remote: "github.com", remotePath: "acme/app" }), repo("app@team", { remote: "github.com", remotePath: "acme/team", worktree: true, parent: "app" })];
    const groups = activeGroups([s("n", { repo: "app@team@wip-x" })], nested, spaces, null);
    expect(groups.map((g) => g.label)).toEqual(["team"]);
  });

  it("leaves a lookalike folder outside the repos directory in Other", () => {
    const placed = [repo("app", { path: "/home/dev/repos/app", remote: "github.com", remotePath: "acme/app" })];
    const list = [s("odd", { repo: "app@notes", dir: "/tmp/app@notes" }), s("ok", { repo: "app@wip-y", dir: "/home/dev/repos/app@wip-y" })];
    const groups = activeGroups(list, placed, spaces, null);
    expect(groups.map((g) => g.kind)).toEqual(["repo", "other"]);
    expect(names(groups[1].sessions)).toEqual(["odd"]);
  });

  it("does not move repository sessions to Other while no list has loaded", () => {
    const list = [s("late", { repo: "app@wip-late" }), s("a", { repo: "app" }), s("home")];
    const groups = activeGroups(list, [], spaces, null, new Set(), undefined, false);
    expect(groups.map((g) => [g.kind, g.label])).toEqual([["repo", "app"], ["other", ""]]);
    expect(names(groups[1].sessions)).toEqual(["home"]);
  });

  it("keeps a folder that belongs to no known repository in Other once the list is known", () => {
    const groups = activeGroups([s("x", { repo: "ghost@wip-1" }), s("y", { repo: "removed" })], repos, spaces, null);
    expect(groups.map((g) => g.kind)).toEqual(["other"]);
    expect(activeGroups([s("y", { repo: "removed" })], [], spaces, null, new Set(), undefined, true)[0].kind).toBe("other");
  });

  // #5219: a row may carry the place but no `space` (the Control Plane's mirror serves those), and
  // an older Agent still wrote the space's folder into `repo`.
  it("files a space by its stored place, never under the repository of the same name", () => {
    const named = [repo("s1", { remote: "github.com", remotePath: "acme/s1" })];
    const list = [
      s("r", { repo: "s1", place: { kind: "repo", id: "s1" } }),
      s("live", { repo: "", space: "s1", place: { kind: "space", id: "s1" } }),
      s("nospace", { repo: "s1", place: { kind: "space", id: "s1" } }),
      s("home", { repo: "", place: { kind: "home" }, dir: "/home/s1" }),
    ];
    const groups = activeGroups(list, named, spaces, null);
    expect(groups.map((g) => [g.kind, g.label, names(g.sessions).sort()])).toEqual([
      ["repo", "s1", ["r"]],
      ["space", "Research", ["live", "nospace"]],
      ["other", "", ["home"]],
    ]);
  });

  it("names a space by its folder until the list knows it", () => {
    expect(activeGroups([s("sp", { space: "s9" })], [], spaces, null)[0].label).toBe("s9");
  });

  it("folds a parent's descendants until the member opens it, and counts before the fold", () => {
    const list = [s("p", { repo: "app" }), s("c1", { repo: "app", originSession: "p" }), s("c2", { repo: "app", alive: false, originSession: "p" })];
    const folded = activeGroups(list, repos, spaces, null)[0];
    expect(names(folded.sessions)).toEqual(["p"]);
    expect(folded.fold.get("p")).toEqual({ hasChildren: true, hidden: 2 });
    expect([folded.alive, folded.total]).toEqual([2, 3]);
    const open = activeGroups(list, repos, spaces, null, new Set(["p"]))[0];
    expect(names(open.sessions)).toEqual(["p", "c1", "c2"]);
    expect(open.fold.get("p")).toEqual({ hasChildren: true, hidden: 0 });
  });

  it("an opened parent still leaves its child's own children folded", () => {
    const list = [s("p", { repo: "app" }), s("c", { repo: "app", originSession: "p" }), s("g", { repo: "app", originSession: "c" })];
    const g = activeGroups(list, repos, spaces, null, new Set(["p"]))[0];
    expect(names(g.sessions)).toEqual(["p", "c"]);
    expect(g.fold.get("c")?.hidden).toBe(1);
    expect([g.depth.get("p"), g.depth.get("c")]).toEqual([0, 1]);
  });

  it("says how many of the rows a fold hides are waiting for an answer", () => {
    const list = [
      s("p", { repo: "app" }),
      s("c1", { repo: "app", originSession: "p", state: "question" }),
      s("g1", { repo: "app", originSession: "c1", state: "permission" }),
      s("c2", { repo: "app", originSession: "p" }),
      // A stopped session's carried question is not a wait: answering it moves nothing now.
      s("c3", { repo: "app", originSession: "p", alive: false, carried: "question" }),
    ];
    const folded = activeGroups(list, repos, spaces, null)[0];
    expect(folded.hiddenWaiting.get("p")).toBe(2);
    const open = activeGroups(list, repos, spaces, null, new Set(["p"]))[0];
    expect(open.hiddenWaiting.has("p")).toBe(false);
    expect(open.hiddenWaiting.get("c1")).toBe(1);
  });

  it("a child in another repository is a root under its own heading", () => {
    const list = [s("p", { repo: "app" }), s("c", { repo: "zeta", originSession: "p" })];
    const groups = activeGroups(list, repos, spaces, null);
    expect(groups.map((g) => names(g.sessions))).toEqual([["p"], ["c"]]);
    expect(groups[0].fold.get("p")?.hasChildren).toBe(false);
    // Its parent is under another heading, so here it is a root.
    expect(groups[1].depth.get("c")).toBe(0);
  });

  describe("a corrupted originSession cycle", () => {
    const cycle = [s("a", { repo: "app", originSession: "b", state: "question" }), s("b", { repo: "app", originSession: "a" })];

    it("is cut at one member, the same one whatever the order of the list", () => {
      expect([...cycleCuts(cycle)]).toEqual(["a"]);
      expect([...cycleCuts([...cycle].reverse())]).toEqual(["a"]);
      expect(cycleCuts([s("p"), s("c", { originSession: "p" }), s("self", { originSession: "self" })]).size).toBe(0);
    });

    it("still shows a card to open the family from", () => {
      const g = activeGroups(cycle, repos, spaces, null)[0];
      expect(names(g.sessions)).toEqual(["a"]);
      expect(g.total).toBe(2);
      expect(g.fold.get("a")).toEqual({ hasChildren: true, hidden: 1 });
      // The one waiting is the card on screen, not the row it hides.
      expect(g.hiddenWaiting.has("a")).toBe(false);
      // The card handed out is the member's own session, link and all.
      expect(g.sessions[0]).toBe(cycle[0]);
    });

    it("opens into a parent and a child, and opening the child changes nothing", () => {
      for (const opened of [["a"], ["a", "b"]]) {
        const g = activeGroups(cycle, repos, spaces, null, new Set(opened))[0];
        expect(names(g.sessions)).toEqual(["a", "b"]);
        expect([g.depth.get("a"), g.depth.get("b")]).toEqual([0, 1]);
      }
      // Opening only the member that is not the root leaves the family folded.
      expect(names(activeGroups(cycle, repos, spaces, null, new Set(["b"]))[0].sessions)).toEqual(["a"]);
    });

    it("a session that names itself as its parent is a root", () => {
      const g = activeGroups([s("self", { repo: "app", originSession: "self" })], repos, spaces, null)[0];
      expect(g.depth.get("self")).toBe(0);
      expect(g.fold.get("self")?.hasChildren).toBe(false);
    });
  });

  it("puts the family that waits for an answer first inside its group", () => {
    const list = [
      s("calm", { repo: "app", createdAt: "2026-11-02T00:00:00Z" }),
      s("p", { repo: "app", createdAt: "2026-11-01T00:00:00Z" }),
      s("c", { repo: "app", originSession: "p", state: "question" }),
    ];
    expect(names(activeGroups(list, repos, spaces, null)[0].sessions)).toEqual(["p", "calm"]);
  });
});

describe("unattended groups", () => {
  const list = [
    s("a", { repo: "app" }),
    s("run", { origin: "queue" }),
    s("kid", { origin: "session", originSession: "run", alive: false }),
    s("tick", { origin: "schedule", scheduleId: "sc1" }),
    s("wt", { origin: "session", originSession: "tick", worktree: true, repo: "app@wip-x" }),
    s("old", { origin: "queue", alive: false }),
  ];
  const repos = [repo("app", { remote: "github.com", remotePath: "acme/app" }), repo("app@wip-x", { remote: "github.com", remotePath: "acme/app", worktree: true, parent: "app" })];

  it("lists running runs with every descendant, and not a run that has stopped", () => {
    expect(names(unattendedScope(list, null, undefined, repos))).toEqual(["run", "kid", "tick", "wt"]);
  });

  it("follow the three sections, one group per kind", () => {
    const g = activeGroups(list, repos, [], null);
    expect(g.map((x) => x.kind)).toEqual(["repo", "autorun", "schedule"]);
    expect(g.map((x) => x.key).slice(1)).toEqual(["u:autorun", "u:schedule"]);
    expect(g[1].total).toBe(2);
    expect(g[1].alive).toBe(1);
  });

  it("does not list a schedule's worktree child twice when its schedule shows in Sessions", () => {
    const shown = new Set(["sc1"]);
    const g = activeGroups(list, repos, [], null, new Set(), shown);
    const all = g.flatMap((x) => x.sessions.map((y) => y.name));
    expect(all.filter((n) => n === "wt")).toHaveLength(1);
    expect(g.find((x) => x.kind === "repo")!.sessions.map((y) => y.name)).toContain("wt");
  });

  it("counts their waiting sessions in the heading but not on the rail icon", () => {
    const w = [s("run", { origin: "queue", state: "question" }), s("mine", { state: "question" })];
    const g = activeGroups(w, [], [], null);
    expect(g.find((x) => x.kind === "autorun")!.waiting).toBe(1);
    expect(waitingSessions(w)).toBe(1);
  });
});
