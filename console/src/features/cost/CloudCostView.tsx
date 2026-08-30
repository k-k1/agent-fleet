// クラウド費用 — AWS の請求を、コスト配分タグでメンバーに割り当てて見せる面
// （docs/log/67 + ADR 0048）。
//
// ⚠️ 隣の「使用量」2 つとは別物である。個人設定の使用量は**トークン**、管理と
// テナント設定の使用量は**稼働時間（秒）**。ここだけが**金額**で、しかも AWS の
// 請求があるデプロイにしか存在しない。だから同じ名前を 4 つ目として使わず
// 「クラウド費用」にした（ADR 0048 決定 5）。
//
// ⚠️ この画面で一番大事なのは数字ではなく**ラベル**である。実測では請求の
// 約 2 割しか人に紐づかない（残りは NAT・DNS・ロードバランサ・DB・空きプール）。
// なので個人向けは「あなたのコスト」ではなく
// 「あなたのワークスペースに直接ひも付く費用（共有分は含みません）」と書く。
// ここを縮めると、会社が払っている額の 1/5 を「あなたのコスト」と呼ぶことになる。
import { useCallback, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { api, errText } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import type { MsgKey } from "../../lib/i18n/index.ts";
import "./cost.css";

// CP が返す能力申告（GET /api/cost/profile）。available が false のデプロイには
// 請求そのものが無いので、呼び出し側は面を描かない。
export type CostProfile = {
  runtime: string;
  available: boolean;
  attributable?: string[];
  shared?: string[];
  verified: boolean;
};

// CP が返すコスト配分タグの有効化状態。⚠️ `pending` のキーがあるということは、
// その軸の費用が「今まさに永久に失われつつある」ということで、読み込み中ではない。
type CostTagState = {
  active?: string[];
  pending?: string[];
  declined?: string[];
  error?: string;
  // 有効化状態は読めなかったが、その軸の値が請求に実際に返ってきている＝**効いている
  // ことが実物で分かった**キー（CP の noteAttribution）。組織のメンバーアカウントでは
  // `ListCostAllocationTags` が構造的に AccessDenied になり、payer がやった有効化を
  // CP からは永久に読めないので、この経路でしか「効いている」と言えない。
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

// useCostProfile — 「このデプロイに費用の面はあるか」を一度だけ聞く。
// null = まだ聞いていない（ちらつき防止に、確定するまで何も描かない）。
export function useCostProfile(): CostProfile | null {
  const [p, setP] = useState<CostProfile | null>(null);
  useEffect(() => {
    api("api/cost/profile")
      .then((d) => setP(d && typeof d.available === "boolean" ? d : { runtime: "", available: false, verified: false }))
      .catch(() => setP({ runtime: "", available: false, verified: false }));
  }, []);
  return p;
}

// 金額はマイクロ単位の整数で運ばれてくる（1 USD = 1_000_000）。
// ⚠️ 通貨は AWS が返したものをそのまま出す。円換算しない（ADR 0048 決定 6）——
// 換算した瞬間それは請求書ではなくなり、レートの出どころを誰も更新しなくなる。
function fmtMoney(micro: number, currency: string): string {
  const v = (micro || 0) / 1_000_000;
  const cur = currency || "USD";
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency: cur, maximumFractionDigits: 2 }).format(v);
  } catch {
    // 未知の通貨コードでも数字は出す（Intl は知らないコードで throw する）。
    return `${v.toFixed(2)} ${cur}`;
  }
}

// 費用センターの識別子は CP が返し、文言は Console が持つ（生の AWS サービス名を
// そのまま並べない＝ドメインの言葉で出す）。
function centreLabel(tr: (k: MsgKey) => string, id: string): string {
  const key = ("cost.centre_" + id) as MsgKey;
  const s = tr(key);
  return s === key ? id : s;
}

// labelStride — 日次の棒に目盛りを何本おきに出すか。10 本前後に収める。
// 30 日ぶんを毎日ラベルすると隣と重なり、連結して読めない文字列になる。
function labelStride(n: number): number {
  return Math.max(1, Math.ceil(n / 10));
}

// 期間の入力とデータ取得。稼働時間の面と同じ形（適用ボタンで明示的に取り直す）。
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

// CostNotes — 数字の「隣」に出す但し書き。
//
// ⚠️ 脚注にしない。約 24 時間遅れであること・当日分が未確定であること・
// タグ有効化より前は取得できないことは、どれも数字の読み方そのものを変える。
// 後注に置くと読まれず、「昨日使ったのに 0 円だ、壊れている」になる。
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
      {/* ⚠️ ここは「まだ読み込み中」ではない。有効化が済むまでの分は永久に取れない
          ので、待っている間ずっと出し続ける必要がある。 */}
      {(meta.tags?.pending?.length || 0) > 0 && (
        <p className="form-err">
          <Icon name="warning" /> {tr("cost.tags_pending", { keys: (meta.tags?.pending || []).join(", ") })}
        </p>
      )}
      {/* ⚠️ 按分が効いていることが実物で分かっているなら、ここは**何も出さない**。
          メンバーアカウントでは有効化状態が読めないので `error` は永久に空にならず、
          それをそのまま描くと「費用が誰にも割り当てられておらず取り戻せません」という
          真逆の警告を、按分済みの金額の真上に出し続けることになる（<prod-deployment> で発生）。
          効いているのは正常な状態であり、正常な状態は黙っている。証拠は数字そのもの。 */}
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

