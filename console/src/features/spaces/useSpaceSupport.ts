// useSpaceSupport — whether this workspace's Agent can start a session in a space. An older Agent
// ignores the `space` field and would start the session in home instead (#4978), so every place
// that offers a space asks GET /api/spaces first. The same answer fills the spaces store, so the
// place chip's list needs no second request.
import { useEffect, useState } from "react";
import { api, isTransientErr } from "../../core/api/client.ts";
import { spacesOwner, useSpacesStore } from "./store.ts";

export type SpaceSupport = "checking" | "yes" | "no" | "failed";

export function useSpaceSupport(): { support: SpaceSupport; recheck: () => void } {
  const [support, setSupport] = useState<SpaceSupport>("checking");
  const [probe, setProbe] = useState(0);
  // A switch of workspace makes the answer for the old one meaningless: ask again.
  const from = spacesOwner();
  useEffect(() => {
    let alive = true;
    setSupport("checking");
    const ticket = useSpacesStore.getState().begin();
    api("api/spaces")
      .then((d) => {
        if (!alive) return;
        if (d && !d.error && Array.isArray(d.spaces)) {
          setSupport("yes");
          // Superseded meanwhile (a switch of workspace clears the store after this request began,
          // a later refresh started): the list is read again rather than left empty under a
          // support answer that says "yes".
          if (!useSpacesStore.getState().adopt(ticket, d)) void useSpacesStore.getState().refresh();
        }
        // Only "no such route" means an Agent without spaces; anything else may pass. A plain-text
        // 404 / 405 (the Agent's mux) arrives as http_404 / http_405 with no status.
        else if (d?.error && !isTransientErr(d) && (d.error.status === 404 || d.error.status === 405 || d.error.code === "http_404" || d.error.code === "http_405")) setSupport("no");
        else setSupport("failed");
      })
      .catch(() => alive && setSupport("failed"));
    return () => {
      alive = false;
    };
  }, [probe, from]);
  return { support, recheck: () => setProbe((n) => n + 1) };
}
