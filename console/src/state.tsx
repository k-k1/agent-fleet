import { createContext, useContext, useState, useEffect, useCallback, useRef } from "react";
import type { ReactNode } from "react";
import { api, getTenant, setTenant as persistTenant } from "./api.js";
import { keepOnly as termKeepOnly, reconnectSession as termReconnectSession } from "./term.js";
import { hydrateUIPrefs } from "./lib/settings.js";
import { MOBILE_QUERY, mobileMatches } from "./lib/device.js";
import { setChatSeed } from "./lib/chatSeed.js";
import { useToast } from "./components/ToastProvider.jsx";
import type { Layout, Pane, Column } from "./types/layout.ts";
import type { Session } from "./types/session.ts";
import type { AppState, Whoami, Tenant, Ocweb, PanePatch, Reveal } from "./types/app.ts";

// AppContext holds everything shared across the top bar, WS bar, left pane, main
// area, and settings dialog: identity, tenant selection, workspace state, the
// main-area pane layout, and refresh signals that let one section trigger another
// to refetch (e.g. clone-then-start refreshes repos).
//
// The main area is a "layout" of up to 4 columns shown side by side; each column
// can itself be split top/bottom into 2 rows — so up to 8 panes. Each pane shows a
// terminal, the source-control workbench, or a file viewer, independently. Clicking
// an item in the left navigator opens it in the ACTIVE pane. Back-compat selectors
// (mode/scmRepo/filePath/session) project the active pane so existing components
// keep working.
//
//   layout = {
//     cols: [ { id, rowRatio, panes: [pane] | [pane, pane] }, ... ],  // 1–4 columns
//     colRatios: number[],   // column width fractions, sums to 1, len == cols.length
//     activeId,              // id of the active pane (click target / key target)
//   }
const AppContext = createContext<AppState | null>(null);
// useApp is always called within AppProvider, so the value is non-null in practice.
export const useApp = (): AppState => useContext(AppContext) as AppState;

// A pane descriptor. kind drives which view renders; the *Path/Repo/session fields
// are the per-kind payload. Empty terminal pane = "セッション未接続".
function blankPane(id: string, patch?: PanePatch): Pane {
  return { id, kind: "terminal", session: null, chat: false, filePath: null, scmRepo: null, commitSha: null, docTitle: null, docContent: null, diffTool: null, diffEdits: null, conversationId: null, draftAssistantId: null, wrap: null, ...patch };
}

// An empty terminal pane — no session, no file, no SCM target. Closing a pane's content
// blanks it to this; closing an already-blank pane is what actually removes it.
const isBlankPane = (p: Pane): boolean => p.kind === "terminal" && !p.session && !p.filePath && !p.scmRepo;

const equalRatios = (n: number): number[] => Array(n).fill(1 / n);
const MAX_COLS = 4;

// localStorage key for the persisted pane layout, namespaced per tenant so one
// tenant's sessions never leak into another's restored split.
const LKEY = (slug: string): string => "af.layout." + (slug || "");

const initialLayout: Layout = {
  cols: [{ id: "c0", rowRatio: 0.5, panes: [blankPane("p0")] }],
  colRatios: [1],
  activeId: "p0",
};

