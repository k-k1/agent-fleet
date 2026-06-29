import { createContext, useContext, useState, useEffect, useCallback, useRef } from "react";
import { api, getTenant, setTenant as persistTenant } from "./api.js";
import { keepOnly as termKeepOnly } from "./term.js";
import { hydrateUIPrefs } from "./lib/settings.js";

// AppContext holds everything shared across the top bar, WS bar, left pane, main
// area, and settings dialog: identity, tenant selection, workspace state, the
// main-area pane layout, and refresh signals that let one section trigger another
// to refetch (e.g. clone-then-start refreshes repos).
//
// The main area is a "layout" of one or two panes shown side by side. Each pane
// independently shows a terminal, the source-control workbench, or a file viewer.
// Clicking an item in the left navigator opens it in the ACTIVE pane. Back-compat
// selectors (mode/scmRepo/filePath/session) project the active pane so existing
// components keep working.
const AppContext = createContext(null);
export const useApp = () => useContext(AppContext);

// A pane descriptor. kind drives which view renders; the *Path/Repo/session fields
// are the per-kind payload. Empty terminal pane = "セッション未接続".
function blankPane(id, patch) {
  return { id, kind: "terminal", session: null, filePath: null, scmRepo: null, ...patch };
}

const initialLayout = {
  split: "single", // 'single' | 'vertical'
  ratio: 0.5, // left pane fraction when split (clamped 0.2–0.8)
  activeId: "p0",
  panes: [blankPane("p0")],
};

