// The LENDING side of ADR 0079: the super_admin of the deployment that owns the engines mints
// and reads the `afei_…` issuing token a borrowing deployment puts in `AF_REMOTE_ENGINE_TOKEN`
// (control-plane/engine_issue_token.go, decision 3).
//
// 🔴 What this file exists to keep is not the happy path. The value is derived deterministically
// from the signing master, so there is no such thing as invalidating one copy of it, and the CP
// cannot refuse to mint for a person's membership ("is this a person?" has no truthful column).
// Everything that stops an operator from lending their own token and holding the fleet hostage
// is TEXT ON THIS PANEL: the warning, the revocation instruction, and the `has_workspace` mark.
// So those three have cases of their own, each paired with a control that fails when the block
// is simply not rendered — otherwise "the sentence is missing" and "the panel is missing" look
// alike from here.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { EnginesAdminView } from "./adminEngines.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

/** 🔴 Not a real token and not a real deployment: the prefix is the only thing about it that has
 *  to be right, and `.githooks/pre-commit` reads this tree for real hostnames and real names. */
const TOKEN = "afei_000000000000000000000000dummy";
const TENANT = "acme";
const USER_KEY = "borrow-bot";

/** `POST /api/admin/engines/issue-token`'s answer, field for field (engineIssueTokenView).
 *  `warning` and `revoke` are included BECAUSE the panel must not print them: they are English
 *  prose the CP composes for one deployment, not for one reader. */
const ISSUED = {
  token: TOKEN,
  membership_id: "m_0001",
  tenant_slug: TENANT,
  user_key: USER_KEY,
  role: "member",
  env_var: "AF_REMOTE_ENGINE_TOKEN",
  opens: ["POST /internal/engine/token", "GET /internal/engine/catalog"],
  deterministic: true,
  has_workspace: false,
  revoke: "Remove the membership acme/borrow-bot: it is refused on the next request.",
  warning: "This is the membership's own engine credential, not a per-borrower secret.",
};

/** The screen with no engine of its own. The panel is deliberately not conditional on a row —
 *  a borrower is stood up before the GPU is — and an empty answer keeps the ECS polling and the
 *  heatmaps out of these cases. */
const superAnswer = { super_admin: true, engines: [] };
const tenantAnswer = { super_admin: false, engines: [] };

