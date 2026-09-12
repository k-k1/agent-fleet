import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { apiJSON, errDetail } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { EngineUptimePanel, Sep, useDuration } from "./EngineUptime.tsx";
import { secsUntil, windowIsPartial } from "./engineUptime.ts";
import {
  engineIsExternal,
  engineModes,
  engineTitle,
  useEngineRows,
  type EngineOffer,
  type EngineRow,
} from "./engineTypes.ts";

// The self-hosted inference engines as MACHINES (ADR 0071): one row per engine, each with the
// same off / on-demand / always-on control the VOICEVOX panel has, plus what that engine is
// actually doing right now, which box it is on and what that box has cost.
//
// What it LOADS is the other screen (adminEngineModels.tsx). The split is along the line the
// permissions already follow: everything here is the operator's — it buys and stops a GPU for the
// whole deployment — while a granted tenant_admin may fill the catalogue and never sees this.
//
// Until this existed the only way to switch one off was a CloudFormation parameter
// (`LlmMode` / `ImageMode`), which is not a control anyone reaches for when a GPU is
// misbehaving. The mode was always read from a stored setting — the gateway, the catalogue
// and the controller all consult it — so this panel is the missing half rather than a new
// mechanism.
//
// Mode is what the administrator chose; state is what ECS is doing about it. They are shown
// separately and they disagree on purpose for the minute after "off" (the task is still going
// away), because a panel that echoed ECS back would report the opposite of the button that was
// just pressed.
//
// The status block is written to a rule worth restating whenever it is edited: EVERY LINE IS
// OMITTED RATHER THAN GUESSED. A GPU that costs $1.26/hour is asleep most of the time and the
// CP genuinely does not know some of these things — when a box started before this CP existed,
// how many requests arrived before it was deployed, when an engine pinned "on" will stop (it
// will not). A blank reads as "unknown"; a plausible-looking zero or a fallback date reads as
// fact and gets acted on.

