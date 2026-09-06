// What the workspace runs on (GET /api/workspace/machine), and the rules for reading it.
//
// The CP answers with two halves that are deliberately not merged
// (control-plane/workspace_machine.go): `measured` is what the container read about itself,
// `declared` is the box this deployment's configuration resolves to. Which one a given row
// ends up showing — and the fact that they can disagree — is decided here rather than in
// each screen: the settings section and the WS-bar popover would otherwise answer the same
// question differently, and the one that drifted would be impossible to spot.

export const MIB = 1048576;
export const GIB = 1073741824;

/** Read from inside the container. A field is absent when it could not be measured — never
 *  zero, so "idle" and "unknown" stay distinguishable (ADR 0058). */
export interface MachineMeasured {
  arch?: string;
  vcpu?: number;
  /** cgroup CPU bandwidth cap in cores. Absent = uncapped, which is the normal case. */
  cpu_quota?: number;
  /** This container's memory limit. */
  mem_max?: number;
  /** The box's RAM. The CP drops it unless the box is this member's own. */
  mem_total?: number;
  disk_total?: number;
  /** Only present where the box could be confirmed to be EC2 (SMBIOS). */
  instance_type?: string;
}

/** The box this deployment's configuration says the workspace gets. Present only on a
 *  runtime that has a box to name (the EC2 slot pool). */
export interface MachineDeclared {
  instance_type?: string;
  arch?: string;
  vcpu?: number;
  slot_mem_mib?: number;
  mem_cap_mib?: number;
  home_gib?: number;
  class_id?: string;
  class_label?: string;
  /** The box belongs to one member, which is what allows its RAM and cores to be shown
   *  as "your machine". */
  dedicated?: boolean;
}

export interface WsMachine {
  runtime: string;
  running: boolean;
  measured?: MachineMeasured;
  declared?: MachineDeclared;
}

/** One row's answer: the value, and whether it was measured or only configured. A row with
 *  neither is not drawn — nothing here invents a number. */
export interface MachineValue<T> {
  value: T;
  measured: boolean;
}

const pick = <T>(m: T | undefined, d: T | undefined, empty: T): MachineValue<T> =>
  m !== undefined && m !== null && m !== empty ? { value: m, measured: true } : { value: d ?? empty, measured: false };

export const machineInstance = (x: WsMachine) => pick(x.measured?.instance_type, x.declared?.instance_type, "");
export const machineArch = (x: WsMachine) => pick(x.measured?.arch, x.declared?.arch, "");
export const machineVCPU = (x: WsMachine) => pick(x.measured?.vcpu, x.declared?.vcpu, 0);

/** The memory the WORKSPACE may spend, which is what a build runs into — not the box's
 *  RAM. The two stopped being the same number once a reserve was held back for the box's
 *  own daemons, and showing the box alone promises memory the cgroup refuses. */
export const machineMemLimit = (x: WsMachine) =>
  pick(x.measured?.mem_max, x.declared?.mem_cap_mib ? x.declared.mem_cap_mib * MIB : 0, 0);

/** The box's own RAM, and only where the box is the member's: on a shared host it is other
 *  people's memory. The CP already withholds the measurement; this keeps the declared side
 *  to the same rule. */
export const machineMemBox = (x: WsMachine) =>
  pick(x.measured?.mem_total, x.declared?.dedicated && x.declared.slot_mem_mib ? x.declared.slot_mem_mib * MIB : 0, 0);

export const machineDisk = (x: WsMachine) =>
  pick(x.measured?.disk_total, x.declared?.home_gib ? x.declared.home_gib * GIB : 0, 0);

/** The box the NEXT start will use, when that is not the one running now — "" when they
 *  agree or when only one of them is known.
 *
 *  A size or class change applies at the next start, so a running workspace keeps its old
 *  box; this is the case someone asking "why is this still slow" is looking at. When the
 *  instance types cannot be compared (no SMBIOS reading), the memory limit answers the
 *  same question: a different rung always means a different cap. */
export function machineNextStart(x: WsMachine): { box: string; changed: boolean } {
  const m = x.measured;
  const d = x.declared;
  if (!m || !d) return { box: "", changed: false };
  if (m.instance_type && d.instance_type) {
    const changed = m.instance_type !== d.instance_type;
    return { box: changed ? d.instance_type : "", changed };
  }
  const cap = d.mem_cap_mib ? d.mem_cap_mib * MIB : 0;
  // A megabyte of slack: the declared cap is a whole number of MiB and the cgroup value is
  // the same number, but nothing guarantees the arithmetic stays exact.
  return { box: "", changed: m.mem_max != null && cap > 0 && Math.abs(m.mem_max - cap) > MIB };
}

/** The one-line form for the WS bar: "m8g.large · arm64 · 2 vCPU", dropping whatever is
 *  unknown. Empty when nothing at all is known, so the caller renders no line rather than
 *  an empty one. */
export function machineSummary(x: WsMachine): string {
  const vcpu = machineVCPU(x).value;
  return [machineInstance(x).value, machineArch(x).value, vcpu ? `${vcpu} vCPU` : ""].filter(Boolean).join(" · ");
}
