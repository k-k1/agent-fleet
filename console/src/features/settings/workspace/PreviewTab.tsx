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
import { useEffect, useRef, useState } from "react";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { api, apiJSON, errText } from "../../../core/api/client.ts";
import { OnOff, Row } from "../parts/controls.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { Button } from "../../../ui/Button.tsx";
import { DevIdpSection } from "./DevIdpSection.tsx";
import { useSettingsUI } from "../store.ts";
import { previewInfoFrom, previewScopeUnchanged, previewToken, usePreviewStore } from "../../preview/previewState.ts";

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

// Writes go one after another, so their answers settle in the order they were sent and the last
// save is the one the shared state ends on (a field blur and a toggle click can overlap).
// What a queued write resolves to when its scope changed before it was sent.
const CANCELLED = Symbol("cancelled");
// What a queued request resolves to when it was sent and its answer was lost.
const LOST = Symbol("lost");
let writeChain: Promise<unknown> = Promise.resolve();
function serialized<T>(fn: () => Promise<T>): Promise<T> {
  const run = writeChain.then(fn, fn);
  writeChain = run.catch(() => {});
  return run;
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
    const from = previewToken(); // taken NOW, when the person acted, not when the queue gets to it
    const res = await serialized(async () => {
      // A write queued under another tenant / identity is cancelled, not sent: fetch stamps the
      // tenant current at send time, so it would land on the wrong workspace.
      if (!previewScopeUnchanged(from)) return CANCELLED;
      const r = await apiJSON("api/env/ws-settings", "PUT", patch);
      if (r && !r.error) usePreviewStore.getState().set(previewInfoFrom(r), from); // the port controls follow
      return r;
    });
    if (res === CANCELLED) return;
    if (res && !res.error) {
      setAu(res);
    } else {
      // A refusal carries its reason (e.g. the dev IdP is not enabled on this deployment);
      // a bare "save failed" would leave the person guessing.
      const why = res?.error ? errText(res.error) : "";
      toast(why ? `${tr("common.save_failed")}: ${why}` : tr("common.save_failed"));
    }
  };

  // Rotating the dev IdP client secret goes through the same queue and scope guard as every
  // other write, so a slow earlier save cannot answer afterwards and bring the old client id
  // back. The new secret reaches the container only at its next start, which the confirm text
  // and the toast say. A lost answer may still have rotated: re-read the settings instead of
  // retrying (a retry would rotate again), and when that fails too show no client id rather than
  // an old one as if it were current.
  const rotateDevIdp = async () => {
    const ok = await askConfirm({
      title: tr("env.devidp_rotate_confirm_title"),
      body: tr("env.devidp_rotate_confirm_body"),
      confirmLabel: tr("env.devidp_rotate_go"),
      danger: true,
    });
    if (!ok) return;
    const from = previewToken();
    const res = await serialized(async () => {
      if (!previewScopeUnchanged(from)) return CANCELLED;
      try {
        return await apiJSON("api/env/ws-settings/dev-idp/rotate", "POST", {});
      } catch {
        return LOST;
      }
    });
    if (res === CANCELLED) return;
    if (res === LOST) {
      const fresh = await serialized(async () => {
        try {
          return await api("api/env/ws-settings");
        } catch {
          return null;
        }
      });
      if (fresh && !fresh.error) {
        setAu(fresh);
        toast(tr("env.devidp_rotate_lost_reloaded"));
      } else {
        setAu((prev: any) => ({ ...prev, devIdpClientId: "" }));
        toast(tr("env.devidp_rotate_lost_unknown"));
      }
      return;
    }
    if (res && !res.error) {
      setAu((prev: any) => ({ ...prev, devIdpClientId: res.devIdpClientId }));
      toast(tr("env.devidp_rotated"));
    } else {
      const why = res?.error ? errText(res.error) : "";
      toast(why ? `${tr("common.save_failed")}: ${why}` : tr("common.save_failed"));
    }
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
    const from = previewToken();
    const res = await serialized(async () => {
      if (!previewScopeUnchanged(from)) return CANCELLED;
      const r = await apiJSON("api/env/ws-settings/preview/reissue", "POST", {});
      if (r && !r.error) usePreviewStore.getState().set(previewInfoFrom(r), from);
      return r;
    });
    if (res === CANCELLED) return;
    if (res && !res.error) {
      // The reissue answer carries the preview block only (no dev IdP fields), so it is merged
      // over what is shown instead of replacing it, or the IdP switch would vanish.
      setAu((prev: any) => ({ ...prev, ...res }));
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
        <PreviewSection au={au} save={save} reissue={reissue} rotateDevIdp={rotateDevIdp} />
      )}
    </div>
  );
}

