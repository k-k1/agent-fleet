// Scenario "review" — a session hands its review to another agent kind and gets one report back
// (docs/img/demo-review-<locale>.webp; README "Sessions that start sessions").
//
// A Claude session finishes its change and starts a Codex reviewer (its create_session tool call);
// the child appears in the overview and the rail in the family's colour; the child's report arrives
// in the parent's chat as a peer message; the fleet graph draws the two lanes, the spawn and the
// report. The shapes follow ADR 0073: a child is origin "session" + originSession
// (console/src/types/session.ts), the report is a user turn with source "peer" and the
// `[agent-fleet:peer from=… intent=answer …]` envelope (workspace/agent/internal/sessionx/
// session_peer.go), and the graph is GET /api/fleet-graph (console/src/types/fleetgraph.ts).
//
// The story's clock is compressed: the timestamps spread it over the last two and a half hours so
// the graph, zoomed to three hours, has room to draw it.
import { L, assistant, messagesBody, phases, repoRow, router, tool, txt, user } from "./kit.mjs";

const PHASES = ["working", "spawned", "reported"];
const PARENT = "sk4rq2f";
const CHILD = "sc0dx9r";
const REPO = "webshop@checkout-validation";

export function fixtures(locale) {
  const state = { phase: "working" };
  const at = (min) => new Date(Date.now() - min * 60_000).toISOString();
  const ms = (min) => Date.now() - min * 60_000;
  const T = {
    parent: L(locale, "チェックアウトの入力検証", "Checkout input validation"),
    child: L(locale, "レビュー: チェックアウトの入力検証", "Review: checkout input validation"),
    ask: L(
      locale,
      "チェックアウトに入力検証を足して。終わったら PR の前に Codex にレビューさせて。",
      "Add input validation to checkout. When it's done, have Codex review it before the PR.",
    ),
    report: L(
      locale,
      "3 ファイルをレビューしました。1 件要修正: `cartTotal` が割引を円でなく銭で引いています（validate.ts:9）。他は問題ありません。",
      "Reviewed 3 files. One fix needed: `cartTotal` subtracts the discount in cents, not yen (validate.ts:9). The rest looks good.",
    ),
  };
  const at2 = (s) => (state.phase === "working" ? false : PHASES.indexOf(state.phase) >= PHASES.indexOf(s));

  const session = (name, over) => ({
    name,
    repo: REPO,
    dir: `~/repos/${REPO}`,
    path: `/home/dev/repos/${REPO}`,
    branch: "feat/checkout-validation",
    worktree: true,
    alive: true,
    ...over,
  });
  const sessions = () => [
    session(PARENT, {
      kind: "claude",
      driver: "tui",
      title: T.parent,
      state: state.phase === "working" ? "working" : "idle",
      model: "claude-opus-5",
      createdAt: at(150),
      origin: "user",
    }),
    ...(at2("spawned")
      ? [
          session(CHILD, {
            kind: "codex",
            driver: "managed",
            title: T.child,
            state: at2("reported") ? "idle" : "working",
            model: "gpt-5.6-luna",
            createdAt: at(95),
            origin: "session",
            originSession: PARENT,
          }),
        ]
      : []),
  ];

  const parentMessages = () => {
    const work = [
      txt(L(locale, "検証を実装します。", "Implementing the validation.")),
      tool("Edit", "src/checkout/validate.ts"),
      tool("Edit", "src/checkout/validate.test.ts"),
      tool("Bash", "npm test -- checkout", "Tests: 6 passed, 6 total"),
    ];
    if (at2("spawned")) {
      work.push(
        txt(L(locale, "テストは緑です。レビューを Codex のセッションに渡します。", "Tests pass. Handing the review to a Codex session.")),
        // How claude records an af MCP call; the mirror has no special card for it
        // (workspace/agent/internal/agents/claude/transcript.go toolInfo).
        tool("mcp__af__create_session", `codex · ${T.child}`),
      );
    }
    const turns = [user(1, T.ask, at(150)), assistant(2, at(96), work, "claude-opus-5")];
    if (at2("reported")) {
      const env = `[agent-fleet:peer from=${CHILD} intent=answer reply=none] ${T.report}`;
      turns.push(
        user(3, env, at(35), { source: "peer", peerFrom: CHILD }),
        assistant(
          4,
          at(22),
          [
            tool("Edit", "src/checkout/total.ts"),
            tool("Bash", "npm test -- checkout", "Tests: 7 passed, 7 total"),
            txt(
              L(
                locale,
                "Codex の指摘どおり割引の単位を直し、回帰テストを 1 本足しました。7 本とも緑で、PR の準備ができています。",
                "Fixed the discount units Codex flagged and added a regression test. All 7 pass — ready for the PR.",
              ),
            ),
          ],
          "claude-opus-5",
        ),
      );
    }
    return messagesBody(PARENT, turns, state.phase === "working" ? "working" : "idle");
  };

  const childMessages = () => {
    const task = `[agent-fleet:spawn from=${PARENT}] ${L(locale, "feat/checkout-validation の差分をレビューして。", "Review the diff on feat/checkout-validation.")}`;
    const turns = [user(1, task, at(95), { source: "spawn" })];
    turns.push(
      assistant(
        2,
        at(36),
        [tool("Bash", "git diff main...HEAD --stat", " 3 files changed, 61 insertions(+), 4 deletions(-)"), ...(at2("reported") ? [txt(T.report)] : [])],
        "gpt-5.6-luna",
      ),
    );
    return messagesBody(CHILD, turns, at2("reported") ? "idle" : "working");
  };

  // The graph as the Agent's ledgers would record this story (console/src/types/fleetgraph.ts).
  const fleetGraph = () => {
    const now = Date.now();
    const lineage = [{ ev: "birth", ts: ms(150), name: PARENT, kind: "claude", repo: REPO, origin: "user", display: T.parent }];
    const activity = [
      { ev: "instruct", ts: ms(150), from: "user", to: PARENT, excerpt: T.ask },
      { ev: "state", ts: ms(150), name: PARENT, to: "working" },
      { ev: "state", ts: ms(95), name: PARENT, to: "idle" },
    ];
    if (at2("spawned")) {
      lineage.push({ ev: "birth", ts: ms(95), name: CHILD, kind: "codex", repo: REPO, origin: "session", originSession: PARENT, display: T.child });
      activity.push({ ev: "state", ts: ms(95), name: CHILD, to: "working" });
    }
    if (at2("reported")) {
      activity.push(
        { ev: "state", ts: ms(36), name: CHILD, to: "idle" },
        { ev: "peer", ts: ms(35), from: CHILD, to: PARENT, intent: "answer", excerpt: T.report },
        { ev: "state", ts: ms(35), name: PARENT, to: "working" },
        { ev: "state", ts: ms(22), name: PARENT, to: "idle" },
      );
    }
    return { since: now - 24 * 3600_000, until: now, now, lineage, activity, coverage: { activitySince: now - 24 * 3600_000, lineageSince: now - 24 * 3600_000 } };
  };

  const exact = {
    "/api/sessions": (q, method) => (method === "GET" ? { sessions: sessions() } : null),
    "/api/repos": () => ({ repos: [repoRow("webshop", "main"), repoRow(REPO, "feat/checkout-validation")] }),
    "/api/fleet-graph": () => fleetGraph(),
  };
  const re = [
    [
      /^\/api\/sessions\/([^/]+)\/messages$/,
      (m) => (m[1] === PARENT ? parentMessages() : m[1] === CHILD ? childMessages() : null),
    ],
  ];
  return { route: router(exact, re), setPhase: phases(PHASES, state) };
}

