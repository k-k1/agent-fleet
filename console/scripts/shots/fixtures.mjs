// Demo fixtures for the README screenshot harness.
//
// Everything here is FICTIONAL — invented repos, sessions, commits and a scripted
// conversation. Nothing is read from a real fleet, so the published screenshots can
// never leak a tenant name, an address, a private repo or an agent's usage numbers.
//
// The shapes are the real wire contracts:
//   - Session         → console/src/types/session.ts
//   - Repo            → console/src/features/repos/store.ts
//   - Turn / Part     → workspace/agent/internal/transcript/transcript.go
//   - GraphCommit     → console/src/lib/gitgraph.ts
// Keep them in sync when those contracts move; a drifted fixture renders an empty
// pane rather than failing loudly.

const L = (locale, ja, en) => (locale === "ja" ? ja : en);

// Clock the fixtures hang off. Real "now", so relative labels ("22分前", "2 か月前")
// read correctly against the machine capturing the shots — a frozen clock would make
// every commit look months old.
export const NOW = new Date();
const ago = (min) => new Date(NOW.getTime() - min * 60_000).toISOString();

export const USER = { email: "demo@example.com", user: "demo" };

export function repos(locale) {
  return [
    { name: "webshop", path: "/home/dev/repos/webshop", branch: "main", dirty: true, ahead: 2, provider: "github", remote: "github.com" },
    {
      name: "webshop@checkout-validation",
      path: "/home/dev/repos/webshop@checkout-validation",
      branch: "feat/checkout-validation",
      dirty: true,
      provider: "github",
      remote: "github.com",
      worktree: true,
      parent: "webshop",
      createdAt: ago(180),
      integration: { targetBranch: "main", targetUnique: 2, worktreeUnique: 3, relation: "diverged" },
    },
    { name: "payments-api", path: "/home/dev/repos/payments-api", branch: "main", provider: "bitbucket", remote: "bitbucket.org" },
    { name: "platform-infra", path: "/home/dev/repos/platform-infra", branch: "main", dirty: false, behind: 1, provider: "github", remote: "github.com" },
    { name: "design-assets", path: "/home/dev/repos/design-assets", vcs: "svn", revision: "1482", url: "https://svn.example.com/design/trunk" },
  ];
}

export function sessions(locale) {
  return [
    {
      name: "sk4rq2f",
      kind: "claude",
      driver: "tui",
      title: L(locale, "チェックアウトの入力検証", "Checkout input validation"),
      repo: "webshop@checkout-validation",
      dir: "~/repos/webshop@checkout-validation",
      path: "/home/dev/repos/webshop@checkout-validation",
      state: "working",
      alive: true,
      model: "claude-opus-5",
      branch: "feat/checkout-validation",
      worktree: true,
      createdAt: ago(46),
    },
    {
      name: "sc9lm3d",
      kind: "codex",
      driver: "managed",
      title: L(locale, "返金 API の契約整理", "Refund API contract"),
      repo: "payments-api",
      dir: "~/repos/payments-api",
      path: "/home/dev/repos/payments-api",
      state: "question",
      alive: true,
      model: "gpt-5.6-luna",
      branch: "main",
      createdAt: ago(21),
    },
    {
      name: "scu7bx1",
      kind: "cursor",
      driver: "managed",
      title: L(locale, "カート UI の余白調整", "Cart UI spacing pass"),
      repo: "webshop",
      dir: "~/repos/webshop",
      path: "/home/dev/repos/webshop",
      state: "idle",
      alive: true,
      model: "composer-1",
      branch: "main",
      createdAt: ago(12),
    },
    {
      name: "sq3hn7v",
      kind: "copilot",
      driver: "managed",
      title: L(locale, "Terraform の lint 追従", "Terraform lint follow-up"),
      repo: "platform-infra",
      dir: "~/repos/platform-infra",
      path: "/home/dev/repos/platform-infra",
      state: "working",
      alive: true,
      branch: "main",
      createdAt: ago(8),
    },
    {
      name: "sh2vt8p",
      kind: "shell",
      title: L(locale, "ビルド確認", "Build check"),
      repo: "webshop",
      dir: "~/repos/webshop",
      path: "/home/dev/repos/webshop",
      state: "",
      alive: true,
      createdAt: ago(33),
    },
    {
      name: "so5nw6k",
      kind: "opencode",
      driver: "managed",
      title: L(locale, "在庫同期バッチの調査", "Stock sync batch triage"),
      repo: "payments-api",
      dir: "~/repos/payments-api",
      path: "/home/dev/repos/payments-api",
      state: "",
      alive: false,
      resumable: true,
      branch: "main",
      createdAt: ago(320),
    },
  ];
}

