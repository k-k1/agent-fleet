import { useCallback, useEffect, useRef, useState } from "react";
import { api, errDetail } from "../../../core/api/client.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { usePolling } from "../parts/usePolling.ts";
import { useConnections } from "../parts/useConnections.ts";
import { getKiroInstall, getMuseInstall, isOk, type CliInstallStatus } from "../api.ts";

type InstallKind = "kiro" | "muse";

const getInstall = { kiro: getKiroInstall, muse: getMuseInstall } as const;

// On-demand CLI install and update for the harnesses whose binary is not in the image (kiro,
// muse), drawn on the harness's Agents card. The sign-in that needs the binary stays on Model
// credentials. The Agent runs the install in the background and GET reports state=installing
// while it does, so a remount (switching tabs mid-download) resumes the progress from that
// answer instead of offering a second install.
//
// Nothing is drawn until the first GET answers: an unknown install state must not read as "not
// installed" and put an install button in front of a member who has the binary. `supported` is the
// connections answer (false = CLI missing); it stands in when the install GET failed, so a
// transient 502 never leaves a missing CLI with no way to install it.
export function CliInstall({ kind, running, supported }: { kind: InstallKind; running: boolean; supported?: boolean }) {
  const tr = useT();
  const toast = useToast();
  const poll = usePolling();
  const { reload } = useConnections();
  const [inst, setInst] = useState<CliInstallStatus | null>(null);
  const [installing, setInstalling] = useState<null | "installing" | "error">(null);
  const [busy, setBusy] = useState(false);
  // One poll chain at a time: a resume and a button press must not run two.
  const polling = useRef(false);

  const loadInstall = useCallback(async (): Promise<CliInstallStatus | null> => {
    if (!running) return null;
    try {
      const r = await getInstall[kind]();
      const s = isOk(r) ? r : null;
      // GET omits the version facts while an install runs; keep the last ones so the card
      // does not flip between "install" and "update" mid-download.
      setInst((prev) =>
        s && s.installed === undefined && prev
          ? { ...s, installed: prev.installed, version: prev.version, pin: prev.pin, updateAvailable: prev.updateAvailable }
          : s,
      );
      return s;
    } catch {
      return null; // stopped workspace / transient 502 — draw nothing rather than guess
    }
  }, [running, kind]);

  const watch = useCallback(() => {
    if (polling.current) return;
    polling.current = true;
    setInstalling("installing");
    poll({
      deadlineMs: 20 * 60 * 1000,
      firstDelayMs: 4000,
      onExpire: () => {
        polling.current = false;
        setInstalling("error");
      },
      step: async () => {
        let p: CliInstallStatus | null;
        try {
          const r = await getInstall[kind]();
          p = isOk(r) ? r : null;
        } catch {
          p = null;
        }
        if (p && p.state === "done") {
          polling.current = false;
          setInst(p); // the answer already carries the new version facts: no flash of the old state
          setInstalling(null);
          reload(); // Model credentials now sees the binary
          return { stop: true };
        }
        if (p && p.state === "error") {
          polling.current = false;
          setInstalling("error");
          toast(tr(`agents.${kind}_install_failed`, { msg: p.error || "" }));
          return { stop: true };
        }
        return { stop: false, nextMs: 4000 };
      },
    });
  }, [poll, kind, loadInstall, reload, toast, tr]);

  useEffect(() => {
    void loadInstall().then((s) => {
      if (s?.state === "installing") watch();
    });
    // Resume once per (running, kind); watch/loadInstall identities change with the toast.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [running, kind]);

  const install = async () => {
    setBusy(true);
    setInstalling("installing");
    try {
      const res = await api(`api/connections/${kind}/install`, { method: "POST" });
      if (!res || res.error) {
        setInstalling("error");
        toast(tr(`agents.${kind}_install_failed`, { msg: res?.error ? errDetail(res.error) : "" }));
        return;
      }
      if (res.state === "done") {
        setInst((prev) => ({ ...(prev ?? { state: "done" }), state: "done", installed: true, updateAvailable: false }));
        setInstalling(null);
        void loadInstall();
        reload();
        return;
      }
      watch();
    } catch (e) {
      // A network drop rejects instead of answering; without this the card stays on "installing".
      setInstalling("error");
      toast(tr(`agents.${kind}_install_failed`, { msg: e instanceof Error ? e.message : "" }));
    } finally {
      setBusy(false);
    }
  };

  if (!running) return null;
  const known = inst ?? (supported === false ? ({ state: "idle", installed: false } as CliInstallStatus) : null);
  if (!known) return null;
  // installed is absent only while an install runs and nothing earlier was seen: that is a first install.
  const missing = known.installed === false || (known.installed === undefined && known.state === "installing");
  const update = known.installed === true && !!known.updateAvailable;
  if (!missing && !update && installing === null) return null;

  return (
    <div className="p-body" data-testid={`cli-install-${kind}`}>
      {installing === "installing" ? (
        <p className="ps-note ps-note-warn">{tr(missing ? `agents.${kind}_installing` : `agents.${kind}_updating`)}</p>
      ) : (
        <>
          {missing ? (
            <div className="p-desc">{tr(`agents.${kind}_install_desc`)}</div>
          ) : (
            <p className="ps-note ps-note-warn">
              {tr(`agents.${kind}_update_avail`, { cur: known.version || "?", pin: known.pin || "?" })}
            </p>
          )}
          <div className="p-opts">
            <button type="button" className="p-opt" disabled={busy} onClick={install}>
              <span className="p-opt-t">{tr(missing ? `agents.${kind}_install` : `agents.${kind}_update`)}</span>
              <span className="p-opt-s">{tr(missing ? `agents.${kind}_install_note` : `agents.${kind}_update_note`)}</span>
            </button>
          </div>
          {installing === "error" && <p className="ps-note ps-note-warn">{tr(`agents.${kind}_install_error`)}</p>}
        </>
      )}
    </div>
  );
}
