// EC2 スロットプール（AF_RUNTIME=ecs-ec2）の運用面。docs/log/64 §64.18.6 / ADR 0045 決定 13。
//
// この面が答えるのは、このランタイムだけが持ち込む 3 つの問い:
//   1. いま何台ぶん払っているのか（スロット数と、そのうち起動している台数）
//   2. どれが眠っているのか（＝止まっていて root EBS だけの課金か）
//   3. 誰の home がどこにあるのか（スロット上か・退避中か・snapshot になったか）
//
// ★ 表示は毎回 AWS から導出したものであって、CP が持っている状態ではない（ADR 0012）。
//   だから「CP を再起動したら見え方が変わる」ということが無い。
// ★ 他のランタイムではプールという概念が無い。空の表を出すと Fargate のデプロイで
//   「スロットが全部消えた」に読めるので、その場合はタブごと出さない（AdminTab 側）。
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { api, errText } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useT, type MsgKey } from "../../lib/i18n/index.ts";

export type PoolBudget = {
  max_slots: number;
  reserved_slots: number;
  capacity: number;
  allocated: number;
  unbounded_tenants?: string[];
  over: boolean;
};

/**
 * PoolBudgetHint は上の警告を出す 1 か所。プール画面（常態として）と、テナントの上限を
 * 保存した直後（いま打った数字について）の両方が使うので、文言は 1 つしかない。
 */
export function PoolBudgetHint({ budget }: { budget: PoolBudget }) {
  const tr = useT();
  const unbounded = budget.unbounded_tenants || [];
  return (
    <>
      {budget.over && (
        <p className="admin-hint warn-text">
          {tr("pool.budget_over", {
            allocated: String(budget.allocated),
            capacity: String(budget.capacity),
            max: String(budget.max_slots),
            reserved: String(budget.reserved_slots),
          })}
        </p>
      )}
      {unbounded.length > 0 && (
        <p className="admin-hint warn-text">
          {tr("pool.budget_unbounded", { tenants: unbounded.join(", ") })}
        </p>
      )}
      {/* 分母が違うことを、数字と同じ場所で言う。ここを書かないと運用者は
          「合計が枠内なら立ち退きは起きない」と読む——起きる。 */}
      <p className="admin-hint">{tr("pool.budget_denominator")}</p>
    </>
  );
}

export type PoolStatus = {
  runtime: string;
  pool?: string;
  max_slots?: number;
  slot_sleep_sec?: number;
  slot_terminate_sec?: number;
  hibernate_after_sec?: number;
  /**
   * テナント上限の合計とプール上限の突き合わせ。**問題があるときだけ**返る
   * （収まっている合計はニュースではなく、材料の 2 つは既にこの画面に出ている）。
   *
   * ⚠️ 2 つは別の分母を数えている。`allocated` は *同時に動いている* Workspace の数、
   * `max_slots` は *存在してよい箱* の数で、停止中の Workspace はどちらのテナント枠にも
   * 数えられないまま箱を掴んでいる。1 つの数字に混ぜないこと。
   */
  budget?: PoolBudget;
  slots?: Slot[];
  homes?: Home[];
  golden_id?: string;
  golden_image?: string;
  golden_stale?: boolean;
  baking?: boolean;
  bake_rejected?: string;
  bake_reason?: string;
  running_image?: string;
  /** 宣言されたアーキ毎の golden（docs/log/70 §70.6）。上の 6 つはこの配列の先頭
   *  （既定クラスのアーキ）と同じ値で、クラスが 1 つのデプロイでは配列も 1 要素。 */
  goldens?: Golden[];
  slot_classes?: { id: string; label: string; arch: string }[];
  /** 自動焼きが有効か（AF_ECS_EC2_GOLDEN_AUTOBAKE）。AWS から導出できない唯一の値で、
   *  これが無いと「まだ焼かれていない」と「この先も焼かれない」が同じ顔になる。 */
  auto_bake?: boolean;
};
type Golden = {
  arch: string;
  snapshot_id?: string;
  image?: string;
  stale?: boolean;
  baking?: boolean;
  rejected?: string;
  reason?: string;
  /** 焼き込みがどこまで進んだか（docs/log/64 §64.30）。BAKE_STEPS の 6 段と、
   *  「焼かれていない理由」4 種（idle / blocked / rejected / gave_up / off）。 */
  phase?: string;
  phase_since?: string;
  candidate?: string;
  progress?: number;
  attempts?: number;
  slots_in_use?: number;
  seed?: BakeWorkspace;
  probe?: BakeWorkspace;
};
/** 焼き込みが立てる予約 workspace。スロットを 1 つ握るので、画面に出さないと
 *  スロット表に「誰にも紐づかない占有」が並ぶことになる。 */