function PreviewSection({
  au,
  save,
  reissue,
  rotateDevIdp,
}: {
  au: any;
  save: (patch: Record<string, unknown>) => void;
  reissue: () => void;
  rotateDevIdp: () => void;
}) {
  const tr = useT();
  const [ports, setPorts] = useState((au.previewPorts || []).join(", "));
  // The issued URLs (empty while stopped: the CP returns nothing it has not issued).
  // Sorted by port number — object key order can put 8080 before 3000.
  const issuedPorts = Object.keys(au.previewUrls || {}).sort((a, b) => Number(a) - Number(b));
  // Save only on blur. A PUT per keystroke would persist half-typed input such as
  // "3000, 80" and end up allowing port 80.
  // "Expose this port…" from a port control asks to add one port. Adding is an explicit act
  // here (the field above saves on blur, so prefilling it would persist on the next click
  // anywhere). Nothing is saved until Add; Cancel or closing the dialog saves nothing.
  const addPort = useSettingsUI((st) => st.previewAddPort);
  const clearAddPort = useSettingsUI((st) => st.clearPreviewAddPort);
  const current: number[] = au.previewPorts || [];
  const maxPorts: number = au.previewMaxPorts || 8;
  const showAdd = addPort != null && !current.includes(addPort);
  const atCap = current.length >= maxPorts;
  const confirmAdd = () => {
    if (addPort == null) return;
    const next = [...current, addPort];
    setPorts(next.join(", "));
    save({ previewPorts: next });
    clearAddPort();
  };
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
      {showAdd && (
        <div className="pv-add-strip" role="alert">
          <span>
            {atCap
              ? tr("env.preview_add_cap", { port: addPort!, n: maxPorts })
              : tr("env.preview_add_ask", { port: addPort! })}
          </span>
          {!atCap && (
            <Button variant="primary" small onClick={confirmAdd}>
              {tr("env.preview_add_go")}
            </Button>
          )}
          <Button variant="ghost" small onClick={clearAddPort}>
            {atCap ? tr("common.close") : tr("env.preview_add_cancel")}
          </Button>
        </div>
      )}
      {/* Show what is currently assigned before any setting is touched. Without it both
          the port list and reissue act on something invisible, and pressing them shows no
          change (reissue was reported as doing nothing). */}
      <Row label={tr("env.preview_current_label")}>
        <span className="ds-note pv-current">
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
      <p className="muted ds-note">
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
      <p className="muted ds-note">{tr("env.preview_ports_note", { n: au.previewMaxPorts || 8 })}</p>
      <Row label={tr("env.preview_fixed_label")}>
        <OnOff value={!!au.previewFixedSlug} onChange={(on) => save({ previewFixedSlug: on })} />
      </Row>
      <p className="muted ds-note">{tr("env.preview_fixed_note")}</p>
      {/* Sharing within the same tenant (docs/log/81 §14). Placed before the public
          toggle so that someone who only wants to show colleagues does not reach for
          public mode to do it. */}
      <Row label={tr("env.preview_share_label")}>
        <OnOff value={!!au.previewTenantShare} onChange={(on) => save({ previewTenantShare: on })} />
      </Row>
      <p className="muted ds-note">{tr("env.preview_share_note")}</p>
      <Row label={tr("env.preview_public_label")}>
        <OnOff value={!!au.previewPublic} onChange={(on) => save({ previewPublic: on })} />
      </Row>
      <p className="muted ds-note">{tr("env.preview_public_note")}</p>
      <Row label={tr("env.preview_cross_origin_label")}>
        <OnOff value={!!au.previewCrossOrigin} onChange={(on) => save({ previewCrossOrigin: on })} />
      </Row>
      <p className="muted ds-note">{tr("env.preview_cross_origin_note")}</p>
      <Row label={tr("env.preview_reissue_label")}>
        <button className="ghost" onClick={reissue}>
          {tr("env.preview_reissue")}
        </button>
      </Row>
      <p className="muted ds-note">{tr("env.preview_reissue_note")}</p>
      {/* Only where the operator turned the IdP on: a switch that every PUT would refuse
          must not be offered. */}
      {au.devIdpAvailable && (
        <>
          <DevIdpSection au={au} rotate={rotateDevIdp} />
          <PersonaEditor />
        </>
      )}
    </section>
  );
}

