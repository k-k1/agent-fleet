// Scenario "plan" — approve a plan in the browser, then commit the result
// (docs/img/demo-plan-<locale>.webp; README "Follow and steer from the browser", "Real git, in the
// console").
//
// A Claude session started in plan mode shows its plan as a card → Approve → it edits and runs the
// tests → the working copy's Changes pane lists the files → a commit message and Commit → the commit
// graph shows the new commit on top. Every step goes through the route the Console really calls:
// Approve is POST /api/sessions/<name>/input {"keys":["Enter"]} (console/src/features/mirror/
// planDecision.ts), Commit is POST /api/repos/<name>/commit {message, all}
// (console/src/features/scm/ChangesView.tsx). Neither the Changes pane nor the graph polls, so the
// recording presses their Refresh buttons, as a person would.
import { L, ago, assistant, messagesBody, phases, repoRow, router, tool, txt, user } from "./kit.mjs";

const PHASES = ["planning", "edited"];
const SESSION = "sp7ln2c";

function plan(locale) {
  return L(
    locale,
    `# 顧客ごとのクーポン利用上限

1. クーポンに \`maxUsesPerCustomer\` を足す（空なら無制限）
2. \`redeemCoupon\` で適用前にその顧客の利用回数を数える
3. 上限に達したら \`coupon_limit_reached\` で弾き、カートのバナーに出す
4. テスト: 上限到達・無制限・ゲスト購入`,
    `# Per-customer coupon usage limit

1. Add \`maxUsesPerCustomer\` to coupons (empty = unlimited)
2. Count the customer's redemptions in \`redeemCoupon\` before applying
3. Reject with \`coupon_limit_reached\` and show it in the cart banner
4. Tests: limit reached, unlimited coupon, guest checkout`,
  );
}

export function fixtures(locale, fx) {
  const state = { phase: "planning", approved: false, committed: "" };
  const t0 = ago(6);
  const title = L(locale, "顧客ごとのクーポン利用上限", "Per-customer coupon limit");

  const sessionState = () => (!state.approved ? "plan" : state.phase === "edited" ? "idle" : "working");
  const sessions = () => [
    {
      name: SESSION,
      kind: "claude",
      driver: "tui",
      title,
      repo: "webshop",
      dir: "~/repos/webshop",
      path: "/home/dev/repos/webshop",
      state: sessionState(),
      alive: true,
      model: "claude-sonnet-5",
      branch: "main",
      createdAt: t0,
      mode: "plan",
    },
  ];

  const messages = () => {
    const turns = [
      user(1, L(locale, "クーポンに顧客ごとの利用上限を付けたい。まず計画を。", "Add a per-customer usage limit to coupons. Plan it first."), t0),
      assistant(
        2,
        t0,
        [
          txt(L(locale, "クーポンの適用経路を調べます。", "Looking at how coupons are applied.")),
          tool("Grep", "redeemCoupon  ·  src/", "src/coupons/redeem.ts:9\nsrc/checkout/index.ts:41"),
          tool("Read", "src/coupons/redeem.ts", L(locale, "38 行を読み込みました", "read 38 lines")),
        ],
        "claude-sonnet-5",
      ),
    ];
    if (!state.approved) return messagesBody(SESSION, turns, "plan", { pendingPlan: plan(locale), mode: "plan" });
    // Approved: the plan stays in the transcript, marked approved (planDecision.ts matches /approv/).
    const work = [
      { kind: "plan", plan: plan(locale), answer: "approved" },
      tool("Edit", "src/coupons/model.ts"),
      tool("Edit", "src/coupons/redeem.ts"),
      tool("Edit", "src/coupons/redeem.test.ts"),
      tool("Bash", "npm test -- coupons", "PASS  src/coupons/redeem.test.ts\n\nTests: 7 passed, 7 total"),
    ];
    if (state.phase === "edited") {
      work.push(
        txt(
          L(
            locale,
            "計画どおり実装しました。上限・無制限・ゲストのテストを足し、7 本とも緑です。",
            "Implemented as planned. Added tests for the limit, unlimited coupons and guests — all 7 pass.",
          ),
        ),
      );
    }
    turns.push(assistant(3, new Date().toISOString(), work, "claude-sonnet-5"));
    return messagesBody(SESSION, turns, state.phase === "edited" ? "idle" : "working");
  };

  // Porcelain codes as the Agent sends them: " " for an unchanged side (gitx/git_view.go).
  const changes = () =>
    state.phase === "edited" && !state.committed
      ? {
          changes: [
            { path: "src/coupons/model.ts", index: " ", worktree: "M" },
            { path: "src/coupons/redeem.ts", index: " ", worktree: "M" },
            { path: "src/coupons/redeem.test.ts", index: " ", worktree: "M" },
          ],
        }
      : { changes: [] };

  // The stock webshop graph with the new commit on top once one was made; `main` moves onto it.
  const graph = () => {
    const g = fx.graph(locale);
    if (!state.committed) return g;
    const head = g.commits[0];
    const sha = "e4d27b9c1f08a5e3b6d92c47f1a08e5d3c6b19f2";
    return {
      ...g,
      commits: [
        {
          sha,
          short: sha.slice(0, 7),
          parents: [head.sha],
          author: "Demo User",
          date: new Date().toISOString(),
          subject: state.committed,
          refs: [{ name: "main", type: "head" }],
          inBranch: true,
        },
        { ...head, refs: head.refs.filter((r) => r.type !== "head") },
        ...g.commits.slice(1),
      ],
    };
  };

  const exact = {
    "/api/sessions": (q, method) => (method === "GET" ? { sessions: sessions() } : null),
    "/api/repos": () => ({
      repos: [repoRow("webshop", "main", { dirty: state.phase === "edited" && !state.committed, ahead: state.committed ? 1 : 0 })],
    }),
  };
  const re = [
    [
      /^\/api\/sessions\/([^/]+)\/input$/,
      (m, q, method, body) => {
        if (method !== "POST" || m[1] !== SESSION) return null;
        if (Array.isArray(body?.keys) && body.keys.includes("Enter")) state.approved = true;
        return {};
      },
    ],
    [/^\/api\/sessions\/([^/]+)\/messages$/, (m) => (m[1] === SESSION ? messages() : null)],
    [/^\/api\/repos\/webshop\/changes$/, () => changes()],
    [/^\/api\/repos\/webshop\/graph$/, () => graph()],
    [/^\/api\/repos\/webshop\/status$/, () => ({ branch: "main", ahead: state.committed ? 1 : 0, behind: 0 })],
    [/^\/api\/repos\/webshop\/identity$/, () => ({ effective: { name: "Demo User", email: "demo@example.com" }, source: "global" })],
    [
      /^\/api\/repos\/webshop\/commit$/,
      (m, q, method, body) => {
        if (method !== "POST") return null;
        state.committed = String(body?.message || "").trim();
        return { branch: "main", ahead: 1, behind: 0, dirty: false };
      },
    ],
  ];
  return { route: router(exact, re), setPhase: phases(PHASES, state) };
}

