// Workspace lifecycle store (zustand). Replaces the workspace slice of the old
// God-context: the per-membership container's state plus start/stop.
//
// state values come from the CP ("running" / "starting" / "stopped" / "none");
// "starting" is a real server state (ECS: the workspace image cold-pulls for
// minutes) — the 4s poll keeps following it and flips to "running" on its own,
// no manual reload. "unknown" = fetch failed, and a trailing "…" marks an
// optimistic in-flight transition (the old convention — pollers and buttons
// treat it as busy and keep their hands off).
import { create } from "zustand";
import { api, errText, isTransientErr } from "../api/client.ts";
import { toast } from "../../ui/toast.ts";
import { pushHealthy, pushStamp } from "../push/events.ts";
import { t } from "../../lib/i18n/index.ts";
import { confirmDirtyNavigation } from "../../features/editor/dirtyRegistry.ts";

/** A destructive lifecycle call (recreate / clean home) that did not complete. `untouched` means
 * the CP refused before it stopped anything, so every view still points at a live workspace and
 * must not be reset. */
export interface LifecycleFailure {
  message: string;
  untouched: boolean;
}

// The refusals the CP sends before it stops the workspace (control-plane/workspace_handlers.go):
// not available on this deployment, another lifecycle operation holding the lease, and a stop
// that failed while the workspace kept running.
const UNTOUCHED_CODES = new Set(["home_wipe_unsupported", "workspace_operation_in_progress", "stop_failed"]);

// lifecycleFailure turns a lifecycle POST's answer into the caller's failure, or null when it
// succeeded. The code picks the localized wording (errText); a code with no catalog entry keeps
// the server's message.
function lifecycleFailure(res: any): LifecycleFailure | null {
  if (!res || !res.error) return null;
  const code = typeof res.error === "object" ? res.error.code : undefined;
  return { message: errText(res.error), untouched: typeof code === "string" && UNTOUCHED_CODES.has(code) };
}

interface WorkspaceStore {
  state: string;
  /** Live boot-install phase surfaced by the CP during a native rootfs first
   * start (docs/log/35 §35.9-9): the latest "[entrypoint] …" line, e.g.
   * "boot-install (pinned): claude-code@…". "" = no boot in progress. The
   * starting dialog shows it so the user sees the pinned-CLI download instead of
   * a silent multi-minute wait. */
  bootPhase: string;
  /** The running container/agent predates the deployed backend: a stop→start would
   * swap in newer code (CP-side detection — control-plane/workspace_stale.go). The
   * CP only sets it while running, and clears it on its own once the workspace comes
   * back on the current build, so this is a STATE (the WS-bar restart-needed badge), not an
   * event. False whenever the CP can't tell — never guessed client-side. */
  stale: boolean;
  /** Error code explaining why the state could not be read ("" = read fine). "unknown" says
   * no more than "the fetch failed", which leaves a not-yet-invited super_admin with an
   * unexplained "unknown" and a start button that does nothing (anyone not invited lands on
   * NotProvisioned instead, so only the person creating the first tenant gets here). Keeping
   * the code lets the bar show the real reason. */
  reason: string;
  refresh(): Promise<void>;
  /** Apply a pushed workspace payload (api/events). Poll parity: an optimistic
   * "…" transition is never clobbered — while busy only bootPhase updates (the
   * same thing start()'s transient 2s poll does); the settle refresh() after the
   * POST is what clears the busy state. */
  applyPush(w: { state?: string; bootPhase?: string; stale?: boolean }): void;
  start(): Promise<void>;
  stop(): Promise<void>;
  /** Stop then start, keeping everything on disk — how a backend update is applied
   * (stale). NOT recreate: repos and uncommitted work stay. Sessions stop and are
   * resumable, so the caller confirms first. */
  restart(): Promise<void>;
  /** Tear the container down and start fresh from the current image. Logins +
   * connections persist; cloned repos and running sessions are wiped — the caller
   * guards this behind a warning dialog (Settings > Danger zone) and resets the layout
   * afterwards. Returns the failure (the caller toasts), or null. */
  recreate(skipDirtyGuard?: boolean): Promise<LifecycleFailure | null>;
  /** Deeper reset than recreate: wipe the whole home EXCEPT logins/connections
   * (repos, ~/.local, ~/.cache, dotfiles all go), then start fresh from the image.
   * For when something under home outside ~/repos is wedged. Returns the failure
   * (the caller toasts), or null. */
  cleanHome(skipDirtyGuard?: boolean): Promise<LifecycleFailure | null>;
}

