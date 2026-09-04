// Cloud cost - the screen that shows the AWS bill attributed to members through cost allocation
// tags (docs/log/67 + ADR 0048).
//
// This is not one of the two neighbouring usage screens. Personal settings' "agent usage" counts
// tokens; admin and tenant settings' "usage" counts running time in seconds. Only this one is
// money, and it exists only on deployments that have an AWS bill - hence a separate name, "cloud
// cost", rather than a fourth "usage" (ADR 0048 decision 5).
//
// What matters most on this screen is the labels, not the numbers. Measured, only about 20% of the
// bill attaches to a person (the rest is NAT, DNS, load balancer, DB and the idle pool), so the
// personal view says "costs attached directly to your workspace (shared costs not included)" and
// never "your cost". Shorten it and a fifth of what the company pays is being called your cost.
import { useCallback, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { api, errText } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import type { MsgKey } from "../../lib/i18n/index.ts";
import "./cost.css";

// Capability declaration returned by the CP (GET /api/cost/profile). A deployment with available
// false has no bill at all, so the caller does not render the screen.
export type CostProfile = {
  runtime: string;
  available: boolean;
  attributable?: string[];
  shared?: string[];
  verified: boolean;
};

// Cost allocation tag activation state returned by the CP. A key under `pending` means that axis's
// cost is being lost permanently right now; it does not mean "still loading".
type CostTagState = {
  active?: string[];
  pending?: string[];
  declined?: string[];
  error?: string;
  // Keys whose activation state could not be read, but whose values did come back in the bill, so
  // they are proven to be in effect (the CP's noteAttribution). On an organisation member account
  // `ListCostAllocationTags` is structurally AccessDenied and the payer's activation can never be
  // read from the CP, so this is the only route to saying it works.
  attributed?: string[];
};

type CostMeta = {
  tags?: CostTagState;
  currency?: string;
  first_day?: string;
  last_day?: string;
  estimated?: boolean;
  lag_hours?: number;
  error?: string;
  profile?: CostProfile;
};

// useCostProfile asks once whether this deployment has a cost screen at all.
// null = not asked yet; nothing is drawn until it settles, to avoid a flicker.
export function useCostProfile(): CostProfile | null {
  const [p, setP] = useState<CostProfile | null>(null);
  useEffect(() => {
    api("api/cost/profile")
      .then((d) => setP(d && typeof d.available === "boolean" ? d : { runtime: "", available: false, verified: false }))
      .catch(() => setP({ runtime: "", available: false, verified: false }));
  }, []);
  return p;
}

// Amounts arrive as integers in micro units (1 USD = 1_000_000).
// The currency AWS returned is shown as-is and never converted to yen (ADR 0048 decision 6): the
// moment it is converted it stops being an invoice, and nobody keeps the rate source up to date.
function fmtMoney(micro: number, currency: string): string {
  const v = (micro || 0) / 1_000_000;
  const cur = currency || "USD";
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency: cur, maximumFractionDigits: 2 }).format(v);
  } catch {
    // Still show the number for an unknown currency code; Intl throws on codes it does not know.
    return `${v.toFixed(2)} ${cur}`;
  }
}

// The CP returns cost centre identifiers and the Console owns the wording, so raw AWS service names
// are not listed as-is but named in the domain's terms.
function centreLabel(tr: (k: MsgKey) => string, id: string): string {
  const key = ("cost.centre_" + id) as MsgKey;
  const s = tr(key);
  return s === key ? id : s;
}

// labelStride - how often to put a tick on the daily bars, aiming for about ten.
// Labelling all 30 days makes neighbours overlap into one unreadable run of characters.
function labelStride(n: number): number {
  return Math.max(1, Math.ceil(n / 10));
}