// ---- the recording -------------------------------------------------------------------

export const meta = { width: 1280, height: 800 };

function text(locale) {
  return {
    en: [
      "A session can start another — here Claude hands its review to Codex.",
      "The reviewer works on its own and sends exactly one report back.",
      "The fleet graph draws who started whom and what passed between them.",
    ],
    ja: [
      "セッションは別のセッションを起こせる。ここでは Claude がレビューを Codex に渡す。",
      "レビュー役は自分で作業し、報告を 1 本だけ返してくる。",
      "フリート俯瞰図に、誰が誰を起こし、何をやり取りしたかが描かれる。",
    ],
  }[locale];
}

export function seed() {
  const view = (id, content, session = null) => ({ id, session, content, wrap: null });
  return {
    layout: {
      version: 3,
      mode: "split",
      // Stacked, not side by side: the fleet graph at the end needs the full width to read.
      cols: [
        {
          id: "c1",
          rowRatio: 0.56,
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
  await c.caption(1, T[0]);
  await c.record();
  await c.sleep(2000);

  await c.phase("spawned");
  await c.waitFor(`!!document.querySelectorAll(".ovw-card")[1]`);
  await c.sleep(600);
  await c.hover(find(".mirror-turn", "create_session"), 700);
  await c.sleep(1400);
  await c.hover(`document.querySelectorAll(".ovw-card")[1]`, 700);
  await c.sleep(1800);

  await c.caption(2, T[1]);
  await c.phase("reported");
  await c.waitFor(`!!${find(".mirror-turn", CHILD)} && !/working/.test(document.querySelectorAll(".ovw-card")[1]?.className || "working")`);
  await c.sleep(500);
  await c.hover(find(".mirror-turn", CHILD), 700);
  await c.sleep(2800);

  await c.caption(3, T[2]);
  await c.click(find(".ovw-switch"), 700);
  await c.waitFor(`/^2 (lanes|レーン)$/.test(document.querySelector(".fgraph-count")?.textContent || "")`);
  await c.sleep(900);
  for (let i = 0; i < 3; i++) {
    await c.click(`[...document.querySelectorAll("button[title]")].find((b) => /^(Zoom in|拡大)/.test(b.title))`, i ? 250 : 700);
    await c.sleep(650);
  }
  await c.sleep(3200);
}
