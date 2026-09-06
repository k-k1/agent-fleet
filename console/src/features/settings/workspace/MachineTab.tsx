import { useEffect, useState } from "react";
import { api } from "../../../core/api/client.ts";
import { useWorkspaceStore } from "../../../core/store/workspace.ts";
import { Row } from "../parts/controls.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { fmtGiB } from "../../../lib/bytes.ts";
import { TrendChart } from "../../../ui/TrendChart.tsx";
import { useWsStatsSeries } from "../../../core/store/wsStatsFeed.ts";
import {
  machineArch,
  machineDisk,
  machineInstance,
  machineMemBox,
  machineMemLimit,
  machineNextStart,
  machineVCPU,
  type MachineValue,
  type WsMachine,
} from "../../../lib/machine.ts";

// MachineTab — what this workspace RUNS ON: instance type, architecture, vCPU, memory and
// the home volume. Read-only; the size and the class themselves are a tenant_admin setting.
//
// It exists because on `ecs-ec2` a member gets an EC2 box to themselves and had no way to
// see which one. That is not trivia: arm64 decides whether rtk is installed at all and
// which JDK gets downloaded, and the memory limit is the difference between a build that
// finishes and one the kernel kills.
//
// Every row states whether its value was measured or only configured, and the two are
// never blended — see lib/machine.ts for why.
// The usage chart's x-axis: it grows with the data rather than reserving the buffer's whole
// hour, so a tab opened a minute ago is a full chart instead of a line hugging the right edge.
const MIN_SPAN_MS = 2 * 60 * 1000;
const MAX_SPAN_MS = 60 * 60 * 1000;

export function MachineTab() {
  const tr = useT();
  const wsState = useWorkspaceStore((s) => s.state);
  const [d, setD] = useState<WsMachine | null>(null);
  const [err, setErr] = useState(false);

  // Re-read on every workspace state change: a Stop → Start is exactly when the configured
  // box becomes the running one, and a stale answer would keep showing a "changes at the
  // next start" note for a change that has already been applied.
  useEffect(() => {
    let cancelled = false;
    setErr(false);
    api("api/workspace/machine")
      .then((res: any) => {
        if (cancelled) return;
        if (!res || res.error) throw new Error("");
        setD(res);
      })
      .catch(() => !cancelled && setErr(true));
    return () => {
      cancelled = true;
    };
  }, [wsState]);

  if (err) return <p className="muted pad">{tr("machine.load_failed")}</p>;
  if (!d) return <p className="muted pad">{tr("common.loading")}</p>;
  return <MachineView d={d} />;
}

// Split from the fetch so the rendering can be exercised against a payload directly.
export function MachineView({ d }: { d: WsMachine }) {
  const tr = useT();
  const own = !!d.declared?.dedicated;
  const instance = machineInstance(d);
  const arch = machineArch(d);
  const vcpu = machineVCPU(d);
  const memLimit = machineMemLimit(d);
  const memBox = machineMemBox(d);
  const disk = machineDisk(d);
  const next = machineNextStart(d);
  const quota = d.measured?.cpu_quota;

  const gib = (bytes: number) => fmtGiB(bytes) + " GiB";
  // The source marker qualifies the number rather than judging it: "configured" is the
  // right and only possible answer while the workspace is stopped.
  const src = (v: MachineValue<unknown>) => (
    <span className={"mv-src" + (v.measured ? "" : " is-declared")}>
      {tr(v.measured ? "machine.src_measured" : "machine.src_declared")}
    </span>
  );

  return (
    <div className="display-settings">
      <section className="ds-group">
        <h4 className="ds-title">{tr("machine.title")}</h4>
        {!d.running && <p className="muted ds-note">{tr("machine.stopped_note")}</p>}
        {instance.value && (
          <Row label={tr("machine.instance_type")}>
            <span className="mv-val">
              {instance.value}
              {d.declared?.class_label && <span className="mv-class">{d.declared.class_label}</span>}
              {src(instance)}
            </span>
          </Row>
        )}
        {arch.value && (
          <Row label={tr("machine.arch")}>
            <span className="mv-val">
              <code>{arch.value}</code>
              {src(arch)}
            </span>
          </Row>
        )}
        {vcpu.value > 0 && (
          <Row label={tr("machine.vcpu")}>
            <span className="mv-val">
              {quota ? tr("machine.vcpu_quota", { n: String(vcpu.value), q: String(quota) }) : String(vcpu.value)}
              {src(vcpu)}
            </span>
          </Row>
        )}
        {/* An uncapped workspace has no cgroup limit at all, and there the box IS the
            answer — so the row falls back to the box rather than disappearing. */}
        {(memLimit.value > 0 || memBox.value > 0) && (
          <Row label={tr("machine.memory")}>
            <span className="mv-val">
              {memLimit.value > 0 && memBox.value > 0 && memBox.value !== memLimit.value
                ? tr("machine.mem_of_box", { n: gib(memLimit.value), box: gib(memBox.value) })
                : gib(memLimit.value || memBox.value)}
              {src(memLimit.value > 0 ? memLimit : memBox)}
            </span>
          </Row>
        )}
        {disk.value > 0 && (
          <Row label={tr("machine.home_disk")}>
            <span className="mv-val">
              {gib(disk.value)}
              {src(disk)}
            </span>
          </Row>
        )}
        {next.changed && (
          <p className="muted ds-sub mv-drift">
            {next.box ? tr("machine.next_start_box", { type: next.box }) : tr("machine.next_start_size")}
          </p>
        )}
        <p className="muted ds-sub">{own ? tr("machine.note_own_box") : tr("machine.note_shared_host")}</p>
        <p className="muted ds-sub">{tr("machine.note_who_changes")}</p>
      </section>
      {d.running && <UsageSection memMax={memLimit.value} vcpu={vcpu.value} />}
    </div>
  );
}