export function EnginesAdminView() {
  const tr = useT();
  const { rows, isSuper, err, setErr, setRows, load } = useEngineRows();
  const [busy, setBusy] = useState("");

  // Poll only while something is actually moving. An engine parked at "off", or stopped under
  // on-demand with nobody asking, is a settled state, and a GPU panel that polls forever is a
  // request per five seconds for a screen nobody is watching.
  useEffect(() => {
    if (!rows) return;
    const moving = rows.some(
      (e) =>
        e.state === "starting" ||
        e.state === "stopping" ||
        (e.mode === "ondemand" && e.state === "running"),
    );
    if (!moving) return;
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, [rows, load]);

  const setMode = async (key: string, mode: string) => {
    setBusy(key);
    try {
      const d = await apiJSON("api/admin/engines/" + encodeURIComponent(key), "PUT", { mode });
      if (d?.error) {
        setErr(errDetail(d.error));
        return;
      }
      setErr("");
      setRows((cur) => (cur || []).map((e) => (e.key === key ? { ...e, ...d } : e)));
    } finally {
      setBusy("");
    }
  };

  /** Stop the box so the next one is bought on the rung that is now chosen. It does NOT touch
   *  the mode: an engine pinned `on` comes back by itself, an on-demand one with the next
   *  request, and the control plane holds that start until the old box has left the cluster. */
  const replaceBox = async (key: string) => {
    setBusy(key);
    try {
      const d = await apiJSON(`api/admin/engines/${encodeURIComponent(key)}/replace-box`, "POST", {});
      if (d?.error) {
        setErr(errDetail(d.error));
        return;
      }
      setErr("");
      setRows((cur) => (cur || []).map((x) => (x.key === key ? { ...x, ...d } : x)));
    } finally {
      setBusy("");
    }
  };

  /** Which GPU this role buys next (ADR 0074). It does NOT replace a running box — the API
   *  reaches new instances only — so the answer carries class_replace_pending and the card
   *  turns that into an explicit "replace it now", which costs a cold start. */
  const setClass = async (key: string, cls: string) => {
    setBusy(key);
    try {
      const d = await apiJSON(
        `api/admin/engines/${encodeURIComponent(key)}/class`,
        "PUT",
        { class: cls },
      );
      if (d?.error) {
        setErr(errDetail(d.error));
        // 🔴 A refusal here is not a request that did nothing: the CP stores the choice before
        // it applies it, so the rung on screen is already stale and the failure it should be
        // showing is only in the fresh row. Without this re-read the picker snaps back to the
        // OLD rung — which is neither what is stored nor what the provider holds — and the
        // retry below has nothing to appear beside.
        await load();
        return;
      }
      setErr("");
      setRows((cur) => (cur || []).map((e) => (e.key === key ? { ...e, ...d } : e)));
    } finally {
      setBusy("");
    }
  };

  /** What this deployment excludes from every image this engine makes. One setting, applied to
   *  every request whoever made it and whichever checkpoint answers.
   *
   * 🔴 Not a content filter, and the panel says so beside the box: the words reach the sampler
   * through the negative branch of classifier-free guidance, which two of the five checkpoint
   * families do not have at all. It is a default, not a gate. */
  const setNegative = async (key: string, negative: string) => {
    setBusy(key + "/negative");
    try {
      const d = await apiJSON(
        `api/admin/engines/${encodeURIComponent(key)}/negative`,
        "PUT",
        { negative },
      );
      if (d?.error) {
        setErr(errDetail(d.error));
        return;
      }
      setErr("");
      setRows((cur) => (cur || []).map((e) => (e.key === key ? { ...e, ...d } : e)));
    } finally {
      setBusy("");
    }
  };

  if (rows === null) return <p className="muted pad">{tr("common.loading")}</p>;

  return (
    <div className="admin-stage">
      {rows.length === 0 && (
        <section className="admin-panel">
          <p className="muted">{tr("admin.engines_none")}</p>
          {/* 🔴 Where the browse used to be. A deployment with no engine still has a question
              worth answering — "what could I run?" — and it is now answered on the models
              screen, which needs neither an engine nor a token to ask Hugging Face. */}
          <p className="muted">{tr("admin.engines_none_models_hint")}</p>
        </section>
      )}
      {/* 🔴 Everything on this screen buys or stops a GPU for the WHOLE deployment, so it is the
          operator's alone. A granted tenant_admin reaches the models screen instead (ADR 0072
          open question 11) and has no door to this one — but the component is reachable from
          tenant settings' own rail, so it says so rather than rendering an empty page. */}
      {!isSuper && rows.length > 0 && <p className="admin-hint pad">{tr("admin.engines_ops_super_only")}</p>}

      {rows.map((e) => (
        <section className="admin-panel" key={e.key}>
          <div className="usage-toolbar">
            <span>{engineTitle(e)}</span>
            {/* The mode buys and stops a GPU for the WHOLE deployment, so it is the operator's
                and is not rendered at all for anyone else. Not disabled: a greyed-out row of
                buttons invites an email asking to have them enabled.
                An external row gets two: `off` closes the route, `on` opens it, and `ondemand`
                has nothing to buy or stop — the API refuses it with 400 (ADR 0076 decision 5). */}
            {isSuper && (
              <span className="seg sm">
                {engineModes(e).map((m) => (
                  <button
                    key={m}
                    type="button"
                    className={"seg-btn" + (e.mode === m ? " active" : "")}
                    disabled={busy === e.key}
                    onClick={() => setMode(e.key, m)}
                  >
                    {tr(("admin.tts_mode_" + m) as never)}
                  </button>
                ))}
              </span>
            )}
            <button type="button" className="ghost" title={tr("admin.refresh")} onClick={load}>
              <Icon name="refresh" />
            </button>
          </div>
          {/* What ECS is doing, and what the engine would hold if it were up. A badge and chips
              rather than one sentence: the state is the fact the rest of this panel is read
              against, and inside "状態: 停止中 / モデル: a, b （宣言。…）" it was a phrase like
              any other. The model names carry no tone of their own — whether they are in VRAM
              is the note that follows them, and it is not the same claim. */}
          {/* The box: whether it is up, which models are in VRAM. Operator-only because it is
              the state of a machine only the operator starts, stops and pays for — and because
              the reduced row does not carry the fields it is drawn from. */}
          {isSuper && (
          <div className="engines-state">
            <span className={"engines-model-tag " + engineStateTone(e)}>{engineStateLabel(e, tr)}</span>
            {/* Where an external engine points. The one fact this panel can give about a machine
                it does not own — and without it "externally managed" names nothing an operator
                could go and look at. Printed verbatim, as the CP composed it. */}
            {engineIsExternal(e) && e.url ? (
              <>
                <span className="engines-fact-label">{tr("admin.engines_url_label")}</span>
                <span className="mono engines-model-tag">{e.url}</span>
              </>
            ) : null}
            {e.models?.length ? (
              <>
                <span className="engines-fact-label">{tr("admin.engines_models_label")}</span>
                {e.models.map((m) => (
                  <span key={m} className="mono engines-model-tag">
                    {m}
                  </span>
                ))}
                <span className="muted">
                  {tr(e.warm ? "admin.engines_model_loaded" : "admin.engines_model_declared")}
                </span>
              </>
            ) : null}
          </div>
          )}
          {isSuper && <EngineStatus row={e} />}
          {/* The GPU ladder is a choice of box to buy. An external row has no box and no rungs,
              so the section is absent rather than empty (ADR 0076 decision 1). */}
          {isSuper && !engineIsExternal(e) && (
            <EngineClassPicker
              row={e}
              busy={busy === e.key}
              onPick={(cls) => setClass(e.key, cls)}
              onReplace={() => replaceBox(e.key)}
            />
          )}
          {/* What this deployment will not draw. On the MACHINE panel rather than with the
              catalogue because it is one answer for the whole engine, and super-admin like the
              mode and the rung: it is a statement about the deployment. */}
          {isSuper && e.api === "images" && e.negative_always !== undefined && (
            <EngineNegative
              row={e}
              busy={busy === e.key + "/negative"}
              onSave={(v) => setNegative(e.key, v)}
            />
          )}
          {/* Always-on is a warning about an hourly bill. An external engine is always on by
              default and costs this deployment nothing, so the warning would be pure noise. */}
          {isSuper && e.mode === "on" && !engineIsExternal(e) && (
            <p className="form-err">{tr("admin.engines_always_on_note")}</p>
          )}
          {isSuper && e.error && <p className="form-err">{e.error}</p>}
          {/* The events are the only place ECS says why a start failed ("no container
              instances met the placement constraints", a pull failure). An engine stuck in
              `starting` is exactly when somebody needs them, and the alternative is a trip to
              the AWS console for a string the CP already has. */}
          {isSuper && e.state === "starting" && e.events?.length ? (
            <p className="muted mono engines-events">{e.events.join(" | ")}</p>
          ) : null}
          {/* Uptime is billing history for a machine somebody else pays for, and the route
              behind it is super_admin anyway — rendering it would be a permanent spinner.
              An external row has no history at all: the samples are written by the control loop's
              tick, and an external engine has no control loop (ADR 0076 decision 8). An empty
              14-day grid would read as "it was down for two weeks". */}
          {isSuper && !engineIsExternal(e) && <EngineHistory engineKey={e.key} />}
        </section>
      ))}
      {err && <p className="form-err pad">{err}</p>}
      {/* What the modes do. Same reason as the sentence in EngineModels: it explains the segment
          above, and that segment is the operator's. Two sentences because there are two segments:
          a deployment whose only engine is a URL on the LAN would otherwise be told that on-demand
          buys instances, with no on-demand button anywhere on the screen. */}
      {isSuper && rows.some((e) => !engineIsExternal(e)) && (
        <p className="muted pad">{tr("admin.engines_note")}</p>
      )}
      {isSuper && rows.some(engineIsExternal) && (
        <p className="muted pad">{tr("admin.engines_note_external")}</p>
      )}
    </div>
  );
}

/** Which GPU this role buys (ADR 0074).
 *
 * Absent entirely on a deployment that declares no ladder — that is decision 3, and it has to
 * look like "there is no such choice here", not like a control that does nothing.
 *
 * Three things this section must keep saying, because each one is a bill somebody would
 * otherwise only find later:
 *
 *   - a saved rung reaches the NEXT box only. Reporting success while the old card keeps
 *     answering is the most expensive lie available here, so the replace step is separate,
 *     explicit, and priced;
 *   - running on something other than the default is stated permanently. "Temporarily try a
 *     bigger box" turns into a permanent hourly bill exactly when nobody is reminded;
 *   - a model that will not fit is pointed at, with the STRENGTH of the evidence attached:
 *     a measured number and a weights-only floor are different claims, and "nobody measured
 *     this" is never drawn as "it fits";
 *   - a rung that was saved but could not be APPLIED offers its own retry. The select cannot be
 *     one: the choice is stored before it is applied, so after a failure this picker already
 *     shows that rung and re-picking it fires no change event at all. Recovery was a detour
 *     through another rung until this button existed (ADR 0074). */
/** The engine-wide exclusion list (ADR 0072 follow-up, negative prompts).
 *
 * A local draft with an explicit save rather than saving as you type: every keystroke would be
 * a PUT that fans out to every running workspace, and an exclusion list half-typed is a list
 * that excludes the wrong thing.
 *
 * 🔴 The note under it is not decoration. These words reach the sampler through the negative
 * branch of classifier-free guidance — a nudge, not a gate — and three of the five checkpoint
 * families sample where it cannot matter, which the generated result reports in its own
 * warnings. A panel that let this be read as a content filter would be the most expensive kind
 * of wrong. */
function EngineNegative({
  row,
  busy,
  onSave,
}: {
  row: EngineRow;
  busy: boolean;
  onSave: (v: string) => void;
}) {
  const tr = useT();
  const saved = row.negative_always || "";
  const [draft, setDraft] = useState(saved);
  // The server's value wins whenever it changes under us (another admin, a reload), but only
  // then: re-running this on every render would delete what is being typed.
  useEffect(() => setDraft(saved), [saved]);
  const max = row.negative_max || 500;
  const tooLong = draft.length > max;
  return (
    <div className="engines-negative">
      <label className="engines-model-add-row">
        <span>{tr("admin.engines_negative_label")}</span>
        <input
          type="text"
          value={draft}
          maxLength={max + 1}
          placeholder={tr("admin.engines_negative_placeholder") as string}
          onChange={(ev) => setDraft(ev.currentTarget.value)}
        />
      </label>
      <div className="engines-model-add-actions">
        <button
          type="button"
          className="btn-secondary"
          disabled={busy || tooLong || draft === saved}
          onClick={() => onSave(draft.trim())}
        >
          {tr("admin.engines_negative_save")}
        </button>
      </div>
      <p className="muted">{tr("admin.engines_negative_note")}</p>
      {tooLong && <p className="form-err">{tr("admin.engines_negative_too_long")}</p>}
    </div>
  );
}

function EngineClassPicker({
  row,
  busy,
  onPick,
  onReplace,
}: {
  row: EngineRow;
  busy: boolean;
  onPick: (cls: string) => void;
  onReplace: () => void;
}) {
  const tr = useT();
  // ADR 0075 decision 1: where a deployment declares offers, THEY are the ladder — the same rungs
  // with the purchase form attached — and `classes` is what a control plane too old to send them
  // serves instead. `buy` is normalised here and only here, so everything below can say the form
  // of every offer and nothing below can say one of a rung that never had one.
  const offers = (row.offers || []).map((o) => ({ ...o, buy: o.buy || "od" }));
  const auto = offers.length > 0;
  const classes: EngineOffer[] = auto ? offers : row.classes || [];
  if (classes.length === 0) return null;
  // 🔴 What the administrator STORED, which is not what is running. Under offers an empty value
  // is automatic (decision 8) and `row.class` then names the CP's own pick — so the picker may
  // only sit on a rung when the CP says the choice is pinned, or "automatic" would flip to a
  // pin the moment the first box was bought.
  const pinned = auto && row.class_is_default === false;
  const current = auto ? (pinned && row.class?.id) || "" : row.class?.id || row.class_default || "";
  // The box that is answering right now, when it is not one this rung covers. It is read from
  // the container instance rather than from the capacity provider, because the provider
  // describes the NEXT box.
  const oldBox =
    row.box?.instance_type && row.class && !row.class.types.includes(row.box.instance_type)
      ? row.box.instance_type
      : "";
  const trail = row.offer_trail || [];
  return (
    <div className="engines-class">
      {/* Which offer is answering, in the panel's first line about this box. Decision 11 takes it
          from the service's strategy and not from what the CP chose: a remembered choice starts
          lying the moment CloudFormation rewrites the service, which is the shape of the ADR 0074
          rename that reported "starting on l4" beside a null box. */}
      {row.offer && (
        <p className="engines-offer-now">
          <span className="muted">{tr("admin.engines_offer_now")}</span>
          <span className="engines-offer-label">{engineOfferName(row.offer.id, offers)}</span>
          <span className="engines-model-tag">{engineBuyLabel(row.offer.buy, tr)}</span>
        </p>
      )}
      <div className="engines-class-head">
        <span className="muted">{tr("admin.engines_class")}</span>
        <select
          className="sm"
          value={current}
          disabled={busy}
          onChange={(ev) => onPick(ev.currentTarget.value)}
        >
          {/* Not selecting is a choice with a name (decision 8). Without this entry the only way
              back from a pin would be to pick the offer that happens to be the default — which
              is a different thing: it would still refuse to fall through to the next one. */}
          {auto && <option value="">{tr("admin.engines_class_auto")}</option>}
          {classes.map((c) => (
            <option key={c.id} value={c.id}>
              {engineClassLabel(c, tr)}
            </option>
          ))}
        </select>
        {row.class_is_default === false && (
          <>
            <span className="engines-model-tag">
              {tr(auto ? "admin.engines_class_pinned" : "admin.engines_class_not_default")}
            </span>
            {/* One click back, as ADR 0074 decision 7 required — to AUTOMATIC under offers, which
                is the empty stored value, and to the default rung without them. */}
            <button
              type="button"
              className="sm"
              disabled={busy}
              onClick={() => onPick(auto ? "" : row.class_default || "")}
            >
              {tr(auto ? "admin.engines_class_unpin" : "admin.engines_class_reset")}
            </button>
          </>
        )}
      </div>
      {/* The list itself, because a collapsed select shows one row and the question this answers
          is a comparison. 🔴 Drawn in DECLARATION order and never sorted by price: the order is
          the try order and it is the operator's, and re-ordering it here would let one number
          they wrote silently overrule the sequence they wrote (decision 1). */}
      {auto && (
        <>
          <p className="muted">{tr("admin.engines_offers_head")}</p>
          <ul className="engines-offers">
            {offers.map((o, i) => (
              <li key={o.id} className={row.offer?.id === o.id ? "engines-offer on" : "engines-offer"}>
                <span className="mono engines-offer-order">{i + 1}</span>
                <span className="engines-offer-label">{o.label}</span>
                <span className="engines-model-tag">{engineBuyLabel(o.buy, tr)}</span>
                <span className="muted">
                  {(tr("admin.engines_model_vram" as never) as string).replace("{n}", String(o.vram_mib))}
                </span>
                {/* Declared or nothing. An invented 0 reads as free (ADR 0074), and the number
                    that IS there is the dearest type this row can buy — see EngineOffer. */}
                {o.usd_per_hour ? <span className="muted mono">{"$" + o.usd_per_hour + "/h"}</span> : null}
              </li>
            ))}
          </ul>
        </>
      )}
      {/* How far down the list this demand walked, and why each one was left. Only from two
          entries: one entry is "it was bought on the first offer", which the line above already
          says, and a trail of one reads as a failure that did not happen. */}
      {trail.length > 1 && (
        <p className="muted engines-offer-trail">
          <span>{tr("admin.engines_offer_trail")}</span>
          {trail.map((t, i) => (
            <span key={i} className="engines-offer-try">
              {/* Numbered, because the attempts are a row of chips and rendered headless the
                  four of them read as one long line (measured). The number is also the fact:
                  the same offer can appear twice, so "which try is this" is not derivable. */}
              <span className="mono engines-offer-order">{i + 1}</span>
              <span>{engineOfferName(t.id, offers)}</span>
              <span className="engines-model-tag">{engineBuyLabel(t.buy, tr)}</span>
              {t.result && (
                <span className="engines-model-tag">{engineOfferResultText(t.result, tr)}</span>
              )}
            </span>
          ))}
        </p>
      )}
      {/* Saved, not applied. The provider's own words are quoted rather than summarised: a
          missing IAM grant and a throttle need different things from the person reading them.
          The retry re-sends the rung already selected, which is the one request the select can
          never produce. */}
      {row.class_apply_error && (
        <div className="engines-class-pending">
          <p className="form-err">
            {tr("admin.engines_class_apply_failed").replace("{m}", row.class_apply_error)}
          </p>
          <button type="button" className="sm" disabled={busy} onClick={() => onPick(current)}>
            {tr("admin.engines_class_apply_retry")}
          </button>
        </div>
      )}
      {/* The saved rung has reached nothing yet: a box of another type is still up. Both halves
          are said — that the change is pending, and that acting on it costs a cold start. */}
      {oldBox && (
        <div className="engines-class-pending">
          <p className="form-err">{tr("admin.engines_class_pending").replace("{t}", oldBox)}</p>
          {/* On its own line rather than trailing the sentence. Rendered headless it read as
              part of the paragraph — and this is the button that costs a cold start, so it has
              to look like one before somebody presses it by accident. */}
          <button type="button" className="sm" disabled={busy} onClick={onReplace}>
            {tr("admin.engines_class_replace")}
          </button>
        </div>
      )}
      <p className="muted">{engineClassVramNote(row, tr)}</p>
    </div>
  );
}

/** One rung as an option: the operator's label, its VRAM, the purchase form where there is one,
 *  and the price only when they declared one. A missing price prints nothing — an invented 0
 *  would read as free. `buy` is absent for a rung of the ADR 0074 ladder and set for every offer
 *  (the picker normalises it), so this prints nothing extra for an old control plane. */
function engineClassLabel(c: EngineOffer, tr: (k: never) => string): string {
  const bits = [c.label, (tr("admin.engines_model_vram" as never) as string).replace("{n}", String(c.vram_mib))];
  if (c.buy) bits.push(engineBuyLabel(c.buy, tr));
  if (c.usd_per_hour) bits.push("$" + c.usd_per_hour + "/h");
  return bits.join(" · ");
}

/** On-demand or Spot, in the reader's language. 🔴 An unrecognised value is printed AS IT CAME:
 *  "od" is the omittable default of the declaration (ADR 0075 decision 1) so an empty one really
 *  is on-demand, but calling some third word on-demand would be the panel inventing the one fact
 *  this column exists to carry. */
function engineBuyLabel(buy: string | undefined, tr: (k: never) => string): string {
  if (buy && buy !== "od" && buy !== "spot") return buy;
  return tr(("admin.engines_offer_buy_" + (buy === "spot" ? "spot" : "od")) as never) as string;
}

/** The label the operator gave this offer, or the id when the answer names one the declaration no
 *  longer holds — which happens while a demand started under the previous ladder is still being
 *  walked. The id is what the CP said; a blank would hide that the two disagree. */
function engineOfferName(id: string, offers: EngineOffer[]): string {
  return offers.find((o) => o.id === id)?.label || id;
}

/** How one attempt ended, as a message key — or "" for a code this Console does not know.
 *
 * 🔴 The codes are the CP's reading of `CreateFleet`'s own response (ADR 0077 decision 8; under
 * ADR 0075 they came from matching ECS service-event strings), so a vocabulary the CP learns adds
 * a value here rather than removing one. Unknown is shown verbatim by the caller instead of being
 * dropped: the raw word is the only clue the next person gets that the table stopped matching. */
export function engineOfferResultKey(result: string | undefined): string {
  const known = [
    "active",
    "unfulfillable",
    "insufficient",
    "quota",
    "budget",
    // ADR 0077: a row `CreateFleet` refuses outright (decision 8), and a box taken away while
    // the service still wanted its task (decision 4 — not a failure, the CP rebuilds).
    "unusable",
    "interrupted",
  ];
  return result && known.includes(result) ? "admin.engines_offer_result_" + result : "";
}

function engineOfferResultText(result: string, tr: (k: never) => string): string {
  const key = engineOfferResultKey(result);
  return key ? (tr(key as never) as string) : result;
}

/** What the enabled models want against what the card has. Three sentences, because the three
 *  cases are not the same claim (ADR 0074 decision 6). */
function engineClassVramNote(row: EngineRow, tr: (k: never) => string): string {
  const have = row.class?.vram_mib || 0;
  if (!have) return "";
  // ⚠️ What the comparison above is, on an engine that can hold more than one model at a time.
  // The number is a MAXIMUM and not a sum (ADR 0074 decision 6, `--models-max 1` and sd-server's
  // one checkpoint) — but comfy chooses a checkpoint per REQUEST, keeps what it loaded cached
  // and only evicts when it needs the room, so several can be resident. The rule is not changed
  // to a sum: comfy evicts rather than dies, and a sum would warn on every start of a deployment
  // with four enabled models, which is the warning nobody reads.
  const many = row.provider === "comfy" ? " " + (tr("admin.engines_class_vram_many" as never) as string) : "";
  if (row.vram_need_source === "unknown" || !row.vram_need_mib) {
    return (tr("admin.engines_class_vram_unknown" as never) as string) + many;
  }
  const key = row.vram_fits === false ? "admin.engines_class_vram_over" : "admin.engines_class_vram_ok";
  return (
    (tr(key as never) as string)
      .replace("{n}", String(row.vram_need_mib))
      .replace("{m}", String(have))
      .replace("{id}", row.vram_need_model || "")
      .replace("{src}", tr(("admin.engines_vram_src_" + (row.vram_need_source || "unknown")) as never) as string) +
    many
  );
}

/** The collapsed history, one 14-day query per engine.
 *
 * ⚠️ The heatmap is mounted from an `open` flag rather than left inside a closed `<details>`.
 * `<details>` hides its children, it does not unmount them: React renders them and the fetch
 * fires anyway, so every engine on the panel would spend a 336-bucket query on load for a
 * section nobody opened. The visual result is identical and the request is not made. */
function EngineHistory({ engineKey }: { engineKey: string }) {
  const tr = useT();
  const [open, setOpen] = useState(false);
  return (
    <details
      className="engines-history"
      onToggle={(ev) => setOpen((ev.currentTarget as HTMLDetailsElement).open)}
    >
      <summary>{tr("admin.engines_history")}</summary>
      {open && <EngineUptimePanel engineKey={engineKey} />}
    </details>
  );
}

/** A clock that ticks only while there is something to count.
 *
 * "Up for" and the stop countdown are live figures, and a status panel that needs a manual
 * refresh to stop being wrong is worse than one that shows nothing. Two things bound the cost:
 * the interval is torn down as soon as there is nothing to count (a deployment whose engines are
 * all parked runs no timer at all), and it ticks every 15 seconds rather than every second —
 * every figure it feeds is rounded to whole minutes, so a one-second tick would be 59 re-renders
 * producing identical text. */
const ENGINE_TICK_MS = 15_000;

function useSecondHand(active: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    const t = setInterval(() => setNow(Date.now()), ENGINE_TICK_MS);
    return () => clearInterval(t);
  }, [active]);
  return now;
}

