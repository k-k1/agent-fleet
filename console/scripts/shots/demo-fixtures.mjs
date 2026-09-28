// Stateful fixtures for the README demo recording (demo.mjs, `server.mjs --demo`).
//
// The screenshot fixtures (fixtures.mjs) are one frozen moment. A recording needs time to pass:
// sessions the Console launches have to appear, and the fleet has to move from "just started"
// to "one finished, one waiting on you" between two scenes. So this module keeps state — the
// sessions created through POST /api/sessions and a phase the recorder advances through
// POST /__demo/phase — and answers only the routes that state touches. Everything else falls
// through to fixtures.mjs.
//
// Same rule as fixtures.mjs: everything is FICTIONAL, and the shapes are the real wire contracts
// (console/src/types/session.ts, workspace/agent/internal/transcript/transcript.go,
// console/src/features/workitems/read.ts).

const L = (locale, ja, en) => (locale === "ja" ? ja : en);

export const PHASES = ["morning", "away", "back"];

// The two issues the demo hands out, and which agent takes which. The recorder reads these too,
// so the rows it clicks and the sessions the stub reports cannot drift apart.
export function demoIssues(locale) {
  return [
    {
      id: "wd1",
      key: "demo/webshop#318",
      number: 318,
      kind: "claude",
      title: L(locale, "合計 0 円の注文が決済まで進んでしまう", "Orders with a zero total reach payment"),
      labels: ["bug", "checkout"],
    },
    {
      id: "wd2",
      key: "demo/webshop#321",
      number: 321,
      kind: "codex",
      title: L(locale, "クーポンの期限判定が店舗のタイムゾーンを無視する", "Coupon expiry ignores the store's time zone"),
      labels: ["bug", "coupons"],
    },
  ];
}

// Session names are fixed per kind so that every run of the recording produces the same frames.
const NAME_OF = { claude: "sd3k7qa", codex: "sd8m2xv" };

// Short lines on purpose: the diff is read in half a pane at README size.
const DIFF_VALIDATE = `diff --git a/src/checkout/validate.ts b/src/checkout/validate.ts
index 4c2e9a1..b71f0d6 100644
--- a/src/checkout/validate.ts
+++ b/src/checkout/validate.ts
@@ -1,7 +1,11 @@
 import { err, ok, type Result } from "../lib/result";
 import type { Cart } from "./types";
+import { cartTotal } from "./total";

 export function validateCart(cart: Cart): Result {
   if (cart.items.length === 0) return err("cart_empty");
+  // A 100%-off coupon can bring the total to 0, and
+  // payment must never start for such an order.
+  if (cartTotal(cart) <= 0) return err("cart_total_zero");
   return ok();
 }
`;

const DIFF_TEST = `diff --git a/src/checkout/validate.test.ts b/src/checkout/validate.test.ts
index 9a0d3e2..e4c81b5 100644
--- a/src/checkout/validate.test.ts
+++ b/src/checkout/validate.test.ts
@@ -8,2 +8,7 @@ it("rejects an empty cart", () => {
   expect(validateCart(cart([]))).toEqual(err("cart_empty"));
 });
+
+it("rejects a cart whose coupon brings the total to zero", () => {
+  const c = cart([item({ price: 1200, qty: 1 })], { discount: 1200 });
+  expect(validateCart(c)).toEqual(err("cart_total_zero"));
+});
`;

const DIFF_MESSAGES = `diff --git a/src/checkout/messages.ts b/src/checkout/messages.ts
index 1b7c0e4..6fa2d19 100644
--- a/src/checkout/messages.ts
+++ b/src/checkout/messages.ts
@@ -3,3 +3,4 @@ export const checkoutErrors = {
   cart_empty: "Your cart is empty.",
+  cart_total_zero: "This order has nothing to pay for. Remove the coupon or add an item.",
   payment_declined: "The payment was declined.",
 };
`;