// Range input and fetching, shaped like the running-time screen: the apply button re-fetches
// explicitly.
function useCostRange() {
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const qs = useCallback(
    (extra?: Record<string, string>) => {
      const p = new URLSearchParams();
      if (from) p.set("from", from);
      if (to) p.set("to", to);
      for (const [k, v] of Object.entries(extra || {})) if (v) p.set(k, v);
      const s = p.toString();
      return s ? "?" + s : "";
    },
    [from, to],
  );
  return { from, setFrom, to, setTo, qs };
}

// CostNotes - the caveats printed next to the numbers.
//
// Not footnotes. The ~24 hour lag, today's figures not being final, and nothing being available
// from before the tags were activated each change how the numbers are to be read. Placed at the end
// they go unread, and the reading becomes "I used it yesterday and it says zero, it is broken".
function CostNotes({ meta, from }: { meta: CostMeta; from: string }) {
  const tr = useT();
  const gap = meta.first_day && from && from < meta.first_day;
  return (
    <div className="cc-notes">
      {meta.error && (
        <p className="form-err">
          <Icon name="warning" /> {tr("cost.poll_error")} <span className="mono">{meta.error}</span>
        </p>
      )}
      <p className="muted">
        <Icon name="clock" /> {tr("cost.lag", { h: String(meta.lag_hours ?? 24) })}
        {meta.estimated ? " " + tr("cost.estimated_note") : ""}
      </p>
      {gap && (
        <p className="muted">
          <Icon name="info" /> {tr("cost.no_backfill", { day: meta.first_day || "" })}
        </p>
      )}
      {meta.profile && !meta.profile.verified && (
        <p className="muted">
          <Icon name="warning" /> {tr("cost.unverified_runtime")}
        </p>
      )}
      {/* This is not "still loading": what falls before activation completes can never be fetched,
          so it has to stay up for as long as the wait lasts. */}
      {(meta.tags?.pending?.length || 0) > 0 && (
        <p className="form-err">
          <Icon name="warning" /> {tr("cost.tags_pending", { keys: (meta.tags?.pending || []).join(", ") })}
        </p>
      )}
      {/* Say nothing here when attribution is known to be working. A member account cannot read the
          activation state, so `error` never clears, and rendering it puts the exact opposite
          warning - costs attributed to nobody and unrecoverable - directly above correctly
          attributed amounts (seen on <prod-deployment>). Working is the normal state, the normal
          state is silent, and the numbers are the evidence. */}
      {meta.tags?.error && !(meta.tags?.attributed?.length || 0) && (
        <p className="form-err">
          <Icon name="warning" /> {tr("cost.tags_error")} <span className="mono">{meta.tags.error}</span>
        </p>
      )}
      {(meta.tags?.declined?.length || 0) > 0 && (
        <p className="muted">
          <Icon name="info" /> {tr("cost.tags_declined", { keys: (meta.tags?.declined || []).join(", ") })}
        </p>
      )}
    </div>
  );
}

// --- One person's cost, shared by the personal view and admin's member detail ----
//
// `/api/cost/me` and `.../members/{key}/cost` return the same shape (the CP keeps one aggregation
// function), so fetching and rendering stay one implementation: copied into two, a drift such as
// one of them shrinking to "your cost" is certain to appear.
function useCostOne(endpoint: string) {
  const tr = useT();
  const range = useCostRange();
  const { qs } = range;
  const [data, setData] = useState<any>(null);
  const [err, setErr] = useState("");
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setErr("");
    try {
      const d = await api(endpoint + qs());
      if (d?.error) {
        setErr(errText(d.error));
        setData(null);
      } else setData(d);
    } catch {
      setErr(tr("cost.load_error"));
      setData(null);
    } finally {
      setLoading(false);
    }
  }, [endpoint, qs, tr]);

  // The range is re-fetched only on apply, and is deliberately not a dependency: a date input
  // changes on every keystroke, so including it would fetch while the user is still typing.
  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [endpoint]);

  return { ...range, data, err, loading, load };
}

