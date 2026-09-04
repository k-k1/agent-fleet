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

describe("repo import (docs/log/78)", () => {
  beforeEach(() => {
    fetchMock.mockReset();
    useRepoJobsStore.setState({ jobs: [] });
    useReposStore.setState({ repos: [] });
    vi.useRealTimers();
  });

  // The failure itself: a truncated response must never turn a still-running checkout into
  // a success. The only evidence is the job's state, not whether a folder appeared.
  it("waits for the job to finish, not for the POST response", async () => {
    route({
      post: () => json({ job: job() }, 202),
      // still running on the first poll, done on the second.
      jobs: [() => json({ jobs: [job()] }), () => json({ jobs: [job({ state: "done" })] })],
      repos: () => json({ repos: [{ name: "docs", vcs: "svn" }] }),
    });
    const toast = vi.fn();
    await expect(svnCheckout({ url: "https://svn.example/repo" }, toast)).resolves.toEqual({ ok: true, name: "docs" });
    expect(toast).not.toHaveBeenCalled();
  });

  it("returns a failed job as a failure and surfaces svn's own message", async () => {
    route({
      post: () => json({ job: job() }, 202),
      jobs: [() => json({ jobs: [job({ state: "failed", error: "svn: E170000: URL not found" })] })],
    });
    const toast = vi.fn();
    await expect(svnCheckout({ url: "https://svn.example/repo" }, toast)).resolves.toEqual({ ok: false, name: "" });
    expect(String(toast.mock.calls[0][0])).toContain("E170000");
  });

  // A cancel is the user's own decision, so it is not scolded with a failure toast (the
  // row still carries the outcome).
  it("shows no toast for a cancel", async () => {
    route({
      post: () => json({ job: job() }, 202),
      jobs: [() => json({ jobs: [job({ state: "canceled", error: "context canceled" })] })],
    });
    const toast = vi.fn();
    await expect(svnCheckout({ url: "https://svn.example/repo" }, toast)).resolves.toEqual({ ok: false, name: "" });
    expect(toast).not.toHaveBeenCalled();
  });

  it("routes a git clone through the same job path", async () => {
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

  // A new folder (no import source) is mkdir + git init only and touches no network.
  // Putting it on the job path would mean waiting through a list poll for the outcome of
  // work that has already finished; staying off it is faster and waits on the right thing.
  it("creates a new folder without a job, taking the response as the outcome", async () => {
    let jobPolls = 0;
    fetchMock.mockImplementation(async (url: string, opts?: RequestInit) => {
      if ((opts?.method || "GET") === "POST") {
        expect(String(url)).toContain("api/repos/init");
        return json({ repo: { name: "new-project", branch: "main", unborn: true } }, 201);
      }
      if (String(url).includes("repo-jobs")) {
        jobPolls++;
        return json({ jobs: [] });
      }
      return json({ repos: [{ name: "new-project", branch: "main", unborn: true }] });
    });
    const { initRepo } = await import("./clone.ts");
    const toast = vi.fn();
    await expect(initRepo("new-project", toast)).resolves.toEqual({ ok: true, name: "new-project" });
    expect(jobPolls).toBe(0);
    expect(toast).not.toHaveBeenCalled();
    // The list must have been re-fetched: that is what puts the new folder in the left pane.
    expect(useReposStore.getState().repos.map((r) => r.name)).toEqual(["new-project"]);
  });

  it("is refused with 409 for an existing name, and is not reported as success", async () => {
    fetchMock.mockImplementation(async () => json({ error: { code: "exists", message: "repo already exists: docs" } }, 409));
    const { initRepo } = await import("./clone.ts");
    const toast = vi.fn();
    await expect(initRepo("docs", toast)).resolves.toEqual({ ok: false, name: "" });
    expect(toast).toHaveBeenCalled();
  });

  it("treats a missing job as never started (not a success)", async () => {
    route({ post: () => json({ error: { code: "exists", message: "repo already exists: docs" } }, 409), jobs: [] });
    const toast = vi.fn();
    await expect(svnCheckout({ url: "https://svn.example/repo" }, toast)).resolves.toEqual({ ok: false, name: "" });
    expect(toast).toHaveBeenCalled();
  });
});