export function createDemo(locale, fx) {
  const ago = (min) => new Date(Date.now() - min * 60_000).toISOString();
  const state = { phase: "morning", created: new Map(), ledger: [] };
  const issues = demoIssues(locale);
  const issueOf = (kind) => issues.find((i) => i.kind === kind);

  // ---- repos -------------------------------------------------------------------------
  const baseRepos = [
    { name: "webshop", path: "/home/dev/repos/webshop", branch: "main", provider: "github", remote: "github.com", remotePath: "demo/webshop" },
    { name: "payments-api", path: "/home/dev/repos/payments-api", branch: "main", provider: "github", remote: "github.com", remotePath: "demo/payments-api" },
    { name: "platform-infra", path: "/home/dev/repos/platform-infra", branch: "main", provider: "github", remote: "github.com", remotePath: "demo/platform-infra" },
  ];
  const repos = () => [
    ...baseRepos,
    ...[...state.created.values()].map((s) => ({
      name: s.repo,
      path: `/home/dev/repos/${s.repo}`,
      branch: s.branch,
      dirty: s.kind === "claude" && state.phase === "back",
      provider: "github",
      remote: "github.com",
      remotePath: "demo/webshop",
      worktree: true,
      parent: "webshop",
      createdAt: s.createdAt,
    })),
  ];

  // ---- sessions ----------------------------------------------------------------------
  const sessionOf = (s) => {
    const back = state.phase === "back";
    const base = {
      name: s.name,
      kind: s.kind,
      driver: s.driver,
      title: s.title,
      repo: s.repo,
      dir: `~/repos/${s.repo}`,
      path: `/home/dev/repos/${s.repo}`,
      alive: true,
      model: s.model,
      branch: s.branch,
      worktree: true,
      createdAt: s.createdAt,
      initialPromptState: "delivered",
    };
    if (s.kind === "claude") {
      return {
        ...base,
        state: back ? "idle" : "working",
        lastSay: back
          ? L(
              locale,
              "再現テストを書いて修正案を当てました。テストは緑です。差分を worktree に置いたので確認してください。",
              "Reproduced it with a failing test and drafted the fix — the test passes now. The diff is in the worktree for review.",
            )
          : L(locale, "まず Issue を読みます。", "Reading the issue first."),
        tokenSpends: back ? [3100, 5200, 2400, 8800, 4100, 2600, 6900] : [3100],
        context: { read: back ? 88000 : 18000, create: 6200, fresh: 1400, model: s.model },
      };
    }
    return {
      ...base,
      state: back ? "question" : "working",
    };
  };

  const sessions = () => [...state.created.values()].map(sessionOf);

  // POST /api/sessions from the launch dialog: the body the Console really sends
  // (console/src/features/repos/useStartWork.ts). The worktree folder follows the Agent's rule,
  // <repo>@<branch with "/" folded to "-"> (workspace/agent/internal/gitx/git.go).
  const create = (body) => {
    const kind = body?.kind || "claude";
    const name = NAME_OF[kind] || "s" + Math.random().toString(36).slice(2, 8);
    const branch = body?.new_branch || `feature/issue-${issueOf(kind)?.number ?? 0}`;
    const s = {
      name,
      kind,
      driver: body?.driver || "",
      model: kind === "claude" ? "claude-sonnet-5" : "gpt-5.6-luna",
      title: body?.title || issueOf(kind)?.title || "",
      branch,
      repo: `webshop@${branch.replace(/\//g, "-")}`,
      prompt: body?.initial_prompt || "",
      createdAt: new Date().toISOString(),
    };
    state.created.set(name, s);
    return { name };
  };

  // ---- transcripts -------------------------------------------------------------------
  const user = (idx, text, ts) => ({ role: "user", idx, ts, text, parts: [{ kind: "text", text }] });
  const assistant = (idx, ts, parts, model) => ({ role: "assistant", idx, ts, model, text: "", parts });
  const txt = (text) => ({ kind: "text", text });
  const tool = (t, info, output) => ({ kind: "tool", tool: t, info, output });

  const claudeTurns = (s) => {
    const n = issueOf("claude").number;
    const first = [
      txt(L(locale, "まず Issue を読みます。", "Reading the issue first.")),
      tool("Bash", `gh issue view ${n}`, L(locale, "#318 合計 0 円の注文が決済まで進んでしまう\n100% 割引クーポンで合計が 0 円になると、決済画面に進めてしまう。", "#318 Orders with a zero total reach payment\nWith a 100%-off coupon the total becomes 0 and checkout still moves on to payment.")),
      tool("Grep", "validateCart  ·  src/", "src/checkout/validate.ts:4\nsrc/checkout/index.ts:22"),
    ];
    const t = [user(1, s.prompt, s.createdAt), assistant(2, s.createdAt, first, s.model)];
    if (state.phase !== "back") return t;
    t[1].parts.push(
      tool("Read", "src/checkout/validate.ts", L(locale, "13 行を読み込みました", "read 13 lines")),
      txt(
        L(
          locale,
          "`validateCart` は**空カート**だけを弾いていて、割引後の合計を見ていません。再現テストを書いてから直します。",
          "`validateCart` only rejects an **empty** cart and never looks at the total after discounts. Writing a failing test first.",
        ),
      ),
      { kind: "tool", tool: "Edit", info: "src/checkout/validate.test.ts", file: `${s.repo}/src/checkout/validate.test.ts` },
      { kind: "tool", tool: "Edit", info: "src/checkout/validate.ts", file: `${s.repo}/src/checkout/validate.ts` },
      tool("Bash", "npm test -- checkout", "PASS  src/checkout/validate.test.ts\n  ✓ rejects an empty cart (3 ms)\n  ✓ rejects a cart whose coupon brings the total to zero (1 ms)\n\nTests: 2 passed, 2 total"),
      txt(
        L(
          locale,
          "原因と修正案です。\n\n- **原因**: 合計のチェックが割引前の金額だけで、0 円になる注文を通していた\n- **修正案**: `validateCart` で割引後の合計が 0 以下なら `cart_total_zero` を返し、既存のインライン表示に載せる\n\n再現テストと修正を worktree に置きました。この方針で進めてよければ PR にします。",
          "Here is the cause and a proposed fix.\n\n- **Cause**: the total was only checked before discounts, so an order that comes to 0 got through\n- **Fix**: `validateCart` returns `cart_total_zero` when the discounted total is 0 or less, shown by the existing inline banner\n\nThe regression test and the fix are in the worktree. If this approach is right, I'll open a PR.",
        ),
      ),
    );
    return t;
  };

  const codexTurns = (s) => {
    const first = [
      txt(L(locale, "Issue とクーポンの期限判定を確認します。", "Checking the issue and the coupon expiry check.")),
      tool("Bash", `gh issue view ${issueOf("codex").number}`, ""),
      tool("Bash", "rg -n expiresAt src/coupons", "src/coupons/expiry.ts:12:  return now() > coupon.expiresAt;"),
    ];
    return [user(1, s.prompt, s.createdAt), assistant(2, s.createdAt, first, s.model)];
  };

  const codexQuestion = () => [
    {
      header: L(locale, "期限の基準", "Expiry clock"),
      question: L(locale, "クーポンの期限はどの時刻で判定しますか？", "Which clock should decide when a coupon expires?"),
      options: [
        {
          label: L(locale, "店舗のタイムゾーン", "The store's time zone"),
          description: L(locale, "店舗設定の TZ で日付の終わりを判定する。表示と一致する。", "End of day in the store's configured zone — matches what the shopper sees."),
        },
        {
          label: L(locale, "UTC のまま、表示だけ変換", "Keep UTC, convert on display"),
          description: L(locale, "判定は変えず、期限の表示を店舗時刻に直す。", "Leave the check alone and show the expiry in store time."),
        },
      ],
    },
  ];

  // The mirror's "Changed files" panel (docs/log/68): what the agent edited, joined by the Console
  // with /api/fs/changes below to badge each row as still unstaged.
  const claudeFiles = (s) => {
    const f = (rel, added, removed, lastIdx) => ({
      path: `repos/${s.repo}/${rel}`, repo: s.repo, rel, verb: "edit", added, removed, count: 1, lastIdx, lastTs: new Date().toISOString(),
    });
    return [f("src/checkout/validate.ts", 4, 0, 9), f("src/checkout/validate.test.ts", 5, 0, 8), f("src/checkout/messages.ts", 1, 0, 10)];
  };

  const messages = (name) => {
    const s = state.created.get(name);
    if (!s) return null;
    const turns = s.kind === "claude" ? claudeTurns(s) : codexTurns(s);
    const back = state.phase === "back";
    const status = s.kind === "claude" ? (back ? "idle" : "working") : back ? "question" : "working";
    return {
      name,
      messages: turns,
      cursor: turns.length * 4,
      status,
      alive: true,
      reset: true,
      firstLine: 0,
      hasMore: false,
      ...(s.kind === "claude" && back ? { files: claudeFiles(s) } : {}),
      ...(s.kind === "codex" && back
        ? {
            pendingQuestions: codexQuestion(),
            pendingText: L(locale, "直し方が 2 通りあるので、先に決めさせてください。", "There are two ways to fix this — let's pick one first."),
          }
        : {}),
      jsonlLines: turns.length * 4,
      jsonlMtime: new Date().toISOString(),
    };
  };

  // ---- working-tree changes of the claude worktree -------------------------------------
  const claudeRepo = () => [...state.created.values()].find((s) => s.kind === "claude")?.repo;
  const changes = (repo) => {
    if (state.phase !== "back" || repo !== claudeRepo()) return { changes: [] };
    return {
      changes: [
        { path: "src/checkout/validate.ts", index: "", worktree: "M" },
        { path: "src/checkout/validate.test.ts", index: "", worktree: "M" },
        { path: "src/checkout/messages.ts", index: "", worktree: "M" },
      ],
    };
  };
  const diffOf = (p) => {
    if (p.endsWith("validate.test.ts")) return DIFF_TEST;
    if (p.endsWith("validate.ts")) return DIFF_VALIDATE;
    if (p.endsWith("messages.ts")) return DIFF_MESSAGES;
    return "";
  };

  // ---- work items --------------------------------------------------------------------
  const workItems = () => {
    const base = fx.workItems(locale);
    const it = (i) => ({
      id: i.id,
      queryId: "wq1",
      provider: "github",
      kind: "issue",
      key: i.key,
      title: i.title,
      state: "open",
      url: `https://github.com/demo/webshop/issues/${i.number}`,
      assignee: "demo",
      labels: i.labels,
      repo: "demo/webshop",
      updatedAt: ago(40 + i.number - 318),
    });
    // The two demo issues on top, then a short tail of the stock rows: enough to read as a real
    // tracker without the list scrolling the repo tree out of the frame.
    const tail = base.items.filter((x) => x.key !== "demo/webshop#312").slice(0, 2);
    return { ...base, items: [...issues.map(it), ...tail], sessions: state.ledger };
  };
  const recordLedger = (body) => {
    if (!body?.itemKey) return {};
    const row = { id: "wl" + (state.ledger.length + 1), createdAt: new Date().toISOString(), ...body };
    state.ledger.push(row);
    return row;
  };

  // ---- routes ------------------------------------------------------------------------
  const exact = {
    "/api/repos": () => ({ repos: repos() }),
    "/api/sessions": (q, method, body) => (method === "POST" ? create(body) : { sessions: sessions() }),
    "/api/work-items": () => workItems(),
    "/api/work-item-sessions": (q, method, body) => (method === "POST" ? recordLedger(body) : { sessions: state.ledger }),
    "/api/fs/changes": () => {
      const r = claudeRepo();
      return { changes: r ? changes(r).changes.map((c) => ({ ...c, repo: r, path: `repos/${r}/${c.path}` })) : [] };
    },
    // The engine pills and the fleet graph belong to other stories; an empty answer keeps the
    // top bar to what the demo is about.
    "/api/engines/status": () => ({ engines: [] }),
  };
  const re = [
    // The launch dialog's live model list (console/src/lib/agentModels.ts). Unanswered, codex's
    // picker sits on "Loading models…" through the whole launch.
    [
      /^\/api\/agents\/([^/]+)\/models$/,
      (m) =>
        m[1] === "codex"
          ? { models: [{ id: "gpt-5.6-luna", label: "gpt-5.6-luna", efforts: ["low", "medium", "high"], defaultEffort: "medium" }] }
          : { models: [] },
    ],
    [/^\/api\/sessions\/([^/]+)\/messages$/, (m) => messages(decodeURIComponent(m[1]))],
    [/^\/api\/repos\/([^/]+)\/changes$/, (m) => changes(decodeURIComponent(m[1]))],
    [/^\/api\/repos\/([^/]+)\/diff$/, (m, q) => ({ path: q.get("path") || "", diff: diffOf(q.get("path") || "") })],
    [
      /^\/api\/repos\/([^/]+)\/status$/,
      (m) => {
        const r = repos().find((x) => x.name === decodeURIComponent(m[1]));
        return r ? { branch: r.branch, ahead: 0, behind: 0 } : null;
      },
    ],
  ];

  // Returns the body for a demo-owned route, or undefined to fall through to fixtures.mjs.
  const route = (pathname, query, method, body) => {
    if (exact[pathname]) return exact[pathname](query, method, body);
    for (const [rx, fn] of re) {
      const m = rx.exec(pathname);
      if (m) {
        const v = fn(m, query, method, body);
        if (v != null) return v;
      }
    }
    return undefined;
  };

  const setPhase = (p) => {
    if (!PHASES.includes(p)) return { error: `unknown phase ${p}` };
    state.phase = p;
    return { phase: p };
  };

  return { route, setPhase };
}
