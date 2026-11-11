import { afterEach, describe, expect, it, vi } from "vitest";
import { applyWorktreeOutput, loadSurvey, sortReports, wtoGrade, type WtoReport } from "./worktreeOutput.ts";

const rep = (over: Partial<WtoReport>): WtoReport => ({ name: "a", path: "/p", status: "candidate", bytes: 1, ...over });

const answer = (status: number, body: unknown) =>
  vi.fn(async () => ({
    ok: status < 400,
    status,
    statusText: "",
    headers: { get: () => null },
    text: async () => (typeof body === "string" ? body : JSON.stringify(body)),
    json: async () => (typeof body === "string" ? JSON.parse(body) : body),
  }));

afterEach(() => vi.unstubAllGlobals());

describe("wtoGrade", () => {
  it("grades only a candidate regenerable — never safe, which is the trash-backed grade", () => {
    expect(wtoGrade(rep({ status: "candidate" }))).toBe("regenerable");
    expect(wtoGrade(rep({ status: "empty" }))).toBe("keep");
    expect(wtoGrade(rep({ status: "refused", reason: "dirty" }))).toBe("keep");
  });
});

describe("sortReports", () => {
  it("puts candidates first, largest first, then the rest", () => {
    const out = sortReports([
      rep({ name: "r", status: "refused" }),
      rep({ name: "small", bytes: 1 }),
      rep({ name: "e", status: "empty", bytes: 0 }),
      rep({ name: "big", bytes: 9 }),
    ]);
    expect(out.map((r) => r.name)).toEqual(["big", "small", "e", "r"]);
  });
});

describe("applyWorktreeOutput", () => {
  it("sends the name in the query, with dry_run=1 only for a preview", async () => {
    const f = answer(200, rep({}));
    vi.stubGlobal("fetch", f);
    await applyWorktreeOutput("web@x y", true);
    await applyWorktreeOutput("web@x y", false);
    const urls = f.mock.calls.map((c) => String((c as unknown[])[0]));
    expect(urls[0]).toMatch(/api\/cleanup\/worktree-output\?worktree=web%40x%20y&dry_run=1$/);
    expect(urls[1]).toMatch(/api\/cleanup\/worktree-output\?worktree=web%40x%20y$/);
    expect(f.mock.calls.every((c) => (c as unknown[][])[1] && ((c as unknown[][])[1] as RequestInit).method === "POST")).toBe(true);
  });

  it("returns a 409 refusal as a report, not an error", async () => {
    vi.stubGlobal("fetch", answer(409, rep({ status: "refused", reason: "live_session", detail: "s1" })));
    const res = await applyWorktreeOutput("a", true);
    expect(res).toMatchObject({ kind: "report", report: { status: "refused", reason: "live_session" } });
  });

  it("treats an answer without a report as an error", async () => {
    vi.stubGlobal("fetch", answer(400, { error: { code: "bad_name", message: "invalid working copy name" } }));
    expect((await applyWorktreeOutput("a", true)).kind).toBe("error");
    vi.stubGlobal("fetch", answer(502, "bad gateway"));
    expect((await applyWorktreeOutput("a", false)).kind).toBe("error");
    vi.stubGlobal("fetch", vi.fn(async () => Promise.reject(new Error("net"))));
    expect((await applyWorktreeOutput("a", false)).kind).toBe("error");
  });
});

describe("loadSurvey", () => {
  it("reads the survey, and tells an Agent without the endpoint from a failure", async () => {
    vi.stubGlobal("fetch", answer(200, { dormant_days: 30, worktrees: [rep({})], measured_at: "t" }));
    expect((await loadSurvey()).kind).toBe("ok");
    vi.stubGlobal("fetch", answer(404, "404 page not found"));
    expect((await loadSurvey()).kind).toBe("unsupported");
    vi.stubGlobal("fetch", answer(502, "bad gateway"));
    expect((await loadSurvey()).kind).toBe("error");
  });
});
