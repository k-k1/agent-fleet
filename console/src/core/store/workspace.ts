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

interface WorkspaceStore {
  state: string;
  /** Live boot-install phase surfaced by the CP during a native rootfs first
   * start (docs/35 §35.9-9): the latest "[entrypoint] …" line, e.g.
   * "boot-install (pinned): claude-code@…". "" = no boot in progress. The
   * starting dialog shows it so the user sees the pinned-CLI download instead of
   * a silent multi-minute wait. */
  bootPhase: string;
  /** The running container/agent predates the deployed backend: a stop→start would
   * swap in newer code (CP-side detection — control-plane/workspace_stale.go). The
   * CP only sets it while running, and clears it on its own once the workspace comes
   * back on the current build, so this is a STATE (WS-bar 要再起動 badge), not an
   * event. False whenever the CP can't tell — never guessed client-side. */
  stale: boolean;
  /** なぜ state が読めなかったのかのエラーコード（"" = 正常に読めている）。"unknown" は
   * 「取得に失敗した」以上のことを言えず、招待前の super_admin には**理由の書かれていない
   * 「不明」と反応しない起動ボタン**だけが残る（招待済みでない人は NotProvisioned の面に
   * 降りるので、ここに来るのは最初のテナントを作る人だけ）。コードを残して、バーが
   * 「不明」ではなく実際の理由を出せるようにする。 */
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
   * guards this behind a warning dialog (設定 > 環境) and resets the layout first.
   * Returns the error message on failure (the caller toasts). */
  recreate(skipDirtyGuard?: boolean): Promise<string | null>;
  /** Deeper reset than recreate: wipe the whole home EXCEPT logins/connections
   * (repos, ~/.local, ~/.cache, dotfiles all go), then start fresh from the image.
   * For when something under home outside ~/repos is wedged. Returns the error
   * message on failure (the caller toasts). */
  cleanHome(skipDirtyGuard?: boolean): Promise<string | null>;
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
        // ゲートウェイ/CP 再起動中の一時的な 5xx（{error:{code:"http_5xx"}} — tenant.init
        // と同じ isTransientErr 判定）で "unknown"/stale=false に落とすと、running ゲートの
        // 配下ポーラーまで止まってしまう — 現在値を保持して次のポーリングに任せる。ただし
        // 楽観 "…" の settle 中に保持すると busy が固着する（4s ポーリングは busy 中スキップ）
        // ので、その間と terminal エラーは従来どおり unknown へ落とす。
        if (isTransientErr(w) && !wsBusy(get().state)) return;
        set({ state: "unknown", bootPhase: "", stale: false, reason: String(w.error.code || "") });
        return;
      }
      set({ state: w.state || "unknown", bootPhase: w.bootPhase || "", stale: !!w.stale, reason: "" });
    } catch {
      set({ state: "unknown" }); // 通信断: 理由は前回のまま（次のポーリングで確定する）
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
    // the instant the process spawns (pid-alive, not health, docs/35 §35.9-9), so a
    // state refresh here would close the starting dialog while boot-install is still
    // downloading. bootPhase is the real "still booting" signal.
    const iv = setInterval(() => {
      void api("api/workspace")
        .then((w) => set({ bootPhase: w.bootPhase || "" }))
        .catch(() => {});
    }, 2000);
    // The poll skips while the state ends in "…", so even an aborted POST (e.g. a
    // gateway timeout on a slow ECS start) must settle to the real server state —
    // otherwise the bar sticks on 起動中… until a manual reload. api() only
    // rejects on network failure (HTTP errors come back as {error} JSON), so a
    // catch + unconditional refresh covers both.
    // ★ 起動できなかった理由は必ず出す。api() は HTTP エラーを {error} JSON で返して
    // reject しないので、戻り値を見ないと**何も起きなかったように見える**（楽観の
    // 「起動中…」が一瞬出て「不明」に戻るだけ）。招待前の super_admin が押したときの
    // 403 not_provisioned がまさにこれで、押しても無反応としか読めなかった。
    try {
      const r = await api("api/workspace/start", { method: "POST" });
      if (r?.error) toast(errText(r.error) || t("wsbar.start_failed"), { kind: "error" });
    } catch {
      /* ネットワーク断。下の refresh が実状態に落とす */
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
    let err: string | null = null;
    try {
      const res = await api("api/workspace/recreate", { method: "POST" });
      if (res && res.error) err = res.error.message || String(res.error);
    } catch {
      err = t("ui.recreate_failed");
    }
    await get().refresh();
    return err;
  },

  async cleanHome(skipDirtyGuard = false) {
    if (!skipDirtyGuard && !(await confirmDirtyNavigation("workspace_lifecycle"))) return null;
    set({ state: "recreating…" });
    let err: string | null = null;
    try {
      const res = await api("api/workspace/clean-home", { method: "POST" });
      if (res && res.error) err = res.error.message || String(res.error);
    } catch {
      err = t("ui.cleanup_failed");
    }
    await get().refresh();
    return err;
  },
}));

/** True while a start/stop transition is in flight (or state not yet fetched). */
export const wsBusy = (state: string): boolean => state.endsWith("…");

/** True when the workspace agent is up. Per-workspace pollers that proxy to the
 * agent (sessions/repos/stats/…) gate on this so a STOPPED workspace stops
 * generating 502s every few seconds (docs/35 §35.9-9; the ws-boot-view-stuck
 * running-gate, applied to the pollers themselves). Read imperatively in poll
 * loops via `wsRunning(useWorkspaceStore.getState().state)`. */
export const wsRunning = (state: string): boolean => state === "running";

/** True while starting the workspace would be wrong: an optimistic "…" transition
 * is in flight OR the server already reports "starting" (ECS cold pull, minutes).
 * The CP no-ops a re-Start anyway, but every start button disables on this so the
 * UI doesn't offer 起動 for a workspace that is already coming up. */
export const wsStartBusy = (state: string): boolean => wsBusy(state) || state === "starting";

/** True while the workspace is coming up and the starting dialog should show: an
 * optimistic "starting…"/"recreating…" transition, the server-reported "starting"
 * (ECS cold pull), OR a live boot phase (native rootfs boot-install — the process
 * is pid-alive so state already reads "running", but the agent is still installing
 * pinned CLIs, docs/35 §35.9-9). NOT "stopping…". */
export const wsPreparing = (state: string, bootPhase: string): boolean =>
  bootPhase !== "" || state === "starting…" || state === "starting" || state === "recreating…";

// Auto-sync every 4s so an externally-changed workspace (admin stop, OOM death,
// crash) reflects on its own. Skipped while hidden, while the push channel
// covers this stream (api/events — the poll is the fallback), or mid-transition
// (trailing "…" only — the server-reported "starting" keeps polling, which is
// what walks an ECS cold start to 稼働中 without a reload). Returns the cleanup,
// so the caller (App boot effect) is StrictMode-safe.
export function startWorkspacePolling(): () => void {
  const t = setInterval(() => {
    if (document.hidden || pushHealthy() || wsBusy(useWorkspaceStore.getState().state)) return;
    useWorkspaceStore.getState().refresh();
  }, 4000);
  return () => clearInterval(t);
}
