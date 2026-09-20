// engineFamilyVram — what a family was MEASURED to occupy while it was generating, offered to the
// operator at the two screens where `vram_mib` is decided (ADR 0094 decision 8).
//
// 🔴 This table is never written to a row by the machine. `vram_mib` is defined as the operator's
// own measurement (engine_class.go's engineVramDeclared), and a family default flowed in by the
// ingest would change what the column means — from "somebody measured this" to "somebody wrote
// this" — which is the assumption ADR 0074 rests on. So the screens below show the number, name
// the conditions it was taken under, and fill the field only when a person presses the button.
//
// Why it is worth a press: without a declaration the Control Plane falls back to the sum of the
// row's files (engineModelVramNeed's floor), and for a split family that sum is far above what the
// run actually uses — the text encoder is evicted after it encodes. Measured on the development
// deployment 2026-09-20: qwen-image-edit-2509's files total 28,676 MiB, the run used 20,862, and
// the difference decides which RUNG of the class ladder the deployment buys (22,000 MiB L4 against
// a 45,458 MiB L40S). The column is a cost control as much as a safety one.
//
// ⚠️ A family with no entry here is the normal case, not a gap — nobody has measured it on this
// deployment. Nothing is drawn for those, because an estimate presented as a measurement is the
// failure this whole table exists to avoid.

/** One family's measurement, with the conditions it holds for. The conditions are not decoration:
 *  VRAM scales with the picture and the batch, so the same family at a larger size is a different
 *  number this deployment has not taken. */
export interface FamilyVramMeasurement {
  /** MiB in use at the peak of a run, read from the engine's own `/system_stats`
   *  (`vram_total` − `vram_free`). */
  mib: number;
  /** The picture size it was measured at, `WxH`. */
  size: string;
  /** Pictures per request. */
  batch: number;
  /** Reference pictures fed to the graph — 0 for a family that generates from a prompt alone. */
  inputs: number;
}

export const FAMILY_VRAM_MEASURED: Record<string, FamilyVramMeasurement> = {
  // ADR 0094 実測: `vram_total` 23,659,151,360 B and `vram_free` 1,783,934,774 B on an L4 24GB,
  // i.e. 20,862 MiB in use, at 1024² with one reference picture.
  "qwen-image-edit-2509": { mib: 20862, size: "1024x1024", batch: 1, inputs: 1 },
  // 🔴 qwen-image-edit-2511 is deliberately absent until somebody reads /system_stats during a
  // 2511 run. It loads a diffusion model 0.1 GB larger than 2509's through the same graph, so the
  // number is *probably* within a few hundred MiB — and "probably" is exactly what this column is
  // not allowed to contain.
};

/** The measurement for a row's `base_model`, or null when nobody took one. */
export function familyVramMeasurement(family: string | undefined | null): FamilyVramMeasurement | null {
  if (!family) return null;
  return FAMILY_VRAM_MEASURED[family.trim().toLowerCase()] ?? null;
}
