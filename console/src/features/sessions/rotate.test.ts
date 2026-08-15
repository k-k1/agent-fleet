import { describe, expect, it } from "vitest";
import { rotatableSessions, rotateTarget } from "./rotate.ts";
import type { WorkingSet } from "../../lib/workingSets.ts";
import type { Session } from "../../types/session.ts";

const s = (name: string, extra: Partial<Session> = {}): Session => ({
  name,
  kind: "claude",
  alive: true,
  ...extra,
});

const set = (over: Partial<WorkingSet> = {}): WorkingSet => ({
  id: "wabcdef",
  name: "g",
  repos: [],
  convs: [],
  sessions: [],
  schedules: [],
  ...over,
});

describe("rotatableSessions", () => {
  it("稼働中だけを、一覧の順序のまま返す", () => {
    const list = [s("s1"), s("s2", { alive: false }), s("s3", { alive: undefined }), s("s4")];
    expect(rotatableSessions(list, null).map((x) => x.name)).toEqual(["s1", "s4"]);
  });

  it("作業グループが選ばれていれば、その絞り込みに従う", () => {
    const list = [
      s("s1", { dir: "/home/dev/repos/alpha" }),
      s("s2", { dir: "/home/dev/repos/beta" }),
      s("s3"), // repo なし: 直接指名されたときだけ入る
    ];
    const w = set({ repos: ["alpha"], sessions: ["s3"] });
    expect(rotatableSessions(list, w).map((x) => x.name)).toEqual(["s1", "s3"]);
  });

  it("ワークツリーは親クローンの所属を継ぐ", () => {
    const list = [s("s1", { dir: "/home/dev/repos/alpha@wip-x1" })];
    expect(rotatableSessions(list, set({ repos: ["alpha"] })).map((x) => x.name)).toEqual(["s1"]);
  });
});

describe("rotateTarget", () => {
  const list = [s("s1"), s("s2"), s("s3")];

  it("次へ送る（末尾は先頭へ巻き戻る）", () => {
    expect(rotateTarget(list, "s1", 1)?.session.name).toBe("s2");
    expect(rotateTarget(list, "s3", 1)?.session.name).toBe("s1");
  });

  it("戻す方向も同じ規則", () => {
    expect(rotateTarget(list, "s1", -1)?.session.name).toBe("s3");
    expect(rotateTarget(list, "s3", -1)?.session.name).toBe("s2");
  });

  it("位置は 0 始まりの index と総数を返す（トーストの n/total 用）", () => {
    expect(rotateTarget(list, "s1", 1)).toMatchObject({ index: 1, total: 3 });
  });

  it("現在地が対象外なら、前進は先頭・後退は末尾から始める", () => {
    // 停止済み／別グループ／そもそもセッションでないペイン（null）
    expect(rotateTarget(list, null, 1)?.session.name).toBe("s1");
    expect(rotateTarget(list, "gone", 1)?.session.name).toBe("s1");
    expect(rotateTarget(list, null, -1)?.session.name).toBe("s3");
  });

  it("対象が無ければ null", () => {
    expect(rotateTarget([], "s1", 1)).toBeNull();
  });

  it("対象が自分 1 件だけなら null（何もしない）", () => {
    expect(rotateTarget([s("s1")], "s1", 1)).toBeNull();
    // 1 件でも、いま見ているのが別物なら移動先になる
    expect(rotateTarget([s("s1")], "other", 1)?.session.name).toBe("s1");
  });

  it("件数より大きい delta でも負の添字にならない", () => {
    expect(rotateTarget(list, "s1", -7)?.session.name).toBe("s3");
    expect(rotateTarget(list, "s1", 7)?.session.name).toBe("s2");
  });
});
