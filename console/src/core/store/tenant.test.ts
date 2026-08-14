// tenant ストアの whoami 再取得の契約テスト。whoami はブート 1 回きりの
// スナップショットで、そこにデプロイ capability（scheduler_enabled）も乗って
// いるため、CP を設定変更つきで再起動してもリロードするまで古い値のままだった。
// push 再接続で読み直す一方、①短時間の再接続連打では叩かない ②エラー応答や
// 通信断で解決済みアイデンティティを消さない、を固定する。
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

// store は api client（window.fetch 束縛・document.baseURI）を import するので
// 先にグローバルを stub してから import（workspace.test.ts と同じ流儀）。
const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});
vi.stubGlobal("document", { baseURI: "http://localhost/", hidden: false });
const fetchMock = vi.fn<() => Promise<Response>>();
vi.stubGlobal("window", { fetch: fetchMock });
vi.stubGlobal("fetch", fetchMock);

let useTenantStore: (typeof import("./tenant.ts"))["useTenantStore"];
let setTenant: (typeof import("../api/client.ts"))["setTenant"];
beforeAll(async () => {
  ({ useTenantStore } = await import("./tenant.ts"));
  ({ setTenant } = await import("../api/client.ts"));
});

const jsonRes = (body: unknown) =>
  new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });

// 間引きは Date.now 基準（モジュール内 state）— テストから時計を進める。
let now = 1_700_000_000_000;
const advance = (ms: number) => (now += ms);
vi.spyOn(Date, "now").mockImplementation(() => now);

describe("tenant store refreshWhoami", () => {
  beforeEach(() => {
    fetchMock.mockReset();
    useTenantStore.setState({ whoami: { user: "u1", scheduler_enabled: false } });
    advance(60_000); // 前テストの取得から十分に離す
  });

  it("adopts the re-read deployment flags (a CP restart flips them)", async () => {
    fetchMock.mockResolvedValue(jsonRes({ user: "u1", scheduler_enabled: true }));
    await useTenantStore.getState().refreshWhoami();
    expect(useTenantStore.getState().whoami?.scheduler_enabled).toBe(true);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("throttles back-to-back reconnects (tab show/hide reconnects too)", async () => {
    fetchMock.mockResolvedValue(jsonRes({ user: "u1", scheduler_enabled: true }));
    await useTenantStore.getState().refreshWhoami();
    await useTenantStore.getState().refreshWhoami();
    await useTenantStore.getState().refreshWhoami();
    expect(fetchMock).toHaveBeenCalledTimes(1);

    advance(60_000);
    await useTenantStore.getState().refreshWhoami();
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("never clobbers a resolved identity with an error payload", async () => {
    // 再起動中の CP はプレーンテキストの 5xx を返す — api() が http_5xx へ合成。
    fetchMock.mockResolvedValue(new Response("workspace agent unreachable", { status: 502 }));
    await useTenantStore.getState().refreshWhoami();
    expect(useTenantStore.getState().whoami).toEqual({ user: "u1", scheduler_enabled: false });
  });

  it("survives a network drop without touching the identity", async () => {
    fetchMock.mockRejectedValue(new Error("network"));
    await expect(useTenantStore.getState().refreshWhoami()).resolves.toBeUndefined();
    expect(useTenantStore.getState().whoami).toEqual({ user: "u1", scheduler_enabled: false });
  });
});

// ?tenant=<slug> is the hint the Control Plane leaves after a sign-in that started
// at /login/<slug> (docs/61 §61.10.4), so somebody who opened their department's
// link lands in that department rather than in whichever tenant this browser last
// used. It is a PRESELECTION only: it is honoured just when the server already
// listed that tenant among the person's memberships (ADR0043 決定 14).
describe("tenant store boot hint", () => {
  const boot = async (search: string, slugs: string[], persisted: string) => {
    vi.stubGlobal("location", { search, pathname: "/" });
    setTenant(persisted);
    fetchMock.mockReset();
    fetchMock.mockImplementation((...args: unknown[]) => {
      const url = String(args[0]);
      if (url.includes("whoami")) return Promise.resolve(jsonRes({ user: "u1" }));
      return Promise.resolve(jsonRes({ tenants: slugs.map((slug) => ({ slug })), super_admin: false }));
    });
    advance(60_000);
    await useTenantStore.getState().init();
    return useTenantStore.getState().tenant;
  };

  it("prefers the hinted tenant over the persisted selection", async () => {
    expect(await boot("?tenant=sales", ["dev", "sales"], "dev")).toBe("sales");
  });

  it("ignores a tenant the person is not a member of", async () => {
    // Anyone can type any slug — this is exactly why the hint may never be an
    // authorization input. Fall back to the persisted (still valid) selection.
    expect(await boot("?tenant=secret", ["dev", "sales"], "dev")).toBe("dev");
  });

  it("keeps the persisted selection when there is no hint", async () => {
    expect(await boot("", ["dev", "sales"], "sales")).toBe("sales");
  });
});