// ---- the scripted mirror conversation (claude session sk4rq2f) --------------------

const DIFF_OLD = `export function validateCart(cart: Cart): Result {
  if (cart.items.length === 0) return err("cart_empty");
  return ok();
}`;

const DIFF_NEW = `export function validateCart(cart: Cart): Result {
  if (cart.items.length === 0) return err("cart_empty");
  const total = cart.items.reduce((n, i) => n + i.price * i.qty, 0);
  if (total <= 0) return err("cart_total_zero");
  return ok();
}`;

export function turns(locale) {
  const ja = locale === "ja";
  const t = [
    {
      role: "user",
      idx: 12,
      ts: ago(46),
      text: ja
        ? "チェックアウトで合計金額が 0 円のときに決済に進めてしまう。原因を調べて直して。"
        : "Checkout lets an order with a zero total through to payment. Find the cause and fix it.",
      parts: [
        {
          kind: "text",
          text: ja
            ? "チェックアウトで合計金額が 0 円のときに決済に進めてしまう。原因を調べて直して。"
            : "Checkout lets an order with a zero total through to payment. Find the cause and fix it.",
        },
      ],
    },
    {
      role: "assistant",
      idx: 13,
      ts: ago(45),
      model: "claude-opus-5",
      inTok: 18420,
      outTok: 640,
      cacheRead: 96210,
      text: "",
      parts: [
        {
          kind: "text",
          text: ja
            ? "カート検証の入口から見ます。"
            : "Starting from the cart-validation entry point.",
        },
        { kind: "tool", tool: "Grep", info: "validateCart  ·  src/", output: "src/checkout/validate.ts:4\nsrc/checkout/index.ts:22\nsrc/api/orders.ts:58" },
        { kind: "tool", tool: "Read", info: "src/checkout/validate.ts", output: ja ? "42 行を読み込みました" : "read 42 lines" },
        {
          kind: "text",
          text: ja
            ? "`validateCart` は**空カート**だけを弾いていて、合計金額を見ていません。100% 割引クーポンが乗ると 0 円のまま通過します。\n\n合計のチェックを足します。"
            : "`validateCart` only rejects an **empty** cart — it never looks at the total. A zero-quantity line or a 100%-off coupon slips through with a total of 0.\n\nAdding a total check.",
        },
        {
          kind: "tool",
          tool: "Edit",
          info: "src/checkout/validate.ts",
          file: "webshop@checkout-validation/src/checkout/validate.ts",
          edits: [{ old: DIFF_OLD, new: DIFF_NEW }],
        },
        { kind: "tool", tool: "Bash", info: "npm test -- checkout", output: "PASS  src/checkout/validate.test.ts\n  ✓ rejects an empty cart (3 ms)\n  ✓ rejects a zero total (1 ms)\n\nTests: 2 passed, 2 total" },
        {
          kind: "text",
          text: ja
            ? "`validate.ts` に合計金額のガードを追加し、回帰テストを 1 本足しました。テストは緑です。"
            : "Added a total-amount guard in `validate.ts` plus one regression test. Tests are green.",
        },
      ],
    },
    {
      role: "user",
      idx: 21,
      ts: ago(6),
      text: ja ? "エラーはユーザーにどう見せる？" : "How should the error surface to the user?",
      parts: [{ kind: "text", text: ja ? "エラーはユーザーにどう見せる？" : "How should the error surface to the user?" }],
    },
  ];
  return t;
}

