// プランへのコメント送信を、実ビルド（console/dist）を headless で動かして固定する。
// CP も Docker も要らない mock 系（editor-mock.spec.ts と同じ骨格）。
//
// 守りたいのは 2026-08-10 の実障害:
//   - Agent 側: ExitPlanMode の承認中は status が "permission" に化けるため
//     /plan-respond が no_plan を返し、Console が /input へ落ちて permission_pending
//     で弾かれ、コメントがどちらの経路でも届かなかった。
//   - Console 側: その失敗を見ずにコメントを「送信済み」へ倒していたので、送信ボタン
//     ごと消えて打ち直せなくなった。
// deliverPlanComments の単体テストは planComments.test.ts にあるが、そこは「畳むか
// どうか」しか見ない。カードのボタンが実際に残るか・トーストが出るかという利用者から
// 見える側は、バンドルを動かさないと確かめられない。
import fs from "node:fs";
import http from "node:http";
import path from "node:path";
import { test, expect, type Page } from "@playwright/test";

const root = path.resolve(__dirname, "../..");
const dist = path.join(root, "console", "dist");
let server: http.Server;
let origin = "";

const SESSION = "s1mock";
const PLAN = "# ビルド失敗の修正\n\nprebuild を直す。\n";
const COMMENT = "build-stgが動くようにして";