/** The live half of an engine row: when it started, when it will stop, and what has been asked
 *  of it lately. */
function EngineStatus({ row }: { row: EngineRow }) {
  const tr = useT();
  const dur = useDuration();
  // The box's own registeredAt when there is one, and only then the service's deployment time.
  // They are different facts: `service_since` moves when a stack update or a replaced task
  // changes the deployment, without a new box being bought, and an operator looking at a GPU
  // bill wants to know when THE BOX started.
  // Every figure below describes a box this deployment bought: when it came up, when the
  // controller will stop it, the idle window it is measured against, the demand the control loop
  // counted. An external engine has none of them — the CP omits the fields rather than sending
  // zeros — so the whole ECS half is skipped here too, and only what the GATEWAY knows (which
  // model answered last) survives for an external row.
  const external = engineIsExternal(row);
  const startedAt = external ? undefined : row.box?.since || row.service_since;
  const upKind = row.box?.since ? "admin.engines_since_box" : "admin.engines_since_service";
  const stopIn = external ? undefined : row.stop_eta;
  const now = useSecondHand(!!startedAt || !!stopIn);
  const upSecs = startedAt ? -(secsUntil(startedAt, now) ?? 0) : null;
  const leftSecs = secsUntil(stopIn, now);

  const lines: { key: string; body: ReactNode; warn?: boolean }[] = [];

  if (startedAt && upSecs !== null && upSecs >= 0) {
    lines.push({
      key: "since",
      body: (
        <>
          {tr(upKind as never)}
          <span className="mono">{localStamp(startedAt)}</span>
          <Sep />
          {tr("admin.engines_up_for").replace("{d}", dur(upSecs))}
          {row.box?.id ? <span className="mono engines-boxid"> {row.box.id}</span> : null}
          {/* WHICH card is answering. Once the class is selectable this is not derivable from
              the class shown above: that one describes the next box, and after a change the
              two disagree until this one is replaced (ADR 0074 decision 4). */}
          {row.box?.instance_type ? (
            <span className="mono engines-boxid"> {row.box.instance_type}</span>
          ) : null}
          {/* DRAINING is stopped-but-still-billing: the task is gone, the instance is not.
              Measured 427-477 s on a GPU box, and it is money already spent — which is why
              shortening the idle window below it buys nothing (ADR 0071 決定 7). */}
          {row.box?.status && row.box.status !== "ACTIVE" ? (
            <span className="mono"> ({row.box.status})</span>
          ) : null}
        </>
      ),
    });
  }

  // ABSENT means "no answer", and the CP omits it in exactly the cases where a countdown would
  // be a lie: pinned on (it never stops), switched off, already stopped, or nothing has ever
  // stamped the demand mark. Do not invent a fallback here — the honesty lives in the omission.
  if (leftSecs !== null) {
    lines.push({
      key: "stop",
      body:
        leftSecs > 0 ? (
          <>
            {tr("admin.engines_stops_at")}
            <span className="mono">{localStamp(stopIn!)}</span>
            <Sep />
            {tr("admin.engines_stops_in").replace("{d}", dur(leftSecs))}
          </>
        ) : (
          // The window has elapsed but the controller has not ticked yet (up to 30 seconds).
          // Saying "in -4 seconds" or silently flipping to "stopped" both misdescribe it.
          <>{tr("admin.engines_stops_due")}</>
        ),
    });
  } else if (!external && row.mode === "ondemand" && row.idle_secs) {
    // No live countdown — the engine is not up, so there is nothing to stop. The POLICY is
    // still worth stating, and it is a different claim from a time: "it will go quiet after
    // 30 minutes" rather than "it goes at 10:47". Without it, the operator of a stopped
    // engine has no way to see the window at all.
    lines.push({
      key: "policy",
      body: tr("admin.engines_idle_policy").replace("{d}", dur(row.idle_secs)),
    });
  }

  if (!external && row.window_secs) {
    const partial = windowIsPartial(row);
    lines.push({
      key: "demand",
      warn: partial,
      body: (
        <>
          {tr("admin.engines_recent")
            .replace("{m}", String(Math.round(row.window_secs / 60)))
            .replace("{n}", String(row.window_units ?? 0))}
          {/* ⚠️ The count lives in the CP's memory and nowhere else, so a control plane
              replaced two minutes ago reports 0 while somebody is mid-conversation. Saying so
              is the whole point: an unqualified 0 here is the one number on this panel that
              can be confidently wrong. */}
          {partial && (
            <>
              {" "}
              {tr("admin.engines_recent_partial").replace(
                "{d}",
                dur(row.window_counted_secs ?? 0),
              )}
            </>
          )}
          {/* Persisted (engine_<key>_demand_at), so it survives the restart the count does
              not — which is what makes a zero above readable rather than alarming. */}
          {row.last_demand && (
            <>
              <Sep />
              {tr("admin.engines_last_demand")}
              <span className="mono">{localStamp(row.last_demand)}</span>
            </>
          )}
        </>
      ),
    });
  }

  // Which model is in VRAM right now, and what the taking of turns has cost. A router holding
  // one model at a time (LlmModelsMax=1) reloads on every change of model — 267 s of weights,
  // measured — and an engine that only ever says "warm" hides that entirely (ADR 0072
  // decision 3). Both numbers are this CP process's own, like the demand count above.
  if (row.warm_model || row.model_swaps) {
    lines.push({
      key: "warm-model",
      body: (
        <>
          {row.warm_model && (
            <>
              {tr("admin.engines_warm_model")}
              <span className="mono">{row.warm_model}</span>
            </>
          )}
          {!!row.model_swaps && (
            <>
              {row.warm_model && <Sep />}
              {tr("admin.engines_model_swaps").replace("{n}", String(row.model_swaps))}
            </>
          )}
        </>
      ),
    });
  }

  if (lines.length === 0) return null;
  return (
    <ul className="engines-status muted">
      {lines.map((l) => (
        <li key={l.key} className={l.warn ? "engines-partial" : undefined}>
          {l.body}
        </li>
      ))}
    </ul>
  );
}

