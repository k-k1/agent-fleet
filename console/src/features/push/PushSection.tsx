import { useCallback, useEffect, useRef, useState } from "react";
import { useT } from "../../lib/i18n/index.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import {
  PUSH_KINDS,
  getPushPrefs,
  listPushDevices,
  putPushPrefs,
  removePushDevice,
  type PushDevice,
  type PushKind,
  type PushOrigin,
} from "./pushApi.ts";
import { usePushKey } from "./usePushKey.ts";
import { currentSubscription, deviceIdFor, disablePush, dropLocalSubscription, enablePush, pushSupport, subscriptionMatchesKey, thisDeviceId } from "./webPush.ts";

const KIND_LABEL = {
  question: "push.kind_question",
  "plan-approval": "push.kind_plan",
  "permission-request": "push.kind_permission",
  "answer-ready": "push.kind_answer",
} as const;

// The three origin rows; each has a switch for the session itself and one for the sessions it starts.
const ORIGIN_ROWS = [
  { label: "push.origin_own", self: "own", child: "own-child" },
  { label: "push.origin_autorun", self: "autorun", child: "autorun-child" },
  { label: "push.origin_schedule", self: "schedule", child: "schedule-child" },
] as const satisfies readonly { label: string; self: PushOrigin; child: PushOrigin }[];

// PushSection — Settings › Notifications: push to a phone with the Console closed (#4750). Absent
// when the deployment has no Web Push (the config probe answers no key). The kind choices and the
// device list are the member's server-side state, so they follow the member to every device.
const READ_TIMEOUT_MS = 10_000;
const ID_TIMEOUT_MS = 2_000;
/** Settings reads that never answer must not hold the switch for good. */
function withTimeout<T>(p: Promise<T>, ms = READ_TIMEOUT_MS): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const limit = new Promise<never>((_, rej) => (timer = setTimeout(() => rej(new Error("timeout")), ms)));
  return Promise.race([p, limit]).finally(() => clearTimeout(timer));
}