// The question the agent is asking RIGHT NOW. Served as `pendingQuestions` (+ the prose
// that preceded it as `pendingText`), which is what makes the mirror render the live,
// answerable card — a question inside a past turn is always shown resolved.
export function pendingQuestions(locale) {
  const ja = locale === "ja";
  return [
    {
      header: ja ? "表示方法" : "Surface",
      question: ja ? "合計 0 円のエラーはどこに出しますか？" : "Where should the zero-total error appear?",
      options: [
        {
          label: ja ? "カート画面にインライン表示" : "Inline on the cart page",
          description: ja
            ? "決済へ進む前に気づける。既存のバリデーション表示に相乗りできる。"
            : "The shopper sees it before payment; reuses the existing validation banner.",
        },
        {
          label: ja ? "決済ボタンを無効化" : "Disable the pay button",
          description: ja
            ? "誤操作は防げるが理由が伝わらない。理由の tooltip が別途必要。"
            : "Prevents the mistake but explains nothing — needs a separate tooltip.",
        },
        {
          label: ja ? "サーバ側 400 のみ" : "Server-side 400 only",
          description: ja
            ? "API 経由の注文も守れるが、UI の体験は変わらない。"
            : "Also covers API orders, but the UI experience is unchanged.",
        },
      ],
    },
  ];
}

export function pendingText(locale) {
  return locale === "ja"
    ? "見せ方で影響範囲が変わるので、方針を決めさせてください。"
    : "The blast radius depends on where it surfaces — let's pick the approach first.";
}

export function tasks(locale) {
  const ja = locale === "ja";
  return [
    { id: "t1", subject: ja ? "合計金額のガードを追加" : "Add the total-amount guard", status: "completed" },
    { id: "t2", subject: ja ? "回帰テストを書く" : "Write a regression test", status: "completed" },
    {
      id: "t3",
      subject: ja ? "エラー表示を UI に配線" : "Wire the error into the UI",
      activeForm: ja ? "エラー表示を UI に配線中" : "Wiring the error into the UI",
      status: "in_progress",
    },
  ];
}

// ---- commit graph (webshop) ------------------------------------------------------

// A short sha padded to a plausible 40-hex object id (zero-padding would show up in
// the commit-detail header, which prints the first 10 characters).
export const fullSha = (short) => (short + "9f3ac1d7e05b6482ca71d0e9b3f45a8c76d21e0f").slice(0, 40);

/** The commit the SCM scene opens next to the graph. */
export const DETAIL_SHA = fullSha("5c1b9f4");

export function graph(locale) {
  const ja = locale === "ja";
  const c = (sha, parents, author, min, subject, refs = []) => ({
    sha: fullSha(sha),
    short: sha.slice(0, 7),
    parents: parents.map(fullSha),
    author,
    date: ago(min),
    subject,
    refs,
    inBranch: true,
  });
  return {
    current: "main",
    // Two merged side branches so the lane layout actually shows a graph, not a line.
    commits: [
      c("a91f4c2", ["7d3e08b", "5c1b9f4"], "Rin Takada", 22, "Merge branch 'feat/cart-badge'", [
        { name: "main", type: "head" },
        { name: "origin/main", type: "remote" },
      ]),
      c("5c1b9f4", ["4b2ce71"], "Rin Takada", 35, ja ? "カートバッジの件数を購読で更新" : "Update the cart badge from a subscription"),
      c("4b2ce71", ["2f60ad1"], "Rin Takada", 74, ja ? "カート件数のフックを切り出し" : "Extract the cart-count hook"),
      c("7d3e08b", ["2f60ad1"], "Kai Morgan", 96, ja ? "決済フォームのラベルを i18n 化" : "Localize the payment form labels"),
      c("2f60ad1", ["9b47e5c"], "Kai Morgan", 240, ja ? "在庫 API のリトライを指数バックオフに" : "Back off exponentially on stock API retries", [{ name: "v2.4.0", type: "tag" }]),
      c("9b47e5c", ["c30d81a", "61ad0f8"], "Rin Takada", 1450, "Merge branch 'fix/shipping-tax'"),
      c("61ad0f8", ["c30d81a"], "Sora Nishida", 1620, ja ? "配送料の計算を税抜きベースに統一" : "Compute shipping on the pre-tax subtotal"),
      c("c30d81a", ["e58a2b7"], "Sora Nishida", 2880, ja ? "注文確定のメール送信を非同期化" : "Send the order confirmation mail asynchronously"),
      c("e58a2b7", ["7c1de40"], "Kai Morgan", 4310, ja ? "商品検索のインデックスを追加" : "Add the product search index"),
      c("7c1de40", ["44b91f0"], "Kai Morgan", 5900, ja ? "カート永続化を localStorage から API へ" : "Move cart persistence from localStorage to the API"),
      c("44b91f0", [], "Sora Nishida", 7200, ja ? "初期コミット" : "Initial commit"),
    ],
  };
}

