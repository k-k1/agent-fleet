// ログイン面のうち「どこで何が読めるか」を固定する（docs/log/61 §61.11.6 / §61.11.8）。
// 前半は登録簿（SignInMethodRegister）、後半はログイン規則に添えるデプロイ共通の
// サインイン方法一覧。
//
// 登録簿で押さえるのは「承認がここで完結すること」だけ:
//   ① 承認待ちの行で「承認して有効化」を押すと、その行の tenant_slug で組んだ
//      POST .../tenants/{slug}/idp/{id}/status が飛ぶ（台帳は GET /api/admin/idp を
//      読むので、slug は行から拾うしかない — ここを取り違えると別テナントを触る）
//   ② 押したあと一覧を読み直す（1 回きりの fetch だと押した本人にだけ結果が見えない）
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  rawJSON: () => Promise.resolve(new Response("")),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { SignInMethodRegister, TenantSignInMethods } from "./tenantSignInMethods.tsx";
import { acceptedIds, ruleLocks, ruleStateFor, toggleRule } from "./tenantLoginRules.tsx";

const ROW = {
  id: "idp1",
  name: "entra",
  tenant_slug: "acme",
  issuer: "https://login.microsoftonline.com/guid/v2.0",
  client_id: "cid",
  trust: "issuer",
  allowed_domains: "@acme.co.jp",
  status: "pending",
  usable: false,
};

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount(node: React.ReactNode = <SignInMethodRegister />) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(node);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const findButton = (label: string) =>
  Array.from(document.querySelectorAll<HTMLButtonElement>(".allow-acts button")).find((b) =>
    (b.textContent || "").includes(label),
  );

beforeEach(() => {
  api.mockReset();
  apiJSON.mockReset();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("SignInMethodRegister", () => {
  it("承認待ちの行から、その行のテナントへ承認を投げて読み直す", async () => {
    api.mockResolvedValueOnce({ providers: [ROW] });
    apiJSON.mockResolvedValue({});
    // 2 回目の GET（承認後の読み直し）は有効化された姿を返す。
    api.mockResolvedValueOnce({ providers: [{ ...ROW, status: "active", usable: true }] });
    await mount();
    expect(api).toHaveBeenCalledWith("api/admin/idp");

    const approve = findButton("承認して有効化");
    expect(approve).toBeTruthy();
    await act(async () => {
      approve!.click();
    });
    await act(async () => {
      await Promise.resolve();
    });

    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/acme/idp/idp1/status", "POST", {
      status: "active",
    });
    expect(api).toHaveBeenCalledTimes(2); // 承認したら読み直す
    // 承認済みの行は「停止する」に変わる（台帳は空にならず残る）。
    expect(findButton("承認して有効化")).toBeFalsy();
    expect(findButton("停止する")).toBeTruthy();
  });

  it("有効な行からは停止できる", async () => {
    api.mockResolvedValue({ providers: [{ ...ROW, status: "active", usable: true }] });
    apiJSON.mockResolvedValue({});
    await mount();
    const suspend = findButton("停止する");
    expect(suspend).toBeTruthy();
    await act(async () => {
      suspend!.click();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/acme/idp/idp1/status", "POST", {
      status: "suspended",
    });
  });
});


