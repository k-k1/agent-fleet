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
  // 再接続では whoami と /api/tenants の 2 本を読む（= 1 リフレッシュあたり fetch 2 回）。
  const reconnectRes = (tenants: unknown) =>
    fetchMock.mockImplementation((...args: unknown[]) =>
      Promise.resolve(
        String(args[0]).includes("whoami") ? jsonRes({ user: "u1", scheduler_enabled: true }) : jsonRes(tenants),
      ),
    );

  beforeEach(() => {
    fetchMock.mockReset();
    useTenantStore.setState({ whoami: { user: "u1", scheduler_enabled: false }, superAdmin: false });
    advance(60_000); // 前テストの取得から十分に離す
  });

  it("adopts the re-read deployment flags (a CP restart flips them)", async () => {
    reconnectRes({ tenants: [{ slug: "dev" }], super_admin: false });
    await useTenantStore.getState().refreshWhoami();
    expect(useTenantStore.getState().whoami?.scheduler_enabled).toBe(true);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  // ★ 本命の再発防止: superAdmin と名簿はブート 1 回きりの読み出しで、
  // その 1 回が DB 障害に当たると「管理 / テナント管理」がタブの寿命ぶん消えたままだった。
  // 再接続はまさに答えが変わり得る瞬間なので、ここで読み直して自力で回復する。
  it("re-reads the roster so a boot-time failure heals without a reload", async () => {
    reconnectRes({ tenants: [{ slug: "dev", role: "member" }], super_admin: true });
    await useTenantStore.getState().refreshWhoami();
    expect(useTenantStore.getState().superAdmin).toBe(true);
  });

  // ?tenant= は「着地の初期選択」なので、再接続のたびに効かせると、その後に自分で
  // 切り替えたテナントから引き戻される。ブートのときだけ見る。
  it("does not re-apply the ?tenant= boot hint on a reconnect", async () => {
    vi.stubGlobal("location", { search: "?tenant=sales", pathname: "/" });
    setTenant("dev");
    reconnectRes({ tenants: [{ slug: "dev" }, { slug: "sales" }], super_admin: false });
    await useTenantStore.getState().refreshWhoami();
    expect(useTenantStore.getState().tenant).toBe("dev");
    vi.stubGlobal("location", { search: "", pathname: "/" });
  });

  it("throttles back-to-back reconnects (tab show/hide reconnects too)", async () => {
    reconnectRes({ tenants: [{ slug: "dev" }], super_admin: false });
    await useTenantStore.getState().refreshWhoami();
    await useTenantStore.getState().refreshWhoami();
    await useTenantStore.getState().refreshWhoami();
    expect(fetchMock).toHaveBeenCalledTimes(2);

    advance(60_000);
    await useTenantStore.getState().refreshWhoami();
    expect(fetchMock).toHaveBeenCalledTimes(4);
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
// at /login/<slug> (docs/log/61 §61.10.4), so somebody who opened their department's
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

// 招待前（not_provisioned）の着地（docs/log/61 §61.10.2・P7-2）。
//
// AF_PROVISION=invite が新規インストールの既定になったので、これは異常系ではなく
// 「招待される前の人が最初に見る状態」。フラグが立たないと通常の Console が開き、
// 以後すべてのリクエストが 403 で弾かれてトーストが 1 つずつ出るだけになる。
describe("tenant store not_provisioned", () => {
  const errRes = (code: string, status = 403) =>
    new Response(JSON.stringify({ error: { code, message: code } }), {
      status,
      headers: { "Content-Type": "application/json" },
    });

  const boot = async (tenantsRes: () => Response) => {
    values.clear();
    fetchMock.mockReset();
    fetchMock.mockImplementation((...args: unknown[]) =>
      Promise.resolve(String(args[0]).includes("whoami") ? jsonRes({ user: "u1", email: "u1@example.com" }) : tenantsRes()),
    );
    advance(60_000);
    useTenantStore.setState({ notProvisioned: false });
    await useTenantStore.getState().init();
  };

  it("flags the landing state so App can render it", async () => {
    await boot(() => errRes("not_provisioned"));
    expect(useTenantStore.getState().notProvisioned).toBe(true);
  });

  // ★ 他の 403 と混ぜない。テナント未選択や権限不足でここに落ちると、Console が
  // 開けるべき人に「まだ招待されていません」と言うことになる。
  it("does not flag any other terminal error", async () => {
    await boot(() => errRes("forbidden_tenant"));
    expect(useTenantStore.getState().notProvisioned).toBe(false);
  });

  // 一覧が返れば落ちる（管理者がタブを開いたまま追加した → リトライで通った）。
  it("clears once the roster answers", async () => {
    useTenantStore.setState({ notProvisioned: true });
    await boot(() => jsonRes({ tenants: [{ slug: "dev" }], super_admin: false }));
    expect(useTenantStore.getState().notProvisioned).toBe(false);
  });

  // ★ super_admin はそもそもここに来ない: CP は所属ゼロでも 200 を返す（決定 23）。
  // その契約が壊れると、最初のテナントを作る人が着地面に閉じ込められる。
  it("never lands a super_admin with no membership", async () => {
    await boot(() => jsonRes({ tenants: [], super_admin: true }));
    const s = useTenantStore.getState();
    expect(s.notProvisioned).toBe(false);
    expect(s.superAdmin).toBe(true);
  });

  // ★ CP は DB 障害を **JSON 本文つきの 500**（`{"error":{"code":"internal"}}`）で返す。
  // コードだけで判定すると「アプリの恒久エラー」に見えてリトライが止まり、superAdmin が
  // false のまま固定される＝管理メニューが無言で消える（実デプロイで踏んだ）。
  it("retries a JSON-bodied 500 instead of settling on it", async () => {
    let calls = 0;
    useTenantStore.setState({ superAdmin: false });
    await boot(() => {
      calls++;
      return calls === 1 ? errRes("internal", 500) : jsonRes({ tenants: [], super_admin: true });
    });
    expect(useTenantStore.getState().superAdmin).toBe(false); // 1 回目は落ちたまま
    await new Promise((r) => setTimeout(r, 900)); // バックオフ 700ms のリトライを待つ
    expect(useTenantStore.getState().superAdmin).toBe(true);
    expect(calls).toBeGreaterThan(1);
  });
});