// A canned PTY screen so a terminal pane renders like a real one (the harness has no
// tmux). Plain ANSI; xterm paints it exactly as a live attach would.
export function ptyScreen(locale) {
  const ja = locale === "ja";
  const g = "\x1b[32m", d = "\x1b[90m", b = "\x1b[1m", y = "\x1b[33m", r = "\x1b[0m", c = "\x1b[36m";
  return [
    `${d}dev@webshop${r}:${c}~/repos/webshop${r}$ npm run build`,
    "",
    `${d}> webshop@2.4.0 build${r}`,
    `${d}> vite build${r}`,
    "",
    `vite v6.0.7 ${g}building for production...${r}`,
    `${g}✓${r} 412 modules transformed.`,
    `dist/index.html                   ${y}0.62 kB${r} │ gzip:  0.36 kB`,
    `dist/assets/index-C8xq21Za.css   ${y}18.44 kB${r} │ gzip:  4.10 kB`,
    `dist/assets/index-DkP9v3Rt.js   ${y}214.87 kB${r} │ gzip: 68.92 kB`,
    `${g}✓ built in 3.41s${r}`,
    "",
    `${d}dev@webshop${r}:${c}~/repos/webshop${r}$ git status --short`,
    ` ${y}M${r} src/checkout/validate.ts`,
    ` ${y}M${r} src/checkout/validate.test.ts`,
    ` ${y}M${r} src/cart/CartSummary.tsx`,
    `${y}??${r} docs/checkout-validation.md`,
    "",
    `${d}dev@webshop${r}:${c}~/repos/webshop${r}$ npm test -- checkout`,
    "",
    `${g}PASS${r} src/checkout/validate.test.ts`,
    `  ${g}✓${r} ${ja ? "空カートを弾く" : "rejects an empty cart"} ${d}(3 ms)${r}`,
    `  ${g}✓${r} ${ja ? "合計 0 円を弾く" : "rejects a zero total"} ${d}(1 ms)${r}`,
    "",
    `Test Suites: ${g}1 passed${r}, 1 total`,
    `Tests:       ${g}2 passed${r}, 2 total`,
    `Time:        1.284 s`,
    "",
    `${d}dev@webshop${r}:${c}~/repos/webshop${r}$ ${b}${r}\x1b[?25h`,
  ].join("\r\n");
}

// ---- the remaining endpoints the shell polls ---------------------------------------

export function messages(locale, session) {
  if (session !== "sk4rq2f") return { name: session, messages: [], cursor: 0, status: "", alive: true, reset: true };
  return {
    name: session,
    messages: turns(locale),
    cursor: 23,
    status: "question",
    alive: true,
    reset: true,
    firstLine: 0,
    hasMore: false,
    mode: "Default",
    tasks: tasks(locale),
    pendingQuestions: pendingQuestions(locale),
    pendingText: pendingText(locale),
    jsonlLines: 23,
    jsonlMtime: ago(5),
  };
}

export function scmStatus(locale, repo) {
  const r = repos(locale).find((x) => x.name === repo);
  return { branch: r?.branch || "main", ahead: r?.ahead || 0, behind: r?.behind || 0 };
}

export function changes(locale, repo) {
  // index/worktree carry the two-column git status codes the view renders.
  return {
    changes: [
      { path: "src/checkout/validate.ts", index: "M", worktree: "" },
      { path: "src/checkout/validate.test.ts", index: "M", worktree: "" },
      { path: "src/checkout/messages.ts", index: "", worktree: "M" },
      { path: "src/cart/CartSummary.tsx", index: "", worktree: "M" },
      { path: "docs/checkout-validation.md", index: "", worktree: "?", untracked: true },
    ],
  };
}

// The left rail's 変更 view (FilesChanges) asks ONE cross-repo endpoint instead of
// per-repo status, so its entries carry the working copy and a home-relative path.
export function fsChanges(locale) {
  const of = (repo) =>
    changes(locale, repo).changes.map((c) => ({ ...c, repo, path: `repos/${repo}/${c.path}` }));
  return {
    changes: [
      ...of("webshop@checkout-validation"),
      { path: "repos/payments-api/src/refund/handler.go", repo: "payments-api", index: "", worktree: "M" },
      { path: "repos/payments-api/src/refund/legacy.go", repo: "payments-api", index: "D", worktree: "" },
    ],
  };
}

