// Scenario "unattended" — a schedule wakes a stopped workspace, and a usage limit is waited out
// (docs/img/demo-unattended-<locale>.webp; README "Unattended work").
//
// 08:59, the workspace is stopped → at 09:00 the schedule wakes it (the Console's "Starting
// workspace" dialog) and starts a Claude session → at 11:20 the session hits its usage limit and
// waits for the reset → at 14:00 it resumes by itself and finishes. What each step looks like is the
// real wire: GET /api/workspace states and boot phases (console/src/core/store/workspace.ts), a
// session with origin "schedule" whose first turn has source "schedule", state "limited" with
// rateLimitResumeAt (console/src/lib/sessionview.ts), and the resume as a scheduled turn carrying
// the Agent's own resume prompt (workspace/agent/internal/sessionx/rate_limit_resume.go) — the
// auto-resume is a once-schedule, so that turn is badged "Scheduled", not "Auto-resume".
//
// The story spans five hours, so the page runs on a story clock (seed().init replaces Date) and
// the fixtures stamp everything with the same clock: relative labels such as "started 2 minutes
// ago" stay true to the scene instead of to the machine recording it.
import { L, assistant, messagesBody, phases, repoRow, router, tool, txt, user } from "./kit.mjs";

const PHASES = ["stopped", "waking", "running", "limited", "resumed", "done"];
const SESSION = "sd9dep1";

// Today at hh:mm in the recording machine's zone, which the browser shares.
export const storyTime = (hh, mm, ss = 0) => {
  const d = new Date();
  d.setHours(hh, mm, ss, 0);
  return d.getTime();
};
const iso = (hh, mm) => new Date(storyTime(hh, mm)).toISOString();

export function fixtures(locale) {
  const state = { phase: "stopped" };
  const past = (p) => PHASES.indexOf(state.phase) >= PHASES.indexOf(p);
  const prompt = L(locale, "依存パッケージを更新して、壊れたところを直して。", "Update the dependencies and fix whatever breaks.");

  const workspace = () =>
    state.phase === "stopped"
      ? { state: "stopped", bootPhase: "" }
      : state.phase === "waking"
        ? { state: "starting", bootPhase: "slot: waking" }
        : { state: "running", bootPhase: "" };

  const sessions = () =>
    past("running")
      ? [
          {
            name: SESSION,
            kind: "claude",
            driver: "tui",
            title: L(locale, "依存パッケージの定期更新", "Weekly dependency update"),
            repo: "webshop",
            dir: "~/repos/webshop",
            path: "/home/dev/repos/webshop",
            branch: "main",
            alive: true,
            model: "claude-sonnet-5",
            origin: "schedule",
            createdAt: iso(9, 0),
            state: state.phase === "limited" ? "limited" : state.phase === "done" ? "idle" : "working",
            ...(state.phase === "limited" ? { rateLimitResumeAt: iso(14, 0) } : {}),
          },
        ]
      : [];

  const schedules = () => [
    {
      id: "sch-deps",
      spec_kind: "cron",
      spec: "0 9 * * 1-5",
      spec_label: L(locale, "平日 9:00", "Weekdays at 9:00"),
      tz: Intl.DateTimeFormat().resolvedOptions().timeZone,
      session_mode: "new",
      agent_kind: "claude",
      repo: "webshop",
      prompt,
      enabled: true,
      wake_policy: "wake",
      next_run: past("running") ? new Date(storyTime(9, 0) + 86400_000).toISOString() : iso(9, 0),
      next_run_local: past("running") ? L(locale, "明日 09:00", "tomorrow 09:00") : L(locale, "今日 09:00", "today 09:00"),
      last_run: past("running") ? iso(9, 0) : "",
      last_status: past("running") ? "fired" : "",
    },
  ];

  const messages = () => {
    if (!past("running")) return null;
    const turns = [
      user(1, prompt, iso(9, 0), { source: "schedule" }),
      assistant(
        2,
        iso(9, 1),
        [
          txt(L(locale, "古くなったパッケージを確認します。", "Checking for outdated packages.")),
          tool("Bash", "npm outdated", "vitest   2.1.8   3.2.0\nzod      3.23.8  3.25.1\n… 4 more"),
          tool("Edit", "package.json"),
          tool("Bash", "npm test", L(locale, "2 件失敗: スナップショットの形式が変わった", "2 failed: snapshot format changed")),
          ...(past("limited") ? [txt(L(locale, "vitest 3 でスナップショットの形式が変わったので直します。", "vitest 3 changed the snapshot format; fixing those two."))] : []),
        ],
        "claude-sonnet-5",
      ),
    ];
    if (past("resumed")) {
      turns.push(
        user(
          3,
          L(
            locale,
            "利用上限がリセットされました。上限で中断した作業を、止まったところから続けてください。これは自動再開なので新しい指示はありません。どこで止まったか分からない場合は、新しい作業を始めずにその旨を伝えてください。",
            "The usage limit has reset. Continue the work that was cut off, from where it stopped. This is an automatic resume — there is no new instruction. If you cannot tell where it stopped, say so instead of starting something new.",
          ),
          iso(14, 0),
          { source: "schedule" },
        ),
        assistant(
          4,
          iso(14, 1),
          [
            tool("Edit", "src/cart/__snapshots__/CartSummary.test.tsx.snap"),
            tool("Bash", "npm test", "Tests: 214 passed, 214 total"),
            ...(state.phase === "done"
              ? [
                  txt(
                    L(
                      locale,
                      "6 パッケージを更新し、vitest 3 で壊れたスナップショット 2 件を直しました。テストは全部通っています。",
                      "Updated 6 packages and fixed the two snapshots vitest 3 broke. All tests pass.",
                    ),
                  ),
                ]
              : []),
          ],
          "claude-sonnet-5",
        ),
      );
    }
    const status = state.phase === "limited" ? "limited" : state.phase === "done" ? "idle" : "working";
    return messagesBody(SESSION, turns, status);
  };

  const exact = {
    "/api/workspace": () => workspace(),
    "/api/sessions": (q, method) => (method === "GET" ? { sessions: sessions() } : null),
    "/api/schedules": () => schedules(),
    "/api/repos": () => ({ repos: [repoRow("webshop", "main")] }),
  };
  const re = [[/^\/api\/sessions\/([^/]+)\/messages$/, (m) => (m[1] === SESSION ? messages() : null)]];
  return { route: router(exact, re), setPhase: phases(PHASES, state) };
}

