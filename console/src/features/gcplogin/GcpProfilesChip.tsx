// GcpProfilesChip — the WS bar's Google Cloud badge (ADR 0107 phase 3), beside the AWS one and
// built the same way: how many Settings profiles are logged in, and a popover listing each
// one's login. As with AWS there is no profile "in effect": af-gcloud-exec takes --profile on
// every run, so the badge never singles one out as the default.
//
// "Logged in" is the Agent's reading of its gcloud store (a user credential is stored for the
// selected account). It cannot tell a login Google has revoked from a good one until a command
// uses it, which is why every logged-in row offers "Log in again" rather than a state it
// cannot back. A row with a pending af-gcloud-exec request turns the chip amber: an agent is
// waiting on it now.
//
// Every login starts in GcpProfileLoginModal from the member's own press there (rendered by
// GcpLoginHost); the badge only opens it and never sees an attempt id.
//
// No interval poll: it asks when the workspace comes up, when the popover opens, when the
// list of pending requests changes (a login from a toast settles one), after a login, when
// the tab comes back into view, and after a Settings change (now and once the Agent's next
// Settings pull is due: the badge is hidden while the list is empty, so nothing on it could
// ask).
import { useEffect, useMemo, useRef, useState } from "react";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useSettingsUI } from "../settings/store.ts";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useT } from "../../lib/i18n/index.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useGcpLoginStore, type GcpProfileState } from "./store.ts";

export const isLoggedIn = (p: GcpProfileState) => p.state === "signed_in";

export function sortGcpProfiles(list: GcpProfileState[]): GcpProfileState[] {
  return [...list].sort(
    (a, b) => Number(isLoggedIn(b)) - Number(isLoggedIn(a)) || (a.label || a.name).localeCompare(b.label || b.name),
  );
}

export function GcpProfilesChip() {
  const tr = useT();
  const running = useWorkspaceStore((s) => s.state) === "running";
  const profiles = useGcpLoginStore((s) => s.profiles);
  const requests = useGcpLoginStore((s) => s.requests);
  const refresh = useGcpLoginStore((s) => s.refreshProfiles);
  const showProfile = useGcpLoginStore((s) => s.showProfile);
  const openSettings = useSettingsUI((s) => s.openSettings);
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const shown = running && !!profiles && profiles.length > 0;
  // A popover that vanished with the chip must not keep its dismiss layer: that layer would
  // swallow the next press anywhere on the page.
  useDismiss(ref, open && shown, () => setOpen(false));
  useEffect(() => {
    if (!shown) setOpen(false);
  }, [shown]);

  // Keyed by the ids, not the array: GcpLoginHost re-reads the list every few seconds while a
  // toast is up, and an unchanged answer must not cost a second request each time.
  const requestIds = requests.map((r) => r.id).join(",");
  useEffect(() => {
    if (running) void refresh();
  }, [running, refresh, requestIds]);

  // Coming back to the tab asks once: a login finished in another tab or device, or a
  // Settings change made there, notifies nothing here.
  const refreshRequests = useGcpLoginStore((s) => s.refresh);
  useEffect(() => {
    if (!running) return;
    let last = 0;
    const again = () => {
      // A tab switch fires both events; one pair of asks is enough.
      if (document.visibilityState === "hidden" || Date.now() - last < 2000) return;
      last = Date.now();
      void refreshRequests();
      void refresh();
    };
    window.addEventListener("focus", again);
    document.addEventListener("visibilitychange", again);
    return () => {
      window.removeEventListener("focus", again);
      document.removeEventListener("visibilitychange", again);
    };
  }, [running, refresh, refreshRequests]);

  // Profiles with a pending request. A Set, not an object: a profile named "constructor"
  // would read Object.prototype's.
  const waiting = useMemo(() => new Set(requests.map((r) => r.profile)), [requests]);

  if (!shown || !profiles) return null;

  const active = profiles.filter(isLoggedIn);
  const anyWaiting = profiles.some((p) => waiting.has(p.name));
  const tone = anyWaiting ? " warn" : active.length === 0 ? " muted" : " ok";
  // One logged-in profile is named, since "which one" is the question; more get a count.
  const label = active.length === 1 ? active[0].label || active[0].name : `${active.length}/${profiles.length}`;
  const title = anyWaiting
    ? tr("wsbar.gcp.chip_waiting")
    : tr("wsbar.gcp.chip_title", { active: active.length, total: profiles.length });

  return (
    <div className="ws-usage-wrap ws-aws ws-gcp" ref={ref}>
      <button
        type="button"
        className={"kind-tag ws-usage-btn ws-aws-btn ws-gcp-btn" + tone}
        title={title}
        aria-label={title}
        aria-expanded={open}
        onClick={() => {
          if (!open) void refresh();
          setOpen((o) => !o);
        }}
      >
        <Icon name="brand:gcp" />
        <span className="ws-aws-label">{label}</span>
        <Icon name="chevron-down" />
      </button>
      {open && (
        <div className="ws-usage-pop ws-aws-pop ws-gcp-pop">
          <div className="wu-title">{tr("wsbar.gcp.title")}</div>
          <ul className="ws-aws-list">
            {sortGcpProfiles(profiles).map((p) => {
              const on = isLoggedIn(p);
              return (
                <li key={p.name} className="ws-aws-row ws-gcp-row">
                  <div className="ws-aws-head">
                    <span className="ws-aws-name" title={p.name}>
                      {p.label || p.name}
                    </span>
                    <span
                      className={"ws-aws-state " + (on ? "signed_in" : "none")}
                      title={tr(on ? "wsbar.gcp.state_signed_in_title" : "wsbar.gcp.state_none_title")}
                    >
                      {tr(on ? "wsbar.gcp.state_signed_in" : "wsbar.gcp.state_none")}
                    </span>
                  </div>
                  <div className="ws-aws-meta muted">
                    {[p.label && p.label !== p.name ? p.name : "", p.project, on ? p.account : ""].filter(Boolean).join(" · ")}
                  </div>
                  <div className="ws-aws-foot">
                    {waiting.has(p.name) ? <span className="warn">{tr("wsbar.gcp.waiting")}</span> : <span />}
                    <span className="ws-aws-actions">
                      <button
                        type="button"
                        className="ghost ws-aws-login ws-gcp-login"
                        title={tr(on ? "gcplogin.login_again_title" : "gcplogin.login_title")}
                        onClick={() => {
                          // The modal sits outside the popover, whose dismiss layer would
                          // close it on the first press there.
                          setOpen(false);
                          showProfile({
                            profile: { name: p.name, label: p.label, project: p.project, account: p.account },
                            force: on,
                          });
                        }}
                      >
                        {tr(on ? "gcplogin.login_again" : "gcplogin.login")}
                      </button>
                    </span>
                  </div>
                </li>
              );
            })}
          </ul>
          <div className="wu-note muted">
            {tr("wsbar.gcp.no_default")}{" "}
            <code className="ws-aws-cmd">af-gcloud-exec --profile {tr("wsbar.aws.cmd_name")}</code>
          </div>
          <button
            type="button"
            className="wu-manage"
            onClick={() => {
              setOpen(false);
              openSettings("gcp");
            }}
          >
            <Icon name="settings-gear" /> {tr("wsbar.gcp.settings")}
          </button>
        </div>
      )}
    </div>
  );
}