// One commit's detail, as the commit pane renders it (GitDiff.CommitData): the header
// fields plus a real unified diff, which the view splits into per-file foldable blocks.
const SHOW_DIFF = `diff --git a/src/cart/CartBadge.tsx b/src/cart/CartBadge.tsx
index 2f1a9c4..8b7e0d3 100644
--- a/src/cart/CartBadge.tsx
+++ b/src/cart/CartBadge.tsx
@@ -1,20 +1,22 @@
-import { useEffect, useState } from "react";
-import { fetchCart } from "../api/cart";
+import { useCartCount } from "./useCartCount";

 export function CartBadge() {
-  const [count, setCount] = useState(0);
-
-  // Polled every 5s: drifts from the real cart between
-  // ticks, and keeps firing on an unwatched tab.
-  useEffect(() => {
-    const t = setInterval(() => {
-      void fetchCart().then((c) => setCount(c.items.length));
-    }, 5000);
-    return () => clearInterval(t);
-  }, []);
+  // Driven by the cart store's subscription, so the badge
+  // updates on the same tick the cart itself does.
+  const count = useCartCount();

   if (count === 0) return null;
   return (
-    <span className="cart-badge">{count}</span>
+    <span className="cart-badge" aria-label={\`\${count} items in cart\`}>
+      {count > 99 ? "99+" : count}
+    </span>
   );
 }
diff --git a/src/cart/CartBadge.test.tsx b/src/cart/CartBadge.test.tsx
index 71c0ab2..d95f118 100644
--- a/src/cart/CartBadge.test.tsx
+++ b/src/cart/CartBadge.test.tsx
@@ -12,4 +12,10 @@ it("hides itself on an empty cart", () => {
   render(<CartBadge />);
   expect(screen.queryByLabelText(/items in cart/)).toBeNull();
 });
+
+it("caps the label at 99+", () => {
+  cart.set(Array.from({ length: 120 }, item));
+  render(<CartBadge />);
+  expect(screen.getByLabelText("120 items in cart").textContent).toBe("99+");
+});
`;

export function show(locale, sha) {
  return {
    sha,
    short: (sha || "").slice(0, 7),
    subject: locale === "ja" ? "カートバッジの件数を購読で更新" : "Update the cart badge from a subscription",
    body:
      locale === "ja"
        ? "5 秒ポーリングをストアの購読に置き換え。バックグラウンドタブでの無駄な取得もなくなる。"
        : "Replace the 5s poll with a store subscription; also stops the pointless fetches on a background tab.",
    author: "Rin Takada",
    email: "rin@example.com",
    date: ago(35),
    diff: SHOW_DIFF,
  };
}

export function diff(locale, p) {
  return { path: p, diff: "" };
}

export function fsList(locale, p) {
  return { path: p, entries: [] };
}

export function fsFile(locale, p) {
  return { path: p, content: "" };
}

export function conversations(locale) {
  const ja = locale === "ja";
  const ms = (min) => NOW.getTime() - min * 60_000;
  return [
    {
      id: "c-ops-1",
      slug: "a3k9m2t",
      agent: "claude",
      assistant_id: "operator",
      title: ja ? "フリート運用の相談" : "Fleet operations",
      model: "claude-sonnet-5",
      created_at: ms(600),
      updated_at: ms(14),
      message_count: 12,
    },
    {
      id: "c-rel-1",
      slug: "a7f2q5x",
      agent: "codex",
      assistant_id: "general",
      title: ja ? "リリースノートの下書き" : "Release notes draft",
      model: "gpt-5.6-luna",
      created_at: ms(2600),
      updated_at: ms(210),
      message_count: 6,
    },
  ];
}

export function assistants(locale) {
  const ja = locale === "ja";
  return [
    {
      id: "operator",
      name: ja ? "フリートオペレーター" : "Fleet operator",
      icon: "rocket",
      builtin: true,
      agent: "claude",
      model: "claude-sonnet-5",
      description: ja ? "セッションの起動・操作・引き継ぎを代行します。" : "Starts, steers and hands over sessions for you.",
      tools: {},
    },
    {
      id: "general",
      name: ja ? "アシスタント" : "Assistant",
      icon: "comment-discussion",
      builtin: true,
      agent: "claude",
      model: "claude-sonnet-5",
      tools: {},
    },
  ];
}