export function PushSection() {
  const tr = useT();
  const toast = useToast();
  // The CP's key, loaded when the section opens so the enable click can ask for permission at once.
  const key = usePushKey();
  const [here, setHere] = useState(false);
  // A subscription made with a key the CP no longer uses: shown as off with a hint, re-enabling replaces it.
  const [stale, setStale] = useState(false);
  const [devices, setDevices] = useState<PushDevice[]>([]);
  // The CP row id of this browser's subscription ("" without one), and whether the first read of
  // the list has finished: until then "on" cannot be told from "off", so the switch is held.
  const [myId, setMyId] = useState("");
  const [loaded, setLoaded] = useState(false);
  // null until the saved choices arrive: a write built from an empty guess would erase them.
  const [prefs, setPrefs] = useState<{ off: string[]; offOrigins: string[] } | null>(null);
  const [prefsFailed, setPrefsFailed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [perm, setPerm] = useState(() => ("Notification" in window ? Notification.permission : "unsupported"));

  const loadPrefs = useCallback(async () => {
    try {
      setPrefs(await getPushPrefs());
      setPrefsFailed(false);
    } catch {
      setPrefsFailed(true);
    }
  }, []);

  // Only the newest read is shown: one that outlived its limit must not overwrite a later one.
  const readSeq = useRef(0);
  // This device's row id and the endpoint it was shown with, as one snapshot: "remove" takes both
  // before its first await, so a later read cannot give it a different pair.
  const me = useRef({ id: "", endpoint: "" });
  const loadDevices = useCallback(async (k: string) => {
    const mine = ++readSeq.current;
    try {
      // Only fetching is under the limit, so that nothing applies after it has passed.
      const [d, sub] = await withTimeout(Promise.all([listPushDevices(), currentSubscription()]));
      const matches = !!sub && subscriptionMatchesKey(sub, k);
      // This browser's row id is published together with the list: a "remove" pressed in between
      // would otherwise not know which row is this device's own. A derivation that does not answer
      // falls back to the stored id rather than holding the list back.
      const id = sub ? await withTimeout(deviceIdFor(sub), ID_TIMEOUT_MS).catch(() => thisDeviceId()) : "";
      if (mine !== readSeq.current) return;
      me.current = { id, endpoint: sub?.endpoint ?? "" };
      setDevices(d);
      setMyId(id);
      setStale(!!sub && !matches);
      // This browser counts as enabled only while its key is current and the CP lists its row.
      // With no way to tell the row (no stored id and no SubtleCrypto) the subscription alone decides.
      setHere(matches && (!id || d.some((x) => x.id === id)));
    } catch {
      /* transient (a failed or unanswered read leaves the switch off and usable); keep the last view */
    } finally {
      setLoaded(true);
    }
  }, []);

  // The key an enable on this screen actually subscribed with: the CP may have rotated it since the
  // screen loaded, and comparing the new subscription with the old key would read "stale".
  const usedKey = useRef<string | null>(null);
  const reload = useCallback(() => {
    if (key) void loadDevices(usedKey.current ?? key);
  }, [key, loadDevices]);

  useEffect(() => {
    if (!key) return;
    void loadDevices(key);
    void loadPrefs();
  }, [key, loadDevices, loadPrefs]);

  if (!key) return null;

  const fail = (e: unknown) => toast(tr("push.failed", { msg: e instanceof Error ? e.message : String(e) }));

  const toggleHere = async (on: boolean) => {
    setBusy(true);
    let leaving = false;
    try {
      if (on) {
        const res = await enablePush(usedKey.current ?? key, (k) => (usedKey.current = k));
        // Signed out meanwhile: the page is leaving and nothing here is worth a toast or a read.
        if (res === "cancelled") {
          leaving = true;
          return;
        }
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
      if (!leaving) void reload();
    }
  };

  const savePrefs = async (next: { off: string[]; offOrigins: string[] }, kindsOnly = false) => {
    setBusy(true);
    try {
      await putPushPrefs(kindsOnly ? { off: next.off } : next);
      setPrefs(next);
    } catch (e) {
      fail(e);
    } finally {
      setBusy(false);
    }
  };
  const flip = (list: string[], k: string, on: boolean) => (on ? list.filter((x) => x !== k) : [...list.filter((x) => x !== k), k]);
  const toggleKind = (k: PushKind, on: boolean) => {
    if (prefs) void savePrefs({ ...prefs, off: flip(prefs.off, k, on) }, true);
  };
  const toggleOrigin = (o: PushOrigin, on: boolean) => {
    if (prefs) void savePrefs({ ...prefs, offOrigins: flip(prefs.offOrigins, o, on) });
  };

  const remove = async (d: PushDevice) => {
    const self = me.current;
    setBusy(true);
    try {
      await removePushDevice(d.id);
      // Removing this very device also ends the browser's own subscription.
      if (self.id && d.id === self.id) await dropLocalSubscription(self.endpoint).catch(() => {});
    } catch (e) {
      fail(e);
    } finally {
      setBusy(false);
      void reload();
    }
  };

  const supported = pushSupport() === "ok";
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
              disabled={busy || !loaded || perm === "denied"}
              onChange={(e) => void toggleHere(e.target.checked)}
            />{" "}
            {tr("push.this_device")}
          </label>
          {perm === "denied" && <p className="muted ds-note">{tr("noti.os_permission_denied")}</p>}
          {stale && <p className="muted ds-note">{tr("push.stale")}</p>}
        </div>
      )}
      {prefsFailed && (
        <p className="muted ds-note">
          {tr("push.prefs_failed")}{" "}
          <button type="button" className="linklike" onClick={() => void loadPrefs()}>
            {tr("push.retry")}
          </button>
        </p>
      )}
      <fieldset className="push-kinds" disabled={busy || prefs === null}>
        <legend className="muted ds-note">{tr("push.kinds")}</legend>
        {PUSH_KINDS.map((k) => (
          <label key={k} className="push-kind">
            <input type="checkbox" role="switch" className="noti-switch" checked={!(prefs?.off ?? []).includes(k)} onChange={(e) => void toggleKind(k, e.target.checked)} />{" "}
            {tr(KIND_LABEL[k])}
          </label>
        ))}
      </fieldset>
      <fieldset className="push-origins" disabled={busy || prefs === null}>
        <legend className="muted ds-note">{tr("push.origins")}</legend>
        {ORIGIN_ROWS.map((row) => (
          <div key={row.self} className="push-origin-row">
            <span className="push-origin-name">{tr(row.label)}</span>{" "}
            {([row.self, row.child] as const).map((o) => (
              <label key={o} className="push-origin">
                <input type="checkbox" role="switch" className="noti-switch" aria-label={`${tr(row.label)}: ${tr(o === row.self ? "push.origin_self" : "push.origin_children")}`} checked={!(prefs?.offOrigins ?? []).includes(o)} onChange={(e) => toggleOrigin(o, e.target.checked)} />{" "}
                {tr(o === row.self ? "push.origin_self" : "push.origin_children")}
              </label>
            ))}
          </div>
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
              <button type="button" disabled={busy} onClick={() => void remove(d)} aria-label={tr("push.remove_aria", { device: `${shortAgent(d.userAgent)} ${d.createdAt.slice(0, 10)}${d.id === myId ? ` (${tr("push.this_badge")})` : ""}` })}>
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
