// PreviewTab holds the per-workspace preview-subdomain settings (docs/log/81). It is its
// own tab rather than a section of the toolchains tab because "which ports go out" and
// "publish without a login" are a different weight of decision from picking a timezone or
// a JDK, and are hard to find hanging under language-version pickers.
//
// It appears in the rail only where this deployment actually issues preview subdomains (an
// empty AF_PREVIEW_DOMAIN on the CP leaves previewDomain empty, so no URL can ever come
// out); usePreviewAvailable decides, so that no setting is offered that does nothing when
// pressed.
//
// Saving goes through CP-owned ws-settings (PUT /api/env/ws-settings), so it is editable
// while the workspace is stopped. Only the list of issued URLs needs a running workspace
// (they are dropped on stop).
//
// The i18n keys stay `env.preview_*`: a key is an identifier, not a location, so moving the
// UI is not a reason to rewrite every one of them.
import { useEffect, useState } from "react";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { api, apiJSON } from "../../../core/api/client.ts";
import { OnOff, Row } from "../parts/controls.tsx";
import { useT } from "../../../lib/i18n/index.ts";

// Whether this deployment has preview subdomains at all. null = not known yet; the rail
// stays empty until it resolves, so the entry never flashes in and disappears again.
// previewDomain is fixed per deployment, so the answer is reused for the life of the page
// instead of being refetched every time settings open.
let availCache: boolean | null = null;

export function usePreviewAvailable(): boolean | null {
  const [ok, setOk] = useState<boolean | null>(availCache);
  useEffect(() => {
    if (availCache !== null) return;
    let cancelled = false;
    void api("api/env/ws-settings").then(
      (res: any) => {
        const v = !!(res && !res.error && res.previewDomain);
        availCache = v;
        if (!cancelled) setOk(v);
      },
      // A failed fetch (CP unreachable, …) is not the same as "not available", so it is
      // not cached — but it is still hidden for this render: better to hide the entry than
      // to offer a setting that may not exist.
      () => {
        if (!cancelled) setOk(false);
      },
    );
    return () => {
      cancelled = true;
    };
  }, []);
  return ok;
}

export function PreviewTab() {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  // { previewDomain, previewUrls, previewPorts, previewPublic, … } | null
  const [au, setAu] = useState<any>(null);
  const [err, setErr] = useState(false);

  useEffect(() => {
    let cancelled = false;
    void api("api/env/ws-settings").then(
      (res: any) => {
        if (cancelled) return;
        if (res && !res.error) setAu(res);
        else setErr(true);
      },
      () => !cancelled && setErr(true),
    );
    return () => {
      cancelled = true;
    };
  }, []);

  const save = async (patch: Record<string, unknown>) => {
    const res = await apiJSON("api/env/ws-settings", "PUT", patch);
    if (res && !res.error) setAu(res);
    else toast(tr("common.save_failed"));
  };

  // Reissue (docs/log/81 §4.1) throws away URLs that have already been handed out. It
  // cannot be undone, so confirm first and say what will happen (tabs open on the old URL
  // start returning 404).
  const reissue = async () => {
    const ok = await askConfirm({
      title: tr("env.preview_reissue_confirm_title"),
      body: tr("env.preview_reissue_confirm_body"),
      confirmLabel: tr("env.preview_reissue_go"),
      danger: true,
    });
    if (!ok) return;
    const res = await apiJSON("api/env/ws-settings/preview/reissue", "POST", {});
    if (res && !res.error) {
      setAu(res);
      // While stopped there are no issued URLs to throw away, so a successful reissue
      // changes nothing on screen. Always say which of the two happened, or the button
      // looks dead.
      toast(tr(res.previewReissued ? "env.preview_reissue_done" : "env.preview_reissue_nothing"));
    } else toast(tr("common.save_failed"));
  };

  return (
    <div className="display-settings">
      {!au ? (
        <p className="muted pad">{err ? tr("env.fetch_failed") : tr("common.loading")}</p>
      ) : !au.previewDomain ? (
        // Normally unreachable (the rail hides it), but a remembered tab or an old link
        // can land here directly: say the deployment has no preview rather than showing a
        // blank pane.
        <p className="muted pad">{tr("env.preview_unavailable")}</p>
      ) : (
        <PreviewSection au={au} save={save} reissue={reissue} />
      )}
    </div>
  );
}