// UsageSection — how much of the machine above is being used, as a moving chart.
//
// The ceilings come from the rows above rather than from the series: memory is drawn against
// THIS workspace's limit and CPU against its core count, so "70% of what?" is answered on the
// same screen. That is the reason these charts belong here and not only in the WS bar, where
// a 28px sparkline has no room to say what it is a fraction of.
//
// The samples come from wsStatsFeed, which keeps its own clock — see that module for why the
// push stream alone cannot produce a moving chart.
function UsageSection({ memMax, vcpu }: { memMax: number; vcpu: number }) {
  const tr = useT();
  const samples = useWsStatsSeries();
  const last = samples[samples.length - 1];
  // The window grows with the data up to the buffer's hour, so the chart is full from the
  // first minute instead of a line hugging the right edge of an empty hour.
  const spanMs = Math.min(MAX_SPAN_MS, Math.max(MIN_SPAN_MS, samples.length ? Date.now() - samples[0].t : 0));
  const memCeil = last?.memMax || memMax;
  const cpuCeil = vcpu > 0 ? vcpu * 100 : undefined;
  const oom = samples.some((s) => s.oom);

  if (!samples.length || !last) return null;
  const memPct = memCeil && last.memUsed != null ? (last.memUsed / memCeil) * 100 : null;
  const diskPct = last.diskTotal && last.diskUsed != null ? (last.diskUsed / last.diskTotal) * 100 : null;
  const lvl = (pct: number | null, warn: number, crit: number) =>
    pct == null ? "" : pct >= crit ? " is-crit" : pct >= warn ? " is-warn" : "";

  return (
    <section className="ds-group">
      <h4 className="ds-title">
        {tr("machine.usage_title")}
        <span className="mu-span">{tr("machine.usage_window", { n: String(Math.round(spanMs / 60000)) })}</span>
      </h4>
      {oom && <p className="mu-oom">{tr("machine.usage_oom")}</p>}
      <div className={"mu-chart" + lvl(memPct, 75, 90)}>
        <div className="mu-head">
          <span className="mu-k">{tr("machine.memory")}</span>
          <span className="mu-v">
            {last.memUsed != null
              ? tr("machine.usage_of", {
                  used: fmtGiB(last.memUsed) + " GiB",
                  total: memCeil ? fmtGiB(memCeil) + " GiB" : "?",
                  pct: memPct != null ? String(Math.round(memPct)) : "–",
                })
              : "–"}
          </span>
        </div>
        <TrendChart points={samples.map((s) => ({ t: s.t, v: s.memUsed }))} max={memCeil || undefined} spanMs={spanMs} />
      </div>
      <div className={"mu-chart" + lvl(last.cpu, 60, 90)}>
        <div className="mu-head">
          <span className="mu-k">{tr("machine.vcpu")}</span>
          <span className="mu-v">
            {last.cpu != null
              ? cpuCeil
                ? tr("machine.usage_cpu_of", { pct: String(Math.round(last.cpu)), max: String(cpuCeil) })
                : `${Math.round(last.cpu)}%`
              : "–"}
          </span>
        </div>
        <TrendChart points={samples.map((s) => ({ t: s.t, v: s.cpu }))} max={cpuCeil} spanMs={spanMs} />
      </div>
      {/* Disk is a level, not a rate: it moves in steps over hours, so a trend line of it
          says nothing a bar does not. */}
      {diskPct != null && last.diskUsed != null && last.diskTotal != null && (
        <div className={"mu-chart" + lvl(diskPct, 80, 92)}>
          <div className="mu-head">
            <span className="mu-k">{tr("machine.home_disk")}</span>
            <span className="mu-v">
              {tr("machine.usage_of", {
                used: fmtGiB(last.diskUsed) + " GiB",
                total: fmtGiB(last.diskTotal) + " GiB",
                pct: String(Math.round(diskPct)),
              })}
            </span>
          </div>
          <div className="mu-bar">
            <span style={{ width: `${Math.min(100, diskPct)}%` }} />
          </div>
        </div>
      )}
      <p className="muted ds-sub">{tr("machine.usage_note")}</p>
    </section>
  );
}
