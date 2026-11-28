import { useLayoutEffect, useRef } from "react";
import { makeSessionLink } from "../viewer/parts/mdRefLinks.ts";
import { useSessionLinkMenu } from "./SessionLinkMenu.tsx";
import { openSessionChat, openSessionChatSplit } from "./open.ts";
import { useSessionsStore } from "./store.ts";

/** SessionRefLink shows a session's slug as the same link a Markdown reply gives one: built by
 *  makeSessionLink, so a click, a modified click, a middle click, the context menu, the long
 *  press and the tooltip cannot drift from the transcript's. A session that no longer exists
 *  stays plain text, as it does in the transcript. `onOpen` replaces the default destination
 *  (the chat mirror, a new pane for a modified click) for a caller that has to do more than
 *  open the pane; it is read at click time, so a new closure does not rebuild the link. */
export function SessionRefLink({
  name,
  className,
  onOpen,
}: {
  name: string;
  className?: string;
  onOpen?: (name: string, openInNew: boolean) => void;
}) {
  const exists = useSessionsStore((st) => st.sessions.some((s) => s.name === name));
  const openMenu = useSessionLinkMenu();
  const menuRef = useRef(openMenu);
  menuRef.current = openMenu;
  const openRef = useRef(onOpen);
  openRef.current = onOpen;
  const hasMenu = openMenu !== null;
  const host = useRef<HTMLSpanElement>(null);
  useLayoutEffect(() => {
    const el = host.current;
    if (!el || !exists) return;
    const a = makeSessionLink(
      name,
      (n, openInNew) => {
        if (openRef.current) openRef.current(n, openInNew);
        else if (openInNew) openSessionChatSplit(n);
        else openSessionChat(n);
      },
      hasMenu ? (n, x, y) => menuRef.current?.(n, x, y) : undefined,
    );
    a.classList.add(...(className?.match(/\S+/g) ?? []));
    el.append(a);
    return () => a.remove();
  }, [name, exists, hasMenu, className]);
  if (!exists) {
    return (
      <span key="text" className={className} title={name}>
        {name}
      </span>
    );
  }
  return <span ref={host} className="md-ref-host" />;
}