// Range input, shaped like the running-time screen: an explicit apply re-fetches.
// children is the field slotted between the dates and apply (the tenant picker on the list). The
// three screens have to keep the same order, or the same control lands somewhere different on each.
function CostRangeBar({
  from,
  setFrom,
  to,
  setTo,
  onApply,
  loading,
  children,
}: {
  from: string;
  setFrom: (v: string) => void;
  to: string;
  setTo: (v: string) => void;
  onApply: () => void;
  loading: boolean;
  children?: ReactNode;
}) {
  const tr = useT();
  return (
    <div className="usage-toolbar">
      <label>
        {tr("admin.from")}
        <input type="date" value={from} onChange={(e) => setFrom(e.target.value)} />
      </label>
      <label>
        {tr("admin.to")}
        <input type="date" value={to} onChange={(e) => setTo(e.target.value)} />
      </label>
      {children}
      <button className="primary" onClick={onApply} disabled={loading}>
        {loading ? "…" : tr("admin.apply")}
      </button>
    </div>
  );
}

// CostOneBody - total, caveats, daily bars and breakdown.
//
// totalLabelKey comes from the caller because the label is the only thing that changes with whose
// cost this is. The content (shared costs excluded, only part of the real amount) is identical, so
// second and third person swap the label instead of having two implementations.
function CostOneBody({ data, from, totalLabelKey }: { data: any; from: string; totalLabelKey: MsgKey }) {
  const tr = useT();
  const meta: CostMeta = data?.meta || {};
  const currency = meta.currency || "USD";
  const days: any[] = data?.days || [];
  const maxDay = days.reduce((m, d) => Math.max(m, d.unblended_micro || 0), 0);
  const services: any[] = data?.services || [];
  return (
    <>
      <div className="cc-headline">
        <div className="cc-total">{fmtMoney(data?.total_micro || 0, currency)}</div>
        <div className="cc-total-lab muted">{tr(totalLabelKey)}</div>
      </div>
      {/* The caveats always travel with the numbers. Without them a 0 covering a period before the
          tags were activated reads as "it was free", and that 0 never corrects itself. */}
      <CostNotes meta={meta} from={from} />

      {data === null ? (
        <p className="muted">{tr("common.loading")}</p>
      ) : days.length === 0 ? (
        <p className="muted">{tr("cost.no_records")}</p>
      ) : (
        <>
          <div className="cc-days">
            {days.map((d, i) => (
              <div key={d.day} className={"cc-day" + (d.estimated ? " est" : "")} title={`${d.day} ${fmtMoney(d.unblended_micro, currency)}`}>
                <span
                  className="cc-day-fill"
                  style={{ height: (maxDay ? Math.round((d.unblended_micro / maxDay) * 100) : 0) + "%" }}
                />
                {/* Labelling all 30 days overlaps them into an unreadable "08-1708-1808-19"
                    (measured); thin the ticks out and keep the exact date in each bar's title. */}
                {i % labelStride(days.length) === 0 && <span className="cc-day-lab muted">{d.day.slice(5)}</span>}
              </div>
            ))}
          </div>
          <h5 className="cc-sub">{tr("cost.breakdown")}</h5>
          <div className="usage-rows">
            {services.map((s) => (
              <div key={s.service} className="usage-row cc-svc">
                <span className="ur-key" title={s.service}>{s.service}</span>
                <span className="ur-hrs mono">{fmtMoney(s.unblended_micro, currency)}</span>
              </div>
            ))}
          </div>
        </>
      )}
    </>
  );
}