// ---- the recording -------------------------------------------------------------------

export const meta = { width: 1280, height: 800 };

function text(locale) {
  return {
    en: {
      cap: [
        "Started in plan mode, the agent proposes a plan first. Approve it in place.",
        "It gets to work. The working copy's changes are right beside the chat.",
        "Stage and commit without leaving the browser — real git, per working copy.",
      ],
      message: "Add a per-customer coupon usage limit",
    },
    ja: {
      cap: [
        "プランモードで始めたセッションは、まず計画を出す。その場で承認する。",
        "作業が始まる。作業コピーの変更はチャットのすぐ横に出る。",
        "ブラウザのままステージしてコミット。作業コピーごとの本物の git。",
      ],
      message: "顧客ごとのクーポン利用上限を追加",
    },
  }[locale];
}

export function seed() {
  const view = (id, content, session = null) => ({ id, session, content, wrap: null });
  return {
    layout: {
      version: 3,
      mode: "split",
      cols: [
        { id: "c1", rowRatio: 0.5, cells: [{ id: "g1", selectedViewId: "p1", views: [view("p1", { kind: "terminal", chat: true }, SESSION)] }] },
        {
          id: "c2",
          rowRatio: 0.46,
          cells: [
            { id: "g2", selectedViewId: "p2", views: [view("p2", { kind: "changes", scmRepo: "webshop" })] },
            { id: "g3", selectedViewId: "p3", views: [view("p3", { kind: "scm", scmRepo: "webshop" })] },
          ],
        },
      ],
      colRatios: [0.57, 0.43],
      activeCellId: "g1",
    },
    sections: { assistant: 0, workitems: 0, memos: 0, schedules: 0, repos: 1, files: 0 },
    ready: `!!document.querySelector(".mt-plan-approve")`,
  };
}

export async function script(c) {
  const T = text(c.locale);
  const { find } = c;
  await c.caption(1, T.cap[0]);
  await c.record();
  await c.sleep(1800);

  await c.hover(find(".mt-plan"), 700);
  await c.sleep(1500);
  await c.click(find(".mt-plan-approve"));
  await c.waitFor(`!document.querySelector(".mt-plan-approve")`);
  await c.caption(2, T.cap[1]);
  await c.sleep(2600);

  await c.phase("edited");
  await c.waitFor(`/${L(c.locale, "7 本とも緑", "all 7 pass")}/.test(document.body.innerText)`);
  await c.sleep(900);
  // The Changes pane re-reads only when asked: its header's Refresh (ChangesView.tsx).
  await c.click(`[...document.querySelectorAll(".scmview")].find((v) => v.querySelector(".commitbox"))?.querySelector(".ui-iconbtn")`);
  await c.waitFor(`document.querySelectorAll("li.change").length === 3`);
  await c.sleep(1200);

  await c.caption(3, T.cap[2]);
  await c.click(find(".commitbox textarea"), 600);
  await c.type(T.message);
  await c.sleep(400);
  await c.click(find(".commitbox-all"), 500);
  await c.sleep(400);
  await c.click(find(".commitbox .ui-btn-primary"), 500);
  await c.waitFor(`document.querySelectorAll("li.change").length === 0`);
  await c.sleep(900);
  // The graph pane is narrow here, so its header actions sit behind ⋯ (SourceControlView.tsx).
  await c.click(find(".scm-more > .ui-iconbtn"), 700);
  await c.sleep(400);
  await c.click(find(".scm-more-menu .ui-menu-item", "Refresh|更新"), 500);
  await c.waitFor(`/${T.message}/.test(document.body.innerText)`);
  await c.sleep(3200);
}
