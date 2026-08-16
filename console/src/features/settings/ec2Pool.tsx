// EC2 スロットプール（AF_RUNTIME=ecs-ec2）の運用面。docs/64 §64.18.6 / ADR 0045 決定 13。
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
import { useCallback, useEffect, useRef, useState } from "react";
import { api, errText } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";

export type PoolStatus = {
  runtime: string;
  pool?: string;
  max_slots?: number;
  slot_sleep_sec?: number;
  hibernate_after_sec?: number;
  slots?: Slot[];
  homes?: Home[];
  golden_id?: string;
  golden_image?: string;
  golden_stale?: boolean;
  running_image?: string;
};
type Slot = {
  instance_id: string;
  instance_type: string;
  az: string;
  state: string;
  registered: boolean;
  workspace: string;
  idle_minutes: number;
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
  const running = slots.filter((s) => s.state === "running").length;
  const asleep = slots.filter((s) => s.state === "stopped").length;
  const free = slots.filter((s) => !s.workspace).length;
  const atCap = st.max_slots != null && slots.length >= st.max_slots;

  return (
    <div className="admin-stage pool-view">
      <section className="admin-panel">
        <h4>{tr("pool.slots_title")}</h4>
        <div className="res-tiles">
          <PoolTile label={tr("pool.provisioned")} value={`${slots.length}`} sub={tr("pool.of_max", { n: String(st.max_slots ?? 0) })} warn={atCap} />
          <PoolTile label={tr("pool.running")} value={`${running}`} sub={tr("pool.running_sub")} />
          <PoolTile label={tr("pool.asleep")} value={`${asleep}`} sub={tr("pool.asleep_sub")} />
          <PoolTile label={tr("pool.free")} value={`${free}`} sub={tr("pool.free_sub")} />
        </div>
        {/* 上限に達している＝次の人はスロットを取り上げて作る。運用者が最初に知りたいのは
            「増えないこと」ではなく「立ち退きが起きること」なので、そう書く。 */}
        {atCap && <p className="admin-hint warn-text">{tr("pool.at_cap")}</p>}
        {/* 「退避しない」を「…の後に退避します」の穴に入れると "after never" になって
            読めなくなる。0 は別の文にする（既定はオフなので、これが普通に出る方）。 */}
        <p className="admin-hint">
          {(st.hibernate_after_sec ?? 0) > 0
            ? tr("pool.timers", {
                sleep: fmtDuration(st.slot_sleep_sec ?? 0, tr),
                hibernate: fmtDuration(st.hibernate_after_sec ?? 0, tr),
              })
            : tr("pool.timers_no_hibernate", { sleep: fmtDuration(st.slot_sleep_sec ?? 0, tr) })}
        </p>
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
                    <span className={"state-dot " + (s.state === "running" ? "on" : "off")} />
                    {s.state === "stopped" ? tr("pool.state_asleep") : s.state}
                    {s.state === "running" && !s.registered && (
                      <span className="muted"> {tr("pool.not_registered")}</span>
                    )}
                  </td>
                  <td className="mono">{s.workspace || <span className="muted">{tr("pool.free_slot")}</span>}</td>
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
                  <td className="mono">{h.workspace}</td>
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
        {!st.golden_id ? (
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

function PoolTile({ label, value, sub, warn }: { label: string; value: string; sub: string; warn?: boolean }) {
  return (
    <div className={"res-tile" + (warn ? " warn" : "")}>
      <div className="rt-label">{label}</div>
      <div className="rt-value">{value}</div>
      <div className="rt-sub muted">{sub}</div>
    </div>
  );
}
