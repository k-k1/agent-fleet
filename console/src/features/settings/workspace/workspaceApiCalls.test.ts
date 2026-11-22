import { beforeEach, describe, expect, it, vi } from "vitest";

// Runs the wrappers against a mocked client: the URL, method, body and argument count each one
// sends (a DELETE must not grow a body argument), and what a null body does.
const { api, apiJSON } = vi.hoisted(() => ({ api: vi.fn(), apiJSON: vi.fn() }));
vi.mock("../../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => api(...a),
  apiJSON: (...a: unknown[]) => apiJSON(...a),
}));

import * as A from "./workspaceApi.ts";

beforeEach(() => {
  api.mockReset().mockResolvedValue({ ok: 1 });
  apiJSON.mockReset().mockResolvedValue({ ok: 1 });
});

const del = { method: "DELETE" };
const reads: [string, () => Promise<unknown>, unknown[]][] = [
  ["getWorkspaceMachine", () => A.getWorkspaceMachine(), ["api/workspace/machine"]],
  ["getCleanupUsage", () => A.getCleanupUsage(), ["api/cleanup/usage"]],
  ["getCleanupLeftovers", () => A.getCleanupLeftovers(), ["api/cleanup/leftovers"]],
  ["deleteCleanupLeftover", () => A.deleteCleanupLeftover("a/b", false), ["api/cleanup/leftovers/a%2Fb", del]],
  ["deleteCleanupLeftover confirm", () => A.deleteCleanupLeftover("k", true), ["api/cleanup/leftovers/k?confirm=1", del]],
  ["getToolCaches", () => A.getToolCaches(), ["api/cleanup/tool-caches"]],
  ["deleteToolCache", () => A.deleteToolCache("g/1"), ["api/cleanup/tool-caches/g%2F1", del]],
  ["getAfWorkOlderThan", () => A.getAfWorkOlderThan(30), ["api/cleanup/af-work?older_than_days=30"]],
  ["getHomeUsage read", () => A.getHomeUsage(), ["api/cleanup/home", undefined]],
  ["getHomeUsage measure", () => A.getHomeUsage({ method: "POST" }), ["api/cleanup/home/measure", { method: "POST" }]],
  ["getWorkspaceSizing", () => A.getWorkspaceSizing(), ["api/workspace/sizing"]],
  ["cancelBoost", () => A.cancelBoost(), ["api/workspace/boost", del]],
  ["getDatabases", () => A.getDatabases(), ["api/env/databases"]],
  ["listInternalRepos", () => A.listInternalRepos(), ["api/internal-git/repos"]],
  ["deleteInternalRepo", () => A.deleteInternalRepo("r/1"), ["api/internal-git/repos/r%2F1", del]],
  ["getRepoBranches", () => A.getRepoBranches("r/1"), ["api/internal-git/repos/r%2F1/branches"]],
  ["getRepoTree", () => A.getRepoTree("r/1", "m&x", "a b"), ["api/internal-git/repos/r%2F1/tree?ref=m%26x&path=a%20b"]],
  ["getRepoBlob", () => A.getRepoBlob("r/1", "main", "a/b"), ["api/internal-git/repos/r%2F1/blob?ref=main&path=a%2Fb"]],
  ["getToolchainLatest", () => A.getToolchainLatest("jdk-latest", "2/1"), ["api/env/jdk-latest?major=2%2F1"]],
  ["getJdkInstall", () => A.getJdkInstall(), ["api/env/jdk-install"]],
  ["getNodeInstall", () => A.getNodeInstall(), ["api/env/node-install"]],
  ["getToolchainInstalls", () => A.getToolchainInstalls(), ["api/env/toolchain-installs"]],
  ["getGcpLoginStates", () => A.getGcpLoginStates(), ["api/gcp-login/profiles"]],
  ["listGcpProfileRows", () => A.listGcpProfileRows(), ["api/gcp/profiles"]],
  ["listExistingGcpProfiles", () => A.listExistingGcpProfiles(), ["api/gcp/profiles"]],
];
const writes: [string, () => Promise<unknown>, unknown[]][] = [
  ["deleteAfWorkFolders", () => A.deleteAfWorkFolders(7, ["x"]), ["api/cleanup/af-work", "POST", { older_than_days: 7, names: ["x"] }]],
  ["pressBoost", () => A.pressBoost(4), ["api/workspace/boost", "POST", { hours: 4 }]],
  ["postDatabaseAction", () => A.postDatabaseAction("postgres", "stop"), ["api/env/databases/postgres/stop", "POST", {}]],
  ["postDatabaseAction query", () => A.postDatabaseAction("mysql", "drop", "db=a"), ["api/env/databases/mysql/drop?db=a", "POST", {}]],
  [
    "createShellSession",
    () => A.createShellSession("af-db connect postgres --db=d", "psql d"),
    ["api/sessions", "POST", { kind: "shell", initial_prompt: "af-db connect postgres --db=d", title: "psql d" }],
  ],
  ["createInternalRepo", () => A.createInternalRepo("n"), ["api/internal-git/repos", "POST", { name: "n" }]],
  ["renameInternalRepo", () => A.renameInternalRepo("o/1", "n"), ["api/internal-git/repos/o%2F1/rename", "POST", { new_name: "n" }]],
  ["applyHostUpdate", () => A.applyHostUpdate(), ["api/update/apply", "POST", {}]],
  ["startJdkInstall", () => A.startJdkInstall("21"), ["api/env/jdk-install", "POST", { major: "21" }]],
  ["startNodeInstall", () => A.startNodeInstall("22"), ["api/env/node-install", "POST", { major: "22" }]],
  ["deleteToolchain jdk", () => A.deleteToolchain("jdk", "21"), ["api/env/jdk/21", "DELETE"]],
  ["deleteToolchain node", () => A.deleteToolchain("node", "2/2"), ["api/env/node/2%2F2", "DELETE"]],
];

describe("workspace wrappers send what the components used to", () => {
  it.each(reads)("%s", async (_n, call, args) => {
    await call();
    expect(apiJSON).not.toHaveBeenCalled();
    expect(api).toHaveBeenCalledTimes(1);
    expect(api.mock.calls[0]).toEqual(args);
    expect(api.mock.calls[0].length).toBe(args.length);
  });

  it.each(writes)("%s", async (_n, call, args) => {
    await call();
    expect(api).not.toHaveBeenCalled();
    expect(apiJSON).toHaveBeenCalledTimes(1);
    expect(apiJSON.mock.calls[0]).toEqual(args);
    expect(apiJSON.mock.calls[0].length).toBe(args.length);
  });

  it("keeps a null body as null and lets assertBody throw where the caller dereferenced", async () => {
    api.mockResolvedValue(null);
    apiJSON.mockResolvedValue(null);
    expect(await A.getRepoTree("r", "m", "")).toBeNull();
    expect(await A.createInternalRepo("n")).toBeNull();
    expect(await A.createShellSession("p", "t")).toBeNull();
    expect(() => A.assertBody(null)).toThrow();
    expect(() => A.assertBody({})).not.toThrow();
  });

  it("hands an error body through unchanged", async () => {
    apiJSON.mockResolvedValue({ error: { code: "x" } });
    expect(await A.pressBoost(1)).toEqual({ error: { code: "x" } });
  });
});
