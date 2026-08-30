import { useEffect, useState } from "react";
import { api } from "../../core/api/client.ts";

// Native host self-update status (docs/log/42). Shape of GET /api/update/status.
export interface HostUpdateStatus {
  current: string; // version the running control-plane is serving
  installed: string; // newest version staged on disk (== current when up to date)
  restartRequired: boolean; // a newer version is staged and needs a restart to take effect
  systemd?: boolean;
}

// useHostUpdate — shared source for the host self-update banner. GET /api/update/status
// is NATIVE-ONLY: on Docker / ECS / dev the CP does not register the route, api()
// resolves to an http_404 error, and this returns null so every caller renders nothing
// (same guard the EnvTab section has always used). Returns null while loading too.
// Read-only + fetched once on mount; the apply action lives in EnvTab's section, and a
// successful apply restarts the CP (the reconnect re-mounts consumers with fresh state).
export function useHostUpdate(): HostUpdateStatus | null {
  const [st, setSt] = useState<HostUpdateStatus | null>(null);
  useEffect(() => {
    let live = true;
    api("api/update/status")
      .then((res) => {
        if (live) setSt(res && !res.error ? res : null);
      })
      .catch(() => {
        if (live) setSt(null);
      });
    return () => {
      live = false;
    };
  }, []);
  return st;
}
