// Scenario "orchestrate" — one session directs three of other kinds
// (docs/img/demo-orchestrate-<locale>.webp; the site's features page).
//
// A Claude session splits a design review across a Codex, an Antigravity and a Muse Code session
// (three create_session calls), each in its own worktree; they line up under the parent in the
// family colour in the overview and the rail; their three reports arrive in the parent's chat as
// peer answers and the parent pulls them together; the fleet graph draws four lanes. Same wire
// shapes as review.mjs (ADR 0073): children are origin "session" + originSession, reports are user
// turns with source "peer" and the `[agent-fleet:peer from=… intent=answer …]` envelope. Each
// report is answered by a short parent turn before the next arrives: consecutive user turns merge
// in the mirror (console/src/features/mirror/transcript/model.ts), and a merged turn would bury the
// second envelope mid-text — which is also what the real parent does, one turn per message.
//
// The story's clock is compressed over the last two and a half hours, as in review.mjs, so the
// graph zoomed to three hours has room for it.
import { L, assistant, messagesBody, phases, repoRow, router, tool, txt, user } from "./kit.mjs";

const PHASES = ["working", "spawned", "r1", "r2", "r3"];
const PARENT = "sk7dsn2";

// In the order they report back.
function children(locale) {
  return [
    {
      name: "sm8ui4n",
      kind: "muse",
      model: "",
      repo: "webshop@review-ui",
      branch: "review/ui",
      title: L(locale, "レビュー: 画面への影響", "Review: UI impact"),
      area: L(locale, "Muse Code — 画面への影響", "Muse Code — UI impact"),
      report: L(
        locale,
        "画面: 旧エンドポイントを読むのは注文履歴ページだけです。コンポーネント 2 つ、リスクは低め。",
        "UI: only the order history page reads the old endpoint — two components, low risk.",
      ),
      done: 58,
    },
    {
      name: "sc2dm7k",
      kind: "codex",
      model: "gpt-5.6-luna",
      repo: "webshop@review-data-model",
      branch: "review/data-model",
      title: L(locale, "レビュー: データモデルと移行", "Review: data model & migrations"),
      area: L(locale, "Codex — データモデルと移行", "Codex — data model and migrations"),
      report: L(
        locale,
        "データモデル: 過去の注文 120 万件の埋め戻しが要ります。計画の一括移行だと `orders` が約 9 分ロックされるので、分割バックフィルを提案します。",
        "Data model: 1.2M historic orders need a backfill, and the single migration in the plan would lock `orders` for about 9 minutes. Suggest a batched backfill.",
      ),
      done: 44,
    },
    {
      name: "sa5api3",
      kind: "agy",
      model: "gemini-3-pro",
      repo: "webshop@review-api",
      branch: "review/api",
      title: L(locale, "レビュー: API 互換性", "Review: API compatibility"),
      area: L(locale, "Antigravity — API 互換性", "Antigravity — API compatibility"),
      report: L(
        locale,
        "API: `/v1/orders` はプロキシ経由で動き続けますが、`GET /orders?status=` のページングが変わります。モバイルアプリには破壊的変更なので v1 の互換層が必要です。",
        "API: `/v1/orders` keeps working through the proxy, but `GET /orders?status=` changes its paging — a breaking change for the mobile app. It needs a v1 shim.",
      ),
      done: 33,
    },
  ];
}