export function memos(locale) {
  const ja = locale === "ja";
  const m = (id, repo, category, body, pos) => ({
    id,
    repo,
    category,
    kind: "text",
    body,
    refPath: "",
    position: pos,
    createdAt: ago(pos * 30 + 40),
    sentAt: "",
  });
  return [
    m("m1", "webshop", ja ? "決済" : "Checkout", ja ? "クーポン併用時の税計算を確認する" : "Check tax when coupons stack", 1),
    m("m2", "webshop", ja ? "決済" : "Checkout", ja ? "0 円注文のログを Sentry に出す" : "Log zero-total orders to Sentry", 2),
    m("m3", "payments-api", "", ja ? "返金の冪等キーを再設計" : "Redesign the refund idempotency key", 3),
  ];
}

export function memoCategories(locale) {
  const ja = locale === "ja";
  return [
    { id: "k1", repo: "webshop", name: ja ? "決済" : "Checkout", position: 1 },
    { id: "k2", repo: "payments-api", name: ja ? "返金" : "Refunds", position: 2 },
  ];
}

export function schedules(locale) {
  const ja = locale === "ja";
  const at = (min) => new Date(NOW.getTime() + min * 60_000).toISOString();
  return [
    {
      id: "sch-1",
      spec_kind: "cron",
      spec: "0 9 * * 1-5",
      spec_label: ja ? "平日 9:00" : "Weekdays at 9:00",
      tz: "Asia/Tokyo",
      session_mode: "new",
      agent_kind: "claude",
      repo: "webshop",
      prompt: ja ? "昨日の変更をレビューして要点を報告して" : "Review yesterday's changes and report the highlights",
      enabled: true,
      next_run: at(1399),
      last_run: ago(41),
      last_status: "fired",
    },
    {
      id: "sch-2",
      spec_kind: "interval",
      spec: "21600",
      spec_label: ja ? "6 時間ごと" : "Every 6 hours",
      tz: "Asia/Tokyo",
      session_mode: "reuse",
      agent_kind: "codex",
      repo: "payments-api",
      prompt: ja ? "依存パッケージの脆弱性を確認" : "Check dependency advisories",
      enabled: true,
      next_run: at(154),
      last_run: ago(206),
      last_status: "fired",
    },
  ];
}

export function usage(locale) {
  return [];
}

export function stats() {
  return { cpu: 0.18, memUsed: 2147483648, memLimit: 10737418240 };
}

// ---- usage ledger (GET /api/usage/series) ------------------------------------------
// The Console asks for three series per view (the selected axis over time, feature ×
// model, kind × model), so this answers any (by, split, bucket) combination from one
// weight table. Wire shape: workspace/agent/usage_series.go ↔ features/usage/api.ts.

// Relative weight of each value on each axis. Numbers are invented but ordered the way
// a real week looks: session work dwarfs everything, claude/opus carry most of it.
const USAGE_WEIGHTS = {
  feature: {
    session: 0.72,
    "assistant.chat": 0.13,
    compact: 0.06,
    "suggest.session": 0.04,
    "title.session": 0.03,
    "assistant.autoturn": 0.02,
  },
  kind: { claude: 0.61, codex: 0.22, cursor: 0.1, opencode: 0.07 },
  model: {
    "claude-opus-5": 0.44,
    "claude-sonnet-5": 0.19,
    "gpt-5.6-luna": 0.22,
    "composer-1": 0.1,
    "opencode/nemotron-3-ultra-free": 0.05,
  },
  origin: { user: 0.78, schedule: 0.13, operator: 0.07, handoff: 0.02 },
  trigger: { user: 0.74, schedule: 0.13, auto: 0.08, operator: 0.05 },
};

