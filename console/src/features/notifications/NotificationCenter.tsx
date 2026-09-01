import { useEffect, useRef, useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { TOAST_ICONS, useToast } from "../../ui/ToastProvider.tsx";
import { setSetting, useSettings } from "../../lib/settings.ts";
import { useToastLog, type ToastLogItem } from "../../lib/toastLog.ts";
import { openNotificationTarget, replayNotification, useNotificationStore, type FleetNotification } from "./store.ts";
import { useOpenSignal } from "../../core/store/uiOpen.ts";
import { relTime } from "../../lib/intl.ts";
import { useT } from "../../lib/i18n/index.ts";
import { notificationKindLabel } from "./wording.ts";
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
  // Keyboard selection: once the center opens, focus the first row and let ↑/↓ (Home/End)
  // rove between rows; Enter activates the focused notification natively (each row is a
  // button — Ctrl/⌘+Enter opens it in a new pane, handled in FleetRow). Escape closes via
  // useDismiss. Log rows are made focusable so they're reachable in the sweep too.
  useEffect(() => {
    if (!open) return;
    const list = ref.current?.querySelector<HTMLElement>(".notification-list");
    if (!list) return;
    const rowsOf = () => Array.from(list.querySelectorAll<HTMLElement>(".notification-item"));
    for (const el of rowsOf()) if (el.tagName !== "BUTTON") el.tabIndex = 0;
    // Focus the first row on open, but don't yank focus back to the top if the user is
    // already roving (a new notification arriving mid-navigation would otherwise reset it).
    const raf = requestAnimationFrame(() => {
      if (!list.contains(document.activeElement)) rowsOf()[0]?.focus();
    });
    const onKey = (e: KeyboardEvent) => {
      const l = rowsOf();
      if (!l.length) return;
      const i = l.indexOf(document.activeElement as HTMLElement);
      if (e.key === "ArrowDown") {
        e.preventDefault();
        l[i < 0 ? 0 : (i + 1) % l.length].focus();
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        l[i < 0 ? l.length - 1 : (i - 1 + l.length) % l.length].focus();
      } else if (e.key === "Home") {
        e.preventDefault();
        l[0].focus();
      } else if (e.key === "End") {
        e.preventDefault();
        l[l.length - 1].focus();
      }
    };
    list.addEventListener("keydown", onKey);
    return () => {
      cancelAnimationFrame(raf);
      list.removeEventListener("keydown", onKey);
    };
  }, [open, items.length, logItems.length]);
  // Badge counts both unseen server notifications and unseen local toast-log entries.
  const unseen = unseenCount + logItems.reduce((n, i) => (i.seen ? n : n + 1), 0);
  const show = () => {
    setOpen((v) => !v);
    if (!open) {
      if (maxSeq) void useNotificationStore.getState().markSeen(maxSeq);
      useToastLog.getState().markAllSeen();
    }
  };
  // Keyboard: Ctrl/⌘+K g n toggles the center just like clicking the bell.
  useOpenSignal("notifications", show);
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
      // Enter opens in the active pane; Ctrl/⌘+Enter in a new pane. Handled here (not left
      // to the native click) so the modifier is honored consistently across browsers.
      onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); onActivate(n, e.ctrlKey || e.metaKey); } }}
      onMouseDown={(e) => e.button === 1 && e.preventDefault()}
      onAuxClick={(e) => { if (e.button === 1) { e.preventDefault(); onActivate(n, true); } }}>
      <Icon name={n.kind === "answer-ready" ? "check"
        : n.kind.startsWith("schedule-") ? "watch" // 左レールのスケジュール節と同じ字面
          : n.kind.startsWith("handoff-") ? "git-branch" // 共有レールの引き継ぎバッジと同じ字面
            : ["usage-reset", "rate-limit-reached", "rate-limit-resumed"].includes(n.kind) ? "pulse" : "comment-discussion"} />
      <span><b>{notificationKindLabel(n.kind)}</b><small>{n.displayName} · {relative(n.createdAt)}</small></span>
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
