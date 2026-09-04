// features/usage/colors — assigns series to colour slots (docs/log/46 P4).
//
// Three rules; keeping them is what keeps the charts readable under colour-vision deficiency.
//
//  1. A colour belongs to the entity, not to its rank. Filtering out series must not change the
//     colour of the ones that survive (a reader who learned "Acme is blue" is not betrayed).
//     The enumerated axes (feature / kind / trigger / origin / measured / model_src / verb) use
//     fixed tables and do not depend on the data at all.
//  2. Stack in slot order. In a stacked bar the only pairs that ever touch are neighbouring
//     slots, so validating the palette's adjacent pairs is enough to make real adjacency safe
//     (the dataviz skill's adjacency check assumes exactly this; see the --viz-* comments in
//     tokens.css).
//  3. Never invent a ninth colour. Anything past the 8 slots folds into the grey "other". A
//     generated ninth colour is indistinguishable from an existing one under CVD and breaks the
//     validation.
//
// model / origin_conv can grow without bound, so no fixed table is possible: the preferred slot
// comes from a hash of the key string (the same model name always gets the same colour, i.e. the
// colour belongs to the entity), and only collisions fall through to a free slot. Anything past
// the top 8 folds into "other" — only the choice of what to fold depends on magnitude, which is
// the ordinary top-N + other fold and is compatible with pinning colours to entities.

/** One series painted by the usage view. Slot 0 = "other" (grey; never assigned to an entity). */
export interface SeriesPaint {
  key: string;
  slot: number;
  color: string;
  /** Was this series folded into "other"? The legend and table still name the folded entries. */
  folded: boolean;
}

/** Categorical ceiling, matching dataviz's "8 slots, never a ninth colour". */
export const MAX_SLOTS = 8;

export const OTHER_KEY = "__other__";

/**
 * Fixed slot table for feature (the enumeration in docs/log/46 §1-a).
 *
 * There are 13 enum values (the 12 frozen by ADR0029 plus `suggest.edit` from docs/log/44
 * Phase 4) and only 8 slots. Rule 3 forbids a ninth colour, so the 5 that overflow always land
 * in the grey "other" — a choice, not an oversight: the 8 that keep a colour are the ones that
 * can carry an order of magnitude on their own.
 *
 * - `assistant.ask` (one-shot advisory, non-persistent) is dwarfed by `assistant.chat`.
 * - `title.chat` / `suggest.chat` fire a few times per conversation, an order of magnitude less
 *   than `title.session` / `suggest.session`, which fire automatically per session.
 * - `branch.suggest` / `suggest.edit` are manual only.
 *
 * The table is only acceptable because the folded part stays visible, which the UI guarantees
 * three ways: "other" always appears in the legend when anything folded (even for a single
 * series); the tooltip lists the real folded keys; clicking "other" filters on all folded keys
 * at once (OR within the axis). The breakdown list and the table view show the real keys
 * unfolded.
 */
const FEATURE_SLOT: Record<string, number> = {
  session: 1,
  "assistant.chat": 2,
  "assistant.autoturn": 3,
  compact: 4,
  "title.session": 5,
  "suggest.session": 6,
  "assistant.bridge": 7,
  // unknown signals a missing tag, i.e. consumption nobody can see, so it always keeps a colour.
  unknown: 8,
};

/** Stacking order for kind (which is also slot order).
 *
 * tokens.css --kind-* is the single source of truth for kind colours and must not be repainted
 * for the usage view (the one-source rule of agent-display-naming). But --kind-* is an identifier
 * palette, never validated as a chart palette: stacked in their natural order the adjacent pairs
 * fail the CVD check (agy blue vs kiro purple is protan ΔE 2.0; copilot and opencode are both
 * near-achromatic).
 *
 * So the colours are untouched and only the order was chosen, out of all permutations. In this
 * order the adjacent pairs measure dark: CVD ΔE 13.0 / normal vision 19.8, light: CVD ΔE 17.3 /
 * normal vision 19.4, passing the adjacency gate in both themes. The saturation and lightness
 * bands (the two greys) still fail, because that is a property of the colours themselves; the
 * legend labels, tooltip and table view cover that by never relying on colour alone. */
export const KIND_STACK_ORDER = ["cursor", "agy", "claude", "copilot", "codex", "kiro", "opencode"];

/** Fixed order for the small enumerated axes (slots are assigned in order, starting at 1). */
const ENUM_ORDER: Record<string, string[]> = {
  trigger: ["user", "auto", "manual", "schedule", "operator", "bridge", "recovery"],
  origin: ["user", "operator", "schedule", "handoff", "unknown"],
  model_src: ["reported", "requested", "default_unknown"],
  measured: ["exact", "partial", "none"],
  verb: ["", "translate", "summarize"],
};

/** Key string to a stable hash (FNV-1a 32-bit): the same name always prefers the same slot. */
export function hashKey(s: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}

/** Slot number to a CSS colour (0 = other). */
export const slotColor = (slot: number): string => (slot <= 0 ? "var(--viz-other)" : `var(--viz-${slot})`);

const kindColor = (kind: string): string =>
  KIND_STACK_ORDER.includes(kind) ? `var(--kind-${kind})` : "var(--viz-other)";

/**
 * paintSeries — assigns colours to the series keys of axis `dim` and returns them in draw order,
 * i.e. slot order.
 *
 * Pass keysByMagnitude largest-first; magnitude is used only to decide what to fold, never to
 * decide a colour. The result is always sorted by ascending slot, with "other" (slot 0) last.
 */
export function paintSeries(dim: string, keysByMagnitude: string[]): SeriesPaint[] {
  const out: SeriesPaint[] = [];
  const push = (key: string, slot: number, color: string) =>
    out.push({ key, slot, color, folded: slot <= 0 });

  if (dim === "kind") {
    for (const key of keysByMagnitude) {
      const i = KIND_STACK_ORDER.indexOf(key);
      push(key, i < 0 ? 0 : i + 1, kindColor(key));
    }
  } else if (dim === "feature") {
    for (const key of keysByMagnitude) {
      const slot = FEATURE_SLOT[key] ?? 0;
      push(key, slot, slotColor(slot));
    }
  } else if (ENUM_ORDER[dim]) {
    const order = ENUM_ORDER[dim];
    for (const key of keysByMagnitude) {
      const i = order.indexOf(key);
      const slot = i < 0 || i >= MAX_SLOTS ? 0 : i + 1;
      push(key, slot, slotColor(slot));
    }
  } else {
    // Unbounded axes (model / origin_conv): hashed preferred slot, collisions go to a free one.
    const taken = new Set<number>();
    keysByMagnitude.forEach((key, idx) => {
      if (idx >= MAX_SLOTS) {
        push(key, 0, slotColor(0));
        return;
      }
      let slot = (hashKey(key) % MAX_SLOTS) + 1;
      if (taken.has(slot)) {
        for (let s = 1; s <= MAX_SLOTS; s++) {
          if (!taken.has(s)) {
            slot = s;
            break;
          }
        }
      }
      taken.add(slot);
      push(key, slot, slotColor(slot));
    });
  }

  // Draw in slot order (rule 2). Within one slot ("other") key name keeps it stable.
  return out.sort((a, b) => {
    const as = a.slot <= 0 ? Infinity : a.slot;
    const bs = b.slot <= 0 ? Infinity : b.slot;
    return as - bs || (a.key < b.key ? -1 : a.key > b.key ? 1 : 0);
  });
}
