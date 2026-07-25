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

export function graph(locale) {
  const ja = locale === "ja";
  const c = (sha, parents, author, min, subject, refs = []) => ({
    sha: sha.padEnd(40, "0"),
    short: sha.slice(0, 7),
    parents: parents.map((p) => p.padEnd(40, "0")),
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

export function show(locale, sha) {
  return {
    sha,
    subject: locale === "ja" ? "カートバッジの件数を購読で更新" : "Update the cart badge from a subscription",
    author: "Rin Takada",
    email: "rin@example.com",
    date: ago(35),
    body: "",
    files: [{ path: "src/cart/Badge.tsx", added: 14, deleted: 3, status: "M" }],
    diff: "",
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
