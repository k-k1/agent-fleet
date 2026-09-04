// Pins what is readable where on the sign-in surface (docs/log/61 §61.11.6 / §61.11.8). The
// first half is the registry (SignInMethodRegister), the second the deployment-wide sign-in
// method list shown beside the login rules.
//
// For the registry the only claim is that approval completes here:
//   1. Approving a pending row POSTs .../tenants/{slug}/idp/{id}/status built from that row's
//      tenant_slug. The ledger is read from GET /api/admin/idp, so the slug can only come from
//      the row — get it wrong and you act on another tenant.
//   2. The list is re-read afterwards; with a single fetch the person who pressed the button
//      is the one who cannot see the result.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  rawJSON: () => Promise.resolve(new Response("")),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

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
  it("approves a pending row against that row's tenant, then re-reads", async () => {
    api.mockResolvedValueOnce({ providers: [ROW] });
    apiJSON.mockResolvedValue({});
    // The second GET (the re-read after approval) returns the row activated.
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
    expect(api).toHaveBeenCalledTimes(2); // approving re-reads
    // An approved row switches to suspend (「停止する」); the ledger keeps the row.
    expect(findButton("承認して有効化")).toBeFalsy();
    expect(findButton("停止する")).toBeTruthy();
  });

  it("suspends from an active row", async () => {
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


// --- the algebra of accept / show on a button (docs/log/61 §61.17.5) --------------------
//
// The UI touches booleans only; these four functions are the only readers and writers of the
// two CSV columns. What is pinned are the three traps that follow from the existing meaning of
// "empty = all", each of which saves happily and then does the opposite of what was intended.
describe("accept / show algebra", () => {
  const KNOWN = ["google", "github", "t:acme:entra"];

  it("treats empty as all, so an unconfigured tenant shows every row on", () => {
    expect(acceptedIds(KNOWN, "")).toEqual(KNOWN);
    expect(ruleStateFor(KNOWN, "", "", "google")).toEqual({ accepted: true, shown: true });
  });

  it("drops unknown ids, so a deleted method left in the CSV cannot affect the state", () => {
    expect(acceptedIds(KNOWN, "google, okta")).toEqual(["google"]);
  });

  // Trap 1: writing "all on" as an explicit list loses the meaning "follow the deployment".
  it("saves empty when everything is on, instead of freezing an explicit list", () => {
    const r = toggleRule(KNOWN, "google,github", "", "t:acme:entra", "accepted", true);
    expect(r.allowed_providers).toBe("");
  });

  it("lists the remainder in knownIds order once one is switched off", () => {
    const r = toggleRule(KNOWN, "", "", "github", "accepted", false);
    expect(r.allowed_providers).toBe("google,t:acme:entra");
  });

  // "Hidden" is meaningless once the method is not accepted; leaving it would resurrect "not
  // shown" without explanation if the method is accepted again later.
  it("removes the row from hidden when accept is switched off", () => {
    const r = toggleRule(KNOWN, "", "github", "github", "accepted", false);
    expect(r.hidden_providers).toBe("");
    expect(r.allowed_providers).toBe("google,t:acme:entra");
  });

  it("adds to hidden when show is switched off, leaving accept unchanged", () => {
    const r = toggleRule(KNOWN, "", "", "google", "shown", false);
    expect(r.hidden_providers).toBe("google");
    expect(r.allowed_providers).toBe("");
    expect(ruleStateFor(KNOWN, r.allowed_providers, r.hidden_providers, "google")).toEqual({
      accepted: true,
      shown: false,
    });
  });

  // Trap 2: everything off saves as "no restriction", i.e. everything on. Unless the UI stops
  // it, restricting opens the tenant wide, so the last one cannot be turned off.
  it("will not let accept be switched off on the last one", () => {
    const locks = ruleLocks(KNOWN, KNOWN, "google", "", "google");
    expect(locks.acceptOffLocked).toBe(true);
    // One of two accepted can be switched off.
    expect(ruleLocks(KNOWN, KNOWN, "google,github", "", "google").acceptOffLocked).toBe(false);
  });

  // Trap 3: hidden has its own "ignore it if everything is hidden" valve, so switching every
  // row off saves and has no effect, which would make the screen lie. Same lock on the last one.
  it("will not let show be switched off on the last one either", () => {
    expect(ruleLocks(KNOWN, KNOWN, "", "google,github", "t:acme:entra").showOffLocked).toBe(true);
    expect(ruleLocks(KNOWN, KNOWN, "", "google", "github").showOffLocked).toBe(false);
  });

  // Ordering (§61.17.5): restricting first and inviting the tenant admin afterwards locks that
  // person out. A tenant's own row is not usable before approval, so this one rule covers the
  // ordering as well.
  it("keeps a deployment method locked as the last one while the tenant's own row is unapproved", () => {
    const usable = ["google"]; // t:acme:entra is pending approval, github is not deployed
    expect(ruleLocks(KNOWN, usable, "google,t:acme:entra", "", "google").acceptOffLocked).toBe(true);
    // Once approved and usable, it can be switched off.
    expect(
      ruleLocks(KNOWN, ["google", "t:acme:entra"], "google,t:acme:entra", "", "google").acceptOffLocked,
    ).toBe(false);
  });
});

// --- the merged list (docs/log/61 §61.17.5) ----------------------------------------
//
// Four claims:
//   1. Deployment methods and the tenant's own rows appear in one list. Without that, a
//      company that signs in with Google every day sees an empty surface.
//   2. "zero rows" and "could not read" are never conflated (§61.17.9 (2)).
//   3. Flipping a toggle PUTs both CSV columns together, and the two domain columns are sent
//      back as read — the PUT replaces all four columns, so anything omitted is cleared.
//   4. Only super_admin can flip them; a tenant admin gets static chips.
describe("merged sign-in method list", () => {
  const PROVIDERS = [
    { id: "google", label_ja: "Google でサインイン", label_en: "Sign in with Google", issuer: "https://accounts.google.com" },
    { id: "entra", label_ja: "Microsoft でサインイン", label_en: "Sign in with Microsoft", issuer: "https://login.microsoftonline.com/guid/v2.0" },
  ];
  const OWN = { ...ROW, provider_id: "t:acme:entra", status: "active", usable: true };
  const TENANT = { allowed_providers: "", hidden_providers: "", auto_join_domains: "@acme.co.jp", allowed_domains: "@acme.co.jp" };

  // Route the two GETs by URL: this surface reads both the tenant's own rows and the
  // deployment's methods.
  const routes = (own: unknown, deploy: unknown) =>
    api.mockImplementation((path: string) =>
      path === "api/admin/providers" ? Promise.resolve(deploy) : Promise.resolve(own),
    );

  const flags = () => Array.from(host!.querySelectorAll<HTMLElement>(".adm-mcp-row .idp-flags"));

  it("shows deployment methods and the tenant's own rows in one list", async () => {
    routes({ providers: [OWN] }, { providers: PROVIDERS });
    await mount(<TenantSignInMethods slug="acme" isSuper tenant={TENANT} onChanged={() => {}} />);
    const rows = Array.from(host!.querySelectorAll(".adm-mcp-row"));
    expect(rows).toHaveLength(3);
    expect(rows[0].querySelector(".as-name")?.textContent).toBe("Google でサインイン");
    expect(rows[0].querySelector("code")?.textContent).toBe("google");
    expect(rows[0].textContent).toContain("デプロイ共通");
    expect(rows[2].querySelector(".as-name")?.textContent).toBe("entra");
  });

  it("does not claim zero rows when the deployment methods could not be read", async () => {
    routes({ providers: [OWN] }, { error: { code: "forbidden", message: "tenant admin required" } });
    await mount(<TenantSignInMethods slug="acme" isSuper tenant={TENANT} onChanged={() => {}} />);
    const text = host!.textContent ?? "";
    expect(text).toContain("読み込めませんでした");
    expect(text).not.toContain("ボタンが出ません");
  });

  it("does not claim zero rows on a dropped connection (a rejection) either", async () => {
    api.mockImplementation((path: string) =>
      path === "api/admin/providers" ? Promise.reject(new Error("network")) : Promise.resolve({ providers: [OWN] }),
    );
    await mount(<TenantSignInMethods slug="acme" isSuper tenant={TENANT} onChanged={() => {}} />);
    expect(host!.textContent).toContain("読み込めませんでした");
  });

  it("makes no empty cell for a provider that returns no issuer", async () => {
    routes({ providers: [] }, { providers: [{ id: "entra", label_ja: "Microsoft でサインイン", label_en: "Sign in with Microsoft" }] });
    await mount(<TenantSignInMethods slug="acme" isSuper={false} tenant={TENANT} onChanged={() => {}} />);
    const row = host!.querySelector(".adm-mcp-row")!;
    expect(row.querySelector(".as-name")?.textContent).toBe("Microsoft でサインイン");
    expect(row.querySelector(".as-repo")).toBeNull();
  });

  it("folds a toggle into the two CSV columns and returns the domain columns as read", async () => {
    routes({ providers: [OWN] }, { providers: PROVIDERS });
    apiJSON.mockResolvedValue({});
    await mount(<TenantSignInMethods slug="acme" isSuper tenant={TENANT} onChanged={() => {}} />);
    // Switch off "show on a button" for the first row (google).
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

  it("does not let show be flipped on a row that is not accepted", async () => {
    routes({ providers: [OWN] }, { providers: PROVIDERS });
    await mount(
      <TenantSignInMethods slug="acme" isSuper tenant={{ ...TENANT, allowed_providers: "entra,t:acme:entra" }} onChanged={() => {}} />,
    );
    const [accept, show] = Array.from(flags()[0].querySelectorAll<HTMLInputElement>("input"));
    expect(accept.checked).toBe(false);
    expect(show.checked).toBe(false);
    expect(show.disabled).toBe(true);
  });

  it("shows a tenant admin no toggles, but still shows the state", async () => {
    routes({ providers: [OWN] }, { providers: PROVIDERS });
    await mount(<TenantSignInMethods slug="acme" isSuper={false} tenant={TENANT} onChanged={() => {}} />);
    expect(host!.querySelectorAll(".idp-flags input")).toHaveLength(0);
    expect(flags()[0].textContent).toContain("受け入れる");
  });
});

// Ordering guard on suspension (docs/log/61 §61.17.4, P7-3).
//
// Before an old method is suspended, ask the CP whether anyone has only ever used it. Such a
// person cannot add another method afterwards — linking one requires signing in, and the sign-in
// would use the method being suspended — so the action cannot be undone from their side.
// It confirms rather than refuses: suspension is also how a leaked IdP is shut off, and stopping
// may always be faster than starting.
describe("suspending a sign-in method", () => {
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

  it("asks back with a head count when someone has only that method", async () => {
    routes();
    apiJSON.mockResolvedValue({ error: { code: "tenant_idp_last_method_for_members" }, members: 3 });
    await mount(<TenantSignInMethods slug="acme" isSuper tenant={null} onChanged={() => {}} />);
    await clickSuspend();
    // The first attempt goes out without confirm.
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/acme/idp/idp1/status", "POST", { status: "suspended" });
    // The count comes from the server, rendered into our own wording rather than the CP's English.
    expect(document.body.textContent).toContain("3 人");
  });

  it("goes through with confirm=1 once confirmed, since stopping may be faster than starting", async () => {
    routes();
    apiJSON.mockResolvedValue({ error: { code: "tenant_idp_last_method_for_members" }, members: 1 });
    await mount(<TenantSignInMethods slug="acme" isSuper tenant={null} onChanged={() => {}} />);
    await clickSuspend();
    apiJSON.mockResolvedValue({});
    // Take the button inside the dialog. The row carries a button with the same label, so a
    // plain querySelectorAll("button") grabs that one and re-sends without confirm.
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

  it("does not ask back when nobody is affected", async () => {
    routes();
    apiJSON.mockResolvedValue({});
    await mount(<TenantSignInMethods slug="acme" isSuper tenant={null} onChanged={() => {}} />);
    await clickSuspend();
    expect(document.body.textContent).not.toContain("停止すると");
  });
});
