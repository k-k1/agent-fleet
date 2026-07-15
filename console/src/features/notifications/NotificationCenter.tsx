import { useRef, useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { setSetting, useSettings } from "../../lib/settings.ts";
import { openNotificationTarget, replayNotification, useNotificationStore, type FleetNotification } from "./store.ts";

const labels: Record<string, string> = {
  "answer-ready": "回答が返りました", question: "質問が来ています", "plan-approval": "プランの承認待ちです",
  "permission-request": "権限の確認が必要です", "usage-reset": "利用制限がリセットされました",
};
function relative(at: string): string {
  const sec = Math.max(0, Math.floor((Date.now() - new Date(at).getTime()) / 1000));
  if (sec < 60) return "たった今";
  if (sec < 3600) return `${Math.floor(sec / 60)}分前`;
  if (sec < 86400) return `${Math.floor(sec / 3600)}時間前`;
  return `${Math.floor(sec / 86400)}日前`;
}

export function NotificationCenter() {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const toast = useToast();
  const s = useSettings();
  const { items, unseenCount, maxSeq } = useNotificationStore();
  useDismiss(ref, open, () => setOpen(false));
  const show = () => {
    setOpen((v) => !v);
    if (!open && maxSeq) void useNotificationStore.getState().markSeen(maxSeq);
  };
  const activate = async (n: FleetNotification, split: boolean) => {
    if (!(await openNotificationTarget(n, split))) toast("該当セッションは現在の一覧にありません。", { kind: "warn" });
    else setOpen(false);
    void useNotificationStore.getState().markSeen(undefined, [n.id]);
  };
  return <div className="notification-wrap" ref={ref}>
    <button className="gear notification-btn" title="通知" aria-label="通知" aria-expanded={open} onClick={show}>
      <Icon name="bell" />{unseenCount > 0 && <span className="notification-badge">{unseenCount > 9 ? "9+" : unseenCount}</span>}
    </button>
    {open && <section className="notification-panel" role="dialog" aria-label="通知センター">
      <header>
        <div className="notification-titles"><strong>通知</strong><span>過去7日間</span></div>
        <button type="button" className={"notification-mute" + (s.ttsSessionNotify ? " on" : "")}
          title={s.ttsSessionNotify ? "音声通知：オン（クリックでオフ）" : "音声通知：オフ（クリックでオン）"}
          aria-label="セッションの音声通知" aria-pressed={s.ttsSessionNotify}
          onClick={() => setSetting("ttsSessionNotify", !s.ttsSessionNotify)}>
          <Icon name={s.ttsSessionNotify ? "unmute" : "mute"} /><span>音声通知</span>
        </button>
      </header>
      {"Notification" in window && Notification.permission === "default" &&
        <button className="notification-permission" onClick={() => void Notification.requestPermission()}>デスクトップ通知を許可</button>}
      <div className="notification-list">
        {items.length === 0 ? <p className="notification-empty">通知はありません</p> : items.map((n) =>
          <div key={n.id} className={"notification-row" + (n.seen ? "" : " unread")}>
            {n.seen
              ? <span className="notification-dot" aria-hidden="true" />
              : <span className="notification-dot" role="img" aria-label="未読" />}
            <button className="notification-item"
              onClick={(e) => void activate(n, e.ctrlKey || e.metaKey)}
              onMouseDown={(e) => e.button === 1 && e.preventDefault()}
              onAuxClick={(e) => { if (e.button === 1) { e.preventDefault(); void activate(n, true); } }}>
              <Icon name={n.kind === "answer-ready" ? "check" : n.kind === "usage-reset" ? "pulse" : "comment-discussion"} />
              <span><b>{labels[n.kind] || n.kind}</b><small>{n.displayName} · {relative(n.createdAt)}</small></span>
            </button>
            <button className="notification-replay" title="音声で再生" aria-label="音声で再生" onClick={() => replayNotification(n)}><Icon name="unmute" /></button>
          </div>)}
      </div>
    </section>}
  </div>;
}
