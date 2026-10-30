// "Suggest memories now" for one session (ADR 0117, "Review now"). The Agent starts the review in
// the background and answers at once (a review is a model call), so this asks it to start, then
// watches the session's review status until it is not running any more and says how it ended.
// The member-origin mark that lets this request through is put on it by the Control Plane.
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { t } from "../../lib/i18n/index.ts";
import type { MsgKey } from "../../lib/i18n/index.ts";

type Notify = (message: string, opts?: { kind?: "error" | "warn" | "info" | "success" }) => void;

interface ReviewStatus {
  session?: {
    running?: boolean;
    // How the last "Review now" ended (written every time one ends, even when there was nothing to
    // review): "done", "empty", "unreadable", or the code of the refusal that stopped it.
    // Totals over every window that review worked through (counts only).
    manual?: { at: string; outcome: string; windows?: number; queued?: number; failed?: number };
  };
}

const POLL_MS = 3000;
const POLL_MAX = 60;

// The Agent's error codes, and the refusal words its status uses, as member-facing text.
const REFUSAL: Record<string, MsgKey> = {
  memory_review_off: "srow.memory_review_off",
  disabled: "srow.memory_review_off",
  memory_review_busy: "srow.memory_review_busy",
  busy: "srow.memory_review_busy",
  daily_cap: "srow.memory_review_daily_cap",
  queue_full: "srow.memory_review_queue_full",
  memory_review_no_backend: "srow.memory_review_no_backend",
  no_backend: "srow.memory_review_no_backend",
};

const statusPath = (name: string) => "api/agents/memory-review?session=" + encodeURIComponent(name);

export async function reviewSessionNow(name: string, notify: Notify, sleep: (ms: number) => Promise<void> = delay): Promise<void> {
  const before: ReviewStatus | null = await api(statusPath(name)).catch(() => null);
  const prevAt = before?.session?.manual?.at ?? "";
  const res = await apiJSON("api/agents/memory-review/run?session=" + encodeURIComponent(name), "POST", { session: name }).catch(
    () => ({ error: { code: "unreachable" } }),
  );
  if (res?.error) {
    const key = REFUSAL[res.error.code ?? ""];
    notify(key ? t(key) : t("srow.memory_review_unknown") + ": " + errText(res.error));
    return;
  }
  notify(t("srow.memory_review_started"), { kind: "info" });
  for (let i = 0; i < POLL_MAX; i++) {
    await sleep(POLL_MS);
    const st: ReviewStatus | null = await api(statusPath(name)).catch(() => null);
    const s = st?.session;
    if (!s || s.running) continue;
    if (!s.manual || s.manual.at === prevAt) continue; // this review has not ended yet
    const queued = s.manual.queued ?? 0;
    // What the windows before a refusal already queued is not lost with it.
    if (queued > 0) notify(t("srow.memory_review_queued", { n: queued }), { kind: "success" });
    if (s.manual.outcome === "empty") {
      notify(t("srow.memory_review_empty"), { kind: "info" });
      return;
    }
    if (s.manual.outcome === "unreadable") {
      notify(t("srow.memory_review_unreadable"), { kind: "warn" });
      return;
    }
    if (s.manual.outcome !== "done") {
      notify(t(REFUSAL[s.manual.outcome] ?? "srow.memory_review_unknown"));
      return;
    }
    // Some windows may have got nothing usable, whatever the others queued.
    if ((s.manual.failed ?? 0) > 0) notify(t("srow.memory_review_failed"), { kind: "warn" });
    else if (queued === 0) notify(t("srow.memory_review_none"), { kind: "info" });
    return;
  }
  // The watch ends before a long review does (up to 10 windows, each its own helper call): say so
  // rather than leave "Review started" as the last word. The result still lands in the proposals.
  notify(t("srow.memory_review_still_running"), { kind: "info" });
}

const delay = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));