export const useWorkspaceStore = create<WorkspaceStore>((set, get) => ({
  state: "…",
  bootPhase: "",
  stale: false,
  reason: "",

  async refresh() {
    const stamp = pushStamp("workspace");
    try {
      const w = await api("api/workspace");
      // A pushed frame that arrived while this fetch was in flight is at least as
      // fresh — don't let a slow (mobile) response clobber it and stick until the
      // next server-side change. The busy settle path is exempt: applyPush never
      // clears an optimistic "…", so the settle refresh must always land.
      if (pushStamp("workspace") !== stamp && !wsBusy(get().state)) return;
      if (w?.error) {
        // Dropping to "unknown"/stale=false on a transient 5xx while the gateway or CP
        // restarts ({error:{code:"http_5xx"}}, the same isTransientErr judgement as
        // tenant.init) would also stop every poller gated on running — keep the current
        // value and let the next poll decide. Holding it while an optimistic "…" settles
        // would wedge busy instead (the 4s poll skips while busy), so during that, and on a
        // terminal error, fall to unknown as before.
        if (isTransientErr(w) && !wsBusy(get().state)) return;
        set({ state: "unknown", bootPhase: "", stale: false, reason: String(w.error.code || "") });
        return;
      }
      set({ state: w.state || "unknown", bootPhase: w.bootPhase || "", stale: !!w.stale, reason: "" });
    } catch {
      set({ state: "unknown" }); // network drop: keep the previous reason; the next poll settles it
    }
  },

  applyPush(w) {
    const cur = get().state;
    if (wsBusy(cur)) {
      if (cur === "starting…" || cur === "recreating…") set({ bootPhase: w.bootPhase || "" });
      return;
    }
    set({ state: w.state || "unknown", bootPhase: w.bootPhase || "", stale: !!w.stale });
  },

  async start() {
    set({ state: "starting…", bootPhase: "" });
    // While the (blocking) start POST is in flight, poll bootPhase ONLY — never the
    // state — so the optimistic "starting…" holds. Native State() reports "running"
    // the instant the process spawns (pid-alive, not health, docs/log/35 §35.9-9), so a
    // state refresh here would close the starting dialog while boot-install is still
    // downloading. bootPhase is the real "still booting" signal.
    const iv = setInterval(() => {
      void api("api/workspace")
        .then((w) => set({ bootPhase: w.bootPhase || "" }))
        .catch(() => {});
    }, 2000);
    // The poll skips while the state ends in "…", so even an aborted POST (e.g. a
    // gateway timeout on a slow ECS start) must settle to the real server state —
    // otherwise the bar sticks on the starting label (「起動中…」) until a manual reload.
    // api() only rejects on network failure (HTTP errors come back as {error} JSON), so a
    // catch + unconditional refresh covers both.
    // Always surface why the start failed. Since api() returns HTTP errors as {error} JSON
    // instead of rejecting, ignoring the return value makes it look like nothing happened:
    // the optimistic "starting…" flashes and falls back to "unknown". That is exactly what a
    // not-yet-invited super_admin saw on a 403 not_provisioned — a button with no reaction.
    try {
      const r = await api("api/workspace/start", { method: "POST" });
      if (r?.error) toast(errText(r.error) || t("wsbar.start_failed"), { kind: "error" });
    } catch {
      /* network drop; the refresh below settles the real state */
    }
    clearInterval(iv);
    await get().refresh();
  },

  async stop() {
    if (!(await confirmDirtyNavigation("workspace_lifecycle"))) return;
    // Optimistic transition so the toggle goes inert (busy = trailing "…") and the
    // poll skips mid-stop — otherwise a second click re-issues the stop / a poll
    // clobbers the state during the multi-second docker stop.
    set({ state: "stopping…" });
    try {
      await api("api/workspace/stop", { method: "POST" });
    } catch {
      /* settled by the refresh below */
    }
    await get().refresh();
  },

  // Reuses the plain stop/start endpoints — there is no "restart" route, and there
  // must not be a recreate here: recreate wipes ~/repos, which applying a backend
  // update must never do. The dirty guard runs once, up front, so the user isn't
  // asked twice mid-restart; start() then owns the optimistic state + boot polling.
  async restart() {
    if (!(await confirmDirtyNavigation("workspace_lifecycle"))) return;
    set({ state: "stopping…" });
    try {
      await api("api/workspace/stop", { method: "POST" });
    } catch {
      /* settled by start()'s refresh below */
    }
    await get().start();
  },

  async recreate(skipDirtyGuard = false) {
    if (!skipDirtyGuard && !(await confirmDirtyNavigation("workspace_lifecycle"))) return null;
    set({ state: "recreating…" });
    let err: LifecycleFailure | null = null;
    try {
      err = lifecycleFailure(await api("api/workspace/recreate", { method: "POST" }));
    } catch {
      err = { message: t("ui.recreate_failed"), untouched: false };
    }
    await get().refresh();
    return err;
  },

  async cleanHome(skipDirtyGuard = false) {
    if (!skipDirtyGuard && !(await confirmDirtyNavigation("workspace_lifecycle"))) return null;
    set({ state: "recreating…" });
    let err: LifecycleFailure | null = null;
    try {
      err = lifecycleFailure(await api("api/workspace/clean-home", { method: "POST" }));
    } catch {
      err = { message: t("ui.cleanup_failed"), untouched: false };
    }
    await get().refresh();
    return err;
  },
}));

