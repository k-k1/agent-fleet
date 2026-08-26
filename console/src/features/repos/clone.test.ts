import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { RepoJob } from "./jobs.ts";

// clone.ts / jobs.ts reach the api client, which binds window.fetch, reads
// localStorage and resolves URLs against document.baseURI at module load.
const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});
vi.stubGlobal("document", { baseURI: "http://localhost/", hidden: false });
const fetchMock = vi.fn<(url: string, opts?: RequestInit) => Promise<Response>>();
vi.stubGlobal("window", { fetch: fetchMock });
vi.stubGlobal("fetch", fetchMock);

let cloneRepo: typeof import("./clone.ts")["cloneRepo"];
let svnCheckout: typeof import("./clone.ts")["svnCheckout"];
let useRepoJobsStore: typeof import("./jobs.ts")["useRepoJobsStore"];
let useReposStore: typeof import("./store.ts")["useReposStore"];

beforeAll(async () => {
  ({ cloneRepo, svnCheckout } = await import("./clone.ts"));
  ({ useRepoJobsStore } = await import("./jobs.ts"));
  ({ useReposStore } = await import("./store.ts"));
});

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

const job = (over: Partial<RepoJob> = {}): RepoJob => ({
  id: "rj1",
  kind: "svn",
  name: "docs",
  state: "running",
  startedAt: "2026-08-26T00:00:00Z",
  ...over,
});

// route dispatches the mocked fetch by URL so a test can script the poll sequence.
function route(handlers: { post?: () => Response; jobs: Array<() => Response>; repos?: () => Response }) {
  let n = 0;
  fetchMock.mockImplementation(async (url: string, opts?: RequestInit) => {
    if ((opts?.method || "GET") === "POST") return handlers.post ? handlers.post() : json({});
    if (String(url).includes("repo-jobs")) return handlers.jobs[Math.min(n++, handlers.jobs.length - 1)]();
    if (String(url).includes("api/repos")) return handlers.repos ? handlers.repos() : json({ repos: [] });
    return json({});
  });
}

describe("repo import (docs/78)", () => {
  beforeEach(() => {
    fetchMock.mockReset();
    useRepoJobsStore.setState({ jobs: [] });
    useReposStore.setState({ repos: [] });
    vi.useRealTimers();
  });

  // ★ 事故そのもの: 応答が途中で切れても、走行中の checkout を成功にしてはいけない。
  // 判定材料はジョブの state ひとつで、フォルダが増えたかどうかではない。
  it("待つのはジョブの終端であって、POST の応答ではない", async () => {
    route({
      post: () => json({ job: job() }, 202),
      // 1 回目のポーリングでは まだ running。2 回目で done。
      jobs: [() => json({ jobs: [job()] }), () => json({ jobs: [job({ state: "done" })] })],
      repos: () => json({ repos: [{ name: "docs", vcs: "svn" }] }),
    });
    const toast = vi.fn();
    await expect(svnCheckout({ url: "https://svn.example/repo" }, toast)).resolves.toEqual({ ok: true, name: "docs" });
    expect(toast).not.toHaveBeenCalled();
  });

  it("失敗したジョブは失敗として返し、svn の言い分をそのまま出す", async () => {
    route({
      post: () => json({ job: job() }, 202),
      jobs: [() => json({ jobs: [job({ state: "failed", error: "svn: E170000: URL not found" })] })],
    });
    const toast = vi.fn();
    await expect(svnCheckout({ url: "https://svn.example/repo" }, toast)).resolves.toEqual({ ok: false, name: "" });
    expect(String(toast.mock.calls[0][0])).toContain("E170000");
  });

  // 中止は利用者の意思なので、失敗トーストで叱らない（行に結末は残る）。
  it("中止はトーストを出さない", async () => {
    route({
      post: () => json({ job: job() }, 202),
      jobs: [() => json({ jobs: [job({ state: "canceled", error: "context canceled" })] })],
    });
    const toast = vi.fn();
    await expect(svnCheckout({ url: "https://svn.example/repo" }, toast)).resolves.toEqual({ ok: false, name: "" });
    expect(toast).not.toHaveBeenCalled();
  });

  it("git の clone も同じジョブ経路を通る", async () => {
    route({
      post: () => json({ job: job({ id: "rj2", kind: "git", name: "app" }) }, 202),
      jobs: [() => json({ jobs: [job({ id: "rj2", kind: "git", name: "app", state: "done" })] })],
      repos: () => json({ repos: [{ name: "app" }] }),
    });
    await expect(cloneRepo({ remote_url: "https://git.example/app.git", branch: "", name: "" }, vi.fn())).resolves.toEqual({
      ok: true,
      name: "app",
    });
  });

  it("ジョブが返ってこなければ開始できていない（成功にしない）", async () => {
    route({ post: () => json({ error: { code: "exists", message: "repo already exists: docs" } }, 409), jobs: [] });
    const toast = vi.fn();
    await expect(svnCheckout({ url: "https://svn.example/repo" }, toast)).resolves.toEqual({ ok: false, name: "" });
    expect(toast).toHaveBeenCalled();
  });
});
