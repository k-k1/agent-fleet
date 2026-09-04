// Pin down sending comments on a plan by driving the real build (console/dist) headless. A mock
// test that needs neither CP nor Docker (same skeleton as editor-mock.spec.ts).
//
// The failure it guards against: while an ExitPlanMode approval is pending the Agent reports
// status "permission", so /plan-respond answers no_plan; the Console then fell back to /input,
// which rejected it with permission_pending, and the comment reached the agent by neither path.
// The Console did not look at that failure and marked the comments as sent, which removed the
// send button so they could not be retyped.
//
// planComments.test.ts unit-tests deliverPlanComments, but only decides whether comments are
// folded away. Whether the card's button survives and a toast appears is user-visible and can
// only be checked by running the bundle.
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

// A copy of planKey / normalizePlan (console/src/features/mirror/planComments.ts), needed to
// seed localStorage directly and bypass the comment-collection UI (select then annotate in
// DocView). If it drifts from the implementation no comment appears on the card and the first
// expect fails.
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

/** Set up a Console holding exactly one claude session with one plan awaiting approval. */
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
    // The mirror's main poll. A plan awaiting approval arrives as pendingPlan rather than in
    // the transcript, because claude writes no tool_use until it is resolved.
    if (url.pathname === `/api/sessions/${SESSION}/messages`) {
      return route.fulfill({ json: { messages: [], cursor: 1, status: "idle", alive: true, pendingPlan: PLAN } });
    }
    if (url.pathname === `/api/sessions/${SESSION}/plan-respond`) {
      const r = routes.planRespond();
      return route.fulfill({ status: r.status, json: r.json });
    }
    // Free text goes to /turn; /input is the legacy fallback used only on 404/405 (client.ts
    // sessionTurn).
    if (url.pathname === `/api/sessions/${SESSION}/turn`) {
      const r = routes.turn();
      return route.fulfill({ status: r.status, json: r.json });
    }
    // Fail unmocked APIs instead of swallowing them, to catch a path typo or an API change.
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
      // One comment as collected on the review surface (DocView). The store is localStorage,
      // so it can be seeded here without going through the select-then-annotate UI.
      localStorage.setItem(
        lsKey as string,
        JSON.stringify({ [key as string]: [{ id: "c1", quote: "prebuild", nth: 0, body: comment as string, ts: 1 }] }),
      );
    },
    ["af.layout2..", "af.plan-comments", planKey(SESSION, PLAN), COMMENT] as const,
  );
  await page.goto(origin);
}

test("comments on a plan awaiting approval: if they do not arrive, keep the button and do not mark them sent", async ({ page }) => {
  const calls: { path: string; body: unknown }[] = [];
  // The first round reproduces the failure (the Agent does not recognise the approval dialog as
  // a plan, so no_plan, and /input is refused with permission_pending); the second is the fixed
  // behaviour, where /plan-respond accepts and carries the body.
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

  // The awaiting-approval card appears, with a send button carrying one unsent comment.
  const card = page.locator(".mt-plan");
  await expect(card).toBeVisible();
  await expect(card.locator(".mt-plan-comment-body")).toHaveText(COMMENT);
  const send = card.locator(".mt-plan-send");
  await expect(send).toContainText("（1）");
  await expect(card.locator(".mt-plan-approve")).toContainText("承認して実行");
  await expect(card.locator(".mt-plan-reject")).toContainText("却下");

  // --- when it does not arrive ---
  await send.click();
  // The reason for the refusal reaches the user as-is (wording that points at the permission
  // card). Stacking a generic "could not send" on top would bury the specific reason, so there
  // must be exactly one toast.
  await expect(page.getByRole("alert")).toHaveCount(1);
  await expect(page.getByRole("alert")).toContainText("許可の判断待ち");
  // The point: unless the comments stay unsent and the button keeps its count, they cannot be
  // retyped.
  await expect(card.locator(".mt-plan-comment-sent")).toHaveCount(0);
  await expect(send).toContainText("（1）");
  await expect(card.locator(".mt-plan-comment.sent")).toHaveCount(0);
  // Sending free text while the approval dialog is open is the silent-approval path. Pin that
  // free text is only ever sent as a fallback after plan-respond refused.
  expect(calls.map((c) => c.path)).toEqual([
    `/api/sessions/${SESSION}/plan-respond`,
    `/api/sessions/${SESSION}/turn`,
  ]);

  // --- when it does arrive ---
  planRespondFails = false;
  calls.length = 0;
  await send.click();
  await expect(card.locator(".mt-plan-comment-sent")).toHaveText("送信済み");
  await expect(card.locator(".mt-plan-send")).toHaveCount(0); // nothing unsent -> button is gone
  // The accepted path uses no free text at all; the body must not be swallowed by the modal.
  expect(calls.map((c) => c.path)).toEqual([`/api/sessions/${SESSION}/plan-respond`]);
  expect((calls[0].body as { decision: string; feedback: string }).decision).toBe("reject");
  expect((calls[0].body as { decision: string; feedback: string }).feedback).toContain(COMMENT);
});