type BakeWorkspace = { workspace: string; volume_id?: string; instance_id?: string };
type Slot = {
  instance_id: string;
  instance_type: string;
  az: string;
  state: string;
  registered: boolean;
  workspace: string;
  idle_minutes: number;
  // 隔離されたスロット（決定 20）。プールからは外れているが、まだ課金されている。
  quarantined?: boolean;
  quarantine_reason?: string;
};
type Home = {
  volume_id: string;
  workspace: string;
  size_gib: number;
  az: string;
  attached_to: string;
  idle_minutes: number;
  hibernating: boolean;
  backups?: number;
  backup_age_minutes?: number;
  snapshot_id: string;
  snapshot_state: string;
};

// 分を「45分 / 3.2時間 / 12日」に。休眠は分単位から日単位までまたぐので、
// 単位を固定すると 43200 のような読めない数字が並ぶ。
type TR = ReturnType<typeof useT>;

function fmtIdle(min: number, tr: TR): string {
  if (min < 60) return tr("pool.idle_min", { n: String(min) });
  if (min < 60 * 48) return tr("pool.idle_hour", { n: (min / 60).toFixed(1) });
  return tr("pool.idle_day", { n: String(Math.round(min / 1440)) });
}

function fmtDuration(sec: number, tr: TR): string {
  if (sec <= 0) return tr("pool.off");
  return fmtIdle(Math.round(sec / 60), tr);
}