// Which models each feature / agent actually used, so the "× model" matrices are not a
// dense grid of every combination (a real one is sparse).
const USAGE_PAIRS = {
  feature: {
    session: ["claude-opus-5", "gpt-5.6-luna", "composer-1", "opencode/nemotron-3-ultra-free"],
    "assistant.chat": ["claude-sonnet-5", "gpt-5.6-luna"],
    compact: ["claude-sonnet-5"],
    "suggest.session": ["claude-sonnet-5"],
    "title.session": ["claude-sonnet-5"],
    "assistant.autoturn": ["claude-sonnet-5"],
  },
  kind: {
    claude: ["claude-opus-5", "claude-sonnet-5"],
    codex: ["gpt-5.6-luna"],
    cursor: ["composer-1"],
    opencode: ["opencode/nemotron-3-ultra-free"],
  },
};

const WEEK_SPEND = 41_800_000; // total spend across the default 7-day window

// A deterministic 0.6–1.4 shape per bucket so the stacked chart has a weekday rhythm
// instead of flat bars (no Math.random — a re-run must not reshuffle the picture).
const bucketShape = (i, n) => 0.62 + 0.78 * Math.abs(Math.sin((i + 1) * 1.7)) * (i === n - 1 ? 0.55 : 1);

function agg(spend, calls) {
  const inTok = Math.round(spend * 0.34);
  const ccreate = Math.round(spend * 0.11);
  return {
    spend: Math.round(spend),
    in: inTok,
    out: Math.round(spend - inTok - ccreate),
    cread: Math.round(spend * 4.6), // cache reads dominate and are excluded from spend
    ccreate,
    calls: Math.max(1, Math.round(calls)),
    cost_usd: Math.round(spend * 0.0000042 * 10000) / 10000,
  };
}

const addAgg = (a, b) => ({
  spend: a.spend + b.spend,
  in: a.in + b.in,
  out: a.out + b.out,
  cread: a.cread + b.cread,
  ccreate: a.ccreate + b.ccreate,
  calls: a.calls + b.calls,
  cost_usd: Math.round((a.cost_usd + b.cost_usd) * 10000) / 10000,
});

const EMPTY_AGG = { spend: 0, in: 0, out: 0, cread: 0, ccreate: 0, calls: 0, cost_usd: 0 };

export function usageSeries(locale, q) {
  const by = q.get("by") || "feature";
  const split = q.get("split") || "";
  const bucket = q.get("bucket") === "hour" ? "hour" : "day";
  const to = q.get("to") ? new Date(q.get("to")) : NOW;
  const from = q.get("from") ? new Date(q.get("from")) : new Date(to.getTime() - 7 * 86400_000);
  const stepMs = bucket === "hour" ? 3600_000 : 86400_000;
  const n = Math.max(1, Math.min(48, Math.round((to - from) / stepMs)));
  const weights = USAGE_WEIGHTS[by] || USAGE_WEIGHTS.feature;
  // Scale to the requested window so a 24h view isn't shown a week's worth of tokens.
  const windowSpend = (WEEK_SPEND * (to - from)) / (7 * 86400_000);

  const shapes = Array.from({ length: n }, (_, i) => bucketShape(i, n));
  const shapeSum = shapes.reduce((a, b) => a + b, 0);

  // Bucket starts run backwards from the newest one, aligned to a LOCAL day/hour
  // boundary: the chart labels buckets in local time, so aligning to UTC would leave
  // the newest bucket short of "to" and the chart would end with dead space.
  const newest = new Date(to.getTime());
  if (bucket === "day") newest.setHours(0, 0, 0, 0);
  else newest.setMinutes(0, 0, 0);

  const buckets = [];
  let totals = { ...EMPTY_AGG };
  for (let i = 0; i < n; i++) {
    const t = new Date(newest.getTime() - (n - 1 - i) * stepMs);
    const bSpend = (windowSpend * shapes[i]) / shapeSum;
    const series = {};
    for (const [key, w] of Object.entries(weights)) {
      const a = agg(bSpend * w, (bSpend * w) / 26_000);
      series[key] = a;
      totals = addAgg(totals, a);
    }
    buckets.push({ t: t.toISOString(), series });
  }

  const resp = {
    from: from.toISOString(),
    to: to.toISOString(),
    bucket,
    by,
    buckets,
    totals,
    coverage: {
      claude: { tokens: "exact", model: "reported" },
      codex: { tokens: "exact", model: "reported" },
      cursor: { tokens: "none", model: "requested" },
      opencode: { tokens: "partial", model: "reported" },
    },
    unmeasured_calls: 37,
  };

  if (split) {
    resp.split = split;
    resp.matrix = {};
    const pairs = USAGE_PAIRS[by];
    for (const [key, w] of Object.entries(weights)) {
      const models = (pairs && pairs[key]) || Object.keys(USAGE_WEIGHTS.model);
      const rowSpend = windowSpend * w;
      // Split the row across its models by their own weights, renormalized to the row.
      const norm = models.reduce((s, m) => s + (USAGE_WEIGHTS.model[m] || 0.05), 0);
      resp.matrix[key] = {};
      for (const m of models) {
        const share = (USAGE_WEIGHTS.model[m] || 0.05) / norm;
        resp.matrix[key][m] = agg(rowSpend * share, (rowSpend * share) / 26_000);
      }
    }
  }
  return resp;
}