export function AppProvider({ children }: { children: ReactNode }) {
  const toast = useToast();
  const [whoami, setWhoami] = useState<Whoami | null>(null); // { email, user, ... }
  const [tenants, setTenants] = useState<Tenant[]>([]); // [{ slug, name, role }]
  const [tenant, setTenantState] = useState(getTenant());
  const tenantRef = useRef(tenant);
  tenantRef.current = tenant;
  const [showPicker, setShowPicker] = useState(false);
  const [superAdmin, setSuperAdmin] = useState(false);

  const [wsState, setWsState] = useState("…");
  const [chatListKey, setChatListKey] = useState(0); // bump → AssistantSection refreshes its list

  // opencode web (per-workspace pk-webui) status: {available,enabled,running,port}
  // or null when unavailable/unreachable. Shared so the WS bar surfaces an "open"
  // entry while the toggle lives in 設定 > エージェント — both read/write this.
  const [ocweb, setOcweb] = useState<Ocweb | null>(null);

  // The main-area pane layout. Mirrored into browser history (back/forward).
  const [layout, setLayout] = useState(initialLayout);
  const layoutRef = useRef(layout);
  layoutRef.current = layout;
  const paneSeq = useRef(1); // next pane id suffix (p0 already used)
  const colSeq = useRef(1); // next column id suffix (c0 already used)
  const newPaneId = () => `p${paneSeq.current++}`;
  const newColId = () => `c${colSeq.current++}`;
  const hydrated = useRef(false); // becomes true once the saved layout is loaded

  // Tear down terminal instances whose pane no longer exists (pane closed, browser
  // back, tenant switch). Each pane keeps its xterm alive while the pane exists —
  // even while showing a file/scm view — so the terminal (and its scrollback +
  // socket) survives view switches, exactly like the original single-terminal app.
  // Runs after every layout change; cheap (a set diff over a handful of panes).
  useEffect(() => {
    termKeepOnly(layout.cols.flatMap((c) => c.panes.map((p) => p.id)));
  }, [layout]);

  // Persist the layout per tenant so a reload / re-login restores the same split.
  // Gated on `hydrated` so the initial single-pane render doesn't clobber the saved
  // layout before loadLayout() has read it. Reads tenantRef so it always writes
  // under the currently-active tenant.
  useEffect(() => {
    if (!hydrated.current) return;
    try {
      localStorage.setItem(LKEY(tenantRef.current), JSON.stringify(layout));
    } catch {}
  }, [layout]);

  // commit applies a new layout and (by default) pushes a browser history entry, so
  // the browser's back/forward — and the mouse back button — traverse the views you
  // visited. We keep the URL unchanged (state-only pushState): the Console lives
  // behind a path-stripping proxy, so putting paths in the URL would break reloads /
  // the base path. History therefore restores within a session, not across reloads.
  const commit = useCallback((next: Layout, push = true) => {
    if (push && JSON.stringify(next) === JSON.stringify(layoutRef.current)) return; // no dup entry
    setLayout(next);
    if (push) {
      try {
        history.pushState({ __af: true, layout: next }, "");
      } catch {}
    }
  }, []);

  // patchActive returns a new layout with the active pane shallow-merged with patch.
  const patchActive = useCallback((patch: PanePatch, push = true) => {
    const cur = layoutRef.current;
    const cols = cur.cols.map((c) => ({
      ...c,
      panes: c.panes.map((p) => (p.id === cur.activeId ? { ...p, ...patch } : p)),
    }));
    commit({ ...cur, cols }, push);
  }, [commit]);

  // setPaneWrap toggles line-wrapping for one pane (by id) — a per-pane override of the
  // global wrap setting. Not pushed to history (a wrap toggle isn't a navigation step).
  const setPaneWrap = useCallback((id: string, wrap: boolean | null) => {
    const cur = layoutRef.current;
    const cols = cur.cols.map((c) => ({
      ...c,
      panes: c.panes.map((p) => (p.id === id ? { ...p, wrap } : p)),
    }));
    commit({ ...cur, cols }, false);
  }, [commit]);

  // shows returns true if pane already displays exactly the target described by patch
  // (same kind + identity). Used to avoid showing one thing in both split panes.
  const shows = (pane: Pane | undefined, patch: PanePatch): boolean => {
    if (!pane || pane.kind !== patch.kind) return false;
    if (patch.kind === "terminal") return patch.session !== undefined && pane.session === patch.session;
    if (patch.kind === "file") return pane.filePath === patch.filePath;
    if (patch.kind === "scm") return pane.scmRepo === patch.scmRepo;
    if (patch.kind === "changes") return pane.scmRepo === patch.scmRepo;
    if (patch.kind === "commit") return pane.scmRepo === patch.scmRepo && pane.commitSha === patch.commitSha;
    if (patch.kind === "doc") return pane.docTitle === patch.docTitle;
    if (patch.kind === "diff") return pane.docTitle === patch.docTitle && pane.diffEdits === patch.diffEdits;
    if (patch.kind === "chat") {
      // A conversation is identified by its id; a not-yet-created draft by its assistant.
      // Lets ctrl/middle-click "open in a split" focus an existing chat/draft instead of
      // duplicating it (docs/19).
      if (patch.conversationId) return pane.conversationId === patch.conversationId;
      if (patch.draftAssistantId) return pane.draftAssistantId === patch.draftAssistantId;
      return false;
    }
    return false;
  };

  // openActive opens the target (patch) in the active pane. When split and the OTHER
  // pane already shows exactly that target, it would duplicate — so instead the two
  // panes swap contents (the target slides to the active side, the active pane's old
  // content slides to the other side), keeping ids + which side is active. Otherwise
  // it's a plain patch of the active pane.
  const openActive = useCallback(
    (patch: PanePatch) => {
      const cur = layoutRef.current;
      const all = cur.cols.flatMap((c) => c.panes);
      const active = all.find((p) => p.id === cur.activeId);
      const other = all.find((p) => p.id !== cur.activeId && shows(p, patch));
      if (other && active) {
        const cols = cur.cols.map((c) => ({
          ...c,
          panes: c.panes.map((p) =>
            p.id === cur.activeId
              // Honor the patch's view intent when swapping in the existing pane:
              // showTerminal (chat:false) must land on the terminal even if the
              // session is currently shown as chat in `other`, and showChat
              // (chat:true) the reverse. Without this the swap kept other.chat, so
              // clicking an alive session already open as chat elsewhere left it in
              // chat — its terminal never appeared. Non-terminal patches omit chat,
              // so it falls back to other.chat (unchanged).
              ? { ...other, id: cur.activeId, chat: patch.chat ?? other.chat }
              : p.id === other.id
                ? { ...active, id: other.id }
                : p,
          ),
        }));
        commit({ ...cur, cols });
        return;
      }
      patchActive(patch);
    },
    [commit, patchActive],
  );

  // openInNewPane creates a fresh pane and opens `patch` in it (made active) in a
  // SINGLE commit — splitting in one call then opening in another would read a stale
  // layoutRef and patch the old active pane instead. It grows to the full 4×2 = 8
  // panes: first fills columns (up to MAX_COLS), then splits any single-pane column
  // downward (the active one first, else the leftmost) until every column holds 2.
  // Once all 8 slots are used it overwrites the LAST pane (bottom of the rightmost
  // column) so a further open still lands somewhere instead of being capped.
  const openInNewPane = useCallback(
    (patch: PanePatch, force = false) => {
      const cur = layoutRef.current;
      // Already shown somewhere? A Ctrl/middle-click "open in a split" on a target
      // that's already visible (a session, file, repo/scm/changes/commit…) shouldn't
      // duplicate it — just focus the pane that has it. Uses the same identity test
      // as openActive's swap-dedup. `force` skips this for an explicit "新しいペインで開く".
      if (!force) {
        const dup = cur.cols.flatMap((c) => c.panes).find((p) => shows(p, patch));
        if (dup) {
          if (dup.id !== cur.activeId) commit({ ...cur, activeId: dup.id }, false);
          return;
        }
      }
      const fresh = (id: string) => ({ ...blankPane(id), ...patch });
      const splitCol = (col: Column) => {
        const id = newPaneId();
        const cols = cur.cols.map((c) =>
          c.id === col.id ? { ...c, rowRatio: 0.5, panes: [...c.panes, fresh(id)] } : c,
        );
        commit({ ...cur, cols, activeId: id });
      };

      // A phone shows only the first column (others are hidden), so a new right column
      // would be invisible — grow the active column downward (top/bottom), max 2.
      const mobile = mobileMatches();
      if (mobile) {
        const col = cur.cols.find((c) => c.panes.some((p) => p.id === cur.activeId)) || cur.cols[0];
        if (col && col.panes.length < 2) splitCol(col);
        else openActive(patch);
        return;
      }

      // Desktop: add a new right column while under the column cap.
      if (cur.cols.length < MAX_COLS) {
        const id = newPaneId();
        const cols = [...cur.cols, { id: newColId(), rowRatio: 0.5, panes: [fresh(id)] }];
        commit({ ...cur, cols, colRatios: equalRatios(cols.length), activeId: id });
        return;
      }

      // Columns are capped: split a single-pane column (the active one first, else the
      // leftmost) so all four fill to two rows — up to 8 panes.
      const activeCol = cur.cols.find((c) => c.panes.some((p) => p.id === cur.activeId));
      const target = activeCol && activeCol.panes.length < 2 ? activeCol : cur.cols.find((c) => c.panes.length < 2);
      if (target) {
        splitCol(target);
        return;
      }

      // All 8 slots are full — overwrite the last pane (bottom of the rightmost column).
      const lastCol = cur.cols[cur.cols.length - 1];
      const last = lastCol.panes[lastCol.panes.length - 1];
      const cols = cur.cols.map((c) =>
        c.id === lastCol.id
          ? { ...c, panes: c.panes.map((p) => (p.id === last.id ? { ...blankPane(last.id), ...patch } : p)) }
          : c,
      );
      commit({ ...cur, cols, activeId: last.id });
    },
    [commit, openActive],
  );

  // Restore layout on browser back/forward.
  useEffect(() => {
    const onPop = (e: PopStateEvent) => {
      const l = e.state && e.state.__af && e.state.layout ? e.state.layout : initialLayout;
      setLayout(l);
      setNavOpen(!!(e.state && e.state.drawer));
      // Browser back closes the settings / admin modals. Admin pushes an entry per
      // drill level (with adminView in the state), so a back lands on the parent level
      // (AdminTab's own popstate listener restores its view) and only the pre-admin
      // entry (no modal) actually closes it.
      setSettingsOpen(!!(e.state && e.state.modal === "settings"));
      setAdminOpen(!!(e.state && e.state.modal === "admin"));
    };
    window.addEventListener("popstate", onPop);
    try {
      history.replaceState({ __af: true, layout: layoutRef.current }, "");
    } catch {}
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  const [settingsOpen, setSettingsOpen] = useState(false);
  const [settingsSection, setSettingsSection] = useState("agents"); // initial tab when opened
  const [adminOpen, setAdminOpen] = useState(false);
  // Live mirrors + the admin drill-down back handler, consulted by the popstate and
  // swipe handlers (which capture their closures once).
  const settingsOpenRef = useRef(settingsOpen);
  settingsOpenRef.current = settingsOpen;
  const adminOpenRef = useRef(adminOpen);
  adminOpenRef.current = adminOpen;
  // adminDepth = drill-down depth (0=tenants, 1=tenant, 2=member). AdminTab pushes a
  // history entry per level, so a browser "back" pops one level and only closes at the
  // top; the X/backdrop pops all levels at once.
  const adminDepthRef = useRef(0);

  // navOpen drives the mobile navigator drawer. On desktop the drawer styles are
  // inert (the left pane is always visible), so this flag is a no-op there; under
  // the mobile breakpoint it slides the navigator in/out. Selecting any item closes
  // it (wired into the show* helpers below) so the main area comes forward.
  const [navOpen, setNavOpen] = useState(false);
  const navOpenRef = useRef(navOpen);
  navOpenRef.current = navOpen;
  const closeNav = useCallback(() => setNavOpen(false), []);
  const toggleNav = useCallback(() => setNavOpen((o) => !o), []);

  // Desktop left-pane collapse (separate from the mobile navOpen drawer so it doesn't
  // touch the history/back-button + swipe wiring). leftOpen = the pane is shown;
  // leftMode = how it shows when open: "push" docks it (main reflows) or "overlay"
  // floats it above main (no reflow) like the mobile drawer. Both persist so the
  // choice survives a reload. Single-click the hamburger toggles open/closed;
  // double-click toggles the mode (see TopBar).
  const [leftOpen, setLeftOpen] = useState<boolean>(() => localStorage.getItem("af-left-open") !== "0");
  const [leftMode, setLeftMode] = useState<string>(() =>
    localStorage.getItem("af-left-mode") === "overlay" ? "overlay" : "push",
  );
  const toggleLeft = useCallback(
    () =>
      setLeftOpen((o) => {
        const n = !o;
        localStorage.setItem("af-left-open", n ? "1" : "0");
        return n;
      }),
    [],
  );
  const closeLeft = useCallback(() => {
    setLeftOpen(false);
    localStorage.setItem("af-left-open", "0");
  }, []);
  const toggleLeftMode = useCallback(
    () =>
      setLeftMode((m) => {
        const n = m === "push" ? "overlay" : "push";
        localStorage.setItem("af-left-mode", n);
        return n;
      }),
    [],
  );

  // Mobile: swipe right to open the navigator drawer (same as the hamburger), swipe
  // left to close it while open. Only under the 760px breakpoint (where the drawer
  // is a slide-in). "open" arms when the touch starts in the left region with the
  // drawer shut; "close" arms anywhere with it open. We deliberately DON'T require
  // the extreme edge: Android reserves the first ~tens of px for its system back
  // gesture, which eats edge-start touches before the page sees them — so we accept
  // a rightward swipe starting anywhere in the left third. Passive listeners (no
  // preventDefault) so terminal/list scrolling is unaffected — a vertical drag
  // (dy ≥ dx) is ignored.
  useEffect(() => {
    const mq = window.matchMedia(MOBILE_QUERY);
    const DIST = 50; // px of horizontal travel to trigger
    let sx = 0, sy = 0, mode: "open" | "close" | null = null;
    const onStart = (e: TouchEvent) => {
      const t = e.touches[0];
      mode = null;
      // While a modal is up, don't let an edge swipe open the drawer behind it (the
      // settings modal also uses horizontal swipes for its own tabs).
      if (t && mq.matches && !settingsOpenRef.current && !adminOpenRef.current) {
        if (navOpenRef.current) mode = "close";
        else if (t.clientX < Math.min(window.innerWidth * 0.33, 160)) mode = "open";
      }
      if (t) {
        sx = t.clientX;
        sy = t.clientY;
      }
    };
    const onMove = (e: TouchEvent) => {
      if (!mode) return;
      const t = e.touches[0];
      if (!t) return;
      const dx = t.clientX - sx;
      const dy = t.clientY - sy;
      if (Math.abs(dx) <= Math.abs(dy)) return; // vertical → leave it for scrolling
      if (mode === "open" && dx > DIST) {
        setNavOpen(true);
        mode = null;
      } else if (mode === "close" && dx < -DIST) {
        setNavOpen(false);
        mode = null;
      }
    };
    const onEnd = () => {
      mode = null;
    };
    window.addEventListener("touchstart", onStart, { passive: true });
    window.addEventListener("touchmove", onMove, { passive: true });
    window.addEventListener("touchend", onEnd, { passive: true });
    window.addEventListener("touchcancel", onEnd, { passive: true });
    return () => {
      window.removeEventListener("touchstart", onStart);
      window.removeEventListener("touchmove", onMove);
      window.removeEventListener("touchend", onEnd);
      window.removeEventListener("touchcancel", onEnd);
    };
  }, []);

  // pushDrawerEntry records a history entry representing "the mobile drawer is open
  // over the current view". The show* helpers push it just before navigating away
  // from an OPEN drawer, so the device/browser back button reopens the drawer
  // instead of jumping straight to the previous main view. popstate reads the
  // `drawer` flag to restore navOpen. No-op-ish on desktop (navOpen stays false).
  const pushDrawerEntry = useCallback(() => {
    try {
      history.pushState({ __af: true, layout: layoutRef.current, drawer: true }, "");
    } catch {}
  }, []);

  // refresh signals — bump to make the matching section refetch
  const [sessionsKey, setSessionsKey] = useState(0);
  const [reposKey, setReposKey] = useState(0);
  const [connKey, setConnKey] = useState(0);
  const [filesKey, setFilesKey] = useState(0);
  const bumpSessions = useCallback(() => setSessionsKey((k) => k + 1), []);

  // Canonical session list, polled once here and shared via context (the left-pane
  // Sessions list and each terminal pane's header both read it). Re-polls every 4s
  // and immediately on bumpSessions (create/recreate/archive/tenant switch). Only
  // setState on an actual change — an unconditional 4s repaint flickers the cursor.
  const [sessions, setSessions] = useState<Session[]>([]);
  const sessionsSer = useRef("");
  useEffect(() => {
    let alive = true;
    const load = () =>
      api("api/sessions")
        .then((d) => {
          if (!alive) return;
          const list = d.sessions || [];
          const ser = JSON.stringify(list);
          if (ser !== sessionsSer.current) {
            sessionsSer.current = ser;
            setSessions(list);
          }
        })
        .catch(() => {
          if (alive && sessionsSer.current !== "[]") {
            sessionsSer.current = "[]";
            setSessions([]);
          }
        });
    load();
    const id = setInterval(load, 4000);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, [sessionsKey, tenant]);
  // The WsBar resource chips (own workspace mem/CPU, host load/mem) poll every 4s.
  // That polling lives in WsBar itself (useWsResourceChips), NOT here: keeping the
  // 4s-changing stats out of this top-level provider stops every 4s tick from
  // re-rendering the whole app (terminals included) — which janked the main thread
  // and flickered the cursor. WsBar is the sole consumer, so the re-render is now
  // confined to it. WsBar reads `tenant` and `superAdmin` from this context.

  const bumpRepos = useCallback(() => setReposKey((k) => k + 1), []);
  const bumpConn = useCallback(() => setConnKey((k) => k + 1), []);
  const bumpFiles = useCallback(() => setFilesKey((k) => k + 1), []);

  // openNewSession: a global signal so anything (e.g. the onboarding card) can open
  // the New Session dialog, which otherwise lives as local state inside the left-pane
  // Sessions section. SessionsSection watches this tick and opens the modal.
  const [newSessionTick, setNewSessionTick] = useState(0);
  const openNewSession = useCallback(() => {
    // The dialog is mounted inside the left-pane Sessions section. On mobile that
    // pane is an off-canvas drawer with a CSS transform, which would offset the
    // modal's fixed positioning — so open the drawer first (a no-op on desktop,
    // where the pane is always in flow). The full-screen mobile modal covers it.
    setNavOpen(true);
    setNewSessionTick((t) => t + 1);
  }, []);

  // reveal: a home-relative path (e.g. "repos/foo") the Files tree should expand to
  // and select — set when the user clicks a repo. {path, n} so repeat clicks on the
  // same repo still re-trigger the effect (n increments).
  const [reveal, setReveal] = useState<Reveal>({ path: null, n: 0 });
  const revealInFiles = useCallback((path: string) => setReveal((r) => ({ path, n: r.n + 1 })), []);

  const refreshWs = useCallback(async () => {
    try {
      const w = await api("api/workspace");
      setWsState(w.state || "unknown");
    } catch {
      setWsState("unknown");
    }
  }, []);

  // opencode web status is optional (older images lack the endpoint) — a failure
  // just leaves it null and the bar/settings hide their controls.
  const refreshOcweb = useCallback(async () => {
    try {
      const d = await api("api/agents/opencode-web");
      setOcweb(d && !d.error ? d : null);
    } catch {
      setOcweb(null);
    }
  }, []);

  const startWs = useCallback(async () => {
    setWsState("starting…");
    await api("api/workspace/start", { method: "POST" });
    await refreshWs();
    refreshOcweb();
    bumpSessions();
    bumpRepos();
    bumpFiles();
  }, [refreshWs, refreshOcweb, bumpSessions, bumpRepos, bumpFiles]);

  const stopWs = useCallback(async () => {
    // Optimistic transition (mirrors startWs's "starting…") so the WsBar toggle goes
    // inert (busy = state ends in "…") and the 4s auto-sync poll skips mid-stop —
    // otherwise the button stays live during the multi-second docker stop and a
    // second click re-issues the stop / a poll clobbers the state.
    setWsState("stopping…");
    await api("api/workspace/stop", { method: "POST" });
    await refreshWs();
    setOcweb(null);
    bumpSessions();
    bumpFiles();
  }, [refreshWs, bumpSessions, bumpFiles]);

  // Auto-sync wsState every 4s so an externally-changed workspace (admin stop, OOM
  // death, crash) reflects on its own — this replaces the manual "状態を更新" button
  // that WsBar used to carry. Skipped while a transition is in flight (state ends in
  // "…": starting…/stopping…/recreating…) so a poll can't clobber the optimistic
  // label, and while the tab is hidden. setWsState with an unchanged value is a
  // no-op re-render, so a steady workspace costs nothing.
  const wsStateRef = useRef(wsState);
  wsStateRef.current = wsState;
  useEffect(() => {
    const id = setInterval(() => {
      if (document.hidden || wsStateRef.current.endsWith("…")) return;
      refreshWs();
    }, 4000);
    return () => clearInterval(id);
  }, [refreshWs]);

  // Refresh FILES (and repos/sessions on start) whenever the workspace actually flips
  // running↔stopped — including external changes the 4s sync catches (admin stop, OOM,
  // restart). Keyed on the transition, and on the RUNNING edge (not the "starting…"
  // click), so the tree loads once the agent is really up rather than showing the
  // pre-toggle tree. Transient "…" states are ignored until they settle.
  const prevWsRef = useRef(wsState);
  useEffect(() => {
    const settle = (s: string) => (s === "running" ? "running" : s === "none" || s === "stopped" ? "stopped" : "");
    const from = settle(prevWsRef.current);
    const to = settle(wsState);
    if (to !== "" && to !== from) {
      bumpFiles();
      if (to === "running") {
        bumpRepos();
        bumpSessions();
      }
    }
    prevWsRef.current = wsState;
  }, [wsState, bumpFiles, bumpRepos, bumpSessions]);

  // resetToTerminal collapses the layout back to a single, empty terminal pane. Used
  // when the world changes underneath the views (recreate / tenant switch): the
  // terminal-kind reconciler then disposes every other pane's xterm + socket.
  const resetToTerminal = useCallback(() => {
    paneSeq.current = 1;
    colSeq.current = 1;
    commit({ cols: [{ id: "c0", rowRatio: 0.5, panes: [blankPane("p0")] }], colRatios: [1], activeId: "p0" });
  }, [commit]);

  // loadLayout restores a tenant's saved split from localStorage (or resets to a
  // single terminal when none/invalid). It advances the id counters past the
  // restored ids so later splits don't collide, and marks hydration done so the
  // persist effect starts saving.
  const loadLayout = useCallback(
    (slug: string) => {
      hydrated.current = true;
      // Untrusted parsed JSON from localStorage — validated at runtime below.
      let l: any = null;
      try {
        const s = localStorage.getItem(LKEY(slug));
        if (s) l = JSON.parse(s);
      } catch {}
      if (!l || !Array.isArray(l.cols) || l.cols.length === 0) {
        resetToTerminal();
        return;
      }
      let pMax = 0;
      let cMax = 0;
      for (const c of l.cols) {
        const cn = parseInt(String(c.id).slice(1), 10);
        if (!Number.isNaN(cn)) cMax = Math.max(cMax, cn);
        for (const p of c.panes || []) {
          const pn = parseInt(String(p.id).slice(1), 10);
          if (!Number.isNaN(pn)) pMax = Math.max(pMax, pn);
        }
      }
      paneSeq.current = pMax + 1;
      colSeq.current = cMax + 1;
      if (!Array.isArray(l.colRatios) || l.colRatios.length !== l.cols.length) l.colRatios = equalRatios(l.cols.length);
      if (!l.cols.some((c: any) => c.panes.some((p: any) => p.id === l.activeId))) l.activeId = l.cols[0].panes[0].id;
      setLayout(l);
      try {
        history.replaceState({ __af: true, layout: l }, "");
      } catch {}
    },
    [resetToTerminal],
  );

  // recreateWs tears the container down and starts a fresh one from the current
  // image. Login + connections persist; cloned repos and running sessions are
  // wiped, so the caller guards this behind a warning dialog. Drop the views up
  // front since everything they point at is about to go away.
  const recreateWs = useCallback(async () => {
    resetToTerminal();
    setWsState("recreating…");
    const res = await api("api/workspace/recreate", { method: "POST" });
    if (res && res.error) toast("作り直しに失敗: " + (res.error.message || res.error));
    await refreshWs();
    bumpSessions();
    bumpRepos();
    bumpFiles();
  }, [resetToTerminal, refreshWs, bumpSessions, bumpRepos, bumpFiles]);

  // navigation helpers used across the UI. Each opens content in the ACTIVE pane and
  // pushes a history entry. When invoked while the mobile drawer is open, they first
  // record a "drawer open" history entry so the back button reopens the drawer.
  const showTerminal = useCallback(
    (sess?: string) => {
      if (navOpenRef.current) pushDrawerEntry();
      // With an arg: attach that session. Without: just switch the pane to terminal
      // (keep whatever it was showing). The pane's TerminalView attaches declaratively
      // from the `session` field, so we only set state here. openActive swaps the
      // panes instead of duplicating when the other pane already shows this session.
      // chat:false — a terminal open always means "attach the live session" (clears any
      // prior read-only chat/history mode on this pane).
      const patch: PanePatch = sess !== undefined ? { kind: "terminal", session: sess, chat: false } : { kind: "terminal", chat: false };
      openActive(patch);
      // Re-clicking an already-open but disconnected session doesn't change the pane's
      // props (so the declarative attach won't re-run) — revive its dropped socket here
      // so clicking a "[disconnected]" session in the list reconnects it.
      if (sess !== undefined) termReconnectSession(sess);
      setNavOpen(false);
    },
    [openActive, pushDrawerEntry],
  );
  // showChat opens a session's conversation history in read-only chat mode WITHOUT
  // attaching the terminal (no resume) — used for clicking a stopped claude session.
  // The chat's "再開して続ける" (or the ターミナル toggle) attaches + resumes on demand.
  const showChat = useCallback(
    (sess: string) => {
      if (navOpenRef.current) pushDrawerEntry();
      openActive({ kind: "terminal", session: sess, chat: true });
      setNavOpen(false);
    },
    [openActive, pushDrawerEntry],
  );
  // showTerminalSplit attaches a session in a freshly split pane (middle-click in
  // the session list), instead of replacing the active pane's content.
  const showTerminalSplit = useCallback(
    (sess: string) => {
      openInNewPane({ kind: "terminal", session: sess, chat: false });
      setNavOpen(false);
    },
    [openInNewPane],
  );
  // showChatSplit is the split-pane form of showChat: open a stopped session's
  // read-only chat history in a fresh pane (Ctrl/middle-click in the session list),
  // still without attaching (no resume).
  const showChatSplit = useCallback(
    (sess: string) => {
      openInNewPane({ kind: "terminal", session: sess, chat: true });
      setNavOpen(false);
    },
    [openInNewPane],
  );
  const showSCM = useCallback(
    (repo: string) => {
      if (navOpenRef.current) pushDrawerEntry();
      openActive({ kind: "scm", scmRepo: repo });
      setNavOpen(false);
    },
    [openActive, pushDrawerEntry],
  );
  // showSCMSplit opens a repo's Source Control in a freshly split pane (Ctrl/middle-
  // click on a repo), instead of replacing the active pane's content.
  const showSCMSplit = useCallback(
    (repo: string) => {
      openInNewPane({ kind: "scm", scmRepo: repo });
      setNavOpen(false);
    },
    [openInNewPane],
  );
  // showChanges opens a repo's working-tree changes + commit box (split out from the
  // graph SCM view) in the active pane; the split form opens it in a fresh pane.
  const showChanges = useCallback(
    (repo: string) => {
      if (navOpenRef.current) pushDrawerEntry();
      openActive({ kind: "changes", scmRepo: repo });
      setNavOpen(false);
    },
    [openActive, pushDrawerEntry],
  );
  const showChangesSplit = useCallback(
    (repo: string) => {
      openInNewPane({ kind: "changes", scmRepo: repo });
      setNavOpen(false);
    },
    [openInNewPane],
  );
  // showCommit opens one commit's detail/diff in its OWN pane (split out from the
  // graph). Clicking commits in the graph reuses a single commit pane if one is open
  // (updates its sha) instead of spawning a new pane per click; else it splits one.
  const showCommit = useCallback(
    (repo: string, sha: string) => {
      const cur = layoutRef.current;
      const existing = cur.cols.flatMap((c) => c.panes).find((p) => p.kind === "commit");
      if (existing) {
        const cols = cur.cols.map((c) => ({
          ...c,
          panes: c.panes.map((p) =>
            p.id === existing.id ? { ...p, kind: "commit" as const, scmRepo: repo, commitSha: sha } : p,
          ),
        }));
        commit({ ...cur, cols, activeId: existing.id });
        return;
      }
      openInNewPane({ kind: "commit", scmRepo: repo, commitSha: sha });
    },
    [commit, openInNewPane],
  );
  // showCommitSplit always opens the commit's detail in a fresh pane (Ctrl/middle-click
  // a commit in the graph), instead of reusing the existing commit pane.
  const showCommitSplit = useCallback(
    (repo: string, sha: string) => {
      openInNewPane({ kind: "commit", scmRepo: repo, commitSha: sha });
    },
    [openInNewPane],
  );
  const showFile = useCallback(
    (path: string) => {
      if (navOpenRef.current) pushDrawerEntry();
      openActive({ kind: "file", filePath: path });
      setNavOpen(false);
    },
    [openActive, pushDrawerEntry],
  );
  // showFileSplit opens a file in a freshly split pane (middle-click in the Files
  // tree), instead of replacing the active pane's content.
  const showFileSplit = useCallback(
    (path: string) => {
      openInNewPane({ kind: "file", filePath: path });
      setNavOpen(false);
    },
    [openInNewPane],
  );
  // showDoc opens arbitrary Markdown (e.g. a plan) in a freshly split pane — no file
  // on disk needed; the content travels in the pane descriptor.
  const showDoc = useCallback(
    (title: string, content: string) => {
      openInNewPane({ kind: "doc", docTitle: title, docContent: content });
    },
    [openInNewPane],
  );
  // showDiff opens an edit-family tool's before/after as a diff pane. The edits array
  // (captured from the transcript) travels in the pane descriptor — no file is read.
  const showDiff = useCallback(
    (title: string, edits: unknown, tool: string) => {
      openInNewPane({ kind: "diff", docTitle: title, diffEdits: edits, diffTool: tool });
    },
    [openInNewPane],
  );
  // openChat opens an assistant-chat conversation (docs/19) in the active pane — a
  // non-terminal chat surface keyed by conversation id, backed by a headless CLI.
  const openChat = useCallback(
    (conversationId: string, seed?: string) => {
      if (seed) setChatSeed(conversationId, seed); // one-shot composer prefill (Phase C)
      if (navOpenRef.current) pushDrawerEntry();
      openActive({ kind: "chat", conversationId, draftAssistantId: null });
    },
    [openActive, pushDrawerEntry],
  );

  // openAssistantDraft opens a NOT-yet-created chat for an assistant (docs/19): the
  // conversation is persisted only when the user sends the first message, so browsing an
  // assistant leaves nothing in the list. ChatView shows the assistant's greeting until then.
  const openAssistantDraft = useCallback(
    (assistantId: string) => {
      if (navOpenRef.current) pushDrawerEntry();
      openActive({ kind: "chat", conversationId: null, draftAssistantId: assistantId });
    },
    [openActive, pushDrawerEntry],
  );

  // promoteDraft binds a real conversation id onto a pane once its draft's first message
  // created the conversation (by pane id, not "active", so a background pane is safe).
  const promoteDraft = useCallback(
    (paneId: string, conversationId: string) => {
      const cur = layoutRef.current;
      const cols = cur.cols.map((c) => ({
        ...c,
        panes: c.panes.map((p) =>
          p.id === paneId ? { ...p, conversationId, draftAssistantId: null } : p,
        ),
      }));
      commit({ ...cur, cols }, false);
    },
    [commit],
  );

  // chatListKey bumps whenever a conversation is created/changed so the AssistantSection
  // list refreshes (a draft becoming real happens inside ChatView, out of the section).
  const bumpChatList = useCallback(() => setChatListKey((k) => k + 1), []);

  // ---- pane layout controls ----
  // splitRight appends a new full-height column (up to MAX_COLS) holding a fresh
  // terminal pane, made active. Column widths reset to equal. No-op at the cap.
  const splitRight = useCallback(() => {
    const cur = layoutRef.current;
    if (cur.cols.length >= MAX_COLS) return;
    const id = newPaneId();
    const cols = [...cur.cols, { id: newColId(), rowRatio: 0.5, panes: [blankPane(id)] }];
    commit({ ...cur, cols, colRatios: equalRatios(cols.length), activeId: id });
  }, [commit]);

  // splitDown splits the column holding paneId into two rows (top/bottom), adding a
  // fresh terminal pane below, made active. No-op if that column already has 2 rows.
  const splitDown = useCallback(
    (paneId: string) => {
      const cur = layoutRef.current;
      const col = cur.cols.find((c) => c.panes.some((p) => p.id === paneId));
      if (!col || col.panes.length >= 2) return;
      const id = newPaneId();
      const cols = cur.cols.map((c) =>
        c.id === col.id ? { ...c, rowRatio: 0.5, panes: [...c.panes, blankPane(id)] } : c,
      );
      commit({ ...cur, cols, activeId: id });
    },
    [commit],
  );

  // closePane closes a pane in TWO steps so a split never collapses on the first close:
  //  1. A pane still holding something (a session / chat / file / SCM) is cleared back
  //     to an empty terminal IN PLACE — same pane id, split layout untouched.
  //  2. An already-empty terminal pane is actually REMOVED: its column loses a row
  //     (collapses to a single row), an emptied column is dropped and widths
  //     re-equalize, and the reconciler disposes the closed terminal.
  // So closing the content of a split pane leaves an empty pane behind; closing that
  // empty pane again is what un-splits. The very last pane can't be removed (the layout
  // needs ≥1), so it just stays as the base empty terminal.
  const closePane = useCallback(
    (paneId: string) => {
      const cur = layoutRef.current;
      const target = cur.cols.flatMap((c) => c.panes).find((p) => p.id === paneId);
      if (!target) return;
      // Step 1: content pane → clear to an empty terminal, keeping the pane (and split).
      if (!isBlankPane(target)) {
        const cols = cur.cols.map((c) => ({
          ...c,
          panes: c.panes.map((p) => (p.id === paneId ? blankPane(paneId) : p)),
        }));
        commit({ ...cur, cols, activeId: paneId });
        return;
      }
      // Step 2: already-empty pane → remove it (collapse the split).
      const cols = cur.cols
        .map((c) => {
          const panes = c.panes.filter((p) => p.id !== paneId);
          return panes.length === c.panes.length ? c : { ...c, rowRatio: 0.5, panes };
        })
        .filter((c) => c.panes.length > 0);
      const remaining = cols.flatMap((c) => c.panes);
      if (remaining.length === 0) {
        resetToTerminal(); // removed the last pane → reset to one blank terminal
        return;
      }
      const activeId = remaining.some((p) => p.id === cur.activeId) ? cur.activeId : remaining[0].id;
      const colRatios = cols.length === cur.cols.length ? cur.colRatios : equalRatios(cols.length);
      commit({ ...cur, cols, colRatios, activeId });
    },
    [commit, resetToTerminal],
  );

  // closeSessionPanes closes every pane currently attached to a session (by name) —
  // used when a session leaves the list (archive / clear / recreate): its panes would
  // otherwise linger showing a now-gone session. Same collapse rules as closePane, but
  // it can drop several panes at once, so it does them in a single commit. No-op when
  // the session isn't shown anywhere.
  const closeSessionPanes = useCallback(
    (name: string) => {
      const cur = layoutRef.current;
      const hit = (p: Pane) => p.kind === "terminal" && p.session === name;
      if (!cur.cols.some((c) => c.panes.some(hit))) return;
      const cols = cur.cols
        .map((c) => {
          const panes = c.panes.filter((p) => !hit(p));
          return panes.length === c.panes.length ? c : { ...c, rowRatio: 0.5, panes };
        })
        .filter((c) => c.panes.length > 0);
      const remaining = cols.flatMap((c) => c.panes);
      if (remaining.length === 0) {
        resetToTerminal(); // closed the last pane → reset to one blank terminal
        return;
      }
      const activeId = remaining.some((p) => p.id === cur.activeId) ? cur.activeId : remaining[0].id;
      const colRatios = cols.length === cur.cols.length ? cur.colRatios : equalRatios(cols.length);
      commit({ ...cur, cols, colRatios, activeId });
    },
    [commit, resetToTerminal],
  );

  const setActivePane = useCallback(
    (id: string) => {
      const cur = layoutRef.current;
      if (cur.activeId === id) return;
      commit({ ...cur, activeId: id }, false); // not a history-worthy navigation
    },
    [commit],
  );

  // setColRatios / setRowRatio update split fractions during divider drag (no
  // history entry; they fire many times per drag).
  const setColRatios = useCallback((ratios: number[]) => {
    setLayout((cur) => ({ ...cur, colRatios: ratios }));
  }, []);
  const setRowRatio = useCallback((colId: string, r: number) => {
    const ratio = Math.min(0.8, Math.max(0.2, r));
    setLayout((cur) => ({
      ...cur,
      cols: cur.cols.map((c) => (c.id === colId ? { ...c, rowRatio: ratio } : c)),
    }));
  }, []);

  // swapPanes exchanges the contents of two panes (kept in place by id), made by a
  // drag-and-drop from one pane onto another. The drop target becomes active.
  const swapPanes = useCallback(
    (aId: string, bId: string) => {
      if (!aId || !bId || aId === bId) return;
      const cur = layoutRef.current;
      const all = cur.cols.flatMap((c) => c.panes);
      const a = all.find((p) => p.id === aId);
      const b = all.find((p) => p.id === bId);
      if (!a || !b) return;
      const cols = cur.cols.map((c) => ({
        ...c,
        panes: c.panes.map((p) => (p.id === aId ? { ...b, id: aId } : p.id === bId ? { ...a, id: bId } : p)),
      }));
      commit({ ...cur, cols, activeId: bId });
    },
    [commit],
  );

  // dropSplit MOVES a dragged pane to a NEW split position (a new right column, or a
  // downward split of the pane it was dropped onto). It relocates the SAME pane
  // object — keeping its id — rather than minting a new pane and copying content:
  // a new id would build a fresh xterm + a fresh WebGL context, and creating that
  // context (before the old one is reconciled away, and while the moving grid hasn't
  // settled) is what left the moved terminal blank until it was closed (see the
  // WebGL note in term.js). Reusing the id re-homes the live terminal instead. The
  // origin is closed for free (the pane just isn't in its old column anymore). dir:
  // 'right' | 'down'; refId is the pane dropped onto. (Center drops are swaps.)
  const dropSplit = useCallback(
    (srcId: string, refId: string, dir: "right" | "down") => {
      const cur = layoutRef.current;
      const src = cur.cols.flatMap((c) => c.panes).find((p) => p.id === srcId);
      if (!src || srcId === refId) return;

      // Pull the pane out of its current column; drop a column it leaves empty.
      const without = cur.cols
        .map((c) => {
          const panes = c.panes.filter((p) => p.id !== srcId);
          return panes.length === c.panes.length ? c : { ...c, rowRatio: 0.5, panes };
        })
        .filter((c) => c.panes.length > 0);

      if (dir === "right") {
        const cols = without.concat([{ id: newColId(), rowRatio: 0.5, panes: [src] }]);
        if (cols.length > MAX_COLS) return; // the freed origin column may offset this, so re-check
        commit({ ...cur, cols, colRatios: equalRatios(cols.length), activeId: srcId });
        return;
      }
      // dir === 'down': add the pane as a second row under the dropped-onto pane's
      // column. The down zone is only offered when that column has a single pane, so
      // after pulling the source out, the target still has room for the row.
      const col = without.find((c) => c.panes.some((p) => p.id === refId));
      if (!col || col.panes.length >= 2) return;
      const cols = without.map((c) =>
        c.id === col.id ? { ...c, rowRatio: 0.5, panes: [...c.panes, src] } : c,
      );
      const colRatios = cols.length === cur.cols.length ? cur.colRatios : equalRatios(cols.length);
      commit({ ...cur, cols, colRatios, activeId: srcId });
    },
    [commit],
  );

  // cycleSession attaches the previous/next session (wrapping) to the active pane,
  // for Ctrl+PgUp/PgDn.
  const cycleSession = useCallback(
    async (dir: number) => {
      let list: Session[] = [];
      try {
        const d = await api("api/sessions");
        list = d.sessions || [];
      } catch {}
      const names = list.map((s) => s.name);
      if (names.length < 2) {
        if (names.length === 1) showTerminal(names[0]);
        return;
      }
      const cur = layoutRef.current;
      const all = cur.cols.flatMap((c) => c.panes);
      const active = all.find((p) => p.id === cur.activeId) || all[0];
      let i = names.indexOf(active.session ?? "");
      if (i < 0) i = 0;
      const next = (i + dir + names.length) % names.length;
      showTerminal(names[next]);
    },
    [showTerminal],
  );

  // Global Ctrl/⌘ + PageUp/PageDown switches the active pane's session. Capture phase
  // so it beats xterm; note the browser may still claim it for tab-switching unless
  // the page holds a Keyboard Lock (fullscreen, Chromium) — see TerminalView ⛶.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.ctrlKey || e.metaKey) || e.altKey) return;
      if (e.key !== "PageUp" && e.key !== "PageDown") return;
      e.preventDefault();
      cycleSession(e.key === "PageDown" ? 1 : -1);
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [cycleSession]);

  // initTenants: resolve identity + memberships. Single-membership users never
  // see a picker; super_admin unlocks the Admin settings tab.
  const initTenants = useCallback(async () => {
    try {
      const me = await api("api/whoami");
      setWhoami(me);
    } catch {}
    let data;
    try {
      data = await api("api/tenants");
    } catch {
      return; // dev/single-tenant or CP without the endpoint
    }
    setSuperAdmin(!!data.super_admin);
    const list: Tenant[] = data.tenants || [];
    setTenants(list);
    if (list.length <= 1) {
      const slug = list[0] ? list[0].slug : "";
      persistTenant(slug);
      setTenantState(slug);
      setShowPicker(false);
      return;
    }
    let cur = getTenant();
    if (!list.some((t) => t.slug === cur)) cur = list[0].slug;
    persistTenant(cur);
    setTenantState(cur);
    setShowPicker(true);
  }, []);

  // selectTenant re-syncs everything for the newly active tenant (the legacy
  // reloadAll): collapse to a single empty terminal, refetch lists.
  const selectTenant = useCallback(
    (slug: string) => {
      persistTenant(slug);
      setTenantState(slug);
      loadLayout(slug); // restore this tenant's saved split (or reset if none)
      refreshWs();
      bumpSessions();
      bumpRepos();
      bumpConn();
    },
    [loadLayout, refreshWs, bumpSessions, bumpRepos, bumpConn],
  );

  // boot
  useEffect(() => {
    (async () => {
      await initTenants();
      loadLayout(getTenant()); // restore the saved split for the resolved tenant
      hydrateUIPrefs(); // pull per-user display settings from the server (after tenant)
      refreshWs();
      refreshOcweb();
    })();
  }, [initTenants, loadLayout, refreshWs, refreshOcweb]);

  // Back-compat projection of the active pane, for components not yet pane-aware.
  const allPanes = layout.cols.flatMap((c) => c.panes);
  const activePane = allPanes.find((p) => p.id === layout.activeId) || allPanes[0];

  const value: AppState = {
    whoami,
    tenants,
    tenant,
    showPicker,
    superAdmin,
    selectTenant,
    wsState,
    refreshWs,
    startWs,
    stopWs,
    recreateWs,
    ocweb,
    setOcweb,
    refreshOcweb,
    // active-pane projection (back-compat)
    mode: activePane.kind,
    scmRepo: activePane.scmRepo,
    commitSha: activePane.commitSha,
    filePath: activePane.filePath,
    session: activePane.session,
    // pane layout
    layout,
    activePaneId: layout.activeId,
    splitRight,
    splitDown,
    closePane,
    closeSessionPanes,
    resetToTerminal,
    setActivePane,
    setColRatios,
    setRowRatio,
    swapPanes,
    dropSplit,
    showTerminal,
    showTerminalSplit,
    showChat,
    showChatSplit,
    openInNewPane,
    showSCM,
    showSCMSplit,
    showChanges,
    showChangesSplit,
    showCommit,
    showCommitSplit,
    showFile,
    showFileSplit,
    showDoc,
    showDiff,
    openChat,
    openAssistantDraft,
    promoteDraft,
    chatListKey,
    bumpChatList,
    setPaneWrap,
    settingsOpen,
    settingsSection,
    openSettings: (section?: string) => {
      // "connections" is a legacy alias for the merged エージェント tab (the old 接続
      // tab was folded into it), so any caller asking for connections lands there.
      setSettingsSection(section === "connections" ? "agents" : section || "agents");
      setSettingsOpen(true);
      // Push a history entry so the device/browser back button closes the modal.
      try { history.pushState({ __af: true, layout: layoutRef.current, modal: "settings" }, ""); } catch {}
    },
    closeSettings: () => {
      if (typeof history !== "undefined" && history.state && history.state.modal === "settings") history.back();
      else setSettingsOpen(false);
    },
    adminOpen,
    openAdmin: () => {
      setAdminOpen(true);
      try { history.pushState({ __af: true, layout: layoutRef.current, modal: "admin" }, ""); } catch {}
    },
    adminDepthRef,
    closeAdmin: () => {
      // Full close (X / backdrop): pop ALL admin entries (base + each drill level) so
      // one action closes the modal from any depth and a later back can't re-open it.
      if (typeof history !== "undefined" && history.state && history.state.modal === "admin") {
        history.go(-(adminDepthRef.current + 1));
      } else setAdminOpen(false);
    },
    navOpen,
    toggleNav,
    closeNav,
    leftOpen,
    leftMode,
    toggleLeft,
    closeLeft,
    toggleLeftMode,
    sessions,
    sessionsKey,
    reposKey,
    connKey,
    filesKey,
    bumpSessions,
    bumpRepos,
    bumpConn,
    bumpFiles,
    newSessionTick,
    openNewSession,
    reveal,
    revealInFiles,
  };
  return <AppContext.Provider value={value}>{children}</AppContext.Provider>;
}
