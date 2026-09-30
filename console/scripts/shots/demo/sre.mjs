// Scenario "sre" — from an alert to a fix session, through two assistants
// (docs/img/demo-sre-<locale>.webp; the site's features page).
//
// The SRE assistant is asked what is firing; it reads PagerDuty and CloudWatch and narrows the
// cause. It is read-only (workspace/agent/internal/assistants/assistants.go: sre carries
// ToolsAFRead), so it cannot start anything — the member asks the fleet operator (af_write), whose
// create_session starts a Codex session in a new worktree, and that session gets to work. Both
// replies arrive the way the Console reads them: POST …/stream answered as Server-Sent Events
// ({agent} / {step} / {delta} / {done, conversation}, console/src/core/api/client.ts chatStream).
// There is no path by which an alert pushes itself into a chat, so the scene opens with a question.
import { L, ago, assistant, messagesBody, phases, repoRow, router, tool, txt, user } from "./kit.mjs";

const PHASES = ["start"];
const SRE = "c-sre-1";
const OPS = "c-ops-1";
const FIX = "sf3rds1";
const FIX_REPO = "platform-infra@fix-redis-pool";

function text(locale) {
  return {
    ask: L(locale, "PagerDuty でいま開いているものは？", "What's open on PagerDuty right now?"),
    steps: [
      L(locale, "PagerDuty の未解決インシデントを確認します。", "Checking open PagerDuty incidents."),
      L(locale, "checkout-api の 5xx とログを CloudWatch で見ます。", "Looking at checkout-api's 5xx rate and logs in CloudWatch."),
    ],
    finding: L(
      locale,
      "**未解決は 1 件**: `P7XK2Q` — *checkout-api の 5xx 率が 5% 超*（高緊急度、11:02 発報）\n\n- **メトリクス**: 11:01 のデプロイ `v2.14.0` 直後に 5xx が約 0.1% → 7% に跳ねています\n- **ログ**: `ECONNREFUSED redis:6379` が多発。新しいセッションストアのクライアントが、`REDIS_POOL_SIZE` の無いタスク定義で既定の 1 接続のまま動いています\n- **修正案**: `platform-infra` の checkout-api のタスク定義に `REDIS_POOL_SIZE=32` を足して再デプロイ\n\n私は読み取り専用です。修正はフリートオペレーターにセッションを立ててもらってください。",
      "**One open incident**: `P7XK2Q` — *checkout-api 5xx rate above 5%* (high urgency, triggered 11:02)\n\n- **Metric**: 5xx jumped from ~0.1% to 7% at 11:01, right after deploy `v2.14.0`\n- **Logs**: a burst of `ECONNREFUSED redis:6379` — the new session-store client runs on a task definition without `REDIS_POOL_SIZE`, so it gets the default single connection\n- **Likely fix**: add `REDIS_POOL_SIZE=32` to checkout-api's task definition in `platform-infra` and redeploy\n\nI'm read-only — ask the fleet operator to start a session for the fix.",
    ),
    order: L(
      locale,
      "platform-infra で Codex のセッションを立てて、checkout-api に REDIS_POOL_SIZE=32 を入れさせて。",
      "Start a Codex session in platform-infra to set REDIS_POOL_SIZE=32 for checkout-api.",
    ),
    opsStep: L(locale, "新しい worktree で Codex のセッションを起動します。", "Starting a Codex session in a new worktree."),
    started: L(
      locale,
      `**${FIX}** を \`${FIX_REPO}\` で起動しました。依頼: checkout-api のタスク定義に \`REDIS_POOL_SIZE=32\` を足し、\`terraform plan\` の結果を添えて報告すること。いま作業中で、報告はこの会話に届きます。`,
      `Started **${FIX}** in \`${FIX_REPO}\`. Its task: add \`REDIS_POOL_SIZE=32\` to checkout-api's task definition and report back with the \`terraform plan\`. It's working now; the report will arrive in this conversation.`,
    ),
    fixTitle: L(locale, "修正: checkout-api の Redis 接続数", "Fix: Redis pool size for checkout-api"),
    fixTask: L(
      locale,
      "checkout-api のタスク定義に REDIS_POOL_SIZE=32 を足して、terraform plan の結果を添えて報告して。",
      "Add REDIS_POOL_SIZE=32 to checkout-api's task definition and report back with the terraform plan.",
    ),
  };
}

