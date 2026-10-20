import { readFileSync, existsSync } from "node:fs";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const apiMock = vi.fn();
let tenant = "";
let expired = false; // the real latch cannot be reset, so stand it in
vi.mock("../core/auth/authExpired.ts", () => ({ isAuthExpired: () => expired }));
vi.mock("../core/api/client.ts", () => ({ api: (...a: unknown[]) => apiMock(...a), getTenant: () => tenant }));

const { AGENTS, agentOf, availableKinds, repoLaunchKinds } = await import("./registry.ts");
const { FALLBACK_KIND_FACTS, catalogKinds, kindFacts, parseKindCatalog, setKindCatalog } = await import("./kindCatalog.ts");
const { loadKindCatalog, loadKindCatalogAfter, resetKindCatalog, syncKindCatalogTenant } = await import("./kindCatalogLoad.ts");
const { MCP_KINDS, mcpKinds } = await import("../features/settings/mcp/mcpWire.ts");
const { SESSION_KINDS } = await import("../types/session.ts");

// The build-time snapshot of GET /agents/kinds, embedded by the Control Plane and held equal to the
// Agent's registry by TestKindCatalogSnapshot. A checkout without it skips rather than fails, like
// the Go guard this one replaces for the Console side.
const SNAPSHOT = new URL("../../../control-plane/internal/agentkinds/catalog.json", import.meta.url);
const haveSnapshot = existsSync(SNAPSHOT);
const snapshot: { kinds: { kind: string; managedOnly: boolean; managedDefault: boolean; mcpClient: string }[] } = haveSnapshot
  ? JSON.parse(readFileSync(SNAPSHOT, "utf8"))
  : { kinds: [] };

describe.skipIf(!haveSnapshot)("static kind lists against the catalog snapshot", () => {
  it("SESSION_KINDS names exactly the kinds the Agent registers", () => {
    expect([...SESSION_KINDS].sort()).toEqual(snapshot.kinds.map((k) => k.kind).sort());
  });

  it("FALLBACK_KIND_FACTS equals the snapshot for every kind", () => {
    for (const e of snapshot.kinds) {
      expect(FALLBACK_KIND_FACTS[e.kind as keyof typeof FALLBACK_KIND_FACTS], e.kind).toEqual({
        managedOnly: e.managedOnly,
        managedDefault: e.managedDefault,
        mcpClient: e.mcpClient,
      });
    }
  });

  it("MCP_KINDS lists exactly the kinds whose mcpClient is yes", () => {
    expect([...MCP_KINDS].sort()).toEqual(
      snapshot.kinds
        .filter((k) => k.mcpClient === "yes")
        .map((k) => k.kind)
        .sort(),
    );
  });
});

beforeEach(() => {
  tenant = "";
  expired = false;
  apiMock.mockReset();
  resetKindCatalog();
});
afterEach(() => {
  resetKindCatalog();
  vi.useRealTimers();
});

const body = (kinds: unknown[]) => ({ kinds });
const row = (kind: string, o: Record<string, unknown> = {}) => ({ kind, managedOnly: false, managedDefault: false, mcpClient: "yes", ...o });

describe("descriptors without a catalog (fallback)", () => {
  it("answer the driver facts the hand-kept fields used to", () => {
    const managed = SESSION_KINDS.filter((k) => AGENTS[k].managedDriver);
    expect(managed).toEqual(["codex", "cursor", "copilot", "kiro", "opencode", "lcpp", "muse"]);
    expect(SESSION_KINDS.filter((k) => AGENTS[k].terminalDriver === false)).toEqual(["lcpp", "muse"]);
    expect(AGENTS.claude.terminalDriver).toBe(true);
  });

  it("keep the property enumerable, so a spread copies the answer", () => {
    expect({ ...AGENTS.codex }.managedDriver).toBe(true);
  });
});

