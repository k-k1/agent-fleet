import { createContext, useContext, useState, useEffect, useCallback } from "react";
import { api, getTenant, setTenant as persistTenant } from "./api.js";
import { attach as termAttach, detach as termDetach } from "./term.js";

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

  // main-area navigation
  const [mode, setMode] = useState("terminal"); // 'terminal' | 'scm' | 'file'
  const [scmRepo, setScmRepo] = useState(null);
  const [filePath, setFilePath] = useState(null);
  const [session, setSession] = useState(null);

  const [settingsOpen, setSettingsOpen] = useState(false);

  // refresh signals — bump to make the matching section refetch
  const [sessionsKey, setSessionsKey] = useState(0);
  const [reposKey, setReposKey] = useState(0);
  const [connKey, setConnKey] = useState(0);
  const bumpSessions = useCallback(() => setSessionsKey((k) => k + 1), []);
  const bumpRepos = useCallback(() => setReposKey((k) => k + 1), []);
  const bumpConn = useCallback(() => setConnKey((k) => k + 1), []);

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
  }, [refreshWs, bumpSessions]);

  const stopWs = useCallback(async () => {
    await api("api/workspace/stop", { method: "POST" });
    await refreshWs();
    bumpSessions();
  }, [refreshWs, bumpSessions]);

  // recreateWs tears the container down and starts a fresh one from the current
  // image. Login + connections persist; cloned repos and running sessions are
  // wiped, so the caller guards this behind a warning dialog. Drop the terminal
  // and any open repo view up front since both are about to go away.
  const recreateWs = useCallback(async () => {
    termDetach();
    setSession(null);
    setMode("terminal");
    setWsState("recreating…");
    const res = await api("api/workspace/recreate", { method: "POST" });
    if (res && res.error) alert("作り直しに失敗: " + (res.error.message || res.error));
    await refreshWs();
    bumpSessions();
    bumpRepos();
  }, [refreshWs, bumpSessions, bumpRepos]);

  // navigation helpers used across the UI
  const showTerminal = useCallback((sess) => {
    if (sess) {
      termAttach(sess);
      setSession(sess);
    }
    setMode("terminal");
  }, []);
  const showSCM = useCallback((repo) => {
    setScmRepo(repo);
    setMode("scm");
  }, []);
  const showFile = useCallback((path) => {
    setFilePath(path);
    setMode("file");
  }, []);

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
      setMode("terminal");
      refreshWs();
      bumpSessions();
      bumpRepos();
      bumpConn();
    },
    [refreshWs, bumpSessions, bumpRepos, bumpConn],
  );

  // boot
  useEffect(() => {
    (async () => {
      await initTenants();
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
    mode,
    scmRepo,
    filePath,
    session,
    showTerminal,
    showSCM,
    showFile,
    settingsOpen,
    openSettings: () => setSettingsOpen(true),
    closeSettings: () => setSettingsOpen(false),
    sessionsKey,
    reposKey,
    connKey,
    bumpSessions,
    bumpRepos,
    bumpConn,
  };
  return <AppContext.Provider value={value}>{children}</AppContext.Provider>;
}