function PreviewSection({
  au,
  save,
  reissue,
}: {
  au: any;
  save: (patch: Record<string, unknown>) => void;
  reissue: () => void;
}) {
  const tr = useT();
  const [ports, setPorts] = useState((au.previewPorts || []).join(", "));
  // The issued URLs (empty while stopped: the CP returns nothing it has not issued).
  // Sorted by port number — object key order can put 8080 before 3000.
  const issuedPorts = Object.keys(au.previewUrls || {}).sort((a, b) => Number(a) - Number(b));
  // Save only on blur. A PUT per keystroke would persist half-typed input such as
  // "3000, 80" and end up allowing port 80.
  const commitPorts = () => {
    const parsed = ports
      .split(/[\s,]+/)
      .map((v: string) => Number(v))
      .filter((n: number) => Number.isInteger(n) && n >= 1 && n <= 65535);
    save({ previewPorts: parsed });
  };
  return (
    <section className="ds-group">
      <h4 className="ds-title">{tr("env.preview_title")}</h4>
      {/* Show what is currently assigned before any setting is touched. Without it both
          the port list and reissue act on something invisible, and pressing them shows no
          change (reissue was reported as doing nothing). */}
      <Row label={tr("env.preview_current_label")}>
        <span className="ds-sub pv-current">
          {issuedPorts.length > 0 ? (
            issuedPorts.map((p) => (
              <a key={p} className="pv-current-url" href={au.previewUrls[p]} target="_blank" rel="noreferrer noopener">
                {au.previewUrls[p].replace(/^https:\/\//, "")}
              </a>
            ))
          ) : (
            <span className="muted">{tr("env.preview_current_none")}</span>
          )}
        </span>
      </Row>
      <p className="muted ds-sub">
        {/* The domain is shown even while stopped: which domain the workspace maps to is
            the premise of every setting here and does not depend on a URL being issued. */}
        {tr("env.preview_current_note", { domain: au.previewDomain || "" })}
      </p>
      <Row label={tr("env.preview_ports_label")}>
        <input
          className="ds-select"
          value={ports}
          onChange={(e) => setPorts(e.target.value)}
          onBlur={commitPorts}
          onKeyDown={(e) => e.key === "Enter" && commitPorts()}
          placeholder="3000, 8080"
          aria-label={tr("env.preview_ports_label")}
          spellCheck={false}
        />
      </Row>
      <p className="muted ds-sub">{tr("env.preview_ports_note", { n: au.previewMaxPorts || 8 })}</p>
      <Row label={tr("env.preview_fixed_label")}>
        <OnOff value={!!au.previewFixedSlug} onChange={(on) => save({ previewFixedSlug: on })} />
      </Row>
      <p className="muted ds-sub">{tr("env.preview_fixed_note")}</p>
      {/* Sharing within the same tenant (docs/log/81 §14). Placed before the public
          toggle so that someone who only wants to show colleagues does not reach for
          public mode to do it. */}
      <Row label={tr("env.preview_share_label")}>
        <OnOff value={!!au.previewTenantShare} onChange={(on) => save({ previewTenantShare: on })} />
      </Row>
      <p className="muted ds-sub">{tr("env.preview_share_note")}</p>
      <Row label={tr("env.preview_public_label")}>
        <OnOff value={!!au.previewPublic} onChange={(on) => save({ previewPublic: on })} />
      </Row>
      <p className="muted ds-sub">{tr("env.preview_public_note")}</p>
      <Row label={tr("env.preview_cross_origin_label")}>
        <OnOff value={!!au.previewCrossOrigin} onChange={(on) => save({ previewCrossOrigin: on })} />
      </Row>
      <p className="muted ds-sub">{tr("env.preview_cross_origin_note")}</p>
      <Row label={tr("env.preview_reissue_label")}>
        <button className="ghost" onClick={reissue}>
          {tr("env.preview_reissue")}
        </button>
      </Row>
      <p className="muted ds-sub">{tr("env.preview_reissue_note")}</p>
    </section>
  );
}