export function fixtures(locale, fx) {
  const T = text(locale);
  // The operator conversation is an ongoing one; the SRE conversation starts empty.
  const state = {
    phase: "start",
    sre: [],
    ops: [
      { role: "user", content: L(locale, "いま動いているセッションはある？", "Is anything running right now?"), ts: Date.now() - 42 * 60_000 },
      {
        role: "assistant",
        agent: "claude",
        model: "claude-sonnet-5",
        content: L(locale, "いまは何も動いていません。フリートは待機中です。", "Nothing is running right now — the fleet is idle."),
        ts: Date.now() - 41 * 60_000,
      },
    ],
    started: false,
    reposRead: false,
  };
  const now = () => Date.now();

  const assistants = () => {
    const base = fx.assistants(locale);
    return [
      {
        id: "sre",
        name: L(locale, "SRE アシスタント", "SRE assistant"),
        icon: "pulse",
        builtin: true,
        agent: "claude",
        model: "claude-sonnet-5",
        description: L(locale, "インシデント対応の相談相手（読み取り専用）。", "Incident response and monitoring, read-only."),
        tools: {},
      },
      ...base,
    ];
  };
  const metas = () => [
    {
      id: SRE,
      slug: "a5sre9k",
      agent: "claude",
      assistant_id: "sre",
      title: L(locale, "SRE・checkout の 5xx", "SRE · checkout 5xx"),
      model: "claude-sonnet-5",
      created_at: now() - 60_000,
      updated_at: now(),
      message_count: state.sre.length,
    },
    {
      id: OPS,
      slug: "a3k9m2t",
      agent: "claude",
      assistant_id: "operator",
      title: L(locale, "フリート運用", "Fleet operations"),
      model: "claude-sonnet-5",
      created_at: now() - 3600_000,
      updated_at: now(),
      message_count: state.ops.length,
    },
  ];
  const conversation = (id) => {
    const meta = metas().find((m) => m.id === id);
    return { ...meta, tools: id === SRE ? "af_read" : "af_write", messages: id === SRE ? state.sre : state.ops };
  };

  // One streamed reply: the steps, then the answer in small deltas, then the saved conversation.
  const reply = (conv, steps, content, onStep) => {
    const list = conv === SRE ? state.sre : state.ops;
    const frames = [{ delay: 150, data: { agent: "claude" } }];
    steps.forEach((s, i) =>
      frames.push({
        delay: i ? 1900 : 700,
        data: () => {
          onStep?.(i);
          return { step: s };
        },
      }),
    );
    for (let i = 0; i < content.length; i += 14) frames.push({ delay: i ? 45 : 1500, data: { delta: content.slice(i, i + 14) } });
    frames.push({
      delay: 200,
      data: () => {
        list.push({ role: "assistant", agent: "claude", model: "claude-sonnet-5", content, steps, ts: now() });
        return { done: true, conversation: conversation(conv) };
      },
    });
    return frames;
  };

  const stream = (p, method, body) => {
    const m = /^\/api\/chat\/conversations\/([^/]+)\/stream$/.exec(p);
    if (!m || method !== "POST") return null;
    const conv = m[1];
    const content = String(body?.content || "");
    (conv === SRE ? state.sre : state.ops).push({ role: "user", content, ts: now() });
    if (conv === SRE) {
      return reply(
        SRE,
        [
          { text: T.steps[0], tools: ["mcp__pagerduty__list_incidents"] },
          { text: T.steps[1], tools: ["mcp__cloudwatch__get_metric_data", "mcp__cloudwatch__execute_log_insights_query"] },
        ],
        T.finding,
      );
    }
    // The operator's create_session is what makes the session exist.
    return reply(OPS, [{ text: T.opsStep, tools: ["mcp__af__create_session"] }], T.started, () => {
      state.started = true;
    });
  };

  const fixSession = () => ({
    name: FIX,
    kind: "codex",
    driver: "managed",
    title: T.fixTitle,
    repo: FIX_REPO,
    dir: `~/repos/${FIX_REPO}`,
    path: `/home/dev/repos/${FIX_REPO}`,
    branch: "fix/redis-pool",
    worktree: true,
    alive: true,
    model: "gpt-5.6-luna",
    origin: "operator",
    createdAt: new Date().toISOString(),
    state: "working",
  });

  const fixMessages = () =>
    messagesBody(
      FIX,
      [
        user(1, T.fixTask, ago(0), { source: "operator" }),
        assistant(
          2,
          ago(0),
          [
            txt(L(locale, "タスク定義を探します。", "Finding the task definition.")),
            tool("Bash", "rg -n REDIS_ modules/checkout-api", "modules/checkout-api/task.tf:41:        { name = \"REDIS_URL\", value = var.redis_url },"),
            tool("Edit", "modules/checkout-api/task.tf"),
          ],
          "gpt-5.6-luna",
        ),
      ],
      "working",
    );

  const exact = {
    "/api/assistants": () => assistants(),
    "/api/chat/conversations": (q, method) => (method === "GET" ? { conversations: metas() } : null),
    "/api/sessions": (q, method) => (method === "GET" ? { sessions: state.started && state.reposRead ? [fixSession()] : [] } : null),
    "/api/repos": () => {
      if (state.started) state.reposRead = true;
      return { repos: [repoRow("webshop", "main"), repoRow("platform-infra", "main"), ...(state.started ? [repoRow(FIX_REPO, "fix/redis-pool")] : [])] };
    },
  };
  const re = [
    [/^\/api\/chat\/conversations\/([^/]+)$/, (m, q, method) => (method === "GET" && (m[1] === SRE || m[1] === OPS) ? conversation(m[1]) : null)],
    [/^\/api\/sessions\/([^/]+)\/messages$/, (m) => (m[1] === FIX && state.started ? fixMessages() : null)],
  ];
  return { route: router(exact, re), stream, setPhase: phases(PHASES, state) };
}

