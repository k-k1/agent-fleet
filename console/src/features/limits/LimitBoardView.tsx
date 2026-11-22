// LimitBoardView — the opt-in limit board (ADR 0111 decisions 6 and 7, issue #1962).
//
// Two halves, both for the current tenant only: what the member publishes (hidden by default),
// and what teammates publish to them. The CP has already reduced every entry to what this
// viewer may see; a member who publishes nothing to them and a member nobody ever observed
// arrive identical, and both read "unknown" here, never "available".
//
// Members are listed in roster order. There is deliberately no sort by headroom and no chart:
// limits are never pooled or compared (decision 7).
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import type { MsgKey } from "../../lib/i18n/index.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import {
  COVERAGE,
  effectiveStatus,
  kindName,
  sessionText,
  unitLabel,
  unitText,
  type Audience,
  type BoardMe,
  type BoardMember,
  type Level,
  type LimitView,
} from "./limitBoard.ts";
import "./limits.css";
import { useInterval } from "../../lib/useInterval.ts";

const PAGE = 50;
// How often an open board re-reads the CP: values age, publishers revoke, and a tenant
// admin may switch the board off. The CP decides what is shown; the client only re-asks.
const REFRESH_MS = 60_000;
const TICK_MS = 30_000;

export function LimitBoardView() {
  const tr = useT();
  const toast = useToast();
  // The scope every request belongs to. A tenant switch (or a different identity) while the
  // board is open clears everything shown and re-reads: tenant A's roster, values and draft
  // must never be shown, or saved, under tenant B.
  const scope = useTenantStore((s) => `${s.tenant}|${s.identityRev}`);
  const scopeRef = useRef(scope);
  scopeRef.current = scope;
  const [me, setMe] = useState<BoardMe | null>(null);
  const [members, setMembers] = useState<BoardMember[]>([]);
  const [next, setNext] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [failed, setFailed] = useState(false);
  // The draft of the member's own setting; saved explicitly, never overwritten by a refresh.
  const [audience, setAudience] = useState<Audience>("none");
  const [level, setLevel] = useState<Level>("state");
  const [chosen, setChosen] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  const dirtyRef = useRef(false);
  // Every load and every page request belongs to one roster generation. A newer load bumps it,
  // so an older load or a "show more" that answers late (from before a revocation, or before
  // the board was switched off) is dropped rather than painted over the newer answer.
  const genRef = useRef(0);
  // A refresh that failed leaves values whose audience could not be re-checked; they are not
  // shown until a read succeeds again.
  const [unverified, setUnverified] = useState(false);
  const shownRef = useRef(0);
  shownRef.current = members.length;

  const applyMe = useCallback((d: BoardMe, force: boolean) => {
    setMe(d);
    if (force || !dirtyRef.current) {
      setAudience(d.audience);
      setLevel(d.level);
      setChosen(d.members.map((m) => m.userKey));
    }
  }, []);

  // load re-reads the setting and the roster pages shown so far. A response for a scope that
  // is no longer current is dropped.
  const load = useCallback(
    async (initial: boolean) => {
      const at = scopeRef.current;
      const gen = ++genRef.current;
      const limit = Math.min(100, Math.max(PAGE, shownRef.current));
      const [m, b] = await Promise.all([api("api/limit-board/me"), api(`api/limit-board?limit=${limit}`)]);
      if (scopeRef.current !== at || genRef.current !== gen) return;
      if (m?.error || b?.error) {
        if (initial) setFailed(true);
        else setUnverified(true);
        return;
      }
      setFailed(false);
      setUnverified(false);
      applyMe(m as BoardMe, initial);
      setEnabled(!!b.enabled);
      setMembers(b.members || []);
      setNext(b.next || "");
      setNow(Date.now());
    },
    [applyMe],
  );

  useEffect(() => {
    setMe(null);
    setMembers([]);
    setNext("");
    setFailed(false);
    setUnverified(false);
    dirtyRef.current = false;
    void load(true);
  }, [scope, load]);

  useEffect(() => {
    const onVisible = () => {
      if (document.visibilityState === "visible") void load(false);
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [load]);
  useInterval(() => {
    if (document.visibilityState !== "hidden") void load(false);
  }, REFRESH_MS);
  useInterval(() => setNow(Date.now()), TICK_MS);

  const more = async () => {
    const at = scopeRef.current;
    const gen = genRef.current;
    const b = await api(`api/limit-board?limit=${PAGE}&after=${encodeURIComponent(next)}`);
    if (scopeRef.current !== at || genRef.current !== gen || b?.error) return;
    setMembers((cur) => [...cur, ...(b.members || [])]);
    setNext(b.next || "");
  };

  const save = async () => {
    const at = scopeRef.current;
    setBusy(true);
    try {
      const res = await apiJSON("api/limit-board/me", "PUT", {
        audience,
        level,
        members: audience === "members" ? chosen : [],
      });
      if (scopeRef.current !== at) return;
      if (res?.error) {
        toast(errText(res.error));
        return;
      }
      dirtyRef.current = false;
      applyMe(res as BoardMe, true);
      toast(tr("limits.saved"));
    } finally {
      setBusy(false);
    }
  };

  // Every member this setting can name: the roster pages loaded so far, plus anyone already
  // named who is on a page not loaded yet.
  const pickable = useMemo(() => {
    const out = new Map<string, string>();
    for (const m of me?.members ?? []) out.set(m.userKey, m.email);
    for (const m of members) out.set(m.userKey, m.email);
    return [...out.entries()];
  }, [me, members]);

  if (failed) return <p className="muted pad">{tr("limits.load_failed")}</p>;
  if (!me) return <p className="muted pad">{tr("common.loading")}</p>;

  const dirty =
    audience !== me.audience ||
    level !== me.level ||
    (audience === "members" && chosen.join("\n") !== me.members.map((m) => m.userKey).join("\n"));
  dirtyRef.current = dirty;

  return (
    <div className="display-settings limit-board">
      <p className="field-help">{tr("limits.intro")}</p>
      {!enabled && <p className="warn-text">{tr("limits.tenant_off")}</p>}

      <section className="ds-group">
        <h4 className="ds-title">{tr("limits.publish_title")}</h4>
        <p className="muted ds-note">{tr("limits.publish_note")}</p>
        <div className="lb-form">
          <label>
            <span>{tr("limits.audience")}</span>
            <select value={audience} onChange={(e) => setAudience(e.target.value as Audience)}>
              <option value="none">{tr("limits.audience.none")}</option>
              <option value="tenant">{tr("limits.audience.tenant")}</option>
              <option value="members">{tr("limits.audience.members")}</option>
            </select>
          </label>
          <label>
            <span>{tr("limits.level")}</span>
            <select value={level} disabled={audience === "none"} onChange={(e) => setLevel(e.target.value as Level)}>
              <option value="state">{tr("limits.level.state")}</option>
              <option value="band">{tr("limits.level.band")}</option>
              <option value="exact">{tr("limits.level.exact")}</option>
            </select>
          </label>
        </div>
        {audience !== "none" && <p className="muted ds-note">{tr(`limits.level.${level}_note` as MsgKey)}</p>}
        {audience === "members" && (
          <ul className="lb-pick" aria-label={tr("limits.audience.members")}>
            {pickable.length === 0 && <li className="muted">{tr("limits.no_members")}</li>}
            {pickable.map(([key, email]) => (
              <li key={key}>
                <label>
                  <input
                    type="checkbox"
                    checked={chosen.includes(key)}
                    onChange={(e) =>
                      setChosen((c) => (e.target.checked ? [...c, key].sort() : c.filter((k) => k !== key)))
                    }
                  />{" "}
                  {email || key}
                </label>
              </li>
            ))}
          </ul>
        )}
        <div className="flow">
          <button type="button" className="primary" disabled={busy || !dirty} onClick={save}>
            {tr("common.save")}
          </button>
        </div>
        <p className="muted ds-note">
          {me.publishes.length === 0
            ? tr("limits.publishes_none")
            : tr("limits.publishes", { tenants: me.publishes.map((p) => p.name || p.slug).join(", ") })}
        </p>
      </section>

      <section className="ds-group">
        <h4 className="ds-title">{tr("limits.own_title")}</h4>
        {!enabled ? (
          <p className="muted lb-unknown">{tr("limits.tenant_off_short")}</p>
        ) : unverified ? (
          <p className="muted lb-unknown">{tr("limits.refresh_failed")}</p>
        ) : (
          <LimitEntry view={me.own} now={now} />
        )}
      </section>

      <section className="ds-group">
        <h4 className="ds-title">{tr("limits.team_title")}</h4>
        {/* Off, or not re-verified: nothing of anyone is drawn, whatever a late answer held. */}
        {unverified && <p className="muted ds-note">{tr("limits.refresh_failed")}</p>}
        {enabled && !unverified && members.length === 0 && <p className="muted ds-note">{tr("limits.no_members")}</p>}
        {enabled && !unverified && (
          <ul className="lb-roster">
            {members.map((m) => (
              <li key={m.userKey}>
                <div className="lb-who">{m.email || m.userKey}</div>
                <LimitEntry view={m} now={now} />
              </li>
            ))}
          </ul>
        )}
        {enabled && !unverified && next && (
          <button type="button" className="linklike" onClick={more}>
            {tr("limits.more")}
          </button>
        )}
      </section>

      <section className="ds-group">
        <h4 className="ds-title">{tr("limits.coverage_title")}</h4>
        <table className="lb-coverage">
          <thead>
            <tr>
              <th>{tr("limits.cov.kind")}</th>
              <th>{tr("limits.cov.quota")}</th>
              <th>{tr("limits.cov.sessions")}</th>
            </tr>
          </thead>
          <tbody>
            {COVERAGE.map((c) => (
              <tr key={c.kind}>
                <td>{kindName(c.kind)}</td>
                <td>{tr(c.quota)}</td>
                <td>{tr(c.sessions)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        <p className="muted ds-note">{tr("limits.coverage_note")}</p>
      </section>
    </div>
  );
}

// LimitEntry is one member's limits as received. Nothing received is "unknown".
export function LimitEntry({ view, now }: { view: LimitView; now: number }) {
  const tr = useT();
  if (!view.kinds?.length) {
    return <p className="muted lb-unknown">{tr("limits.unknown")}</p>;
  }
  return (
    <ul className="lb-kinds">
      {view.kinds.map((k) => (
        <li key={k.kind}>
          <span className="lb-kind">{kindName(k.kind)}</span>
          {(k.units ?? []).map((u) => (
            <span key={u.unit} className={"lb-unit lb-" + effectiveStatus(u, now)}>
              {unitLabel(tr, k.kind, u)}: {unitText(tr, u, now)}
            </span>
          ))}
          {(k.sessions ?? []).map((s, i) => (
            <span key={i} className="lb-session">
              {sessionText(tr, s)}
              {view.sessionsStale ? ` · ${tr("limits.stale")}` : ""}
            </span>
          ))}
        </li>
      ))}
    </ul>
  );
}