export function fixtures(locale) {
  // reposRead: the Console has re-read /api/repos since the spawn. The children are listed only
  // after that, so they never flash under "other sessions" before their worktrees are known.
  const state = { phase: "working", reposRead: false };
  const at = (min) => new Date(Date.now() - min * 60_000).toISOString();
  const ms = (min) => Date.now() - min * 60_000;
  const past = (p) => PHASES.indexOf(state.phase) >= PHASES.indexOf(p);
  const kids = children(locale);
  const reported = (i) => past(["r1", "r2", "r3"][i]);
  const T = {
    parent: L(locale, "設計レビュー: 注文サービスの分割", "Design review: order service split"),
    ask: L(
      locale,
      "着手前に注文サービス分割の設計（docs/design/order-split.md）をレビューして。Codex・Antigravity・Muse Code に分担させて、最後に 1 本にまとめて。",
      "Review the order-service split design (docs/design/order-split.md) before we start. Split it across Codex, Antigravity and Muse Code, then give me one summary.",
    ),
  };

  const sessions = () => [
    {
      name: PARENT,
      kind: "claude",
      driver: "tui",
      title: T.parent,
      repo: "webshop",
      dir: "~/repos/webshop",
      path: "/home/dev/repos/webshop",
      branch: "main",
      alive: true,
      model: "claude-opus-5",
      createdAt: at(150),
      origin: "user",
      state: state.phase === "working" ? "working" : state.phase === "r3" ? "idle" : "idle",
    },
    ...(past("spawned") && state.reposRead
      ? kids.map((k, i) => ({
          name: k.name,
          kind: k.kind,
          driver: k.kind === "agy" ? "tui" : "managed",
          title: k.title,
          repo: k.repo,
          dir: `~/repos/${k.repo}`,
          path: `/home/dev/repos/${k.repo}`,
          branch: k.branch,
          worktree: true,
          alive: true,
          ...(k.model ? { model: k.model } : {}),
          createdAt: at(121 - i),
          origin: "session",
          originSession: PARENT,
          state: reported(i) ? "idle" : "working",
        }))
      : []),
  ];

  const parentMessages = () => {
    const first = [
      txt(L(locale, "まず設計書を読みます。", "Reading the design first.")),
      tool("Read", "docs/design/order-split.md", L(locale, "212 行を読み込みました", "read 212 lines")),
    ];
    if (past("spawned")) {
      first.push(txt(L(locale, "レビューを 3 つに分けて渡します。", "Splitting the review three ways.")));
      // Text between the calls keeps each create_session on its own line; consecutive tool parts
      // fold into one "N tools" row (console/src/features/mirror/transcript/blocks.tsx ToolRun).
      for (const k of [kids[1], kids[2], kids[0]]) {
        first.push(txt(k.area), tool("mcp__af__create_session", `${k.kind} · ${k.title}`));
      }
    }
    const turns = [user(1, T.ask, at(150)), assistant(2, at(122), first, "claude-opus-5")];
    const acks = [
      L(locale, "受け取りました。残り 2 本を待ちます。", "Noted — waiting for the other two."),
      L(locale, "2 本そろいました。あと 1 本です。", "Two in, one to go."),
    ];
    kids.forEach((k, i) => {
      if (!reported(i)) return;
      turns.push(user(3 + i * 2, `[agent-fleet:peer from=${k.name} intent=answer reply=none] ${k.report}`, at(k.done), { source: "peer", peerFrom: k.name }));
      const reply =
        i < 2
          ? [txt(acks[i])]
          : [
              txt(
                L(
                  locale,
                  "3 本のレビューをまとめました。\n\n- **要対応** — `GET /orders` のページング変更でモバイルアプリが壊れる（Antigravity）。v1 の互換層を足す\n- **リスク** — 一括移行で `orders` が約 9 分ロック（Codex）。バックフィルを分割する\n- **低** — 注文履歴ページのコンポーネント 2 つ（Muse Code）\n\n着手前に上の 2 つを設計に反映するのがよいと思います。",
                  "Summary of the three reviews:\n\n- **Blocker** — the paging change in `GET /orders` breaks the mobile app (Antigravity). Add a v1 shim.\n- **Risk** — the single migration locks `orders` for ~9 min (Codex). Batch the backfill.\n- **Low** — the order history page, two components (Muse Code).\n\nI'd fold the first two into the design before starting.",
                ),
              ),
            ];
      turns.push(assistant(4 + i * 2, at(k.done - 1), reply, "claude-opus-5"));
    });
    return messagesBody(PARENT, turns, state.phase === "working" ? "working" : "idle");
  };

  const childMessages = (i) => {
    const k = kids[i];
    const task = `[agent-fleet:spawn from=${PARENT}] ${L(locale, `docs/design/order-split.md をレビューして。担当: ${k.area}`, `Review docs/design/order-split.md. Your part: ${k.area}.`)}`;
    const turns = [
      user(1, task, at(121 - i), { source: "spawn" }),
      assistant(2, at(k.done + 1), [tool("Read", "docs/design/order-split.md"), ...(reported(i) ? [txt(k.report)] : [])], k.model),
    ];
    return messagesBody(k.name, turns, reported(i) ? "idle" : "working");
  };

  // The graph as the Agent's ledgers would record it (console/src/types/fleetgraph.ts).
  const fleetGraph = () => {
    const now = Date.now();
    const lineage = [{ ev: "birth", ts: ms(150), name: PARENT, kind: "claude", repo: "webshop", origin: "user", display: T.parent }];
    const activity = [
      { ev: "instruct", ts: ms(150), from: "user", to: PARENT, excerpt: T.ask },
      { ev: "state", ts: ms(150), name: PARENT, to: "working" },
      { ev: "state", ts: ms(119), name: PARENT, to: "idle" },
    ];
    if (past("spawned")) {
      kids.forEach((k, i) => {
        lineage.push({ ev: "birth", ts: ms(121 - i), name: k.name, kind: k.kind, repo: k.repo, origin: "session", originSession: PARENT, display: k.title });
        activity.push({ ev: "state", ts: ms(121 - i), name: k.name, to: "working" });
      });
    }
    kids.forEach((k, i) => {
      if (!reported(i)) return;
      activity.push(
        { ev: "state", ts: ms(k.done + 1), name: k.name, to: "idle" },
        { ev: "peer", ts: ms(k.done), from: k.name, to: PARENT, intent: "answer", excerpt: k.report },
        { ev: "state", ts: ms(k.done), name: PARENT, to: "working" },
        { ev: "state", ts: ms(k.done - (i < 2 ? 1 : 6)), name: PARENT, to: "idle" },
      );
    });
    const since = now - 24 * 3600_000;
    return { since, until: now, now, lineage, activity, coverage: { activitySince: since, lineageSince: since } };
  };

  const exact = {
    "/api/sessions": (q, method) => (method === "GET" ? { sessions: sessions() } : null),
    "/api/repos": () => {
      if (past("spawned")) state.reposRead = true;
      return { repos: [repoRow("webshop", "main"), ...(past("spawned") ? kids.map((k) => repoRow(k.repo, k.branch)) : [])] };
    },
    "/api/fleet-graph": () => fleetGraph(),
  };
  const re = [
    [
      /^\/api\/sessions\/([^/]+)\/messages$/,
      (m) => {
        if (m[1] === PARENT) return parentMessages();
        const i = kids.findIndex((k) => k.name === m[1]);
        return i >= 0 && past("spawned") ? childMessages(i) : null;
      },
    ],
  ];
  return { route: router(exact, re), setPhase: phases(PHASES, state) };
}

