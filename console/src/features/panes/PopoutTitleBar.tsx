// PopoutTitleBar — the minimal pop-out tab's only chrome: what this pane shows,
// a session state dot when one is bound, and the 展開 button that converts the
// tab into a full console in place (setPopoutMode("full") — the layout store
// already holds the 1-pane layout, so App just re-renders with full chrome).
import { useEffect } from "react";
import { useLayoutStore } from "../../layout/store.ts";
import { activePane } from "../../layout/ops.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useSharedSessionsStore } from "../sharing/store.ts";
import { useChatTitles } from "../chat/store.ts";
import { stateInfo } from "../../lib/sessionview.ts";
import { setPopoutMode } from "../../lib/popoutMode.ts";
import { useT } from "../../lib/i18n/index.ts";
import { IconButton } from "../../ui/Button.tsx";
import { paneTitle } from "./paneTitle.ts";

const BASE_TITLE = "Agent Fleet — Console";

export function PopoutTitleBar() {
  const tr = useT();
  const layout = useLayoutStore((s) => s.layout);
  const sessions = useSessionsStore((s) => s.sessions);
  const sharedSessions = useSharedSessionsStore((s) => s.sessions);
  const pane = activePane(layout);
  const session = pane?.session ? (sessions.find((s) => s.name === pane.session) ?? null) : null;
  const sharedSessionId = pane && pane.content.kind === "sharedSession" ? pane.content.sharedSessionId : null;
  const shared = sharedSessionId ? sharedSessions.find((s) => s.id === sharedSessionId) : undefined;
  // A pop-out has no assistant rail to keep the conversation list fresh, so the title
  // this window shows comes from its own ChatView via the store (useChatTitles).
  const chatTitles = useChatTitles();
  const conversationId = pane && pane.content.kind === "chat" ? pane.content.conversationId : null;
  const chatTitle = conversationId ? chatTitles.get(conversationId) : undefined;
  const title = pane ? paneTitle(pane, session, { shared, chatTitle }) : "";
  const st = session ? stateInfo(session) : null;

  // The tab's browser title mirrors the pane so the tab strip stays readable
  // with several pop-outs. Restored when the bar unmounts (展開 to full).
  useEffect(() => {
    document.title = title ? `${title} — Agent Fleet` : BASE_TITLE;
    return () => {
      document.title = BASE_TITLE;
    };
  }, [title]);

  return (
    <div className="popout-titlebar">
      {st && <span className={"popout-dot " + st.cls} title={st.text} />}
      <span className="popout-title" title={title}>
        {title}
      </span>
      <span className="popout-spacer" />
      <IconButton icon="screen-full" label={tr("ui.popout_expand")} onClick={() => setPopoutMode("full")} />
    </div>
  );
}
