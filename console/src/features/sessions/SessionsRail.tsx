// SessionsRail — P1 の暫定セッション一覧。ターミナル+レイアウトコアを目視する
// ための最小リスト（クリック=アクティブペインで開く、Ctrl/中クリック=分割で開く、
// ペイン序数バッジ表示）。本実装（dirグループ・操作メニュー・モーダル群）は P2。
import { useLayoutStore } from "../../layout/store.ts";
import { sessionPanes, ordClass } from "../../layout/badges.ts";
import { useSessionsStore } from "./store.ts";
import { kindIcon, kindShort, kindClass } from "../../lib/sessionkind.ts";
import { displayName, stateInfo } from "../../lib/sessionview.ts";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Pill } from "../../ui/Pill.tsx";
import type { PillTone } from "../../ui/Pill.tsx";
import type { Session } from "../../types/session.ts";
import type { OpenTarget } from "../../layout/types.ts";

const STATE_TONE: Record<string, PillTone> = {
  on: "ok",
  working: "accent",
  question: "warn",
  bg: "warn",
  off: "muted",
  "off dead": "danger",
};

const target = (s: Session): OpenTarget => ({
  content: { kind: "terminal", chat: false },
  session: s.name,
});

export function SessionsRail() {
  const sessions = useSessionsStore((s) => s.sessions);
  const layout = useLayoutStore((s) => s.layout);
  const openTarget = useLayoutStore((s) => s.openTarget);
  const openTargetInNew = useLayoutStore((s) => s.openTargetInNew);
  const byName = sessionPanes(layout);

  if (sessions.length === 0) {
    return (
      <EmptyState
        icon="terminal"
        title="セッションなし"
        hint="作成・起動などの操作は P2 で移植されます（旧コンソールで作成してください）。"
      />
    );
  }
  return (
    <ul className="rail-sessions">
      {sessions.map((s) => {
        const st = stateInfo(s);
        const panes = byName.get(s.name) || [];
        return (
          <li key={s.name}>
            <button
              type="button"
              className="rail-session"
              title={`ID: ${s.name} — クリックで開く / Ctrl+クリックで分割して開く`}
              onMouseDown={(e) => e.button === 1 && e.preventDefault()}
              onAuxClick={(e) => {
                if (e.button === 1) {
                  e.preventDefault();
                  openTargetInNew(target(s));
                }
              }}
              onClick={(e) =>
                e.ctrlKey || e.metaKey ? openTargetInNew(target(s)) : openTarget(target(s))
              }
            >
              <span className={"kind-tag kind-" + kindClass(s.kind)}>
                <span className={`codicon codicon-${kindIcon(s.kind)}`} aria-hidden="true" />
                <span className="kt-short">{kindShort(s.kind)}</span>
              </span>
              <span className="rail-session-name">{displayName(s)}</span>
              {panes.map((p) => (
                <span key={p.id} className={"rail-ord " + ordClass(p.ordinal)}>
                  {p.ordinal}
                </span>
              ))}
              <Pill tone={STATE_TONE[st.cls] || "muted"} icon={st.icon}>
                {st.text}
              </Pill>
            </button>
          </li>
        );
      })}
    </ul>
  );
}