// planKey / normalizePlan の写し（console/src/features/mirror/planComments.ts）。
// localStorage を直に仕込んでコメント蓄積 UI（DocView の選択→注釈）を迂回するために
// 必要。実装とズレたらカードにコメントが出ず、最初の expect で落ちる。
function planKey(session: string, plan: string): string {
  const norm = plan
    .split("\n")
    .map((l) => l.replace(/\s+$/, ""))
    .join("\n")
    .trim();
  let h = 0x811c9dc5;
  for (let i = 0; i < norm.length; i++) {
    h ^= norm.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return session + ":" + (h >>> 0).toString(36);
}

test.beforeAll(async () => {
  server = http.createServer((req, res) => {
    const pathname = new URL(req.url || "/", "http://local").pathname;
    const relative = pathname === "/" ? "index.html" : pathname.replace(/^\/+/, "");
    const file = path.join(dist, relative);
    if (!file.startsWith(dist) || !fs.existsSync(file) || fs.statSync(file).isDirectory()) {
      res.writeHead(404).end();
      return;
    }
    const type = file.endsWith(".html")
      ? "text/html"
      : file.endsWith(".js")
        ? "text/javascript"
        : file.endsWith(".css")
          ? "text/css"
          : "application/octet-stream";
    res.writeHead(200, { "content-type": type });
    fs.createReadStream(file).pipe(res);
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  origin = `http://127.0.0.1:${(server.address() as { port: number }).port}`;
});

test.afterAll(async () => {
  if (!server) return;
  await new Promise<void>((resolve) => server.close(() => resolve()));
});

/** 承認待ちのプランを 1 件抱えた claude セッションを 1 つだけ持つ Console を用意する。 */
async function openMirrorWithPendingPlan(page: Page, calls: { path: string; body: unknown }[], routes: {
  planRespond: () => { status: number; json: unknown };
  turn: () => { status: number; json: unknown };
}) {
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const method = route.request().method();
    if (method === "POST") calls.push({ path: url.pathname, body: route.request().postDataJSON() });
    if (url.pathname === "/api/whoami") return route.fulfill({ json: { auth_mode: "dev" } });
    if (url.pathname === "/api/tenants") return route.fulfill({ json: { tenants: [] } });
    if (url.pathname === "/api/workspace") return route.fulfill({ json: { state: "running" } });
    if (url.pathname === "/api/sessions") {
      return route.fulfill({
        json: { sessions: [{ name: SESSION, kind: "claude", alive: true, state: "plan", dir: "/repo", repo: "repo" }] },
      });
    }
    // ミラーの本体ポーリング。承認待ちのプランは転写ではなく pendingPlan で来る
    // （claude は解決するまで tool_use を書かないため）。
    if (url.pathname === `/api/sessions/${SESSION}/messages`) {
      return route.fulfill({ json: { messages: [], cursor: 1, status: "idle", alive: true, pendingPlan: PLAN } });
    }
    if (url.pathname === `/api/sessions/${SESSION}/plan-respond`) {
      const r = routes.planRespond();
      return route.fulfill({ status: r.status, json: r.json });
    }
    // 自由文は /turn（/input は 404/405 のときだけのレガシー退避 — client.ts sessionTurn）。
    if (url.pathname === `/api/sessions/${SESSION}/turn`) {
      const r = routes.turn();
      return route.fulfill({ status: r.status, json: r.json });
    }
    // モックしていない API は握り潰さず落とす（path のタイポ／API 変更の検知）。
    return route.abort();
  });
  await page.addInitScript(
    ([layoutKey, lsKey, key, comment]) => {
      sessionStorage.setItem(
        layoutKey as string,
        JSON.stringify({
          cols: [
            {
              id: "c1",
              rowRatio: 1,
              panes: [{ id: "p1", session: "s1mock", wrap: null, content: { kind: "terminal", chat: true } }],
            },
          ],
          colRatios: [1],
          activeId: "p1",
        }),
      );
      // レビュー面（DocView）で溜めたコメント 1 件。ストアの実体は localStorage なので、
      // 選択→注釈の UI を経由せずここから積める。
      localStorage.setItem(
        lsKey as string,
        JSON.stringify({ [key as string]: [{ id: "c1", quote: "prebuild", nth: 0, body: comment as string, ts: 1 }] }),
      );
    },
    ["af.layout2..", "af.plan-comments", planKey(SESSION, PLAN), COMMENT] as const,
  );
  await page.goto(origin);
}

test("承認待ちプランへのコメント: 届かなければ送信済みにせずボタンを残す", async ({ page }) => {
  const calls: { path: string; body: unknown }[] = [];
  // 1 回目は実障害の再現（Agent が承認ダイアログを plan と認識できず no_plan → /input も
  // permission_pending で拒否）、2 回目は修正後（/plan-respond が受理して本文も届く）。
  let planRespondFails = true;
  await openMirrorWithPendingPlan(page, calls, {
    planRespond: () =>
      planRespondFails
        ? { status: 409, json: { error: { code: "no_plan", message: "no pending plan approval" } } }
        : { status: 200, json: { responded: SESSION, decision: "reject", feedback_delivered: true } },
    turn: () => ({
      status: 409,
      json: { error: { code: "permission_pending", message: "a permission prompt is awaiting a decision" } },
    }),
  });

  // 承認待ちカードと、未送信 1 件を抱えた送信ボタンが出る。
  const card = page.locator(".mt-plan");
  await expect(card).toBeVisible();
  await expect(card.locator(".mt-plan-comment-body")).toHaveText(COMMENT);
  const send = card.locator(".mt-plan-send");
  await expect(send).toContainText("（1）");
  await expect(card.locator(".mt-plan-approve")).toContainText("承認して実行");
  await expect(card.locator(".mt-plan-reject")).toContainText("却下");

  // --- 届かなかったとき ---
  await send.click();
  // 拒否の理由がそのまま利用者に出る（許可カードへ誘導する文言）。汎用の「送信できません
  // でした」を重ねると具体的な理由が埋もれるので、トーストはちょうど 1 枚であること。
  await expect(page.getByRole("alert")).toHaveCount(1);
  await expect(page.getByRole("alert")).toContainText("許可の判断待ち");
  // ここが実障害の芯: 送信済みにせず、ボタンも（1）のまま残っていなければ打ち直せない。
  await expect(card.locator(".mt-plan-comment-sent")).toHaveCount(0);
  await expect(send).toContainText("（1）");
  await expect(card.locator(".mt-plan-comment.sent")).toHaveCount(0);
  // 承認ダイアログが開いたまま自由文を投げるのは「無言の承認」経路。plan-respond が
  // 断ったあとのフォールバックとしてしか自由文を送っていないことを固定する。
  expect(calls.map((c) => c.path)).toEqual([
    `/api/sessions/${SESSION}/plan-respond`,
    `/api/sessions/${SESSION}/turn`,
  ]);

  // --- 届いたとき ---
  planRespondFails = false;
  calls.length = 0;
  await send.click();
  await expect(card.locator(".mt-plan-comment-sent")).toHaveText("送信済み");
  await expect(card.locator(".mt-plan-send")).toHaveCount(0); // 未送信ゼロ → ボタンは消える
  // 受理された経路では自由文を一切使わない（本文はモーダルに飲まれてはならない）。
  expect(calls.map((c) => c.path)).toEqual([`/api/sessions/${SESSION}/plan-respond`]);
  expect((calls[0].body as { decision: string; feedback: string }).decision).toBe("reject");
  expect((calls[0].body as { decision: string; feedback: string }).feedback).toContain(COMMENT);
});