describe("a loaded catalog", () => {
  it("overrides the fallback per kind, and falls back for a kind it omits", () => {
    setKindCatalog(parseKindCatalog(body([row("claude", { managedDefault: true })])));
    expect(AGENTS.claude.managedDriver).toBe(true);
    expect(AGENTS.codex.managedDriver).toBe(true); // omitted: the fallback answers
    expect(AGENTS.lcpp.terminalDriver).toBe(false);
  });

  it("treats managedOnly as managed-default even if the entry forgets the flag", () => {
    setKindCatalog(parseKindCatalog(body([row("claude", { managedOnly: true })])));
    expect(AGENTS.claude.managedDriver).toBe(true);
    expect(AGENTS.claude.terminalDriver).toBe(false);
  });

  it("drops malformed entries and treats an empty answer as no catalog", () => {
    expect(parseKindCatalog(null)).toBeNull();
    expect(parseKindCatalog({})).toBeNull();
    expect(parseKindCatalog(body([]))).toBeNull();
    expect(parseKindCatalog(body([null, { kind: 3 }, { kind: "" }]))).toBeNull();
    expect(parseKindCatalog(body([row("x"), null]))?.get("x")).toEqual({ managedOnly: false, managedDefault: false, mcpClient: "yes" });
  });
});

describe("a malformed row", () => {
  it.each([
    ["no flags", { kind: "muse" }],
    ["managedOnly missing", { kind: "muse", managedDefault: false, mcpClient: "yes" }],
    ["managedDefault a string", { kind: "muse", managedOnly: false, managedDefault: "no", mcpClient: "yes" }],
    ["mcpClient unknown word", { kind: "muse", managedOnly: false, managedDefault: false, mcpClient: "maybe" }],
  ])("is dropped, so the kind keeps its fallback facts (%s)", (_n, bad) => {
    expect(parseKindCatalog(body([bad]))).toBeNull(); // nothing usable: counts as no catalog
    setKindCatalog(parseKindCatalog(body([bad, row("claude")])));
    expect(AGENTS.muse.managedDriver).toBe(true);
    expect(AGENTS.muse.terminalDriver).toBe(false);
  });
});

describe("a kind the Console has no presentation for", () => {
  const entry = row("newcli", { managedDefault: true });

  it("renders generically, claims no capability, and is not launchable", () => {
    setKindCatalog(parseKindCatalog(body([entry])));
    const a = agentOf("newcli");
    expect(a.label).toBe("newcli");
    expect(a.icon).toBe("symbol-misc");
    expect(a.managedDriver).toBe(true);
    const { chat, headlessChat, scheduledRuns, launchableFromRepo, planMode, forkAt } = a.caps;
    expect([chat, headlessChat, scheduledRuns, launchableFromRepo, planMode, forkAt]).toEqual([false, false, false, false, false, false]);
    expect(a.available({})).toBe(false);
    expect(agentOf("newcli")).toBe(a); // stable identity between renders
    expect(repoLaunchKinds).not.toContain("newcli");
    expect(Object.keys(availableKinds({}))).not.toContain("newcli");
  });

  it("is claude before a catalog names it, exactly as an unknown kind was", () => {
    expect(agentOf("newcli")).toBe(AGENTS.claude);
  });

  it("is dropped again with the catalog", () => {
    setKindCatalog(parseKindCatalog(body([entry])));
    setKindCatalog(null);
    expect(agentOf("newcli")).toBe(AGENTS.claude);
  });
});

describe("alternate spellings behave as before", () => {
  const spellings = ["Claude", " claude", "claude ", "claude-code", "kiro-cli", "cursor-agent", "llamacpp", "antigravity", "CODEX", ""];

  it.each(spellings)("%j resolves to claude and has no facts", (s) => {
    for (const cat of [false, true]) {
      if (cat) setKindCatalog(parseKindCatalog(body(snapshot.kinds.length ? snapshot.kinds : [row("claude")])));
      expect(agentOf(s)).toBe(AGENTS.claude);
      expect(kindFacts(s)).toBeUndefined();
      expect(mcpKinds()).not.toContain(s);
    }
  });
});

describe("mcpKinds", () => {
  it("is MCP_KINDS in order without a catalog", () => {
    expect(mcpKinds()).toEqual([...MCP_KINDS]);
  });

  it("follows the catalog: no and unverified drop out, an unknown yes kind is appended", () => {
    setKindCatalog(
      parseKindCatalog(
        body([
          row("codex", { mcpClient: "no" }),
          row("kiro", { mcpClient: "unverified" }),
          row("zeta"),
          row("alpha"),
        ]),
      ),
    );
    // The tri-state survives parsing: unverified is not folded into no (or yes).
    expect(kindFacts("kiro")?.mcpClient).toBe("unverified");
    expect(kindFacts("codex")?.mcpClient).toBe("no");
    expect(kindFacts("zeta")?.mcpClient).toBe("yes");
    const got = mcpKinds();
    expect(got).not.toContain("codex");
    expect(got).not.toContain("kiro");
    expect(got.slice(-2)).toEqual(["zeta", "alpha"]);
    expect(got.slice(0, 3)).toEqual(["claude", "opencode", "cursor"]);
    expect(catalogKinds()).toEqual(["codex", "kiro", "zeta", "alpha"]);
  });
});

