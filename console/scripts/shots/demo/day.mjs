// Scenario "day" — the README's "A day with Agent Fleet" (docs/img/demo-day-<locale>.webp).
//
// Two issues are started from the issue tracker as a Claude Code and a Codex session, each in its
// own worktree; the laptop closes; a permission request is allowed from the session's Slack thread
// on a phone; back at the desk the overview shows one finished and one waiting, beside the finished
// session's diff.
//
// The fixtures keep state: the sessions created through POST /api/sessions, and a phase the
// recorder advances (kit.mjs `phases`). Same rule as fixtures.mjs: everything is FICTIONAL, and the
// shapes are the real wire contracts (console/src/types/session.ts,
// workspace/agent/internal/transcript/transcript.go, console/src/features/workitems/read.ts).
import { L, ago, assistant, messagesBody, phases, router, tool, txt, user } from "./kit.mjs";

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

export function fixtures(locale, fx) {
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
    return messagesBody(name, turns, status, {
      ...(s.kind === "claude" && back ? { files: claudeFiles(s) } : {}),
      ...(s.kind === "codex" && back
        ? {
            pendingQuestions: codexQuestion(),
            pendingText: L(locale, "直し方が 2 通りあるので、先に決めさせてください。", "There are two ways to fix this — let's pick one first."),
          }
        : {}),
    });
  };

  // ---- working-tree changes of the claude worktree -------------------------------------
  const claudeRepo = () => [...state.created.values()].find((s) => s.kind === "claude")?.repo;
  const changes = (repo) => {
    if (state.phase !== "back" || repo !== claudeRepo()) return { changes: [] };
    return {
      changes: [
        { path: "src/checkout/validate.ts", index: " ", worktree: "M" },
        { path: "src/checkout/validate.test.ts", index: " ", worktree: "M" },
        { path: "src/checkout/messages.ts", index: " ", worktree: "M" },
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
  };
  const re = [
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

  return { route: router(exact, re), setPhase: phases(PHASES, state) };
}

// ---- the recording -------------------------------------------------------------------

export const meta = { width: 1280, height: 800, band: 56 };

// The captions are the README's four steps (README.md / README.ja.md "A day with Agent Fleet"),
// shortened to one line each. The phone texts are the chat bridge's own strings:
// workspace/agent/internal/bridge/format.go (headline), slack_interact.go (button labels) and
// workspace/agent/internal/sessionx/bridge_answer.go (the line a press leaves behind).
function text(locale) {
  const [claude] = demoIssues(locale);
  return {
    en: {
      cap: [
        "Hand one issue to Claude Code and another to Codex, each in its own git worktree.",
        "Close the laptop and leave. Both keep working on the server.",
        "One asks for permission. It arrives in the session's Slack thread — answer from your phone.",
        "Back at a desk: what finished, what waits on you, and each worktree's changes.",
      ],
      away: "Laptop closed",
      phone: {
        thread: "Thread",
        channel: "#agents",
        bot: "Agent Fleet",
        headline: "A tool permission is awaiting your approval",
        session: `"#${claude.number} ${claude.title}" (Claude Code)`,
        link: "Open in Console",
        replies: "1 reply",
        allow: "Allow",
        deny: "Deny",
        done: "✓ Allowed",
        compose: "Reply…",
      },
      splitDown: "Split down",
    },
    ja: {
      cap: [
        "Claude Code と Codex に別々の Issue を頼む。それぞれ自分の git worktree で。",
        "ノートを閉じて出かける。どちらもサーバーの上で作業を続ける。",
        "片方が許可を求める。依頼はセッションの Slack スレッドに届き、スマートフォンから答える。",
        "机に戻ると、どれが終わりどれがあなたを待っているか、各 worktree の変更まで分かる。",
      ],
      away: "ノートは閉じたまま",
      phone: {
        thread: "スレッド",
        channel: "#agents",
        bot: "Agent Fleet",
        headline: "ツール実行の許可待ちです",
        session: `「#${claude.number} ${claude.title}」（Claude Code）`,
        link: "Console で開く",
        replies: "1 件の返信",
        allow: "許可",
        deny: "拒否",
        done: "✓ 許可しました",
        compose: "返信する…",
      },
      splitDown: "下に分割",
    },
  }[locale];
}

// What a returning user's browser would restore: the webshop commit graph where the first session
// will open, the sessions overview beside it (the left column gets more room: it ends up holding
// the finished session's chat over its diff), the issue tracker and the repo tree open in the rail,
// and the Claude session's "Changed files" panel open as someone who uses it would have left it.
export function seed() {
  return {
    layout: {
      version: 3,
      mode: "split",
      cols: [
        { id: "c1", rowRatio: 0.5, cells: [{ id: "g1", selectedViewId: "p1", views: [{ id: "p1", session: null, content: { kind: "scm", scmRepo: "webshop" }, wrap: null }] }] },
        { id: "c2", rowRatio: 0.5, cells: [{ id: "g2", selectedViewId: "p2", views: [{ id: "p2", session: null, content: { kind: "sessions", showStopped: false }, wrap: null }] }] },
      ],
      colRatios: [0.58, 0.42],
      activeCellId: "g1",
    },
    sections: { assistant: 0, workitems: 1, memos: 0, schedules: 0, repos: 1, files: 0 },
    storage: { [`af.mirror-files-open.${NAME_OF.claude}`]: "1" },
    ready: `!!document.querySelector(".wi-row")`,
  };
}

export async function script(c) {
  const T = text(c.locale);
  const [claude, codex] = demoIssues(c.locale);
  const { find } = c;

  await c.caption(1, T.cap[0]);
  await c.record();

  // ---- 1. two issues, two agents, two worktrees ----
  await c.sleep(1800);
  await c.click(find(".wi-row", `#${claude.number}`));
  await c.sleep(1000);
  await c.click(find(".ui-modal .ui-btn-default"));
  await c.sleep(900);
  await c.hover(find(".ui-modal .launch-sec-head", "worktree"));
  await c.sleep(900);
  await c.click(find(".ui-modal .ui-btn-primary"));
  await c.waitFor(`!!${find(".ovw-card", `#${claude.number}`)}`);
  await c.sleep(1800);

  await c.click(find(".wi-row", `#${codex.number}`));
  await c.sleep(800);
  await c.click(find(".ui-modal .ui-btn-default"));
  await c.sleep(800);
  await c.click(find(".ui-modal .seg-btn.kind-codex"));
  // codex's model list is fetched on selection; start only once the picker has it.
  await c.waitFor(`!document.querySelector(".model-picker-loading")`);
  await c.sleep(700);
  await c.click(find(".ui-modal .ui-btn-primary"));
  await c.waitFor(`!!${find(".ovw-card", `#${codex.number}`)}`);
  await c.sleep(2200);

  // ---- 2. the laptop closes ----
  await c.ev(`__demo.hideCursor()`);
  await c.caption(2, T.cap[1]);
  await c.ev(`__demo.veil(true, "09:10 → 11:40", ${JSON.stringify(T.away)})`);
  await c.phase("away");
  await c.sleep(3000);

  // ---- 3. the permission request, answered on the phone ----
  await c.caption(3, T.cap[2]);
  await c.ev(`__demo.veil(true)`);
  await c.ev(`__demo.phone(true, ${JSON.stringify({ ...T.phone, time: "11:40" })})`);
  await c.sleep(2200);
  await c.ev(`__demo.setFinger(true); __demo.move(${c.W / 2 + 120}, ${c.H - 60}, 0)`);
  const allow = await c.ev(`__demo.phoneAllowRect()`);
  await c.ev(`__demo.move(${Math.round(allow.x)}, ${Math.round(allow.y)}, 800)`);
  await c.ev(`__demo.ripple()`);
  await c.ev(`__demo.phonePress()`);
  // While the phone is up, the fleet moves on to "back at a desk": the Claude session finished,
  // Codex has a question. Its chat is opened from the repo tree behind the veil, so the Console
  // comes back showing the finished session.
  await c.phase("back");
  await c.sleep(1800);
  await c.ev(`(${find(".sess-row .sess-btn", `#${claude.number}`)})?.click()`);
  await c.waitFor(`!/working/.test((${find(".ovw-card", `#${claude.number}`)})?.className || "working") && !!document.querySelector(".mfl-row")`);
  await c.ev(`__demo.hideCursor(); __demo.setFinger(false)`);
  await c.ev(`__demo.phone(false)`);
  await c.sleep(700);

  // ---- 4. back at a desk ----
  await c.ev(`__demo.veil(false)`);
  await c.caption(4, T.cap[3]);
  await c.sleep(1600);
  await c.hover(find(".ovw-card", `#${codex.number}`), 800);
  await c.sleep(1300);
  await c.hover(find(".ovw-card", `#${claude.number}`), 600);
  await c.sleep(1000);
  await c.click(find("button", `^${T.splitDown}$`), 800);
  await c.sleep(700);
  await c.click(`${find(".mfl-name", "^validate\\.ts$")}?.closest(".mfl-row")`, 800);
  await c.waitFor(`[...document.querySelectorAll(".scmview .scm-scroll")].some((e) => /cart_total_zero/.test(e.textContent))`);
  await c.sleep(3800);
}
