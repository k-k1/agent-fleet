import type { ReactNode } from "react";
import { useT } from "../../lib/i18n/index.ts";
import type { MsgKey } from "../../lib/i18n/index.ts";
import { openSessionChat } from "../sessions/open.ts";
import { useSessionsStore } from "../sessions/store.ts";

// Sentinel standing in for the session name while the catalogue sentence is split around it, so
// the link keeps the word order of every locale. Never shown.
const SLOT = "\u0000";

/** A catalogue sentence naming the session that produced a memo ("Session {name}"). When `name`
 *  is a live session the name is a button that opens it, like the deliveries modal; otherwise
 *  (deleted, not a session, list not loaded yet) the sentence stays plain text. */
export function MemoProducerText({ k, name, link }: { k: MsgKey; name: string; link: boolean }) {
  const tr = useT();
  const live = useSessionsStore((s) => link && s.sessions.some((x) => x.name === name));
  if (!live) return <>{tr(k, { name })}</>;
  const parts = tr(k, { name: SLOT }).split(SLOT);
  if (parts.length < 2) return <>{tr(k, { name })}</>; // a sentence without {name}: nothing to link
  const out: ReactNode[] = [];
  parts.forEach((p, i) => {
    if (i > 0) {
      out.push(
        <button
          key={"b" + i}
          type="button"
          className="linkish"
          title={tr("memo.d_open_session", { name })}
          onClick={(e) => {
            // The row body toggles expand/edit on click; opening a session must not.
            e.stopPropagation();
            openSessionChat(name);
          }}
        >
          {name}
        </button>,
      );
    }
    out.push(p);
  });
  return <>{out}</>;
}
