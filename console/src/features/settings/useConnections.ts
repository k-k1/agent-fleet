import { useCallback, useState } from "react";
import { api } from "../../core/api/client.ts";
import { useSettingsUI } from "./store.ts";

// useConnections: the shared api/connections loader for the settings tabs. Keeps a
// local copy (null = loading) and, on reload, notifies global listeners (onboarding
// card, repo launch filter) via bumpConn so they refetch after a connect/disconnect.
// Used by both AgentsTab and GitTab instead of each re-implementing the same fetch.
export function useConnections() {
  const bumpConn = useSettingsUI((s) => s.bumpConn);
  const [conns, setConns] = useState<any>(null);
  const reload = useCallback(() => {
    api("api/connections")
      .then(setConns)
      .catch(() => setConns({}));
    bumpConn();
  }, [bumpConn]);
  return { conns, reload };
}
