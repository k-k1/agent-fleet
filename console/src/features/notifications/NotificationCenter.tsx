import { useRef, useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { TOAST_ICONS, useToast } from "../../ui/ToastProvider.tsx";
import { setSetting, useSettings } from "../../lib/settings.ts";
import { useToastLog, type ToastLogItem } from "../../lib/toastLog.ts";
import { openNotificationTarget, replayNotification, useNotificationStore, type FleetNotification } from "./store.ts";
import { relTime } from "../../lib/intl.ts";
import { useT, type MsgKey } from "../../lib/i18n/index.ts";

const labelKeys: Record<string, MsgKey> = {
  "answer-ready": "noti.kind_answer_ready", question: "noti.kind_question", "plan-approval": "noti.kind_plan_approval",
  "permission-request": "noti.kind_permission_request", "usage-reset": "noti.kind_usage_reset",
};
// 通知の相対時刻。共通実装（lib/intl）へ委譲する。
const relative = (at: string): string => relTime(at);

export function NotificationCenter() {
  const tr = useT();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const toast = useToast();
  const s = useSettings();
  const { items, unseenCount, maxSeq } = useNotificationStore();
  const logItems = useToastLog((st) => st.items);
  useDismiss(ref, open, () => setOpen(false));
  // Badge counts both unseen server notifications and unseen local toast-log entries.
  const unseen = unseenCount + logItems.reduce((n, i) => (i.seen ? n : n + 1), 0);
  const show = () => {
    setOpen((v) => !v);
    if (!open) {
      if (maxSeq) void useNotificationStore.getState().markSeen(maxSeq);
      useToastLog.getState().markAllSeen();
    }
  };
  const activate = async (n: FleetNotification, split: boolean) => {
    if (!(await openNotificationTarget(n, split))) toast(tr("noti.session_not_in_list"), { kind: "warn" });
    else setOpen(false);
    void useNotificationStore.getState().markSeen(undefined, [n.id]);
  };
  // Merge server (fleet) notifications with the local toast log into one time-ordered list.
  const rows = [
    ...items.map((n) => ({ src: "fleet" as const, at: new Date(n.createdAt).getTime(), n })),
    ...logItems.map((l) => ({ src: "log" as const, at: new Date(l.createdAt).getTime(), l })),
  ].sort((a, b) => b.at - a.at);
  return <div className="notification-wrap" ref={ref}>
    <button className="gear notification-btn" title={tr("noti.notifications")} aria-label={tr("noti.notifications")} aria-expanded={open} onClick={show}>
      <Icon name="bell" />{unseen > 0 && <span className="notification-badge">{unseen > 9 ? "9+" : unseen}</span>}
    </button>
    {open && <section className="notification-panel" role="dialog" aria-label={tr("noti.center")}>
      <header>
        <div className="notification-titles"><strong>{tr("noti.notifications")}</strong><span>{tr("noti.past_7_days")}</span></div>
        <button type="button" className={"notification-mute" + (s.ttsSessionNotify ? " on" : "")}
          title={s.ttsSessionNotify ? tr("noti.tts_on") : tr("noti.tts_off")}
          aria-label={tr("noti.tts_aria")} aria-pressed={s.ttsSessionNotify}
          onClick={() => setSetting("ttsSessionNotify", !s.ttsSessionNotify)}>
          <Icon name={s.ttsSessionNotify ? "unmute" : "mute"} /><span>{tr("noti.tts_label")}</span>
        </button>
      </header>
      {"Notification" in window && Notification.permission === "default" &&
        <button className="notification-permission" onClick={() => void Notification.requestPermission()}>{tr("noti.allow_desktop")}</button>}
      <div className="notification-list">
        {rows.length === 0 ? <p className="notification-empty">{tr("noti.empty")}</p> : rows.map((row) =>
          row.src === "log"
            ? <LogRow key={"l:" + row.l.id} l={row.l} />
            : <FleetRow key={"f:" + row.n.id} n={row.n} onActivate={(n, split) => void activate(n, split)} />)}
      </div>
    </section>}
  </div>;
}

// Unread marker (leading dot). Reserved column keeps read/unread rows aligned; role=img so
// the unread state is announced, aria-hidden when read.
function Dot({ seen }: { seen: boolean }) {
  const tr = useT();
  return seen
    ? <span className="notification-dot" aria-hidden="true" />
    : <span className="notification-dot" role="img" aria-label={tr("noti.unread")} />;
}

function FleetRow({ n, onActivate }: { n: FleetNotification; onActivate: (n: FleetNotification, split: boolean) => void }) {
  const tr = useT();
  return <div className={"notification-row" + (n.seen ? "" : " unread")}>
    <Dot seen={n.seen} />
    <button className="notification-item"
      onClick={(e) => onActivate(n, e.ctrlKey || e.metaKey)}
      onMouseDown={(e) => e.button === 1 && e.preventDefault()}
      onAuxClick={(e) => { if (e.button === 1) { e.preventDefault(); onActivate(n, true); } }}>
      <Icon name={n.kind === "answer-ready" ? "check" : n.kind === "usage-reset" ? "pulse" : "comment-discussion"} />
      <span><b>{labelKeys[n.kind] ? tr(labelKeys[n.kind]) : n.kind}</b><small>{n.displayName} · {relative(n.createdAt)}</small></span>
    </button>
    <button className="notification-replay" title={tr("noti.replay")} aria-label={tr("noti.replay")} onClick={() => replayNotification(n)}><Icon name="unmute" /></button>
  </div>;
}

// A persisted toast (error / destructive-action result). Informational only — no navigation
// or TTS replay; the × removes it from the local log.
function LogRow({ l }: { l: ToastLogItem }) {
  const tr = useT();
  return <div className={"notification-row" + (l.seen ? "" : " unread")}>
    <Dot seen={l.seen} />
    <div className={"notification-item notification-log k-" + l.kind}>
      <Icon name={TOAST_ICONS[l.kind]} />
      <span><b>{l.message}</b><small>{relative(l.createdAt)}</small></span>
    </div>
    <button className="notification-dismiss" title={tr("noti.dismiss")} aria-label={tr("noti.dismiss")} onClick={() => useToastLog.getState().remove(l.id)}><Icon name="close" /></button>
  </div>;
}