// ---- the recording -------------------------------------------------------------------

export const meta = { width: 1280, height: 800 };

function captions(locale) {
  return {
    en: [
      "Ask the SRE assistant what's firing. It reads PagerDuty and CloudWatch — read-only.",
      "It can't change anything, so hand the fix to the fleet operator.",
      "The operator starts a session in a new worktree, and the fix is under way.",
    ],
    ja: [
      "SRE アシスタントに何が起きているか聞く。PagerDuty と CloudWatch を読んで絞り込む（読み取り専用）。",
      "SRE アシスタントは何も変えられないので、修正はフリートオペレーターに任せる。",
      "オペレーターが新しい worktree でセッションを立て、修正が動き出す。",
    ],
  }[locale];
}

export function seed() {
  const view = (id, content) => ({ id, session: null, content, wrap: null });
  return {
    layout: {
      version: 3,
      mode: "split",
      cols: [
        { id: "c1", rowRatio: 0.5, cells: [{ id: "g1", selectedViewId: "p1", views: [view("p1", { kind: "chat", conversationId: SRE, draftAssistantId: null })] }] },
        {
          id: "c2",
          rowRatio: 0.58,
          cells: [
            { id: "g2", selectedViewId: "p2", views: [view("p2", { kind: "chat", conversationId: OPS, draftAssistantId: null })] },
            { id: "g3", selectedViewId: "p3", views: [view("p3", { kind: "sessions", showStopped: false })] },
          ],
        },
      ],
      colRatios: [0.52, 0.48],
      activeCellId: "g1",
    },
    sections: { assistant: 1, workitems: 0, memos: 0, schedules: 0, repos: 1, files: 0 },
    ready: `document.querySelectorAll(".chat-input").length === 2`,
  };
}

export async function script(c) {
  const T = text(c.locale);
  const C = captions(c.locale);
  const input = (i) => `document.querySelectorAll(".chat-input")[${i}]`;
  const send = (i) => `document.querySelectorAll(".chat-send.primary")[${i}]`;
  // Settled: the assistant bubble carrying `needle` is in the pane and nothing is streaming.
  const answered = (needle) => `[...document.querySelectorAll(".chat-send.primary")].length === 2 && document.body.innerText.includes(${JSON.stringify(needle)})`;

  await c.caption(1, C[0]);
  await c.record();
  await c.sleep(1500);

  await c.click(input(0), 700);
  await c.type(T.ask);
  await c.sleep(300);
  await c.click(send(0), 500);
  await c.ev(`__demo.hideCursor()`);
  await c.waitFor(answered("P7XK2Q"), 20000);
  await c.sleep(2600);

  await c.caption(2, C[1]);
  await c.click(input(1), 800);
  await c.type(T.order, 45);
  await c.sleep(300);
  await c.click(send(1), 500);
  await c.ev(`__demo.hideCursor()`);
  await c.waitFor(answered(FIX), 20000);
  // The new worktree reaches the rail on the Console's 60-second repos poll
  // (console/src/features/repos/store.ts); the story's clock is compressed, so press the rail's
  // own Refresh now.
  await c.ev(`document.querySelector(".proj-head-sep")?.parentElement.querySelector('button[aria-label="Refresh"], button[aria-label="更新"]')?.click()`);
  await c.caption(3, C[2]);
  await c.waitFor(`!!document.querySelector(".ovw-card")`, 12000);
  await c.sleep(800);
  await c.hover(c.find(".ovw-card"), 700);
  await c.sleep(3400);
}