/** An instant in the reader's own timezone. The CP speaks UTC throughout (the buckets are cut
 *  there), and an operator deciding whether to stop a GPU should not have to do the arithmetic. */
function localStamp(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

/** The badge colour for a state. `stopped` is the resting state of an on-demand GPU, not a
 *  fault, so it stays neutral — a red one there would cry wolf on every panel load. */
function engineStateTone(e: EngineRow): string {
  if (!e.managed) return "";
  switch (e.state) {
    case "running":
      return "on";
    case "starting":
      return "lead";
    case "stopping":
      return "warn";
    default:
      return "off";
  }
}

function engineStateLabel(e: EngineRow, tr: (k: never) => string): string {
  // Not the TTS panel's wording: that one names a standalone docker container, which is what
  // VOICEVOX is and what a ComfyUI on somebody's LAN is not. The claim both share is the one
  // worth making — this deployment neither starts nor stops it (ADR 0076 decision 1).
  if (!e.managed) return tr("admin.engines_external" as never);
  switch (e.state) {
    case "stopping":
      return tr("admin.tts_stopping" as never);
    case "starting":
      return tr("admin.tts_starting" as never);
    case "running":
      return tr("admin.tts_running" as never);
    default:
      return tr("admin.tts_stopped" as never);
  }
}