// --- 1 人分の費用（本人向けと、管理のメンバー詳細で共有する）--------------------
//
// ⚠️ 応答の形は `/api/cost/me` と `.../members/{key}/cost` で同一である（CP 側で
// 集計関数を 1 つにしてある）。だから取得も描画も 1 実装にする——2 つに写すと、
// 「片方だけ『あなたのコスト』に縮んでいる」類のズレが必ず出る。
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

  // ⚠️ 期間は「適用」でしか取り直さない（依存に入れない）。日付入力は 1 文字ごとに
  // 変わるので、入れると打っている最中に取得が走る。
  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [endpoint]);

  return { ...range, data, err, loading, load };
}

// 期間の入力。稼働時間の面と同じ形（明示的な「適用」で取り直す）。
// children は日付と「適用」の間に差し込む欄（一覧のテナント選択）。3 つの面で
// 同じ並びにしておかないと、同じ操作が面ごとに違う位置に出る。
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

// CostOneBody — 合計・但し書き・日次・内訳。
//
// ⚠️ totalLabelKey を呼び出し側から渡すのは、ここが「誰の話か」で変わる唯一の場所
// だからである。中身（共有を含まない・実額の一部でしかない）は同じなので、
// 二人称と三人称で別の実装を持つのではなく、ラベルだけを差し替える。
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
      {/* ⚠️ 但し書きは必ず数字と一緒に運ぶ。これが無いと、タグ有効化より前の期間の
          0 が「無料だった」と読まれ、その 0 は永久に自己訂正しない。 */}
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
                {/* 30 日を毎日ラベルすると重なって「08-1708-1808-19」と読めなくなる
                    （実測）。目盛りは間引き、正確な日付は各棒の title に持たせる。 */}
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

// MyCloudCostView — 本人向け。自分に直接ひも付く分だけ。
//
// ⚠️ 他人の費用も、デプロイ合計も返ってこないし出さない（引き算で他人の分を
// 割り出せてしまう）。
export function MyCloudCostView() {
  const tr = useT();
  const { from, setFrom, to, setTo, data, err, loading, load } = useCostOne("api/cost/me");

  return (
    <div className="admin-stage cloud-cost">
      <section className="admin-panel">
        <h4>{tr("cost.my_title")}</h4>
        {/* ★ このデプロイの請求の 8 割は共有インフラで、誰にも割り当てられない。
            「あなたのコスト」と書かないための一文であって、飾りではない。 */}
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

// MemberCostPanel — 管理のメンバー詳細に差す 1 枚（docs/log/67 §67.15）。
//
// なぜ一覧の再掲ではないか: 一覧（CloudCostAdminView）が持っているのは 1 人 1 行の
// **合計だけ**で、日次の形もサービス別の内訳も入っていない。そしてこの詳細画面には
// 「ワークスペースを強制停止」と「上限を設定（ディスク）」が同居している——
// 「週末も含めて毎日乗っている＝スロットを握りっぱなし」と「EBS に寄っている＝home が
// 大きい」は、その 2 つの操作にそのまま対応する読みである。
//
// ⚠️ リソースのタイル（res-tiles）に 4 枚目として足さない。あれは 4 秒ポーリングの
// 「今」で、こちらは約 24 時間遅れの「期間」である。同じカードに時間と $ を並べない
// のは ADR 0048 決定 2。
//
// ⚠️ 能力の確認をここで自分でやるのは、prop を渡し忘れると **請求の無いデプロイ
// （docker / native）に金額 0 の面が出る**からである。0 が並ぶ画面は「無料」と読まれる。
export function MemberCostPanel({ slug, userKey }: { slug: string; userKey: string }) {
  const profile = useCostProfile();
  // 能力が確定してから中身をマウントする。ここで分けているのは、取得まで含めて
  // 「請求の無いデプロイでは何も起きない」ようにするため（管理 API を無駄に叩かない）。
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
      {/* ★ 「このメンバーのコスト」と書かないための一文。実測では請求の 2 割ほどしか
          人に紐づかないので、縮めた時点で会社が払う額の 1/5 を指すことになる。 */}
      <p className="muted cc-lede">{tr("cost.member_intro")}</p>
      <CostRangeBar from={from} setFrom={setFrom} to={to} setTo={setTo} onApply={load} loading={loading} />
      {err && <p className="form-err">{err}</p>}
      <CostOneBody data={data} from={from} totalLabelKey="cost.member_total_label" />
    </section>
  );
}

// CloudCostAdminView — 管理（全テナント）とテナント設定（自テナント）で共用。
//
// ⚠️ 共有インフラのカードは super_admin にしか返ってこない。テナント管理者に
// デプロイ全体の ALB / RDS / Route53 の請求を見せるのは、テナントの外の情報を
// 渡すことになる（ADR 0048 決定 4）。ここは「返って来なければ描かない」だけで、
// 出し分けを画面側で判断しない——判断はサーバが持つ。
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

      {/* 共有インフラ。⚠️ 頭割りしない——配った瞬間に実費ではなく見積になる。
          空きプールのスロット時間もここに落ちるので、「プールが大きすぎる」の
          実費がここで初めて数字になる。 */}
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