// ---- the recording -------------------------------------------------------------------

export const meta = { width: 1280, height: 800 };

function text(locale) {
  return {
    en: [
      "One session can direct others: Claude splits a design review across three agent kinds.",
      "Each reviewer works in its own worktree and sends exactly one report back.",
      "The parent pulls the reports together, and the fleet graph draws the whole exchange.",
    ],
    ja: [
      "セッションは他のセッションを指揮できる。Claude が設計レビューを 3 種のエージェントに分担させる。",
      "レビュー役はそれぞれ自分の worktree で作業し、報告を 1 本ずつ返す。",
      "親が報告をまとめ、やり取りの全体はフリート俯瞰図に描かれる。",
    ],
  }[locale];
}

export function seed() {
  const view = (id, content, session = null) => ({ id, session, content, wrap: null });
  return {
    layout: {
      version: 3,
      mode: "split",
      // Stacked: the overview's four cards and the four-lane graph want the full width.
      cols: [
        {
          id: "c1",
          rowRatio: 0.55,
          cells: [
            { id: "g1", selectedViewId: "p1", views: [view("p1", { kind: "terminal", chat: true }, PARENT)] },
            { id: "g2", selectedViewId: "p2", views: [view("p2", { kind: "sessions", showStopped: false })] },
          ],
        },
      ],
      colRatios: [1],
      activeCellId: "g1",
    },
    sections: { assistant: 0, workitems: 0, memos: 0, schedules: 0, repos: 1, files: 0 },
    ready: `!!document.querySelector(".ovw-card")`,
  };
}

export async function script(c) {
  const T = text(c.locale);
  const { find } = c;
  const kids = children(c.locale);
  const cardOf = (k) => find(".ovw-card", k.title.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));

  await c.caption(1, T[0]);
  await c.record();
  await c.sleep(2000);

  await c.phase("spawned");
  // The worktrees the children were started in reach the rail on the Console's 60-second repos
  // poll (console/src/features/repos/store.ts); the story's clock is compressed, so press the
  // rail's own Refresh now instead of waiting it out on camera.
  await c.ev(`document.querySelector(".proj-head-sep")?.parentElement.querySelector('button[aria-label="Refresh"], button[aria-label="更新"]')?.click()`);
  await c.waitFor(`document.querySelectorAll(".ovw-card").length === 4`);
  await c.sleep(700);
  await c.hover(find(".mirror-turn", "create_session"), 700);
  await c.sleep(1200);
  await c.hover(cardOf(kids[2]), 600);
  await c.sleep(900);

  await c.caption(2, T[1]);
  // The reviewers' worktrees in the rail's repo tree, each under the working copy that made it.
  await c.hover(find(".sess-row", kids[1].title.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")), 700);
  await c.sleep(1100);
  for (const p of ["r1", "r2"]) {
    await c.phase(p);
    await c.sleep(1900);
  }
  await c.phase("r3");
  await c.waitFor(`/${L(c.locale, "3 本のレビューをまとめました", "Summary of the three reviews")}/.test(document.body.innerText)`);
  await c.sleep(600);
  await c.hover(find(".mirror-turn", L(c.locale, "3 本のレビューをまとめました", "Summary of the three reviews")), 700);
  await c.caption(3, T[2]);
  await c.sleep(2600);

  await c.click(find(".ovw-switch"), 700);
  await c.waitFor(`/^4 (lanes|レーン)$/.test(document.querySelector(".fgraph-count")?.textContent || "")`);
  await c.sleep(900);
  for (let i = 0; i < 3; i++) {
    await c.click(`[...document.querySelectorAll("button[title]")].find((b) => /^(Zoom in|拡大)/.test(b.title))`, i ? 250 : 700);
    await c.sleep(650);
  }
  await c.sleep(3400);
}
