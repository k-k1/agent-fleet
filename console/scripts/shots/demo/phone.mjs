// Scenario "phone" — answering a waiting session from a phone's browser
// (docs/img/demo-phone-<locale>.webp; README "From anywhere").
//
// The sessions overview on a 390px screen → tap the session that is waiting on a question → pick
// an option and submit → the agent carries on and finishes → back on the overview it reads Ready.
// The answer goes through the real route: a codex managed session answers with
// POST /api/sessions/<name>/respond (console/src/core/api/client.ts sessionRespond), and the stub
// only moves on when that request arrives.
import { L, ago, assistant, messagesBody, phases, repoRow, router, tool, txt, user } from "./kit.mjs";

const PHASES = ["waiting", "done"];
const CODEX = "sd8m2xv";

function question(locale) {
  return [
    {
      id: "q-expiry",
      header: L(locale, "期限の基準", "Expiry clock"),
      question: L(locale, "クーポンの期限はどの時刻で判定しますか？", "Which clock should decide when a coupon expires?"),
      options: [
        {
          label: L(locale, "店舗のタイムゾーン", "The store's time zone"),
          description: L(locale, "店舗設定の TZ で日付の終わりを判定する。", "End of day in the store's configured zone."),
        },
        {
          label: L(locale, "UTC のまま、表示だけ変換", "Keep UTC, convert on display"),
          description: L(locale, "判定は変えず、表示を店舗時刻に直す。", "Leave the check alone, show store time."),
        },
      ],
    },
  ];
}

export function fixtures(locale) {
  const state = { phase: "waiting", answered: false };
  const t0 = ago(14);

  const sessions = () => {
    const done = state.phase === "done";
    return [
      {
        name: CODEX,
        kind: "codex",
        driver: "managed",
        title: L(locale, "#321 クーポンの期限判定が店舗のタイムゾーンを無視する", "#321 Coupon expiry ignores the store's time zone"),
        repo: "webshop@feature-issue-321",
        dir: "~/repos/webshop@feature-issue-321",
        path: "/home/dev/repos/webshop@feature-issue-321",
        state: done ? "" : state.answered ? "working" : "question",
        alive: true,
        model: "gpt-5.6-luna",
        branch: "feature/issue-321",
        worktree: true,
        createdAt: t0,
      },
      {
        name: "sd3k7qa",
        kind: "claude",
        driver: "tui",
        title: L(locale, "#318 合計 0 円の注文が決済まで進んでしまう", "#318 Orders with a zero total reach payment"),
        repo: "webshop@feature-issue-318",
        dir: "~/repos/webshop@feature-issue-318",
        path: "/home/dev/repos/webshop@feature-issue-318",
        state: "idle",
        alive: true,
        model: "claude-sonnet-5",
        branch: "feature/issue-318",
        worktree: true,
        createdAt: ago(16),
        lastSay: L(locale, "再現テストを書いて修正案を当てました。テストは緑です。", "Reproduced it with a failing test and drafted the fix — the test passes now."),
      },
      {
        name: "sq3hn7v",
        kind: "copilot",
        driver: "managed",
        title: L(locale, "Terraform の lint 追従", "Terraform lint follow-up"),
        repo: "webshop",
        dir: "~/repos/webshop",
        path: "/home/dev/repos/webshop",
        state: "working",
        alive: true,
        branch: "main",
        createdAt: ago(40),
      },
    ];
  };

  const answer = L(locale, "店舗のタイムゾーン", "The store's time zone");
  const messages = () => {
    const turns = [
      user(1, L(locale, "#321 のクーポン期限の判定を直して。", "Fix the coupon expiry check from #321."), t0),
      assistant(
        2,
        t0,
        [
          txt(L(locale, "期限判定の実装を確認します。", "Looking at how expiry is checked.")),
          tool("Bash", "rg -n expiresAt src/coupons", "src/coupons/expiry.ts:12:  return now() > coupon.expiresAt;"),
          txt(L(locale, "判定が UTC の現在時刻と比べています。直し方が 2 通りあります。", "The check compares against the current UTC time. There are two ways to fix it.")),
        ],
        "gpt-5.6-luna",
      ),
    ];
    if (!state.answered) {
      return messagesBody(CODEX, turns, "question", {
        pendingQuestions: question(locale),
        pendingText: L(locale, "先にどちらにするか決めさせてください。", "Let's pick one first."),
      });
    }
    // Answered: the question stays in the transcript, resolved with the option that was picked.
    const after = [
      { kind: "question", questions: question(locale), answer },
      txt(L(locale, "店舗のタイムゾーンで判定するように直します。", "Switching the check to the store's time zone.")),
      tool("Edit", "src/coupons/expiry.ts"),
      tool("Bash", "npm test -- coupons", "PASS  src/coupons/expiry.test.ts\n\nTests: 4 passed, 4 total"),
    ];
    if (state.phase === "done") {
      after.push(
        txt(
          L(
            locale,
            "期限を店舗のタイムゾーンの日付の終わりで判定するようにしました。境界のテストを 2 本足し、全部緑です。",
            "Expiry is now decided at the end of the day in the store's time zone. Added two boundary tests; all green.",
          ),
        ),
      );
    }
    turns.push(assistant(3, new Date().toISOString(), after, "gpt-5.6-luna"));
    return messagesBody(CODEX, turns, state.phase === "done" ? "idle" : "working");
  };

  const exact = {
    "/api/sessions": (q, method) => (method === "GET" ? { sessions: sessions() } : null),
    "/api/repos": () => ({
      repos: [repoRow("webshop", "main"), repoRow("webshop@feature-issue-318", "feature/issue-318"), repoRow("webshop@feature-issue-321", "feature/issue-321")],
    }),
  };
  const re = [
    [
      /^\/api\/sessions\/([^/]+)\/respond$/,
      (m, q, method) => {
        if (method !== "POST" || m[1] !== CODEX) return null;
        state.answered = true;
        return {};
      },
    ],
    [/^\/api\/sessions\/([^/]+)\/messages$/, (m) => (m[1] === CODEX ? messages() : null)],
  ];
  return { route: router(exact, re), setPhase: phases(PHASES, state) };
}