async function mount(answer: unknown) {
  api.mockImplementation((p: string) =>
    String(p).endsWith("/ingest") ? Promise.resolve({ jobs: [] }) : Promise.resolve(answer),
  );
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<EnginesAdminView />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const text = () => host?.textContent || "";
const btn = (label: string) =>
  Array.from(host?.querySelectorAll("button") || []).find((b) => b.textContent?.trim() === label);

const click = async (el: Element | undefined) => {
  expect(el, "the button is not on the screen").toBeTruthy();
  await act(async () => {
    el!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
};

const type = async (el: Element, v: string) => {
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    setter.call(el, v);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
};

/** Fill the two fields and press. `answer` is what the CP replies with — a view, or an
 *  `{error}` body. */
async function issue(answer: unknown, tenant = TENANT, key = USER_KEY) {
  const inputs = Array.from(host!.querySelectorAll(".engines-issue input"));
  expect(inputs.length, "tenant and user_key").toBe(2);
  await type(inputs[0], tenant);
  await type(inputs[1], key);
  apiJSON.mockResolvedValue(answer);
  await click(btn("発行して表示する"));
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
});

describe("issuing a borrowing token (ADR 0079 decision 3)", () => {
  it("is not on the screen for a tenant_admin", async () => {
    await mount(tenantAnswer);
    // The panel did render — otherwise the assertion below would pass on an empty document.
    expect(text()).toContain("この配備は自前の推論エンジンを動かしていません");
    // 🔴 This credential opens the engines of the whole DEPLOYMENT, so it is not a tenant's to
    // hand out — and the route is super_admin anyway (withSuperAdmin), so a tenant_admin
    // pressing it would only get a 403.
    expect(text()).not.toContain("借用トークンを発行して見せる");
    expect(btn("発行して表示する")).toBeFalsy();
  });

  it("is on the screen for a super_admin — the positive control", async () => {
    await mount(superAnswer);
    expect(text()).toContain("借用トークンを発行して見せる");
    expect(btn("発行して表示する")).toBeTruthy();
  });

  it("says what pressing it does before it is pressed", async () => {
    await mount(superAnswer);
    // The distinction that has to survive: it SHOWS the membership's existing credential rather
    // than creating one per borrower. An operator who believes the second issues a second token
    // to cut the first off, and there is no second token.
    expect(text()).toContain("そのメンバーシップ自身のエンジン資格情報です");
    expect(text()).toContain("借りる側ごとに違う値を出すことはできません");
    expect(text()).toContain("他に何にも使っていないメンバーシップにだけ発行してください");
    // ...and nothing is on screen yet.
    expect(text()).not.toContain(TOKEN);
  });

  it("asks the route the CP actually serves", async () => {
    await mount(superAnswer);
    await issue(ISSUED);
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/issue-token", "POST", {
      tenant_slug: TENANT,
      user_key: USER_KEY,
    });
  });

  it("shows the token only when asked, and names the variable it goes in", async () => {
    await mount(superAnswer);
    await issue(ISSUED);
    // Masked on arrival: the value is in state and not in the document, so a screen share of
    // this panel shows the dots. Copying it does not need it revealed.
    expect(text()).not.toContain(TOKEN);
    expect(btn("トークンをコピー")).toBeTruthy();
    await click(btn("表示する"));
    expect(text()).toContain(TOKEN);
    // Where the borrowing deployment puts it (decision 2), named by the CP rather than by this
    // screen, plus the ready-to-paste assignment.
    expect(text()).toContain("AF_REMOTE_ENGINE_TOKEN");
    expect(btn("AF_REMOTE_ENGINE_TOKEN= の形でコピー")).toBeTruthy();
    // Whose token it is, so it is not pasted for the wrong membership.
    expect(text()).toContain(TENANT + "/" + USER_KEY);
    expect(text()).toContain("m_0001");
  });

  it("takes the value off the screen on request", async () => {
    await mount(superAnswer);
    await issue(ISSUED);
    await click(btn("表示する"));
    expect(text()).toContain(TOKEN);
    await click(btn("画面から消す"));
    expect(text()).not.toContain(TOKEN);
    // The whole answer goes, not just the reveal: the membership line and the copy buttons are
    // as much "a credential is open on this screen" as the string is.
    expect(btn("トークンをコピー")).toBeFalsy();
    expect(text()).toContain("借用トークンを発行して見せる");
  });

  it("lists what the token opens, and that it is only that", async () => {
    await mount(superAnswer);
    await issue(ISSUED);
    expect(text()).toContain("POST /internal/engine/token");
    expect(text()).toContain("GET /internal/engine/catalog");
    expect(text()).toContain("git・MCP・メモ・API は、このトークンでは開きません");
  });

  // 🔴 The two cases this file exists for. Everything above is convenience; these are the
  // accident: an operator who lends their own token, or a borrower nobody can cut off.
  it("says why it cannot be revoked, and how it is", async () => {
    await mount(superAnswer);
    await issue(ISSUED);
    expect(text()).toContain("1 本だけ無効にすることはできません");
    // The instruction names the membership, because "delete the membership" with no subject is
    // an instruction somebody carries out on the wrong one.
    expect(text()).toContain("失効のしかた");
    expect(text()).toContain(`${TENANT}/${USER_KEY} のメンバーシップを削除してください`);
    // ...and the price of the alternative in the same breath, or "delete the membership" reads
    // as one option among several.
    expect(text()).toContain("この配備の全員がログアウトします");
  });

  it("keeps the warning when the CP omits `deterministic`", async () => {
    // An older control plane must leave this panel saying LESS, never leave the one sentence
    // that explains why the value cannot be taken back missing.
    await mount(superAnswer);
    const { deterministic: _drop, ...noFlag } = ISSUED;
    await issue(noFlag);
    expect(text()).toContain("1 本だけ無効にすることはできません");
    expect(text()).toContain("この配備の全員がログアウトします");
  });

  it("draws the warning from the structured fields, not from the server's English", async () => {
    await mount(superAnswer);
    await issue(ISSUED);
    // `warning` and `revoke` arrive as English prose (the CP serves one deployment, not one
    // reader). Printing them would put the two sentences that must be read into a language
    // half this Console's readers do not read.
    expect(text()).not.toContain("per-borrower secret");
    expect(text()).not.toContain("Remove the membership");
  });

  it("marks a membership that has a workspace", async () => {
    await mount(superAnswer);
    await issue({ ...ISSUED, has_workspace: true });
    // The closest thing to evidence that this membership belongs to a person — the state
    // decision 3's 🔴 forbids lending. The CP reports it and does not refuse, so this mark is
    // the whole of the enforcement.
    expect(text()).toContain("ワークスペースあり");
    expect(text()).toContain("人が使っているメンバーシップの形です");
    expect(text()).toContain("借用専用のメンバーシップを別に作ってから発行してください");
    // Marked out, not left as one more paragraph — and 🔴 ABOVE the value, because a caution
    // under a credential is read after the credential is on the clipboard.
    const box = host!.querySelector(".engines-issue-person");
    expect(box, "the person warning has no block of its own").toBeTruthy();
    const value = host!.querySelector(".engines-issue-value");
    expect(box!.compareDocumentPosition(value!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("says the opposite for a membership with none — the positive control", async () => {
    await mount(superAnswer);
    await issue({ ...ISSUED, has_workspace: false });
    expect(text()).toContain("借用専用のメンバーシップとして期待される形です");
    expect(text()).not.toContain("ワークスペースあり");
    expect(host!.querySelector(".engines-issue-person")).toBeFalsy();
  });

  it("translates the refusal for a membership that is not active", async () => {
    await mount(superAnswer);
    // 409 membership_inactive: the gateway resolves the membership on every request, so minting
    // for a dead one hands the operator a string that answers 401 with no way to tell that from
    // a typo in the user key.
    await issue({
      error: {
        code: "membership_inactive",
        status: 409,
        message: "this membership is not active — the engine gateway refuses its token",
      },
    });
    expect(text()).toContain("そのメンバーシップは有効ではないので、発行しても使えません");
    // errDetail, not errText: the CP's own message is the rest of the sentence.
    expect(text()).toContain("the engine gateway refuses its token");
    // 🔴 And nothing was shown. A failed mint that left the previous answer on screen would be
    // a token attributed to the membership the form now names.
    expect(text()).not.toContain(TOKEN);
    expect(btn("トークンをコピー")).toBeFalsy();
  });

  it("translates a mistyped tenant or user_key", async () => {
    await mount(superAnswer);
    // The likeliest failure of this form by far, and the CP answers both with a 404 whose
    // message is English.
    await issue({ error: { code: "no_tenant", status: 404, message: "unknown tenant" } }, "nope");
    expect(text()).toContain("そのスラッグのテナントはこの配備にありません");
  });
});