// Cleanup survey (GET /sessions/cleanup) — a fleet that has drifted: several merged
// worktrees of one repo, a live one that must be kept, leftover merged branches, and
// stopped/archived sessions. Reasons travel as `reason_key` (ADR 0033), so the modal
// renders them from the Console catalog; `reason` is the ja fallback the Agent sends.
export function cleanupCandidates(locale) {
  const wt = (seg, branch, safety, key, extra = {}) => ({
    type: "worktree",
    action: safety === "keep" ? undefined : "delete_worktree",
    id: `webshop@${seg}`,
    repo: `webshop@${seg}`,
    path: `/home/dev/repos/webshop@${seg}`,
    branch,
    safety,
    reason_key: key,
    reason: "",
    ...extra,
  });
  return [
    wt("checkout-validation", "feat/checkout-validation", "keep", "clean.reason.wt_live"),
    wt("cart-badge", "temp/cart-badge", "safe", "clean.reason.wt_merged", { relation: "contained" }),
    wt("price-rounding", "temp/price-rounding", "safe", "clean.reason.wt_merged", { relation: "contained" }),
    wt("search-facets", "temp/search-facets", "review", "clean.reason.wt_unmerged", { relation: "unmerged" }),
    wt("i18n-sweep", "temp/i18n-sweep", "keep", "clean.reason.wt_dirty", { dirty: true, ahead: 1 }),
    {
      type: "branch", action: "delete_branch", id: "webshop", repo: "webshop",
      branch: "temp/cart-badge", safety: "safe", reason_key: "clean.reason.branch_merged", reason: "",
    },
    {
      type: "branch", action: "delete_branch", id: "webshop", repo: "webshop",
      branch: "temp/legacy-checkout", safety: "safe", reason_key: "clean.reason.branch_merged", reason: "",
    },
    {
      type: "session", action: "archive_session", id: "sk9wq1a",
      display: L(locale, "カート表示のバッジ調整", "Cart badge tweak"), kind: "claude",
      repo: "webshop@cart-badge", path: "/home/dev/repos/webshop@cart-badge",
      safety: "review", reason_key: "clean.reason.stopped", reason: "",
    },
    {
      type: "session", action: "delete_session", id: "sm2vt7c",
      display: L(locale, "検索ファセットの設計メモ", "Search facet design notes"), kind: "codex",
      repo: "webshop@search-facets", path: "/home/dev/repos/webshop@search-facets",
      safety: "review", reason_key: "clean.reason.archived", reason: "",
    },
    {
      type: "session", action: "archive_session", id: "sp3hd8k",
      display: L(locale, "返金 API の契約整理", "Refund API contract"), kind: "codex",
      repo: "payments-api", path: "/home/dev/repos/payments-api",
      safety: "review", reason_key: "clean.reason.stopped", reason: "",
    },
    {
      type: "branch", action: "delete_branch", id: "payments-api", repo: "payments-api",
      branch: "temp/refund-contract", safety: "safe", reason_key: "clean.reason.branch_merged", reason: "",
    },
  ];
}

export function cleanupArchives(locale) {
  return [
    {
      id: "20260726-091500-slot3", at: "2026-07-26T09:15:00Z", reason: "delete_session",
      sessions: [{ name: "sq8kd2m", display: L(locale, "旧チェックアウト調査", "Old checkout probe") }],
    },
    {
      id: "20260724-183000-slot1", at: "2026-07-24T18:30:00Z", reason: "delete_branch",
      branches: [{ repo: "webshop", name: "temp/old-cart" }],
    },
  ];
}