interface Persona {
  id: string;
  handle: string;
  name: string;
  email: string;
  sub: string;
}

// Test personas (ADR 0116, #2223): extra identities the consent page offers next to the member
// themself, for exercising role checks. Their own endpoint, not ws-settings, so a persona edit
// never queues behind (or clobbers) a preview save.
function PersonaEditor() {
  const tr = useT();
  const toast = useToast();
  const [list, setList] = useState<Persona[] | null>(null);
  const [handle, setHandle] = useState("");
  const [name, setName] = useState("");
  const [domain, setDomain] = useState("example.test");
  const [busy, setBusy] = useState(false);
  // Every request takes a number when it starts and only the newest may write the list: a slow
  // first load answering after an add must not wipe what the add just showed.
  const latest = useRef(0);
  const alive = useRef(true);
  // Set in setup as well as cleared in cleanup: StrictMode runs setup, cleanup, setup, and a flag
  // that only ever turns false would drop every later answer.
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  // run sends one request and applies its answer if no newer request was started meanwhile.
  // Returns whether the server accepted it. Failure of any kind clears busy and says why.
  const run = async (send: () => Promise<any>): Promise<boolean> => {
    const mine = ++latest.current;
    setBusy(true);
    try {
      const res = await send();
      if (!alive.current) return false;
      if (res && !res.error) {
        if (mine === latest.current) {
          setList(res.personas || []);
          if (res.domain) setDomain(res.domain);
        }
        return true;
      }
      const why = res?.error ? errText(res.error) : "";
      toast(why ? `${tr("common.save_failed")}: ${why}` : tr("common.save_failed"));
      return false;
    } catch {
      if (alive.current) toast(tr("common.save_failed"));
      return false;
    } finally {
      if (alive.current && mine === latest.current) setBusy(false);
    }
  };

  useEffect(() => {
    void run(() => api("api/env/ws-settings/dev-idp/personas"));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const add = async () => {
    const h = handle.trim();
    if (!h || busy) return;
    if (await run(() => apiJSON("api/env/ws-settings/dev-idp/personas", "POST", { handle: h, name: name.trim() }))) {
      setHandle("");
      setName("");
    }
  };
  const remove = async (id: string) => {
    if (busy) return;
    await run(() => apiJSON(`api/env/ws-settings/dev-idp/personas/${encodeURIComponent(id)}`, "DELETE"));
  };

  return (
    <>
      <h4 className="ds-title">{tr("env.persona_title")}</h4>
      <p className="muted ds-note">{tr("env.persona_note", { domain })}</p>
      {(list || []).map((p) => (
        <Row key={p.id} label={p.name || p.handle}>
          <code className="ds-note">{p.email}</code> <code className="ds-note">{p.sub}</code>{" "}
          <button className="ghost" disabled={busy} onClick={() => void remove(p.id)} aria-label={`${tr("env.persona_remove")}: ${p.handle}`}>
            {tr("env.persona_remove")}
          </button>
        </Row>
      ))}
      <Row label={tr("env.persona_add_label")}>
        <input
          className="ds-select"
          value={handle}
          onChange={(e) => setHandle(e.target.value)}
          placeholder={tr("env.persona_handle_ph")}
          aria-label={tr("env.persona_handle")}
          spellCheck={false}
          maxLength={32}
        />{" "}
        <input
          className="ds-select"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={tr("env.persona_name_ph")}
          aria-label={tr("env.persona_name")}
          maxLength={64}
        />{" "}
        <button className="ghost" onClick={() => void add()} disabled={!handle.trim() || busy}>
          {tr("env.persona_add")}
        </button>
      </Row>
    </>
  );
}
