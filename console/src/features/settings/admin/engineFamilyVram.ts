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
// run actually uses — the text encoder is evicted once it has encoded. Measured on the development
// deployment 2026-09-20: qwen-image-edit-2509's files total 28,676 MiB, the run used 20,862, and
// the difference decides which RUNG of the class ladder the deployment buys (22,000 MiB L4 against
// a 45,458 MiB L40S). The column is a cost control as much as a safety one.
//
// 🔴 And it is the WEIGHTS FILE that was measured, not the family. `vram_mib` overrides the
// file-sum floor outright (engine_class.go: `m.VramMiB > 0` short-circuits it), so a number
// carried across builds is a number that sizes a GPU for weights it was never taken with: the
// bf16 build of this same family is 40.86 GB and does not fit an L4 at all, and a Q4 conversion
// would over-declare and buy a bigger rung than it needs — the two failures this feature exists
// between. So a measurement names its file, and it is offered only to a row that holds that file.
//
// ⚠️ A family with no entry here is the normal case, not a gap — nobody has measured it on this
// deployment. Nothing is drawn for those, because an estimate presented as a measurement is the
// failure this whole table exists to avoid.

/** One family's measurement, with the conditions it holds for. The conditions are not decoration:
 *  VRAM scales with the picture, the batch and the build, so the same family at a larger size —
 *  or in another quantisation — is a different number this deployment has not taken. */
export interface FamilyVramMeasurement {
  /** MiB in use at the peak of a run, read from the engine's own `/system_stats`
   *  (`vram_total` − `vram_free`). */
  mib: number;
  /** The weights file it was measured with, as the upstream publishes it. The offer is made to a
   *  row holding THIS file and no other build of the family. */
  file: string;
  /** The picture size it was measured at, `WxH`. */
  size: string;
  /** Pictures per request. */
  batch: number;
  /** Reference pictures fed to the graph — 0 for a family that generates from a prompt alone. */
  inputs: number;
}

/** A Map rather than an object literal, and that is not style: a plain object answers
 *  `["constructor"]` with a function, which `?? null` does not catch — and an image row on an
 *  engine that lists no families has a free-text family field, so an arbitrary string reaches
 *  this lookup and would render `.mib` of a Function. `families.ts` uses a Map for the same
 *  reason. */
export const FAMILY_VRAM_MEASURED = new Map<string, FamilyVramMeasurement>([
  // ADR 0094 実測: `vram_total` 23,659,151,360 B and `vram_free` 1,783,934,774 B on an L4 24GB,
  // i.e. 20,862 MiB in use, at 1024² with one reference picture.
  ["qwen-image-edit-2509", {
    mib: 20862, file: "qwen_image_edit_2509_fp8_e4m3fn.safetensors",
    size: "1024x1024", batch: 1, inputs: 1,
  }],
  // 🔴 Both numbers here were read on an **L4 24GB**, and that is load-bearing rather than
  // incidental: the same 2511 run on an L40S 48GB answered 28,358 MiB — the whole 28,774 MiB file
  // set resident, because a card with room to spare evicts nothing. These figures are small only
  // BECAUSE the card is tight enough to evict the text encoder after encoding. A reading taken on
  // a bigger card is not a value of this field (ADR 0094 decision 8's third 🔴; the field has no
  // column for the card yet — open question 7).
  ["qwen-image-edit-2511", {
    mib: 20974, file: "qwen_image_edit_2511_fp8mixed.safetensors",
    size: "1024x1024", batch: 1, inputs: 1,
  }],
]);

/**
 * The measurement for a row's `base_model`, or null.
 *
 * `files` is what the row or the plan actually holds — S3 keys or upstream file names, either
 * way. Null when nobody measured this family, and null when they measured a DIFFERENT build of
 * it: silence is the honest answer to "how much does this one need", and the field stays the
 * operator's to type.
 */
export function familyVramMeasurement(
  family: string | undefined | null,
  files: (string | undefined)[],
): FamilyVramMeasurement | null {
  if (!family) return null;
  const measured = FAMILY_VRAM_MEASURED.get(family.trim().toLowerCase());
  if (!measured) return null;
  const holdsIt = files.some((name) => {
    const clean = (name || "").trim();
    return clean === measured.file || clean.endsWith(`/${measured.file}`);
  });
  return holdsIt ? measured : null;
}
