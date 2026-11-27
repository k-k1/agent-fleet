import { useEffect, useRef, useState } from "react";

const CHANNEL = "af.mcp-elicit.cards";
const REQUEST = "request";
const CHECK_MS = 3000;
const LEASE_MS = CHECK_MS * 3;
const NO_CARDS: ReadonlySet<string> = new Set();

function connect(onCards: (ids: ReadonlySet<string>) => void) {
  if (typeof BroadcastChannel === "undefined") return null;
  let channel: BroadcastChannel;
  try {
    channel = new BroadcastChannel(CHANNEL);
  } catch {
    return null;
  }

  const sender = crypto.getRandomValues(new Uint32Array(4)).join("-");
  const mirrors = new Map<string, { ids: string[]; expires: number }>();
  let local: readonly string[] = [];
  let pageActive = true;
  let visible = !document.hidden;
  let expiry: ReturnType<typeof setTimeout> | undefined;

  const collect = () => {
    clearTimeout(expiry);
    const now = performance.now();
    let next = Infinity;
    const ids = new Set<string>();
    for (const [sender, mirror] of mirrors) {
      if (mirror.expires <= now) mirrors.delete(sender);
      else {
        next = Math.min(next, mirror.expires);
        for (const id of mirror.ids) ids.add(id);
      }
    }
    onCards(ids);
    if (next < Infinity) expiry = setTimeout(collect, next - now);
  };

  // The identifiers are random per call. The receiver can only match a card to a call it
  // independently read for its current tenant and member, without broadcasting either identity.
  const announce = (ids: readonly string[]) => channel.postMessage({ sender, ids });
  const request = () => { if (pageActive) channel.postMessage(REQUEST); };
  channel.onmessage = ({ data }) => {
    if (data === REQUEST) {
      if (visible && local.length) announce(local);
      return;
    }
    if (typeof data?.sender !== "string" || !Array.isArray(data.ids) || !data.ids.every((id: unknown) => typeof id === "string")) return;
    if (data.ids.length) mirrors.set(data.sender, { ids: data.ids, expires: performance.now() + LEASE_MS });
    else mirrors.delete(data.sender);
    collect();
  };

  // A mirror stays laid out in a background tab, but its card is out of view.
  const syncVisibility = () => {
    const next = pageActive && !document.hidden;
    if (next === visible) return;
    visible = next;
    if (local.length) announce(visible ? local : []);
    if (visible) {
      collect();
      request();
    }
  };
  const pagehide = () => { pageActive = false; syncVisibility(); };
  const pageshow = () => { pageActive = true; syncVisibility(); };
  document.addEventListener("visibilitychange", syncVisibility);
  window.addEventListener("pagehide", pagehide);
  window.addEventListener("pageshow", pageshow);

  return {
    request,
    setCards(ids: readonly string[]) {
      if (local.length === ids.length && local.every((id, i) => id === ids[i])) return;
      local = ids;
      if (visible) announce(ids);
    },
    close() {
      pagehide();
      document.removeEventListener("visibilitychange", syncVisibility);
      window.removeEventListener("pagehide", pagehide);
      window.removeEventListener("pageshow", pageshow);
      clearTimeout(expiry);
      channel.close();
    },
  };
}

/** Ask again while there are toasts to place. An unresponsive pane loses its lease even if
 *  it closed without sending a withdrawal, so its card can't hide the toast indefinitely. */
export function useRemoteMcpCards(local: readonly string[], watch: boolean): ReadonlySet<string> {
  const [remote, setRemote] = useState(NO_CARDS);
  const connection = useRef<ReturnType<typeof connect>>(null);
  useEffect(() => {
    const conn = connect((ids) => setRemote((prev) =>
      prev.size === ids.size && [...ids].every((id) => prev.has(id)) ? prev : ids,
    ));
    connection.current = conn;
    return () => conn?.close();
  }, []);
  useEffect(() => connection.current?.setCards(local), [local]);
  useEffect(() => {
    const conn = connection.current;
    if (!watch || !conn) return;
    conn.request();
    const timer = setInterval(conn.request, CHECK_MS);
    return () => clearInterval(timer);
  }, [watch]);
  return remote;
}
