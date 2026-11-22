import { useCallback, useEffect, useState } from "react";
import { useT } from "../../lib/i18n/index.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import {
  PUSH_KINDS,
  getPushKey,
  getPushOff,
  listPushDevices,
  putPushOff,
  removePushDevice,
  type PushDevice,
  type PushKind,
} from "./pushApi.ts";
import { currentSubscription, disablePush, enablePush, pushSupport, thisDeviceId } from "./webPush.ts";

const KIND_LABEL = {
  question: "push.kind_question",
  "plan-approval": "push.kind_plan",
  "permission-request": "push.kind_permission",
  "answer-ready": "push.kind_answer",
} as const;

// PushSection — Settings › Notifications: push to a phone with the Console closed (#4750). Absent
// when the deployment has no Web Push (the config probe answers no key). The kind choices and the
// device list are the member's server-side state, so they follow the member to every device.
export function PushSection() {
  const tr = useT();
  const toast = useToast();
  const [available, setAvailable] = useState<boolean | null>(null);
  const [here, setHere] = useState(false);
  const [devices, setDevices] = useState<PushDevice[]>([]);
  const [off, setOff] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [perm, setPerm] = useState(() => ("Notification" in window ? Notification.permission : "unsupported"));

  const reload = useCallback(async () => {
    try {
      const [d, o, sub] = await Promise.all([listPushDevices(), getPushOff(), currentSubscription()]);
      setDevices(d);
      setOff(o);
      // This browser counts as enabled only while the CP also knows it.
      const id = thisDeviceId();
      setHere(!!sub && (!id || d.some((x) => x.id === id)));
    } catch {
      /* transient; keep the last view */
    }
  }, []);

  useEffect(() => {
    let alive = true;
    void getPushKey().then((k) => {
      if (!alive) return;
      setAvailable(!!k);
      if (k) void reload();
    });
    return () => {
      alive = false;
    };
  }, [reload]);

  if (!available) return null;

  const fail = (e: unknown) => toast(tr("push.failed", { msg: e instanceof Error ? e.message : String(e) }));

  const toggleHere = async (on: boolean) => {
    setBusy(true);
    try {
      if (on) {
        const res = await enablePush();
        if ("Notification" in window) setPerm(Notification.permission);
        if (res === "denied") toast(tr("push.denied"));
        else if (res === "unsupported") toast(tr("push.unsupported"));
        else if (res === "unavailable") toast(tr("push.unavailable"));
      } else {
        await disablePush();
      }
    } catch (e) {
      fail(e);
    } finally {
      setBusy(false);
      void reload();
    }
  };

  const toggleKind = async (k: PushKind, on: boolean) => {
    const next = on ? off.filter((x) => x !== k) : [...off.filter((x) => x !== k), k];
    setBusy(true);
    try {
      await putPushOff(next);
      setOff(next);
    } catch (e) {
      fail(e);
    } finally {
      setBusy(false);
    }
  };

  const remove = async (d: PushDevice) => {
    setBusy(true);
    try {
      await removePushDevice(d.id);
      // Removing this very device also ends the browser's own subscription.
      if (d.id === thisDeviceId()) await disablePush().catch(() => {});
    } catch (e) {
      fail(e);
    } finally {
      setBusy(false);
      void reload();
    }
  };

  const supported = pushSupport() === "ok";
  const myId = thisDeviceId();
  return (
    <section className="ds-group" data-testid="push-section">
      <h4 className="ds-title">{tr("push.title")}</h4>
      <p className="muted ds-note">{tr("push.note")}</p>
      {!supported ? (
        <p className="muted ds-note">{tr("push.unsupported")}</p>
      ) : (
        <div className="ds-row">
          <label>
            <input
              type="checkbox"
              role="switch"
              className="noti-switch"
              checked={here}
              disabled={busy || perm === "denied"}
              onChange={(e) => void toggleHere(e.target.checked)}
            />{" "}
            {tr("push.this_device")}
          </label>
          {perm === "denied" && <p className="muted ds-note">{tr("noti.os_permission_denied")}</p>}
        </div>
      )}
      <fieldset className="push-kinds" disabled={busy}>
        <legend className="muted ds-note">{tr("push.kinds")}</legend>
        {PUSH_KINDS.map((k) => (
          <label key={k} className="push-kind">
            <input type="checkbox" role="switch" className="noti-switch" checked={!off.includes(k)} onChange={(e) => void toggleKind(k, e.target.checked)} />{" "}
            {tr(KIND_LABEL[k])}
          </label>
        ))}
      </fieldset>
      <h5 className="ds-title">{tr("push.devices")}</h5>
      {devices.length === 0 ? (
        <p className="muted ds-note">{tr("push.no_devices")}</p>
      ) : (
        <ul className="push-devices">
          {devices.map((d) => (
            <li key={d.id}>
              <span className="push-device-name">
                {shortAgent(d.userAgent)}
                {d.id === myId && <b> — {tr("push.this_badge")}</b>}
              </span>{" "}
              <span className="muted">{d.createdAt.slice(0, 10)}</span>{" "}
              <button type="button" disabled={busy} onClick={() => void remove(d)} aria-label={tr("push.remove_aria", { device: shortAgent(d.userAgent) })}>
                {tr("push.remove")}
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

/** A readable device label out of a User-Agent: the browser and platform words, not the whole string. */
export function shortAgent(ua: string): string {
  const browser = /Edg\//.test(ua) ? "Edge" : /Firefox\//.test(ua) ? "Firefox" : /(Chrome|CriOS)\//.test(ua) ? "Chrome" : /Safari\//.test(ua) ? "Safari" : "";
  const os = /iPhone|iPad/.test(ua) ? "iOS" : /Android/.test(ua) ? "Android" : /Windows/.test(ua) ? "Windows" : /Mac OS X/.test(ua) ? "macOS" : /Linux/.test(ua) ? "Linux" : "";
  return [browser, os].filter(Boolean).join(" / ") || ua.slice(0, 40) || "?";
}