// ---- the recording -------------------------------------------------------------------

export const meta = { width: 1280, height: 800 };

function text(locale) {
  return {
    en: [
      "Nobody at the desk: at 9:00 a schedule wakes the stopped workspace and starts the work.",
      "It hits a usage limit, waits for the reset, and resumes on its own.",
    ],
    ja: [
      "誰もいない朝 9:00、スケジュールが停止中のワークスペースを起こして作業を始める。",
      "利用上限に達したらリセットを待ち、自分で再開して終わらせる。",
    ],
  }[locale];
}

export function seed() {
  const view = (id, content, session = null) => ({ id, session, content, wrap: null });
  return {
    layout: {
      version: 3,
      mode: "split",
      cols: [{ id: "c1", rowRatio: 0.5, cells: [{ id: "g1", selectedViewId: "p1", views: [view("p1", { kind: "sessions", showStopped: false })] }] }],
      colRatios: [1],
      activeCellId: "g1",
    },
    sections: { assistant: 0, workitems: 0, memos: 0, schedules: 1, repos: 1, files: 0 },
    // The story clock: Date answers story time, offset from the real clock so time still passes.
    init: `(() => {
      const R = Date;
      let off = ${storyTime(8, 59, 38)} - R.now();
      function D(...a) {
        if (!new.target) return new R(R.now() + off).toString();
        return a.length ? new R(...a) : new R(R.now() + off);
      }
      D.prototype = R.prototype;
      D.now = () => R.now() + off;
      D.parse = R.parse;
      D.UTC = R.UTC;
      window.Date = D;
      window.__demoSetClock = (ms) => { off = ms - R.now(); };
    })();`,
    ready: `/Stopped|停止/.test(document.querySelector(".ws-state, .wsbar")?.textContent || document.body.innerText)`,
  };
}

export async function script(c) {
  const T = text(c.locale);
  const { find } = c;
  const setClock = (hh, mm, ss = 0) => c.ev(`__demoSetClock(${storyTime(hh, mm, ss)})`);

  await c.ev(`__demo.clock("08:59")`);
  await c.caption(1, T[0]);
  await c.record();
  await c.sleep(1800);
  await c.hover(find(".sched-info", L(c.locale, "平日 9:00", "Weekdays at 9:00")), 700);
  await c.sleep(1200);

  await setClock(9, 0, 2);
  await c.ev(`__demo.clock("09:00")`);
  await c.phase("waking");
  await c.waitFor(`/${L(c.locale, "ワークスペースを起動中", "Starting workspace")}/.test(document.body.innerText)`, 12000);
  await c.sleep(1800);
  await c.phase("running");
  await c.waitFor(`!!${find(".ovw-card")}`, 12000);
  await c.sleep(900);
  await c.click(find(".ovw-card"), 700);
  await c.waitFor(`!!document.querySelector(".from-schedule")`);
  await c.sleep(2200);

  await c.caption(2, T[1]);
  await setClock(11, 20);
  await c.ev(`__demo.clock("11:20")`);
  await c.phase("limited");
  await c.waitFor(`/${L(c.locale, "制限解除待ち", "Waiting for limit reset")}/.test(document.body.innerText)`, 12000);
  await c.sleep(1400);
  await c.hover(find(".ovw-card"), 700);
  await c.sleep(1200);
  await c.ev(`__demo.hideCursor()`);
  await c.ev(`__demo.clockLapse("11:20", "14:00", 1600)`);
  await setClock(14, 0, 5);
  await c.phase("resumed");
  await c.waitFor(`document.querySelectorAll(".from-schedule").length >= 2`, 12000);
  await c.sleep(1800);
  await setClock(14, 26);
  await c.ev(`__demo.clock("14:26")`);
  await c.phase("done");
  await c.waitFor(`/${L(c.locale, "テストは全部通っています", "All tests pass")}/.test(document.body.innerText)`);
  await c.sleep(3000);
}