export function AppProvider({ children }) {
  const [whoami, setWhoami] = useState(null); // { email, user, ... }
  const [tenants, setTenants] = useState([]); // [{ slug, name, role }]
  const [tenant, setTenantState] = useState(getTenant());
  const [showPicker, setShowPicker] = useState(false);
  const [superAdmin, setSuperAdmin] = useState(false);

  const [wsState, setWsState] = useState("…");

  // The main-area pane layout. Mirrored into browser history (back/forward).
  const [layout, setLayout] = useState(initialLayout);
  const layoutRef = useRef(layout);
  layoutRef.current = layout;
  const paneSeq = useRef(1); // next pane id suffix (p0 already used)
  const newPaneId = () => `p${paneSeq.current++}`;

  // Tear down terminal instances whose pane no longer exists (pane closed, browser
  // back, tenant switch). Each pane keeps its xterm alive while the pane exists —
  // even while showing a file/scm view — so the terminal (and its scrollback +
  // socket) survives view switches, exactly like the original single-terminal app.
  // Runs after every layout change; cheap (a set diff over ≤2 panes).
  useEffect(() => {
    termKeepOnly(layout.panes.map((p) => p.id));
  }, [layout]);

  // commit applies a new layout and (by default) pushes a browser history entry, so
  // the browser's back/forward — and the mouse back button — traverse the views you
  // visited. We keep the URL unchanged (state-only pushState): the Console lives
  // behind a path-stripping proxy, so putting paths in the URL would break reloads /
  // the base path. History therefore restores within a session, not across reloads.
  const commit = useCallback((next, push = true) => {
    if (push && JSON.stringify(next) === JSON.stringify(layoutRef.current)) return; // no dup entry
    setLayout(next);
    if (push) {
      try {
        history.pushState({ __af: true, layout: next }, "");
      } catch {}
    }
  }, []);

  // patchActive returns a new layout with the active pane shallow-merged with patch.
  const patchActive = useCallback((patch, push = true) => {
    const cur = layoutRef.current;
    const panes = cur.panes.map((p) => (p.id === cur.activeId ? { ...p, ...patch } : p));
    commit({ ...cur, panes }, push);
  }, [commit]);

  // shows returns true if pane already displays exactly the target described by patch
  // (same kind + identity). Used to avoid showing one thing in both split panes.
  const shows = (pane, patch) => {
    if (!pane || pane.kind !== patch.kind) return false;
    if (patch.kind === "terminal") return patch.session !== undefined && pane.session === patch.session;
    if (patch.kind === "file") return pane.filePath === patch.filePath;
    if (patch.kind === "scm") return pane.scmRepo === patch.scmRepo;
    return false;
  };

  // openActive opens the target (patch) in the active pane. When split and the OTHER
  // pane already shows exactly that target, it would duplicate — so instead the two
  // panes swap contents (the target slides to the active side, the active pane's old
  // content slides to the other side), keeping ids + which side is active. Otherwise
  // it's a plain patch of the active pane.
  const openActive = useCallback(
    (patch) => {
      const cur = layoutRef.current;
      if (cur.split === "vertical") {
        const active = cur.panes.find((p) => p.id === cur.activeId);
        const other = cur.panes.find((p) => p.id !== cur.activeId);
        if (shows(other, patch)) {
          const panes = cur.panes.map((p) =>
            p.id === cur.activeId ? { ...other, id: cur.activeId } : { ...active, id: other.id },
          );
          commit({ ...cur, panes });
          return;
        }
      }
      patchActive(patch);
    },
    [commit, patchActive],
  );

  // Restore layout on browser back/forward.
  useEffect(() => {
    const onPop = (e) => {
      const l = e.state && e.state.__af && e.state.layout ? e.state.layout : initialLayout;
      setLayout(l);
      setNavOpen(!!(e.state && e.state.drawer));
    };
    window.addEventListener("popstate", onPop);
    try {
      history.replaceState({ __af: true, layout: layoutRef.current }, "");
    } catch {}
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  const [settingsOpen, setSettingsOpen] = useState(false);
  const [adminOpen, setAdminOpen] = useState(false);

  // navOpen drives the mobile navigator drawer. On desktop the drawer styles are
  // inert (the left pane is always visible), so this flag is a no-op there; under
  // the mobile breakpoint it slides the navigator in/out. Selecting any item closes
  // it (wired into the show* helpers below) so the main area comes forward.
  const [navOpen, setNavOpen] = useState(false);
  const navOpenRef = useRef(navOpen);
  navOpenRef.current = navOpen;
  const closeNav = useCallback(() => setNavOpen(false), []);
  const toggleNav = useCallback(() => setNavOpen((o) => !o), []);

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
  const bumpRepos = useCallback(() => setReposKey((k) => k + 1), []);
  const bumpConn = useCallback(() => setConnKey((k) => k + 1), []);
  const bumpFiles = useCallback(() => setFilesKey((k) => k + 1), []);

  // reveal: a home-relative path (e.g. "repos/foo") the Files tree should expand to
  // and select — set when the user clicks a repo. {path, n} so repeat clicks on the
  // same repo still re-trigger the effect (n increments).
  const [reveal, setReveal] = useState({ path: null, n: 0 });
  const revealInFiles = useCallback((path) => setReveal((r) => ({ path, n: r.n + 1 })), []);

  const refreshWs = useCallback(async () => {
    try {
      const w = await api("api/workspace");
      setWsState(w.state || "unknown");
    } catch {
      setWsState("unknown");
    }
  }, []);

  const startWs = useCallback(async () => {
    setWsState("starting…");
    await api("api/workspace/start", { method: "POST" });
    await refreshWs();
    bumpSessions();
    bumpRepos();
    bumpFiles();
  }, [refreshWs, bumpSessions, bumpRepos, bumpFiles]);

  const stopWs = useCallback(async () => {
    await api("api/workspace/stop", { method: "POST" });
    await refreshWs();
    bumpSessions();
    bumpFiles();
  }, [refreshWs, bumpSessions, bumpFiles]);

  // resetToTerminal collapses the layout back to a single, empty terminal pane. Used
  // when the world changes underneath the views (recreate / tenant switch): the
  // terminal-kind reconciler then disposes every other pane's xterm + socket.
  const resetToTerminal = useCallback(() => {
    paneSeq.current = 1;
    commit({ split: "single", ratio: 0.5, activeId: "p0", panes: [blankPane("p0")] });
  }, [commit]);

  // recreateWs tears the container down and starts a fresh one from the current
  // image. Login + connections persist; cloned repos and running sessions are
  // wiped, so the caller guards this behind a warning dialog. Drop the views up
  // front since everything they point at is about to go away.
  const recreateWs = useCallback(async () => {
    resetToTerminal();
    setWsState("recreating…");
    const res = await api("api/workspace/recreate", { method: "POST" });
    if (res && res.error) alert("作り直しに失敗: " + (res.error.message || res.error));
    await refreshWs();
    bumpSessions();
    bumpRepos();
    bumpFiles();
  }, [resetToTerminal, refreshWs, bumpSessions, bumpRepos, bumpFiles]);

  // navigation helpers used across the UI. Each opens content in the ACTIVE pane and
  // pushes a history entry. When invoked while the mobile drawer is open, they first
  // record a "drawer open" history entry so the back button reopens the drawer.
  const showTerminal = useCallback(
    (sess) => {
      if (navOpenRef.current) pushDrawerEntry();
      // With an arg: attach that session. Without: just switch the pane to terminal
      // (keep whatever it was showing). The pane's TerminalView attaches declaratively
      // from the `session` field, so we only set state here. openActive swaps the
      // panes instead of duplicating when the other pane already shows this session.
      const patch = sess !== undefined ? { kind: "terminal", session: sess } : { kind: "terminal" };
      openActive(patch);
      setNavOpen(false);
    },
    [openActive, pushDrawerEntry],
  );
  const showSCM = useCallback(
    (repo) => {
      if (navOpenRef.current) pushDrawerEntry();
      openActive({ kind: "scm", scmRepo: repo });
      setNavOpen(false);
    },
    [openActive, pushDrawerEntry],
  );
  const showFile = useCallback(
    (path) => {
      if (navOpenRef.current) pushDrawerEntry();
      openActive({ kind: "file", filePath: path });
      setNavOpen(false);
    },
    [openActive, pushDrawerEntry],
  );

  // ---- pane layout controls ----
  // splitPane goes single → vertical, adding a fresh empty terminal pane on the
  // right and making it active (so the next click opens there). No-op if already
  // split.
  const splitPane = useCallback(() => {
    const cur = layoutRef.current;
    if (cur.split === "vertical") return;
    const id = newPaneId();
    commit({ ...cur, split: "vertical", panes: [...cur.panes, blankPane(id)], activeId: id });
  }, [commit]);

  // closePane removes a pane (vertical → single). The surviving pane becomes active.
  // The reconciler effect disposes the closed pane's terminal.
  const closePane = useCallback(
    (id) => {
      const cur = layoutRef.current;
      if (cur.split !== "vertical") return;
      const panes = cur.panes.filter((p) => p.id !== id);
      if (panes.length === 0) return;
      commit({ ...cur, split: "single", panes, activeId: panes[0].id });
    },
    [commit],
  );

  const setActivePane = useCallback(
    (id) => {
      const cur = layoutRef.current;
      if (cur.activeId === id) return;
      commit({ ...cur, activeId: id }, false); // not a history-worthy navigation
    },
    [commit],
  );

  // setRatio updates the split fraction during divider drag (no history entry).
  const setRatio = useCallback(
    (r) => {
      const cur = layoutRef.current;
      const ratio = Math.min(0.8, Math.max(0.2, r));
      if (ratio === cur.ratio) return;
      setLayout({ ...cur, ratio });
    },
    [],
  );

  // cycleSession attaches the previous/next session (wrapping) to the active pane,
  // for Ctrl+PgUp/PgDn.
  const cycleSession = useCallback(
    async (dir) => {
      let list = [];
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
      const active = cur.panes.find((p) => p.id === cur.activeId) || cur.panes[0];
      let i = names.indexOf(active.session);
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
    const onKey = (e) => {
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
    const list = data.tenants || [];
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
    (slug) => {
      persistTenant(slug);
      setTenantState(slug);
      resetToTerminal();
      refreshWs();
      bumpSessions();
      bumpRepos();
      bumpConn();
    },
    [resetToTerminal, refreshWs, bumpSessions, bumpRepos, bumpConn],
  );

  // boot
  useEffect(() => {
    (async () => {
      await initTenants();
      hydrateUIPrefs(); // pull per-user display settings from the server (after tenant)
      refreshWs();
    })();
  }, [initTenants, refreshWs]);

  // Back-compat projection of the active pane, for components not yet pane-aware.
  const activePane = layout.panes.find((p) => p.id === layout.activeId) || layout.panes[0];

  const value = {
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
    // active-pane projection (back-compat)
    mode: activePane.kind,
    scmRepo: activePane.scmRepo,
    filePath: activePane.filePath,
    session: activePane.session,
    // pane layout
    layout,
    activePaneId: layout.activeId,
    splitPane,
    closePane,
    setActivePane,
    setRatio,
    showTerminal,
    showSCM,
    showFile,
    settingsOpen,
    openSettings: () => setSettingsOpen(true),
    closeSettings: () => setSettingsOpen(false),
    adminOpen,
    openAdmin: () => setAdminOpen(true),
    closeAdmin: () => setAdminOpen(false),
    navOpen,
    toggleNav,
    closeNav,
    sessionsKey,
    reposKey,
    connKey,
    filesKey,
    bumpSessions,
    bumpRepos,
    bumpConn,
    bumpFiles,
    reveal,
    revealInFiles,
  };
  return <AppContext.Provider value={value}>{children}</AppContext.Provider>;
}
