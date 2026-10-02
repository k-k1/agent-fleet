// AwsProfilesChip — the WS bar's AWS badge (#1477): how many Settings profiles are signed in,
// and a popover listing each one's login. It answers "which profile is in effect" by saying
// there is none: af-aws-exec takes --profile on every run and each profile has its own SSO
// session, so every signed-in profile is usable at once. A badge that singled one out would
// be the misunderstanding the members asked about.
//
// No interval poll: the bar is always on screen. It asks when the workspace comes up, when the
// popover opens, and once when a known end passes; the expiry notification and the login
// modal refresh the same store (AwsLoginHost, ProfileLoginModal).
import { useEffect, useMemo, useRef, useState } from "react";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useSettingsUI } from "../settings/store.ts";
import { fmtDateTime, TIME_HM } from "../../lib/intl.ts";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useT, type MsgKey } from "../../lib/i18n/index.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useAwsLoginStore, type AwsProfileState } from "./store.ts";
import { useProfileLogout } from "./useProfileLogout.ts";

const STATE_KEYS: Record<string, { label: MsgKey; title: MsgKey }> = {
  signed_in: { label: "ssm.state_signed_in", title: "ssm.state_signed_in_title" },
  renew: { label: "ssm.state_renew", title: "ssm.state_renew_title" },
  none: { label: "ssm.state_none", title: "ssm.state_none_title" },
};
const STATE_ORDER: Record<string, number> = { signed_in: 0, renew: 1 };

// "renew" counts: the CLI renews it on the next use while the portal session lasts, and the
// Agent cannot tell from the cache whether it still does.
export const isActive = (p: AwsProfileState) => p.state === "signed_in" || p.state === "renew";

export function sortProfiles(list: AwsProfileState[]): AwsProfileState[] {
  return [...list].sort(
    (a, b) =>
      (STATE_ORDER[a.state] ?? 2) - (STATE_ORDER[b.state] ?? 2) ||
      (a.label || a.name).localeCompare(b.label || b.name),
  );
}

// The longest a one-shot refresh waits; setTimeout overflows past ~24.8 days anyway.
const MAX_WAIT_MS = 24 * 3600_000;

export function AwsProfilesChip() {
  const tr = useT();
  const running = useWorkspaceStore((s) => s.state) === "running";
  const profiles = useAwsLoginStore((s) => s.profiles);
  const refresh = useAwsLoginStore((s) => s.refreshExpiry);
  const showProfile = useAwsLoginStore((s) => s.showProfile);
  const logout = useProfileLogout();
  const loggingOut = useAwsLoginStore((s) => s.loggingOut);
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

  // The App-level ask may have run while the workspace was still stopped, which leaves the
  // list empty until something else asks.
  useEffect(() => {
    if (running) void refresh();
  }, [running, refresh]);

  // A login with a known end turns "not signed in" at that end and nothing notifies then.
  // Picked once per answer, not per render: a render between the end and the refresh would
  // otherwise drop that end from the pick and cancel the refresh. An end already past when
  // the answer came is left out, or the refresh would fire again on every answer.
  const nextEnd = useMemo(() => {
    const now = Date.now();
    return (profiles || [])
      .map((p) => Date.parse(p.expiresAt))
      .filter((t) => Number.isFinite(t) && t > now)
      .reduce((m, t) => Math.min(m, t), Infinity);
  }, [profiles]);
  useEffect(() => {
    if (!running || !Number.isFinite(nextEnd)) return;
    const t = window.setTimeout(() => void refresh(), Math.min(nextEnd - Date.now() + 2000, MAX_WAIT_MS));
    return () => clearTimeout(t);
  }, [running, nextEnd, refresh]);

  if (!shown || !profiles) return null;

  const active = profiles.filter(isActive);
  const expiring = profiles.some((p) => p.expiring);
  const tone = expiring ? " warn" : active.length === 0 ? " muted" : " ok";
  // One signed-in profile is named, since "which one" is the question; more get a count.
  const label = active.length === 1 ? active[0].label || active[0].name : `${active.length}/${profiles.length}`;
  const title = expiring
    ? tr("wsbar.aws.chip_expiring")
    : tr("wsbar.aws.chip_title", { active: active.length, total: profiles.length });

  return (
    <div className="ws-usage-wrap ws-aws" ref={ref}>
      <button
        type="button"
        className={"kind-tag ws-usage-btn ws-aws-btn" + tone}
        title={title}
        aria-label={title}
        aria-expanded={open}
        onClick={() => {
          if (!open) void refresh();
          setOpen((o) => !o);
        }}
      >
        <Icon name="brand:aws" />
        <span className="ws-aws-label">{label}</span>
        <Icon name="chevron-down" />
      </button>
      {open && (
        <div className="ws-usage-pop ws-aws-pop">
          <div className="wu-title">{tr("wsbar.aws.title")}</div>
          <ul className="ws-aws-list">
            {sortProfiles(profiles).map((p) => {
              const st = STATE_KEYS[p.state];
              const end = p.expiresAt && Date.parse(p.expiresAt) > Date.now() ? p.expiresAt : "";
              return (
                <li key={p.name} className="ws-aws-row">
                  <div className="ws-aws-head">
                    <span className="ws-aws-name" title={p.name}>
                      {p.label || p.name}
                    </span>
                    {st && (
                      <span className={"ws-aws-state " + p.state} title={tr(st.title)}>
                        {tr(st.label)}
                      </span>
                    )}
                  </div>
                  <div className="ws-aws-meta muted">
                    {[p.label && p.label !== p.name ? p.name : "", p.accountId, p.roleName].filter(Boolean).join(" · ")}
                  </div>
                  {(end || p.state !== "signed_in" || isActive(p)) && (
                    <div className="ws-aws-foot">
                      {end ? (
                        <span className={p.expiring ? "warn" : "muted"}>
                          {tr("wsbar.aws.ends", { time: fmtDateTime(end, TIME_HM) })}
                        </span>
                      ) : (
                        <span />
                      )}
                      <span className="ws-aws-actions">
                        {(p.state !== "signed_in" || p.expiring) && (
                          <button
                            type="button"
                            className="ghost ws-aws-login"
                            onClick={() => {
                              setOpen(false);
                              showProfile(p);
                            }}
                          >
                            {tr("wsbar.aws.login")}
                          </button>
                        )}
                        {isActive(p) && (
                          <button
                            type="button"
                            className="ghost ws-aws-logout"
                            title={tr("awslogin.logout_title")}
                            disabled={loggingOut[p.name] === true}
                            onClick={() => {
                              // The confirm dialog sits outside the popover, whose dismiss
                              // layer would close it on the first press there.
                              setOpen(false);
                              void logout(p);
                            }}
                          >
                            {tr("awslogin.logout")}
                          </button>
                        )}
                      </span>
                    </div>
                  )}
                </li>
              );
            })}
          </ul>
          <div className="wu-note muted">
            {tr("wsbar.aws.no_default")} <code className="ws-aws-cmd">af-aws-exec --profile {tr("wsbar.aws.cmd_name")}</code>
          </div>
          <button
            type="button"
            className="wu-manage"
            onClick={() => {
              setOpen(false);
              openSettings("ssm");
            }}
          >
            <Icon name="settings-gear" /> {tr("wsbar.aws.settings")}
          </button>
        </div>
      )}
    </div>
  );
}