// --- 受け入れる／ボタンに出す の代数（docs/log/61 §61.17.5）------------------------
//
// ここが P7-0 の本体。UI は真偽値しか触らず、CSV 2 本の読み書きはこの 4 関数だけが
// やる。押さえるのは「空＝全部」という既存の意味から出る 3 つの罠で、どれも
// **保存できてしまい、そして意図と逆に効く**種類のもの。
describe("受け入れる／出す の代数", () => {
  const KNOWN = ["google", "github", "t:acme:entra"];

  it("空＝全部。未設定のテナントは全行 ON に見える", () => {
    expect(acceptedIds(KNOWN, "")).toEqual(KNOWN);
    expect(ruleStateFor(KNOWN, "", "", "google")).toEqual({ accepted: true, shown: true });
  });

  it("知らない id は落とす（消された方式が CSV に残っていても状態に影響させない）", () => {
    expect(acceptedIds(KNOWN, "google, okta")).toEqual(["google"]);
  });

  // ★ 罠 1: 全部 ON を明示リストで書くと「デプロイに追従する」意味が消える。
  it("全部 ON なら空で保存する（明示リストに固めない）", () => {
    const r = toggleRule(KNOWN, "google,github", "", "t:acme:entra", "accepted", true);
    expect(r.allowed_providers).toBe("");
  });

  it("1 つ外すと、残りを knownIds の順で明示する", () => {
    const r = toggleRule(KNOWN, "", "", "github", "accepted", false);
    expect(r.allowed_providers).toBe("google,t:acme:entra");
  });

  // ★ 受け入れないなら「出さない」指定は意味を持たない。残すと、後で受け入れ直した
  // ときに「出ない」が説明なく復活する。
  it("受け入れるを OFF にすると、その行は hidden からも消える", () => {
    const r = toggleRule(KNOWN, "", "github", "github", "accepted", false);
    expect(r.hidden_providers).toBe("");
    expect(r.allowed_providers).toBe("google,t:acme:entra");
  });

  it("出すを OFF にすると hidden に入り、受け入れは変わらない", () => {
    const r = toggleRule(KNOWN, "", "", "google", "shown", false);
    expect(r.hidden_providers).toBe("google");
    expect(r.allowed_providers).toBe("");
    expect(ruleStateFor(KNOWN, r.allowed_providers, r.hidden_providers, "google")).toEqual({
      accepted: true,
      shown: false,
    });
  });

  // ★ 罠 2: 全部 OFF は「制限なし＝全部 ON」として保存される。UI が止めないと
  // 「絞ったつもりで全開」になるので、最後の 1 つは倒せない。
  it("最後の 1 つは受け入れを外せない", () => {
    const locks = ruleLocks(KNOWN, KNOWN, "google", "", "google");
    expect(locks.acceptOffLocked).toBe(true);
    // 2 つ受け入れているうちの 1 つなら外せる。
    expect(ruleLocks(KNOWN, KNOWN, "google,github", "", "google").acceptOffLocked).toBe(false);
  });

  // ★ 罠 3: hidden にも「全部隠したら無視する」弁がある。全行 OFF は保存できて
  // 効かない＝画面が嘘をつくので、こちらも最後の 1 つは倒せない。
  it("最後の 1 つは「出す」も外せない", () => {
    expect(ruleLocks(KNOWN, KNOWN, "", "google,github", "t:acme:entra").showOffLocked).toBe(true);
    expect(ruleLocks(KNOWN, KNOWN, "", "google", "github").showOffLocked).toBe(false);
  });

  // ★ 順序（§61.17.5）: 先に絞ってからテナント管理者を招くと、その人が入れない。
  // 承認前の自前の行は usable でないので、この 1 本の規則が順序も兼ねる。
  it("自前の行がまだ承認前なら、デプロイの方式は最後の 1 つとして外せない", () => {
    const usable = ["google"]; // t:acme:entra は承認待ち、github は未導入とする
    expect(ruleLocks(KNOWN, usable, "google,t:acme:entra", "", "google").acceptOffLocked).toBe(true);
    // 承認されて usable になれば外せる。
    expect(
      ruleLocks(KNOWN, ["google", "t:acme:entra"], "google,t:acme:entra", "", "google").acceptOffLocked,
    ).toBe(false);
  });
});

