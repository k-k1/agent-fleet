// クラウド費用の面（docs/67・ADR 0048）。押さえるのは、数字そのものより
// **数字の意味を壊さないこと** の 3 点:
//   ① 個人向けの見出しラベルが「あなたのコスト」になっていないこと。実測では請求の
//      2 割ほどしか人に紐づかないので、縮めた時点で会社が払う額の 1/5 を「あなたの
//      コスト」と呼ぶことになる（ADR 0048 決定 4）。
//   ② 共有インフラの額・他人の額・デプロイ合計が個人の面に一切出ないこと
//      （出れば引き算で他人の分が割り出せる）。
//   ③ タグ有効化より前の期間を「0 円」ではなく「取得できない」と言うこと。
//      有効化は遡らないので、この 0 は永久に 0 のままで自己訂正しない。
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  errText: (e: { message?: string }) => e?.message || "",
}));

import { MyCloudCostView, CloudCostAdminView, MemberCostPanel } from "./CloudCostView.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount(el: React.ReactElement) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(el);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
});

const text = () => host?.textContent || "";

const meta = (extra: Record<string, unknown> = {}) => ({
  currency: "USD",
  first_day: "2026-08-17",
  last_day: "2026-08-20",
  estimated: true,
  lag_hours: 24,
  profile: { runtime: "ecs-ec2", available: true, verified: true, shared: ["nat", "dns"] },
  ...extra,
});

describe("MyCloudCostView", () => {
  it("金額をマイクロ単位から組み立て、AWS が返した通貨のまま出す", async () => {
    api.mockResolvedValue({
      from: "2026-08-17",
      to: "2026-08-20",
      total_micro: 2_345_678,
      days: [{ day: "2026-08-17", unblended_micro: 2_345_678, estimated: false }],
      services: [{ service: "Amazon EC2", unblended_micro: 2_345_678 }],
      meta: meta(),
    });
    await mount(<MyCloudCostView />);
    // 2345678 マイクロ = $2.35。⚠️ 円に換算しない——換算した時点で請求書ではなくなる。
    expect(text()).toMatch(/\$2\.35/);
    expect(text()).not.toMatch(/￥|¥/);
  });

  it("見出しは「あなたのコスト」ではなく「直接ひも付く費用（共有分は含みません）」", async () => {
    api.mockResolvedValue({ total_micro: 1_000_000, days: [], services: [], meta: meta() });
    await mount(<MyCloudCostView />);
    expect(text()).toContain("直接ひも付く費用");
    expect(text()).toContain("共有分は含みません");
  });

  it("他人の分を逆算できるものを何も出さない", async () => {
    api.mockResolvedValue({ total_micro: 1_000_000, days: [], services: [], meta: meta() });
    await mount(<MyCloudCostView />);
    // 共有インフラのカードは本人の面には存在しない（サーバも返さない）。
    expect(host?.querySelector(".cloud-cost .admin-panel h4")?.textContent).not.toContain("共有インフラ");
    expect(text()).not.toContain("共有（割り当てなし）");
    expect(text()).not.toContain("メンバーにひも付く費用");
  });

  it("タグ有効化より前を尋ねたら、0 円ではなく「取得できない」と言う", async () => {
    api.mockResolvedValue({ total_micro: 0, days: [], services: [], meta: meta() });
    await mount(<MyCloudCostView />);
    // 既定の期間（過去 30 日）は first_day より前から始まる。
    const from = host?.querySelectorAll<HTMLInputElement>('input[type="date"]')[0];
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")!.set!;
      setter.call(from, "2026-08-01");
      from!.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(text()).toContain("遡って埋めることはできません");
  });

  it("遅延と未確定は数字の隣に出す（脚注にしない）", async () => {
    api.mockResolvedValue({ total_micro: 5_000_000, days: [], services: [], meta: meta() });
    await mount(<MyCloudCostView />);
    const notes = host?.querySelector(".cc-notes");
    expect(notes?.textContent).toContain("24 時間遅れ");
    expect(notes?.textContent).toContain("まだ確定しておらず");
    // 見出しの金額とすぐ隣り合っていること（同じパネルの中）。
    expect(host?.querySelector(".admin-panel .cc-headline + .cc-notes")).toBeTruthy();
  });

  it("Cost Explorer が読めていないときは、空ではなく理由を出す", async () => {
    api.mockResolvedValue({
      total_micro: 0,
      days: [],
      services: [],
      meta: meta({ error: "AccessDeniedException: not authorized" }),
    });
    await mount(<MyCloudCostView />);
    expect(text()).toContain("Cost Explorer を読めていない");
    expect(text()).toContain("AccessDeniedException");
  });
});

