import { createContext, useContext, useState, useEffect, useCallback, useRef } from "react";
import { api, getTenant, setTenant as persistTenant } from "./api.js";
import { attach as termAttach, detach as termDetach } from "./term.js";
import { hydrateUIPrefs } from "./lib/settings.js";

// AppContext holds everything shared across the top bar, WS bar, left pane, main
// area, and settings dialog: identity, tenant selection, workspace state, the
// current main-area "mode" (terminal | scm | file), and refresh signals that let
// one section trigger another to refetch (e.g. clone-then-start refreshes repos).
const AppContext = createContext(null);
export const useApp = () => useContext(AppContext);

export function AppProvider({ children }) {
  const [whoami, setWhoami] = useState(null); // { email, user, ... }
  const [tenants, setTenants] = useState([]); // [{ slug, name, role }]
  const [tenant, setTenantState] = useState(getTenant());
  const [showPicker, setShowPicker] = useState(false);
  const [superAdmin, setSuperAdmin] = useState(false);

  const [wsState, setWsState] = useState("…");

  // main-area navigation, as a single object so we can mirror it into browser
  // history (back/forward). { mode: 'terminal'|'scm'|'file', scmRepo, filePath }.
  const [nav, setNav] = useState({ mode: "terminal", scmRepo: null, filePath: null });
  const navRef = useRef(nav);
  navRef.current = nav;
  const [session, setSession] = useState(null);
  const sessionRef = useRef(session);
  sessionRef.current = session;

  // go applies a partial nav change and (by default) pushes a browser history
  // entry, so the browser's back/forward — and the mouse back button — traverse
  // the views you visited. We keep the URL unchanged (state-only pushState): the
  // Console lives behind a path-stripping proxy, so putting paths in the URL would
  // break reloads / the base path. History therefore restores within a session,
  // not across reloads.
  const go = useCallback((partial, push = true) => {
    const next = { ...navRef.current, ...partial };
    if (push && JSON.stringify(next) === JSON.stringify(navRef.current)) return; // no dup entry
    setNav(next);
    if (push) {
      try {
        history.pushState({ __af: true, ...next }, "");
      } catch {}
    }
  }, []);

  // Restore nav on browser back/forward. Does NOT re-attach sessions — switching
  // to the terminal view just shows whatever is currently attached.
  useEffect(() => {
    const onPop = (e) => {
      const s = e.state && e.state.__af ? e.state : { mode: "terminal", scmRepo: null, filePath: null };
      setNav({ mode: s.mode ?? "terminal", scmRepo: s.scmRepo ?? null, filePath: s.filePath ?? null });
    };
    window.addEventListener("popstate", onPop);
    try {
      history.replaceState({ __af: true, ...navRef.current }, "");
    } catch {}
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  const [settingsOpen, setSettingsOpen] = useState(false);
  const [adminOpen, setAdminOpen] = useState(false);

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

  // recreateWs tears the container down and starts a fresh one from the current
  // image. Login + connections persist; cloned repos and running sessions are
  // wiped, so the caller guards this behind a warning dialog. Drop the terminal
  // and any open repo view up front since both are about to go away.
  const recreateWs = useCallback(async () => {
    termDetach();
    setSession(null);
    go({ mode: "terminal" });
    setWsState("recreating…");
    const res = await api("api/workspace/recreate", { method: "POST" });
    if (res && res.error) alert("作り直しに失敗: " + (res.error.message || res.error));
    await refreshWs();
    bumpSessions();
    bumpRepos();
    bumpFiles();
  }, [go, refreshWs, bumpSessions, bumpRepos, bumpFiles]);

  // navigation helpers used across the UI (each pushes a history entry via go)
  const showTerminal = useCallback(
    (sess) => {
      if (sess) {
        termAttach(sess);
        setSession(sess);
      }
      go({ mode: "terminal" });
    },
    [go],
  );
  const showSCM = useCallback((repo) => go({ mode: "scm", scmRepo: repo }), [go]);
  const showFile = useCallback((path) => go({ mode: "file", filePath: path }), [go]);

  // cycleSession attaches the previous/next session (wrapping), for Ctrl+PgUp/PgDn.
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
      let i = names.indexOf(sessionRef.current);
      if (i < 0) i = 0;
      const next = (i + dir + names.length) % names.length;
      showTerminal(names[next]);
    },
    [showTerminal],
  );

  // Global Ctrl/⌘ + PageUp/PageDown switches sessions. Capture phase so it beats
  // xterm; note the browser may still claim it for tab-switching unless the page
  // holds a Keyboard Lock (fullscreen, Chromium) — see TerminalView ⛶.
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
  // reloadAll): drop the terminal, return to the terminal view, refetch lists.
  const selectTenant = useCallback(
    (slug) => {
      persistTenant(slug);
      setTenantState(slug);
      termDetach();
      setSession(null);
      go({ mode: "terminal" });
      refreshWs();
      bumpSessions();
      bumpRepos();
      bumpConn();
    },
    [go, refreshWs, bumpSessions, bumpRepos, bumpConn],
  );

  // boot
  useEffect(() => {
    (async () => {
      await initTenants();
      hydrateUIPrefs(); // pull per-user display settings from the server (after tenant)
      refreshWs();
    })();
  }, [initTenants, refreshWs]);

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
    mode: nav.mode,
    scmRepo: nav.scmRepo,
    filePath: nav.filePath,
    session,
    showTerminal,
    showSCM,
    showFile,
    settingsOpen,
    openSettings: () => setSettingsOpen(true),
    closeSettings: () => setSettingsOpen(false),
    adminOpen,
    openAdmin: () => setAdminOpen(true),
    closeAdmin: () => setAdminOpen(false),
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
