// Bitbucket の保存クエリを組み立てる純ロジック（docs/80 §80.23）。ここで固定したいのは
// 「人が書けない書式を af が代わりに書く」ことなので、**出力の文字列そのもの**を見る。
import { describe, expect, it } from "vitest";
import { bbNeedsRepo, bbQuery, bbRepoNames, bbWorkspaceOf, bbWorkspaces } from "./bitbucketQuery.ts";

describe("bbQuery", () => {
  it("レビュー待ちは repo ＋ reviewers.uuid=\"@me\"（人が UUID を知らなくてよい形）", () => {
    expect(bbQuery("reviewing", "acme/web")).toBe('acme/web reviewers.uuid="@me"');
  });

  it("リポジトリの PR は対象だけ（Bitbucket の既定が state=OPEN）", () => {
    expect(bbQuery("repo_open", "acme/web")).toBe("acme/web");
  });

  it("自分の PR はワークスペース単位。リポジトリを選んでいてもワークスペースに畳む", () => {
    expect(bbQuery("authored", "acme")).toBe("acme");
    expect(bbQuery("authored", "acme/web")).toBe("acme");
  });

  it("★ リポジトリが要る意図にワークスペースだけを渡させない（Bitbucket が答えられない）", () => {
    expect(bbQuery("reviewing", "acme")).toBe("");
    expect(bbQuery("repo_open", "acme")).toBe("");
  });

  it("未選択は空＝保存させない（既定値のまま保存されて 404 になったのが発端）", () => {
    expect(bbQuery("reviewing", "")).toBe("");
    expect(bbQuery("authored", "   ")).toBe("");
  });

  it("意図ごとに要る対象の粒度", () => {
    expect(bbNeedsRepo("reviewing")).toBe(true);
    expect(bbNeedsRepo("repo_open")).toBe(true);
    expect(bbNeedsRepo("authored")).toBe(false);
  });
});

describe("bbWorkspaces", () => {
  it("重複を潰して並べる（ワークスペースが 1 つなら選ばせない判断に使う）", () => {
    expect(bbWorkspaces(["acme/web", "acme/api", "zeta/tools"])).toEqual(["acme", "zeta"]);
    expect(bbWorkspaces(["acme/web", "acme/api"])).toEqual(["acme"]);
  });

  it("空や壊れた値は落とす", () => {
    expect(bbWorkspaces(["", "/web", "acme/web"])).toEqual(["acme"]);
    expect(bbWorkspaceOf("acme/web")).toBe("acme");
  });
});

describe("bbRepoNames", () => {
  it("接続の一覧から full_name だけ取り、並べる", () => {
    const d = { repos: [{ full_name: "zeta/tools" }, { full_name: "acme/web", clone_url: "…" }] };
    expect(bbRepoNames(d)).toEqual(["acme/web", "zeta/tools"]);
  });

  it("★ 形が違う・null・error は「候補なし」に潰す（呼び手は手書きに落ちる）", () => {
    expect(bbRepoNames(null)).toEqual([]);
    expect(bbRepoNames({ error: { code: "workspace_stopped" } })).toEqual([]);
    expect(bbRepoNames({ repos: null })).toEqual([]);
    expect(bbRepoNames({ repos: [null, { full_name: 42 }, { name: "acme/web" }] })).toEqual([]);
  });

  it("workspace/repo の形でないものは候補にしない（そのまま渡すと 404 になる）", () => {
    expect(bbRepoNames({ repos: [{ full_name: "web" }, { full_name: "acme/web" }] })).toEqual(["acme/web"]);
  });
});