// MyCloudCostView - the personal view: only what attaches directly to you.
//
// Neither other people's costs nor the deployment total are returned or shown, because subtraction
// would reveal someone else's share.
export function MyCloudCostView() {
  const tr = useT();
  const { from, setFrom, to, setTo, data, err, loading, load } = useCostOne("api/cost/me");

  return (
    <div className="admin-stage cloud-cost">
      <section className="admin-panel">
        <h4>{tr("cost.my_title")}</h4>
        {/* Eight tenths of this deployment's bill is shared infrastructure that attaches to
            nobody. This sentence is what keeps the screen from saying "your cost", not decoration. */}
        <p className="muted cc-lede">{tr("cost.my_intro")}</p>
        <CostRangeBar from={from} setFrom={setFrom} to={to} setTo={setTo} onApply={load} loading={loading} />
        {err && <p className="form-err">{err}</p>}
      </section>

      <section className="admin-panel">
        <CostOneBody data={data} from={from} totalLabelKey="cost.my_total_label" />
      </section>
    </div>
  );
}

// MemberCostPanel - the single card slotted into admin's member detail (docs/log/67 §67.15).
//
// Why this is not a repeat of the list: the list (CloudCostAdminView) holds only a per-person total,
// with no daily shape and no per-service breakdown. This detail screen also carries "force-stop the
// workspace" and "set a disk quota", and the two readings this card offers - "cost every day
// including weekends = holding a slot" and "weighted towards EBS = a large home" - map straight onto
// those two actions.
//
// Do not add it as a fourth resource tile (res-tiles): those poll every 4 seconds and show now,
// while this is a period roughly 24 hours behind. Not putting time and $ on one card is ADR 0048
// decision 2.
//
// The capability check happens here rather than through a prop, because a forgotten prop puts a
// screen of zero amounts on a deployment with no bill (docker / native), and a screen of zeros
// reads as "free".
export function MemberCostPanel({ slug, userKey }: { slug: string; userKey: string }) {
  const profile = useCostProfile();
  // Mount the body only once the capability has settled, so that on a deployment with no bill
  // nothing happens at all, fetching included - no pointless calls to the admin API.
  if (!profile?.available) return null;
  return <MemberCostBody slug={slug} userKey={userKey} />;
}

function MemberCostBody({ slug, userKey }: { slug: string; userKey: string }) {
  const tr = useT();
  const endpoint = `api/admin/tenants/${encodeURIComponent(slug)}/members/${encodeURIComponent(userKey)}/cost`;
  const { from, setFrom, to, setTo, data, err, loading, load } = useCostOne(endpoint);
  return (
    <section className="admin-panel cloud-cost member-cost">
      <h4>{tr("cost.member_title")}</h4>
      {/* The sentence that keeps this from saying "this member's cost". Measured, only about 20%
          of the bill attaches to a person, so shortening it points at a fifth of what the company
          pays. */}
      <p className="muted cc-lede">{tr("cost.member_intro")}</p>
      <CostRangeBar from={from} setFrom={setFrom} to={to} setTo={setTo} onApply={load} loading={loading} />
      {err && <p className="form-err">{err}</p>}
      <CostOneBody data={data} from={from} totalLabelKey="cost.member_total_label" />
    </section>
  );
}

