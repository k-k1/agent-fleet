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
beforeAll(async () => {
  ({ useTenantStore } = await import("./tenant.ts"));
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