// ---- the recording -------------------------------------------------------------------

// A phone: 390x844 CSS pixels at 2x, the width the Console's phone layout starts below (761px).
export const meta = { width: 390, height: 844, dpr: 2, mobile: true, band: 76, captionPx: 16 };

function text(locale) {
  return {
    en: [
      "Away from your desk, the Console runs in your phone's browser.",
      "One session waits on a question. Answer it with a tap.",
      "It carries on where it stopped — and the list shows it done.",
    ],
    ja: [
      "机を離れても、Console はスマートフォンのブラウザで動く。",
      "質問で止まっているセッションに、タップで答える。",
      "止まったところから作業が続き、一覧では完了と分かる。",
    ],
  }[locale];
}

export function seed() {
  return {
    layout: {
      version: 3,
      mode: "split",
      cols: [
        { id: "c1", rowRatio: 0.5, cells: [{ id: "g1", selectedViewId: "p1", views: [{ id: "p1", session: null, content: { kind: "sessions", showStopped: false }, wrap: null }] }] },
      ],
      colRatios: [1],
      activeCellId: "g1",
    },
    ready: `!!document.querySelector(".ovw-card")`,
  };
}

export async function script(c) {
  const T = text(c.locale);
  const { find } = c;
  await c.ev(`__demo.setFinger(true); __demo.move(${c.W / 2}, ${c.H - 120}, 0); __demo.hideCursor()`);
  await c.caption(1, T[0]);
  await c.record();
  await c.sleep(2200);

  await c.click(find(".ovw-card", "#321"), 700);
  await c.waitFor(`!!document.querySelector(".mq-opt")`);
  await c.sleep(700);
  await c.caption(2, T[1]);
  await c.sleep(1600);
  await c.click(`${find(".mq-opt-label", L(c.locale, "店舗のタイムゾーン", "store's time zone"))}?.closest(".mq-opt")`, 600);
  await c.sleep(700);
  await c.click(find(".mq-submit"), 600);
  await c.waitFor(`!!document.querySelector(".mt-question.answered")`);
  await c.sleep(2400);

  await c.phase("done");
  await c.caption(3, T[2]);
  await c.waitFor(`!document.querySelector(".mirror-typing, .mt-typing") && /${L(c.locale, "全部緑", "all green")}/.test(document.body.innerText)`);
  await c.sleep(1600);
  await c.click(find(".ws-overview"), 700);
  await c.waitFor(`!/question|working/.test((${find(".ovw-card", "#321")})?.className || "working")`, 12000);
  await c.sleep(2600);
}