// --- 統合リスト（docs/log/61 §61.17.5）--------------------------------------------
//
// 押さえるのは 4 つ:
//   ① デプロイの方式と自前の行が**同じリスト**に並ぶ（以前はデプロイの方式がどこにも
//      出ず、Google で毎日入っている会社でもこの面が空だった）
//   ② ★ 「0 件」と「読めなかった」を混ぜない（§61.17.9 ②）
//   ③ トグルを倒すと CSV 2 本にまとめて PUT され、**ドメインの 2 列は読んだ値のまま
//      返る**（この PUT は 4 列を丸ごと置き換えるので、送らないと消える）
//   ④ 倒せるのは super_admin だけ。テナント管理者には静的なチップ
describe("サインイン方法の統合リスト", () => {
  const PROVIDERS = [
    { id: "google", label_ja: "Google でサインイン", label_en: "Sign in with Google", issuer: "https://accounts.google.com" },
    { id: "entra", label_ja: "Microsoft でサインイン", label_en: "Sign in with Microsoft", issuer: "https://login.microsoftonline.com/guid/v2.0" },
  ];
  const OWN = { ...ROW, provider_id: "t:acme:entra", status: "active", usable: true };
  const TENANT = { allowed_providers: "", hidden_providers: "", auto_join_domains: "@acme.co.jp", allowed_domains: "@acme.co.jp" };

  // 2 本の GET を URL で振り分ける（この面は自前の行とデプロイの方式の両方を読む）。
  const routes = (own: unknown, deploy: unknown) =>
    api.mockImplementation((path: string) =>
      path === "api/admin/providers" ? Promise.resolve(deploy) : Promise.resolve(own),
    );

  const flags = () => Array.from(host!.querySelectorAll<HTMLElement>(".adm-mcp-row .idp-flags"));

  it("デプロイの方式と自前の行が同じリストに並ぶ", async () => {
    routes({ providers: [OWN] }, { providers: PROVIDERS });
    await mount(<TenantSignInMethods slug="acme" isSuper tenant={TENANT} onChanged={() => {}} />);
    const rows = Array.from(host!.querySelectorAll(".adm-mcp-row"));
    expect(rows).toHaveLength(3);
    expect(rows[0].querySelector(".as-name")?.textContent).toBe("Google でサインイン");
    expect(rows[0].querySelector("code")?.textContent).toBe("google");
    expect(rows[0].textContent).toContain("デプロイ共通");
    expect(rows[2].querySelector(".as-name")?.textContent).toBe("entra");
  });

  it("デプロイの方式が読めなかったときは「0 件」と言わない", async () => {
    routes({ providers: [OWN] }, { error: { code: "forbidden", message: "tenant admin required" } });
    await mount(<TenantSignInMethods slug="acme" isSuper tenant={TENANT} onChanged={() => {}} />);
    const text = host!.textContent ?? "";
    expect(text).toContain("読み込めませんでした");
    expect(text).not.toContain("ボタンが出ません");
  });

  it("通信断（reject）でも「0 件」と言わない", async () => {
    api.mockImplementation((path: string) =>
      path === "api/admin/providers" ? Promise.reject(new Error("network")) : Promise.resolve({ providers: [OWN] }),
    );
    await mount(<TenantSignInMethods slug="acme" isSuper tenant={TENANT} onChanged={() => {}} />);
    expect(host!.textContent).toContain("読み込めませんでした");
  });

  it("issuer が返らない相手には、空セルを作らない", async () => {
    routes({ providers: [] }, { providers: [{ id: "entra", label_ja: "Microsoft でサインイン", label_en: "Sign in with Microsoft" }] });
    await mount(<TenantSignInMethods slug="acme" isSuper={false} tenant={TENANT} onChanged={() => {}} />);
    const row = host!.querySelector(".adm-mcp-row")!;
    expect(row.querySelector(".as-name")?.textContent).toBe("Microsoft でサインイン");
    expect(row.querySelector(".as-repo")).toBeNull();
  });

  it("トグルを倒すと CSV 2 本になり、ドメインの 2 列は読んだ値のまま返る", async () => {
    routes({ providers: [OWN] }, { providers: PROVIDERS });
    apiJSON.mockResolvedValue({});
    await mount(<TenantSignInMethods slug="acme" isSuper tenant={TENANT} onChanged={() => {}} />);
    // 1 行目（google）の「ボタンに出す」を外す。
    const show = flags()[0].querySelectorAll<HTMLInputElement>("input")[1];
    await act(async () => {
      show.click();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/acme/login", "PUT", {
      allowed_providers: "",
      hidden_providers: "google",
      auto_join_domains: "@acme.co.jp",
      allowed_domains: "@acme.co.jp",
    });
  });

  it("受け入れていない行の「出す」は倒せない", async () => {
    routes({ providers: [OWN] }, { providers: PROVIDERS });
    await mount(
      <TenantSignInMethods slug="acme" isSuper tenant={{ ...TENANT, allowed_providers: "entra,t:acme:entra" }} onChanged={() => {}} />,
    );
    const [accept, show] = Array.from(flags()[0].querySelectorAll<HTMLInputElement>("input"));
    expect(accept.checked).toBe(false);
    expect(show.checked).toBe(false);
    expect(show.disabled).toBe(true);
  });

  it("テナント管理者にはトグルを出さない（状態は読める）", async () => {
    routes({ providers: [OWN] }, { providers: PROVIDERS });
    await mount(<TenantSignInMethods slug="acme" isSuper={false} tenant={TENANT} onChanged={() => {}} />);
    expect(host!.querySelectorAll(".idp-flags input")).toHaveLength(0);
    expect(flags()[0].textContent).toContain("受け入れる");
  });
});