export function PoolView() {
  const tr = useT();
  const [st, setSt] = useState<PoolStatus | null>(null);
  const [err, setErr] = useState("");
  const timer = useRef<ReturnType<typeof setInterval> | undefined>(undefined);

  const poll = useCallback(async () => {
    try {
      const d = await api("api/admin/ec2-pool");
      if (d?.error) {
        setErr(errText(d.error));
        return;
      }
      setErr("");
      setSt(d);
    } catch {
      /* transient; keep the last picture rather than blanking the screen */
    }
  }, []);

  useEffect(() => {
    poll();
    timer.current = setInterval(() => {
      if (!document.hidden) poll();
    }, 10000);
    return () => clearInterval(timer.current);
  }, [poll]);

  if (err) return <p className="muted pad">{err}</p>;
  if (st === null) return <p className="muted pad">{tr("common.loading")}</p>;
  if (st.runtime !== "ecs-ec2") return <p className="muted pad">{tr("pool.not_ec2")}</p>;

  const slots = st.slots || [];
  const homes = st.homes || [];
  // 隔離された箱はプールの数に入れない——上限にも空きにも数えると、運用者は
  // 「空きが 1 あるのに誰も入れない」を見ることになる。表には残す（まだ課金されている）。
  const pool = slots.filter((s) => !s.quarantined);
  const quarantined = slots.filter((s) => s.quarantined);
  const running = pool.filter((s) => s.state === "running").length;
  const asleep = pool.filter((s) => s.state === "stopped").length;
  const free = pool.filter((s) => !s.workspace).length;
  const atCap = st.max_slots != null && pool.length >= st.max_slots;
  // 焼き込みが立てた予約 workspace。スロットと home の表では「人」に見えるので、
  // それが誰でもなく golden のためのものだと分かるようにする（この 2 つが埋まって
  // いること自体が、上限に近いプールでは説明の要る事実でもある）。
  const bakeWS = new Set(
    (st.goldens || []).flatMap((g) => [g.seed?.workspace, g.probe?.workspace].filter(Boolean) as string[]),
  );

  return (
    <div className="admin-stage pool-view">
      <section className="admin-panel">
        <h4>{tr("pool.slots_title")}</h4>
        <div className="res-tiles">
          <PoolTile label={tr("pool.provisioned")} value={`${pool.length}`} sub={tr("pool.of_max", { n: String(st.max_slots ?? 0) })} warn={atCap} />
          <PoolTile label={tr("pool.running")} value={`${running}`} sub={tr("pool.running_sub")} />
          <PoolTile label={tr("pool.asleep")} value={`${asleep}`} sub={tr("pool.asleep_sub")} />
          <PoolTile label={tr("pool.free")} value={`${free}`} sub={tr("pool.free_sub")} />
        </div>
        {/* 上限に達している＝次の人はスロットを取り上げて作る。運用者が最初に知りたいのは
            「増えないこと」ではなく「立ち退きが起きること」なので、そう書く。 */}
        {atCap && <p className="admin-hint warn-text">{tr("pool.at_cap")}</p>}
        {/* 隔離は「勝手に減った」ではなく「この箱はもう使えないので外した。まだ課金される」
            と読めないと意味がない。台数と、終了は運用者の手であることを書く。 */}
        {quarantined.length > 0 && (
          <p className="admin-hint warn-text">{tr("pool.quarantined_hint", { n: String(quarantined.length) })}</p>
        )}
        {/* 「退避しない」を「…の後に退避します」の穴に入れると "after never" になって
            読めなくなる。0 は別の文にする（既定はオフなので、これが普通に出る方）。 */}
        <p className="admin-hint">
          {(st.hibernate_after_sec ?? 0) > 0
            ? tr("pool.timers", {
                sleep: fmtDuration(st.slot_sleep_sec ?? 0, tr),
                hibernate: fmtDuration(st.hibernate_after_sec ?? 0, tr),
              })
            : tr("pool.timers_no_hibernate", { sleep: fmtDuration(st.slot_sleep_sec ?? 0, tr) })}{" "}
          {/* 「終了しない」は事象ではなく常態なので、オフのときこそ書く。停止で止まるのは
              compute だけで、root ボリュームは箱が消えるまで課金され続ける——そしてそれは
              画面のどこにも出ない（AutoBake を出しているのと同じ理由）。 */}
          {(st.slot_terminate_sec ?? 0) > 0
            ? tr("pool.timers_terminate", { terminate: fmtDuration(st.slot_terminate_sec ?? 0, tr) })
            : tr("pool.timers_no_terminate", { max: String(st.max_slots ?? 0) })}
        </p>
        {/* テナントに配った同時利用の合計が、この箱の数に収まっているか。サーバが
            「問題があるとき」にだけ載せてくる。 */}
        {st.budget && <PoolBudgetHint budget={st.budget} />}
        {slots.length === 0 ? (
          <p className="muted">{tr("pool.no_slots")}</p>
        ) : (
          <table className="admin-table pool-table">
            <thead>
              <tr>
                <th>{tr("pool.col_instance")}</th>
                <th>{tr("pool.col_type")}</th>
                <th>{tr("pool.col_state")}</th>
                <th>{tr("pool.col_occupant")}</th>
                <th>{tr("pool.col_dormant")}</th>
                <th>{tr("pool.col_backup")}</th>
              </tr>
            </thead>
            <tbody>
              {slots.map((s) => (
                <tr key={s.instance_id}>
                  <td className="mono">{s.instance_id}</td>
                  <td className="mono">{s.instance_type}<span className="muted"> {s.az}</span></td>
                  <td>
                    <span className={"state-dot " + (s.quarantined ? "off" : s.state === "running" ? "on" : "off")} />
                    {s.quarantined ? (
                      <span className="warn-text" title={s.quarantine_reason || ""}>{tr("pool.state_quarantined")}</span>
                    ) : s.state === "stopped" ? (
                      tr("pool.state_asleep")
                    ) : (
                      s.state
                    )}
                    {!s.quarantined && s.state === "running" && !s.registered && (
                      <span className="muted"> {tr("pool.not_registered")}</span>
                    )}
                  </td>
                  <td className="mono">
                    {s.workspace || <span className="muted">{tr("pool.free_slot")}</span>}
                    {bakeWS.has(s.workspace) && <span className="pool-badge bake">{tr("pool.bake_owner")}</span>}
                  </td>
                  <td>{s.workspace ? fmtIdle(s.idle_minutes, tr) : "–"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      <section className="admin-panel">
        <h4>{tr("pool.homes_title")}</h4>
        {homes.length === 0 ? (
          <p className="muted">{tr("pool.no_homes")}</p>
        ) : (
          <table className="admin-table pool-table">
            <thead>
              <tr>
                <th>{tr("pool.col_workspace")}</th>
                <th>{tr("pool.col_volume")}</th>
                <th>{tr("pool.col_where")}</th>
                <th>{tr("pool.col_dormant")}</th>
              </tr>
            </thead>
            <tbody>
              {homes.map((h) => (
                <tr key={h.volume_id || h.workspace}>
                  <td className="mono">
                    {h.workspace}
                    {bakeWS.has(h.workspace) && <span className="pool-badge bake">{tr("pool.bake_owner")}</span>}
                  </td>
                  <td className="mono">
                    {h.volume_id ? `${h.volume_id} (${h.size_gib} GiB)` : <span className="muted">{tr("pool.no_volume")}</span>}
                  </td>
                  <td>
                    {h.snapshot_id && !h.volume_id ? (
                      <span className="pool-badge hib"><Icon name="archive" /> {tr("pool.hibernated")}</span>
                    ) : h.hibernating ? (
                      <span className="pool-badge hib"><Icon name="archive" /> {tr("pool.hibernating", { state: h.snapshot_state || "…" })}</span>
                    ) : h.attached_to ? (
                      <span className="mono">{h.attached_to}</span>
                    ) : (
                      <span className="muted">{tr("pool.detached")}</span>
                    )}
                  </td>
                  <td>{h.volume_id && h.idle_minutes > 0 ? fmtIdle(h.idle_minutes, tr) : "–"}</td>
                  {/* 予備が「無い」と「さっき取った」は正反対の答えなので、同じ空欄に
                      まとめない。退避済みの home は snapshot そのものなので対象外。 */}
                  <td>
                    {!h.volume_id ? (
                      "–"
                    ) : (h.backup_age_minutes ?? -1) >= 0 ? (
                      <span title={tr("pool.backup_count", { n: h.backups ?? 0 })}>
                        {fmtIdle(h.backup_age_minutes ?? 0, tr)}
                      </span>
                    ) : (
                      <span className="warn-text">{tr("pool.backup_none")}</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      <section className="admin-panel">
        <h4>{tr("pool.golden_title")}</h4>
        {/* ★ golden は「バイナリの詰まった home」なので、アーキ毎に別物である
            （docs/log/70 §70.6）。複数アーキを宣言したデプロイでは 1 本だけ見せると
            「golden はある」と読めてしまい、まだ焼けていない側のクラスの新規ユーザー
            だけが毎回空の home から始まる——という、当人以外には見えない失敗になる。 */}
        {st.goldens?.length ? (
          <>
            {st.goldens.map((g) => (
              <GoldenBake key={g.arch} g={g} st={st} showArch={st.goldens!.length > 1} />
            ))}
            {/* 焼いている最中の「で、いま何が配られるのか」は 1 回だけ書く。アーキ毎に
                繰り返すと、読む値（どの段か）が定型文に埋もれる。 */}
            {st.goldens.some((g) => BAKE_STEPS.indexOf(g.phase || "") >= 0 && g.phase !== "published") && (
              <p className="admin-hint">{tr("pool.bake_meanwhile")}</p>
            )}
          </>
        ) : st.bake_rejected ? (
          // 拒否は「イベント」ではなく「状態」である。焼けた golden が起動できなかった
          // ときの症状は再起動ループだけで、CP ログの 1 行は流れてしまう（§64.28.3）。
          // 直るまでこの面に出し続ける。
          <p className="warn-text">
            {tr("pool.golden_rejected", { snapshot: st.bake_rejected, reason: st.bake_reason || "?" })}
          </p>
        ) : st.baking ? (
          <p className="muted">{tr("pool.golden_baking", { image: st.running_image || "" })}</p>
        ) : !st.golden_id ? (
          <p className="muted">{tr("pool.golden_none", { image: st.running_image || "" })}</p>
        ) : st.golden_stale ? (
          // 忘れると見えないまま新規ユーザーだけが古い CLI で始まる種類の失敗なので、
          // 「一致しない」ではなく「いま何が起きているか」を書く。
          <p className="warn-text">
            {tr("pool.golden_stale", { snapshot: st.golden_id, baked: st.golden_image || "?", running: st.running_image || "?" })}
          </p>
        ) : (
          <p>
            <span className="mono">{st.golden_id}</span>{" "}
            <span className="muted">{tr("pool.golden_ok", { image: st.golden_image || "" })}</span>
          </p>
        )}
      </section>

    </div>
  );
}

// 焼き込みの 6 段（CP の ec2BakePhase* と同じ順序・同じ名前）。ここが「進んでいる」
// ことを見せる唯一の場所である——焼きは 11 分前後かかり、そのうち前半（種の起動・
// boot-install・スロット解放）は snapshot がまだ存在しない。以前の画面はその間ずっと
// 「golden はありません」と言っていて、初回起動が遅い理由を調べに来た運用者は
// **起きていることの逆**を読まされていた。
const BAKE_STEPS = ["seed", "boot", "capture", "snapshot", "probe", "published"];
const STEP_LABEL: Record<string, MsgKey> = {
  seed: "pool.bake_step_seed",
  boot: "pool.bake_step_boot",
  capture: "pool.bake_step_capture",
  snapshot: "pool.bake_step_snapshot",
  probe: "pool.bake_step_probe",
  published: "pool.bake_step_published",
};

// GoldenBake is one architecture's golden: what is published, and — when nothing is —
// either how far the bake has got or why there is no bake. The four "no bake" answers
// are deliberately different sentences: only one of them (idle) fixes itself, and the
// other three previously existed solely as a CP log line that scrolls away.
function GoldenBake({ g, st, showArch }: { g: Golden; st: PoolStatus; showArch: boolean }) {
  const tr = useT();
  const running = st.running_image || "";
  const phase = g.phase || "";
  const at = BAKE_STEPS.indexOf(phase);

  return (
    <div className="golden-arch">
      {showArch && <span className="golden-arch-name mono">{g.arch}</span>}
      {/* 古い golden が残っていることは、焼き直しの進み具合とは別の事実。焼いている
          最中でも「いま配られているのは古い方」を先に言う（忘れると、新規ユーザーだけ
          が古い CLI で始まるという、当人以外に見えない失敗になる）。 */}
      {g.stale && (
        <p className="warn-text">
          {tr("pool.golden_stale", { snapshot: g.snapshot_id || "?", baked: g.image || "?", running: running || "?" })}
        </p>
      )}
      {phase === "published" ? (
        <p>
          <span className="mono">{g.snapshot_id}</span>{" "}
          <span className="muted">{tr("pool.golden_ok", { image: g.image || "" })}</span>
        </p>
      ) : at >= 0 ? (
        <>
          <p className="golden-head">
            <span className="state-dot on" />
            {tr("pool.bake_running", { image: running })}
            {g.phase_since && <span className="muted"> {fmtElapsed(g.phase_since, tr)}</span>}
          </p>
          <BakeSteps at={at} />
          <BakeDetail g={g} />
        </>
      ) : phase === "off" ? (
        <p className="muted">{tr("pool.bake_off")}</p>
      ) : phase === "blocked" ? (
        // 実デプロイ（本番配備）で焼きを止めたのはこれ。歯止めは正しく効いていた
        // のに、効いたことがログの 1 行にしか出ていなかった。
        <p className="warn-text">
          {tr("pool.bake_blocked", { used: String(g.slots_in_use ?? 0), max: String(st.max_slots ?? 0) })}
        </p>
      ) : phase === "gave_up" ? (
        <p className="warn-text">
          {tr("pool.bake_gave_up", { snapshot: g.rejected || "?", reason: g.reason || "?" })}
        </p>
      ) : g.rejected ? (
        // 拒否は「イベント」ではなく「状態」である。焼けた golden が起動できなかった
        // ときの症状は再起動ループだけで、CP ログの 1 行は流れてしまう（§64.28.3）。
        <p className="warn-text">
          {tr("pool.golden_rejected", { snapshot: g.rejected, reason: g.reason || "?" })}{" "}
          {tr("pool.bake_retry_left")}
        </p>
      ) : (
        <p className="muted">{tr("pool.golden_none", { image: running })}</p>
      )}
    </div>
  );
}

// BakeSteps is the progress line. Steps already passed are filled, the current one is
// marked, the rest are muted — the question it answers is "is this moving", which a
// single status word cannot answer at all when one step takes 3 minutes and the next
// takes 90 seconds.
function BakeSteps({ at }: { at: number }) {
  const tr = useT();
  return (
    <ol className="bake-steps">
      {BAKE_STEPS.map((s, i) => (
        <li key={s} className={"bake-step" + (i < at ? " done" : i === at ? " now" : "")}>
          <span className="bake-dot" />
          <span className="bake-label">{tr(STEP_LABEL[s])}</span>
        </li>
      ))}
    </ol>
  );
}

// BakeDetail is the one line that says what is actually being waited on: the copy
// percentage, or which workspace is holding a slot. The reserved workspaces are on the
// screen because they occupy slots — without them the slot table shows a box taken by
// af-ws-af-golden-… that nothing else on the page accounts for.
function BakeDetail({ g }: { g: Golden }) {
  const tr = useT();
  const at = BAKE_STEPS.indexOf(g.phase || "");
  const parts: ReactNode[] = [];
  if (g.phase === "snapshot" && g.candidate) {
    parts.push(
      <span key="cand" className="mono">
        {g.candidate}
        {g.progress ? ` ${g.progress}%` : ""}
      </span>,
    );
  }
  if (g.phase === "probe") {
    parts.push(<span key="verify">{tr("pool.bake_detail_probe", { snapshot: g.candidate || "?" })}</span>);
  }
  // 種は「スロットを握っている間」だけ出す。snapshot 以降その箱はもう返っていて、
  // 残った home はボリューム表の方に（焼き込み用の印つきで）並んでいる。
  if (g.seed && at >= 0 && at <= BAKE_STEPS.indexOf("capture")) {
    parts.push(<BakeWS key="seed" label={tr("pool.bake_detail_seed")} ws={g.seed} />);
  }
  if (g.probe) parts.push(<BakeWS key="probe" label={tr("pool.bake_detail_probe_ws")} ws={g.probe} />);
  if (!parts.length) return null;
  return <p className="bake-detail muted">{parts}</p>;
}

function BakeWS({ label, ws }: { label: string; ws: BakeWorkspace }) {
  return (
    <span>
      {label} <span className="mono">{ws.workspace}</span>
      {ws.instance_id && <span className="mono"> ({ws.instance_id})</span>}
    </span>
  );
}

// 焼きの経過は「12 秒 → 4 分 12 秒 → 1 時間 3 分」と桁をまたぐ。分に固定すると、
// 始まった直後がすべて「0 分」になって、動いているのか固まっているのか読めない。
function fmtElapsed(since: string, tr: TR): string {
  const started = Date.parse(since);
  if (!Number.isFinite(started)) return "";
  const sec = Math.max(0, Math.round((Date.now() - started) / 1000));
  if (sec < 60) return tr("pool.elapsed_sec", { s: String(sec) });
  if (sec < 3600) return tr("pool.elapsed_min", { m: String(Math.floor(sec / 60)), s: String(sec % 60) });
  return tr("pool.elapsed_hour", { h: String(Math.floor(sec / 3600)), m: String(Math.floor((sec % 3600) / 60)) });
}

function PoolTile({ label, value, sub, warn }: { label: string; value: string; sub: string; warn?: boolean }) {
  return (
    <div className={"res-tile" + (warn ? " warn" : "")}>
      <div className="rt-label">{label}</div>
      <div className="rt-value">{value}</div>
      <div className="rt-sub muted">{sub}</div>
    </div>
  );
}