describe("CloudCostAdminView", () => {
  const tenants = [{ slug: "acme", name: "Acme" }];

  it("super_admin には共有インフラを内訳付きで出す", async () => {
    api.mockResolvedValue({
      members: [{ membership_id: "M-1", user_key: "alice", email: "a@x.com", unblended_micro: 2_000_000 }],
      attributed_micro: 2_000_000,
      shared_micro: 9_000_000,
      shared_services: [{ service: "Amazon Route 53", unblended_micro: 9_000_000 }],
      meta: meta(),
    });
    await mount(<CloudCostAdminView tenants={tenants} isSuper={true} />);
    expect(text()).toContain("共有インフラ");
    expect(text()).toMatch(/\$9\.00/);
    expect(text()).toContain("Amazon Route 53");
    // 頭割りしていないことが読み手に伝わる一文。
    expect(text()).toContain("割り振っていません");
  });

  it("共有が返らないテナント管理者の画面には共有カードを描かない", async () => {
    // ⚠️ 出し分けを画面側で判断しない。サーバが返さなければ描かない、それだけ。
    api.mockResolvedValue({
      members: [{ membership_id: "M-1", user_key: "alice", email: "a@x.com", unblended_micro: 2_000_000 }],
      attributed_micro: 2_000_000,
      meta: meta(),
    });
    await mount(<CloudCostAdminView tenants={tenants} isSuper={false} />);
    expect(text()).toContain("メンバーにひも付く費用");
    // 導入文は「共有分は別扱い」と説明する（メンバーの合計＝請求額ではないと
    // 分かる必要がある）ので、無いことを確かめるのは**カードそのもの**。
    const headings = Array.from(host!.querySelectorAll("h4")).map((h) => h.textContent);
    expect(headings).not.toContain("共有インフラ");
    expect(text()).not.toContain("共有（割り当てなし）");
  });

  it("未検証のランタイムでは数字が欠けうることを言う", async () => {
    api.mockResolvedValue({
      members: [],
      attributed_micro: 0,
      meta: meta({ profile: { runtime: "ecs", available: true, verified: false } }),
    });
    await mount(<CloudCostAdminView tenants={tenants} isSuper={true} />);
    expect(text()).toContain("実環境でまだ確認できていない");
  });
});

describe("コスト配分タグの状態", () => {
  // ⚠️ 未有効の軸があるのは「読み込み中」ではない。その間の費用は永久に失われる
  // ので、待っている間ずっと、目立つ形で出し続けなければならない。
  it("未登録の軸があるなら、失われつつあることを警告として出す", async () => {
    api.mockResolvedValue({
      total_micro: 0,
      days: [],
      services: [],
      meta: meta({ tags: { active: ["af-membership"], pending: ["af-tenant"] } }),
    });
    await mount(<MyCloudCostView />);
    const warn = host?.querySelector(".cc-notes .form-err");
    expect(warn?.textContent).toContain("af-tenant");
    expect(warn?.textContent).toContain("あとから取り戻せません");
  });

  it("自動有効化できなかったときは理由を出す", async () => {
    api.mockResolvedValue({
      total_micro: 0,
      days: [],
      services: [],
      meta: meta({ tags: { pending: ["af-tenant"], error: "AccessDeniedException: ce:UpdateCostAllocationTagsStatus" } }),
    });
    await mount(<MyCloudCostView />);
    expect(text()).toContain("自動で有効化できませんでした");
    expect(text()).toContain("AccessDeniedException");
  });

  // 人が請求コンソールで切ったものは CP が勝手に戻さない。画面もそれを警告では
  // なく事実として出す（警告にすると「直せ」と読まれる）。
  it("人が無効にした軸は、警告ではなく注記として出す", async () => {
    api.mockResolvedValue({
      total_micro: 1_000_000,
      days: [],
      services: [],
      meta: meta({ tags: { active: ["af-membership"], declined: ["af-slot-size"] } }),
    });
    await mount(<MyCloudCostView />);
    expect(text()).toContain("そのままにしています");
    expect(host?.querySelector(".cc-notes .form-err")).toBeNull();
  });

  it("全部 active なら何も足さない", async () => {
    api.mockResolvedValue({
      total_micro: 1_000_000,
      days: [],
      services: [],
      meta: meta({ tags: { active: ["af-membership", "af-tenant"] } }),
    });
    await mount(<MyCloudCostView />);
    expect(text()).not.toContain("取り戻せません");
    expect(text()).not.toContain("そのままにしています");
  });
});