/** True while a start/stop transition is in flight (or state not yet fetched). */
export const wsBusy = (state: string): boolean => state.endsWith("…");

/** True when the workspace agent is up. Per-workspace pollers that proxy to the
 * agent (sessions/repos/stats/…) gate on this so a STOPPED workspace stops
 * generating 502s every few seconds (docs/log/35 §35.9-9; the ws-boot-view-stuck
 * running-gate, applied to the pollers themselves). Read imperatively in poll
 * loops via `wsRunning(useWorkspaceStore.getState().state)`. */
export const wsRunning = (state: string): boolean => state === "running";

/** True while starting the workspace would be wrong: an optimistic "…" transition
 * is in flight OR the server already reports "starting" (ECS cold pull, minutes).
 * The CP no-ops a re-Start anyway, but every start button disables on this so the
 * UI doesn't offer Start for a workspace that is already coming up. */
export const wsStartBusy = (state: string): boolean => wsBusy(state) || state === "starting";

/** True when the power toggle must STOP rather than start.
 *
 * Deliberately NOT `state === "running"`. The server-reported "starting" has no
 * guaranteed exit: an ECS task the scheduler cannot place sits at desired=1/running=0
 * and State() reports "starting" forever (measured on the dev deployment — docs/log/70 §70.14.6,
 * a task definition declaring ARM64 while pinned to an x86_64 slot). While the toggle
 * sent START on everything that was not "running", the two exits the UI offered from
 * that state were both "start", and the CP no-ops a Start for a workspace it already
 * considers `starting`. There was no way to stop it from the Console at all.
 *
 * Cancelling a start is a legitimate operation on its own merits, so this is the rule
 * even where a start does converge. wsStartBusy still guards every START affordance —
 * that is what keeps a second Start from re-driving a deployment. */
export const wsPowerStops = (state: string): boolean => state === "running" || state === "starting";

/** True while the workspace is coming up and the starting dialog should show: an
 * optimistic "starting…"/"recreating…" transition, the server-reported "starting"
 * (ECS cold pull), OR a live boot phase (native rootfs boot-install — the process
 * is pid-alive so state already reads "running", but the agent is still installing
 * pinned CLIs, docs/log/35 §35.9-9). NOT "stopping…". */
export const wsPreparing = (state: string, bootPhase: string): boolean =>
  bootPhase !== "" || state === "starting…" || state === "starting" || state === "recreating…";

// Auto-sync every 4s so an externally-changed workspace (admin stop, OOM death,
// crash) reflects on its own. Skipped while hidden, while the push channel
// covers this stream (api/events — the poll is the fallback), or mid-transition
// (trailing "…" only — the server-reported "starting" keeps polling, which is
// what walks an ECS cold start to running without a reload). Returns the cleanup,
// so the caller (App boot effect) is StrictMode-safe.
export function startWorkspacePolling(): () => void {
  const t = setInterval(() => {
    if (document.hidden || pushHealthy() || wsBusy(useWorkspaceStore.getState().state)) return;
    useWorkspaceStore.getState().refresh();
  }, 4000);
  return () => clearInterval(t);
}
