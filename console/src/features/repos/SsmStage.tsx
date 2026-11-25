// SsmStage — the SSM host picker and its start flow, shared by the Start hub's SSM stage and the
// Terminal view's "Start a terminal" dialog so both list the same hosts and start through the
// same path. The caller owns the SSO device-code handshake: `onStarted` gets the new session's
// name (empty when the create answered without one) and shows `SsmLoginModal` for it.
import { useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { Button } from "../../ui/Button.tsx";
import { NO_AUTOFILL } from "../../ui/noAutofill.ts";
import { t, useT } from "../../lib/i18n/index.ts";
import { Trans } from "../../lib/i18n/Trans.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useSettings, setSetting } from "../../lib/settings.ts";
import { autoAddToActiveWorkingSet } from "../../lib/workingSetsStore.ts";
import { hostColorBase } from "../../lib/termcolor.ts";
import { useSettingsUI } from "../settings/store.ts";
import type { SsmHost } from "../../types/session.ts";

export interface SsmProfile {
  id: string;
  label: string;
  /** SSO account id. An attribute of the PROFILE, not of the host (it lives on the
   * control-plane's ssmProfileDTO). The host cards' subtitle reads it from here too — see
   * ssmAcctLabel below. */
  accountId: string;
}

interface SsmInstance {
  instanceId: string;
  name?: string;
  computerName?: string;
  ipAddress?: string;
  platformName?: string;
  pingStatus: string;
}

interface SsmStageProps {
  busy: boolean;
  setBusy: (b: boolean) => void;
  /** The profiles the caller already fetched; null while unfetched. */
  ssmProfiles: SsmProfile[] | null;
  /** A kind=ssm session was created: the SSO handshake for `name` is the caller's to show. */
  onStarted: (name: string) => void;
  /** "modal": own footer (back / cancel / connect) for a dialog whose body is this stage.
   *  "inline": the connect button follows the fields, for a dialog with other sections. */
  layout: "modal" | "inline";
  onBack?: () => void;
  onClose: () => void;
}

export function SsmStage({ busy, setBusy, ssmProfiles, onStarted, layout, onBack, onClose }: SsmStageProps) {
  const tr = useT();
  const toast = useToast();
  const settings = useSettings();
  const [ssmHosts, setSsmHosts] = useState<SsmHost[] | null>(null); // null = not fetched yet
  const [ssmHostId, setSsmHostId] = useState("");
  const [ssmQuery, setSsmQuery] = useState("");
  const [ssmProfileId, setSsmProfileId] = useState("");
  const [ssmInstances, setSsmInstances] = useState<SsmInstance[] | null>(null);
  const [ssmInstanceQuery, setSsmInstanceQuery] = useState("");
  const [ssmSearching, setSsmSearching] = useState(false);
  const [ssmSearchError, setSsmSearchError] = useState("");
  const [ssmForce, setSsmForce] = useState(false);
  useEffect(() => {
    if (ssmHosts !== null) return;
    let alive = true;
    api("api/ssm/hosts")
      .then((hosts) => alive && setSsmHosts(Array.isArray(hosts) ? hosts : []))
      .catch(() => alive && setSsmHosts([]));
    return () => {
      alive = false;
    };
  }, [ssmHosts]);
  const visibleSsmHosts = (ssmHosts || []).filter((h) => {
    const q = ssmQuery.trim().toLocaleLowerCase();
    return !q || h.alias.toLocaleLowerCase().includes(q) || h.instanceId.toLocaleLowerCase().includes(q);
  });
  // Quick-connect cards. Rank the (query-filtered) hosts by usage frequency, tie-broken by
  // most-recent use. When few hosts are registered (≤ SSM_CARD_ALL_MAX) show them ALL as
  // cards and drop the dropdown; otherwise surface only the top SSM_CARD_TOP as cards and
  // keep the dropdown for the long tail.
  const SSM_CARD_ALL_MAX = 8;
  const SSM_CARD_TOP = 6;
  // The profile a host refers to. Both the label and the account id are attributes of the
  // PROFILE, not of the host, so both are read from this one place. Reading `h.accountId`
  // instead always yields undefined — ssmHostDTO does not expose accountId — and the account
  // part of the card subtitle then never renders, silently, because the field is optional and
  // the type check stays quiet. Leave the wire alone and ride on this path, which already
  // resolves the profile.
  const ssmProfileOf = (pid: string) => ssmProfiles?.find((p) => p.id === pid);
  const ssmProfileLabel = (pid: string) => ssmProfileOf(pid)?.label || "";
  // While the profiles are unfetched (ssmProfiles === null) this falls back to empty, the same
  // degradation the label already has.
  const ssmAcctLabel = (pid: string) => {
    const acct = ssmProfileOf(pid)?.accountId;
    return acct ? tr("start.ssm_acct", { id: acct }) : "";
  };
  const ssmCardSub = (h: SsmHost) =>
    [ssmProfileLabel(h.profileId), ssmAcctLabel(h.profileId), h.instanceId].filter(Boolean).join(" · ");
  const ssmAllAsCards = (ssmHosts?.length || 0) <= SSM_CARD_ALL_MAX;
  const rankedSsmHosts = [...visibleSsmHosts].sort((a, b) => {
    const ua = settings.ssmHostUsage?.[a.id];
    const ub = settings.ssmHostUsage?.[b.id];
    const ca = ua?.count || 0;
    const cb = ub?.count || 0;
    if (cb !== ca) return cb - ca;
    return (ub?.at || 0) - (ua?.at || 0);
  });
  const ssmCardHosts = ssmAllAsCards ? rankedSsmHosts : rankedSsmHosts.slice(0, SSM_CARD_TOP);
  const searchSsmInstances = async () => {
    const profileId = ssmProfileId || ssmProfiles?.[0]?.id || "";
    if (!profileId || ssmSearching) return;
    setSsmSearchError("");
    setSsmSearching(true);
    const res = await apiJSON("api/ssm/instances", "POST", { profileId });
    setSsmSearching(false);
    if (res?.error) {
      const message = errText(res.error);
      setSsmSearchError(message);
      if (res.error.code !== "ssm_search_forbidden") toast(t("start.aws_search_failed", { msg: message }));
      return;
    }
    setSsmInstances(Array.isArray(res?.instances) ? res.instances : []);
  };
  const visibleSsmInstances = (ssmInstances || []).filter((instance) => {
    const q = ssmInstanceQuery.trim().toLocaleLowerCase();
    return !q || [instance.name, instance.instanceId, instance.computerName, instance.ipAddress]
      .some((value) => value?.toLocaleLowerCase().includes(q));
  });
  const registerSsmInstance = async (instance: SsmInstance) => {
    const profileId = ssmProfileId || ssmProfiles?.[0]?.id || "";
    if (!profileId || busy) return;
    setBusy(true);
    const res = await apiJSON("api/ssm/hosts", "POST", {
      alias: instance.name || instance.computerName || instance.instanceId,
      profileId,
      region: "",
      instanceId: instance.instanceId,
      documentName: "",
    });
    setBusy(false);
    if (res?.error) {
      toast(t("start.host_register_failed", { msg: errText(res.error) }));
      return;
    }
    setSsmHosts((cur) => [...(cur || []), res as SsmHost]);
    setSsmHostId(res.id);
    toast(t("start.host_registered", { id: instance.instanceId }));
  };
  // Start an SSM session. hostId lets a quick-connect card launch its host directly
  // (no setState round-trip); omitted → the dropdown selection (ssmHostId).
  const startSsm = async (hostId?: string) => {
    const id = hostId || ssmHostId;
    if (!id || busy) return;
    setBusy(true);
    const res = await apiJSON("api/sessions", "POST", {
      kind: "ssm",
      ssm_host_id: id,
      ssm_force_login: ssmForce,
      color: hostColorBase(settings.ssmHostColors?.[id], id),
    });
    setBusy(false);
    if (res && res.error) {
      toast(t("start.create_failed", { msg: errText(res.error) }));
      return;
    }
    // Tally usage so the quick-connect cards rank by frequency (recency breaks ties).
    const prev = settings.ssmHostUsage?.[id];
    setSetting("ssmHostUsage", { ...(settings.ssmHostUsage || {}), [id]: { count: (prev?.count || 0) + 1, at: Date.now() } });
    if (res?.name) autoAddToActiveWorkingSet("sessions", res.name); // docs/log/52 §1: repo-less session
    onStarted((res && res.name) || "");
  };

  const connect = !ssmAllAsCards && (
    <Button variant="primary" onClick={() => void startSsm()} disabled={!ssmHostId || busy}>
      {busy ? tr("start.creating") : tr("start.connect")}
    </Button>
  );
  const fields = (
    <>
        <div className="ui-field">
          <span className="ui-field-label">{tr("start.ssm_host_label")}</span>
          {ssmHosts === null ? (
            <p className="sm-muted">{tr("chat.ph_loading")}</p>
          ) : ssmHosts.length === 0 ? (
            <span className="ui-field-hint">
              {tr("start.ssm_no_hosts")}
              <button type="button" className="linklike" onClick={() => useSettingsUI.getState().openSettings("ssm")}>
                {tr("start.settings_ssm")}
              </button>
              {tr("start.register_there")}
            </span>
          ) : (
            <>
              <input {...NO_AUTOFILL}
                type="search"
                value={ssmQuery}
                onChange={(e) => setSsmQuery(e.target.value)}
                placeholder={tr("start.ssm_search_ph")}
              />
              {!ssmAllAsCards && ssmCardHosts.length > 0 && (
                <span className="ui-field-hint">{tr("start.frequent_hosts")}</span>
              )}
              {ssmCardHosts.length > 0 && (
                <div className="ssm-card-grid">
                  {ssmCardHosts.map((h) => (
                    <button
                      type="button"
                      key={h.id}
                      className="ssm-card"
                      disabled={busy}
                      title={tr("start.quick_connect")}
                      onClick={() => void startSsm(h.id)}
                    >
                      <span
                        className="ssm-card-dot"
                        style={{ background: hostColorBase(settings.ssmHostColors?.[h.id], h.id) }}
                        aria-hidden="true"
                      />
                      <span className="ssm-card-body">
                        <span className="ssm-card-alias">{h.alias}</span>
                        <span className="ssm-card-sub">{ssmCardSub(h)}</span>
                      </span>
                    </button>
                  ))}
                </div>
              )}
              {!ssmAllAsCards && (
                <select value={ssmHostId} onChange={(e) => setSsmHostId(e.target.value)}>
                  <option value="">{tr("start.select_host")}</option>
                  {visibleSsmHosts.map((h) => {
                    const acct = ssmAcctLabel(h.profileId); // same path as the card subtitle (the profile)
                    return (
                      <option key={h.id} value={h.id}>
                        {h.alias} — {h.instanceId}
                        {acct ? ` (${acct})` : ""}
                      </option>
                    );
                  })}
                </select>
              )}
              {visibleSsmHosts.length === 0 && <span className="ui-field-hint">{tr("start.no_matching_hosts")}</span>}
              <label className="ssm-check">
                <input type="checkbox" checked={ssmForce} onChange={(e) => setSsmForce(e.target.checked)} />
                {tr("start.force_relogin")}
              </label>
              {/* Two paragraphs, not one <br>-joined block: the warning has to read as
                  its own line rather than as the tail of the wrapped explanation. */}
              <span className="ui-field-hint">
                <Trans k="start.ssm_auth_note" components={[<code />]} />
              </span>
              <span className="ui-field-hint ui-field-hint--warn">
                <Trans k="start.ssm_auth_warn" components={[<b />]} />
              </span>
            </>
          )}
        </div>
        <div className="ui-field">
          <span className="ui-field-label">{tr("start.aws_instances_label")}</span>
          {ssmProfiles && ssmProfiles.length > 1 && (
            // The initial "" matches no option (there is no placeholder), leaving the display
            // undefined. Search/register fall back to profiles[0], so the display is pinned to
            // that same first entry.
            <select value={ssmProfileId || ssmProfiles[0].id} onChange={(e) => setSsmProfileId(e.target.value)}>
              {ssmProfiles.map((p) => <option key={p.id} value={p.id}>{p.label}</option>)}
            </select>
          )}
          <Button small icon="search" onClick={() => void searchSsmInstances()} disabled={ssmSearching}>
            {ssmSearching ? tr("start.searching") : tr("start.search_aws")}
          </Button>
          {ssmSearchError && <span role="alert" className="start-search-error">{ssmSearchError}</span>}
          {ssmInstances !== null && ssmInstances.length > 0 && (
            <input {...NO_AUTOFILL}
              type="search"
              value={ssmInstanceQuery}
              onChange={(e) => setSsmInstanceQuery(e.target.value)}
              placeholder={tr("start.instance_filter_ph")}
            />
          )}
          {visibleSsmInstances.map((instance) => (
            <div key={instance.instanceId} className="ssm-instance-row">
              <span className="start-row-body">
                <span className="start-row-title">{instance.name || instance.computerName || instance.instanceId}</span>
                <span className="start-row-desc">
                  {instance.instanceId}
                  {instance.name && instance.computerName ? ` · ${instance.computerName}` : ""}
                  {instance.ipAddress ? ` · ${instance.ipAddress}` : ""}
                  {instance.platformName ? ` · ${instance.platformName}` : ""}
                </span>
              </span>
              <Button small variant="ghost" onClick={() => void registerSsmInstance(instance)} disabled={busy}>
                {tr("start.register")}
              </Button>
            </div>
          ))}
          {ssmInstances?.length === 0 && <span className="ui-field-hint">{tr("start.no_online_instances")}</span>}
          {ssmInstances !== null && ssmInstances.length > 0 && visibleSsmInstances.length === 0 && (
            <span className="ui-field-hint">{tr("start.no_matching_instances")}</span>
          )}
          <span className="ui-field-hint">{tr("start.sso_expired_note")}</span>
        </div>
    </>
  );
  if (layout === "inline") return <>{fields}{connect}</>;
  return (
    <>
      <div className="ui-modal-body">{fields}</div>
      <footer className="ui-modal-foot">
        {onBack && (
          <Button variant="ghost" className="launch-back" icon="arrow-left" onClick={onBack} disabled={busy}>
            {tr("launch.back_to_hub")}
          </Button>
        )}
        <Button variant="ghost" onClick={onClose} disabled={busy}>
          {tr("common.cancel")}
        </Button>
        {connect}
      </footer>
    </>
  );
}