// メンバー詳細のクラウド費用（docs/67 §67.15）。個人向けと同じ 3 点に加えて、
// ④ 請求の無いデプロイでは面ごと存在しないこと（0 が並ぶ画面は「無料」と読まれる）。
describe("MemberCostPanel", () => {
  // この部品は profile → 費用の 2 本を続けて叩くので、URL で振り分ける。
  const route = (cost: Record<string, unknown>, available = true) => {
    api.mockImplementation((url: string) => {
      if (url.startsWith("api/cost/profile")) {
        return Promise.resolve({ runtime: "ecs-ec2", available, verified: true });
      }
      return Promise.resolve(cost);
    });
  };

  const oneMember = {
    total_micro: 2_500_000,
    days: [
      { day: "2026-08-17", unblended_micro: 2_000_000, estimated: false },
      { day: "2026-08-18", unblended_micro: 500_000, estimated: true },
    ],
    services: [
      { service: "Amazon EC2", unblended_micro: 2_000_000 },
      { service: "Amazon Elastic Block Store", unblended_micro: 500_000 },
    ],
    meta: meta(),
  };

  it("「このメンバーのコスト」とは書かず、直接ひも付く分だと明示する", async () => {
    route(oneMember);
    await mount(<MemberCostPanel slug="sales" userKey="w-acme-co-jp" />);
    expect(text()).toContain("直接ひも付く費用");
    expect(text()).toContain("共有分は含みません");
    // ⚠️ ここが縮むと、会社が払っている額の 1/5 を「このメンバーのコスト」と呼ぶことになる。
    expect(text()).not.toContain("このメンバーのコスト");
  });

  it("一覧には無い日次と内訳を出す（合計の再掲で終わらせない）", async () => {
    route(oneMember);
    await mount(<MemberCostPanel slug="sales" userKey="w-acme-co-jp" />);
    expect(text()).toMatch(/\$2\.50/);
    expect(host?.querySelectorAll(".cc-day").length).toBe(2);
    // 未確定の日は確定した日と同じ重みで読ませない。
    expect(host?.querySelectorAll(".cc-day.est").length).toBe(1);
    expect(host?.querySelectorAll(".usage-row.cc-svc").length).toBe(2);
    expect(text()).toContain("Amazon Elastic Block Store");
  });

  it("そのメンバーだけを、メンバー単位のエンドポイントから引く", async () => {
    route(oneMember);
    await mount(<MemberCostPanel slug="sales" userKey="w@acme.co.jp" />);
    const urls = api.mock.calls.map((c) => String(c[0]));
    // ⚠️ user_key はメールのこともあるので、必ずエスケープして組み立てる。
    expect(urls).toContain("api/admin/tenants/sales/members/w%40acme.co.jp/cost");
    // 一覧（テナント全員ぶん）は叩かない——1 人を出すために全員を取り寄せない。
    expect(urls.some((u) => u.startsWith("api/admin/cloud-cost"))).toBe(false);
  });

  it("共有インフラも他人の額もデプロイ合計も出さない", async () => {
    route(oneMember);
    await mount(<MemberCostPanel slug="sales" userKey="w-acme-co-jp" />);
    // 導入文は「共有インフラは含みません」と言う（それが本体）。禁じたいのは
    // 共有の **額** が出ることなので、カードの見出しと合計ラベルで見る。
    const heads = [...(host?.querySelectorAll("h4") || [])].map((h) => h.textContent || "");
    expect(heads.some((h) => h.includes("共有インフラ"))).toBe(false);
    expect(text()).not.toContain("共有（割り当てなし）");
    expect(text()).not.toContain("メンバーにひも付く費用");
  });

  it("但し書きは合計のすぐ隣に運ぶ（0 を「無料」と読ませない）", async () => {
    route({ total_micro: 0, days: [], services: [], meta: meta() });
    await mount(<MemberCostPanel slug="sales" userKey="w-acme-co-jp" />);
    expect(host?.querySelector(".cc-headline + .cc-notes")).toBeTruthy();
    expect(host?.querySelector(".cc-notes")?.textContent).toContain("24 時間遅れ");
  });

  it("請求の無いデプロイでは、何も描かず管理 API も叩かない", async () => {
    route(oneMember, false);
    await mount(<MemberCostPanel slug="sales" userKey="w-acme-co-jp" />);
    expect(text()).toBe("");
    expect(api.mock.calls.map((c) => String(c[0])).every((u) => u.startsWith("api/cost/profile"))).toBe(true);
  });
});