// 停止の順序ガード（docs/log/61 §61.17.4 · P7-3）。
//
// 古い方式を止める前に、その方式しか使ったことのない人が居ないか CP に聞く。止めたあとで
// 本人が別の方式を足すことはできない（紐づけにはサインインが要り、そのサインインに使うのが
// 今止めようとしている方式）ので、あとから取り返せない種類の操作。
// ★ 拒否ではなく確認。停止は「漏れた IdP を止める」手段でもあり、常に始めるより速くてよい。
describe("サインイン方法の停止", () => {
  const ROW_ACTIVE = { ...ROW, id: "idp1", status: "active", usable: true, provider_id: "t:acme:entra" };
  const routes = () =>
    api.mockImplementation((path: string) =>
      Promise.resolve(path === "api/admin/providers" ? { providers: [] } : { providers: [ROW_ACTIVE] }),
    );
  const clickSuspend = async () => {
    const b = findButton("停止する");
    await act(async () => {
      b!.click();
    });
  };

  it("その方式しか持たない人が居ると、人数を出して聞き返す", async () => {
    routes();
    apiJSON.mockResolvedValue({ error: { code: "tenant_idp_last_method_for_members" }, members: 3 });
    await mount(<TenantSignInMethods slug="acme" isSuper tenant={null} onChanged={() => {}} />);
    await clickSuspend();
    // 1 回目は confirm 無しで飛ぶ。
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/acme/idp/idp1/status", "POST", { status: "suspended" });
    // ★ 人数はサーバ由来。CP の英文ではなく、こちらの文言に差して出す。
    expect(document.body.textContent).toContain("3 人");
  });

  it("確認すると confirm=1 で通す（止めるのは常に始めるより速くてよい）", async () => {
    routes();
    apiJSON.mockResolvedValue({ error: { code: "tenant_idp_last_method_for_members" }, members: 1 });
    await mount(<TenantSignInMethods slug="acme" isSuper tenant={null} onChanged={() => {}} />);
    await clickSuspend();
    apiJSON.mockResolvedValue({});
    // ★ ダイアログの中のボタンを取る。行にも同じ文言のボタンがあるので、素の
    // querySelectorAll("button") では行の方を掴んでしまう（confirm 無しで再送される）。
    const ok = Array.from(
      document.querySelectorAll<HTMLButtonElement>(".confirm-actions button"),
    ).find((b) => (b.textContent || "").includes("停止する"));
    await act(async () => {
      ok!.click();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/acme/idp/idp1/status?confirm=1", "POST", {
      status: "suspended",
    });
  });

  it("誰も困らないなら聞き返さない", async () => {
    routes();
    apiJSON.mockResolvedValue({});
    await mount(<TenantSignInMethods slug="acme" isSuper tenant={null} onChanged={() => {}} />);
    await clickSuspend();
    expect(document.body.textContent).not.toContain("停止すると");
  });
});