describe("loadKindCatalog", () => {
  it("installs a good answer once and does not ask again", async () => {
    apiMock.mockResolvedValue(body([row("claude", { managedDefault: true })]));
    await loadKindCatalog();
    await loadKindCatalog();
    expect(apiMock).toHaveBeenCalledTimes(1);
    // No leading slash: a Console served under a sub-path (rel() resolves against document.baseURI)
    // must not be sent to the host root.
    expect(apiMock).toHaveBeenCalledWith("api/agents/kinds");
    expect(new URL("api/agents/kinds", "https://example.invalid/agent-fleet/").pathname).toBe("/agent-fleet/api/agents/kinds");
    expect(AGENTS.claude.managedDriver).toBe(true);
  });

  it("keeps the fallback on a failure and retries later with a growing delay", async () => {
    vi.useFakeTimers();
    apiMock.mockRejectedValueOnce(new Error("502")).mockResolvedValueOnce({ nope: 1 }).mockResolvedValue(
      body([row("claude", { managedDefault: true })]),
    );
    await loadKindCatalog();
    expect(AGENTS.claude.managedDriver).toBe(false);
    expect(AGENTS.codex.managedDriver).toBe(true);
    await vi.advanceTimersByTimeAsync(2_999);
    expect(apiMock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(2); // 3 s: the unusable answer
    expect(apiMock).toHaveBeenCalledTimes(2);
    expect(AGENTS.claude.managedDriver).toBe(false);
    await vi.advanceTimersByTimeAsync(9_990);
    expect(apiMock).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(20); // +10 s
    expect(apiMock).toHaveBeenCalledTimes(3);
    expect(AGENTS.claude.managedDriver).toBe(true);
    await vi.advanceTimersByTimeAsync(120_000);
    expect(apiMock).toHaveBeenCalledTimes(3); // success stops the retries
  });

  it.each([
    ["401", { error: { code: "unauthorized", status: 401 } }],
    ["403", { error: { code: "forbidden", status: 403 } }],
  ])("stops retrying on a terminal %s", async (_n, answer) => {
    vi.useFakeTimers();
    apiMock.mockResolvedValue(answer);
    await loadKindCatalog();
    await vi.advanceTimersByTimeAsync(300_000);
    expect(apiMock).toHaveBeenCalledTimes(1);
    expect(AGENTS.codex.managedDriver).toBe(true); // still on the fallback
  });

  it("stops retrying once the login-expired latch has fired", async () => {
    vi.useFakeTimers();
    apiMock.mockResolvedValue({ error: { code: "http_502", status: 502 } });
    await loadKindCatalog();
    expired = true;
    await vi.advanceTimersByTimeAsync(300_000);
    expect(apiMock).toHaveBeenCalledTimes(1);
  });

  it.each([
    ["5xx", { error: { code: "internal", status: 502 } }],
    ["429", { error: { code: "rate", status: 429 } }],
    ["an empty catalog", { kinds: [] }],
  ])("keeps retrying on %s", async (_n, answer) => {
    vi.useFakeTimers();
    apiMock.mockResolvedValueOnce(answer).mockResolvedValue(body([row("claude", { managedDefault: true })]));
    await loadKindCatalog();
    await vi.advanceTimersByTimeAsync(3_100);
    expect(apiMock).toHaveBeenCalledTimes(2);
    expect(AGENTS.claude.managedDriver).toBe(true);
  });

  it("does not keep a catalog fetched for tenant A once boot settled on tenant B", async () => {
    tenant = "a";
    let resolveA!: (v: unknown) => void;
    apiMock.mockReturnValueOnce(new Promise((r) => (resolveA = r)));
    apiMock.mockResolvedValueOnce(body([row("claude", { managedDefault: false }), row("codex", { managedDefault: false })]));
    const p = loadKindCatalog();
    tenant = "b"; // tenant.init() picked b (?tenant=b) with no reset
    resolveA(body([row("claude", { managedDefault: true })]));
    await p;
    await vi.waitFor(() => expect(apiMock).toHaveBeenCalledTimes(2));
    await vi.waitFor(() => expect(AGENTS.codex.managedDriver).toBe(false)); // B's answer, not A's
    expect(AGENTS.claude.managedDriver).toBe(false);
  });

  it("drops an answer that was in flight when the tenant changed", async () => {
    let resolve!: (v: unknown) => void;
    apiMock.mockReturnValueOnce(new Promise((r) => (resolve = r)));
    const p = loadKindCatalog();
    resetKindCatalog();
    resolve(body([row("claude", { managedDefault: true })]));
    await p;
    expect(AGENTS.claude.managedDriver).toBe(false);
  });

  it("does not start a load for an effect that was cleaned up while the tenant was settling", async () => {
    apiMock.mockResolvedValue(body([row("claude")]));
    let alive = true;
    let settle!: () => void;
    const init = new Promise<void>((r) => (settle = r));
    const p = loadKindCatalogAfter(init, () => alive);
    alive = false; // cleanup / StrictMode's first effect
    resetKindCatalog();
    settle();
    await p;
    await Promise.resolve();
    expect(apiMock).not.toHaveBeenCalled();
  });

  it("starts the load once the tenant settles while the effect is live", async () => {
    apiMock.mockResolvedValue(body([row("claude", { managedDefault: true })]));
    await loadKindCatalogAfter(Promise.resolve(), () => true);
    await vi.waitFor(() => expect(AGENTS.claude.managedDriver).toBe(true));
  });

  it("replaces a catalog that succeeded for tenant A once the tenant is B (late change during boot)", async () => {
    tenant = "a";
    apiMock.mockResolvedValueOnce(body([row("claude", { managedDefault: true })]));
    await loadKindCatalog(); // A's answer lands before anything notices the tenant moved
    expect(AGENTS.claude.managedDriver).toBe(true);
    tenant = "b";
    apiMock.mockResolvedValueOnce(body([row("claude", { managedDefault: false })]));
    syncKindCatalogTenant();
    await vi.waitFor(() => expect(apiMock).toHaveBeenCalledTimes(2));
    await vi.waitFor(() => expect(AGENTS.claude.managedDriver).toBe(false));
  });

  it("drops the retry backoff of tenant A when the tenant becomes B, and B starts again at 3 s", async () => {
    vi.useFakeTimers();
    tenant = "a";
    apiMock.mockResolvedValue({ error: { code: "http_502", status: 502 } });
    await loadKindCatalog(); // A fails: a 3 s retry is pending
    expect(apiMock).toHaveBeenCalledTimes(1);
    tenant = "b";
    syncKindCatalogTenant();
    await vi.advanceTimersByTimeAsync(0);
    expect(apiMock).toHaveBeenCalledTimes(2); // B asked at once; A's timer was cancelled
    await vi.advanceTimersByTimeAsync(2_900);
    expect(apiMock).toHaveBeenCalledTimes(2); // A's old 3 s timer does not fire
    await vi.advanceTimersByTimeAsync(200); // B's own first backoff is 3 s
    expect(apiMock).toHaveBeenCalledTimes(3);
    resetKindCatalog();
    await vi.advanceTimersByTimeAsync(120_000);
    expect(apiMock).toHaveBeenCalledTimes(3); // nothing survives the reset
  });

  it("does not start a second retry chain when load is called again during a backoff", async () => {
    vi.useFakeTimers();
    apiMock.mockResolvedValue({ error: { code: "http_502", status: 502 } });
    await loadKindCatalog(); // fails: 3 s backoff pending
    syncKindCatalogTenant(); // e.g. booted flips while waiting
    await loadKindCatalog();
    await vi.advanceTimersByTimeAsync(2_900);
    expect(apiMock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(200);
    expect(apiMock).toHaveBeenCalledTimes(2);
    resetKindCatalog();
    await vi.advanceTimersByTimeAsync(120_000);
    expect(apiMock).toHaveBeenCalledTimes(2); // one chain, so one reset ends it
  });

  it("keeps the catalog when the tenant has not changed", async () => {
    tenant = "a";
    apiMock.mockResolvedValue(body([row("claude", { managedDefault: true })]));
    await loadKindCatalog();
    syncKindCatalogTenant();
    expect(apiMock).toHaveBeenCalledTimes(1);
    expect(AGENTS.claude.managedDriver).toBe(true);
  });

  it("reset forgets the catalog and cancels the retry", async () => {
    vi.useFakeTimers();
    apiMock.mockRejectedValue(new Error("down"));
    await loadKindCatalog();
    resetKindCatalog();
    await vi.advanceTimersByTimeAsync(120_000);
    expect(apiMock).toHaveBeenCalledTimes(1);
  });
});
