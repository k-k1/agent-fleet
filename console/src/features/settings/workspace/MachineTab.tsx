import { useEffect, useState } from "react";
import { api } from "../../../core/api/client.ts";
import { useWorkspaceStore } from "../../../core/store/workspace.ts";
import { Row } from "../parts/controls.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { fmtGiB } from "../../../lib/bytes.ts";
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
    </div>
  );
}