// CloudCostAdminView - shared by admin (all tenants) and tenant settings (own tenant).
//
// The shared-infrastructure card comes back only for super_admin: showing a tenant admin the whole
// deployment's ALB / RDS / Route53 bill would hand over information from outside the tenant
// (ADR 0048 decision 4). This screen only declines to draw what did not come back; it never decides
// who sees what - the server owns that.
export function CloudCostAdminView({
  tenants,
  isSuper,
}: {
  tenants: { slug: string; name: string }[];
  isSuper: boolean;
}) {
  const tr = useT();
  const { from, setFrom, to, setTo, qs } = useCostRange();
  const [tenant, setTenant] = useState(isSuper ? "" : tenants[0]?.slug || "");
  const [data, setData] = useState<any>(null);
  const [err, setErr] = useState("");
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setErr("");
    try {
      const d = await api("api/admin/cloud-cost" + qs({ tenant }));
      if (d?.error) {
        setErr(errText(d.error));
        setData(null);
      } else setData(d);
    } catch {
      setErr(tr("cost.load_error"));
      setData(null);
    } finally {
      setLoading(false);
    }
  }, [qs, tenant, tr]);

  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tenant]);

  const meta: CostMeta = data?.meta || {};
  const currency = meta.currency || "USD";
  const members: any[] = data?.members || [];
  const maxMember = members.reduce((m, x) => Math.max(m, x.unblended_micro || 0), 0);
  const shared: number | undefined = data?.shared_micro;
  const sharedServices: any[] = data?.shared_services || [];
  const attributed: number = data?.attributed_micro || 0;

  return (
    <div className="admin-stage cloud-cost">
      <section className="admin-panel">
        <h4>{tr("cost.admin_title")}</h4>
        <p className="muted cc-lede">{tr("cost.admin_intro")}</p>
        <CostRangeBar from={from} setFrom={setFrom} to={to} setTo={setTo} onApply={load} loading={loading}>
          {isSuper && (
            <label>
              {tr("admin.tenant")}
              <select value={tenant} onChange={(e) => setTenant(e.target.value)}>
                <option value="">{tr("admin.all_tenants")}</option>
                {tenants.map((t) => (
                  <option key={t.slug} value={t.slug}>
                    {t.name}
                  </option>
                ))}
              </select>
            </label>
          )}
        </CostRangeBar>
        {err && <p className="form-err">{err}</p>}
      </section>

      <section className="admin-panel">
        <div className="cc-headline">
          <div className="cc-total">{fmtMoney(attributed, currency)}</div>
          <div className="cc-total-lab muted">{tr("cost.attributed_label")}</div>
        </div>
        <CostNotes meta={meta} from={from} />
        {data === null ? (
          <p className="muted">{tr("common.loading")}</p>
        ) : members.length === 0 ? (
          <p className="muted">{tr("cost.no_records")}</p>
        ) : (
          <div className="usage-rows">
            {members.map((m) => (
              <div key={m.membership_id} className="usage-row">
                <span className="ur-key mono" title={m.email || ""}>
                  {m.user_key || tr("admin.unknown")}
                </span>
                {isSuper && !tenant && <span className="ur-tenant muted">{m.tenant}</span>}
                <span className="ur-bar">
                  <span
                    className="ur-fill"
                    style={{ width: (maxMember ? Math.round((m.unblended_micro / maxMember) * 100) : 0) + "%" }}
                  />
                </span>
                <span className="ur-hrs mono">{fmtMoney(m.unblended_micro, currency)}</span>
              </div>
            ))}
          </div>
        )}
      </section>

      {/* Shared infrastructure. Never divided per head: the moment it is spread out it stops being
          an actual cost and becomes an estimate. Slot time in the idle pool lands here too, which is
          where "the pool is too large" first becomes a number. */}
      {shared !== undefined && (
        <section className="admin-panel">
          <h4>{tr("cost.shared_title")}</h4>
          <p className="muted cc-lede">{tr("cost.shared_intro")}</p>
          <div className="cc-headline">
            <div className="cc-total">{fmtMoney(shared, currency)}</div>
            <div className="cc-total-lab muted">{tr("cost.shared_label")}</div>
          </div>
          <div className="usage-rows">
            {sharedServices.map((s) => (
              <div key={s.service} className="usage-row cc-svc">
                <span className="ur-key" title={s.service}>{s.service}</span>
                <span className="ur-hrs mono">{fmtMoney(s.unblended_micro, currency)}</span>
              </div>
            ))}
          </div>
          {meta.profile?.shared && meta.profile.shared.length > 0 && (
            <p className="admin-hint">
              {tr("cost.shared_centres")}{" "}
              {meta.profile.shared.map((c) => centreLabel(tr, c)).join(" / ")}
            </p>
          )}
          <p className="admin-hint">{tr("cost.account_scope")}</p>
        </section>
      )}
    </div>
  );
}
